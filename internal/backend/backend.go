// Package backend provides the top-level backend lifecycle commands
// (build / push / pull / clone / deploys / rollback / status / spec / types /
// the platform link commands). palbase IS the backend CLI — there is no
// `backend` parent command.
//
// EVERY one of them acts on ONE environment: the one this checkout is linked to
// (`palbase link <project>`, or `palbase link <ref>` for a single environment of
// it), and otherwise the selection the global --project / --environment narrow.
// The link WINS, and the flags are refused rather than dropped when it answers.
// (`palbase env use` is what this said; it was retired at the v2 cutover.)
//
// There is no `--branch`: the Palbase branch is gone as a resource, and a Git
// branch is never a runtime selector — it only maps to an Environment for
// auto-deploy.
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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// defaultHTTPClient is reused by the platform `spec`/`link` commands' spec + OAuth-
// provider fetches, the SDK-major check `build` runs against npm, and any
// future direct HTTP we add. There is no `palbase web gen` — @palbase/web's
// own `palbe-gen` owns client codegen (see web_link.go). 30s read timeout
// matches the SDK side so a slow Kong response surfaces consistently.
var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

func newJSONRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

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
//go:embed devjs/build-check.js devjs/env-gen.js devjs/stack-gen.js devjs/return_types.js devjs/throw_analysis.js devjs/tx_analysis.js devjs/extract_meta.js
var buildCheckFS embed.FS

// REST is the subset of the Management-API transport the provider-aware deploy
// verbs (push/pull/clone) and the v2 reads use: PostMultipart for the
// palbase-provider tarball deploy (with its Idempotency-Key) and Do for
// everything else. *transport.Client satisfies it; tests substitute a stub.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
	PostMultipart(ctx context.Context, path string, tarball []byte, fields map[string]string, idempotencyKey string) ([]byte, error)
}

// Resolvers returns lazy accessors for the shared CLI globals, so the
// `backend` command tree can be wired into cobra at startup without the auth
// client having been initialised yet (cobra's PersistentPreRunE on the root
// command is what populates it).
//
// A Studio client used to live here too. Every verb that held one has a REST
// route now — the project's own management surface for what a project knows,
// the plane's for what the plane knows — so there is one protocol and one door
// per question instead of two of each.
type Resolvers struct {
	Auth      func() *auth.Client
	Endpoints func() config.Endpoints
	// REST returns the authed Management-API client used by push/pull/clone.
	// Lazy (a func) like the other accessors, and only CALLED at RunE time —
	// constructing the command tree with a zero-value Resolvers must not panic.
	REST func() REST
	// Selection resolves (--project, --environment, .palbase/config.json) into
	// the Project + Environment every command below acts on.
	Selection func() *selection.Resolver
}

// resolve is the one-liner every RunE opens with. It exists so a nil Selection
// accessor (the structural registration tests build the tree with a zero-value
// Resolvers) fails with a clear error instead of a nil dereference.
func (r Resolvers) resolve(ctx context.Context) (selection.Selection, error) {
	if r.Selection == nil || r.Selection() == nil {
		// `--environment <ref>` is not the second way in: it narrows a project
		// the resolver has already found, and here there is none.
		return selection.Selection{}, errors.New(
			"no project selected — run `palbase link <project>`, or `palbase link <ref>` for one environment of it")
	}
	return r.Selection().Resolve(ctx)
}

