// Package notifications configures senders and templates through the linked
// project's management API. Sender credentials are stored in its secret vault.
package notifications

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Resolvers carries the linked project's transport.
type Resolvers struct {
	REST func(*cobra.Command) (REST, error)
}

// providerEntry holds a sender's non-secret fields for validation.
//
// Değerler `any` — çünkü modülün kimlik yapıları `int` ve `bool` alanlar da
// okuyor ve bu harita doğrudan gövdeye kopyalanıyor. Bkz. collectProviderFields.
type providerEntry struct {
	fields map[string]any
}

// REST reaches the linked stack's management surface.
type REST interface {
	Do(ctx context.Context, method, path string, body []byte) (int, []byte, error)
}

const providersPath = "/v1/management/notifications/providers"

// templatesPath is the whole template set, replaced in one write. The route is
// a PUT of the COMPLETE document rather than per-key edits: templates are
// authored as a set (an e-mail and its SMS twin change together), and a partial
// door would let the two drift.
const templatesPath = "/v1/management/notifications/templates"

// secretPath is where a provider's credential is written: the project's own
// vault, the same door `palbase secret set` uses.
func secretPath(name string) string {
	return "/v1/management/secrets/" + url.PathEscape(name)
}

func call(r Resolvers, cmd *cobra.Command, method, path string, body []byte) ([]byte, error) {
	rest, err := r.REST(cmd)
	if err != nil {
		return nil, err
	}
	status, raw, err := rest.Do(cmd.Context(), method, path, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Description != "" {
			return nil, fmt.Errorf("%s: %s", e.Error, e.Description)
		}
		return nil, fmt.Errorf("the stack answered %d: %s", status, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "Manage this project's notification senders",
		Long: `The push/email/SMS/WhatsApp senders this project delivers through.

  palbase notifications providers                 Show the catalog and what is configured.
  palbase notifications add <provider> [flags]    Configure a sender.
  palbase notifications status --channel whatsapp Check the sender configuration.
  palbase notifications remove <provider>         Stop delivering through one.
  palbase notifications templates list            Show the templates this stack holds.
  palbase notifications templates set --file F    Apply a template document.

Configuration is stored on the linked backend and used by subsequent sends.
Provider credentials are encrypted; listing shows configuration status without
returning secrets. Message content is managed separately through templates.`,
	}
	cmd.AddCommand(providersCmd(r), addCmd(r), removeCmd(r), templatesCmd(r), statusCmd(r))
	return cmd
}

// providersCmd lists the catalog + which providers are configured in config.
func providersCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "Show the catalog and what this stack has configured",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := call(r, cmd, http.MethodGet, providersPath, nil)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			configured := map[string]bool{}
			var live []struct {
				Provider string `json:"provider"`
				Name     string `json:"name"`
			}
			// AN ANSWER THIS COMMAND CANNOT READ IS NOT AN EMPTY STACK.
			//
			// This was `if Unmarshal(...) == nil`, which dropped the error: any
			// answer of another shape — an error envelope, a wrapped list — left
			// the map empty and printed every sender as NOT configured. Same
			// silent wrong answer FR-012 removed from `flags list`, same file,
			// one function away.
			if err := json.Unmarshal(raw, &live); err != nil {
				return fmt.Errorf("this stack answered something `notifications providers` cannot read: %s",
					strings.TrimSpace(string(raw)))
			}
			for _, p := range live {
				key := p.Provider
				if key == "" {
					key = p.Name
				}
				configured[key] = true
			}
			fmt.Fprintln(out, "notification senders (● configured on this stack):")
			fmt.Fprintln(out)
			for _, spec := range catalog {
				mark := " "
				if configured[spec.name] {
					mark = "●"
				}
				fmt.Fprintf(out, "  %s %-18s %s\n", mark, spec.name, spec.channel)
			}
			return nil
		},
	}
}

