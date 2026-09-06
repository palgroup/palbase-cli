package versions

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type fakeREST struct {
	paths          []string
	current, daily string
	dailyStatus    int
}

func (f *fakeREST) Do(_ context.Context, method, path string, _ []byte) (int, []byte, error) {
	f.paths = append(f.paths, path)
	if method != http.MethodGet {
		return http.StatusMethodNotAllowed, nil, nil
	}
	if strings.HasPrefix(path, dailyPath) {
		if f.dailyStatus != 0 {
			return f.dailyStatus, []byte(f.daily), nil
		}
		return http.StatusOK, []byte(f.daily), nil
	}
	return http.StatusOK, []byte(f.current), nil
}

func runVersions(rest *fakeREST, args ...string) (string, error) {
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	cmd.SilenceUsage = true // The root suppresses usage for runtime errors.
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestJSONIncludesCurrentAndDailyInOneDocument(t *testing.T) {
	current := `{"buckets":[{"platform":"ios","app_version":"2.0","installations":10}],"unidentified":3,"extra":"preserved"}`
	daily := `[{"day":"2026-09-05","platform":"ios","app_version":"2.0","installations":8}]`
	for _, withDays := range []bool{false, true} {
		rest := &fakeREST{current: current, daily: daily}
		args := []string{"--json"}
		if withDays {
			args = append(args, "--days", "30")
		}
		out, err := runVersions(rest, args...)
		require.NoError(t, err)
		require.True(t, json.Valid([]byte(out)), out)
		if withDays {
			var doc map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(out), &doc))
			require.JSONEq(t, current, string(doc["current"]))
			require.JSONEq(t, daily, string(doc["daily"]))
			require.Equal(t, []string{currentPath, dailyPath + "?days=30"}, rest.paths)
		} else {
			require.JSONEq(t, current, out)
			require.Equal(t, []string{currentPath}, rest.paths)
		}
	}
}

func TestInvalidDaysFailBeforeAnyRequest(t *testing.T) {
	for _, days := range []string{"-1", "91"} {
		rest := &fakeREST{}
		out, err := runVersions(rest, "--days", days)
		require.ErrorContains(t, err, "--days")
		require.Empty(t, rest.paths)
		require.Empty(t, out)
	}
}

func TestFailedResponsesProduceNoPartialJSON(t *testing.T) {
	for _, rest := range []*fakeREST{
		{current: `not json`},
		{current: `{"buckets":[]}`, daily: `not json`},
		{current: `{"buckets":[]}`, daily: `{"error":"unavailable"}`, dailyStatus: http.StatusServiceUnavailable},
	} {
		out, err := runVersions(rest, "--json", "--days", "30")
		require.Error(t, err)
		require.Empty(t, out)
	}
}