// Commands returns the flat, top-level command set the root mounts
// directly — there is no `backend` parent anymore (palbase IS the
// backend CLI). Subcommands call the resolvers at action time, after
// PersistentPreRunE has finished.
//
// There is no `init`/`enable`/`disable`: the Project is only the SaaS control-
// plane container and link anchor. Backend runtimes live on its Environments;
// every runtime command below resolves one concrete Environment first.
//
// push/pull/clone are mode-aware deploy verbs: for a github-mode project they
// shell out to git (push/pull/clone → webhook → orchestrator deploys); for a
// platform-mode project they upload/fetch a tarball bundle via the Management
// API. `merge` stays retired (the old go-git merge verb is gone). Alongside
// them the CLI keeps the pre-deploy validator (`build`) and the
// observation/control verbs (deploys, rollback, status).
//
// `spec` is ONE command: fetching the contract is one act, and where it lands
// is a fact about the checkout (read from the committed platform slots), not a
// question for the caller. It writes each linked platform's directory and runs
// the one generator the CLI owns — the SDK's swiftgen, for Apple, which is the
// only platform with no build-time generator of its own. Web's palbe-gen and
// Android's Gradle plugin run from their own build steps.
func Commands(r Resolvers) []*cobra.Command {
	return []*cobra.Command{
		newWebCmd(r),
		newIOSCmd(r),
		newMacOSCmd(r),
		newAndroidCmd(r),
		newBuildCmd(r),
		newDeploysCmd(r),
		newRollbackCmd(r),
		newStatusCmd(r),
		newSpecCmd(r),
		newCloneCmd(r),
		newPullCmd(r),
		newPushCmd(r),
		// Takes the resolvers for ONE reason: a bare Environment ref has to become
		// an address, and only the configured cloud knows the suffix. Everything
		// after that is unchanged — it is told where the stack is, once, and asks
		// the stack itself for the rest.
		newLinkCmd(r),
		newPlanCmd(),
		newInitCmd(),
		newStartCmd(),
		newStopCmd(),
	}
}

// backendTarget is the resolved (URL + publishable key) for one ENVIRONMENT's
// backend. Used by lookupBackendTarget (which the platform `spec` and link
// commands' artifact fetch share) to address the deployed tenant host.
type backendTarget struct {
	URL    string
	APIKey string
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

// deployRow mirrors one control-pg `deployments` attempt as the deployments v2
// route returns it. This is the canonical attempt log — a FAILED attempt (or a
// pod that never went Ready) has no git commit but does have a row here, so it
// never hides a failure behind "(no versions)". succeeded + a non-empty Error =
// "deployed with warnings".
type deployRow struct {
	Status        string  `json:"status"`
	Version       *string `json:"version"`
	Trigger       string  `json:"trigger"`
	Error         *string `json:"error"`
	CommitMessage *string `json:"commitMessage"`
	CreatedAt     string  `json:"createdAt"`
}

func newDeploysCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "deploys",
		Args:  cobra.NoArgs,
		Short: "Show the selected environment's deploy history (newest first)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TARGET-RELATIVE, like push and status.
			if target, err := ReadTarget(); err == nil {
				if err := refuseCloudSelectionFlags(cmd, target); err != nil {
					return err
				}
			}
			if handled, err := deploysOfProject(cmd); handled {
				return err
			}

			sel, err := r.resolve(cmd.Context())
			if err != nil {
				return unlinkedOrCloudError(err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", sel.Describe())
			var resp struct {
				Deployments []deployRow `json:"deployments"`
			}
			if err := r.REST().Do(cmd.Context(), http.MethodGet,
				DeploymentsPath(sel.ProjectID, sel.EnvironmentRef())+"?limit=20", nil, &resp); err != nil {
				return fmt.Errorf("list deployments: %w", err)
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return json.NewEncoder(out).Encode(resp.Deployments)
			}
			if len(resp.Deployments) == 0 {
				fmt.Fprintln(out, "(no deploy attempts yet — deploy with `palbase push`)")
				return nil
			}
			fmt.Fprintf(out, "%-11s %-8s %-20s %-12s %s\n",
				"STATUS", "VERSION", "WHEN", "TRIGGER", "NOTE")
			for _, d := range resp.Deployments {
				fmt.Fprintf(out, "%-11s %-8s %-20s %-12s %s\n",
					deployStatusLabel(d),
					deployVersion(d.Version),
					deployWhen(d.CreatedAt),
					d.Trigger,
					deployNote(d),
				)
			}
			return nil
		},
	}
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
	// SHORTENED, because on this plane a version is an artifact DIGEST — 64 hex
	// characters, which overruns the column and pushes every field after it off
	// the line. Twelve is what every other surface prints and what `git log`
	// taught everyone to read.
	return short(*v)
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
	cmd := &cobra.Command{
		Use:   "rollback <version-sha>",
		Args:  cobra.ExactArgs(1),
		Short: "Roll back the selected environment to a previous version",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TARGET-RELATIVE, like push and status.
			if target, err := ReadTarget(); err == nil {
				if err := refuseCloudSelectionFlags(cmd, target); err != nil {
					return err
				}
			}
			_, err := rollbackOnProject(cmd, args[0])
			return err
		},
	}
	return cmd
}

