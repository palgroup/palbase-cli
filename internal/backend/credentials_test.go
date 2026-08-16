package backend

// credentials_test.go — one resolver, three sources, and a file two processes
// can write at once.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestTheEnvironmentWinsAndTouchesNoFile is the AI-in-a-container case: a token
// in the environment, no browser, no home directory to depend on.
func TestTheEnvironmentWinsAndTouchesNoFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(AccessTokenEnv, "pat-from-the-environment")

	token, source, err := Credential("https://todoapp.palbase.studio")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if token != "pat-from-the-environment" || source != SourceEnv {
		t.Fatalf("resolved %q from %s", token, source)
	}
	if _, err := os.Stat(filepath.Join(home, ".palbase", "credentials.json")); !os.IsNotExist(err) {
		t.Error("resolving from the environment created a credentials file")
	}
}

// TestTheStoreIsKeyedByURL: one machine, several projects, and a local stack
// whose credential `palbase start` writes for every checkout to find.
func TestTheStoreIsKeyedByURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(AccessTokenEnv, "")

	if err := StoreCredential("http://localhost:54321", "local-key"); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential("https://todoapp.palbase.studio", "cloud-session"); err != nil {
		t.Fatal(err)
	}

	for url, want := range map[string]string{
		"http://localhost:54321":         "local-key",
		"https://todoapp.palbase.studio": "cloud-session",
	} {
		token, source, err := Credential(url)
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		if token != want || source != SourceStore {
			t.Errorf("%s resolved %q from %s, want %q from the store", url, token, source, want)
		}
	}

	info, err := os.Stat(filepath.Join(home, ".palbase", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the credentials file is %v — it is the ability to deploy to every project in it", info.Mode().Perm())
	}
}

// TestTheRefusalNamesBothWaysIn: a person with no credential is one sentence
// away from having one, and the sentence has to say which one applies.
func TestTheRefusalNamesBothWaysIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "")

	_, _, err := Credential("http://localhost:54321")
	if err == nil {
		t.Fatal("an unknown target resolved a credential")
	}
	for _, want := range []string{"palbase start", "palbase login", AccessTokenEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// TestConcurrentWritesDoNotLoseACredential is the shared-machine case: two
// agents in two panes, or a `start` finishing while a `link` writes. A
// read-modify-write without a lock loses whichever finished first, and the
// symptom is being signed out of a project you just linked.
func TestConcurrentWritesDoNotLoseACredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "")

	const writers = 16
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			url := "http://localhost:" + string(rune('a'+i))
			if err := StoreCredential(url, "token-"+string(rune('a'+i))); err != nil {
				t.Errorf("write %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < writers; i++ {
		url := "http://localhost:" + string(rune('a'+i))
		token, _, err := Credential(url)
		if err != nil {
			t.Fatalf("%s was lost: %v", url, err)
		}
		if token != "token-"+string(rune('a'+i)) {
			t.Errorf("%s resolved %q", url, token)
		}
	}
}

// TestForgettingOneLeavesTheOthers — `palbase logout` is per target.
func TestForgettingOneLeavesTheOthers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "")

	if err := StoreCredential("http://localhost:54321", "local"); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential("https://cloud.example", "cloud"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetCredential("http://localhost:54321"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Credential("http://localhost:54321"); err == nil {
		t.Error("the forgotten credential still resolves")
	}
	if _, _, err := Credential("https://cloud.example"); err != nil {
		t.Errorf("forgetting one target removed another: %v", err)
	}
}
