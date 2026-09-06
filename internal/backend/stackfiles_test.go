package backend

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The compose document the binary carries is a COPY of v2/deploy's. Copies
// drift, and this one drifting means `palbase start` brings up a stack that
// stopped resembling the one the repository tests. Held against the original
// whenever the palbase repository is beside this checkout — every development
// machine, and no CI runner, which is why an absence skips rather than fails.
//
// ONE SERVICE IS DELIBERATELY NOT VENDORED, and the exclusion is NAMED here
// rather than left as a silent difference. `barman` (continuous WAL streaming
// and scheduled base backups) is declared with `build:` and mounts
// `./barman/initdb/...` — a local build context that exists in the palbase
// repository and NOWHERE on the machine of somebody who installed the CLI from
// brew. Vendoring it verbatim would make `palbase start` fail for exactly the
// audience this file was created for.
//
// So the vendored copy is the four-container PRODUCT stack, and the fifth
// service is a cloud operations concern. The exclusion is verified, not
// assumed: `barmanBlock` must still be FOUND in the repository copy, so the day
// barman is removed — or vendored properly — this gate goes red and the
// exception has to be revisited instead of quietly outliving its reason.
func TestTheVendoredComposeMatchesTheRepository(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	// internal/backend → sdk/cli → sdk → palbase → v2/deploy
	original := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "v2", "deploy", composeFile)
	want, err := os.ReadFile(original)
	if err != nil {
		t.Skipf("the palbase repository is not beside this checkout: %v", err)
	}

	trimmed, found := withoutBarman(string(want))
	if !found {
		t.Fatalf("no `barman:` service in %s — the exclusion below has outlived its reason; "+
			"vendor the file as-is and delete this carve-out", original)
	}
	if trimmed != string(stackCompose) {
		t.Errorf("the vendored %s differs from %s (barman aside) — re-vendor it, or the CLI starts "+
			"a stack the repository does not test", composeFile, original)
	}
}

// withoutBarman removes the barman service and the lines that exist only to
// serve it, returning whether the service was there at all.
//
// The block ends at the NEXT key at the same indent — barman sits BETWEEN two
// services, so cutting to `networks:` swallowed everything after it. Measured:
// the first attempt produced a vendored file with two services missing and the
// image-default gates caught it.
func withoutBarman(doc string) (string, bool) {
	lines := strings.Split(doc, "\n")
	origin := -1
	for i, l := range lines {
		if l == "  barman:" {
			origin = i
			break
		}
	}
	if origin < 0 {
		return doc, false
	}
	start := origin
	// Walk back over the comment block that introduces it.
	for start > 0 && (strings.HasPrefix(lines[start-1], "  #") || lines[start-1] == "") {
		start--
	}
	end := len(lines)
	// SEARCH FROM THE SERVICE LINE, not from the comment we just walked back
	// over. topLevelKey skips comments, so starting at start+1 walked forward
	// through the comment block and matched `  barman:` ITSELF as the next
	// top-level key — end landed ON the service, `lines[end:]` put it back, and
	// the cut removed only the comment. The vendored file kept a service whose
	// volume this function had already deleted, and `docker compose config`
	// refused it in v0.52.0 and again in v0.53.0.
	for i := origin + 1; i < len(lines); i++ {
		if topLevelKey(lines[i]) {
			end = i
			break
		}
	}
	// GIVE THE NEXT SERVICE ITS HEADER BACK. The walk stops at the next
	// top-level key, but the comment block introducing THAT service sits above
	// it — so cutting to `end` also removed `# ── palsvc ──`, measured: the
	// header is in the repository copy and was missing from the vendored one,
	// and the parity gate could not see it because both sides pass through
	// here. This is the symmetry of the walk-back above.
	//
	// ONLY WHEN THERE IS A NEXT SERVICE. If barman were the LAST block in the
	// file, `end` is len(lines) and the trailing "" that a final newline
	// produces looks exactly like the blank line above a header — the walk would
	// eat it and the vendored file would lose its final newline. Nothing hits
	// this today (barman sits between two services), which is precisely why it
	// is worth pinning: the day somebody moves it, the failure would be a
	// one-byte diff nobody reads.
	if end < len(lines) {
		for end-1 > origin && (strings.HasPrefix(lines[end-1], "  #") || lines[end-1] == "") {
			end--
		}
	}
	kept := append(append([]string{}, lines[:start]...), lines[end:]...)
	out := strings.Join(kept, "\n")
	for _, line := range []string{
		"#   barman     continuous WAL streaming + scheduled base backups (PITR)\n",
		"      # Yeni bir dev stack'te de replication erişimi hazır gelsin (FR-004) — yoksa\n",
		"      # barman bağlanamaz ve yedekleme dev'de sessizce çalışmaz.\n",
		"      - ./barman/initdb/10-replication.sh:/docker-entrypoint-initdb.d/10-replication.sh:ro\n",
		"  barmandata:\n",
	} {
		out = strings.Replace(out, line, "", 1)
	}
	out = strings.ReplaceAll(out, "five containers", "four containers")
	out = strings.ReplaceAll(out, "five-container stack", "four-container stack")
	return out, true
}

