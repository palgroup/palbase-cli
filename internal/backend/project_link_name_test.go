package backend

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/config"
)

// nameREST answers a project listing from a fixture.
type nameREST struct {
	rows []map[string]any
	err  error
	path string
}

func (n *nameREST) Do(_ context.Context, _ string, path string, _ any, out any) error {
	n.path = path
	if n.err != nil {
		return n.err
	}
	blob, _ := json.Marshal(n.rows)
	return json.Unmarshal(blob, out)
}

func resolvers(rest REST) Resolvers {
	return Resolvers{REST: func() REST { return rest }}
}

// AD ÇÖZÜMÜ ARTIK ÜRÜNE — ve eski yolun kusuru tam buradaydı.
//
// `refByProjectName` `/v1/cloud/projects`e soruyordu: o uç ORTAM başına satır
// döndürüyor ve her satır ÜRÜNÜN adını taşıyor. Yani iki ortamlı bir projede
// iki satır aynı adı taşıyor, fonksiyon bunu "belirsiz" sayıp BOŞ dönüyor, ve
// çağıran ref şekli kontrolüne düşüyordu — "todoapp" 7 küçük harf olduğu için
// `https://todoapp.<host>` kuruluyor ve "does not look like a Palbase stack"
// deniyordu. Ad çözümü, ortamların ÜRÜN altında geldiği yüzeye taşındı; iki
// ortam artık tek cevabın içinde ve belirsizlik YOK.
func TestNameResolvesToTheProductWithItsEnvironments(t *testing.T) {
	rest := &nameREST{rows: []map[string]any{
		{"id": "prd_a", "name": "todoapp", "environments": []map[string]any{
			{"ref": "8qitbtucm", "name": "main", "status": "Running"},
			{"ref": "mu0028", "name": "staging", "status": "Running"},
		}},
		{"id": "prd_b", "name": "centauri", "environments": []map[string]any{
			{"ref": "8bbwb2pbm", "name": "main", "status": "Running"},
		}},
	}}
	product, envs, err := productByName(context.Background(), resolvers(rest), "todoapp")
	require.NoError(t, err)
	require.Equal(t, "prd_a", product.ID)
	require.Len(t, envs, 2, "iki ortam tek cevabın içinde gelmedi")
	require.Equal(t, "/api/v2/projects", rest.path)

	// Büyük/küçük harf duyarsız: listede gördüğünü yazan insan onu aynen yazmaz.
	product, _, err = productByName(context.Background(), resolvers(rest), "  TodoApp ")
	require.NoError(t, err)
	require.Equal(t, "prd_a", product.ID)
}

// BULAMAMAK ARTIK BİR HATADIR, ve bu bir iyileşme.
//
// Eskiden boş dönüyordu ve çağıran ref yoluna düşüyordu — yazım hatası "o
// adrese ulaşamadım" diye raporlanıyordu. Şimdi ne olmadığı söyleniyor ve
// kullanıcının projeleri listeleniyor.
func TestAMissNamesWhatYouActuallyHave(t *testing.T) {
	rest := &nameREST{rows: []map[string]any{
		{"id": "prd_b", "name": "baska", "environments": []map[string]any{{"ref": "r1", "name": "main"}}},
	}}
	_, _, err := productByName(context.Background(), resolvers(rest), "todoapp")
	require.Error(t, err)
	require.Contains(t, err.Error(), "todoapp")
	require.Contains(t, err.Error(), "baska", "red kullanıcının projelerini listelemiyor")
}

// OTURUM YOKSA SÖYLENİR — self-host akışı adres ister, ad değil.
func TestNoSessionIsNamedRatherThanGuessed(t *testing.T) {
	_, _, err := productByName(context.Background(), Resolvers{}, "todoapp")
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase login")

	_, _, err = productByName(context.Background(), resolvers(&nameREST{err: errors.New("401")}), "todoapp")
	require.Error(t, err, "okunamayan bir liste sessizce yutuldu")
}

