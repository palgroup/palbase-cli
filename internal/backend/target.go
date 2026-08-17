package backend

// target.go — which project this checkout talks to.
//
// TWO facts, in two places, because they belong to different people.
//
// The TARGET is a fact about the PROJECT: `.palbase/project.json`, committed, so
// a colleague who clones the repository reaches the same place without being
// told which one it is. It names either a cloud project and environment, or a
// URL for something running on this machine — and nothing else. It used to carry
// the publishable key as well; that key now comes from the project itself, over
// an authenticated route, so a committed file no longer hands one out.
//
// The CREDENTIAL is a fact about the PERSON: `~/.palbase/credentials.json`,
// never near the repository (see credentials.go). A token committed by accident
// is a token in every clone and every CI log.
//
// And there is a third, temporary fact: while `palbase start` is running, the
// stack in front of you is the target. That lives in `.palbase/local.json`,
// gitignored, and it wins for as long as it exists — which is what makes "work
// locally, then push" a two-word switch rather than a re-link.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Target is where a verb acts.
type Target struct {
	// URL is an address this CLI talks to directly. Set for a project running on
	// this machine, and resolved from Project/Env for a cloud one.
	URL string `json:"url,omitempty"`
	// Project and Env name a cloud project group and one of its environments.
	// Empty for a direct URL target.
	Project string `json:"project,omitempty"`
	Env     string `json:"env,omitempty"`
	// Insecure records that this address still serves the certificate its first
	// boot generated. Remembered rather than retyped, because a flag somebody
	// has to repeat is a flag they will eventually paste at the wrong project.
	Insecure bool `json:"insecure,omitempty"`
	// Local is true when this target came from a running dev stack rather than
	// from the committed file. Not serialised — it is a fact about right now.
	Local bool `json:"-"`
}

// Describe is what every verb prints before it acts.
func (t Target) Describe() string {
	if t.Local {
		return t.URL + " (local)"
	}
	if t.Project != "" {
		if t.Env != "" {
			return t.Project + "/" + t.Env
		}
		return t.Project
	}
	return t.URL
}

func projectPath() string { return filepath.Join(nativeArtifactsDir, "project.json") }
func localPath() string   { return filepath.Join(nativeArtifactsDir, "local.json") }

// WriteTarget records the project this checkout belongs to.
func WriteTarget(t Target) error {
	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
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
	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(localPath(), append(blob, '\n'), 0o644)
}

// ReadTarget answers where a verb should act.
//
// A running dev stack WINS. `palbase start` writes `.palbase/local.json` and
// `palbase stop` removes it, so "am I working locally right now" is a fact on
// disk rather than a flag on every command — and every verb prints what it
// resolved, so nobody has to remember.
func ReadTarget() (Target, error) {
	if raw, err := os.ReadFile(localPath()); err == nil {
		var local Target
		if err := json.Unmarshal(raw, &local); err != nil {
			return Target{}, fmt.Errorf("read %s: %w", localPath(), err)
		}
		if local.URL != "" {
			local.Local = true
			return local, nil
		}
	}
	return readLinkedProject()
}

func readLinkedProject() (Target, error) {
	raw, err := os.ReadFile(projectPath())
	if errors.Is(err, os.ErrNotExist) {
		return Target{}, errors.New(
			"this checkout is not linked to a project.\n" +
				"  palbase link <project>        a project in the cloud\n" +
				"  palbase link <url>            something running on this machine\n" +
				"  palbase start                 bring one up here and link to it")
	}
	if err != nil {
		return Target{}, err
	}
	var t Target
	if err := json.Unmarshal(raw, &t); err != nil {
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
