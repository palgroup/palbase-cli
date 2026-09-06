package backend

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageCheckReportsRegistryFailureForTheRequestedVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Docker test executable requires a POSIX shell")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$PALBASE_TEST_DOCKER_CALLS"
if [ "$1" = manifest ]; then
  echo 'manifest unknown: missing SDK release' >&2
fi
exit 1
`), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PALBASE_TEST_DOCKER_CALLS", calls)
	err := imagesPresent(context.Background(), stackImages, "34.1.0")
	require.ErrorContains(t, err, "ghcr.io/palgroup/palbase/palsvc:34.1.0")
	require.ErrorContains(t, err, "manifest unknown: missing SDK release")
	require.NotContains(t, err.Error(), "docker build")
	raw, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, "image inspect ghcr.io/palgroup/palbase/palsvc:34.1.0\nmanifest inspect ghcr.io/palgroup/palbase/palsvc:34.1.0\n", string(raw))
}
