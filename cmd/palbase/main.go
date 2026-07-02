package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/palgroup/palbase-cli/internal/admin"
	"github.com/palgroup/palbase-cli/internal/apikey"
	"github.com/palgroup/palbase-cli/internal/apps"
	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/branch"
	"github.com/palgroup/palbase-cli/internal/config"
	dbcmd "github.com/palgroup/palbase-cli/internal/db"
	"github.com/palgroup/palbase-cli/internal/flags"
	"github.com/palgroup/palbase-cli/internal/github"
	"github.com/palgroup/palbase-cli/internal/groups"
	"github.com/palgroup/palbase-cli/internal/logs"
	"github.com/palgroup/palbase-cli/internal/members"
	"github.com/palgroup/palbase-cli/internal/notifications"
	"github.com/palgroup/palbase-cli/internal/project"
	"github.com/palgroup/palbase-cli/internal/scaffold"
	"github.com/palgroup/palbase-cli/internal/secret"
	"github.com/palgroup/palbase-cli/internal/storage"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/palgroup/palbase-cli/internal/testuser"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/spf13/cobra"
)

var Version = "dev"

// modeFlag is bound to the persistent --mode flag on the root command.
var modeFlag string

// resolved is populated in PersistentPreRunE and consumed by subcommands.
var resolved config.Resolved

// authClient is built per invocation from the resolved mode/endpoints.
var authClient *auth.Client

// studioClient is the tRPC client the backend lifecycle commands
// (serve/list/status/types/...) use to talk to Studio. Built per
// invocation against resolved.Endpoints.Studio.
// Retained ONLY for backend until backend REST routes exist (S5.4
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
// The Resolvers callbacks below are `func() REST` — they don't carry the
// cobra command context. Wiring that through every command package would
// be a wide refactor for a single side-effect (refresh-token HTTP call);
// instead we cap the refresh with a short timeout here. Refresh hits
// /oauth/token on palauth, no streaming, so 30s is generous.
func managementREST() *transport.Client {
	key, _ := auth.LoadDPoPKey(string(resolved.Mode))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// ManagementToken auto-refreshes an expired login token via /oauth/token
	// (CLI-12); on failure we leave the credential empty and let
	// transport.Client.Do emit the actionable "run palbase login" error.
	pat, _ := authClient.ManagementToken(ctx)
	return transport.New(resolved.Endpoints.PlatformAPI, key, pat)
}

func main() {
	rootCmd := &cobra.Command{
		Use:     "palbase",
		Short:   "Palbase CLI — Backend-as-a-Service platform",
		Long:    "Develop, test, and deploy backend projects on Palbase.",
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
			studioClient = studio.New(r.Endpoints.Studio, func(ctx context.Context) (string, error) {
				return authClient.GetValidToken(ctx)
			})
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&modeFlag, "mode", "",
		"environment mode: prod or dev (overrides config + PALBASE_MODE)")

	rootCmd.AddCommand(
		loginCmd(),
		logoutCmd(),
		whoamiCmd(),
		modeCmd(),
		doctorCmd(),
		openCmd(),
		scaffold.Cmd(),
		project.Cmd(project.Resolvers{
			REST: func() project.REST { return managementREST() },
		}),
		branch.Cmd(branch.Resolvers{
			REST: func() branch.REST { return managementREST() },
		}),
		apikey.Cmd(apikey.Resolvers{
			REST: func() apikey.REST { return managementREST() },
		}),
		apps.Cmd(apps.Resolvers{
			Studio: func() apps.Studio { return studioClient },
		}),
		groups.Cmd(groups.Resolvers{
			Studio: func() groups.Studio { return studioClient },
		}),
		logs.Cmd(logs.Resolvers{
			Studio: func() logs.Studio { return studioClient },
		}),
		members.Cmd(members.Resolvers{
			Studio: func() members.Studio { return studioClient },
		}),
		github.Cmd(github.Resolvers{
			Studio:    func() github.Studio { return studioClient },
			StudioURL: func() string { return resolved.Endpoints.Studio },
		}),
		secret.Cmd(secret.Resolvers{
			Studio: func() *studio.Client { return studioClient },
		}),
		dbCmdWithTypes(),
		storage.Cmd(),
		flags.Cmd(),
		notifications.Cmd(notifications.Resolvers{
			Studio: func() *studio.Client { return studioClient },
		}),
		admin.NewCommand(admin.Resolvers{
			REST: func() admin.REST { return managementREST() },
		}),
		testuser.Cmd(testuser.Resolvers{
			// *studio.Client satisfies testuser.Studio (Query/Mutation).
			Studio: func() testuser.Studio { return studioClient },
		}),
	)

	// CLI-1 flat redesign: the backend lifecycle commands (pull/push/dev/
	// list/rollback/status/disable/types) live at the TOP LEVEL — palbase
	// IS the backend CLI, there is no `backend` parent. Resolvers close
	// over the package-level globals so PersistentPreRunE has populated
	// them by the time a subcommand's RunE fires.
	rootCmd.AddCommand(backend.Commands(backend.Resolvers{
		Auth:      func() *auth.Client { return authClient },
		Studio:    func() *studio.Client { return studioClient },
		Endpoints: func() config.Endpoints { return resolved.Endpoints },
		// Reuse the single mgmt-client builder (same DPoP/PAT auth path as
		// project/apikey) for the mode-aware push/pull/clone verbs.
		REST: func() backend.REST { return managementREST() },
	})...)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// dbCmdWithTypes composes the db command group with `db types` (the
// palbase-env.d.ts generator, owned by the backend package's local Node
// tooling). Composition happens here in main — the db package stays free of a
// backend import.
func dbCmdWithTypes() *cobra.Command {
	cmd := dbcmd.Cmd(dbcmd.Resolvers{
		Studio: func() *studio.Client { return studioClient },
	})
	cmd.AddCommand(backend.EnvTypesCmd())
	return cmd
}

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in to Palbase via browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(os.Stdout, "Mode: %s (source=%s, studio=%s)\n",
				resolved.Mode, resolved.Source, resolved.Endpoints.Studio)
			return authClient.Login(cmd.Context())
		},
	}
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out and revoke session",
		RunE: func(cmd *cobra.Command, args []string) error {
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

