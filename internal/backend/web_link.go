package backend

// web_link.go — `palbase web link` / `palbase web unlink`
//
// `palbase web link` wires a web project (package.json present) to a Palbase
// project:
//   1. Verifies package.json exists in the cwd.
//   2. resolveOrLinkRef — writes .palbase/config.json when missing.
//   3. Runs TS codegen (via webLinkCodegen seam) → --out (default palbe.gen.ts).
//   4. Patches package.json: adds scripts.predev / scripts.prebuild to run
//      `palbase types --soft`, preserving top-level key order and warning (not
//      clobbering) when a script already has a different value.
//   5. Inserts `import './<rel-to-gen>';` at the top of the detected entry file.
//   6. Warns (exit 0) when .gitignore ignores the gen file.
//
// `palbase web unlink` removes .palbase/config.json (+ dir if empty), leaving
// the gen file and scripts untouched.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// webLinkCodegen is the codegen seam. Tests replace it with a stub; production
// calls pullTSTypes via the real ts codegen pipeline (env "auto").
var webLinkCodegen = func(ctx context.Context, r Resolvers, ref, outFile string, w io.Writer) error {
	// branch: load from .palbase/config.json DefaultEnv, or default to "main".
	branch := "main"
	if cfg, err := auth.LoadProjectConfig(); err == nil && cfg.DefaultEnv != "" {
		branch = cfg.DefaultEnv
	}
	return pullTSTypes(ctx, r.Studio(), r.Endpoints(), ref, branch, "auto", outFile, w)
}

// webCmd holds the resolvers for the web command group. Exported as a struct
// so tests can get a handle on it without a full cobra setup.
type webCmd struct {
	r Resolvers
}

const webTypesCmd = "palbase types --soft"

// newWebCmd builds the `palbase web` command group.
func newWebCmd(r Resolvers) *cobra.Command {
	wc := &webCmd{r: r}
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Wire a web project to a Palbase project",
	}
	cmd.AddCommand(wc.newWebLinkCmd(), wc.newWebUnlinkCmd())
	return cmd
}

// newWebLinkCmd builds `palbase web link`.
func (wc *webCmd) newWebLinkCmd() *cobra.Command {
	var refFlag string
	var entryFlag string
	var outFlag string

	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link this web project to a Palbase project and generate typed SDK code",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ctx := cmd.Context()

			outFile := outFlag
			if outFile == "" {
				outFile = "palbe.gen.ts"
			}

			// 1. Must run where package.json exists.
			if _, err := os.Stat("package.json"); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("web link must run in the web app root (package.json not found)")
				}
				return err
			}

			// 2. Resolve or link the project ref.
			// Guard the Studio() call: in tests noopResolvers sets Studio=nil
			// and the func would panic. When --ref is provided, resolveOrLinkRef
			// returns before touching the studio client anyway.
			var sc *studio.Client
			if wc.r.Studio != nil {
				sc = wc.r.Studio()
			}
			ref, err := resolveOrLinkRef(ctx, refFlag, sc, out)
			if err != nil {
				return err
			}
			// resolveOrLinkRef only writes .palbase/config.json when it goes
			// through the picker path (no override). When --ref is supplied in a
			// fresh cwd the config is absent. Materialise it so subsequent steps
			// (codegen branch resolution, re-runs) always find the link.
			if _, cfgErr := auth.LoadProjectConfig(); cfgErr != nil {
				if saveErr := auth.SaveProjectConfig(&auth.ProjectConfig{Ref: ref, DefaultEnv: "main"}); saveErr != nil {
					return fmt.Errorf("save .palbase/config.json: %w", saveErr)
				}
				fmt.Fprintf(out, "✓ Linked to %s\n", ref)
			}

			// 3. First codegen.
			if err := webLinkCodegen(ctx, wc.r, ref, outFile, out); err != nil {
				return fmt.Errorf("codegen: %w", err)
			}

			// 4. Patch package.json scripts.
			if err := patchPackageJSONScripts("package.json", out); err != nil {
				return fmt.Errorf("patch package.json: %w", err)
			}

			// 5. Wire import in entry file.
			if err := wireEntryImport(entryFlag, outFile, out); err != nil {
				return fmt.Errorf("wire entry import: %w", err)
			}

			// 6. Gitignore guard.
			checkGitignoreGuard(outFile, out)

			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (skips interactive picker; required in non-interactive shells)")
	cmd.Flags().StringVar(&entryFlag, "entry", "", "Entry file to wire the import into (auto-detected when absent)")
	cmd.Flags().StringVar(&outFlag, "out", "", "Gen file name (default: palbe.gen.ts)")
	return cmd
}

