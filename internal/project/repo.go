package project

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// connectRepoCmd wires `palbase project connect-repo [owner/name]`.
//
// The other direction — Palbase creates the repo — is part of `project create
// --repo`. This is for a repository that ALREADY exists, which until now had no
// way in: without a Project↔repo mapping the deploy webhook resolves the push to
// no Project and deploys nothing. Silently, because ignoring an unknown repo is
// also the right behaviour for a repo that genuinely is not ours.
//
// With no argument it reads `origin` from the current git checkout, so the
// common case is one word in the directory you already have open.
func connectRepoCmd(r Resolvers) *cobra.Command {
	var rootDir string
	cmd := &cobra.Command{
		Use:   "connect-repo [owner/name]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Connect an existing GitHub repository to the selected project",
		Long: `Connect a repository that already exists to the selected project, so its
pushes deploy (the "bring your own repo" direction — ` + "`project create --repo`" + `
is for letting Palbase create one).

With no argument the repo is read from this checkout's ` + "`origin`" + ` remote.

Requirements, each of which is checked before anything is written:
  • you are an admin of the project;
  • the Palbase GitHub app is installed on that account AND has access to that
    repository (a "selected repositories" install that excludes it is the usual
    cause of a connect that looks fine and then never deploys);
  • the repository is not already connected to another project.

The project's production environment is mapped to the repository's default
branch when it has no mapping yet — an unmapped branch never deploys, so a
connected repo with no mapping would look exactly like one that is not
connected.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sel, err := r.Selection().Resolve(ctx)
			if err != nil {
				return err
			}

			repo := ""
			if len(args) == 1 {
				repo = strings.TrimSpace(args[0])
			} else {
				repo, err = repoFromOriginRemote()
				if err != nil {
					return fmt.Errorf("%w\n\npass it explicitly: palbase project connect-repo <owner/name>", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "using origin: %s\n", repo)
			}

			body := map[string]any{"repo": repo}
			if rootDir != "" {
				body["root_directory"] = rootDir
			}

			var out struct {
				Repo                 string `json:"repo"`
				DefaultBranch        string `json:"default_branch"`
				InstallationID       int64  `json:"installation_id"`
				MappedEnvironmentRef string `json:"mapped_environment_ref"`
			}
			if err := r.REST().Do(ctx, http.MethodPost,
				fmt.Sprintf("/api/v2/projects/%s/repository", sel.ProjectID), body, &out); err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "✓ connected %s (default branch %s)\n", out.Repo, out.DefaultBranch)
			if out.MappedEnvironmentRef != "" {
				fmt.Fprintf(w, "  %s now deploys from %s\n", out.MappedEnvironmentRef, out.DefaultBranch)
			} else {
				// Not a failure: the environments already carry their own mappings.
				// Saying so beats leaving the reader to wonder which branch deploys.
				fmt.Fprintf(w, "  environment branch mappings left as they are (see `palbase env list`)\n")
			}
			fmt.Fprintf(w, "  push to %s to deploy\n", out.DefaultBranch)
			return nil
		},
	}
	cmd.Flags().StringVar(&rootDir, "root-directory", "",
		"monorepo subtree the backend lives in (when it is not the repo root)")
	return cmd
}

// disconnectRepoCmd wires `palbase project disconnect-repo`.
func disconnectRepoCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect-repo",
		Args:  cobra.NoArgs,
		Short: "Stop deploying the selected project's repository pushes",
		Long: `Remove the link between this project and its GitHub repository.

Nothing on GitHub is touched and nothing already deployed is removed — only the
mapping goes, so pushes stop deploying.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sel, err := r.Selection().Resolve(ctx)
			if err != nil {
				return err
			}
			if err := r.REST().Do(ctx, http.MethodDelete,
				fmt.Sprintf("/api/v2/projects/%s/repository", sel.ProjectID), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"✓ disconnected — pushes no longer deploy (the repository itself is untouched)\n")
			return nil
		},
	}
}

// repoFromOriginRemote turns this checkout's `origin` into `owner/name`.
//
// Both URL forms GitHub hands out are accepted, because a developer never picked
// one on purpose:
//
//	git@github.com:owner/name.git
//	https://github.com/owner/name.git
//
// Anything else (a non-GitHub host, no origin) is an error naming what to pass
// instead — guessing would connect the wrong repository.
func repoFromOriginRemote() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("no `origin` remote here (run this in the repo, or pass owner/name)")
	}
	return parseGitHubRemote(strings.TrimSpace(string(out)))
}

func parseGitHubRemote(url string) (string, error) {
	u := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	switch {
	case strings.HasPrefix(u, "git@github.com:"):
		u = strings.TrimPrefix(u, "git@github.com:")
	case strings.HasPrefix(u, "ssh://git@github.com/"):
		u = strings.TrimPrefix(u, "ssh://git@github.com/")
	case strings.HasPrefix(u, "https://github.com/"):
		u = strings.TrimPrefix(u, "https://github.com/")
	case strings.HasPrefix(u, "http://github.com/"):
		u = strings.TrimPrefix(u, "http://github.com/")
	default:
		return "", fmt.Errorf("origin %q is not a github.com remote", url)
	}
	u = strings.Trim(u, "/")
	parts := strings.Split(u, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("could not read owner/name out of origin %q", url)
	}
	return parts[0] + "/" + parts[1], nil
}
