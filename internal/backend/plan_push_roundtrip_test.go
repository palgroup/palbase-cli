package backend

// plan_push_roundtrip_test.go — THE PLAN A PUSH WAS GIVEN MUST SURVIVE ITS GATE.
//
// The gate that refuses a stale plan recomputes the plan's measurements and
// names whatever diverged. Every existing test of it HANDS IT a plan file it
// wrote by hand — so the one thing none of them could ever measure is whether a
// plan the real `palbase plan` produced still matches when the real
// `palbase push` recomputes it. That is the whole point of the gate, and it is
// the round trip a person actually types.

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestAPlanJustWrittenIsNotStaleToThePushThatFollows(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	pushableCheckout(t, dir)

	target := newProjectServer(t, map[string]string{})
	asCloudProject(t, target.URL)
	cred := Credentials{Value: "k", Kind: KindKey}

	var planOut strings.Builder
	if err := runPlan(context.Background(), dir, Target{URL: target.URL}, cred, &planOut); err != nil {
		t.Fatalf("plan: %v\n%s", err, planOut.String())
	}

	var pushOut strings.Builder
	err := runStackPush(context.Background(), Target{URL: target.URL}, cred, false, false, &pushOut)
	// The fake project cannot complete a deploy, so a push against it fails —
	// but it must not fail HERE. "plan is stale" straight after `plan` means
	// the gate measures something the plan never measured, and no amount of
	// re-planning can ever satisfy it.
	if err != nil && strings.Contains(err.Error(), "plan is stale") {
		t.Fatalf("the push refused the plan written seconds earlier: %v", err)
	}
}
