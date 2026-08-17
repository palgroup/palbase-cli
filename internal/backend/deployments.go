package backend

// deployments.go — `palbase deploys` and `palbase rollback` on a linked project.
//
// A rollback is not a deploy. The artifact is already on disk and already
// verified, so going back moves a pointer and nothing else — which is why it
// takes a DIGEST rather than a bundle: you can only return to something that was
// actually here. That distinction is the reason this is worth having at all. The
// alternative, "check out the old commit and push again", rebuilds, re-resolves
// dependencies and re-applies a schema, at the moment somebody is least able to
// afford a surprise.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type projectDeployment struct {
	Digest string `json:"digest"`
	// EndpointCount is present ONLY when the runtime confirmed it is answering
	// from this digest. Absent means "not confirmed", which is why it is a
	// pointer: a zero here would read as "this release served nothing".
	EndpointCount *int      `json:"endpoint_count"`
	ActivatedAt   time.Time `json:"activated_at"`
	SDKVersion    string    `json:"sdk_version"`
	Active        bool      `json:"active"`
	// ServingDigest is what the RUNTIME says it is answering from, which for a
	// few seconds after any activation is not what the pointer says. Empty means
	// it could not be asked — a third state, and not the same as "behind".
	ServingDigest string `json:"serving_digest"`
}

// deploysOfProject lists what a linked project has deployed. Returns false when
// there is no linked project, so the caller falls through to the cloud arm.
func deploysOfProject(cmd *cobra.Command) (bool, error) {
	target, cred, ok, err := openLinked(cmd)
	if !ok || err != nil {
		return ok, err
	}

	ctx := cmd.Context()
	deployments, err := listProjectDeployments(ctx, target, cred)
	if err != nil {
		return true, err
	}
	out := cmd.OutOrStdout()
	if len(deployments) == 0 {
		// Same rule `status` follows: a stack started here serves this directory
		// and never activates an artifact, so an empty history is its permanent
		// and correct state — and `palbase push` refuses on exactly this target.
		// Advising a command the same binary rejects a second later is how a tool
		// teaches people to stop reading its advice.
		if target.Local {
			fmt.Fprintln(out, "no deploy history — this stack serves this directory, and rebuilds when you save")
		} else {
			fmt.Fprintln(out, "nothing deployed yet — `palbase push`")
		}
		return true, nil
	}

	// NO ENDPOINTS COLUMN. A manifest never recorded a count, so only the row the
	// runtime confirms can carry one — every other line was a dash, and a column
	// of dashes is a column that teaches people to ignore it. The serving row's
	// count goes beside its marker instead, where it means something.
	table := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(table, "\tVERSION\tACTIVATED\tSDK")
	for _, d := range deployments {
		marker := " "
		if d.Active {
			// The only thing a person is looking for in this list.
			marker = "▸"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n",
			marker, short(d.Digest), d.ActivatedAt.Local().Format("2006-01-02 15:04"),
			orDash(d.SDKVersion))
	}
	if err := table.Flush(); err != nil {
		return true, err
	}
	// The count comes from `current`, not from the history: a manifest never
	// recorded one, so the listing cannot carry it, and only the runtime can say
	// what it is answering with. One extra call, and it is the line a person
	// reads this list for.
	if status, body, err := managementCall(ctx, target, cred, http.MethodGet,
		"/v1/management/deployments/current", nil, ""); err == nil && status == http.StatusOK {
		var current projectDeployment
		if json.Unmarshal(body, &current) == nil && current.EndpointCount != nil {
			fmt.Fprintf(out, "\n▸ %s is serving %d endpoint(s)\n",
				short(current.ServingDigest), *current.EndpointCount)
		}
	}
	fmt.Fprintf(out, "\n`palbase rollback %s` serves that version again\n", short(deployments[len(deployments)-1].Digest))
	return true, nil
}

// rollbackOnProject activates a version the project already has.
func rollbackOnProject(cmd *cobra.Command, digest string) (bool, error) {
	target, cred, ok, err := openLinked(cmd)
	if !ok || err != nil {
		return ok, err
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	// A short digest is what the listing PRINTS, so it has to be what this
	// takes: making somebody paste 64 characters they cannot verify by eye is
	// how the wrong version gets activated.
	deployments, err := listProjectDeployments(ctx, target, cred)
	if err != nil {
		return true, err
	}
	full, err := resolveDigest(digest, deployments)
	if err != nil {
		return true, err
	}

	// The project's activate now WAITS for the runtime to confirm it is
	// answering from this digest, and returns the count it observed — so there
	// is nothing to poll for here and nothing to attribute by guesswork. It
	// blocks for about one runtime recheck interval (~10s measured).
	status, body, err := managementCall(ctx, target, cred, http.MethodPost,
		"/v1/management/deployments/"+full+"/activate", nil, "")
	if err != nil {
		return true, err
	}
	switch status {
	case http.StatusOK, http.StatusNoContent:
	case http.StatusNotFound:
		return true, fmt.Errorf("%s has no version %s", target.Describe(), short(full))
	case http.StatusUnprocessableEntity:
		// The pointer moved and the runtime never came, or came and served
		// nothing. Refused rather than reported as a rollback, because the state
		// this leaves is the one a rollback was trying to escape.
		return true, fmt.Errorf("%s: %s", short(full), describeError(body))
	default:
		return true, fmt.Errorf("%s answered %d: %s", target.Describe(), status, trimBody(body))
	}

	fmt.Fprintf(out, "▸ %s is active\n", short(full))
	var confirmed projectDeployment
	if json.Unmarshal(body, &confirmed) == nil && confirmed.EndpointCount != nil {
		fmt.Fprintf(out, "  serving %d endpoint(s)\n", *confirmed.EndpointCount)
	}
	return true, nil
}

// describeError reads the stack's error envelope, falling back to the body.
func describeError(body []byte) string {
	var env struct {
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &env) == nil && env.Description != "" {
		return env.Description
	}
	return trimBody(body)
}

// resolveDigest turns what a person typed into the digest the project knows.
func resolveDigest(typed string, deployments []projectDeployment) (string, error) {
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return "", fmt.Errorf("which version? `palbase deploys` lists them")
	}
	var matches []string
	for _, d := range deployments {
		if strings.HasPrefix(d.Digest, typed) {
			matches = append(matches, d.Digest)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		var known []string
		for _, d := range deployments {
			known = append(known, short(d.Digest))
		}
		if len(known) == 0 {
			return "", fmt.Errorf("this project has deployed nothing to go back to")
		}
		return "", fmt.Errorf("no version starts with %s — this project has %s", typed, strings.Join(known, ", "))
	default:
		return "", fmt.Errorf("%s matches %d versions — give more of it", typed, len(matches))
	}
}

func listProjectDeployments(ctx context.Context, target Target, cred Credentials) ([]projectDeployment, error) {
	status, body, err := managementCall(ctx, target, cred, http.MethodGet,
		"/v1/management/deployments?limit=20", nil, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d: %s", target.Describe(), status, trimBody(body))
	}
	var answer struct {
		Deployments []projectDeployment `json:"deployments"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("read the history: %w", err)
	}
	return answer.Deployments, nil
}

// openLinked resolves the target and credential these two verbs share, and
// announces where they are acting.
func openLinked(cmd *cobra.Command) (Target, Credentials, bool, error) {
	target, err := ReadTarget()
	if err != nil {
		return Target{}, Credentials{}, false, nil
	}
	cred, _, err := Credential(target.URL)
	if err != nil {
		return Target{}, Credentials{}, true, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", target.Describe())
	return target, cred, true, nil
}

func short(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