// topLevelKey reports whether the line opens a sibling of `barman:` — a service
// at two-space indent, or a document-level key like `networks:`.
func topLevelKey(l string) bool {
	if l == "" || strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "  #") {
		return false
	}
	trimmed := strings.TrimPrefix(l, "  ")
	return strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, " ")
}

// The images must be NAMED as registry references. A local tag is exactly what
// made `palbase start` require this repository: docker cannot fetch
// `palbase-palsvc` from anywhere, so the command only worked on a machine where
// somebody had already built it.
//
// THE SUBJECT MOVED, THE RULE DID NOT (2026-09-05). The reference used to live
// in the compose file as a `${VAR:-<ref>}` default, and this test read it there.
// Defaults are gone — a default is a second source of the version, and the one
// that goes stale — so the reference now lives in exactly one place, the Go
// table, and is completed with the installed SDK's version. That is where it is
// measured.
//
// It reads our own declaration and nothing else, so it says the reference POINTS
// at a registry — not that the registry will serve it. Worth stating because the
// two came apart on 2026-08-18: the edge package was created private by ghcr,
// this test was green, and an anonymous `docker manifest inspect` answered
// `unauthorized`. Whether a stranger can actually pull is a property of the
// registry, and the only honest place to assert it is against the registry.
func TestTheVendoredStackPullsItsImages(t *testing.T) {
	for _, img := range stackImages {
		ref := img.ref("0.0.0")
		if !strings.Contains(ref, "/") {
			t.Errorf("%s resolves to %q, which docker cannot fetch from anywhere — "+
				"`palbase start` would need this repository", img.env, ref)
		}
	}
	// AND THE COMPOSE FILE MUST NOT ANSWER THE QUESTION ITSELF. A default there
	// would override the table silently the day the CLI forgets to export one.
	if strings.Contains(string(stackCompose), "_IMAGE:-") {
		t.Error("the vendored compose carries an image default — the version has one source, and this is not it")
	}
}

// `--lan` widens ONE port, and there is only one to widen.
//
// A local stack holds a real database password and a service_role key. It used
// to publish two ports — palsvc's and postgres' — and this test's job was to
// keep `--lan` away from the second. Now nothing but the edge is published at
// all, so the test asserts the stronger thing: exactly ONE publish line exists,
// it is the edge's, it reads the bind variable, and no line anywhere in the
// file puts the database on the host.
func TestOnlyTheEdgeIsPublished(t *testing.T) {
	var publishes []string
	for _, line := range strings.Split(string(stackCompose), "\n") {
		trimmed := strings.TrimSpace(line)
		// A PUBLISH line, not a mention: the header names both variables in
		// prose long before anything is published, and a search that stops at
		// the first match reads the documentation instead of the file.
		if strings.HasPrefix(trimmed, "- \"") && strings.Contains(trimmed, ":") &&
			strings.Contains(trimmed, "${PALBASE_") {
			publishes = append(publishes, trimmed)
		}
	}

	if len(publishes) != 1 {
		t.Fatalf("the stack publishes %d port(s), want exactly one (the edge):\n%s",
			len(publishes), strings.Join(publishes, "\n"))
	}
	edge := publishes[0]
	if !strings.Contains(edge, "${"+BindEnv) {
		t.Errorf("the published port does not read %s — `--lan` would change nothing: %s", BindEnv, edge)
	}
	if !strings.HasSuffix(edge, ":8080\"") {
		t.Errorf("the published port does not reach the edge's 8080: %s", edge)
	}
	if strings.Contains(string(stackCompose), ":5432\"") {
		t.Error("something publishes the database port — a stack's password would be on the host, and `--lan` would put it on the network")
	}
}

