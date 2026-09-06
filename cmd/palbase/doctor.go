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
	switch store {
	case "":
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
			detail: "not found on PATH — `palbase build` needs Node.js",
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
// project link, and the two JS engines the CLI drives: Node (`build`)
// and Bun (`push`'s bundler). Informative
// only (always exit 0): doctor diagnoses, the failing command still owns its
// error.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Show cloud addresses and diagnose login, link, Docker, Node and Bun",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ok := func(label, detail string) { fmt.Fprintf(out, "  ✓ %-10s %s\n", label, detail) }
			bad := func(label, detail string) { fmt.Fprintf(out, "  ✗ %-10s %s\n", label, detail) }

			fmt.Fprintf(out, "palbase %s\n", Version)
			ok("cloud", fmt.Sprintf("studio %s, auth %s, api %s, projects <ref>.%s",
				resolved.Endpoints.Studio, resolved.Endpoints.Auth,
				resolved.Endpoints.PlatformAPI, resolved.Endpoints.PublicHost))

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

// openCmd opens the configured Studio dashboard.
func openCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open",
		Short: "Open Studio in your browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u := strings.TrimRight(resolved.Endpoints.Studio, "/")
			fmt.Fprintf(cmd.OutOrStdout(), "Opening %s …\n", u)
			return auth.OpenURL(u)
		},
	}
}
