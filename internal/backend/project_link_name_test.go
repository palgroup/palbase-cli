package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/config"
)

// `palbase link <ad>` ADI ÇÖZER — yardım metni bunu vaat ediyor.
//
// Bir ad çoğu zaman ref ŞEKLİNE de uyar ("ioslinkprobe": 4-24 küçük harf), ve
// yalnız şekle bakan kod adı sessizce ref sanıp var olmayan bir konağa
// gidiyordu: "does not look like a Palbase stack" (ölçüldü 25.08.2026).
// Belgelenmiş ama var olmayan bir davranıştı.
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

func TestLinkResolvesAProjectByName(t *testing.T) {
	rest := &nameREST{rows: []map[string]any{
		{"ref": "8qitbtucm", "name": "todoapp"},
		{"ref": "8bbwb2pbm", "name": "centauri"},
	}}
	if got := refByProjectName(context.Background(), resolvers(rest), "todoapp"); got != "8qitbtucm" {
		t.Fatalf("ad çözülmedi: %q", got)
	}
	if rest.path != "/v1/cloud/projects" {
		t.Fatalf("yanlış uç: %s", rest.path)
	}
	// Büyük/küçük harf duyarsız: listede gördüğünü yazan insan onu aynen yazmaz.
	if got := refByProjectName(context.Background(), resolvers(rest), "  TodoApp "); got != "8qitbtucm" {
		t.Fatalf("harf duyarlılığı: %q", got)
	}
}

// BULAMAMAK HATA DEĞİLDİR — çağıran ref yoluna düşer.
//
// Self-host bir checkout'ta ad çözecek bir defter yoktur ve orada ref/adres tek
// doğru cevaptır; oturum yokluğunu hata saymak o akışı kırardı.
func TestAMissForNameIsNotAnError(t *testing.T) {
	rest := &nameREST{rows: []map[string]any{{"ref": "r1", "name": "baska"}}}
	if got := refByProjectName(context.Background(), resolvers(rest), "todoapp"); got != "" {
		t.Fatalf("olmayan ad çözüldü: %q", got)
	}
	// Oturum yok / uç okunamıyor.
	if got := refByProjectName(context.Background(), resolvers(&nameREST{err: errors.New("401")}), "todoapp"); got != "" {
		t.Fatalf("hata yutulmadı: %q", got)
	}
	// REST hiç yok (self-host).
	if got := refByProjectName(context.Background(), Resolvers{}, "todoapp"); got != "" {
		t.Fatalf("REST'siz çözüm: %q", got)
	}
}

// AYNI ADDAN İKİ TANE VARSA SEÇMEYİZ.
//
// Birini seçmek, YANLIŞ projeye bağlanmak olabilir — ve bağlandıktan sonra
// push oraya gider.
func TestAnAmbiguousNameResolvesToNothing(t *testing.T) {
	rest := &nameREST{rows: []map[string]any{
		{"ref": "r1", "name": "shop"},
		{"ref": "r2", "name": "Shop"},
	}}
	if got := refByProjectName(context.Background(), resolvers(rest), "shop"); got != "" {
		t.Fatalf("belirsiz ad çözüldü: %q", got)
	}
}

// ADSIZ SATIR ÇÖKME ÜRETMEZ: eski projelerin adı NULL olabilir.
func TestANullNameIsSkipped(t *testing.T) {
	rest := &nameREST{rows: []map[string]any{
		{"ref": "r1", "name": nil},
		{"ref": "r2", "name": "todoapp"},
	}}
	if got := refByProjectName(context.Background(), resolvers(rest), "todoapp"); got != "r2" {
		t.Fatalf("adsız satır akışı bozdu: %q", got)
	}
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
	rest := &nameREST{rows: []map[string]any{{"ref": "8qitbtucm", "name": "todoapp"}}}
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
	cmd.SetArgs([]string{"todoapp", "--platform", "web"})
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
