package backend

import "regexp"

var projectRefPattern = regexp.MustCompile(`^[a-z0-9]{4,24}$`)

// isCanonicalProjectRef validates the cloud ref before using it as a tenant host.
func isCanonicalProjectRef(ref string) bool {
	return projectRefPattern.MatchString(ref)
}
