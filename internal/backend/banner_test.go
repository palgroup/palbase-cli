package backend

import (
	"bytes"
	"github.com/spf13/cobra"
	"os"
	"strings"
	"testing"
)

// TestPrintTargetNamesTheLocalStack: while a stack is up, that is where the verb
// acts, and the line says so plainly — `(local)` is the whole warning that this
// push is not going to the cloud.
func TestPrintTargetNamesTheLocalStack(t *testing.T) {
	inScratchCheckout(t)
	if err := WriteTarget(Target{Project: "todoapp", Env: "main"}); err != nil {
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
	if err := WriteTarget(Target{Project: "todoapp", Env: "staging"}); err != nil {
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

func TestTheCloudSelectionFlagsAreRefusedRatherThanIgnored(t *testing.T) {
	inScratchCheckout(t)
	target := Target{URL: "https://127.0.0.1"}

	root := &cobra.Command{Use: "palbase"}
	root.PersistentFlags().String("project", "", "")
	root.PersistentFlags().String("environment", "", "")
	child := &cobra.Command{Use: "push"}
	root.AddCommand(child)

	if err := refuseCloudSelectionFlags(child, target); err != nil {
		t.Fatalf("an untouched flag was refused: %v", err)
	}

	if err := root.PersistentFlags().Set("project", "bogus"); err != nil {
		t.Fatal(err)
	}
	err := refuseCloudSelectionFlags(child, target)
	if err == nil {
		t.Fatal("--project was accepted and would have been ignored")
	}
	if !strings.Contains(err.Error(), "--project") || !strings.Contains(err.Error(), target.Describe()) {
		t.Errorf("the refusal does not say what was ignored or where it is pointed: %v", err)
	}
	// Named once, not twice: the flag lives on both the command and its root.
	if strings.Count(err.Error(), "--project") != 1 {
		t.Errorf("the flag is named more than once: %v", err)
	}
}
