package sealedclient

// The transport: one stack, one verified keyset, every request that the server
// marks sealed-required sent sealed and its answer opened.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxBody bounds what we will read from a stack — the same 1 MiB the server's
// middleware bounds an envelope at (v2/internal/sealed/middleware.go:63).
const maxBody = 1 << 20

// refetchCooldown is how long a `sealed_unknown_kid` refetch is remembered.
//
// The retry exists for one real event — the stack rotated to a key the cached
// document does not name — and a stack that answers unknown_kid to everything
// would otherwise turn every request into two requests plus a keyset fetch. The
// cooldown makes the second attempt a rotation response rather than a retry
// loop.
const refetchCooldown = time.Minute

// Config wires a client to one stack.
type Config struct {
	// BaseURL is the address of the stack, as the CLI dialled it.
	BaseURL string
	// APIKey is the publishable key for this environment. It is not sent by
	// this package — it is read for its `pb_<ref>_…` middle, which is how the
	// client knows WHICH stack a fleet binding must be about.
	APIKey string
	// SelfHostRoot is the base64 Ed25519 public root a self-hosted stack
	// published through `/v1/management/keys`. Empty means the fleet root, and
	// a value here REPLACES it rather than joining it — see roots.go.
	SelfHostRoot string
	// HTTPClient is the client that already knows this target's TLS policy.
	HTTPClient *http.Client
	// Now is injectable so an expiry test can control the clock.
	Now func() time.Time
}

// Client seals requests to one stack.
type Client struct {
	baseURL    string
	apiKey     string
	roots      TrustedRoots
	selfHosted bool
	stackRef   string
	http       *http.Client
	now        func() time.Time

	mu          sync.Mutex
	keyset      *Keyset
	lastSeen    uint64
	noKeyset    bool // measured: this stack publishes none, so it requires none
	measured    bool
	lastRefetch time.Time
}

// New builds a client. It fails rather than guessing: an unusable root and an
// address whose stack cannot be identified are both states in which the chain
// would verify nothing while looking like it verified something.
func New(cfg Config) (*Client, error) {
	roots, selfHosted, err := rootsFor(cfg.SelfHostRoot)
	if err != nil {
		return nil, err
	}
	c := &Client{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		roots:      roots,
		selfHosted: selfHosted,
		http:       cfg.HTTPClient,
		now:        cfg.Now,
	}
	if c.http == nil {
		c.http = http.DefaultClient
	}
	if c.now == nil {
		c.now = time.Now
	}
	// The stack ref is resolved EAGERLY, not at the moment a binding needs it.
	// A client that cannot say which stack it is talking to can never check a
	// binding's subject, and finding that out on the first sealed request would
	// report it as a signature failure.
	ref, err := expectedStackRef(c.apiKey, c.baseURL, c.selfHosted)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, c.baseURL)
	}
	c.stackRef = ref
	return c, nil
}

// StackError is a refusal the sealing layer itself produced, in the caller's
// language rather than in the wire's.
//
// The four codes and what each means for whoever is reading the terminal are at
// v2/internal/sealed/middleware.go:14-21.
type StackError struct {
	Code    string
	Status  int
	Message string
}

func (e *StackError) Error() string { return e.Message }

// Do sends a request, sealing it when the server's rule says that path refuses
// plaintext, and returns the response with the sealed answer already opened —
// its real status, its real body, its real content type.
//
// A path the rule does not name goes out untouched. There is exactly one rule
// and it is Required(); this is the only place the CLI decides.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if !Required(req.URL.Path) {
		return c.http.Do(req)
	}

	plaintext, err := drainBody(req)
	if err != nil {
		return nil, err
	}

	for attempt := 0; ; attempt++ {
		ks, err := c.keysetFor(req.Context())
		if err != nil {
			return nil, err
		}
		if ks == nil {
			// The stack publishes no keyset, so it cannot require sealing —
			// exactly the server's own condition, `len(cfg.signedKeyset) > 0`
			// at v2/internal/server/sealed.go:433. Sending plaintext here is
			// not a downgrade: there is no key to seal to and the stack is not
			// asking for one.
			return c.http.Do(withPlaintextPermitted(req, plaintext))
		}
		kid, pub, err := ks.SealingKey()
		if err != nil {
			return nil, err
		}
		res, serr := c.sealOnce(req, plaintext, kid, pub)
		if serr == nil {
			return res, nil
		}
		var se *StackError
		if attempt == 0 && errors.As(serr, &se) && se.Code == "sealed_unknown_kid" && c.beginRefetch() {
			// The one refusal a client acts on: the stack rotated to a key the
			// document we hold does not name. Drop it and fetch once.
			continue
		}
		return nil, serr
	}
}

