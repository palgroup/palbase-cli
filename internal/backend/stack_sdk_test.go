package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// sdkSkewStack answers the two routes ensureProjectSDK walks: the public
// document that says what the project RUNS, and the tarball route.
//
// The tarball route answers 500 on purpose. The install itself is npm's job and
// needs a real registry-shaped archive; what this file is about is the SENTENCE
// the CLI writes BEFORE it downloads anything — so the download is allowed to
// fail and the message is read off the writer.
func sdkSkewStack(t *testing.T, runs string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == wellKnownPath {
			_ = json.NewEncoder(w).Encode(stackDescription{Hosting: "project", SDKVersion: runs})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeProjectSDK lays out a checkout: what package.json DECLARES and what
// node_modules actually holds. The two are different facts and the skew notice
// is about exactly that difference.
func writeProjectSDK(t *testing.T, dir, declared, installed string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"app","dependencies":{"`+backendPkg+`":"`+declared+`"}}`), 0o644))
	if installed == "" {
		return
	}
	mod := filepath.Join(dir, "node_modules", "@palbase", "backend")
	require.NoError(t, os.MkdirAll(mod, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mod, "package.json"),
		[]byte(`{"name":"`+backendPkg+`","version":"`+installed+`"}`), 0o644))
}

// PUSH DOWNGRADES THE CHECKOUT AND USED TO SAY ALMOST NOTHING ABOUT IT.
//
// The line was "this project runs @palbase/backend 23.0.0 and this checkout has
// 24.1.0 — installing the project's". True, and it leaves out the part that
// costs someone an afternoon: the checkout's package.json still DECLARES ^24,
// every `tsc` run in this directory was typed against 24, and after the install
// node_modules holds 23. So the types the author validated against are not the
// types this push builds, and nothing said so.
//
// The downgrade itself is NOT up for debate — its reason is measured
// (stack_sdk.go's header: a real backend failing with
// "getRegisteredControllers is not a function"), and there is no verb to move a
// project's SDK, because `palbase env` went at the v2 cutover. What changes is
// that the CLI now says what it did.
func TestSDKSkewSaysTheTypecheckNoLongerDescribesTheBuild(t *testing.T) {
	dir := t.TempDir()
	writeProjectSDK(t, dir, "^24.0.0", "24.1.0")
	srv := sdkSkewStack(t, "23.0.0")

	var out bytes.Buffer
	err := ensureProjectSDK(context.Background(), dir,
		Target{URL: srv.URL}, Credentials{Value: "k", Kind: KindKey}, &out)
	// The tarball route answers 500, so the install fails — after the message.
	require.Error(t, err, "the stub refuses the tarball; the message is what this test reads")

	got := out.String()
	// (a) what the local package.json declares — the range AND its major.
	require.Containsf(t, got, "^24.0.0", "the declared range is missing:\n%s", got)
	require.Regexpf(t, regexp.MustCompile(`major\s+24`), got,
		"the declared MAJOR is not named:\n%s", got)
	// (b) what is being installed, and what was there.
	require.Containsf(t, got, "23.0.0", "the version being installed is missing:\n%s", got)
	require.Containsf(t, got, "24.1.0", "the version already in node_modules is missing:\n%s", got)
	// (c) that a typecheck done in this checkout now belongs to another major.
	require.Regexpf(t, regexp.MustCompile(`(?i)typecheck`), got,
		"nothing says the typecheck is now stale:\n%s", got)
}

// NEGATIVE CONTROL: same major, nothing to say. Without this, "always print the
// warning" would satisfy the test above and the notice would be noise on every
// push.
func TestSDKSkewIsSilentWhenTheMajorsAgree(t *testing.T) {
	dir := t.TempDir()
	writeProjectSDK(t, dir, "^24.0.0", "24.1.0")
	srv := sdkSkewStack(t, "24.0.0")

	var out bytes.Buffer
	require.NoError(t, ensureProjectSDK(context.Background(), dir,
		Target{URL: srv.URL}, Credentials{Value: "k", Kind: KindKey}, &out))
	require.Emptyf(t, out.String(),
		"a compatible minor difference must install nothing and say nothing:\n%s", out.String())
}

// `palbase status` REPORTED A DIFFERENT SDK THAN THE ONE PUSH ACTS ON.
//
// Two numbers, two sources, one label. `push` asks the project what it RUNS
// (/.well-known/palbase.json, served from a live probe of the runtime —
// v2/internal/server/wellknown.go) and reinstalls when that major differs.
// `status` printed the sdk_version off deployments/current, which is what the
// last ACTIVATED ARTIFACT was BUILT with. Those two diverge the moment a project
// is moved onto a newer runtime without a redeploy — and then status hands
// someone a number that predicts nothing about what push is going to do.
func TestStatusSDKComesFromTheSameSourceAsPush(t *testing.T) {
	inScratchCheckout(t)

	const runs = "23.1.0"      // what the runtime is running — push's source
	const builtWith = "23.0.0" // what the live artifact was built with

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wellKnownPath:
			_ = json.NewEncoder(w).Encode(stackDescription{Hosting: "project", SDKVersion: runs})
		case "/v1/management/deployments/current":
			count := 12
			_ = json.NewEncoder(w).Encode(deploymentState{
				Digest:        "7c232f1484db13acc8b083d905df6ac4d8b00ea8",
				EndpointCount: &count, SDKVersion: builtWith,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	target := Target{URL: srv.URL}
	require.NoError(t, WriteTarget(target))
	cred := Credentials{Value: "k", Kind: KindKey}
	require.NoError(t, StoreCredential(srv.URL, cred))

	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	handled, err := statusOfProject(cmd, Resolvers{}, false)
	require.True(t, handled)
	require.NoError(t, err)

	// SAME SOURCE, asserted against the function push itself calls rather than
	// against a literal — a test that only pinned "23.1.0" would still pass if
	// both sides were rewired to a third document.
	fromPush, err := projectSDKVersion(context.Background(), target, cred)
	require.NoError(t, err)
	require.Equal(t, runs, fromPush)

	got := out.String()
	require.Containsf(t, got, "sdk:", "status reports no SDK line at all:\n%s", got)
	sdkLine := regexp.MustCompile(`(?m)^sdk:.*$`).FindString(got)
	require.Containsf(t, sdkLine, fromPush,
		"the sdk line does not carry what push reads (%s):\n%s", fromPush, got)
	require.NotContainsf(t, sdkLine, builtWith,
		"the sdk line still carries the ARTIFACT's version (%s), which push never looks at:\n%s",
		builtWith, got)

	// The artifact's own SDK is not deleted — it is a real fact — but it must no
	// longer be presented under the same bare "SDK" label that made the two
	// numbers look interchangeable.
	require.Containsf(t, got, builtWith, "the artifact's SDK was dropped entirely:\n%s", got)
	require.NotContainsf(t, got, ", SDK "+builtWith,
		"the artifact's SDK is still labelled as if it were THE sdk:\n%s", got)
	require.Truef(t, strings.Contains(got, "built with"),
		"the artifact's SDK is not labelled as the version it was BUILT with:\n%s", got)
}
