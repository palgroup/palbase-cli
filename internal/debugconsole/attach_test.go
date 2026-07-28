package debugconsole

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/palgroup/palbase-cli/internal/studio"
)

// The frames below are REAL bytes, not hand-authored shapes:
//
//   - the server-produced ones were emitted by palsvc's own encoder
//     (modules/palsvc/internal/modules/rt EncodeReply / EncodeBroadcast), which
//     is what a live attach actually receives;
//   - the client-produced shapes are the ones RealtimeConnection.swift builds
//     (sendJoin / sendHeartbeat), which is what palsvc's DecodeFrame accepts
//     today from every shipped SDK.
//
// So a drift on EITHER side of the boundary fails here rather than on a live
// device — the failure mode where both sides are unit-green and share one wrong
// assumption is exactly what this locks out.
const (
	goldenTopic = "debug:todoappm8p6zm:018f4c2a-6b1d-7a3e-9c55-2b7f0e1d4a88"

	// palsvc EncodeReply(joinRef:"1", ref:"1", status:"ok").
	serverReplyOK = `["1","1","realtime:debug:todoappm8p6zm:018f4c2a-6b1d-7a3e-9c55-2b7f0e1d4a88","phx_reply",{"status":"ok","response":{}}]`
	// palsvc EncodeReply(status:"error", response:{"reason":...}).
	serverReplyErr = `["1","1","realtime:debug:todoappm8p6zm:018f4c2a-6b1d-7a3e-9c55-2b7f0e1d4a88","phx_reply",{"status":"error","response":{"reason":%q}}]`
	// palsvc's heartbeat ack rides its own pseudo-topic, with a null join_ref.
	serverReplyHeartbeat = `[null,"2","phoenix","phx_reply",{"status":"ok","response":{}}]`
	// palsvc EncodeBroadcast(event:"record", payload:<the console envelope>).
	serverBroadcast = `["1",null,"realtime:debug:todoappm8p6zm:018f4c2a-6b1d-7a3e-9c55-2b7f0e1d4a88","broadcast",{"type":"broadcast","event":"record","payload":%s}]`
)

// MARK: - Frames out (what palsvc must be able to decode)

func TestJoinFrameIsTheSubscriberShape(t *testing.T) {
	got, err := joinFrame(1, goldenTopic, "K7M4P2QX")
	if err != nil {
		t.Fatal(err)
	}
	// Slot-for-slot the shape RealtimeConnection.sendJoin emits — join_ref and
	// ref are the SAME stringified int, the topic carries the "realtime:"
	// prefix — with the one contract-mandated difference: where the device
	// sends its access_token, a viewer sends only the pairing code.
	want := `["1","1","realtime:debug:todoappm8p6zm:018f4c2a-6b1d-7a3e-9c55-2b7f0e1d4a88","phx_join",{"debug_code":"K7M4P2QX"}]`
	if string(got) != want {
		t.Errorf("join frame\n got: %s\nwant: %s", got, want)
	}
	// A viewer must not be able to present itself as the publisher.
	if bytes.Contains(got, []byte("access_token")) {
		t.Error("the subscriber join frame carries access_token — a viewer must join by code alone")
	}
}

func TestHeartbeatFrameIsTheSDKShape(t *testing.T) {
	got, err := heartbeatFrame(2)
	if err != nil {
		t.Fatal(err)
	}
	// RealtimeConnection.sendHeartbeat: [null, "<ref>", "phoenix", "heartbeat", {}]
	want := `[null,"2","phoenix","heartbeat",{}]`
	if string(got) != want {
		t.Errorf("heartbeat frame\n got: %s\nwant: %s", got, want)
	}
}

