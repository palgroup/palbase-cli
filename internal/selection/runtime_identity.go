package selection

import (
	"fmt"
	"regexp"
)

var (
	canonicalEnvironmentRefPattern = regexp.MustCompile(`^[a-z0-9]{4,24}$`)
	publishableAPIKeyPattern       = regexp.MustCompile(`^pb_([a-z0-9]{4,24})_c[A-Za-z0-9]{20}$`)
)

// IsCanonicalEnvironmentRef reports whether ref is the runtime wire identity
// accepted by DNS, API keys, gateway routing, and control-plane persistence.
func IsCanonicalEnvironmentRef(ref string) bool {
	return canonicalEnvironmentRefPattern.MatchString(ref)
}

// ValidateRuntimeBinding proves that a response and its publishable key both
// belong to the exact Environment selected by the caller. It never includes
// the credential itself in an error.
func ValidateRuntimeBinding(expectedRef, returnedRef, publishableKey string) error {
	if !IsCanonicalEnvironmentRef(expectedRef) {
		return fmt.Errorf("selected environment_ref %q is non-canonical (expected 4-24 lowercase ASCII alphanumeric characters)", expectedRef)
	}
	if !IsCanonicalEnvironmentRef(returnedRef) {
		return fmt.Errorf("response environment_ref %q is non-canonical (expected 4-24 lowercase ASCII alphanumeric characters)", returnedRef)
	}
	if returnedRef != expectedRef {
		return fmt.Errorf("response environment_ref %q does not match selected environment %q", returnedRef, expectedRef)
	}

	parts := publishableAPIKeyPattern.FindStringSubmatch(publishableKey)
	if len(parts) != 2 {
		return fmt.Errorf("publishable API key is non-canonical for environment %q", expectedRef)
	}
	if parts[1] != expectedRef {
		return fmt.Errorf("publishable API key is bound to environment %q, not selected environment %q", parts[1], expectedRef)
	}
	return nil
}
