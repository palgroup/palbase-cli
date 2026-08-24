package selection

import (
	"fmt"
	"regexp"
)

var (
	canonicalEnvironmentRefPattern = regexp.MustCompile(`^[a-z0-9]{4,24}$`)
	// The body is 20 characters on the v1 line and 32 on the v2 line; both are
	// drawn from the same base62 alphabet, so the envelope is a range and not a
	// second pattern.
	publishableAPIKeyPattern = regexp.MustCompile(`^pb_([a-z0-9]{4,24})_c[A-Za-z0-9]{20,64}$`)
)

// tenantStackRef is the compile-time identity every v2 tenant stack boots with
// (`platform.BootStackRef`). It is a CONSTANT there on purpose: nothing a
// request can reach may choose the value the stack checks itself against.
//
// The consequence for this file is the whole reason it was rewritten. On the v1
// line a key spelled its own Environment (`pb_<ref>_c…`) and the ref segment was
// an independent witness of the binding. On the v2 line the segment names the
// STACK, and the Environment is the stack's ADDRESS — resolved at the edge, not
// carried in the credential. Measured 2026-08-24 against the live plane: every
// one of the 20 tenants holds a `pb_project_c…` key. Demanding the Environment
// ref inside the key therefore asserted something that is false for every v2
// tenant, and `palbase ios link` failed on a key that was entirely correct.
const tenantStackRef = "project"

// IsCanonicalEnvironmentRef reports whether ref is the runtime wire identity
// accepted by DNS, API keys, gateway routing, and control-plane persistence.
func IsCanonicalEnvironmentRef(ref string) bool {
	return canonicalEnvironmentRefPattern.MatchString(ref)
}

// ValidateRuntimeBinding proves that a response and its publishable key both
// belong to the exact Environment selected by the caller. It never includes
// the credential itself in an error.
//
// The key's stack segment is allowed to be exactly one of two things: the
// selected Environment (v1 and self-host, where a stack serves one Environment
// and the two names coincide) or the v2 tenant-stack constant. A key minted for
// some OTHER named stack is still rejected — the check is narrowed to the one
// shape the platform actually mints, not opened to any well-formed key.
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
	if parts[1] != expectedRef && parts[1] != tenantStackRef {
		return fmt.Errorf("publishable API key is bound to stack %q, which is neither the selected environment %q nor a tenant stack", parts[1], expectedRef)
	}
	return nil
}
