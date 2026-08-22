package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// A SELF-HOSTED STACK HAS NOBODY TO ASK ABOUT IT.
//
// The credential chain has four links and a stack running on somebody's own
// cluster matches only one of them: the store. It is not on this machine, so
// `palbase start` has nothing to read; it is not in our ledger, so signing in
// buys nothing — `doctor` says "not logged in" and the link works anyway,
// which is the tell.
//
// That left `PALBASE_ACCESS_TOKEN`, and an environment variable is a poor
// place for a secret: every process in the shell can read it, and typed inline
// it lands in the history file. The store is a 0600 file that already existed
// for exactly this case — it just had no caller.
//
// Verifying BEFORE writing is the point. A stored credential the stack refuses
// turns every later command into "did not accept this credential", with
// nothing to say which of the two is stale.
func storeVerifiedToken(ctx context.Context, target Target, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("empty token")
	}
	cred := Credentials{Value: token, Kind: kindOf(token)}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(target.URL, "/")+"/v1/management/keys", nil)
	if err != nil {
		return err
	}
	cred.Apply(req)

	res, err := stackClient(target).Do(req)
	if err != nil {
		return fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s did not accept this token (%d) — nothing was stored",
			target.Describe(), res.StatusCode)
	}
	return StoreCredential(target.URL, cred)
}

// readTokenFrom takes the token off a reader so it never has to be typed on a
// command line, where it would be recorded in the shell's history and visible
// in the process list. Mirrors `palbase secret set --stdin`.
//
// Only the trailing newline goes: a token is one line, and an `echo` that
// feeds one would otherwise be rejected for the newline it appended.
func readTokenFrom(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 1<<16))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("no token on stdin")
	}
	return token, nil
}
