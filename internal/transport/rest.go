// Package transport is the CLI's REST client for the Palbase Management
// API (`/api/v2/*` on api.palbase.studio). It is the single transport for the
// project / env / apikey / apps command families; `backend *` still rides the
// tRPC `internal/studio` client for the procedures that have no v2 route
// (logs, env vars, migrations, test data).
//
// `/api/v1` survives ONLY for `palbase admin *` — the fleet-operator routes
// deliberately stay on v1.
//
// Auth model (D-32 / RFC 9449): every request carries
//
//	Authorization: Bearer <session token>
//
// The PAT is a DPoP-bound Personal Access Token; the proof's htm/htu match
// the actual request and its `ath` binds to the PAT. palauth (reached by
// Studio's introspection hop) re-derives htm/htu and checks the binding.
// Bearer presentation is rejected server-side — we never downgrade.
//
// IDEMPOTENCY. Every mutating v2 route honours an `Idempotency-Key` header:
// the first 2xx is stored and replayed byte-for-byte for 24h, and an in-flight
// duplicate is a 409. The CLI sends one on the deploy upload (the only mutation
// whose side effect is not already collapsed by a stable Temporal workflow id),
// so a push whose response is lost to a timeout can be retried on the SAME key
// without deploying twice.
package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// NewIdempotencyKey mints a fresh key for one logical mutation. crypto/rand —
// never math/rand: a predictable key would let one caller replay another's
// stored response if the scope hash were ever weakened.
func NewIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is fatal-class; returning "" makes the client send
		// no header (a plain, non-idempotent request) rather than a guessable one.
		return ""
	}
	return hex.EncodeToString(b[:])
}

// NewOperationID mints a RFC 4122 v4 UUID for one logical durable mutation.
//
// Distinct from NewIdempotencyKey (32 hex chars) because the admin key-rotation
// endpoint validates its operationId as a UUID: the server derives the durable
// mutation id from it, so a retry carrying the SAME value joins the same
// rotation instead of minting a second key. crypto/rand for the same reason the
// idempotency key uses it.
func NewOperationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Empty rather than a guessable value: the server rejects it as
		// invalid_request, which is a visible failure instead of a silent one.
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Client issues authenticated requests to the control plane.
type Client struct {
	// BaseURL is the control plane origin (e.g. https://api.v2.palbase.studio).
	BaseURL string
	// Token is the session bearer token `palbase login` stored. Empty = not
	// signed in; every request fails closed rather than going out anonymous.
	Token string
	// HTTPClient is the transport. Nil uses a default with a sane timeout.
	HTTPClient *http.Client
}

// New builds a Client. baseURL is the Management API origin; key is the
// keyring DPoP key; pat is the DPoP-bound management PAT (may be empty,
// in which case Do fails closed).
// New builds a client for the control plane at baseURL, authenticating with the
// session bearer token.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// APIError carries the parsed `{error, error_description, status,
// request_id}` envelope (CLAUDE.md "Error Response Format"). Callers can
// errors.As into it to switch on the machine-readable Code.
type APIError struct {
	Code        string
	Description string
	Status      int
	RequestID   string
}

// StatusCode exposes the HTTP status so callers can classify a failure without
// importing this package's concrete type — a "the tenant is still waking" 503
// is retryable while a 404 is not, and the difference is a status, not a string.
func (e *APIError) StatusCode() int { return e.Status }

func (e *APIError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Description)
	}
	return fmt.Sprintf("%s (%d)", e.Code, e.Status)
}

// errorEnvelope is the failure shape.
type errorEnvelope struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
	Status      int    `json:"status"`
	RequestID   string `json:"request_id"`
}

// Do performs one Management-API request. method is the HTTP verb; path
// is appended to BaseURL (e.g. "/api/v2/projects"). body is JSON-encoded
// when non-nil (a nil body sends no request body). On a 2xx the success
// envelope's `data` is decoded into out (out may be nil to discard). On a
// non-2xx the error envelope is parsed into an *APIError.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	return c.doWithIdempotency(ctx, method, path, body, out, "")
}

