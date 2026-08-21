package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The proof-of-work solver must agree with the SERVER's rule, bit for bit.
//
// A hex-prefix implementation passes at difficulty 4, 8, 12 … and silently
// produces nonces the server rejects at every other value. So the check here is
// the server's arithmetic, not a literal.
func TestSolveProofOfWorkMeetsTheBitRule(t *testing.T) {
	for _, difficulty := range []int{1, 3, 7, 10, 13} {
		ch := powChallenge{ID: "pow_1", Prefix: "abc123", Difficulty: difficulty}
		_, nonce := solveProofOfWork(ch)
		sum := sha256.Sum256([]byte(ch.Prefix + strconv.FormatUint(nonce, 10)))
		if got := binary.BigEndian.Uint64(sum[:8]) >> uint(64-difficulty); got != 0 {
			t.Fatalf("difficulty %d: nonce %d yields leading bits %d, want 0", difficulty, nonce, got)
		}
		// And it must be the FIRST such nonce: a solver that skips candidates
		// still "passes" while doing more work than the gate asked for.
		for n := uint64(0); n < nonce; n++ {
			s := sha256.Sum256([]byte(ch.Prefix + strconv.FormatUint(n, 10)))
			if binary.BigEndian.Uint64(s[:8])>>uint(64-difficulty) == 0 {
				t.Fatalf("difficulty %d: skipped a valid nonce %d before %d", difficulty, n, nonce)
			}
		}
	}
}

func testToken(t *testing.T, sub, email string) string {
	t.Helper()
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256"}`))
	claims, err := json.Marshal(map[string]string{"sub": sub, "email": email})
	if err != nil {
		t.Fatal(err)
	}
	return head + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

type recorded struct {
	path      string
	apikey    string
	powID     string
	powNonce  string
	bodyEmail string
}

// A control plane that behaves like the real one: it refuses the first attempt
// with a challenge and only then issues a session.
func fakePlane(t *testing.T, difficulty int) (*httptest.Server, *[]recorded) {
	t.Helper()
	var calls []recorded
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cloud/config", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, recorded{path: r.URL.Path})
		_ = json.NewEncoder(w).Encode(Bootstrap{
			AnonKey:      "pb_project_test_anon",
			Issuer:       "https://plane.test",
			TenantDomain: "plane.test",
		})
	})
	handleAuth := func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, recorded{
			path:      r.URL.Path,
			apikey:    r.Header.Get("apikey"),
			powID:     r.Header.Get("X-PoW-Challenge-ID"),
			powNonce:  r.Header.Get("X-PoW-Nonce"),
			bodyEmail: body["email"],
		})
		if r.Header.Get("X-PoW-Nonce") == "" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(powRejection{
				Error:     "pow_required",
				Challenge: powChallenge{ID: "pow_abc", Prefix: "seed", Difficulty: difficulty},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": testToken(t, "usr_from_token", "who@plane.test"),
			"expires_in":   3600,
		})
	}
	mux.HandleFunc("/auth/login", handleAuth)
	mux.HandleFunc("/auth/signup", handleAuth)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

// Signing in solves the challenge and replays the SAME request.
func TestSignInSolvesTheChallengeAndReplays(t *testing.T) {
	srv, calls := fakePlane(t, 8)
	c := NewCloudClient(srv.URL)

	boot, err := c.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	creds, err := c.SignIn(context.Background(), boot, "me@plane.test", "hunter2")
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}

	if creds.AccessToken == "" {
		t.Fatal("no token stored")
	}
	if creds.User.ID != "usr_from_token" {
		t.Fatalf("identity not read from the token: %+v", creds.User)
	}
	if creds.IsExpired() {
		t.Fatal("a fresh session is already expired")
	}

	login := []recorded{}
	for _, c := range *calls {
		if c.path == "/auth/login" {
			login = append(login, c)
		}
	}
	if len(login) != 2 {
		t.Fatalf("expected the attempt to be replayed once, got %d calls", len(login))
	}
	if login[0].apikey == "" || login[1].apikey == "" {
		t.Fatal("the anon apikey was not sent — /auth/* refuses without it")
	}
	if login[1].powID != "pow_abc" || login[1].powNonce == "" {
		t.Fatalf("the replay carried no solution: %+v", login[1])
	}
	// Same request, not a fresh one: a changed body earns a fresh challenge and
	// the loop never closes.
	if login[0].bodyEmail != login[1].bodyEmail {
		t.Fatal("the replay changed the request body")
	}
}

// A control plane that publishes no anon key cannot be signed in to, and saying
// so here beats a 401 the person cannot explain.
func TestBootstrapRefusesAnEmptyAnonKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Bootstrap{Issuer: "https://plane.test"})
	}))
	t.Cleanup(srv.Close)

	if _, err := NewCloudClient(srv.URL).Bootstrap(context.Background()); err == nil {
		t.Fatal("an empty anon key was accepted")
	}
}

// A rejection must arrive as the server's own words.
func TestSignInSurfacesTheServersReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_credentials","error_description":"Email or password is incorrect"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := NewCloudClient(srv.URL).SignIn(context.Background(),
		Bootstrap{AnonKey: "k"}, "me@plane.test", "wrong")
	if err == nil {
		t.Fatal("a rejected sign-in reported success")
	}
	if !strings.Contains(err.Error(), "Email or password is incorrect") {
		t.Fatalf("the server's reason was dropped: %v", err)
	}
}

// A session with no expires_in must not be born expired: every later call would
// refuse a token that is in fact valid.
func TestSessionWithoutExpiryIsNotBornExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"` + testToken(t, "usr_x", "x@plane.test") + `"}`))
	}))
	t.Cleanup(srv.Close)

	creds, err := NewCloudClient(srv.URL).SignIn(context.Background(),
		Bootstrap{AnonKey: "k"}, "x@plane.test", "pw")
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if creds.IsExpired() {
		t.Fatal("a session with no expires_in was stored as already expired")
	}
}
