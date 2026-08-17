package backend

// env_switch.go — `palbase env <slug>` : the same code, a different environment.
//
// An environment is not a mode this CLI carries; it is WHICH PROJECT the
// checkout points at. Underneath, each environment is its own database, its own
// keys and its own address — the platform's own model — so switching is a
// re-point, and everything downstream (push, spec, logs) follows without knowing
// a switch happened.
//
// It refuses on a checkout bound to a URL, and the refusal is the honest one: a
// project you run has one of everything, so there is nothing to switch to.

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env <slug>",
		Args:  cobra.ExactArgs(1),
		Short: "Point this checkout at another environment of the linked project",
		Long: `Switch which environment this checkout acts on.

Only the environment changes: the project stays, the code stays, and every verb
after this one goes to the new address. Run ` + "`palbase spec`" + ` afterwards if an app
in this checkout carries a generated client, because the contract may differ.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvSwitch(strings.TrimSpace(args[0]), cmd.OutOrStdout())
		},
	}
	return cmd
}

func runEnvSwitch(slug string, w io.Writer) error {
	if slug == "" {
		return fmt.Errorf("which environment? e.g. `palbase env staging`")
	}
	target, err := readLinkedProject()
	if err != nil {
		return err
	}
	if target.Project == "" {
		return fmt.Errorf(
			"this checkout is bound to %s, which is one project with one environment — there is nothing to switch to.\n"+
				"Bind it to a cloud project with `palbase link <project>` if that is what you meant",
			target.URL)
	}
	if target.Env == slug {
		fmt.Fprintf(w, "▸ %s (unchanged)\n", target.Describe())
		return nil
	}

	// A NAME THIS APP DOES NOT CARRY IS A TYPO, and the answer to a typo is the
	// list. Accepting it wrote a target nothing could resolve, and the next verb
	// failed somewhere further away with a worse message.
	//
	// Checked against the app's own slot rather than against the cloud: that
	// file is what `link` wrote and what a build selects from, so it is the same
	// set of names the person is choosing between.
	if envs, err := readAppEnvironments("ios"); err == nil && len(envs.Environments) > 0 {
		if _, ok := envs.Environments[slug]; !ok {
			return fmt.Errorf("this app carries no environment called %q.\n  %s\nRun `palbase link` if the project has one this checkout has not fetched",
				slug, strings.Join(envs.names(), "\n  "))
		}
	}

	previous := target.Env
	target.Env = slug
	// The address is resolved from (project, env) when a verb acts, so nothing
	// is cached here: a URL written now would be a second source of truth that
	// goes stale the first time an environment moves.
	target.URL = ""
	if err := WriteTarget(target); err != nil {
		return err
	}
	fmt.Fprintf(w, "▸ %s (was %s)\n", target.Describe(), previous)
	fmt.Fprintln(w, "run `palbase spec` if this checkout carries a generated client")
	return nil
}
