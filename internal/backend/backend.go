// Package backend provides commands for building, linking and deploying a project.
// Target-relative commands act on the checkout's linked address.
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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/spf13/cobra"
)

// buildCheckFS embeds the Node.js side of `palbase build`. Shipped beside the
// CLI binary so validation works without an internet round trip; copied to a
// temp dir at runtime so Node can resolve relative requires the way it would
// inside a real package — build-check.js require()s its three siblings, so all
// of them must land in that dir.
//
// return_types.js, throw_analysis.js, tx_analysis.js and extract_meta.js are
// byte-identical copies of the deploy runtime's own
// (modules/backend/internal/runtime/*). That identity is what makes a local
// PASS mean the deploy accepts the tree. Neither submodule's CI can see the
// other, so the comparison runs in the PARENT repo: palbase's
// .github/workflows/stager-copy-parity.yml diffs each file against
// modules/backend on every push/PR touching either submodule and fails the
// moment a copy drifts — there is no Go test for this in this repo.
//
//go:embed devjs/build-check.js devjs/env-gen.js devjs/stack-gen.js devjs/return_types.js devjs/throw_analysis.js devjs/tx_analysis.js devjs/extract_meta.js devjs/generics.js
var buildCheckFS embed.FS

// REST is the control-plane transport used to resolve cloud project names.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers supplies cloud configuration at command execution time.
type Resolvers struct {
	Endpoints func() config.Endpoints
	REST      func() REST
}

// Commands returns the backend commands mounted at the CLI root.
func Commands(r Resolvers) []*cobra.Command {
	return []*cobra.Command{
		newBuildCmd(),
		newDeploysCmd(),
		newRollbackCmd(),
		newStatusCmd(),
		newSpecCmd(),
		newCloneCmd(r),
		newPullCmd(),
		newPushCmd(),
		// Takes the resolvers for ONE reason: a bare Environment ref has to become
		// an address, and only the configured cloud knows the suffix. Everything
		// after that is unchanged — it is told where the stack is, once, and asks
		// the stack itself for the rest.
		newLinkCmd(r),
		newUnlinkCmd(),
		newPlanCmd(),
		newInitCmd(),
		newStartCmd(),
		newStopCmd(),
	}
}

// ── helpers ─────────────────────────────────────────────────────────────

// removeTemp deletes a scratch directory, best effort. Every caller is either
// deferring cleanup at the end of a one-shot command or already returning a
// more important error, so a failed removal has nowhere to go: the worst case
// is a temp dir the OS sweeps later, and reporting it would displace the error
// the user actually needs to read.
func removeTemp(dir string) { _ = os.RemoveAll(dir) }

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

// buildToolMissing reports whether `pkg` is absent from the project's
// node_modules. Used for the metadata extractor's OWN runtime tools (not the
// user's declared deps) that the deploy runtime provides globally (Dockerfile)
// but a local validation run must supply itself.
func buildToolMissing(projectDir, pkg string) bool {
	_, err := os.Stat(filepath.Join(projectDir, "node_modules", pkg))
	return os.IsNotExist(err)
}

// ensureBuildCheckTools guarantees the packages extract_meta.js require()s to
// behave like the deployed extractor are present in node_modules, WITHOUT
// writing them into the user's package.json (--no-save) — they're the runtime's
// dependencies, not the project's. Today that's zod-to-json-schema, which the
// deploy runtime installs globally (modules/backend/Dockerfile) but a local run
// resolves from the project's node_modules; without it the extractor's header
// rules and schema lowering degrade. Best-effort: a failed install only weakens
// the check, so we warn and continue rather than fail the build.
func ensureBuildCheckTools(dir string) {
	const pkg = "zod-to-json-schema"
	if !buildToolMissing(dir, pkg) {
		return
	}
	bin, err := exec.LookPath("npm")
	if err != nil {
		return // installNodeDeps already surfaced the npm-missing error path
	}
	fmt.Printf("→ installing %s (extractor tool, --no-save) ...\n", pkg)
	cmd := exec.Command(bin, "install", "--no-save", "--silent", "--no-audit", "--no-fund", pkg)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  warning: could not install %s — the header/schema checks will be weaker (run `npm i %s` manually)\n", pkg, pkg)
	}
}

func newDeploysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploys",
		Args:  cobra.NoArgs,
		Short: "Show the linked project's deploy history (newest first)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return deploysOfProject(cmd)
		},
	}
	cmd.Flags().Bool("json", false, "Print deployment history as JSON")
	return cmd
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func newRollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback <version-sha>",
		Args:  cobra.ExactArgs(1),
		Short: "Roll back the linked project to a previous version",
		RunE: func(cmd *cobra.Command, args []string) error {
			return rollbackOnProject(cmd, args[0])
		},
	}
	return cmd
}

func newStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Args:  cobra.NoArgs,
		Short: "Show the linked project's active version and deploy state",
		RunE: func(cmd *cobra.Command, args []string) error {
			return statusOfProject(cmd, jsonOut)
		},
	}
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

// envGenExternals are kept external when bundling db/*.ts so the bundle
// resolves @palbase/* to the project's installed package on NODE_PATH at eval
// time — exactly like the backend-runtime's schema extractor does. Bundling
// them in would duplicate the DSL and break the `defineSchema(...)` identity.
var envGenExternals = []string{"@palbase/backend", "@palbase/core"}

// generateEnvTypes regenerates the project's palbase-env.d.ts from its
// db/*.ts. This is the typed-Database wiring: the generated file augments
// @palbase/backend/env's `Tables` interface so a project's handlers get a typed
// `Database.tables.*` with no import and no generic.
//
// EVERY schema at once, not one call per file. A foreign key from `billing` to
// `public` can only be named with both ends in hand, so a per-file generator
// would emit a relation pointing at a table it thinks does not exist — and the
// types would disagree with the database the same deploy just built.
//
// Mechanism mirrors the backend-runtime's schema extractor
// (modules/backend internal/runtime/schema_extract.js): db/*.ts imports
// @palbase/backend via ESM (which bare Node can't resolve from a tenant dir), so
// we esbuild-bundle a generated entry to a temp CJS file with @palbase/*
// external, then run the embedded env-gen.js bridge over that bundle. The
// bridge require()s the project's @palbase/backend for makeEnvDts(), calls it
// with every declaration, and writes palbase-env.d.ts to projectDir.
//
// It is a clean no-op when the project declares no database at all (a v1
// project, or a v2 project with no db/): there is no typed Database to
// generate, so we return nil without touching the filesystem. A db/ that IS
// there and cannot be read is the opposite — a mistake somebody is waiting to
// see — and it comes back as an error.
// nodeModules is where @palbase/backend actually lives, which is not always
// inside projectDir: `palbase build` validates a DEPLOY-SHAPED staging tree
// (node_modules stripped, exactly like the push tarball) while the bridge still
// has to require() the real SDK — the same split the pod has between the tenant
// source and the runtime's global install.
// envTypesFile is the derived declaration file: it augments the SDK's `Tables`
// interface so `Database.tables.*` is typed with no import and no generic.
const envTypesFile = "palbase-env.d.ts"

// stackTypesFile is the derived declaration file for the names the STACK holds.
// It augments `@palbase/backend/stack`, so `Secrets.get(...)`, the Flags client
// and `@Upload({ bucket })` accept this project's real names and nothing else.
//
// It is the thing that replaced `config/secrets.ts`. That file asked the author
// to restate, in the repo, names the vault already held, and the push compared
// the two lists at deploy. This is read FROM the stack and checked by the
// compiler, so there is no second list to drift and no check to forget.
const stackTypesFile = "palbase-stack.d.ts"

