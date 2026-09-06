package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// The CLI's SURFACE is a contract (spec §7.3). This file golden-tests it:
// `palbase --help` and the two canonical groups' `--help` are compared against
// literal expected text, so a resurrected `branch`/`groups` command, a
// reintroduced `--branch`/`--group`/`--organization` flag, or a silently
// dropped verb fails HERE — in CI, at build time — instead of on a live run.

func helpOf(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append(args, "--help"))
	require.NoError(t, root.Execute())
	return out.String()
}

// topLevel returns the sorted names of every top-level command.
func topLevel(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, c := range newRootCmd().Commands() {
		if c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		names = append(names, c.Name())
	}
	sort.Strings(names)
	return names
}

// GOLDEN: the top-level command set.
//
// `branch` and `groups` are ABSENT. They were not renamed and not aliased —
// this is a direct cutover, and an alias would keep a dead resource alive in
// muscle memory and in scripts.
func TestGolden_TopLevelCommands(t *testing.T) {
	require.Equal(t, []string{
		// `admin` is ABSENT — and its absence is the POINT (FR-092).
		//
		// It carried three operator verbs: `fleet drift`, `fleet upgrade` and
		// `sweep`. Rolling the fleet was, for a long time, ONLY possible here:
		// the operator needed the right binary, the right credential and the
		// right image name — the three things least available during an
		// incident. That verb now lives in the operator console
		// (`/admin/fleet`), where it was proved live on 2026-09-01 (job
		// ed667120-6cc1-428b-9f37-097ae74abde4 → done), with the canary
		// REQUIRED and the plane's own refusal rendered verbatim.
		//
		// `drift` is the console's "Tenant image drift" section. `sweep` was
		// retired outright by the project owner's decision — the plane still
		// serves POST /v1/cloud/sweep, but nothing in this CLI calls it.
		//
		// No alias, no hidden command: an alias would keep a removed surface
		// alive in muscle memory and in scripts.
		"apikey", "auth", "build",
		"clone",
		"db",
		"debug",
		"deploys",
		"doctor",
		// egress: the outbound allowlist was the one module setting with no
		// command — it had to be hand-written into a file, and a host the
		// deploy's fail-closed validator rejects only surfaced as a failed
		// deploy. It is a management endpoint now, like every other setting.
		"egress", "flags", "init",
		// The direct half of the CLI: `link <url>` binds a checkout to a stack
		// somebody runs, with no project to select and no control plane to ask.
		"link",
		"login",
		"logout",
		"logs",
		"members",
		"notifications",
		"open",
		"plan",
		"project",
		"pull",
		"push",
		"roles",
		"rollback",
		"run",
		"secret",
		"spec",
		"start",
		"status",
		"stop",
		"storage",
		//  runs BOTH layers: the project's own npm test for services, and
		// the same suite again with PALBASE_TEST_* exported for the ones that
		// call the stack. It exists because the SDK told authors to test two
		// layers and shipped no way to run them together.
		"test",
		"test-user",
		// unlink: `link`in tersi ve bağın kendisi .palbase/project.json.
		// Öncesinde `web unlink` altında yaşıyordu ve SEÇİM dosyasını siliyordu —
		// yani bağı değil, yanındaki başka bir şeyi.
		"unlink",
		// f668ec6'de eklendi ve bu listeye yazılmamıştı — kapı o commit'ten beri
		// kırmızıydı. Kapının işi tam da bu: yeni bir komut BİLEREK buraya yazılır.
		"versions",
		"whoami",
	}, topLevel(t))
}

func TestGolden_RetiredCommandsAreGone(t *testing.T) {
	have := map[string]bool{}
	for _, name := range topLevel(t) {
		have[name] = true
	}
	// `init` was retired for a real reason and is BACK for a different one, so
	// the reason is worth keeping: it used to scaffold a SECOND skeleton onto
	// disk while the server had already deployed its own seed template as
	// version 1. The two drifted — init's said `palbase build`, the server's
	// said the retired `palbase serve` — and pushing init's tree overwrote a v1
	// the user never saw.
	//
	// What changed is where the skeleton comes from: it is copied out of the
	// installed @palbase/backend, so the scaffold and the SDK that compiles it
	// are the same version by construction and this CLI carries no copy to go
	// stale. The orchestrator still embeds its own backend_template/ for cloud
	// project creation; pointing that at the same package is the remaining half
	// and is in the deviation ledger.
	// `apps`, `env` ve `github` v2'de YOK: üçünün de arkasında bir yüzey
	// bulunmuyor. Bir rotaya vuran ama o rotanın var olmadığı bir fiil, olmayan
	// bir fiilden KÖTÜDÜR — geç, insanın terminalinde, o çalıştığına inandıktan
	// sonra düşer.
	//   env    → v2'de "environment" yok; bir branch KENDİ projesidir.
	//   github → depo güdümlü deploy'un v2 karşılığı yok.
	//   apps   → App Attest / uygulama kaydı yüzeyi v2'de hiç doğmadı.
	for _, gone := range retiredCommands {
		require.False(t, have[gone],
			"`palbase %s` must NOT exist after the cutover (no shims, no aliases)", gone)
	}
}

