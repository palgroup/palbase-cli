package auth

import "testing"

// SDK İLE CLI AYNI ANAHTARI OKUMALI — yoksa bilet sessizce 401 alır.
//
// Bir deploy bileti, onu BASAN tarafın anahtarının parmak izine bağlanıyor.
// palcore'un backend'i bileti `@palbase/account` ile basıyor ve `palbase`
// CLI'ını ayrı bir süreç olarak koşturuyor. İki taraf anahtarı farklı
// biçimlerde saklasaydı ya da parmak izini farklı hesaplasaydı, CLI'ın
// imzaladığı proof biletin bağlı olduğu anahtarla EŞLEŞMEZDİ — ve sonuç,
// hiçbir log satırının açıklamadığı bir 401 olurdu. Tam olarak canlıya
// çıkarken.
//
// FİKSTÜRÜN KAYNAĞI: bu JWK ve parmak izi UYDURULMADI — `@palbase/account`'un
// kendi generateKey() + loadKey().thumbprint çıktısı (25.08.2026). Yani bu
// test bir literal'i değil, KARŞI TARAFIN ÜRETTİĞİNİ doğruluyor.
const (
	sdkGeneratedJWK       = `{"kty": "EC", "crv": "P-256", "x": "pjhYVfeidn2-Th2nidFmFPlcadjcZzOwgyK7s2akp6o", "y": "Nfo7aGcfWC8lUHweTF6Rm9UttcR6l9ZjLBJQqAw0mR0", "d": "VwP-M-GESgG8mf3LbvWk0wuaGcKjd_V-9jbDXo8lqj0"}`
	sdkComputedThumbprint = "FeXTT71vBCn_MoPU38xmXyUbOIIO8QTB40ugEtfabW4"
)

func TestSDKKeyMaterialLoadsAndAgreesOnThumbprint(t *testing.T) {
	t.Setenv(DPoPKeyEnv, sdkGeneratedJWK)

	key, err := LoadDPoPKey()
	if err != nil {
		t.Fatalf("SDK'nın ürettiği anahtar CLI'da OKUNAMADI: %v", err)
	}
	if got := key.Thumbprint(); got != sdkComputedThumbprint {
		t.Fatalf("parmak izi AYRIŞTI — bilet CLI'da sessizce 401 alır\n  SDK: %s\n  CLI: %s",
			sdkComputedThumbprint, got)
	}
}

// Ortamdaki anahtar KEYRING'İN ÖNÜNDE okunur; aksi hâlde CLI, çağıranın
// VERDİĞİ anahtarı yok sayıp kendi keyring'indeki başkasıyla imzalardı.
func TestEnvKeyWinsOverKeyring(t *testing.T) {
	t.Setenv(DPoPKeyEnv, sdkGeneratedJWK)
	key, err := LoadDPoPKey()
	if err != nil {
		t.Fatalf("ortam anahtarı okunamadı: %v", err)
	}
	if key.Thumbprint() != sdkComputedThumbprint {
		t.Fatal("keyring ortam anahtarını EZDİ")
	}
}

// Bozuk bir ortam anahtarı SESLİ düşer, keyring'e sessizce KAYMAZ.
func TestBrokenEnvKeyFailsLoudly(t *testing.T) {
	t.Setenv(DPoPKeyEnv, "{bu jwk degil")
	if _, err := LoadDPoPKey(); err == nil {
		t.Fatal("bozuk ortam anahtarı kabul edildi — CLI başka bir anahtarla imzalardı")
	}
}
