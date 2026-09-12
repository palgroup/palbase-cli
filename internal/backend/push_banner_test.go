package backend

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// PUSH ANNOUNCES ONCE, and the announcement is the environment-aware one.
//
// `runStackPush` used to print `▸ target.Describe()` itself, to cover a second
// caller (a probe in the cloud command) that no longer exists. With one call
// site left — and that one announcing through `PrintResolvedFor` — the line
// became a SECOND banner, and the less specific of the two: `Target.Describe()`
// cannot name the environment, so a push printed
//
//	▸ uatclone/main
//	▸ uatclone
//
// and the vaguer line came last. Measured on the product at 0.65.0.
//
// THE LOCAL REFUSAL IS THE SEAM: it returns before anything reaches the
// network, and it is reached after the point where the banner used to print, so
// this measures the line and nothing else.
func TestPushDoesNotAnnounceASecondTime(t *testing.T) {
	var out bytes.Buffer
	err := runStackPush(context.Background(),
		Target{URL: "http://127.0.0.1:54321", Local: true, Project: "prd_a", Name: "uatclone"},
		Credentials{}, false, false, &out)

	require.Error(t, err, "a push at the local stack must refuse")
	require.NotContains(t, out.String(), "▸",
		"push announced a second time, and this one cannot name the environment")
}

// AND THE REFUSAL ITSELF STILL REACHES THE READER — a test that only counted
// banners would pass just as well if the whole function went silent.
func TestPushStillRefusesTheLocalStackByName(t *testing.T) {
	var out bytes.Buffer
	err := runStackPush(context.Background(),
		Target{URL: "http://127.0.0.1:54321", Local: true}, Credentials{}, false, false, &out)

	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase stop")
	require.Contains(t, strings.ToLower(err.Error()), "already serves this directory")
}
