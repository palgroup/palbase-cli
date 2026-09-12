package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A PUSH WRITES NO PER-ENVIRONMENT FILES IN A BACKEND-ONLY CHECKOUT, and this
// is where the churn actually came from.
//
// FR-020/021 closed `link`, and `link` runs once. `push` runs all day. A
// backend-only checkout was still having `palbase/environments/main/
// openapi.json` and `roles.json` rewritten on every deploy — the diff on every
// branch this whole change exists to remove, produced by the verb nobody
// thought to check. Measured on the product: `palbase push` into a fresh
// `palbase init` checkout that `link` had just told "no client app here".
//
// THE DECISION IS MEASURED BY WHAT IT SKIPS. The gate is the first thing the
// refresh does, before it resolves an environment or asks for a credential, so
// in an UNLINKED backend-only directory the push path returns nil (it never got
// far enough to need any of that) while the asked-for verb fails trying. That
// difference is the gate.
func TestAPushWritesNoArtifactsInABackendOnlyCheckout(t *testing.T) {
	inScratchCheckout(t)

	var out bytes.Buffer
	require.NoError(t, RefreshSpecAfterPush(context.Background(), &out),
		"the push path tried to resolve, so the artifact gate did not fire")
	require.Empty(t, out.String(), "the push path announced work it did not do")
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
}

// `palbase spec` STILL WRITES WITHOUT PLATFORMS (FR-023) — that verb is
// somebody asking, and an answer they asked for is not churn. It gets far
// enough to need a project, which is the proof it did not skip.
func TestSpecDoesNotSkipWhenThereIsNoGenerator(t *testing.T) {
	inScratchCheckout(t)

	var out bytes.Buffer
	err := RefreshSpec(context.Background(), &out)
	require.Error(t, err, "the asked-for verb skipped the way a push does")
	require.Contains(t, err.Error(), "palbase link",
		"it failed for some reason other than having nothing to act on")
}

// AND A CHECKOUT WITH A GENERATOR IS NOT SKIPPED BY THE PUSH PATH EITHER: the
// gate reads the checkout, so a web app still gets its contract on deploy.
func TestAPushStillRefreshesWhereAGeneratorReadsIt(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.WriteFile("package.json", []byte(`{"name":"w"}`), 0o644))
	require.NoError(t, os.WriteFile("index.html", []byte("<!doctype html>"), 0o644))

	var out bytes.Buffer
	err := RefreshSpecAfterPush(context.Background(), &out)
	require.Error(t, err,
		"a checkout with a generator was skipped, so its contract would go stale on every push")
	require.Contains(t, err.Error(), "palbase link")
}
