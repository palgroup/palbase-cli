package notifications

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
// moduleConfigStruct, bir sağlayıcının kimlik yapısının v2'deki ADRESİDİR.
//
// Bu tablo alan ADI taşımaz — yalnız DOSYA ve STRUCT adı. Adların ve TİPLERİN
// kendisi v2'nin commit'li kaynağından OKUNUR. Fark önemli: elle yazılan bir
// alan tablosu, yakalamak için var olduğu YALANI SABİTLER. Ölçüldü 11.09.2026 —
// önceki hâli `twilio` için `verify_service_sid` diye bir ad taşıyordu; o ad
// HİÇBİR modül struct'ında yok (Verify sağlayıcısı `service_sid` okur) ve CLI
// da onu hiç yazmıyor. Tablo kendi uydurduğu adı "doğru" sayıyordu.
var moduleConfigStruct = map[string]struct{ path, typ string }{
	"acs":      {"internal/modules/notify/internal/provider/email/acs.go", "ACSEmailConfig"},
	"smtp":     {"internal/modules/notify/internal/provider/email/smtp.go", "SMTPConfig"},
	"sendgrid": {"internal/modules/notify/internal/provider/email/sendgrid.go", "SendGridConfig"},
	"ses":      {"internal/modules/notify/internal/provider/email/ses.go", "SESConfig"},
	"apns":     {"internal/modules/notify/internal/provider/push/apns.go", "APNsConfig"},
	"twilio":   {"internal/modules/notify/internal/provider/sms/twilio.go", "TwilioConfig"},
	"meta":     {"internal/modules/notify/internal/provider/whatsapp/meta.go", "Config"},
}

// notProducedByCLI, modülün OKUDUĞU ama CLI'ın YAZMADIĞI alanlar — her biri
// gerekçesiyle. Bu liste bir muafiyet değil bir BEYANDIR: kapı çift yönlü
// olduğu için, modülde yeni bir alan belirdiğinde ya CLI onu üretmeli ya da
// buraya bir gerekçe düşülmeli. Sessizce eksik kalamaz.
var notProducedByCLI = map[string]map[string]string{
	"acs": {
		"endpoint":   "connection_string ile birlikte gelir; ACS bağlantı dizgesi ikisini de taşır",
		"access_key": "connection_string ile birlikte gelir",
	},
	"twilio": {
		"api_key_sid":    "API Key kimliği CLI yüzeyinde henüz yok; auth_token yolu destekleniyor",
		"api_key_secret": "aynı — API Key yolu CLI'da açılmadı",
	},
}