// DoIdempotent is Do with an `Idempotency-Key` header. The v2 API REQUIRES one
// on some mutations — API-key creation and rotation answer
// `400 Idempotency-Key is required` without it — so those routes must call this
// rather than Do. Mint the key with NewIdempotencyKey once per logical
// mutation, and reuse the SAME key on a retry: a fresh key on a timed-out
// request is how one intended mutation becomes two.
func (c *Client) DoIdempotent(ctx context.Context, method, path string, body, out any, idempotencyKey string) error {
	return c.doWithIdempotency(ctx, method, path, body, out, idempotencyKey)
}

func (c *Client) doWithIdempotency(ctx context.Context, method, path string, body, out any, idempotencyKey string) error {
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(raw)
	}

	req, err := c.newSignedRequest(ctx, method, path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("management API request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseError(raw, resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	// SUCCESS IS THE VALUE ITSELF — there is no `data` wrapper.
	//
	// Measured live (2026-08-21): the v2 control plane answers
	// GET /v1/cloud/projects with a bare `[]`, and a decoder expecting a
	// wrapper failed with "cannot unmarshal array into okEnvelope". Worse was
	// the shape that DID parse: `create` returned an object, the wrapper's
	// absent `data` decoded to nothing, and the command cheerfully printed
	// "Created  (, cell )" — a success message about a project it never read.
	//
	// Failures stay enveloped (error / error_description / status /
	// request_id); that path is handled above.
	if string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w (body=%s)", err, truncate(raw, 240))
	}
	return nil
}

// newSignedRequest builds an authenticated control-plane request.
//
// One place, every verb: JSON calls and multipart uploads alike come through
// here, so "how does this CLI authenticate" has exactly one answer.
//
// Fails closed when there is no token. Sending the request anonymously would
// earn a 401 that reads like a server problem rather than the truth, which is
// that nobody signed in.
func (c *Client) newSignedRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("not authenticated — run `palbase login` " +
			"(or, for headless use, export PALBASE_ACCESS_TOKEN)")
	}

	method = strings.ToUpper(method)
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return req, nil
}

// PostMultipart uploads a gzipped tarball to a Management-API endpoint as
// multipart/form-data: a file part named `tarball` (filename bundle.tar.gz,
// Content-Type application/gzip) plus one text field per fields entry. It
// reuses the exact DPoP/PAT signing of every other request (newSignedRequest),
// so the proof's htm/htu match this POST. Returns the raw 2xx response body;
// a non-2xx is surfaced as an *APIError (same envelope shape as Do).
//
// idempotencyKey (when non-empty) rides the `Idempotency-Key` header: the same
// key replayed on the same route by the same user returns the FIRST response
// instead of running the mutation again. The deploy upload is the CLI's one
// mutation with no server-side stable workflow id to collapse a double-submit,
// so a timed-out push MUST be retried with the same key — never a new one.
func (c *Client) PostMultipart(ctx context.Context, path string, tarball []byte, fields map[string]string, idempotencyKey string) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("write multipart field %q: %w", k, err)
		}
	}

	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="tarball"; filename="bundle.tar.gz"`)
	hdr.Set("Content-Type", "application/gzip")
	part, err := mw.CreatePart(hdr)
	if err != nil {
		return nil, fmt.Errorf("create multipart file part: %w", err)
	}
	if _, err := part.Write(tarball); err != nil {
		return nil, fmt.Errorf("write tarball part: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	// Build + sign AFTER the body exists; the multipart Content-Type
	// (with boundary) must be the writer's FormDataContentType().
	req, err := c.newSignedRequest(ctx, http.MethodPost, path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("management API request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseError(raw, resp.StatusCode)
	}
	return raw, nil
}

// parseError turns a non-2xx body into an *APIError. The body is normally
// the standard error envelope; a non-envelope body (e.g. an HTML 502 from
// a proxy) still yields a non-nil *APIError carrying the HTTP status so
// the caller never loses the failure.
func parseError(raw []byte, status int) error {
	var env errorEnvelope
	if json.Unmarshal(raw, &env) == nil && env.Code != "" {
		st := env.Status
		if st == 0 {
			st = status
		}
		return &APIError{
			Code:        env.Code,
			Description: env.Description,
			Status:      st,
			RequestID:   env.RequestID,
		}
	}
	return &APIError{
		Code:        "http_error",
		Description: truncate(raw, 240),
		Status:      status,
	}
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
