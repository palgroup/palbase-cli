package backend

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// YIĞIN ADLARI MAKİNE-YEREL ÖNBELLEKTE DURUR (FR-003, FR-004).
//
// `palbase build` ağsız çalışır; üretilen dosyanın stack bloğu ise yığından
// okunan adlardan render edilir. İkisi ancak son başarılı okumanın BU makinede,
// BU checkout için saklanmasıyla bir arada durur — checkout'ta değil, çünkü
// commit'lenen bir kopya yığından kayan ikinci bir ad listesi olurdu.

func TestStackNamesPathLivesBesideTheOtherMachineState(t *testing.T) {
	useTempMachineHome(t)
	checkout := t.TempDir()

	names, err := StackNamesPath(checkout)
	require.NoError(t, err)
	local, err := LocalStatePath(checkout)
	require.NoError(t, err)
	plan, err := PlanStatePath(checkout)
	require.NoError(t, err)

	// (a) AYNI KÖK: bu checkout'un hash dizini, local.json'ın yanı.
	require.Equal(t, filepath.Dir(local), filepath.Dir(names))
	// (b) KENDİ DOSYASI: ne start'ın kaydını ne plan'ın ölçümünü ezer.
	require.NotEqual(t, local, names)
	require.NotEqual(t, plan, names)

	// (c) BAŞKA CHECKOUT, BAŞKA DİZİN — anahtar checkout'un mutlak yolu.
	other, err := StackNamesPath(t.TempDir())
	require.NoError(t, err)
	require.NotEqual(t, filepath.Dir(names), filepath.Dir(other))

	// (d) YOLU SORMAK DİZİN YARATMAZ (D-010).
	_, statErr := os.Stat(filepath.Dir(names))
	require.ErrorIs(t, statErr, fs.ErrNotExist)
}

func TestStackNamesCacheReadsBackWhatWasWritten(t *testing.T) {
	useTempMachineHome(t)
	checkout := t.TempDir()
	target := Target{URL: "https://prj.example.test", Project: "prj_a"}
	want := StackNames{
		Secrets: []string{"STRIPE_KEY"},
		Flags:   []string{"new_ui"},
		Buckets: []StackBucket{{Name: "avatars", Variants: []string{"thumb"}}},
		Roles:   []string{"admin", "member"},
	}

	// Taze klon: kayıt yok, ve bu adıyla söylenir.
	_, err := readCachedStackNames(checkout, target)
	require.ErrorIs(t, err, errNoCachedStackNames)

	require.NoError(t, writeCachedStackNames(checkout, target, want))
	got, err := readCachedStackNames(checkout, target)
	require.NoError(t, err)
	require.Equal(t, want, got, "roller dahil dört küme de geri okunmalı")

	// Yazılan dosya StackNamesPath'in dediği yerde ve yalnız sahibine açık.
	path, err := StackNamesPath(checkout)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// NEGATİF KONTROL: başka bir yığına bağlanan checkout eskisinin adlarını
	// MİRAS ALMAZ — ne başka bir adreste…
	_, err = readCachedStackNames(checkout, Target{URL: "https://other.example.test", Project: "prj_a"})
	require.ErrorIs(t, err, errNoCachedStackNames)
	// …ne de AYNI adreste başka bir projede (review-T009): bir bulut adresi ya
	// da aynı yerel port, bir link'ten sonra başka bir yığını gösterebilir.
	_, err = readCachedStackNames(checkout, Target{URL: target.URL, Project: "prj_b"})
	require.ErrorIs(t, err, errNoCachedStackNames)
}

// BOZUK BİR KAYIT "YOK" DEĞİLDİR (review-T009 MINOR). Sebep adıyla çağırana
// ulaşmazsa dosya sonsuza kadar bozuk kalır ve kimse nedenini öğrenmez.
func TestStackNamesCorruptRecordIsNotNone(t *testing.T) {
	useTempMachineHome(t)
	checkout := t.TempDir()
	target := Target{URL: "https://prj.example.test"}
	path, err := StackNamesPath(checkout)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	_, err = readCachedStackNames(checkout, target)
	require.Error(t, err)
	require.NotErrorIs(t, err, errNoCachedStackNames, "a corrupt record was read as a fresh clone")
	require.ErrorContains(t, err, "did not parse")
}

