package testuser

// The COMMANDS, end to end, against a project that answers on the wire.
//
// These used to run against a fake Studio and assert tRPC procedure names —
// which proved the CLI could speak a protocol nothing serves any more, and
// proved nothing about the door it actually knocks on. Every case here drives
// the real cobra command in a checkout linked to an httptest project, so what
// is pinned is the METHOD, the PATH and the BODY a person's flags turn into.
//
// project_test.go covers the same routes one layer down, at the helpers. Both
// are worth having: a helper can be right while nothing calls it.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// seenCall is one request the project received.
type seenCall struct {
	method string
	path   string
	body   map[string]any
}

// runCmd drives `test-user <args...>` in a checkout linked to a project that
// answers with `reply`, and hands back what the project was asked.
func runCmd(t *testing.T, reply string, args ...string) (seenCall, string, error) {
	t.Helper()
	var seen seenCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path = r.Method, r.URL.Path
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &seen.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	linkedTo(t, srv.URL)

	var out bytes.Buffer
	cmd := Cmd()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return seen, out.String(), err
}

// runCmdSplit is runCmd with the streams KEPT APART. `--json` is a scripting
// surface: the destination banner goes to stderr precisely so stdout stays
// machine-readable, and a test that merged them could not tell a broken payload
// from a working one.
func runCmdSplit(t *testing.T, reply string, args ...string) (string, string, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	linkedTo(t, srv.URL)

	var stdout, stderr bytes.Buffer
	cmd := Cmd()
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

// TestTestUserSurface pins the subcommand set and the flags. The command group
// is the whole lifecycle, not just minting.
func TestTestUserSurface(t *testing.T) {
	t.Chdir(t.TempDir())
	parent := Cmd()
	require.Equal(t, "test-user", parent.Name())

	subs := map[string]*cobra.Command{}
	for _, c := range parent.Commands() {
		subs[c.Name()] = c
	}
	for _, name := range []string{"create", "list", "templates", "clone", "delete"} {
		require.Contains(t, subs, name, "test-user must have a `%s` subcommand", name)
	}

	for _, flag := range []string{"template", "count", "json"} {
		require.NotNil(t, subs["create"].Flags().Lookup(flag), "create must define --%s", flag)
	}
	// --scenario is gone: templates come from config/test-users.ts now, and the
	// old scenario format had no way to author a scenario in the first place.
	require.Nil(t, subs["create"].Flags().Lookup("scenario"), "--scenario must be gone")

	for _, flag := range []string{"email", "password", "set", "json"} {
		require.NotNil(t, subs["clone"].Flags().Lookup(flag), "clone must define --%s", flag)
	}
}

func TestCreateAsksTheProjectToMint(t *testing.T) {
	seen, out, err := runCmd(t,
		`{"users":[{"user_id":"usr_1","email":"t1@test.invalid","password":"pw1","access_token":"tok1"}]}`,
		"create", "--json")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, seen.method)
	require.Equal(t, "/v1/management/test-users", seen.path)
	require.EqualValues(t, 1, seen.body["count"])
	require.Equal(t, true, seen.body["with_tokens"])
	require.Contains(t, out, "tok1", "--json must carry the token")
}

func TestCreateCountTravels(t *testing.T) {
	seen, _, err := runCmd(t, `{"users":[]}`, "create", "--count", "3")
	require.NoError(t, err)
	require.EqualValues(t, 3, seen.body["count"])
}

// --count AND --template together is an ordinary thing to want: several
// instances of one declared user, each with its own copy of the declared rows.
// The old tRPC arm refused the combination because its procedure minted exactly
// one; the project's door does not, so the refusal went with the arm.
func TestCountAndTemplateTravelTogether(t *testing.T) {
	seen, _, err := runCmd(t, `{"users":[]}`, "create", "--count", "2", "--template", "demo")
	require.NoError(t, err)
	require.EqualValues(t, 2, seen.body["count"])
	require.Equal(t, "demo", seen.body["template"])
}

func TestCreateRefusesACountBelowOne(t *testing.T) {
	_, _, err := runCmd(t, `{}`, "create", "--count", "0")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--count must be at least 1")
}

func TestListReadsTheProject(t *testing.T) {
	seen, out, err := runCmd(t,
		`{"users":[{"id":"usr_1","email":"a@test.invalid","email_verified":true}]}`, "list")
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, seen.method)
	require.Equal(t, "/v1/management/test-users", seen.path)
	require.Contains(t, out, "usr_1")
	require.Contains(t, out, "a@test.invalid")
}