// retiredCommands is the ONE list. TestGolden_RetiredCommandsAreGone proves none
// of them is reachable; TestHelpProse_NamesNoRetiredCommand proves none of them
// is NAMED. Two lists would drift, and the drift is the defect: `env` left the
// binary on the cutover and stayed in four shipped strings for months, because
// the prose gate carried its own three-name copy.
var retiredCommands = []string{
	"branch", "groups", "group", "org", "organization", "serve", "dev", "apps", "env", "github",
	"endpoints",       // doctor reports all configured cloud addresses.
	"archive", "wake", // the cloud manages idle projects automatically.
	"upgrade", // the cloud reconciles the runtime with the project's SDK.
}

// GOLDEN: `palbase project --help` — the canonical Project surface.
//
// Four verbs, because in the v2 cloud a project IS a tenant: one microVM, one
// ref, one address. What left, and why:
//
//   - `use` — the linked target is what a directory acts on, and `palbase link`
//     already writes it. Two mechanisms for "which project is this directory"
//     is how somebody pushes to the wrong one.
//   - `connect-repo` / `disconnect-repo` — repository-driven deploys are a v1
//     platform feature with no v2 surface behind them. A verb that reaches a
//     route the cloud does not serve is worse than an absent verb: it fails
//     late, in the person's terminal, after they believed it worked.
func TestGolden_ProjectSurface(t *testing.T) {
	require.Equal(t,
		[]string{"create", "delete", "list", "status"},
		subcommands(t, "project"))
}

// subcommands returns the sorted child names of a top-level command.
func subcommands(t *testing.T, parent string) []string {
	t.Helper()
	for _, c := range newRootCmd().Commands() {
		if c.Name() != parent {
			continue
		}
		var names []string
		for _, sub := range c.Commands() {
			names = append(names, sub.Name())
		}
		sort.Strings(names)
		return names
	}
	t.Fatalf("no `%s` command", parent)
	return nil
}

// The GLOBAL context flags are exactly --project and --environment (plus the
// pre-existing --mode). Organization is intentionally NOT a CLI context, and the
// Palbase branch is gone, so neither --organization nor --branch may exist.
func TestGolden_GlobalFlags(t *testing.T) {
	var names []string
	newRootCmd().PersistentFlags().VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	sort.Strings(names)
	// THERE ARE NO GLOBAL FLAGS LEFT, and that is the deliverable.
	//
	// `--mode` went first: it selected between two clouds, only one of which was
	// ever deployed, and it was the DEFAULT — a fresh install pointed every
	// command at a host that does not exist.
	//
	// `--project` / `--environment` went in T010 for a sharper reason: they
	// resolved through `GET /api/v2/projects`, which the v2 cloud does not
	// serve. They parsed, they were documented, and they selected nothing.
	//
	// A flag offering a choice the product does not have is a flag somebody will
	// use.
	require.Empty(t, names)
}