// YAZILAMAYAN ÖNBELLEK BUILD'İ DÜŞÜRMEZ AMA SÖYLENİR (review-T009 IMPORTANT-2).
// `CacheErr` bu hâli ayırt etmek için var; hiçbir test onu gerçek bir yazma
// hatasıyla ölçmüyordu, yani alan sessizce anlamsızlaşabilirdi.
func TestStackNamesCacheWriteFailureIsReported(t *testing.T) {
	notADirectory := filepath.Join(t.TempDir(), "home-is-a-file")
	require.NoError(t, os.WriteFile(notADirectory, []byte("x"), 0o600))
	prev := machineStateHome
	machineStateHome = func() (string, error) { return notADirectory, nil }
	t.Cleanup(func() { machineStateHome = prev })

	fresh := StackNames{Secrets: []string{"STRIPE_KEY"}, Flags: []string{}, Buckets: []StackBucket{}, Roles: []string{}}
	withStackNamesReader(t, func(context.Context, Target) (StackNames, error) { return fresh, nil })

	got := stackNamesForCheckout(context.Background(), t.TempDir(), Target{URL: "https://prj.example.test"})
	require.Equal(t, namesFromStack, got.Source, "a cache that cannot be written must not change where the names came from")
	require.Equal(t, fresh, got.Names)
	require.Error(t, got.CacheErr, "the failed cache write was swallowed")
}

