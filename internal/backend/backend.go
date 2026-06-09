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
	"crypto/sha1"
	"embed"
	"encoding/hex"
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
	"regexp"
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
//go:embed devjs/dev-server.js devjs/module-clients.js devjs/env-gen.js devjs/return_types.js
var devServerFS embed.FS

// Resolvers returns lazy accessors for the shared CLI globals, so the
// `backend` command tree can be wired into cobra at startup without
// the auth + studio clients having been initialised yet (cobra's
// PersistentPreRunE on the root command is what populates them).
type Resolvers struct {
	Auth      func() *auth.Client
	Studio    func() *studio.Client
	Endpoints func() config.Endpoints
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
// There is no `push`/`pull`/`merge`: deploy is GitHub-native (`git push`
// to the project's GitHub repo → webhook → orchestrator deploys + applies
// the repo's config-as-code). The CLI keeps local dev (`serve`) and the
// observation/control verbs (list, rollback, status, types, mobile).
func Commands(r Resolvers) []*cobra.Command {
	return []*cobra.Command{
		newMobileCmd(r),
		newDevCmd(r),
		newListCmd(r),
		newRollbackCmd(r),
		newStatusCmd(r),
		newTypesCmd(r),
		newGenTypesCmd(r),
	}
}

// The Swift codegen output (typed methods + types) lands in a "Generated"
// subfolder INSIDE the app target's own source folder (e.g.
// palbase/Generated/PalbaseGenerated.swift), with the matching JSON next to
// it (the runtime config the Palbe SDK loads from Bundle.main at
// pb.configure()). Living inside the target folder is what makes it appear
// ONCE, naturally, in the navigator: a modern Xcode 16 app folder is itself
// a synchronized folder, so a nested Generated/ auto-joins the target with
// ZERO pbxproj plumbing. Writing to a top-level "Palbase/Generated" instead
// would (a) collide case-insensitively with a "palbase/" app folder on macOS
// and (b) need a stray root-level synchronized-folder reference — the exact
// double-appearance bug this avoids.
//
// The concrete directory is resolved at runtime from the project's target
// (resolveIOSGeneratedDir). fallbackIOSGeneratedDir is used only when no
// project/target can be detected — a bare "Generated" at cwd, never the
// case-colliding capital "Palbase/...".
const (
	fallbackIOSGeneratedDir = "Generated"
	iosGeneratedSwiftName   = "PalbaseGenerated.swift"
	iosGeneratedJSONName    = "PalbaseGenerated.json"
)

// iosGeneratedSwiftFile returns the Swift output path for a generated dir.
func iosGeneratedSwiftFile(dir string) string {
	return dir + "/" + iosGeneratedSwiftName
}

type backendTarget struct {
	URL    string
	APIKey string
}

func newMobileCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mobile",
		Short: "Generate mobile SDK config and typed endpoint code",
	}
	cmd.AddCommand(
		newMobileCheckoutCmd(r),
		newMobileCodegenCmd(r),
		newMobileLinkCmd(r),
		newMobileUnlinkCmd(r),
	)
	return cmd
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

// projectRef resolves the linked project ref. Order:
//  1. --ref flag override
//  2. .palbase/config.json's ref (link writes it)
//  3. ErrNotLinked — caller decides whether to prompt or fail.
//
// Returning ErrNotLinked instead of bubbling the underlying os.IsNotExist
// lets the serve/observation commands auto-link via project.list when the
// cwd has no .palbase/config.json yet (the interactive picker writes it).
func projectRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	cfg, err := auth.LoadProjectConfig()
	if err != nil {
		if os.IsNotExist(errors.Unwrap(err)) || strings.Contains(err.Error(), "not linked") {
			return "", ErrNotLinked
		}
		return "", err
	}
	if cfg.Ref == "" {
		return "", ErrNotLinked
	}
	return cfg.Ref, nil
}

// ErrNotLinked is returned by projectRef when the cwd doesn't carry a
// .palbase/config.json. Subcommands catch this and offer to pick a
// project from the user's project.list before falling through.
var ErrNotLinked = errors.New("project not linked")

// resolveOrLinkRef wraps projectRef with an interactive picker that
// reads project.list, prompts the user when there's >1, and writes the
// chosen ref to .palbase/config.json so subsequent runs are silent.
//
// The picker fires only when stdin is a TTY — non-interactive callers
// (CI, piped scripts) get the original ErrNotLinked back so they can
// fail loudly instead of hanging waiting for input.
func resolveOrLinkRef(ctx context.Context, override string, c *studio.Client, out io.Writer) (string, error) {
	ref, err := projectRef(override)
	if err == nil {
		return ref, nil
	}
	if !errors.Is(err, ErrNotLinked) {
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

			ref, _ := projectRef("") // best-effort; default ref to "local" inside the JS

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
	// (generateIOSAuto, openAPIURL "local", `--env` help) all probe
	// localhost:4003 for the local /openapi.json, so a plain `palbase serve`
	// must land there. --port still overrides for the rare conflict.
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

func newMobileCheckoutCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkout <branch>",
		Args:  cobra.ExactArgs(1),
		Short: "Switch the mobile backend branch and regenerate SDK code",
		RunE: func(cmd *cobra.Command, args []string) error {
			branch := strings.TrimSpace(args[0])
			if branch == "" {
				return fmt.Errorf("branch is required")
			}
			ref, err := resolveOrLinkRef(cmd.Context(), "", r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			cfg, err := auth.LoadProjectConfig()
			if err != nil {
				return err
			}
			// Commit-on-success, like `git checkout`: write the new branch,
			// then regenerate. If codegen fails (e.g. the branch doesn't
			// exist → apikey.reveal 404), roll the link back to the previous
			// branch so the cwd isn't stranded on a broken branch where every
			// later command fails. The config write only "sticks" once
			// codegen succeeds.
			prevBranch := cfg.DefaultEnv
			cfg.DefaultEnv = branch
			if err := auth.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
			}
			if err := generateIOSRemote(cmd.Context(), r.Studio(), r.Endpoints(), ref, branch, iosGeneratedSwiftFile(resolveIOSGeneratedDir()), os.Stdout); err != nil {
				cfg.DefaultEnv = prevBranch
				if saveErr := auth.SaveProjectConfig(cfg); saveErr != nil {
					return fmt.Errorf("ios codegen failed (%w); ALSO failed to roll back branch to %q: %v", err, prevBranch, saveErr)
				}
				return fmt.Errorf("could not check out branch %q: %w (staying on %q)", branch, err, prevBranch)
			}
			fmt.Fprintf(os.Stdout, "✓ checked out mobile backend branch %q (project %s)\n", branch, ref)
			return nil
		},
	}
	return cmd
}

func newMobileCodegenCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codegen",
		Short: "Generate mobile SDK code from the active Palbase contract",
	}
	cmd.AddCommand(newCodegenIOSCmd(r))
	return cmd
}

func newMobileLinkCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link a mobile app to a Palbase project and wire generated SDK code",
	}
	cmd.AddCommand(newMobileLinkIOSCmd(r))
	return cmd
}

// newMobileUnlinkCmd detaches the current directory from its Palbase
// project. It removes only .palbase/config.json — the Xcode "Palbase
// Codegen iOS" build phase and any generated files are left in place
// (the phase is a no-op once the CLI can't find a link). Re-link with
// `palbase mobile link ios`.
func newMobileUnlinkCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unlink",
		Short: "Unlink the current directory from its Palbase project",
	}
	cmd.AddCommand(newMobileUnlinkIOSCmd(r))
	return cmd
}

func newMobileUnlinkIOSCmd(_ Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "ios",
		Args:  cobra.NoArgs,
		Short: "Remove the Palbase project link from this directory",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := auth.UnlinkProjectConfig(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "✓ unlinked — removed .palbase/config.json (Xcode build phase left in place; re-link with `palbase mobile link ios`)")
			return nil
		},
	}
}

