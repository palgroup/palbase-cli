package backend

import (
	"context"
	"errors"
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
	const url = "https://prj.example.test"
	want := StackNames{
		Secrets: []string{"STRIPE_KEY"},
		Flags:   []string{"new_ui"},
		Buckets: []StackBucket{{Name: "avatars", Variants: []string{"thumb"}}},
		Roles:   []string{"admin", "member"},
	}

	// Taze klon: kayıt yok, ve bu adıyla söylenir.
	_, err := readCachedStackNames(checkout, url)
	require.ErrorIs(t, err, errNoCachedStackNames)

	require.NoError(t, writeCachedStackNames(checkout, url, want))
	got, err := readCachedStackNames(checkout, url)
	require.NoError(t, err)
	require.Equal(t, want, got, "roller dahil dört küme de geri okunmalı")

	// Yazılan dosya StackNamesPath'in dediği yerde ve yalnız sahibine açık.
	path, err := StackNamesPath(checkout)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// NEGATİF KONTROL: başka bir yığına bağlanan checkout eskisinin adlarını
	// MİRAS ALMAZ.
	_, err = readCachedStackNames(checkout, "https://other.example.test")
	require.ErrorIs(t, err, errNoCachedStackNames)
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
	cached, err := readCachedStackNames(checkout, target.URL)
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
	cached, err = readCachedStackNames(checkout, target.URL)
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
		require.NotNil(t, names.Roles, "404 bir cevaptır: rol yok, boş küme olarak render edilir")
		require.Empty(t, names.Roles)
	})

	t.Run("a failing roles door fails the whole read", func(t *testing.T) {
		target := stack(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
		_, err := readStackNames(context.Background(), target)
		require.Error(t, err, "kısmi bir cevap rol tiplerini hiçliğe daraltırdı")
	})
}
