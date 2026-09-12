package backend

// target.go — which project this checkout talks to.
//
// TWO facts, in two places, because they belong to different people.
//
// The TARGET is a fact about the PROJECT: `palbase/project.json` (projectPath,
// below), committed, so a colleague who clones the repository reaches the same
// place without being told which one it is. It names either a cloud project and
// environment, or a URL for something running on this machine — and nothing
// else. It used to carry the publishable key as well; that key now comes from
// the project itself, over an authenticated route, so a committed file no
// longer hands one out. (It also used to live at `.palbase/project.json`, the
// retired hidden root — see layout.go's LegacyRoots.)
//
// The CREDENTIAL is a fact about the PERSON: `~/.palbase/credentials.json`,
// never near the repository (see credentials.go). A token committed by accident
// is a token in every clone and every CI log.
//
// And there is a third, temporary fact: while `palbase start` is running, the
// stack in front of you is the target. That lives outside the checkout
// entirely now (LocalStatePath, in machine_state.go — it used to be
// `.palbase/local.json`, gitignored, inside it) and it wins for as long as it
// exists — which is what makes "work locally, then push" a two-word switch
// rather than a re-link.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/palgroup/palbase-cli/internal/authcontract"
)

// Target is where a verb acts.
type Target struct {
	checkoutRoot string
	// OAuth selects provider clients for this checkout once. Platform records and
	// native identifiers remain in the server's typed configuration.
	OAuth map[string]OAuthSelection `json:"oauth,omitempty"`
	// URL is an address this CLI talks to directly. Set for a project running on
	// this machine, and resolved from Project/Env for a cloud one.
	URL string `json:"url,omitempty"`
	// Project is the PRODUCT this checkout belongs to — the identity that does
	// not change. Which ENVIRONMENT a verb acts on is never stored here: it is
	// resolved per call (environments.go) and remembered, if at all, on this
	// machine. A committed environment is how a colleague pulls your branch and
	// pushes to your staging.
	//
	// Empty for a direct URL target.
	Project string `json:"project,omitempty"`
	// Name is what a person calls this project. The ref is the identity and it
	// never changes; this does — and a banner that printed `prd_9f21c7/staging`
	// would be correct and useless.
	Name string `json:"name,omitempty"`
	// Insecure records that this address still serves the certificate its first
	// boot generated. Remembered rather than retyped, because a flag somebody
	// has to repeat is a flag they will eventually paste at the wrong project.
	Insecure bool `json:"insecure,omitempty"`
	// Local is true when this target came from a running dev stack rather than
	// from the committed file. Not serialised — it is a fact about right now.
	Local bool `json:"-"`
}

