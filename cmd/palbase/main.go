package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/apikey"
	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/authadmin"
	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/config"
	dbcmd "github.com/palgroup/palbase-cli/internal/db"
	"github.com/palgroup/palbase-cli/internal/debugconsole"
	"github.com/palgroup/palbase-cli/internal/egress"
	palbaseenv "github.com/palgroup/palbase-cli/internal/env"
	"github.com/palgroup/palbase-cli/internal/flags"
	"github.com/palgroup/palbase-cli/internal/logs"
	"github.com/palgroup/palbase-cli/internal/members"
	"github.com/palgroup/palbase-cli/internal/notifications"
	"github.com/palgroup/palbase-cli/internal/project"
	"github.com/palgroup/palbase-cli/internal/roles"
	"github.com/palgroup/palbase-cli/internal/secret"
	"github.com/palgroup/palbase-cli/internal/storage"
	palbasetest "github.com/palgroup/palbase-cli/internal/test"
	"github.com/palgroup/palbase-cli/internal/testuser"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/palgroup/palbase-cli/internal/versions"
	"github.com/spf13/cobra"
)

var Version = "dev"

// resolved is populated in PersistentPreRunE and consumed by subcommands.
var resolved config.Resolved

// selectedEnv carries the root command's --env value into the backend package.
// A cobra flag cannot be read from a function that takes no command, and the
// resolver is called from packages that never see the root.
var selectedEnv string

// authClient is built per invocation from the resolved mode/endpoints.
var authClient *auth.Client

// managementREST lazily resolves the account credential and builds a cloud
// client. Browser sessions use Bearer auth and refresh in place; machine
// identities use DPoP with the private JWK supplied in PALBASE_DPOP_KEY.
// Missing credentials surface when a request is made, so login and config
// commands can run without an account credential.
//
// The Resolvers callbacks below are `func() REST` — they don't carry the cobra
// command context. Wiring that through every command package would be a wide
// refactor for a single side-effect (the refresh-token HTTP call); instead the
// refresh is capped with a short timeout here.
func managementREST() *transport.Client {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// ManagementToken refreshes an expired session in place; on failure the
	// credential stays empty and transport emits the actionable
	// "run palbase login" error rather than a bare 401.
	token, _ := authClient.ManagementToken(ctx)
	return transport.New(resolved.Endpoints.PlatformAPI, token)
}

// wireDPoPSigner connects auth to transport without a package dependency.
// A machine token must be presented with its paired PALBASE_DPOP_KEY.
func wireDPoPSigner() {
	transport.DPoPSigner = func(method, url, accessToken string) (string, error) {
		key, err := auth.LoadDPoPKey()
		if err != nil {
			return "", err
		}
		return key.NewProof(auth.ProofOptions{
			HTTPMethod:  method,
			URL:         url,
			AccessToken: accessToken,
		})
	}
}

