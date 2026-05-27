// Package backend provides the `palbase backend` subcommand group:
// init / dev / deploy / list / rollback. These commands cover the full
// developer loop for the per-project backend-runtime pod (Phase 7).
//
// All remote calls go through Studio's tRPC layer via the studio
// package — never directly to br-<ref> — so org-membership + the
// backend_enabled gate are enforced server-side.
package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"encoding/base64"
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

// defaultHTTPClient is reused by `palbase backend types` (and any
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
// CLI binary so `palbase backend dev` works without an internet round
// trip; copied to a temp dir at runtime so Node can resolve relative
// requires the way it would inside a real package.
//
//go:embed devjs/dev-server.js
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
// so "pull/push the project" is a single action for the developer.
func Commands(r Resolvers) []*cobra.Command {
	return []*cobra.Command{
		newPullCmd(r),
		newPushCmd(r),
		newDevCmd(r),
		newListCmd(r),
		newRollbackCmd(r),
		newStatusCmd(r),
		newTypesCmd(r),
	}
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
// lets the init/dev/deploy commands auto-link via project.list when the
// user hasn't run `palbase link` first.
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

	if !isInteractive() {
		return "", fmt.Errorf("project not linked — pass --ref or run `palbase link <ref>`")
	}

	var rows []auth.Project
	if listErr := c.Query(ctx, "project.list", nil, &rows); listErr != nil {
		return "", fmt.Errorf("auto-link: %w", listErr)
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("no projects in your account — create one at the Palbase Studio dashboard first")
	}

	var picked auth.Project
	if len(rows) == 1 {
		picked = rows[0]
		fmt.Fprintf(out, "Linking to your only project: %s (%s)\n", picked.Name, picked.Ref)
	} else {
		fmt.Fprintln(out, "Select a project:")
		for i, p := range rows {
			fmt.Fprintf(out, "  %d) %s (%s)\n", i+1, p.Name, p.Ref)
		}
		fmt.Fprint(out, "Enter number: ")
		var choice int
		if _, scanErr := fmt.Fscan(os.Stdin, &choice); scanErr != nil {
			return "", fmt.Errorf("invalid selection: %w", scanErr)
		}
		if choice < 1 || choice > len(rows) {
			return "", fmt.Errorf("invalid selection: %d", choice)
		}
		picked = rows[choice-1]
	}

	// Default to the project's main branch: a fresh link should pull/dev
	// against main, not staging. --branch selects another branch per command.
	if err := auth.SaveProjectConfig(&auth.ProjectConfig{Ref: picked.Ref, DefaultEnv: "main"}); err != nil {
		return "", fmt.Errorf("save .palbase/config.json: %w", err)
	}
	fmt.Fprintf(out, "✓ Linked to %s (%s)\n", picked.Name, picked.Ref)
	return picked.Ref, nil
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

// bundleCwd packs the current project (excluding common dev junk) as
// tar.gz for the deploy upload, with a freshly-rendered palbase.toml
// stitched in at the end. The TOML is regenerated from the project's
// ref on every deploy so user edits can't desync from control-pg.
func bundleCwd(root, ref string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	skipDirs := map[string]struct{}{
		".git":         {},
		"node_modules": {},
		".palbase":     {},
		".next":        {},
		"dist":         {},
		"build":        {},
	}
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
		base := filepath.Base(rel)
		if _, skip := skipDirs[base]; skip && info.IsDir() {
			return filepath.SkipDir
		}
		// Skip dotfiles outside .gitignore — keep .gitignore but drop
		// stuff like .DS_Store + per-tool caches.
		if strings.HasPrefix(base, ".") && base != ".gitignore" && !info.IsDir() {
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
		Use:   "dev",
		Short: "Run endpoints/ locally with hot reload",
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

			// Reveal the project's anon + service-role keys so dev-server
			// can build a real ServerClient. If reveal fails (no login,
			// no project link, network down), we still launch dev-server
			// without the module clients — ctx.docs/ctx.storage/… throw on
			// first use, so the user sees a clear error instead of silent
			// partial behaviour.
			var revealResp struct {
				EndpointRef    string `json:"endpointRef"`
				AnonKey        string `json:"anonKey"`
				ServiceRoleKey string `json:"serviceRoleKey"`
			}
			if ref != "" && ref != "local" {
				if err := r.Studio().Query(ctx, "apikey.reveal", map[string]any{"ref": ref}, &revealResp); err != nil {
					fmt.Fprintf(os.Stderr, "warning: apikey.reveal failed (%v) — ctx.docs/ctx.storage/… will be unavailable\n", err)
				}
			}

			node := exec.CommandContext(ctx, "node", filepath.Join(tmpDir, "dev-server.js"))
			// dev-server.js runs from a temp dir but needs to require()
			// the user's local @palbase/server (and any other declared
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
				fmt.Sprintf("PALBASE_TENANT_APIKEY=%s", revealResp.AnonKey),
				fmt.Sprintf("PALBASE_TENANT_SERVICE_ROLE=%s", revealResp.ServiceRoleKey),
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
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push the project (deploy code + apply config) to the server",
		Long: `Push everything for the linked project in one step:

  • code → bundled, uploaded as a new version, and activated
  • config-as-code → config/*.toml applied to the server (ordered, conflict-gated)

The code deploy runs first; config push follows. A config-push failure is
reported but does NOT roll back the code deploy that already landed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ref, err := resolveOrLinkRef(ctx, refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			// Active branch (--branch wins, else ProjectConfig.DefaultEnv;
			// "" = main). NAMED GAP: backend.deploy and the config-as-code
			// push procedures don't accept a branch field yet. Branch-aware
			// push lands when Track A adds it server-side; then the
			// backend.deploy payload below gets
			//   if branch != "" { payload["branch"] = branch }
			// and this resolve already feeds it.
			branch := resolveActiveBranch(branchFlag)
			if branch != "" {
				fmt.Printf("note: --branch %q requested, but server-side branch push isn't wired yet; pushing the default branch.\n", branch)
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
			fmt.Println("→ uploading via Studio ...")
			var resp struct {
				Version string `json:"version"`
				Files   int    `json:"files"`
			}
			if err := r.Studio().Mutation(ctx, "backend.deploy", map[string]any{
				"ref":     ref,
				"archive": base64.StdEncoding.EncodeToString(archive),
				"message": message,
			}, &resp); err != nil {
				return fmt.Errorf("backend.deploy: %w", err)
			}
			fmt.Printf("✓ deployed version %s (%d files)\n", resp.Version, resp.Files)
			fmt.Printf("  https://%s.dev.palbase.studio/rpc/*\n", ref)

			// Adım B14 — auto-pull types after every successful deploy.
			// `--no-types` disables for CI loops where the typed
			// declarations aren't useful.
			if !skipTypes {
				if err := pullTypes(ctx, r.Studio(), ref, "remote", os.Stdout); err != nil {
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
			if cfgErr := runConfigPush(ctx, cwd, ref, r.Studio(), os.Stdout); cfgErr != nil {
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
	return cmd
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
// palbase backend pull
//
// Pulls the branch's latest code archive (same as init does) and then
// fetches the decrypted branch env vars via env.pull, writing them to
// .env.local so `palbase backend dev` has the real values.
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
code pull is the load-bearing step.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ref, err := resolveOrLinkRef(ctx, refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			// Active branch (--branch wins, else ProjectConfig.DefaultEnv;
			// "" = main). NAMED GAP: backend.pull / env.pull / config-as-code
			// tRPC procedures don't accept a branch field yet (env.pull is
			// .strict() and would reject one). Branch-aware pull lands when
			// Track A adds the field server-side; then each payload below gets
			//   if branch != "" { payload["branch"] = branch }
			// and this resolve already feeds it.
			branch := resolveActiveBranch(branchFlag)
			if branch != "" {
				fmt.Printf("note: --branch %q requested, but server-side branch pull isn't wired yet; pulling the default branch.\n", branch)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
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
			if cfgErr := runConfigPull(ctx, cwd, ref, r.Studio(), os.Stdout); cfgErr != nil {
				fmt.Fprintf(os.Stderr, "warning: config pull failed (%v) — code + env were still pulled\n", cfgErr)
			}

			fmt.Println("✓ pull complete")
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to pull (defaults to the active branch; omit for main)")
	return cmd
}

// ─────────────────────────────────────────────────────────────────────
// Adım B14 — `palbase backend types`
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

  palbase backend types --lang swift \
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
				return pullSwiftTypes(cmd.Context(), r.Studio(), ref, envFlag, out, os.Stdout)
			case "ts", "":
				if outDir == "" {
					outDir = ".palbase"
				}
				return pullTypesTo(cmd.Context(), r.Studio(), ref, envFlag, outDir, os.Stdout)
			default:
				return fmt.Errorf("unknown --lang %q (expected ts|swift)", langFlag)
			}
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&envFlag, "env", "remote", "Spec source: remote (Kong gateway) | local (palbase backend dev on localhost:4003)")
	cmd.Flags().StringVar(&langFlag, "lang", "ts", "Output language: ts | swift")
	cmd.Flags().StringVar(&outDir, "out", ".palbase", "Output: dir for ts (.palbase), file for swift (PalbaseEndpoints.swift)")
	return cmd
}

// pullTypes is the post-deploy convenience wrapper that writes into
// the default `.palbase/` directory.
func pullTypes(ctx context.Context, sc *studio.Client, ref, env string, w io.Writer) error {
	return pullTypesTo(ctx, sc, ref, env, ".palbase", w)
}

// pullTypesTo does the actual work — fetch /openapi.json, write JSON,
// shell out to `npx openapi-typescript` for the .d.ts. Failures emit a
// warning but don't block; the JSON file alone is useful (Studio,
// Postman). Augmentation of `interface BackendEndpoints` is what
// drives the typed `pb.backend.call(name, input)` API.
// pullSwiftTypes fetches the OpenAPI spec and generates a Swift file of
// namespaced typed calls for the Palbe iOS SDK. Self-contained — no npx.
func pullSwiftTypes(ctx context.Context, sc *studio.Client, ref, env, outFile string, w io.Writer) error {
	specURL, err := openAPIURL(ctx, sc, ref, env)
	if err != nil {
		return err
	}
	apiKey, err := lookupAnonAPIKey(ctx, sc, ref)
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

func pullTypesTo(ctx context.Context, sc *studio.Client, ref, env, outDir string, w io.Writer) error {
	specURL, err := openAPIURL(ctx, sc, ref, env)
	if err != nil {
		return err
	}

	apiKey, err := lookupAnonAPIKey(ctx, sc, ref)
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

// openAPIURL resolves the spec URL for the chosen environment.
func openAPIURL(ctx context.Context, sc *studio.Client, ref, env string) (string, error) {
	switch env {
	case "remote", "":
		// Studio knows the project domain from control-pg; a tRPC
		// helper would be ideal but for B14.2 we hard-code the dev
		// domain — `palbase push` itself uses the same.
		return fmt.Sprintf("https://%s.dev.palbase.studio/openapi.json", ref), nil
	case "local":
		return "http://localhost:4003/openapi.json", nil
	default:
		return "", fmt.Errorf("unknown --env %q (expected remote|local)", env)
	}
}

// lookupAnonAPIKey calls Studio's apikey.reveal to get the project's
// anon key. The deployed `/openapi.json` is gated by the same Kong
// apikey middleware as `/rpc/*` so we need a real key to fetch it.
func lookupAnonAPIKey(ctx context.Context, sc *studio.Client, ref string) (string, error) {
	var resp struct {
		AnonKey string `json:"anonKey"`
	}
	if err := sc.Query(ctx, "apikey.reveal", map[string]any{"ref": ref}, &resp); err != nil {
		return "", fmt.Errorf("apikey.reveal: %w", err)
	}
	if resp.AnonKey == "" {
		return "", errors.New("apikey.reveal: missing anon key")
	}
	return resp.AnonKey, nil
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
	header := []byte("// AUTO-GENERATED FROM YOUR DEPLOYED BACKEND. DO NOT EDIT — RUN 'palbase backend types' TO REFRESH.\n\n")
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
