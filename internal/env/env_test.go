package env

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// call is one control-plane request these commands made.
type call struct {
	method, path string
	body         any
}

// stubREST answers the project listing from a fixture and records every call.
// It routes on METHOD because the reply shapes differ: the listing is an array,
// a creation is one object — a stub that answers both with the same fixture
// fails at the json boundary and blames the production code for it.
type stubREST struct {
	projects any
	created  any
	err      error
	calls    []call
}

func (s *stubREST) Do(_ context.Context, method, path string, body, out any) error {
	s.calls = append(s.calls, call{method, path, body})
	if s.err != nil {
		return s.err
	}
	var reply any
	switch method {
	case http.MethodGet:
		reply = s.projects
	case http.MethodPost:
		reply = s.created
		if reply == nil {
			reply = map[string]any{"ref": "new1234", "name": "staging2", "phase": "Creating"}
		}
	}
	if reply == nil || out == nil {
		return nil
	}
	blob, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(blob, out)
}

// last is the request the command finished on, and first is what it asked
// before acting. Both matter: a destructive verb must READ before it WRITES.
func (s *stubREST) last() call {
	if len(s.calls) == 0 {
		return call{}
	}
	return s.calls[len(s.calls)-1]
}

// linkedCheckout puts the test in a scratch directory bound to one project,
// with this machine's state pointed somewhere throwaway.
func linkedCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "palbase"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "palbase", "project.json"),
		[]byte(`{"project":"prd_a","name":"todoapp"}`), 0o644))
	return dir
}

func twoEnvironments() []map[string]any {
	return []map[string]any{{
		"id": "prd_a", "name": "todoapp",
		"environments": []map[string]any{
			{"ref": "j06bwtuum", "name": "main", "status": "Running"},
			{"ref": "mu0028", "name": "staging", "status": "Running"},
		},
	}}
}

func run(t *testing.T, rest REST, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd(Resolvers{REST: func() REST { return rest }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestListShowsEveryEnvironmentWithItsRef(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	out, err := run(t, rest, "", "list")
	require.NoError(t, err)
	require.Equal(t, "/api/v2/projects", rest.last().path)
	for _, want := range []string{"main", "j06bwtuum", "staging", "mu0028", "Running"} {
		require.Contains(t, out, want)
	}
}

// THE SELECTED ONE IS MARKED, and that mark is why anybody runs this command:
// "which one am I on" is the question they open it with.
func TestListMarksTheSelectedEnvironment(t *testing.T) {
	dir := linkedCheckout(t)
	require.NoError(t, backend.WriteSelection(dir, backend.Selection{
		Project: "prd_a", Env: "staging", Ref: "mu0028",
	}))

	out, err := run(t, &stubREST{projects: twoEnvironments()}, "", "list")
	require.NoError(t, err)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "staging") {
			require.True(t, strings.HasPrefix(strings.TrimSpace(line), "*"),
				"the selected environment is not marked: %q", line)
			return
		}
	}
	t.Fatalf("staging is not in the listing:\n%s", out)
}

// A SELECTION FROM ANOTHER PROJECT IS NOT MARKED. The record carries its
// project precisely so a relinked checkout cannot inherit one.
func TestListDoesNotMarkAnotherProjectsSelection(t *testing.T) {
	dir := linkedCheckout(t)
	require.NoError(t, backend.WriteSelection(dir, backend.Selection{
		Project: "prd_OTHER", Env: "staging", Ref: "mu0028",
	}))

	out, err := run(t, &stubREST{projects: twoEnvironments()}, "", "list")
	require.NoError(t, err)
	require.NotContains(t, out, "*", "a selection belonging to another project was marked")
}

func TestUseRemembersOneEnvironmentOutsideTheRepository(t *testing.T) {
	dir := linkedCheckout(t)

	out, err := run(t, &stubREST{projects: twoEnvironments()}, "", "use", "staging")
	require.NoError(t, err)
	require.Contains(t, out, "todoapp/staging")

	got, readErr := backend.ReadSelection(dir)
	require.NoError(t, readErr)
	require.Equal(t, "staging", got.Env)
	require.Equal(t, "mu0028", got.Ref)
	require.Equal(t, "prd_a", got.Project, "the record does not say which project it belongs to")

	// NOT IN THE REPOSITORY. A selection in version control switches a
	// colleague from staging to production the moment they pull.
	var leaked []string
	require.NoError(t, filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(strings.ToLower(filepath.Base(p)), "selection") {
			leaked = append(leaked, p)
		}
		return nil
	}))
	require.Empty(t, leaked, "the selection leaked into the customer's checkout")
}

