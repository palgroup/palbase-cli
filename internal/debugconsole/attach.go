// `palbase debug attach <code>` — watch a REAL device's console live, over the
// realtime socket, instead of reading a simulator's files off this disk.
//
// The device arms a session and shows a short pairing code; a human carries the
// code to whoever is watching. Consent therefore lives on the device: there is
// nothing for a viewer to point at until the device offers it. Contract:
// docs/superpowers/specs/2026-07-28-phase5-live-remote-console-wire-contract.md
//
// The records are the SAME envelope `tail` reads off disk, so this file decodes
// nothing and formats nothing — it reuses `record` and `format`. Two renderers
// for one wire is how `attach` and `tail` start disagreeing about what a 401
// looks like.
package debugconsole

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/selection"
)

// schemaVersion is the ONE console envelope version this CLI renders. A record
// that says anything else is REPORTED and left alone — never coerced into the
// v1 shape, because a field that moved between versions would then render as a
// confident wrong value rather than as an error.
const schemaVersion = 1

// Phoenix v2 wire constants, shared with the iOS SDK's RealtimeConnection and
// palsvc's rt codec. Topics carry the prefix on the wire; "phoenix" (the
// heartbeat pseudo-topic) is the one that never does.
const (
	realtimePrefix = "realtime:"
	heartbeatTopic = "phoenix"
	eventJoin      = "phx_join"
	eventReply     = "phx_reply"
	eventHeartbeat = "heartbeat"
	eventBroadcast = "broadcast"
	// recordEvent is the broadcast event name each console record rides under.
	recordEvent = "record"
)

// heartbeatInterval keeps Kong's 180s idle-WS timeout from closing the socket;
// it matches the SDK's cadence. A var so tests can shrink it.
var heartbeatInterval = 25 * time.Second

// maxBackoff caps the reconnect wait. Vars so tests don't sleep.
var (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
)

// readLimit matches the 8 MiB `tail` allows per line — a full-fidelity build
// broadcasts bodies inline, and a record silently dropped for being large is
// exactly the record someone is attached to see.
const readLimit = 8 << 20

// Studio is the tRPC transport subset this package needs.
//
// `attach` resolves a pairing code with Mutation and NOT Query, because tRPC
// puts a query's input in the URL and the code is a capability — the same
// reason §D2 keeps it out of the topic name. `history` reads with Query: its
// inputs (an end user id, a window) are not capabilities.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
	Mutation(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries what `attach` needs from main. `tail` needs none of it — it
// reads this machine's disk.
type Resolvers struct {
	Studio     func() Studio
	Selection  func() *selection.Resolver
	PublicHost func() string
}

func attachCmd(r Resolvers) *cobra.Command {
	var (
		errorsOnly bool
		asJSON     bool
	)

	cmd := &cobra.Command{
		Use:   "attach <code>",
		Short: "Watch a real device's console live, with its pairing code",
		Long: "Streams a device's console to this terminal as it happens — the same\n" +
			"records `palbase debug tail` reads off a simulator, but from a real device\n" +
			"anywhere.\n\n" +
			"The device arms a session and shows an 8-character code; you type it here.\n" +
			"Nothing is stored and nothing is replayed: you see what happens next, not\n" +
			"what you missed. The dash and the letter case do not matter.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := normalizeCode(args[0])
			if err != nil {
				return err
			}

			// TARGET-RELATIVE, like login, push and spec: a checkout linked to a
			// project attaches to THAT project, which resolves the pairing code
			// itself. Without this the command asked our cloud about a code a
			// device on somebody's own machine had shown — and there is nothing
			// there to ask.
			if attached, err := attachToLinkedProject(cmd, code, errorsOnly, asJSON); err != errNoLinkedProject {
				_ = attached
				return err
			}

			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			ref := sel.EnvironmentRef()
			sessionID, err := resolveSession(cmd.Context(), r.Studio(), ref, code)
			if err != nil {
				return err
			}
			topic := topicFor(ref, sessionID)
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s · attaching to %s\n", ref, topic)
			return run(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				websocketURL(ref, r.PublicHost()), topic, code, errorsOnly, asJSON)
		},
	}

	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "only failed requests and error logs")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw records, one JSON object per line")
	return cmd
}