// OnThisMachine says whether this target's stack runs HERE.
//
// The URL field's comment used to promise "a project running on this machine",
// and that held while `link` could only name a local stack. `palbase link
// https://<ref>.palbase.studio` writes a REMOTE one, and a verb that reads
// containers has to tell the two apart: measured 2026-08-21, `palbase logs` in a
// cloud-linked checkout answered "No such container: palbase-todoapp-runtime-1"
// — naming a container that was never going to exist.
//
// The host is PARSED, not searched for. `https://localhost.example.com` contains
// "localhost" and is somebody else's machine.
func (t Target) OnThisMachine() bool {
	if t.Local {
		return true
	}
	u, err := url.Parse(t.URL)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// Describe is what every verb prints before it acts.
func (t Target) Describe() string {
	if t.Local {
		return t.URL + " (local)"
	}
	if t.Project != "" {
		// The ENVIRONMENT is not part of a target any more — `Resolved.Describe`
		// prints `<project>/<env>` because only the resolver knows which one.
		if t.Name != "" {
			return t.Name
		}
		return t.Project
	}
	return t.URL
}

// projectPath is the committed record of which project this checkout belongs
// to — it lives in the ONE visible directory (C-1), beside the contracts.
func projectPath() string { return path.Join(RootDir(), "project.json") }

// localPath is where THIS MACHINE records the stack in front of you.
//
// It is not in the checkout. `palbase start` writing into the repository is how
// a repository acquires a file that must be ignored forever, and the layout's
// rule is that everything under `palbase/` is committed — so the per-machine
// half moved out entirely, next to the credentials that were already there.
func localPath() (string, error) { return LocalStatePath(".") }

func decodeTarget(raw []byte, target *Target) error {
	if err := authcontract.DecodeStrict(raw, target); err != nil {
		return err
	}
	for platform := range target.OAuth {
		if err := validatePlatforms([]string{platform}); err != nil {
			return fmt.Errorf("oauth: %w", err)
		}
	}
	return nil
}

// WriteTarget records the project this checkout belongs to.
func WriteTarget(t Target) error {
	if err := os.MkdirAll(RootDir(), 0o755); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(projectPath(), append(blob, '\n'), 0o644)
}

// ReadTarget returns the linked stack, or an error naming the command that
// would fix it. A tool that says "not linked" without saying how to link is a
// tool that sends people to the documentation for one line.
// WriteLocalTarget records the stack running in front of this checkout.
//
// Separate from WriteTarget because the two files answer different questions and
// have different lifetimes: project.json is committed and says which project this
// code belongs to, local.json is gitignored and says where it is running RIGHT
// NOW. Writing one through the other is how a `palbase start` ends up committing
// a localhost address into a colleague's checkout.
func WriteLocalTarget(t Target) error {
	blob, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	dest, err := localPath()
	if err != nil {
		return err
	}
	// SWEEP FIRST, THEN CREATE. The other order left a window: this process
	// created its directory, and a concurrent `palbase start` in another
	// checkout could see it still EMPTY — an empty record is litter by
	// definition — and remove it between the mkdir and the write. The write
	// then failed with ENOENT and `start` reported "no such file or directory"
	// about a path the person never chose. Sweeping before anything of ours
	// exists closes it.
	reapDeadCheckoutState()

	// THE WRITER CREATES THE DIRECTORY. Asking for the path does not — a read
	// that writes left one directory per question in the user's home.
	if err := ensureMachineStateDir(dest); err != nil {
		return err
	}
	rememberOrigin(filepath.Dir(dest), ".")
	return os.WriteFile(dest, append(blob, '\n'), 0o600)
}

// ReadTarget answers where a verb should act.
//
// A running dev stack WINS. `palbase start` records it under `~/.palbase` and
// `palbase stop` removes that record, so "am I working locally right now" is a
// fact on disk rather than a flag on every command — and every verb prints what
// it resolved, so nobody has to remember.
func ReadTarget() (Target, error) {
	local, pathErr := localPath()
	if pathErr != nil {
		return Target{}, pathErr
	}
	raw, err := os.ReadFile(local)
	if err == nil {
		var running Target
		if err := decodeTarget(raw, &running); err != nil {
			return Target{}, fmt.Errorf("read %s: %w", local, err)
		}
		if strings.TrimSpace(running.URL) == "" {
			return Target{}, fmt.Errorf("%s has no address — run `palbase start` again", local)
		}
		running.Local = true
		return running, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Target{}, fmt.Errorf("read %s: %w", local, err)
	}
	target, err := readLinkedProject()
	if err != nil {
		return Target{}, err
	}
	if strings.TrimSpace(target.URL) == "" {
		return Target{}, fmt.Errorf("%s has no address — run `palbase link <ref>` again", projectPath())
	}
	return target, nil
}

// ReadLinkedProject is readLinkedProject for packages outside this one — the
// `env` commands need to know which project a checkout belongs to before they
// can list its environments.
func ReadLinkedProject() (Target, error) { return readLinkedProject() }

func readLinkedProject() (Target, error) {
	raw, err := os.ReadFile(projectPath())
	if errors.Is(err, os.ErrNotExist) {
		// THE LAST LINE IS THE ONE PEOPLE TYPE, and it used to be
		// `--environment <ref>   act on one without linking`. That flag NARROWS
		// a project; it cannot name one. With no `--project`, the resolver reads
		// `.palbase/selection.json` for the project id before it ever looks at
		// the environment flag, and a checkout with no link has no such file —
		// so the advice printed here led straight to "no project selected".
		// A bare ref IS an address this cloud knows (`link` resolves it to
		// `<ref>.<PublicHost>`), so that is the line that does what the reader
		// came for.
		return Target{}, errors.New(
			"this checkout is not linked to a project.\n" +
				"  palbase link <project>        a project in the cloud\n" +
				"  palbase link <ref>            one environment of it, by ref\n" +
				"  palbase link <url>            something running on this machine\n" +
				"  palbase start                 bring one up here and link to it")
	}
	if err != nil {
		return Target{}, err
	}
	var t Target
	if err := decodeTarget(raw, &t); err != nil {
		return Target{}, fmt.Errorf("read %s: %w", projectPath(), err)
	}
	// A target names EITHER a cloud project or a direct address. Demanding an
	// address of both was wrong the moment `palbase env` started clearing it: the
	// address of a cloud environment is resolved from (project, env) when a verb
	// acts, so one cached here would be a second source of truth that goes stale
	// the first time an environment moves.
	if strings.TrimSpace(t.URL) == "" && strings.TrimSpace(t.Project) == "" {
		return Target{}, fmt.Errorf("%s names neither a project nor an address — run `palbase link` again", projectPath())
	}
	return t, nil
}

// credentials is the whole store: one identity per target URL.
type credentials struct {
	Credentials map[string]Credentials `json:"credentials"`
}

func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".palbase", "credentials.json"), nil
}

// stackVersion answers which stack generation this checkout runs.
//
// IT IS DERIVED, AND IT WRITES NOTHING. The committed file used to carry the
// answer and win over the installed package; measured, that field decided
// nothing (`start` picks images from `installedSDKVersion`) and could lie about
// the one thing it was printed for — bumping @palbase/backend 38 → 39 brought
// the 39 images up under a banner that still said 38.
//
// A PROJECT THAT CANNOT ANSWER GETS A REFUSAL, not an embedded default:
// falling back silently is how the stack becomes a property of the binary
// again, which is the whole defect.
func stackVersion(projectDir string) (string, error) {
	installed := installedBackendVersion(projectDir)
	if installed == "" {
		return "", fmt.Errorf(
			"%s is not installed here — run `npm install`", backendPkg)
	}
	major, _, _ := strings.Cut(installed, ".")
	if major == "" {
		return "", fmt.Errorf("cannot read a major out of the installed %s version %q", backendPkg, installed)
	}
	return major, nil
}
