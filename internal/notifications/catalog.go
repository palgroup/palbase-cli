package notifications

import "strings"

// The provider catalog defines CLI inputs and their management API field names.
// It also derives the reserved vault keys used by the provider setup flow.

// reservedSecretPrefix is the env-var namespace that backs provider secrets.
// A `palbase secret set` of a key under this prefix is refused (see secret-guard).
const reservedSecretPrefix = "PB_NOTIFICATIONS"

// field is one non-secret config field an author supplies for a provider.
type field struct {
	// name is the credential field accepted by this provider's backend API.
	name string
	// flag is the CLI flag (kebab-case) that sets it, e.g. "team-id".
	flag string
	// required marks a field that must be supplied for an enabled provider.
	required bool
	// isInt marks a numeric CLI input (port).
	isInt bool
	// isBool marks a boolean field (is_production, use_starttls).
	isBool bool
	// help is the flag's one-line usage string.
	help string
}

// secretField is one secret an author supplies via a file/prompt; it is uploaded
// to the reserved vault key and sent in the encrypted provider configuration.
type secretField struct {
	// name is the credential field, also used to derive the reserved vault key.
	name string
	// flag is the `--<flag>-file` flag that points at the secret's file.
	flag string
	// prompt, when true, lets the value be entered interactively (hidden) if no
	// file is given (e.g. Twilio auth token). When false the file is required.
	prompt bool
	// help describes the secret for prompts/usage.
	help string
}

// providerSpec is one provider's catalog entry.
type providerSpec struct {
	name    string // provider key (apns, fcm, sendgrid, …)
	channel string // push | email | sms | whatsapp
	fields  []field
	secrets []secretField
}

// catalog groups providers by channel so
// `notifications providers` prints a stable, channel-grouped view.
var catalog = []providerSpec{
	{
		name:    "meta",
		channel: "whatsapp",
		fields: []field{
			{name: "phone_number_id", flag: "phone-number-id", required: true, help: "Meta WhatsApp phone number ID"},
			{name: "api_version", flag: "api-version", help: "Meta Graph API version (default v26.0)"},
		},
		secrets: []secretField{
			{name: "access_token", flag: "access-token", prompt: true, help: "Meta system user access token"},
			{name: "app_secret", flag: "app-secret", prompt: true, help: "Meta app secret for webhook signature verification"},
			{name: "verify_token", flag: "verify-token", prompt: true, help: "webhook verification token chosen by you"},
		},
	},
	{
		name:    "apns",
		channel: "push",
		fields: []field{
			{name: "team_id", flag: "team-id", required: true, help: "Apple Developer Team ID"},
			{name: "key_id", flag: "key-id", required: true, help: "APNs auth key ID"},
			{name: "bundle_id", flag: "bundle-id", required: true, help: "app bundle identifier (e.g. com.acme.app)"},
			{name: "is_production", flag: "production", isBool: true, help: "use the APNs production gateway (default true)"},
		},
		secrets: []secretField{
			// ‼️ BU AD KASA ANAHTARINI KAYDIRIR — diğer sekiz hizalama kaydırmaz.
			// `reservedSecretKey` adı `camelToUpperSnake`'ten geçiriyor ve o, camelCase
			// ile snake_case'i AYNI çıktıya veriyor (`teamId` ve `team_id` → `TEAM_ID`).
			// `p8` ise farklı: eski anahtar `..._APNS_P8`, yenisi `..._APNS_P8_PRIVATE_KEY`.
			// Modül `p8_private_key` okuduğu için ad değişmek ZORUNDA; daha önce
			// `palbase notifications add apns` koşmuş bir kurulum sırrı yeni anahtarla
			// yeniden yüklemelidir. Ölçüldü ve deftere yazıldı 2026-09-11.
			{name: "p8_private_key", flag: "p8", help: "APNs .p8 auth key file"},
		},
	},
	{
		name:    "fcm",
		channel: "push",
		fields:  nil,
		secrets: []secretField{
			{name: "serviceAccount", flag: "service-account", help: "Firebase service-account JSON file"},
		},
	},
	{
		name:    "sendgrid",
		channel: "email",
		fields: []field{
			{name: "from_domain", flag: "from-domain", required: true, help: "verified sender domain"},
		},
		secrets: []secretField{
			{name: "api_key", flag: "api-key", prompt: true, help: "SendGrid API key"},
		},
	},
	{
		name:    "ses",
		channel: "email",
		fields: []field{
			{name: "region", flag: "region", required: true, help: "AWS region (e.g. us-east-1)"},
			{name: "access_key_id", flag: "access-key-id", required: true, help: "AWS access key ID"},
			{name: "from_domain", flag: "from-domain", required: true, help: "verified sender domain"},
		},
		secrets: []secretField{
			{name: "secret_access_key", flag: "secret-access-key", prompt: true, help: "AWS secret access key"},
		},
	},
	{
		name:    "smtp",
		channel: "email",
		fields: []field{
			{name: "host", flag: "host", required: true, help: "SMTP server host"},
			{name: "port", flag: "port", required: true, isInt: true, help: "SMTP server port"},
			{name: "from_email", flag: "from-email", required: true, help: "sender email address"},
			{name: "username", flag: "username", help: "SMTP username (optional)"},
			{name: "use_starttls", flag: "starttls", isBool: true, help: "use STARTTLS (optional)"},
		},
		secrets: []secretField{
			{name: "password", flag: "password", prompt: true, help: "SMTP password"},
		},
	},
	{
		name:    "acs",
		channel: "email",
		fields: []field{
			{name: "from_email", flag: "from-email", required: true, help: "sender email address"},
			{name: "from_name", flag: "from-name", help: "sender display name (optional)"},
		},
		secrets: []secretField{
			{name: "connection_string", flag: "connection-string", prompt: true, help: "Azure Communication Services connection string"},
		},
	},
	{
		name:    "twilio",
		channel: "sms",
		fields: []field{
			{name: "account_sid", flag: "account-sid", required: true, help: "Twilio Account SID"},
			{name: "from_number", flag: "from-number", help: "sender phone number (one of from-number / messaging-sid)"},
			{name: "messaging_service_sid", flag: "messaging-sid", help: "Twilio Messaging Service SID (one of from-number / messaging-sid)"},
		},
		secrets: []secretField{
			{name: "auth_token", flag: "auth-token", prompt: true, help: "Twilio auth token"},
		},
	},
}

