// Package studio provides a thin tRPC HTTP client used by `palbase
// backend init/deploy/...`. CLI access tokens are sender-constrained, so every
// request presents the token with the DPoP scheme and a request-bound proof.
package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Client is a tRPC-over-HTTP client.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	// Token resolver runs lazily so the CLI can refresh its access
	// token without baking the auth.Client import into this package.
	TokenFn func(ctx context.Context) (string, error)
	// ProofFn signs one proof for this exact method, URL, and access token.
	// It is required whenever TokenFn is configured.
	ProofFn ProofFn
}

type ProofFn func(ctx context.Context, method, rawURL, accessToken string) (string, error)

// New builds a Client with sane defaults.
func New(baseURL string, token func(ctx context.Context) (string, error), proof ProofFn) *Client {
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
		TokenFn:    token,
		ProofFn:    proof,
	}
}

// WithTimeout returns a COPY of the client whose HTTP timeout is d.
//
// The 120s default is right for ordinary tRPC calls, but Studio blocks for up to
// 330s on the procedures that spawn a one-shot Job (migration-sql, db-reset).
// Against those, the default client gives up while the SERVER IS STILL WORKING —
// measured live on `db reset`: the CLI printed "context deadline exceeded" while
// the Job ran on. Worse than a slow command: the user is told it failed, and the
// obvious response is to run the destructive verb again.
func (c *Client) WithTimeout(d time.Duration) *Client {
	clone := *c
	clone.HTTPClient = &http.Client{Timeout: d}
	return &clone
}

// JobCallTimeout is the client timeout for procedures that block on a one-shot
// Job. It exceeds Studio's own 330s Job budget so the SERVER's timeout is always
// the one that fires — the CLI must never be the component that gives up first.
const JobCallTimeout = 360 * time.Second

