package debugconsole

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// historyStudio answers one debug.history query and records what it was asked.
type historyStudio struct {
	path    string
	input   map[string]any
	records []string
	err     error
}

func (h *historyStudio) Query(_ context.Context, path string, input any, out any) error {
	h.path = path
	if m, ok := input.(map[string]any); ok {
		h.input = m
	}
	if h.err != nil {
		return h.err
	}
	resp, ok := out.(*historyResponse)
	if !ok {
		return errors.New("unexpected out type")
	}
	for _, r := range h.records {
		resp.Records = append(resp.Records, json.RawMessage(r))
	}
	return nil
}

// Mutation exists only to satisfy the interface — `history` is a read.
func (h *historyStudio) Mutation(_ context.Context, path string, _ any, _ any) error {
	h.path = path
	return errors.New("history must not mutate")
}

func runHistory(t *testing.T, studio Studio, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	resolver := selectiontest.New(t).Resolver()
	// Stands in for the global --project flag; `history` resolves the selected
	// Environment the same way every other context-bound command does.
	resolver.ProjectFlag = "proj_1"
	debug := Cmd(Resolvers{
		Studio:    func() Studio { return studio },
		Selection: func() *selection.Resolver { return resolver },
	})
	debug.SetOut(&out)
	debug.SetErr(&errOut)
	debug.SilenceErrors, debug.SilenceUsage = true, true
	debug.SetArgs(append([]string{"history"}, args...))
	err := debug.Execute()
	return out.String(), errOut.String(), err
}

// Exactly one of --user / --session: neither is a query nobody can answer, both
// is two different questions.
func TestHistoryRequiresExactlyOneSubject(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"neither", nil},
		{"both", []string{"--user", "u_42", "--session", "018f4c2a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runHistory(t, &historyStudio{}, tc.args...)
			if err == nil {
				t.Fatal("want an error")
			}
			for _, want := range []string{"exactly one", "--user", "--session"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error is missing %q:\n%v", want, err)
				}
			}
		})
	}
}

// An empty result has several causes with different fixes. A developer who
// cannot tell "nothing happened" from "we did not keep it" chases the wrong one.
func TestHistoryEmptyResultSaysWhyItMightBeEmpty(t *testing.T) {
	_, errOut, err := runHistory(t, &historyStudio{}, "--user", "u_42", "--since", "24h")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"no records in the last 24h",
		"on a signal", // uploads are triggered, so empty usually means healthy
		"switched on", // the per-project switch
		"far enough",  // the window
		"retention",   // the record aged out
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("empty message is missing %q:\n%s", want, errOut)
		}
	}
}

// --json must emit the SAME envelope `attach --json` does, so one jq works
// across both. Anything reshaped here forks the CLI's decoder in practice.
func TestHistoryJSONIsTheSameEnvelopeAsAttach(t *testing.T) {
	studio := &historyStudio{records: []string{realNetworkLine, realMessageLine}}
	out, _, err := runHistory(t, studio, "--user", "u_42", "--json")
	if err != nil {
		t.Fatal(err)
	}

	var viaAttach bytes.Buffer
	warned := map[int]bool{}
	for _, line := range []string{realNetworkLine, realMessageLine} {
		printRecord(&viaAttach, &bytes.Buffer{}, []byte(line), false, true, false, warned)
	}
	if out != viaAttach.String() {
		t.Errorf("history --json and attach --json disagree:\nhistory:\n%s\nattach:\n%s", out, viaAttach.String())
	}
}

func TestHistorySendsTheSubjectAndWindow(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want map[string]any
	}{
		{"by user", []string{"--user", "u_42", "--since", "24h"},
			map[string]any{"user": "u_42", "since": "24h"}},
		{"by session", []string{"--session", "018f4c2a"},
			map[string]any{"session": "018f4c2a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			studio := &historyStudio{}
			if _, _, err := runHistory(t, studio, tc.args...); err != nil {
				t.Fatal(err)
			}
			if studio.path != "debug.history" {
				t.Errorf("tRPC path = %q", studio.path)
			}
			for key, want := range tc.want {
				if studio.input[key] != want {
					t.Errorf("input[%q] = %v, want %v", key, studio.input[key], want)
				}
			}
			// The subject not asked for must not be sent at all — an empty
			// string is a filter that matches nothing, not an absent filter.
			for _, key := range []string{"user", "session"} {
				if _, sent := tc.want[key]; !sent {
					if _, present := studio.input[key]; present {
						t.Errorf("input carries an empty %q", key)
					}
				}
			}
		})
	}
}

