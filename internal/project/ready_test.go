package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// statusAnswers answers the status route in order, repeating the last answer.
// An answer is a reply document or an error.
type statusAnswers struct {
	answers []any
	calls   int
}

func (s *statusAnswers) Do(_ context.Context, method, path string, _ any, out any) error {
	s.calls++
	if method != http.MethodGet || path != "/v1/cloud/projects/abc123xyz" {
		return fmt.Errorf("unexpected %s %s", method, path)
	}
	i := s.calls - 1
	if i >= len(s.answers) {
		i = len(s.answers) - 1
	}
	if err, ok := s.answers[i].(error); ok {
		return err
	}
	raw, err := json.Marshal(s.answers[i])
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func shortReadiness(t *testing.T, budget time.Duration) {
	t.Helper()
	prevBudget, prevPoll := ReadyBudget, ReadyPoll
	ReadyBudget, ReadyPoll = budget, time.Millisecond
	t.Cleanup(func() { ReadyBudget, ReadyPoll = prevBudget, prevPoll })
}

func TestWaitReturnsOnceTheEnvironmentAnswers(t *testing.T) {
	shortReadiness(t, time.Second)
	rest := &statusAnswers{answers: []any{
		map[string]any{"reachable": false},
		map[string]any{"reachable": false},
		map[string]any{"reachable": true},
	}}
	var progress bytes.Buffer
	if err := WaitUntilReachable(context.Background(), rest, "abc123xyz", &progress); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if rest.calls != 3 {
		t.Fatalf("asked %d times, want 3", rest.calls)
	}
	if !strings.Contains(progress.String(), "waiting for abc123xyz to answer") {
		t.Fatalf("the wait was not announced:\n%s", progress.String())
	}
}

func TestWaitGivesUpAtTheBudgetAndSaysTheProjectExists(t *testing.T) {
	shortReadiness(t, 20*time.Millisecond)
	rest := &statusAnswers{answers: []any{map[string]any{"reachable": false}}}
	err := WaitUntilReachable(context.Background(), rest, "abc123xyz", io.Discard)
	if !errors.Is(err, ErrNotReachable) {
		t.Fatalf("want ErrNotReachable, got %v", err)
	}
	for _, want := range []string{"abc123xyz was created", "palbase project status abc123xyz"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q missing from: %v", want, err)
		}
	}
}

// A FAILED REQUEST IS NOT "NOT YET" (NFR-003). Asking again after an error is a
// retry, and a retry is what hides a cause.
func TestWaitStopsAtTheFirstFailedRequest(t *testing.T) {
	shortReadiness(t, time.Second)
	rest := &statusAnswers{answers: []any{errors.New("503 upstream unavailable")}}
	err := WaitUntilReachable(context.Background(), rest, "abc123xyz", io.Discard)
	if err == nil {
		t.Fatal("a failed request passed as an answer")
	}
	for _, want := range []string{"503 upstream unavailable", "abc123xyz was created"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q missing from: %v", want, err)
		}
	}
	if rest.calls != 1 {
		t.Fatalf("a failed request was asked again: %d calls", rest.calls)
	}
}
