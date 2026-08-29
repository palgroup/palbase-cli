package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/hook"
	"github.com/palgroup/palbase-cli/internal/selection"
)

// runCombined is the production cmdRunner: it captures stderr too, because
// `docker compose version` reports a missing plugin THERE, not on stdout.
func runCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// lookupFunc and cmdRunner are the two pieces of the environment `dockerProbes`
// touches, injected so the probes are testable without a docker on the box.
type lookupFunc func(name string) (string, error)
type cmdRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// probeLine is one reported check: whether it passed, its short label, and a
// detail that must be ACTIONABLE — a failing line names the next command.
type probeLine struct {
	ok     bool
	label  string
	detail string
}

// dockerProbes answers "can `palbase start` work on this machine" before start
// tries and fails with docker's own raw error.
//
// THE THREE THINGS THAT ACTUALLY BROKE A START (measured 2026-08-29, customer run):
//  1. no docker at all;
//  2. docker present but no `compose` subcommand — `docker: unknown command:
//     docker compose`, which reads like a typo and is a missing plugin;
//  3. ~/.docker/config.json naming a credsStore whose helper binary is gone —
//     `docker-credential-desktop not found` after a move to Colima. The daemon is
//     healthy; the config points at a helper that left.
//
// `home` is a parameter rather than a call to os.UserHomeDir so a test can put a
// config.json where it wants one.
func dockerProbes(ctx context.Context, look lookupFunc, run cmdRunner, home string) []probeLine {
	docker, err := look("docker")
	if err != nil {
		return []probeLine{{
			label:  "docker",
			detail: "not found on PATH — `palbase start` runs the local stack in containers; install Docker or Colima",
		}}
	}
	out := []probeLine{{ok: true, label: "docker", detail: docker}}

	if v, cErr := run(ctx, docker, "compose", "version"); cErr != nil {
		out = append(out, probeLine{
			label:  "compose",
			detail: "`docker compose` is not available — the local stack is a compose project; install the Docker Compose plugin (v2)",
		})
	} else {
		out = append(out, probeLine{ok: true, label: "compose", detail: strings.TrimSpace(string(v))})
	}

	store := credsStore(home)
	switch {
	case store == "":
		out = append(out, probeLine{ok: true, label: "creds", detail: "no credsStore configured (fine — docker will use plain config)"})
	default:
		helper := "docker-credential-" + store
		if _, hErr := look(helper); hErr != nil {
			out = append(out, probeLine{
				label: "creds",
				detail: fmt.Sprintf(
					"~/.docker/config.json asks for credsStore %q but %s is not on PATH — install it, or remove the credsStore line",
					store, helper),
			})
		} else {
			out = append(out, probeLine{ok: true, label: "creds", detail: helper})
		}
	}
	return out
}

