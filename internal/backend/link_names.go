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
// Two rules, in this order:
//
//   - a name that is not one directory (envname.CheckDir) never becomes a path;
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
	envRoot := path.Dir(EnvDir("any"))
	named := make([]Environment, 0, len(envs))
	for _, e := range envs {
		err := envname.CheckDir(e.Name)
		if err == nil {
			named = append(named, e)
			continue
		}
		reason := fmt.Sprintf("the name %v, so it cannot be a directory under %s", err, envRoot)
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
		labels := make([]string, 0, len(group))
		readsFromIt := false
		for _, e := range group {
			labels = append(labels, fmt.Sprintf("%q (%s)", e.Name, e.Ref))
			readsFromIt = readsFromIt || e.Name == linkedEnv
		}
		all := strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
		if readsFromIt {
			return nil, fmt.Errorf("environments %s match when letter case and Unicode form are ignored, so they would "+
				"share one directory, and %q is the one this link reads from — rename one in the dashboard", all, linkedEnv)
		}
		fmt.Fprintf(w, "skipped environments %s: their names match when letter case and Unicode form are ignored, "+
			"so they would share one directory — rename one in the dashboard\n", all)
	}
	return kept, nil
}
