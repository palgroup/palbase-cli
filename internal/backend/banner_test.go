package backend

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// THE LOCAL-STACK AND PROJECT-NAME CLAIMS MOVED, THEY DID NOT VANISH.
//
// Two tests stood here and drove `PrintTarget`, the project-only banner that no
// longer exists (see banner.go for why it went). Their claims live on where the
// answer does: `(local)` is measured by TestPrintResolvedStillMarksALocalStack
// below, and "the banner names the project" became "the banner names the
// project AND the environment" in TestPrintResolvedNamesProjectAndEnvironment —
// which is the same sentence made reachable.

// TestAnUnlinkedCheckoutIsRefusedWithBothWaysIn is FR-008: the refusal has to
// carry the fix, and there are two of them — a cloud project and something
// running here — because the person who hit this does not yet know which one
// they want.
func TestAnUnlinkedCheckoutIsRefusedWithBothWaysIn(t *testing.T) {
	inScratchCheckout(t)

	var out bytes.Buffer
	_, err := PrintResolvedTo(&out, cmdFor(t))
	if err == nil {
		t.Fatal("an unlinked checkout was accepted")
	}
	for _, want := range []string{"palbase link <project>", "palbase link <url>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not offer %q: %v", want, err)
		}
	}
	if out.Len() != 0 {
		t.Errorf("a target was announced before one was resolved: %q", out.String())
	}
}

// A REFUSAL MAY ONLY NAME A WAY OUT THAT WORKS, and `--environment <ref>` is
// not one.
//
// Every one of these lines is printed at the moment somebody is stuck, so the
// last line is the one they type. `--environment` alone resolves NOTHING: with
// no `--project`, selection.Resolver reads `.palbase/selection.json` for the
// project id first, and an unlinked checkout has no such file — the run dies
// with "no project selected" before the environment flag is ever consulted
// (proved in TestResolve_EnvironmentFlagAloneNamesNoProject). Measured live
// 2026-08-26: `--environment` alone died with "no project selected", linked it
// was ignored. It addresses a cloud project in ZERO configurations.
//
// `palbase link <ref>` is the line that does what the reader wanted: a bare ref
// IS an address this cloud knows (project_link.go resolves `<ref>` to
// `https://<ref>.<PublicHost>`), so one environment can be acted on by name.
func TestTheUnlinkedRefusalsOfferNoFlagThatCannotSelect(t *testing.T) {
	inScratchCheckout(t)

	_, readErr := ReadTarget()
	if readErr == nil {
		t.Fatal("an unlinked checkout resolved a target")
	}
	for name, err := range map[string]error{
		"ReadTarget": readErr,
	} {
		if strings.Contains(err.Error(), "--environment") {
			t.Errorf("%s offers --environment, which selects nothing without --project: %v", name, err)
		}
		if !strings.Contains(err.Error(), "palbase link <ref>") {
			t.Errorf("%s never names the way to act on one environment: %v", name, err)
		}
	}
}

// THE BANNER NAMES THE ENVIRONMENT, and that is the half that was tested but
// unreachable for months: `Target.Env` had assertions and no production writer,
// so "the banner prints project/env" was true in the suite and false in the
// product. It is the resolver's answer now, so the assertion lives where the
// answer does.
func TestPrintResolvedNamesProjectAndEnvironment(t *testing.T) {
	inScratchCheckout(t)
	if err := WriteTarget(Target{Project: "prd_a", Name: "todoapp"}); err != nil {
		t.Fatal(err)
	}
	resolverRig(t, []Environment{{Ref: "mu0028", Name: "staging", Status: "Running"}})

	var out bytes.Buffer
	got, err := PrintResolvedTo(&out, cmdFor(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Env != "staging" {
		t.Errorf("resolved env = %q", got.Env)
	}
	if s := out.String(); s != "▸ todoapp/staging\n" {
		t.Errorf("banner = %q, want the project AND the environment", s)
	}
}

// A RUNNING LOCAL STACK STILL SAYS SO — `(local)` is the whole warning that
// this push is not going to the cloud.
func TestPrintResolvedStillMarksALocalStack(t *testing.T) {
	inScratchCheckout(t)
	if err := WriteTarget(Target{Project: "prd_a", Name: "todoapp"}); err != nil {
		t.Fatal(err)
	}
	resolverRig(t, []Environment{{Ref: "mu0028", Name: "staging"}})
	local, err := localPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureMachineStateDir(local); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte(`{"url":"http://127.0.0.1:54321"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := PrintResolvedTo(&out, cmdFor(t)); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); s != "▸ http://127.0.0.1:54321 (local)\n" {
		t.Errorf("banner = %q", s)
	}
}

// THE REFUSAL REACHES THE READER. A verb that cannot resolve an environment
// must print WHY — the list of environments — not a bare error somewhere else.
func TestPrintResolvedSurfacesTheRefusal(t *testing.T) {
	inScratchCheckout(t)
	if err := WriteTarget(Target{Project: "prd_a", Name: "todoapp"}); err != nil {
		t.Fatal(err)
	}
	resolverRig(t, []Environment{
		{Ref: "j06bwtuum", Name: "main"},
		{Ref: "mu0028", Name: "staging"},
	})

	var out bytes.Buffer
	_, err := PrintResolvedTo(&out, cmdFor(t))
	if err == nil {
		t.Fatal("two environments and no selection resolved anyway")
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Errorf("the refusal does not list the environments: %v", err)
	}
}
