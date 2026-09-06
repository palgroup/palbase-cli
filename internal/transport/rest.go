// Package transport provides the CLI's REST client for the Palbase control
// plane. Browser sessions use Bearer auth. Machine tokens use DPoP with a fresh
// proof bound to the request method, URL, and token.
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
)

// Client issues authenticated requests to the control plane.
type Client struct {
	// BaseURL is the control plane origin (e.g. https://api.palbase.studio).
	BaseURL string
	// Token is a browser session or machine token. An empty token prevents
	// requests from being sent.
	Token string
	// HTTPClient is initialized by New with a bounded request timeout.
	HTTPClient *http.Client
}

// New builds a client for the control plane at baseURL with an account
// credential. Machine tokens require DPoPSigner to be wired before use.
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
	// Fields carries the per-field detail a validation refusal puts in the
	// envelope. See Error() — it is where the actual reason lives.
	Fields []APIErrorField
}

// APIErrorField is one entry of the envelope's `fields` array.
type APIErrorField struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// StatusCode exposes the HTTP status so callers can classify a failure without
// importing this package's concrete type — a "the tenant is still waking" 503
// is retryable while a 404 is not, and the difference is a status, not a string.
func (e *APIError) StatusCode() int { return e.Status }

// Error renders the envelope INCLUDING its per-field detail.
//
// THE REASON IS IN `fields`, AND IT WAS BEING THROWN AWAY. A validation refusal
// answers `{"error":"bad_request","error_description":"Bad request","fields":[…]}`
// — the description is a category and the fields are the sentence. Printing only
// the description gave `bad_request (400): Bad request`, which says nothing at
// all. Measured 2026-08-24: a push refused because the project's own tests failed
// reported exactly that, and the reason had to be read out of the tenant's log.
func (e *APIError) Error() string {
	head := fmt.Sprintf("%s (%d)", e.Code, e.Status)
	if e.Description != "" {
		head += ": " + e.Description
	}
	if len(e.Fields) == 0 {
		return head
	}
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		switch {
		case f.Field != "" && f.Message != "":
			parts = append(parts, f.Field+": "+f.Message)
		case f.Message != "":
			parts = append(parts, f.Message)
		case f.Field != "":
			parts = append(parts, f.Field)
		}
	}
	if len(parts) == 0 {
		return head
	}
	return head + "\n  " + strings.Join(parts, "\n  ")
}

// errorEnvelope is the failure shape.
//
// `fields` ARRIVES IN TWO PLACES, and both are real. A tenant's own surface puts
// it at the top level; the control plane's SDK wraps a data-first error, so it
// lands under `data`. Reading only one of them silently drops the reason for
// half the refusals a person can hit — measured 2026-08-24, when the plane's
// push refusal parsed to nothing and the flat shape parsed fine.
type errorEnvelope struct {
	Code        string          `json:"error"`
	Description string          `json:"error_description"`
	Status      int             `json:"status"`
	RequestID   string          `json:"request_id"`
	Fields      []APIErrorField `json:"fields"`
	Data        struct {
		Fields []APIErrorField `json:"fields"`
	} `json:"data"`
}

// fields returns whichever of the two shapes carried the detail.
func (e errorEnvelope) fields() []APIErrorField {
	if len(e.Fields) > 0 {
		return e.Fields
	}
	return e.Data.Fields
}

// Do performs one control-plane request. The path is appended to BaseURL
// (e.g. "/v1/cloud/projects"). A non-nil body is JSON-encoded. On success the
// response is decoded directly into out; nil discards it. Non-2xx responses
// are parsed into an *APIError.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
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

	// MAKİNE KİMLİĞİ TAŞIYICI-BAĞLIDIR — ve sunumu da öyle.
	//
	// `pat_` bir DPoP token'ı (RFC 9449): `Authorization: DPoP <token>` ile
	// gider ve yanında BU isteğin metodunu, URL'sini ve token'ının özetini
	// imzalayan bir proof taşır. Bearer olarak sunmak, düzlemin tanımadığı bir
	// şekil üretir ve doğru kimliği elinde tutan çağıran 401 alır.
	//
	// Proof BURADA üretiliyor çünkü htm/htu bu isteğin kendisi — bir katman
	// yukarıda üretilseydi, yeniden yönlendirilen ya da yeniden denenen bir
	// istek yanlış bir proof taşırdı. Ve her çağrı TAZE bir proof imzalıyor:
	// sunucu tekrarları reddediyor.
	if strings.HasPrefix(c.Token, patPrefix) {
		if DPoPSigner == nil {
			return nil, fmt.Errorf(
				"cannot present a machine identity: no DPoP signer is wired")
		}
		proof, perr := DPoPSigner(method, c.BaseURL+path, c.Token)
		if perr != nil {
			// SESLİ DÜŞ: proof'suz göndermek, kimliği Bearer'a düşürüp
			// anlaşılmaz bir 401 almak olurdu.
			return nil, fmt.Errorf("could not produce a DPoP proof: %w", perr)
		}
		req.Header.Set("Authorization", "DPoP "+c.Token)
		req.Header.Set("DPoP", proof)
		return req, nil
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)
	return req, nil
}

// patPrefix, kontrol düzleminin bastığı makine kimliklerinin ön ekidir.
const patPrefix = "pat_"

// DPoPSigner, bir istek için RFC 9449 proof'u üretir.
//
// `main.go`'dan bağlanıyor, bu paket `internal/auth`'a bağımlı olmasın diye —
// aynı sebeple `backend.CloudKeyFetcher` de orada bağlanıyor. Nil ise makine
// kimliği taşıyan bir istek SESLİ düşer; sessizce Bearer'a düşmek, çağıranın
// anlamayacağı bir 401 üretirdi.
var DPoPSigner func(method, url, accessToken string) (string, error)

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
			Fields:      env.fields(),
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