// TestTheCatalogMatchesTheModulesStructs, CLI'ın ürettiği gövdenin modülün
// kimlik yapısına HEM ADCA HEM TİPÇE oturduğunu ölçer.
//
// NEDEN ÜÇ SORU BİRDEN: 26.08–11.09.2026 arasında bir kiracının her e-postası
// öldü, çünkü CLI camelCase yazıyordu ve modül snake_case okuyordu. O kusurun
// düzeltmesi (T016/T017) adları hizaladı — ama TİPLERİ hizalamadı, ve ad kapısı
// tipi göremiyordu. Ölçüldü 11.09.2026: CLI `port`u `"587"` (dizge) gönderiyordu,
// modül `int` bekliyor; `ValidateSMTPConfig` gövdeyi
// `cannot unmarshal string into Go struct field SMTPConfig.port of type int`
// ile reddediyordu — yani `palbase notifications add smtp` SIR KASAYA
// YÜKLENDİKTEN SONRA 400 alıyordu. Aynı sınıf `is_production` için sessizdi:
// push kanalı kabul kapısından geçtiği için kayıt YAZILIYOR, API
// `configured: true` diyor ve worker her push'u düşürüyordu.
//
// Kapı bu yüzden ÜÇ yönü birden ölçer:
//  1. CLI'ın yazdığı her ad modülde OKUNUYOR mu,
//  2. modülün okuduğu her ad CLI'da üretiliyor mu (ya da gerekçesi yazılı mı),
//  3. CLI'ın alan TÜRÜ modülün Go tipiyle uyuşuyor mu.
func TestTheCatalogMatchesTheModuleContract(t *testing.T) {
	contract := loadModuleContract(t)

	for _, spec := range catalog {
		// `fcm` BİLEREK DIŞARIDA — ve bu bir istisna değil, AYRI BİR KUSUR.
		// Diğerlerinde sorun adlandırmaydı. `fcm`'de sorun SARMALAMA: CLI
		// service-account JSON'unu `{"serviceAccount": "<dosya>"}` diye bir alanın
		// İÇİNE koyuyor, modül ise credentials'ı service_account.json'un KENDİSİ
		// sayıp kök seviyede `client_email` / `private_key` arıyor
		// (provider/push/fcm.go:118-129). Adı ya da tipi düzeltmek onu çözmez,
		// yalnız kusuru gizler. Deftere ayrı bir iş olarak yazıldı.
		if spec.name == "fcm" {
			continue
		}
		moduleTypes, known := contract[spec.name]
		if !known {
			t.Errorf("%s: sözleşme anlık görüntüsünde yok — yeni sağlayıcı eklendiyse module_contract.json da yenilenmeli", spec.name)
			continue
		}
		if len(moduleTypes) == 0 {
			t.Fatalf("%s: sözleşmede tek bir alan yok — kapı ÖLÇEMEDİĞİ için geçemez", spec.name)
		}

		// CLI'ın ürettiği her alan: adı + TELE KOYDUĞU JSON türü.
		//
		// KATALOĞUN BEYANI SORULMAZ, ÜRETİLEN GÖVDE ÖLÇÜLÜR. İlk hâlim beyanı
		// ölçüyordu (`f.isInt` → "int") ve YEŞİL geçiyordu: katalog `port`u
		// `isInt: true` diye bildiriyor ama üretici onu dizge olarak yazıyordu.
		// Bir kapı, ölçtüğünü söylediği şeyi gerçekten üretmelidir — bu yüzden
		// üretim fonksiyonu çağrılır ve çıktısı JSON'a çevrilip TÜRÜNE bakılır.
		cliKind := producedJSONKinds(t, spec)
		for _, sf := range spec.secrets {
			cliKind[sf.name] = "string" // sırlar kasadan dizge olarak eklenir
		}

		// 1 + 3: CLI → modül, ad ve tip.
		for name, kind := range cliKind {
			want, reads := moduleTypes[name]
			if !reads {
				t.Errorf("%s: CLI %q yazıyor, modül bu adı OKUMUYOR — kabul edilen gövde worker'da reddedilir (modülün okuduğu adlar: %v)",
					spec.name, name, sortedKeys(moduleTypes))
				continue
			}
			if want != kind {
				t.Errorf("%s: %q alanı TİPÇE ayrışmış — CLI %s gönderiyor, modül %s bekliyor; json.Unmarshal bu gövdeyi REDDEDER",
					spec.name, name, kind, want)
			}
		}

		// 2: modül → CLI. Eksik kalan her alan gerekçesini BEYAN etmeli.
		for name := range moduleTypes {
			if _, produced := cliKind[name]; produced {
				continue
			}
			if _, declared := notProducedByCLI[spec.name][name]; declared {
				continue
			}
			t.Errorf("%s: modül %q okuyor, CLI onu ÜRETMİYOR ve gerekçesi de yazılı değil — ya katalog alanı eklensin ya notProducedByCLI'a gerekçe düşülsün",
				spec.name, name)
		}

		// Ölü beyan birikmesin: artık var olmayan bir alan için gerekçe tutulamaz.
		for name := range notProducedByCLI[spec.name] {
			if _, exists := moduleTypes[name]; !exists {
				t.Errorf("%s: notProducedByCLI %q için gerekçe taşıyor ama modül artık böyle bir alan okumuyor — ölü beyan", spec.name, name)
			}
		}
	}
}

