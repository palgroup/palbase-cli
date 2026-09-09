package authadmin

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type revisionREST struct {
	fakeREST
	readHeaders  http.Header
	writeStatus  int
	writeHeaders http.Header
}

func (f *revisionREST) DoWithHeaders(_ context.Context, method, path string, body []byte, headers http.Header) (int, []byte, http.Header, error) {
	f.calls++
	f.method, f.path, f.body, f.headers = method, path, body, headers.Clone()
	if method == http.MethodGet {
		return 200, []byte(`{"password_min_length":12,"password_max_length":48,"site_url":"https://keep.example"}`), f.readHeaders, nil
	}
	return f.writeStatus, []byte(`{"password_min_length":16,"password_max_length":48,"site_url":"https://keep.example"}`), f.writeHeaders, nil
}

func TestSettingsRevisionGuardsAndPartialWrite(t *testing.T) {
	etag := `"auth-settings-` + strings.Repeat("a", 64) + `"`
	next := `"auth-settings-` + strings.Repeat("b", 64) + `"`
	for _, tc := range []struct {
		name      string
		read      http.Header
		status    int
		saved     http.Header
		calls     int
		errorPart string
	}{
		{"success", http.Header{"Etag": {etag}}, 200, http.Header{"Etag": {next}}, 2, ""},
		{"missing revision", nil, 200, nil, 1, "does not support"},
		{"weak revision", http.Header{"Etag": {"W/" + etag}}, 200, nil, 1, "does not support"},
		{"multiple revisions", http.Header{"Etag": {etag, next}}, 200, nil, 1, "does not support"},
		{"conflict", http.Header{"Etag": {etag}}, 412, nil, 2, "no retry was sent"},
		{"unknown save outcome", http.Header{"Etag": {etag}}, 200, nil, 2, "verify the current settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest := &revisionREST{readHeaders: tc.read, writeStatus: tc.status, writeHeaders: tc.saved}
			cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"settings", "set", "--password-min", "16"})
			err := cmd.Execute()
			if tc.errorPart == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.errorPart)
			}
			require.Equal(t, tc.calls, rest.calls)
			if tc.calls == 2 {
				require.Equal(t, etag, rest.headers.Get("If-Match"))
				require.JSONEq(t, `{"password_min_length":16}`, string(rest.body))
			}
		})
	}
}