// No command anywhere in the tree may take --branch, --group or --organization.
// A single leftover would keep a retired concept reachable from a script.
func TestNoRetiredFlagAnywhereInTheTree(t *testing.T) {
	retired := []string{"branch", "group", "organization", "org", "mode"}
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, dead := range retired {
			if c.Flags().Lookup(dead) != nil {
				t.Fatalf("`palbase %s` still takes --%s", strings.TrimSpace(path), dead)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(newRootCmd(), "")
}

// The help text must not advertise a retired command in its prose either — a
// user copying `palbase branch create` out of a help string is a bug report.
//
// IT WALKS THE WHOLE TREE, AND IT WALKS THE SOURCE. The version this replaces
// checked three literal names against the ROOT help only, and that is exactly
// how `palbase env` survived the cutover in four shipped strings: none of them
// is in the root help, and two of them are not in any help at all — they are
// refusals, printed at the moment somebody is already stuck. A gate that reads
// only the front page of the manual measures the front page.
func TestHelpProse_NamesNoRetiredCommand(t *testing.T) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, dead := range retiredCommands {
			named := "palbase " + dead
			for label, prose := range map[string]string{
				"Short": c.Short, "Long": c.Long, "Example": c.Example,
			} {
				require.False(t, namesCommand(prose, dead),
					"`palbase%s` --help (%s) tells the reader to run `%s`, which this binary rejects",
					path, label, named)
			}
			c.Flags().VisitAll(func(f *pflag.Flag) {
				require.False(t, namesCommand(f.Usage, dead),
					"`palbase%s --%s` usage names `%s`, which this binary rejects", path, f.Name, named)
			})
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(newRootCmd(), "")

	help := helpOf(t)
	require.Contains(t, help, "environment")
	require.Contains(t, help, "project")
}

// namesCommand reports whether prose tells the reader to run `palbase <name>`.
//
// Bounded on the right so `palbase deploys` is not read as the retired
// `palbase dev`, and so `--environment` is not read as `palbase env`: a gate
// that cries wolf on a live verb gets a `//nolint` and then it measures nothing.
func namesCommand(prose, name string) bool {
	needle := "palbase " + name
	for i := 0; ; {
		at := strings.Index(prose[i:], needle)
		if at < 0 {
			return false
		}
		end := i + at + len(needle)
		if end == len(prose) || !isWordByte(prose[end]) {
			return true
		}
		i = end
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// A retired command must not be named in ANY shipped string either.
//
// The four `palbase env` survivors were not help text: `banner.go` offered it as
// the way out of a refusal, `stack_spec.go` printed it after a partial refresh,
// `deploy.go` and `selection/resolve.go` each returned it inside an error. Every
// one of them reaches a person at the exact moment they are looking for a
// command to type, and every one of them named a command that does not exist —
// so the walk above cannot be the whole gate.
//
// STRING LITERALS ONLY. This package deliberately carries commentary that
// narrates what was retired and why (`env` is discussed by name a dozen times,
// in two languages), and a gate that read comments would be deleted within a
// week for being unusable.
func TestNoShippedStringNamesARetiredCommand(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	var offences []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0) // 0 = drop comments
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				text = lit.Value // a raw literal with an odd escape: scan it as written
			}
			for _, dead := range retiredCommands {
				if namesCommand(text, dead) {
					rel, _ := filepath.Rel(root, path)
					offences = append(offences, fmt.Sprintf(
						"%s:%d names `palbase %s`: %q",
						rel, fset.Position(lit.Pos()).Line, dead, text))
				}
			}
			return true
		})
		return nil
	}))
	require.Empty(t, offences,
		"shipped strings name a command this binary rejects:\n  %s",
		strings.Join(offences, "\n  "))
}

// FR-037: the surface is the SAME on a self-hosted stack.
//
// Every module setting is reachable by command, and every one of those commands
// calls a management endpoint on whatever stack the checkout is linked to —
// cloud or somebody's own machine. There is no command that exists only for one
// of them and no flag that turns one off.
//
// This is the assertion that stops the drift the cutover invites. A setting that
// loses its command does not announce itself: the file it used to live in is
// gone, so the only thing that would notice is a person looking for a verb that
// is not there any more.
func TestGolden_EverySettingIsReachableWithoutAFile(t *testing.T) {
	have := map[string]bool{}
	for _, name := range topLevel(t) {
		have[name] = true
	}
	// One command per module whose settings used to live in config/*.ts, plus
	// the two that never did.
	for _, verb := range []string{
		"auth",          // config/auth.json
		"egress",        // config/egress.ts
		"flags",         // config/flags.ts
		"storage",       // config/storage.ts
		"notifications", // config/notifications.ts
		"test-user",     // config/test-users.ts
		"secret",        // never a file, and the reason the rest followed
		"apikey",
	} {
		if !have[verb] {
			t.Errorf("`palbase %s` is gone — that setting is now reachable only from the panel", verb)
		}
	}
}