// Frames this CLI sends must survive the decode palsvc actually performs: a
// 5-slot array whose topic and event slots are strings. Replicated here rather
// than calling decodeFrame, so a matching bug in our own decoder cannot hide a
// broken encoder.
func TestOutboundFramesSatisfyPhoenixSlotContract(t *testing.T) {
	join, err := joinFrame(7, goldenTopic, "K7M4P2QX")
	if err != nil {
		t.Fatal(err)
	}
	beat, err := heartbeatFrame(8)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name              string
		frame             []byte
		wantTopic, wantEv string
	}{
		{"join", join, "realtime:" + goldenTopic, "phx_join"},
		{"heartbeat", beat, "phoenix", "heartbeat"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			topic, event := phoenixSlots(t, tc.frame)
			if topic != tc.wantTopic || event != tc.wantEv {
				t.Errorf("slots = (%q, %q), want (%q, %q)", topic, event, tc.wantTopic, tc.wantEv)
			}
		})
	}
}

// phoenixSlots parses a frame the way palsvc's DecodeFrame does.
func phoenixSlots(t *testing.T, frame []byte) (topic, event string) {
	t.Helper()
	var slots []json.RawMessage
	if err := json.Unmarshal(frame, &slots); err != nil {
		t.Fatalf("not a JSON array: %v (%s)", err, frame)
	}
	if len(slots) != 5 {
		t.Fatalf("got %d slots, Phoenix v2 is exactly 5: %s", len(slots), frame)
	}
	if err := json.Unmarshal(slots[2], &topic); err != nil {
		t.Fatalf("topic slot is not a string: %s", frame)
	}
	if err := json.Unmarshal(slots[3], &event); err != nil {
		t.Fatalf("event slot is not a string: %s", frame)
	}
	return topic, event
}

// MARK: - Frames in

func TestDecodeFrame(t *testing.T) {
	tests := []struct {
		name              string
		raw               string
		wantOK            bool
		wantTopic, wantEv string
	}{
		{"server reply ok", serverReplyOK, true, "realtime:" + goldenTopic, "phx_reply"},
		{"server heartbeat ack", serverReplyHeartbeat, true, "phoenix", "phx_reply"},
		{"server broadcast", strings.Replace(serverBroadcast, "%s", `{"schemaVersion":1}`, 1), true,
			"realtime:" + goldenTopic, "broadcast"},
		{"not an array", `{"topic":"x"}`, false, "", ""},
		{"four slots", `["1","1","realtime:x","phx_reply"]`, false, "", ""},
		{"six slots", `["1","1","realtime:x","phx_reply",{},null]`, false, "", ""},
		{"non-string topic", `["1","1",42,"phx_reply",{}]`, false, "", ""},
		{"non-string event", `["1","1","realtime:x",42,{}]`, false, "", ""},
		{"torn frame", `["1","1","realt`, false, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := decodeFrame([]byte(tc.raw))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if f.Topic != tc.wantTopic || f.Event != tc.wantEv {
				t.Errorf("got (%q, %q), want (%q, %q)", f.Topic, f.Event, tc.wantTopic, tc.wantEv)
			}
		})
	}
}

// MARK: - Rejection

// Every reason the contract can send must tell the reader what to DO. A bare
// "forbidden" on a terminal costs someone an afternoon.
func TestRejectionsAreTerminalAndActionable(t *testing.T) {
	tests := []struct {
		reason    string
		wantWords []string // the actionable part, not the whole sentence
	}{
		// At join time this is a session that STOPPED, so it must not read as a
		// bad code — that is resolve-time's meaning.
		{"unknown_session", []string{"the session ended", "disarmed", "Arm again"}},
		{"expired", []string{"time ran out", "Arm again"}},
		{"invalid_code", []string{"does not match", "case-insensitive", "dash is optional"}},
		{"not_publisher", []string{"never joins as a publisher", "Arm again", "report this"}},
		{"forbidden", []string{"may watch and nothing else", "palbase status", "--environment"}},
		// Not in the contract's list: palsvc's join authorizer replies
		// "unauthorized" today, and a reason invented later must still land
		// somewhere useful rather than printing bare.
		{"unauthorized", []string{"unauthorized", "armed and streaming"}},
		{"", []string{"no reason given"}},
	}
	for _, tc := range tests {
		t.Run("reason="+tc.reason, func(t *testing.T) {
			raw := []byte(fmtReplyErr(tc.reason))
			err := handleFrame(raw, &bytes.Buffer{}, &bytes.Buffer{}, false, false, map[int]bool{})

			var rej rejected
			if !errors.As(err, &rej) {
				t.Fatalf("a rejected join must END the attach, got err = %v", err)
			}
			if rej.reason != tc.reason {
				t.Errorf("reason = %q, want %q", rej.reason, tc.reason)
			}
			for _, word := range tc.wantWords {
				if !strings.Contains(rej.Error(), word) {
					t.Errorf("message is missing %q — it must say what to do next:\n%s", word, rej.Error())
				}
			}
		})
	}
}

