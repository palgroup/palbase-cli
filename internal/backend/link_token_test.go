package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// SELF-HOST'A KİMLİK VERMENİN BİR YOLU OLMAK ZORUNDA.
//
// Zincir dört halka: bu makinedeki yığın, elle yazılmış depo, ortam değişkeni,
// bulut defteri. Kendi kümesinde koşan bir yığın için ilki ve sonuncusu yok —
// geriye ortam değişkeni kalıyordu, ve o sırrı kabuğun tamamına açıyor.
//
// Depo ZATEN vardı (`StoreCredential`) ama onu çağıran hiçbir komut yoktu:
// yazılmış, ulaşılamaz bir kapı. Bu test o kapıyı `link`e bağlar.
func TestLinkStoresTheTokenItWasGiven(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(AccessTokenEnv, "") // ortamdan sızmasın: depoyu sınıyoruz

	const good = "pb_project_sGOODKEY0123456789abcdefghij"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apikey") != good {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"publishable":"pb_project_cPUB"}`))
	}))
	defer srv.Close()

	// Kabul edilmeyen bir anahtar DEPOLANMAMALI: bayat bir kayıt, sonraki her
	// komutu "did not accept this credential" ile karşılar ve kişi neyin
	// bozuk olduğunu bilmez.
	err := storeVerifiedToken(t.Context(), Target{URL: srv.URL}, "pb_project_sWRONG")
	require.Error(t, err)
	_, _, credErr := Credential(srv.URL)
	require.ErrorIs(t, credErr, ErrNoCredential, "reddedilen anahtar depoya yazılmamalı")

	// Kabul edilen anahtar depolanır ve zincir onu bulur.
	require.NoError(t, storeVerifiedToken(t.Context(), Target{URL: srv.URL}, good))
	cred, source, err := Credential(srv.URL)
	require.NoError(t, err)
	require.Equal(t, good, cred.Value)
	require.Equal(t, KindKey, cred.Kind, "proje anahtarı `apikey` başlığında gider, Bearer'da değil")
	require.Equal(t, SourceStore, source)
}

// Sır, kabuk geçmişine düşmemeli: değer stdin'den okunur ve SONUNDAKİ satır
// sonu atılır — bir `echo` ile beslenen anahtar aksi hâlde reddedilirdi.
func TestTokenFromStdinIsTrimmed(t *testing.T) {
	got, err := readTokenFrom(strings.NewReader("pb_project_sABC\n"))
	require.NoError(t, err)
	require.Equal(t, "pb_project_sABC", got)

	_, err = readTokenFrom(strings.NewReader("   \n"))
	require.Error(t, err, "boş girdi sessizce boş bir kimlik yazmamalı")
}