// lastDeploy mirrors backend.status's `lastDeploy` field: the newest deploy row
// for the environment (from control-pg.deployments). Nil when it has never been
// deployed. This is the visibility surface — a server-side deploy failure that
// only lived in logs shows up in `palbase status`.
type lastDeploy struct {
	Status    string  `json:"status"`
	Error     *string `json:"error"`
	Version   *string `json:"version"`
	UpdatedAt *string `json:"updatedAt"`
}

func newStatusCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Args:  cobra.NoArgs,
		Short: "Show the selected environment's active version + deploy state",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TARGET-RELATIVE, like push and spec: a checkout linked to a
			// project reports on THAT project. One verb either way.
			if target, err := ReadTarget(); err == nil {
				if err := refuseCloudSelectionFlags(cmd, target); err != nil {
					return err
				}
			}
			_, err := statusOfProject(cmd, r, jsonOut)
			return err
		},
	}
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
	fmt.Fprintf(&b, "last deploy: %s%s\n", strings.ToUpper(label), deployMeta(d, now))
	if d.Error != nil && *d.Error != "" {
		fmt.Fprintf(&b, "  error: %s\n", *d.Error)
	}
	return b.String()
}

// deployMeta formats the " (3m ago)" suffix. The age is dropped when the
// timestamp is missing or unparseable rather than printing a bogus duration.
// There is no branch to name — the environment IS the deploy target.
func deployMeta(d *lastDeploy, now time.Time) string {
	if d.UpdatedAt == nil {
		return ""
	}
	t, err := time.Parse(time.RFC3339, *d.UpdatedAt)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(" (%s)", humanizeAgo(now.Sub(t)))
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

// lookupBackendTarget resolves an Environment's URL and publishable key directly
// from its Environment ref.
func lookupBackendTarget(ctx context.Context, rest selection.REST, endpoints config.Endpoints, ref string) (backendTarget, error) {
	if !selection.IsCanonicalEnvironmentRef(ref) {
		return backendTarget{}, fmt.Errorf("environment ref %q is non-canonical", ref)
	}
	var resp struct {
		EnvironmentRef string `json:"environment_ref"`
		PublishableKey string `json:"publishable_key"`
	}
	// YÖNETİM API'SİNDEN, STUDIO'NUN tRPC YÜZEYİNDEN DEĞİL.
	//
	// Eskiden `apikey.reveal` Studio'ya DPoP'la bağlanmış bir TARAYICI oturumu
	// istiyordu. Headless bir koşumda öyle bir oturum yok ve komut
	// "acquire token: refresh tokens: 401 — invalid_token" ile ölüyordu — oysa
	// bu CLI'ın kendi yardım metni şunu söylüyor: "For a headless run — CI, an
	// agent in a container — there is no sign-in at all: set
	// PALBASE_ACCESS_TOKEN and every command resolves it." O söz, `link` ve
	// `spec` için tutulmuyordu.
	//
	// REST istemcisi headless token'ı zaten çözüyor, ve bu uç yalnız
	// YAYINLANABİLİR anahtarı döndürüyor — bir checkout'u bağlamak için gereken
	// tam olarak o.
	if err := rest.Do(ctx, http.MethodGet, "/api/v2/environments/"+ref+"/apikey", nil, &resp); err != nil {
		return backendTarget{}, fmt.Errorf("read environment key: %w", err)
	}
	if err := selection.ValidateRuntimeBinding(ref, resp.EnvironmentRef, resp.PublishableKey); err != nil {
		return backendTarget{}, fmt.Errorf("apikey.reveal returned an invalid Environment binding: %w", err)
	}
	return backendTarget{
		URL:    fmt.Sprintf("https://%s.%s", ref, endpoints.PublicHost),
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

// fetchOpenAPISpecOpts is the wake-aware core. It retries ONLY on signals that
// mean "the backend pod is still waking" — HTTP 502/503 and per-attempt
// timeouts — honoring a Retry-After header when present. Everything else fails
// immediately: a connection-refused returns fast rather than burning the
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

func generateEnvTypes(ctx context.Context, projectDir, nodeModules string) error {
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

	outPath := filepath.Join(projectDir, envTypesFile)
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
