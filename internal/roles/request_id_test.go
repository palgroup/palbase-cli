package roles

import (
	"strings"
	"testing"
)

// palbase-cli#7 §1: the roles refusal keeps the request id the envelope carries.
func TestARolesRefusalNamesItsRequestID(t *testing.T) {
	got := describe([]byte(`{"error":"forbidden","error_description":"not yours","status":403,"request_id":"req_TENANT_10"}`))
	if !strings.Contains(got, "req_TENANT_10") || !strings.Contains(got, "not yours") {
		t.Fatalf("describe = %q — the request id and the stack's sentence must both reach the person", got)
	}
}
