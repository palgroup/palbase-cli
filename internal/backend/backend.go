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
)

// defaultHTTPClient is reused by `palbase types` (and any
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
//go:embed devjs/dev-server.js devjs/module-clients.js devjs/env-gen.js devjs/return_types.js devjs/throw_analysis.js
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
// them the CLI keeps local dev (`serve`) and the observation/control verbs
// (list, rollback, status, types, mobile).
func Commands(r Resolvers) []*cobra.Command {
	return []*cobra.Command{
		newWebCmd(r),
		newDevCmd(r),
		newListCmd(r),
		newRollbackCmd(r),
		newStatusCmd(r),
		newTypesCmd(r),
		newGenTypesCmd(r),
		newPullSpecCmd(r),
		newCloneCmd(r),
		newPullCmd(r),
		newPushCmd(r),
	}
}

// backendTarget is the resolved (URL + publishable key) for a project's
// backend at a given branch. Used by lookupBackendTarget (which pull-spec and
// `palbase types` share) to address the deployed tenant host.
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
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
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
	// (`palbase types` local/auto probe, the SPM plugin's pull-spec local
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

func newListCmd(r Resolvers) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show deploy history (newest first)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			var resp struct {
				Versions []struct {
					Version   string    `json:"version"`
					Files     int       `json:"files"`
					CreatedAt time.Time `json:"created_at"`
					Message   string    `json:"message"`
				} `json:"versions"`
				ActiveVersion string `json:"active_version"`
			}
			if err := r.Studio().Query(cmd.Context(), "backend.versions", map[string]any{"ref": ref}, &resp); err != nil {
				return fmt.Errorf("backend.versions: %w", err)
			}
			if len(resp.Versions) == 0 {
				fmt.Println("(no versions)")
				return nil
			}
			fmt.Printf("%-10s %-7s %-20s %s\n", "VERSION", "FILES", "WHEN", "MESSAGE")
			for _, v := range resp.Versions {
				marker := ""
				if v.Version == resp.ActiveVersion {
					marker = "*"
				}
				fmt.Printf("%-1s %-8s %-7d %-20s %s\n",
					marker,
					v.Version,
					v.Files,
					v.CreatedAt.Local().Format("2006-01-02 15:04:05"),
					v.Message,
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	return cmd
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

func newStatusCmd(r Resolvers) *cobra.Command {
	var refFlag string
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
				Ref           string  `json:"ref"`
				Head          *string `json:"head"`
				ActiveVersion *string `json:"activeVersion"`
			}
			if err := r.Studio().Query(cmd.Context(), "backend.status", map[string]any{"ref": ref}, &resp); err != nil {
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
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit status as JSON")
	return cmd
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

// ─────────────────────────────────────────────────────────────────────
// `palbase types`
//
// Fetches the backend's `/openapi.json` and generates a typed client
// module: `palbe.gen.ts` for the palbe web SDK (default) or a Swift
// file for the Palbe iOS SDK. Output is auto-generated and overwritten
// on every run; users edit their controllers instead.
// ─────────────────────────────────────────────────────────────────────

func newTypesCmd(r Resolvers) *cobra.Command {
	var refFlag string
	var envFlag string
	var outFlag string
	var softFlag bool
	var watchFlag bool
	cmd := &cobra.Command{
		Use:   "types",
		Short: "Generate a typed client from your backend's OpenAPI spec",
		Long: `Fetch the OpenAPI document from your backend and generate the typed
palbe.gen.ts client — typed namespaced calls (pb.rooms.create(...)) plus the
embedded runtime config for the palbe web SDK. Commit the file and re-run after
every deploy (a predev/prebuild script keeps it fresh).

(iOS Swift generation lives in the palbase-swift-codegen SPM build-tool plugin,
which consumes the openapi.json fetched by 'palbase pull-spec' — not this command.)

Spec source (--env):
  auto    (default) probe a local 'palbase serve' on localhost:4003 first;
          when it is up, the generated config points at the local server and
          OAuth discovery is skipped (works without login/network). When it
          is down, fall back to the deployed backend.
  local   require the local 'palbase serve' (error when it is down)
  remote  always use the deployed backend (wake-aware fetch through Kong)

The committed file embeds the spec source URL — if you develop against a
local serve, regenerate with --env remote before a release build.

--soft turns ANY failure into a 'warning: codegen skipped (...)' line and
exit 0, so a predev/prebuild hook never blocks a machine without login or
network access.

--watch keeps running, polling the local 'palbase serve' on :4003 every second
and rewriting palbe.gen.ts whenever the spec changes. Start 'palbase serve'
first; --watch handles the case where serve is not yet up (useful in
split-terminal dev). Remote sync (release / CI) happens via the predev/prebuild
hook — --watch is for local development only.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			run := func() error {
				ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), out)
				if err != nil {
					return err
				}
				// Branch = the linked .palbase/config.json DefaultEnv. --ref makes
				// resolveOrLinkRef return without writing the config, so a fresh
				// cwd falls back to main.
				branch := "main"
				if cfg, cfgErr := auth.LoadProjectConfig(); cfgErr == nil && cfg.DefaultEnv != "" {
					branch = cfg.DefaultEnv
				}
				outFile := outFlag
				if outFile == "" {
					outFile = "palbe.gen.ts"
				}
				if watchFlag {
					// SIGINT/SIGTERM cancel the watch ctx (serve's pattern)
					// so Ctrl-C exits the loop cleanly — "watch stopped",
					// exit 0, no mid-write truncation of palbe.gen.ts.
					wctx, stop := watchSignalContext(cmd.Context())
					defer stop()
					return runTypesWatch(wctx, r, ref, branch, envFlag, outFile, softFlag, out)
				}
				return pullTSTypes(cmd.Context(), r.Studio(), r.Endpoints(), ref, branch, envFlag, outFile, out)
			}
			if err := run(); err != nil {
				if softFlag {
					fmt.Fprintf(out, "warning: codegen skipped (%v)\n", err)
					return nil
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&envFlag, "env", "auto", "Spec source: auto (local serve when up, else deployed) | local | remote")
	cmd.Flags().StringVar(&outFlag, "out", "", "Output file (default: palbe.gen.ts)")
	cmd.Flags().BoolVar(&softFlag, "soft", false, "Never fail: print a warning and exit 0 on any error (for predev/prebuild hooks)")
	cmd.Flags().BoolVar(&watchFlag, "watch", false, "Watch mode: poll local serve on :4003 and regenerate on spec changes")
	return cmd
}

// newGenTypesCmd is the standalone regeneration of palbase-env.d.ts from the
// project's db/schema.ts — the same step `palbase serve` runs on startup,
// exposed on its own so it can run from a build/CI step or after editing the
// schema without booting the dev server.
//
// Distinct from `palbase types`: that pulls the DEPLOYED OpenAPI spec to type
// the client SDK's `pb.backend.call(...)`; this types the project's OWN handlers
// (`Database.tables.*`) from the local schema source. No project link, no
// network — purely local.
func newGenTypesCmd(_ Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "gen-types",
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

// pullTSTypes implements `palbase types --lang ts`: resolve the spec source
// per --env, parse the OpenAPI document, and write the palbe.gen.ts module
// (typed namespaced calls + embedded __configure runtime config) for the
// palbe web SDK. The file is meant to be COMMITTED — unlike the retired
// .palbase/openapi.json + types.d.ts pair, it is the app's typed client.
//
// env routing:
//   - "auto" (default): probe the local `palbase serve` (localhost:4003,
//     single 3s fast-fail attempt). Serve up → the config points at the
//     local server (apiKey empty — serve's spec is unauthenticated) and
//     OAuth discovery is SKIPPED, so a logged-out / offline machine still
//     regenerates against serve. Serve down → remote.
//   - "local": like the auto local path, but serve-down is a hard error.
//   - "remote": resolve the branch target (apikey.reveal), wake-aware fetch
//     of the deployed spec, best-effort OAuth discovery.
func pullTSTypes(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, branch, env, outFile string, w io.Writer) error {
	if env == "" {
		env = "auto"
	}
	cfg := tsGeneratedConfig{Branch: branch}
	var specBytes []byte

	const localURL = "http://localhost:4003"
	useRemote := false
	switch env {
	case "auto", "local":
		b, localErr := fetchLocalOpenAPISpec(ctx, localURL+"/openapi.json")
		switch {
		case localErr == nil:
			specBytes = b
			cfg.URL = localURL
		case env == "local":
			return fmt.Errorf("no local `palbase serve` at %s (%w) — start `palbase serve` or use --env remote", localURL, localErr)
		default:
			// Distinguish "serve ANSWERED with an error" (it is up — fix it,
			// don't start it) from "nothing listening" before falling back to
			// the deployed spec.
			var hs httpStatusError
			if errors.As(localErr, &hs) {
				fmt.Fprintf(w, "local serve responded with an error (HTTP %d) at %s/openapi.json; using the deployed spec — fix `palbase serve` for local dev\n", hs.status, localURL)
			} else {
				fmt.Fprintf(w, "local spec not found at %s/openapi.json (%v); using the deployed spec — run `palbase serve` for local dev\n", localURL, localErr)
			}
			useRemote = true
		}
	case "remote":
		useRemote = true
	default:
		return fmt.Errorf("unknown --env %q (expected auto|local|remote)", env)
	}

	if useRemote {
		target, err := lookupBackendTarget(ctx, sc, endpoints, ref, branch)
		if err != nil {
			return err
		}
		specBytes, err = fetchRemoteOpenAPISpec(ctx, target.URL+"/openapi.json", target.APIKey, w)
		if err != nil {
			return err
		}
		cfg.URL = target.URL
		cfg.APIKey = target.APIKey
		oauth, oauthErr := fetchOAuthProviders(ctx, target.URL, target.APIKey)
		if oauthErr != nil {
			fmt.Fprintf(w, "  (oauth providers not fetched: %v)\n", oauthErr)
		}
		cfg.OAuth = oauth
	}

	ops, err := parseOpenAPIForSwift(specBytes)
	if err != nil {
		return err
	}
	// Zero-op overwrite guard. A live spec with 0 operations almost always
	// means the backend's controller metadata extraction broke (the "zero
	// endpoints collected" failure class — deploy reports success anyway),
	// not that the app has no endpoints. Never clobber a previously good
	// palbe.gen.ts with an empty client; warn and exit 0 so a predev hook
	// doesn't fail the build. With nothing to protect, write the (empty)
	// module anyway — a fresh project still gets a compilable file — but
	// warn loudly.
	if len(ops) == 0 {
		if existing, readErr := os.ReadFile(outFile); readErr == nil && len(existing) > 0 {
			fmt.Fprintf(w, "warning: live spec has 0 operations — keeping existing %s (fix your controllers and rerun)\n", outFile)
			return nil
		}
		fmt.Fprintf(w, "warning: live spec has 0 operations — %s registers no calls (fix your controllers and rerun)\n", outFile)
	}
	nOps, writeErr := emitAndWriteTS(ops, cfg, outFile)
	if writeErr != nil {
		return writeErr
	}
	fmt.Fprintf(w, "✓ wrote %s (%d operations)\n", outFile, nOps)
	return nil
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

// fetchLocalOpenAPISpec probes a local `palbase serve` with a single short
// attempt and NO wake retries — when serve is down the connection refuses
// instantly and the caller falls back to the remote host without delay.
func fetchLocalOpenAPISpec(ctx context.Context, specURL string) ([]byte, error) {
	return fetchOpenAPISpecOpts(ctx, specURL, "", fetchOpts{
		attemptTimeout: 3 * time.Second,
		totalBudget:    3 * time.Second,
		minBackoff:     time.Second,
	})
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
