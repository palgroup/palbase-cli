package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/palgroup/palbase-cli/internal/admin"
	"github.com/palgroup/palbase-cli/internal/apikey"
	"github.com/palgroup/palbase-cli/internal/apps"
	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/config"
	dbcmd "github.com/palgroup/palbase-cli/internal/db"
	"github.com/palgroup/palbase-cli/internal/debugconsole"
	"github.com/palgroup/palbase-cli/internal/egress"
	"github.com/palgroup/palbase-cli/internal/flags"
	"github.com/palgroup/palbase-cli/internal/github"
	"github.com/palgroup/palbase-cli/internal/logs"
	"github.com/palgroup/palbase-cli/internal/members"
	"github.com/palgroup/palbase-cli/internal/notifications"
	"github.com/palgroup/palbase-cli/internal/project"
	"github.com/palgroup/palbase-cli/internal/secret"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/storage"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/palgroup/palbase-cli/internal/testuser"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/spf13/cobra"
)

var Version = "dev"

// modeFlag is bound to the persistent --mode flag on the root command.
var modeFlag string

// projectFlag / environmentFlag are the GLOBAL headless overrides (spec §7.3).
// They are the ONLY context flags: Organization is not a CLI context, and the
// Palbase branch no longer exists, so there is no --organization and no
// --branch. An unset flag falls back to `.palbase/selection.json`.
var projectFlag, environmentFlag string

// sel is the shared selection resolver every context-bound command reads. Built
// once per invocation in PersistentPreRunE so a command that needs both the
// Project and the Environment pays for ONE environments listing.
var sel *selection.Resolver

// resolved is populated in PersistentPreRunE and consumed by subcommands.
var resolved config.Resolved

// authClient is built per invocation from the resolved mode/endpoints.
var authClient *auth.Client

// studioClient is the tRPC client MOST command groups use to talk to Studio —
// the backend lifecycle verbs (build/push/pull/clone/deploys/rollback/status/
// spec/web/ios/macos/android), db types, debug console, logs, members,
// github, secret, flags, notifications, testuser. Built per invocation
// against resolved.Endpoints.Studio.
// Retained ONLY until every group has its own Management-API REST route (S5.4
// decision — see docs/decisions/2026-05-24-s5-cli-pat-provisioning-...).
var studioClient *studio.Client

// managementREST lazily builds the Management-API REST client used by
// `palbase project`/`apikey`. It loads the keyring DPoP key + resolves
// the management credential (PALBASE_ACCESS_TOKEN PAT or, for
// interactive use, the DPoP-bound login access token — refreshed in
// place if expired). Missing material surfaces a clear error from
// transport.Client.Do rather than at wiring time, so `palbase login` /
// `config` still run without a credential present.
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

