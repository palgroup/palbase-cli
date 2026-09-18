package egress

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type refusingREST struct {
	status int
	body   string
}

func (f refusingREST) Do(context.Context, string, string, []byte) (int, []byte, error) {
	return f.status, []byte(f.body), nil
}

// palbase-cli#7 §1: the fence's refusal keeps the request id the envelope carries.
func TestAFenceRefusalNamesItsRequestID(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := write(refusingREST{400, `{"error":"bad_request","error_description":"not a host","status":400,"request_id":"req_TENANT_8"}`}, cmd, fence{})
	if err == nil || !strings.Contains(err.Error(), "req_TENANT_8") || !strings.Contains(err.Error(), "not a host") {
		t.Fatalf("refusal = %v — the request id and the stack's sentence must both reach the person", err)
	}
}