func TestTemplatesReadTheProject(t *testing.T) {
	seen, out, err := runCmd(t,
		`{"templates":[{"name":"demo","email":"demo@test.invalid","tables":["profiles"]}]}`, "templates")
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, seen.method)
	require.Equal(t, "/v1/management/test-users/templates", seen.path)
	require.Contains(t, out, "demo")
	require.Contains(t, out, "profiles")
}

// The named-credentials pair reaches the project now. It used to be refused
// here by name — "not available against a local stack" — because the door did
// not take them; it does, so the flags mean what the help says they mean.
func TestCloneCarriesFixedCredentials(t *testing.T) {
	seen, _, err := runCmd(t,
		`{"users":[{"user_id":"usr_2","email":"fixed@test.invalid","password":"pw"}]}`,
		"clone", "usr_1", "--email", "fixed@test.invalid", "--password", "kUzey-4271-orman")
	require.NoError(t, err)
	require.Equal(t, "/v1/management/test-users/clone", seen.path)
	require.Equal(t, "usr_1", seen.body["source_user_id"])
	require.Equal(t, "fixed@test.invalid", seen.body["email"])
	require.Equal(t, "kUzey-4271-orman", seen.body["password"])
}

func TestCloneWithoutCredentialsSendsNone(t *testing.T) {
	seen, _, err := runCmd(t,
		`{"users":[{"user_id":"usr_2","email":"gen@test.invalid","password":"pw"}]}`,
		"clone", "usr_1")
	require.NoError(t, err)
	require.NotContains(t, seen.body, "email", "an unasked-for e-mail must not be invented")
	require.NotContains(t, seen.body, "password")
}

func TestCloneRefusesHalfACredentialPair(t *testing.T) {
	_, _, err := runCmd(t, `{}`, "clone", "usr_1", "--email", "only@test.invalid")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be given together")
}

func TestDeletePurgesAtTheProject(t *testing.T) {
	seen, out, err := runCmd(t, `{}`, "delete", "usr_9")
	require.NoError(t, err)
	require.Equal(t, http.MethodDelete, seen.method)
	require.Equal(t, "/v1/management/test-users/usr_9", seen.path)
	require.Contains(t, out, "usr_9")
}