// addCmd configures a single provider: it (1) uploads the provider's secret(s)
// to the project's own vault, then (2) sends the provider's non-secret fields to
// the stack. No file, no deploy — config/notifications.ts stopped being read at
// the v2 cutover (see the Resolvers comment above).
func addCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "add <provider> [flags]",
		Aliases: []string{"configure"},
		Short:   "Configure or rotate a provider on the linked backend",
		Long: `Configure a notification provider. The provider's NON-SECRET fields are passed
as flags and sent straight to the Environment — live, with no deploy in between;
its SECRET (cert/key/api-key) is read from a file (or prompted, hidden) and
uploaded to a reserved encrypted vault key (PB_NOTIFICATIONS_<PROVIDER>_<FIELD>)
— never written to git.

Examples:
  palbase notifications add apns --team-id T --key-id K --bundle-id com.acme.app --p8-file AuthKey.p8
  palbase notifications add fcm --service-account-file service-account.json
  palbase notifications add sendgrid --from-domain mail.acme.com            (prompts for API key)
  palbase notifications add twilio --account-sid AC.. --messaging-sid MG..  (prompts for auth token)
  palbase notifications configure meta --phone-number-id 123456

Meta prompts for the access token, app secret and webhook verify token, or reads
them from --access-token-file, --app-secret-file and --verify-token-file.
This configures the linked backend's connection. When that backend is the cloud
control plane, its relay uses this sender for customer projects. Re-run with the
complete connection configuration to rotate it. Content is managed separately
with notifications templates --channel whatsapp; the message's locale selects
a translation. Palnotify ships its standard verification definitions.

Run ` + "`palbase notifications providers`" + ` to see every provider's flags.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			spec := specByName(name)
			if spec == nil {
				return fmt.Errorf("unknown provider %q — run `palbase notifications providers` to list them", name)
			}

			// 1. Collect non-secret fields from flags; validate required ones.
			entry, cerr := collectProviderFields(spec, cmd)
			if cerr != nil {
				return cerr
			}
			if verr := validateProvider(spec, entry); verr != nil {
				return verr
			}
			// Pin the linked backend and credential for this whole operation.
			// Re-reading the target between secret writes could split a setup
			// across backends if another process changes the link concurrently.
			rest, err := r.REST(cmd)
			if err != nil {
				return err
			}
			r := Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }}

			// Collect every secret before the first write. A missing final file
			// must not leave half of a credential rotation uploaded.
			out := cmd.OutOrStdout()
			secretValues := map[string]string{}
			for _, s := range spec.secrets {
				value, serr := resolveSecretValue(cmd, name, s)
				if serr != nil {
					return serr
				}
				if name == "meta" {
					value = strings.TrimSpace(value)
					if value == "" || strings.ContainsAny(value, "\r\n\t ") {
						return fmt.Errorf("--%s must be a single non-empty token", s.flag)
					}
				}
				// KİMLİĞİ SIRRIN KENDİSİ OLAN SAĞLAYICIDA, DOSYA BURADA
				// REDDEDİLİR — kasaya yazılmadan ÖNCE.
				//
				// Sıra bir ayrıntı değil: 11.09.2026'da `smtp`'de tam tersi
				// yaşandı. Kabul kapısı kaydı reddetti ama komut oraya sırrı
				// kasaya YÜKLEDİKTEN sonra varmıştı; kullanıcıda parola kasada,
				// sağlayıcı yok.
				//
				// BU, MODÜLÜN KURALININ İKİNCİ BİR KOPYASI DEĞİL. Belgenin
				// tür ayracını Google tanımlıyor; kök alan listesi ise
				// `module_contract.json`'dan, yani v2'nin kendi struct'ından
				// türetilmiş ve tazeliği ayrı bir kapıyla ölçülen anlık
				// görüntüden geliyor. Elle yazılmış bir alan listesi burada
				// olsaydı, iki kopyanın sessizce ayrışması bu koşunun kapattığı
				// sınıfın ta kendisi olurdu.
				if spec.credentialsAreTheSecret {
					if verr := verifySecretDocument(s, spec.name, value); verr != nil {
						return verr
					}
				}
				secretValues[s.name] = value
			}
			// 2. Upload credentials, then configure the sender. The existing
			// encrypted provider row stays active until the final upsert succeeds.
			for _, s := range spec.secrets {
				value := secretValues[s.name]
				reserved := reservedSecretKey(name, s.name)
				// THE PROJECT'S OWN VAULT, through the door `palbase secret set`
				// uses. It used to be an `env.set` mutation on the Studio, which
				// held a second copy of every secret and handed it to the stack at
				// deploy time; the stack owns them now, so writing anywhere else
				// would put a value where nothing reads it.
				secretBody, merr := json.Marshal(map[string]string{"value": value})
				if merr != nil {
					return merr
				}
				if _, uerr := call(r, cmd, http.MethodPut,
					secretPath(reserved), secretBody); uerr != nil {
					return fmt.Errorf("upload secret %s: %w", reserved, redactProviderError(uerr, secretValues))
				}
				fmt.Fprintf(out, "✓ uploaded secret %s (encrypted)\n", reserved)
			}

			// Hizalama bir ANAHTAR bıraktıysa, onu adıyla söyle.
			// Silmiyoruz: canlı bir sırrı komutun yan etkisi olarak yok etmek
			// sahibinin kararı olmalı. Ama sessiz bırakmak da bir karardır ve
			// kasada okunmayan bir özel anahtar bırakır.
			for _, stale := range supersededSecretKeys(name) {
				fmt.Fprintf(out, "! %s is no longer read (the field was aligned to %s).\n"+
					"  It may still hold an old private key. To check and remove it:\n"+
					"    palbase secret remove %s\n", stale.key, stale.replacedBy, stale.key)
			}

			// 3. Tell the stack. No file, no deploy to wait for.
			//
			// GÖVDE MODÜLÜN SÖZLEŞMESİDİR: {channel, provider, credentials}.
			// Eskiden {provider, config} gönderiliyordu ve modül bunu
			// `bad_request: invalid channel:` ile REDDEDİYORDU — yani bu komut
			// sırrı yükleyip config'i YAZAMADAN bitiyordu, her sağlayıcı için.
			// Ölçüldü canlı 26.08.2026 (`palbase notifications add acs`).
			//
			// KİMLİK GÖVDEDE GİDER, ve bu bir gerileme değil: ayrılmış env
			// anahtarını (`PB_NOTIFICATIONS_*`) okuyup sağlayıcı yapılandıran
			// mekanizma v1'in "br-pod apply" adımıydı ve v2'de YOK — SDK'nın
			// kendi yorumu da onu "resolved at deploy from control-pg" diye
			// tarif ediyor. Yani sırrı yalnız kasaya koymak, onu HİÇBİR ŞEYİN
			// okumadığı bir yere koymaktı. Modül `credentials`'ı ŞİFRELİ saklar
			// ve geri okutmaz; sır git'e yine girmez.
			creds, cerr := buildCredentials(spec, entry, secretValues)
			if cerr != nil {
				return cerr
			}
			body, err := json.Marshal(map[string]any{
				"channel":     spec.channel,
				"provider":    name,
				"credentials": creds,
			})
			if err != nil {
				return err
			}
			if _, err := call(r, cmd, http.MethodPost, providersPath, body); err != nil {
				return redactProviderError(err, secretValues)
			}
			fmt.Fprintf(out, "✓ provider %q configured on the linked backend\n", name)
			if name == "meta" {
				fmt.Fprintln(out, "Meta callback: <this backend's public URL>/v1/notifications/webhooks/whatsapp/meta")
				fmt.Fprintln(out, "Use the same verify token in Meta and subscribe the app to the WABA's messages events.")
				fmt.Fprintln(out, "Configuration saved; template approval and live delivery are not verified by this command.")
			}
			return nil
		},
	}
	// Register a flag for every catalog field + every secret file across all
	// providers. cobra ignores flags a given provider doesn't use; the RunE only
	// reads the ones in that provider's spec.
	registerProviderFlags(cmd)
	return cmd
}

// verifySecretDocument, kimliği sırrın kendisi olan bir sağlayıcının dosyasını
// KASAYA YAZILMADAN ÖNCE reddeder.
//
// İKİ ŞEY ölçüyor ve ikisi de kasa PUT'undan önce:
//
//  1. belgenin KENDİ tür ayracı (`type`), ve
//  2. modülün o sağlayıcı için beyan ettiği KÖK ALANLARIN hepsinin var ve boş
//     olmadığı.
//
// İkincisi 12.09.2026'da eklendi ve sebebi ölçülmüş bir açıktı: yalnız `type`e
// bakan hâl, elle kırpılmış bir servis hesabı dosyasını
// (`{"type":"service_account","project_id":"p","client_email":"e"}` — `private_key`
// YOK) GEÇİRİYORDU. Akış şuydu: kapı geçer → sır KASAYA YAZILIR → sağlayıcı
// POST'u modülden 400 alır → kullanıcının kasasında YETİM bir sır kalır ve
// sağlayıcı yoktur. Bu, 11.09'da smtp'de birebir yaşanan dizidir ve bu fiilin
// var olma sebebidir.
//
// KURAL İKİNCİ KEZ YAZILMIYOR. Alan listesi `module_contract.json`'dan geliyor —
// v2'nin config struct'ından türetilmiş, depoya vendor'lanmış ve tazeliği
// `TestTheModuleContractSnapshotIsCurrent` tarafından v2'nin HEAD'ine karşı
// ölçülen anlık görüntü. Burada elle bir alan listesi tutmak, bu koşunun
// kapattığı sınıfın kendisi olurdu.
//
// SIKILIK SINIRI, açıkça: sözleşme config struct'ının TÜM alanlarını kaydediyor,
// "zorunlu olanları" değil. Bu yüzden kontrol yalnız `credentialsAreTheSecret`
// sağlayıcılarına uygulanıyor — orada belge KİMLİĞİN KENDİSİ ve bugünkü tek
// örneği olan `fcm` için modülün doğrulayıcısı dört alanın DÖRDÜNÜ de istiyor
// (`ValidateFCMConfig`). İleride bu dala isteğe bağlı alanı olan bir sağlayıcı
// girerse, bu kontrol sunucudan KATI olur ve çalışan bir kimliği reddeder;
// o gün sözleşmenin zorunluluğu da taşıması gerekir. Bu cümle o günün uyarısıdır.
func verifySecretDocument(s secretField, provider string, raw string) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return fmt.Errorf("--%s-file is not valid JSON: %w", s.flag, err)
	}
	var typ string
	if v, ok := doc["type"]; ok {
		_ = json.Unmarshal(v, &typ)
	}
	if typ != "service_account" {
		return fmt.Errorf("--%s-file is not a service-account document: its %q field is %q, "+
			"expected \"service_account\" — download the JSON key from the Firebase console "+
			"(Project settings → Service accounts → Generate new private key)",
			s.flag, "type", typ)
	}
	for _, field := range contractFields(provider) {
		v, ok := doc[field]
		if !ok {
			return fmt.Errorf("--%s-file is missing %q — nothing was written to the vault. "+
				"A service-account file the stack cannot send with is worse than no file at "+
				"all: the credential would sit in the vault under a provider that does not "+
				"exist. Download the key again from the Firebase console (Project settings → "+
				"Service accounts → Generate new private key)", s.flag, field)
		}
		// DİZGE OLMAYAN DEĞER DE REDDEDİLİR, ve bu sunucudan KATI değil: modül
		// belgeyi kendi struct'ına ayrıştırıyor ve `"private_key": 123` orada
		// `invalid service account JSON` ile 400 alıyor. Bu kontrol olmadan CLI
		// onu GEÇİRİYORDU (`Unmarshal` hata verir, boşluk kontrolü atlanır) —
		// yani sır kasaya yazılıyor, sağlayıcı POST'u 400 alıyor ve kullanıcıda
		// yetim bir sır kalıyordu. Tam da bu fiilin engellemek için var olduğu
		// dizi, bir kenarda hayatta kalmış hâliyle.
		var str string
		if err := json.Unmarshal(v, &str); err != nil {
			return fmt.Errorf("--%s-file has a non-string %q — a service-account document "+
				"carries text in every field, and the stack refuses this one. Nothing was "+
				"written to the vault", s.flag, field)
		}
		if strings.TrimSpace(str) == "" {
			return fmt.Errorf("--%s-file has an empty %q — nothing was written to the vault",
				s.flag, field)
		}
	}
	return nil
}

//go:embed module_contract.json
var moduleContractJSON []byte

// contractFields, bir sağlayıcının modül sözleşmesinde beyan edilen kök
// alanlarını verir; sağlayıcı sözleşmede yoksa boş döner.
//
// FAIL-CLOSED DEĞİL, ve bilerek: bu fiilin çağrıldığı tek yer zaten `type`
// kontrolünden geçmiş bir belge, ve sözleşmede olmayan bir sağlayıcı için
// "hiçbir alan zorunlu" demek kullanıcıyı BUGÜNKÜ davranışta bırakır — daha
// kötüye değil. Sözleşmenin eksikliğini yakalayan kapı ayrı ve zaten var
// (`TestTheCatalogMatchesTheModuleContract`, her sağlayıcı için kayıt ister).
func contractFields(provider string) []string {
	var doc struct {
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(moduleContractJSON, &doc); err != nil {
		return nil
	}
	fields := make([]string, 0, len(doc.Providers[provider]))
	for name := range doc.Providers[provider] {
		fields = append(fields, name)
	}
	sort.Strings(fields)
	return fields
}

// buildCredentials, sunucuya gidecek `credentials` gövdesini kurar.
//
// AYRI BİR FİİL, çünkü kapı onu ÇAĞIRMALI. Gövdenin şekli daha önce cobra
// closure'ının içinde kuruluyordu ve hiçbir test onu göremiyordu; sonuç, 11.09
// öncesinde alan adlarının, bugün de `fcm`'in sarmalayıcısının kimseye
// görünmeden yanlış olmasıydı.
//
// İKİ ŞEKİL VAR:
//
//   - Olağan: alanlar ve sırlar ADLARIYLA gövdeye girer.
//   - `credentialsAreTheSecret`: sırrın AYRIŞTIRILMIŞ içeriği gövdenin
//     KENDİSİDİR. `fcm` böyle — modül credentials'ı service_account.json'un
//     kendisi sayıyor (v2 provider/push/fcm.go ValidateFCMConfig: kökte `type`
//     == "service_account", `project_id`, `private_key`, `client_email`).
//     Sarmalayıcı bir alan koymak, kabul edilen ama her push'u ölen bir kayıt
//     yazmaktı.
func buildCredentials(
	spec *providerSpec, entry providerEntry, secrets map[string]string,
) (map[string]any, error) {
	if spec.credentialsAreTheSecret {
		if len(spec.secrets) != 1 {
			return nil, fmt.Errorf("provider %q: credentials are the secret, so it must declare "+
				"exactly one secret (declares %d)", spec.name, len(spec.secrets))
		}
		raw := secrets[spec.secrets[0].name]
		var doc map[string]any
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, fmt.Errorf("--%s-file is not valid JSON: %w", spec.secrets[0].flag, err)
		}
		return doc, nil
	}

	creds := map[string]any{}
	for k, v := range entry.fields {
		creds[k] = v
	}
	for k, v := range secrets {
		creds[k] = v
	}
	return creds, nil
}

// collectProviderFields, bayraklardan sağlayıcının GİZLİ OLMAYAN alanlarını
// toplar — ve her alanı MODÜLÜN BEKLEDİĞİ TÜRDE saklar.
//
// TÜR BURADA BİR SÖZLEŞMEDİR, biçim tercihi değil. Modülün kimlik yapıları
// `port`u `int`, `use_starttls` ve `is_production`'ı `bool` olarak okuyor
// (provider/email/smtp.go, provider/push/apns.go). Bu fonksiyon 11.09.2026'ya
// kadar HEPSİNİ dizge olarak yazıyordu: `strconv.Atoi` çağrılıyor ama sonucu
// ATILIYOR, `strconv.FormatBool` ise bool'u dizgeye çeviriyordu. Sonuç iki
// kanalda iki ayrı arıza oldu:
//
//   - `smtp`: gövde `{"port":"587"}` gidiyor, modülün doğrulayıcısı
//     `cannot unmarshal string into Go struct field SMTPConfig.port of type int`
//     ile REDDEDİYOR — ve komut bu noktaya SIRRI KASAYA YÜKLEDİKTEN sonra
//     varıyor, yani kullanıcıda parola kasada ama sağlayıcı yok.
//   - `apns`: `{"is_production":"true"}` push kanalında kabul kapısından
//     GEÇİYOR (o kanal `Create`'te doğrulanmıyor), kayıt yazılıyor, API
//     `configured: true` diyor ve worker her push'u düşürüyor — 26.08–11.09
//     arası e-postada yaşanan sessiz ölümün birebir aynısı.
//
// Fonksiyon olarak AYRILMASININ sebebi de bu: kapı artık kataloğun BEYANINI
// değil bu fonksiyonun ÜRETTİĞİ GÖVDEYİ ölçüyor. Beyanı ölçen bir kapı
// yeşil kalıyordu — `isInt: true` yazıyordu ve tele dizge koyuyordu.
func collectProviderFields(spec *providerSpec, cmd *cobra.Command) (providerEntry, error) {
	entry := providerEntry{fields: map[string]any{}}
	for _, f := range spec.fields {
		raw, _ := cmd.Flags().GetString(f.flag)
		if f.isBool {
			// Bool flags are tri-state here: present → value, absent → unset.
			if cmd.Flags().Changed(f.flag) {
				bv, _ := cmd.Flags().GetBool(f.flag)
				entry.fields[f.name] = bv
			}
			continue
		}
		if raw == "" {
			if f.required {
				return entry, fmt.Errorf("provider %q requires --%s (%s)", spec.name, f.flag, f.help)
			}
			continue
		}
		if f.isInt {
			n, perr := strconv.Atoi(raw)
			if perr != nil {
				return entry, fmt.Errorf("--%s must be a number (got %q)", f.flag, raw)
			}
			entry.fields[f.name] = n
			continue
		}
		entry.fields[f.name] = raw
	}
	return entry, nil
}

// stringField, bir alanın dizge değerini verir; alan yoksa ya da dizge değilse
// boş dizge döner. Çapraz alan kuralları yalnız dizge alanlara bakıyor.
func stringField(entry providerEntry, name string) string {
	if v, ok := entry.fields[name].(string); ok {
		return v
	}
	return ""
}

// validateProvider enforces cross-field rules the flat required-check can't:
// twilio needs one of from_number / messaging_service_sid.
// (Adlar 11.09.2026'da modülün `json:` etiketleriyle hizalandı; bu kontrol
// alan adının ÜÇÜNCÜ okuyucusuydu ve hizalama onu sessizce kırıyordu —
// mevcut kapı `TestTheProviderEntryMatchesTheModuleContract` yakaladı.)
func validateProvider(spec *providerSpec, entry providerEntry) error {
	if spec.name == "meta" {
		checks := []struct{ field, flag, pattern string }{
			{"phone_number_id", "phone-number-id", `^[0-9]+$`},
			{"api_version", "api-version", `^v[0-9]+\.0$`},
		}
		for _, check := range checks {
			if value := stringField(entry, check.field); value != "" && !regexp.MustCompile(check.pattern).MatchString(value) {
				return fmt.Errorf("--%s is not a valid Meta value", check.flag)
			}
		}
	}
	if spec.name == "twilio" {
		if stringField(entry, "from_number") == "" && stringField(entry, "messaging_service_sid") == "" {
			return fmt.Errorf("provider \"twilio\" requires one of --from-number or --messaging-sid")
		}
	}
	return nil
}

// A rejected write can echo its JSON body. Neither that response nor a
// transport error may put a credential into a terminal or CI log.
func redactProviderError(err error, secrets map[string]string) error {
	message := err.Error()
	values := make([]string, 0, len(secrets)*2)
	for _, value := range secrets {
		if value != "" {
			values = append(values, value)
			encoded, _ := json.Marshal(value)
			values = append(values, string(encoded[1:len(encoded)-1]))
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	return fmt.Errorf("%s", message)
}

// registerProviderFlags declares every field flag + every secret `--<flag>-file`
// flag once on the add command. Names are unique across the catalog (verified by
// test), so a single shared flag set works for all providers.
func registerProviderFlags(cmd *cobra.Command) {
	seen := map[string]bool{}
	for _, spec := range catalog {
		for _, f := range spec.fields {
			if seen[f.flag] {
				continue
			}
			seen[f.flag] = true
			if f.isBool {
				cmd.Flags().Bool(f.flag, false, f.help)
			} else {
				cmd.Flags().String(f.flag, "", f.help)
			}
		}
		for _, s := range spec.secrets {
			fileFlag := s.flag + "-file"
			if !seen[fileFlag] {
				seen[fileFlag] = true
				cmd.Flags().String(fileFlag, "", s.help+" (file path)")
			}
		}
	}
}

// resolveSecretValue gets a provider secret's value: from its `--<flag>-file`
// flag if given, else (when the secret allows prompting) an interactive hidden
// prompt. A secret that can't be sourced is a hard error.
func resolveSecretValue(cmd *cobra.Command, provider string, s secretField) (string, error) {
	fileFlag := s.flag + "-file"
	path, _ := cmd.Flags().GetString(fileFlag)
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --%s %q: %w", fileFlag, path, err)
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return "", fmt.Errorf("--%s %q is empty", fileFlag, path)
		}
		return string(data), nil
	}
	if s.prompt {
		return promptHidden(cmd, fmt.Sprintf("%s %s: ", provider, s.help))
	}
	return "", fmt.Errorf("provider %q requires --%s (%s)", provider, fileFlag, s.help)
}

// promptHidden reads a secret from the terminal without echoing it. Falls back to
// a clear error when stdin is not a TTY (so a non-interactive run fails loudly
// rather than hanging or reading a blank value).
func promptHidden(cmd *cobra.Command, label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no value provided and stdin is not a terminal — pass the secret via its --*-file flag")
	}
	fmt.Fprint(cmd.OutOrStdout(), label)
	data, err := term.ReadPassword(fd)
	fmt.Fprintln(cmd.OutOrStdout())
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", fmt.Errorf("empty value — aborting")
	}
	return value, nil
}

// removeCmd disables + drops a provider from config/notifications.ts. The live
// provider stays until removed (the deploy is upsert-only / never
// auto-deletes). The reserved secret is NOT deleted here (use `palbase secret
// remove <key>` if you want to purge it).
func removeCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <provider>",
		Short: "Stop delivering through one sender",
		Long: `Remove a sender from the stack.

It stops delivering. This used to drop the entry from config/notifications.ts and
leave the live provider in place, so "removed" and "still sending" were both true
at once.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := providerConfigID(r, cmd, args[0])
			if err != nil {
				return err
			}
			if _, err := call(r, cmd, http.MethodDelete, providersPath+"/"+url.PathEscape(id), nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ provider %q removed\n", args[0])
			return nil
		},
	}
}

// templatesCmd is the door `config/notifications.ts` used to be.
//
// The providers moved to the stack and got `add`/`remove`; the TEMPLATES stayed
// in the file, which the deploy evaluated and applied to NOTHING once the
// declaration applier was retired. So a project could edit a subject line, push,
// deploy green, and send the old copy. This is the door that actually writes.
func templatesCmd(r Resolvers) *cobra.Command {
	c := &cobra.Command{
		Use:   "templates",
		Short: "The message bodies this project sends",
	}
	var channel string
	c.PersistentFlags().StringVar(&channel, "channel", "email", "template channel: email or whatsapp")

	var file string
	set := &cobra.Command{
		Use:   "set --file <path>",
		Short: "Add this project's templates from a JSON document",
		Long: `Manage message content from a JSON document.

