package backend

// stack_login.go — `palbase login` : proving who you are to the linked stack.
//
// The rule the design settled (design-management-api.md §10): authentication is
// against the CURRENT TARGET. Someone self-hosting logs in to their own stack;
// against ours the same command reaches a platform account. Same verb, same
// place to type it, different thing on the other end — which is what makes the
// CLI work everywhere rather than only against one host.
//
// It signs in as a PERSON, not with a key. The stack decides whether that person
// may manage it (`auth.users.is_management`, granted by the operator's own
// command), so this file has no notion of roles at all: it gets a token and
// stores it, and the surface answers 403 if the person may not.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// ErrNoLinkedStack says this checkout is not bound to a stack, so a
// target-relative command should fall through to the cloud path. A sentinel
// rather than a string, because a caller matching on wording is a caller that
// breaks when the wording improves.
var ErrNoLinkedStack = errors.New("no linked stack")

// StackLogin signs in to the linked stack. It answers ErrNoLinkedStack when
// there is none, which is the caller's cue to take the cloud path.
func StackLogin(ctx context.Context, email, password string, w io.Writer) error {
	target, err := ReadTarget()
	if err != nil {
		return ErrNoLinkedStack
	}
	if email == "" {
		return errors.New("--email is required to sign in to " + target.URL)
	}
	if password == "" {
		password, err = promptPassword(w)
		if err != nil {
			return err
		}
	}
	return runStackLogin(ctx, target, email, password, w)
}

func promptPassword(w io.Writer) (string, error) {
	fmt.Fprint(w, "password: ")
	raw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(w)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(raw), nil
}

func runStackLogin(ctx context.Context, target Target, email, password string, w io.Writer) error {
	client := stackClient(target)
	token, err := signIn(ctx, client, target, email, password)
	if err != nil {
		return err
	}
	if err := SaveToken(target.URL, token); err != nil {
		return err
	}

	// Say what the token can do, from the stack rather than from a guess: a
	// person who was never granted management should learn it here, not at
	// their first push.
	who, err := managementWhoAmI(ctx, client, target.URL, token)
	if err != nil {
		fmt.Fprintf(w, "signed in to %s\n", target.URL)
		fmt.Fprintf(w, "note: this account cannot manage that stack yet — ask whoever runs it for\n")
		fmt.Fprintf(w, "  palsvc --grant-management %s\n", email)
		return nil
	}
	fmt.Fprintf(w, "signed in to %s as %s (%s)\n", target.URL, email, who)
	return nil
}

// signIn performs the password sign-in, paying the proof-of-work toll if the
// stack asks for one.
//
// The toll is solved rather than avoided: it is the same gate a real client
// passes, and a tool that could skip it would be a tool whose success says
// nothing about whether a client can sign in.
func signIn(ctx context.Context, client *http.Client, target Target, email, password string) (string, error) {
	base := target.URL
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})

	post := func(extra map[string]string) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/auth/login", bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("content-type", "application/json")
		// The publishable key: /auth/* is the customer contract and every client
		// sends it. Signing in is something an app does, and this does it the
		// same way rather than through a private door.
		if target.AnonKey != "" {
			req.Header.Set("apikey", target.AnonKey)
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("reach %s: %w\n(a self-signed certificate needs `palbase link --insecure`)", base, err)
		}
		defer func() { _ = res.Body.Close() }()
		out, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return res.StatusCode, out, err
	}

	status, out, err := post(nil)
	if err != nil {
		return "", err
	}

	if status == http.StatusForbidden {
		var challenge struct {
			Error     string `json:"error"`
			Challenge struct {
				ID         string `json:"id"`
				Prefix     string `json:"prefix"`
				Difficulty int    `json:"difficulty"`
			} `json:"challenge"`
		}
		if json.Unmarshal(out, &challenge) == nil && challenge.Error == "pow_required" {
			nonce := solveProofOfWork(challenge.Challenge.Prefix, challenge.Challenge.Difficulty)
			status, out, err = post(map[string]string{
				"X-PoW-Challenge-ID": challenge.Challenge.ID,
				"X-PoW-Nonce":        nonce,
			})
			if err != nil {
				return "", err
			}
		}
	}

	if status != http.StatusOK {
		return "", fmt.Errorf("that stack refused the sign-in (%d): %s", status, trimBody(out))
	}
	var ok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(out, &ok); err != nil || ok.AccessToken == "" {
		return "", fmt.Errorf("the stack answered 200 without a token: %s", trimBody(out))
	}
	return ok.AccessToken, nil
}

// solveProofOfWork finds the first nonce whose SHA-256 over prefix‖nonce has
// `difficulty` leading zero bits — the server's own rule, applied to the string
// the header will carry.
func solveProofOfWork(prefix string, difficulty int) string {
	for n := 0; ; n++ {
		candidate := strconv.Itoa(n)
		sum := sha256.Sum256([]byte(prefix + candidate))
		if binary.BigEndian.Uint64(sum[:8])>>(64-uint(difficulty)) == 0 {
			return candidate
		}
	}
}

// managementWhoAmI asks the stack what this token may do. An error means "not
// management", which is a normal answer for an ordinary account.
func managementWhoAmI(ctx context.Context, client *http.Client, base, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/management/whoami", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whoami answered %d", res.StatusCode)
	}
	var who struct {
		User struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&who); err != nil {
		return "", err
	}
	return who.User.Role, nil
}

// stackClient talks to one stack, trusting its self-signed certificate only when
// the link said to.
func stackClient(t Target) *http.Client {
	c := &http.Client{Timeout: 5 * time.Minute}
	if t.Insecure {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in at link time
	}
	return c
}

var _ = os.Stdout
var _ = strings.TrimSpace
