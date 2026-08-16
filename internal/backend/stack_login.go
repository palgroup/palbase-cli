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
	"path/filepath"
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
	if err := runStackLogin(ctx, target, email, password, w); err != nil {
		return err
	}

	// And finish what `link` could not: the contract needs a session, and this
	// is the first moment there is one. A sign-in that leaves the client
	// ungenerated makes the next step something a person has to remember.
	//
	// A stack with nothing deployed yet has no contract to give, which is a
	// state and not a fault — say it in one line and stop there.
	if err := RefreshSpec(ctx, w); err != nil && !errors.Is(err, ErrNotSignedIn) {
		fmt.Fprintf(w, "note: the contract is not available yet — %v\n", err)
	}
	return nil
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
	// The publishable key comes from the PROJECT, every time.
	//
	// It is remembered in .palbase/target.json so a colleague who clones the
	// repository has one, but a remembered key goes stale: a project rebuilt
	// from nothing mints a new one, and the sign-in then fails with
	// "invalid_api_key" about a key nobody typed. The document is public and one
	// request away, so this asks rather than trusts what it wrote last time.
	if described, err := describeStack(ctx, target.URL, target.Insecure); err == nil {
		if described.AnonKey != target.AnonKey {
			target.AnonKey = described.AnonKey
			if err := WriteTarget(target); err != nil {
				return err
			}
		}
		// And every app's committed config, checked on ITS OWN — that is where
		// the key actually SHIPS. Comparing only the CLI's copy left an app
		// building against a key the project had replaced: the same person's
		// terminal signed in perfectly while the device answered
		// "invalid_api_key", which reads as the app being wrong.
		if err := rewriteAppKeys(target.URL, described.AnonKey, w); err != nil {
			return err
		}
	}

	if password == "" {
		var err error
		if password, err = promptPassword(w); err != nil {
			return err
		}
	}
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

// rewriteAppKeys puts a project's CURRENT publishable key into every committed
// app config in this checkout THAT POINTS AT THAT PROJECT.
//
// The key ships inside the app, so a project rebuilt from nothing — a fresh
// install, a wiped test rail — leaves every linked app holding one that no
// longer exists. The failure then lands on the device ("invalid_api_key") while
// the same person's terminal signs in perfectly, which reads as the app being
// wrong rather than out of date.
//
// baseURL is what keeps that from becoming a worse failure. A checkout can carry
// slots for more than one target — an app with a cloud past and a local present
// has both — and writing this key into a slot whose base_url is somebody else's
// produces a config that points at one host holding another's credential.
// Measured on 2026-08-16: the web slot still named a cloud environment while its
// key had been replaced with a local project's. Stale is obvious; mixed is not.
func rewriteAppKeys(baseURL, anonKey string, w io.Writer) error {
	target := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	for _, dir := range appConfigDirs() {
		path := filepath.Join(dir, "palbase-config.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry pullSpecConfigEntry
		if err := json.Unmarshal(raw, &entry); err != nil || entry.APIKey == anonKey {
			continue
		}
		if strings.TrimSuffix(strings.TrimSpace(entry.BaseURL), "/") != target {
			continue // another target's slot; not this project's to rewrite
		}
		entry.APIKey = anonKey
		blob, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(w, "✓ %s now carries this project's current key — rebuild the app\n", path)
	}
	return nil
}

// appConfigDirs is every directory a link may have written an app config into.
func appConfigDirs() []string {
	dirs := []string{webArtifactsDir}
	for _, platform := range []string{"ios", "macos", "android"} {
		dirs = append(dirs, filepath.Join(nativeArtifactsDir, platform))
	}
	return dirs
}
