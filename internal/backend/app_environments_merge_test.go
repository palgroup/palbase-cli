package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// BİR LİNK, ÜRETEMEDİĞİ ŞEYİ SİLMEZ.
//
// Bu dosyayı iki yol yazıyor ve biri diğerinin bildiğini bilmiyor: bulut yolu
// uygulamanın OAuth yapılandırmasını düzlemden çözüyor, yığın-URL yolu onu HİÇ
// görmüyor. İkincisi dosyayı olduğu gibi yazınca Apple+Google bloğu siliniyordu.
//
// Ölçüldü 25.08.2026, centauri: `link <url>` `app_id`'yi yığın ref'iyle
// değiştirdi, `api_key`'i düşürdü ve OAuth bloğunu tamamen sildi. Uygulama
// derlenmeye devam ederdi — kaybolan tek şey GİRİŞ olurdu.
func TestALinkDoesNotDeleteWhatItCannotProduce(t *testing.T) {
	dir := t.TempDir()
	existing := appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {
				AppID: "app_gercek", BaseURL: "https://r.palbase.studio", APIKey: "pb_r_cKEY",
				OAuth: json.RawMessage(`{"google":{"enabled":true,"client_id":"g"}}`),
			},
			// Bir koşumun GÖREMEDİĞİ ortam: yerel yığın kapalı olabilir.
			"local": {AppID: "app_gercek", BaseURL: "http://127.0.0.1:9999"},
		},
	}
	blob, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "palbase-config.json"), blob, 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// Yığın-URL yolunun ürettiği şey: OAuth yok, anahtar yok, ref'i app_id
	// sanıyor, ve `local`'ı hiç görmüyor.
	poorer := appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: "", BaseURL: "https://r.palbase.studio", APIKey: ""},
		},
	}
	got := mergeWithExisting(dir, poorer)

	main := got.Environments["main"]
	if len(main.OAuth) == 0 {
		t.Fatal("OAuth bloğu SİLİNDİ — uygulamanın Apple/Google girişi sessizce ölürdü")
	}
	if main.APIKey != "pb_r_cKEY" {
		t.Fatalf("api_key düştü: %q", main.APIKey)
	}
	if main.AppID != "app_gercek" {
		t.Fatalf("app_id düştü: %q", main.AppID)
	}
	if _, ok := got.Environments["local"]; !ok {
		t.Fatal("görülemeyen ortam KAYBOLDU — derleme yapılandırması yok olur")
	}
}

// ÜRETİLEN DEĞER KAZANIR. Aksi hâlde birleştirme, döndürülmesi gereken bir
// anahtarı sonsuza kadar eski tutardı — sessiz bir rotasyon arızası.
func TestAFreshValueWins(t *testing.T) {
	dir := t.TempDir()
	blob, _ := json.Marshal(appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: "eski", BaseURL: "https://r", APIKey: "eski",
				OAuth: json.RawMessage(`{"google":{"enabled":false}}`)},
		},
	})
	_ = os.WriteFile(filepath.Join(dir, "palbase-config.json"), blob, 0o644)

	got := mergeWithExisting(dir, appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: "yeni", BaseURL: "https://r", APIKey: "yeni",
				OAuth: json.RawMessage(`{"google":{"enabled":true}}`)},
		},
	})
	m := got.Environments["main"]
	if m.APIKey != "yeni" || m.AppID != "yeni" {
		t.Fatalf("taze değer ezildi: %+v", m)
	}
	if string(m.OAuth) != `{"google":{"enabled":true}}` {
		t.Fatalf("taze OAuth ezildi: %s", m.OAuth)
	}
}