func TestOKRepliesAndHeartbeatAcksAreNotRejections(t *testing.T) {
	for _, raw := range []string{serverReplyOK, serverReplyHeartbeat} {
		if err := handleFrame([]byte(raw), &bytes.Buffer{}, &bytes.Buffer{}, false, false, map[int]bool{}); err != nil {
			t.Errorf("%s\n ended the attach: %v", raw, err)
		}
	}
}

// A rejection must not be retried: the same code presented again gets the same
// answer, and a viewer staring at a silent terminal being refused every two
// seconds is worse than an error.
func TestRunStopsOnRejectionAndRetriesTransportFailures(t *testing.T) {
	shrinkBackoff(t)

	t.Run("rejection is terminal", func(t *testing.T) {
		var joins int
		url := wsTestServer(t, func(ctx context.Context, c *websocket.Conn) {
			joins++
			readJoin(t, ctx, c)
			writeFrame(t, ctx, c, fmtReplyErr("invalid_code"))
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		err := run(ctx, &bytes.Buffer{}, &bytes.Buffer{}, url, goldenTopic, "K7M4P2QX", false, false)
		var rej rejected
		if !errors.As(err, &rej) {
			t.Fatalf("run() = %v, want a rejected", err)
		}
		if joins != 1 {
			t.Errorf("joined %d times — a refused code must be presented exactly once", joins)
		}
	})

	t.Run("a dropped socket reconnects", func(t *testing.T) {
		conns := make(chan struct{}, 8)
		url := wsTestServer(t, func(ctx context.Context, c *websocket.Conn) {
			conns <- struct{}{}
			readJoin(t, ctx, c)
			// Drop without replying: a transport failure, not a refusal.
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			_ = run(ctx, &bytes.Buffer{}, &bytes.Buffer{}, url, goldenTopic, "K7M4P2QX", false, false)
		}()

		for i := 0; i < 2; i++ {
			select {
			case <-conns:
			case <-time.After(5 * time.Second):
				t.Fatalf("only %d connection(s) — a dropped socket must reconnect", i)
			}
		}
	})
}

// MARK: - Schema version

func TestForeignSchemaVersionIsReportedNeverCoerced(t *testing.T) {
	// A v2 record whose `message` shape this CLI has no business reading.
	foreign := `{"schemaVersion":2,"message":{"level":5,"message":"cart is empty","createdAt":1785172985075.425}}`

	t.Run("rendered output withholds it and says why", func(t *testing.T) {
		var out, errOut bytes.Buffer
		warned := map[int]bool{}
		printRecord(&out, &errOut, []byte(foreign), false, false, false, warned)

		if out.Len() != 0 {
			t.Errorf("rendered a schemaVersion=2 record through the v1 formatter:\n%s", out.String())
		}
		for _, want := range []string{"schemaVersion=2", "renders 1", "NOT being rendered"} {
			if !strings.Contains(errOut.String(), want) {
				t.Errorf("report is missing %q:\n%s", want, errOut.String())
			}
		}
	})

	t.Run("reported once per version, not per record", func(t *testing.T) {
		var out, errOut bytes.Buffer
		warned := map[int]bool{}
		for i := 0; i < 5; i++ {
			printRecord(&out, &errOut, []byte(foreign), false, false, false, warned)
		}
		if got := strings.Count(errOut.String(), "schemaVersion=2"); got != 1 {
			t.Errorf("reported %d times — repeating it buries the records that DO render", got)
		}
	})

	t.Run("--json still emits the bytes verbatim", func(t *testing.T) {
		var out, errOut bytes.Buffer
		printRecord(&out, &errOut, []byte(foreign), false, true, false, map[int]bool{})
		if strings.TrimSpace(out.String()) != foreign {
			t.Errorf("--json interprets nothing, so the record must pass through unchanged:\n got: %s\nwant: %s",
				out.String(), foreign)
		}
		if !strings.Contains(errOut.String(), "schemaVersion=2") {
			t.Error("the mismatch must still be reported on --json")
		}
	})

	t.Run("v1 renders", func(t *testing.T) {
		var out, errOut bytes.Buffer
		printRecord(&out, &errOut, []byte(realMessageLine), false, false, false, map[int]bool{})
		if !strings.Contains(out.String(), "cart is empty") {
			t.Errorf("a v1 record must render:\n%s", out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("v1 must not be reported as a mismatch: %s", errOut.String())
		}
	})
}

// MARK: - Pairing code

func TestNormalizeCode(t *testing.T) {
	tests := []struct {
		name, in, want, wantErr string
	}{
		{name: "dashed as spoken", in: "K7M4-P2QX", want: "K7M4P2QX"},
		{name: "undashed", in: "K7M4P2QX", want: "K7M4P2QX"},
		{name: "lower case", in: "k7m4-p2qx", want: "K7M4P2QX"},
		{name: "spaces and padding", in: "  K7M4 P2QX ", want: "K7M4P2QX"},
		// Crockford excludes I, L and O, so these letters can never appear in a
		// real code — folding them to the digit they were misheard from cannot
		// corrupt one, and rescues a code read aloud over a call.
		{name: "misheard I is 1", in: "K7M4-P2QI", want: "K7M4P2Q1"},
		{name: "misheard L is 1", in: "K7M4-P2QL", want: "K7M4P2Q1"},
		{name: "misheard O is 0", in: "K7M4-P2QO", want: "K7M4P2Q0"},
		// U is excluded too but maps to no digit, so it stays a plain typo.
		{name: "U is a typo", in: "K7M4-P2QU", wantErr: `"U" is not one of its characters`},
		{name: "punctuation", in: "K7M4_P2QX", wantErr: `"_" is not one of its characters`},
		{name: "too short", in: "K7M4-P2Q", wantErr: "expected 8 characters, got 7"},
		{name: "too long", in: "K7M4-P2QXZ", wantErr: "expected 8 characters, got 9"},
		{name: "empty", in: "", wantErr: "expected 8 characters, got 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeCode(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("normalizeCode(%q) = %q, want an error", tc.in, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error is missing %q:\n%v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeCode(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("normalizeCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTopicAndURL(t *testing.T) {
	if got, want := topicFor("todoappm8p6zm", "018f4c2a"), "debug:todoappm8p6zm:018f4c2a"; got != want {
		t.Errorf("topicFor = %q, want %q", got, want)
	}
	got := websocketURL("todoappm8p6zm", "dev.palbase.studio")
	want := "wss://todoappm8p6zm.dev.palbase.studio/realtime/v1/websocket?vsn=2.0.0"
	if got != want {
		t.Errorf("websocketURL = %q, want %q", got, want)
	}
	// The pairing code is the credential; it must never ride a URL, which is
	// where palsvc's own comment says tokens leak from (proxy + access logs).
	if strings.Contains(got, "apikey") || strings.Contains(got, "code") {
		t.Errorf("the handshake URL carries a credential: %s", got)
	}
}

// MARK: - Resolving a code to a session (contract §1b)

// fakeStudio records one tRPC call. It implements Mutation and NOT Query, so a
// future change that puts the pairing code in a URL fails to compile.
type fakeStudio struct {
	path  string
	input any
	out   string // session_id to hand back
	err   error
}

// Query is present only to satisfy the interface: `attach` must never resolve a
// code through it, because tRPC puts a query's input in the URL.
func (f *fakeStudio) Query(_ context.Context, path string, _ any, _ any) error {
	f.path = path
	return errors.New("attach must not put the pairing code in a URL")
}

func (f *fakeStudio) Mutation(_ context.Context, path string, input any, out any) error {
	f.path, f.input = path, input
	if f.err != nil {
		return f.err
	}
	target, ok := out.(*struct {
		SessionID string `json:"session_id"`
	})
	if !ok {
		return errors.New("unexpected out type")
	}
	target.SessionID = f.out
	return nil
}

func TestResolveSession(t *testing.T) {
	tests := []struct {
		name      string
		studio    *fakeStudio
		wantID    string
		wantWords []string
	}{
		{
			name:   "resolved",
			studio: &fakeStudio{out: "018f4c2a-6b1d-7a3e-9c55-2b7f0e1d4a88"},
			wantID: "018f4c2a-6b1d-7a3e-9c55-2b7f0e1d4a88",
		},
		// Studio's reason passes through untouched. `unknown_session` and
		// `expired` need different fixes, so collapsing them into one friendly
		// sentence is the failure mode to avoid.
		{
			name:      "no such session",
			studio:    &fakeStudio{err: errors.New("unknown_session")},
			wantWords: []string{"debug.resolveSession", "unknown_session"},
		},
		{
			name:      "expired",
			studio:    &fakeStudio{err: errors.New("expired")},
			wantWords: []string{"debug.resolveSession", "expired"},
		},
		{
			name:      "answered without a session id",
			studio:    &fakeStudio{out: ""},
			wantWords: []string{"without a session_id"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSession(context.Background(), tc.studio, "todoappm8p6zm", "K7M4P2QX")

			if len(tc.wantWords) > 0 {
				if err == nil {
					t.Fatalf("resolveSession = %q, want an error", got)
				}
				for _, word := range tc.wantWords {
					if !strings.Contains(err.Error(), word) {
						t.Errorf("error is missing %q:\n%v", word, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSession: %v", err)
			}
			if got != tc.wantID {
				t.Errorf("session id = %q, want %q", got, tc.wantID)
			}
		})
	}
}

// Studio validates membership on the Environment it is told about, so the ref
// has to be in the call — and the code has to be the NORMALIZED one, because a
// server that normalizes again still cannot repair a code we mangled.
func TestResolveSendsTheRefAndTheNormalizedCode(t *testing.T) {
	studio := &fakeStudio{out: "s1"}
	code, err := normalizeCode("k7m4-p2qx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSession(context.Background(), studio, "todoappm8p6zm", code); err != nil {
		t.Fatal(err)
	}

	if studio.path != "debug.resolveSession" {
		t.Errorf("tRPC path = %q", studio.path)
	}
	input, ok := studio.input.(map[string]any)
	if !ok {
		t.Fatalf("input is %T, want a map", studio.input)
	}
	if input["ref"] != "todoappm8p6zm" {
		t.Errorf("ref = %v — Studio checks membership on the selected Environment", input["ref"])
	}
	if input["code"] != "K7M4P2QX" {
		t.Errorf("code = %v, want the normalized form", input["code"])
	}
}

// Resolution is authenticated, so an unwired Studio must fail HERE with an
// instruction, not as a nil dereference.
func TestResolveWithoutAStudioSaysWhatToDo(t *testing.T) {
	_, err := resolveSession(context.Background(), nil, "todoappm8p6zm", "K7M4P2QX")
	if err == nil || !strings.Contains(err.Error(), "palbase login") {
		t.Errorf("err = %v, want it to point at `palbase login`", err)
	}
}

// Studio sets the tRPC message to palsvc's reason VERBATIM so this CLI can tell
// the failures apart. That promise spans three hops — palsvc's reason, Studio's
// error envelope, studio.Client's rendering — and every one of them is a place
// someone could "tidy" the reason away. So drive the REAL client against the
// REAL superjson envelope Studio emits, and assert on the string a user reads.
func TestStudioErrorReasonsSurviveToTheTerminal(t *testing.T) {
	tests := []struct {
		name, reason, trpcCode string
		status                 int
	}{
		{"no such session", "unknown_session", "NOT_FOUND", 404},
		// tRPC has no GONE, so expired travels as PRECONDITION_FAILED — the
		// precondition being a live session.
		{"expired", "expired", "PRECONDITION_FAILED", 412},
		{"rate limited", "rate_limited", "TOO_MANY_REQUESTS", 429},
	}

	seen := map[string]bool{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				// tRPC + superjson: the real fields nest under `json`.
				_, _ = fmt.Fprintf(w,
					`{"error":{"json":{"message":%q,"code":-32001,"data":{"code":%q,"httpStatus":%d}}}}`,
					tc.reason, tc.trpcCode, tc.status)
			}))
			defer srv.Close()

			_, err := resolveSession(context.Background(),
				studio.New(srv.URL, nil, nil), "todoappm8p6zm", "K7M4P2QX")
			if err == nil {
				t.Fatal("want an error")
			}
			for _, want := range []string{"debug.resolveSession", tc.trpcCode, tc.reason} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message a user reads is missing %q:\n%s", want, err)
				}
			}
			if seen[err.Error()] {
				t.Errorf("this failure is indistinguishable from an earlier one: %s", err)
			}
			seen[err.Error()] = true
		})
	}
}

// MARK: - Attaching

// palsvc fans out and forgets (§7), so silence after a successful join is
// normal. Saying nothing at all would make "attached and idle" look identical
// to "still connecting".
func TestSuccessfulJoinSaysItIsAttachedAndThatNothingReplays(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := handleFrame([]byte(serverReplyOK), &out, &errOut, false, false, map[int]bool{}); err != nil {
		t.Fatalf("an ok reply must not end the attach: %v", err)
	}
	for _, want := range []string{"attached", "Nothing is replayed"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("join confirmation is missing %q:\n%s", want, errOut.String())
		}
	}
	// It is a status line, not data — piping stdout to a file must not collect it.
	if out.Len() != 0 {
		t.Errorf("the join confirmation went to stdout, where records live:\n%s", out.String())
	}
	// The heartbeat acks constantly; it must not narrate.
	var beatOut bytes.Buffer
	if err := handleFrame([]byte(serverReplyHeartbeat), &bytes.Buffer{}, &beatOut, false, false, map[int]bool{}); err != nil {
		t.Fatal(err)
	}
	if beatOut.Len() != 0 {
		t.Errorf("a heartbeat ack printed something — every 25s:\n%s", beatOut.String())
	}
}

// `tail` reads files off this machine. It must keep working with NO Studio, NO
// credential and NO selected environment — otherwise the one command that
// works offline quietly grows a network dependency.
func TestTailNeedsNoTransport(t *testing.T) {
	debug := Cmd(Resolvers{}) // every resolver nil
	var out bytes.Buffer
	debug.SetOut(&out)
	debug.SetErr(&out)
	// SetArgs is load-bearing: without it cobra falls back to os.Args, which
	// under `go test` is the test binary's own flags — the command would fail
	// on argument parsing and never reach RunE, and this test would pass
	// without proving anything.
	//
	// --device short-circuits the `xcrun simctl list devices` shell-out (which
	// costs ~10s and is not what this test is about); RunE still runs, so the
	// nil-resolver check below is unaffected.
	debug.SetArgs([]string{"tail", "--device", "no-such-simulator"})

	var err error
	var ran bool
	// Nil resolvers must not be reachable from here: a panic IS the regression.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("tail reached a nil resolver: %v", r)
			}
		}()
		err = debug.Execute()
		ran = true
	}()
	if !ran {
		t.Fatal("tail never ran")
	}

	// It may well fail — there is no simulator in CI — but only ever about
	// console data, never about a credential or an environment.
	if err != nil {
		for _, forbidden := range []string{"credential", "environment", "login", "not selected"} {
			if strings.Contains(strings.ToLower(err.Error()), forbidden) {
				t.Errorf("tail failed for a transport reason (%q):\n%v", forbidden, err)
			}
		}
	}
}

