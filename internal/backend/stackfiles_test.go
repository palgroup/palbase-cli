package backend

import (
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
	for end-1 > origin && (strings.HasPrefix(lines[end-1], "  #") || lines[end-1] == "") {
		end--
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

// The images must be NAMED as registry references by default. A local tag as
// the default is exactly what made `palbase start` require this repository:
// docker cannot fetch `palbase-palsvc` from anywhere, so the command only worked
// on a machine where somebody had already built it.
//
// It reads the compose file and nothing else, so it says the default POINTS at a
// registry — not that the registry will serve it. Worth stating because the two
// came apart on 2026-08-18: the edge package was created private by ghcr, this
// test was green, and an anonymous `docker manifest inspect` answered
// `unauthorized`. Whether a stranger can actually pull is a property of the
// registry, and the only honest place to assert it is against the registry.
func TestTheVendoredStackPullsItsImages(t *testing.T) {
	for _, want := range []string{
		"ghcr.io/palgroup/palbase/palsvc:",
		"ghcr.io/palgroup/palbase/runtime-dev:",
		// The edge is a third image now, and it carries the route table — a
		// stack that cannot pull it has no door.
		"ghcr.io/palgroup/palbase/edge:",
	} {
		if !strings.Contains(string(stackCompose), want) {
			t.Errorf("no default image at %s — the default must name a registry, or `palbase start` needs this repository", want)
		}
	}
}

func TestARegistryReferenceIsRecognised(t *testing.T) {
	for _, c := range []struct {
		image string
		want  bool
	}{
		{"ghcr.io/palgroup/palbase/palsvc:0.29.1", true},
		{"localhost:5000/palsvc", true},
		// Docker Hub's short form: docker does pull it, but the first segment is
		// an ORG, not a host — and nothing in this stack defaults to one, so
		// treating it as local (and letting the inspect refuse) is the safe way
		// round.
		{"pgvector/pgvector:pg16", false},
		{"palbase-palsvc", false},
		{"palbase-runtime-dev:latest", false},
	} {
		if got := isRegistryImage(c.image); got != c.want {
			t.Errorf("isRegistryImage(%q) = %v, want %v", c.image, got, c.want)
		}
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

// GO SABITLERI ILE COMPOSE VARSAYILANLARI AYNI ETIKETI SOYLEMELI.
//
// Etiket IKI yerde yasiyor: stackImages'in fallback'i (bu paket, varlik kontrolu
// ve `palbase upgrade` icin) ve compose belgesinin ${VAR:-default}'u (gercekte
// KOSAN sey, cunku bu komut o degiskenleri export etmiyor).
//
// Olculdu 2026-08-29: yalniz Go sabiti 0.39.0'a tasindi, compose 0.36.1'de kaldi,
// ve `palbase start` eski imaji kosmaya devam etti. Sonuc, storage semasi 4'e
// gocmus bir veritabaninda:
//
//	migrate module "storage": no migration found for version 4
//
// Bir sey hakkinda anlasmak zorunda olan iki yer, bir gun anlasmayi birakacak iki
// yerdir. Bu, onu soyleyen test.
func TestTheGoConstantsAndTheComposeDefaultsAgree(t *testing.T) {
	doc := string(stackCompose)
	for _, want := range stackImages {
		// compose satiri: image: ${VAR:-<etiket>}
		marker := "${" + want.env + ":-"
		i := strings.Index(doc, marker)
		if i < 0 {
			t.Errorf("compose %s degiskenini hic okumuyor", want.env)
			continue
		}
		rest := doc[i+len(marker):]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			t.Errorf("compose %s satiri kapanmamis", want.env)
			continue
		}
		got := rest[:j]
		if got != want.fallback {
			t.Errorf("%s: Go sabiti %q, compose varsayilani %q — `palbase start` compose'unkini kosar",
				want.env, want.fallback, got)
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
		t.Skip("docker is not on PATH — the compose document was NOT validated")
	}
	cmd := exec.Command("docker", "compose", "-f", path, "config", "--services")
	// The document interpolates these; without them compose fails on the
	// substitution rather than on the structure we are measuring.
	cmd.Env = append(os.Environ(), "PALBASE_HTTP_PORT=1", "PALBASE_PROJECT_DIR=/tmp")
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
