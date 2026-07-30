package project

import "testing"

// The remote parser exists because a developer never chose which URL form git
// gave them, and guessing the wrong repository would connect someone else's code
// to their project. Both GitHub forms in, `owner/name` out; anything else is an
// error rather than a guess.
func TestParseGitHubRemote(t *testing.T) {
	ok := map[string]string{
		"git@github.com:palgroup/centauri-backdoor.git":     "palgroup/centauri-backdoor",
		"git@github.com:palgroup/centauri-backdoor":         "palgroup/centauri-backdoor",
		"https://github.com/palgroup/centauri-backdoor.git": "palgroup/centauri-backdoor",
		"https://github.com/palgroup/centauri-backdoor":     "palgroup/centauri-backdoor",
		"http://github.com/pal-salih/todoapp.git":           "pal-salih/todoapp",
		"ssh://git@github.com/palgroup/my.backend-2.git":    "palgroup/my.backend-2",
		"  https://github.com/palgroup/spaced.git  ":        "palgroup/spaced",
	}
	for in, want := range ok {
		got, err := parseGitHubRemote(in)
		if err != nil {
			t.Errorf("parseGitHubRemote(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseGitHubRemote(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"git@gitlab.com:owner/name.git",       // another host: connecting it would be wrong
		"https://github.com/owner",            // no repo
		"https://github.com/owner/name/extra", // not owner/name
		"",                                    // no remote at all
		"file:///tmp/repo",                    // local clone
	}
	for _, in := range bad {
		if got, err := parseGitHubRemote(in); err == nil {
			t.Errorf("parseGitHubRemote(%q) = %q, want an error", in, got)
		}
	}
}
