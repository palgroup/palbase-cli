package backend

// status_project.go — `palbase status` for a project you are linked to.
//
// The question it answers is "is what I am holding the same as what is running",
// and it is worth its own command because every wrong answer looks like
// something else. A stale publishable key looks like a broken login. A client
// generated two deploys ago looks like a backend bug. A local stack that was
// stopped this morning looks like a network problem.
//
// So it reports three things it can actually check: what the project is serving,
// whether the key this app ships still matches the project's, and whether the
// local stack this checkout was pointed at is still up.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type deploymentState struct {
	Digest        string    `json:"digest"`
	EndpointCount *int      `json:"endpoint_count"`
	ActivatedAt   time.Time `json:"activated_at"`
	SDKVersion    string    `json:"sdk_version"`
}

// statusJSON is `palbase status --json`: what this command can actually check,
// in the shape a script can read.
//
// It names the ADDRESS rather than a project and environment id pair. Those ids
// were the cloud arm's vocabulary, and that arm is gone; what a status answer
// has to make unambiguous is WHICH RUNTIME was looked at (UAT CLI-005), and the
// address is exactly that — for a project on this machine as much as for one in
// the cloud.
type statusJSON struct {
	Project    string           `json:"project"`
	Address    string           `json:"address"`
	Credential statusCredential `json:"credential"`
	// Deployed is nil when nothing has been pushed, or when this stack serves the
	// directory and never follows the deploy pointer. Reason says which.
	Deployed *deploymentState `json:"deployed"`
	Reason   string           `json:"reason,omitempty"`
	// AppKey is "current", "stale" or "unchecked" — the drift the text output
	// warns about, in one word a script can branch on.
	AppKey string `json:"app_key,omitempty"`
	// LastAttempt is the newest row in the PLANE's push ledger, which is a
	// different fact from Deployed: a push that never reached the project is not
	// in the project's history, and that is exactly the failure worth seeing.
	LastAttempt *deployRow `json:"last_attempt,omitempty"`
}

type statusCredential struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
}

// statusOfProject reports on the project this verb acts on: the one this
// checkout is linked to, or the one the caller selected.
//
// The bool it returns used to mean "I handled it" — false sent the caller to a
// second arm that asked the Studio the same question over tRPC and rendered a
// different shape. There is one arm now; the bool is kept only so the call site
// reads the same as the other target-relative verbs.
func statusOfProject(cmd *cobra.Command, r Resolvers, jsonOut bool) (bool, error) {
	ctx := cmd.Context()
	target, err := ResolveTarget(ctx)
	if err != nil {
		return true, err
	}
	cred, source, err := Credential(target.URL)
	if err != nil {
		return true, err
	}
	if jsonOut {
		return true, statusAsJSON(ctx, cmd, r, target, cred, string(source))
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", target.Describe())

	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "project:      %s\n", target.Describe())
	fmt.Fprintf(out, "address:      %s\n", target.URL)
	fmt.Fprintf(out, "credential:   %s (%s)\n", source, credentialKindWord(cred.Kind))

	// deployments/current, not `deployment`. The singular does not exist, and a
	// 404 from a route that is not there was being read as "you have not deployed
	// yet" — so `palbase status` said "nothing yet" about a project that had 37
	// endpoints live, in the same second `palbase deploys` listed them.
	status, body, err := managementCall(ctx, target, cred, http.MethodGet,
		"/v1/management/deployments/current", nil, "")
	switch {
	case err != nil:
		return true, err
	case status == http.StatusNotFound && target.Local:
		// A stack started HERE serves this directory and never follows the deploy
		// pointer, so "no artifact" is the permanent, correct answer rather than a
		// state to get out of. The generic line below sent people to `palbase
		// push`, which refuses on exactly this target — status was advising a
		// command its own CLI rejects a second later.
		fmt.Fprintln(out, "deployed:     n/a — this stack serves this directory, and rebuilds when you save")
	case status == http.StatusNotFound:
		fmt.Fprintln(out, "deployed:     nothing yet — `palbase push`")
	case status != http.StatusOK:
		return true, fmt.Errorf("%s answered %d: %s", target.Describe(), status, trimBody(body))
	default:
		var deployed deploymentState
		if err := json.Unmarshal(body, &deployed); err != nil {
			return true, fmt.Errorf("read the deployment: %w", err)
		}
		digest := deployed.Digest
		if len(digest) > 12 {
			digest = digest[:12]
		}
		fmt.Fprintf(out, "deployed:     %s", digest)
		if deployed.EndpointCount != nil {
			fmt.Fprintf(out, ", %d endpoint(s)", *deployed.EndpointCount)
		}
		if deployed.SDKVersion != "" {
			fmt.Fprintf(out, ", SDK %s", deployed.SDKVersion)
		}
		fmt.Fprintf(out, "\n              activated %s\n", deployed.ActivatedAt.Local().Format("2006-01-02 15:04"))
	}

	if attempt := lastPushAttempt(ctx, r); attempt != nil {
		if line := formatLastDeploy(&lastDeploy{
			Status:    attempt.Status,
			Error:     attempt.Error,
			Version:   attempt.Version,
			UpdatedAt: &attempt.CreatedAt,
		}, time.Now()); line != "" {
			fmt.Fprint(out, line)
		}
	}

	reportKeyDrift(ctx, target, cred, out)
	reportCommittedDrift(out)

	// The same warning `link` prints, repeated where somebody looks when the app
	// is behaving oddly. It is idempotent and silent once the key is there.
	if envs, err := readAppEnvironments("ios"); err == nil && len(envs.Environments) > 0 {
		if root, err := os.Getwd(); err == nil {
			reportInfoPlistRequirement(root, envs, out)
		}
	}
	return true, nil
}

