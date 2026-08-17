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
	"sort"
	"strings"

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
	if err := buildStackArtifact(ctx, dir, indent(out)); err != nil {
		return err
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

	// CONFIG. There is no read-back — the target has no route that reports what
	// it currently holds — so this says what would be SENT, not what would
	// change. The wording matters: "would apply" was read as a promise, and for
	// a while it was not even true that they were applied at all.
	fmt.Fprintln(out, "config")
	kinds, err := declaredConfigKinds(dir)
	if err != nil {
		return err
	}
	if len(kinds) == 0 {
		fmt.Fprintln(out, "  nothing declared")
	} else {
		fmt.Fprintf(out, "  would send: %s\n", strings.Join(kinds, ", "))
		fmt.Fprintln(out, "  (what each one changes is reported by the push itself)")
	}

	// SECRETS: names only, always.
	fmt.Fprintln(out, "secrets")
	gap, err := planSecrets(ctx, dir, target, cred)
	if err != nil {
		return err
	}
	renderSecretPlan(out, gap)
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

// secretPlan is what a push would do to the target's secrets.
type secretPlan struct {
	// Fill is declared, absent at the target, and available here.
	Fill []string
	// Present is declared and already set at the target — left alone.
	Present []string
	// MissingRequired is declared `required`, absent at the target, and NOT
	// available here. This is the one that stops a push.
	MissingRequired []string
	// MissingOptional is the same, without the stop.
	MissingOptional []string
}

// planSecrets works out the gap between what the code declares and what the
// target holds — by NAME, on both sides, with no value read except from the
// local stack when there is one to fill from.
func planSecrets(ctx context.Context, dir string, target Target, cred Credentials) (secretPlan, error) {
	declared, err := declaredSecrets(dir)
	if err != nil || len(declared) == 0 {
		return secretPlan{}, err
	}

	held := map[string]bool{}
	names, err := secretNames(ctx, target, cred)
	if err != nil {
		return secretPlan{}, err
	}
	for _, name := range names {
		held[name] = true
	}

	// The SOURCE of a value is the stack running on this machine. There is no
	// other: a value cannot come from a file (there are none) and cannot be
	// typed at a prompt (a push is not an interview).
	available := map[string]bool{}
	if source, sourceCred, ok := localSource(target); ok {
		if localNames, err := secretNames(ctx, source, sourceCred); err == nil {
			for _, name := range localNames {
				available[name] = true
			}
		}
	}

	var plan secretPlan
	for _, decl := range declared {
		switch {
		case held[decl.Name]:
			plan.Present = append(plan.Present, decl.Name)
		case available[decl.Name]:
			plan.Fill = append(plan.Fill, decl.Name)
		case decl.Required:
			plan.MissingRequired = append(plan.MissingRequired, decl.Name)
		default:
			plan.MissingOptional = append(plan.MissingOptional, decl.Name)
		}
	}
	sort.Strings(plan.Fill)
	sort.Strings(plan.Present)
	sort.Strings(plan.MissingRequired)
	sort.Strings(plan.MissingOptional)
	return plan, nil
}

// localSource is the stack on this machine, when it is not itself the target.
func localSource(target Target) (Target, Credentials, bool) {
	local, err := ReadTarget()
	if err != nil || !local.Local || local.URL == target.URL {
		return Target{}, Credentials{}, false
	}
	cred, _, err := Credential(local.URL)
	if err != nil {
		return Target{}, Credentials{}, false
	}
	return local, cred, true
}

func renderSecretPlan(out io.Writer, plan secretPlan) {
	if len(plan.Fill)+len(plan.Present)+len(plan.MissingRequired)+len(plan.MissingOptional) == 0 {
		fmt.Fprintln(out, "  nothing declared")
		return
	}
	for _, name := range plan.Fill {
		fmt.Fprintf(out, "  set     %s\n", name)
	}
	for _, name := range plan.Present {
		// Skipped rather than overwritten, and said out loud: a push that
		// silently replaced a production credential with a development one is
		// the failure this line exists to make impossible to miss.
		fmt.Fprintf(out, "  skip    %s (already set there — --approve replaces it)\n", name)
	}
	for _, name := range plan.MissingOptional {
		fmt.Fprintf(out, "  absent  %s (optional)\n", name)
	}
	for _, name := range plan.MissingRequired {
		fmt.Fprintf(out, "  ✗ %s is required and set nowhere\n", name)
	}
}

// declaredSecrets reads config/secrets.ts as EVALUATED by the build.
//
// The evaluated document rather than the TypeScript: `palbase build` already ran
// the declaration through bun, and a second reader here — a regex over source —
// would disagree with it the first time somebody computes a name.
func declaredSecrets(dir string) ([]secretDeclaration, error) {
	doc, err := readEvaluatedConfig(dir)
	if err != nil || doc == nil || doc.Secrets == nil {
		return nil, err
	}
	return doc.Secrets.Secrets, nil
}

func declaredConfigKinds(dir string) ([]string, error) {
	doc, err := readEvaluatedConfig(dir)
	if err != nil || doc == nil {
		return nil, err
	}
	var kinds []string
	for name, present := range map[string]bool{
		"flags":         doc.Flags != nil,
		"notifications": doc.Notifications != nil,
		"storage":       doc.Storage != nil,
		"egress":        doc.Egress != nil,
		"test-users":    doc.TestUsers != nil,
		"auth":          doc.Auth != nil,
		"secrets":       doc.Secrets != nil,
	} {
		if present {
			kinds = append(kinds, name)
		}
	}
	sort.Strings(kinds)
	return kinds, nil
}

// evaluatedConfig mirrors the stack's ConfigDocument — the shape
// `.palbase/config.json` carries. Only the fields this side reads are named:
// the rest travel in the tarball and are the target's business.
type evaluatedConfig struct {
	Flags         json.RawMessage  `json:"flags"`
	Notifications json.RawMessage  `json:"notifications"`
	Storage       json.RawMessage  `json:"storage"`
	Egress        json.RawMessage  `json:"egress"`
	TestUsers     json.RawMessage  `json:"testusers"`
	Auth          json.RawMessage  `json:"auth"`
	Secrets       *secretsDocument `json:"secrets"`
}

type secretsDocument struct {
	Secrets []secretDeclaration `json:"secrets"`
}

type secretDeclaration struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

func readEvaluatedConfig(dir string) (*evaluatedConfig, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ".palbase", "config.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc evaluatedConfig
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("read .palbase/config.json: %w", err)
	}
	return &doc, nil
}

// indent wraps a writer so a nested command's output reads as detail under the
// heading above it.
func indent(w io.Writer) io.Writer { return &prefixed{w: w, prefix: "  "} }