// MARK: - Addressing

// topicFor builds the bare sub-topic (the transport adds the "realtime:"
// prefix). THE ONE PLACE the debug topic's shape lives, so the open question
// about how a viewer learns `session_id` costs one line to settle.
func topicFor(environmentRef, sessionID string) string {
	return "debug:" + environmentRef + ":" + sessionID
}

// tenantOrigin is the Environment's own host.
func tenantOrigin(environmentRef, publicHost string) string {
	return environmentRef + "." + publicHost
}

// websocketURL points at the Environment's realtime endpoint. The handshake
// carries NO credential: palsvc tenants the request by Host and upgrades, then
// authorizes at phx_join against the pairing code (rt/hub.go ServeWS). So the
// code is the only thing a viewer presents, and it never rides in a URL.
func websocketURL(environmentRef, publicHost string) string {
	return "wss://" + tenantOrigin(environmentRef, publicHost) + "/realtime/v1/websocket?vsn=2.0.0"
}

// codeAlphabet is Crockford base32: no I, L, O or U, so a code read aloud over
// a call cannot be misheard.
const (
	codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	codeLength   = 8
)

// normalizeCode turns a code as a human typed it into the form the server
// hashed: dashes and spaces dropped, upper-cased.
//
// It also folds the classic misreads — I and L to 1, O to 0. That cannot
// corrupt a real code: those letters are excluded from the alphabet, so they
// are impossible in one and can only ever be a mishearing of the digit. U is
// excluded too but maps to no digit, so it stays a plain typo.
func normalizeCode(raw string) (string, error) {
	var out strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		switch r {
		case '-', ' ':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		if !strings.ContainsRune(codeAlphabet, r) {
			return "", fmt.Errorf(
				"%q is not a pairing code: %q is not one of its characters (%d characters from %s)",
				raw, string(r), codeLength, codeAlphabet)
		}
		out.WriteRune(r)
	}
	if out.Len() != codeLength {
		return "", fmt.Errorf(
			"%q is not a pairing code: expected %d characters, got %d. The device shows the code when it arms; the dash and the letter case do not matter",
			raw, codeLength, out.Len())
	}
	return out.String(), nil
}

// MARK: - Resolving a code to a session (contract §1b)