// İLK LİNK'TE BİRLEŞTİRİLECEK BİR ŞEY YOKTUR — ve bozuk bir dosya da yeni hâli
// engellemez.
func TestNothingToMergeIsNotAnError(t *testing.T) {
	fresh := appEnvironments{
		Default:      "main",
		Environments: map[string]appEnvironment{"main": {AppID: "a", BaseURL: "u", APIKey: "k"}},
	}
	if got := mergeWithExisting(t.TempDir(), fresh); len(got.Environments) != 1 {
		t.Fatalf("ilk link bozuldu: %+v", got)
	}

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "palbase-config.json"), []byte("{bozuk"), 0o644)
	if got := mergeWithExisting(dir, fresh); got.Environments["main"].AppID != "a" {
		t.Fatalf("bozuk dosya yeni hâli engelledi: %+v", got)
	}
}

// BİRLEŞTİRME YAZICIYA BAĞLI OLMALI — yalnız var olması yetmez.
//
// Yardımcıyı doğrudan süren testler, çağrısı kaldırıldığında YEŞİL kalıyordu:
// birleştirme "beyan edilmiş ama bağlanmamış" hâle gelir ve dosya yine
// eziliyordu. Bu test yazıcının KENDİSİNİ sürüyor.
func TestTheWriterActuallyMerges(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	dir := filepath.Join(nativeArtifactsDir, "ios")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rich, _ := json.MarshalIndent(appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: "app_gercek", BaseURL: "https://r", APIKey: "pb_KEY",
				OAuth: json.RawMessage(`{"apple":{"enabled":true}}`)},
		},
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "palbase-config.json"), rich, 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// Yığın-URL yolunun ürettiği fakir hâl.
	if _, err := writeAppEnvironments("ios", appEnvironments{
		Default:      "main",
		Environments: map[string]appEnvironment{"main": {BaseURL: "https://r"}},
	}); err != nil {
		t.Fatalf("writeAppEnvironments: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "palbase-config.json"))
	if err != nil {
		t.Fatalf("okuma: %v", err)
	}
	var got appEnvironments
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("çözme: %v", err)
	}
	m := got.Environments["main"]
	if len(m.OAuth) == 0 || m.APIKey != "pb_KEY" || m.AppID != "app_gercek" {
		t.Fatalf("YAZICI birleştirmedi — dosya ezildi: %+v", m)
	}
}

// YER TUTUCU GERÇEK KİMLİĞİ EZMEZ.
//
// Yığın-URL yolu `app_id` alanına `projectAppID` sabitini yazıyor ve bu kod
// tabanının KENDİSİ onu gerçek bir kayıt saymıyor — `native_link.go`
// uygulamanın kaydını ararken onu açıkça atlıyor. Boş olmadığı için ilk
// birleştirme kuralı onu "üretilmiş bir değer" sanıyordu ve centauri'nin
// gerçek `app_eeedc529-…` kimliği `project` ile değişti (ölçüldü 25.08.2026).
func TestThePlaceholderDoesNotOverwriteARealAppID(t *testing.T) {
	dir := t.TempDir()
	blob, _ := json.Marshal(appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: "app_gercek", BaseURL: "https://r", APIKey: "k"},
		},
	})
	_ = os.WriteFile(filepath.Join(dir, "palbase-config.json"), blob, 0o644)

	got := mergeWithExisting(dir, appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: projectAppID, BaseURL: "https://r", APIKey: "k"},
		},
	})
	if got.Environments["main"].AppID != "app_gercek" {
		t.Fatalf("gerçek kimlik yer tutucuyla EZİLDİ: %q", got.Environments["main"].AppID)
	}

	// Ama diskte de yer tutucu varsa, yeni yer tutucu yazılır: korunacak bir
	// gerçek kimlik YOK.
	blob2, _ := json.Marshal(appEnvironments{
		Default:      "main",
		Environments: map[string]appEnvironment{"main": {AppID: projectAppID, BaseURL: "https://r"}},
	})
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "palbase-config.json"), blob2, 0o644)
	got2 := mergeWithExisting(dir2, appEnvironments{
		Default:      "main",
		Environments: map[string]appEnvironment{"main": {AppID: projectAppID, BaseURL: "https://r"}},
	})
	if got2.Environments["main"].AppID != projectAppID {
		t.Fatalf("yer tutucu beklenmedik şekilde değişti: %q", got2.Environments["main"].AppID)
	}
}
