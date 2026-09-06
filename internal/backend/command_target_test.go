package backend

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestLinkedCommandsReportTargetErrors(t *testing.T) {
	for _, argv := range [][]string{{"status"}, {"deploys"}, {"push"}, {"push", "--accept-breaking"}, {"pull"}, {"spec"}, {"rollback", "old1"}} {
		for _, broken := range []bool{false, true} {
			name := strings.Join(argv, " ")
			if broken {
				name += "/invalid project file"
			}
			t.Run(name, func(t *testing.T) {
				t.Chdir(t.TempDir())
				if broken {
					require.NoError(t, os.MkdirAll(".palbase", 0o755))
					require.NoError(t, os.WriteFile(projectPath(), []byte(`{"url":`), 0o644))
				}
				cmd := &cobra.Command{Use: "palbase", SilenceErrors: true, SilenceUsage: true}
				cmd.AddCommand(Commands(Resolvers{})...)
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(argv)
				err := cmd.Execute()
				if broken {
					require.ErrorContains(t, err, "read .palbase/project.json")
					require.NotContains(t, err.Error(), "not linked")
				} else {
					require.ErrorContains(t, err, "palbase link <ref>")
					require.NotContains(t, err.Error(), "--environment")
				}
			})
		}
	}
}
