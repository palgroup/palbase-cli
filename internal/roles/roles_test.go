package roles

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// linkedProject bir checkout'u verilen adrese bağlar ve service_role kimliğini
// yerleştirir — komutların `resolveProject` üzerinden bulacağı hâl.
func linkedProject(t *testing.T, url string) {
	t.Helper()
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	if err := backend.WriteLocalTarget(backend.Target{URL: url, Local: true}); err != nil {
		t.Fatal(err)
	}
	if err := backend.StoreCredential(url, backend.Credentials{
		Value: "service-role-key", Kind: backend.KindKey,
	}); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd()
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// `create` rolü PROJENİN KENDİ kapısına yazar ve bayrakları gövdeye taşır.
//
// Yol T009'un açtığıyla aynı (`<stack>/admin/roles`): iki verbin aynı uca iki
// farklı yoldan gitmesi, gün gelip birinin çalışıp öbürünün çalışmaması demek.
func TestCreateWritesTheDefinitionAtTheProjectsOwnDoor(t *testing.T) {
	var seen struct {
		method, path string
		body         map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&seen.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles":[{"name":"moderator","isDefault":false,"permissions":["posts.delete_any"],"userCount":0}]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	out, err := run(t, "create", "moderator", "--permissions", "posts.delete_any,comments.delete")
	if err != nil {
		t.Fatalf("create failed: %v (%s)", err, out)
	}
	if seen.method != http.MethodPut || seen.path != "/admin/roles/moderator" {
		t.Fatalf("called %s %s; want PUT /admin/roles/moderator", seen.method, seen.path)
	}
	perms, _ := seen.body["permissions"].([]any)
	if len(perms) != 2 {
		t.Fatalf("body carried %v permissions; want 2", perms)
	}
}

// `--default` gövdeye iner. Varsayılan rol register akışının tamamını belirliyor;
// bayrağın sessizce düşmesi, kaydolan herkesi rolsüz bırakırdı.
func TestCreateCarriesTheDefaultFlag(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"roles":[]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	if _, err := run(t, "create", "member", "--default"); err != nil {
		t.Fatalf("create --default failed: %v", err)
	}
	if body["isDefault"] != true {
		t.Fatalf("isDefault=%v; want true", body["isDefault"])
	}
}

// `list --json` YALNIZ JSON basar — başka hiçbir metin yok, çünkü çıktının
// tüketicisi bir betik.
func TestListJSONPrintsNothingButJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"roles":[{"name":"member","isDefault":true,"permissions":["posts.read"],"userCount":3}]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	out, err := run(t, "list", "--json")
	if err != nil {
		t.Fatalf("list --json failed: %v (%s)", err, out)
	}
	var parsed any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("output was not pure JSON: %v\n---\n%s", err, out)
	}
}

// İnsan için olan liste rolü, varsayılan işaretini, izinleri ve KULLANICI
// SAYISINI gösterir — "bu rolü silersem kimi etkilerim" sorusunun cevabı.
func TestListShowsDefinitionsAndCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"roles":[{"name":"member","isDefault":true,"permissions":["posts.read"],"userCount":3}]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	out, err := run(t, "list")
	if err != nil {
		t.Fatalf("list failed: %v (%s)", err, out)
	}
	for _, want := range []string{"member", "posts.read", "3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}
}

// Silme reddedildiğinde sunucunun SEBEBİ kullanıcıya ulaşır — kaç atamanın
// gideceği dâhil. Yutulmuş bir 409, kullanıcıyı komutun çalıştığına inandırır.
func TestDeleteSurfacesTheServersRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"role_has_assignments","error_description":"7 user(s) hold \"moderator\"; deleting it revokes them — repeat with confirm=true"}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	out, err := run(t, "delete", "moderator")
	if err == nil {
		t.Fatal("delete succeeded although the server refused")
	}
	if !strings.Contains(err.Error()+out, "7") {
		t.Fatalf("the refusal did not carry the count:\n%v\n%s", err, out)
	}
}

// `--yes` onayı UCA taşır; kapı sunucuda, bayrak yalnız onu tetikliyor.
func TestDeleteYesCarriesTheConfirmation(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"roles":[]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	if _, err := run(t, "delete", "moderator", "--yes"); err != nil {
		t.Fatalf("delete --yes failed: %v", err)
	}
	if !strings.Contains(query, "confirm=true") {
		t.Fatalf("query was %q; want confirm=true", query)
	}
}

func TestCmdIsWiredWithItsSubcommands(t *testing.T) {
	names := map[string]bool{}
	for _, c := range Cmd().Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"create", "list", "delete"} {
		if !names[want] {
			t.Fatalf("`palbase roles` has no %q subcommand", want)
		}
	}
	_ = cobra.Command{}
}

// AD, YOLDAN ÇIKAMAZ — ve onay kapısını KENDİ ADIYLA açamaz.
//
// `path := "/admin/roles/" + name` kaçışsızdı. `http.NewRequest` verilen
// dizgiyi bir URL olarak AYRIŞTIRIR, yani addaki bir `?` sorgu dizesini
// başlatır: `palbase roles delete 'x?confirm=true'` ucun gözünde
// `DELETE /admin/roles/x?confirm=true` olur ve FR-003'ün veri-kaybı onayı
// hiç sorulmadan geçilir. Rolü taşıyan herkesin yetkisi silinir, CLI da
// "✓ deleted" yazar.
//
// Aynı kaçışsızlık `..` ile rotayı da değiştirebiliyor: `../users/u/roles/r`
// normalize edilip BAŞKA bir uca iner.
func TestARoleNameCannotForgeTheConfirmationOrLeaveItsRoute(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arg     string
		wantErr bool
	}{
		{"onay sahtelenemez", "x?confirm=true", false},
		{"rota terk edilemez", "../users/usr_1/roles/agent", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path, query string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path, query = r.URL.EscapedPath(), r.URL.RawQuery
				_, _ = w.Write([]byte(`{"roles":[]}`))
			}))
			defer srv.Close()
			linkedProject(t, srv.URL)

			if _, err := run(t, "delete", tc.arg); err != nil && !tc.wantErr {
				t.Fatalf("delete failed: %v", err)
			}
			if query != "" {
				t.Errorf("ad bir sorgu dizesi uydurdu: query=%q — onay kapısı atlandı", query)
			}
			if !strings.HasPrefix(path, "/admin/roles/") {
				t.Errorf("ad rotasını terk etti: path=%q", path)
			}
		})
	}
}

// NEGATİF KONTROL, aynı koşulda: sıradan bir ad hâlâ doğru yere gider ve
// `--yes` onayı taşımaya devam eder. Yoksa yukarıdaki iddialar, her isteği
// bozan bir kaçışla da sağlanırdı.
func TestOrdinaryNamesStillReachTheirRouteEscaped(t *testing.T) {
	var path, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.EscapedPath(), r.URL.RawQuery
		_, _ = w.Write([]byte(`{"roles":[]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	if _, err := run(t, "delete", "moderator", "--yes"); err != nil {
		t.Fatalf("delete --yes failed: %v", err)
	}
	if path != "/admin/roles/moderator" {
		t.Errorf("path was %q; want /admin/roles/moderator", path)
	}
	if query != "confirm=true" {
		t.Errorf("query was %q; want confirm=true", query)
	}
}