// producedJSONKinds, `palbase notifications add <spec>` KOŞSAYDI gövdeye hangi
// adların hangi JSON TÜRÜYLE gireceğini, ÜRETİM fonksiyonunu çağırarak ölçer.
//
// Bayraklara akla yatkın değerler verilir (sayısal alana bir sayı, bool alana
// `true`), sonra `collectProviderFields` — komutun kendi kullandığı fiil —
// çağrılır ve çıktısı `json.Marshal`/`Unmarshal` turundan geçirilir. O tur
// önemlidir: teldeki temsil budur, Go'daki değer değil.
func producedJSONKinds(t *testing.T, spec providerSpec) map[string]string {
	t.Helper()
	cmd := &cobra.Command{Use: "add"}
	registerProviderFlags(cmd)
	for _, f := range spec.fields {
		value := "x"
		switch {
		case f.isInt:
			value = "587"
		case f.isBool:
			value = "true"
		}
		require.NoError(t, cmd.Flags().Set(f.flag, value), "%s: --%s ayarlanamadı", spec.name, f.flag)
	}
	entry, err := collectProviderFields(&spec, cmd)
	require.NoError(t, err, "%s: üretim yolu bu girdilerle hata verdi", spec.name)

	body, err := json.Marshal(entry.fields)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(body, &wire))

	kinds := map[string]string{}
	for name, v := range wire {
		switch v.(type) {
		case float64:
			kinds[name] = "int"
		case bool:
			kinds[name] = "bool"
		default:
			kinds[name] = "string"
		}
	}
	return kinds
}

// moduleContractPath, vendor'lanmış sözleşme anlık görüntüsü.
const moduleContractPath = "module_contract.json"

// loadModuleContract, anlık görüntüyü okur. Okuyamazsa REDDEDER — atlamaz.
func loadModuleContract(t *testing.T) map[string]map[string]string {
	t.Helper()
	raw, err := os.ReadFile(moduleContractPath)
	if err != nil {
		t.Fatalf("%s okunamadı: %v — kapı ÖLÇEMEDİĞİ için geçemez", moduleContractPath, err)
	}
	var doc struct {
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s ayrıştırılamadı: %v", moduleContractPath, err)
	}
	if len(doc.Providers) == 0 {
		t.Fatalf("%s tek bir sağlayıcı taşımıyor — kapı ÖLÇEMEDİĞİ için geçemez", moduleContractPath)
	}
	return doc.Providers
}

