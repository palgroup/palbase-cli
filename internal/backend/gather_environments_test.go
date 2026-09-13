package backend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type envServerOpts struct {
	notReady    bool          // the well-known document answers 503 forever
	keysRefused bool          // the key read answers 401
	noContract  bool          // the contract read answers 404 (nothing deployed)
	readyDelay  time.Duration // the well-known document takes this long
}

func envServer(t *testing.T, key string, o envServerOpts) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case wellKnownPath:
			time.Sleep(o.readyDelay)
			if o.notReady {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"hosting":"project","sdk_version":"18.0.0"}`))
		case "/v1/management/keys":
			if o.keysRefused {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"publishable":"` + key + `"}`))
		case "/v1/management/openapi":
			if o.noContract {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.2.0","paths":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// routeEnvironments points each ref at its own test server and gives this
// machine a credential for every one of them.
func routeEnvironments(t *testing.T, byRef map[string]string) {
	t.Helper()
	prev := environmentAddress
	environmentAddress = func(ref string) (string, error) {
		if u, ok := byRef[ref]; ok {
			return u, nil
		}
		return "", fmt.Errorf("no test server for %s", ref)
	}
	t.Cleanup(func() { environmentAddress = prev })
	for _, u := range byRef {
		linkedAs(t, u, "a-credential")
	}
}

func TestEveryEnvironmentOfTheProjectIsDescribed(t *testing.T) {
	inScratchCheckout(t)
	main, _ := envServer(t, "pb_main_cK", envServerOpts{})
	staging, _ := envServer(t, "pb_staging_cK", envServerOpts{})
	canary, _ := envServer(t, "pb_canary_cK", envServerOpts{})
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL, "canaref000": canary.URL})
	project := []Environment{
		{Name: "main", Ref: "mainref000", Status: "Running"},
		{Name: "staging", Ref: "stagref000", Status: "Running"},
		{Name: "canary", Ref: "canaref000", Status: "Archived"},
	}

	var out bytes.Buffer
	envs, specs, _, err := gatherEnvironments(context.Background(), Target{URL: main.URL}, "main", "pb_main_cK", project, true, &out)
	require.NoError(t, err, out.String())
	assert.Equal(t, "main", envs.Default)
	require.Len(t, envs.Environments, 3)
	assert.Equal(t, "pb_staging_cK", envs.Environments["staging"].APIKey)
	assert.Equal(t, staging.URL, envs.Environments["staging"].BaseURL)
	assert.Len(t, specs, 3)
	_, statErr := os.Stat(specPath("staging"))
	assert.True(t, os.IsNotExist(statErr), "gather wrote a file; writing is the caller's job (FR-070)")
}

func TestAFailedEnvironmentIsNotAsked(t *testing.T) {
	inScratchCheckout(t)
	main, _ := envServer(t, "pb_main_cK", envServerOpts{})
	broken, brokenHits := envServer(t, "pb_broken_cK", envServerOpts{})
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "brokref000": broken.URL})
	project := []Environment{
		{Name: "main", Ref: "mainref000", Status: "Running"},
		{Name: "broken", Ref: "brokref000", Status: "Failed"},
	}

	var out bytes.Buffer
	envs, _, _, err := gatherEnvironments(context.Background(), Target{URL: main.URL}, "main", "pb_main_cK", project, true, &out)
	require.NoError(t, err)
	assert.NotContains(t, envs.Environments, "broken")
	assert.Zero(t, brokenHits.Load(), "a Failed environment was asked")
	assert.Contains(t, out.String(), "broken")
	assert.Contains(t, out.String(), "Failed")
}

func TestAnEnvironmentThatNeverServesIsDroppedNotFatal(t *testing.T) {
	inScratchCheckout(t)
	prevWait, prevEvery := stackReadyWait, stackReadyRetryEvery
	stackReadyWait, stackReadyRetryEvery = 200*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { stackReadyWait, stackReadyRetryEvery = prevWait, prevEvery })
	main, _ := envServer(t, "pb_main_cK", envServerOpts{})
	asleep, _ := envServer(t, "pb_asleep_cK", envServerOpts{notReady: true})
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "asleref000": asleep.URL})
	project := []Environment{
		{Name: "main", Ref: "mainref000", Status: "Running"},
		{Name: "asleep", Ref: "asleref000", Status: "Archived"},
	}

	var out bytes.Buffer
	envs, _, _, err := gatherEnvironments(context.Background(), Target{URL: main.URL}, "main", "pb_main_cK", project, true, &out)
	require.NoError(t, err)
	assert.NotContains(t, envs.Environments, "asleep")
	assert.Contains(t, out.String(), "asleep")
}

func TestTheDefaultEnvironmentFailingIsFatal(t *testing.T) {
	inScratchCheckout(t)
	main, _ := envServer(t, "pb_main_cK", envServerOpts{keysRefused: true})
	staging, _ := envServer(t, "pb_staging_cK", envServerOpts{})
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	project := []Environment{
		{Name: "main", Ref: "mainref000", Status: "Running"},
		{Name: "staging", Ref: "stagref000", Status: "Running"},
	}

	_, _, _, err := gatherEnvironments(context.Background(), Target{URL: main.URL}, "main", "pb_main_cK", project, true, io.Discard)
	require.Error(t, err)
}

func TestAnEnvironmentWithNothingDeployedIsWrittenAndNamed(t *testing.T) {
	inScratchCheckout(t)
	main, _ := envServer(t, "pb_main_cK", envServerOpts{})
	staging, _ := envServer(t, "pb_staging_cK", envServerOpts{noContract: true})
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	project := []Environment{
		{Name: "main", Ref: "mainref000", Status: "Running"},
		{Name: "staging", Ref: "stagref000", Status: "Running"},
	}

	var out bytes.Buffer
	envs, specs, _, err := gatherEnvironments(context.Background(), Target{URL: main.URL}, "main", "pb_main_cK", project, true, &out)
	require.NoError(t, err)
	assert.Contains(t, envs.Environments, "staging")
	assert.NotContains(t, specs, "staging")
	assert.Contains(t, out.String(), "palbase push --env staging")
}

func TestFourEnvironmentsAreDescribedConcurrently(t *testing.T) {
	inScratchCheckout(t)
	byRef := map[string]string{}
	var project []Environment
	for _, name := range []string{"main", "staging", "canary", "beta"} {
		srv, _ := envServer(t, "pb_"+name+"_cK", envServerOpts{readyDelay: 500 * time.Millisecond})
		ref := (name + "ref0000000")[:10]
		byRef[ref] = srv.URL
		project = append(project, Environment{Name: name, Ref: ref, Status: "Running"})
	}
	routeEnvironments(t, byRef)

	start := time.Now()
	_, _, _, err := gatherEnvironments(context.Background(), Target{URL: byRef["mainref000"]}, "main", "pb_main_cK", project, false, io.Discard)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 1500*time.Millisecond, "four 500 ms descriptions ran one after another")
}
