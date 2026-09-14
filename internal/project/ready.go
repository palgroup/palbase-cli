package project

// ready.go — a create returns once the environment it made answers.
//
// "Provisioning is synchronous" was the promise, and the plane's `Running`
// phase means placement finished, not that the tenant accepts connections
// (provision.ts). Measured on 0.67.1: create printed "(Running)" and the link
// command, the environment refused connections for minutes, and `palbase plan`
// right after it got a 500. The plane already answers whether an environment
// is reachable; this waits for that answer and nothing else.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ErrNotReachable is the budget running out before the environment answered.
var ErrNotReachable = errors.New("not answering yet")

// ReadyBudget and ReadyPoll bound the wait. Readiness measured about two
// minutes for a new project and about five for an added environment.
var (
	ReadyBudget = 10 * time.Minute
	ReadyPoll   = 5 * time.Second
)

// WaitUntilReachable asks the plane whether ref answers until it says so. A
// failed request ends the wait: an error is not "not yet", and asking again
// after one would be a retry that hides its cause.
func WaitUntilReachable(ctx context.Context, rest REST, ref string, progress io.Writer) error {
	fmt.Fprintf(progress, "waiting for %s to answer…\n", ref)
	path := "/v1/cloud/projects/" + url.PathEscape(ref)
	deadline := time.Now().Add(ReadyBudget)
	for {
		var status struct {
			Reachable bool `json:"reachable"`
		}
		if err := rest.Do(ctx, http.MethodGet, path, nil, &status); err != nil {
			return fmt.Errorf("%s was created, but asking whether it answers failed: %w", ref, err)
		}
		if status.Reachable {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s was created and is %w after %s — `palbase project status %s` shows when it answers",
				ref, ErrNotReachable, ReadyBudget, ref)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s was created; stopped waiting for it to answer: %w", ref, ctx.Err())
		case <-time.After(ReadyPoll):
		}
	}
}
