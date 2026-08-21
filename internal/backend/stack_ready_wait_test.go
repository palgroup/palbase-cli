package backend

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A project that is still coming up answers 503 from the cell's edge — not from
// the stack, and not as a refusal. Measured on a fresh project: the pod reports
// Ready and the public route still 503s for another ~26 seconds, and a first
// push landing in that window failed about one time in five.
//
// These tests drive the wait against a real server rather than a stub, because
// the thing under test is the loop's reading of a real response.

func requestTo(t *testing.T, url string) func() (*http.Request, error) {
	t.Helper()
	return func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("ARTEFAKT")))
	}
}

func TestAProjectStillComingUpIsWaitedFor(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("no healthy upstream"))
			return
		}
		_, _ = w.Write([]byte(`{"digest":"abc"}`))
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	status, body, err := sendWaitingForReady(context.Background(), srv.Client(),
		requestTo(t, srv.URL), &out, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("gave up on a project that came up: %d", status)
	}
	if string(body) != `{"digest":"abc"}` {
		t.Fatalf("the successful body was not returned: %s", body)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected two waits then a success, got %d calls", got)
	}
	// SAID OUT LOUD, ONCE. A silent wait looks like a hang; one line per attempt
	// looks like a fault.
	if n := strings.Count(out.String(), "not serving yet"); n != 1 {
		t.Fatalf("expected the wait to be announced exactly once, got %d", n)
	}
}

// A DECISION IS NOT RETRIED. A 4xx is the stack refusing — an unreadable
// bundle, a rejected key, data it will not overwrite — and asking again would
// only make the same answer slower.
func TestARefusalIsNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusConflict, 422} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		}))

		status, _, err := sendWaitingForReady(context.Background(), srv.Client(),
			requestTo(t, srv.URL), &bytes.Buffer{}, time.Second, time.Millisecond)
		srv.Close()
		if err != nil {
			t.Fatalf("%d: %v", code, err)
		}
		if status != code {
			t.Fatalf("%d: got %d", code, status)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("%d: a refusal was asked again — %d calls", code, got)
		}
	}
}

// The wait is BOUNDED. A project that never comes up must produce the 503 for
// the caller to report, not a loop nobody can end.
func TestAProjectThatNeverComesUpGivesUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	status, _, err := sendWaitingForReady(context.Background(), srv.Client(),
		requestTo(t, srv.URL), &bytes.Buffer{}, 60*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("the 503 was swallowed: %d", status)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the budget was not honoured: waited %s", elapsed)
	}
	if calls.Load() < 2 {
		t.Fatalf("it gave up without waiting at all: %d calls", calls.Load())
	}
}

// EACH ATTEMPT IS A FRESH REQUEST. A retry that reuses a consumed body sends an
// EMPTY tarball, and the stack would answer 422 on a bundle that was fine.
func TestEachAttemptSendsTheWholeBody(t *testing.T) {
	var sizes []int
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		sizes = append(sizes, buf.Len())
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}))
	t.Cleanup(srv.Close)

	if _, _, err := sendWaitingForReady(context.Background(), srv.Client(),
		requestTo(t, srv.URL), &bytes.Buffer{}, time.Second, time.Millisecond); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(sizes) != 2 || sizes[0] != sizes[1] || sizes[0] == 0 {
		t.Fatalf("the retry did not carry the same body: %v", sizes)
	}
}

// A CANCELLED PUSH STOPS. Ctrl-C during the wait must end it, not sit out the
// remaining budget.
func TestCancellingDuringTheWaitStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, _, err := sendWaitingForReady(ctx, srv.Client(),
		requestTo(t, srv.URL), &bytes.Buffer{}, time.Minute, 10*time.Millisecond); err == nil {
		t.Fatal("a cancelled wait reported success")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancelling did not stop the wait: %s", elapsed)
	}
}

// A PROJECT THAT IS STILL COMING UP IS NOT "NOT A PALBASE STACK".
//
// `palbase link` collapsed both into one sentence, and it picked the wrong one:
// somebody who had created a project seconds earlier was told their address
// does not look like Palbase. The two failures call for opposite reactions —
// wait, versus check the address — so they must not share a message.
func TestLinkTellsNotUpYetApartFromNotPalbase(t *testing.T) {
	stillComingUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("no healthy upstream"))
	}))
	t.Cleanup(stillComingUp.Close)

	// A short budget so the test measures the MESSAGE, not the wait.
	restore := stackReadyWait
	stackReadyWait = 30 * time.Millisecond
	t.Cleanup(func() { stackReadyWait = restore })

	_, err := describeStack(context.Background(), stillComingUp.URL, false)
	if err == nil {
		t.Fatal("a project that never came up reported success")
	}
	if strings.Contains(err.Error(), "does not look like a Palbase stack") {
		t.Fatalf("a starting project was called not-Palbase: %v", err)
	}
	if !strings.Contains(err.Error(), "not up yet") {
		t.Fatalf("the message does not say it is still starting: %v", err)
	}

	// AND THE OTHER SENTENCE STILL EXISTS. A test that only checks the new
	// message would pass on an implementation that never says the old one.
	notPalbase := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(notPalbase.Close)

	_, err = describeStack(context.Background(), notPalbase.URL, false)
	if err == nil || !strings.Contains(err.Error(), "does not look like a Palbase stack") {
		t.Fatalf("a non-Palbase address lost its own message: %v", err)
	}
}
