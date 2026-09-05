package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A REFUSAL MUST NAME A COMMAND THAT EXISTS.
//
// `palbase pull` in an unlinked directory answered:
//
//	this directory is not linked to a Palbase project — run `palbase web link`,
//	`palbase ios link`, `palbase macos link` or `palbase android link` first
//
// Every one of those four had been retired (FR-009). The reader who followed the
// advice got `unknown command "web" for "palbase"` — a refusal whose only remedy
// could not be typed. Nothing was red: the string is a string, and the commands
// it named had been deleted three tasks earlier in the same run.
//
// This is the same defect as the docs teaching a retired spelling, one layer in.
// The corpus gate reads Markdown; this one reads the binary's own advice.
//
// THE AUTHORITY IS DERIVED, NOT LISTED. The declared set comes from the cobra
// `Use:` fields in this repository, so a command that comes back makes this gate
// go quiet on its own, and a command that goes away makes it speak — neither
// needs anybody to remember to edit a list here. A hand-kept list would be one
// more thing to forget in exactly the moment it mattered.
//
// AND IT READS LITERALS, NOT TEXT. An AST walk sees only what ships in a string;
// the comments that DISCUSS a retired command — which is most of the mentions in
// this package, and all of them legitimate — are invisible to it. A grep would
// have had to guess which mention was prose, and guessing the subject is how
// four earlier gates in this run measured the wrong thing.
func TestEveryCommandTheCLIAdvisesExists(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")

	declared, advised, err := commandVocabulary(root)
	if err != nil {
		t.Fatal(err)
	}

	// THE PARSE IS MEASURED FIRST. An extraction that found nothing would report
	// a clean corpus, which is the failure mode this run hit twice.
	if len(declared) < 40 {
		t.Fatalf("found only %d declared commands — the Use: extraction is broken, not the code", len(declared))
	}
	for _, must := range []string{"link", "push", "start", "init", "unlink", "upgrade"} {
		if !declared[must] {
			t.Errorf("`%s` is a command this CLI ships, but the extraction did not see it", must)
		}
	}

	var bad []string
	for _, a := range advised {
		if !declared[a.word] {
			bad = append(bad, a.where+" advises `palbase "+a.word+"`")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("these strings advise a command this CLI does not have:\n%s\n\n"+
			"the reader who follows one gets `unknown command`, and the advice was the "+
			"only thing standing between them and the fix", strings.Join(bad, "\n"))
	}
}

// NEGATIVE CONTROL. A rule that never fires reports silence it did not earn, so
// this drives the same extraction over a literal the CLI carried until today.
func TestTheAdviceGateCatchesARetiredCommand(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	declared, _, err := commandVocabulary(root)
	if err != nil {
		t.Fatal(err)
	}
	// The exact words of the retired platform groups (FR-009) and of the two
	// addressing verbs the v2 cutover removed.
	for _, retired := range []string{"ios", "web", "macos", "android", "env", "apps", "github"} {
		if declared[retired] {
			t.Errorf("`palbase %s` is declared again — if that is deliberate, this control "+
				"has to name a command that is actually gone", retired)
		}
	}
	// And the rule must MATCH such a string when it meets one.
	if got := advisedWords(`run ` + "`palbase ios link`" + ` first`); len(got) != 1 || got[0] != "ios" {
		t.Errorf("the extraction missed its own example: %v", got)
	}
	// While leaving alone what is not advice about a subcommand.
	if got := advisedWords("palbase --help prints the tree"); len(got) != 0 {
		t.Errorf("a flag was read as a command: %v", got)
	}
}

type advisedCommand struct{ where, word string }

// adviceMention finds `palbase <word>` inside a shipped string. The word must
// start with a letter, which is what keeps `palbase --help` and `palbase -v` out.
var adviceMention = regexp.MustCompile(`palbase ([a-z][a-z0-9-]*)`)

func advisedWords(s string) []string {
	var out []string
	for _, m := range adviceMention.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// commandVocabulary reads the repository twice over: the cobra `Use:` fields it
// DECLARES, and the `palbase <word>` advice its shipped strings GIVE.
func commandVocabulary(root string) (declared map[string]bool, advised []advisedCommand, err error) {
	declared = map[string]bool{}
	fset := token.NewFileSet()

	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(parsed, func(n ast.Node) bool {
			// `Use: "link <ref>"` → the command's first word.
			if kv, isKV := n.(*ast.KeyValueExpr); isKV {
				if key, isIdent := kv.Key.(*ast.Ident); isIdent && key.Name == "Use" {
					if lit, isLit := kv.Value.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
						if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
							if first := strings.Fields(v); len(first) > 0 {
								declared[first[0]] = true
							}
						}
					}
				}
			}
			lit, isLit := n.(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				return true
			}
			value, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			for _, word := range advisedWords(value) {
				advised = append(advised, advisedCommand{
					where: rel + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line),
					word:  word,
				})
			}
			return true
		})
		return nil
	})
	// `palbase` is the root's own name and every command sits under it.
	declared["palbase"] = true
	return declared, advised, err
}
