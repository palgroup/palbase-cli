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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func newPlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Args:  cobra.NoArgs,
		Short: "Show what `palbase push` would change",
		Long: `Show the whole change set — code and schema — and apply none of it.

The config and secret sections are gone with config/ itself (23.0.0); this
help promised them for one release after they stopped being printed, which is
the shape of stale text this CLI exists not to ship.

Nothing is written to the target: the schema half is computed by the project
itself, which is the same computation the push runs, stopped before it writes.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := PrintTargetFor(cmd)
			if err != nil {
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
	uses, _, err := buildStackArtifact(ctx, dir, indent(out))
	// `plan` answers a question and ships nothing, so the bundle it just built
	// has no reader at all — leaving it would put a stale artifact where the
	// next push would find one and have to distrust it.
	defer removeBundleOutput(dir)
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

	// IMAGE, before the schema, because it is the coarser change: a pod replaced
	// is a bigger thing to know about than a column added, and a plan reads
	// top-down. Both sides are read here rather than inside the writer so the
	// writer stays a pure formatter with a test that needs no network.
	//
	// The target is the SDK INSTALLED IN THIS CHECKOUT — the same number
	// `palbase start` resolves locally, which is the whole point: what you tested
	// against is what the push carries. The current is what the project SAYS it
	// runs; silent when it will not say, and the writer treats that silence as
	// "unknown", not as "changing".
	if installed, err := installedSDKVersion(dir); err == nil {
		sdkCtx, cancelSDK := context.WithTimeout(ctx, 10*time.Second)
		running, sdkErr := projectSDKVersion(sdkCtx, target, cred)
		cancelSDK()
		if sdkErr == nil {
			writeImagePlan(out, running, installed)
		}
	}

	// SCHEMA, computed by the project against its own database.
	//
	// EVERY declaration goes. It used to send the public file alone and print a
	// note naming the siblings — but a plan narrower than the project is a plan
	// somebody reads as complete, and the note was the admission that it was not.
	fmt.Fprintln(out, "schema")
	sources, err := ReadSchemaSources(dir)
	switch {
	case errors.Is(err, ErrNoSchema):
		fmt.Fprintf(out, "  no %s — this project declares no tables\n", PublicSchemaFile)
	case err != nil:
		return err
	default:
		payload, err := SchemaSourcesBody(sources)
		if err != nil {
			return err
		}
		status, body, err := managementCall(ctx, target, cred, http.MethodPost,
			"/v1/management/schema/plan", payload, "application/json")
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

// writeImagePlan says what the push will do to the pod — and only when it will
// do something (FR-017/018).
//
// THE IMAGE TAG IS THE SDK VERSION. Nothing else moves it: not a release of the
// platform, not an operator, not a schedule. So the one moment a tenant's pod is
// replaced is the moment they change the version in their own package.json, and
// this is the line that says so before it happens.
//
// A WARNING, NOT AN ALARM. The swap goes through the holder route: requests wait
// and none of them fail. Saying "the pod will restart" without saying that would
// read as an outage nobody is having.
//
// AND NOT NECESSARILY DURING THIS PUSH. The push carries the bundle to the
// project's own management surface; the image is the plane's to change, and it
// does so on its reconciliation round (within five minutes). Measured live
// 2026-09-05 against the control plane itself: `plan` printed this line, the
// push landed hot, and the pod was replaced afterwards. Saying "the pod is
// replaced" full stop would promise a synchronous swap the push does not make.
//
// SILENT ON AN UNKNOWN CURRENT. The plane does not always answer with a running
// version, and comparing "" against a real one is not a change — it is a missing
// fact. Printing "→ 33.0.2" out of that would invent a migration that may not be
// happening.
func writeImagePlan(w io.Writer, current, target string) {
	if current == "" || current == target {
		return
	}
	fmt.Fprintf(w, "image\n  %s → %s   (the pod is replaced when the plane picks this up; requests wait in the holder, none fail)\n",
		current, target)
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
		NonNull *int64 `json:"non_null"`
	} `json:"destructive"`
	Unsupported  []string `json:"unsupported"`
	Incompatible []string `json:"incompatible"`
}

func renderSchemaPlan(out io.Writer, body []byte) {
	var plan schemaPlanWire
	if err := json.Unmarshal(body, &plan); err != nil {
		fmt.Fprintf(out, "  (unreadable plan: %s)\n", trimBody(body))
		return
	}
	if plan.InSync && len(plan.Changes) == 0 && len(plan.Destructive) == 0 &&
		len(plan.Unsupported) == 0 && len(plan.Incompatible) == 0 {
		fmt.Fprintln(out, "  in sync")
		return
	}
	for _, change := range plan.Changes {
		fmt.Fprintf(out, "  %s\n", change)
	}
	// The server's formatted changes already describe drops and which require
	// approval. Printing the structured list again both duplicates them and
	// incorrectly labels empty columns/tables as requiring data-loss approval.
	if len(plan.Changes) == 0 {
		for _, drop := range plan.Destructive {
			needsApproval := drop.Rows > 0
			if drop.Column != "" {
				if drop.NonNull != nil {
					needsApproval = *drop.NonNull > 0
					fmt.Fprintf(out, "  drop %s.%s — %d value(s) in %d row(s)",
						drop.Table, drop.Column, *drop.NonNull, drop.Rows)
				} else {
					fmt.Fprintf(out, "  drop %s.%s — %d row(s), value count unknown",
						drop.Table, drop.Column, drop.Rows)
				}
			} else {
				fmt.Fprintf(out, "  drop table %s — %d row(s)", drop.Table, drop.Rows)
			}
			if needsApproval {
				fmt.Fprint(out, " — needs --approve")
			}
			fmt.Fprintln(out)
		}
	}
	for _, item := range plan.Unsupported {
		fmt.Fprintf(out, "  not applied by this rail: %s\n", item)
	}
	if len(plan.Incompatible) > 0 {
		fmt.Fprintln(out, "  push blocked while the current release is serving:")
		for _, reason := range plan.Incompatible {
			fmt.Fprintf(out, "    %s\n", reason)
		}
		fmt.Fprintln(out, "  --approve does not bypass release compatibility; deploy a compatible transition first")
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
