package backend

// The silent-truncation class.
//
// `io.ReadAll(io.LimitReader(body, N))` ends in a CLEAN EOF: the limit reader
// stops at N and reports success, io.ReadAll returns a nil error, and a body cut
// in half is handed back as a complete one. `palbase pull` unpacked the first
// megabyte of a 3.4 MB bundle that way and failed at the far end with
// "read tar entry: unexpected EOF" — the truncation was silent and only the
// symptom was loud.
//
// The helper under test is the whole point: eleven call sites cannot each
// remember to read one byte past their own cap and look at it.

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadCappedAcceptsExactlyTheCap(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1024)
	got, err := readCapped(bytes.NewReader(body), 1024, "the test")
	require.NoError(t, err, "a body exactly at the cap is complete, not oversized")
	require.Equal(t, body, got)

	// And an empty body is not an overflow.
	got, err = readCapped(bytes.NewReader(nil), 1024, "the test")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestReadCappedRefusesOneByteOverTheCap(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1025)
	got, err := readCapped(bytes.NewReader(body), 1024, "https://stack.example/v1/thing")
	require.Error(t, err, "one byte over the cap must be an error, not a shorter body")
	require.Nil(t, got, "a refused read must hand back nothing — a partial body is what the caller must never see")
	// The limit and the source both have to be in the message: "unexpected EOF"
	// somewhere downstream is what this class costs when they are not.
	require.Contains(t, err.Error(), "1024")
	require.Contains(t, err.Error(), "https://stack.example/v1/thing")
}

// THE REALISTIC OVERFLOW. The OAuth snapshot is the one capped read a project
// can actually cross in the field — several providers, several clients each,
// every one with redirect URIs — and its site already read `cap+1` with nothing
// looking at the extra byte, which is the shape of a check somebody meant to
// write.
//
// MEASURED BEFORE THE FIX, and it is more interesting than "silence": the read
// did not go unnoticed, it went MISATTRIBUTED —
//
//	invalid ios auth snapshot: auth config exceeds 256 KiB
//
// — telling the reader their stack sent an invalid document when the stack sent
// a valid one this door refused to finish reading. And that message only
// appears at all by coincidence: authcontract.Validate refuses anything over
// 256*1024 (internal/authcontract/validate.go:32), which is exactly ONE BYTE
// below what this site read. Had the cap been written without its `+1`, the
// truncated body would have arrived at 262144 bytes, passed that ceiling as a
// complete document, and failed as a JSON parse error instead. Two limits
// happening to line up is not a check.
func TestAnOversizedOAuthSnapshotNamesTheCap(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.Mkdir("Consumer.xcodeproj", 0755))
	require.NoError(t, os.WriteFile("Consumer.xcodeproj/project.pbxproj",
		[]byte(`PRODUCT_BUNDLE_IDENTIFIER = com.example.app;`), 0644))

	// Valid JSON, one field of which is large enough to cross the cap.
	oversized := `{"contract_revision":1,"config_revision":"2","environment_ref":"env",` +
		`"application_key":"consumer","platform":"ios","variant":"release","clients":[],` +
		`"note":"` + strings.Repeat("x", socialSnapshotLimit) + `"}`
	srv := oauthLinkServer(t, oversized)

	_, _, err := linkedOAuth(context.Background(), Target{URL: srv.URL},
		"ios", "pb_env_cPUBLIC", "", OAuthSelection{})
	require.Error(t, err)
	require.Contains(t, err.Error(), strconv.Itoa(socialSnapshotLimit),
		"a snapshot cut at the cap must name the cap, not arrive as a schema error")
	require.NotContains(t, err.Error(), "invalid ios auth snapshot",
		"the truncation must be reported as a truncation, not as the stack sending a bad document")
}

// The token cap was already read one byte past and checked — and never tested.
// A cap with no test is a cap that survives exactly until somebody simplifies it.
func TestATokenLargerThanTheCapIsRefused(t *testing.T) {
	_, err := readTokenFrom(strings.NewReader(strings.Repeat("t", maxTokenBytes+1)))
	require.Error(t, err)
	require.Contains(t, err.Error(), strconv.Itoa(maxTokenBytes))
	require.Contains(t, err.Error(), "nothing was stored")

	// Exactly at the cap is a token, not an overflow.
	token, err := readTokenFrom(strings.NewReader(strings.Repeat("t", maxTokenBytes)))
	require.NoError(t, err)
	require.Len(t, token, maxTokenBytes)
}
