package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestTheStageBelongsToTheCheckout(t *testing.T) {
	// YERLEŞİMİ DOĞRUDAN ÖLÇÜYORUZ, ARTIĞINI DEĞİL.
	//
	// İlk hâli "başarılı bir link'ten sonra üst dizin temiz mi" diye soruyordu ve
	// GEÇİYORDU — çünkü başarılı koşuda `defer os.RemoveAll(stage)` zaten
	// temizliyor. Kusur yalnız koşu yarıda kesildiğinde görünür hâle geliyordu,
	// yani o test kusuru ölçmüyordu. Ölçülmesi gereken şey artık değil, stage'in
	// NEREYE açıldığı.
	root := t.TempDir()
	stage, err := newLinkStage(root)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stage) })

	rel, err := filepath.Rel(root, stage)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("hazırlık alanı checkout'un DIŞINDA: %s (kök %s) — aracın kendisi, "+
			"kullanıcıya dayattığı \"must belong to this checkout\" kuralına uymuyor", stage, root)
	}
	if info, err := os.Stat(stage); err != nil || !info.IsDir() {
		t.Fatalf("hazırlık alanı yaratılmadı: %v", err)
	}
	// AYNI DOSYA SİSTEMİ ZORUNLU: yayımlama `os.Rename(staged, live)` ile yapılıyor
	// ve farklı dosya sistemleri arasında rename EXDEV ile düşer. Checkout'un içi
	// bunu garantiler; OS temp'i garantilemez.
	if err := os.WriteFile(filepath.Join(stage, "probe"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(stage, "probe"), filepath.Join(root, "probe")); err != nil {
		t.Fatalf("stage'den checkout'a rename düştü — yayımlama yolu bozulur: %v", err)
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

// STAGE KENDİNİ İÇEREMEZ — VE BUNU STAGE'İN İÇİNDE KOŞAN ARAÇ SÖYLER.
//
// Hazırlık alanı artık checkout'un İÇİNDE açılıyor, yani `os.ReadDir(root)`
// taramasında görünüyor. Atlanmazsa ayna kendi içine bir symlink kurar
// (`stage/.palbase-link-X -> root/.palbase-link-X`, yani stage'in kendisi) ve
// stage'i symlink takip ederek gezen HER araç — npm, kod üreteçleri — sonsuz
// döngüye girer.
//
// İLK HÂLİ ÖLÇMÜYORDU: atlamayı söküp testleri koştum ve HEPSİ GEÇTİ, çünkü
// `collectArtifacts` symlink'i toplamıyor ve kalıntı testine yansımıyor. Bir
// satırı "korumalı" diye bırakıp gate'i varmış gibi saymak, tam olarak bu
// depoda üç kez ödenen hata. Bu yüzden gözlem, stage'in İÇİNDE koşan aracın
// gördüğü dizin listesinden alınıyor.
func TestTheToolRunningInsideTheStageSeesNoSelfReference(t *testing.T) {
	root, _ := linkedProjectForStaging(t)
	listing := filepath.Join(t.TempDir(), "cwd-listing")
	stub := filepath.Join(t.TempDir(), "stub-swiftgen")
	script := "#!/bin/sh\nls -a . >> \"" + listing + "\"\n" + `while [ $# -gt 0 ]; do
  case "$1" in
    --out-swift|--out-plist) printf 'generated\n' > "$2"; shift 2 ;;
    *) shift ;;
  esac
done
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useStub(t, stub, nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &strings.Builder{}); err != nil {
		t.Fatalf("link: %v", err)
	}

	seen, err := os.ReadFile(listing)
	if err != nil {
		t.Fatalf("stage içinde koşan araç dizini listeleyemedi: %v", err)
	}
	// NEGATİF KONTROL: araç gerçekten stage'de koştu mu. Boş bir listeleme,
	// hiçbir şey iddia etmemiş olurdu.
	if !strings.Contains(string(seen), ".palbase") {
		t.Fatalf("araç beklenen dizinde koşmamış görünüyor:\n%s", seen)
	}
	if strings.Contains(string(seen), linkStagePrefix) {
		t.Errorf("stage kendini içeriyor — symlink döngüsü:\n%s", seen)
	}
	_ = root
}
