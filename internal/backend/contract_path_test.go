package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// BİR v2 YIĞINI SÖZLEŞMESİNİ `/admin/openapi.json`'DA SUNAR.
//
// CLI `/openapi.json` istiyordu ve o yol v2'de HİÇBİR yığında yok — canlıda
// ölçüldü (2026-08-22): kontrol düzleminde `/openapi.json` deep-links modülüne
// düşüp `link_not_found` veriyor, `/admin/openapi.json` ise 200 dönüyor. Yani
// `palbase link` ve `palbase spec` hiçbir v2 yığınından sözleşme çekemiyordu.
//
// Bu test sunucuyu GERÇEK v2 şekliyle kurar: yalnız `/admin/openapi.json`
// cevaplar, `/openapi.json` 404'tür. Sabit geriye kayarsa test kırmızıya döner.
func TestContractPathIsWhatAV2StackServes(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path != contractPath {
			http.NotFound(w, r)
			return
		}
		require.NotEmpty(t, r.Header.Get("apikey"), "sözleşme anahtarsız istenemez")
		_, _ = w.Write([]byte(`{"openapi":"3.2.0","paths":{}}`))
	}))
	defer srv.Close()

	body, err := fetchRemoteOpenAPISpec(context.Background(), srv.URL+contractPath, "k", io.Discard)
	require.NoError(t, err, "istenen yol: %v", asked)
	require.Contains(t, string(body), `"openapi"`)
	require.Equal(t, "/admin/openapi.json", contractPath,
		"sözleşme yolu v2'nin sunduğu yoldur; değiştirmek link ve spec'i kırar")
	// Ve eski yol gerçekten ölü olmalı: aksi hâlde bu test, sunucusu iki yolu
	// birden cevapladığı için geçerdi ve hiçbir şey kanıtlamazdı.
	res, err := http.Get(srv.URL + "/openapi.json")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	require.False(t, strings.Contains(strings.Join(asked, " "), "/admin/admin"))
}
