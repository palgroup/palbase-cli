package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

var Version = "dev"

// modeFlag is bound to the persistent --mode flag on the root command.
var modeFlag string

// resolved is populated in PersistentPreRunE and consumed by subcommands.
var resolved config.Resolved

// authClient is built per invocation from the resolved mode/endpoints.
var authClient *auth.Client

// studioClient is the tRPC client used by `palbase backend ...` to
// talk to Studio. Built per invocation against resolved.Endpoints.Studio.
var studioClient *studio.Client

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
		linkCmd(),
		configCmd(),
		// Phase 7 — backend opt-in lifecycle. Resolvers close over the
		// package-level globals so PersistentPreRunE has populated them
		// by the time a subcommand's RunE actually fires.
		backend.Cmd(backend.Resolvers{
			Auth:   func() *auth.Client { return authClient },
			Studio: func() *studio.Client { return studioClient },
		}),
	)

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

func linkCmd() *cobra.Command {
	var projectID string

	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link current directory to a Palbase project",
		RunE: func(cmd *cobra.Command, args []string) error {
			linker := &auth.Linker{
				AuthClient: authClient,
				PlatformAPI: &auth.HTTPPlatformAPI{
					BaseURL:    resolved.Endpoints.PlatformAPI,
					HTTPClient: &http.Client{Timeout: 30 * time.Second},
				},
				Output: os.Stdout,
				SelectFn: func(projects []auth.Project) (*auth.Project, error) {
					fmt.Println("Select a project:")
					fmt.Println()
					for i, p := range projects {
						fmt.Printf("  %d. %s (%s)\n", i+1, p.Name, p.ID)
					}
					fmt.Println()

					var choice int
					fmt.Print("Enter number: ")
					if _, err := fmt.Scan(&choice); err != nil {
						return nil, fmt.Errorf("invalid input: %w", err)
					}
					if choice < 1 || choice > len(projects) {
						return nil, fmt.Errorf("invalid selection: %d", choice)
					}
					return &projects[choice-1], nil
				},
			}

			return linker.Link(cmd.Context(), projectID)
		},
	}

	cmd.Flags().StringVar(&projectID, "project", "", "Project ID to link directly")
	return cmd
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
			Use:   "set <key> <value>",
			Short: "Set a config value (keys: mode=prod|dev)",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				key, value := args[0], args[1]
				if key != "mode" {
					return fmt.Errorf("unknown key: %s (supported: mode)", key)
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
