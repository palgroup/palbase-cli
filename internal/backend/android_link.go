package backend

import (
	"github.com/spf13/cobra"
)

func newAndroidCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "android",
		Short: "Wire an Android app to a Palbase project",
	}
	cmd.AddCommand(newNativeLinkCmd(r, "android"), newAndroidUseCmd(r))
	return cmd
}
