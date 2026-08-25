package auth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type whoamiRT struct {
	status int
	body   string
	seen   *http.Request
}

func (r *whoamiRT) Do(req *http.Request) (*http.Response, error) {
	r.seen = req
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     make(http.Header),
	}, nil
}

// HEADLESS KİMLİK DE BİR KİMLİKTİR.
//
// `ManagementToken` PALBASE_ACCESS_TOKEN'ı BİRİNCİ sırada okuyor ve `status`,
// `link`, `push`, `project list` onunla çalışıyor. `whoami` ise yalnız tarayıcı
// oturumuna bakıyordu: jeton ayarlıyken bile "refresh tokens: 401 —
// invalid_token" diyordu (ölçüldü 25.08.2026). İşi "ben kimim" olan komut,
// belgelenmiş kimliği görmezden geliyordu.
func TestWhoamiAnswersWithTheHeadlessCredential(t *testing.T) {
	t.Setenv("PALBASE_ACCESS_TOKEN", "pat_123")
	rt := &whoamiRT{status: 200, body: `{"id":"usr_abc","email":"ops@example.test"}`}
	out := &bytes.Buffer{}
	c := &Client{Cfg: Config{AuthURL: "https://api.example.test"}, HttpClient: rt, Output: out}

	if err := c.Whoami(context.Background()); err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "usr_abc") || !strings.Contains(got, "ops@example.test") {
		t.Fatalf("kimlik basılmadı: %q", got)
	}
	// KİMLİK DÜZLEME SORULUR: jetonun içini yerel olarak okuyup ona inanmak,
	// imzasını doğrulamadığımız bir gövdeye inanmak olurdu.
	if rt.seen == nil || !strings.HasSuffix(rt.seen.URL.Path, "/v1/cloud/me") {
		t.Fatalf("düzleme sorulmadı: %+v", rt.seen)
	}
	if rt.seen.Header.Get("Authorization") != "Bearer pat_123" {
		t.Fatalf("jeton taşınmadı: %q", rt.seen.Header.Get("Authorization"))
	}
}

// REDDEDİLEN BİR JETON SESSİZ GEÇMEZ — ve durumu mesajda taşır.
func TestWhoamiReportsARejectedToken(t *testing.T) {
	t.Setenv("PALBASE_ACCESS_TOKEN", "pat_kotu")
	c := &Client{
		Cfg:        Config{AuthURL: "https://api.example.test"},
		HttpClient: &whoamiRT{status: 401, body: `{"error":"unauthorized"}`},
		Output:     &bytes.Buffer{},
	}
	err := c.Whoami(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("reddedilen jeton sessiz geçti: %v", err)
	}
}
