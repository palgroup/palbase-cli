package backend

// selfhost_link.go — `palbase selfhost link` : binding an app to a stack you run.
//
// WHY THIS IS A SEPARATE COMMAND. `palbase ios link` resolves a PROJECT,
// registers an APP under it, picks an ENVIRONMENT, and asks the Management API
// for that environment's key. A self-hosted stack has none of those: it is one
// installation with one identity and one pair of keys, sitting in the `.env`
// beside it. Bending the cloud path onto it would mean inventing a project id
// for a thing that is not one — so this command asks for the only two facts that
// exist (where the stack is, and its keys) and writes exactly the same artifacts
// every generator downstream already reads.
//
// It writes the SAME slot files `link` writes, so everything after this point —
// `palbase spec`, the Swift generator, `palbe-gen` — behaves identically whether
// the stack is yours or ours. That is the point: one toolchain, two hosts.
import (
	"bufio"
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

// specPath is where a self-hosted stack answers with what it is serving.
// service_role only, because the document is the whole API surface.
const specPath = "/admin/openapi.json"

// selfhostRef is the identity every self-hosted stack has. It is a constant in
// the stack too (`migrate.BootEnvironmentRef`), which is why nothing here asks
// for it: a value the operator could get wrong is a value that should not be
// asked for.
const selfhostRef = "selfhost"

type selfhostOpts struct {
	url        string
	envFile    string
	anonKey    string
	serviceKey string
	platforms  []string
	insecure   bool
}

func newSelfhostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "selfhost",
		Short: "Work with a Palbase stack you run yourself",
	}
	cmd.AddCommand(newSelfhostLinkCmd())
	return cmd
}

func newSelfhostLinkCmd() *cobra.Command {
	var o selfhostOpts
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Point this app at a self-hosted stack and generate its client",
		Long: `Bind this checkout to a Palbase stack you run, and generate the typed client
for it.

It fetches the stack's OpenAPI document — what it is ACTUALLY serving, built by
the runtime from the controllers its router took — and writes:

  .palbase/openapi.json                     the contract
  .palbase/<platform>/palbase-config.json   the app's URL + publishable key
  Palbase/Generated/                        (apple) the committed Swift client

Two keys, for two different readers. The SECRET key fetches the document: it
lists every route and error shape, which is what an integrator needs and what a
phone must never hold. The PUBLISHABLE key is what gets written into the app.

Point --env-file at the stack's .env and both are read from it, so neither is
ever pasted into a shell history.

  palbase selfhost link --url https://127.0.0.1 --env-file ../palbase/.env --insecure

--insecure is for a stack still using the self-signed certificate its first boot
generated. Drop it the moment you put a real one in front.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSelfhostLink(cmd.Context(), o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.url, "url", "", "the stack's base URL (e.g. https://127.0.0.1)")
	f.StringVar(&o.envFile, "env-file", "", "read the keys from the stack's .env")
	f.StringVar(&o.anonKey, "anon-key", "", "publishable key (what the app ships); overrides --env-file")
	f.StringVar(&o.serviceKey, "service-key", "", "secret key (fetches the spec); overrides --env-file")
	f.StringSliceVar(&o.platforms, "platform", []string{"ios"}, "ios, macos, android or web")
	f.BoolVar(&o.insecure, "insecure", false, "accept the stack's self-signed certificate")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

func runSelfhostLink(ctx context.Context, o selfhostOpts, w io.Writer) error {
	base := strings.TrimRight(strings.TrimSpace(o.url), "/")
	if base == "" {
		return errors.New("--url is required: the address the stack serves on")
	}
	anon, service, err := resolveSelfhostKeys(o)
	if err != nil {
		return err
	}

	spec, err := fetchSelfhostSpec(ctx, base, service, o.insecure)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", nativeArtifactsDir, err)
	}
	specFile := filepath.Join(nativeArtifactsDir, "openapi.json")
	if err := os.WriteFile(specFile, spec, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", specFile, err)
	}
	fmt.Fprintf(w, "wrote %s (%d bytes)\n", specFile, len(spec))

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
			if err := os.WriteFile(filepath.Join(dir, "openapi.json"), spec, 0o644); err != nil {
				return err
			}
		} else if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}

		entry := pullSpecConfigEntry{
			// The app id a cloud project would mint has no counterpart here, and
			// inventing one would put a value in a committed file that means
			// nothing and can never be looked up. The stack's own identity is the
			// honest answer for both.
			AppID:          selfhostRef,
			EnvironmentRef: selfhostRef,
			Kind:           platform,
			BaseURL:        base,
			APIKey:         anon,
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

	// The same generator `palbase spec` runs, from the same committed slots: a
	// self-hosted app must not end up with a client built by a second code path.
	if err := generateAppleClient(nativeArtifactsDir, w); err != nil {
		return err
	}

	fmt.Fprintf(w, "\nlinked to %s — commit .palbase/ and Palbase/Generated/\n", base)
	return nil
}

// resolveSelfhostKeys reads the two keys from the stack's .env unless they were
// given explicitly.
func resolveSelfhostKeys(o selfhostOpts) (anon, service string, err error) {
	anon, service = strings.TrimSpace(o.anonKey), strings.TrimSpace(o.serviceKey)
	if anon != "" && service != "" {
		return anon, service, nil
	}
	if o.envFile == "" {
		return "", "", errors.New(
			"pass --env-file pointing at the stack's .env, or both --anon-key and --service-key")
	}
	values, err := readEnvFile(o.envFile)
	if err != nil {
		return "", "", err
	}
	if anon == "" {
		anon = values["PALBASE_ANON_KEY"]
	}
	if service == "" {
		service = values["PALBASE_SERVICE_ROLE_KEY"]
	}
	if anon == "" || service == "" {
		return "", "", fmt.Errorf(
			"%s carries no PALBASE_ANON_KEY / PALBASE_SERVICE_ROLE_KEY — is it the stack's .env?", o.envFile)
	}
	return anon, service, nil
}

// readEnvFile reads KEY=value lines. Deliberately small: it is reading a file
// this same product generated, not implementing a shell.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := map[string]string{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

func fetchSelfhostSpec(ctx context.Context, base, serviceKey string, insecure bool) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	if insecure {
		client.Transport = insecureTransport()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+specPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", serviceKey)

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach %s: %w\n(a self-signed certificate needs --insecure)", base+specPath, err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf(
			"the stack refused the key (%d). %s answers for a SERVICE ROLE key — "+
				"the publishable one is refused here on purpose", res.StatusCode, specPath)
	case http.StatusServiceUnavailable:
		return nil, fmt.Errorf("the stack has nothing to describe yet: %s\n"+
			"push a backend first (palsvc --schema-apply, then --push)", trimBody(body))
	default:
		return nil, fmt.Errorf("%s answered %d: %s", specPath, res.StatusCode, trimBody(body))
	}
	if len(body) == 0 {
		return nil, errors.New("the stack answered 200 with an empty document")
	}
	return body, nil
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
