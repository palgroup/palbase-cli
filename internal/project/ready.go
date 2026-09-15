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

	"github.com/palgroup/palbase-cli/internal/transport"
)

// ErrNotReachable is the budget running out before the environment answered.
var ErrNotReachable = errors.New("not answering yet")

// ReadyBudget and ReadyPoll bound the wait. Readiness measured about two
// minutes for a new project and about five for an added environment.
var (
	ReadyBudget = 10 * time.Minute
	ReadyPoll   = 5 * time.Second
)

// WaitUntilReachable asks the plane whether ref answers until it says so.
//
// A NAMED TRANSIENT IS "NOT YET"; ANYTHING ELSE STILL ENDS THE WAIT. This file
// used to say the opposite without qualification — "a failed request ends the
// wait: an error is not 'not yet'" — and that sentence is retired here rather
// than left standing next to the code that now contradicts it, because a reader
// who finds the old rule in the doc puts it back into the code.
//
// WHY IT WAS REVERSED: measured 2026-09-15, `palbase project create` against a
// plane in its opening window spent ZERO of this 10-minute budget and fell over
// in 11 milliseconds, because the first answer was an error and an error ended
// the wait. The budget was written but not in force.
//
// WHY THE OLD RULE SURVIVES FOR EVERYTHING ELSE: the transport already waits out
// the plane's named transient answers on safe methods, so an error that reaches
// this loop is one of two things. Either it has no name — a real fault, and
// ending the wait is right — or it is a named transient that outlasted the
// transport's own budget. TWO LAYERS, ONE RULE, DIFFERENT WINDOWS: the transport
// swallows the short window inside a single GET, this loop waits out the long
// one. The classification is transport.IsNamedTransient's to make, not this
// package's: naming the set twice is how two lists drift apart.
//
// THE PRICE IS AN IMPORT, AND IT IS PAID DELIBERATELY. project.go binds these
// commands to the REST interface rather than the concrete client so tests can
// substitute a stub; importing internal/transport here weakens that separation.
// It is still the right trade — the only correct owner of the name is the
// transport that receives it — but the separation is now partial, and that is
// written down rather than discovered later.
//
// AND IT PRINTS. A longer wait is a longer silence, and the transport has no
// writer of its own (giving it one would invert the layering), so visible
// progress is the caller's job: one line per round, so the worst case is about
// ReadyPoll between signs of life.
func WaitUntilReachable(ctx context.Context, rest REST, ref string, progress io.Writer) error {
	started := time.Now()
	fmt.Fprintf(progress, "waiting for %s to answer…\n", ref)
	path := "/v1/cloud/projects/" + url.PathEscape(ref)
	deadline := time.Now().Add(ReadyBudget)
	for {
		var status struct {
			Reachable bool `json:"reachable"`
		}
		err := rest.Do(ctx, http.MethodGet, path, nil, &status)
		if err != nil && !transport.IsNamedTransient(http.MethodGet, err) {
			return fmt.Errorf("%s was created, but asking whether it answers failed: %w", ref, err)
		}
		if err == nil && status.Reachable {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s was created and is %w after %s — `palbase project status %s` shows when it answers",
				ref, ErrNotReachable, ReadyBudget, ref)
		}
		fmt.Fprintf(progress, "  %s — %s\n", time.Since(started).Round(time.Second), waitingOn(err))
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s was created; stopped waiting for it to answer: %w", ref, ctx.Err())
		case <-time.After(ReadyPoll):
		}
	}
}

// waitingOn says which of the two "not yet" answers this round got: the plane
// answered and said the environment does not accept connections, or the plane
// said it is still starting the environment. They read the same from outside
// and they are not the same thing to anyone diagnosing a slow create.
func waitingOn(err error) string {
	if err != nil {
		return "the plane is still starting it"
	}
	return "it does not answer yet"
}