// reportCommittedDrift says which endpoints the environments this app holds do
// not agree on, from the contracts already on disk.
//
// It asks no environment anything: the contracts were fetched when they were
// linked or refreshed, and reaching production from a laptop to answer a status
// question is a request nobody asked for. What it costs is honesty about
// freshness — this reports the difference between what was FETCHED, which is
// also what the app was built against.
func reportCommittedDrift(out io.Writer) {
	dir := filepath.Join(nativeArtifactsDir, "openapi")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	specs := map[string][]byte{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if e.IsDir() || name == e.Name() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		specs[name] = body
	}
	reportContractDrift(specs, out)
}

func credentialKindWord(kind Kind) string {
	if kind == KindKey {
		return "this project's key"
	}
	return "a person"
}

// reportKeyDrift compares the key this app SHIPS with the one the project hands
// out now.
//
// The drift is ordinary and its symptom is not: a rotated publishable key leaves
// every installed build authenticating with something the project no longer
// accepts, and the app reports it as a sign-in failure. Nothing else in this CLI
// would notice, because the committed slot is a file and files do not expire.
func reportKeyDrift(ctx context.Context, target Target, cred Credentials, out io.Writer) {
	envs, err := readAppEnvironments("ios")
	if err != nil || len(envs.Environments) == 0 {
		return
	}
	entry, ok := envs.Environments[envs.Default]
	if !ok || entry.APIKey == "" {
		return
	}

	current, err := projectPublishableKey(ctx, target)
	if err != nil {
		// Cannot check is not the same as checked. Silence here would read as
		// "the key is fine".
		fmt.Fprintf(out, "app key:      could not be checked (%v)\n", err)
		return
	}
	if current == entry.APIKey {
		fmt.Fprintf(out, "app key:      current\n")
		return
	}
	// The keys themselves are not printed — a publishable key is not a secret,
	// but printing two nearly-identical strings invites reading them for the
	// difference instead of running the command that fixes it.
	fmt.Fprintf(out, "app key:      STALE — %s ships a key this project no longer hands out.\n", envs.Default)
	fmt.Fprintln(out, "              Run `palbase link` to refresh it, then rebuild the app.")
}

// statusAsJSON answers the same questions the text output does, without the
// advice: a script cannot follow "run palbase push", and prose inside a JSON
// document would make it unparseable.
func statusAsJSON(ctx context.Context, cmd *cobra.Command, r Resolvers, target Target, cred Credentials, source string) error {
	doc := statusJSON{
		Project:    target.Describe(),
		Address:    target.URL,
		Credential: statusCredential{Source: source, Kind: credentialKindWord(cred.Kind)},
	}

	status, body, err := managementCall(ctx, target, cred, http.MethodGet,
		"/v1/management/deployments/current", nil, "")
	switch {
	case err != nil:
		return err
	case status == http.StatusNotFound && target.Local:
		doc.Reason = "this stack serves this directory, and rebuilds when you save"
	case status == http.StatusNotFound:
		doc.Reason = "nothing deployed yet"
	case status != http.StatusOK:
		return fmt.Errorf("%s answered %d: %s", target.Describe(), status, trimBody(body))
	default:
		var deployed deploymentState
		if err := json.Unmarshal(body, &deployed); err != nil {
			return fmt.Errorf("read the deployment: %w", err)
		}
		doc.Deployed = &deployed
	}

	doc.AppKey = appKeyState(ctx, target)
	doc.LastAttempt = lastPushAttempt(ctx, r)
	fmt.Fprintln(cmd.OutOrStdout(), renderJSON(doc))
	return nil
}

// appKeyState is reportKeyDrift's finding as one word.
//
// "unchecked" covers both "this checkout ships no key" and "the project could
// not be asked" — a script that must not run against a stale key treats them the
// same, and calling either of them "current" is the failure this reports.
func appKeyState(ctx context.Context, target Target) string {
	envs, err := readAppEnvironments("ios")
	if err != nil || len(envs.Environments) == 0 {
		return "unchecked"
	}
	entry, ok := envs.Environments[envs.Default]
	if !ok || entry.APIKey == "" {
		return "unchecked"
	}
	current, err := projectPublishableKey(ctx, target)
	if err != nil {
		return "unchecked"
	}
	if current == entry.APIKey {
		return "current"
	}
	return "stale"
}

// lastPushAttempt is the newest row in the PLANE's push ledger for the selected
// Environment, or nil when there is no selection to ask about.
//
// It is a different question from "what is this project serving", and status has
// to answer both: a push that failed before the artifact ever reached the
// project leaves the project's own history untouched, so reading only that
// history reports a healthy, months-old deploy and says nothing about the three
// failures since. This is the visibility half.
//
// Best-effort by design. A checkout linked straight to an address has no project
// id and therefore no ledger to read, and a plane that cannot be reached is not
// a reason to refuse a status about the project in front of you — so every
// failure here is silence, and the deployment block above still answers.
func lastPushAttempt(ctx context.Context, r Resolvers) *deployRow {
	if r.Selection == nil || r.REST == nil {
		return nil
	}
	sel, err := r.resolve(ctx)
	if err != nil || sel.ProjectID == "" || sel.EnvironmentRef() == "" {
		return nil
	}
	var resp struct {
		Deployments []deployRow `json:"deployments"`
	}
	if err := r.REST().Do(ctx, http.MethodGet,
		DeploymentsPath(sel.ProjectID, sel.EnvironmentRef())+"?limit=1", nil, &resp); err != nil {
		return nil
	}
	if len(resp.Deployments) == 0 {
		return nil
	}
	return &resp.Deployments[0]
}
