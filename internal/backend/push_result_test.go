package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestUnchangedPushDoesNotRegenerateClients(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		refresh    bool
	}{
		{"unchanged", `{"digest":"abcdef0123456789","endpoint_count":46,"unchanged":true,"schema":{"changed":false}}`, false},
		{"new release", `{"digest":"abcdef0123456789","endpoint_count":46,"schema":{"changed":false}}`, true},
		{"schema changed", `{"digest":"abcdef0123456789","endpoint_count":46,"schema":{"changed":true,"summary":["added a column"]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result pushResult
			if err := json.Unmarshal([]byte(tc.wire), &result); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			refreshed := false
			err := finishStackPush(context.Background(), &out, result, func(context.Context, io.Writer) error { refreshed = true; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if refreshed != tc.refresh {
				t.Fatalf("refreshed=%v, want %v", refreshed, tc.refresh)
			}
			if !tc.refresh && !strings.Contains(out.String(), "already live: abcdef012345 — no changes to push") {
				t.Fatal(out.String())
			}
		})
	}
}
