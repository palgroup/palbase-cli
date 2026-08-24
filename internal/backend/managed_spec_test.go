package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubStatusError carries an HTTP status the way *transport.APIError does.
type stubStatusError struct{ status int }

func (e stubStatusError) Error() string   { return fmt.Sprintf("stub (%d)", e.status) }
func (e stubStatusError) StatusCode() int { return e.status }

// specDoer is a restDoer that replays a scripted sequence of outcomes and
// records every path it was asked for.
type specDoer struct {
	paths   []string
	replies []error
	doc     string
}

func (d *specDoer) Do(_ context.Context, _, path string, _, out any) error {
	d.paths = append(d.paths, path)
	i := len(d.paths) - 1
	if i < len(d.replies) && d.replies[i] != nil {
		return d.replies[i]
	}
	raw, ok := out.(*json.RawMessage)
	if !ok {
		return errors.New("out is not a *json.RawMessage")
	}
	*raw = json.RawMessage(d.doc)
	return nil
}

func fastOpts() fetchOpts {
	return fetchOpts{attemptTimeout: time.Second, totalBudget: 300 * time.Millisecond, minBackoff: 10 * time.Millisecond}
}

// SÖZLEŞME YÖNETİM API'SİNDEN OKUNUR — TENANT'IN ADMIN YOLUNDAN DEĞİL.
//
// Bir v2 yığını belgeyi `/admin/openapi.json`'da yalnız `service_role`'a
// sunuyor; CLI'ın elindeki yayınlanabilir anahtar oradan `403
// service_role_required` alıyor (canlı, 2026-08-24). Bu test istenen YOLU
// kilitler: geri kayarsa `link` ve `spec` yeniden hiçbir v2 yığınından
// sözleşme çekemez hâle gelir.
func TestManagedSpecIsReadThroughTheManagementAPI(t *testing.T) {
	d := &specDoer{doc: `{"openapi":"3.2.0","paths":{}}`}
	body, err := fetchManagedSpec(context.Background(), d, "app1prod", fastOpts())
	require.NoError(t, err)
	require.JSONEq(t, `{"openapi":"3.2.0","paths":{}}`, string(body))
	require.Equal(t, []string{"/api/v2/environments/app1prod/openapi"}, d.paths)
	require.Equal(t, "/api/v2/environments/app1prod/openapi", managedSpecPath("app1prod"))
}

// UYANMA BEKLENİR: boşa indirilmiş bir tenant uyanırken düzlem 503 döndürüyor
// ve bu GEÇEN bir durumdur.
func TestManagedSpecWaitsWhileTheBackendWakes(t *testing.T) {
	d := &specDoer{
		doc:     `{"openapi":"3.2.0"}`,
		replies: []error{stubStatusError{http.StatusServiceUnavailable}, stubStatusError{http.StatusBadGateway}, nil},
	}
	body, err := fetchManagedSpec(context.Background(), d, "app1prod", fastOpts())
	require.NoError(t, err)
	require.Contains(t, string(body), "openapi")
	require.Len(t, d.paths, 3, "iki 503/502'den sonra üçüncü deneme başarılı olmalı")
}

// ÇÖKME BEKLENMEZ: 404 ya da 401 beklemekle geçmez ve beklemek yalnız insanın
// vaktini alır. Bu ayrım kaybolursa, yanlış ortam adı yazan biri 150 saniye
// bekledikten sonra öğrenir.
func TestManagedSpecDoesNotWaitOnAHardError(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		d := &specDoer{doc: `{}`, replies: []error{stubStatusError{status}}}
		_, err := fetchManagedSpec(context.Background(), d, "app1prod", fastOpts())
		require.Error(t, err)
		require.Len(t, d.paths, 1, "HTTP %d yeniden denenmemeli", status)
	}
}

// Durum taşımayan bir hata (ağ kesintisi, bozuk gövde) da yeniden DENENMEZ:
// "bilmiyorum" ile "uyanıyor" aynı şey değil.
func TestManagedSpecDoesNotRetryAnUnclassifiedError(t *testing.T) {
	d := &specDoer{doc: `{}`, replies: []error{errors.New("connection reset")}}
	_, err := fetchManagedSpec(context.Background(), d, "app1prod", fastOpts())
	require.ErrorContains(t, err, "connection reset")
	require.Len(t, d.paths, 1)
}

// BOŞ GÖVDE SÖZLEŞME DEĞİLDİR: diske yazılırsa kod üretimi çok daha sonra,
// anlamsız bir hatayla düşer.
func TestManagedSpecRejectsAnEmptyDocument(t *testing.T) {
	d := &specDoer{doc: ""}
	_, err := fetchManagedSpec(context.Background(), d, "app1prod", fastOpts())
	require.ErrorContains(t, err, "empty contract")
}

var _ io.Writer = io.Discard