// A project that refuses explains itself, and the explanation is the whole
// value of relaying it: "request failed" would send a person to the logs for
// something the answer already said.
func TestAProjectsRefusalReachesThePerson(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"test_user_cap","error_description":"This project allows at most 50 test users."}`))
	}))
	defer srv.Close()
	linkedTo(t, srv.URL)

	cmd := Cmd()
	cmd.SetArgs([]string{"create"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "at most 50 test users"),
		"the project's own explanation was dropped: %v", err)
}

// With neither a link nor a selection there is nothing to act on, and the error
// has to name BOTH ways to fix it — a whole project, or the one environment of
// it the reader came for.
//
// THE SECOND DOOR USED TO BE `--environment`, and this assertion is what held it
// open: the flag narrows a project the resolver has already found, so in a
// checkout with no link it selects nothing at all. `palbase link <ref>` is the
// door that opens.
func TestWithNoProjectTheAdviceNamesBothDoors(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	cmd := Cmd()
	cmd.SetArgs([]string{"list"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase link <project>")
	require.Contains(t, err.Error(), "palbase link <ref>")
	require.NotContains(t, err.Error(), "--environment")
}

func TestParseSets(t *testing.T) {
	t.Run("groups by table and types values", func(t *testing.T) {
		got, err := parseSets([]string{
			"profiles.display_name=Copy",
			"profiles.age=41",
			"profiles.active=true",
			"lists.archived_at=null",
			`lists.meta={"a":1}`,
		})
		require.NoError(t, err)
		require.Equal(t, "Copy", got["profiles"]["display_name"])
		require.EqualValues(t, 41, got["profiles"]["age"])
		require.Equal(t, true, got["profiles"]["active"])
		require.Nil(t, got["lists"]["archived_at"])
		require.Equal(t, map[string]any{"a": float64(1)}, got["lists"]["meta"])
	})

	t.Run("keeps a value that only looks like JSON as a string", func(t *testing.T) {
		got, err := parseSets([]string{"profiles.bio=hello world"})
		require.NoError(t, err)
		require.Equal(t, "hello world", got["profiles"]["bio"])
	})

	t.Run("accepts an empty value", func(t *testing.T) {
		got, err := parseSets([]string{"profiles.bio="})
		require.NoError(t, err)
		require.Equal(t, "", got["profiles"]["bio"])
	})

	t.Run("rejects a missing =", func(t *testing.T) {
		_, err := parseSets([]string{"profiles.display_name"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "table.column=value")
	})

	t.Run("rejects a missing table qualifier", func(t *testing.T) {
		_, err := parseSets([]string{"display_name=Copy"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "table and a column")
	})

	t.Run("nil for no sets", func(t *testing.T) {
		got, err := parseSets(nil)
		require.NoError(t, err)
		require.Nil(t, got)
	})
}

// --- templates set: the door config/test-users.ts used to be ----------------

// `config/test-users.ts` was PUT at the stack by the push (carryTestUsers). The
// file is gone, so this verb is the door — and it must send `{templates: {...}}`,
// not the evaluated document's own shape.
//
// THAT EXACT MISMATCH ALREADY COST A RELEASE: carryTestUsers sent the document
// as-is, which decoded CLEANLY into a request with no templates; the stack
// stored the empty set and answered 200 while the push printed "declared the
// project's test users" (measured 2026-08-24). Same map, different key. This
// test pins the key.
func TestTemplatesSet_SendsTheTemplatesKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "templates.json")
	doc := `{"alice":{"email":"alice@example.com","password":"pw"}}`
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))

	seen, _, err := runCmd(t, `{"applied":1}`, "templates", "set", "--file", path)
	require.NoError(t, err)
	require.Equal(t, http.MethodPut, seen.method)
	require.Equal(t, "/v1/management/test-users/templates", seen.path)

	tpl, ok := seen.body["templates"].(map[string]any)
	require.True(t, ok, "the request must carry a `templates` object, got %v", seen.body)
	require.Contains(t, tpl, "alice")
}

// The bare `templates` verb keeps LISTING. Turning it into a group must not take
// away the reading it already did.
//
// Note the ASYMMETRY the reply shape records: GET answers an ARRAY of templates,
// while the PUT above takes a MAP under `templates`. That is the module's
// existing contract — carryTestUsers sent the same map — and not something this
// change invented.
func TestTemplates_BareStillLists(t *testing.T) {
	seen, _, err := runCmd(t, `{"templates":[]}`, "templates")
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, seen.method)
}

// A file that does not parse is refused BEFORE the round trip.
func TestTemplatesSet_RefusesMalformedJSONLocally(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	require.NoError(t, os.WriteFile(path, []byte(`{nope`), 0o600))

	_, _, err := runCmd(t, `{}`, "templates", "set", "--file", path)
	require.Error(t, err)
}

// `--json` şekli HİÇBİR YERDE belgeli değildi ve harness'ın istediği şekle
// UYMUYORDU: burası {"users":[{user_id,email,password,access_token}]} basıyor,
// createTestApi.identities ise {"<ad>":{id,email,password,accessToken}}
// istiyor. Aradaki çeviriyi her müşteri elle yazıyor — ölçülen koşuda bir
// mint-betiği ve onun kopyaladığı beş satır. Çıktı artık DOĞRUDAN geçirilebilir.
func TestCreateJSONIsWhatTheHarnessAccepts(t *testing.T) {
	reply := `{"users":[
		{"user_id":"usr_1","email":"a@e.f","password":"p1","access_token":"tok1"},
		{"user_id":"usr_2","email":"b@e.f","password":"p2","access_token":"tok2"}
	]}`

	out, banner, err := runCmdSplit(t, reply, "create", "--count", "2", "--json")
	if err != nil {
		t.Fatalf("create --json: %v", err)
	}

	// Banner stderr'de kalmalı: stdout borulanabilir olmalı.
	if !strings.Contains(banner, "▸") {
		t.Errorf("hedef banner stderr'e gitmiyor: %q", banner)
	}

	var got struct {
		Identities map[string]struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			Password    string `json:"password"`
			AccessToken string `json:"accessToken"`
		} `json:"identities"`
	}
	if uErr := json.Unmarshal([]byte(out), &got); uErr != nil {
		t.Fatalf("çıktı JSON değil: %v\n%s", uErr, out)
	}
	if len(got.Identities) != 2 {
		t.Fatalf("identities iki kimlik taşımalı, %d taşıyor: %s", len(got.Identities), out)
	}
	first, ok := got.Identities["user1"]
	if !ok {
		t.Fatalf("kimlikler ada göre anahtarlanmalı (user1, user2): %s", out)
	}
	if first.ID != "usr_1" || first.Email != "a@e.f" || first.Password != "p1" || first.AccessToken != "tok1" {
		t.Errorf("alan adları harness'ın şekline uymuyor: %+v", first)
	}
	// NEGATİF KONTROL: yığının kendi yazımı ÇIKTIDA KALMAMALI, yoksa iki şekil
	// bir arada yaşar ve hangisinin sözleşme olduğu belirsizleşir.
	if strings.Contains(out, "access_token") || strings.Contains(out, "user_id") {
		t.Errorf("yığının snake_case yazımı çıktıda kaldı: %s", out)
	}
}
