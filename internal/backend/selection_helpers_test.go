package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// Compile-time guards: the real mgmt client must satisfy BOTH the deploy client
// (PostMultipart with a context + an Idempotency-Key) and the backend REST
// accessor main.go wires in. A signature drift breaks the build HERE, not at the
// wiring site.
var (
	_ deployClient = (*transport.Client)(nil)
	_ REST         = (*transport.Client)(nil)
)

// rig is the standard backend-test wiring: the fake v2 Management API + a stub
// Studio tRPC server, with proj_1 / production selected in a fresh cwd.
type rig struct {
	Fake     *selectiontest.Fake
	Dir      string
	Studio   *studio.Client
	Resolver *selection.Resolver
	// trpc records the tRPC procedure paths the command called, in order.
	trpc []string
}

// newRig starts everything. `trpcHandler` answers the tRPC calls (apikey.reveal,
// backend.status, ...); a nil handler answers every procedure with `{}`.
func newRig(t *testing.T, trpcHandler http.HandlerFunc) *rig {
	t.Helper()
	r := &rig{
		Fake: selectiontest.New(t),
		Dir:  selectiontest.Chdir(t),
	}
	selectiontest.WriteConfig(t, r.Dir, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.trpc = append(r.trpc, req.URL.Path)
		if trpcHandler != nil {
			trpcHandler(w, req)
			return
		}
		trpcOK(w, map[string]any{})
	}))
	t.Cleanup(srv.Close)
	r.Studio = studio.New(
		srv.URL,
		func(context.Context) (string, error) { return "tok", nil },
		func(context.Context, string, string, string) (string, error) { return "proof", nil },
	)
	r.Resolver = r.Fake.Resolver()
	return r
}

// Resolvers wires the rig into the backend command tree.
func (r *rig) Resolvers() Resolvers {
	rest := r.Fake.REST()
	return Resolvers{
		Studio:    func() *studio.Client { return r.Studio },
		REST:      func() REST { return rest },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
		Selection: func() *selection.Resolver { return r.Resolver },
	}
}

// Run executes one command from the flat top-level set and returns its stdout.
func (r *rig) Run(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	for _, c := range Commands(r.Resolvers()) {
		if c.Name() != name {
			continue
		}
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(args)
		c.SilenceErrors, c.SilenceUsage = true, true
		err := c.Execute()
		return out.String(), err
	}
	t.Fatalf("no top-level command %q", name)
	return "", nil
}

// TRPCCalls returns the tRPC procedure paths the command hit.
func (r *rig) TRPCCalls() []string { return r.trpc }

// trpcOK writes a tRPC-shaped success body.
func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// requireNoV1 asserts that nothing the command did rode a retired path. This is
// the anti-silent-404 guard: a leftover /api/v1 or /branches call 404s on the
// fake, but a command that swallows the error would still "pass" its behaviour
// test — so we assert on the WIRE, not the outcome.
func requireNoV1(t *testing.T, f *selectiontest.Fake) {
	t.Helper()
	for _, route := range f.Routes() {
		require.NotContains(t, route, "/api/v1/", "the CLI must not call v1 (admin excepted, and admin is another package)")
		require.NotContains(t, route, "/branches", "the Palbase branch is gone as a resource")
		require.NotContains(t, route, "/groups/", "groups are gone — apps and members hang off the PROJECT")
	}
}
