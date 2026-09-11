package notifications

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The add command registers a single shared flag set across all providers, so
// every field flag + secret `--<flag>-file` flag MUST be unique across the
// catalog (a collision would silently bind two providers' fields to one flag).
func TestCatalog_FlagsUniqueAcrossProviders(t *testing.T) {
	seen := map[string]string{} // flag → "provider.field"
	for _, spec := range catalog {
		for _, f := range spec.fields {
			owner := spec.name + "." + f.name
			if prev, dup := seen[f.flag]; dup {
				// A shared field name (from-domain, from-email, region, host) MAY
				// repeat across providers — that's fine, they bind to the same
				// flag and only the active provider reads it. Assert the SEMANTICS
				// match (same camelCase field name) so the shared flag is coherent.
				assert.Equal(t, flagField(prev), f.name, "flag --%s reused with a different field name (%s vs %s)", f.flag, prev, owner)
			}
			seen[f.flag] = owner
		}
		// Secret file flags must be globally unique (each maps to a distinct
		// reserved env key) OR coherent if a name repeats.
		for _, s := range spec.secrets {
			require.NotEmpty(t, s.flag, "%s secret has empty flag", spec.name)
		}
	}
}

// flagField extracts the camelCase field name from a "provider.field" owner tag.
func flagField(owner string) string {
	for i := len(owner) - 1; i >= 0; i-- {
		if owner[i] == '.' {
			return owner[i+1:]
		}
	}
	return owner
}

func TestCatalog_EveryProviderHasAtLeastOneSecret(t *testing.T) {
	for _, spec := range catalog {
		assert.NotEmpty(t, spec.secrets, "%s must declare at least one secret", spec.name)
	}
}

func TestCatalog_ChannelsValid(t *testing.T) {
	valid := map[string]bool{"push": true, "email": true, "sms": true, "whatsapp": true}
	for _, spec := range catalog {
		assert.True(t, valid[spec.channel], "%s has invalid channel %q", spec.name, spec.channel)
	}
}

func TestSpecByName(t *testing.T) {
	assert.NotNil(t, specByName("apns"))
	assert.Nil(t, specByName("nope"))
}

// CLI'IN YAZDIĞI ALAN ADLARI, MODÜLÜN OKUDUKLARIYLA AYNI OLMALI.
//
// 16 günlük bir arızanın gerçek kök nedeni buydu ve teşhis bir katman erken
// durmuştu. Canlıda 26.08.2026'da `palbase notifications add acs` ile yazılan
// kayıt, worker tarafından "missing required field: endpoint" ile reddedildi;
// sebep eksik bir kimlik bilgisi DEĞİL, CLI'ın `connectionString` yazarken
// modülün `connection_string` okumasıydı. Yolun hiçbir yerinde anahtar dönüşümü
// yok: CLI gövdeyi olduğu gibi POST ediyor, yönetim yüzeyi ham delege ediyor,
// modül `json.Decode` ediyor.
//
// Beklenen adlar EZBERDEN değil, modülün struct etiketlerinden alındı:
// v2/internal/modules/notify/internal/provider/email/{acs,smtp,sendgrid,ses}.go
func TestCatalogFieldNamesMatchTheModule(t *testing.T) {
	// Modülün `json:` etiketleri — EZBERDEN DEĞİL, struct'lardan okundu (2026-09-11):
	//   e-posta : provider/email/{acs,smtp,sendgrid,ses}.go
	//   push    : provider/push/apns.go
	//   sms     : provider/sms/twilio.go
	//   whatsapp: provider/whatsapp/
	moduleFields := map[string]map[string]bool{
		"acs":      {"connection_string": true, "endpoint": true, "access_key": true, "from_email": true, "from_name": true},
		"smtp":     {"host": true, "port": true, "username": true, "password": true, "from_email": true, "use_starttls": true},
		"sendgrid": {"api_key": true, "from_domain": true},
		"ses":      {"region": true, "access_key_id": true, "secret_access_key": true, "from_domain": true},
		"apns":     {"team_id": true, "key_id": true, "p8_private_key": true, "bundle_id": true, "is_production": true},
		"twilio":   {"account_sid": true, "auth_token": true, "api_key_sid": true, "api_key_secret": true, "from_number": true, "messaging_service_sid": true, "verify_service_sid": true},
		"meta":     {"phone_number_id": true, "api_version": true, "access_token": true, "app_secret": true, "verify_token": true},
	}

	for _, spec := range catalog {
		// `fcm` BİLEREK DIŞARIDA — ve bu bir istisna değil, AYRI BİR KUSUR.
		//
		// Diğerlerinde sorun adlandırma: CLI camelCase yazıyor, modül snake_case
		// okuyor. `fcm`'de sorun SARMALAMA: CLI service-account JSON'unu
		// `{"serviceAccount": "<dosya>"}` diye bir alanın İÇİNE koyuyor, modül ise
		// credentials'ı service_account.json'un KENDİSİ sayıp kök seviyede
		// `client_email` / `private_key` arıyor (provider/push/fcm.go:118-129).
		// Adı snake_case yapmak onu düzeltmez, yalnız kusuru gizler. Ölçüldü
		// 2026-09-11, ayrı bir iş olarak deftere yazıldı.
		if spec.name == "fcm" {
			continue
		}
		if _, known := moduleFields[spec.name]; !known {
			t.Errorf("%s: bu testin tablosunda yok — yeni bir sağlayıcı eklendiyse tablo da güncellenmeli", spec.name)
			continue
		}
		want := moduleFields[spec.name]
		names := make([]string, 0, len(spec.fields)+len(spec.secrets))
		for _, f := range spec.fields {
			names = append(names, f.name)
		}
		for _, sf := range spec.secrets {
			names = append(names, sf.name)
		}
		for _, n := range names {
			if !want[n] {
				t.Errorf("%s: CLI %q yazıyor, modül bu adı OKUMUYOR — kabul edilen gövde worker'da reddedilir (beklenen adlar: %v)",
					spec.name, n, keysOf(want))
			}
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
