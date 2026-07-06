// Package backend provides the top-level backend lifecycle commands
// (serve / list / rollback / status / types / mobile). palbase IS the
// backend CLI — there is no `backend` parent command. These cover the
// local dev + observation loop for the per-project backend-runtime pod.
//
// Deploy is GitHub-native: code flows via `git push` to the project's
// GitHub repo → webhook → orchestrator deploys (and applies the
// config-as-code committed in the repo). The CLI no longer pushes/pulls
// a tar bundle.
//
// All remote calls go through Studio's tRPC layer via the studio
// package — never directly to br-<ref> — so project-membership + the
// backend_enabled gate are enforced server-side.
package backend

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// defaultHTTPClient is reused by `palbase web gen` (and any
// future direct HTTP we add). 30s read timeout matches the SDK side
// so a slow Kong response surfaces consistently.
var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

func newJSONRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// devServerFS embeds the local Node.js dev server. Shipped beside the
// CLI binary so `palbase serve` works without an internet round
// trip; copied to a temp dir at runtime so Node can resolve relative
// requires the way it would inside a real package.
//
// module-clients.js is dev-server.js's sibling require — it carries
// the module-client fetch wrappers (Documents/Storage/… singletons; a
// lockstep mirror of the backend-runtime image's
// internal/runtime/module-clients.js). Both files must land in the temp
// dir so the relative resolve works.
//
//go:embed devjs/dev-server.js devjs/module-clients.js devjs/env-gen.js devjs/return_types.js devjs/throw_analysis.js devjs/extract_meta.js
var devServerFS embed.FS

// REST is the subset of the Management-API transport the mode-aware deploy
// verbs (push/pull/clone) use: PostMultipart for the platform-mode tarball
// deploy and Do for the project lookup. *transport.Client satisfies it; tests
// substitute a stub. Kept as a narrow interface (mirroring project.REST) so the
// backend package doesn't depend on internal/transport and stays testable.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
	PostMultipart(path string, tarball []byte, fields map[string]string) ([]byte, error)
}

// Resolvers returns lazy accessors for the shared CLI globals, so the
// `backend` command tree can be wired into cobra at startup without
// the auth + studio clients having been initialised yet (cobra's
// PersistentPreRunE on the root command is what populates them).
type Resolvers struct {
	Auth      func() *auth.Client
	Studio    func() *studio.Client
	Endpoints func() config.Endpoints
	// REST returns the authed Management-API client used by push/pull/clone.
	// Lazy (a func) like the other accessors, and only CALLED at RunE time —
	// constructing the command tree with a zero-value Resolvers must not panic.
	REST func() REST
}

// Commands returns the flat, top-level command set the root mounts
// directly — there is no `backend` parent anymore (palbase IS the
// backend CLI). Subcommands call the resolvers at action time, after
// PersistentPreRunE has finished.
//
// There is no `init`/`enable`/`disable`: a project IS a backend from
// creation (backend is the default), so the CLI never enables, checks, or
// tears down the backend — it assumes the linked project is ready. The
// server-side gating is owned by the platform, not the CLI.
//
// push/pull/clone are mode-aware deploy verbs: for a github-mode project they
// shell out to git (push/pull/clone → webhook → orchestrator deploys); for a
// platform-mode project they upload/fetch a tarball bundle via the Management
// API. `merge` stays retired (the old go-git merge verb is gone). Alongside
// them the CLI keeps local dev (`serve`), the observation/control verbs
// (deploys, rollback, status) and the artifact fetcher (`spec`) — client
// codegen itself is the SDKs' job, not the CLI's.
func Commands(r Resolvers) []*cobra.Command {
	return []*cobra.Command{
		newWebCmd(r),
		newIOSCmd(r),
		newDevCmd(r),
		newBuildCmd(r),
		newDeploysCmd(r),
		newRollbackCmd(r),
		newStatusCmd(r),
		newSpecCmd(r),
		newCloneCmd(r),
		newPullCmd(r),
		newPushCmd(r),
	}
}

// EnvTypesCmd exposes the palbase-env.d.ts generator (db/schema.ts → typed
// Database.tables.*) so main can register it under `palbase db types` — it
// types the author's OWN handlers from the local schema source, which is db
// tooling, not client codegen. (Client codegen is the SDKs' job: the CLI only
// fetches the artifacts — see `palbase spec`.)
func EnvTypesCmd() *cobra.Command {
	return newGenTypesCmd()
}