// retiredConfigFiles are the author-facing declaration files @palbase/backend
// 23.0.0 removed. `config/` is retired ENTIRELY, so this list is closed: nothing
// joins it, which is why naming the seven here carries no drift risk.
//
// Each one had a door and now has a different one; the message a person reads
// has to name the door, so the verb rides along with the file.
var retiredConfigFiles = []struct{ file, verb string }{
	{"config/egress.ts", "palbase egress add <host>"},
	{"config/flags.ts", "palbase flags add"},
	{"config/storage.ts", "palbase storage add"},
	{"config/notifications.ts", "palbase notifications add"},
	{"config/test-users.ts", "palbase test-user templates set --file <path>"},
	{"config/secrets.ts", "palbase secret set NAME --stdin"},
	{"config/auth.json", "palbase auth settings set"},
}

// retirementRegistry IS the declaration that these files are retired: it pairs
// each one with the verb that replaced it, and `palbase build` refuses a checkout
// still carrying one. Naming them there is the opposite of the defect below.
//
// THE SKIP IS PAIRED, so it cannot widen. The test asserts the registry still
// names ALL seven — the moment it stops being the registry, the exemption stops
// being true and this test says so rather than covering for it.
const retirementRegistry = "internal/backend/checkout_shape.go"

// A shipped string that names a retired config file must SAY it is retired.
//
// ‼️ THE RULE IS POSITIVE ON PURPOSE, AND THE FIRST DRAFT TAUGHT WHY. A gate that
// simply refused the file NAME flagged nine strings, and six of them were right:
// `palbase egress --help` says "It used to be config/egress.ts, applied on every
// push, which meant the panel could not change it" — narration a reader needs, and
// exactly what makes the new door make sense. Hunting a lie by its subject catches
// the truth about the same subject. So the demand is that the truth be WRITTEN: name
// the retired file and the string must also carry a retirement marker.
//
// What that separates, measured on this tree: `palbase test-user --help` said
// "Templates ARE declared in config/test-users.ts" while `templatesSetCmd` sat
// ninety lines below it, and `palbase flags --help` said "config/flags.ts IS
// git-authoritative: commit it and git push to deploy" while its own `remove`
// subcommand said "It is gone: there is no file left to fall back to". Both are
// present tense about a file the SDK deleted in 23.0.0; both carry no marker.
//
// CONCATENATION IS FOLDED FIRST. `flags.go` builds its Long with `+` across four
// literals, so the file name and the marker can land in different ones — judging
// literals separately would fail a string that tells the truth in two pieces.
//
// STRING LITERALS ONLY, as above: this tree narrates the cutover in its comments on
// purpose, and a gate that read comments would be deleted within a week.
func TestNoShippedStringNamesARetiredConfigFile(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	// A string may name a retired file only while saying it is retired. Lower-cased
	// before matching, so prose casing is not part of the contract.
	markers := []string{"used to", "no longer", "is gone", "are gone", "removed", "retired"}
	saysRetired := func(text string) bool {
		low := strings.ToLower(text)
		for _, m := range markers {
			if strings.Contains(low, m) {
				return true
			}
		}
		return false
	}

	// flatten returns the text of a `+` chain of string literals, and false if any
	// operand is not one (a runtime value cannot be judged here).
	var flatten func(ast.Expr) (string, bool)
	flatten = func(e ast.Expr) (string, bool) {
		switch v := e.(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return "", false
			}
			text, err := strconv.Unquote(v.Value)
			if err != nil {
				text = v.Value
			}
			return text, true
		case *ast.BinaryExpr:
			if v.Op != token.ADD {
				return "", false
			}
			l, okL := flatten(v.X)
			r, okR := flatten(v.Y)
			if !okL || !okR {
				return "", false
			}
			return l + r, true
		}
		return "", false
	}

	var offences []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == retirementRegistry {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0) // 0 = drop comments
		if err != nil {
			return err
		}
		judge := func(text string, pos token.Pos) {
			if saysRetired(text) {
				return
			}
			for _, dead := range retiredConfigFiles {
				if strings.Contains(text, dead.file) {
					offences = append(offences, fmt.Sprintf(
						"%s:%d names %s in the present tense — it was retired in @palbase/backend 23.0.0 "+
							"and the door is `%s`. Say so in the string, or stop naming the file.",
						rel, fset.Position(pos).Line, dead.file, dead.verb))
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BinaryExpr:
				if text, ok := flatten(v); ok {
					judge(text, v.Pos())
					return false // its literals are judged as one string
				}
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					text, err := strconv.Unquote(v.Value)
					if err != nil {
						text = v.Value
					}
					judge(text, v.Pos())
				}
			}
			return true
		})
		return nil
	}))
	require.Empty(t, offences,
		"shipped strings name a deleted file as if it still worked:\n  %s",
		strings.Join(offences, "\n  "))

	// The exemption, earned.
	registry, err := os.ReadFile(filepath.Join(root, retirementRegistry))
	require.NoError(t, err, "the registry is exempt from the scan above; it has to exist")
	for _, dead := range retiredConfigFiles {
		require.Containsf(t, string(registry), dead.file,
			"%s no longer names %s. It is exempt BECAUSE it is the registry, so a row it drops "+
				"is a retired file nothing declares retired and nothing refuses.", retirementRegistry, dead.file)
	}
}

