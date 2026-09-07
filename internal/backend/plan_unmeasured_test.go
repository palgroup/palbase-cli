package backend

// plan_unmeasured_test.go — SERVİS ETMEYEN BİR PROJE PLANLANABİLİR OLMALI (FR-063).
//
// Bu koşunun hedefi olan kiracı `palbase plan`ı hiç bitiremiyordu: şema planı
// kiracının KENDİ adresinden hesaplanıyor, ölü kiracı cevap veremiyor, çağrı
// düşüyor ve plan dosyası HİÇ YAZILMIYORDU — sonra `palbase push` "plan yok"
// diye reddediyordu. Kapı kendi çaresini kilitliyordu.
//
// FİKSTÜR TAHMİN DEĞİL, ÖLÇÜM: penny (`na1m7lt2m`) 2026-09-07'de tam olarak
// bunu döndürüyor — kapıdan gelen gövdesiz bir HTTP 503, taşıma hatası DEĞİL:
//
//	$ curl -sS -w '%{http_code}' https://na1m7lt2m.palbase.studio/.well-known/palbase.json
//	upstream connect error or disconnect/reset before headers. reset reason:
//	remote connection failure, transport failure reason: delayed connect error:
//	Connection refused
//	503
//
// Bir gevşetme yalnız `err != nil`i tolere etseydi bu kiracıda ATIL kalırdı.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// declareSchema, projeye GERÇEK bir tablo bildirimi koyar.
//
// FİKSTÜRÜN KENDİSİ BİR İDDİA: `db/public.ts` olmadan şema planı hiç SORULMAZ
// ve testler kusuru göremeden yeşil kalır — ilk hâlleri tam olarak öyle geçti.
func declareSchema(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PublicSchemaFile), []byte(realSchema("public")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newDeadTenantServer, servis etmeyen bir kiracının ARKASINDAKİ KAPIYI taklit
// eder: her yola gövdesiz 503.
func newDeadTenantServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream connect error or disconnect/reset before headers. " +
			"reset reason: remote connection failure, transport failure reason: " +
			"delayed connect error: Connection refused"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newHalfDeadTenantServer, NEGATİF KONTROLÜN kiracısı: sürümünü söyleyebiliyor
// ama şema planını reddediyor. Böyle bir kiracı için "soramadım" YALAN olurdu.
func newHalfDeadTenantServer(t *testing.T, runningSDK string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == wellKnownPath {
			_, _ = w.Write([]byte(`{"hosting":"project","sdk_version":"` + runningSDK + `"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("the schema planner is unavailable"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPlanOfANotAnsweringProjectIsStillWritten(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	buildableBackend(t, dir)
	declareSchema(t, dir)

	target := newDeadTenantServer(t)
	orig := CloudRuntimePlanner
	CloudRuntimePlanner = func(_ context.Context, _, sdk string) (json.RawMessage, error) {
		// PLANLAYICI PLATFORMA GİDER, KİRACIYA DEĞİL — ölü kiracıda da cevap verir.
		return json.RawMessage(`{"running":"","target":"` + sdk + `","changed":true,` +
			`"modules":[{"module":"auth","expandFrom":13,"expandTo":14,"contractFrom":0,"contractTo":1}]}`), nil
	}
	t.Cleanup(func() { CloudRuntimePlanner = orig })

	var out strings.Builder
	if err := runPlan(context.Background(), dir, Target{URL: target.URL},
		Credentials{Value: "k", Kind: KindKey}, &out); err != nil {
		t.Fatalf("ölü kiracı için plan düştü — push'un tek çaresi bu dosya: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "this project is not answering") {
		t.Fatalf("kullanıcı neyin ölçülemediğini görmüyor:\n%s", out.String())
	}

	p, err := ReadPlanFile(dir)
	if err != nil {
		t.Fatalf("plan dosyası yazılmadı: %v", err)
	}
	if p.SDK.Running != "" {
		t.Fatalf("ölçülemeyen sürüm plana bir değer olarak girdi: %q", p.SDK.Running)
	}
	if len(p.Unmeasured) != 1 || !strings.Contains(p.Unmeasured[0], "not answering") {
		t.Fatalf("plan dosyası neyi soramadığını YAZMIYOR: %#v", p.Unmeasured)
	}
	// ÖZET "ŞEMA YOK" İLE "SORAMADIM"I AYIRMAK ZORUNDA. Eskiden ikisi de
	// `{}` idi: iki AYRI dünya aynı parmak izini üretiyordu, ve push plan
	// yazıldığında verilmemiş bir onayı taşıyabilirdi.
	unmeasured := sha256.Sum256(SchemaUnmeasured)
	noSchema := sha256.Sum256([]byte("{}"))
	if p.SchemaPlanDigest != hex.EncodeToString(unmeasured[:]) {
		t.Fatalf("ölçülemeyen şema özeti sentinel'den gelmiyor: %s", p.SchemaPlanDigest)
	}
	if p.SchemaPlanDigest == hex.EncodeToString(noSchema[:]) {
		t.Fatal(`"soramadım" ile "bu proje tablo bildirmiyor" aynı özete katlandı`)
	}
	if p.Fingerprint != Fingerprint(p.BundleDigest, p.SDK.Running, p.SDK.Target, p.SchemaPlanDigest) {
		t.Fatalf("parmak izi kendi içeriğiyle tutmuyor: %s", p.Fingerprint)
	}
}

// NEGATİF KONTROL: CEVAP VEREBİLEN bir kiracının reddi GERÇEK bir reddir.
// Gevşetme yalnız "soramadım"a ait; "sordum, reddetti"ye değil.
func TestPlanStillFailsWhenAnAnsweringProjectRefusesTheSchema(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	buildableBackend(t, dir)
	declareSchema(t, dir)

	target := newHalfDeadTenantServer(t, "36.0.2")
	orig := CloudRuntimePlanner
	CloudRuntimePlanner = func(_ context.Context, _, sdk string) (json.RawMessage, error) {
		return json.RawMessage(`{"running":"36.0.2","target":"` + sdk + `","changed":true}`), nil
	}
	t.Cleanup(func() { CloudRuntimePlanner = orig })

	var out strings.Builder
	err := runPlan(context.Background(), dir, Target{URL: target.URL},
		Credentials{Value: "k", Kind: KindKey}, &out)
	if err == nil {
		t.Fatalf("cevap veren bir kiracının şema reddi yutuldu:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("red kullanıcıya durumuyla söylenmiyor: %v", err)
	}
	if _, rerr := ReadPlanFile(dir); rerr == nil {
		t.Fatal("reddedilen bir şema planı için plan dosyası yazıldı")
	}
}

// requirePlan AYNI GEVŞETMEYİ TAŞIMALI: elle yazılmış doğru bir plan dosyasıyla
// bile push, `prepareCloudRuntime`a hiç varmadan burada ölüyordu.
func TestRequirePlanAcceptsAPlanWhoseProjectIsNotAnswering(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PublicSchemaFile),
		[]byte("export const tables = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := newDeadTenantServer(t)

	bundle, err := BundleDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(SchemaUnmeasured)
	saved := PlanFile{
		Version:          1,
		CreatedAt:        time.Now().UTC(),
		Target:           PlanTarget{URL: target.URL},
		BundleDigest:     bundle,
		SDK:              PlanSDK{Running: "", Target: installedBackendVersion(dir)},
		SchemaPlanDigest: hex.EncodeToString(sum[:]),
		Unmeasured:       []string{unmeasuredNote},
	}
	saved.Fingerprint = Fingerprint(saved.BundleDigest, saved.SDK.Running, saved.SDK.Target, saved.SchemaPlanDigest)
	if err := WritePlanFile(dir, saved); err != nil {
		t.Fatal(err)
	}

	got, err := requirePlan(context.Background(), dir, Target{URL: target.URL}, Credentials{Value: "k", Kind: KindKey})
	if err != nil {
		t.Fatalf("ölü kiracının planı reddedildi — push'un önündeki son kilit buydu: %v", err)
	}
	if got.Fingerprint != saved.Fingerprint {
		t.Fatalf("plan başka bir plana dönüştü: %s → %s", saved.Fingerprint, got.Fingerprint)
	}
}

// NEGATİF KONTROL: cevap veren bir kiracıda requirePlan hâlâ düşer.
func TestRequirePlanStillRefusesWhenAnAnsweringProjectRefusesTheSchema(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PublicSchemaFile),
		[]byte("export const tables = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := newHalfDeadTenantServer(t, "36.0.2")

	bundle, _ := BundleDigest(dir)
	sum := sha256.Sum256(SchemaUnmeasured)
	saved := PlanFile{
		Version:          1,
		Target:           PlanTarget{URL: target.URL},
		BundleDigest:     bundle,
		SDK:              PlanSDK{Running: "36.0.2", Target: installedBackendVersion(dir)},
		SchemaPlanDigest: hex.EncodeToString(sum[:]),
	}
	saved.Fingerprint = Fingerprint(saved.BundleDigest, saved.SDK.Running, saved.SDK.Target, saved.SchemaPlanDigest)
	if err := WritePlanFile(dir, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := requirePlan(context.Background(), dir, Target{URL: target.URL},
		Credentials{Value: "k", Kind: KindKey}); err == nil {
		t.Fatal("cevap veren bir kiracının şema reddi push kapısında yutuldu")
	}
}

// checkRuntimeSchemaPlan, Kapı 5'in gevşetmesinden HEMEN SONRA aynı ölü pod'a
// soruyordu — yani o gevşetme tek başına atıldı.
func TestCheckRuntimeSchemaPlanDoesNotBlockAProjectThatCannotAnswer(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PublicSchemaFile),
		[]byte("export const tables = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := newDeadTenantServer(t)
	var out strings.Builder
	if err := checkRuntimeSchemaPlan(context.Background(), dir, Target{URL: target.URL},
		Credentials{Value: "k", Kind: KindKey}, false, &out, false); err != nil {
		t.Fatalf("ölçülemeyen şema planı push'u engelledi: %v", err)
	}
	// VE NEGATİF KONTROL AYNI FİKSTÜRDE: `answering` true iken aynı 503 ölümcül.
	if err := checkRuntimeSchemaPlan(context.Background(), dir, Target{URL: target.URL},
		Credentials{Value: "k", Kind: KindKey}, false, &out, true); err == nil {
		t.Fatal("cevap verebilen bir kiracıda şema reddi yutuldu")
	}
}
