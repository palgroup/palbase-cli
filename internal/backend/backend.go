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
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// templateFS embeds the default backend project bundle (palbase.toml,
// endpoints/hello/get.js, package.json, .gitignore). `init` falls back
// to this when Studio's pull stream is unavailable, e.g. while the
// orchestrator workflow is still seeding the remote git repo.
//
//go:embed all:template/*
var templateFS embed.FS

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
	Auth   func() *auth.Client
	Studio func() *studio.Client
}

// Cmd builds the cobra `backend` group. Subcommands call the resolvers
// at action time, after PersistentPreRunE has finished.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backend",
		Short: "Manage the per-project backend runtime",
		Long: `Commands for the per-project backend (defineEndpoint runtime).

  palbase backend init      Enable the backend pod and scaffold the project.
  palbase backend dev       Run endpoints/ locally with hot reload.
  palbase backend deploy    Push current code as a new version + activate.
  palbase backend list      Show recent versions (newest first).
  palbase backend rollback  Roll back to a previous version SHA.`,
	}
	cmd.AddCommand(
		newInitCmd(r),
		newDevCmd(),
		newDeployCmd(r),
		newListCmd(r),
		newRollbackCmd(r),
		newStatusCmd(r),
	)
	return cmd
}

// ── helpers ─────────────────────────────────────────────────────────────

// writeStringFlag is a tiny helper so subcommands can compose without
// rewriting boilerplate.
type stringFlag struct {
	value string
}

func (s *stringFlag) String() string         { return s.value }
func (s *stringFlag) Set(v string) error     { s.value = v; return nil }
func (s *stringFlag) Type() string           { return "string" }

