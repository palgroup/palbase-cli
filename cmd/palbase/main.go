package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/seklabsnet/palbase-cli/internal/auth"
	"github.com/spf13/cobra"
)

var Version = "dev"

var authClient *auth.Client

func main() {
	authClient = auth.NewClient(auth.Config{
		AuthURL:  getEnv("PALBASE_AUTH_URL", "https://auth.palbase.io"),
		ClientID: "palbase-cli",
	}, os.Stdout)

	rootCmd := &cobra.Command{
		Use:     "palbase",
		Short:   "Palbase CLI — Backend-as-a-Service platform",
		Long:    "Develop, test, and deploy backend projects on Palbase.",
		Version: Version,
	}

	rootCmd.AddCommand(
		loginCmd(),
		logoutCmd(),
		whoamiCmd(),
		linkCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in to Palbase via browser",
		RunE: func(cmd *cobra.Command, args []string) error {
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
		Short: "Show current logged-in user",
		RunE: func(cmd *cobra.Command, args []string) error {
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
			platformURL := getEnv("PALBASE_PLATFORM_URL", "https://api.palbase.io")

			linker := &auth.Linker{
				AuthClient: authClient,
				PlatformAPI: &auth.HTTPPlatformAPI{
					BaseURL:    platformURL,
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

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