// specByName returns the catalog entry for a provider, or nil if unknown.
func specByName(name string) *providerSpec {
	for i := range catalog {
		if catalog[i].name == name {
			return &catalog[i]
		}
	}
	return nil
}

// supersededSecretKeys, bir sağlayıcının ARTIK OKUNMAYAN kasa anahtarlarıdır.
//
// Bir alan adını hizalamak, o adın DAHA ÖNCE ÜRETTİĞİ kaydı kaldırmaz. `p8` →
// `p8_private_key` hizalaması (11.09.2026) kasa anahtarını da kaydırdı, çünkü
// anahtar alan adından türetiliyor. Daha önce `palbase notifications add apns`
// koşmuş bir projenin kasasında `PB_NOTIFICATIONS_APNS_P8` altında bir APNs
// ÖZEL ANAHTARI duruyor ve artık hiçbir şey onu okumuyor.
//
// Bu liste onu SİLMEZ — canlı bir sırrı bir CLI komutunun yan etkisi olarak
// yok etmek, sahibinin haberi olmadan alınmış bir karardır. Yaptığı şey onu
// ADLANDIRMAK: sessiz bırakmak da bir karar olurdu ve sır hijyeni açısından
// daha kötüsü.
// supersededSecret, emekli bir kasa anahtarı ve onun YERİNİ ALAN alan adı.
//
// İkisi BİRLİKTE döner, çünkü uyarı ikisini de yazıyor ve ayrı tutulduklarında
// ayrışırlar: ilk hâlim anahtarları listeden, yerine geçen adı ise ÇAĞRI
// YERİNDEKİ sabit bir dizgeden alıyordu (`"p8_private_key"`). Bugün doğruydu
// — liste tek girdiliydi — ama ikinci bir sağlayıcı eklendiği gün kullanıcı,
// kendi kasasında duran bir özel anahtarın yerine bakmak için BAŞKA bir
// sağlayıcının alan adını okuyacaktı.
type supersededSecret struct {
	key        string // artık okunmayan kasa anahtarı
	replacedBy string // yerini alan credential alanı
}

func supersededSecretKeys(provider string) []supersededSecret {
	switch provider {
	case "apns":
		return []supersededSecret{{key: reservedSecretPrefix + "_APNS_P8", replacedBy: "p8_private_key"}}
	default:
		return nil
	}
}

// reservedSecretKey derives the env-var key backing a provider's secret field,
// e.g. ("apns","p8_private_key") → "PB_NOTIFICATIONS_APNS_P8_PRIVATE_KEY".
//
// ÖRNEK 11.09.2026'da DÜZELTİLDİ ve bu bir üslup düzeltmesi değil: eski örnek
// ("apns","p8") artık katalogda BULUNMAYAN bir alanı adlandırıyordu ve
// ürettiği anahtar da yanlıştı. Alan adı `p8` → `p8_private_key` olarak
// hizalandığında (modül `p8_private_key` okuyor) kasa anahtarı da KAYDI —
// diğer sekiz hizalamada kaymamıştı, çünkü camelToUpperSnake `teamId` ile
// `team_id`'yi aynı çıktıya veriyor. Bu tekil istisna yukarıda `p8_private_key`
// girdisinin yanında ayrıca uyarılıyor; okuyucunun İKİ yerde aynı şeyi
// görmesi bilerek.
func reservedSecretKey(provider, secretField string) string {
	return reservedSecretPrefix + "_" + camelToUpperSnake(provider) + "_" + camelToUpperSnake(secretField)
}

// camelToUpperSnake turns `serviceAccount` → `SERVICE_ACCOUNT`.
func camelToUpperSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := s[i-1]
			if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
				b.WriteByte('_')
			}
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}
