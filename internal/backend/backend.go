// Package backend provides the top-level backend lifecycle commands
// (pull / push / serve / list / rollback / status / types). palbase IS the
// backend CLI — there is no `backend` parent command. These cover the full
// developer loop for the per-project backend-runtime pod.
//
// All remote calls go through Studio's tRPC layer via the studio
// package — never directly to br-<ref> — so org-membership + the
// backend_enabled gate are enforced server-side.
package backend

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"embed"
	"encoding/base64"
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
// the ctx.<module> fetch wrappers (lockstep mirror of the backend-
// runtime image's internal/runtime/module-clients.js). Both files must
// land in the temp dir so the relative resolve works.
//
//go:embed devjs/dev-server.js devjs/module-clients.js
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
// `pull` and `push` are unified verbs:
//   - pull  = code archive + env (.env.local) + config-as-code (config/*.toml)
//   - push  = deploy (bundle+upload+activate) + config-as-code push
//
// so "pull/push the project" is a single action for the developer.
func Commands(r Resolvers) []*cobra.Command {
	return []*cobra.Command{
		newMobileCmd(r),
		newPullCmd(r),
		newPushCmd(r),
		newDevCmd(r),
		newListCmd(r),
		newRollbackCmd(r),
		newStatusCmd(r),
		newTypesCmd(r),
	}
}

