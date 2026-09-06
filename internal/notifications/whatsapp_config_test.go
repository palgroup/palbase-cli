package notifications

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func metaFiles(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	return []string{
		"--access-token-file", writeSecretIn(t, dir, "token", "META_ACCESS_FIXTURE\n"),
		"--app-secret-file", writeSecretIn(t, dir, "app", "META_APP_FIXTURE\n"),
		"--verify-token-file", writeSecretIn(t, dir, "verify", "META_VERIFY_FIXTURE\n"),
	}
}

func TestConfigureMetaStoresOnlyTheConnection(t *testing.T) {
	rest := &fakeStack{}
	args := append([]string{"configure", "meta", "--phone-number-id", "123456"}, metaFiles(t)...)
	out, err := runWith(t, rest, args...)
	require.NoError(t, err)
	require.JSONEq(t, `{"channel":"whatsapp","provider":"meta","credentials":{"phone_number_id":"123456","access_token":"META_ACCESS_FIXTURE","app_secret":"META_APP_FIXTURE","verify_token":"META_VERIFY_FIXTURE"}}`, rest.last().Body)
	require.NotContains(t, out, "META_ACCESS_FIXTURE")
	require.NotContains(t, out, "META_APP_FIXTURE")
	require.NotContains(t, out, "META_VERIFY_FIXTURE")
	require.Contains(t, out, "/v1/notifications/webhooks/whatsapp/meta")
	require.Contains(t, out, "not verified")
}

func TestInvalidMetaConfigurationNeverStartsWriting(t *testing.T) {
	for _, args := range [][]string{
		{"--phone-number-id", "https://wrong.example"},
		{"--phone-number-id", "123", "--otp-template", "valid_template"},
		{"--phone-number-id", "123", "--otp-language", "tr"},
		{"--phone-number-id", "123", "--api-version", "../messages"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			rest := &fakeStack{}
			_, err := runWith(t, rest, append([]string{"configure", "meta"}, args...)...)
			require.Error(t, err)
			require.Empty(t, rest.all())
		})
	}
	rest := &fakeStack{}
	files := metaFiles(t)
	files[len(files)-1] += ".missing"
	_, err := runWith(t, rest, append([]string{"configure", "meta", "--phone-number-id", "123"}, files...)...)
	require.Error(t, err)
	require.Empty(t, rest.all(), "a missing last credential must not upload the first two")
}

func TestProviderWriteErrorsCannotEchoCredentials(t *testing.T) {
	rest := &fakeStack{status: http.StatusBadRequest, answer: `{"error":"bad_request","error_description":"echo META_ACCESS_FIXTURE META_APP_FIXTURE META_VERIFY_FIXTURE"}`}
	out, err := runWith(t, rest, append([]string{"configure", "meta", "--phone-number-id", "123"}, metaFiles(t)...)...)
	require.Error(t, err)
	for _, secret := range []string{"META_ACCESS_FIXTURE", "META_APP_FIXTURE", "META_VERIFY_FIXTURE"} {
		require.NotContains(t, out+err.Error(), secret)
	}
}

func TestNotificationStatusFiltersWithoutSending(t *testing.T) {
	rest := &fakeStack{answer: `{"channels":[{"channel":"email","ok":false},{"channel":"whatsapp","ok":true,"via":"stored provider config: meta"}]}`}
	out, err := runWith(t, rest, "status", "--channel", "whatsapp", "--json")
	require.NoError(t, err)
	require.Len(t, rest.all(), 1)
	require.Equal(t, http.MethodGet, rest.last().Method)
	require.Equal(t, statusPath, rest.last().Path)
	var result map[string][]channelStatus
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Len(t, result["channels"], 1)
	require.Equal(t, "whatsapp", result["channels"][0].Channel)
}

func TestNotificationStatusFailsForMissingOrUnreadableChannel(t *testing.T) {
	for _, body := range []string{
		`{"channels":[{"channel":"whatsapp","ok":false,"because":"no Meta sender"}]}`,
		`{"channels":[{"channel":"whatsapp","ok":true,"error":"probe unavailable"}]}`,
		`{"channels":[{"channel":"whatsapp"}]}`,
		`{"channels":[{"channel":"email","ok":true}]}`,
		`{"error":"not supported"}`,
	} {
		_, err := runWith(t, &fakeStack{answer: body}, "status", "--channel", "whatsapp")
		require.Error(t, err, body)
	}
}
