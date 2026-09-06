package auth

import (
	"crypto"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

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
	proof, err := key.NewProof(ProofOptions{
		HTTPMethod: "POST", URL: "https://api.palbase.studio/v1/cloud/projects", AccessToken: "pat_test",
	})
	require.NoError(t, err)
	signed, err := jose.ParseSigned(proof, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	require.Len(t, signed.Signatures, 1)
	public := signed.Signatures[0].Protected.JSONWebKey
	require.NotNil(t, public)
	require.True(t, public.IsPublic(), "a proof must not carry private key material")
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	if got := base64.RawURLEncoding.EncodeToString(thumbprint); got != sdkComputedThumbprint {
		t.Fatalf("parmak izi AYRIŞTI — bilet CLI'da sessizce 401 alır\n  SDK: %s\n  CLI: %s",
			sdkComputedThumbprint, got)
	}
	payload, err := signed.Verify(public.Key)
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	require.Equal(t, "POST", claims["htm"])
	require.Equal(t, accessTokenHash("pat_test"), claims["ath"])
}

func TestMachineKeyNeverFallsBackToAnOldFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	keyFile := filepath.Join(dir, ".palbase", "dpop-key.jwk")
	require.NoError(t, os.MkdirAll(filepath.Dir(keyFile), 0o700))
	require.NoError(t, os.WriteFile(keyFile, []byte(sdkGeneratedJWK), 0o600))
	for _, supplied := range []string{"", "  ", "{broken"} {
		t.Run(supplied, func(t *testing.T) {
			t.Setenv(DPoPKeyEnv, supplied)
			key, err := LoadDPoPKey()
			require.Nil(t, key)
			require.ErrorContains(t, err, DPoPKeyEnv)
			if supplied != "{broken" {
				require.ErrorIs(t, err, ErrDPoPKeyMissing)
			}
		})
	}
}

// A malformed supplied key must fail before a request is sent.
func TestBrokenEnvKeyFailsLoudly(t *testing.T) {
	t.Setenv(DPoPKeyEnv, "{bu jwk degil")
	if _, err := LoadDPoPKey(); err == nil {
		t.Fatal("bozuk ortam anahtarı kabul edildi — CLI başka bir anahtarla imzalardı")
	}
}