// credsStore reads the credential helper name out of ~/.docker/config.json.
// An absent or unreadable file means "no helper configured", which is a valid
// setup rather than a failure.
func credsStore(home string) string {
	raw, err := os.ReadFile(filepath.Join(home, ".docker", "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CredsStore string `json:"credsStore"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	return strings.TrimSpace(cfg.CredsStore)
}

// toolchainProbes answers "can `palbase build` and `palbase push` run here".
//
// TWO ENGINES, NOT ONE, and only one of them used to be reported. `palbase
// build` drives Node (devjs/*.js), but `palbase push` bundles the backend with
// BUN — stack_bundle.go:59 refuses with "bun is not installed, and it is what
// builds a backend for this runtime", and bunVersion() (stack_bundle.go:977)
// stamps the artifact with it. A push on a machine without bun therefore died
// on bun's absence while `doctor` — the command whose whole job is "why is the
// CLI not working for me" — printed a clean bill of health and never said the
// word. Measured 2026-08-29, customer run.
func toolchainProbes(ctx context.Context, look lookupFunc, run cmdRunner) []probeLine {
	var out []probeLine
	if node, err := look("node"); err != nil {
		out = append(out, probeLine{
			label:  "node",
			detail: "not found on PATH — `palbase build` and `palbase db types` need Node.js",
		})
	} else {
		v, _ := run(ctx, node, "--version")
		out = append(out, probeLine{ok: true, label: "node", detail: fmt.Sprintf("%s (%s)", strings.TrimSpace(string(v)), node)})
	}
	if bun, err := look("bun"); err != nil {
		out = append(out, probeLine{
			label: "bun",
			detail: "not found on PATH — `palbase push` bundles the backend with Bun (the stack RUNS Bun, " +
				"so the bundle is built by the engine that will run it); install it from https://bun.sh " +
				"(`curl -fsSL https://bun.sh/install | bash`, or `brew install oven-sh/bun/bun`)",
		})
	} else {
		v, _ := run(ctx, bun, "--version")
		out = append(out, probeLine{ok: true, label: "bun", detail: fmt.Sprintf("%s (%s)", strings.TrimSpace(string(v)), bun)})
	}
	return out
}

// doctorCmd is the environment triage verb: one command that answers "why is
// the CLI not working for me" — endpoints, login state, headless PAT,
// project link, and the two JS engines the CLI drives: Node (`build`, `db
// types`) and Bun (`push`'s bundler). Informative
// only (always exit 0): doctor diagnoses, the failing command still owns its
// error.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the CLI environment (cloud, login, link, Docker, Node)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ok := func(label, detail string) { fmt.Fprintf(out, "  ✓ %-10s %s\n", label, detail) }
			bad := func(label, detail string) { fmt.Fprintf(out, "  ✗ %-10s %s\n", label, detail) }

			fmt.Fprintf(out, "palbase %s\n", Version)
			ok("cloud", fmt.Sprintf("studio %s, api %s, projects <ref>.%s",
				resolved.Endpoints.Studio, resolved.Endpoints.PlatformAPI, resolved.Endpoints.PublicHost))

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if _, err := authClient.GetValidToken(ctx); err != nil {
				bad("login", fmt.Sprintf("not logged in (%v) — run `palbase login`", err))
			} else {
				ok("login", "session token valid")
			}

			if os.Getenv("PALBASE_ACCESS_TOKEN") != "" {
				ok("pat", "PALBASE_ACCESS_TOKEN set (headless credential)")
			} else {
				ok("pat", "not set (fine for interactive use; CI needs a Dashboard-issued PAT)")
			}

			// The link this CLI acts on is a TARGET: a stack address in
			// .palbase/project.json, written by `palbase link` (or by
			// `palbase start` for a stack on this machine). Reporting the v1
			// project/environment selection instead would send a person to
			// `palbase project use`, a verb that no longer exists — the v2
			// cloud has one project per tenant and one address per project.
			if target, terr := backend.ReadTarget(); terr == nil {
				ok("link", target.Describe())
			} else {
				ok("link", "this directory is not linked — run `palbase link <url>` here")
			}

			// pre-push deploy-validation hook (report-only). Meaningful only in a
			// github-mode checkout; elsewhere there's nothing to wire.
			if cwd, cwdErr := os.Getwd(); cwdErr == nil {
				switch state, detail := hook.Status(cwd); state {
				case "wired-v2":
					ok("hook", fmt.Sprintf("hooks/pre-push v2 wired (%s)", detail))
				case "outdated":
					ok("hook", fmt.Sprintf("hooks/pre-push %s", detail))
				case "foreign":
					ok("hook", fmt.Sprintf("hooks/pre-push %s", detail))
				default: // missing
					bad("hook", "hooks/pre-push missing — run 'palbase push' or 'palbase clone' once to install it")
				}
			}

			// Docker prerequisites, before Node: `palbase start` needs these and
			// used to discover each one by failing with docker's own raw error.
			home, _ := os.UserHomeDir()
			for _, l := range dockerProbes(ctx, exec.LookPath, runCombined, home) {
				if l.ok {
					ok(l.label, l.detail)
				} else {
					bad(l.label, l.detail)
				}
			}

			for _, l := range toolchainProbes(ctx, exec.LookPath, runCombined) {
				if l.ok {
					ok(l.label, l.detail)
				} else {
					bad(l.label, l.detail)
				}
			}
			return nil
		},
	}
}

// openCmd opens Studio in the browser at the CANONICAL page for the selected
// Project/Environment (`/projects/{id}/environments/{ref}`) — not the bare
// dashboard root. `palbase open` from a production vs a staging context must
// land on that Environment's page (UAT CLI-011), so it resolves the local
// selection and deep-links. Outside a linked directory (nothing selected) it
// falls back to the Studio root, so it still works as a plain "open the UI".
func openCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open",
		Short: "Open the selected Project/Environment in Studio (falls back to the dashboard root)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u := studioSelectionURL(cmd.Context(), resolved.Endpoints.Studio, sel)
			fmt.Fprintf(cmd.OutOrStdout(), "Opening %s …\n", u)
			return auth.OpenURL(u)
		},
	}
}

// studioSelectionURL resolves the local selection and returns its canonical
// Studio deep-link. A resolution failure (not linked, no --project) is NOT an
// error for `open` — canonicalStudioURL then returns the Studio root so the
// escape hatch still opens the UI.
func studioSelectionURL(ctx context.Context, studioRoot string, r *selection.Resolver) string {
	var projectID, ref string
	if r != nil {
		if s, err := r.Resolve(ctx); err == nil {
			projectID, ref = s.ProjectID, s.EnvironmentRef()
		}
	}
	return canonicalStudioURL(studioRoot, projectID, ref)
}

// canonicalStudioURL builds <studio>/projects/{projectId}/environments/{ref} —
// the same deep-link Studio itself uses. With no selected Project/Environment
// it returns the Studio root (the plain "open the dashboard" fallback). Pure,
// so the deep-link shape is locked by a unit test.
func canonicalStudioURL(studioRoot, projectID, ref string) string {
	root := strings.TrimRight(studioRoot, "/")
	if projectID == "" || ref == "" {
		return root
	}
	return fmt.Sprintf("%s/projects/%s/environments/%s", root, projectID, ref)
}
