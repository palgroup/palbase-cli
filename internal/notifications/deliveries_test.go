package notifications

import (
	"bytes"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

func TestDeliveriesPathAsksThePlaneForOneEnvironmentsWindow(t *testing.T) {
	got := deliveriesPath("na1m7lt2m", 7200, 50)
	want := "/v1/panel/environments/na1m7lt2m/messages/deliveries?limit=50&window_seconds=7200"
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// The provider's own word is printed, and a message with no report yet says so
// rather than looking delivered — the whole point is telling those apart (#25).
func TestPrintDeliveriesShowsTheProvidersWordAndItsSMTPAnswer(t *testing.T) {
	var out bytes.Buffer
	printDeliveries(&out, []delivery{
		{MessageID: "f1fd91fb", Template: strp("palauth.email_verify_code"), SentAt: "2026-09-27T03:53:12Z", Status: strp("Delivered")},
		{MessageID: "bbc16a51", Template: strp("palauth.email_verify_code"), SentAt: "2026-09-27T02:12:52Z", Status: strp("Bounced"), Detail: strp("550 5.4.310 DNS domain palbase.test does not exist")},
		{MessageID: "c0ffee00", SentAt: "2026-09-27T04:00:00Z"},
	})
	text := out.String()
	for _, want := range []string{
		"Delivered", "f1fd91fb",
		"Bounced", "550 5.4.310 DNS domain palbase.test does not exist",
		"(pending)", "c0ffee00",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestFilterByIDKeepsOnlyThatMessage(t *testing.T) {
	rows := filterByID([]delivery{{MessageID: "a"}, {MessageID: "b"}}, "b")
	if len(rows) != 1 || rows[0].MessageID != "b" {
		t.Fatalf("rows = %+v", rows)
	}
}