// wireCloudKeyFetcher lets the backend fetch a cloud project's service-role
// key using the caller's account credential.
func wireCloudKeyFetcher() {
	backend.CloudRuntimePreparer = func(ctx context.Context, tenantURL, sdkVersion string, plan backend.PlanRef) error {
		ref, ok := tenantRefOf(tenantURL, resolved.Endpoints.PublicHost)
		if !ok {
			return fmt.Errorf("%s is not a project on this cloud", tenantURL)
		}
		var result struct {
			SDKVersion string `json:"sdkVersion"`
		}
		cloud := managementREST()
		cloud.HTTPClient.Timeout = 5 * time.Minute
		// PLAN GÖVDEDE GİDER (C-6, FR-040). Sunucu ona GÜVENMEZ: parmak izini
		// kendisi yeniden hesaplar ve `running`i canlı runtime'a sorar. Plan bir
		// İDDİA, kapı bir ÖLÇÜMDÜR.
		if err := cloud.Do(ctx, http.MethodPost,
			"/v1/cloud/projects/"+url.PathEscape(ref)+"/runtime",
			map[string]any{"sdkVersion": sdkVersion, "plan": plan}, &result); err != nil {
			return err
		}
		if result.SDKVersion != sdkVersion {
			return fmt.Errorf("platform did not verify SDK %s", sdkVersion)
		}
		return nil
	}
	// PLANIN RUNTIME BÖLÜMÜ (FR-045): yükseltmenin NE YAPACAĞINI platformdan
	// sorar ve hiçbir şeye dokunmaz.
	//
	// `ErrNotACloudProject` BİR CEVAPTIR, hata değil: kendi kendine barındırılan
	// bir yığında ya da `palbase start` ile koşan yerel bir kopyada soracak bir
	// kontrol düzlemi yoktur, ve plan o zaman eski satırını basar. Bunu genel bir
	// hata yapmak, bulutu olmayan her kullanıcının planını düşürürdü.
	backend.CloudRuntimePlanner = func(ctx context.Context, tenantURL, sdkVersion string) (json.RawMessage, error) {
		ref, ok := tenantRefOf(tenantURL, resolved.Endpoints.PublicHost)
		if !ok {
			return nil, backend.ErrNotACloudProject
		}
		var raw json.RawMessage
		cloud := managementREST()
		// Ön kontroller SERVİS EDEN pod'un içinde koşuyor ve büyük bir tabloda
		// saniyeler sürebilir; varsayılan istemci bütçesi bunu kesecek kadar dar.
		cloud.HTTPClient.Timeout = 3 * time.Minute
		if err := cloud.Do(ctx, http.MethodGet,
			"/v1/cloud/projects/"+url.PathEscape(ref)+"/runtime/plan?sdk="+url.QueryEscape(sdkVersion),
			nil, &raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	backend.CloudKeyFetcher = func(tenantURL string) (string, error) {
		ref, ok := tenantRefOf(tenantURL, resolved.Endpoints.PublicHost)
		if !ok {
			// Not an address on this cloud (a stack on this machine, or another
			// deployment). Asking our control plane about it would be asking the
			// wrong authority.
			return "", fmt.Errorf("%s is not a project on this cloud", tenantURL)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var keys struct {
			ServiceRoleKey string `json:"serviceRoleKey"`
		}
		if err := managementREST().Do(ctx, http.MethodGet,
			"/v1/cloud/projects/"+url.PathEscape(ref)+"/keys", nil, &keys); err != nil {
			return "", err
		}
		return keys.ServiceRoleKey, nil
	}
}

// tenantRefOf extracts the project ref from a tenant address on this cloud.
//
// `https://j06bwtuum.palbase.studio` → `j06bwtuum`. Anything not under this
// cloud's tenant suffix returns false, so a local stack or another deployment's
// address is never sent to our control plane.
func tenantRefOf(tenantURL, publicHost string) (string, bool) {
	if publicHost == "" {
		return "", false
	}
	u, err := url.Parse(tenantURL)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	suffix := "." + publicHost
	host := u.Hostname()
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	ref := strings.TrimSuffix(host, suffix)
	if ref == "" || strings.Contains(ref, ".") {
		return "", false
	}
	return ref, true
}

// linkedProject adapts the linked target for commands that need to talk to a
// project — both to the project itself and to the cloud about it.
type linkedProject struct{ target backend.Target }

// stackManagementREST reaches the management surface of the stack this directory
// is linked to — NOT the cloud control plane.
//
// The distinction is the whole point of these verbs: `palbase auth` changes the
// auth of one project's own stack, and that stack answers for itself whether it
// runs in our cloud or on somebody's own machine. Sending these through the
// platform API would make self-host a second, thinner product.
type stackManagementREST struct {
	target backend.Target
	cred   backend.Credentials
	client *http.Client
}

func (s stackManagementREST) Do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	status, raw, _, err := s.DoWithHeaders(ctx, method, path, body, nil)
	return status, raw, err
}

func (s stackManagementREST) DoWithHeaders(ctx context.Context, method, path string, body []byte, headers http.Header) (int, []byte, http.Header, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(s.target.URL, "/")+path, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	s.cred.Apply(req)
	for _, name := range []string{"Palbase-Auth-Contract", "If-Match"} {
		for _, value := range headers.Values(name) {
			req.Header.Add(name, value)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("reach %s: %w", s.target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, raw, res.Header.Clone(), err
}

// openStackEgress is the same client under the egress package's own seam. Two
// tiny interfaces rather than one shared one, because a shared one would make
// every command package import whichever package declared it.
func openStackEgress(cmd *cobra.Command) (egress.REST, error) {
	return openStackManagement(cmd)
}

// openStackManagement resolves the target, announcing it, and builds the client.
func openStackManagement(cmd *cobra.Command) (authadmin.REST, error) {
	target, err := backend.PrintTargetFor(cmd)
	if err != nil {
		return nil, err
	}
	cred, _, err := backend.Credential(target.URL)
	if err != nil {
		return nil, err
	}
	return stackManagementREST{target: target, cred: cred, client: backend.HTTPClient(target)}, nil
}

func linkedTarget() (linkedProject, error) {
	t, err := backend.ReadTarget()
	if err != nil {
		return linkedProject{}, err
	}
	return linkedProject{target: t}, nil
}

// Ref names the cloud project, or reports false for anything that is not one —
// a stack on this machine has no cloud ref, and asking the control plane about
// it would be asking the wrong authority.
func (p linkedProject) Ref() (string, bool) {
	return tenantRefOf(p.target.URL, resolved.Endpoints.PublicHost)
}

func (p linkedProject) Describe() string { return p.target.Describe() }

func (p linkedProject) GetJSON(ctx context.Context, path string, out any) error {
	return backend.GetManagementJSON(ctx, p.target, path, out)
}

// cloudFacts answers questions about the cloud itself — the tenant address
// suffix, today. It reads them from the control plane's bootstrap endpoint
// rather than a compiled-in constant, so one binary is correct against every
// deployment instead of only the one it was built for.
type cloudFacts struct{}

func (cloudFacts) TenantDomain(ctx context.Context) (string, error) {
	boot, err := auth.NewCloudClient(resolved.Endpoints.PlatformAPI).Bootstrap(ctx)
	if err != nil {
		return "", err
	}
	return boot.TenantDomain, nil
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// A command may ask for a SPECIFIC exit status rather than a failure —
		// `db plan --detailed-exitcode` reports "there are changes" as 2, which CI
		// branches on, and `palbase run` carries its child's status. Such an error
		// carries no message: printing an empty line above a meaningful status
		// code would read as a crash.
		//
		// The interface asks for TWO methods, and the second one is the point.
		// `*exec.ExitError` has `ExitCode() int` too, so a one-method interface
		// matched every failed subprocess this CLI shells out to — npm inside
		// `init`, swiftgen inside `link` — and those exited silently with the
		// child's status instead of printing what went wrong.
		var coded interface {
			ExitCode() int
			DeliberateExitStatus()
		}
		if errors.As(err, &coded) {
			os.Exit(coded.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// wireEnvironmentLookup teaches the backend package how to ask the control
// plane which environments a project has.
//
// SAME IDIOM AS THE OTHERS. `internal/backend` cannot import the transport —
// that would tie it to a leaf of the command tree and produce a cycle — so the
// capability arrives as package-level functions, exactly like
// `CloudProjectAddress` and `CloudRuntimePreparer` above.
//
// ALL FOUR VARIABLES ARE BOUND HERE, and that is checked rather than assumed: a
// variable with a reader and no writer is a dead wire, and `TenantHost` in
// particular would surface as "no tenant host configured" on every address the
// resolver tried to build.
func wireEnvironmentLookup() {
	backend.TenantHost = resolved.Endpoints.PublicHost

	backend.EnvironmentsOf = func(ctx context.Context, productID string) ([]backend.Environment, error) {
		var rows []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Environments []struct {
				Ref    string `json:"ref"`
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"environments"`
		}
		if err := managementREST().Do(ctx, http.MethodGet, "/api/v2/projects", nil, &rows); err != nil {
			return nil, err
		}
		for _, p := range rows {
			if p.ID != productID {
				continue
			}
			envs := make([]backend.Environment, 0, len(p.Environments))
			for _, e := range p.Environments {
				envs = append(envs, backend.Environment{Ref: e.Ref, Name: e.Name, Status: e.Status})
			}
			return envs, nil
		}
		return nil, fmt.Errorf("no project of yours has the id %q", productID)
	}

	backend.ProductOfRef = func(ctx context.Context, ref string) (backend.Product, error) {
		var row struct {
			ProjectID string `json:"project_id"`
			Ref       string `json:"ref"`
			Name      string `json:"name"`
		}
		if err := managementREST().Do(ctx, http.MethodGet,
			"/api/v2/environments/"+url.PathEscape(ref), nil, &row); err != nil {
			return backend.Product{}, err
		}
		if row.ProjectID == "" {
			return backend.Product{}, fmt.Errorf("the cloud did not say which project %q belongs to", ref)
		}
		// The product's NAME is what a banner prints, and this endpoint answers
		// about one environment. Ask the project listing for the name; a
		// failure there is not fatal — an id with no name still migrates.
		name := row.ProjectID
		var rows []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := managementREST().Do(ctx, http.MethodGet, "/api/v2/projects", nil, &rows); err == nil {
			for _, p := range rows {
				if p.ID == row.ProjectID && p.Name != "" {
					name = p.Name
				}
			}
		}
		return backend.Product{ID: row.ProjectID, Name: name}, nil
	}
}

// newRootCmd builds the whole command tree. Extracted from main() so the
// canonical surface (spec §7.3) can be golden-tested: `palbase --help` IS the
// contract, and a resurrected `branch` / `groups` command must fail the build,
// not a live smoke run.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "palbase",
		Short:   "Palbase CLI — Backend-as-a-Service platform",
		Long:    "Develop backend code and deploy it to Palbase environments.",
		Version: Version,
		// main() prints the returned error once (os.Stderr) — let it own that so
		// cobra doesn't ALSO print it (double-print).
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Flags/args parsed fine to reach here, so a later RunE failure is a
			// runtime error, not misuse — don't dump the usage block after it.
			// (Genuine flag/arg parse errors fail before this and still show usage.)
			cmd.SilenceUsage = true
			r, err := config.Resolve()
			if err != nil {
				return err
			}
			resolved = r
			authClient = auth.NewClient(auth.Config{
				AuthURL:   r.Endpoints.Auth,
				StudioURL: r.Endpoints.Studio,
				ClientID:  "palbase-cli",
			}, os.Stdout)
			// The bridge from "signed in" to "can open this project": every
			// target-relative verb resolves a credential, and for a cloud
			// project that answer lives in the control plane's ledger.
			wireCloudKeyFetcher()
			wireDPoPSigner()
			// Whether an address is one of OUR projects is a fact about the
			// address, and it must not depend on a request succeeding — see
			// backend.isCloudProjectAddress for what happened when it did.
			backend.CloudProjectAddress = func(tenantURL string) bool {
				_, ok := tenantRefOf(tenantURL, resolved.Endpoints.PublicHost)
				return ok
			}
			// Which environments a project has is the control plane's answer,
			// and this is where the backend package learns how to ask.
			wireEnvironmentLookup()
			backend.SelectedEnvFlag = selectedEnv
			return nil
		},
	}

	// THE ONE GLOBAL FLAG, and it names the thing a verb acts on.
	//
	// There were global flags here once — `--project` and `--environment` —
	// and they were retired for a sharp reason: they resolved through
	// `GET /api/v2/projects`, a route the cloud did not serve, so they parsed,
	// were documented, and selected NOTHING. The route exists now (measured:
	// 401, not 404) and the gate in surface_test.go asserts that rather than
	// asserting the flag's absence.
	//
	// `--env` and not `--environment`: the retired flag's name is banned by a
	// gate whose reason still holds — a reader who types the old name should
	// not be answered by a new mechanism wearing it.
	rootCmd.PersistentFlags().StringVar(&selectedEnv, "env", "",
		"environment to act on (name or ref); overrides `palbase env use` for this call only")

	// Cloud lookup is lazy so help needs no account credential.
	backendResolvers := backend.Resolvers{
		Endpoints: func() config.Endpoints { return resolved.Endpoints },
		// Project names use the same management credential as `project list`.
		REST: func() backend.REST { return managementREST() },
	}

	rootCmd.AddCommand(
		loginCmd(),
		logoutCmd(),
		whoamiCmd(),
		doctorCmd(),
		palbaseenv.Cmd(palbaseenv.Resolvers{REST: func() palbaseenv.REST { return managementREST() }}),
		openCmd(),
		project.Cmd(project.Resolvers{
			REST:  func() project.REST { return managementREST() },
			Cloud: func() project.Bootstrapper { return cloudFacts{} },
		}),
		apikey.Cmd(apikey.Resolvers{
			REST:   func() apikey.REST { return managementREST() },
			Target: func() (apikey.Target, error) { return linkedTarget() },
		}),
		debugconsole.Cmd(debugconsole.Resolvers{}),
		versions.Cmd(versions.Resolvers{
			REST: func(cmd *cobra.Command) (versions.REST, error) { return openStackManagement(cmd) },
		}),
		logs.Cmd(logs.Resolvers{
			REST: func() logs.REST { return managementREST() },
			// Adresten ref: `tenantRefOf` bunu ŞEKİLDEN okuyor ve `push`,
			// `link`, kimlik yolu hep aynı fonksiyonu kullanıyor. İkinci bir
			// çözücü yazmak, aynı soruya iki cevap üretirdi.
			CloudRef: func(url string) (string, bool) {
				return tenantRefOf(url, resolved.Endpoints.PublicHost)
			},
		}),
		members.Cmd(members.Resolvers{
			REST:   func() members.REST { return managementREST() },
			Target: func() (members.Target, error) { return linkedTarget() },
		}),
		secret.Cmd(),
		secret.RunCmd(),
		dbcmd.Cmd(),
		storage.Cmd(storage.Resolvers{REST: func(cmd *cobra.Command) (storage.REST, error) { return openStackManagement(cmd) }}),
		flags.Cmd(flags.Resolvers{
			// One seam: definitions AND per-user overrides both act on the linked
			// project over REST. The overrides rode Studio only while nothing could
			// open their service-role gate.
			REST: func(cmd *cobra.Command) (flags.REST, error) { return openStackManagement(cmd) },
		}),
		authadmin.Cmd(authadmin.Resolvers{REST: openStackManagement, RefreshClientConfig: func(cmd *cobra.Command) error { return backend.RefreshLinkedClients(cmd.Context(), cmd.ErrOrStderr()) }}),
		egress.Cmd(egress.Resolvers{REST: openStackEgress}),
		notifications.Cmd(notifications.Resolvers{
			REST: func(cmd *cobra.Command) (notifications.REST, error) { return openStackManagement(cmd) },
		}),
		testuser.Cmd(),
		roles.Cmd(),
		// `palbase test` mints through testuser's own path, so the identities it
		// exports are the same shape `test-user create --json` prints.
		palbasetest.Cmd(palbasetest.Resolvers{
			Target: func(cmd *cobra.Command) (palbasetest.Target, error) {
				target, err := backend.ReadTarget()
				if err != nil {
					return palbasetest.Target{}, err
				}
				key, keyErr := backend.PublishableKey(cmd.Context(), target)
				if keyErr != nil {
					return palbasetest.Target{}, keyErr
				}
				return palbasetest.Target{URL: target.URL, APIKey: key}, nil
			},
			Mint: testuser.MintIdentities,
		}),
	)

	rootCmd.AddCommand(backend.Commands(backendResolvers)...)

	validateCommandTree(rootCmd)
	return rootCmd
}

// Commands accept positional arguments only when they declare them. Runnable
// groups let Cobra validate unknown subcommands before displaying group help.
func validateCommandTree(cmd *cobra.Command) {
	if cmd.Args == nil {
		cmd.Args = cobra.NoArgs
	}
	if !cmd.Runnable() && cmd.HasSubCommands() {
		cmd.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	}
	for _, child := range cmd.Commands() {
		validateCommandTree(child)
	}
}

func loginCmd() *cobra.Command {
	var create bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Palbase",
		Long: `Sign in, and keep the session on this machine.

This opens a browser. You type your password into the panel, over TLS, on a
page whose address you can read — never into this terminal. What comes back
here is a code that is useless without a secret this process generated and
never sent.

Use --create to open a new account instead.

For a headless run — CI, an agent in a container — there is no sign-in at all:
set PALBASE_ACCESS_TOKEN and every command resolves it.

Two things do NOT come through here:
  a project running on this machine   ` + "`palbase start`" + ` writes that credential itself
  a stack you host yourself           ` + "`palbase link <url> --token-stdin`" + ``,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Only a NON-default deployment announces itself. See printDeployment.
			auth.PrintDeployment(os.Stdout, resolved.Endpoints.PlatformAPI)
			if create {
				return authClient.SignUp(cmd.Context())
			}
			return authClient.Login(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&create, "create", false,
		"open a new account on this cloud instead of signing in to an existing one")
	return cmd
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget this machine's credentials",
		Long: `Revoke the cloud session, and forget the credential for the project this
checkout is linked to.

Both, because "log out" means one thing to a person and this machine holds two
kinds of credential: the cloud session, and whatever ` + "`palbase start`" + ` or
` + "`palbase link`" + ` wrote for a project. Leaving the second behind is how a machine
keeps opening a project its owner believes they signed out of.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The project's first: it cannot fail for a reason the person needs
			// to act on, and doing it after a cloud logout that errors would
			// leave the credential behind with nothing said about it.
			if target, err := backend.ReadTarget(); err == nil {
				if err := backend.ForgetCredential(target.URL); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "forgot the credential for %s\n", target.Describe())
			}
			return authClient.Logout(cmd.Context())
		},
	}
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in account",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Whoami owns the whole block (user + mode + auth). The banner
			// here used to ALSO print Mode:, so `whoami` showed two Mode
			// lines — dropped.
			return authClient.Whoami(cmd.Context())
		},
	}
}
