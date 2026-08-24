package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SÖZLEŞME TENANT'TAN DEĞİL, YÖNETİM API'SİNDEN OKUNUR.
//
// Bir v2 yığını OpenAPI belgesini `/admin/openapi.json`'da ve YALNIZ
// `service_role` anahtarına sunuyor (`v2/internal/server/openapi.go`: *"the
// document lists every route, its parameters and its error shapes, which is
// precisely what a person integrating needs and precisely what nobody else
// does"*). CLI'ın elinde ise yayınlanabilir anahtar var — ve olması gereken de
// o: bir checkout'a servis anahtarı yazmak, telefonda taşınacak bir dosyaya
// sunucu anahtarı koymaktır.
//
// Ölçüldü 2026-08-24, canlı: `GET https://8bbwb2pbm.v2.palbase.studio/admin/openapi.json`
// yayınlanabilir anahtarla `403 service_role_required` veriyor. `palbase ios link`
// tam orada duruyordu ve v2'de hiçbir yığından sözleşme çekilemiyordu.
//
// Kapı artık kontrol düzleminde: çağıran kimliğini düzleme kanıtlıyor, düzlem
// de zaten SAKLADIĞI servis anahtarıyla tenant'a gidiyor. Anahtar düzlemden hiç
// çıkmıyor ve CLI'ın taşıdığı tek kimlik erişim token'ı oluyor.

// managedSpecPath is where the Management API serves an Environment's contract.
func managedSpecPath(environmentRef string) string {
	return "/api/v2/environments/" + environmentRef + "/openapi"
}

// statusCoder is implemented by *transport.APIError. Declared as an interface so
// this package keeps its narrow dependency on restDoer and does not import the
// transport package (see restDoer's comment).
type statusCoder interface{ StatusCode() int }

// managedSpecFetch is the production specFetch: the contract through the
// Management API, wake-aware.
//
// UYANMA BEKLENİR, ÇÖKME BEKLENMEZ. Boşa indirilmiş bir tenant uyanırken düzlem
// `503 spec_unavailable` döndürüyor ve bu GEÇEN bir durumdur; 4xx ise geçmez ve
// beklemek yalnız insanın vaktini alır. Ayrım tam olarak burada yapılıyor.
func managedSpecFetch(rest restDoer) specFetch {
	return func(ctx context.Context, environmentRef string, w io.Writer) ([]byte, error) {
		opts := defaultFetchOpts
		opts.progress = w
		return fetchManagedSpec(ctx, rest, environmentRef, opts)
	}
}

// fetchManagedSpec is the testable core of managedSpecFetch.
func fetchManagedSpec(ctx context.Context, rest restDoer, environmentRef string, opts fetchOpts) ([]byte, error) {
	deadline := time.Now().Add(opts.totalBudget)
	path := managedSpecPath(environmentRef)
	attempt := 0
	for {
		attempt++
		var doc json.RawMessage
		err := rest.Do(ctx, http.MethodGet, path, nil, &doc)
		if err == nil {
			if len(doc) == 0 {
				return nil, fmt.Errorf("fetch %s: the management API returned an empty contract", path)
			}
			return doc, nil
		}
		if !specWakeRetryable(err) {
			return nil, fmt.Errorf("fetch %s: %w", path, err)
		}
		if time.Now().Add(opts.minBackoff).After(deadline) {
			return nil, fmt.Errorf("fetch %s: backend did not wake within %s (last: %w)", path, opts.totalBudget, err)
		}
		if opts.progress != nil {
			fmt.Fprintf(opts.progress, "backend waking (attempt %d): %v — retrying in %s\n",
				attempt, err, opts.minBackoff.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.minBackoff):
		}
	}
}

// specWakeRetryable reports whether an error means "the tenant is still waking".
//
// YALNIZ ÜÇ DURUM. `502/503/504` düzlemin "arka uca ulaşamadım" cevabıdır ve
// beklemekle geçer. Bir `404` (böyle bir ortam yok) ya da `401` beklemekle
// geçmez — onlarda 150 saniye beklemek, yanlış cevabı geç vermektir.
func specWakeRetryable(err error) bool {
	var coded statusCoder
	if !errors.As(err, &coded) {
		return false
	}
	switch coded.StatusCode() {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