func newMobileLinkIOSCmd(r Resolvers) *cobra.Command {
	var projectPath string
	var targetName string
	var refFlag string
	cmd := &cobra.Command{
		Use:   "ios",
		Args:  cobra.NoArgs,
		Short: "Add Palbase iOS generated code + build phase to an Xcode project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureIOSGeneratedStub(iosGeneratedSwiftFile(resolveIOSGeneratedDir())); err != nil {
				return err
			}
			project, target, changed, err := setupIOSXcodeProject(projectPath, targetName)
			if err != nil {
				return err
			}
			if changed {
				fmt.Fprintf(os.Stdout, "✓ wired %s target %q for Palbase iOS codegen\n", project, target)
			} else {
				fmt.Fprintf(os.Stdout, "✓ %s target %q already wired for Palbase iOS codegen\n", project, target)
			}

			// resolveOrLinkRef honours --ref the same way pull/branch/apikey
			// do (CLI-19): in non-interactive shells where the picker would
			// hang, the user supplies --ref to wire link explicitly. Picker
			// runs only when ref is empty AND stdin is a TTY.
			ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			// mobile link ios is special: it BOTH consumes the ref AND
			// asks LoadProjectConfig immediately for the branch default.
			// resolveOrLinkRef only writes .palbase/config.json when it
			// went through the picker path (override == ""), so a --ref
			// invocation in a fresh cwd would error at LoadProjectConfig
			// below. Materialise the link here when missing so the second
			// LoadProjectConfig always succeeds.
			cfg, err := auth.LoadProjectConfig()
			if err != nil {
				cfg = &auth.ProjectConfig{Ref: ref, DefaultEnv: "main"}
				if saveErr := auth.SaveProjectConfig(cfg); saveErr != nil {
					return fmt.Errorf("save .palbase/config.json: %w", saveErr)
				}
				fmt.Fprintf(os.Stdout, "✓ Linked to %s (branch: main)\n", ref)
			}
			branch := cfg.DefaultEnv
			if branch == "" {
				branch = "main"
			}
			if err := generateIOSAuto(cmd.Context(), r.Studio(), r.Endpoints(), ref, branch, iosGeneratedSwiftFile(resolveIOSGeneratedDir()), os.Stdout); err != nil {
				return fmt.Errorf("initial ios codegen: %w", err)
			}

			// Google URL scheme — runs AFTER codegen because the
			// reversed-DNS redirect scheme is derived from the Google
			// client_id, which codegen just fetched from palauth and
			// wrote into PalbaseGenerated.json. No-op when the project
			// has no Google provider configured.
			if err := wireGoogleURLSchemeFromGenerated(project, target, iosGeneratedSwiftFile(resolveIOSGeneratedDir()), os.Stdout); err != nil {
				fmt.Fprintf(os.Stdout, "  (Google URL scheme not wired: %v)\n", err)
			}

			fmt.Fprintln(os.Stdout, "✓ done — the SDK auto-configures from PalbaseGenerated.json; just `import Palbe` and call pb.*")
			return nil
		},
	}
	cmd.Flags().StringVar(&projectPath, "project", "", "Path to .xcodeproj (defaults to the only project in cwd)")
	cmd.Flags().StringVar(&targetName, "target", "", "Xcode target to wire (defaults to the first app target)")
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref to link (skips the interactive picker; required in non-interactive shells)")
	return cmd
}

func newCodegenIOSCmd(r Resolvers) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "ios",
		Args:  cobra.NoArgs,
		Short: "Generate iOS Palbe config + typed endpoint calls",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Auto-link when the cwd isn't linked yet (mirrors what every
			// other cwd-scoped command does — see resolveOrLinkRef). With
			// --ref the picker is skipped; without it, an interactive shell
			// prompts. CI-only shells must pass --ref.
			ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			// Same defensive materialise as mobile link ios: --ref makes
			// resolveOrLinkRef return early without writing the config, so
			// LoadProjectConfig in a fresh cwd would 404. Default branch to
			// main when the link is being created now.
			cfg, err := auth.LoadProjectConfig()
			if err != nil {
				cfg = &auth.ProjectConfig{Ref: ref, DefaultEnv: "main"}
				if saveErr := auth.SaveProjectConfig(cfg); saveErr != nil {
					return fmt.Errorf("save .palbase/config.json: %w", saveErr)
				}
				fmt.Fprintf(os.Stdout, "✓ Linked to %s (branch: main)\n", ref)
			}
			branch := cfg.DefaultEnv
			if branch == "" {
				branch = "main"
			}
			return generateIOSAuto(cmd.Context(), r.Studio(), r.Endpoints(), ref, branch, iosGeneratedSwiftFile(resolveIOSGeneratedDir()), os.Stdout)
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref to link (skips the interactive picker; required in non-interactive shells)")
	return cmd
}

func generateIOSAuto(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, branch, outFile string, w io.Writer) error {
	// Resolve the remote target first — it carries the publishable key + OAuth
	// the app needs, and it's the device fallback / serve-down runtime URL. If
	// it fails (not logged in, no network, sandboxed build phase without
	// credentials), there's nothing valid to embed — surface the error.
	target, err := lookupBackendTarget(ctx, sc, endpoints, ref, branch)
	if err != nil {
		return err
	}

	oauth, oauthErr := fetchOAuthProviders(ctx, target.URL, target.APIKey)
	if oauthErr != nil {
		fmt.Fprintf(w, "  (oauth providers not fetched: %v)\n", oauthErr)
	}

	// Prefer a local spec when `palbase serve` is up (fast, hot-reloaded
	// endpoint shapes). When it IS up, the app should also TALK to it: embed
	// the dev machine's LAN IP:4003 so both the simulator (via host loopback)
	// and a same-network physical device reach the local backend. When serve
	// is down, fall back to the deployed spec AND the remote tenant host.
	localURL := "http://localhost:4003"
	specBytes, localErr := fetchLocalOpenAPISpec(ctx, localURL+"/openapi.json")
	embedURL := target.URL // remote tenant host (device fallback / serve-down)
	if localErr != nil {
		fmt.Fprintf(w, "local spec not found at %s/openapi.json (%v); using deployed spec + remote URL — run `palbase serve` for local dev\n", localURL, localErr)
		specBytes, err = fetchRemoteOpenAPISpec(ctx, target.URL+"/openapi.json", target.APIKey, w)
		if err != nil {
			return err
		}
	} else {
		// Serve is up — point the app at the LAN-reachable serve address.
		embedURL = "http://" + outboundLANIP() + ":4003"
		fmt.Fprintf(w, "local `palbase serve` detected — embedding %s (simulator + same-network device reach it)\n", embedURL)
	}

	return writeSwiftGenerated(specBytes, swiftGeneratedConfig{
		URL:    embedURL,
		APIKey: target.APIKey,
		Branch: branch,
		OAuth:  oauth,
	}, outFile, w)
}

func generateIOSRemote(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, branch, outFile string, w io.Writer) error {
	target, err := lookupBackendTarget(ctx, sc, endpoints, ref, branch)
	if err != nil {
		return err
	}
	return generateIOSRemoteWithTarget(ctx, target, branch, outFile, w)
}

func generateIOSRemoteWithTarget(ctx context.Context, target backendTarget, branch, outFile string, w io.Writer) error {
	specBytes, err := fetchRemoteOpenAPISpec(ctx, target.URL+"/openapi.json", target.APIKey, w)
	if err != nil {
		return err
	}
	// Best-effort OAuth providers fetch — palauth's public endpoint
	// returns just the secret-free bits the SDK needs (Apple enabled
	// flag, Google client_id). A network blip or an older palauth
	// without the route lands as nil; the codegen continues without
	// an `oauth` block and the SDK falls back to the explicit-
	// parameter signInWithGoogle overload.
	oauth, oauthErr := fetchOAuthProviders(ctx, target.URL, target.APIKey)
	if oauthErr != nil {
		fmt.Fprintf(w, "  (oauth providers not fetched: %v)\n", oauthErr)
	}
	return writeSwiftGenerated(specBytes, swiftGeneratedConfig{
		URL:    target.URL,
		APIKey: target.APIKey,
		Branch: branch,
		OAuth:  oauth,
	}, outFile, w)
}

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