// newGenTypesCmd is the standalone regeneration of palbase-env.d.ts from the
// project's db/schema.ts — the same step `palbase serve` runs on startup,
// exposed on its own so it can run from a build/CI step or after editing the
// schema without booting the dev server. It types the project's OWN handlers
// (`Database.tables.*`) from the local schema source. No project link, no
// network — purely local. (NOT client codegen — that is the SDKs' job.)
func newGenTypesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "types",
		Short: "Generate palbase-env.d.ts from db/schema.ts (typed Database.tables.*)",
		Long: `Generate the project's palbase-env.d.ts from its db/schema.ts so handlers
get a typed Database.tables.* with no import and no generic.

esbuild-bundles db/schema.ts (with @palbase/* external), evaluates the
defineSchema() result, and writes palbase-env.d.ts to the project root.
Requires Node.js + npx. ` + "`palbase serve`" + ` runs this automatically on startup;
run it standalone after editing db/schema.ts or from a build step.

No-op when the project has no db/schema.ts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			schemaPath := filepath.Join(cwd, "db", "schema.ts")
			if _, err := os.Stat(schemaPath); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					fmt.Fprintln(os.Stdout, "no db/schema.ts — nothing to generate")
					return nil
				}
				return err
			}
			if err := generateEnvTypes(cmd.Context(), cwd); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "✓ wrote %s\n", filepath.Join(cwd, "palbase-env.d.ts"))
			return nil
		},
	}
}

// backendTarget is the resolved (URL + publishable key) for a project's
// backend at a given branch. Used by lookupBackendTarget (which `palbase spec`
// and `palbase web link`'s artifact fetch share) to address the deployed
// tenant host.
type backendTarget struct {
	URL    string
	APIKey string
}

// ── helpers ─────────────────────────────────────────────────────────────

// writeStringFlag is a tiny helper so subcommands can compose without
// rewriting boilerplate.
type stringFlag struct {
	value string
}

func (s *stringFlag) String() string     { return s.value }
func (s *stringFlag) Set(v string) error { s.value = v; return nil }
func (s *stringFlag) Type() string       { return "string" }

// resolveOrLinkRef wraps auth.ResolveProjectRef with an interactive picker
// that reads project.list, prompts the user when there's >1, and writes the
// chosen ref to .palbase/config.json so subsequent runs are silent.
//
// The picker fires only when stdin is a TTY — non-interactive callers
// (CI, piped scripts) get the original auth.ErrNotLinked back so they can
// fail loudly instead of hanging waiting for input.
func resolveOrLinkRef(ctx context.Context, override string, c *studio.Client, out io.Writer) (string, error) {
	ref, err := auth.ResolveProjectRef(override)
	if err == nil {
		return ref, nil
	}
	if !errors.Is(err, auth.ErrNotLinked) {
		return "", err
	}

	picked, err := pickProject(ctx, override, c, out)
	if err != nil {
		return "", err
	}

	// Default to the project's main branch: a fresh link should pull/serve
	// against main, not staging. --branch selects another branch per command.
	if err := auth.SaveProjectConfig(&auth.ProjectConfig{Ref: picked.Ref, DefaultEnv: "main"}); err != nil {
		return "", fmt.Errorf("save .palbase/config.json: %w", err)
	}
	fmt.Fprintf(out, "✓ Linked to %s (%s)\n", picked.Name, picked.Ref)
	return picked.Ref, nil
}

// pickProject resolves which project the caller should act on when the cwd
// isn't linked: it lists the user's projects and selects one. With a
// non-empty override it matches by ref (CI/scripted path); otherwise it
// auto-picks the only project or prompts interactively. It does NOT write
// any config — the caller persists the link in the cwd.
func pickProject(ctx context.Context, override string, c *studio.Client, out io.Writer) (*auth.Project, error) {
	if override == "" && !isInteractive() {
		return nil, fmt.Errorf("project not linked — pass --ref to select a project in a non-interactive shell")
	}

	var rows []auth.Project
	if listErr := c.Query(ctx, "project.list", nil, &rows); listErr != nil {
		return nil, fmt.Errorf("auto-link: %w", listErr)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no projects in your account — create one at the Palbase Studio dashboard first")
	}

	// --ref override: match by ref, no prompt (works under CI / piped input).
	if override != "" {
		for i := range rows {
			if rows[i].Ref == override {
				return &rows[i], nil
			}
		}
		return nil, fmt.Errorf("no project with ref %q found in your account", override)
	}

	if len(rows) == 1 {
		// Mode-neutral wording: pickProject feeds both the in-place link
		// ("✓ Linked to …" follows) and clone-mode ("Cloning … into …/"),
		// so this line must read sensibly before either.
		fmt.Fprintf(out, "Using your only project: %s (%s)\n", rows[0].Name, rows[0].Ref)
		return &rows[0], nil
	}

	fmt.Fprintln(out, "Select a project:")
	for i, p := range rows {
		fmt.Fprintf(out, "  %d) %s (%s)\n", i+1, p.Name, p.Ref)
	}
	fmt.Fprint(out, "Enter number: ")
	var choice int
	if _, scanErr := fmt.Fscan(os.Stdin, &choice); scanErr != nil {
		return nil, fmt.Errorf("invalid selection: %w", scanErr)
	}
	if choice < 1 || choice > len(rows) {
		return nil, fmt.Errorf("invalid selection: %d", choice)
	}
	return &rows[choice-1], nil
}

// isInteractive returns true when stdin is a TTY. Used by
// resolveOrLinkRef to gate the picker — running under CI / piped input
// shouldn't block waiting for `Enter number:`.
func isInteractive() bool {
	// term.IsTerminal, not a ModeCharDevice check: /dev/null IS a char
	// device, so `palbase ios link </dev/null` used to open the interactive
	// picker and die on EOF instead of returning the actionable
	// "pass --group" error (found by the live non-TTY probe).
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// extractFS unpacks an embed.FS subtree into target on disk.
func extractFS(src embed.FS, root, target string) error {
	return fs.WalkDir(src, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(target, 0o755)
		}
		out := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		body, err := src.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, body, 0o644)
	})
}

// ── subcommands ─────────────────────────────────────────────────────────

// backendDepMissing reports whether the project's @palbase/backend is absent
// from node_modules. serve bundles controllers/ with @palbase/backend external,
// resolving it from node_modules at require() time, so when it's missing every
// controller fails to load and serve must install deps first. A present dir is
// trusted — a present-but-broken install surfaces via the bundler's own error.
func backendDepMissing(projectDir string) bool {
	_, err := os.Stat(filepath.Join(projectDir, "node_modules", "@palbase", "backend"))
	return os.IsNotExist(err)
}

// installNodeDeps runs `npm install` in dir so a freshly-cloned project ships
// with @palbase/backend (and any other declared deps) on disk before serve
// bundles controllers/. Honours an existing package-lock.json, prefers `npm`
// because that's the only manager the template targets.
func installNodeDeps(dir string) error {
	bin, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("npm not found in PATH — install Node.js (https://nodejs.org) and re-run")
	}
	fmt.Println("→ installing dependencies (npm install) ...")
	cmd := exec.Command(bin, "install", "--silent", "--no-audit", "--no-fund")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// devServerToolMissing reports whether `pkg` is absent from the project's
// node_modules. Used for the dev-server's OWN runtime tools (not the user's
// declared deps) that the deployed br-pod provides globally (Dockerfile) but a
// local `palbase serve` must supply itself.
func devServerToolMissing(projectDir, pkg string) bool {
	_, err := os.Stat(filepath.Join(projectDir, "node_modules", pkg))
	return os.IsNotExist(err)
}

// ensureDevServerTools guarantees the runtime packages the dev-server require()s
// to behave like the deployed runtime are present in node_modules, WITHOUT
// writing them into the user's package.json (--no-save) — they're the runtime's
// dependencies, not the project's. Today that's zod-to-json-schema: the deployed
// br-pod installs it globally (modules/backend/Dockerfile), but a local serve
// resolves it from the project's node_modules, so when it's missing
// /openapi.json silently omits every request/response schema and the typed pb.*
// client comes out bodyless (Penny #2's local root cause). Best-effort: a failed
// install only means OpenAPI schemas stay absent — the dev-server's own hint
// fires — so we warn but never block serve.
func ensureDevServerTools(dir string) {
	const pkg = "zod-to-json-schema"
	if !devServerToolMissing(dir, pkg) {
		return
	}
	bin, err := exec.LookPath("npm")
	if err != nil {
		return // installNodeDeps already surfaced the npm-missing error path
	}
	fmt.Printf("→ installing %s (dev-server tool, --no-save) ...\n", pkg)
	cmd := exec.Command(bin, "install", "--no-save", "--silent", "--no-audit", "--no-fund", pkg)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  warning: could not install %s — /openapi.json will omit request/response schemas (run `npm i %s` manually)\n", pkg, pkg)
	}
}

// resolveDevProjectRef picks the ref the dev server should build its
// <ref>.<host> URL from. Kong only routes the branch endpoint_ref
// subdomain, so prefer the endpoint_ref apikey.reveal returns. When
// reveal was skipped (ref "" / "local") or failed, endpointRef is empty
// and we fall back to the bare ref — dev still launches and the module
// clients (ctx.docs/…) surface a clear downstream error rather than
// hard-failing here.
func resolveDevProjectRef(ref, endpointRef string) string {
	if endpointRef != "" {
		return endpointRef
	}
	return ref
}

// freeDevPort best-effort frees `port` before starting the dev server, so a
// re-run after a crashed/orphaned `palbase serve` doesn't bounce off
// EADDRINUSE. The chosen design is the single fixed port (4003) + this
// free-on-start — no marker file.
//
// Safety guards (we'd rather let the bind fail than kill the wrong process):
//   - Only darwin/linux (lsof). Other OSes: skip silently, let node surface
//     EADDRINUSE.
//   - If lsof is absent: skip silently.
//   - Only SIGTERM (never SIGKILL) PIDs whose `ps -o comm=` is exactly "node"
//     — i.e. a previous dev-server, not an unrelated listener. For anything
//     else, print a clear warning and DON'T kill it.
//   - Never signal PID <= 0 and never our own PID.
//
// `w` receives the human-readable log lines (what was freed / what was left).
func freeDevPort(port int, w io.Writer) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return // no lsof → let node's EADDRINUSE surface
	}
	out, err := exec.Command(lsof, "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		// Non-zero exit just means "no listener on this port" — nothing to free.
		return
	}
	self := os.Getpid()
	for _, line := range strings.Fields(string(out)) {
		pid, perr := strconv.Atoi(strings.TrimSpace(line))
		if perr != nil || pid <= 0 || pid == self {
			continue
		}
		comm := strings.TrimSpace(processComm(pid))
		if comm != "node" {
			fmt.Fprintf(w, "warning: port %d is held by pid %d (%s) — not a dev-server; "+
				"not killing it (use --port to pick another port)\n",
				port, pid, comm)
			continue
		}
		if err := terminatePID(pid); err != nil {
			fmt.Fprintf(w, "warning: could not free port %d (pid %d): %v\n", port, pid, err)
			continue
		}
		fmt.Fprintf(w, "freed port %d (stopped stale dev-server pid %d)\n", port, pid)
	}
}

// serveTempPrefix is the os.MkdirTemp prefix for the per-serve dev-server
// extract dir (holds the copied dev-server.js + the 0600 owner-token file).
// Sweeping by THIS exact prefix is how a crashed previous serve's leftovers get
// reclaimed — including the lingering owner-token, since it lives inside the dir.
const serveTempPrefix = "palbase-dev-"

// staleServeTempAge is how old a leftover serve temp dir must be before the
// next serve start reclaims it. A small grace window so we never race a serve
// that's launching concurrently in another shell (its fresh dir is younger than
// this and is kept). The current run's own dir is created AFTER this sweep, so
// it's never a candidate.
const staleServeTempAge = 1 * time.Minute

// sweepStaleServeTempDirs removes serve temp dirs left behind by a previous
// crashed/SIGKILLed run. The graceful-exit paths (signal handler + the
// dev-server's process-exit hook) clean up normally; this reclaims the dirs a
// HARD crash couldn't. It only ever touches entries whose name starts with our
// exact serveTempPrefix AND that are older than `olderThan` — never anything
// else in the shared OS temp dir. Pure + injectable (tempRoot, prefix, now) so
// it's unit-tested without depending on the real os.TempDir or wall clock.
// Returns the directories it removed (for logging/testing). Best-effort: an
// unreadable temp root or a dir we can't stat/remove is skipped, never fatal.
func sweepStaleServeTempDirs(tempRoot, prefix string, now time.Time, olderThan time.Duration) []string {
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue // not ours — leave it strictly alone
		}
		info, err := e.Info()
		if err != nil {
			continue // can't tell its age → don't risk removing a live dir
		}
		if now.Sub(info.ModTime()) < olderThan {
			continue // too fresh — could be a serve launching right now
		}
		full := filepath.Join(tempRoot, e.Name())
		if err := os.RemoveAll(full); err != nil {
			continue // best-effort
		}
		removed = append(removed, full)
	}
	return removed
}

// processComm returns the executable name of a pid via `ps -p <pid> -o comm=`.
// Empty string when the process is gone or ps fails — callers treat a
// non-"node" comm (including "") as "don't touch it".
func processComm(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	// `comm` may be a full path (Linux ps prints the basename, macOS the path);
	// take the basename so "/usr/local/bin/node" → "node".
	return filepath.Base(strings.TrimSpace(string(out)))
}

func newDevCmd(r Resolvers) *cobra.Command {
	var port int
	var branchFlag string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run controllers/ locally with hot reload",
		Long: `Serve the project's controllers/ from a local Node.js dev server with