// MARK: - Bodies

// Bodies are the whole point of history: metadata says WHICH request failed,
// the body says why. Each of the states a body can be in has to read
// differently — "here it is", "here is part of it", "it is stored elsewhere",
// "it was not kept" — because they send a reader somewhere different.
func TestBodyText(t *testing.T) {
	inline := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	tests := []struct {
		name      string
		ref       string // JSON for a bodyRef, or "" for no body at all
		want      []string
		wantEmpty bool
	}{
		{
			name: "inline body is shown",
			ref:  fmt.Sprintf(`{"size":17,"truncated":false,"inline":%q,"blobKey":null}`, inline(`{"email":"a@b.c"}`)),
			want: []string{"response", "17B", `{"email":"a@b.c"}`},
		},
		{
			name: "a prefix says how much of it this is",
			ref:  fmt.Sprintf(`{"size":8400,"truncated":true,"inline":%q,"blobKey":null}`, inline("first part")),
			want: []string{"8.2kB", "first 10B of it", "first part"},
		},
		{
			name: "a blob says where the bytes are",
			ref:  `{"size":40960,"truncated":false,"inline":null,"blobKey":"3f2a9c1d8e7b6a5f4c3d2e1f"}`,
			want: []string{"40.0kB", "kept out of line", "not fetched", "3f2a9c1d8e7b"},
		},
		{
			name: "an empty body is not a missing one",
			ref:  `{"size":0,"truncated":false,"inline":null,"blobKey":null}`,
			want: []string{"(empty)"},
		},
		{
			// Sized but neither inline nor keyed: the bytes were dropped. Saying
			// nothing here would read as "the response was empty".
			name: "dropped bytes say they were dropped",
			ref:  `{"size":900,"truncated":false,"inline":null,"blobKey":null}`,
			want: []string{"900B", "not retained"},
		},
		{
			name:      "no body at all prints nothing",
			ref:       "",
			wantEmpty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ref *bodyRef
			if tc.ref != "" {
				ref = &bodyRef{}
				if err := json.Unmarshal([]byte(tc.ref), ref); err != nil {
					t.Fatal(err)
				}
			}
			got := bodyText("response", ref)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("a GET with no request body is not a gap in the record:\n%s", got)
				}
				return
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
		})
	}
}

// history shows bodies; the live views do not, because a multi-line body breaks
// a stream you scan by eye.
func TestOnlyHistoryPrintsBodies(t *testing.T) {
	withBody := fmt.Sprintf(
		`{"schemaVersion":1,"network":{"method":"POST","url":"https://x/y","statusCode":401,"state":2,`+
			`"duration":0.1,"createdAt":1785172993060.5,"responseBody":{"size":9,"truncated":false,"inline":%q,"blobKey":null}}}`,
		base64.StdEncoding.EncodeToString([]byte(`{"e":"no"}`)))

	var live, historic bytes.Buffer
	printRecord(&live, &bytes.Buffer{}, []byte(withBody), false, false, false, map[int]bool{})
	printRecord(&historic, &bytes.Buffer{}, []byte(withBody), false, false, true, map[int]bool{})

	if strings.Contains(live.String(), `"e":"no"`) {
		t.Errorf("attach/tail printed a body:\n%s", live.String())
	}
	if !strings.Contains(historic.String(), `"e":"no"`) {
		t.Errorf("history did not print the body:\n%s", historic.String())
	}
	// Same first line either way — one renderer, one record.
	if strings.SplitN(live.String(), "\n", 2)[0] != strings.SplitN(historic.String(), "\n", 2)[0] {
		t.Errorf("the record line differs between views:\n%s\n%s", live.String(), historic.String())
	}
}
