package test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func run(t *testing.T, r Resolvers, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := Cmd(r)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// Use a real subprocess without depending on the repository's npm scripts.
func stubNpm(t *testing.T, exitCode string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("npm stub requires a POSIX shell")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "npm-environment")
	path := filepath.Join(dir, "npm")
	require.NoError(t, os.WriteFile(path, []byte(`#!/bin/sh
printf '%s\n' "$PALBASE_TEST_BASE_URL" "$PALBASE_TEST_API_KEY" "$PALBASE_TEST_IDENTITIES" "$PALBASE_TEST_CANDIDATE" > "$PALBASE_NPM_MARKER"
exit `+exitCode+"\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PALBASE_NPM_MARKER", marker)
	return marker
}

const identities = `{"identities":{"user1":{"id":"u1","email":"a@e.f","password":"p"}}}`

func liveResolvers(mint func(*cobra.Command, int) ([]byte, func(context.Context) error, error)) Resolvers {
	return Resolvers{
		Target: func(*cobra.Command) (Target, error) {
			return Target{URL: "http://127.0.0.1:63638", APIKey: "pb_project_x", Candidate: "candidate"}, nil
		},
		Mint: mint,
	}
}

func TestLiveLayerExportsIdentitiesAndCleansAfterSuccess(t *testing.T) {
	marker := stubNpm(t, "0")
	cleaned := false
	r := liveResolvers(func(_ *cobra.Command, count int) ([]byte, func(context.Context) error, error) {
		require.Equal(t, 3, count)
		return []byte(identities), func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "cleanup must be bounded")
			require.InDelta(t, 30, time.Until(deadline).Seconds(), 2)
			raw, err := os.ReadFile(marker)
			require.NoError(t, err, "cleanup must run after npm finishes")
			require.Equal(t, "http://127.0.0.1:63638\npb_project_x\n"+identities+"\ncandidate\n", string(raw))
			cleaned = true
			return nil
		}, nil
	})
	out, err := run(t, r, "--live", "--identities", "3")
	require.NoError(t, err)
	require.True(t, cleaned)
	require.Contains(t, out, "minted 1 identit")
	require.Contains(t, out, "removed this run's test identities")
}

func TestLiveFailureStillCleansAndPreservesBothErrors(t *testing.T) {
	stubNpm(t, "7")
	cleanupFailure := errors.New("user u1 remains: cleanup denied")
	for _, tc := range []struct {
		name       string
		cleanupErr error
	}{
		{"cleanup succeeds", nil}, {"cleanup fails", cleanupFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleaned := false
			r := liveResolvers(func(*cobra.Command, int) ([]byte, func(context.Context) error, error) {
				return []byte(identities), func(context.Context) error { cleaned = true; return tc.cleanupErr }, nil
			})
			_, err := run(t, r, "--live")
			require.True(t, cleaned)
			require.ErrorContains(t, err, "live tests failed")
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 7, exitErr.ExitCode())
			if tc.cleanupErr != nil {
				require.ErrorIs(t, err, tc.cleanupErr)
				require.ErrorContains(t, err, "clean up this run's test identities")
			}
		})
	}
}

func TestSuccessfulTestsFailTheCommandIfCleanupFails(t *testing.T) {
	stubNpm(t, "0")
	failure := errors.New("cleanup denied")
	r := liveResolvers(func(*cobra.Command, int) ([]byte, func(context.Context) error, error) {
		return []byte(identities), func(context.Context) error { return failure }, nil
	})
	out, err := run(t, r, "--live")
	require.ErrorIs(t, err, failure)
	require.NotContains(t, out, "removed this run's test identities")
}

func TestMalformedMintIsRejectedButItsKnownUsersAreCleaned(t *testing.T) {
	for _, raw := range []string{`{"users":[{"user_id":"u1"}]}`, `{"identities":`} {
		t.Run(raw, func(t *testing.T) {
			cleaned := false
			r := liveResolvers(func(*cobra.Command, int) ([]byte, func(context.Context) error, error) {
				return []byte(raw), func(context.Context) error { cleaned = true; return nil }, nil
			})
			_, err := run(t, r, "--live")
			require.ErrorContains(t, err, "identities")
			require.True(t, cleaned)
		})
	}
}

func TestCancelledRunCanStillCleanItsUsers(t *testing.T) {
	stubNpm(t, "0")
	cleaned := false
	r := liveResolvers(func(cmd *cobra.Command, _ int) ([]byte, func(context.Context) error, error) {
		ctx, cancel := context.WithCancel(cmd.Context())
		cancel()
		cmd.SetContext(ctx)
		return []byte(identities), func(ctx context.Context) error {
			require.NoError(t, ctx.Err())
			_, ok := ctx.Deadline()
			require.True(t, ok)
			cleaned = true
			return nil
		}, nil
	})
	_, err := run(t, r, "--live")
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, cleaned)
}

func TestMintFailureDoesNotTryDeletingUnknownUsers(t *testing.T) {
	cleaned := false
	r := liveResolvers(func(*cobra.Command, int) ([]byte, func(context.Context) error, error) {
		return nil, func(context.Context) error { cleaned = true; return nil }, errors.New("rate limited")
	})
	_, err := run(t, r, "--live")
	require.ErrorContains(t, err, "rate limited")
	require.False(t, cleaned)
}

func TestUnitRunNeverMintsOrDeletesIdentities(t *testing.T) {
	stubNpm(t, "0")
	_, err := run(t, Resolvers{}, "--unit")
	require.NoError(t, err)
}

func TestUnitAndLiveTogetherIsRefused(t *testing.T) {
	_, err := run(t, Resolvers{}, "--unit", "--live")
	require.Error(t, err)
}

func TestHelpDoesNotClaimDeployExpiry(t *testing.T) {
	require.False(t, strings.Contains(Cmd(Resolvers{}).Long, "expire with the deploy"))
}