// StackNames are the three sets `palbase-stack.d.ts` is rendered from.
//
// Buckets are not plain names: `stack-gen.ts` renders a SHAPE per bucket
// carrying its variant union, so `getPublicUrl(p, { variant })` refuses a
// rendition the bucket does not declare. Sending only names made every bucket's
// union `never` — the generator was ready for this and had nothing to read.
type StackNames struct {
	Secrets []string      `json:"secrets"`
	Flags   []string      `json:"flags"`
	Buckets []StackBucket `json:"buckets"`
}

// generateStackTypes writes the project's palbase-stack.d.ts from the names the
// linked environment's stack holds.
//
// A read failure is NOT written through: the existing file is left alone and the
// error is returned. A stack that is briefly unreachable must not silently
// narrow a project's types to nothing — that would turn every Secrets.get() in
// the codebase into a compile error and read as "your code is wrong" rather than
// "the stack could not be asked".
func generateStackTypes(ctx context.Context, projectDir, nodeModules string, names StackNames) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not on PATH (Node.js required to generate %s)", stackTypesFile)
	}

	tmpDir, err := os.MkdirTemp("", "palbase-stackgen-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	scriptPath := filepath.Join(tmpDir, "stack-gen.js")
	body, err := buildCheckFS.ReadFile("devjs/stack-gen.js")
	if err != nil {
		return fmt.Errorf("read embedded stack-gen.js: %w", err)
	}
	if err := os.WriteFile(scriptPath, body, 0o644); err != nil {
		return err
	}

	reqData, err := json.Marshal(struct {
		Names   StackNames `json:"names"`
		OutPath string     `json:"out_path"`
	}{Names: names, OutPath: filepath.Join(projectDir, stackTypesFile)})
	if err != nil {
		return err
	}

	evalCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(evalCtx, "node", scriptPath)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "NODE_PATH="+nodeModules)
	cmd.Stdin = bytes.NewReader(reqData)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("stack-gen: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	var res struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return fmt.Errorf("parse stack-gen output: %w (output: %s)", err, strings.TrimSpace(stdout.String()))
	}
	if res.Error != "" {
		return fmt.Errorf("stack-gen: %s", res.Error)
	}
	return nil
}

// validateSchemaDeclaration evaluates db/ exactly as generateEnvTypes does and
// throws the result away.
//
// PUSH NEEDS THE VERDICT AND MUST NOT LEAVE A FILE. `palbase build` writes
// palbase-env.d.ts into the checkout on purpose — that file is what types the
// author's editor. A push is not an authoring action; writing during one would
// mean a refused push had already edited the tree, and this package's own rule
// says a refusal arriving after a side effect is not a refusal.
//
// WHY PUSH VALIDATES AT ALL — measured 2026-09-04. This gate lived ONLY in
// `palbase build`, a command a person may never run, so a schema whose relation
// names are ambiguous pushed cleanly, applied its DDL, and then refused at BOOT
// inside setSchema. The pod died on every start and took palsvc — the process
// that accepts the fixing push — with it. Live precedent 2026-08-30: a push
// carrying two unnamed FKs to one table reported `created table uat_two_fks`
// and succeeded.
func validateSchemaDeclaration(ctx context.Context, projectDir, nodeModules string) error {
	tmp, err := os.MkdirTemp("", "palbase-envcheck-*")
	if err != nil {
		return err
	}
	defer removeTemp(tmp)
	return runEnvGen(ctx, projectDir, nodeModules, filepath.Join(tmp, envTypesFile))
}

// generateEnvTypes writes the project's palbase-env.d.ts. See runEnvGen.
func generateEnvTypes(ctx context.Context, projectDir, nodeModules string) error {
	return runEnvGen(ctx, projectDir, nodeModules, filepath.Join(projectDir, envTypesFile))
}

