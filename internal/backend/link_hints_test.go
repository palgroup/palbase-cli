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
	assert.Contains(t, err.Error(), "palbase link <ref>")
	assert.NotContains(t, err.Error(), "one environment")
}

func TestStaleContractsNameEnvUse(t *testing.T) {
	inScratchCheckout(t)
	var out bytes.Buffer
	reportStaleContracts("main", appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main": {}, "staging": {},
	}}, &out)
	assert.Contains(t, out.String(), "palbase env use")
	assert.NotContains(t, out.String(), "palbase link <ref>")
}

func TestUnlinkSuggestsLinkingTheProject(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	var out bytes.Buffer
	require.NoError(t, runUnlink(&out))
	assert.Contains(t, out.String(), "palbase link <project>")
}