func writeSwiftGenerated(specBytes []byte, cfg swiftGeneratedConfig, outFile string, w io.Writer) error {
	ops, err := parseOpenAPIForSwift(specBytes)
	if err != nil {
		return err
	}
	swift := emitSwift(ops)
	if dir := filepath.Dir(outFile); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(outFile, []byte(swift), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outFile, err)
	}
	// Companion JSON bundle resource — what `pb.configure()` reads via
	// Bundle.main.url(forResource: "PalbaseGenerated", withExtension:
	// "json"). Lives in the same outDir so the xcodeproj wiring covers
	// both files in one shot.
	jsonPath := filepath.Join(filepath.Dir(outFile), "PalbaseGenerated.json")
	if err := writeGeneratedConfigJSON(jsonPath, cfg); err != nil {
		return fmt.Errorf("write %s: %w", jsonPath, err)
	}
	// The generated Swift + JSON now live in a VISIBLE, committed folder
	// (Palbase/Generated/) referenced as a synchronized Xcode folder, so
	// we no longer gitignore them — committing generated code is standard
	// for SwiftGen/R.swift teams. Only the hidden .palbase/config.json
	// (project ref + URL cache) stays out of git.
	if err := ensureGitignored(".gitignore", ".palbase/config.json"); err != nil {
		fmt.Fprintf(w, "  (gitignore not updated: %v)\n", err)
	}
	fmt.Fprintf(w, "✓ wrote %s (%d operation(s))\n", outFile, len(ops))
	fmt.Fprintf(w, "✓ wrote %s (config)\n", jsonPath)
	return nil
}

// writeGeneratedConfigJSON writes the config struct as JSON next to the
// Swift file. The Palbe SDK reads this from Bundle.main at
// `pb.configure()` time, so the app's call site stays a single line
// without a struct literal at the call site.
//
// Field names match PalBackendGeneratedConfig's JSON key naming
// (snake_case for cross-language friendliness; Swift's
// .convertFromSnakeCase decoder handles it).
func writeGeneratedConfigJSON(path string, cfg swiftGeneratedConfig) error {
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}
	body := map[string]any{
		"url":     cfg.URL,
		"api_key": cfg.APIKey,
		"branch":  branch,
	}
	// Surface OAuth provider availability so the SDK can run
	// `pb.auth.signInWithGoogle()` zero-arg (Bundle.main reads this
	// JSON at startup, hands the values to GoogleSignIn at call
	// time). Apple's block is just `enabled: true` because the iOS
	// flow doesn't need client_id on the device. Omitted entirely
	// when the project has no enabled+configured providers — keeps
	// the JSON minimal for projects that don't use OAuth.
	if cfg.OAuth != nil {
		body["oauth"] = cfg.OAuth
	}
	bytes, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal generated config: %w", err)
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return os.WriteFile(path, append(bytes, '\n'), 0o644)
}

func ensureIOSGeneratedStub(outFile string) error {
	if _, err := os.Stat(outFile); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check %s: %w", outFile, err)
	}
	if dir := filepath.Dir(outFile); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	// Minimal Swift stub — empty so the Xcode target compiles before the
	// first real codegen run. Re-running `palbase mobile codegen ios`
	// overwrites this with typed methods + writes PalbaseGenerated.json
	// next to it (the actual runtime config the Palbe SDK loads from
	// Bundle.main).
	stub := `// Generated by palbase mobile link ios. Re-run palbase mobile codegen ios.
// Once codegen runs against your project, this file gains typed pb.* methods
// and PalbaseGenerated.json (in the same directory) carries the runtime config.
import Foundation
`
	if err := os.WriteFile(outFile, []byte(stub), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outFile, err)
	}
	// Empty JSON stub so pb.configure() doesn't crash before first
	// codegen — fields are empty strings; Palbe SDK fatalErrors with a
	// clear message ("run palbase mobile codegen ios") on empty url.
	jsonPath := filepath.Join(filepath.Dir(outFile), "PalbaseGenerated.json")
	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		stubJSON := `{
  "url": "",
  "api_key": "",
  "branch": "main"
}
`
		if err := os.WriteFile(jsonPath, []byte(stubJSON), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", jsonPath, err)
		}
	}
	// The stub lives in the visible, committed Palbase/Generated/ folder
	// (synchronized Xcode folder ref) — not gitignored. Only the hidden
	// .palbase/config.json cache stays out of git.
	return ensureGitignored(".gitignore", ".palbase/config.json")
}

func setupIOSXcodeProject(projectFlag, targetFlag string) (projectPath, targetName string, changed bool, err error) {
	projectPath, err = resolveXcodeProject(projectFlag)
	if err != nil {
		return "", "", false, err
	}
	pbxPath := filepath.Join(projectPath, "project.pbxproj")
	data, err := os.ReadFile(pbxPath)
	if err != nil {
		return "", "", false, fmt.Errorf("read %s: %w", pbxPath, err)
	}
	next, targetName, changed, err := patchXcodeProject(string(data), targetFlag)
	if err != nil {
		return "", "", false, err
	}

	// Sign in with Apple capability. Always wired — the SDK's
	// `pb.auth.signInWithApple()` is zero-config (server-side id_token
	// exchange), so any project that enables the Apple provider in
	// Studio can use it with no per-app code. Two parts: an
	// entitlements file on disk + CODE_SIGN_ENTITLEMENTS pointing at
	// it in EVERY build config (Debug AND Release — setting it in one
	// silently fails the other build path).
	patched, appleChanged, appleErr := wireAppleSignIn(next, projectPath, targetFlag)
	if appleErr != nil {
		// Non-fatal: the codegen + Run Script wiring already
		// succeeded. Surface the capability failure but don't abort
		// the whole setup — the customer can add the capability in
		// Xcode if our splice didn't fit their project shape.
		fmt.Fprintf(os.Stdout, "  (Sign in with Apple capability not wired: %v)\n", appleErr)
	} else {
		next = patched
		changed = changed || appleChanged
	}

	if changed {
		if err := os.WriteFile(pbxPath, []byte(next), 0o644); err != nil {
			return "", "", false, fmt.Errorf("write %s: %w", pbxPath, err)
		}
	}
	return projectPath, targetName, changed, nil
}

// wireAppleSignIn writes the entitlements file and sets
// CODE_SIGN_ENTITLEMENTS on the app target's build configs. Returns
// the patched pbxproj + whether the pbxproj changed (the entitlements
// file write is tracked separately but folded into the changed flag).
func wireAppleSignIn(pbx, projectPath, targetFlag string) (string, bool, error) {
	targets := parseXcodeTargets(pbx)
	target, err := chooseXcodeTarget(targets, targetFlag)
	if err != nil {
		return pbx, false, err
	}
	// Entitlements file lives at SRCROOT (the dir containing the
	// .xcodeproj), named after the target. Matches the demo's
	// INFOPLIST_FILE = Info.plist convention (SRCROOT-relative).
	srcroot := filepath.Dir(projectPath)
	entFileName := target.name + ".entitlements"
	entPath := filepath.Join(srcroot, entFileName)
	fileChanged, err := ensureAppleSignInEntitlement(entPath)
	if err != nil {
		return pbx, false, err
	}
	configIDs := appTargetConfigIDs(pbx, target)
	if len(configIDs) == 0 {
		return pbx, fileChanged, fmt.Errorf("no build configurations found for target %q", target.name)
	}
	next, settingChanged := ensureCodeSignEntitlementsSetting(pbx, entFileName, configIDs)
	return next, fileChanged || settingChanged, nil
}