// TestNoUserFacingStringIsTurkish holds sdk/cli/CLAUDE.md's rule: the task may
// be Turkish, the CLI's OUTPUT may not.
//
// It parses rather than greps, and the difference is not cosmetic — it is the
// reason this gate exists at all. A line-based regex was tried first and missed
// three of the six violations, because each carried its string on the line
// AFTER `fmt.Errorf(`. go/parser also excludes comments structurally, so the
// Turkish reasoning this codebase writes in its comments cannot trip it.
func TestNoUserFacingStringIsTurkish(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")

	const turkish = "çğıöşüÇĞİÖŞÜ"

	// DIACRITICS ARE ONLY HALF THE ALPHABET, and the half that is easy to see.
	//
	// This gate is named "no user-facing string is Turkish", but for its first
	// version it only ever asked "does this string contain ç, ğ, ı, ö, ş or ü".
	// Turkish typed without them — "gecersiz deger", "dosya bulunamadi" — sailed
	// straight through, and the gate would have reported silence while the thing
	// it exists to prevent sat in the binary. A gate blind to half its subject is
	// the same failure this whole run is about: it measures something adjacent to
	// its promise and passes.
	//
	// So the ASCII fold is checked too, with WORD BOUNDARIES: "deger" is inside
	// "ledger" and "icin" is inside "medicine", and a substring search would fail
	// this gate on English prose. These are the words that actually appear in
	// Turkish error text and appear nowhere in English as whole words.
	turkishASCII := regexp.MustCompile(`(?i)\b(gecersiz|bulunamadi|basarisiz|` +
		`olusturulamadi|yazilamadi|okunamadi|kullanici|sunucu|dosya|deger|degeri|` +
		`icin|olmali|gerekli|eksik|bilinmeyen|baglanti|yetkisiz|calismiyor|` +
		`yeniden|guncel|hata|yok|degil)\b`)

	fset := token.NewFileSet()
	var offenders []string
	var unreadable []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, perr := parser.ParseFile(fset, path, nil, 0) // no comments
		if perr != nil {
			// A FILE THIS GATE CANNOT READ IS NOT A FILE WITHOUT VIOLATIONS.
			// Returning nil quietly shrank the gate's field of view to "every
			// file that happens to parse", which is not what its name promises.
			// In practice the compiler fails first — but a gate that reports
			// silence for what it could not open is the shape this run exists
			// to remove.
			unreadable = append(unreadable, fmt.Sprintf("%s: %v", path, perr))
			return nil
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, isLit := n.(*ast.BasicLit)
			// STRING **and** CHAR: `'ş'` is a Turkish letter in the binary just
			// as much as "şey" is, and the gate looked at only one of the two
			// literal kinds that can carry one.
			if !isLit || (lit.Kind != token.STRING && lit.Kind != token.CHAR) {
				return true
			}
			why := ""
			switch {
			case strings.ContainsAny(lit.Value, turkish):
				why = "Turkish letters"
			case turkishASCII.MatchString(lit.Value):
				why = "Turkish word, ASCII-folded: " + turkishASCII.FindString(lit.Value)
			}
			if why != "" {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, fmt.Sprintf("%s:%d (%s) %s",
					rel, fset.Position(lit.Pos()).Line, why, lit.Value))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(unreadable) > 0 {
		t.Fatalf("this gate could not read %d file(s), so it cannot speak for them:\n%s",
			len(unreadable), strings.Join(unreadable, "\n"))
	}
	if len(offenders) > 0 {
		t.Fatalf("user-facing strings must be English (sdk/cli/CLAUDE.md):\n%s",
			strings.Join(offenders, "\n"))
	}
}

// buildPalbase compiles the real binary, because a retired command has to be
// absent from what SHIPS — not from a cobra tree assembled in a test.
func buildPalbase(t *testing.T) string {
	t.Helper()
	requireRealToolchainCmd(t)
	bin := filepath.Join(t.TempDir(), "palbase")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build palbase: %v\n%s", err, out)
	}
	return bin
}

