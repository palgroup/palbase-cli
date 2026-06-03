package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/admin"
	"github.com/palgroup/palbase-cli/internal/apikey"
	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/branch"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/project"
	"github.com/palgroup/palbase-cli/internal/secret"
	"github.com/palgroup/palbase-cli/internal/studio"
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
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
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
		configCmd(),
		project.Cmd(project.Resolvers{
			REST: func() project.REST { return managementREST() },
		}),
		branch.Cmd(branch.Resolvers{
			REST: func() branch.REST { return managementREST() },
		}),
		apikey.Cmd(apikey.Resolvers{
			REST: func() apikey.REST { return managementREST() },
		}),
		secret.Cmd(secret.Resolvers{
			Studio: func() *studio.Client { return studioClient },
		}),
		admin.NewCommand(admin.Resolvers{
			REST: func() admin.REST { return managementREST() },
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
	})...)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
			fmt.Fprintf(os.Stdout, "Mode:    %s (source=%s)\n", resolved.Mode, resolved.Source)
			fmt.Fprintf(os.Stdout, "Studio:  %s\n", resolved.Endpoints.Studio)
			return authClient.Whoami(cmd.Context())
		},
	}
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage CLI configuration",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "get <key>",
			Short: "Get a config value (keys: mode)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				switch args[0] {
				case "mode":
					fmt.Fprintf(os.Stdout, "%s\n", resolved.Mode)
					return nil
				default:
					return fmt.Errorf("unknown key: %s (supported: mode)", args[0])
				}
			},
		},
		&cobra.Command{
			Use:   "set <key> [value]",
			Short: "Set a config value (keys: mode=prod|dev). Omit value for an interactive picker.",
			Args:  cobra.RangeArgs(1, 2),
			RunE: func(cmd *cobra.Command, args []string) error {
				key := args[0]
				if key != "mode" {
					return fmt.Errorf("unknown key: %s (supported: mode)", key)
				}
				var value string
				if len(args) == 2 {
					value = args[1]
				} else {
					v, err := promptMode(resolved.Mode)
					if err != nil {
						return err
					}
					value = v
				}
				m := config.Mode(value)
				if !m.Valid() {
					return fmt.Errorf("invalid mode %q — must be 'prod' or 'dev'", value)
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
		},
		&cobra.Command{
			Use:   "list",
			Short: "Show current resolved config",
			RunE: func(cmd *cobra.Command, args []string) error {
				path, _ := config.Path()
				fmt.Fprintf(os.Stdout, "Config file: %s\n", path)
				fmt.Fprintln(os.Stdout, "")
				fmt.Fprintf(os.Stdout, "Mode:        %s (source=%s)\n", resolved.Mode, resolved.Source)
				fmt.Fprintf(os.Stdout, "Studio:      %s\n", resolved.Endpoints.Studio)
				fmt.Fprintf(os.Stdout, "Auth:        %s\n", resolved.Endpoints.Auth)
				fmt.Fprintf(os.Stdout, "Platform:    %s\n", resolved.Endpoints.PlatformAPI)
				return nil
			},
		},
	)

	return cmd
}

// promptMode shows a numeric picker (1=prod, 2=dev) and returns the
// chosen value. ENTER without typing keeps the current mode so a quick
// `palbase config set mode` doubles as "show me the choices, leave it
// alone if I press enter".
func promptMode(current config.Mode) (string, error) {
	options := []config.Mode{config.ModeProd, config.ModeDev}
	fmt.Fprintln(os.Stdout, "Select environment mode:")
	for i, m := range options {
		marker := " "
		if m == current {
			marker = "*"
		}
		fmt.Fprintf(os.Stdout, "  %s %d) %s\n", marker, i+1, m)
	}
	fmt.Fprintf(os.Stdout, "Enter number [%s]: ", current)

	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read selection: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return string(current), nil
	}
	switch line {
	case "1":
		return string(options[0]), nil
	case "2":
		return string(options[1]), nil
	default:
		return "", fmt.Errorf("invalid selection %q — enter 1 or 2", line)
	}
}
