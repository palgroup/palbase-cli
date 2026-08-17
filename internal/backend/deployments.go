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
	"io"
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

	deployments, err := listProjectDeployments(cmd.Context(), target, cred)
	if err != nil {
		return true, err
	}
	out := cmd.OutOrStdout()
	if len(deployments) == 0 {
		fmt.Fprintln(out, "nothing deployed yet — `palbase push`")
		return true, nil
	}

	table := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(table, "\tVERSION\tACTIVATED\tENDPOINTS\tSDK")
	for _, d := range deployments {
		marker := " "
		if d.Active {
			// The only thing a person is looking for in this list.
			marker = "▸"
		}
		endpoints := "—"
		if d.EndpointCount != nil {
			endpoints = fmt.Sprintf("%d", *d.EndpointCount)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
			marker, short(d.Digest), d.ActivatedAt.Local().Format("2006-01-02 15:04"),
			endpoints, orDash(d.SDKVersion))
	}
	if err := table.Flush(); err != nil {
		return true, err
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

	status, body, err := managementCall(ctx, target, cred, http.MethodPost,
		"/v1/management/deployments/"+full+"/activate", nil, "")
	if err != nil {
		return true, err
	}
	switch status {
	case http.StatusOK, http.StatusNoContent:
	case http.StatusNotFound:
		return true, fmt.Errorf("%s has no version %s", target.Describe(), short(full))
	default:
		return true, fmt.Errorf("%s answered %d: %s", target.Describe(), status, trimBody(body))
	}

	fmt.Fprintf(out, "▸ %s is active\n", short(full))

	// WAIT FOR THE RUNTIME to say it is answering from this digest, then report
	// its count. The pointer moving is not the thing anybody wanted; serving the
	// old artifact is exactly the state a rollback is trying to leave.
	//
	// The project reports `serving_digest` separately from `digest` precisely so
	// this can be told apart — and it omits the count until the two agree, which
	// is why a count here can be attributed at all. Before that (2026-08-17) a
	// rollback answered "serving 37" with the runtime still on the artifact it
	// had just replaced.
	if served, ok := awaitServing(ctx, target, cred, full, out); ok && served != nil {
		fmt.Fprintf(out, "  serving %d endpoint(s)\n", *served)
	}
	return true, nil
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

// awaitServing polls until the runtime confirms it is answering from digest.
//
// Returns (count, true) on confirmation and (nil, false) when the wait ran out —
// which is reported rather than swallowed, because "the pointer moved and
// nothing picked it up" is the failure a rollback most needs to hear about.
func awaitServing(ctx context.Context, target Target, cred Credentials, digest string, out io.Writer) (*int, bool) {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		status, body, err := managementCall(ctx, target, cred, http.MethodGet,
			"/v1/management/deployments/current", nil, "")
		if err == nil && status == http.StatusOK {
			var current projectDeployment
			if json.Unmarshal(body, &current) == nil && current.ServingDigest == digest {
				return current.EndpointCount, true
			}
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(time.Second):
		}
	}
	fmt.Fprintln(out, "  the runtime has not picked it up yet — `palbase status` shows what it is serving")
	return nil, false
}