func resolveXcodeProject(projectFlag string) (string, error) {
	if projectFlag != "" {
		if filepath.Ext(projectFlag) != ".xcodeproj" {
			return "", fmt.Errorf("--project must point to a .xcodeproj")
		}
		if _, err := os.Stat(filepath.Join(projectFlag, "project.pbxproj")); err != nil {
			return "", fmt.Errorf("check %s/project.pbxproj: %w", projectFlag, err)
		}
		return projectFlag, nil
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("read cwd: %w", err)
	}
	var projects []string
	for _, e := range entries {
		if e.IsDir() && filepath.Ext(e.Name()) == ".xcodeproj" {
			projects = append(projects, e.Name())
		}
	}
	switch len(projects) {
	case 0:
		return "", fmt.Errorf("no .xcodeproj found in cwd; pass --project")
	case 1:
		return projects[0], nil
	default:
		return "", fmt.Errorf("multiple .xcodeproj files found; pass --project")
	}
}

type xcodeTarget struct {
	id          string
	name        string
	productType string
	phases      []string
	// syncedGroups are the ids in this target's fileSystemSynchronizedGroups
	// array (Xcode 16 PBXFileSystemSynchronizedRootGroup references). Empty
	// for a classic project that wires sources via per-file PBXBuildFile.
	syncedGroups []string
}

func patchXcodeProject(pbx, requestedTarget string) (string, string, bool, error) {
	targets := parseXcodeTargets(pbx)
	if len(targets) == 0 {
		return "", "", false, fmt.Errorf("no PBXNativeTarget found in Xcode project")
	}
	target, err := chooseXcodeTarget(targets, requestedTarget)
	if err != nil {
		return "", "", false, err
	}
	// Sanity-check the target is a real compile target. The synchronized
	// folder auto-enrols its .swift into Compile Sources, so we no longer
	// splice into this phase — but a target with no Sources phase isn't an
	// app we can wire codegen into.
	hasSources := false
	for _, phaseID := range target.phases {
		if objectContains(pbx, phaseID, "isa = PBXSourcesBuildPhase;") {
			hasSources = true
		}
	}
	if !hasSources {
		return "", "", false, fmt.Errorf("target %q has no Swift sources build phase", target.name)
	}

	shellPhaseID := findObjectIDContaining(pbx, "name = \"Palbase Codegen iOS\";")
	if shellPhaseID == "" {
		shellPhaseID = xcodeObjectID("palbase-ios-shell-phase")
	}
	syncGroupID := xcodeObjectID("palbase-ios-sync-folder")

	next := pbx
	changed := false
	var did bool

	// The generated code lives INSIDE the app target's own source folder
	// (e.g. palbase/Generated), using the project's exact casing. Where to
	// wire it depends on the target shape.
	appFolder, appSynced := detectAppSourceFolder(next, target)
	genDir := iosGeneratedDirFor(appFolder)

	// MIGRATION: strip a STALE synchronized folder we emitted in a prior CLI
	// version — but ONLY when it's at the wrong path. v0.3.35/0.3.36 emitted
	// our syncGroupID at path "Palbase/Generated" parented to the main group;
	// on case-insensitive macOS that aliased the app's own "palbase/" folder,
	// so files surfaced TWICE. We remove that stray (object + child ref +
	// target entry) so it heals on re-link. We must NOT strip our own folder
	// when it's already at the correct genDir (the classic case) — that would
	// churn (remove+re-add) and break idempotency. So: strip only when an
	// existing syncGroupID object's path differs from genDir.
	if existing := objectBlock(next, syncGroupID); existing != "" &&
		strings.Trim(xcodeValue(existing, "path"), `"`) != genDir {
		next, did = stripGeneratedSyncFolder(next, target.id, syncGroupID)
		changed = changed || did
	}

	if appSynced {
		// Modern Xcode 16: the app target's source folder is ITSELF a
		// synchronized folder, so a nested <appFolder>/Generated/ is picked
		// up automatically from disk — we add NOTHING to the pbxproj for the
		// files. One natural appearance, inside the app folder, zero stray
		// references. (Codegen writes the files there; see resolveIOSGeneratedDir.)
	} else {
		// Classic project (no synced source folder): wire our own synchronized
		// FOLDER reference at <appFolder>/Generated and attach it to the
		// target. Bump objectVersion to 77 (synced folders need >= 70). Parent
		// it under the target's own group when we can find it (so it nests in
		// the app, not at project root); fall back to the root group otherwise.
		next, did = ensureObjectVersion(next, 77)
		changed = changed || did
		next, did = ensureSyncedFolderGroup(next, syncGroupID, genDir)
		changed = changed || did
		next, did = ensureFileSystemSynchronizedGroupInTarget(next, target.id, syncGroupID)
		changed = changed || did
		parentID := findClassicTargetGroup(next, appFolder)
		if parentID == "" {
			parentID = findRootPBXGroup(next)
		}
		if parentID != "" {
			beforeAttach := next
			next = appendChildToGroup(next, parentID, syncGroupID, "Generated")
			if next != beforeAttach {
				changed = true
			}
		}
	}

	next, did = ensurePBXShellScriptPhase(next, shellPhaseID, genDir)
	changed = changed || did
	next, did = ensureBuildPhaseInTarget(next, target.id, shellPhaseID)
	changed = changed || did
	return next, target.name, changed, nil
}

// findClassicTargetGroup returns the id of the PBXGroup whose `path` equals
// `folder` (the app target's source dir) — used to parent the generated
// folder inside the app group on a classic (non-synced) project. Returns ""
// when no such group exists.
func findClassicTargetGroup(pbx, folder string) string {
	if folder == "" {
		return ""
	}
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.Contains(t, "= {") || !strings.HasSuffix(t, "= {") {
			continue
		}
		id := strings.Fields(t)[0]
		block := objectBlock(pbx, id)
		if block == "" || !strings.Contains(block, "isa = PBXGroup;") {
			continue
		}
		p := strings.Trim(xcodeValue(block, "path"), `"`)
		if p == folder {
			return id
		}
		_ = i
	}
	return ""
}

// stripGeneratedSyncFolder removes a synchronized FOLDER reference we emitted
// in a prior CLI version: the object block, its child ref under any group,
// and its entry in the target's fileSystemSynchronizedGroups. Idempotent /
// no-op when absent. Heals a v0.3.36-linked project (double appearance) on
// re-link rather than leaving the stray root reference in place.
func stripGeneratedSyncFolder(pbx, targetID, syncID string) (string, bool) {
	_ = targetID // kept for call-site symmetry; the line shape below is global
	before := pbx
	pbx = removeObjectBlock(pbx, syncID)
	// removeChildReference drops every `<syncID> /* Generated */,` line —
	// which covers BOTH the root-group child ref AND the target's
	// fileSystemSynchronizedGroups entry (identical line shape).
	pbx = removeChildReference(pbx, syncID, "Generated")
	return pbx, pbx != before
}