// drainBody reads the request body once, so a retry can seal the same bytes
// again. A sealed retry cannot reuse the first envelope — the replay guard
// refuses a repeated encapsulation (v2/internal/sealed/middleware.go:168) — so
// it has to re-seal, and re-sealing needs the plaintext.
func drainBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	raw, err := readCapped(req.Body, maxBody, "sealed: the request body")
	_ = req.Body.Close()
	req.Body = nil
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) sealOnce(req *http.Request, plaintext []byte, kid string, pub []byte) (*http.Response, error) {
	key, err := SuiteKEM.Scheme().UnmarshalBinaryPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("%w: the advertised key is not a %s key", ErrKeysetMalformed, SuiteName)
	}

	// The inner content type is what the handler will see restored on the other
	// side. A bodyless read carries application/json because that is what the
	// server defaults to and what the recorded vectors pin.
	ict := req.Header.Get("Content-Type")
	if ict == "" {
		ict = "application/json"
	}
	// aadHost mirrors v2/internal/server/sealed.go:462: with trustProxy off the
	// server binds r.Host, which is the Host header — and Go writes that header
	// from req.Host when set, otherwise from req.URL.Host. Reading it any other
	// way (a configured base URL, a stripped port) is how every httptest passes
	// and every live request fails.
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	env, exporter, err := Seal(key, kid, plaintext, ict, host, req.Method, req.URL.Path,
		c.now().Unix(), req.Header.Get("Idempotency-Key"))
	if err != nil {
		return nil, err
	}

	sealedReq := req.Clone(req.Context())
	if len(plaintext) == 0 {
		// THE BODYLESS CARRIAGE, which is the one `palbase link` uses: a GET
		// cannot hold a body, so the envelope rides in a header instead. The
		// AAD, the exporter and the wire format are identical — only the
		// carriage differs (v2/internal/sealed/envelope_header.go:9), and the
		// server decides between them by the same test
		// (v2/internal/sealed/middleware.go:95). A request with nothing to
		// carry takes this form whatever its method, because a body-carriage
		// envelope around zero bytes says exactly as much and cannot be sent
		// on a GET at all.
		value, err := EncodeEnvelopeHeader(env)
		if err != nil {
			return nil, err
		}
		sealedReq.Header.Set(EnvelopeHeaderName, value)
		sealedReq.Header.Del("Content-Type")
		sealedReq.ContentLength = 0
	} else {
		body, err := json.Marshal(env)
		if err != nil {
			return nil, err
		}
		sealedReq.Header.Set("Content-Type", ContentType)
		sealedReq.ContentLength = int64(len(body))
		sealedReq.Body = io.NopCloser(bytes.NewReader(body))
		sealedReq.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	res, err := c.http.Do(sealedReq)
	if err != nil {
		return nil, err
	}
	return c.openResponse(res, exporter)
}