Email: creates the supplied templates; existing slugs are not replaced.
WhatsApp: atomically upserts the supplied slug/locale definitions and preserves
other definitions. It changes no provider credentials and does not submit
templates to Meta or certify their approval.

  palbase notifications templates set --channel whatsapp --file templates.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if channel == "whatsapp" {
				return setWhatsAppTemplates(r, cmd, file)
			}
			if channel != "email" {
				return fmt.Errorf("--channel must be email or whatsapp")
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read %s: %w", file, err)
			}
			// PARSE BEFORE SENDING. A document that does not parse cannot be a
			// template set, and finding that out from a 400 wastes a round trip
			// and reports it in the stack's words rather than the file's.
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(raw, &doc); err != nil {
				return fmt.Errorf("%s is not a JSON object: %w", file, err)
			}
			body, err := json.Marshal(doc)
			if err != nil {
				return err
			}
			answer, err := call(r, cmd, http.MethodPut, templatesPath, body)
			if err != nil {
				return err
			}
			var applied struct {
				Applied int `json:"applied"`
			}
			_ = json.Unmarshal(answer, &applied)
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %d template channel(s) added to the stack\n", len(doc))
			return nil
		},
	}
	set.Flags().StringVar(&file, "file", "", "JSON document holding the template set (required)")
	_ = set.MarkFlagRequired("file")

	// `list` READS the set the stack holds.
	//
	// It was written once, answered 405, and was removed — the contract published
	// PUT for this path and nothing else, because templates used to be declared in
	// config/notifications.ts and the FILE was what you read. The read door exists
	// now (GET /v1/management/notifications/templates), so the verb is back and
	// "what is this stack sending?" has an answer again.
	list := &cobra.Command{
		Use:   "list",
		Short: "Show the templates this stack holds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if channel == "whatsapp" {
				return listWhatsAppTemplates(r, cmd)
			}
			if channel != "email" {
				return fmt.Errorf("--channel must be email or whatsapp")
			}
			raw, err := call(r, cmd, http.MethodGet, templatesPath, nil)
			if err != nil {
				return err
			}
			// The module answers an ARRAY of templates, not a map of channels. It
			// was written here as `{channel: {key: def}}` and the live stack said
			// otherwise (2026-08-29) — the shape a route RETURNS is not the shape it
			// TAKES, and this one differs on both axes.
			var rows []struct {
				Slug      string `json:"slug"`
				Locale    string `json:"locale"`
				Subject   string `json:"subject"`
				IsDefault bool   `json:"is_default"`
			}
			if err := json.Unmarshal(raw, &rows); err != nil {
				return fmt.Errorf("the stack's template set is not readable: %s", strings.TrimSpace(string(raw)))
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No templates on this stack.")
				return nil
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Slug != rows[j].Slug {
					return rows[i].Slug < rows[j].Slug
				}
				return rows[i].Locale < rows[j].Locale
			})
			for _, r := range rows {
				// The built-ins a stack ships with are marked, because "what did I
				// add?" is the question this listing exists to answer.
				origin := "yours"
				if r.IsDefault {
					origin = "built-in"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-34s %-4s %-9s %s\n", r.Slug, r.Locale, origin, r.Subject)
			}
			return nil
		},
	}

	c.AddCommand(set, list)
	return c
}

