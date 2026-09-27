package env

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/envname"
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
		if strings.HasPrefix(path, "/v1/cloud/projects/") {
			reply = map[string]any{"reachable": true}
			break
		}
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

// lastWrite is the last request that changed something. A create now ends on a
// status read, so "what did create send" is no longer the last call.
func (s *stubREST) lastWrite() call {
	for i := len(s.calls) - 1; i >= 0; i-- {
		if s.calls[i].method != http.MethodGet {
			return s.calls[i]
		}
	}
	return call{}
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
	// c2: the plan is the project's and includes ONE environment's compute; a
	// further environment costs its hours at the plan's rate. That is the
	// consequence a person must read before typing the name.
	require.Contains(t, strings.ToLower(out), "per hour",
		"the extra environment's hourly billing was not named before the question")
	// THE ENVELOPE LINE MUST SAY SOMETHING. Printing the label with an empty
	// value is worse than not printing it: the line exists so a person sees
	// what they will be billed for, and a blank reads as "nothing".
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "compute envelope") {
			value := strings.TrimSpace(strings.SplitN(line, "compute envelope", 2)[1])
			require.NotEmpty(t, value, "the compute envelope was printed with no value")
			require.Contains(t, strings.ToLower(value), "plan",
				"with no --tier, the line does not say the plan chooses the envelope")
		}
	}
	require.Equal(t, http.MethodGet, rest.last().method, "an environment was created before the confirmation")
}

func TestCreateProceedsWhenTheNameIsTyped(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	_, err := run(t, rest, "staging2\n", "create", "staging2")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, rest.lastWrite().method)
	require.Equal(t, "/v1/cloud/projects/prd_a/environments", rest.lastWrite().path)
	require.Equal(t, http.MethodGet, rest.calls[0].method, "create acted without reading the project first")
	sent, _ := rest.lastWrite().body.(map[string]any)
	require.Equal(t, "staging2", sent["name"])
	// THE ENVELOPE FIELD IS ABSENT unless somebody names one. The server's own
	// contract says the plan chooses it, so a constant sent from here would be
	// a policy duplicated on the side that does not own the catalogue — and it
	// pins itself: the day the catalogue's smallest changes, this CLI would
	// keep asking for yesterday's.
	_, sentTier := sent["tier"]
	require.False(t, sentTier, "create sent a compute envelope nobody asked for")
}

// AND WHEN SOMEBODY DOES NAME ONE, it is sent verbatim — the omission above is
// a default, not a refusal to carry the flag.
func TestCreateSendsTheEnvelopeWhenItIsNamed(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	out, err := run(t, rest, "", "create", "staging2", "--tier", "pro", "--yes")
	require.NoError(t, err)
	sent, _ := rest.lastWrite().body.(map[string]any)
	require.Equal(t, "pro", sent["tier"])
	require.Contains(t, out, "pro", "the envelope it will ask for is not printed")
}

func TestCreateWithYesSkipsThePrompt(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	_, err := run(t, rest, "", "create", "staging2", "--yes")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, rest.lastWrite().method)
}

func TestCreateWaitsForTheEnvironmentBeforeSayingHowToUseIt(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments()}

	out, err := run(t, rest, "", "create", "staging2", "--yes")
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, rest.last().method)
	require.Equal(t, "/v1/cloud/projects/new1234", rest.last().path, "env create returned without asking whether the environment answers")
	require.Contains(t, out, "waiting for new1234 to answer")
	require.Contains(t, out, "palbase env use staging2")
	require.Less(t, strings.Index(out, "waiting for new1234 to answer"), strings.Index(out, "palbase env use staging2"))
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

// A NEW NAME FOLLOWS THE SLUG GRAMMAR (FR-007, D-008), and it is judged before
// the CLI asks anybody anything: a name the control plane would store is a
// directory in every teammate's checkout and a build type in their Gradle.
// `Feature X` could not even be confirmed at the prompt — Fscanln stopped at
// the space and the command answered "aborted".
func TestCreateRefusesANameOutsideTheGrammarBeforeAnyRequest(t *testing.T) {
	linkedCheckout(t)
	for _, name := range []string{"Feature X", "feature/login", "..", "2fa", "feature_x", "Üretim", strings.Repeat("a", 40)} {
		rest := &stubREST{projects: twoEnvironments()}
		_, err := run(t, rest, "", "create", name, "--yes")
		require.Error(t, err, "%q was accepted", name)
		require.Equal(t, fmt.Sprintf("%q is not a valid environment name: use a letter, then up to 38 letters, digits or hyphens "+
			"(^[A-Za-z][A-Za-z0-9-]{0,38}$) — featureX or feature-login, for example", name), err.Error())
		require.Empty(t, rest.calls, "%q reached the control plane", name)
	}
}