// removeChildReference drops a `<id> /* <name> */,` child line from any
// group's children list or fileSystemSynchronizedGroups array (mirror of
// removePhaseReference for group children).
func removeChildReference(pbx, objectID, name string) string {
	needle := objectID + " /* " + name + " */,"
	var out []string
	for _, line := range strings.Split(pbx, "\n") {
		if strings.TrimSpace(line) == needle {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ensureObjectVersion bumps the project's `objectVersion = N;` line up to
// `want` when N < want. Synchronized FOLDER references need
// objectVersion >= 70; we always emit 77 (Xcode 16.1+, what current Xcode
// round-trips) — emitting the synced-folder ISA while leaving an older
// objectVersion produces an artifact Xcode rejects. archiveVersion is
// left untouched. Idempotent: when N >= want the body is returned
// unchanged.
func ensureObjectVersion(pbx string, want int) (string, bool) {
	re := regexp.MustCompile(`(?m)^(\s*)objectVersion = (\d+);`)
	m := re.FindStringSubmatchIndex(pbx)
	if m == nil {
		return pbx, false
	}
	current, err := strconv.Atoi(pbx[m[4]:m[5]])
	if err != nil || current >= want {
		return pbx, false
	}
	indent := pbx[m[2]:m[3]]
	replacement := indent + "objectVersion = " + strconv.Itoa(want) + ";"
	return pbx[:m[0]] + replacement + pbx[m[1]:], true
}

// ensureSyncedFolderGroup emits a PBXFileSystemSynchronizedRootGroup
// object pointing at `path` (e.g. Palbase/Generated), inside its own
// `/* Begin/End PBXFileSystemSynchronizedRootGroup section */`, creating
// the section if absent. Real Xcode drops the exceptions/
// explicitFileTypes/explicitFolders keys when empty, so we omit them.
//
// Idempotent: if OUR synchronized root group (the one with this exact
// deterministic groupID) already exists, returns the body unchanged. We key
// the guard on groupID — and confirm that object is actually our synced
// folder at `path` — rather than two independent global substring checks:
// a project may legitimately carry an UNRELATED PBXFileSystemSynchronizedRootGroup
// plus some other object whose path is Palbase/Generated, which would make a
// split "isa present" && "path present" guard falsely skip emission while the
// caller still wires the target's fileSystemSynchronizedGroups → a dangling
// "reference to unknown object". Scoping to groupID's own block avoids that.
func ensureSyncedFolderGroup(pbx, groupID, path string) (string, bool) {
	if existing := objectBlock(pbx, groupID); existing != "" &&
		strings.Contains(existing, "isa = PBXFileSystemSynchronizedRootGroup;") &&
		strings.Contains(existing, "path = "+path+";") {
		return pbx, false
	}
	block := "\t\t" + groupID + " /* Generated */ = {\n" +
		"\t\t\tisa = PBXFileSystemSynchronizedRootGroup;\n" +
		"\t\t\tpath = " + path + ";\n" +
		"\t\t\tsourceTree = \"<group>\";\n" +
		"\t\t};\n"
	if strings.Contains(pbx, "/* End PBXFileSystemSynchronizedRootGroup section */") {
		return insertBeforeMarker(pbx, "/* End PBXFileSystemSynchronizedRootGroup section */", block)
	}
	// No section yet — create one. Anchor it before the PBXFrameworksBuildPhase
	// section (alphabetical neighbour in Xcode's canonical ordering); fall
	// back to the PBXGroup section, then the objects close, so we always
	// land somewhere valid regardless of which sections the project has.
	section := "/* Begin PBXFileSystemSynchronizedRootGroup section */\n" +
		block +
		"/* End PBXFileSystemSynchronizedRootGroup section */\n\n"
	for _, anchor := range []string{
		"/* Begin PBXFrameworksBuildPhase section */",
		"/* Begin PBXGroup section */",
		"/* Begin PBXNativeTarget section */",
	} {
		if next, ok := insertBeforeMarker(pbx, anchor, section); ok {
			return next, true
		}
	}
	return pbx, false
}

// ensureFileSystemSynchronizedGroupInTarget adds the synchronized folder
// id to the target's PBXNativeTarget object under
// `fileSystemSynchronizedGroups = ( … );` (a sibling of buildPhases /
// dependencies). Creates the key if absent; appends idempotently if
// present.
func ensureFileSystemSynchronizedGroupInTarget(pbx, targetID, syncGroupID string) (string, bool) {
	block := objectBlock(pbx, targetID)
	if block == "" {
		return pbx, false
	}
	entry := syncGroupID + " /* Generated */,"
	if strings.Contains(block, syncGroupID) {
		return pbx, false // already attached
	}

	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), targetID+" ") || !strings.Contains(line, "= {") {
			continue
		}
		// Find the bounds of this target object.
		end := i + 1
		for ; end < len(lines); end++ {
			if strings.TrimSpace(lines[end]) == "};" {
				break
			}
		}
		// If the array already exists, append into it.
		for j := i + 1; j < end; j++ {
			if strings.TrimSpace(lines[j]) == "fileSystemSynchronizedGroups = (" {
				insert := "\t\t\t\t" + entry
				newLines := append([]string{}, lines[:j+1]...)
				newLines = append(newLines, insert)
				newLines = append(newLines, lines[j+1:]...)
				return strings.Join(newLines, "\n"), true
			}
		}
		// No array yet — create it right after the target's isa line so it
		// sits as a sibling of buildPhases/dependencies.
		for j := i + 1; j < end; j++ {
			if strings.TrimSpace(lines[j]) == "isa = PBXNativeTarget;" {
				insert := "\t\t\tfileSystemSynchronizedGroups = (\n" +
					"\t\t\t\t" + entry + "\n" +
					"\t\t\t);"
				newLines := append([]string{}, lines[:j+1]...)
				newLines = append(newLines, insert)
				newLines = append(newLines, lines[j+1:]...)
				return strings.Join(newLines, "\n"), true
			}
		}
	}
	return pbx, false
}

// findRootPBXGroup returns the id of the first PBXGroup that has no
// `path` attribute and a `sourceTree = "<group>";` — i.e. the project's
// root group. Returns "" if it can't tell.
//
// Xcode templates spell the root group as `<id> = {` with no `/* … */`
// comment (every other PBX object carries the comment). That single
// shape tells us the line opens the root; the 15-line lookahead just
// confirms it's actually a PBXGroup whose sourceTree is "<group>" and
// has no `path` (a child group like /* palbase */ has both `path` and
// the comment).
func findRootPBXGroup(pbx string) string {
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasSuffix(trimmed, "= {") {
			continue
		}
		if strings.Contains(trimmed, "/*") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 || fields[1] != "=" {
			continue
		}
		blob := strings.Join(lines[i:min(i+15, len(lines))], "\n")
		if strings.Contains(blob, "isa = PBXGroup;") &&
			strings.Contains(blob, `sourceTree = "<group>";`) &&
			!strings.Contains(blob, "\tpath = ") {
			return fields[0]
		}
	}
	return ""
}

// appendChildToGroup adds a child reference (e.g. `<id> /* Generated */,`)
// to the named PBXGroup's children list, idempotently.
func appendChildToGroup(pbx, groupID, childID, childName string) string {
	needle := childID + " /* " + childName + " */,"
	if block := objectBlock(pbx, groupID); strings.Contains(block, needle) {
		return pbx
	}
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		if !strings.Contains(line, groupID+" = {") && !strings.Contains(line, groupID+" /*") {
			continue
		}
		for j := i; j < len(lines) && j < i+20; j++ {
			if strings.Contains(lines[j], "children = (") {
				insert := "\t\t\t\t" + needle
				newLines := append([]string{}, lines[:j+1]...)
				newLines = append(newLines, insert)
				newLines = append(newLines, lines[j+1:]...)
				return strings.Join(newLines, "\n")
			}
		}
	}
	return pbx
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseXcodeTargets(pbx string) []xcodeTarget {
	lines := strings.Split(pbx, "\n")
	var targets []xcodeTarget
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.Contains(line, "= {") || !strings.Contains(line, "/*") {
			continue
		}
		id := strings.Fields(line)[0]
		j := i + 1
		for ; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "};" {
				break
			}
		}
		if j >= len(lines) {
			continue
		}
		block := strings.Join(lines[i:j+1], "\n")
		if !strings.Contains(block, "isa = PBXNativeTarget;") {
			i = j
			continue
		}
		t := xcodeTarget{id: id, name: xcodeCommentName(line), productType: xcodeValue(block, "productType")}
		if name := xcodeValue(block, "name"); name != "" {
			t.name = strings.Trim(name, `"`)
		}
		t.phases = xcodeListValue(block, "buildPhases")
		t.syncedGroups = xcodeListValue(block, "fileSystemSynchronizedGroups")
		targets = append(targets, t)
		i = j
	}
	return targets
}

