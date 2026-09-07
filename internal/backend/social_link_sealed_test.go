package backend

// `palbase link --platform ios/android` against a stack that REFUSES plaintext.
//
// Every existing fixture in this package answers 200 to whatever arrives, which
// is why a plaintext GET to `/auth/oauth/config` looked correct here for as long
// as it did: the CLI built a bare request, the fixture answered it, and the only
// thing that ever disagreed was a real stack replying 415 `sealed_required`
// (v2/internal/server/sealed.go:433). This fixture refuses the way a real stack
// refuses, so the link path is measured against the rule rather than around it.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
	"github.com/palgroup/palbase-cli/internal/sealedclient"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/chacha20poly1305"
)

// sealingStack is the server half of the sealed wire: it publishes a
// fleet-signed keyset, refuses an unsealed request under `/auth/`, opens the
// envelope that does arrive and seals its answer under the exporter secret.
type sealingStack struct {
	srv        *httptest.Server
	rootB64    string
	sealedSeen int
	plainSeen  int
}

func newSealingStack(t *testing.T, stackRef, apiKey, snapshot, admin string) *sealingStack {
	t.Helper()
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stackPub, stackPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	kemPub, kemPriv, err := sealedclient.SuiteKEM.Scheme().GenerateKeyPair()
	require.NoError(t, err)
	kemPubRaw, err := kemPub.MarshalBinary()
	require.NoError(t, err)

	binding, err := json.Marshal(sealedclient.Binding{
		V: sealedclient.BindingVersion, StackRef: stackRef,
		SigningKey: base64.StdEncoding.EncodeToString(stackPub),
		Expires:    time.Now().Add(72 * time.Hour).Unix(),
	})
	require.NoError(t, err)
	keyset, err := json.Marshal(sealedclient.Keyset{
		V: sealedclient.KeysetDocumentVersion, Version: 3,
		Expires: time.Now().Add(24 * time.Hour).Unix(),
		Keys: []sealedclient.PublicKeyEntry{{
			Kid: "link-key", Alg: sealedclient.SuiteName,
			Pub: base64.StdEncoding.EncodeToString(kemPubRaw),
		}},
	})
	require.NoError(t, err)
	document, err := json.Marshal(sealedclient.BoundKeyset{
		Binding: &sealedclient.SignedBinding{
			Binding: binding,
			Sig:     base64.StdEncoding.EncodeToString(ed25519.Sign(rootPriv, binding)),
			RootKid: sealedclient.FleetRootKid, Alg: "ed25519",
		},
		SignedKeyset: &sealedclient.SignedKeyset{
			Keyset:  keyset,
			Sig:     base64.StdEncoding.EncodeToString(ed25519.Sign(stackPriv, keyset)),
			RootKid: "stack", Algorithm: "ed25519",
		},
	})
	require.NoError(t, err)

	s := &sealingStack{rootB64: base64.StdEncoding.EncodeToString(rootPub)}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case sealedclient.KeysetPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(document)
			return
		case "/v1/management/auth/social-auth":
			// Not under a sealed prefix, and it must stay plaintext: sealing it
			// would send an envelope to a surface that does not open one.
			require.False(t, sealedclient.IsSealedRequest(r), "the management read was sealed")
			w.Header().Set("Palbase-Auth-Contract", "1")
			_, _ = w.Write([]byte(admin))
			return
		case "/auth/oauth/config":
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		if !sealedclient.IsSealedRequest(r) {
			s.plainSeen++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnsupportedMediaType)
			_, _ = w.Write([]byte(`{"error":"sealed_required","status":415}`))
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get(sealedclient.EnvelopeHeaderName))
		require.NoError(t, err)
		var env sealedclient.Envelope
		require.NoError(t, json.Unmarshal(raw, &env))
		exporter, err := openSealedRequest(kemPriv, env, r)
		require.NoError(t, err)
		s.sealedSeen++

		// The credential still rides in a header — sealing covers bodies, not
		// headers (v2/internal/sealed/sealed.go:12-15) — so the stack reads it
		// exactly as before.
		require.Equal(t, apiKey, r.Header.Get("apikey"))
		require.Equal(t, "1", r.Header.Get("Palbase-Auth-Contract"))

		body, err := sealResponseUnderExporter(exporter, []byte(snapshot), "application/json", http.StatusOK)
		require.NoError(t, err)
		w.Header().Set("Palbase-Auth-Contract", "1")
		w.Header().Set("Content-Type", sealedclient.ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func openSealedRequest(priv kem.PrivateKey, e sealedclient.Envelope, r *http.Request) ([]byte, error) {
	enc, err := base64.StdEncoding.DecodeString(e.Enc)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(e.CT)
	if err != nil {
		return nil, err
	}
	receiver, err := hpke.NewSuite(sealedclient.SuiteKEM, sealedclient.SuiteKDF, sealedclient.SuiteAEAD).
		NewReceiver(priv, nil)
	if err != nil {
		return nil, err
	}
	opener, err := receiver.Setup(enc)
	if err != nil {
		return nil, err
	}
	// r.Host, r.Method, r.URL.Path — aadHost(trustProxy=false) at
	// v2/internal/server/sealed.go:462. The open only succeeds if the CLI bound
	// the same three.
	if _, err := opener.Open(ct, sealedclient.AAD(r.Host, r.Method, r.URL.Path, e.ICT, e.TS,
		r.Header.Get("Idempotency-Key"))); err != nil {
		return nil, err
	}
	return opener.Export(sealedclient.ExporterContext, sealedclient.ExporterLen), nil
}

func sealResponseUnderExporter(exporter, plaintext []byte, ict string, status int) ([]byte, error) {
	aead, err := chacha20poly1305.New(exporter)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// The response AAD binds the inner type and the STATUS, which lives on the
	// response line where no body encryption reaches
	// (v2/internal/sealed/response.go:64).
	aad := []byte("palbase-sealed/response/v1\n" + ict + "\n" + strconv.Itoa(status))
	return json.Marshal(sealedclient.ResponseEnvelope{
		V:   2,
		N:   base64.StdEncoding.EncodeToString(nonce),
		CT:  base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, plaintext, aad)),
		ICT: ict,
		St:  status,
	})
}

