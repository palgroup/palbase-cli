// Package secret is `palbase secret`: what this project holds a credential for.
//
// Two rules shape everything here, and both came from watching the old version
// work.
//
// THE VALUE NEVER LANDS. There is no dotenv file, no `pull` that writes one, and
// no flag that asks for values on a terminal. The old group's `pull` wrote every
// decrypted secret to .env.local, which is how a production credential ends up
// in a screen share, a backup, and eventually a repository. A secret set here is
// sealed in the project's vault and read by exactly two things: the deployed code
// at boot, and `palbase run` when it hands a child process its environment.
//
// THE PROJECT IS THE TARGET. These verbs act on whatever this checkout is linked
// to — the stack on this machine while one is running, the linked environment
// otherwise — through the same management surface as everything else. Secrets
// belong to an environment: staging's SENTRY_DSN is not production's.
package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage what this project holds a credential for",
		Long: `Secrets live in the project's vault and nowhere else.

  palbase secret set NAME --stdin   Read the value from standard input
  palbase secret set NAME=value     Or give it inline (it lands in your shell history)
  palbase secret list               Names and when they last changed — never values
  palbase secret remove NAME        Take one away

There is no .env file: nothing here reads one and nothing here writes one. The
deployed code gets these at boot, and ` + "`palbase run`" + ` gives them to a command you
run on this machine without writing them down anywhere.`,
	}
	cmd.AddCommand(setCmd(), listCmd(), removeCmd())
	return cmd
}

// project is one resolved target and the identity to act on it as.
type project struct {
	target backend.Target
	cred   backend.Credentials
	client *http.Client
}

// open resolves where a secret verb acts and announces it.
func open(cmd *cobra.Command) (project, error) {
	target, err := backend.PrintTargetFor(cmd)
	if err != nil {
		return project{}, err
	}
	cred, _, err := backend.Credential(target.URL)
	if err != nil {
		return project{}, err
	}
	return project{target: target, cred: cred, client: backend.HTTPClient(target)}, nil
}

func (p project) do(ctx context.Context, method, path, contentType string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(p.target.URL, "/")+path, reader)
	if err != nil {
		return 0, nil, err
	}
	p.cred.Apply(req)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("reach %s: %w", p.target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, raw, err
}

const secretsPath = "/v1/management/secrets"

// entry is one row of the listing: a name and when it last changed. There is no
// value field, and that absence is the design — a struct with one could be
// filled by a future handler without anybody deciding to expose values.
type entry struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

func listCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "What this project holds a secret for",
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := open(cmd)
			if err != nil {
				return err
			}
			status, body, err := target.do(cmd.Context(), http.MethodGet, secretsPath, "", nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return apiError(status, body)
			}
			var answer struct {
				Secrets []entry `json:"secrets"`
			}
			if err := json.Unmarshal(body, &answer); err != nil {
				return fmt.Errorf("read the listing: %w", err)
			}

			out := cmd.OutOrStdout()
			if len(answer.Secrets) == 0 {
				fmt.Fprintln(out, "this project holds no secrets")
				return nil
			}
			sort.Slice(answer.Secrets, func(i, j int) bool {
				return answer.Secrets[i].Name < answer.Secrets[j].Name
			})
			table := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
			fmt.Fprintln(table, "NAME\tLAST CHANGED")
			for _, e := range answer.Secrets {
				fmt.Fprintf(table, "%s\t%s\n", e.Name, e.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
			return table.Flush()
		},
	}
	return cmd
}

func setCmd() *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "set <NAME> --stdin | set <NAME=value>",
		Args:  cobra.ExactArgs(1),
		Short: "Store one value, sealed",
		Long: `Store one secret in the project's vault.

    palbase secret set SENTRY_DSN --stdin < dsn.txt
    cat key.pem | palbase secret set SIGNING_KEY --stdin
    palbase secret set SENTRY_DSN=https://…

--stdin is the one that does not end up in your shell history, which is why it
reads the value whole — trailing newline and all are kept, because a PEM without
its final newline is a PEM that fails to parse.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, value, err := readAssignment(cmd, args[0], fromStdin)
			if err != nil {
				return err
			}
			target, err := open(cmd)
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]string{"value": value})
			if err != nil {
				return err
			}
			status, raw, err := target.do(cmd.Context(), http.MethodPut,
				secretsPath+"/"+url.PathEscape(name), "application/json", body)
			if err != nil {
				return err
			}
			if status != http.StatusOK && status != http.StatusNoContent && status != http.StatusCreated {
				return apiError(status, raw)
			}
			// The name, never the value — this line is going into a terminal
			// buffer, a CI log and somebody's screen recording.
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s is set\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the value from standard input")
	return cmd
}

// maxValueBytes is the vault's ceiling on one secret, restated here so this end
// can REFUSE instead of truncating. The read used to stop at exactly this many
// bytes and never ask whether more had been waiting, so a larger PEM or key was
// sealed as its first 64 KiB and announced as set — a secret nobody can tell is
// corrupt until the code that needs it fails somewhere else. Reading one byte
// past the line is what makes the difference visible.
const maxValueBytes = 64 << 10

// readAssignment turns the two accepted forms into (name, value).
func readAssignment(cmd *cobra.Command, arg string, fromStdin bool) (string, string, error) {
	if fromStdin {
		if strings.Contains(arg, "=") {
			return "", "", fmt.Errorf("--stdin takes just the name: `palbase secret set %s --stdin`",
				strings.SplitN(arg, "=", 2)[0])
		}
		value, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxValueBytes+1))
		if err != nil {
			return "", "", err
		}
		if len(value) > maxValueBytes {
			return "", "", fmt.Errorf("the value on standard input is larger than %d bytes, which is the most one secret can hold — nothing was stored", maxValueBytes)
		}
		if len(value) == 0 {
			return "", "", fmt.Errorf("nothing arrived on standard input — a secret set to empty is a secret nobody notices is gone")
		}
		return arg, string(value), nil
	}

	name, value, found := strings.Cut(arg, "=")
	if !found {
		return "", "", fmt.Errorf("give the value: `palbase secret set %s=<value>`, or `palbase secret set %s --stdin` to keep it out of your shell history", arg, arg)
	}
	if value == "" {
		return "", "", fmt.Errorf("%s= has no value — use --stdin to pipe one in", name)
	}
	return name, value, nil
}

func removeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <NAME>",
		Args:  cobra.ExactArgs(1),
		Short: "Take one away",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := open(cmd)
			if err != nil {
				return err
			}
			status, raw, err := target.do(cmd.Context(), http.MethodDelete, secretsPath+"/"+url.PathEscape(args[0]), "", nil)
			if err != nil {
				return err
			}
			if status != http.StatusNoContent && status != http.StatusOK {
				return apiError(status, raw)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s is gone\n", args[0])
			return nil
		},
	}
	return cmd
}

func apiError(status int, body []byte) error {
	var env struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &env) == nil && env.Description != "" {
		return fmt.Errorf("%s (%d)", env.Description, status)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Errorf("the project answered %d with no detail", status)
	}
	if len(trimmed) > 300 {
		trimmed = trimmed[:300] + "…"
	}
	return fmt.Errorf("%s (%d)", trimmed, status)
}