// requireRealToolchainCmd keeps a compile out of the -short budget, the way its
// namesake does in internal/backend.
func requireRealToolchainCmd(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("compiles the binary — outside the -short budget")
	}
}

// A RETIRED COMMAND IS A COMMAND THAT IS NOT THERE.
//
// `palbase ios link`, `web link`, `android link`, `macos link` and
// `<platform> use` were retired in favour of one `palbase link` that reads what
// the checkout IS. The temptation is a friendly stub — "this moved, use X" —
// and that stub is the retired surface still shipping, one indirection along.
// Cobra already says the true thing and exits non-zero.
//
// This test measures the ABSENCE, because absence is the deliverable.
func TestRetiredPlatformCommandsAreGone(t *testing.T) {
	bin := buildPalbase(t)
	for _, argv := range [][]string{
		{"ios", "link"}, {"macos", "link"}, {"android", "link"}, {"web", "link"},
		{"ios", "use", "staging"}, {"android", "use", "staging"}, {"web", "use", "staging"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			out, err := exec.Command(bin, argv...).CombinedOutput()
			if err == nil {
				t.Fatalf("`palbase %s` still runs:\n%s", strings.Join(argv, " "), out)
			}
			// A NON-ZERO EXIT IS NOT ENOUGH, and the first version of this test
			// proved it: every one of these already exited non-zero for its own
			// reasons (missing credentials, missing argument) while the command
			// was still registered — so the test passed against exactly the
			// surface it was written to remove.
			//
			// Cobra says the true thing when a command does not exist. Demand
			// THAT sentence.
			lower := strings.ToLower(string(out))
			if !strings.Contains(lower, "unknown command") {
				t.Fatalf("`palbase %s` failed for some OTHER reason, so this proves nothing "+
					"about the command being gone:\n%s", strings.Join(argv, " "), out)
			}
			// AND IT MUST NOT BE A SIGNPOST. A "moved, use X instead" branch is
			// the old surface wearing a new coat.
			for _, banned := range []string{"instead", "moved", "deprecated", "renamed", "kaldırıldı"} {
				if strings.Contains(lower, banned) {
					t.Errorf("the refusal reads like a redirect (%q):\n%s", banned, out)
				}
			}
		})
	}
}