// KİMLİK TEK KEZ ÇÖZÜLÜR (review-T009 IMPORTANT-1). Bir bulut projesinde her
// çözüm kontrol düzlemine gerçek bir istek; ikinci çözümün geçici bir hatası
// zaten okunmuş adları "yığın erişilemez"e çeviriyordu.
func TestStackNamesReadResolvesTheCredentialOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/v1/management/secrets":
			_, _ = w.Write([]byte(`{"secrets":[]}`))
		case "/v1/management/flags":
			_, _ = w.Write([]byte(`{"flags":[]}`))
		case "/v1/management/storage/buckets":
			_, _ = w.Write([]byte(`{"buckets":[]}`))
		case "/admin/roles":
			_, _ = w.Write([]byte(`{"roles":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	calls := 0
	prev := credentialFn
	credentialFn = func(url string) (Credentials, CredentialSource, error) {
		calls++
		return Credentials{Value: "pb_secret_test", Kind: KindKey}, SourceEnv, nil
	}
	t.Cleanup(func() { credentialFn = prev })

	_, err := readStackNames(context.Background(), Target{URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, 1, calls, "the credential was resolved %d times for one read", calls)
}

// LINK ÖNBELLEĞİ UNUTUR (review-T009 IMPORTANT-3). Aynı yerel port bir
// link'ten sonra başka bir yığını gösterebilir ve URL onu ayırt edemez; yeniden
// bağlanan bir checkout "yığının dediğinden" başlar.
func TestLinkForgetsTheStackNamesCache(t *testing.T) {
	inScratchCheckout(t)
	useTempMachineHome(t)
	checkout, err := os.Getwd()
	require.NoError(t, err)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	require.NoError(t, writeCachedStackNames(checkout, Target{URL: srv.URL}, StackNames{Secrets: []string{"FROM_THE_OLD_STACK"}}))
	path, err := StackNamesPath(checkout)
	require.NoError(t, err)
	require.FileExists(t, path)

	require.NoError(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, io.Discard))

	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, fs.ErrNotExist, "a relinked checkout kept the names of the stack it was linked to before")
}

// withStackNamesReader swaps the stack read for one test, through the
// package's own seam.
func withStackNamesReader(t *testing.T, read func(context.Context, Target) (StackNames, error)) {
	t.Helper()
	prev := readStackNamesFn
	readStackNamesFn = read
	t.Cleanup(func() { readStackNamesFn = prev })
}

func TestStackNamesFallBackToThisMachinesCache(t *testing.T) {
	useTempMachineHome(t)
	checkout := t.TempDir()
	target := Target{URL: "https://prj.example.test"}
	fresh := StackNames{
		Secrets: []string{"STRIPE_KEY"},
		Flags:   []string{},
		Buckets: []StackBucket{},
		Roles:   []string{"admin"},
	}

	// (1) ERİŞİLEBİLİR YIĞIN: adlar yığından gelir VE önbelleğe yazılır (FR-004).
	withStackNamesReader(t, func(context.Context, Target) (StackNames, error) { return fresh, nil })
	got := stackNamesForCheckout(context.Background(), checkout, target)
	require.Equal(t, namesFromStack, got.Source)
	require.NoError(t, got.CacheErr)
	require.NoError(t, got.Why)
	require.Equal(t, fresh, got.Names)
	cached, err := readCachedStackNames(checkout, target)
	require.NoError(t, err)
	require.Equal(t, fresh, cached)

	// (2) ERİŞİLEMEZ YIĞIN: adlar önbellekten gelir (FR-003) ve sebep kaybolmaz.
	unreachable := errors.New("dial tcp: connect: network is unreachable")
	withStackNamesReader(t, func(context.Context, Target) (StackNames, error) { return StackNames{}, unreachable })
	got = stackNamesForCheckout(context.Background(), checkout, target)
	require.Equal(t, namesFromCache, got.Source)
	require.Equal(t, fresh, got.Names)
	require.ErrorIs(t, got.Why, unreachable)

	// (3) DÜŞEN OKUMA ÖNBELLEĞİ EZMEZ.
	cached, err = readCachedStackNames(checkout, target)
	require.NoError(t, err)
	require.Equal(t, fresh, cached)
}

func TestStackNamesUnavailableWithoutACache(t *testing.T) {
	useTempMachineHome(t)
	unreachable := errors.New("no route to host")
	withStackNamesReader(t, func(context.Context, Target) (StackNames, error) { return StackNames{}, unreachable })

	got := stackNamesForCheckout(context.Background(), t.TempDir(), Target{URL: "https://prj.example.test"})
	require.Equal(t, namesUnavailable, got.Source)
	// İki sebep de adıyla: yığın neden cevap vermedi, önbellek neden yok.
	require.ErrorIs(t, got.Why, unreachable)
	require.ErrorIs(t, got.Why, errNoCachedStackNames)
	require.Equal(t, StackNames{}, got.Names, "uydurulmuş ad yok — lander diskteki bloğu korur (FR-003a)")
}

func TestStackNamesReadAsksTheStackForItsRoles(t *testing.T) {
	stack := func(t *testing.T, roles http.HandlerFunc) Target {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			switch r.URL.Path {
			case "/v1/management/secrets":
				_, _ = w.Write([]byte(`{"secrets":[{"name":"STRIPE_KEY"}]}`))
			case "/v1/management/flags":
				_, _ = w.Write([]byte(`{"flags":[{"key":"new_ui"}]}`))
			case "/v1/management/storage/buckets":
				_, _ = w.Write([]byte(`{"buckets":[{"name":"avatars","variants":[{"name":"thumb"}]}]}`))
			case "/admin/roles":
				roles(w, r)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		return Target{URL: srv.URL}
	}
	// SÜREÇ ORTAMI, DİSK DEĞİL: `t.Setenv` test bitince geri alınır ve
	// `~/.palbase/credentials.json`a hiçbir şey yazılmaz.
	t.Setenv(AccessTokenEnv, "pb_secret_test")

	t.Run("roles travel with the other three sets", func(t *testing.T) {
		target := stack(t, servedRoles(`{"roles":[
			{"name":"support","isDefault":false,"permissions":["tickets.read"]},
			{"name":"admin","isDefault":false,"permissions":["*"]}]}`))
		names, err := readStackNames(context.Background(), target)
		require.NoError(t, err)
		require.Equal(t, []string{"STRIPE_KEY"}, names.Secrets)
		require.Equal(t, []string{"new_ui"}, names.Flags)
		require.Equal(t, []StackBucket{{Name: "avatars", Variants: []string{"thumb"}}}, names.Buckets)
		// Adıyla sıralı: aynı yığın aynı baytları üretir.
		require.Equal(t, []string{"admin", "support"}, names.Roles)
	})

	t.Run("a stack older than roles answers none", func(t *testing.T) {
		target := stack(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
		names, err := readStackNames(context.Background(), target)
		require.NoError(t, err)
		// readStackNames'in KENDİ dönüşümü ölçülüyor: 404 hata değil, ve okuma
		// sonucu nil değil boş bir kümedir (fetchStackRoles'un 404 cevabı kendi
		// testlerinde ölçülür — review-T009 MINOR-1).
		require.NotNil(t, names.Roles, "404 bir cevaptır: rol yok, boş küme olarak render edilir")
		require.Empty(t, names.Roles)
	})

	t.Run("a failing roles door fails the whole read", func(t *testing.T) {
		target := stack(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
		_, err := readStackNames(context.Background(), target)
		require.Error(t, err, "kısmi bir cevap rol tiplerini hiçliğe daraltırdı")
	})
}
