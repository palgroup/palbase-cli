package backend

import "github.com/spf13/cobra"

func newAndroidUseCmd(r Resolvers) *cobra.Command {
	return newNativeUseCmd(r, "android")
}
