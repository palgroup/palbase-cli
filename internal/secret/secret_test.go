package secret

// secret_test.go — the two rules, asserted: the value never lands, and the child
// process is the only place it goes.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/backend"
)

const theValue = "sk-live-DO-NOT-LEAK-8f2a"

// projectHolding answers as a project's management surface for the given
// secrets, and records every path it was asked for.
func projectHolding(t *testing.T, secrets map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	seen := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v1/management/secrets" && r.Method == http.MethodGet:
			list := struct {
				Secrets []entry `json:"secrets"`
			}{}
			for name := range secrets {
				list.Secrets = append(list.Secrets, entry{Name: name})
			}
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(list)

		case strings.HasSuffix(r.URL.Path, "/value") && r.Method == http.MethodGet:
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/management/secrets/"), "/value")
			value, ok := secrets[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("content-type", "text/plain")
			_, _ = w.Write([]byte(value))

		case r.Method == http.MethodPut:
			var body struct {
				Value string `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			secrets[strings.TrimPrefix(r.URL.Path, "/v1/management/secrets/")] = body.Value
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete:
			delete(secrets, strings.TrimPrefix(r.URL.Path, "/v1/management/secrets/"))
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

// linkedCheckout puts the test in a scratch directory linked to url, with a
// credential for it — the state `palbase start` or `palbase link` leaves behind.
func linkedCheckout(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(backend.AccessTokenEnv, "")
	if err := backend.WriteTarget(backend.Target{URL: url}); err != nil {
		t.Fatal(err)
	}
	if err := backend.StoreCredential(url, backend.Credentials{Value: "a-credential", Kind: backend.KindPerson}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runSecret(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := Cmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// TestNothingReadsOrWritesADotenv is FR-028, and it is checked by looking at the
// DIRECTORY rather than at the code: the old group's `pull` wrote .env.local,
// which is how a production credential ends up in a screen share and eventually
// a repository.
func TestNothingReadsOrWritesADotenv(t *testing.T) {
	srv, _ := projectHolding(t, map[string]string{"SENTRY_DSN": theValue})
	dir := linkedCheckout(t, srv.URL)

	// A dotenv that ALREADY exists must not be read either — if it were, a
	// stale local file would silently win over the vault.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SENTRY_DSN=from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runSecret(t, "", "list"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, _, err := runSecret(t, theValue, "set", "OTHER", "--stdin"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, _, err := runSecret(t, "", "remove", "OTHER"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".env") && e.Name() != ".env" {
			t.Errorf("a dotenv file was written: %s", e.Name())
		}
	}
	// And the pre-existing one is untouched.
	body, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil || !strings.Contains(string(body), "from-the-file") {
		t.Errorf(".env was modified: %q %v", body, err)
	}
	// The verbs that used to write one are gone entirely.
	for _, c := range Cmd().Commands() {
		if c.Name() == "pull" || c.Name() == "push" {
			t.Errorf("`secret %s` is back", c.Name())
		}
	}
}

// TestSetReadsTheValueFromStdin is FR-029 — the form that keeps a credential out
// of shell history — and it keeps the trailing newline, because a PEM without
// its final newline is a PEM that fails to parse.
func TestSetReadsTheValueFromStdin(t *testing.T) {
	held := map[string]string{}
	srv, _ := projectHolding(t, held)
	linkedCheckout(t, srv.URL)

	pem := "-----BEGIN EC PRIVATE KEY-----\nMHcCAQ…\n-----END EC PRIVATE KEY-----\n"
	out, _, err := runSecret(t, pem, "set", "SIGNING_KEY", "--stdin")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if held["SIGNING_KEY"] != pem {
		t.Errorf("the project stored %q", held["SIGNING_KEY"])
	}
	if strings.Contains(out, "MHcCAQ") {
		t.Errorf("the value was printed back:\n%s", out)
	}
	if !strings.Contains(out, "SIGNING_KEY is set") {
		t.Errorf("output = %q", out)
	}
}

// TestNoOutputEverCarriesAValue is the rule stated as a test: whatever a verb
// prints, the secret is not in it.
func TestNoOutputEverCarriesAValue(t *testing.T) {
	srv, _ := projectHolding(t, map[string]string{"SENTRY_DSN": theValue})
	linkedCheckout(t, srv.URL)

	for _, args := range [][]string{{"list"}, {"set", "SENTRY_DSN=" + theValue}, {"remove", "SENTRY_DSN"}} {
		out, errOut, err := runSecret(t, "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(out+errOut, theValue) {
			t.Errorf("`secret %s` printed the value:\n%s%s", strings.Join(args, " "), out, errOut)
		}
	}
}

// TestListShowsNamesAndWhenTheyChanged is FR-030.
func TestListShowsNamesAndWhenTheyChanged(t *testing.T) {
	srv, _ := projectHolding(t, map[string]string{"SENTRY_DSN": theValue, "STRIPE_KEY": "sk_test_x"})
	linkedCheckout(t, srv.URL)

	out, _, err := runSecret(t, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "LAST CHANGED", "SENTRY_DSN", "STRIPE_KEY"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing is missing %q:\n%s", want, out)
		}
	}
}

// TestRunGivesTheChildTheValues is FR-031, and it asserts all three halves: the
// child SEES the value, the parent does NOT, and nothing was written down.
func TestRunGivesTheChildTheValues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child here is a POSIX shell")
	}
	srv, _ := projectHolding(t, map[string]string{"SENTRY_DSN": theValue})
	dir := linkedCheckout(t, srv.URL)

	var out, errOut bytes.Buffer
	cmd := RunCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"sh", "-c", "printf %s \"$SENTRY_DSN\""})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("run: %v\n%s", err, errOut.String())
	}

	if out.String() != theValue {
		t.Errorf("the child did not see the value: %q", out.String())
	}
	if os.Getenv("SENTRY_DSN") != "" {
		t.Error("the value leaked into THIS process, so everything it starts next inherits it")
	}
	if strings.Contains(errOut.String(), theValue) {
		t.Errorf("the value was announced:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "SENTRY_DSN") {
		t.Errorf("the names were not announced, so nobody can see what the command was given:\n%s", errOut.String())
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".env") {
			t.Errorf("run wrote %s", e.Name())
		}
		if e.IsDir() {
			continue
		}
		body, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if strings.Contains(string(body), theValue) {
			t.Errorf("the value was written to %s", e.Name())
		}
	}
}

// TestRunCarriesTheChildsExitStatus: `palbase run -- npm test` that fails must
// fail, or a green CI step is meaningless.
func TestRunCarriesTheChildsExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child here is a POSIX shell")
	}
	srv, _ := projectHolding(t, map[string]string{})
	linkedCheckout(t, srv.URL)

	cmd := RunCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"sh", "-c", "exit 3"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("a failing command reported success")
	}
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) {
		t.Fatalf("the error carries no exit status: %T", err)
	}
	if coded.ExitCode() != 3 {
		t.Errorf("exit status = %d", coded.ExitCode())
	}
	if err.Error() != "" {
		t.Errorf("a message would print above the child's own output: %q", err.Error())
	}
}

// TestRunPassesTheChildsOwnFlagsThrough: everything after `--` is the child's,
// including flags cobra would otherwise reject.
func TestRunPassesTheChildsOwnFlagsThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child here is a POSIX shell")
	}
	srv, _ := projectHolding(t, map[string]string{})
	linkedCheckout(t, srv.URL)

	var out bytes.Buffer
	cmd := RunCmd()
	cmd.SetOut(&out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"sh", "-c", "printf %s \"$1\"", "sh", "--watch"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.String() != "--watch" {
		t.Errorf("the child's flag did not reach it: %q", out.String())
	}
}
