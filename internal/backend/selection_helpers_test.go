package backend

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// Compile-time guards: the real mgmt client must satisfy BOTH the deploy client
// (a context + an Idempotency-Key) and the backend REST
// accessor main.go wires in. A signature drift breaks the build HERE, not at the
// wiring site.
var (
	_ deployClient = (*transport.Client)(nil)
	_ REST         = (*transport.Client)(nil)
)

// rig is the standard backend-test wiring: the fake v2 Management API, with
// proj_1 / production selected in a fresh cwd.
//
// It used to carry a stub Studio tRPC server as well, because half these verbs
// had a second arm that spoke tRPC. They do not: every one reads either the
// project's own management surface or the plane's REST API, both of which the
// fake serves.
type rig struct {
	Fake     *selectiontest.Fake
	Dir      string
	RESTC    REST
	Resolver *selection.Resolver
}

// newRig starts everything.
func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		Fake: selectiontest.New(t),
		Dir:  selectiontest.Chdir(t),
	}
	selectiontest.WriteConfig(t, r.Dir, nil)
	r.Resolver = r.Fake.Resolver()
	r.RESTC = r.Fake.REST()
	// ORTAMIN YAYINLANABİLİR ANAHTARI ARTIK YÖNETİM API'SİNDE.
	//
	// Her rig'e varsayılan olarak kuruluyor çünkü bu yüzey artık standart:
	// `link`, `spec` ve `use` hepsi onu okuyor. Anahtar ref'e BAĞLI olmak
	// zorunda — istemci bağı doğruluyor ve bağsız bir anahtarı reddediyor.
	r.Fake.OK("GET /api/v2/environments/app1prod/apikey", map[string]any{
		"environment_ref": "app1prod",
		"publishable_key": "pb_app1prod_c01234567890123456789",
	})
	// Sözleşme de aynı yüzeyden: tenant belgeyi yalnız `service_role`'a sunuyor.
	for _, ref := range []string{"app1prod", "app1stg"} {
		r.Fake.OK("GET /api/v2/environments/"+ref+"/openapi", map[string]any{
			"openapi": "3.1.0", "paths": map[string]any{},
		})
	}
	return r
}

// Resolvers wires the rig into the backend command tree.
func (r *rig) Resolvers() Resolvers {
	rest := r.RESTC
	return Resolvers{
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
		require.NotContains(t, route, "/api/trpc/", "tRPC is gone — every verb speaks REST")
	}
}

func TestLookupBackendTarget_RejectsAnUnboundPublishableKey(t *testing.T) {
	// Anahtar artık YÖNETİM API'sinden okunuyor, Studio'nun tRPC yüzeyinden
	// değil: headless bir koşumda DPoP'la bağlanmış tarayıcı oturumu yok ve
	// komut o yolda "acquire token: refresh tokens: 401" ile ölüyordu.
	r := newRig(t)
	r.Fake.OK("GET /api/v2/environments/app1prod/apikey", map[string]any{
		"environment_ref": "app1prod",
		"publishable_key": "pb_app1stg_c01234567890123456789",
	})

	target, err := lookupBackendTarget(
		context.Background(), r.RESTC,
		config.Endpoints{PublicHost: "dev.palbase.studio"}, "app1prod",
	)
	require.ErrorContains(t, err, "publishable")
	require.Empty(t, target)
}

func TestLookupBackendTarget_RejectsInvalidRefBeforeStudioRequest(t *testing.T) {
	r := newRig(t)

	_, err := lookupBackendTarget(
		context.Background(), r.RESTC,
		config.Endpoints{PublicHost: "dev.palbase.studio"}, "bad-ref",
	)
	require.ErrorContains(t, err, "environment ref")
	require.Empty(t, r.Fake.Requests(), "an invalid runtime ref must fail before a network request")
}