hot reload — the local equivalent of the deployed backend-runtime pod.
Routes (controller basePath + route.path), the per-request req, the imported
singleton services, and resources behave identically to production, so what
runs under ` + "`palbase serve`" + ` runs the same once you ` + "`git push`" + ` it to deploy.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			controllersDir := filepath.Join(cwd, "controllers")
			if _, err := os.Stat(controllersDir); err != nil {
				return fmt.Errorf("no controllers/ directory in cwd — run from your project root (clone it with `git clone`)")
			}

			// Controllers import @palbase/backend (Controller/Get/Body decorators)
			// and the bundler keeps it external, require()ing it from the project's
			// node_modules at load time. The new flow is `git clone` + `palbase
			// serve` — no scaffold step installs deps anymore — so a fresh clone has
			// no node_modules and every controller would fail to load (silently
			// skipped → "registered 0 route(s)"). Install once when @palbase/backend
			// is absent so a clean clone serves in one step.
			if backendDepMissing(cwd) {
				if err := installNodeDeps(cwd); err != nil {
					return fmt.Errorf("@palbase/backend is not installed and `npm install` failed: %w\nfix the error above (or run `npm install` manually) and re-run `palbase serve`", err)
				}
			}

			// The dev-server require()s zod-to-json-schema to emit OpenAPI
			// request/response schemas (the deployed br-pod installs it globally;
			// a local serve must supply it). Ensure it's present so /openapi.json
			// is populated and the typed pb.* client isn't bodyless — without
			// adding it to the user's package.json.
			ensureDevServerTools(cwd)

			// serve deliberately runs the LOCAL SDK, so a major behind the
			// deploy runtime PASSES here but FAILS on deploy (the centauri
			// direction). Warn — never block — so `palbase serve` no longer
			// silently promises deploy parity it can't keep.
			warnBackendSkew(cmd.Context(), cwd, os.Stderr)

			// Regenerate palbase-env.d.ts from db/schema.ts so the project's
			// handlers get a typed `Database.tables.*`. No-op when the project
			// has no db/schema.ts. Best-effort: a generation failure must not
			// block local dev, so we warn and continue (the dev-server runs the
			// handlers regardless; only authoring-time types are affected).
			if err := generateEnvTypes(cmd.Context(), cwd); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not regenerate palbase-env.d.ts (%v)\n", err)
			}

			// Reclaim serve temp dirs leaked by a previous HARD crash (SIGKILL /
			// power loss) before we create this run's dir. Graceful exits clean up
			// themselves (signal handler + the dev-server's process-exit hook); this
			// catches only what a crash couldn't. Scoped to our exact prefix + an age
			// grace so a serve launching concurrently in another shell is untouched.
			for _, stale := range sweepStaleServeTempDirs(os.TempDir(), serveTempPrefix, time.Now(), staleServeTempAge) {
				fmt.Fprintf(os.Stderr, "cleaned up stale serve temp dir from a previous run: %s\n", stale)
			}

			tmpDir, err := os.MkdirTemp("", serveTempPrefix+"*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmpDir)
			if err := extractFS(devServerFS, "devjs", tmpDir); err != nil {
				return fmt.Errorf("extract dev server: %w", err)
			}

			ref, _ := auth.ResolveProjectRef("") // best-effort; default ref to "local" inside the JS

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			// Preflight: serve runs your controllers LOCALLY but proxies
			// Database and ctx.* (docs/storage/…) to the DEPLOYED branch. So a
			// branch that isn't a live, active deployment can't back local dev
			// — fail fast with an actionable message ("push first" / "wake it")
			// instead of an opaque reveal warning + a half-working server.
			branchName := devBranchValue(branchFlag) // "main" or the active/flag branch
			if ref != "" && ref != "local" {
				if err := preflightServeBranch(ctx, r.Studio(), ref, branchName); err != nil {
					return err
				}
			}
			// Migration awareness: because serve uses the deployed branch DB,
			// local db/schema.ts or db/migrations/ changes that aren't pushed
			// won't be reflected. Warn (never block) so the gap is obvious.
			warnUndeployedSchema(cwd, branchName, os.Stderr)

			// Reveal the project's publishable key so dev-server can wire its
			// inline module clients (module-clients.js) + the Database edge for the
			// anon/authenticated RLS path.
			var revealResp struct {
				EndpointRef    string `json:"endpointRef"`
				PublishableKey string `json:"publishableKey"`
			}
			if ref != "" && ref != "local" {
				// Thread the active branch so reveal returns THIS branch's
				// endpoint_ref (e.g. test0r8q3p1) — otherwise serve's module
				// clients (ctx.docs/storage/…) route to the default branch.
				revealPayload := map[string]any{"ref": ref}
				if b := resolveActiveBranch(branchFlag); b != "" {
					revealPayload["branch"] = b
				}
				if err := r.Studio().Query(ctx, "apikey.reveal", revealPayload, &revealResp); err != nil {
					fmt.Fprintf(os.Stderr, "warning: apikey.reveal failed (%v) — ctx.docs/ctx.storage/… will be unavailable\n", err)
				}
			}

			// KEYLESS asService(): we do NOT pull the service-role key to the laptop
			// (a leaked/committed BYPASSRLS key is a disaster). Instead serve forwards
			// the OWNER's normal palauth session token to the Database edge; the edge
			// verifies project ownership against control-pg (via Studio) and grants
			// service_role server-side as a tx-local SET LOCAL ROLE — no service-role
			// credential ever reaches the laptop. ownerToken is the same login session
			// serve already uses to call Studio; it's a short-lived, refreshable,
			// revocable USER session, NOT a service-role credential.
			ownerToken, _ := r.Auth().GetValidToken(ctx) // best-effort; empty → asService degrades

			node := exec.CommandContext(ctx, "node", filepath.Join(tmpDir, "dev-server.js"))
			// dev-server.js runs from a temp dir but needs to require()
			// the user's local @palbase/backend (and any other declared
			// deps). NODE_PATH adds the project's node_modules to the
			// resolver path, and setting Dir keeps `process.cwd()` and
			// any relative paths from user endpoints anchored to the
			// project root.
			node.Dir = cwd
			node.Env = append(os.Environ(),
				fmt.Sprintf("PALBASE_DEV_PORT=%d", port),
				fmt.Sprintf("PALBASE_DEV_ROOT=%s", cwd),
				// PROJECT_REF feeds dev-server.js's https://<ref>.<host> URL,
				// which goes through Kong — and Kong routes the branch
				// endpoint_ref subdomain (e.g. test0r8q3m), NOT the bare ref.
				// apikey.reveal returns the resolved endpoint_ref; prefer it,
				// falling back to the bare ref when reveal was skipped/failed
				// so dev still launches (ctx.docs/… then errors clearly).
				fmt.Sprintf("PALBASE_PROJECT_REF=%s", resolveDevProjectRef(ref, revealResp.EndpointRef)),
				fmt.Sprintf("PALBASE_PUBLIC_HOST=%s", r.Endpoints().PublicHost),
				fmt.Sprintf("PALBASE_TENANT_APIKEY=%s", revealResp.PublishableKey),
				// PALBASE_BRANCH gives dev-server the active branch (--branch
				// wins, else ProjectConfig.DefaultEnv from `palbase branch
				// switch`). resolveActiveBranch returns "" for main; we surface
				// "main" explicitly so the value is always present for the
				// dev-server to read (local only — no Kong/server round-trip).
				fmt.Sprintf("PALBASE_BRANCH=%s", devBranchValue(branchFlag)),
				fmt.Sprintf("NODE_PATH=%s", filepath.Join(cwd, "node_modules")),
			)
			// The owner's palauth session token — enables local asService() KEYLESSLY:
			// dev-server.js forwards it to the Database edge, which verifies project
			// ownership (control-pg, via Studio) and grants service_role server-side.
			// NOT a service-role credential — a short-lived, refreshable USER session.
			// Written to a FILE (not a static env) and refreshed periodically while
			// serve runs, because a session token expires (~30m) — a static env
			// would silently break asService() mid-session. dev-server.js reads the
			// file fresh on each asService() call. Absent file → asService() degrades.
			ownerTokenFile := filepath.Join(tmpDir, "owner-token")
			if ownerToken != "" {
				if werr := os.WriteFile(ownerTokenFile, []byte(ownerToken), 0o600); werr == nil {
					node.Env = append(node.Env, fmt.Sprintf("PALBASE_OWNER_TOKEN_FILE=%s", ownerTokenFile))
				}
			}
			node.Stdout = os.Stdout
			node.Stderr = os.Stderr
			// Best-effort: free the port if a stale dev-server is still holding
			// it, so a re-run doesn't bounce off EADDRINUSE. Only ever stops a
			// process whose comm is "node" (a previous dev-server); anything
			// else is left alone with a warning. Safe to call even when nothing
			// is listening.
			freeDevPort(port, os.Stderr)
			if err := node.Start(); err != nil {
				return fmt.Errorf("start node: %w (is Node.js installed?)", err)
			}
			// Keep the owner-token file fresh while serve runs so asService()
			// (BYPASSRLS) doesn't silently break when the session token expires.
			// We REFRESH-AHEAD: GetFreshToken refreshes whenever the token has
			// less than 5 minutes of life left (not only once it's already
			// expired, as GetValidToken does), so the file we write always holds
			// a token with several minutes of margin. Combined with the 30s tick
			// this closes the stale window entirely — a tick can no longer
			// rewrite a token that's seconds from expiry. Stops when the context
			// is cancelled (Ctrl+C) or node exits.
			const ownerTokenMinRemaining = 5 * time.Minute
			if ownerToken != "" {
				go func() {
					t := time.NewTicker(30 * time.Second)
					defer t.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-t.C:
							if tok, err := r.Auth().GetFreshToken(ctx, ownerTokenMinRemaining); err == nil && tok != "" {
								_ = os.WriteFile(ownerTokenFile, []byte(tok), 0o600)
							}
						}
					}
				}()
			}
			waitErr := node.Wait()
			// Translate the typical SIGINT exit so the user doesn't see a
			// scary stack trace when they Ctrl+C.
			if waitErr != nil {
				if exit, ok := waitErr.(*exec.ExitError); ok {
					if exit.ExitCode() == 130 || exit.ExitCode() == -1 {
						return nil
					}
				}
				return fmt.Errorf("dev server exited: %w", waitErr)
			}
			return nil
		},
	}
	// 4003 is the single canonical local port: the codegen consumers
	// (`palbase web gen` local/auto probe, the SPM plugin's spec-fetch local
	// probe) all hit localhost:4003 for the local /openapi.json, so a plain
	// `palbase serve` must land there. --port still overrides for the rare conflict.
	cmd.Flags().IntVar(&port, "port", 4003, "Local port for the dev server")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to run against (defaults to the active branch; omit for main)")
	return cmd
}