// projectRef resolves the linked project ref. Order:
//   1. --ref flag override
//   2. .palbase/config.json's project_id (link writes it as the ref)
//   3. error
func projectRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	cfg, err := auth.LoadProjectConfig()
	if err != nil {
		return "", fmt.Errorf("project not linked — run `palbase link --project <ref>` first (%w)", err)
	}
	if cfg.ProjectID == "" {
		return "", fmt.Errorf("project_id missing from .palbase/config.json")
	}
	return cfg.ProjectID, nil
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
// tar.gz for the deploy upload.
func bundleCwd(root string) ([]byte, error) {
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

func newInitCmd(r Resolvers) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Enable backend on this project and download the template into cwd",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := projectRef(refFlag)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fmt.Printf("→ enabling backend on %s ...\n", ref)
			var enableResult struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			if err := r.Studio().Mutation(ctx, "backend.enable", map[string]any{"ref": ref}, &enableResult); err != nil {
				return fmt.Errorf("backend.enable: %w", err)
			}
			fmt.Printf("  workflow: %s\n", enableResult.WorkflowID)
			fmt.Println("  waiting for backend to become ready ...")
			if err := waitForBackendEnabled(ctx, r.Studio(), ref, 4*time.Minute); err != nil {
				return fmt.Errorf("backend never became ready: %w", err)
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			fmt.Println("  pulling template into cwd ...")
			var pull struct {
				Version string `json:"version"`
				Archive string `json:"archive"`
				Size    int    `json:"size"`
			}
			if err := r.Studio().Query(ctx, "backend.pull", map[string]any{"ref": ref}, &pull); err != nil {
				// Fall back to the embedded template — this is normal during
				// the brief window where the workflow has marked enabled but
				// the seed commit's reflection in HEAD hasn't propagated.
				fmt.Println("  ! pull failed, falling back to embedded template")
				if err := extractFS(templateFS, "template", cwd); err != nil {
					return fmt.Errorf("extract embedded template: %w", err)
				}
			} else {
				archive, decErr := base64.StdEncoding.DecodeString(pull.Archive)
				if decErr != nil {
					return fmt.Errorf("decode pull archive: %w", decErr)
				}
				if err := extractTarGzReplace(archive, cwd); err != nil {
					return fmt.Errorf("extract pull archive: %w", err)
				}
				fmt.Printf("  template version: %s (%d bytes)\n", pull.Version, pull.Size)
			}

			// Stamp the ref into palbase.toml so dev/deploy don't need a
			// separate flag once init has run.
			if err := stampRefIntoToml(filepath.Join(cwd, "palbase.toml"), ref); err != nil {
				return fmt.Errorf("update palbase.toml: %w", err)
			}

			// Re-use the existing link helper so .palbase/config.json gets
			// the project ref and .gitignore picks up .palbase/.
			if err := auth.SaveProjectConfig(&auth.ProjectConfig{ProjectID: ref, DefaultEnv: "main"}); err != nil {
				return err
			}

			fmt.Println("✓ backend ready")
			fmt.Println()
			fmt.Println("Next steps:")
			fmt.Println("  palbase backend dev       # run locally with hot reload")
			fmt.Println("  palbase backend deploy    # publish to production")
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	return cmd
}

func newDevCmd() *cobra.Command {
	var port int
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
				return fmt.Errorf("no endpoints/ directory in cwd — run `palbase backend init` first")
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

			node := exec.CommandContext(ctx, "node", filepath.Join(tmpDir, "dev-server.js"))
			node.Env = append(os.Environ(),
				fmt.Sprintf("PALBASE_DEV_PORT=%d", port),
				fmt.Sprintf("PALBASE_DEV_ROOT=%s", cwd),
				fmt.Sprintf("PALBASE_PROJECT_REF=%s", ref),
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
	return cmd
}

func newDeployCmd(r Resolvers) *cobra.Command {
	var refFlag string
	var message string
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Bundle current project and deploy a new version",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := projectRef(refFlag)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			fmt.Printf("→ bundling %s ...\n", cwd)
			archive, err := bundleCwd(cwd)
			if err != nil {
				return fmt.Errorf("bundle: %w", err)
			}
			fmt.Printf("  bundle: %d bytes\n", len(archive))
			fmt.Println("→ uploading via Studio ...")
			var resp struct {
				Version string `json:"version"`
				Files   int    `json:"files"`
			}
			if err := r.Studio().Mutation(cmd.Context(), "backend.deploy", map[string]any{
				"ref":     ref,
				"archive": base64.StdEncoding.EncodeToString(archive),
				"message": message,
			}, &resp); err != nil {
				return fmt.Errorf("backend.deploy: %w", err)
			}
			fmt.Printf("✓ deployed version %s (%d files)\n", resp.Version, resp.Files)
			fmt.Printf("  https://%s.dev.palbase.studio/api/*\n", ref)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVarP(&message, "message", "m", "", "Optional commit message recorded in git history")
	return cmd
}

func newListCmd(r Resolvers) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show deploy history (newest first)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := projectRef(refFlag)
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
	cmd := &cobra.Command{
		Use:   "rollback <version-sha>",
		Args:  cobra.ExactArgs(1),
		Short: "Roll back to a previous version (creates a new commit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := projectRef(refFlag)
			if err != nil {
				return err
			}
			version := args[0]
			var resp struct {
				Status         string `json:"status"`
				Version        string `json:"version"`
				RolledBackFrom string `json:"rolled_back_from"`
			}
			if err := r.Studio().Mutation(cmd.Context(), "backend.rollback", map[string]any{
				"ref":     ref,
				"version": version,
			}, &resp); err != nil {
				return fmt.Errorf("backend.rollback: %w", err)
			}
			fmt.Printf("✓ rolled back to %s (new HEAD: %s)\n", resp.RolledBackFrom, resp.Version)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	return cmd
}

func newStatusCmd(r Resolvers) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show backend enable + active-version state",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := projectRef(refFlag)
			if err != nil {
				return err
			}
			var resp struct {
				Ref            string  `json:"ref"`
				BackendEnabled bool    `json:"backendEnabled"`
				Head           *string `json:"head"`
				ActiveVersion  *string `json:"activeVersion"`
			}
			if err := r.Studio().Query(cmd.Context(), "backend.status", map[string]any{"ref": ref}, &resp); err != nil {
				return fmt.Errorf("backend.status: %w", err)
			}
			fmt.Printf("ref:             %s\n", resp.Ref)
			fmt.Printf("backend_enabled: %v\n", resp.BackendEnabled)
			if resp.Head != nil {
				fmt.Printf("head:            %s\n", *resp.Head)
			}
			if resp.ActiveVersion != nil {
				fmt.Printf("active:          %s\n", *resp.ActiveVersion)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	return cmd
}

// waitForBackendEnabled polls Studio until backend.status reports
// backend_enabled = true (or the timeout elapses). The CLI doesn't
// trust the workflow start response alone — Temporal acks the start
// well before activities run.
func waitForBackendEnabled(ctx context.Context, c *studio.Client, ref string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var resp struct {
			BackendEnabled bool `json:"backendEnabled"`
		}
		if err := c.Query(ctx, "backend.status", map[string]any{"ref": ref}, &resp); err == nil && resp.BackendEnabled {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("timed out after %s", timeout)
}

// stampRefIntoToml is a small file edit so the user doesn't have to.
// We don't fully parse TOML — we just look for `ref = ""` under
// `[project]` and replace it. If the structure is more elaborate, we
// leave it untouched and return nil; the user can edit by hand.
func stampRefIntoToml(path, ref string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	updated := strings.Replace(string(body), `ref = ""`, fmt.Sprintf(`ref = %q`, ref), 1)
	if updated == string(body) {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644)
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
