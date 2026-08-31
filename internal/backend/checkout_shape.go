package backend

// checkout_shape.go — two things about a CHECKOUT that no other gate measures.
//
// Both were found by the same customer report on 2026-08-31: a tree written
// against @palbase/backend 21.0.1 was upgraded, `palbase build` said
// "build OK — 67 route(s)", `tsc --noEmit` exited 0, and two real defects were
// sitting in the tree the whole time. The pattern in both is a file that NOTHING
// COMPILES: an unmounted module never complains.
//
// They live here rather than in build.go because they are about the SHAPE of the
// checkout, not about the code's validity — they run before a controller is read,
// they need no node, no bun and no network, and they are the cheapest things
// `build` does.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// retiredDeclaration is one of the author-facing config files @palbase/backend
// 23.0.0 removed, paired with the verb that replaced it.
type retiredDeclaration struct {
	file string
	verb string
	// what a person should read BEFORE deleting: the setting may or may not be on
	// their stack, and the CHANGELOG says so outright — "the settings it declared
	// were, in most cases, never on your stack to begin with".
	read string
}

// retiredDeclarations is CLOSED. `config/` is retired entirely, so nothing joins
// this list — which is why naming the seven here carries no drift risk, and why
// deriving them from the SDK's exports would be machinery bought for a set that
// cannot change.
//
// The table is the one in @palbase/backend's 23.0.0 CHANGELOG, and
// cmd/palbase/surface_test.go asserts every verb below still exists.
var retiredDeclarations = []retiredDeclaration{
	{"config/egress.ts", "palbase egress add <host>", "palbase egress list"},
	{"config/flags.ts", "palbase flags add <key> --type ...", "palbase flags list"},
	{"config/storage.ts", "palbase storage add <name>", "palbase storage list"},
	{"config/notifications.ts", "palbase notifications add <provider>", "palbase notifications providers"},
	{"config/test-users.ts", "palbase test-user templates set --file <path>", "palbase test-user templates"},
	{"config/secrets.ts", "palbase secret set NAME --stdin", "palbase secret list"},
	{"config/auth.json", "palbase auth settings set", "palbase auth settings"},
}

// deadDeclarations returns the retired declaration files present in dir.
//
// ‼️ IT MATCHES FILE NAMES, NOT THE DIRECTORY. `config/` may legitimately hold a
// project's own modules — the tree this was found on keeps `config/pricing.ts`,
// its price table, imported by `webhooks/stripe.ts` and nothing to do with
// palbase. A gate keyed on the directory would refuse a correct repository, which
// is a worse failure than the one it is here to catch.
func deadDeclarations(dir string) []retiredDeclaration {
	var found []retiredDeclaration
	for _, d := range retiredDeclarations {
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(d.file))); err == nil && !st.IsDir() {
			found = append(found, d)
		}
	}
	return found
}

// reportDeadDeclarations prints the refusal. Returns true when there was one.
func reportDeadDeclarations(dir string, out io.Writer) bool {
	dead := deadDeclarations(dir)
	if len(dead) == 0 {
		return false
	}
	fmt.Fprintf(out, "✗ %s — @palbase/backend 23.0.0 removed the surface they use.\n",
		plural(len(dead), "retired declaration file", "retired declaration files"))
	fmt.Fprintln(out, "  Nothing has read them since: not this build, not the push, not tsc (`config/` is in no")
	fmt.Fprintln(out, "  include list, by design). Whatever they declare is NOT on your stack unless somebody")
	fmt.Fprintln(out, "  wrote it with the CLI.")
	for _, d := range dead {
		fmt.Fprintf(out, "\n  %s\n", d.file)
		fmt.Fprintf(out, "    read what your stack actually holds:  %s\n", d.read)
		fmt.Fprintf(out, "    write it:                             %s\n", d.verb)
	}
	fmt.Fprintln(out, "\n  Then delete the file(s). A copy in the source tree can only disagree with the live one.")
	return true
}

// compiledEntryDirs are the directories the deploy reads OFF DISK as entry
// points — one default-exported class per file, discovered by walking, never
// imported by anything.
//
// That is the whole criterion, and it is why `models/`, `services/` and `db/` are
// absent: a controller imports them, so tsc pulls them in transitively from a
// single include entry. Nothing imports a job. If `jobs/` is not in `include`,
// the compiler never opens it — and the file still deploys.
var compiledEntryDirs = []string{"controllers", "jobs", "webhooks", "hooks"}

// includeBlindSpots returns the include patterns dir's tsconfig should carry and
// does not, for entry-point directories that EXIST.
//
// ‼️ AN ABSENT `include` IS NOT A BLIND SPOT. tsc then compiles the whole
// directory, which is strictly more than any list — refusing that would fail the
// one shape immune to this defect. It also settles `extends` without resolving
// it: a tsconfig inheriting its include has no `include` key of its own, so it is
// treated as "not narrowed here", which is exactly what it is.
func includeBlindSpots(dir string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, "tsconfig.json"))
	if err != nil {
		return nil // no tsconfig: the project runs no tsc we can reason about
	}
	var cfg struct {
		Include []string `json:"include"`
	}
	if json.Unmarshal(stripJSONComments(raw), &cfg) != nil || len(cfg.Include) == 0 {
		return nil
	}
	covered := func(sub string) bool {
		for _, pat := range cfg.Include {
			p := filepath.ToSlash(strings.TrimPrefix(pat, "./"))
			if p == sub || strings.HasPrefix(p, sub+"/") || strings.HasPrefix(p, "**/") {
				return true
			}
		}
		return false
	}
	var missing []string
	for _, sub := range compiledEntryDirs {
		st, err := os.Stat(filepath.Join(dir, sub))
		if err != nil || !st.IsDir() || covered(sub) {
			continue
		}
		missing = append(missing, sub+"/**/*.ts")
	}
	return missing
}

// reportIncludeBlindSpots prints the refusal. Returns true when there was one.
func reportIncludeBlindSpots(dir string, out io.Writer) bool {
	missing := includeBlindSpots(dir)
	if len(missing) == 0 {
		return false
	}
	fmt.Fprintf(out, "✗ tsconfig.json leaves %s the deploy ships out of its \"include\" list.\n", plural(len(missing), "directory", "directories"))
	fmt.Fprintln(out, "  These hold entry points — one default-exported class per file, found by walking the")
	fmt.Fprintln(out, "  directory. Nothing imports them, so `include` is the only thing that can put them in")
	fmt.Fprintln(out, "  front of the compiler. A file in one is type-checked by NOTHING until it is running.")
	fmt.Fprintln(out, "\n  Add to \"include\":")
	for _, m := range missing {
		fmt.Fprintf(out, "    %q,\n", m)
	}
	return true
}

// stripJSONComments removes // and /* */ comments so a tsconfig written as JSONC
// parses. Comment markers INSIDE a string are left alone — a tsconfig carries
// URLs, and eating "https://…" would turn a valid file into a parse failure and
// this gate into silence.
func stripJSONComments(b []byte) []byte {
	var out []byte
	inString, escaped, inLine, inBlock := false, false, false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				out = append(out, c)
			}
		case inBlock:
			if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				inBlock = false
				i++
			}
		case inString:
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			inLine = true
			i++
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			inBlock = true
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}

// plural renders "1 thing" / "2 things". A gate that says "1 directory(s)" reads
// like nobody ran it.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
