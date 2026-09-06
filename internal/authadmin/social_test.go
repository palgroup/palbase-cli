package authadmin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestSocialProviderMutationUsesReadRevisionAndRefreshesClients(t *testing.T) {
	rest := &fakeREST{}
	refreshed := 0
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }, RefreshClientConfig: func(*cobra.Command) error { refreshed++; return nil }})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"providers", "config", "set", "google", "--json", `{"native_clients":[]}`})
	require.NoError(t, cmd.Execute())
	require.Equal(t, 2, rest.calls)
	require.Equal(t, http.MethodPatch, rest.method)
	require.Equal(t, `"revision-1"`, rest.headers.Get("If-Match"))
	require.Equal(t, "1", rest.headers.Get(contractHeader))
	require.Equal(t, `{"native_clients":[]}`, string(rest.body))
	require.Equal(t, 1, refreshed)
}

func TestSecretInputNeverRefreshesOrPrintsSecret(t *testing.T) {
	for _, inline := range []bool{false, true} {
		rest := &fakeREST{}
		var out bytes.Buffer
		cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }, RefreshClientConfig: func(*cobra.Command) error { t.Fatal("secret rotation regenerated a client"); return nil }})
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		secret := `{"provider":"google","kind":"oauth_client_secret","client_secret":"server-secret"}`
		input := "-"
		if inline {
			input = secret
		}
		cmd.SetIn(strings.NewReader(secret))
		cmd.SetArgs([]string{"providers", "config", "set", "google", "--credential", "web-secret", "--json", input})
		err := cmd.Execute()
		if inline {
			require.Error(t, err)
			require.Zero(t, rest.calls)
		} else {
			require.NoError(t, err)
			require.Equal(t, http.MethodPut, rest.method)
		}
		require.NotContains(t, out.String(), "server-secret")
	}
}

type conflictingREST struct{ fakeREST }

func (f *conflictingREST) DoWithHeaders(ctx context.Context, method, path string, body []byte, headers http.Header) (int, []byte, http.Header, error) {
	if method == http.MethodGet {
		return f.fakeREST.DoWithHeaders(ctx, method, path, body, headers)
	}
	f.calls++
	return 412, []byte(`{"error":"config_revision_conflict","error_description":"Read again before saving"}`), http.Header{}, nil
}
func TestConcurrentConfigConflictDoesNotRetryOrGenerate(t *testing.T) {
	rest := &conflictingREST{}
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }, RefreshClientConfig: func(*cobra.Command) error { t.Fatal("a rejected change refreshed local files"); return nil }})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"providers", "disable", "google"})
	require.ErrorContains(t, cmd.Execute(), "config_revision_conflict")
	require.Equal(t, 2, rest.calls)
}

func TestSavedConfigAndFailedGenerationAreReportedSeparately(t *testing.T) {
	rest := &fakeREST{}
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }, RefreshClientConfig: func(*cobra.Command) error { return errors.New("generator failed") }})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"providers", "disable", "google"})
	require.ErrorContains(t, cmd.Execute(), "server configuration saved; local client generation failed")
	require.Equal(t, 2, rest.calls)
}
