package main

// main_test.go — the gate on FR-009a: a mutation never reaches a tenant that is
// still starting, and the safe request that waits for it goes to the ONE
// endpoint that can say so.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// planeCall is one request the fake control plane received.
type planeCall struct {
	method string
	path   string
	query  string
	at     time.Duration
}

// fakePlane records what the preparer asked and in which order.
type fakePlane struct {
	calls []planeCall
	start time.Time

	// transientProbes is how many GETs answer a named transient before the
	// plane says the project is ready. A negative value never relents.
	transientProbes int
	// postStatus is the status the POST answers with; 0 means 200.
	postStatus int

	probes int
}

// namedTransient is the plane's "not yet" answer, in the SHAPE the plane really
// writes it: a JSON envelope whose `error` is the machine-readable name. A bare
// 503 with a plain-text body is the defect this whole run closes.
const namedTransient = `{"error":"tenant_unreachable","error_description":"the environment is still starting",` +
	`"status":503,"request_id":"req_probe_1"}`

func (p *fakePlane) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.calls = append(p.calls, planeCall{
			method: r.Method, path: r.URL.Path,
			query: r.URL.RawQuery, at: time.Since(p.start),
		})
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			p.probes++
			if p.transientProbes < 0 || p.probes <= p.transientProbes {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(namedTransient))
				return
			}
			_, _ = w.Write([]byte(`{"runtime":{"ready":true}}`))
		case http.MethodPost:
			if p.postStatus != 0 {
				w.WriteHeader(p.postStatus)
				_, _ = w.Write([]byte(namedTransient))
				return
			}
			_, _ = w.Write([]byte(`{"sdkVersion":"1.2.3"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (p *fakePlane) count(method string) int {
	n := 0
	for _, c := range p.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

// wirePlane stands the fake plane up and binds the real wiring to it.
//
// IT BUILDS `resolved` AND CALLS wireCloudKeyFetcher ITSELF — the precedent is
// env_route_test.go, which does the same rather than driving a command: the
// point is what the WIRING does, not that some verb happens to reach it.
//
// THE TENANT ADDRESS MUST SIT UNDER PublicHost or `tenantRefOf` answers false
// and the preparer returns before touching the plane at all — a test that got
// this wrong would measure an empty plane and pass.
func wirePlane(t *testing.T, p *fakePlane) *bytes.Buffer {
	t.Helper()
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	p.start = time.Now()

	t.Setenv("PALBASE_ACCESS_TOKEN", "tok_test_not_a_pat")

	prevResolved, prevAuth := resolved, authClient
	prevPrepare, prevPlanner, prevKeys := backend.CloudRuntimePreparer, backend.CloudRuntimePlanner, backend.CloudKeyFetcher
	prevProgress := runtimeProgress
	resolved = config.Resolved{Endpoints: config.Endpoints{
		PlatformAPI: srv.URL,
		PublicHost:  "palbase.studio",
	}}
	authClient = auth.NewClient(auth.Config{}, &bytes.Buffer{})
	var progress bytes.Buffer
	runtimeProgress = &progress

	// THE WAIT IS SHRUNK, NOT DISABLED: these tests measure the BRANCH, not the
	// clock. Leaving the shipped 240s budget here would make the envelope test
	// take four minutes to prove a boolean.
	prevBudget, prevMin, prevMax := transport.TransientBudget, transport.TransientPollMin, transport.TransientPollMax
	transport.TransientBudget = 300 * time.Millisecond
	transport.TransientPollMin = 5 * time.Millisecond
	transport.TransientPollMax = 20 * time.Millisecond

	t.Cleanup(func() {
		resolved, authClient = prevResolved, prevAuth
		backend.CloudRuntimePreparer, backend.CloudRuntimePlanner, backend.CloudKeyFetcher = prevPrepare, prevPlanner, prevKeys
		runtimeProgress = prevProgress
		transport.TransientBudget, transport.TransientPollMin, transport.TransientPollMax = prevBudget, prevMin, prevMax
	})

	wireCloudKeyFetcher()
	return &progress
}

const tenantUnderPublicHost = "https://app1prod.palbase.studio"

func samplePlan() backend.PlanRef {
	return backend.PlanRef{Running: "1.2.2", Target: "1.2.3", Fingerprint: "fp"}
}

// THE MUTATION WAITS BEHIND A SAFE REQUEST (FR-009a, D-7).
//
// Measured: `palbase push` reads its plan from a FILE, so the first call that
// can meet a starting tenant is the POST — and the transport's method gate
// deliberately never waits on a POST, because two of the plane's paths on these
// names WRITE before they answer. So the verb asks first.
//
// AND THE ENDPOINT IS THE POINT. Of the three GETs this CLI makes, only
// `…/runtime/plan` reaches the cell and can answer a named transient; `…/keys`
// and `…/projects/{ref}` answer 200 while a tenant is still starting. Probing
// either of those would leave this test green and the product broken, so the
// endpoint is asserted, not assumed.
func TestRuntimePrepareWaitsForReadinessBeforeMutating(t *testing.T) {
	plane := &fakePlane{transientProbes: 2}
	progress := wirePlane(t, plane)

	err := backend.CloudRuntimePreparer(context.Background(),
		tenantUnderPublicHost, "1.2.3", samplePlan())
	require.NoError(t, err)

	require.Len(t, plane.calls, 4, "calls: %+v", plane.calls)
	for i, c := range plane.calls[:3] {
		require.Equal(t, http.MethodGet, c.method, "call %d", i)
		require.Equal(t, "/v1/cloud/projects/app1prod/runtime/plan", c.path,
			"the probe went to an endpoint that cannot report a starting tenant")
		require.Contains(t, c.query, "sdk=1.2.3")
	}
	// THE MUTATION CAME LAST, AND ONCE.
	require.Equal(t, http.MethodPost, plane.calls[3].method)
	require.Equal(t, "/v1/cloud/projects/app1prod/runtime", plane.calls[3].path)
	require.Equal(t, 1, plane.count(http.MethodPost))

	// EC-5: a wait nobody announced is a silence in the middle of a push.
	require.Contains(t, progress.String(), "app1prod")
}

// A MUTATION IS NEVER SILENTLY REPEATED (FR-009a, D-7).
//
// The plane writes before it answers on this path, so a POST that came back
// "not yet" may already have taken effect. It is reported, not retried — and it
// is reported in FR-013's sentence rather than as a bare transient: the state's
// name, how long it took, what the person can do, and the request_id that ties
// it to the plane's own logs.
func TestRuntimePrepareDoesNotRetryItsPost(t *testing.T) {
	plane := &fakePlane{postStatus: http.StatusServiceUnavailable}
	wirePlane(t, plane)

	err := backend.CloudRuntimePreparer(context.Background(),
		tenantUnderPublicHost, "1.2.3", samplePlan())
	require.Error(t, err)
	require.Equal(t, 1, plane.count(http.MethodPost), "the mutation was repeated")

	for _, want := range []string{"still starting", "run the same command again", "req_probe_1"} {
		require.Contains(t, err.Error(), want, "FR-013's sentence is incomplete")
	}
	require.NotContains(t, err.Error(), "nothing was changed at all",
		"a POST that may already have taken effect must not claim otherwise")
}

// THE PROBE AND THE MUTATION SHARE ONE ENVELOPE, and the probe must not eat it.
//
// Measured (verifier round 4): both run inside runtime_prepare.go's
// `swapCtx = context.WithTimeout(ctx, 5*time.Minute)`. The transport's transient
// budget is 240s, which fits — but a probe that spends all of it leaves the POST
// the remainder, so the remainder is measured here rather than assumed.
//
// AND A PROBE THAT GIVES UP DOES NOT CANCEL THE PUSH. The probe is a courtesy
// wait, not a gate: refusing here would invent a brand-new way for a push to
// fail that the plane itself never asked for.
func TestProbeAndPostShareTheSwapEnvelope(t *testing.T) {
	plane := &fakePlane{transientProbes: -1} // never relents
	progress := wirePlane(t, plane)

	const envelope = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), envelope)
	defer cancel()

	started := time.Now()
	err := backend.CloudRuntimePreparer(ctx, tenantUnderPublicHost, "1.2.3", samplePlan())
	require.NoError(t, err, "a probe that gave up cancelled the push")

	require.Equal(t, 1, plane.count(http.MethodPost), "the mutation was not sent after the probe gave up")
	require.Greater(t, plane.count(http.MethodGet), 1, "the probe did not wait at all")

	postAt := plane.calls[len(plane.calls)-1].at
	remaining := envelope - postAt
	t.Logf("probe spent %s of the %s envelope; %s remained for the mutation",
		postAt.Round(time.Millisecond), envelope, remaining.Round(time.Millisecond))
	require.Greater(t, remaining, time.Duration(0),
		"the probe consumed the whole envelope and left the mutation none")
	require.Less(t, time.Since(started), envelope)

	require.Contains(t, progress.String(), "app1prod")
}

// The probe is only reached for an address this cloud owns. A foreign address
// is refused before any request leaves — otherwise the probe would ask our
// control plane about somebody else's stack.
func TestAForeignAddressNeverReachesThePlane(t *testing.T) {
	plane := &fakePlane{}
	wirePlane(t, plane)

	err := backend.CloudRuntimePreparer(context.Background(),
		"https://stack.example.com", "1.2.3", samplePlan())
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "not a project on this cloud"))
	require.Empty(t, plane.calls)
}