// MARK: - The whole path

// Dial, join, get acked, receive a record, print it. The one test where the
// bytes actually cross a socket.
func TestAttachStreamsRecordsOverARealSocket(t *testing.T) {
	url := wsTestServer(t, func(ctx context.Context, c *websocket.Conn) {
		join := readJoin(t, ctx, c)

		topic, event := phoenixSlots(t, join)
		if topic != "realtime:"+goldenTopic || event != "phx_join" {
			t.Errorf("join = (%q, %q)", topic, event)
		}
		var slots []json.RawMessage
		if err := json.Unmarshal(join, &slots); err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Code string `json:"debug_code"`
		}
		if err := json.Unmarshal(slots[4], &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Code != "K7M4P2QX" {
			t.Errorf("debug_code = %q, want the normalized code", payload.Code)
		}

		writeFrame(t, ctx, c, serverReplyOK)
		writeFrame(t, ctx, c, strings.Replace(serverBroadcast, "%s", realNetworkLine, 1))
		writeFrame(t, ctx, c, strings.Replace(serverBroadcast, "%s", realMessageLine, 1))
	})

	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The server closes after the last frame; stream() surfaces that as the
	// read error that ends this call.
	_ = stream(ctx, &out, &errOut, url, goldenTopic, "K7M4P2QX", false, false, map[int]bool{})

	for _, want := range []string{"POST", "401", "/auth/login", "cart is empty"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("streamed output is missing %q:\n%s", want, out.String())
		}
	}
}