// providerConfigID turns the provider NAME a person types into the CONFIGURATION
// ID the route deletes by.
//
// The two were conflated and `remove` therefore NEVER removed anything: the
// module deletes `WHERE id = $1`, and no configuration's id is "apns". Every
// invocation answered 404 while the help promised "It stops delivering". The
// package's own test asserted the broken path, which is why it survived.
func providerConfigID(r Resolvers, cmd *cobra.Command, name string) (string, error) {
	raw, err := call(r, cmd, http.MethodGet, providersPath, nil)
	if err != nil {
		return "", err
	}
	var configured []struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(raw, &configured); err != nil {
		return "", fmt.Errorf("could not read this stack's providers: %w", err)
	}
	// AN ID IS AN EXACT ANSWER, and it short-circuits the name search.
	//
	// Two configurations of the same sender is a LEGAL shape — `UNIQUE (channel,
	// provider, app_id)` with a nullable app_id — and the refusal below tells the
	// person to use the id instead. It used to be a dead end: the matcher only
	// ever compared `c.Provider`, so handing the id back was refused with the
	// very same words. A hatch nobody can reach is not a hatch.
	wanted := strings.TrimSpace(name)
	for _, c := range configured {
		if c.ID != "" && c.ID == wanted {
			return c.ID, nil
		}
	}
	var matches []string
	for _, c := range configured {
		if strings.EqualFold(strings.TrimSpace(c.Provider), wanted) && c.ID != "" {
			matches = append(matches, c.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf(
			"this stack has no %q provider configured — `palbase notifications providers` lists what it has", name)
	case 1:
		return matches[0], nil
	default:
		// Picking one would remove a sender the person did not name.
		return "", fmt.Errorf(
			"this stack carries %d %q configurations — name the one to remove by its id (%s)",
			len(matches), name, strings.Join(matches, ", "))
	}
}
