package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestPushRuntimeReportsObservedStateSeparatelyFromCodeActivation(t *testing.T) {
	for _, tc := range []struct {
		name, built, running, want string
		err                        error
	}{
		{"matched", "36.0.2", "36.0.2", "verified @palbase/backend 36.0.2", nil},
		{"old runtime", "36.0.2", "33.0.2", "image migration is NOT complete", nil},
		{"patch mismatch", "36.0.3", "36.0.2", "image migration is NOT complete", nil},
		{"unknown", "36.0.2", "", "image migration is unconfirmed", nil},
		{"failed probe", "36.0.2", "", "image migration is unconfirmed", errors.New("unreachable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			writePushRuntime(&out, tc.built, tc.running, tc.err)
			if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), "nothing was deployed") {
				t.Fatal(out.String())
			}
		})
	}
}
