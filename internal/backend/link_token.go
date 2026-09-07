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

	// REDDİ, CEVAP VEREMEMEKTEN AYIR (FR-063).
	//
	// Bu doğrulama, tuğlalaşmış bir kiracıyı kurtarmayı İMKÂNSIZ kılıyordu ve
	// kusurun en kötü yanı mesajıydı: servis etmeyen bir kiracıda kapı 503
	// döndürüyor, kod onu "yığın bu anahtarı kabul etmedi" diye okuyordu —
	// oysa yığın anahtarı HİÇ GÖRMEDİ. Kullanıcı doğru anahtarı yanlış sanıp
	// aramaya gidiyordu.
	//
	// Ve döngü kapanıyordu: `push`un çaresi bir kimlik, kimliğin çaresi `link`,
	// `link`in şartı da CANLI bir proje. Ölü kiracının kurtulmasının önünde
	// duran son halka buydu (ölçüldü 07.09.2026, penny `na1m7lt2m` — anahtarı
	// elle yazmak zorunda kaldım).
	//
	// AYRIM DAR VE ANLAMLI: 401/403 yığının KENDİ cevabıdır ve gerçek bir
	// reddir — hiçbir şey saklanmaz. Taşıma hatası ya da 502/503/504 ise
	// "soramadım"dır: anahtar saklanır ve doğrulanamadığı AÇIKÇA söylenir.
	// Sessizce saklamak da, reddetmek de yanlış olurdu.
	res, err := stackClient(target).Do(req)
	if err != nil {
		if serr := StoreCredential(target.URL, cred); serr != nil {
			return serr
		}
		return errUnverifiedToken{target: target.Describe(), reason: err.Error()}
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))

	switch {
	case res.StatusCode == http.StatusOK:
		return StoreCredential(target.URL, cred)
	case res.StatusCode >= 500:
		if serr := StoreCredential(target.URL, cred); serr != nil {
			return serr
		}
		return errUnverifiedToken{target: target.Describe(), reason: fmt.Sprintf("it answered %d", res.StatusCode)}
	default:
		return fmt.Errorf("%s did not accept this token (%d) — nothing was stored",
			target.Describe(), res.StatusCode)
	}
}

// errUnverifiedToken: anahtar SAKLANDI ama doğrulanamadı. Hata olarak taşınır
// çünkü kullanıcı bunu bilmeli; `link` onu ölümcül saymaz.
type errUnverifiedToken struct{ target, reason string }

func (e errUnverifiedToken) Error() string {
	return fmt.Sprintf("%s is not answering (%s), so this key could not be checked against it — "+
		"it was remembered anyway so `palbase plan` and `palbase push` can reach the platform; "+
		"if the key is wrong those will say so", e.target, e.reason)
}

// readTokenFrom takes the token off a reader so it never has to be typed on a
// command line, where it would be recorded in the shell's history and visible
// in the process list. Mirrors `palbase secret set --stdin`.
//
// Only the trailing newline goes: a token is one line, and an `echo` that
// feeds one would otherwise be rejected for the newline it appended.
//
// The cap is read one byte PAST the line so an oversized input is refused
// rather than silently cut: a truncated token is still a well-formed string,
// so the caller would only learn about it as "did not accept this token",
// which points at the stack instead of at the pipe.
const maxTokenBytes = 1 << 16

func readTokenFrom(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxTokenBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxTokenBytes {
		return "", fmt.Errorf("the token on stdin is larger than %d bytes — nothing was stored", maxTokenBytes)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("no token on stdin")
	}
	return token, nil
}