const linkAdminConfig = `{"contract_revision":1,"credentials":[],"providers":{` +
	`"google":{"enabled":true,"browser_clients":[],"native_clients":[{"key":"google-ios","enabled":true,"application_key":"consumer","platform":"ios","variant":"release","bundle_id":"com.example.app","ios_client_id":"IOS.apps.googleusercontent.com","redirect_uri":"com.googleusercontent.apps.IOS:/oauthredirect"}]},` +
	`"apple":{"enabled":false,"browser_clients":[],"native_clients":[]},` +
	`"microsoft":{"enabled":false,"browser_clients":[],"native_clients":[]},` +
	`"github":{"enabled":false,"browser_clients":[],"native_clients":[]}}}`

// THE DEFECT. `palbase link --platform ios` reads `/auth/oauth/config`, and
// against a stack that publishes a sealing keyset that read is refused with 415
// unless it is sealed. Before this test the CLI had no sealing code at all.
func TestLinkReadsTheAuthSnapshotSealed(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.Mkdir("Consumer.xcodeproj", 0755))
	require.NoError(t, os.WriteFile("Consumer.xcodeproj/project.pbxproj",
		[]byte(`PRODUCT_BUNDLE_IDENTIFIER = com.example.app;`), 0644))

	s := newSealingStack(t, "env", "pb_env_cPUBLIC", iosSnapshot, linkAdminConfig)
	linkedAs(t, s.srv.URL, "operator")

	// The stack really does refuse a plaintext read there. Without this the
	// test would pass against a fixture that never refused anything, which is
	// exactly how the defect survived.
	plain, err := s.srv.Client().Get(s.srv.URL + "/auth/oauth/config")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnsupportedMediaType, plain.StatusCode)
	require.NoError(t, plain.Body.Close())

	snapshot, selection, err := linkedOAuth(context.Background(), Target{URL: s.srv.URL},
		"ios", "pb_env_cPUBLIC", s.rootB64, OAuthSelection{})
	require.NoError(t, err)
	require.Equal(t, "consumer", selection.ApplicationKey)
	require.Equal(t, "release", selection.Variant)
	require.NotNil(t, snapshot)
	require.Equal(t, "consumer", snapshot.ApplicationKey)
	require.Equal(t, 1, s.sealedSeen, "the snapshot read did not arrive sealed")
	require.Equal(t, 1, s.plainSeen, "only the probe this test sent itself may be plaintext")
}

// A stack whose sealing root is not the one the environment recorded must be
// refused before anything is sent — that is the substituted-key attack the
// signed keyset exists to catch, and it is why the root travels in the app's
// configuration rather than being fetched beside the keyset.
func TestLinkRefusesASnapshotFromAnUnvouchedRoot(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.Mkdir("Consumer.xcodeproj", 0755))
	require.NoError(t, os.WriteFile("Consumer.xcodeproj/project.pbxproj",
		[]byte(`PRODUCT_BUNDLE_IDENTIFIER = com.example.app;`), 0644))

	s := newSealingStack(t, "env", "pb_env_cPUBLIC", iosSnapshot, linkAdminConfig)
	other := newSealingStack(t, "env", "pb_env_cPUBLIC", iosSnapshot, linkAdminConfig)
	linkedAs(t, s.srv.URL, "operator")

	_, _, err := linkedOAuth(context.Background(), Target{URL: s.srv.URL},
		"ios", "pb_env_cPUBLIC", other.rootB64, OAuthSelection{})
	require.ErrorContains(t, err, "does not verify")
	require.Zero(t, s.sealedSeen)
	require.Zero(t, s.plainSeen)
}
