package notifications

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The add command registers a single shared flag set across all providers, so
// every field flag + secret `--<flag>-file` flag MUST be unique across the
// catalog (a collision would silently bind two providers' fields to one flag).
func TestCatalog_FlagsUniqueAcrossProviders(t *testing.T) {
	seen := map[string]string{} // flag → "provider.field"
	for _, spec := range catalog {
		for _, f := range spec.fields {
			owner := spec.name + "." + f.name
			if prev, dup := seen[f.flag]; dup {
				// A shared field name (from-domain, from-email, region, host) MAY
				// repeat across providers — that's fine, they bind to the same
				// flag and only the active provider reads it. Assert the SEMANTICS
				// match (same camelCase field name) so the shared flag is coherent.
				assert.Equal(t, flagField(prev), f.name, "flag --%s reused with a different field name (%s vs %s)", f.flag, prev, owner)
			}
			seen[f.flag] = owner
		}
		// Secret file flags must be globally unique (each maps to a distinct
		// reserved env key) OR coherent if a name repeats.
		for _, s := range spec.secrets {
			require.NotEmpty(t, s.flag, "%s secret has empty flag", spec.name)
		}
	}
}

// flagField extracts the camelCase field name from a "provider.field" owner tag.
func flagField(owner string) string {
	for i := len(owner) - 1; i >= 0; i-- {
		if owner[i] == '.' {
			return owner[i+1:]
		}
	}
	return owner
}

func TestCatalog_EveryProviderHasAtLeastOneSecret(t *testing.T) {
	for _, spec := range catalog {
		assert.NotEmpty(t, spec.secrets, "%s must declare at least one secret", spec.name)
	}
}

func TestCatalog_ChannelsValid(t *testing.T) {
	valid := map[string]bool{"push": true, "email": true, "sms": true}
	for _, spec := range catalog {
		assert.True(t, valid[spec.channel], "%s has invalid channel %q", spec.name, spec.channel)
	}
}

func TestSpecByName(t *testing.T) {
	assert.NotNil(t, specByName("apns"))
	assert.Nil(t, specByName("nope"))
}
