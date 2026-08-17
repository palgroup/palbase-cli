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

// statusOfProject reports on the project this checkout is linked to. It returns
// false when there is no linked project, so the caller can fall through to the
// cloud arm.
func statusOfProject(cmd *cobra.Command) (bool, error) {
	target, err := ReadTarget()
	if err != nil {
		return false, nil
	}
	cred, source, err := Credential(target.URL)
	if err != nil {
		return true, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", target.Describe())

	out := cmd.OutOrStdout()
	ctx := cmd.Context()

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

	reportKeyDrift(ctx, target, cred, out)
	reportCommittedDrift(out)
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