// `local` AND `main` ARE NOT NAMES A PERSON GIVES (D-008), in any case: the
// control plane keeps names unique regardless of case, so `Main` is `main`.
func TestCreateRefusesAReservedNameInAnyCase(t *testing.T) {
	linkedCheckout(t)
	for name, why := range map[string]string{
		"local": "it names the stack `palbase start` runs on this machine",
		"LOCAL": "it names the stack `palbase start` runs on this machine",
		"main":  "it names a project's first environment",
		"Main":  "it names a project's first environment",
	} {
		rest := &stubREST{projects: twoEnvironments()}
		_, err := run(t, rest, "", "create", name, "--yes")
		require.Error(t, err, "%q was accepted", name)
		require.Equal(t, fmt.Sprintf("%q is reserved: %s", name, why), err.Error())
		require.Empty(t, rest.calls, "%q reached the control plane", name)
	}
}

// AND THE NAMES THE GRAMMAR IS FOR GO THROUGH UNCHANGED — camelCase is the
// point: `featureX` is `create("featureX")` in Gradle with no mapping.
func TestCreateAcceptsTheNamesTheGrammarIsFor(t *testing.T) {
	linkedCheckout(t)
	for _, name := range []string{"featureX", "feature-profile-update", "staging2", strings.Repeat("a", 39)} {
		rest := &stubREST{projects: twoEnvironments()}
		_, err := run(t, rest, "", "create", name, "--yes")
		require.NoError(t, err, name)
		sent, _ := rest.lastWrite().body.(map[string]any)
		require.Equal(t, name, sent["name"])
	}
}

// hostileName is an environment name the control plane accepts today: an OSC
// sequence that retitles the terminal, then a bell.
const (
	hostileName   = "evil\x1b]0;owned\a"
	hostileQuoted = `"evil\x1b]0;owned\a"`
)

func withAHostileName() []map[string]any {
	return []map[string]any{{
		"id": "prd_a", "name": "todoapp",
		"environments": []map[string]any{
			{"ref": "j06bwtuum", "name": "main", "status": "Running"},
			{"ref": "evilref001", "name": hostileName, "status": "Running"},
		},
	}}
}

// EVERY LINE OF `palbase env` THAT PRINTS A NAME PRINTS IT ESCAPED (FR-006):
// the name is somebody else's text, and raw it rewrote a teammate's terminal.
func TestEnvPrintsANameEscapedEverywhere(t *testing.T) {
	linkedCheckout(t)
	for _, c := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{"list"}, hostileQuoted + "  evilref001"},
		{"", []string{"use", "evilref001"}, "▸ todoapp/" + hostileQuoted},
		{"", []string{"use", "nope"}, "  " + hostileQuoted + "   evilref001"},
		{"wrong\n", []string{"delete", "evilref001"}, "This deletes todoapp/" + hostileQuoted + " (evilref001)"},
		{"", []string{"delete", "evilref001", "--yes"}, "Deleted " + hostileQuoted + " (evilref001)"},
	} {
		out, err := run(t, &stubREST{projects: withAHostileName()}, c.stdin, c.args...)
		if err != nil {
			out += err.Error()
		}
		require.NotContains(t, out, "\x1b", "`env %s` printed a name raw", strings.Join(c.args, " "))
		require.Contains(t, out, c.want, "env %s", strings.Join(c.args, " "))
	}
}

// AND THE NAME THE CONTROL PLANE ANSWERS A CREATE WITH IS ITS TEXT TOO — the
// CLI checked the name it SENT, not the one that came back.
func TestCreatePrintsTheNameItWasAnsweredWithEscaped(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments(), created: map[string]any{"ref": "evilref002", "name": hostileName, "phase": "Creating"}}

	out, err := run(t, rest, "", "create", "featureX", "--yes")
	require.NoError(t, err)
	require.NotContains(t, out, "\x1b", "`env create` printed a name raw")
	require.Contains(t, out, "Created "+hostileQuoted+" — evilref002 (Creating)")
	// THE HINT IS A COMMAND TO PASTE, not just text to read (T008 review): it
	// must survive BOTH the shell it might run in and the terminal it is
	// printed to right now, so it goes through ShellWord, not Label.
	require.Contains(t, out, "palbase env use "+envname.ShellWord(hostileName))
}