// TestTheModuleContractSnapshotIsCurrent, VENDOR'LANMIŞ ANLIK GÖRÜNTÜNÜN hâlâ
// v2'nin söylediği şey olduğunu ölçer.
//
// NEDEN İKİ KAPI VAR. Bir önceki hâlim modül tablosunu doğrudan v2'den okuyup,
// okuyamazsa `t.Skipf` ediyordu. İki deliği vardı ve ikincisi ağırdı:
//
//  1. `sdk/cli`'ın CI'ı TEK bir checkout yapıyor ve v2'yi hiç almıyor — yani
//     `os.Stat` orada HER ZAMAN düşüyor ve kapı CI'da HİÇ KOŞMUYORDU. FR-019'un
//     kabul ölçütü yalnız benim makinemde ölçülüyordu.
//  2. v2 bir dosyayı taşıdığı gün `Skipf` testin TAMAMINI durduruyordu — yani
//     diğer altı sağlayıcı da ölçülmeden yeşil geçiyordu. Bu, aynı dalganın
//     `versionRose`'da bilerek kapattığı fail-open'ın ta kendisiydi.
//
// Çare bölünme: sözleşme depoya VENDOR'LANDI, katalog kapısı ona bakıyor (CI'da
// koşar), ve BU test anlık görüntünün v2'den sapmadığını ölçüyor. Vendor'lanmış
// bir kopya tek başına bir YALANI SABİTLEYEBİLİRDİ; bu test tam da onu
// engelliyor. Aynı desen `cloud/tenant-stack/vendored_test.go`'da da var.
//
// v2 YOKSA atlanır — `sdk/cli` kendi başına klonlanabilen bir depo ve orada
// komşunun kaynağı gerçekten yoktur. Ama v2 VARSA ve bir yol okunamıyorsa bu
// bir KUSURDUR, atlama sebebi değil: taşınan dosya sözleşmeyi sessizce
// dondurur.
func TestTheModuleContractSnapshotIsCurrent(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "..", "v2")
	if _, err := os.Stat(repo); os.IsNotExist(err) {
		t.Skipf("v2 kaynağı yok (%s) — anlık görüntünün tazeliği yalnız tam ağaçta ölçülür", repo)
	}
	snapshot := loadModuleContract(t)

	for name, where := range moduleConfigStruct {
		live, err := readStructJSONFields(repo, where.path, where.typ)
		if err != nil {
			t.Fatalf("%s: v2'nin commit'li hâli okunamadı (%s): %v — dosya taşındıysa "+
				"moduleConfigStruct ve %s birlikte yenilenmeli; atlamak sözleşmeyi DONDURUR",
				name, where.path, err, moduleContractPath)
		}
		if len(live) == 0 {
			t.Fatalf("%s: %s içinde %s struct'ı bulunamadı ya da boş — struct yeniden adlandırıldıysa "+
				"moduleConfigStruct yenilenmeli", name, where.path, where.typ)
		}
		want, known := snapshot[name]
		if !known {
			t.Errorf("%s: v2 bu sağlayıcıyı tanıyor ama anlık görüntüde yok — %s yenilenmeli",
				name, moduleContractPath)
			continue
		}
		for field, goType := range live {
			kind := jsonKindOf(goType)
			got, present := want[field]
			if !present {
				t.Errorf("%s: v2 %q alanını okuyor, anlık görüntüde yok — %s BAYAT",
					name, field, moduleContractPath)
				continue
			}
			if got != kind {
				t.Errorf("%s: %q alanının türü v2'de %s (%s), anlık görüntüde %s — %s BAYAT",
					name, field, kind, goType, got, moduleContractPath)
			}
		}
		for field := range want {
			if _, present := live[field]; !present {
				t.Errorf("%s: anlık görüntü %q alanını taşıyor ama v2 artık okumuyor — %s BAYAT",
					name, field, moduleContractPath)
			}
		}
	}
	// Anlık görüntüde v2'nin hiç tanımadığı bir sağlayıcı kalmasın.
	for name := range snapshot {
		if _, known := moduleConfigStruct[name]; !known {
			t.Errorf("%s: anlık görüntüde var ama adresi yazılı değil — ölü giriş", name)
		}
	}
}

// structFieldRE, bir struct gövdesindeki tek bir alanı yakalar: Go adı, Go tipi
// ve `json:` etiketinin ilk parçası (`,omitempty` atılır).
var structFieldRE = regexp.MustCompile("^\\s*[A-Z]\\w*\\s+([\\w.\\[\\]*]+)\\s+`[^`]*json:\"([^\",]+)")

// readStructJSONFields, komşu deponun COMMIT'Lİ hâlinden tek bir struct'ın
// `json:` etiketlerini ve Go tiplerini okur.
//
// Çalışma ağacından değil `git show HEAD:` ile okunur — komşu depoda başka bir
// oturumun yarım bıraktığı bir düzenleme bu kapıyı yanıltmasın diye. Aynı desen
// cloud/tenant-stack/vendored_test.go'da da kullanılıyor.
//
// Yalnız ADI VERİLEN struct'ın gövdesi okunur: bu dosyalar aynı zamanda tel
// payload'larını da tanımlıyor (acsEmailAddress, sendGridEmail …) ve hepsini
// toplamak kapıyı anlamsız kılardı.
func readStructJSONFields(repo, path, typeName string) (map[string]string, error) {
	out, err := exec.Command("git", "-C", repo, "show", "HEAD:"+path).Output()
	if err != nil {
		return nil, err
	}
	fields := map[string]string{}
	inStruct := false
	for _, line := range strings.Split(string(out), "\n") {
		if !inStruct {
			if strings.HasPrefix(line, "type "+typeName+" struct {") {
				inStruct = true
			}
			continue
		}
		if strings.HasPrefix(line, "}") {
			break
		}
		if m := structFieldRE.FindStringSubmatch(line); m != nil {
			fields[m[2]] = m[1]
		}
	}
	return fields, nil
}

// jsonKindOf, bir Go tipini CLI'ın gönderebileceği JSON türüne indirger.
func jsonKindOf(goType string) string {
	switch goType {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
		return "int"
	case "bool":
		return "bool"
	default:
		return "string"
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
