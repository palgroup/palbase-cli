package backend

// platform_link_target.go — `palbase <platform> link` obeys the target this
// checkout is bound to.
//
// `login`, `push` and `spec` are all TARGET-RELATIVE: a checkout bound to a
// project you run works against THAT project, and the same verb reaches the
// cloud when it is not. The platform link commands were the exception, and the
// exception was silent: run in a checkout bound to https://127.0.0.1, `palbase
// ios link` resolved a CLOUD project, overwrote .palbase/ios/palbase-config.json
// with that environment's URL and key, and regenerated the Swift client from the
// cloud's contract. Measured on 2026-08-16 in the todoapp checkout — the app
// went on pointing at a host the person had stopped using, holding a key that
// still worked there.
//
// Which makes it worse than an error: everything downstream succeeded.

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// linkToBoundProject performs the platform link against the project this
// checkout is bound to, and reports whether it did.
//
// false with a nil error means "not bound to a project" — the caller takes the
// cloud path, which is the only other thing it could have meant.
func linkToBoundProject(cmd *cobra.Command, platform string, w io.Writer) (bool, error) {
	target, err := ReadTarget()
	if err != nil {
		return false, nil
	}
	return true, runLink(cmd.Context(), linkOpts{
		url:       target.URL,
		insecure:  target.Insecure,
		platforms: []string{platform},
	}, w)
}

// refuseUseOnBoundProject stops `palbase <platform> use <environment>` in a
// checkout bound to a project.
//
// A project you run has ONE of everything — one database, one key pair, one
// address — so there is no environment to switch to, and the cloud command
// would answer by pointing the app somewhere else entirely. Refusing names the
// binding, because "no such environment" would send somebody looking for one.
func refuseUseOnBoundProject(platform string) error {
	target, err := ReadTarget()
	if err != nil {
		return nil
	}
	return fmt.Errorf(
		"this checkout is bound to %s, which is one project with one environment — "+
			"there is nothing to switch to.\nRe-run `palbase link %s` to refresh its config, "+
			"or `palbase %s link` once this checkout is no longer bound",
		target.URL, target.URL, platform)
}