// resolveSession turns the code a human carried into the session id the topic
// is keyed by. The device shows ONLY the code — it never displays its session
// id — so this call is the only way a viewer can build the topic.
//
// It goes through Studio, not palsvc, for the same reason `palbase logs` does:
// the viewer is a PERSON, and "is this person allowed to see this Environment"
// is a membership question only Studio can answer. palsvc-rt authenticates
// devices (palauth JWKS) and br-pods (per-tenant HS256) and nothing else.
//
// It is NOT the security boundary. What authorizes the watch is the code,
// verified against its argon2id hash at phx_join; a session id learned here
// buys nothing without it. So resolving and THEN being refused at the join is a
// normal flow, not a contradiction — the join's own reason is what surfaces.
//
// Studio's error is passed through unwrapped: `unknown_session` and `expired`
// have to stay tellable apart, and rewording them here is how two failures
// with different fixes turn into one unhelpful sentence.
func resolveSession(ctx context.Context, studio Studio, environmentRef, code string) (string, error) {
	if studio == nil {
		return "", errors.New("not authenticated — run `palbase login`")
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	input := map[string]any{"ref": environmentRef, "code": code}
	if err := studio.Mutation(ctx, "debug.resolveSession", input, &out); err != nil {
		return "", fmt.Errorf("debug.resolveSession: %w", err)
	}
	if out.SessionID == "" {
		return "", errors.New("debug.resolveSession: Studio answered without a session_id")
	}
	return out.SessionID, nil
}

// MARK: - Phoenix v2 frames

// A frame is the 5-slot array [join_ref, ref, topic, event, payload]. join_ref
// and ref only matter to a client matching pending pushes; this one has exactly
// one channel, so they are read and discarded.
type frame struct {
	Topic   string
	Event   string
	Payload json.RawMessage
}

// joinFrame is the subscriber's join. Where the device sends its access_token,
// a viewer sends the pairing code — and nothing else, so a viewer cannot pass
// itself off as the publisher.
func joinFrame(ref int, topic, code string) ([]byte, error) {
	id := strconv.Itoa(ref)
	return json.Marshal([]any{id, id, realtimePrefix + topic, eventJoin,
		map[string]string{"debug_code": code}})
}

func heartbeatFrame(ref int) ([]byte, error) {
	return json.Marshal([]any{nil, strconv.Itoa(ref), heartbeatTopic, eventHeartbeat, struct{}{}})
}

// decodeFrame parses one text frame. ok=false for anything that is not a 5-slot
// array with string topic and event — the same shape contract the SDK and
// palsvc enforce, so a malformed frame is dropped rather than guessed at.
func decodeFrame(text []byte) (frame, bool) {
	var slots []json.RawMessage
	if err := json.Unmarshal(text, &slots); err != nil || len(slots) != 5 {
		return frame{}, false
	}
	var f frame
	if json.Unmarshal(slots[2], &f.Topic) != nil || json.Unmarshal(slots[3], &f.Event) != nil {
		return frame{}, false
	}
	f.Payload = slots[4]
	return f, true
}

type replyPayload struct {
	Status   string `json:"status"`
	Response struct {
		Reason string `json:"reason"`
	} `json:"response"`
}

type broadcastPayload struct {
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

// MARK: - Rejection

// rejected is the server refusing the join. It is TERMINAL: the same code
// presented again gets the same answer, so the reconnect loop must not retry it
// — a viewer staring at a silent terminal that is quietly being refused every
// two seconds is worse than an error.
type rejected struct{ reason string }

// rejectionHelp says what to DO about each reason. A bare "forbidden" on a
// terminal costs someone an afternoon.
var rejectionHelp = map[string]string{
	// At JOIN time this is almost never a bad code — it is a session that STOPPED,
	// typically because the device disarmed mid-stream. Resolve-time
	// `unknown_session` means something else entirely; see resolveHelp.
	"unknown_session": "the session ended. The device disarmed it — closing or backgrounding the " +
		"app both do that — so this code is now dead. Arm again on the device and use the new code.",
	"expired": "that session's time ran out (sessions are TTL-bounded and are never " +
		"extended from this side). Arm again on the device for a fresh code.",
	"invalid_code": "that code does not match this session. Codes are case-insensitive and the " +
		"dash is optional, so check the characters themselves — and note that arming " +
		"again invalidates the previous code.",
	"not_publisher": "the server took this connection for the device that armed the session. " +
		"`attach` never joins as a publisher — it presents the pairing code and nothing " +
		"else — so there is nothing to correct here. Arm again on the device for a fresh " +
		"code, and report this if it keeps happening.",
	"forbidden": "not allowed on this session. A viewer may watch and nothing else. Check that " +
		"the selected environment is the one the device armed against — `palbase status` " +
		"shows it, and `--environment` overrides it.",
}

func (e rejected) Error() string {
	reason := e.reason
	if reason == "" {
		reason = "no reason given"
	}
	if help, ok := rejectionHelp[e.reason]; ok {
		return fmt.Sprintf("cannot watch that session (%s): %s", reason, help)
	}
	return fmt.Sprintf("cannot watch that session (%s). The device must be armed and streaming, "+
		"and the code must be the one it is showing right now.", reason)
}

// MARK: - Streaming

// run keeps one attachment alive across dropped sockets. Transport failures
// retry with capped exponential backoff; a rejection stops immediately.
//
// It takes NO Studio, deliberately: resolution happens once, above this loop,
// so a flapping socket re-joins with the session id it already has and can
// never re-resolve. Resolving is rate-limited per viewer at a few calls a
// minute — a reconnect loop with a resolve inside it would spend that budget
// in seconds, and the compiler is a better guarantee of that than a comment.
func run(ctx context.Context, out, errOut io.Writer, url, topic, code string, errorsOnly, asJSON bool) error {
	// warned remembers which foreign schema versions were already reported, so a
	// mismatched device does not bury the records that DO render.
	warned := map[int]bool{}
	backoff := initialBackoff
	for {
		started := time.Now()
		err := stream(ctx, out, errOut, url, topic, code, errorsOnly, asJSON, warned)
		var rej rejected
		if errors.As(err, &rej) {
			return rej
		}
		if ctx.Err() != nil {
			return nil
		}
		// A socket that lived a while was healthy; its drop is not evidence the
		// server is struggling, so don't punish the next attempt for it.
		if time.Since(started) > time.Minute {
			backoff = initialBackoff
		}
		_, _ = fmt.Fprintf(errOut, "▸ disconnected (%v) — reconnecting in %s\n", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// dialOptions carries the ONE thing a socket to a project you run may need: the
// certificate that project generated for itself.
//
// The choice is not made here — it was made once, at `palbase link --insecure`,
// and is remembered with the target. A flag repeated on every command is a flag
// somebody eventually types at the wrong address.
func dialOptions(url string) *websocket.DialOptions {
	target, err := backend.ReadTarget()
	if err != nil || !target.Insecure {
		return nil
	}
	if !strings.HasPrefix(url, "wss://"+strings.TrimPrefix(strings.TrimPrefix(target.URL, "https://"), "http://")) {
		// A different address than the one the link trusted: the exception does
		// not travel.
		return nil
	}
	return &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // opt-in at link time
		},
	}
}

// stream runs ONE socket: dial, join, then print records until it drops.
func stream(ctx context.Context, out, errOut io.Writer, url, topic, code string, errorsOnly, asJSON bool, warned map[int]bool) error {
	conn, _, err := websocket.Dial(ctx, url, dialOptions(url))
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(readLimit)

	join, err := joinFrame(1, topic, code)
	if err != nil {
		return err
	}
	if err := conn.Write(ctx, websocket.MessageText, join); err != nil {
		return err
	}

	beat, stopBeat := context.WithCancel(ctx)
	defer stopBeat()
	go heartbeat(beat, conn)

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if err := handleFrame(raw, out, errOut, errorsOnly, asJSON, warned); err != nil {
			return err
		}
	}
}

func heartbeat(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for ref := 2; ; ref++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		f, err := heartbeatFrame(ref)
		if err != nil {
			return
		}
		if conn.Write(ctx, websocket.MessageText, f) != nil {
			return // the read loop meets the same dead socket and drives the reconnect
		}
	}
}

// handleFrame routes one inbound frame. A non-nil error ends the connection.
func handleFrame(raw []byte, out, errOut io.Writer, errorsOnly, asJSON bool, warned map[int]bool) error {
	f, ok := decodeFrame(raw)
	if !ok {
		return nil // one dropped frame is not a dropped session
	}
	switch f.Event {
	case eventReply:
		// The heartbeat acks on its own pseudo-topic; only the channel's own
		// reply can reject the join.
		if f.Topic == heartbeatTopic {
			return nil
		}
		var reply replyPayload
		if err := json.Unmarshal(f.Payload, &reply); err != nil {
			return nil
		}
		if reply.Status == "ok" {
			// Say so explicitly. palsvc fans out and forgets (§7), so a viewer
			// that joins between records sees NOTHING for a while — without this
			// line, being successfully attached is indistinguishable from being
			// stuck mid-connect.
			_, _ = fmt.Fprintln(errOut, "▸ attached — waiting for records. "+
				"Nothing is replayed: you see what the device does from now on.")
			return nil
		}
		return rejected{reason: reply.Response.Reason}
	case eventBroadcast:
		var b broadcastPayload
		if err := json.Unmarshal(f.Payload, &b); err != nil || b.Event != recordEvent {
			return nil
		}
		printRecord(out, errOut, b.Payload, errorsOnly, asJSON, false, warned)
	}
	return nil
}

// printRecord renders one envelope through the SAME formatter `tail` uses, so
// attaching to a device, tailing a simulator and reading history all look
// identical. `bodies` adds the request/response payloads underneath — `history`
// wants them (they are the whole question it answers); the live views do not,
// because a multi-line body breaks a stream you scan by eye.
func printRecord(out, errOut io.Writer, payload []byte, errorsOnly, asJSON, bodies bool, warned map[int]bool) {
	var rec record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return
	}
	if rec.SchemaVersion != schemaVersion {
		if !warned[rec.SchemaVersion] {
			warned[rec.SchemaVersion] = true
			_, _ = fmt.Fprintf(errOut,
				"▸ this device sends schemaVersion=%d and this CLI renders %d — those records are NOT being rendered.\n"+
					"  Update the CLI, or the Palbe version the app links, so both speak one version.\n",
				rec.SchemaVersion, schemaVersion)
		}
		// --json interprets nothing, so the bytes still go out verbatim; the
		// rendered path stops here rather than reading v1 meaning into them.
		if asJSON {
			_, _ = fmt.Fprintln(out, string(payload))
		}
		return
	}
	if text, keep := format(rec, errorsOnly, asJSON, string(payload)); keep {
		_, _ = fmt.Fprintln(out, text)
		if bodies && !asJSON && rec.Network != nil {
			_, _ = fmt.Fprint(out, bodyText("request", rec.Network.RequestBody))
			_, _ = fmt.Fprint(out, bodyText("response", rec.Network.ResponseBody))
		}
	}
}

