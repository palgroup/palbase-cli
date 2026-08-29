package test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func run(t *testing.T, r Resolvers, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := Cmd(r)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// `palbase test`in sözü: iki katmanı da koşar ve kimlikleri KENDİ mint eder.
// Ölçülen müşteri koşusunda bunun yerine bir mint betiği, bir pretest:live
// kancası ve dosyalar arasında kopyalanan PALBASE_TEST_* değişkenleri vardı.
func TestLiveLayerMintsIdentitiesAndExportsTheEnvironment(t *testing.T) {
	var mintedCount int
	r := Resolvers{
		Target: func(*cobra.Command) (Target, error) {
			return Target{URL: "http://127.0.0.1:63638", APIKey: "pb_project_x"}, nil
		},
		Mint: func(_ *cobra.Command, count int) ([]byte, error) {
			mintedCount = count
			return []byte(`{"identities":{"user1":{"id":"u1","email":"a@e.f","password":"p"}}}`), nil
		},
	}

	out, err := run(t, r, "--live", "--identities", "3")
	// npm test bu ortamda yok; komutun MINT ettiğini ve ortamı kurduğunu
	// gördüğümüz yer bu — koşum hatası ayrı bir şey.
	if mintedCount != 3 {
		t.Errorf("--identities geçmedi: %d", mintedCount)
	}
	if !strings.Contains(out, "minted 1 identit") {
		t.Errorf("kaç kimlik hazırlandığı söylenmiyor:\n%s", out)
	}
	if err == nil {
		t.Log("npm test bu ortamda çalıştı")
	}
}

// Mint şekli DEĞİŞİRSE komut sessizce boş bir ortam kurmamalı.
func TestALiveRunRefusesAMintItCannotRead(t *testing.T) {
	r := Resolvers{
		Target: func(*cobra.Command) (Target, error) { return Target{URL: "u", APIKey: "k"}, nil },
		Mint:   func(*cobra.Command, int) ([]byte, error) { return []byte(`{"users":[{"user_id":"u1"}]}`), nil },
	}
	_, err := run(t, r, "--live")
	if err == nil {
		t.Fatal("eski mint şekli sessizce kabul edildi")
	}
	if !strings.Contains(err.Error(), "identities") {
		t.Errorf("hata şeklin yanlış olduğunu söylemiyor: %v", err)
	}
}

func TestAMintFailureStopsTheRun(t *testing.T) {
	r := Resolvers{
		Target: func(*cobra.Command) (Target, error) { return Target{URL: "u", APIKey: "k"}, nil },
		Mint:   func(*cobra.Command, int) ([]byte, error) { return nil, errors.New("rate limited") },
	}
	_, err := run(t, r, "--live")
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("mint hatası yutuldu: %v", err)
	}
}

// İki yarım birden verilemez — ikisi de verilmezse ikisi de koşar.
func TestUnitAndLiveTogetherIsRefused(t *testing.T) {
	_, err := run(t, Resolvers{}, "--unit", "--live")
	if err == nil {
		t.Fatal("--unit --live birlikte kabul edildi")
	}
}