// newWebUnlinkCmd builds `palbase web unlink`.
func (wc *webCmd) newWebUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Detach this web project from its Palbase project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			cfgPath := filepath.Join(".palbase", "config.json")
			if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove project config: %w", err)
			}

			// Remove .palbase/ if empty.
			if entries, err := os.ReadDir(".palbase"); err == nil && len(entries) == 0 {
				_ = os.Remove(".palbase")
			}

			fmt.Fprintln(out, "✓ unlinked — removed .palbase/config.json")
			fmt.Fprintln(out, "  palbe.gen.ts and package.json scripts left in place — re-link with `palbase web link`")
			return nil
		},
	}
}

// ── package.json ordered editing ─────────────────────────────────────────────

// orderedKV is a single key-value pair in a JSON object, preserving insertion
// order (encoding/json's map doesn't guarantee order).
type orderedKV struct {
	key string
	raw json.RawMessage
}

// parseOrderedObject parses a JSON object into a slice of key-value pairs,
// preserving key order. Only the TOP level of the object is ordered; nested
// values are kept as raw JSON and are not re-ordered.
func parseOrderedObject(data []byte) ([]orderedKV, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("expected '{', got %v", tok)
	}

	var pairs []orderedKV
	for dec.More() {
		// key
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key, got %T", keyTok)
		}
		// value — capture as raw JSON
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		pairs = append(pairs, orderedKV{key: key, raw: raw})
	}
	return pairs, nil
}

// marshalOrderedObject serialises a []orderedKV back into a JSON object with
// 2-space indent, matching npm's default package.json style.
func marshalOrderedObject(pairs []orderedKV, indent string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, kv := range pairs {
		keyBytes, err := json.Marshal(kv.key)
		if err != nil {
			return nil, err
		}
		// Re-indent the raw value. If it starts with { or [ we need to
		// indent its inner lines; otherwise it's a scalar and needs none.
		valIndented, err := reindentJSON(kv.raw, indent+"  ")
		if err != nil {
			return nil, err
		}
		buf.WriteString(indent + "  ")
		buf.Write(keyBytes)
		buf.WriteString(": ")
		buf.Write(valIndented)
		if i < len(pairs)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(indent + "}")
	return buf.Bytes(), nil
}

// reindentJSON takes a raw JSON value and returns it with its inner lines
// re-indented to `innerIndent`. For scalars (string, number, bool, null),
// it's returned as-is. For objects and arrays, json.MarshalIndent is used.
func reindentJSON(raw json.RawMessage, innerIndent string) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, nil
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return trimmed, nil
	}
	// Re-marshal the inner object with the chosen indent.
	var v interface{}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return trimmed, nil
	}
	out, err := json.MarshalIndent(v, strings.TrimSuffix(innerIndent, "  "), "  ")
	if err != nil {
		return trimmed, nil
	}
	return out, nil
}

// patchPackageJSONScripts reads package.json, adds/warns-about
// scripts.predev and scripts.prebuild, and writes it back preserving key order.
func patchPackageJSONScripts(pkgPath string, w io.Writer) error {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return err
	}

	pairs, err := parseOrderedObject(data)
	if err != nil {
		return fmt.Errorf("parse package.json: %w", err)
	}

	// Find or create the "scripts" key.
	scriptsIdx := -1
	for i, kv := range pairs {
		if kv.key == "scripts" {
			scriptsIdx = i
			break
		}
	}

	var scriptPairs []orderedKV
	if scriptsIdx >= 0 {
		// Parse the existing scripts object.
		sp, err := parseOrderedObject(pairs[scriptsIdx].raw)
		if err != nil {
			return fmt.Errorf("parse scripts: %w", err)
		}
		scriptPairs = sp
	}

	// For each hook key, check / add / warn.
	for _, hookKey := range []string{"predev", "prebuild"} {
		existing := ""
		existIdx := -1
		for i, kv := range scriptPairs {
			if kv.key == hookKey {
				// Unmarshal the existing value to compare.
				var s string
				if err := json.Unmarshal(kv.raw, &s); err == nil {
					existing = s
				}
				existIdx = i
				break
			}
		}
		if existIdx >= 0 {
			if existing != webTypesCmd {
				// Warn, leave untouched.
				fmt.Fprintf(w, "warning: scripts.%s is already set to %q — skipping (suggested value: %q)\n", hookKey, existing, webTypesCmd)
			}
			// Already correct — nothing to do.
			continue
		}
		// Add the key.
		val, _ := json.Marshal(webTypesCmd)
		scriptPairs = append(scriptPairs, orderedKV{key: hookKey, raw: val})
	}

	// Serialise the scripts sub-object.
	scriptsBytes, err := marshalOrderedObject(scriptPairs, "  ")
	if err != nil {
		return fmt.Errorf("marshal scripts: %w", err)
	}

	if scriptsIdx >= 0 {
		pairs[scriptsIdx].raw = scriptsBytes
	} else {
		// Append a new "scripts" key at the end.
		pairs = append(pairs, orderedKV{key: "scripts", raw: scriptsBytes})
	}

	out, err := marshalOrderedObject(pairs, "")
	if err != nil {
		return fmt.Errorf("marshal package.json: %w", err)
	}
	out = append(out, '\n')
	return os.WriteFile(pkgPath, out, 0o644)
}

