package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// LINK, ÇAĞRILDIĞI DİZİNİN DIŞINA YAZMAMALI.
//
// Ölçüldü (07.09.2026, müşteri deposu): `link` hazırlık alanını
// `os.MkdirTemp(filepath.Dir(root), ".palbase-link-*")` ile açıyordu — yani
// projenin BİR ÜSTÜNE. Müşteride proje `smartex/palbase`, üstü ise git
// deposunun kökü; kalıntı orada `?? .palbase-link-1344746409/` olarak duruyordu,
// bir gün önceden kalma, içinde `.env.local` kopyası (bir API anahtarı) ve canlı
// checkout'a symlink'ler vardı. `git check-ignore` hiçbir kural bulmadı.
//
// Dosyanın kendi kuralı bunu zaten yasaklıyor: kullanıcıdan gelen yollar için
// "link output and entry must belong to this checkout" diyor. Aracın kendisi o
// kurala uymuyordu.
//
// STAGE OS TEMP'İNE TAŞINAMAZ: yayımlama `os.Rename(staged, live)` ile yapılıyor
// ve farklı dosya sistemleri arasında rename EXDEV ile düşer. Doğru yer
// checkout'un İÇİ — aynı dosya sistemi garantili, ve hiçbir şey dışarı yazılmaz.
func linkedProjectForStaging(t *testing.T) (root, parent string) {
	t.Helper()
	inScratchCheckout(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return root, filepath.Dir(root)
}

func stagingLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".palbase-link") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func TestTheStageNeverTouchesTheCheckout(t *testing.T) {
	// SÜPÜRMEK YETMEZ, HİÇ DOĞMASIN.
	//
	// İlk düzeltmede hazırlık alanını checkout'un İÇİNE almıştım (üstünden
	// alarak) ve her koşuda eskiyi süpürüyordum. Kullanıcı haklı olarak reddetti:
	// "süpürülüyor değil, hiç oluşmasın". Bir link sürerken müşterinin projesinde
	// `.palbase-link-XXXX/` görünmesi, kalıntı olmasa bile onun görmek istemediği
	// şey.
	//
	// Engel `os.Rename`'di: yayımlama stage'den checkout'a taşıma yapıyor ve
	// farklı dosya sistemleri arasında rename EXDEV ile düşer. Çare stage'i
	// checkout'a sokmak değil, taşımanın EXDEV'i karşılaması.
	root := t.TempDir()
	stage, err := newLinkStage(root)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stage) })

	rel, relErr := filepath.Rel(root, stage)
	if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("hazırlık alanı checkout'un İÇİNDE doğdu: %s (kök %s)", stage, root)
	}
	if info, statErr := os.Stat(stage); statErr != nil || !info.IsDir() {
		t.Fatalf("hazırlık alanı yaratılmadı: %v", statErr)
	}
}

// (Burada bir `TestTheToolRunningInsideTheStageSeesNoSelfReference` vardı: stage
// checkout'un İÇİNDEYKEN kök taraması onu görüyordu ve atlanmazsa ayna kendi
// içine symlink kurup stage'i gezen her aracı döngüye sokuyordu. Stage temp'e
// taşınınca o dal YAPISAL olarak imkânsız hâle geldi — atlama ölü koda dönüştü
// ve silindi, testi de kırılamaz hâle geldiği için kaldırıldı. Bilgisi burada
// duruyor: stage checkout'a geri sokulursa o tuzak geri gelir, ve yukarıdaki
// iddia tam olarak onu tutuyor.)

