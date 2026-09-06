package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestUnknownSubcommandsFailBeforeRunning(t *testing.T) {
	var walk func(*cobra.Command, []string)
	walk = func(cmd *cobra.Command, path []string) {
		if !cmd.HasSubCommands() {
			return
		}
		t.Run(cmd.CommandPath(), func(t *testing.T) {
			root := newRootCmd()
			ran := false
			root.PersistentPreRunE = func(*cobra.Command, []string) error {
				ran = true
				return nil
			}
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append(append([]string{}, path...), "unknown-subcommand"))
			require.ErrorContains(t, root.Execute(), "unknown-subcommand")
			require.False(t, ran, "invalid arguments reached command setup")
		})
		for _, child := range cmd.Commands() {
			walk(child, append(append([]string{}, path...), child.Name()))
		}
	}
	walk(newRootCmd(), nil)
}

func TestGroupHelpWorksWithoutCredentials(t *testing.T) {
	t.Setenv("PALBASE_ACCESS_TOKEN", "")
	for _, args := range [][]string{nil, {"project"}, {"auth", "providers", "config"}, {"test-user", "templates", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(args)
			require.NoError(t, root.Execute())
			require.Contains(t, out.String(), "Usage:")
		})
	}
}

func TestSessionCommandsRejectUnexpectedArguments(t *testing.T) {
	for _, name := range []string{"login", "logout", "whoami"} {
		t.Run(name, func(t *testing.T) {
			root := newRootCmd()
			ran := false
			root.PersistentPreRunE = func(*cobra.Command, []string) error { ran = true; return nil }
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{name, "unexpected"})
			require.Error(t, root.Execute())
			require.False(t, ran)
		})
	}
}