// COMPOSE'UN OKUDUĞU DEĞİŞKENLER İLE GO TABLOSUNUNKİLER AYNI OLMALI.
//
// Bu test eskiden iki ETİKETİ karşılaştırıyordu: `stackImages`'in fallback'i ve
// compose'un `${VAR:-<etiket>}` varsayılanı. Ölçüldü 2026-08-29: yalnız Go
// sabiti 0.39.0'a taşındı, compose 0.36.1'de kaldı, ve `palbase start` eski
// imajı koşmaya devam etti — storage şeması 4'e göçmüş bir veritabanında
// `migrate module "storage": no migration found for version 4`.
//
// 2026-09-05'te ikinci etiket TÜMÜYLE kaldırıldı: sürümün tek kaynağı kurulu
// `@palbase/backend`. Karşılaştırılacak iki sayı kalmadı — ama iki ADIN
// buluşması hâlâ gerekiyor: CLI `PBC_PALSVC_IMAGE` export ederken compose
// başka bir ad okuyorsa, `:?` sayesinde stack AÇILMAZ. Kapının ölçtüğü şey bu.
func TestTheComposeDemandsEveryImageVariable(t *testing.T) {
	doc := string(stackCompose)
	for _, want := range stackImages {
		// `:?` = compose değeri OLMADAN başlamayı reddeder. Bu, kuralın
		// kendisi: değeri veren tek yer CLI, o da kurulu SDK'dan alıyor.
		required := "${" + want.env + ":?"
		if !strings.Contains(doc, required) {
			t.Errorf("compose %s degiskenini ZORUNLU olarak okumuyor — CLI'in verdigi deger bir yere varmiyor", want.env)
		}
	}
}

// composeConfigIsValid hands the vendored document to the REAL tool.
//
// The equality test above compares two strings that BOTH pass through
// withoutBarman, so a defect in that function is invisible to it. Measured
// twice: the invalid file shipped in v0.52.0, and its own fix shipped in
// v0.53.0 with the file STILL invalid — the gate was green through both.
// Only docker can say whether docker accepts this document.
func composeConfigIsValid(t *testing.T, path string) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		requireToolOnCI(t, "docker", err)
		t.Skip("docker is not on PATH — the compose document was NOT validated")
	}
	cmd := exec.Command("docker", "compose", "-f", path, "config", "--services")
	// The document interpolates these; without them compose fails on the
	// substitution rather than on the structure we are measuring. The image
	// variables became `:?` — REQUIRED — on 2026-09-05, so they belong here too;
	// their values are irrelevant to the structure, and giving them a real
	// version here would put a second source of the version in a test file.
	env := append(os.Environ(), "PALBASE_HTTP_PORT=1", "PALBASE_PROJECT_DIR=/tmp")
	for _, img := range stackImages {
		env = append(env, img.env+"=placeholder")
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker rejects the vendored compose document:\n%s", strings.TrimSpace(string(out)))
	}
}

func TestTheVendoredComposeIsAValidComposeProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, composeFile)
	if err := os.WriteFile(path, stackCompose, 0o644); err != nil {
		t.Fatal(err)
	}
	composeConfigIsValid(t, path)
}

// EVERY IMAGE THE STACK RUNS MUST BE PINNED IN ONE PLACE — and the gate that
// checks the pins has to be able to SEE all of them.
//
// TestTheComposeDemandsEveryImageVariable loops over `stackImages`, so it
// measures exactly the images Go already knows about. An image compose names
// WITHOUT a variable is therefore invisible to it: not a disagreement it
// tolerates, one it cannot express. Measured 2026-09-05: `postgres` ran
// `pgvector/pgvector:pg16` as a bare literal, and `grep -rn pgvector` over the
// Go sources returned nothing at all.
//
// This test starts from the COMPOSE side instead, so a new service arrives
// pinned or arrives red.
func TestEveryComposeImageIsPinnedInOnePlace(t *testing.T) {
	known := map[string]bool{}
	for _, img := range stackImages {
		known[img.env] = true
	}

	var unpinned []string
	for i, line := range strings.Split(string(stackCompose), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "image:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
		if !strings.HasPrefix(value, "${") {
			unpinned = append(unpinned, fmt.Sprintf("%s:%d %s (no variable at all)", composeFile, i+1, value))
			continue
		}
		// `${VAR:?mesaj}` ve `${VAR:-varsayilan}` — ikisinde de ad, ilk
		// noktalama işaretinde biter. `:-` arayan hâli varsayılanlar
		// kaldırılınca (2026-09-05) DEĞİŞKEN ADINI mesajla birlikte okudu ve
		// dördünü de "Go bilmiyor" diye bildirdi.
		env := strings.TrimPrefix(value, "${")
		if i := strings.IndexAny(env, ":-}"); i >= 0 {
			env = env[:i]
		}
		if !known[env] {
			unpinned = append(unpinned, fmt.Sprintf("%s:%d reads %s, which Go does not carry", composeFile, i+1, env))
		}
	}

	if len(unpinned) > 0 {
		t.Errorf("these images are outside the one place that pins them, so no gate can measure "+
			"them:\n%s", strings.Join(unpinned, "\n"))
	}
}