// `attach` and `tail` carry the SAME envelope, so they must produce the SAME
// text. Two renderers for one wire is how they start disagreeing about what a
// 401 looks like.
func TestAttachAndTailRenderIdentically(t *testing.T) {
	for _, mode := range []struct {
		name               string
		errorsOnly, asJSON bool
	}{
		{"rendered", false, false},
		{"errors only", true, false},
		{"json", false, true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			tailed, _ := render(t, writeSession(t, realMessageLine, realNetworkLine, successNetworkLine),
				0, 0, mode.errorsOnly, mode.asJSON)

			var attached bytes.Buffer
			warned := map[int]bool{}
			for _, line := range []string{realMessageLine, realNetworkLine, successNetworkLine} {
				printRecord(&attached, &bytes.Buffer{}, []byte(line), mode.errorsOnly, mode.asJSON, false, warned)
			}
			if attached.String() != tailed {
				t.Errorf("attach and tail disagree:\nattach:\n%s\ntail:\n%s", attached.String(), tailed)
			}
		})
	}
}

// MARK: - Helpers

func fmtReplyErr(reason string) string {
	quoted, err := json.Marshal(reason)
	if err != nil {
		panic(err)
	}
	return strings.Replace(serverReplyErr, "%q", string(quoted), 1)
}

// shrinkBackoff makes the reconnect loop test-fast.
func shrinkBackoff(t *testing.T) {
	t.Helper()
	initial, max := initialBackoff, maxBackoff
	initialBackoff, maxBackoff = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { initialBackoff, maxBackoff = initial, max })
}

// wsTestServer runs `handle` for each accepted connection and returns its ws:// URL.
func wsTestServer(t *testing.T, handle func(context.Context, *websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		handle(r.Context(), c)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func readJoin(t *testing.T, ctx context.Context, c *websocket.Conn) []byte {
	t.Helper()
	_, raw, err := c.Read(ctx)
	if err != nil {
		return nil // the client went away; the test's own assertions report it
	}
	return raw
}

func writeFrame(t *testing.T, ctx context.Context, c *websocket.Conn, frame string) {
	t.Helper()
	if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Errorf("write %s: %v", frame, err)
	}
}