// openResponse turns the wire answer into the one the handler produced.
//
// TWO SHAPES ARRIVE, and both are legal because the server produces both. A
// request that OPENED is answered sealed — SealResponses sits inside the
// middleware and seals whatever the handler wrote
// (v2/internal/sealed/response.go:145). A request that did NOT open is refused
// by the middleware itself, which writes a plaintext JSON error and never
// reaches SealResponses (v2/internal/sealed/middleware.go:99-156). So a sealed
// content type means "opened", and a plaintext body means "refused" — and a
// plaintext SUCCESS means neither, which is the one thing this must not accept.
func (c *Client) openResponse(res *http.Response, exporter []byte) (*http.Response, error) {
	if isSealedResponse(res) {
		raw, err := readCapped(res.Body, maxBody, c.baseURL+"'s sealed answer")
		_ = res.Body.Close()
		if err != nil {
			return nil, err
		}
		var env ResponseEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("%w: the answer is not a sealed response", ErrResponseInvalid)
		}
		pt, err := OpenResponse(exporter, &env)
		if err != nil {
			return nil, err
		}
		res.StatusCode = env.St
		res.Status = strconv.Itoa(env.St) + " " + http.StatusText(env.St)
		res.Body = io.NopCloser(bytes.NewReader(pt))
		res.ContentLength = int64(len(pt))
		res.Header.Set("Content-Type", env.ICT)
		res.Header.Set("Content-Length", strconv.Itoa(len(pt)))
		return res, nil
	}

	raw, err := readCapped(res.Body, maxBody, c.baseURL+"'s answer")
	_ = res.Body.Close()
	if err != nil {
		return nil, err
	}
	if serr := c.sealedRefusal(res, raw); serr != nil {
		return nil, serr
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		// FAIL CLOSED. We sealed, the stack answered success in the clear, and
		// the guarantee is "sealed request implies sealed response". Accepting
		// this hides the exact state palsvc shipped in v0.40.0: a 200 with a
		// valid token for a body it had never read.
		return nil, fmt.Errorf(
			"%s answered in the clear (%d) to a sealed request; it published a sealing keyset but did not open the envelope",
			c.baseURL, res.StatusCode)
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	res.ContentLength = int64(len(raw))
	return res, nil
}

func isSealedResponse(res *http.Response) bool {
	ct := res.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), ContentType)
}

// sealedRefusal turns the middleware's own error codes into something an
// operator can act on. Silence — a nil return — means this was an ordinary
// application error and belongs to the caller.
func (c *Client) sealedRefusal(res *http.Response, raw []byte) error {
	var problem struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil {
		return nil
	}
	switch problem.Error {
	case "sealed_unknown_kid":
		return &StackError{Code: problem.Error, Status: res.StatusCode, Message: fmt.Sprintf(
			"%s does not hold the sealing key its own keyset advertises. Its published keyset and the keys it can open with have diverged.",
			c.baseURL)}
	case "sealed_skew":
		return &StackError{Code: problem.Error, Status: res.StatusCode, Message: fmt.Sprintf(
			"%s refused this request's timestamp: this machine's clock is %s. A sealed request carries the time it was made and is refused more than 5 minutes out — set this machine's clock (or its timezone service) and run the command again.",
			c.baseURL, describeSkew(res, c.now()))}
	case "sealed_unsupported":
		return &StackError{Code: problem.Error, Status: res.StatusCode, Message: fmt.Sprintf(
			"%s cannot open sealed requests, yet it publishes a sealing keyset that says it can. The stack is missing its root key: check STACK_ROOT_KEY on the stack, not this checkout.",
			c.baseURL)}
	case "sealed_replay":
		return &StackError{Code: problem.Error, Status: res.StatusCode, Message: fmt.Sprintf(
			"%s has already seen this exact sealed request. Run the command again.", c.baseURL)}
	case "sealed_invalid", "sealed_required":
		return &StackError{Code: problem.Error, Status: res.StatusCode, Message: fmt.Sprintf(
			"%s refused the sealed request (%s). This CLI and that stack disagree about the sealed wire format; upgrade whichever is older.",
			c.baseURL, problem.Error)}
	}
	return nil
}

// describeSkew says HOW wrong the clock is, from the one clock reading a
// refusal already carries: the server's own Date header. Without it the message
// can only assert that something is wrong with a clock, which is the half of
// the diagnosis the reader already had.
func describeSkew(res *http.Response, now time.Time) string {
	served, err := http.ParseTime(res.Header.Get("Date"))
	if err != nil {
		return "outside the window the stack accepts"
	}
	d := now.Sub(served).Round(time.Second)
	if d < 0 {
		return fmt.Sprintf("%s behind the stack's", (-d).String())
	}
	return fmt.Sprintf("%s ahead of the stack's", d.String())
}

