package versions

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

// A REFUSAL KEEPS ITS REQUEST ID (palbase-cli#7 §1). The envelope carries it and
// it is the one thread that ties a failure a person reports to the stack's log.
func TestARefusalFromTheStackNamesItsRequestID(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	rest := refusingREST{404, `{"error":"not_found","error_description":"no counts yet","status":404,"request_id":"req_TENANT_7"}`}
	_, err := call(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }}, cmd, currentPath)
	if err == nil || !strings.Contains(err.Error(), "req_TENANT_7") || !strings.Contains(err.Error(), "no counts yet") {
		t.Fatalf("refusal = %v — the request id and the stack's sentence must both reach the person", err)
	}
}
