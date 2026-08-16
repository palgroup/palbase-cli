package backend

// project_link.go — `palbase link <target>` : binding an app to a project.
//
// TARGET-RELATIVE, which is the rule the design settled on
// (docs/paltimate/2026-08-12-v2-faz0-selfhost/design-management-api.md §6, §10):
// a command that touches ONE tenant works against whatever target it is given,
// and only who authenticates changes. What you write decides which it is —
// something carrying a scheme is the target itself and no control plane is
// asked; a bare environment ref is resolved by ours.
//
// This is the direct half. `palbase ios link` still owns the ref half, because
// that one resolves a project in the cloud, registers an app and asks for an
// environment's key — none of which a project somebody runs has: it is one
// installation with one identity and one pair of keys in the .env beside it.
// The two meet when the management API exists on both sides; until then, the
// direct half is what makes a project you run usable at all.
//
// It writes the SAME slot files `link` writes, so everything after this point —
// `palbase spec`, the Swift generator, `palbe-gen` — behaves identically whether
// the stack is yours or ours. That is the point: one toolchain, two hosts.
import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// projectAppID names the app slot a linked checkout writes.
//
// A cloud project mints an app id when an app registers; a project somebody runs
// has no registry to mint one from, and inventing a value would put something in
// a committed file that means nothing and can never be looked up. The project's
// own name is the honest answer — and it matches the identity the stack boots
// with (`migrate.BootStackRef`), which is also what its API key carries.
const projectAppID = "project"

type linkOpts struct {
	url       string
	email     string
	password  string
	platforms []string
	insecure  bool
}

