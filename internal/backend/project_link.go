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
	platforms []string
	insecure  bool
}

func newLinkCmd() *cobra.Command {
	var o linkOpts
	cmd := &cobra.Command{
		Use:   "link <target>",
		Args:  cobra.MaximumNArgs(1),
		Short: "Bind this app to a stack and generate its client",
		Long: `Bind this checkout to a project, and generate the typed client for it.

    palbase link http://localhost:54321      something running on this machine
    palbase link todoapp                     a project in the cloud

Linking is something you do AS SOMEBODY. Both the publishable key and the
contract come from the project over authenticated routes — the public document
says only what kind of thing answered — so a stranger who knows the address gets
neither. The credential comes from ` + "`palbase start`" + ` (which writes one for the
stack it just brought up), from ` + "`palbase login`" + `, or from PALBASE_ACCESS_TOKEN.

It writes:

  .palbase/project.json                     the project this checkout belongs to
  .palbase/<platform>/palbase-config.json   the app's URL + publishable key
  .palbase/openapi.json                     the contract
  Palbase/Generated/                        (apple) the committed Swift client

Run it again after every ` + "`palbase push`" + ` — or just ` + "`palbase spec`" + `, which
refreshes the contract alone, because that is the part that changed.

--insecure is for an address still using the self-signed certificate its first
boot generated.`,
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
	f.StringSliceVar(&o.platforms, "platform", []string{"ios"}, "ios, macos, android or web")
	f.BoolVar(&o.insecure, "insecure", false, "accept the stack's self-signed certificate")
	return cmd
}

func runLink(ctx context.Context, o linkOpts, w io.Writer) error {
	base := strings.TrimRight(strings.TrimSpace(o.url), "/")
	if base == "" {
		return errors.New("--url is required: the address the stack serves on")
	}
	// What is at this address, and does it answer at all. One request, no
	// credential: the document says WHAT answered and which SDK it runs, and a
	// project that does not answer is either not a Palbase project or not up.
	described, err := describeStack(ctx, base, o.insecure)
	if err != nil {
		return err
	}

	// The publishable key comes from the project, over an authenticated route.
	// It used to ride on the public document, which meant anyone who knew the
	// address was handed a working client credential; now linking is something
	// you do as somebody.
	target := Target{URL: base, Insecure: o.insecure}
	anon, err := projectPublishableKey(ctx, target)
	if err != nil {
		return err
	}

	// Remember the target. `login`, `push` and `spec` read it, so none of them
	// asks for an address again — and a colleague who clones this repository
	// reaches the same stack without being told which one it is.
	if err := WriteTarget(target); err != nil {
		return err
	}

	// EVERY environment, not the one being linked. An app that holds only the
	// environment somebody linked last is an app whose address depends on when
	// it was built — which is how a TestFlight build ends up pointed at staging.
	envs, specs, err := gatherEnvironments(ctx, target, anon, w)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", nativeArtifactsDir, err)
	}
	for _, platform := range o.platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		path, err := writeAppEnvironments(platform, envs)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %s (%s)\n", path, strings.Join(envs.names(), ", "))
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := writeXcconfigs(root, envs, w); err != nil {
		return err
	}

	fmt.Fprintf(w, "\nlinked to %s (%s)\n", base, described.Hosting)

	// The contract, once per environment: they can differ, and a client merged
	// across them would compile calls that do not exist where the app points.
	if err := generateForEnvironments(ctx, envs, w); err != nil {
		return err
	}
	reportContractDrift(specs, w)

	fmt.Fprintln(w, "commit .palbase/ and Palbase/Generated/")
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
	if described.Hosting == "" {
		return stackDescription{}, fmt.Errorf(
			"%s answered %s without saying what it is", base, wellKnownPath)
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