// devBranchValue resolves the branch name for the dev-server's PALBASE_BRANCH
// env. Unlike the server payload (which omits "main" for back-compat),
// dev-server is local and always wants a concrete value, so resolveActiveBranch's
// "" (main/unset) is surfaced as "main".
func devBranchValue(flag string) string {
	if b := resolveActiveBranch(flag); b != "" {
		return b
	}
	return "main"
}

// servedBranch is the subset of a `project.listBranches` row the serve
// preflight needs.
type servedBranch struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

// preflightServeBranch fails fast with an actionable message when the branch
// the dev server targets isn't a live, active deployment. serve proxies
// Database/ctx.* to the deployed branch, so an undeployed/provisioning/
// hibernated branch can't back local dev. A listing failure (offline/auth) is
// non-fatal: it warns and lets the reveal step surface any real problem.
func preflightServeBranch(ctx context.Context, sc *studio.Client, ref, branch string) error {
	var rows []servedBranch
	if err := sc.Query(ctx, "project.listBranches", map[string]any{"ref": ref}, &rows); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't verify branch %q is deployed (%v) — continuing\n", branch, err)
		return nil
	}
	var found *servedBranch
	for i := range rows {
		if rows[i].Name == branch {
			found = &rows[i]
			break
		}
	}
	return branchPreflightError(branch, found)
}

