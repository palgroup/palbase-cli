package debugconsole

// attach_project.go — attaching to a device on a project you run.
//
// The cloud path asks Studio to turn a pairing code into a session id, then
// opens a socket at the environment's own host. Neither exists for a project
// somebody runs: there is no Studio to ask, and the host is whatever they typed
// into `palbase link`. The project resolves its own codes — it is the thing that
// issued them — so this asks it directly, over the address the checkout is bound
// to.
import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// attachToProject streams a device's console from the project this verb acts on
// — the one this checkout is linked to, or the one the caller selected.
//
// It used to fall through to a tRPC arm that asked our cloud to resolve the
// pairing code. That arm answered for cloud projects only, and it asked a
// service that does not hold the session: a code is armed on the PROJECT, and
// the project is the only thing that can turn it into a topic. One door now, and
// it is the right one either way.
func attachToProject(cmd *cobra.Command, code string, errorsOnly, asJSON bool) (bool, error) {
	target, err := backend.ResolveTarget(cmd.Context())
	if err != nil {
		return false, err
	}
	cred, _, err := backend.Credential(target.URL)
	if err != nil {
		return false, fmt.Errorf("not signed in to %s — run `palbase login`", target.URL)
	}

	sessionID, err := resolveSessionOnProject(cmd, target, cred, code)
	if err != nil {
		return false, err
	}

	// The stack namespaces debug topics under its own ref, and a project's ref
	// is the constant it boots with. Read from the code's own answer would be
	// better still, but the topic shape is the stack's, not the session's.
	topic := topicFor(projectStackRef, sessionID)
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s · attaching to %s\n", target.URL, topic)

	socket := strings.Replace(strings.TrimSuffix(target.URL, "/"), "https://", "wss://", 1)
	socket = strings.Replace(socket, "http://", "ws://", 1) + "/realtime/v1/websocket?vsn=2.0.0"
	return true, run(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
		socket, topic, code, errorsOnly, asJSON)
}

// projectStackRef is the identity a project boots with — the same constant the
// stack stamps on every request it serves (internal/platform, BootStackRef).
const projectStackRef = "project"

// resolveSessionOnProject turns the code a person read off a device into the
// session id its topic is named after.
func resolveSessionOnProject(cmd *cobra.Command, target backend.Target, cred backend.Credentials, code string) (string, error) {
	body, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost,
		strings.TrimSuffix(target.URL, "/")+"/rt/v1/debug/sessions/resolve", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	cred.Apply(req)

	client := &http.Client{Timeout: 30 * time.Second}
	if target.Insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in at link time
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", errors.New("no device is showing that code — check it, or ask for a fresh one")
	case http.StatusGone:
		return "", errors.New("that session has expired — the device can arm a new one")
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", fmt.Errorf("%s did not accept this session — run `palbase login`", target.URL)
	case http.StatusTooManyRequests:
		// Named rather than left to the generic line below. Three refusals reach
		// this switch and each has a different fix — wrong code, expired session,
		// too many tries — and collapsing one of them into "the project answered
		// 429" sends somebody looking for a code that was right all along.
		return "", errors.New("too many attempts on that code — wait a moment, or have the device arm a fresh one")
	default:
		return "", fmt.Errorf("the project answered %d: %s", res.StatusCode, strings.TrimSpace(string(answer)))
	}

	var resolved struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(answer, &resolved); err != nil || resolved.SessionID == "" {
		return "", fmt.Errorf("the project answered without a session: %s", strings.TrimSpace(string(answer)))
	}
	return resolved.SessionID, nil
}
