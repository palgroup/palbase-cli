package backend

// The consent's LAST HOP — the one link nobody was testing.
//
// Break-glass reaches the server as a query parameter, and it crossed three
// pieces of code to get there: the cobra flag, `runStackPush`'s parameter, and
// `pushURL`. Two of them were pinned. `TestPushCmd_FlagsReachTheRightConsents`
// STUBS `stackPush`, so `runStackPush`'s body never runs in it, and
// `TestPushURL_CarriesBothConsents` exercises `pushURL` in isolation. Between
// them sat one line — `runStackPush` handing its own parameter on — that no test
// touched.
//
// Measured: `pushURL(target.URL, approve, false)` left the whole package green
// (64s, zero failures). The operator types `--accept-breaking`, the flag arrives
// correctly, the URL builder works when asked, and the consent dies in the last
// jump. That is the shape this run has now hit seven times: each piece correct,
// the join untested.
//
// It is asserted through the OUTPUT rather than a captured request because the
// sentence a person reads and the request that goes are now the same string —
// `consentsIn(url)` reads the URL itself. A transcript that says a breaking
// change was forced is also the thing the next person greps for when a column
// disappeared, which is why this line exists at all.
//
// WHAT THIS DOES NOT CATCH, measured rather than assumed: if someone recomputes
// the sentence from the booleans instead of reading `url` — a second `pushURL`
// call inside this function — the transcript stays right while the request goes
// wrong, and this test passes. I ran that mutation; it is green. The single-value
// property is the whole guarantee here and it lives in the code, not in this
// file: one `pushURL` call, printed and sent. A second one is the regression.

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// pushOutput runs a push far enough to have decided where it is going, and
// returns everything it printed. It is expected to fail afterwards — this
// directory is not a backend — and the failure is not what is under test.
func pushOutput(t *testing.T, approve, acceptBreaking bool) string {
	t.Helper()
	var out bytes.Buffer
	// A non-local target, so the local-stack refusal (which returns before any
	// of this) does not fire.
	target := Target{URL: "https://stack.example"}
	_ = runStackPush(context.Background(), target, Credentials{}, approve, acceptBreaking, &out)
	return out.String()
}

func TestAStackPushSaysWhatItConsentedTo(t *testing.T) {
	for _, tc := range []struct {
		name             string
		approve, breakG  bool
		wants, wantsNone []string
	}{
		{
			name:   "cam-kır çıktıda ADIYLA görünür",
			breakG: true,
			wants:  []string{"accept-breaking", "BREAKING"},
		},
		{
			name:    "veri kaybı onayı da",
			approve: true,
			wants:   []string{"data loss"},
			// …and it must not claim a consent nobody gave.
			wantsNone: []string{"BREAKING"},
		},
		{
			name:      "onaysız push hiçbir rıza iddia etmez",
			wantsNone: []string{"consents:"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pushOutput(t, tc.approve, tc.breakG)
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("çıktı %q demiyor:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.wantsNone {
				if strings.Contains(got, unwanted) {
					t.Errorf("çıktı verilmemiş bir rızayı %q diye anıyor:\n%s", unwanted, got)
				}
			}
		})
	}
}
