package backend

// link_token_unreachable_test.go — KURTARMANIN ÖNÜNDEKİ SON HALKA.
//
// `push`un çaresi bir kimlik, kimliğin çaresi `link`, ve `link` anahtarı CANLI
// projeye karşı doğruluyordu. Tuğlalaşmış bir kiracıda o doğrulama yapılamaz,
// yani kurtarma imkânsızdı — ve kusurun en kötü yanı MESAJIYDI: kapı 503
// döndürüyor, kod onu "yığın bu anahtarı kabul etmedi" diye okuyordu. Yığın
// anahtarı hiç görmemişti.
//
// Ölçüldü 07.09.2026, penny (`na1m7lt2m`): anahtarı store'a elle yazmak
// zorunda kaldım.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStoreVerifiedTokenKeepsTheKeyWhenTheStackCannotAnswer(t *testing.T) {
	inScratchCheckout(t)
	// Servis etmeyen kiracının ARKASINDAKİ kapı: gövdesiz 503 (canlıda ölçülen).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream connect error or disconnect/reset before headers"))
	}))
	t.Cleanup(srv.Close)

	err := storeVerifiedToken(context.Background(), Target{URL: srv.URL}, "pb_secret_recovery")
	var unverified errUnverifiedToken
	if !errors.As(err, &unverified) {
		t.Fatalf("cevap veremeyen yığın GERÇEK bir red gibi işlendi: %v", err)
	}
	// ANAHTAR SAKLANDI — kurtarmanın koştuğu şey bu.
	cred, _, cerr := Credential(srv.URL)
	if cerr != nil || cred.Value != "pb_secret_recovery" {
		t.Fatalf("anahtar saklanmadı: %v %+v", cerr, cred)
	}
	// VE SEBEP SÖYLENDİ, "kabul etmedi" DENMEDİ.
	if msg := unverified.Error(); !strings.Contains(msg, "is not answering") || strings.Contains(msg, "did not accept") {
		t.Fatalf("mesaj yığını suçluyor: %s", msg)
	}
}

// NEGATİF KONTROL: yığının KENDİ reddi (401) hâlâ ölümcül ve HİÇBİR ŞEY saklanmaz.
func TestStoreVerifiedTokenStillRefusesARejectedKey(t *testing.T) {
	inScratchCheckout(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	err := storeVerifiedToken(context.Background(), Target{URL: srv.URL}, "pb_secret_wrong")
	if err == nil {
		t.Fatal("reddedilen anahtar kabul edildi")
	}
	var unverified errUnverifiedToken
	if errors.As(err, &unverified) {
		t.Fatalf("gerçek bir red 'ölçemedim' sayıldı: %v", err)
	}
	if _, _, cerr := Credential(srv.URL); cerr == nil {
		t.Fatal("reddedilen anahtar YİNE DE saklandı")
	}
}
