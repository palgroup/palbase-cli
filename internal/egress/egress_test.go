package egress

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chdirTemp(t *testing.T) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	return out.String(), func() error { err := cmd.Execute(); return err }()
}

func readFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	return string(data)
}

func TestAdd_WritesImportableConfig(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "api.open-meteo.com")
	require.NoError(t, err)

	src := readFile(t)
	assert.Contains(t, src, `import { defineEgress } from "@palbase/backend";`)
	assert.Contains(t, src, "export default defineEgress({")
	assert.Contains(t, src, `"api.open-meteo.com",`)

	hosts, err := readConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"api.open-meteo.com"}, hosts)
}

func TestAddRemove_RoundTripsAndSorts(t *testing.T) {
	chdirTemp(t)
	for _, h := range []string{"zenquotes.io", "api.open-meteo.com", ".example.com"} {
		_, err := run(t, "add", h)
		require.NoError(t, err)
	}
	hosts, err := readConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{".example.com", "api.open-meteo.com", "zenquotes.io"}, hosts)

	_, err = run(t, "remove", "zenquotes.io")
	require.NoError(t, err)
	hosts, err = readConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{".example.com", "api.open-meteo.com"}, hosts)

	// Removing something that was never allowed is an error, not a silent no-op —
	// a typo'd remove must not read as "done".
	_, err = run(t, "remove", "nope.example.com")
	require.Error(t, err)
}

// The whole point of the command: reject locally exactly what the deploy's
// fail-closed validator rejects, so a bad host is caught at authoring time
// instead of as a failed deploy. Mirrors modules/backend validateEgressHost.
// MUTATION CHECK: make validateHost return nil and every case below goes RED.
func TestAdd_RejectsWhatTheDeployWouldReject(t *testing.T) {
	cases := []struct{ name, host, wantErr string }{
		{"url not host", "https://api.example.com", "hostname only"},
		{"host with port", "api.example.com:8443", "hostname only"},
		{"host with path", "api.example.com/v1", "hostname only"},
		{"wildcard", "*.example.com", "hostname only"},
		{"ip literal", "127.0.0.1", "IP literals not allowed"},
		{"decimal ip", "2130706433", "single-label"},
		{"short-form ip", "127.1", "top-level label must be alphabetic"},
		{"localhost", "localhost", "single-label"},
		{"cluster internal", "palauth.services-shared.svc", "internal host not allowed"},
		{"single label", "intranet", "single-label"},
		{"empty", "   ", "empty host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chdirTemp(t)
			_, err := run(t, "add", tc.host)
			require.Error(t, err, "host %q must be rejected", tc.host)
			assert.Contains(t, err.Error(), tc.wantErr)
			// A rejected host must never reach the file.
			_, statErr := os.Stat(configPath)
			assert.True(t, os.IsNotExist(statErr), "no config file should be written for a rejected host")
		})
	}
}

func TestReadConfig_RefusesUnrelatedFile(t *testing.T) {
	chdirTemp(t)
	require.NoError(t, os.MkdirAll("config", 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte("export const x = 1;\n"), 0o644))
	_, err := readConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not look like a defineEgress")
}

// A hand-written config (the only way to author this before the command existed)
// must round-trip: comments stripped, hosts preserved.
func TestParseConfig_ReadsHandWrittenFile(t *testing.T) {
	src := `import { defineEgress } from "@palbase/backend";

export default defineEgress({
  hosts: [
    "api.open-meteo.com",         // weather forecast
    // "disabled.example.com",    commented out on purpose
    "zenquotes.io",
  ],
});
`
	hosts, err := parseConfig(src)
	require.NoError(t, err)
	assert.Equal(t, []string{"api.open-meteo.com", "zenquotes.io"}, hosts)
}