// bodyText renders one body under its record, or nothing when there was no body
// at all. The three states a reader must be able to tell apart:
//
//	we have the bytes · we have a PREFIX of them · they are stored elsewhere
//
// "no body" is the absent case and prints nothing, because a GET with no
// request body is not a gap in the record — it is the record.
func bodyText(label string, ref *bodyRef) string {
	if ref == nil {
		return ""
	}
	head := fmt.Sprintf("    %-8s %s", label, byteCount(ref.Size))
	switch {
	case len(ref.Inline) > 0:
		body := string(ref.Inline)
		if ref.Truncated {
			// size is the TRUE size, so this says how much is missing rather
			// than letting a prefix read as the whole thing.
			head += fmt.Sprintf(" (first %s of it)", byteCount(len(ref.Inline)))
		}
		return head + "\n" + indent(body) + "\n"
	case ref.BlobKey != "":
		// ponytail: not fetched. The blob read path is history-ingest's and does
		// not exist yet; say where the bytes are rather than pretend they are
		// absent. One call swaps in here when that contract lands.
		return head + fmt.Sprintf(" (kept out of line, not fetched — blob %s)\n", short(ref.BlobKey))
	case ref.Size == 0:
		return head + " (empty)\n"
	default:
		// Sized, but neither inline nor keyed: the bytes were dropped, and the
		// only honest thing is to say so instead of showing an empty body.
		return head + " (not retained)\n"
	}
}

func indent(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "      " + line
	}
	return strings.Join(lines, "\n")
}

func byteCount(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fkB", float64(n)/1024)
}

func short(key string) string {
	if len(key) > 12 {
		return key[:12] + "…"
	}
	return key
}