// detectAppSourceFolder finds the on-disk source folder for a target and
// whether that folder is an Xcode 16 synchronized folder.
//
//   - Synced (Xcode 16): the target lists its source folder(s) in
//     fileSystemSynchronizedGroups; each is a PBXFileSystemSynchronizedRootGroup
//     whose `path` is the folder (e.g. "palbase"). We pick the group whose path
//     equals the target name (case-insensitively) when present, else the first
//     — that's the app's primary source folder. Returns (path, true).
//   - Classic (no synced groups): we can't reliably read a folder from the
//     pbxproj group graph here, so we fall back to the target name (Xcode
//     conventionally names the app's source folder after the target). Returns
//     (targetName, false).
//
// The returned path preserves the project's exact casing — critical on
// case-insensitive macOS, where writing "Palbase/" into an existing "palbase/"
// would alias and double-surface.
func detectAppSourceFolder(pbx string, target xcodeTarget) (folder string, synced bool) {
	// Our OWN generated synced folder (classic-project case) is registered in
	// the target's fileSystemSynchronizedGroups too — skip it so re-linking
	// doesn't mistake "App/Generated" for the app source folder and nest into
	// "App/Generated/Generated".
	ownSyncID := xcodeObjectID("palbase-ios-sync-folder")
	var firstPath string
	for _, gid := range target.syncedGroups {
		if gid == ownSyncID {
			continue
		}
		block := objectBlock(pbx, gid)
		if block == "" || !strings.Contains(block, "isa = PBXFileSystemSynchronizedRootGroup;") {
			continue
		}
		p := strings.Trim(xcodeValue(block, "path"), `"`)
		if p == "" {
			continue
		}
		if firstPath == "" {
			firstPath = p
		}
		if strings.EqualFold(p, target.name) {
			return p, true
		}
	}
	if firstPath != "" {
		return firstPath, true
	}
	// Classic project: no synced folder. Use the target name as the source dir.
	return target.name, false
}

// iosGeneratedDirFor returns the generated-output dir for a detected app
// source folder: "<folder>/Generated". Empty folder → the bare fallback.
func iosGeneratedDirFor(folder string) string {
	if folder == "" {
		return fallbackIOSGeneratedDir
	}
	return folder + "/Generated"
}

// resolveIOSGeneratedDir resolves where codegen should write, by reading the
// project in cwd and detecting the (first/app) target's source folder. Used
// by `mobile codegen ios` (standalone) and as the default for `mobile link
// ios`. Falls back to a bare "Generated" (never the case-colliding capital
// "Palbase/Generated") when no project/target is resolvable.
func resolveIOSGeneratedDir() string {
	// Explicit override wins. The Xcode build phase sets this from
	// $SCRIPT_OUTPUT_FILE_0 (the declared outputPath, e.g.
	// ".../palbase/Generated/PalbaseGenerated.swift") so codegen writes EXACTLY
	// where Xcode's user-script sandbox grants write permission. Without it,
	// resolveXcodeProject below does os.ReadDir(".") to find the .xcodeproj —
	// but the user-script sandbox blocks reading the project directory during a
	// build, so the lookup fails and we fall back to a bare "Generated" at cwd,
	// which the sandbox then refuses to mkdir ("operation not permitted"). The
	// env var sidesteps runtime project discovery entirely.
	if dir := strings.TrimSpace(os.Getenv("PALBASE_IOS_GENERATED_DIR")); dir != "" {
		return dir
	}
	projectPath, err := resolveXcodeProject("")
	if err != nil {
		return fallbackIOSGeneratedDir
	}
	pbx, err := os.ReadFile(filepath.Join(projectPath, "project.pbxproj"))
	if err != nil {
		return fallbackIOSGeneratedDir
	}
	targets := parseXcodeTargets(string(pbx))
	target, err := chooseXcodeTarget(targets, "")
	if err != nil {
		return fallbackIOSGeneratedDir
	}
	folder, _ := detectAppSourceFolder(string(pbx), target)
	return iosGeneratedDirFor(folder)
}

func chooseXcodeTarget(targets []xcodeTarget, requested string) (xcodeTarget, error) {
	if requested != "" {
		for _, t := range targets {
			if t.name == requested {
				return t, nil
			}
		}
		return xcodeTarget{}, fmt.Errorf("target %q not found", requested)
	}
	for _, t := range targets {
		if strings.Contains(t.productType, "com.apple.product-type.application") {
			return t, nil
		}
	}
	return targets[0], nil
}

func xcodeCommentName(line string) string {
	start := strings.Index(line, "/*")
	end := strings.Index(line, "*/")
	if start == -1 || end == -1 || end <= start+2 {
		return ""
	}
	return strings.TrimSpace(line[start+2 : end])
}

func xcodeValue(block, key string) string {
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		prefix := key + " = "
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, ";") {
			return strings.TrimSuffix(strings.TrimPrefix(line, prefix), ";")
		}
	}
	return ""
}

func xcodeListValue(block, key string) []string {
	lines := strings.Split(block, "\n")
	var out []string
	inList := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == key+" = (" {
			inList = true
			continue
		}
		if inList && trimmed == ");" {
			break
		}
		if !inList || trimmed == "" {
			continue
		}
		out = append(out, strings.Fields(trimmed)[0])
	}
	return out
}

func objectContains(pbx, objectID, needle string) bool {
	block := objectBlock(pbx, objectID)
	return strings.Contains(block, needle)
}

func objectBlock(pbx, objectID string) string {
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), objectID+" ") && strings.Contains(line, "= {") {
			j := i + 1
			for ; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "};" {
					break
				}
			}
			if j < len(lines) {
				return strings.Join(lines[i:j+1], "\n")
			}
		}
	}
	return ""
}

// findPhaseIDByName returns the object id of the PBXShellScriptBuildPhase
// whose `name = "<n>";` matches the given name. Returns "" when no such
// phase exists. Used by ensurePBXShellScriptPhase to find a stale phase
// emitted by an earlier CLI version so we can re-write it with the
// current canonical block (inputs/outputs/script kept in lock-step).
func findPhaseIDByName(pbx, name string) string {
	needle := "name = \"" + name + "\";"
	return findObjectIDContaining(pbx, needle)
}

// removeObjectBlock strips the full `<id> /* … */ = { … };` definition
// for the given object id from the pbxproj body. Returns the body
// unchanged if the id isn't present. Pair with removePhaseReference to
// fully detach a build phase before re-emitting it.
func removeObjectBlock(pbx, objectID string) string {
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		if !strings.Contains(line, objectID+" ") || !strings.Contains(line, "= {") {
			continue
		}
		// Scan forward to the matching closing "};".
		j := i + 1
		for ; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "};" {
				break
			}
		}
		if j >= len(lines) {
			return pbx
		}
		// Drop lines [i, j] inclusive. Preserve surrounding blank lines.
		return strings.Join(append(append([]string{}, lines[:i]...), lines[j+1:]...), "\n")
	}
	return pbx
}

// removePhaseReference removes the build-phases entry that references
// the given shell-phase id (lines like `<id> /* <name> */,` inside
// `buildPhases = ( … );` blocks). Without this, the phase is gone from
// the section but the target still tries to dispatch it and Xcode errors
// with "Reference to unknown object".
func removePhaseReference(pbx, objectID, name string) string {
	needle := objectID + " /* " + name + " */,"
	lines := strings.Split(pbx, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, needle) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func findObjectIDContaining(pbx, needle string) string {
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		if !strings.Contains(line, needle) {
			continue
		}
		for j := i; j >= 0 && j >= i-20; j-- {
			trimmed := strings.TrimSpace(lines[j])
			if strings.Contains(trimmed, "= {") {
				fields := strings.Fields(trimmed)
				if len(fields) > 0 {
					return fields[0]
				}
			}
		}
	}
	return ""
}

func xcodeObjectID(seed string) string {
	sum := sha1.Sum([]byte("palbase:" + seed))
	return strings.ToUpper(hex.EncodeToString(sum[:]))[:24]
}