// defaultIOSGeneratedFile is the Swift codegen output (typed methods +
// types). PalbaseGenerated.json sits next to it (same directory) and
// carries the runtime config the Palbe SDK loads from Bundle.main at
// `pb.configure()` time — kept implicit so callers that pass a custom
// --out only need to think about the Swift path.
const defaultIOSGeneratedFile = ".palbase/generated/ios/PalbaseGenerated.swift"

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
		newMobileSetupCmd(r),
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
// lets the init/serve/deploy commands auto-link via project.list when the
// user hasn't run `palbase pull` first (pull writes .palbase/config.json).
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
// any config — the caller persists the link where it wants (cwd for the
// in-place path, a fresh <projectName>/ dir for `palbase pull` clone-mode).
func pickProject(ctx context.Context, override string, c *studio.Client, out io.Writer) (*auth.Project, error) {
	if override == "" && !isInteractive() {
		return nil, fmt.Errorf("project not linked — pass --ref or run `palbase pull` in a fresh directory to pick a project")
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

// extractTarGzReplace replaces the contents of dest with the contents
// of the tar.gz archive. Existing files are overwritten; existing
// directories that don't appear in the archive are left in place
// (so e.g. node_modules survives an init-on-existing-dir).
func extractTarGzReplace(archive []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		clean := filepath.Clean(header.Name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("invalid archive path: %s", header.Name)
		}
		out := filepath.Join(dest, clean)
		if !strings.HasPrefix(filepath.Clean(out), filepath.Clean(dest)+string(os.PathSeparator)) &&
			filepath.Clean(out) != filepath.Clean(dest) {
			return fmt.Errorf("path traversal in archive: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

// backendBundleAllowedRoots lists the only top-level cwd entries that
// belong in a backend deploy archive. Switched to an allow-list after
// CLI-18: the old deny-list ("skip .git/node_modules/.palbase/...")
// happily packed anything else the cwd happened to contain, which on a
// hybrid project tree (e.g. an iOS app where the backend lives in a
// subdir, or a monorepo with a webapp next to the backend) meant
// Xcode projects, Swift sources, *.xcodeproj/ bundles, Info.plist —
// everything — got uploaded to the backend-runtime pod and then re-
// pulled by the next `palbase pull`. Allow-list is the only safe
// default: backend artifacts are a small, well-known set; everything
// else is opt-in (a future palbase.toml [bundle].include).
var backendBundleAllowedRoots = map[string]struct{}{
	// Required runtime: handlers + their colocated meta sidecars.
	"endpoints": {},
	// Config-as-code (auth/storage/notifications/flags/documents TOMLs).
	"config": {},
	// Node deps + lockfile — pod installs from these.
	"package.json":      {},
	"package-lock.json": {},
	"pnpm-lock.yaml":    {},
	"yarn.lock":         {},
	// TypeScript config — pod's typecheck/bundling reads it.
	"tsconfig.json": {},
	// User-supplied runtime config — bundled but overwritten by the
	// injected palbase.toml below (kept here so a customer-provided
	// override survives the walk-phase filter, then loses to inject).
	"palbase.toml": {},
	// Shared source dirs an endpoint may import (e.g. the schema passed to
	// defineEndpoint({ schema }), use-case/service classes, helpers). The
	// server bundler runs `esbuild --bundle` from endpoints/ and follows
	// relative imports like `../../../schema/index.js`, so these dirs MUST
	// reach the pod or the deploy fails "Could not resolve". Bug caught on
	// a real SQL todo app whose endpoints imported `schema/`. node_modules
	// is intentionally NOT here — the pod installs deps from package.json.
	"schema": {},
	"lib":    {},
	"shared": {},
	"src":    {},
	"utils":  {},
	// SQL migrations live under db/migrations/*.up.sql — the deploy-time
	// migration runner reads them from db/migrations on the pod. Without
	// db/ in the archive the migrations never reach the server, so the
	// schema-drift gate trips ("table X not present") on the very tables
	// the migrations would have created. (Also covers a db/schema.ts form.)
	"db": {},
}

// bundleCwd packs the project's backend artifacts (allow-list) as
// tar.gz for the deploy upload, with a freshly-rendered palbase.toml
// stitched in at the end. The TOML is regenerated from the project's
// ref on every deploy so user edits can't desync from control-pg.
func bundleCwd(root, ref string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	walked := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Allow-list gate: a path is in if its top-level component
		// (everything up to the first separator) is allowed. Anything
		// else — Info.plist, *.xcodeproj/, palbase/ (Swift sources),
		// docs/, fixtures/, … — is dropped silently.
		top := rel
		if idx := strings.IndexAny(rel, "/\\"); idx >= 0 {
			top = rel[:idx]
		}
		if _, ok := backendBundleAllowedRoots[top]; !ok {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		walked++
		return err
	})
	if err != nil {
		return nil, err
	}
	// Inject the runtime config (overrides any user-side palbase.toml the
	// archive may have picked up — the walk above doesn't filter it
	// because exclusion + injection are the same operation).
	tomlBody := runtimeTOML(ref)
	if err := tw.WriteHeader(&tar.Header{
		Name:    "palbase.toml",
		Mode:    0o644,
		Size:    int64(len(tomlBody)),
		ModTime: time.Now(),
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(tomlBody); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	if walked == 0 {
		return nil, fmt.Errorf("nothing to bundle from %s", root)
	}
	return buf.Bytes(), nil
}

// ── subcommands ─────────────────────────────────────────────────────────

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

func newDevCmd(r Resolvers) *cobra.Command {
	var port int
	var branchFlag string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run endpoints/ locally with hot reload",
		Long: `Serve the project's endpoints/ from a local Node.js dev server with
hot reload — the local equivalent of the deployed backend-runtime pod.
Routes, ctx, and defineEndpoint behave identically to production, so
what runs under ` + "`palbase serve`" + ` runs the same after ` + "`palbase push`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			endpointsDir := filepath.Join(cwd, "endpoints")
			if _, err := os.Stat(endpointsDir); err != nil {
				return fmt.Errorf("no endpoints/ directory in cwd — run `palbase pull` first")
			}
			tmpDir, err := os.MkdirTemp("", "palbase-dev-*")
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

			// Reveal the project's publishable key so dev-server can wire its
			// inline module clients (module-clients.js). Service-role keys are
			// platform-internal and are never pulled into local dev.
			var revealResp struct {
				EndpointRef    string `json:"endpointRef"`
				PublishableKey string `json:"publishableKey"`
			}
			if ref != "" && ref != "local" {
				if err := r.Studio().Query(ctx, "apikey.reveal", map[string]any{"ref": ref}, &revealResp); err != nil {
					fmt.Fprintf(os.Stderr, "warning: apikey.reveal failed (%v) — ctx.docs/ctx.storage/… will be unavailable\n", err)
				}
			}

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
			node.Stdout = os.Stdout
			node.Stderr = os.Stderr
			if err := node.Start(); err != nil {
				return fmt.Errorf("start node: %w (is Node.js installed?)", err)
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
	cmd.Flags().IntVar(&port, "port", 4000, "Local port for the dev server")
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

func newPushCmd(r Resolvers) *cobra.Command {
	var refFlag string
	var message string
	var skipTypes bool
	var branchFlag string
	var acceptDataLoss bool
	var force bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push the project (deploy code + apply config) to the server",
		Long: `Push everything for the linked project in one step:

  • code → bundled, uploaded as a new version, and activated
  • config-as-code → config/*.toml applied to the server (ordered, conflict-gated)

The code deploy runs first; config push follows. A config-push failure is
reported but does NOT roll back the code deploy that already landed.

Schema changes are auto-migrated (Prisma "db push" style). A destructive change
that would LOSE data (DROP TABLE on a non-empty table, DROP COLUMN with non-null
values) requires confirmation: an interactive y/N prompt on a TTY, or the
--accept-data-loss flag (alias: --force) in CI. Dropping empty tables/columns
applies without prompting.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// --force is an alias for --accept-data-loss.
			if force {
				acceptDataLoss = true
			}
			ref, err := resolveOrLinkRef(ctx, refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			// Active branch (--branch wins, else ProjectConfig.DefaultEnv;
			// "" = main). In the branch=project model each branch is its own
			// tenant pod (endpoint_ref = <ref><branchSlug>), so push must land
			// on the branch you're on. backend.deploy now accepts a `branch`
			// field (server resolves ref+branch → endpoint_ref); we thread it
			// below. Empty = default branch (main).
			branch := resolveActiveBranch(branchFlag)
			if branch != "" {
				fmt.Printf("→ branch: %s\n", branch)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			fmt.Printf("→ bundling %s ...\n", cwd)
			archive, err := bundleCwd(cwd, ref)
			if err != nil {
				return fmt.Errorf("bundle: %w", err)
			}
			fmt.Printf("  bundle: %d bytes\n", len(archive))

			// ── destructive-migration plan/consent round-trip ──────────
			// Before the deploy mutation, ask the server what schema changes
			// the auto-migrate engine would make (PLAN mode: computes the diff
			// + per-drop row counts, applies NOTHING). If the project has no
			// schema file we skip the plan entirely (additive one-shot deploy —
			// backward compatible with code-only projects).
			schemaPath := locateSchemaFile(cwd)
			if schemaPath != "" {
				schemaSrc, readErr := os.ReadFile(schemaPath)
				if readErr != nil {
					return fmt.Errorf("read schema %s: %w", schemaPath, readErr)
				}
				planPayload := map[string]any{"ref": ref, "schema": string(schemaSrc)}
				if branch != "" {
					planPayload["branch"] = branch
				}
				var plan migrationPlan
				if planErr := r.Studio().Mutation(ctx, "backend.migrationPlan", planPayload, &plan); planErr != nil {
					return fmt.Errorf("backend.migrationPlan: %w", planErr)
				}
				proceed, decErr := decideDestructive(plan, acceptDataLoss, os.Stdin, isInteractive(), os.Stdout)
				if decErr != nil {
					return decErr
				}
				if !proceed {
					return fmt.Errorf("aborted: destructive schema change not confirmed")
				}
				// Carry consent into the deploy only when the plan actually
				// loses data — empty drops apply server-side without a flag.
				if plan.HasDataLoss {
					acceptDataLoss = true
				}
			}

			fmt.Println("→ uploading via Studio ...")
			var resp struct {
				Version string `json:"version"`
				Files   int    `json:"files"`
			}
			deployPayload := map[string]any{
				"ref":     ref,
				"archive": base64.StdEncoding.EncodeToString(archive),
				"message": message,
			}
			if branch != "" {
				deployPayload["branch"] = branch
			}
			if acceptDataLoss {
				deployPayload["acceptDataLoss"] = true
			}
			if err := r.Studio().Mutation(ctx, "backend.deploy", deployPayload, &resp); err != nil {
				return fmt.Errorf("backend.deploy: %w", err)
			}
			fmt.Printf("✓ deployed version %s (%d files)\n", resp.Version, resp.Files)
			// Endpoints are served file-based (e.g. POST /hello/get from
			// endpoints/hello/post.ts) — there is no /rpc/* prefix on the
			// deployed runtime, so print the project base URL only.
			//
			// The URL MUST use the branch endpoint_ref (Kong routes only
			// that subdomain) and the mode-aware public host. Best-effort:
			// look it up via apikey.reveal; on failure fall back to a
			// generic note instead of printing a bare-ref URL that 404s.
			if target, lerr := lookupBackendTarget(ctx, r.Studio(), r.Endpoints(), ref, branch); lerr == nil {
				fmt.Printf("  %s\n", target.URL)
			} else {
				fmt.Printf("  (project base URL unavailable: %v)\n", lerr)
			}

			// Adım B14 — auto-pull types after every successful deploy.
			// `--no-types` disables for CI loops where the typed
			// declarations aren't useful.
			if !skipTypes {
				if err := pullTypes(ctx, r.Studio(), r.Endpoints(), ref, "remote", os.Stdout); err != nil {
					// Don't fail the deploy on a types-pull glitch — the
					// deploy itself succeeded; types are an ergonomic
					// artifact developers can refresh manually.
					fmt.Fprintf(os.Stderr, "⚠ types pull failed: %v\n", err)
				}
			}

			// ── config-as-code push ───────────────────────────────────
			// Runs AFTER the code deploy (advisor ordering: code first).
			// A config failure is surfaced but the code deploy already
			// landed and is NOT rolled back (no-rollback — Faz 3b).
			if cfgErr := runConfigPush(ctx, cwd, ref, branch, r.Studio(), os.Stdout); cfgErr != nil {
				return fmt.Errorf("code deployed (version %s) but config push failed: %w", resp.Version, cfgErr)
			}

			fmt.Println("✓ push complete")
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVarP(&message, "message", "m", "", "Optional commit message recorded in git history")
	cmd.Flags().BoolVar(&skipTypes, "no-types", false, "Skip the post-deploy types generation step")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to push (defaults to the active branch; omit for main)")
	cmd.Flags().BoolVar(&acceptDataLoss, "accept-data-loss", false, "Approve destructive schema changes (DROP TABLE/COLUMN that lose data) without an interactive prompt")
	cmd.Flags().BoolVar(&force, "force", false, "Alias for --accept-data-loss")
	return cmd
}

// migrationPlan mirrors the Studio backend.migrationPlan result (which in turn
// mirrors modules/backend internal/schema.DestructivePlan). Decoded from the
// tRPC JSON envelope by studio.Client.
type migrationPlan struct {
	Drops       []destructiveOp     `json:"drops"`
	Unsupported []unsupportedChange `json:"unsupported"`
	HasDataLoss bool                `json:"hasDataLoss"`
}

type destructiveOp struct {
	Kind     string `json:"kind"` // "table" | "column"
	Table    string `json:"table"`
	Column   string `json:"column,omitempty"`
	RowCount int64  `json:"rowCount"`
	NonNull  int64  `json:"nonNull,omitempty"`
}

type unsupportedChange struct {
	Table  string `json:"table"`
	Column string `json:"column,omitempty"`
	Reason string `json:"reason"`
	Hint   string `json:"hint,omitempty"`
}

// schemaFileCandidates mirrors the server's resolveSchemaPath precedence so the
// CLI uploads the SAME file the deploy-time auto-migrate engine evaluates.
var schemaFileCandidates = []string{
	"schema.ts",
	filepath.Join("db", "schema.ts"),
	filepath.Join("schema", "index.ts"),
	filepath.Join("db", "schema", "index.ts"),
}

// locateSchemaFile returns the first existing schema definition under root, or
// "" when the project declares no schema (a code-only project — push then does
// an additive one-shot deploy with no plan round-trip).
func locateSchemaFile(root string) string {
	for _, rel := range schemaFileCandidates {
		p := filepath.Join(root, rel)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// decideDestructive implements the consent decision for a migration plan:
//
//   - no drops at all          → proceed silently
//   - drops but no data loss   → print a one-line note, proceed (no prompt)
//   - data loss + flag/force   → proceed
//   - data loss + TTY          → print the warning block, read y/N
//   - data loss + non-TTY      → abort with a hint to pass --accept-data-loss
//
// It writes user-facing output to out and reads the y/N answer from in.
func decideDestructive(plan migrationPlan, acceptFlag bool, in io.Reader, isTTY bool, out io.Writer) (bool, error) {
	if len(plan.Drops) == 0 {
		return true, nil
	}
	if !plan.HasDataLoss {
		// Destructive but empty — apply without friction (Prisma db push).
		for _, op := range plan.Drops {
			if op.Kind == "table" {
				fmt.Fprintf(out, "  (dropping empty table %s — no data loss)\n", op.Table)
			} else {
				fmt.Fprintf(out, "  (dropping empty column %s.%s — no data loss)\n", op.Table, op.Column)
			}
		}
		return true, nil
	}

	// Real data loss — print the warning block.
	fmt.Fprintln(out, "⚠ This push will DESTROY data:")
	for _, op := range plan.Drops {
		switch op.Kind {
		case "table":
			if op.RowCount > 0 {
				fmt.Fprintf(out, "  ⚠ DROP TABLE %s (%d row(s) will be lost)\n", op.Table, op.RowCount)
			}
		case "column":
			if op.NonNull > 0 {
				fmt.Fprintf(out, "  ⚠ DROP COLUMN %s.%s (%d non-null value(s) will be lost)\n", op.Table, op.Column, op.NonNull)
			}
		}
	}
	return confirmDestructive(in, isTTY, acceptFlag)
}

// confirmDestructive resolves the proceed/abort decision for a data-losing
// change. The flag (--accept-data-loss / --force) short-circuits to proceed.
// On a TTY it reads a y/N answer from in. Without the flag and without a TTY
// (CI), it aborts so a destructive change never lands unattended.
func confirmDestructive(in io.Reader, isTTY bool, flag bool) (bool, error) {
	if flag {
		return true, nil
	}
	if !isTTY {
		return false, fmt.Errorf("destructive schema change would lose data; re-run with --accept-data-loss (or --force) to proceed in a non-interactive shell")
	}
	fmt.Print("Proceed and lose this data? [y/N]: ")
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
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

// runtimeTOML serializes the per-project palbase.toml the deploy step
// injects into every uploaded archive. Kept in one place so dev / deploy
// see the same shape, and so changing a default doesn't drift across
// CLI versions in the field.
func runtimeTOML(ref string) []byte {
	return []byte(fmt.Sprintf(`# Generated by `+"`palbase push`"+`. Do not edit by hand —
# Palbase regenerates this file on every push from the project metadata
# in control-pg.

[project]
ref = %q

[backend]
runtime = "node"
timeout_seconds = 30
memory_mb = 256
`, ref))
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

// ─────────────────────────────────────────────────────────────────────
// palbase pull
//
// Pulls the branch's latest code archive and then fetches the decrypted
// branch env vars via env.pull, writing them to
// .env.local so `palbase serve` has the real values.
//
// .env.local is already in the template .gitignore; we also ensure it
// is listed in the project's own .gitignore so values never reach git,
// even in projects initialised before this command existed.
//
// If the env.pull step fails (e.g. no vars set, transient network
// error), a warning is printed and the command returns successfully —
// the code pull already completed.
// ─────────────────────────────────────────────────────────────────────

// envVar holds a single key/value pair returned by the env.pull tRPC procedure.
type envVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// needsQuoting returns true when the value contains characters that
// require double-quoting in a .env file: whitespace, #, or ".
func needsQuoting(v string) bool {
	return strings.ContainsAny(v, " \t#\"")
}

// formatEnvLine serialises one env var as a dotenv line.
// Values containing whitespace, # or " are wrapped in double-quotes with
// embedded quotes and backslashes escaped.
// NOTE: values with literal newlines are not supported in v1.
func formatEnvLine(key, value string) string {
	if !needsQuoting(value) {
		return key + "=" + value
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	return key + `="` + escaped + `"`
}

// writeEnvLocal writes vars as a .env.local file inside dir and ensures
// .env.local is listed in dir/.gitignore. When vars is empty the file
// is not written (but the .gitignore is still not touched).
func writeEnvLocal(dir string, vars []envVar) error {
	if len(vars) == 0 {
		return nil
	}

	var sb strings.Builder
	for _, v := range vars {
		sb.WriteString(formatEnvLine(v.Key, v.Value))
		sb.WriteByte('\n')
	}

	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("write .env.local: %w", err)
	}

	// Safety: ensure .env.local is gitignored so the value never reaches git,
	// even in projects initialised before this command existed.
	if err := ensureGitignored(filepath.Join(dir, ".gitignore"), ".env.local"); err != nil {
		// Non-fatal — the file is written; warn rather than roll back.
		fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
	}

	return nil
}

// sanitizeProjectDir turns a project's display name into a safe directory
// name for `palbase pull` clone-mode: lowercase, spaces/underscores → "-",
// non [a-z0-9-] stripped, runs of "-" collapsed, leading/trailing "-"
// trimmed. Names that reduce to "", "." or ".." (which would target the cwd
// or escape it) yield "" so the caller falls back to the project ref.
func sanitizeProjectDir(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '_' || r == '-':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// drop everything else (slashes, dots, punctuation, unicode)
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}

// cloneTarget computes the directory `palbase pull` clone-mode should create
// for a picked project: the sanitized name, falling back to the ref when the
// name sanitizes to empty. Returns an error if the target already exists and
// is non-empty (an empty/just-created dir is fine to clone into).
func cloneTarget(p *auth.Project) (string, error) {
	name := sanitizeProjectDir(p.Name)
	if name == "" {
		name = p.Ref
	}
	entries, err := os.ReadDir(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// target doesn't exist yet — clone into a fresh dir
	case err != nil:
		return "", fmt.Errorf("check %s: %w", name, err)
	case len(entries) > 0:
		return "", fmt.Errorf("directory %q already exists and is not empty", name)
	}
	return name, nil
}

// isCloneMode reports whether `palbase pull` should run in clone-mode: the
// cwd has no .palbase/config.json (ErrNotLinked) AND no endpoints/ dir. The
// endpoints/ guard prevents clobbering an existing project whose .palbase/
// was merely deleted — that case stays in-place and surfaces the normal
// not-linked path instead of pulling into a sibling directory.
func isCloneMode() bool {
	if _, err := projectRef(""); !errors.Is(err, ErrNotLinked) {
		return false
	}
	if _, err := os.Stat("endpoints"); err == nil {
		return false
	}
	return true
}

func newPullCmd(r Resolvers) *cobra.Command {
	var refFlag string
	var branchFlag string
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Pull the project (code + env + config) to your local machine",
		Long: `Pull everything for the linked project in one step:

  • code archive   → extracted into the cwd
  • env vars       → decrypted into .env.local (gitignored)
  • config-as-code → config/*.toml + .palbase/state.json

.env.local is gitignored — values reach the dev machine, never git. A
transient env or config failure warns but does not abort the pull; the
code pull is the load-bearing step.

Run from an empty directory (no .palbase/config.json, no endpoints/) and
pull works like 'git clone': it picks a project, creates a <projectName>/
directory, and pulls into it. It does NOT change your shell's directory —
it prints a "cd <projectName>" hint to run afterwards.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Clone-mode vs in-place. Clone-mode (empty/unlinked dir) creates a
			// fresh <projectName>/ tree and pulls into it; in-place updates the
			// already-linked cwd (the original, unchanged behaviour). The two
			// differ only in (a) where ref comes from and (b) which dir the
			// pull targets — the body below is shared.
			var ref string
			var cwd string
			cloneDir := "" // non-empty only in clone-mode; drives the final "cd" hint
			if isCloneMode() {
				picked, err := pickProject(ctx, refFlag, r.Studio(), os.Stdout)
				if err != nil {
					return err
				}
				target, err := cloneTarget(picked)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(target, 0o755); err != nil {
					return fmt.Errorf("create %s: %w", target, err)
				}
				// main by default — a fresh clone tracks main, not staging.
				if err := auth.SaveProjectConfigIn(target, &auth.ProjectConfig{Ref: picked.Ref, DefaultEnv: "main"}); err != nil {
					return fmt.Errorf("save %s/.palbase/config.json: %w", target, err)
				}
				ref = picked.Ref
				cwd = target
				cloneDir = target
				fmt.Printf("Cloning %s (%s) into %s/ ...\n", picked.Name, picked.Ref, target)
			} else {
				var err error
				ref, err = resolveOrLinkRef(ctx, refFlag, r.Studio(), os.Stdout)
				if err != nil {
					return err
				}
				cwd, err = os.Getwd()
				if err != nil {
					return err
				}
			}

			// Active branch (--branch wins, else ProjectConfig.DefaultEnv;
			// "" = main). Config-as-code pull IS branch-aware now — its
			// serializers accept a branch and the Studio config routers
			// resolve ref+branch → endpoint_ref (branch=project model), so
			// `palbase pull --branch qa` snapshots THAT branch's module
			// config. NAMED GAP (still): backend.pull / env.pull don't accept
			// a branch field yet (env.pull is .strict() and would reject one),
			// so the code archive + .env.local below still come from the
			// default branch. Those land when their tRPC inputs grow a branch
			// field server-side; this resolve already feeds them.
			branch := resolveActiveBranch(branchFlag)
			if branch != "" {
				fmt.Printf("note: --branch %q — config pulled from that branch; code + env still pull the default branch (not branch-wired yet).\n", branch)
			}

			// ── code pull ─────────────────────────────────────────────
			fmt.Printf("→ pulling code for %s ...\n", ref)
			var pull struct {
				Version string `json:"version"`
				Archive string `json:"archive"`
				Size    int    `json:"size"`
			}
			if err := r.Studio().Query(ctx, "backend.pull", map[string]any{"ref": ref}, &pull); err != nil {
				return fmt.Errorf("backend.pull: %w", err)
			}
			archive, err := base64.StdEncoding.DecodeString(pull.Archive)
			if err != nil {
				return fmt.Errorf("decode pull archive: %w", err)
			}
			if err := extractTarGzReplace(archive, cwd); err != nil {
				return fmt.Errorf("extract pull archive: %w", err)
			}
			fmt.Printf("  code version: %s (%d bytes)\n", pull.Version, pull.Size)

			// ── env pull ──────────────────────────────────────────────
			fmt.Println("→ pulling env vars ...")
			var envVars []envVar
			if envErr := r.Studio().Query(ctx, "env.pull", map[string]any{"ref": ref}, &envVars); envErr != nil {
				fmt.Fprintf(os.Stderr, "warning: env.pull failed (%v) — .env.local not written\n", envErr)
			} else {
				if err := writeEnvLocal(cwd, envVars); err != nil {
					fmt.Fprintf(os.Stderr, "warning: %v — .env.local not written\n", err)
				} else if len(envVars) > 0 {
					fmt.Printf("  wrote .env.local (%d var(s))\n", len(envVars))
				} else {
					fmt.Println("  no env vars configured — .env.local not written")
				}
			}

			// ── config-as-code pull ───────────────────────────────────
			// Non-fatal: a config glitch must not lose the code/env pull
			// that already succeeded.
			if cfgErr := runConfigPull(ctx, cwd, ref, branch, r.Studio(), os.Stdout); cfgErr != nil {
				fmt.Fprintf(os.Stderr, "warning: config pull failed (%v) — code + env were still pulled\n", cfgErr)
			}

			fmt.Println("✓ pull complete")
			// Clone-mode pulled into a sibling directory. A CLI can't change
			// the parent shell's cwd (neither can `git clone`), so print the
			// hint instead of cd-ing.
			if cloneDir != "" {
				fmt.Printf("✓ cd %s to start\n", cloneDir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to pull (defaults to the active branch; omit for main)")
	return cmd
}

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
			cfg.DefaultEnv = branch
			if err := auth.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
			}
			fmt.Fprintf(os.Stdout, "✓ checked out mobile backend branch %q (project %s)\n", branch, ref)
			if err := generateIOSRemote(cmd.Context(), r.Studio(), r.Endpoints(), ref, branch, defaultIOSGeneratedFile, os.Stdout); err != nil {
				return fmt.Errorf("ios codegen: %w", err)
			}
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

func newMobileSetupCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Wire a mobile app to generated Palbase SDK code",
	}
	cmd.AddCommand(newMobileSetupIOSCmd(r))
	return cmd
}

func newMobileSetupIOSCmd(r Resolvers) *cobra.Command {
	var projectPath string
	var targetName string
	var refFlag string
	cmd := &cobra.Command{
		Use:   "ios",
		Args:  cobra.NoArgs,
		Short: "Add Palbase iOS generated code + build phase to an Xcode project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureIOSGeneratedStub(defaultIOSGeneratedFile); err != nil {
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
			// mobile setup ios is special: it BOTH consumes the ref AND
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
			if err := generateIOSAuto(cmd.Context(), r.Studio(), r.Endpoints(), ref, branch, defaultIOSGeneratedFile, os.Stdout); err != nil {
				return fmt.Errorf("initial ios codegen: %w", err)
			}

			// Google URL scheme — runs AFTER codegen because the
			// reversed-DNS redirect scheme is derived from the Google
			// client_id, which codegen just fetched from palauth and
			// wrote into PalbaseGenerated.json. No-op when the project
			// has no Google provider configured.
			if err := wireGoogleURLSchemeFromGenerated(project, target, defaultIOSGeneratedFile, os.Stdout); err != nil {
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
			// Same defensive materialise as mobile setup ios: --ref makes
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
			return generateIOSAuto(cmd.Context(), r.Studio(), r.Endpoints(), ref, branch, defaultIOSGeneratedFile, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref to link (skips the interactive picker; required in non-interactive shells)")
	return cmd
}

func generateIOSAuto(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, branch, outFile string, w io.Writer) error {
	localURL := "http://localhost:4003"
	if specBytes, err := fetchOpenAPISpec(ctx, localURL+"/openapi.json", ""); err == nil {
		apiKey := ""
		var oauth *swiftOAuthConfig
		if target, lookupErr := lookupBackendTarget(ctx, sc, endpoints, ref, branch); lookupErr == nil {
			apiKey = target.APIKey
			// OAuth providers live in remote palauth — the local
			// backend dev server doesn't proxy /auth/*. Hit the remote
			// (the same URL the SDK would when configure()'d for the
			// remote project) so codegen output is identical no matter
			// which spec source we use.
			if oauthCfg, oauthErr := fetchOAuthProviders(ctx, target.URL, target.APIKey); oauthErr != nil {
				fmt.Fprintf(w, "  (oauth providers not fetched: %v)\n", oauthErr)
			} else {
				oauth = oauthCfg
			}
		} else {
			fmt.Fprintf(w, "  (publishable key not embedded for local config: %v)\n", lookupErr)
		}
		return writeSwiftGenerated(specBytes, swiftGeneratedConfig{
			URL:    localURL,
			APIKey: apiKey,
			Branch: branch,
			Source: "local",
			OAuth:  oauth,
		}, outFile, w)
	}
	target, err := lookupBackendTarget(ctx, sc, endpoints, ref, branch)
	if err != nil {
		return err
	}
	return generateIOSRemoteWithTarget(ctx, target, branch, outFile, w)
}

func generateIOSRemote(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, branch, outFile string, w io.Writer) error {
	target, err := lookupBackendTarget(ctx, sc, endpoints, ref, branch)
	if err != nil {
		return err
	}
	return generateIOSRemoteWithTarget(ctx, target, branch, outFile, w)
}

func generateIOSRemoteWithTarget(ctx context.Context, target backendTarget, branch, outFile string, w io.Writer) error {
	specBytes, err := fetchOpenAPISpec(ctx, target.URL+"/openapi.json", target.APIKey)
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
		Source: "remote",
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
	if err := ensureGitignored(".gitignore", ".palbase/"); err != nil {
		fmt.Fprintf(w, "  (gitignore not updated: %v)\n", err)
	}
	fmt.Fprintf(w, "✓ wrote %s (%d operation(s))\n", outFile, len(ops))
	fmt.Fprintf(w, "✓ wrote %s (config — %s)\n", jsonPath, cfg.Source)
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
	source := cfg.Source
	if source == "" {
		source = "remote"
	}
	body := map[string]any{
		"url":     cfg.URL,
		"api_key": cfg.APIKey,
		"branch":  branch,
		"source":  source,
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
	stub := `// Generated by palbase mobile setup ios. Re-run palbase mobile codegen ios.
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
  "branch": "main",
  "source": "setup"
}
`
		if err := os.WriteFile(jsonPath, []byte(stubJSON), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", jsonPath, err)
		}
	}
	return ensureGitignored(".gitignore", ".palbase/")
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
	sourcesID := ""
	resourcesID := ""
	for _, phaseID := range target.phases {
		if objectContains(pbx, phaseID, "isa = PBXSourcesBuildPhase;") {
			sourcesID = phaseID
		}
		if objectContains(pbx, phaseID, "isa = PBXResourcesBuildPhase;") {
			resourcesID = phaseID
		}
	}
	if sourcesID == "" {
		return "", "", false, fmt.Errorf("target %q has no Swift sources build phase", target.name)
	}
	if resourcesID == "" {
		return "", "", false, fmt.Errorf("target %q has no PBXResourcesBuildPhase — needed to bundle PalbaseGenerated.json", target.name)
	}

	const (
		generatedSwiftPath = ".palbase/generated/ios/PalbaseGenerated.swift"
		generatedJSONPath  = ".palbase/generated/ios/PalbaseGenerated.json"
	)
	fileRefID := findObjectIDContaining(pbx, "path = "+generatedSwiftPath+";")
	if fileRefID == "" {
		fileRefID = xcodeObjectID("palbase-ios-file-ref")
	}
	buildFileID := findObjectIDContaining(pbx, "PalbaseGenerated.swift in Sources")
	if buildFileID == "" {
		buildFileID = xcodeObjectID("palbase-ios-build-file")
	}
	jsonRefID := findObjectIDContaining(pbx, "path = "+generatedJSONPath+";")
	if jsonRefID == "" {
		jsonRefID = xcodeObjectID("palbase-ios-json-file-ref")
	}
	jsonBuildFileID := findObjectIDContaining(pbx, "PalbaseGenerated.json in Resources")
	if jsonBuildFileID == "" {
		jsonBuildFileID = xcodeObjectID("palbase-ios-json-build-file")
	}
	shellPhaseID := findObjectIDContaining(pbx, "name = \"Palbase Codegen iOS\";")
	if shellPhaseID == "" {
		shellPhaseID = xcodeObjectID("palbase-ios-shell-phase")
	}

	next := pbx
	changed := false
	var did bool
	next, did = ensurePBXBuildFile(next, buildFileID, fileRefID)
	changed = changed || did
	next, did = ensurePBXFileReference(next, fileRefID)
	changed = changed || did
	// JSON config — Bundle resource the Palbe SDK loads at pb.configure()
	// time. Lives next to the .swift in the same Generated group so it
	// shows up in the navigator alongside the typed methods.
	next, did = ensurePBXBuildFileForJSON(next, jsonBuildFileID, jsonRefID)
	changed = changed || did
	next, did = ensurePBXFileReferenceForJSON(next, jsonRefID)
	changed = changed || did
	// Slot both file refs into a "Generated" group so Xcode shows them
	// under the project navigator (and not "Recovered References", which
	// is where Xcode parks PBXFileReferences that no group claims). The
	// helper is idempotent: if a Generated group already exists we just
	// append the fileRef when missing.
	groupID := findObjectIDContaining(next, "name = Generated;")
	if groupID == "" {
		groupID = xcodeObjectID("palbase-ios-generated-group")
	}
	next, did = ensurePalbaseGeneratedGroup(next, groupID, fileRefID)
	changed = changed || did
	// Also add the JSON ref to the Generated group. appendChildToGroup
	// is itself idempotent — early-returns the same `pbx` when the ref
	// is already a child — so compare-by-identity to gate `changed`.
	beforeJSON := next
	next = appendChildToGroup(next, groupID, jsonRefID, "PalbaseGenerated.json")
	if next != beforeJSON {
		changed = true
	}
	next, did = ensurePBXShellScriptPhase(next, shellPhaseID)
	changed = changed || did
	next, did = ensureBuildFileInSources(next, sourcesID, buildFileID)
	changed = changed || did
	next, did = ensureBuildFileInResources(next, resourcesID, jsonBuildFileID)
	changed = changed || did
	next, did = ensureBuildPhaseInTarget(next, target.id, shellPhaseID)
	changed = changed || did
	return next, target.name, changed, nil
}

// ensurePalbaseGeneratedGroup makes sure the PalbaseGenerated.swift file
// reference is parented to a named "Generated" PBXGroup, attached to the
// project's root group. Without a parent, Xcode displays the file under
// "Recovered References" — technically buildable but visually broken.
//
// Cases:
//   - Generated group exists: append fileRefID to its children if missing.
//   - Generated group doesn't exist + the project has a PBXGroup section:
//     emit the new group and attach it under the root group.
//   - No PBXGroup section at all (rare: minimal hand-rolled pbxproj /
//     test fixture): bail without mutating — the project shape is
//     non-standard and we can't safely splice. Keeps idempotency in those
//     cases too (the second patch is a no-op).
func ensurePalbaseGeneratedGroup(pbx, groupID, fileRefID string) (string, bool) {
	groupBlock := objectBlock(pbx, groupID)
	if groupBlock != "" {
		changed := false
		// Ensure fileRefID listed inside Generated's children.
		if !strings.Contains(groupBlock, fileRefID) {
			needle := groupID + " /* Generated */ = {"
			lines := strings.Split(pbx, "\n")
			for i, line := range lines {
				if !strings.Contains(line, needle) {
					continue
				}
				for j := i; j < len(lines) && j < i+20; j++ {
					if strings.Contains(lines[j], "children = (") {
						insert := "\t\t\t\t" + fileRefID + " /* PalbaseGenerated.swift */,"
						newLines := append([]string{}, lines[:j+1]...)
						newLines = append(newLines, insert)
						newLines = append(newLines, lines[j+1:]...)
						pbx = strings.Join(newLines, "\n")
						changed = true
						break
					}
				}
				break
			}
		}
		// Self-heal: ensure the Generated group is also attached to the
		// root PBXGroup. An earlier CLI version (pre this fix) created
		// the group but never wired it into the navigator, so it
		// floated and Xcode parked the file under Recovered References.
		// Re-running `palbase mobile setup ios` picks up the attachment
		// without needing to recreate the group.
		if rootGroupID := findRootPBXGroup(pbx); rootGroupID != "" {
			rootBlock := objectBlock(pbx, rootGroupID)
			if !strings.Contains(rootBlock, groupID+" /* Generated */") {
				pbx = appendChildToGroup(pbx, rootGroupID, groupID, "Generated")
				changed = true
			}
		}
		return pbx, changed
	}

	// No Generated group yet. We need a PBXGroup section to slot a new
	// group into; if the project doesn't have one, skip silently (the
	// file ref still lives in the project — Xcode will park it under
	// Recovered References, same as before this fix; but at least the
	// patch stays idempotent, which the test guards).
	if !strings.Contains(pbx, "/* End PBXGroup section */") {
		return pbx, false
	}

	groupDef := "\t\t" + groupID + " /* Generated */ = {\n" +
		"\t\t\tisa = PBXGroup;\n" +
		"\t\t\tchildren = (\n" +
		"\t\t\t\t" + fileRefID + " /* PalbaseGenerated.swift */,\n" +
		"\t\t\t);\n" +
		"\t\t\tname = Generated;\n" +
		"\t\t\tsourceTree = \"<group>\";\n" +
		"\t\t};\n"
	next, inserted := insertBeforeMarker(pbx, "/* End PBXGroup section */", groupDef)
	if !inserted {
		return pbx, false
	}

	// Attach to the root group so it shows up in the navigator.
	if rootGroupID := findRootPBXGroup(next); rootGroupID != "" {
		next = appendChildToGroup(next, rootGroupID, groupID, "Generated")
	}
	return next, true
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
		targets = append(targets, t)
		i = j
	}
	return targets
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

func ensurePBXBuildFile(pbx, buildFileID, fileRefID string) (string, bool) {
	if objectBlock(pbx, buildFileID) != "" || strings.Contains(pbx, "PalbaseGenerated.swift in Sources") {
		return pbx, false
	}
	line := "\t\t" + buildFileID + " /* PalbaseGenerated.swift in Sources */ = {isa = PBXBuildFile; fileRef = " + fileRefID + " /* PalbaseGenerated.swift */; };\n"
	return insertBeforeMarker(pbx, "/* End PBXBuildFile section */", line)
}

func ensurePBXFileReference(pbx, fileRefID string) (string, bool) {
	if objectBlock(pbx, fileRefID) != "" || strings.Contains(pbx, "path = .palbase/generated/ios/PalbaseGenerated.swift;") {
		return pbx, false
	}
	line := "\t\t" + fileRefID + " /* PalbaseGenerated.swift */ = {isa = PBXFileReference; lastKnownFileType = sourcecode.swift; path = .palbase/generated/ios/PalbaseGenerated.swift; sourceTree = \"<group>\"; };\n"
	return insertBeforeMarker(pbx, "/* End PBXFileReference section */", line)
}

// JSON resource counterparts: PalbaseGenerated.json is added to the
// Resources build phase (not Sources) so Xcode copies it verbatim into
// the app bundle. The Palbe SDK loads it from Bundle.main at
// pb.configure() time. Same idempotency contract as the Swift pair.
func ensurePBXBuildFileForJSON(pbx, buildFileID, fileRefID string) (string, bool) {
	if objectBlock(pbx, buildFileID) != "" || strings.Contains(pbx, "PalbaseGenerated.json in Resources") {
		return pbx, false
	}
	line := "\t\t" + buildFileID + " /* PalbaseGenerated.json in Resources */ = {isa = PBXBuildFile; fileRef = " + fileRefID + " /* PalbaseGenerated.json */; };\n"
	return insertBeforeMarker(pbx, "/* End PBXBuildFile section */", line)
}

func ensurePBXFileReferenceForJSON(pbx, fileRefID string) (string, bool) {
	if objectBlock(pbx, fileRefID) != "" || strings.Contains(pbx, "path = .palbase/generated/ios/PalbaseGenerated.json;") {
		return pbx, false
	}
	line := "\t\t" + fileRefID + " /* PalbaseGenerated.json */ = {isa = PBXFileReference; lastKnownFileType = text.json; path = .palbase/generated/ios/PalbaseGenerated.json; sourceTree = \"<group>\"; };\n"
	return insertBeforeMarker(pbx, "/* End PBXFileReference section */", line)
}

// ensureBuildFileInResources appends the JSON buildFile id to the
// target's PBXResourcesBuildPhase children list (mirror of
// ensureBuildFileInSources but for the Resources phase). Idempotent.
func ensureBuildFileInResources(pbx, resourcesID, buildFileID string) (string, bool) {
	block := objectBlock(pbx, resourcesID)
	if block == "" {
		return pbx, false
	}
	if strings.Contains(block, buildFileID) || strings.Contains(block, "PalbaseGenerated.json in Resources") {
		return pbx, false
	}
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), resourcesID+" ") || !strings.Contains(line, "= {") {
			continue
		}
		for j := i; j < len(lines) && j < i+25; j++ {
			if strings.Contains(lines[j], "files = (") {
				insert := "\t\t\t\t" + buildFileID + " /* PalbaseGenerated.json in Resources */,"
				newLines := append([]string{}, lines[:j+1]...)
				newLines = append(newLines, insert)
				newLines = append(newLines, lines[j+1:]...)
				return strings.Join(newLines, "\n"), true
			}
		}
	}
	return pbx, false
}

func ensurePBXShellScriptPhase(pbx, shellPhaseID string) (string, bool) {
	// Self-healing semantics: if a phase with our name already exists,
	// compare against the canonical block we'd emit today. If they match
	// (idempotent), bail out — nothing to do. If they differ (older CLI
	// shipped a different block, e.g. before CLI-19 added .palbase/config.json
	// to inputPaths so Xcode's USER_SCRIPT_SANDBOXING permits the read),
	// strip the stale block + reference and fall through to re-emit the
	// current canonical one with the SAME id so any references stay valid.
	if existingID := findPhaseIDByName(pbx, "Palbase Codegen iOS"); existingID != "" {
		existing := objectBlock(pbx, existingID)
		canonical := palbaseCodegenPhaseBlock(existingID)
		if normalizePBXBlock(existing) == normalizePBXBlock(canonical) {
			return pbx, false
		}
		pbx = removeObjectBlock(pbx, existingID)
		pbx = removePhaseReference(pbx, existingID, "Palbase Codegen iOS")
		shellPhaseID = existingID // re-use the id so existing buildPhases refs stay valid
	}
	block := palbaseCodegenPhaseBlock(shellPhaseID)
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
func palbaseCodegenPhaseBlock(shellPhaseID string) string {
	// Fail-soft: codegen failure (login expired, network down, ref
	// unlinked) prints a warning and lets the build continue with
	// whatever generated files are already on disk. Hard-failing the
	// build every time the CLI hiccups blocks Xcode workflow for
	// trivial reasons. The previous codegen output stays valid until
	// the customer re-logs in or fixes the underlying issue.
	script := "cd \"${SRCROOT:-.}\"\n" +
		"if ! command -v palbase >/dev/null 2>&1; then\n" +
		"  echo \"warning: palbase CLI not found; skipping Palbase iOS codegen\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if ! palbase mobile codegen ios; then\n" +
		"  echo \"warning: palbase mobile codegen ios failed; using last generated files (check your login + project link)\"\n" +
		"fi\n" +
		"exit 0\n"
	return "\t\t" + shellPhaseID + " /* Palbase Codegen iOS */ = {\n" +
		"\t\t\tisa = PBXShellScriptBuildPhase;\n" +
		"\t\t\tbuildActionMask = 2147483647;\n" +
		"\t\t\tfiles = (\n\t\t\t);\n" +
		"\t\t\tinputFileListPaths = (\n\t\t\t);\n" +
		"\t\t\tinputPaths = (\n" +
		"\t\t\t\t\"$(SRCROOT)/.palbase/config.json\",\n" +
		"\t\t\t);\n" +
		"\t\t\tname = \"Palbase Codegen iOS\";\n" +
		"\t\t\toutputFileListPaths = (\n\t\t\t);\n" +
		"\t\t\toutputPaths = (\n" +
		"\t\t\t\t\"$(SRCROOT)/.palbase/generated/ios/PalbaseGenerated.swift\",\n" +
		"\t\t\t\t\"$(SRCROOT)/.palbase/generated/ios/PalbaseGenerated.json\",\n" +
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

func ensureBuildFileInSources(pbx, sourcesID, buildFileID string) (string, bool) {
	block := objectBlock(pbx, sourcesID)
	if block == "" {
		return pbx, false
	}
	if strings.Contains(block, buildFileID) || strings.Contains(block, "PalbaseGenerated.swift in Sources") {
		return pbx, false
	}
	lines := strings.Split(pbx, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), sourcesID+" ") && strings.Contains(line, "= {") {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "files = (" {
					insert := "\t\t\t\t" + buildFileID + " /* PalbaseGenerated.swift in Sources */,"
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

Re-run after every push to stay in sync; ` + "`palbase push`" + ` already does (ts).`,
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

// pullTypes is the post-deploy convenience wrapper that writes into
// the default `.palbase/` directory.
func pullTypes(ctx context.Context, sc *studio.Client, endpoints config.Endpoints, ref, env string, w io.Writer) error {
	return pullTypesTo(ctx, sc, endpoints, ref, env, ".palbase", w)
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
	specBytes, err := fetchOpenAPISpec(ctx, specURL, apiKey)
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

	specBytes, err := fetchOpenAPISpec(ctx, specURL, apiKey)
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

// fetchOpenAPISpec issues a GET against the project's /openapi.json
// with the apikey header set. Returns raw bytes — we don't parse here
// because the caller writes the JSON straight to disk.
func fetchOpenAPISpec(ctx context.Context, specURL, apiKey string) ([]byte, error) {
	req, err := newJSONRequest(ctx, "GET", specURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", specURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fetch %s: %d %s", specURL, resp.StatusCode, string(body))
	}
	return body, nil
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
