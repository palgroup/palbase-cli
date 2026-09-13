package backend

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheUnlinkedHintDescribesARefAsItsProject(t *testing.T) {
	inScratchCheckout(t)
	_, err := readLinkedProject()
	require.Error(t, err)
	assert.Regexp(t, "palbase link <ref> +the project that environment belongs to", err.Error(),
		"the ref form is not described as binding the project its environment belongs to")
	assert.NotContains(t, err.Error(), "one environment")
}

func TestStaleContractsNameEnvUse(t *testing.T) {
	inScratchCheckout(t)
	var out bytes.Buffer
	reportStaleContracts("main", appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main": {}, "staging": {},
	}}, &out)
	assert.Contains(t, out.String(), "palbase env use")
	assert.Contains(t, out.String(), "palbase spec --env <name>",
		"the hint does not name the one call that refreshes an environment while a local stack runs")
	assert.NotContains(t, out.String(), "palbase link <ref>")
}

func TestUnlinkSuggestsLinkingTheProject(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	var out bytes.Buffer
	require.NoError(t, runUnlink(&out))
	assert.Contains(t, out.String(), "palbase link <project>")
}
