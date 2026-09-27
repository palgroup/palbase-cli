package backend

// link_names.go — which of the listed environments a link may WRITE.
//
// `palbase/environments/<name>/` is built from the name the control plane
// lists, and the listing is somebody else's text (internal/envname). So the
// listing is judged here, once, before the link asks anybody anything and
// before it writes a byte. Both ways a link learns the listing — a project
// named on the command line (listCLIProjects) and a committed record linked
// again (EnvironmentsOf) — arrive in linkOpts.environments, so this is the one
// place that sees every name on its way to becoming a path. The listing itself
// stays as the cloud sent it: a verb that RESOLVES a name (`--env`, `env use`)
// never makes a directory of it.

import (
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/palgroup/palbase-cli/internal/envname"
)

// linkableEnvironments is the listing without the environments this link must
// not write, with a line for each one it left out.
//
// Three rules, in this order:
//
//   - a name that is not one directory (envname.CheckDir) never becomes a path;
//   - `local`, in any case, is the directory of the stack on THIS machine
//     (FR-004): a cloud environment written there was silently replaced by
//     that stack, or left beside its config when none was registered;
//   - names that match when letter case and Unicode form are ignored
//     (envname.SameDirectory) are ONE directory on APFS, so the second write
//     landed in the first one's directory and the app built one environment's
//     address under the other's name (FR-003). All of them are left out, each
//     named by its ref — choosing one is choosing at random.
//
// LEAVING ONE OUT IS SAID, AND THE LINK GOES ON (FR-001). Leaving out the
// environment this link READS FROM cannot be: it is what the app builds against
// when nothing else is chosen, so the link refuses and names the fix (D-012).
func linkableEnvironments(envs []Environment, linkedEnv string, w io.Writer) ([]Environment, error) {
	named := make([]Environment, 0, len(envs))
	for _, e := range envs {
		reason := whyNotWritable(e.Name, false)
		if reason == "" {
			named = append(named, e)
			continue
		}
		if e.Name == linkedEnv {
			return nil, fmt.Errorf("environment %q (%s) is the one this link reads from, and %s — "+
				"rename it in the dashboard, or read from another environment with `palbase link --from-env <name>`",
				e.Name, e.Ref, reason)
		}
		fmt.Fprintf(w, "skipped environment %q (%s): %s — rename it in the dashboard\n", e.Name, e.Ref, reason)
	}

	// One group per directory, in the listing's order.
	var groups [][]Environment
	for _, e := range named {
		placed := false
		for i := range groups {
			if envname.SameDirectory(groups[i][0].Name, e.Name) {
				groups[i] = append(groups[i], e)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []Environment{e})
		}
	}
	kept := make([]Environment, 0, len(named))
	for _, group := range groups {
		if len(group) == 1 {
			kept = append(kept, group[0])
			continue
		}
		readsFromIt := false
		for _, e := range group {
			readsFromIt = readsFromIt || e.Name == linkedEnv
		}
		if readsFromIt {
			return nil, sharedDirectoryRefusal(group, linkedEnv, "this link reads from")
		}
		fmt.Fprintf(w, "skipped environments %s: their names match when letter case and Unicode form are ignored, "+
			"so they would share one directory — rename one in the dashboard\n", twinLabels(group))
	}
	return kept, nil
}

// twinLabels names each environment of a group sharing one directory, by name
// and ref: "a" (ref1), "b" (ref2) and "c" (ref3).
func twinLabels(group []Environment) string {
	labels := make([]string, 0, len(group))
	for _, e := range group {
		labels = append(labels, fmt.Sprintf("%q (%s)", e.Name, e.Ref))
	}
	return strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
}

// sharedDirectoryRefusal refuses to act on named, one of a group of
// environments that would share one directory (FR-003). `link` and `palbase
// spec` say it in one sentence, so the two verbs cannot drift apart on it;
// role completes "… and named is the one …".
func sharedDirectoryRefusal(group []Environment, named, role string) error {
	return fmt.Errorf("environments %s match when letter case and Unicode form are ignored, so they would "+
		"share one directory, and %q is the one %s — rename one in the dashboard", twinLabels(group), named, role)
}

// whyNotWritable says why the environment called name must not get a directory
// in this checkout, or "" when it may. The link and `palbase spec` ask it the
// same way, so the two cannot disagree about which names are paths.
//
// thisMachine is true only when the environment IS the stack on this machine:
// that one, and only that one, is written to `local/`.
func whyNotWritable(name string, thisMachine bool) string {
	envRoot := path.Dir(EnvDir("any"))
	if err := envname.CheckDir(name); err != nil {
		return fmt.Sprintf("the name %v, so it cannot be a directory under %s", err, envRoot)
	}
	if !thisMachine && strings.EqualFold(name, localEnvName) {
		return fmt.Sprintf("%s belongs to the stack `palbase start` runs on this machine, never to a cloud environment",
			path.Join(envRoot, localEnvName))
	}
	return ""
}
