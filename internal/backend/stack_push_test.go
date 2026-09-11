package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// What a stack push must carry, and what it must never carry.
//
// A stack does not BUILD — it collects. So the archive has to include the build
// products, which the cloud tarball deliberately leaves out because the cloud
// builds them server-side. Getting this wrong is quiet: the push succeeds as far
// as the network, and the stack refuses with "no built code" about a project
// that built fine.
func entriesOf(t *testing.T, blob []byte) map[string]bool {
	t.Helper()
	gz, err := gzip.NewReader(strings.NewReader(string(blob)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.ToSlash(strings.TrimPrefix(h.Name, "./"))] = true
	}
	return out
}

// projectForPush, KAYNAĞI projeye ve ÜRÜNLERİ ayrı bir bundle köküne koyar.
//
// Derleme ürünleri artık müşterinin checkout'una yazılmıyor; tar onları geçici
// kökten alıyor. Fikstür de o ayrımı taşımak zorunda, yoksa "tar ürünleri
// buluyor mu" sorusu, ürünlerin ARTIK BULUNMADIĞI bir yerden sorulur ve test
// sessizce boş bir artefaktı onaylar.
func projectForPush(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bundleRoot := t.TempDir()
	writeTo := func(root, rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		// Ürünler bundle köküne, geri kalan her şey projeye.
		if strings.HasPrefix(rel, ".palbase/esm/") || strings.HasPrefix(rel, ".palbase/jobs/") || strings.HasPrefix(rel, ".palbase/hooks/") {
			writeTo(bundleRoot, rel, body)
			return
		}
		writeTo(dir, rel, body)
	}
	write("controllers/todo.controller.ts", "// source")
	write("db/public.ts", "export default {}")
	write("db/billing.ts", "export default {}")
	write(".palbase/esm/controllers/controllers.js", "export const controllers = [];")
	write(".palbase/esm/tests/todos.test.js", "// the suite a deploy grades this release by")
	// A leftover from before the cutover. People have one of these on disk right
	// now, and it must NOT ride along: settings are written directly, so a copy
	// in a push could only overwrite what somebody set from the panel.
	write(".palbase/config.json", `{"flags":{}}`)
	write(".palbase/target.json", `{"url":"https://127.0.0.1"}`)
	write(".palbase/ios/palbase-config.json", `{"api_key":"pb_selfhost_c…"}`)
	write(".palbase/openapi.json", `{"openapi":"3.2.0"}`)
	write("node_modules/left-pad/index.js", "// huge")
	return dir, bundleRoot
}

