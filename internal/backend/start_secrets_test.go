package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vault is one environment's secret store as its management surface serves it.
type vault struct {
	mu     sync.Mutex
	values map[string]string
	status int // non-zero: the listing answers with this status
	puts   []string
}

func (v *vault) serve(t *testing.T) *httptest.Server {
	t.Helper()
	const prefix = "/v1/management/secrets"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == prefix:
			if v.status != 0 {
				w.WriteHeader(v.status)
				return
			}
			var list struct {
				Secrets []map[string]string `json:"secrets"`
			}
			for name := range v.values {
				list.Secrets = append(list.Secrets, map[string]string{"name": name})
			}
			_ = json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/value"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix+"/"), "/value")
			value, ok := v.values[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, value)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, prefix+"/"):
			var body struct {
				Value string `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			name := strings.TrimPrefix(r.URL.Path, prefix+"/")
			v.values[name] = body.Value
			v.puts = append(v.puts, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

var (
	onlyMain       = []Environment{{Ref: "mainref000", Name: "main", Status: "Running"}}
	mainAndStaging = []Environment{
		{Ref: "mainref000", Name: "main", Status: "Running"},
		{Ref: "stagref000", Name: "staging", Status: "Running"},
	}
)

// startSecretsRig binds the checkout to todoapp, serves every environment's
// vault at the address its ref resolves to, and returns the local stack's vault.
func startSecretsRig(t *testing.T, envs []Environment, vaults map[string]*vault) (*vault, Target) {
	t.Helper()
	inScratchCheckout(t)
	useTempMachineHome(t)
	t.Setenv("PALBASE_ENV", "")
	resolverRig(t, envs)
	byRef := map[string]string{}
	for _, e := range envs {
		byRef[e.Ref] = vaults[e.Name].serve(t).URL
	}
	routeEnvironments(t, byRef)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	local := &vault{values: map[string]string{}}
	localSrv := local.serve(t)
	linkedAs(t, localSrv.URL, "local-credential")
	return local, Target{URL: localSrv.URL, Local: true}
}

func TestStartPullsFromTheOnlyEnvironment(t *testing.T) {
	local, target := startSecretsRig(t, onlyMain, map[string]*vault{
		"main": {values: map[string]string{"A": "a-main", "B": "b-main"}},
	})
	var out strings.Builder
	pullSecrets(context.Background(), "todoapp", target, &out)
	assert.Contains(t, out.String(), "secrets: 2 pulled from todoapp/main")
	assert.Equal(t, map[string]string{"A": "a-main", "B": "b-main"}, local.values)
}

func TestStartPullsFromTheNamedEnvironment(t *testing.T) {
	local, target := startSecretsRig(t, mainAndStaging, map[string]*vault{
		"main":    {values: map[string]string{"A": "a-main"}},
		"staging": {values: map[string]string{"A": "a-staging"}},
	})
	SelectedEnvFlag = "staging" // resolverRig restores it
	var out strings.Builder
	pullSecrets(context.Background(), "todoapp", target, &out)
	assert.Contains(t, out.String(), "secrets: 1 pulled from todoapp/staging")
	assert.Equal(t, "a-staging", local.values["A"])
}

func TestStartPullsFromThisMachinesSelection(t *testing.T) {
	local, target := startSecretsRig(t, mainAndStaging, map[string]*vault{
		"main":    {values: map[string]string{"A": "a-main"}},
		"staging": {values: map[string]string{"A": "a-staging"}},
	})
	require.NoError(t, WriteSelection(".", Selection{Project: "prd_a", Env: "staging", Ref: "stagref000"}))
	var out strings.Builder
	pullSecrets(context.Background(), "todoapp", target, &out)
	assert.Contains(t, out.String(), "secrets: 1 pulled from todoapp/staging")
	assert.Equal(t, "a-staging", local.values["A"])
}

// AMBIGUITY PULLS NOTHING. A start that guessed would eventually guess
// production and copy its credentials onto a laptop.
func TestStartPullsNothingWhenTheEnvironmentIsAmbiguous(t *testing.T) {
	local, target := startSecretsRig(t, mainAndStaging, map[string]*vault{
		"main":    {values: map[string]string{"A": "a-main"}},
		"staging": {values: map[string]string{"A": "a-staging"}},
	})
	var out strings.Builder
	pullSecrets(context.Background(), "todoapp", target, &out)
	for _, want := range []string{"secrets: not pulled", "main", "staging", "--env <name>"} {
		assert.Contains(t, out.String(), want)
	}
	assert.Empty(t, local.puts)
}

func TestStartSaysWhyTheChosenEnvironmentCouldNotBeRead(t *testing.T) {
	local, target := startSecretsRig(t, onlyMain, map[string]*vault{
		"main": {values: map[string]string{"A": "a-main"}, status: http.StatusInternalServerError},
	})
	var out strings.Builder
	pullSecrets(context.Background(), "todoapp", target, &out)
	assert.Contains(t, out.String(), "secrets: not pulled from todoapp/main — ")
	assert.Contains(t, out.String(), "500")
	assert.Empty(t, local.puts)
}

func TestStartStillNamesAnAddressLink(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteTarget(Target{URL: "https://stack.example"}))
	var out strings.Builder
	pullSecrets(context.Background(), "todoapp", Target{URL: "http://127.0.0.1:1", Local: true}, &out)
	assert.Contains(t, out.String(), "secrets: this checkout is linked to an address, so there is no environment to pull from")
}

func TestStartKeepsAValueChangedHereSincePulled(t *testing.T) {
	local, target := startSecretsRig(t, onlyMain, map[string]*vault{
		"main": {values: map[string]string{"A": "a-main", "B": "b-main"}},
	})
	pullSecrets(context.Background(), "todoapp", target, io.Discard)
	local.mu.Lock()
	local.values["B"] = "b-changed-here"
	local.mu.Unlock()

	var out strings.Builder
	pullSecrets(context.Background(), "todoapp", target, &out)
	assert.Contains(t, out.String(), "secrets: 1 pulled from todoapp/main · 1 kept (changed here since)")
	assert.Equal(t, "b-changed-here", local.values["B"])
}