// keysetFor returns the verified keyset for this stack, or nil when the stack
// publishes none.
//
// Fetched once per client and kept, which is what makes the retry below a
// ROTATION response rather than a second fetch: the cached version is also the
// `lastSeenVersion` a refetched document has to be no older than. A client
// lives as long as the caller keeps it; a caller that builds one per request
// gets one fetch per request and rollback protection only within it.
func (c *Client) keysetFor(ctx context.Context) (*Keyset, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.measured {
		if c.noKeyset {
			return nil, nil
		}
		if c.keyset != nil {
			return c.keyset, nil
		}
	}
	return c.fetchLocked(ctx)
}

// beginRefetch drops the cached keyset so the next attempt fetches a fresh one,
// and reports whether the cooldown allows it.
func (c *Client) beginRefetch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.lastRefetch.IsZero() && now.Sub(c.lastRefetch) < refetchCooldown {
		return false
	}
	c.lastRefetch = now
	c.keyset = nil
	c.measured = false
	return true
}

func (c *Client) fetchLocked(ctx context.Context) (*Keyset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+KeysetPath, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read the sealing keyset from %s: %w", c.baseURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := readCapped(res.Body, maxBody, c.baseURL+KeysetPath)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotFound {
		// A stack with no fleet binding publishes no document and answers 404
		// (v2/internal/server/sealed.go:264). It therefore does not require
		// sealing either, and saying so once is what keeps a `link` against an
		// older stack working.
		c.measured, c.noKeyset, c.keyset = true, true, nil
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d for its sealing keyset", c.baseURL, res.StatusCode)
	}

	var doc BoundKeyset
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %s served something that is not a keyset document", ErrKeysetMalformed, c.baseURL)
	}
	ks, err := VerifyBoundKeyset(c.roots, &doc, c.stackRef, c.lastSeen, c.now())
	if err != nil {
		return nil, fmt.Errorf("the sealing keyset %s publishes does not verify: %w", c.baseURL, err)
	}
	c.keyset, c.lastSeen, c.measured, c.noKeyset = ks, ks.Version, true, false
	return ks, nil
}

// plaintextPermittedKey marks a request the sealed client decided may travel in
// the clear because the stack it addresses publishes NO keyset.
//
// It carries a value only this package can produce, so it records a MEASUREMENT
// and can never be a caller's preference: an outbound guard that could be
// switched off by whoever it guards is not a guard.
type plaintextPermittedKey struct{}

type plaintextPermitted struct{ measuredNoKeyset bool }

func withPlaintextPermitted(req *http.Request, plaintext []byte) *http.Request {
	out := req.Clone(context.WithValue(req.Context(), plaintextPermittedKey{}, plaintextPermitted{true}))
	if len(plaintext) > 0 {
		out.Body = io.NopCloser(bytes.NewReader(plaintext))
		out.ContentLength = int64(len(plaintext))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(plaintext)), nil }
	}
	return out
}

// StackPublishesNoKeyset reports whether this request was cleared for plaintext
// by a keyset fetch that answered 404 — the server's own condition for not
// requiring a seal.
func StackPublishesNoKeyset(ctx context.Context) bool {
	v, ok := ctx.Value(plaintextPermittedKey{}).(plaintextPermitted)
	return ok && v.measuredNoKeyset
}

// readCapped reads at most `limit` bytes and refuses a longer body rather than
// returning a truncated one.
//
// `io.ReadAll(io.LimitReader(r, N))` ends in a CLEAN EOF, so io.ReadAll reports
// success and the caller cannot tell a body cut at N from a complete one. Three
// reads in this file had that shape, and on the plaintext-refusal path a
// truncated error body was handed straight back to the caller.
//
// It is a second copy of internal/backend's helper of the same name, and that
// is the import direction rather than an oversight: `backend` imports this
// package, so this package cannot import `backend`. Eight lines stated twice
// beats an inverted dependency between a sealing client and a CLI package.
func readCapped(r io.Reader, limit int64, what string) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes; a truncated body cannot be told from a complete one", what, limit)
	}
	return raw, nil
}