func TestAStackPushCarriesTheBuiltCode(t *testing.T) {
	blob, err := BuildStackTarball(projectForPush(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, blob)
	for _, want := range []string{
		"controllers/todo.controller.ts",
		// EVERY schema file travels, not a named one: the stack reads the
		// DIRECTORY, so a tarball carrying only db/public.ts would deploy a
		// project whose second schema silently ceased to exist.
		"db/public.ts",
		"db/billing.ts",
		".palbase/esm/controllers/controllers.js",
		// The suites travel BUILT. A deploy runs them against the release it
		// just made, in a container with no node_modules to resolve them.
		".palbase/esm/tests/todos.test.js",
	} {
		if !entries[want] {
			t.Errorf("the push does not carry %s", want)
		}
	}
	if entries[".palbase/config.json"] {
		t.Error("a stale config document rode along — it would overwrite what the panel set")
	}
}

func TestAStackPushLeavesTheCLIsOwnStateBehind(t *testing.T) {
	blob, err := BuildStackTarball(projectForPush(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, blob)
	// None of these are the backend. `target.json` in particular describes the
	// machine that pushed, and shipping it would put one developer's link into
	// everybody's deployment.
	for _, unwanted := range []string{
		".palbase/target.json",
		".palbase/ios/palbase-config.json",
		".palbase/openapi.json",
		"node_modules/left-pad/index.js",
	} {
		if entries[unwanted] {
			t.Errorf("the push carries %s, which is not the backend", unwanted)
		}
	}
}

func TestTheCloudTarballIsUnchanged(t *testing.T) {
	// The cloud builds server-side from source, so its archive must not start
	// carrying build products: that would ship a bundle the cloud then rebuilds,
	// and the two could disagree.
	pushDir, _ := projectForPush(t)
	blob, err := BuildTarball(pushDir)
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, blob)
	if entries[".palbase/esm/controllers/controllers.js"] || entries[".palbase/config.json"] {
		t.Error("the cloud tarball now carries build products")
	}
	if !entries["controllers/todo.controller.ts"] {
		t.Error("the cloud tarball lost the source")
	}
}

// A refusal a person has to ACT on must be readable, and three of them now are:
// the tests failed, the tests hung, or the schema cannot be applied while the
// running release still serves. Each carries a multi-line explanation — a
// failing assertion, or the objects to split a migration on — and the generic
// path truncates the raw JSON at 300 characters, which turns exactly the thing
// they need into a fragment of an escaped string.
func TestARefusalAPersonMustReadIsPrintedInFull(t *testing.T) {
	long := "the tests failed against the new release, so it was discarded and the " +
		"previous one keeps serving.\n" + strings.Repeat("  todos › ownership is enforced — expected 403, got 200\n", 12)

	for _, code := range []string{"tests_failed", "tests_timed_out", "schema_incompatible", "candidate_failed"} {
		var out strings.Builder
		body := []byte(`{"error":"` + code + `","error_description":` + quote(long) + `,"status":422}`)
		err := renderPushRefusal(&out, 422, body)
		if err == nil {
			t.Fatalf("%s: a refused push reported success", code)
		}
		printed := out.String() + err.Error()
		if !strings.Contains(printed, "ownership is enforced") {
			t.Errorf("%s: the reason was lost:\n%s", code, printed)
		}
		if strings.Contains(printed, "error_description") {
			t.Errorf("%s: the person is reading raw JSON:\n%s", code, printed)
		}
		if strings.Contains(printed, "…") {
			t.Errorf("%s: the reason was truncated:\n%s", code, printed)
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestAStackPushCarriesTheJobAndHookManifests.
//
// Writing a manifest that never gets packed is the same silence one step later.
// The tarball filtered `.palbase` down to `esm` alone, so even a correct
// `jobs.manifest.json` would have been dropped on the way out — and palsvc,
// finding none in the artifact, PRUNES every definition. The tenant would have
// gone from "never scheduled" to "unscheduled on every deploy".
func TestAStackPushCarriesTheJobAndHookManifests(t *testing.T) {
	dir, bundleRoot := projectForPush(t)
	// Manifestolar birer ÜRÜN: bundle köküne yazılırlar, projeye değil.
	write := func(rel, body string) {
		p := filepath.Join(bundleRoot, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".palbase/jobs/jobs.manifest.json",
		`{"jobs":[{"name":"mac-scaler","schedule":"* * * * *","timeout":60,"retry":0,"file":"jobs/mac-scaler.ts"}]}`)
	write(".palbase/hooks/hooks.manifest.json",
		`{"hooks":[{"event":"auth.before_signup","blocking":true,"file":"hooks/signup.ts"}]}`)

	blob, err := BuildStackTarball(dir, bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(t, blob)
	for _, want := range []string{
		".palbase/jobs/jobs.manifest.json",
		".palbase/hooks/hooks.manifest.json",
	} {
		if !entries[want] {
			t.Errorf("%s did not travel — the Go half has no schedule to read, and prunes what it cannot see", want)
		}
	}
}

// CAM-KIR BAYRAĞI TELE ÇIKIYOR MU.
//
// Bir bayrağın var olması yetmiyor: FR-010'un ilk hâlinde parametre HANDLER'da
// vardı ama hiçbir çağrı onu gönderemiyordu, ve taze bir doğrulayıcı bunu
// yakaladı. Ölçülen şey URL'in kendisi.
func TestPushURL_CarriesBothConsents(t *testing.T) {
	const base = "https://stack.example"
	for _, tc := range []struct {
		dataLoss, breaking bool
		want               string
	}{
		{false, false, base + "/v1/management/push"},
		{true, false, base + "/v1/management/push?accept-data-loss=true"},
		{false, true, base + "/v1/management/push?accept-breaking=true"},
		{true, true, base + "/v1/management/push?accept-data-loss=true&accept-breaking=true"},
	} {
		if got := pushURL(base, tc.dataLoss, tc.breaking); got != tc.want {
			t.Errorf("pushURL(dataLoss=%v, breaking=%v) = %q, beklenen %q",
				tc.dataLoss, tc.breaking, got, tc.want)
		}
	}
}

// BAYRAK → PARAMETRE HALKASI.
//
// `TestPushURL_CarriesBothConsents` saf fonksiyonu sabitliyor ama onu KİMİN,
// HANGİ bayrakla çağırdığını tutmuyordu. Taze bir doğrulayıcı ölçtü: `breaking`
// değişkenini `GetBool("approve")`'a bağlayınca suite yeşil kalıyordu — yani
// `--approve` sessizce uyumluluk kapısını açabilir, `--accept-breaking` hiçbir
// şey yapmayabilirdi.
//
// Bu, kapatmak için yazılan kusurun bir halka yukarısı: parametre var, tele
// çıkıyor, ama bağ kanıtsız.
func TestPushCmd_FlagsReachTheRightConsents(t *testing.T) {
	// KİMLİK BU TESTİN KENDİ KURDUĞU ŞEY — geliştiricinin makinesinden ödünç
	// ALINMAZ. Ölçüldü 2026-09-01: bu test yalnız `~/.palbase/credentials.json`
	// içinde `https://127.0.0.1` kaydı OLAN makinelerde yeşildi; temiz bir
	// HOME'da (yani her CI runner'ında) push kimlik bulamayıp stack yoluna hiç
	// girmiyor ve dört vakanın dördü de hiçbir şey ölçmüyordu. Yeşil, kodun
	// değil o makinenin özelliğiydi.
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "")
	if err := StoreCredential("https://127.0.0.1", Credentials{Value: "stack-key", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	dir, _ := projectForPush(t)
	// The local target lives with this MACHINE, not in the checkout; ask for it
	// rather than rebuilding the path `start` stopped writing.
	local, err := LocalStatePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The rig writes, so the rig creates the directory — asking for the path
	// does not, exactly as it does not for the product's own writers.
	if err := ensureMachineStateDir(local); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte(`{"url":"https://127.0.0.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	realPush := stackPush
	t.Cleanup(func() { stackPush = realPush })

	for _, tc := range []struct {
		args                    []string
		wantDataLoss, wantBreak bool
	}{
		{nil, false, false},
		{[]string{"--approve"}, true, false},
		{[]string{"--accept-breaking"}, false, true},
		{[]string{"--approve", "--accept-breaking"}, true, true},
	} {
		var gotDataLoss, gotBreak bool
		called := false
		stackPush = func(_ context.Context, _ Target, _ Credentials, approve, breaking bool, _ io.Writer) error {
			called, gotDataLoss, gotBreak = true, approve, breaking
			return nil
		}

		cmd := newPushCmd()
		cmd.SetArgs(tc.args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		_ = cmd.Execute()

		if !called {
			t.Fatalf("%v: push stack yoluna hiç girmedi — bu vaka o hâlde hiçbir şey ölçmez", tc.args)
		}
		if gotDataLoss != tc.wantDataLoss {
			t.Errorf("%v → accept-data-loss=%v, beklenen %v", tc.args, gotDataLoss, tc.wantDataLoss)
		}
		if gotBreak != tc.wantBreak {
			t.Errorf("%v → accept-breaking=%v, beklenen %v", tc.args, gotBreak, tc.wantBreak)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// PLANSIZ PUSH YOK (FR-046, FR-047, D-014)
//
// Kullanıcının cümlesi: "plansız push yapılamaması lazım, ben her push
// denediğimde direkt gidiyo o zaman planın ne işi var". Kapı iki şey ölçer:
// planın VAR olması ve HÂLÂ DOĞRU olması. İkincisi olmadan plan bir tören
// olurdu — kullanıcı bir dünya için onay verir, push başka bir dünyaya gider.
// ─────────────────────────────────────────────────────────────────────────

// asCloudProject, bu adresi bulut projesi sayan kapıyı test süresince bağlar.
func asCloudProject(t *testing.T, url string) {
	t.Helper()
	prev := CloudProjectAddress
	CloudProjectAddress = func(u string) bool { return u == url }
	t.Cleanup(func() { CloudProjectAddress = prev })
}

// pushableCheckout: `buildableBackend` bir modül yazar ama şema yazmaz, ve push
// İKİSİNİ birden ister (`PlaneOf`: modül + şema). Plan yolu şemasız da koşuyor,
// push yolu koşmuyor — kapı testinin gerçek push yolunda durması gerekiyor.
func pushableCheckout(t *testing.T, dir string) {
	t.Helper()
	buildableBackend(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "public.ts"), []byte(realSchema("public")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPushRefusesWithoutAPlanAndTouchesNothing(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	pushableCheckout(t, dir)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	asCloudProject(t, srv.URL)

	var out bytes.Buffer
	err := runStackPush(context.Background(), Target{URL: srv.URL},
		Credentials{Value: "k", Kind: KindKey}, false, false, &out)
	if err == nil || !errors.Is(err, ErrNoPlan) {
		t.Fatalf("plansız push reddedilmeli (ErrNoPlan): %v", err)
	}
	// SIFIR DOKUNUŞ: red, kiracıya tek bir istek bile atmadan verilmeli.
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("plansız push sunucuya %d istek attı (FR-046: sıfır dokunuş)", n)
	}
}

func TestPushNamesWhatMadeThePlanStale(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	pushableCheckout(t, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wellKnownPath:
			_, _ = w.Write([]byte(`{"hosting":"project","sdk_version":"37.0.2"}`))
		default:
			_, _ = w.Write([]byte(`{"in_sync":true,"changes":[],"destructive":[]}`))
		}
	}))
	defer srv.Close()
	asCloudProject(t, srv.URL)

	// Plan 36.0.2'yi görmüştü; kiracı o zamandan beri 37.0.2'ye taşındı ve
	// bundle da bu plandakinden farklı.
	p := PlanFile{
		Version:          1,
		Target:           PlanTarget{URL: srv.URL, Ref: "abc12345m"},
		BundleDigest:     strings.Repeat("a", 64),
		SDK:              PlanSDK{Running: "36.0.2", Target: installedBackendVersion(dir)},
		SchemaPlanDigest: strings.Repeat("b", 64),
	}
	p.Fingerprint = Fingerprint(p.BundleDigest, p.SDK.Running, p.SDK.Target, p.SchemaPlanDigest)
	if err := WritePlanFile(dir, p); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runStackPush(context.Background(), Target{URL: srv.URL},
		Credentials{Value: "k", Kind: KindKey}, false, false, &out)
	if err == nil {
		t.Fatal("bayat plan kabul edildi")
	}
	for _, want := range []string{"plan is stale", "runtime moved 36.0.2 → 37.0.2", "palbase plan"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("bayatlık sebebi %q adlandırılmadı: %v", want, err)
		}
	}
}

// PLATFORMUN REDDİ KIRPILMADAN BASILIR (FR-049, FR-051).
//
// Ölçülmüş ders: aletin hata çıktısını gövde kırpıcısından geçirmek teşhisi
// imkânsız kılıyor — çerçeve başta, ASIL CÜMLE sonda kalıyor. Ve çare cümlesi
// PLATFORM yanıdır: müşteriden eylem istenmez.
func TestPushPrintsAPlatformRefusalInFull(t *testing.T) {
	long := strings.Repeat("syntax error near jsonb_object_agg ", 20)
	report := `{"target":"37.0.2","mode":"expand","outcome":"failed","items":[{"module":"auth",` +
		`"migration":"000014_social_auth.up.sql","severity":"critical","count":1,"message":"` + long + `"}],` +
		`"ledger":{"auth":{"version":13,"dirty":true,"repaired":false}},"skippedContract":[],` +
		`"durationMs":812,"reportId":"ab12cd34ef56"}`
	var out bytes.Buffer
	renderUpgradeReport(&out, []byte("abc12345m cannot move to SDK 37.0.2: expand failed (report ab12cd34ef56)\n"+report))
	s := out.String()
	if !strings.Contains(s, "critical · auth/000014_social_auth.up.sql · 1 · ") {
		t.Fatalf("rapor kalemi basılmadı:\n%s", s)
	}
	if n := strings.Count(s, "syntax error near jsonb_object_agg"); n != 20 {
		t.Fatalf("asıl cümle kırpıldı: %d/20 kez göründü:\n%s", n, s)
	}
	if !strings.Contains(s, "ledger auth: version 13 dirty=true") {
		t.Fatalf("defter durumu basılmadı:\n%s", s)
	}
	if !strings.Contains(s, "report ab12cd34ef56") || !strings.Contains(s, "no customer action") {
		t.Fatalf("çare cümlesi eksik ya da müşteriye iş yüklüyor:\n%s", s)
	}
}
