package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/transport"
)

// A LINKED CHECKOUT ALREADY NAMES ITS PROJECT — IN ITS ADDRESS.
//
// `palbase link` writes only a URL (`https://<ref>.<domain>`), because the
// address is all the other target-relative verbs need: push, status and deploys
// all talk to the stack directly. `upgrade` is different — it calls a CLOUD
// route that names the project — so it has to read the ref back out of the
// address rather than asking for a selection the user already made.
//
// Measured 2026-09-01: without this, `palbase upgrade` in a freshly linked
// checkout answered "no project selected — run `palbase start`", which is
// advice for a completely different situation and sends the reader nowhere.
func TestRefFromTargetURL(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"https://1jhp7jbrm.palbase.studio", "1jhp7jbrm"},
		{"https://1jhp7jbrm.palbase.studio/", "1jhp7jbrm"},
		{"https://abc12345m.dev.palbase.studio", "abc12345m"},
	} {
		if got := refFromTargetURL(tc.url); got != tc.want {
			t.Fatalf("refFromTargetURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// A LOCAL STACK IS NOT A CLOUD PROJECT, and guessing a ref out of `localhost`
// would send an upgrade at a project id that does not exist.
func TestRefFromTargetURLRefusesWhatIsNotARef(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1:54321",
		"http://localhost:9080",
		"https://api.palbase.studio",
		"",
		"not a url",
	} {
		if got := refFromTargetURL(u); got != "" {
			t.Fatalf("refFromTargetURL(%q) = %q, want empty", u, got)
		}
	}
}

// UZUN BİR TAKAS, BİR KAPIYI AŞAR — ve CLI bunu başarısızlık sanmamalı.
//
// CANLIDA ÖLÇÜLDÜ (2026-09-01): `palbase upgrade` bir kiracıyı eski imajdan
// kanıtlanmış imaja GERÇEKTEN taşıdı (pod öncesi `sha-f2a96b7b`, sonrası
// `sha-6a4eebea9`, healthz 200) ama kullanıcıya şunu yazdı:
//
//	http_error (503): upstream connect error or disconnect/reset before headers
//
// Sebep: sunucu takas boyunca 120 sn'ye kadar bekliyor ve bu POST bir pod
// değişimini kapsıyor; kapı bağlantıyı kesiyor. İstek idempotent — hedef her
// zaman kanıtlanmış imaj — o yüzden doğru davranış TAŞIYICIYA İNANMAK DEĞİL,
// SONUCU DOĞRULAMAK.
//
// Başarılı bir işlemi "başarısız" diye raporlamak, kullanıcıyı ya tekrar
// denemeye ya da taşınmış bir kiracıyı taşınmamış sanmaya iter.
func TestUpgradeConfirmsAfterADroppedConnection(t *testing.T) {
	t.Run("düşen bağlantıdan sonra sonucu DOĞRULAR", func(t *testing.T) {
		calls := 0
		rest := restFunc(func(_ context.Context, _, _ string, _, out any) error {
			calls++
			if calls == 1 {
				return &transport.APIError{Code: "http_error", Status: 503, Description: "connection termination"}
			}
			*(out.(*upgradeResult)) = upgradeResult{Changed: false, Image: "img:proven"}
			return nil
		})
		got, dropped, err := upgradeWithConfirmation(context.Background(), rest, "/p", 3, 0)
		if err != nil {
			t.Fatalf("doğrulama başarısız: %v", err)
		}
		if !dropped {
			t.Error("bağlantının düştüğü BİLDİRİLMELİ — kullanıcı ne olduğunu bilmeli")
		}
		if got.Image != "img:proven" {
			t.Errorf("imaj %q", got.Image)
		}
		if calls != 2 {
			t.Errorf("çağrı sayısı %d, 2 bekleniyordu", calls)
		}
	})

	t.Run("kalıcı 5xx HÂLÂ hata", func(t *testing.T) {
		rest := restFunc(func(_ context.Context, _, _ string, _, _ any) error {
			return &transport.APIError{Code: "http_error", Status: 503}
		})
		if _, _, err := upgradeWithConfirmation(context.Background(), rest, "/p", 3, 0); err == nil {
			t.Fatal("kalıcı arıza sessizce başarı sayılamaz")
		}
	})

	t.Run("4xx yeniden DENENMEZ — reddin sebebi geçici değil", func(t *testing.T) {
		calls := 0
		rest := restFunc(func(_ context.Context, _, _ string, _, _ any) error {
			calls++
			return &transport.APIError{Code: "bad_request", Status: 400, Description: "kuşağı bilinmiyor"}
		})
		if _, _, err := upgradeWithConfirmation(context.Background(), rest, "/p", 3, 0); err == nil {
			t.Fatal("400 hata olarak dönmeli")
		}
		if calls != 1 {
			t.Errorf("4xx %d kez denendi — bir kez denenmeliydi", calls)
		}
	})
}

// restFunc, REST arayüzünü tek bir fonksiyondan karşılar.
type restFunc func(ctx context.Context, method, path string, body, out any) error

func (f restFunc) Do(ctx context.Context, method, path string, body, out any) error {
	return f(ctx, method, path, body, out)
}

// A LOCAL STACK IS NOT AN UNLINKED CHECKOUT, and telling the reader to link is
// advice for a situation they are not in.
//
// `refFromTargetURL` correctly yields "" for `http://127.0.0.1:54321` — a
// loopback address carries no ref and guessing one would aim the upgrade at a
// project id that does not exist. But the caller then fell through to "no
// project to upgrade: run `palbase link <project>` first", and the checkout WAS
// linked: to a stack on this machine. The reader is told to do the thing they
// already did.
//
// The same class of defect this file's first test fixed on 2026-09-01, one
// target-kind further along.
func TestUpgradeOnALocalStackSaysWhatItIsAndWhatToDoInstead(t *testing.T) {
	local := Target{URL: "http://127.0.0.1:54321"}

	err := localStackUpgradeRefusal(local)
	if err == nil {
		t.Fatal("a loopback target was accepted for a cloud upgrade")
	}
	msg := err.Error()
	for _, want := range []string{"this machine", "stackVersion", "palbase start"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q — it has to say what this checkout IS and "+
				"what moves a local stack: %s", want, msg)
		}
	}
	if strings.Contains(msg, "palbase link") {
		t.Errorf("the refusal tells a LINKED checkout to link: %s", msg)
	}

	// A cloud target passes through untouched.
	if err := localStackUpgradeRefusal(Target{URL: "https://app1prod.palbase.studio"}); err != nil {
		t.Errorf("a cloud target was refused: %v", err)
	}
}
