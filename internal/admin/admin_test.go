package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type stubREST struct {
	method, path string
	body         any
	reply        any
}

func (s *stubREST) Do(_ context.Context, method, path string, body, out any) error {
	s.method, s.path, s.body = method, path, body
	if out == nil || s.reply == nil {
		return nil
	}
	raw, err := json.Marshal(s.reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func run(t *testing.T, rest *stubREST, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd(Resolvers{REST: func() REST { return rest }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// A fleet roll is the widest blast radius this CLI has. The confirmation must
// be something nobody satisfies by reflex: typing the image means having read
// WHICH image.
func TestFleetUpgradeRefusesAMismatchedConfirmation(t *testing.T) {
	rest := &stubREST{}
	_, err := run(t, rest, "yes\n", "fleet", "upgrade", "acr.example/stack:sha-abc", "--canary", "canary0m")
	if err == nil {
		t.Fatal("a reflex confirmation was accepted")
	}
	if rest.method != "" {
		t.Fatalf("the roll was sent anyway: %s %s", rest.method, rest.path)
	}
}

func TestFleetUpgradeSendsImageAndParallelism(t *testing.T) {
	rest := &stubREST{reply: UpgradeAccepted{JobID: "job_1"}}
	out, err := run(t, rest, "", "fleet", "upgrade", "acr.example/stack:sha-abc", "--canary", "canary0m", "--yes", "--parallel", "6")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if rest.method != "POST" || rest.path != "/v1/cloud/fleet/upgrade" {
		t.Fatalf("wrong call: %s %s", rest.method, rest.path)
	}
	sent, _ := rest.body.(map[string]any)
	if sent["image"] != "acr.example/stack:sha-abc" {
		t.Fatalf("image did not reach the wire: %#v", rest.body)
	}
	if fmt.Sprint(sent["parallel"]) != "6" {
		t.Fatalf("parallelism did not reach the wire: %#v", rest.body)
	}
	if !strings.Contains(out, "job_1") {
		t.Fatalf("the job id is missing:\n%s", out)
	}
}

// The plane returns a job id and NOTHING else. Printing a tenant count it never
// sent is how an operator was told "Rolling 0 tenant(s) (0 already there)"
// while a fleet of fourteen sat there — a lie with a tabwriter.
func TestFleetUpgradeInventsNoCounts(t *testing.T) {
	rest := &stubREST{reply: map[string]string{"jobId": "job_2"}}
	out, err := run(t, rest, "", "fleet", "upgrade", "acr.example/stack:sha-abc", "--canary", "canary0m", "--yes")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if strings.Contains(out, "0 tenant") || strings.Contains(out, "0 already") {
		t.Fatalf("printed a count the server never sent:\n%s", out)
	}
}

func TestSweepRefusesAMismatchedConfirmation(t *testing.T) {
	rest := &stubREST{}
	if _, err := run(t, rest, "ok\n", "sweep"); err == nil {
		t.Fatal("a reflex confirmation was accepted")
	}
	if rest.method != "" {
		t.Fatal("the sweep was sent anyway")
	}
}

// An empty sweep is the healthy answer and must READ as one — a bare blank line
// looks like the command failed.
func TestSweepSaysSoWhenThereIsNothingToDo(t *testing.T) {
	out, err := run(t, &stubREST{reply: []SweepEntry{}}, "", "sweep", "--yes")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !strings.Contains(out, "Nothing to sweep") {
		t.Fatalf("an empty sweep printed nothing useful:\n%s", out)
	}
}

func TestSweepReportsWhatItDeletedAndWhy(t *testing.T) {
	rest := &stubREST{reply: []SweepEntry{
		{Cell: "pbc-cell-01", Ref: "abandoned1", Action: "deleted", Reason: "unknown-ref"},
		{Cell: "pbc-cell-02", Ref: "keepthis1", Action: "kept", Reason: "raced-create"},
	}}
	out, err := run(t, rest, "", "sweep", "--yes")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, want := range []string{"abandoned1", "deleted", "unknown-ref", "keepthis1", "kept", "raced-create"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
}

// The v1 verbs are gone: they addressed a plane of many modules with per-module
// images. v2 runs ONE stack image per tenant, so there is nothing per-module
// left to point anywhere.
func TestSurfaceCarriesOnlyTheV2OperatorVerbs(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
	}
	got := fmt.Sprint(names)
	if got != "[fleet sweep]" {
		t.Fatalf("unexpected surface: %s", got)
	}
}

// KANARYA İŞARETİ ZORUNLU — VE VARSAYILANI YOKTU DEĞİL, YANLIŞTI.
//
// Düzlem eskiden envanterin İLKİNİ kanarya yapıyordu: yani rastgele bir ÖDEYEN
// MÜŞTERİ, bir imajın açılmadığını ilk öğrenen oluyordu. O kesinti geri
// alınamaz — migrasyon runtime reddedilmeden ÖNCE koşuyor, yani eski imaja
// dönmek eski runtime'ı yeni şemanın önüne koymak demek.
//
// Bayrak eksikken komut İSTEĞİ HİÇ GÖNDERMEMELİ: sunucu da reddediyor, ama
// reddi ağa çıkmadan söylemek operatöre bir tur kazandırıyor.
func TestFleetUpgradeRefusesWithoutACanary(t *testing.T) {
	rest := &stubREST{}
	out, err := run(t, rest, "", "fleet", "upgrade", "acr.example/stack:sha-abc", "--yes")
	if err == nil {
		t.Fatalf("kanaryasız yükseltme KABUL EDİLDİ: %s", out)
	}
	if !strings.Contains(err.Error(), "--canary is required") {
		t.Fatalf("red kanaryayı adlandırmıyor: %v", err)
	}
	if rest.path != "" {
		t.Fatalf("istek yine de GÖNDERİLDİ (%s) — red ağa çıkmadan verilmeliydi", rest.path)
	}
}

// VE GÖNDERİLEN GÖVDE onu TAŞIMALI: bayrağı okuyup göndermemek, kapıyı yalnız
// istemcide kurmak olurdu.
func TestFleetUpgradeSendsTheCanaryRef(t *testing.T) {
	rest := &stubREST{reply: map[string]any{"jobId": "j1"}}
	if _, err := run(t, rest, "", "fleet", "upgrade", "acr.example/stack:sha-abc",
		"--canary", "kanarya0m", "--yes"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	body, ok := rest.body.(map[string]any)
	if !ok {
		t.Fatalf("gövde okunamadı: %#v", rest.body)
	}
	if body["canaryRef"] != "kanarya0m" {
		t.Fatalf("gövde canaryRef taşımıyor: %#v", body)
	}
}

// OPERATÖR SÜRÜKLENMEYİ OKUYABİLMELİ (FR-014).
//
// `GET /v1/panel/fleet/drift` var ve üçlü ayrımı doğru yapıyor — ama bugün onu
// çağırabilen tek istemci Studio ve o ekran henüz yok. Yani operatörün "filom
// nerede sürüklenmiş" sorusunu soracağı hiçbir yer yoktu: kapıları olan ama
// gözü olmayan bir sistem.
func TestFleetDriftReadsThePanelSurface(t *testing.T) {
	rest := &stubREST{reply: []map[string]any{
		{"ref": "aaa11111m", "observed": "acr/t:sha-OLD", "desired": "acr/t:sha-NEW", "cell_id": "01", "bucket": "toAlign"},
		{"ref": "zzz99999m", "observed": "", "desired": "acr/t:sha-NEW", "cell_id": "01", "bucket": "onWake"},
	}}
	out, err := run(t, rest, "", "fleet", "drift")
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if rest.method != "GET" || rest.path != "/v1/panel/fleet/drift" {
		t.Fatalf("yanlış uç: %s %s", rest.method, rest.path)
	}
	// KOVALAR AYRI GÖSTERİLMELİ. Tek listede vermek tam da ekranın okunmaz
	// olmasına yol açan şeydi: arşivli çoğunluk, gerçekten yanlış imaj koşan
	// azınlığı gömüyordu.
	if !strings.Contains(out, "aaa11111m") || !strings.Contains(out, "zzz99999m") {
		t.Fatalf("her iki kiracı da listelenmedi:\n%s", out)
	}
	if !strings.Contains(out, "toAlign") || !strings.Contains(out, "onWake") {
		t.Fatalf("kovalar adlandırılmadı:\n%s", out)
	}
}

// BOŞ SÜRÜKLENME AÇIKÇA SÖYLENMELİ.
//
// Sessiz bir boş çıktı, "filo hizalı" ile "komut çalışmadı"yı aynı gösterir —
// ve bu depo o sınıf sessizliğin bedelini defalarca ödedi.
func TestFleetDriftSaysAlignedRatherThanPrintingNothing(t *testing.T) {
	rest := &stubREST{reply: []map[string]any{}}
	out, err := run(t, rest, "", "fleet", "drift")
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("boş sürüklenme SESSİZCE geçti — 'hizalı' ile 'komut çalışmadı' ayırt edilemez")
	}
}