func ensurePBXShellScriptPhase(pbx, shellPhaseID, genDir string) (string, bool) {
	// Self-healing semantics: if a phase with our name already exists,
	// compare against the canonical block we'd emit today. If they match
	// (idempotent), bail out — nothing to do. If they differ (older CLI
	// shipped a different block, e.g. before CLI-19 added .palbase/config.json
	// to inputPaths so Xcode's USER_SCRIPT_SANDBOXING permits the read),
	// strip the stale block + reference and fall through to re-emit the
	// current canonical one with the SAME id so any references stay valid.
	if existingID := findPhaseIDByName(pbx, "Palbase Codegen iOS"); existingID != "" {
		existing := objectBlock(pbx, existingID)
		canonical := palbaseCodegenPhaseBlock(existingID, genDir)
		if normalizePBXBlock(existing) == normalizePBXBlock(canonical) {
			return pbx, false
		}
		pbx = removeObjectBlock(pbx, existingID)
		pbx = removePhaseReference(pbx, existingID, "Palbase Codegen iOS")
		shellPhaseID = existingID // re-use the id so existing buildPhases refs stay valid
	}
	block := palbaseCodegenPhaseBlock(shellPhaseID, genDir)
	if strings.Contains(pbx, "/* End PBXShellScriptBuildPhase section */") {
		return insertBeforeMarker(pbx, "/* End PBXShellScriptBuildPhase section */", block)
	}
	section := "/* Begin PBXShellScriptBuildPhase section */\n" + block + "/* End PBXShellScriptBuildPhase section */\n\n"
	return insertBeforeMarker(pbx, "/* Begin PBXSourcesBuildPhase section */", section)
}

// palbaseCodegenPhaseBlock renders the canonical Palbase Codegen iOS
// PBXShellScriptBuildPhase definition. Kept here so both the initial
// emit (ensurePBXShellScriptPhase) and the "is it stale?" comparison
// (same function, idempotency check) read the same source of truth.
//
// inputPaths carries .palbase/config.json so Xcode's
// USER_SCRIPT_SANDBOXING grants the script read access — without it
// `palbase mobile codegen ios` errors "open .palbase/config.json:
// operation not permitted" inside sandbox-exec, since the sandbox
// profile only opens up explicitly-declared inputs plus the output
// path. The generated PalbaseGenerated.swift is already covered by
// outputPaths.
func palbaseCodegenPhaseBlock(shellPhaseID, genDir string) string {
	// Fail-soft: codegen failure (login expired, network down, ref
	// unlinked) prints a warning and lets the build continue with
	// whatever generated files are already on disk. Hard-failing the
	// build every time the CLI hiccups blocks Xcode workflow for
	// trivial reasons. The previous codegen output stays valid until
	// the customer re-logs in or fixes the underlying issue.
	// Xcode runs build phases with a minimal PATH that does NOT include
	// the user's login-shell PATH (~/.zshrc etc.), so a `palbase` installed
	// in Homebrew (/opt/homebrew/bin on Apple Silicon, /usr/local/bin on
	// Intel) or a Go/local bin is invisible to `command -v` and codegen
	// silently skips on a machine where `palbase` clearly works in the
	// terminal. Prepend the standard install locations so the lookup
	// succeeds; this keeps the fail-soft `command -v` guard meaningful (it
	// now only trips when the CLI is genuinely absent, not merely off-PATH).
	// The codegen line exports PALBASE_IOS_GENERATED_DIR from Xcode's declared
	// outputPath ($SCRIPT_OUTPUT_FILE_0 = .../palbase/Generated/Palbase…swift).
	// Under the user-script sandbox, codegen can't os.ReadDir(".") to discover
	// the .xcodeproj, so it would fall back to a bare "Generated" at cwd and fail
	// with "mkdir Generated: operation not permitted". The declared output dir is
	// the only place the sandbox grants write access — point codegen straight at
	// it (resolveIOSGeneratedDir reads this env first).
	script := "echo \"Palbase Codegen iOS: running\"\n" +
		"cd \"${SRCROOT:-.}\"\n" +
		"export PATH=\"/opt/homebrew/bin:/usr/local/bin:$HOME/go/bin:$HOME/.local/bin:$PATH\"\n" +
		"if [ -n \"${SCRIPT_OUTPUT_FILE_0:-}\" ]; then\n" +
		"  export PALBASE_IOS_GENERATED_DIR=\"$(dirname \"$SCRIPT_OUTPUT_FILE_0\")\"\n" +
		"fi\n" +
		"if ! command -v palbase >/dev/null 2>&1; then\n" +
		"  echo \"warning: palbase CLI not found; skipping Palbase iOS codegen\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"palbase --version\n" +
		"if ! palbase mobile codegen ios; then\n" +
		"  echo \"warning: palbase mobile codegen ios failed — generated types may be STALE (codegen waits out a cold backend wake; this means a real error: check your login + project link, or the backend never woke)\"\n" +
		"fi\n" +
		"exit 0\n"
	return "\t\t" + shellPhaseID + " /* Palbase Codegen iOS */ = {\n" +
		"\t\t\tisa = PBXShellScriptBuildPhase;\n" +
		// 0x7FFFFFFF — the canonical "all build actions" mask Xcode writes for
		// shell-script phases. Some older CLI builds (and hand-edits) left this
		// at 12, which is not a reliable value for a normal build.
		"\t\t\tbuildActionMask = 2147483647;\n" +
		// alwaysOutOfDate = 1 is the load-bearing line: it's the pbxproj form of
		// unchecking "Based on dependency analysis". Without it, Xcode compares
		// the declared outputPaths' mtimes and SKIPS the phase when they look
		// up-to-date — so codegen would silently NOT run on most builds. We want
		// it to run every build (the CLI is cheap and idempotent: it re-reads the
		// spec and only rewrites the generated files, picking up local-serve vs
		// remote automatically each time). With this set, outputPaths are kept
		// only so Xcode grants the sandbox write permission and downstream phases
		// know these files are produced — they no longer gate execution.
		"\t\t\talwaysOutOfDate = 1;\n" +
		"\t\t\tfiles = (\n\t\t\t);\n" +
		"\t\t\tinputFileListPaths = (\n\t\t\t);\n" +
		"\t\t\tinputPaths = (\n" +
		"\t\t\t\t\"$(SRCROOT)/.palbase/config.json\",\n" +
		"\t\t\t);\n" +
		"\t\t\tname = \"Palbase Codegen iOS\";\n" +
		"\t\t\toutputFileListPaths = (\n\t\t\t);\n" +
		"\t\t\toutputPaths = (\n" +
		"\t\t\t\t\"$(SRCROOT)/" + genDir + "/" + iosGeneratedSwiftName + "\",\n" +
		"\t\t\t\t\"$(SRCROOT)/" + genDir + "/" + iosGeneratedJSONName + "\",\n" +
		"\t\t\t);\n" +
		"\t\t\trunOnlyForDeploymentPostprocessing = 0;\n" +
		"\t\t\tshellPath = /bin/sh;\n" +
		"\t\t\tshellScript = " + fmt.Sprintf("%q", script) + ";\n" +
		"\t\t};\n"
}

// normalizePBXBlock canonicalises whitespace so two textually-different
// blocks that are semantically identical compare equal. Trim every
// line + drop blank lines is enough for the idempotency check (every
// emit-site uses tabs the same way; this absorbs the rare diff from
// users hand-editing the project).
func normalizePBXBlock(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return b.String()
}

func ensureBuildPhaseInTarget(pbx, targetID, shellPhaseID string) (string, bool) {
	block := objectBlock(pbx, targetID)
	if block == "" || strings.Contains(block, shellPhaseID) || strings.Contains(block, "Palbase Codegen iOS") {
		return pbx, false
	}
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), targetID+" ") && strings.Contains(line, "= {") {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "buildPhases = (" {
					insert := "\t\t\t\t" + shellPhaseID + " /* Palbase Codegen iOS */,"
					lines = append(lines[:j+1], append([]string{insert}, lines[j+1:]...)...)
					return strings.Join(lines, "\n"), true
				}
				if strings.TrimSpace(lines[j]) == "};" {
					break
				}
			}
		}
	}
	return pbx, false
}

func insertBeforeMarker(s, marker, insertion string) (string, bool) {
	idx := strings.Index(s, marker)
	if idx == -1 {
		return s, false
	}
	return s[:idx] + insertion + s[idx:], true
}