// THE SECOND ADDRESSING MECHANISM IS GONE (T010).
//
// `--project` and `--environment` were global overrides that resolved through
// `GET /api/v2/projects` — a route the v2 cloud does not serve (measured
// 2026-08-25: "No route matches this method and path"). So the flags were
// documented, accepted, and quietly selected nothing in 15+ commands.
//
// What a checkout talks to is `.palbase/project.json`, written by `link`.
func TestTheSelectionFlagsAreGone(t *testing.T) {
	bin := buildPalbase(t)
	out, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("palbase --help failed: %v\n%s", err, out)
	}
	for _, flag := range []string{"--project", "--environment"} {
		if strings.Contains(string(out), flag) {
			t.Errorf("%s still appears in the global help:\n%s", flag, out)
		}
	}

	// EVERY COMMAND'S OWN HELP, NOT JUST THE ROOT'S.
	//
	// This gate read `palbase --help` and spoke about the whole CLI. The set it
	// READ and the set it TALKED ABOUT were different questions, and four
	// subcommands went on describing the flags in their own `Long` text:
	//
	//   The global --project / --environment flags select a CLOUD environment.
	//   In a checkout linked to a project they do not apply, and saying so is
	//   the point: a flag that is accepted and ignored is worse than one that
	//   is refused.
	//
	// The irony is exact — the paragraph argues that an accepted-and-ignored
	// flag is worse than a refused one, while describing a flag the binary no
	// longer parses at all. `palbase push --help`, `spec`, `test-user` and
	// `flags user` all carried it, and the root help was clean the whole time.
	//
	// The tree is walked in-process: a person reads `<command> --help`, and that
	// text is the command's own, not the root's.
	var stale []string
	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		name := strings.TrimSpace(path + " " + cmd.Name())
		for _, text := range []struct{ where, body string }{
			{"Short", cmd.Short}, {"Long", cmd.Long}, {"Example", cmd.Example},
		} {
			for _, gone := range []string{"--project", "--environment", "repository_provider"} {
				if strings.Contains(text.body, gone) {
					stale = append(stale, name+" "+text.where+" names "+gone)
				}
			}
		}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "project" || f.Name == "environment" {
				stale = append(stale, name+" declares --"+f.Name)
			}
		})
		for _, sub := range cmd.Commands() {
			walk(sub, name)
		}
	}
	walk(newRootCmd(), "")
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("the second addressing mechanism is still described by the commands themselves:\n%s\n\n"+
			"a reader who types `<command> --help` is told about a flag the binary answers with "+
			"`unknown flag`", strings.Join(stale, "\n"))
	}

	// AND THE ROUTE THEY LEANED ON IS NOT CALLED ANY MORE. A flag can be removed
	// while the call it justified stays behind, wired to nothing.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	grep := exec.Command("grep", "-rn", `"/api/v2/projects"`, "--include=*.go", root)
	found, _ := grep.Output()
	var live []string
	for _, line := range strings.Split(strings.TrimSpace(string(found)), "\n") {
		if line == "" || strings.Contains(line, "_test.go") || strings.Contains(line, "selectiontest") {
			continue
		}
		// A MENTION IS NOT A CALL. transport/rest.go names the route in a doc
		// comment as an example of what a path looks like; a gate that cannot
		// tell prose from code reports a defect that is not there.
		if _, code, ok := strings.Cut(line, ":"); ok {
			if _, body, ok := strings.Cut(code, ":"); ok && strings.HasPrefix(strings.TrimSpace(body), "//") {
				continue
			}
		}
		live = append(live, line)
	}
	if len(live) > 0 {
		t.Errorf("the dead route is still called from production code:\n%s", strings.Join(live, "\n"))
	}
}

// THE "REFUSES THE ENVIRONMENT FLAG" TESTS WENT WITH THE FLAG (T010).
//
// They measured that a linked checkout refused `--environment` rather than
// letting two authorities disagree about the target. Sound rule, and the flag it
// guarded is gone — a checkout has one address and it is in .palbase/project.json.
// A guard against a flag nobody can pass measures nothing.

// FR-013: NOTHING IN PRODUCTION TOUCHES `.palbase/selection.json`.
//
// The requirement says the flags AND the file "SHALL NOT exist", and its edge
// case is explicit: an old checkout carrying one is simply "no longer read".
// The flags went; the file did not. It kept ONE reader for months — and not the
// one anybody would grep for. `pull_spec.go` called `selection.Load` directly,
// which is easy to find; the resolver reached it INDIRECTLY, through
// `Resolver.Resolve → r.Config() → Load(r.Dir)`, and five commands called that.
//
// So a grep for `selection.Load(` reported the file was almost gone while the
// mechanism was still wired into `push`, `pull`, `logs`, `flags user` and
// `open`. This gate does not grep for one call: it asserts that no production
// file imports the file API at all.
//
// It also fixed a live defect on the way: `palbase flags user` asked the
// resolver for an environment ref it used ONLY to decorate a success message,
// and the lookup gated the whole command — so in a checkout linked by
// `palbase link` the command answered "this directory is not linked to a
// project". Measured before and after through the shipped binary.
func TestNothingInProductionReadsTheRetiredSelectionFile(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")

	// The file API — the reader, the writer, and the resolver that wraps them.
	retired := []string{
		"selection.Load(", "selection.Save(", "selection.ConfigPath(",
		"selection.ApplySelection(", "selection.Config{", "selection.Resolver",
	}
	var offences []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // prose about the retirement is the point, not a call
			}
			for _, name := range retired {
				if strings.Contains(line, name) {
					offences = append(offences, rel+":"+strconv.Itoa(i+1)+" uses "+name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("production still reaches the retired selection file (FR-013):\n%s\n\n"+
			"the target comes from .palbase/project.json, and .palbase/local.json while a "+
			"stack runs here — there is no second mechanism", strings.Join(offences, "\n"))
	}
}
