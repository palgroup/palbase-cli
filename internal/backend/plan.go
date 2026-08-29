package backend

// plan.go — `palbase plan`: what a push would do, before it does it.
//
// A push carries FOUR things and always has: the code, the schema the code
// declares, the configuration beside it, and the secrets it needs to run. They
// travel together because they fail together — code that reads a flag which has
// not been declared, or a credential that has not been set, is code that deploys
// green and 500s on its first request.
//
// So this shows all four, and touches nothing. The schema half is computed by
// the project itself (the same computation the push runs, stopping before it
// writes), which is what makes it a plan rather than a guess: a differ written
// here would have its own opinion about what a type change costs.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newPlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Args:  cobra.NoArgs,
		Short: "Show what `palbase push` would change",
		Long: `Show the whole change set — code, schema, config and secrets — and apply none of it.

Nothing is written to the target: the schema half is computed by the project
itself, which is the same computation the push runs, stopped before it writes.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := PrintTargetFor(cmd)
			if err != nil {
				return err
			}
			if err := refuseCloudSelectionFlags(cmd, target); err != nil {
				return err
			}
			cred, _, err := Credential(target.URL)
			if err != nil {
				return err
			}
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := RequireBackendPlane(dir); err != nil {
				return err
			}
			return runPlan(cmd.Context(), dir, target, cred, cmd.OutOrStdout())
		},
	}
}

func runPlan(ctx context.Context, dir string, target Target, cred Credentials, out io.Writer) error {
	// CODE. Building is how "would this even deploy" gets answered here rather
	// than on the target, and it is THE SAME BUILD the push runs — the same
	// function, not merely a build of the same sources.
	//
	// It used to call runBuild, which is the esbuild path `palbase build` and the
	// cloud deploy take, while a push to a stack goes through buildStackArtifact,
	// which is bun. The comment here claimed they were the same build and they
	// were two bundlers with two opinions about how a decorator lowers — which is
	// the exact difference stack_bundle.go says the bun choice exists to remove.
	// A plan that goes green on code the push then refuses is worse than no plan:
	// it is a check whose passing means nothing.
	fmt.Fprintln(out, "code")
	uses, err := buildStackArtifact(ctx, dir, indent(out))
	if err != nil {
		return err
	}
	// An @Upload naming a bucket the stack does not have is a push that will be
	// refused, so a plan that stayed quiet about it would be a plan that missed
	// the one thing it is for.
	if len(uses) > 0 {
		have, bucketErr := stackBuckets(ctx, target)
		if bucketErr != nil {
			return bucketErr
		}
		if bucketErr := unknownUploadBuckets(uses, bucketNames(have)); bucketErr != nil {
			return bucketErr
		}
		fmt.Fprintf(indent(out), "%d @Upload route(s), every bucket exists\n", len(uses))
	}

	// SCHEMA, computed by the project against its own database.
	fmt.Fprintln(out, "schema")
	source, err := os.ReadFile(filepath.Join(dir, "db", "schema.ts"))
	switch {
	case os.IsNotExist(err):
		fmt.Fprintln(out, "  no db/schema.ts — this project declares no tables")
	case err != nil:
		return err
	default:
		status, body, err := managementCall(ctx, target, cred, http.MethodPost,
			"/v1/management/schema/plan", source, "text/plain")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("%s answered %d when asked to plan the schema: %s",
				target.Describe(), status, trimBody(body))
		}
		renderSchemaPlan(out, body)
	}

	// NO CONFIG SECTION, and its absence is the point.
	//
	// There used to be one, and it could not tell the truth: the target had no
	// route that reported what it currently held, so the line said what would be
	// SENT rather than what would CHANGE — and for a while it was not even true
	// that the sections were applied at all.
	//
	// Settings are written directly now, by whoever changes them, so a plan has
	// nothing to say about them: they are already in effect. What a push carries
	// is code and schema, and that is what this shows.
	return nil
}

// schemaPlanWire is the project's SchemaPlan.
type schemaPlanWire struct {
	InSync      bool     `json:"in_sync"`
	Changes     []string `json:"changes"`
	Destructive []struct {
		Kind    string `json:"kind"`
		Table   string `json:"table"`
		Column  string `json:"column"`
		Rows    int64  `json:"rows"`
		NonNull int64  `json:"non_null"`
	} `json:"destructive"`
	Unsupported []string `json:"unsupported"`
}

func renderSchemaPlan(out io.Writer, body []byte) {
	var plan schemaPlanWire
	if err := json.Unmarshal(body, &plan); err != nil {
		fmt.Fprintf(out, "  (unreadable plan: %s)\n", trimBody(body))
		return
	}
	if plan.InSync && len(plan.Changes) == 0 && len(plan.Destructive) == 0 {
		fmt.Fprintln(out, "  in sync")
		return
	}
	for _, change := range plan.Changes {
		fmt.Fprintf(out, "  %s\n", change)
	}
	for _, drop := range plan.Destructive {
		if drop.Column != "" {
			fmt.Fprintf(out, "  ⚠ drop %s.%s — %d value(s) in %d row(s)\n",
				drop.Table, drop.Column, drop.NonNull, drop.Rows)
		} else {
			fmt.Fprintf(out, "  ⚠ drop table %s — %d row(s)\n", drop.Table, drop.Rows)
		}
	}
	for _, item := range plan.Unsupported {
		fmt.Fprintf(out, "  not applied by this rail: %s\n", item)
	}
	if len(plan.Destructive) > 0 {
		fmt.Fprintln(out, "  the ⚠ changes need --approve")
	}
}

// THE SECRET AND CONFIG PLAN IS GONE, and its absence is the change.
//
// `palbase plan` used to read `.palbase/config.json` and report what a push
// would do to the target's secrets, plus which config kinds the project
// declared. Both halves went with `config/` (2026-08-29):
//
//   - the secret plan compared config/secrets.ts against the target's vault.
//     The check moved EARLIER and got stricter: a name a controller may spell
//     comes from `palbase-stack.d.ts`, generated off the stack, so reading a
//     secret nobody set does not compile.
//   - `declaredConfigKinds` printed "config: flags, storage, …" for declarations
//     the deploy applied to NOTHING — the report the management contract calls
//     "silent AND untrue" (measured 2026-08-17). It had zero callers by the end.
//
// `localSource` stayed: `palbase secret set` still carries values from the stack
// on this machine, and stack_push.go uses it.

func indent(w io.Writer) io.Writer { return &prefixed{w: w, prefix: "  "} }