// ─────────────────────────────────────────────────────────────────────
// Adım B14 — `palbase types`
//
// Pulls the deployed `/openapi.json` for the project and writes
// `.palbase/openapi.json` + `.palbase/types.d.ts`. Both files are
// auto-generated and overwritten on every run; users edit the
// underlying `defineEndpoint` source instead.
// ─────────────────────────────────────────────────────────────────────

func newTypesCmd(r Resolvers) *cobra.Command {
	var refFlag string
	var envFlag string
	var outDir string
	var langFlag string
	cmd := &cobra.Command{
		Use:   "types",
		Short: "Pull the deployed OpenAPI spec + generate typed client code",
		Long: `Fetch the OpenAPI document from the deployed backend and generate
typed client code.

  --lang ts     (default) writes .palbase/openapi.json + .palbase/types.d.ts
  --lang swift  writes a Swift file of namespaced typed calls
                (pb.rooms.create(...)) for the Palbe iOS SDK

Swift codegen is self-contained (no Node/npx). The generated file
'import Palbe' and lowers each call to the SDK's public seam, so it
compiles in the consumer app target. Use it from an Xcode Run Script
build phase for automatic regeneration on every build:

  palbase types --lang swift \
    --out "$DERIVED_FILE_DIR/PalbaseEndpoints.swift" \
    --env "$([ "$CONFIGURATION" = Debug ] && echo local || echo remote)"

Re-run after every deploy to stay in sync with the live spec.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			switch langFlag {
			case "swift":
				out := outDir
				if out == ".palbase" || out == "" {
					out = "PalbaseEndpoints.swift" // swift default: a file, not a dir
				}
				return pullSwiftTypes(cmd.Context(), r.Studio(), r.Endpoints(), ref, envFlag, out, os.Stdout)
			case "ts", "":
				if outDir == "" {
					outDir = ".palbase"
				}
				return pullTypesTo(cmd.Context(), r.Studio(), r.Endpoints(), ref, envFlag, outDir, os.Stdout)
			default:
				return fmt.Errorf("unknown --lang %q (expected ts|swift)", langFlag)
			}
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&envFlag, "env", "remote", "Spec source: remote (Kong gateway) | local (palbase serve on localhost:4003)")
	cmd.Flags().StringVar(&langFlag, "lang", "ts", "Output language: ts | swift")
	cmd.Flags().StringVar(&outDir, "out", ".palbase", "Output: dir for ts (.palbase), file for swift (PalbaseEndpoints.swift)")
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

// pullTypesTo does the actual work — fetch /openapi.json, write JSON,
// shell out to `npx openapi-typescript` for the .d.ts. Failures emit a
// warning but don't block; the JSON file alone is useful (Studio,
// Postman). Augmentation of `interface BackendEndpoints` is what
// drives the typed `pb.backend.call(name, input)` API.
// pullSwiftTypes fetches the OpenAPI spec and generates a Swift file of
// namespaced typed calls for the Palbe iOS SDK. Self-contained — no npx.
func pullSwiftTypes(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, env, outFile string, w io.Writer) error {
	specURL, apiKey, err := openAPIURL(ctx, sc, endpoints, ref, env)
	if err != nil {
		return err
	}
	specBytes, err := fetchRemoteOpenAPISpec(ctx, specURL, apiKey, w)
	if err != nil {
		return err
	}
	ops, err := parseOpenAPIForSwift(specBytes)
	if err != nil {
		return err
	}
	swift := emitSwift(ops)
	if dir := filepath.Dir(outFile); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(outFile, []byte(swift), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outFile, err)
	}
	fmt.Fprintf(w, "✓ wrote %s (%d operation(s))\n", outFile, len(ops))
	return nil
}

func pullTypesTo(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, env, outDir string, w io.Writer) error {
	specURL, apiKey, err := openAPIURL(ctx, sc, endpoints, ref, env)
	if err != nil {
		return err
	}

	specBytes, err := fetchRemoteOpenAPISpec(ctx, specURL, apiKey, w)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	jsonPath := filepath.Join(outDir, "openapi.json")
	if err := writeAutogenFile(jsonPath, specBytes); err != nil {
		return fmt.Errorf("write %s: %w", jsonPath, err)
	}
	fmt.Fprintf(w, "✓ wrote %s\n", jsonPath)

	// .palbase/types.d.ts via openapi-typescript shell-out. Optional —
	// users without Node.js still get the JSON contract.
	tsPath := filepath.Join(outDir, "types.d.ts")
	if err := generateTypesDecl(ctx, jsonPath, tsPath); err != nil {
		fmt.Fprintf(w, "  (skipped types.d.ts: %v)\n", err)
	} else {
		fmt.Fprintf(w, "✓ wrote %s\n", tsPath)
	}

	// Ensure .palbase/ is gitignored so generated artifacts don't end
	// up in customer commits. Idempotent — append a single line if
	// neither `.palbase/` nor `.palbase` is already listed.
	if err := ensureGitignored(".gitignore", ".palbase/"); err != nil {
		fmt.Fprintf(w, "  (gitignore not updated: %v)\n", err)
	}

	return nil
}

// openAPIURL resolves the spec URL for the chosen environment AND the
// publishable key needed to fetch it.
//
// remote: routes through Kong, so the URL MUST use the branch
// endpoint_ref subdomain (apikey.reveal returns it) and the mode-aware
// public host (endpoints.PublicHost — `dev.palbase.studio` or
// `palbase.studio`). The old hard-coded `<bareRef>.dev.palbase.studio`
// 404-ed two ways: bare ref had no Kong route (tenant_not_found), and
// the host was wrong in prod. `palbase mobile codegen ios` already
// resolves this correctly via lookupBackendTarget; we route here too so
// both codegen paths agree on a single source of truth (and the typed
// path drops to a single tRPC roundtrip).
//
// Returns (specURL, apiKey, err) — for the local env the apiKey is "" by
// design (no auth on `palbase serve`'s localhost spec).
func openAPIURL(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, env string) (string, string, error) {
	switch env {
	case "remote", "":
		target, err := lookupBackendTarget(ctx, sc, endpoints, ref, "")
		if err != nil {
			return "", "", err
		}
		return target.URL + "/openapi.json", target.APIKey, nil
	case "local":
		return "http://localhost:4003/openapi.json", "", nil
	default:
		return "", "", fmt.Errorf("unknown --env %q (expected remote|local)", env)
	}
}

// fetchOAuthProviders calls palauth's public `/auth/oauth/providers`
// endpoint (anon-key authed, secret-free) and lowers the response
// into the swiftOAuthConfig we serialise into PalbaseGenerated.json.
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
		return nil, 0, fmt.Errorf("fetch %s: %d %s", specURL, resp.StatusCode, string(body))
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

// writeAutogenFile writes data to path with a header comment that
// surfaces "do not edit" when the file is opened. JSON files take a
// `// AUTO-GENERATED` line via a key in the JSON itself rather than a
// comment (JSON has no comments) — so we just write the bytes as-is
// and rely on the OpenAPI document's `info.description` from the
// backend SDK.
func writeAutogenFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// generateTypesDecl runs `npx openapi-typescript <jsonPath> -o <tsPath>`
// to produce a .d.ts file. Requires Node.js + npx on PATH; if absent
// the function returns an error and the caller logs a warning.
func generateTypesDecl(ctx context.Context, jsonPath, tsPath string) error {
	if _, err := exec.LookPath("npx"); err != nil {
		return fmt.Errorf("npx not on PATH (Node.js required for types.d.ts generation)")
	}
	cmd := exec.CommandContext(ctx, "npx", "--yes", "openapi-typescript@^7", jsonPath, "-o", tsPath)
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("openapi-typescript: %w", err)
	}
	// Prepend the AUTO-GENERATED header so editors flag it.
	existing, err := os.ReadFile(tsPath)
	if err != nil {
		return err
	}
	header := []byte("// AUTO-GENERATED FROM YOUR DEPLOYED BACKEND. DO NOT EDIT — RUN 'palbase types' TO REFRESH.\n\n")
	return os.WriteFile(tsPath, append(header, existing...), 0o644)
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
