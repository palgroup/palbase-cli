package authadmin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// palbase-cli#7 §1: both of this package's refusal readers keep the request id.
func TestAuthRefusalsNameTheirRequestID(t *testing.T) {
	const env = `{"error":"conflict","error_description":"settings moved","status":409,"request_id":"req_TENANT_9"}`

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	rest := &fakeREST{status: http.StatusConflict, answer: env}
	err := call(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }}, cmd, http.MethodGet, base+"/settings", nil)
	if err == nil || !strings.Contains(err.Error(), "req_TENANT_9") || !strings.Contains(err.Error(), "settings moved") {
		t.Fatalf("call refusal = %v — the request id and the stack's sentence must both reach the person", err)
	}

	err = socialError(http.StatusConflict, []byte(env))
	if err == nil || !strings.Contains(err.Error(), "req_TENANT_9") || !strings.Contains(err.Error(), "settings moved") {
		t.Fatalf("social refusal = %v — the request id and the stack's sentence must both reach the person", err)
	}
}
