package secret

// run.go — `palbase run -- <command>`: your command, with this project's secrets.
//
// The problem it solves is the one that created .env files. A worker, a script,
// a `npm run dev` needs the same credentials the deployed code has, so people
// write them to a file, and the file outlives the reason for it.
//
// So the values go into the CHILD PROCESS and nowhere else: read from the vault,
// placed in the environment of one command, gone when it exits. Nothing is
// written to disk, nothing is printed, and the parent's own environment is not
// modified — a value that leaks into this process would be inherited by
// everything it starts afterwards.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// RunCmd is a TOP-LEVEL verb, not `secret run`: what you are running is your
// command, and the secrets are a detail of how it starts.
func RunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run -- <command> [args…]",
		Args:  cobra.MinimumNArgs(1),
		Short: "Run a command with this project's secrets in its environment",
		Long: `Run a command with the project's secrets in its environment.

    palbase run -- npm run worker
    palbase run -- ./scripts/backfill.sh

The values are read from the vault and placed in the child process's environment.
Nothing is written to disk, nothing is printed, and this shell's environment is
untouched — so a value cannot be inherited by whatever you run next.

The child's exit status is this command's exit status.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := open(cmd)
			if err != nil {
				return err
			}
			values, err := loadSecrets(cmd, target)
			if err != nil {
				return err
			}
			return execute(cmd, args, values)
		},
	}
	// Everything after `--` belongs to the child, including its own flags. Without
	// this, `palbase run -- npm test --watch` would have cobra reject --watch.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// loadSecrets reads every name this project holds, one value at a time.
//
// One request per name is the shape the surface offers, and it is the right one
// here: a bulk read would be a single call that carries the whole project, and
// this is the only caller that ever needs values at all.
func loadSecrets(cmd *cobra.Command, target project) (map[string]string, error) {
	status, body, err := target.do(cmd.Context(), http.MethodGet, secretsPath, "", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(status, body)
	}
	var answer struct {
		Secrets []entry `json:"secrets"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("read the listing: %w", err)
	}

	values := make(map[string]string, len(answer.Secrets))
	for _, e := range answer.Secrets {
		status, raw, err := target.do(cmd.Context(), http.MethodGet, secretsPath+"/"+e.Name+"/value", "", nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			// Loud, not skipped. A child that starts without a credential it
			// needs fails later, somewhere else, in a way nobody traces back to
			// here — so the run stops at the name that could not be read.
			return nil, fmt.Errorf(
				"%s could not be read (%w).\nSet it again with `palbase secret set %s --stdin`, or remove it if the code no longer wants it",
				e.Name, apiError(status, raw), e.Name)
		}
		values[e.Name] = string(raw)
	}
	return values, nil
}

// execute runs the command with the secrets added to this process's environment
// COPY. os.Setenv is deliberately not used: it would put a credential in the
// parent, where everything started afterwards inherits it.
func execute(cmd *cobra.Command, args []string, values map[string]string) error {
	child := exec.CommandContext(cmd.Context(), args[0], args[1:]...) //nolint:gosec // the command is the user's
	child.Env = append(os.Environ(), envPairs(values)...)
	child.Stdin = cmd.InOrStdin()
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()

	// Names only, so the operator can see what the command was given without the
	// values ending up in a scrollback — and on stderr, so a child whose output
	// is being piped stays parsable.
	fmt.Fprintf(cmd.ErrOrStderr(), "▸ %d secret(s): %s\n", len(values), strings.Join(sortedNames(values), " "))

	// Ctrl-C belongs to the child. Without this the CLI would take the signal,
	// return, and leave a running process attached to the terminal.
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)

	err := child.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitStatus(exitErr.ExitCode())
	}
	if err != nil {
		return fmt.Errorf("run %s: %w", args[0], err)
	}
	return nil
}

func envPairs(values map[string]string) []string {
	pairs := make([]string, 0, len(values))
	for _, name := range sortedNames(values) {
		pairs = append(pairs, name+"="+values[name])
	}
	return pairs
}

func sortedNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// exitStatus carries the child's status out without a message: `palbase run --
// npm test` that fails must exit non-zero, and printing an error above the test
// runner's own output would read as a second, different failure.
type exitStatus int

func (exitStatus) Error() string   { return "" }
func (e exitStatus) ExitCode() int { return int(e) }
