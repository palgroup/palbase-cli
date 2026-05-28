// Package transport is the CLI's REST client for the Palbase Management
// API (`/api/v1/*` on api.palbase.studio). It is the single transport
// for the project + apikey command families (Spec 1 of the Management
// API plan); `backend *` still rides the tRPC `internal/studio` client
// until backend REST routes exist (see
// docs/decisions/2026-05-24-s5-cli-pat-provisioning-and-backend-trpc.md).
//
// Auth model (D-32 / RFC 9449): every request carries
//
//	Authorization: DPoP <pat>
//	DPoP: <proof JWT signed by the keyring ECDSA P-256 key, fresh per request>
//
// The PAT is a DPoP-bound Personal Access Token; the proof's htm/htu match
// the actual request and its `ath` binds to the PAT. palauth (reached by
// Studio's introspection hop) re-derives htm/htu and checks the binding.
// Bearer presentation is rejected server-side — we never downgrade.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// Client issues DPoP-bound requests to the Management API.
type Client struct {
	// BaseURL is the Management API origin (e.g. https://api.dev.palbase.studio).
	// Paths passed to Do are appended verbatim.
	BaseURL string
	// Key signs each request's DPoP proof. Required.
	Key *auth.DPoPKey
	// PAT is the DPoP-bound Personal Access Token presented as the
	// `Authorization: DPoP <pat>` credential. Empty = not configured;
	// Do fails closed with an actionable error.
	PAT string
	// HTTPClient is overridable for tests; defaults to a 120s-timeout client.
	HTTPClient *http.Client
}

// New builds a Client. baseURL is the Management API origin; key is the
// keyring DPoP key; pat is the DPoP-bound management PAT (may be empty,
// in which case Do fails closed).
func New(baseURL string, key *auth.DPoPKey, pat string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Key:        key,
		PAT:        pat,
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

func (e *APIError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Description)
	}
	return fmt.Sprintf("%s (%d)", e.Code, e.Status)
}

// okEnvelope is the success wrapper every /api/v1 200/201/202 emits.
// The real payload lives under `data`; request_id is correlation only.
type okEnvelope struct {
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

// errorEnvelope is the failure shape.
type errorEnvelope struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
	Status      int    `json:"status"`
	RequestID   string `json:"request_id"`
}

// Do performs one Management-API request. method is the HTTP verb; path
// is appended to BaseURL (e.g. "/api/v1/projects"). body is JSON-encoded
// when non-nil (a nil body sends no request body). On a 2xx the success
// envelope's `data` is decoded into out (out may be nil to discard). On a
// non-2xx the error envelope is parsed into an *APIError.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	if c.Key == nil {
		return fmt.Errorf("management API: no dpop key — run `palbase login` to provision one")
	}
	if c.PAT == "" {
		return fmt.Errorf("management API: not authenticated — run `palbase login` " +
			"(or, for headless use, export PALBASE_ACCESS_TOKEN with a Dashboard-issued " +
			"DPoP-bound PAT)")
	}

	fullURL := c.BaseURL + path

	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), fullURL, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// DPoP-bound credential + a fresh per-request proof. The proof's
	// htm/htu MUST equal this request's method + URL (RFC 9449 §4.2);
	// `ath` binds it to the PAT.
	req.Header.Set("Authorization", "DPoP "+c.PAT)
	proof, err := c.Key.NewProof(auth.ProofOptions{
		HTTPMethod:  strings.ToUpper(method),
		URL:         fullURL,
		AccessToken: c.PAT,
	})
	if err != nil {
		return fmt.Errorf("sign dpop proof: %w", err)
	}
	req.Header.Set("DPoP", proof)

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
	var env okEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode response envelope: %w (body=%s)", err, truncate(raw, 240))
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode response data: %w", err)
	}
	return nil
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
