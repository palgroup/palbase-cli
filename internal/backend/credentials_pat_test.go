package backend

import (
	"net/http/httptest"
	"testing"
)

// MAKİNE KİMLİĞİ AYRI BİR TÜRDÜR — ve sunumu da ayrı.
//
// `pat_` bir taşıyıcı-bağlı (DPoP) kimliktir: `Authorization: DPoP <token>` ile
// gider ve yanında bir proof taşır. Onu `Bearer` diye sunmak, düzlemin
// tanımadığı bir şekil üretir ve kullanıcı doğru kimliği elinde tutarken 401
// alır — bu paketin daha önce `pb_` anahtarında yaşadığı arızanın aynısı:
// "kimlik doğruydu, SUNUM yanlıştı".
func TestPATIsItsOwnKindAndIsPresentedAsDPoP(t *testing.T) {
	if got := kindOf("pat_abc123"); got != KindPAT {
		t.Fatalf("pat_ değeri %q olarak sınıflandı, KindPAT bekleniyordu", got)
	}

	req := httptest.NewRequest("POST", "https://api.example.test/v1/cloud/projects", nil)
	Credentials{Value: "pat_abc123", Kind: KindPAT}.Apply(req)

	if got := req.Header.Get("Authorization"); got != "DPoP pat_abc123" {
		t.Fatalf("Authorization %q — DPoP şeması bekleniyordu", got)
	}
	// Proof'u TAŞIYICI katman ekliyor (isteğin metodunu ve URL'sini bağlıyor);
	// `Apply` şemayı koyar, imzayı değil.
	if req.Header.Get("apikey") != "" {
		t.Fatal("PAT `apikey` başlığına konmamalı — o, proje anahtarlarının yolu")
	}
}

// BUGÜNKÜ İKİ YOL HİÇ DEĞİŞMİYOR.
//
// Bu testin varlık sebebi: yeni bir kimlik türü eklerken en kolay yapılan
// hata, var olanların sınıflandırmasını kaydırmaktır. `pb_` hâlâ apikey,
// oturum jetonu hâlâ Bearer.
func TestExistingKindsAreUntouched(t *testing.T) {
	if got := kindOf("pb_project_sABC"); got != KindKey {
		t.Fatalf("pb_ anahtarı %q oldu, KindKey bekleniyordu", got)
	}
	if got := kindOf("eyJhbGciOiJFUzI1NiJ9.x.y"); got != KindPerson {
		t.Fatalf("oturum jetonu %q oldu, KindPerson bekleniyordu", got)
	}

	keyReq := httptest.NewRequest("GET", "https://x.test/a", nil)
	Credentials{Value: "pb_project_sABC", Kind: KindKey}.Apply(keyReq)
	if keyReq.Header.Get("apikey") != "pb_project_sABC" || keyReq.Header.Get("Authorization") != "" {
		t.Fatal("proje anahtarının sunumu değişti")
	}

	personReq := httptest.NewRequest("GET", "https://x.test/a", nil)
	Credentials{Value: "tok", Kind: KindPerson}.Apply(personReq)
	if personReq.Header.Get("Authorization") != "Bearer tok" {
		t.Fatal("oturum jetonunun sunumu değişti")
	}
}
