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

	"github.com/palgroup/palbase-cli/internal/envname"
)

// linkableEnvironments is the listing without the environments this link must
// not write, with a line for each one it left out.
//
// LEAVING ONE OUT IS SAID, AND THE LINK GOES ON (FR-001). Leaving out the
// environment this link READS FROM cannot be: it is what the app builds against
// when nothing else is chosen, so the link refuses and names the fix.
func linkableEnvironments(envs []Environment, linkedEnv string, w io.Writer) ([]Environment, error) {
	envRoot := path.Dir(EnvDir("any"))
	kept := make([]Environment, 0, len(envs))
	for _, e := range envs {
		err := envname.CheckDir(e.Name)
		if err == nil {
			kept = append(kept, e)
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
	return kept, nil
}
