package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ROL ARTEFAKTININ YOKLUĞU SESSİZ OLAMAZ.
//
// `palbase web link` sözleşmeyi ortamdan TAZE çekiyor ama rolleri yerel bir
// dosyadan KOPYALIYOR — ve o dosyayı yalnız `palbase spec` turu yazıyor. Link
// yayımlanabilir anahtarla koşuyor, rol ucu ise service_role kapılı, yani link
// rolleri ÇEKEMEZ; bu tasarım gereği doğru.
//
// Yanlış olan sessizlikti: dosya yoksa kopyalayıcı hiçbir şey demeden geçiyordu.
// Sonuç, geliştiricinin göreceği hâliyle şu — link "başarılı" diyor, `palbe-gen`
// koşuyor, ve üretilen istemcide `Roles`/`Permissions` sabitleri HİÇ olmuyor.
// Kimse sebebini söylemiyor. Bu dosyanın iki fonksiyon yukarısındaki kendi
// cümlesi zaten kuralı koymuş: "sessizce izlemeyi bırakan bir artefakt,
// buradaki kimsenin fark etmeyeceği tek hata biçimidir."
func TestLinkSaysWhenRolesWereNotFetched(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	// Yerel rol artefaktı YOK — `spec` hiç koşmamış bir checkout'un hâli.
	if err := copyRolesToWeb("main", &out); err != nil {
		t.Fatalf("copyRolesToWeb: %v", err)
	}

	if _, err := os.Stat(filepath.Join(webArtifactsDir, "roles.json")); !os.IsNotExist(err) {
		t.Fatalf("çekilmemiş roller için dosya yazıldı — boş bir liste, tanımları SİLERDİ")
	}
	msg := out.String()
	if !strings.Contains(msg, "palbase spec") {
		t.Errorf("yokluk sessiz kaldı; çare adlandırılmıyor:\n%s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "roles") {
		t.Errorf("çıktı neyin eksik olduğunu söylemiyor:\n%s", msg)
	}
}
