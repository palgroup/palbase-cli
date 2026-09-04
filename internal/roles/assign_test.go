package roles

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `assign` atamayı kullanıcının kendi kapısına yazar.
func TestAssignWritesTheGrant(t *testing.T) {
	var seen struct{ method, path string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"roles":["moderator"]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	out, err := run(t, "assign", "usr_1", "moderator")
	if err != nil {
		t.Fatalf("assign failed: %v (%s)", err, out)
	}
	if seen.method != http.MethodPut || seen.path != "/admin/users/usr_1/roles/moderator" {
		t.Fatalf("called %s %s; want PUT /admin/users/usr_1/roles/moderator", seen.method, seen.path)
	}
}

func TestRevokeRemovesTheGrant(t *testing.T) {
	var seen struct{ method, path string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"roles":[]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	if _, err := run(t, "revoke", "usr_1", "moderator"); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}
	if seen.method != http.MethodDelete || seen.path != "/admin/users/usr_1/roles/moderator" {
		t.Fatalf("called %s %s; want DELETE /admin/users/usr_1/roles/moderator", seen.method, seen.path)
	}
}

// `list <userId>` bir KULLANICININ rollerini gösterir — argümansız hâli
// ortamın TANIMLARINI gösteriyordu; ikisi ayrı sorular, tek fiil.
func TestListWithAUserShowsThatUsersRoles(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"roles":["agent","moderator"]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	out, err := run(t, "list", "usr_1")
	if err != nil {
		t.Fatalf("list <user> failed: %v (%s)", err, out)
	}
	if path != "/admin/users/usr_1/roles" {
		t.Fatalf("called %s; want /admin/users/usr_1/roles", path)
	}
	for _, want := range []string{"agent", "moderator"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// Tanımsız rol atamaya çalışmak sunucuda 400 veriyor; CLI o cümleyi İLETMELİ
// ve ne yapılacağını söylemeli — "role_not_defined" tek başına bir yön değil.
func TestAssignSurfacesAnUndefinedRole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"role_not_defined","error_description":"role is not defined"}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	out, err := run(t, "assign", "usr_1", "ghost")
	if err == nil {
		t.Fatal("assign succeeded although the role is not defined")
	}
	combined := err.Error() + out
	if !strings.Contains(combined, "not defined") {
		t.Fatalf("refusal not surfaced:\n%v\n%s", err, out)
	}
	if !strings.Contains(combined, "palbase roles create") {
		t.Fatalf("the error does not say how to fix it:\n%v\n%s", err, out)
	}
}

// `list --json` bir kullanıcı için de yalnız JSON basar.
func TestListUserJSONIsPureJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"roles":["agent"]}`))
	}))
	defer srv.Close()
	linkedProject(t, srv.URL)

	out, err := run(t, "list", "usr_1", "--json")
	if err != nil {
		t.Fatalf("list --json failed: %v (%s)", err, out)
	}
	var parsed any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("output was not pure JSON: %v\n---\n%s", err, out)
	}
}

func TestAssignVerbsAreWired(t *testing.T) {
	names := map[string]bool{}
	for _, c := range Cmd().Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"assign", "revoke"} {
		if !names[want] {
			t.Fatalf("`palbase roles` has no %q subcommand", want)
		}
	}
}