func runEnvGen(ctx context.Context, projectDir, nodeModules, outPath string) error {
	sources, err := ReadSchemaSources(projectDir)
	if errors.Is(err, ErrNoSchema) {
		return nil // no declaration → nothing to type, skip cleanly
	}
	if err != nil {
		return err
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
	defer removeTemp(tmpDir)

	// One entry importing every declaration → temp CJS, @palbase/* external.
	entryPath := filepath.Join(tmpDir, "schemas.entry.mjs")
	if err := os.WriteFile(entryPath, []byte(envGenEntry(projectDir, sources)), 0o644); err != nil {
		return err
	}
	bundlePath := filepath.Join(tmpDir, "schema.js")
	if err := bundleSchemaTS(ctx, projectDir, nodeModules, entryPath, bundlePath); err != nil {
		return fmt.Errorf("bundle %s/: %w", SchemaDir, err)
	}

	// Extract the embedded env-gen.js bridge next to the bundle.
	scriptPath := filepath.Join(tmpDir, "env-gen.js")
	body, err := buildCheckFS.ReadFile("devjs/env-gen.js")
	if err != nil {
		return fmt.Errorf("read embedded env-gen.js: %w", err)
	}
	if err := os.WriteFile(scriptPath, body, 0o644); err != nil {
		return err
	}

	if err := runEnvGenBridge(ctx, projectDir, nodeModules, scriptPath, bundlePath, outPath); err != nil {
		return err
	}
	return nil
}

// bundleSchemaTS runs `npx esbuild` over the generated entry, emitting a CJS
// bundle at outPath with @palbase/* kept external. Runs from projectDir so
// node_modules resolution anchors to the project; the schema files are named by
// absolute path, so a sibling import inside one of them still resolves against
// db/ rather than against the temp directory the entry lives in.
func bundleSchemaTS(ctx context.Context, projectDir, nodeModules, entryPath, outPath string) error {
	bundleCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	args := []string{
		"--yes", "esbuild",
		entryPath,
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
	// Deliberately NOT nodeModules: esbuild honours NODE_PATH, so handing it the
	// real tree would resolve a bare third-party import here that the deploy —
	// which bundles a node_modules-free tarball — cannot resolve. Under
	// `palbase build` this path does not exist, and that is what makes the two agree.
	cmd.Env = append(os.Environ(), "NODE_PATH="+filepath.Join(projectDir, "node_modules"))
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("esbuild: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// envGenEntry writes the module esbuild starts from: every schema file as a
// NAMESPACE import, plus the names in the same order.
//
// Namespaces rather than defaults because a schema file may export its
// declaration three ways — `export default`, a named `schema`, or the module
// itself — and all three exist in real projects. Choosing here would make this
// the fourth place that rule is written; the bridge applies it once, to each
// namespace it is handed.
//
// The order is ReadSchemaSources' order, which is sorted by file name. Nothing
// downstream re-sorts, so an unsorted entry would reorder the generated file on
// every run and turn `palbase-env.d.ts` into a permanent diff.
func envGenEntry(projectDir string, sources []SchemaSource) string {
	var b strings.Builder
	names := make([]string, 0, len(sources))
	for i, src := range sources {
		fmt.Fprintf(&b, "import * as __s%d from %q;\n", i,
			filepath.Join(projectDir, SchemaDir, src.Name+".ts"))
		names = append(names, src.Name)
	}
	b.WriteString("export const modules = [")
	for i := range sources {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "__s%d", i)
	}
	b.WriteString("];\n")
	blob, _ := json.Marshal(names)
	fmt.Fprintf(&b, "export const names = %s;\n", blob)
	return b.String()
}

// runEnvGenBridge runs the env-gen.js bridge over the bundled schema, writing
// palbase-env.d.ts to outPath. NODE_PATH points at the project's node_modules so
// the bridge's `require('@palbase/backend')` (for makeEnvDts) resolves to the
// project's installed SDK.
func runEnvGenBridge(ctx context.Context, projectDir, nodeModules, scriptPath, bundlePath, outPath string) error {
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
	cmd.Env = append(os.Environ(), "NODE_PATH="+nodeModules)
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
