package backend

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestPrintTargetNamesTheLocalStack: while a stack is up, that is where the verb
// acts, and the line says so plainly — `(local)` is the whole warning that this
// push is not going to the cloud.
func TestPrintTargetNamesTheLocalStack(t *testing.T) {
	inScratchCheckout(t)
	if err := WriteTarget(Target{URL: "https://todoapp.palbase.studio", Project: "todoapp", Env: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath(), []byte(`{"url":"http://localhost:54321"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	target, err := PrintTarget(&out)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "▸ http://localhost:54321 (local)\n" {
		t.Errorf("banner = %q", got)
	}
	if !target.Local {
		t.Error("the resolved target does not know it is local")
	}
}

// TestPrintTargetNamesTheCloudEnvironment: no local stack, so the committed
// project file decides — and the environment is part of the name, because
// `todoapp` alone does not distinguish staging from production.
func TestPrintTargetNamesTheCloudEnvironment(t *testing.T) {
	inScratchCheckout(t)
	if err := WriteTarget(Target{URL: "https://staging.palbase.studio", Project: "todoapp", Env: "staging"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := PrintTarget(&out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "▸ todoapp/staging\n" {
		t.Errorf("banner = %q", got)
	}
}

// TestAnUnlinkedCheckoutIsRefusedWithBothWaysIn is FR-008: the refusal has to
// carry the fix, and there are two of them — a cloud project and something
// running here — because the person who hit this does not yet know which one
// they want.
func TestAnUnlinkedCheckoutIsRefusedWithBothWaysIn(t *testing.T) {
	inScratchCheckout(t)

	var out bytes.Buffer
	_, err := PrintTarget(&out)
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