// ── entry file import wiring ──────────────────────────────────────────────────

// autoEntryPaths is the ordered list of entry files to probe when --entry is
// not given.
var autoEntryPaths = []string{
	"app/layout.tsx",
	"src/app/layout.tsx",
	"src/main.tsx",
	"src/main.ts",
	"main.tsx",
}

// wireEntryImport inserts `import '<rel>';` into the detected (or explicit)
// entry file. Idempotent: skips if an import of a path ending in the gen stem
// already exists.
func wireEntryImport(entryFlag, outFile string, w io.Writer) error {
	genStem := strings.TrimSuffix(outFile, ".ts") // e.g. "palbe.gen"

	// Resolve entry file.
	entryPath := entryFlag
	if entryPath == "" {
		for _, candidate := range autoEntryPaths {
			if _, err := os.Stat(candidate); err == nil {
				entryPath = candidate
				break
			}
		}
	}

	if entryPath == "" {
		// Unknown layout — print manual instruction, exit 0.
		fmt.Fprintf(w, "note: could not detect an entry file — add the following import manually:\n")
		fmt.Fprintf(w, "  import './%s';\n", genStem)
		return nil
	}

	// Compute the relative import path from the entry file's dir to the gen file.
	entryDir := filepath.Dir(entryPath)
	// genFile lives in cwd.
	rel, err := filepath.Rel(entryDir, outFile)
	if err != nil {
		return fmt.Errorf("rel path from %s to %s: %w", entryDir, outFile, err)
	}
	// POSIX path, strip .ts extension.
	rel = filepath.ToSlash(rel)
	rel = strings.TrimSuffix(rel, ".ts")
	// Ensure leading ./
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}

	importLine := fmt.Sprintf("import '%s';", rel)

	body, err := os.ReadFile(entryPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", entryPath, err)
	}

	// Idempotency: skip if any line already imports a path ending with the stem.
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") && strings.Contains(trimmed, genStem) {
			return nil
		}
	}

	// Insert after the last contiguous top-of-file import statement.
	lines := strings.Split(string(body), "\n")
	insertAfter := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "//") {
			insertAfter = i
		} else {
			break
		}
	}

	var newLines []string
	if insertAfter < 0 {
		// No imports — prepend.
		newLines = append([]string{importLine}, lines...)
	} else {
		newLines = append(lines[:insertAfter+1], append([]string{importLine}, lines[insertAfter+1:]...)...)
	}

	return os.WriteFile(entryPath, []byte(strings.Join(newLines, "\n")), 0o644)
}

// ── gitignore guard ───────────────────────────────────────────────────────────

// checkGitignoreGuard prints a loud warning if .gitignore would ignore the gen
// file. Does NOT modify .gitignore.
func checkGitignoreGuard(outFile string, w io.Writer) {
	data, err := os.ReadFile(".gitignore")
	if err != nil {
		return
	}
	content := string(data)
	basename := filepath.Base(outFile)
	ext := filepath.Ext(basename) // e.g. ".ts"
	stem := strings.TrimSuffix(basename, ext)

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Match: exact filename, *.gen.ts pattern, or exact out path.
		if line == basename || line == outFile {
			printGitignoreWarning(w, outFile)
			return
		}
		// *.gen.ts style glob: *.gen.<ext>
		if strings.HasPrefix(line, "*.") {
			suffix := line[1:] // e.g. ".gen.ts"
			if strings.HasSuffix(basename, suffix) {
				printGitignoreWarning(w, outFile)
				return
			}
		}
		// stem.* style: if the line is "palbe.gen.*" — treat generously.
		if strings.Contains(line, "*") && strings.HasPrefix(line, stem+".") {
			printGitignoreWarning(w, outFile)
			return
		}
	}
}

func printGitignoreWarning(w io.Writer, outFile string) {
	fmt.Fprintf(w, "\nWARNING: .gitignore appears to ignore %s — this file must be committed\n", outFile)
	fmt.Fprintf(w, "         (it is the typed client your app imports, not a build artifact)\n")
	fmt.Fprintf(w, "         Remove the matching .gitignore rule or force-add the file:\n")
	fmt.Fprintf(w, "           git add -f %s\n\n", outFile)
}