func TestUseRefusesAnUnknownNameAndListsTheRealOnes(t *testing.T) {
	linkedCheckout(t)

	_, err := run(t, &stubREST{projects: twoEnvironments()}, "", "use", "production")
	require.Error(t, err)
	require.Contains(t, err.Error(), "production")
	require.Contains(t, err.Error(), "staging", "the refusal does not list what does exist")
}

// A REF WORKS WHERE A NAME DOES: the listing prints both, and a person types
// what they see.
func TestUseAcceptsARef(t *testing.T) {
	dir := linkedCheckout(t)

	_, err := run(t, &stubREST{projects: twoEnvironments()}, "", "use", "mu0028")
	require.NoError(t, err)
	got, readErr := backend.ReadSelection(dir)
	require.NoError(t, readErr)
	require.Equal(t, "staging", got.Env, "a ref must resolve to its environment's NAME")
}

// CREATE PRINTS THE BILLING CONSEQUENCE BEFORE IT ASKS.
//
// When this verb was left out of the CLI in August the reason given was that
// the web surface does it better — "confirmations, membership, billing
// consequences". Bringing it back without the consequence would drop the very
// thing that justified leaving it out.
func TestCreateNamesTheCostAndAsksFirst(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	// An empty answer is not the name: the prompt is not satisfied by reflex.
	out, err := run(t, rest, "\n", "create", "staging2")
	require.Error(t, err)
	require.Contains(t, out, "staging2")
	require.Contains(t, strings.ToLower(out), "billing",
		"the billing consequence was not printed before the question")
	require.Contains(t, strings.ToLower(out), "quota")
	require.Equal(t, http.MethodGet, rest.last().method, "an environment was created before the confirmation")
}

func TestCreateProceedsWhenTheNameIsTyped(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	_, err := run(t, rest, "staging2\n", "create", "staging2")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, rest.last().method)
	require.Equal(t, "/v1/cloud/projects/prd_a/environments", rest.last().path)
	require.Equal(t, http.MethodGet, rest.calls[0].method, "create acted without reading the project first")
	sent, _ := rest.last().body.(map[string]any)
	require.Equal(t, "staging2", sent["name"])
	require.Equal(t, "free", sent["tier"], "the default compute envelope is not the plan's smallest")
}

func TestCreateWithYesSkipsThePrompt(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	_, err := run(t, rest, "", "create", "staging2", "--yes")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, rest.last().method)
}

// DELETE ASKS FOR THE REF, not a yes. Typing it means having read which
// environment this is — and there is no undo.
func TestDeleteRefusesAMismatchedConfirmation(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	out, err := run(t, rest, "staging\n", "delete", "staging")
	require.Error(t, err, "typing the NAME satisfied a prompt that asks for the REF")
	require.Contains(t, out, "mu0028", "the prompt does not show the ref it wants")
	require.Equal(t, http.MethodGet, rest.last().method, "an environment was deleted before the confirmation")
}

func TestDeleteProceedsOnTheRef(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	_, err := run(t, rest, "mu0028\n", "delete", "staging")
	require.NoError(t, err)
	require.Equal(t, http.MethodDelete, rest.last().method)
	require.Equal(t, "/v1/cloud/projects/mu0028", rest.last().path)
}

// A SELF-HOST CHECKOUT IS TOLD WHY, by name.
func TestASelfHostCheckoutIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "palbase"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "palbase", "project.json"),
		[]byte(`{"url":"https://stack.firma.com"}`), 0o644))

	_, err := run(t, &stubREST{}, "", "list")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stack.firma.com")
	require.Contains(t, err.Error(), "one environment")
}

// AND AN UNLINKED CHECKOUT GETS THE REFUSAL THAT CARRIES THE FIX.
func TestAnUnlinkedCheckoutIsRefusedWithTheWayIn(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	_, err := run(t, &stubREST{}, "", "list")
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase link")
}