// AYNI ADDAN İKİ ÜRÜN VARSA SEÇMEYİZ.
//
// Birini seçmek, YANLIŞ projeye bağlanmak olabilir — ve bağlandıktan sonra
// push oraya gider. Ama artık bu gerçekten İKİ ÜRÜN demek; iki ORTAM değil.
func TestTwoProductsWithOneNameRefuse(t *testing.T) {
	rest := &nameREST{rows: []map[string]any{
		{"id": "prd_1", "name": "shop", "environments": []map[string]any{{"ref": "r1", "name": "main"}}},
		{"id": "prd_2", "name": "Shop", "environments": []map[string]any{{"ref": "r2", "name": "main"}}},
	}}
	_, _, err := productByName(context.Background(), resolvers(rest), "shop")
	require.Error(t, err)
	require.Contains(t, err.Error(), "id")
}

// ORTAMSIZ BİR ÜRÜN ÇÖZÜLÜR ama ondan okunamaz — ve söylenen şey budur.
func TestAProductWithNoEnvironmentsSaysSo(t *testing.T) {
	rest := &nameREST{rows: []map[string]any{
		{"id": "prd_a", "name": "todoapp", "environments": []map[string]any{}},
	}}
	product, envs, err := productByName(context.Background(), resolvers(rest), "todoapp")
	require.NoError(t, err)
	require.Empty(t, envs)

	_, refErr := linkEnvironmentRef(product, envs, "")
	require.Error(t, refErr)
	require.Contains(t, refErr.Error(), "palbase env create")
}

var _ = http.MethodGet

// AD ÇÖZÜMÜ KOMUTA BAĞLI OLMALI — yalnız var olması yetmez.
//
// Yardımcıyı doğrudan süren testler, komuttaki çağrı kaldırıldığında YEŞİL
// kalıyordu: çözüm "beyan edilmiş ama bağlanmamış" hâle gelir ve `link <ad>`
// yine var olmayan bir konağa giderdi.
//
// Komut ağdan bir cevap alamayacağı için düşecek; ölçülen şey HANGİ ADRESE
// gittiği — hata mesajı onu taşıyor.
func TestTheCommandActuallyResolvesTheName(t *testing.T) {
	// THE LISTING IS BY PRODUCT NOW, and that is the whole point of the change
	// this test guards: two environments under one project used to come back as
	// two rows sharing a name, which the old resolver read as a collision and
	// refused — so `palbase link todoapp` fell through to the ref-shape check
	// and built `https://todoapp.palbase.studio`.
	rest := &nameREST{rows: []map[string]any{{
		"id": "prd_a", "name": "todoapp",
		"environments": []map[string]any{
			{"ref": "8qitbtucm", "name": "main", "status": "Running"},
			{"ref": "mu0028", "name": "staging", "status": "Running"},
		},
	}}}
	r := Resolvers{
		REST:      func() REST { return rest },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
	// A REAL WEB CHECKOUT, in a scratch directory. `--platform web` is here to
	// keep detection out of a test about NAME RESOLUTION — and the web wiring's
	// prerequisite is now checked before the network, so the flag needs a
	// directory that can carry it. Without the chdir this ran in the package's
	// own source tree, which is not a web app and never was.
	t.Chdir(t.TempDir())
	if err := os.WriteFile("package.json", []byte(`{"name":"app"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newLinkCmd(r)
	// `--from-env` names WHICH environment this link reads from; with two of
	// them and nothing named the command refuses, which is the rule every verb
	// follows. Naming one keeps this test about NAME RESOLUTION.
	cmd.SetArgs([]string{"todoapp", "--from-env", "main", "--platform", "web"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("ağ olmadan link başarılı döndü — test bir şey ölçmüyor")
	}
	msg := err.Error()
	if !strings.Contains(msg, "8qitbtucm") {
		t.Fatalf("komut ADI çözmedi, olduğu gibi kullandı: %s", msg)
	}
	if strings.Contains(msg, "todoapp.palbase.studio") {
		t.Fatalf("ad konak sanıldı: %s", msg)
	}
}