// branchPreflightError maps a branch's deployment state to an actionable error
// (nil = good to serve). Pure, so the status→guidance mapping is unit-tested.
func branchPreflightError(branch string, found *servedBranch) error {
	if found == nil {
		return fmt.Errorf(`branch %q isn't deployed yet.

`+"`palbase serve`"+` runs your controllers locally but proxies Database and ctx.*
to the deployed branch — which doesn't exist until you create and push it:

  • new branch:                palbase branch create %s
  • or deploy the current one: git push origin %s

then re-run `+"`palbase serve --branch %s`"+`.`, branch, branch, branch, branch)
	}
	switch found.Status {
	case "active", "":
		return nil
	case "creating":
		return fmt.Errorf("branch %q is still provisioning — check `palbase branch list` and re-run once it's active", branch)
	case "hibernated", "paused", "stopped", "idle":
		return fmt.Errorf("branch %q is hibernated — wake it first:\n\n  palbase branch wake %s", branch, branch)
	case "deleted":
		return fmt.Errorf("branch %q was deleted — recreate it:\n\n  palbase branch create %s", branch, branch)
	default:
		// Unknown/transient state: don't block local dev, but make it visible.
		fmt.Fprintf(os.Stderr, "warning: branch %q reports status %q — serving anyway\n", branch, found.Status)
		return nil
	}
}

// warnUndeployedSchema prints a best-effort note when the project's local
// db/schema.ts or db/migrations/ differ from what's deployed to `branch`.
// serve runs against the deployed branch DB, so unpushed schema changes won't
// be reflected. Never blocks; silent when git is unavailable or db/ is clean.
func warnUndeployedSchema(cwd, branch string, w io.Writer) {
	if _, err := os.Stat(filepath.Join(cwd, "db", "schema.ts")); err != nil {
		return // no schema → nothing to migrate
	}
	paths := []string{"db/schema.ts", "db/migrations"}

	dirty := false
	statusArgs := append([]string{"-C", cwd, "status", "--porcelain", "--"}, paths...)
	if out, err := exec.Command("git", statusArgs...).Output(); err == nil {
		dirty = len(strings.TrimSpace(string(out))) > 0
	}

	unpushed := false
	if !dirty {
		logArgs := append([]string{"-C", cwd, "log", "--oneline", "@{upstream}..HEAD", "--"}, paths...)
		if out, err := exec.Command("git", logArgs...).Output(); err == nil {
			unpushed = len(strings.TrimSpace(string(out))) > 0
		}
	}

	if !dirty && !unpushed {
		return
	}
	fmt.Fprintf(w, `note: local db/schema.ts or db/migrations/ has changes not deployed to branch %q.
serve runs against the DEPLOYED branch database — new tables/columns won't exist
until you push. Additive changes auto-migrate on deploy; type changes need an
explicit migration in db/migrations/ (the deploy drift-gate blocks unmigrated
type changes).

`, branch)
}

// deployRow mirrors one control-pg `deployments` attempt as the deployments REST
// route returns it. This is the canonical attempt log — a FAILED attempt (or a
// pod that never went Ready) has no git commit but does have a row here, so
// unlike the old Store A `backend.versions` read it never hides a failure behind
// "(no versions)". succeeded + a non-empty Error = "deployed with warnings".
type deployRow struct {
	Status        string  `json:"status"`
	Version       *string `json:"version"`
	Branch        string  `json:"branch"`
	Trigger       string  `json:"trigger"`
	Error         *string `json:"error"`
	CommitMessage *string `json:"commitMessage"`
	CreatedAt     string  `json:"createdAt"`
}