// selectionResolver hands every command package the ONE resolver built in
// PersistentPreRunE. It is a func (not the value) because the command tree is
// constructed BEFORE PersistentPreRunE runs.
func selectionResolver() *selection.Resolver { return sel }

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
			r, err := config.Resolve(modeFlag)
			if err != nil {
				return err
			}
			resolved = r
			authClient = auth.NewClient(auth.Config{
				AuthURL:  r.Endpoints.Auth,
				ClientID: "palbase-cli",
				Mode:     string(r.Mode),
			}, os.Stdout)
			studioClient = studio.New(
				r.Endpoints.Studio,
				func(ctx context.Context) (string, error) { return authClient.GetValidToken(ctx) },
				func(_ context.Context, method, rawURL, token string) (string, error) {
					key, err := auth.LoadDPoPKey(string(r.Mode))
					if err != nil {
						return "", fmt.Errorf("load dpop key: %w", err)
					}
					return key.NewProof(auth.ProofOptions{
						HTTPMethod: method, URL: rawURL, AccessToken: token,
					})
				},
			)
			sel = &selection.Resolver{
				REST:            func() selection.REST { return managementREST() },
				ProjectFlag:     projectFlag,
				EnvironmentFlag: environmentFlag,
			}
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&modeFlag, "mode", "",
		"environment mode: prod or dev (overrides config + PALBASE_MODE)")
	rootCmd.PersistentFlags().StringVar(&projectFlag, "project", "",
		"Project id to act on (overrides .palbase/selection.json)")
	rootCmd.PersistentFlags().StringVar(&environmentFlag, "environment", "",
		"Environment slug or ref to act on (overrides .palbase/selection.json)")

	// One Resolvers value for every backend-package entry point: the top-level
	// lifecycle commands below, and `project create`'s Materialize hop, which
	// reuses the SAME bundle download as clone/pull.
	backendResolvers := backend.Resolvers{
		Auth:      func() *auth.Client { return authClient },
		Studio:    func() *studio.Client { return studioClient },
		Endpoints: func() config.Endpoints { return resolved.Endpoints },
		// Reuse the single mgmt-client builder (same DPoP/PAT auth path as
		// project/apikey) for the provider-aware push/pull/clone verbs.
		REST:      func() backend.REST { return managementREST() },
		Selection: selectionResolver,
	}

	rootCmd.AddCommand(
		loginCmd(),
		logoutCmd(),
		whoamiCmd(),
		modeCmd(),
		doctorCmd(),
		openCmd(),
		project.Cmd(project.Resolvers{
			REST:  func() project.REST { return managementREST() },
			Cloud: func() project.Bootstrapper { return cloudFacts{} },
		}),
		apikey.Cmd(apikey.Resolvers{
			REST:      func() apikey.REST { return managementREST() },
			Selection: selectionResolver,
		}),
		apps.Cmd(apps.Resolvers{
			REST:       func() apps.REST { return managementREST() },
			Selection:  selectionResolver,
			PublicHost: func() string { return resolved.Endpoints.PublicHost },
		}),
		debugconsole.Cmd(debugconsole.Resolvers{
			Studio:     func() debugconsole.Studio { return studioClient },
			Selection:  selectionResolver,
			PublicHost: func() string { return resolved.Endpoints.PublicHost },
		}),
		logs.Cmd(logs.Resolvers{
			Studio:    func() logs.Studio { return studioClient },
			Selection: selectionResolver,
		}),
		members.Cmd(members.Resolvers{
			Studio:    func() members.Studio { return studioClient },
			Selection: selectionResolver,
		}),
		github.Cmd(github.Resolvers{
			Studio:    func() github.Studio { return studioClient },
			StudioURL: func() string { return resolved.Endpoints.Studio },
		}),
		secret.Cmd(),
		secret.RunCmd(),
		dbcmd.Cmd(),
		storage.Cmd(),
		flags.Cmd(flags.Resolvers{
			// *studio.Client satisfies flags.Studio (Query/Mutation); only the
			// `flags user` overrides use it.
			Studio:    func() flags.Studio { return studioClient },
			Selection: selectionResolver,
		}),
		egress.Cmd(),
		notifications.Cmd(notifications.Resolvers{
			Studio:    func() *studio.Client { return studioClient },
			Selection: selectionResolver,
		}),
		// admin deliberately stays on /api/v1: the fleet-operator routes are not
		// part of the Project/Environment surface and were not cut over.
		admin.NewCommand(admin.Resolvers{
			REST: func() admin.REST { return managementREST() },
			// schema-cutover dispatches a throwaway Job and blocks on it, so it gets
			// the long client. With the ordinary timeout the CLI gives up while the
			// Job is still running and the operator is told the render failed.
			Studio: func() admin.Studio { return studioClient.WithTimeout(studio.JobCallTimeout) },
		}),
		testuser.Cmd(testuser.Resolvers{
			// *studio.Client satisfies testuser.Studio (Query/Mutation).
			Studio:    func() testuser.Studio { return studioClient },
			Selection: selectionResolver,
		}),
	)

	// CLI-1 flat redesign: the backend lifecycle commands (build/push/pull/
	// clone/deploys/rollback/status/spec/web/ios/macos/android) live at the
	// TOP LEVEL — palbase IS the backend CLI, there is no `backend` parent.
	// Resolvers close over the package-level globals so PersistentPreRunE has
	// populated them by the time a subcommand's RunE fires.
	rootCmd.AddCommand(backend.Commands(backendResolvers)...)

	return rootCmd
}

// dbCmd is the local stack's schema surface. There is no `db types` here any
// more: palbase-env.d.ts is derived output, and `palbase build` produces
// everything derived — a second command for one of them was a second thing to
// remember and a second thing to forget.

func loginCmd() *cobra.Command {
	var create bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Palbase",
		Long: `Sign in, and keep the session on this machine.

Signing in asks for the email and password of a Palbase cloud account. For a
headless run — CI, an agent in a container — set PALBASE_EMAIL and
PALBASE_PASSWORD, or supply a token directly with PALBASE_ACCESS_TOKEN and skip
this entirely.

Use --create to open a new account on this cloud and sign in with it.

There is no separate sign-in for a project running on this machine: ` + "`palbase start`" + `
writes that credential itself.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(os.Stdout, "Mode: %s (source=%s, cloud=%s)\n",
				resolved.Mode, resolved.Source, resolved.Endpoints.PlatformAPI)
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
		Short: "Show current logged-in user and mode",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Whoami owns the whole block (user + mode + auth). The banner
			// here used to ALSO print Mode:, so `whoami` showed two Mode
			// lines — dropped.
			return authClient.Whoami(cmd.Context())
		},
	}
}

// modeCmd is the single CLI-configuration verb: `palbase mode` shows the
// resolved mode + endpoints, `palbase mode prod|dev` persists a new mode.
// (It replaces the retired `config get/set/list` triple — mode is the only
// config key, so one command owns it.)
func modeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mode [prod|dev]",
		Short: "Show or set the environment mode (prod | dev)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				path, _ := config.Path()
				fmt.Fprintf(os.Stdout, "Mode:        %s (source=%s)\n", resolved.Mode, resolved.Source)
				fmt.Fprintf(os.Stdout, "Config file: %s\n", path)
				fmt.Fprintf(os.Stdout, "Studio:      %s\n", resolved.Endpoints.Studio)
				fmt.Fprintf(os.Stdout, "Auth:        %s\n", resolved.Endpoints.Auth)
				fmt.Fprintf(os.Stdout, "Platform:    %s\n", resolved.Endpoints.PlatformAPI)
				return nil
			}
			m := config.Mode(args[0])
			if !m.Valid() {
				return fmt.Errorf("invalid mode %q — must be 'prod' or 'dev'", args[0])
			}
			f, err := config.Load()
			if err != nil {
				return err
			}
			f.Mode = m
			if err := config.Save(f); err != nil {
				return err
			}
			path, _ := config.Path()
			fmt.Fprintf(os.Stdout, "✓ mode=%s (saved to %s)\n", m, path)
			return nil
		},
	}
}