func newLinkCmd() *cobra.Command {
	var o linkOpts
	cmd := &cobra.Command{
		Use:   "link <target>",
		Args:  cobra.MaximumNArgs(1),
		Short: "Bind this app to a stack and generate its client",
		Long: `Bind this checkout to a Palbase stack you run, and generate the typed client
for it.

    palbase link https://my-stack --email you@example.com

The stack says what it is — its publishable key comes from
/.well-known/palbase.json, which it serves itself — so nothing is pasted into a
shell and no file has to be found on disk.

The CONTRACT is the one part that needs a person. It lists every route, body and
error shape of the deployed backend, and this stack keeps it behind its
management surface rather than serving it to anyone who knows the address — so
--email signs you in while linking and the client is generated on the spot. Drop
it if you are already signed in, or if you only want the app's config now.

It writes:

  .palbase/target.json                      the stack this checkout talks to
  .palbase/<platform>/palbase-config.json   the app's URL + publishable key
  .palbase/openapi.json                     the contract, once you are signed in
  Palbase/Generated/                        (apple) the committed Swift client

The contract is the one part that needs a person: it lists every route and error
shape, so it comes from the management surface, with your own session. Run
` + "`palbase login`" + ` and it is fetched and generated on the spot — and again after
every ` + "`palbase push`" + `, because that is exactly when it changed.

--insecure is for a stack still using the self-signed certificate its first boot
generated. Drop it the moment you put a real one in front.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The target may be written as an argument or as --url; they are the
			// same thing, and refusing one of them would be a rule to remember
			// for no reason.
			if len(args) == 1 && o.url == "" {
				o.url = args[0]
			}
			if o.url != "" && !strings.Contains(o.url, "://") {
				return fmt.Errorf(
					"%q has no scheme, so it is an environment ref rather than a stack address — "+
						"that half is resolved by our cloud: use `palbase ios link` for it", o.url)
			}
			return runLink(cmd.Context(), o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.url, "url", "", "the stack's base URL (e.g. https://127.0.0.1)")
	f.StringVar(&o.email, "email", "", "sign in as this person while linking, to fetch the contract")
	f.StringVar(&o.password, "password", "", "skip the prompt (a password on a command line lands in shell history)")
	f.StringSliceVar(&o.platforms, "platform", []string{"ios"}, "ios, macos, android or web")
	f.BoolVar(&o.insecure, "insecure", false, "accept the stack's self-signed certificate")
	return cmd
}

func runLink(ctx context.Context, o linkOpts, w io.Writer) error {
	base := strings.TrimRight(strings.TrimSpace(o.url), "/")
	if base == "" {
		return errors.New("--url is required: the address the stack serves on")
	}
	// The stack says what it is. Nothing to paste, nothing to find on disk.
	described, err := describeStack(ctx, base, o.insecure)
	if err != nil {
		return err
	}
	anon := described.AnonKey

	// Remember the target. `login`, `push` and `spec` read it, so none of them
	// asks for an address again — and a colleague who clones this repository
	// reaches the same stack without being told which one it is.
	if err := WriteTarget(Target{URL: base, AnonKey: anon, Insecure: o.insecure}); err != nil {
		return err
	}

	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", nativeArtifactsDir, err)
	}

	for _, platform := range o.platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		dir := filepath.Join(nativeArtifactsDir, platform)
		if platform == "web" {
			// The web SDK reads its slot from Palbase/, not .palbase/ — a
			// difference the link commands already carry, mirrored here rather
			// than corrected, because the generators on the other side are what
			// define it.
			dir = webArtifactsDir
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", dir, err)
			}
		} else if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}

		entry := pullSpecConfigEntry{
			AppID:   projectAppID,
			Kind:    platform,
			BaseURL: base,
			// The key carries the project's identity, so nothing here writes a
			// second copy of it — see pullSpecConfigEntry.
			APIKey: anon,
		}
		blob, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return err
		}
		cfg := filepath.Join(dir, "palbase-config.json")
		if err := os.WriteFile(cfg, append(blob, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", cfg, err)
		}
		fmt.Fprintf(w, "wrote %s\n", cfg)
	}

	fmt.Fprintf(w, "\nlinked to %s (%s)\n", base, described.Hosting)

	// The contract, and a sign-in if that is what it takes.
	//
	// Asked for rather than assumed: the session is tried FIRST and the sign-in
	// happens only if it is missing or no longer accepted. A stack rebuilt since
	// the last link leaves a token behind that verifies as nothing, and
	// "already have a token" would be exactly the wrong reason to skip signing
	// in — it is the case that needs it most.
	err = RefreshSpec(ctx, w)
	if errors.Is(err, ErrNotSignedIn) && o.email != "" {
		if err := runStackLogin(ctx, Target{URL: base, AnonKey: anon, Insecure: o.insecure},
			o.email, o.password, w); err != nil {
			return err
		}
		err = RefreshSpec(ctx, w)
	}

	switch {
	case err == nil:
		fmt.Fprintln(w, "commit .palbase/ and Palbase/Generated/")
	case errors.Is(err, ErrNotSignedIn):
		fmt.Fprint(w, "the app's config is written; the CONTRACT needs a person — this stack keeps it\n"+
			"behind its management surface. Re-run with --email, or `palbase login`.\n")
	default:
		return err
	}
	return nil
}

// describeStack asks a stack what it is.
//
// One request, no credential: the document is public because everything in it
// is (see the stack's internal/server/wellknown.go). A stack that does not
// answer is either not a Palbase stack or not up, and saying which is the whole
// value of asking first.
func describeStack(ctx context.Context, base string, insecure bool) (stackDescription, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	if insecure {
		client.Transport = insecureTransport()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+wellKnownPath, nil)
	if err != nil {
		return stackDescription{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return stackDescription{}, fmt.Errorf(
			"reach %s: %w\n(a self-signed certificate needs --insecure)", base, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return stackDescription{}, err
	}
	if res.StatusCode != http.StatusOK {
		return stackDescription{}, fmt.Errorf(
			"%s does not look like a Palbase stack: %s answered %d", base, wellKnownPath, res.StatusCode)
	}
	var described stackDescription
	if err := json.Unmarshal(body, &described); err != nil {
		return stackDescription{}, fmt.Errorf("%s answered something unexpected: %s", wellKnownPath, trimBody(body))
	}
	if described.AnonKey == "" {
		return stackDescription{}, fmt.Errorf(
			"%s has no publishable key configured — its clients cannot authenticate at all", base)
	}
	return described, nil
}

// wellKnownPath is where a stack describes itself.
const wellKnownPath = "/.well-known/palbase.json"

// stackDescription is that document. It mirrors the stack's own struct; the two
// are small and the contract between them is three fields, so this is written
// out rather than generated.
type stackDescription struct {
	Hosting    string `json:"hosting"`
	AnonKey    string `json:"anon_key"`
	SDKVersion string `json:"sdk_version"`
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// insecureTransport accepts a certificate nobody issued.
//
// It exists for exactly one case: a stack still using the self-signed pair its
// first boot generated, being linked from the machine that runs it. It is opt-in
// per invocation (`--insecure`), never a default and never inferred from the
// host — a tool that silently stopped verifying certificates for "local-looking"
// addresses would be a tool that stops verifying them.
func insecureTransport() http.RoundTripper {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in, documented above
}