func newDeploysCmd(r Resolvers) *cobra.Command {
	var refFlag string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "deploys",
		Short: "Show deploy history (newest first, all branches)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			var resp struct {
				Deployments []deployRow `json:"deployments"`
			}
			if err := r.REST().Do(cmd.Context(), http.MethodGet,
				"/api/v1/projects/"+ref+"/deployments?limit=20", nil, &resp); err != nil {
				return fmt.Errorf("list deployments: %w", err)
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(resp.Deployments)
			}
			if len(resp.Deployments) == 0 {
				fmt.Println("(no deploy attempts yet — push with 'palbase push' or git push)")
				return nil
			}
			fmt.Printf("%-11s %-8s %-10s %-20s %-12s %s\n",
				"STATUS", "VERSION", "BRANCH", "WHEN", "TRIGGER", "NOTE")
			for _, d := range resp.Deployments {
				fmt.Printf("%-11s %-8s %-10s %-20s %-12s %s\n",
					deployStatusLabel(d),
					deployVersion(d.Version),
					d.Branch,
					deployWhen(d.CreatedAt),
					d.Trigger,
					deployNote(d),
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON (full untruncated notes/errors)")
	return cmd
}

// deployStatusLabel uppercases the status; a succeeded row that still carries an
// error is "WARN" — the deploy landed but the runtime flagged something (e.g.
// zero endpoints collected), which must not read as a clean success.
func deployStatusLabel(d deployRow) string {
	if d.Status == "succeeded" && d.Error != nil && *d.Error != "" {
		return "WARN"
	}
	if d.Status == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(d.Status)
}

func deployVersion(v *string) string {
	if v == nil || *v == "" {
		return "-"
	}
	return *v
}

// deployWhen renders the createdAt (RFC3339 from the JSON API) as a local
// timestamp; an unparseable value falls back to the raw string rather than a
// bogus zero-time.
func deployWhen(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// deployNote is the last column: the FIRST line of the deploy error (the
// server-side failure reason — the whole reason this reads control-pg and not
// Store A), truncated to ~100 chars for the table. `--json` carries the full
// text. A clean success with no error falls back to the commit message.
func deployNote(d deployRow) string {
	if d.Error != nil && *d.Error != "" {
		return truncateNote(firstLine(*d.Error), 100)
	}
	if d.CommitMessage != nil {
		return truncateNote(firstLine(*d.CommitMessage), 100)
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncateNote(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func newRollbackCmd(r Resolvers) *cobra.Command {
	var refFlag string
	var branchFlag string
	cmd := &cobra.Command{
		Use:   "rollback <version-sha>",
		Args:  cobra.ExactArgs(1),
		Short: "Roll back to a previous version (creates a new commit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			version := args[0]
			// Branch context (Track A · Feature 3): --branch wins; otherwise
			// the locally-active branch from `palbase branch switch`
			// (ProjectConfig.DefaultEnv). "main"/empty is omitted so the
			// server resolves the default branch (back-compat).
			branch := resolveActiveBranch(branchFlag)
			payload := map[string]any{"ref": ref, "version": version}
			if branch != "" {
				payload["branch"] = branch
			}
			var resp struct {
				Status         string `json:"status"`
				Version        string `json:"version"`
				RolledBackFrom string `json:"rolled_back_from"`
			}
			if err := r.Studio().Mutation(cmd.Context(), "backend.rollback", payload, &resp); err != nil {
				return fmt.Errorf("backend.rollback: %w", err)
			}
			target := "default branch"
			if branch != "" {
				target = "branch " + branch
			}
			fmt.Printf("✓ rolled back %s to %s (new HEAD: %s)\n", target, resp.RolledBackFrom, resp.Version)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to roll back (defaults to the active branch; omit for main)")
	return cmd
}

// resolveActiveBranch picks the branch the rollback targets: the --branch flag
// if set, else the locally-active branch (ProjectConfig.DefaultEnv, set by
// `palbase branch switch`). Returns "" for main / unset so the caller omits the
// branch field and the server resolves the default branch (back-compat).
func resolveActiveBranch(flag string) string {
	if flag != "" {
		if flag == "main" {
			return ""
		}
		return flag
	}
	cfg, err := auth.LoadProjectConfig()
	if err != nil || cfg == nil || cfg.DefaultEnv == "" || cfg.DefaultEnv == "main" {
		return ""
	}
	return cfg.DefaultEnv
}

// lastDeploy mirrors backend.status's `lastDeploy` field: the newest deploy row
// for the active branch (from control-pg.deployments). Nil when the branch has
// never been deployed. This is the visibility surface — a server-side deploy
// failure that only lived in logs now shows up in `palbase status`.
type lastDeploy struct {
	Status    string  `json:"status"`
	Error     *string `json:"error"`
	Version   *string `json:"version"`
	Branch    *string `json:"branch"`
	UpdatedAt *string `json:"updatedAt"`
}

func newStatusCmd(r Resolvers) *cobra.Command {
	var refFlag string
	var branchFlag string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the project's active version + deploy state",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			// backend.status still carries backendEnabled server-side, but the
			// CLI no longer surfaces it: backend is the default (every project
			// is a backend), so there's no enable state for a user to act on.
			// We show the version/deploy info, which is what `status` is for.
			var resp struct {
				Ref           string      `json:"ref"`
				Head          *string     `json:"head"`
				ActiveVersion *string     `json:"activeVersion"`
				LastDeploy    *lastDeploy `json:"lastDeploy"`
			}
			// Send the active branch so status reflects the branch the user is
			// on (--branch wins, else ProjectConfig.DefaultEnv; "main"/empty is
			// omitted so the server resolves the default branch — F14 fix).
			payload := map[string]any{"ref": ref}
			if branch := resolveActiveBranch(branchFlag); branch != "" {
				payload["branch"] = branch
			}
			if err := r.Studio().Query(cmd.Context(), "backend.status", payload, &resp); err != nil {
				return fmt.Errorf("backend.status: %w", err)
			}
			// --json: emit the raw status so a script/CI can poll deploy state
			// without parsing the human lines. Mirrors `project status --json` /
			// `apikey list --json`; the flag was missing here, so `palbase status
			// --json` errored "unknown flag" and a poll loop got no output.
			if jsonOut {
				fmt.Println(renderJSON(resp))
				return nil
			}
			fmt.Printf("ref:    %s\n", resp.Ref)
			if resp.Head != nil {
				fmt.Printf("head:   %s\n", *resp.Head)
			}
			if resp.ActiveVersion != nil {
				fmt.Printf("active: %s\n", *resp.ActiveVersion)
			}
			if line := formatLastDeploy(resp.LastDeploy, time.Now()); line != "" {
				fmt.Print(line)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to report (defaults to the active branch; omit for main)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit status as JSON")
	return cmd
}

// formatLastDeploy renders the human "last deploy" block for `palbase status`.
// Returns "" when there is no deploy to report (nil), so the caller prints
// nothing for a never-deployed branch. This is the centauri surface: a FAILED
// (or succeeded-with-warnings) deploy carries the server-side error to the
// user's terminal instead of it dying in the logs.
func formatLastDeploy(d *lastDeploy, now time.Time) string {
	if d == nil {
		return ""
	}
	// succeeded + a non-null error means the deploy landed but the runtime
	// flagged something (e.g. zero endpoints collected) — call that out so a
	// green-looking deploy that silently produced no routes doesn't read as fine.
	label := d.Status
	if label == "" {
		label = "unknown"
	}
	if d.Status == "succeeded" && d.Error != nil && *d.Error != "" {
		label = "succeeded with warnings"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("last deploy: %s%s\n", strings.ToUpper(label), deployMeta(d, now)))
	if d.Error != nil && *d.Error != "" {
		b.WriteString(fmt.Sprintf("  error: %s\n", *d.Error))
	}
	return b.String()
}

// deployMeta formats the " (branch, 3m ago)" suffix. Branch defaults to "main"
// when the server omitted it (default branch), and the age is dropped when the
// timestamp is missing or unparseable rather than printing a bogus duration.
func deployMeta(d *lastDeploy, now time.Time) string {
	branch := "main"
	if d.Branch != nil && *d.Branch != "" {
		branch = *d.Branch
	}
	if d.UpdatedAt == nil {
		return fmt.Sprintf(" (%s)", branch)
	}
	t, err := time.Parse(time.RFC3339, *d.UpdatedAt)
	if err != nil {
		return fmt.Sprintf(" (%s)", branch)
	}
	return fmt.Sprintf(" (%s, %s)", branch, humanizeAgo(now.Sub(t)))
}

// humanizeAgo renders a coarse "3m ago" / "2h ago" / "5d ago" relative age.
// ponytail: coarse buckets, no library — a deploy age never needs seconds.
func humanizeAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// renderJSON pretty-prints a value for `--json` output paths if a
// future flag wires that on. Kept here so command files stay terse.
func renderJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

var _ = renderJSON // silence "unused" until --json lands

func lookupBackendTarget(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref string, branch string) (backendTarget, error) {
	var resp struct {
		EndpointRef    string `json:"endpointRef"`
		PublishableKey string `json:"publishableKey"`
	}
	payload := map[string]any{"ref": ref}
	if branch != "" {
		payload["branch"] = branch
	}
	if err := sc.Query(ctx, "apikey.reveal", payload, &resp); err != nil {
		return backendTarget{}, fmt.Errorf("apikey.reveal: %w", err)
	}
	if resp.PublishableKey == "" {
		return backendTarget{}, errors.New("apikey.reveal: missing publishable key")
	}
	endpointRef := resp.EndpointRef
	if endpointRef == "" {
		endpointRef = ref
	}
	return backendTarget{
		URL:    fmt.Sprintf("https://%s.%s", endpointRef, endpoints.PublicHost),
		APIKey: resp.PublishableKey,
	}, nil
}


// fetchOAuthProviders calls palauth's public `/auth/oauth/providers`
// endpoint (anon-key authed, secret-free) and lowers the response into a
// swiftOAuthConfig. After the config cutover this is mapped onto the per-env
// Palbase-Info.plist's `oauth` block (via swiftOAuthToApps), the SOLE config
// source the iOS SDK reads.
//
// Best-effort: a 404 (older palauth without the endpoint), a
// non-OAuth-providers response, or a network failure all return
// (nil, nil) — the codegen continues without an `oauth` block, and
// the SDK's zero-arg `signInWithGoogle()` overload short-circuits at
// runtime. We only return an error for things callers genuinely need
// to act on (malformed JSON from a 2xx response).
//
// Apple's response carries only `enabled` because the iOS SDK doesn't
// need credentials to drive AuthenticationServices — the id_token
// exchange happens server-side. Google's response carries
// `client_id`, and we derive the standard reversed-DNS redirectURI
// (`com.googleusercontent.apps.<numeric-id>:/oauthredirect`) from
// it so customer apps don't have to hand-author the value too.
func fetchOAuthProviders(ctx context.Context, baseURL, apiKey string) (*swiftOAuthConfig, error) {
	if baseURL == "" || apiKey == "" {
		return nil, nil
	}
	url := strings.TrimRight(baseURL, "/") + "/auth/oauth/providers"
	req, err := newJSONRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, nil // best-effort: caller continues without oauth
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil
	}
	var raw struct {
		Providers map[string]struct {
			Enabled  bool   `json:"enabled"`
			ClientID string `json:"client_id,omitempty"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode /auth/oauth/providers: %w", err)
	}
	if len(raw.Providers) == 0 {
		return nil, nil
	}
	out := &swiftOAuthConfig{}
	if apple, ok := raw.Providers["apple"]; ok && apple.Enabled {
		out.Apple = &swiftOAuthApple{Enabled: true}
	}
	if google, ok := raw.Providers["google"]; ok && google.Enabled && google.ClientID != "" {
		out.Google = &swiftOAuthGoogle{
			Enabled:     true,
			ClientID:    google.ClientID,
			RedirectURI: googleRedirectURIFromClientID(google.ClientID),
		}
	}
	// All providers came back disabled / unconfigured — collapse to nil
	// so the JSON omits the empty `oauth: {}` block.
	if out.Apple == nil && out.Google == nil {
		return nil, nil
	}
	return out, nil
}

// googleRedirectURIFromClientID builds the standard reversed-DNS
// callback URL Google issues for iOS OAuth clients:
//
//	1234567890-abc.apps.googleusercontent.com
//	→ com.googleusercontent.apps.1234567890-abc:/oauthredirect
//
// Customers can override per-call with the explicit
// `pb.auth.signInWithGoogle(clientID:redirectURI:)` overload if their
// Google OAuth client uses a non-standard scheme.
func googleRedirectURIFromClientID(clientID string) string {
	// Strip the .apps.googleusercontent.com suffix and prepend the
	// reversed domain. If the suffix is missing (unusual), fall back
	// to the raw client_id — the SDK error message will surface the
	// mismatch clearly.
	const suffix = ".apps.googleusercontent.com"
	id := strings.TrimSuffix(clientID, suffix)
	return "com.googleusercontent.apps." + id + ":/oauthredirect"
}

// fetchOpts tunes the wake-aware retry loop in fetchOpenAPISpecOpts.
//
//   - attemptTimeout caps a single GET. It must comfortably exceed the
//     gateway's wake-and-hold (~90s) so a slow-but-OK cold wake returns on the
//     held connection instead of being aborted client-side.
//   - totalBudget caps the whole loop (all attempts + backoff) so codegen never
//     blocks an Xcode build forever.
//   - minBackoff is the floor between retries when the backend gives no
//     Retry-After hint.
type fetchOpts struct {
	attemptTimeout time.Duration
	totalBudget    time.Duration
	minBackoff     time.Duration
	progress       io.Writer // optional: a line is written before each retry
}

// defaultFetchOpts sizes the loop for a Free-tier cold wake. A scaled-to-zero
// br-<ref> pod is woken synchronously by Kong (up to ~90s plugin hold) and, when
// node capacity is starved, the orchestrator returns 503 backend_unavailable
// (Retry-After: 5) until a node is provisioned. 120s per attempt covers the
// hold; the 150s total budget allows ~2 short 503 retries on top.
var defaultFetchOpts = fetchOpts{
	attemptTimeout: 120 * time.Second,
	totalBudget:    150 * time.Second,
	minBackoff:     2 * time.Second,
}

// fetchRemoteOpenAPISpec is the wake-aware fetch used for REMOTE tenant hosts
// (https://<ref>.dev.palbase.studio), where a Free-tier pod may be idle-paused
// and Kong holds the request while it cold-wakes. It prints "backend waking…"
// progress to w so a long first build after idle doesn't look like a hang.
func fetchRemoteOpenAPISpec(ctx context.Context, specURL, apiKey string, w io.Writer) ([]byte, error) {
	opts := defaultFetchOpts
	opts.progress = w
	return fetchOpenAPISpecOpts(ctx, specURL, apiKey, opts)
}

// fetchOpenAPISpecOpts is the wake-aware core. It retries ONLY on signals that
// mean "the backend pod is still waking" — HTTP 502/503 and per-attempt
// timeouts — honoring a Retry-After header when present. Everything else fails
// immediately: a connection-refused (the local `palbase serve` probe when serve
// is down) returns fast so the remote fallback kicks in without burning the
// budget, and a hard 4xx (e.g. 401 invalid_apikey) is a real error, not a wake.
func fetchOpenAPISpecOpts(ctx context.Context, specURL, apiKey string, opts fetchOpts) ([]byte, error) {
	deadline := time.Now().Add(opts.totalBudget)
	var lastErr error
	attempt := 0
	for {
		attempt++
		body, retryAfter, err := fetchOnce(ctx, specURL, apiKey, opts.attemptTimeout)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !errors.As(err, new(wakeRetryable)) {
			return nil, err // hard error (conn-refused, 4xx, body read) — no retry
		}
		// Wake in progress. Back off (Retry-After if the gateway gave one, else
		// the floor) but never past the total budget.
		wait := opts.minBackoff
		if retryAfter > wait {
			wait = retryAfter
		}
		if time.Now().Add(wait).After(deadline) {
			return nil, fmt.Errorf("fetch %s: backend did not wake within %s (last: %w)", specURL, opts.totalBudget, lastErr)
		}
		if opts.progress != nil {
			fmt.Fprintf(opts.progress, "backend waking (attempt %d): %v — retrying in %s\n", attempt, err, wait.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// wakeRetryable marks an error returned by fetchOnce as a transient "pod is
// waking" condition (502/503 or a per-attempt timeout) that fetchOpenAPISpecOpts
// should retry, as opposed to a hard failure.
type wakeRetryable struct{ err error }

func (w wakeRetryable) Error() string { return w.err.Error() }
func (w wakeRetryable) Unwrap() error { return w.err }

// httpStatusError marks a fetchOnce failure where the server ANSWERED with a
// non-2xx status (excluding the wake-retryable 502/503), so callers can
// distinguish "it is up but erroring" from "nothing listening" (connection
// refused). pullTSTypes's auto-fallback wording depends on the split.
type httpStatusError struct {
	status int
	err    error
}

func (e httpStatusError) Error() string { return e.err.Error() }
func (e httpStatusError) Unwrap() error { return e.err }

// fetchOnce does a single GET with its own timeout. The second return is a
// parsed Retry-After (0 if absent/unparseable).
func fetchOnce(ctx context.Context, specURL, apiKey string, timeout time.Duration) ([]byte, time.Duration, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := newJSONRequest(attemptCtx, "GET", specURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		// Caller cancelled → surface as-is (do not mask as retryable).
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		// Per-attempt timeout while a cold pod is held → retryable wake signal.
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, wakeRetryable{fmt.Errorf("fetch %s: attempt timed out after %s", specURL, timeout)}
		}
		// Connection-refused / DNS / TLS etc. — a hard error (e.g. local serve
		// down). Fail fast so the caller falls back without retrying.
		return nil, 0, fmt.Errorf("fetch %s: %w", specURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusBadGateway {
		return nil, parseRetryAfter(resp.Header.Get("Retry-After")),
			wakeRetryable{fmt.Errorf("fetch %s: %d %s", specURL, resp.StatusCode, strings.TrimSpace(string(body)))}
	}
	if resp.StatusCode/100 != 2 {
		return nil, 0, httpStatusError{
			status: resp.StatusCode,
			err:    fmt.Errorf("fetch %s: %d %s", specURL, resp.StatusCode, string(body)),
		}
	}
	return body, 0, nil
}

// parseRetryAfter reads a delta-seconds Retry-After value. We only honor the
// numeric form (the gateway emits retry_after_seconds=5); an HTTP-date form or
// garbage yields 0 and the caller's floor backoff applies.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// envGenExternals are kept external when bundling db/schema.ts so the bundle
// resolves @palbase/* to the project's installed package on NODE_PATH at eval
// time — exactly like the backend-runtime's schema extractor does. Bundling
// them in would duplicate the DSL and break the `defineSchema(...)` identity.
var envGenExternals = []string{"@palbase/backend", "@palbase/core"}

// generateEnvTypes regenerates the project's palbase-env.d.ts from its
// db/schema.ts. This is the typed-Database wiring: the generated file augments
// @palbase/backend/env's `Tables` interface so a project's handlers get a typed
// `Database.tables.*` with no import and no generic.
//
// Mechanism mirrors the backend-runtime's schema extractor
// (modules/backend internal/runtime/schema_extract.js): db/schema.ts imports
// @palbase/backend via ESM (which bare Node can't resolve from a tenant dir), so
// we esbuild-bundle it to a temp CJS file with @palbase/* external, then run the
// embedded env-gen.js bridge over that bundle. The bridge require()s the
// project's @palbase/backend for makeEnvDts(), calls it with the schema, and
// writes palbase-env.d.ts to projectDir.
//
// It is a clean no-op when the project has no db/schema.ts (a v1 project, or a
// v2 project that doesn't declare a schema): there is no typed Database to
// generate, so we return nil without touching the filesystem.
func generateEnvTypes(ctx context.Context, projectDir string) error {
	schemaPath := filepath.Join(projectDir, "db", "schema.ts")
	if _, err := os.Stat(schemaPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no schema → nothing to type, skip cleanly
		}
		return fmt.Errorf("stat db/schema.ts: %w", err)
	}

	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not on PATH (Node.js required to generate palbase-env.d.ts)")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		return fmt.Errorf("npx not on PATH (Node.js required to generate palbase-env.d.ts)")
	}

	tmpDir, err := os.MkdirTemp("", "palbase-envgen-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Bundle db/schema.ts → temp CJS, keeping @palbase/* external.
	bundlePath := filepath.Join(tmpDir, "schema.js")
	if err := bundleSchemaTS(ctx, projectDir, schemaPath, bundlePath); err != nil {
		return fmt.Errorf("bundle db/schema.ts: %w", err)
	}

	// Extract the embedded env-gen.js bridge next to the bundle.
	scriptPath := filepath.Join(tmpDir, "env-gen.js")
	body, err := devServerFS.ReadFile("devjs/env-gen.js")
	if err != nil {
		return fmt.Errorf("read embedded env-gen.js: %w", err)
	}
	if err := os.WriteFile(scriptPath, body, 0o644); err != nil {
		return err
	}

	outPath := filepath.Join(projectDir, "palbase-env.d.ts")
	if err := runEnvGenBridge(ctx, projectDir, scriptPath, bundlePath, outPath); err != nil {
		return err
	}
	return nil
}

// bundleSchemaTS runs `npx esbuild` over db/schema.ts, emitting a CJS bundle at
// outPath with @palbase/* kept external. Runs from projectDir so node_modules
// resolution and any relative imports inside schema.ts anchor to the project.
func bundleSchemaTS(ctx context.Context, projectDir, schemaPath, outPath string) error {
	bundleCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	args := []string{
		"--yes", "esbuild",
		schemaPath,
		"--bundle",
		"--platform=node",
		"--format=cjs",
		"--target=es2022",
		"--outfile=" + outPath,
	}
	for _, ext := range envGenExternals {
		args = append(args, "--external:"+ext)
	}

	cmd := exec.CommandContext(bundleCtx, "npx", args...)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "NODE_PATH="+filepath.Join(projectDir, "node_modules"))
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("esbuild: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// runEnvGenBridge runs the env-gen.js bridge over the bundled schema, writing
// palbase-env.d.ts to outPath. NODE_PATH points at the project's node_modules so
// the bridge's `require('@palbase/backend')` (for makeEnvDts) resolves to the
// project's installed SDK.
func runEnvGenBridge(ctx context.Context, projectDir, scriptPath, bundlePath, outPath string) error {
	evalCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	reqData, err := json.Marshal(struct {
		BundlePath string `json:"bundle_path"`
		OutPath    string `json:"out_path"`
	}{BundlePath: bundlePath, OutPath: outPath})
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(evalCtx, "node", scriptPath)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "NODE_PATH="+filepath.Join(projectDir, "node_modules"))
	cmd.Stdin = bytes.NewReader(reqData)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("env-gen: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return fmt.Errorf("parse env-gen output: %w (output: %s)", err, strings.TrimSpace(stdout.String()))
	}
	if env.Error != "" {
		return fmt.Errorf("env-gen: %s", env.Error)
	}
	return nil
}

// ensureGitignored appends `entry` to the .gitignore at gitignorePath
// if neither it nor its trailing-slash variant is already listed.
// Creates the file when missing. Whitespace-trimmed comparisons keep
// this idempotent across editor quirks.
func ensureGitignored(gitignorePath, entry string) error {
	current, err := os.ReadFile(gitignorePath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	want := strings.TrimSuffix(entry, "/")
	for _, line := range strings.Split(string(current), "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "/"))
		if trimmed == want {
			return nil
		}
	}
	suffix := entry + "\n"
	if len(current) > 0 && !strings.HasSuffix(string(current), "\n") {
		suffix = "\n" + suffix
	}
	return os.WriteFile(gitignorePath, append(current, []byte(suffix)...), 0o644)
}