// Query runs a tRPC query (GET) against `<router>.<procedure>`. The
// `out` pointer receives JSON-decoded `result.data.json` on success.
func (c *Client) Query(ctx context.Context, path string, input any, out any) error {
	inputJSON, err := json.Marshal(map[string]any{"json": input})
	if err != nil {
		return fmt.Errorf("marshal input: %w", err)
	}
	u := fmt.Sprintf("%s/api/trpc/%s?input=%s", c.BaseURL, path, url.QueryEscape(string(inputJSON)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(ctx, req, out)
}

// Mutation runs a tRPC mutation (POST) against `<router>.<procedure>`.
func (c *Client) Mutation(ctx context.Context, path string, input any, out any) error {
	body, err := json.Marshal(map[string]any{"json": input})
	if err != nil {
		return fmt.Errorf("marshal input: %w", err)
	}
	u := fmt.Sprintf("%s/api/trpc/%s", c.BaseURL, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(ctx, req, out)
}

func (c *Client) do(ctx context.Context, req *http.Request, out any) error {
	if c.TokenFn != nil {
		token, err := c.TokenFn(ctx)
		if err != nil {
			return fmt.Errorf("acquire token: %w", err)
		}
		if c.ProofFn == nil {
			return fmt.Errorf("studio: dpop proof signer is not configured")
		}
		proof, err := c.ProofFn(ctx, req.Method, req.URL.String(), token)
		if err != nil {
			return fmt.Errorf("sign studio dpop proof: %w", err)
		}
		req.Header.Set("Authorization", "DPoP "+token)
		req.Header.Set("DPoP", proof)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("trpc request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if os.Getenv("PALBASE_DEBUG_TRPC") != "" {
		fmt.Fprintf(
			os.Stderr,
			"studio trpc debug: status=%d content_length=%d request_id=%q\n",
			resp.StatusCode,
			len(body),
			resp.Header.Get("X-Request-ID"),
		)
	}
	if resp.StatusCode != http.StatusOK {
		return parseTRPCError(body, resp.StatusCode)
	}

	// tRPC envelope:
	//   { result: { data: { json: <out> } } }
	var envelope struct {
		Result struct {
			Data struct {
				JSON json.RawMessage `json:"json"`
			} `json:"data"`
		} `json:"result"`
		Error *trpcErrorBody `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode envelope: %w (body=%s)", err, truncate(body, 240))
	}
	if envelope.Error != nil {
		return envelope.Error.toError()
	}
	if out == nil || len(envelope.Result.Data.JSON) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result.Data.JSON, out); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}

type trpcErrorData struct {
	Code     string `json:"code"`
	HTTPCode int    `json:"httpStatus"`
	// Some Studio errors stash the readable reason here (Zod
	// validation surfaces, tRPC errorFormatter additions) — when
	// Message is empty we want this so the user doesn't see a bare
	// "studio: " line.
	Cause string `json:"cause,omitempty"`
	Path  string `json:"path,omitempty"`
}

type trpcErrorBody struct {
	Message string        `json:"message"`
	Code    int           `json:"code"`
	Data    trpcErrorData `json:"data"`
}

// UnmarshalJSON handles BOTH tRPC error wire shapes:
//
//	flat:      {"message":"...","code":-32001,"data":{...}}
//	superjson: {"json":{"message":"...","code":-32001,"data":{...}}}
//
// Studio configures `transformer: superjson`, so its HTTP error responses
// nest the real fields under a `json` key. The previous decoder only knew
// the flat shape, so every Studio error unmarshalled to an all-zero struct
// and toError() fell back to the useless "empty error envelope" — hiding
// the actual message (e.g. "UNAUTHORIZED") that was right there under
// error.json.message. We try the superjson envelope first and fall back to
// the flat shape so this client works against transformer'd and plain tRPC
// servers alike.
func (e *trpcErrorBody) UnmarshalJSON(b []byte) error {
	type rawBody struct {
		Message string        `json:"message"`
		Code    int           `json:"code"`
		Data    trpcErrorData `json:"data"`
	}
	var wrapped struct {
		JSON *rawBody `json:"json"`
	}
	if err := json.Unmarshal(b, &wrapped); err == nil && wrapped.JSON != nil {
		e.Message = wrapped.JSON.Message
		e.Code = wrapped.JSON.Code
		e.Data = wrapped.JSON.Data
		return nil
	}
	var flat rawBody
	if err := json.Unmarshal(b, &flat); err != nil {
		return err
	}
	e.Message = flat.Message
	e.Code = flat.Code
	e.Data = flat.Data
	return nil
}

func (e *trpcErrorBody) toError() error {
	// CLI-14 fix: build a descriptive line even when Message is empty,
	// which Studio routinely returns for module-not-provisioned errors
	// (the path then sees the empty message wrapped as "auth.providers.list:
	// studio: " and the user can't tell whether it's a 404, a 500, or a
	// network drop). Layer the available signals — message → cause →
	// http status → tRPC code — so something always lands in the output.
	msg := e.Message
	if msg == "" && e.Data.Cause != "" {
		msg = e.Data.Cause
	}
	if msg == "" {
		switch {
		case e.Data.HTTPCode > 0:
			msg = fmt.Sprintf("HTTP %d (no error message)", e.Data.HTTPCode)
		case e.Code != 0:
			msg = fmt.Sprintf("tRPC code %d (no error message)", e.Code)
		default:
			msg = "empty error envelope"
		}
	}
	if e.Data.Code != "" {
		return fmt.Errorf("studio %s: %s", e.Data.Code, msg)
	}
	return fmt.Errorf("studio: %s", msg)
}

func parseTRPCError(body []byte, status int) error {
	// Body may be a raw error envelope OR a plain error string.
	// The envelope can also be an ARRAY (tRPC batch responses) — handle
	// that shape too so a batch error isn't silently swallowed as
	// "studio http <status>: [..." with the real reason hidden inside.
	var single struct {
		Error *trpcErrorBody `json:"error"`
	}
	if json.Unmarshal(body, &single) == nil && single.Error != nil {
		return single.Error.toError()
	}
	var batch []struct {
		Error *trpcErrorBody `json:"error"`
	}
	if json.Unmarshal(body, &batch) == nil && len(batch) > 0 && batch[0].Error != nil {
		return batch[0].Error.toError()
	}
	return fmt.Errorf("studio http %d: %s", status, truncate(body, 240))
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
