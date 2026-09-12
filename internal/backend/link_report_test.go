package backend

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const anEnvironmentAddress = "https://m5rafc8rm.palbase.studio"

// A CLOUD LINK'S CLOSING LINE NAMES THE PROJECT, NOT AN ADDRESS.
//
// This is the sentence the whole change is about. `palbase link` used to end
// with "linked to https://<ref>.palbase.studio", and that was an accurate
// report of a mechanism that pinned the checkout to one environment. The
// mechanism is gone; a line that still names the environment's address would
// teach the model the product no longer has, to the one reader who is looking
// straight at it.
func TestACloudLinkReportsTheProjectAndNotTheEnvironmentAddress(t *testing.T) {
	var out bytes.Buffer
	reportLinked(&out, Product{ID: "proj_ojxluytsm", Name: "linkuat"}, anEnvironmentAddress, "project", "main")

	got := out.String()
	require.Contains(t, got, "linkuat")
	require.Contains(t, got, "proj_ojxluytsm")
	require.NotContains(t, got, anEnvironmentAddress,
		"the closing line names an environment's address as the thing linked — that is the old model")
	// AND IT SAYS THE ENVIRONMENT WAS READ, not pinned: without this, naming
	// `main` at all reads as a pin.
	require.Contains(t, got, "main")
	require.Contains(t, strings.ToLower(got), "each verb resolves its own environment")
}

// A SELF-HOST LINK KEEPS THE ADDRESS, because there the URL IS the identity.
// Dropping it would leave that reader with no name for what they linked.
func TestASelfHostLinkReportsTheAddress(t *testing.T) {
	var out bytes.Buffer
	reportLinked(&out, Product{}, "https://stack.firma.example", "self-hosted", "")

	got := out.String()
	require.Contains(t, got, "https://stack.firma.example")
	require.Contains(t, got, "self-hosted")
	require.NotContains(t, got, "each verb resolves its own environment",
		"a self-host stack has one environment; promising a per-verb choice would be false")
}