func TestMovingATreeIntoTheCheckoutWorks(t *testing.T) {
	// `os.Rename` aynı dosya sisteminde çalışır; test makinesinde temp ile
	// checkout aynı FS'te olabilir ve rename hiç düşmeyebilir. Bu yüzden taşıyıcı
	// rename'i DENEYİP EXDEV'de kopyaya düşen bir fiil, ve kopya dalı ayrıca
	// ölçülüyor (aşağıda) — nadir dallar test edilmezse üretimde ilk kez koşar.
	from := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(from, "pkg", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := "module.exports=1"
	if err := os.WriteFile(filepath.Join(from, "pkg", "nested", "index.js"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(t.TempDir(), "node_modules")

	if err := moveTree(from, to); err != nil {
		t.Fatalf("taşıma düştü: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(to, "pkg", "nested", "index.js"))
	if err != nil || string(body) != want {
		t.Fatalf("taşınan ağaç eksik: %v %q", err, body)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Errorf("kaynak ağaç geride kaldı: %s", from)
	}
}

func TestCopyFallbackReproducesTheTreeExactly(t *testing.T) {
	from := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(from, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "a", "b", "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "top.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(t.TempDir(), "dst")
	if err := copyTree(from, to); err != nil {
		t.Fatalf("kopya: %v", err)
	}
	for rel, want := range map[string]string{"a/b/f.txt": "x", "top.txt": "y"} {
		got, err := os.ReadFile(filepath.Join(to, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Errorf("%s: %v %q", rel, err, got)
		}
	}
	// İZİNLER KORUNMALI: 0600'lük bir dosya kopyada 0644 olursa, sır taşıyan bir
	// dosya kopyada okunabilir hâle gelir.
	fi, err := os.Stat(filepath.Join(to, "a", "b", "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("izin korunmadı: %v", fi.Mode().Perm())
	}
}

func TestASuccessfulLinkLeavesNoStageBehind(t *testing.T) {
	root, parent := linkedProjectForStaging(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &strings.Builder{}); err != nil {
		t.Fatalf("link: %v", err)
	}
	for _, dir := range []string{root, parent} {
		if got := stagingLeftovers(t, dir); len(got) != 0 {
			t.Errorf("başarılı link'ten sonra kalıntı: %v", got)
		}
	}
}

// YARIDA KESİLEN BİR KOŞU KALINTI BIRAKIR — VE BİR SONRAKİ KOŞU ONU TOPLAMALI.
//
// Temizlik yalnız `defer os.RemoveAll(stage)` ile yapılıyordu; SIGINT, SIGKILL,
// panik ya da elektrik kesintisinde o defer KOŞMAZ. Müşterideki kalıntı bir gün
// önceden kalmıştı, yani tam olarak bu yol gerçekleşmişti.
//
// Sinyal yakalamak yetmez (SIGKILL yakalanamaz). Sağlam olan, her koşunun
// BAŞINDA eskiyi süpürmesidir: hazırlık alanı koşu başına tekildir ve tek
// yazarlıdır, dolayısıyla önceden duran her şey ölüdür.
func TestLinkSweepsAStaleStageBeforeRunning(t *testing.T) {
	root, _ := linkedProjectForStaging(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	stale := filepath.Join(root, ".palbase-link-999999999")
	if err := os.MkdirAll(filepath.Join(stale, ".palbase"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, ".env.local"), []byte("SECRET=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &strings.Builder{}); err != nil {
		t.Fatalf("link: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("yarıda kalmış bir koşunun kalıntısı süpürülmedi: %s — "+
			"içinde sır kopyası taşıyabilir ve .gitignore onu kapsamıyor", stale)
	}
}

// EXDEV DALI GERÇEKTEN KOPYAYA DÜŞÜYOR MU.
//
// `copyTree` ayrıca test ediliyor, ama BAĞLANTISI edilmiyordu: dalı silen bir
// mutasyon, temp ile hedefin aynı FS'te olduğu bu makinede hiçbir testi kırmadı.
// Rename'i EXDEV döndürmeye zorlayarak dalı ölçülebilir kılıyoruz.
func TestMoveFallsBackToCopyAcrossFilesystems(t *testing.T) {
	original := renameForMove
	t.Cleanup(func() { renameForMove = original })
	calls := 0
	renameForMove = func(string, string) error {
		calls++
		return &os.LinkError{Op: "rename", Err: syscall.EXDEV}
	}

	from := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "f"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(t.TempDir(), "dst")

	if err := moveTree(from, to); err != nil {
		t.Fatalf("EXDEV'de taşıma düştü — kopya dalı bağlı değil: %v", err)
	}
	if calls == 0 {
		t.Fatal("negatif kontrol: rename hiç denenmedi, ucuz yol atlanıyor")
	}
	got, err := os.ReadFile(filepath.Join(to, "f"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("kopya eksik: %v %q", err, got)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Error("kopya sonrası kaynak silinmedi")
	}
}
