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
	// isBool marks a boolean field (isProduction, useStarttls).
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
			{name: "teamId", flag: "team-id", required: true, help: "Apple Developer Team ID"},
			{name: "keyId", flag: "key-id", required: true, help: "APNs auth key ID"},
			{name: "bundleId", flag: "bundle-id", required: true, help: "app bundle identifier (e.g. com.acme.app)"},
			{name: "isProduction", flag: "production", isBool: true, help: "use the APNs production gateway (default true)"},
		},
		secrets: []secretField{
			{name: "p8", flag: "p8", help: "APNs .p8 auth key file"},
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
			{name: "fromDomain", flag: "from-domain", required: true, help: "verified sender domain"},
		},
		secrets: []secretField{
			{name: "apiKey", flag: "api-key", prompt: true, help: "SendGrid API key"},
		},
	},
	{
		name:    "ses",
		channel: "email",
		fields: []field{
			{name: "region", flag: "region", required: true, help: "AWS region (e.g. us-east-1)"},
			{name: "accessKeyId", flag: "access-key-id", required: true, help: "AWS access key ID"},
			{name: "fromDomain", flag: "from-domain", required: true, help: "verified sender domain"},
		},
		secrets: []secretField{
			{name: "secretAccessKey", flag: "secret-access-key", prompt: true, help: "AWS secret access key"},
		},
	},
	{
		name:    "smtp",
		channel: "email",
		fields: []field{
			{name: "host", flag: "host", required: true, help: "SMTP server host"},
			{name: "port", flag: "port", required: true, isInt: true, help: "SMTP server port"},
			{name: "fromEmail", flag: "from-email", required: true, help: "sender email address"},
			{name: "username", flag: "username", help: "SMTP username (optional)"},
			{name: "useStarttls", flag: "starttls", isBool: true, help: "use STARTTLS (optional)"},
		},
		secrets: []secretField{
			{name: "password", flag: "password", prompt: true, help: "SMTP password"},
		},
	},
	{
		name:    "acs",
		channel: "email",
		fields: []field{
			{name: "fromEmail", flag: "from-email", required: true, help: "sender email address"},
			{name: "fromName", flag: "from-name", help: "sender display name (optional)"},
		},
		secrets: []secretField{
			{name: "connectionString", flag: "connection-string", prompt: true, help: "Azure Communication Services connection string"},
		},
	},
	{
		name:    "twilio",
		channel: "sms",
		fields: []field{
			{name: "accountSid", flag: "account-sid", required: true, help: "Twilio Account SID"},
			{name: "fromNumber", flag: "from-number", help: "sender phone number (one of from-number / messaging-sid)"},
			{name: "messagingServiceSid", flag: "messaging-sid", help: "Twilio Messaging Service SID (one of from-number / messaging-sid)"},
		},
		secrets: []secretField{
			{name: "authToken", flag: "auth-token", prompt: true, help: "Twilio auth token"},
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

// reservedSecretKey derives the env-var key backing a provider's secret field,
// e.g. ("apns","p8") → "PB_NOTIFICATIONS_APNS_P8".
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
