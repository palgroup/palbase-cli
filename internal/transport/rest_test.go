package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Browser sessions use a Bearer credential.
func TestREST_Do_SendsBearerToken(t *testing.T) {
	var gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}, "request_id": "req_x"})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok_session")
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/me", nil, nil))
	require.Equal(t, "Bearer tok_session", gotAuth)
	require.Equal(t, "application/json", gotAccept)
}

// With no token the request never leaves: going out anonymous would earn a 401
// that reads like a server problem rather than the truth, which is that nobody
// signed in.
func TestREST_Do_WithoutATokenFailsClosed(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	err := c.Do(context.Background(), http.MethodGet, "/v1/cloud/me", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase login")
	require.False(t, reached, "an unauthenticated request must not reach the server")
}

// The control plane answers with the VALUE, not a {"data": …} wrapper.
//
// This is the regression that shipped a lie: with a wrapper-shaped decoder,
// `project create` parsed the response, found no `data`, decoded nothing, and
// printed "Created  (, cell )" — a success line about a project it had never
// read. A list came off worse and at least failed loudly.
func TestREST_Do_DecodesTheValueNotAWrapper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/projects") && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"ref":"aaa","phase":"Running"}]`))
			return
		}
		_, _ = w.Write([]byte(`{"ref":"bbb","slot":7,"cell":"pbc-cell-01","phase":"Running"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	var list []struct {
		Ref   string `json:"ref"`
		Phase string `json:"phase"`
	}
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects", nil, &list))
	require.Len(t, list, 1)
	require.Equal(t, "aaa", list[0].Ref)

	var one struct {
		Ref  string `json:"ref"`
		Slot int    `json:"slot"`
	}
	require.NoError(t, c.Do(context.Background(), http.MethodPost, "/v1/cloud/projects", nil, &one))
	require.Equal(t, "bbb", one.Ref, "an object response must reach the caller, not vanish into a wrapper")
	require.Equal(t, 7, one.Slot)
}

func TestREST_Do_GETHasNoBody(t *testing.T) {
	var hadBody bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1)
		n, _ := r.Body.Read(buf)
		hadBody = n > 0
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok_x")
	var out []any
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects", nil, &out))
	require.False(t, hadBody, "GET with nil body must not send a request body")
}

// `pat_` taşıyan her istek üretimde bir imzalayıcı BULUR (main.go
// wireDPoPSigner). Bu paketin testleri onsuz koşsaydı, üretimde var olmayan
// bir durumu taklit ederlerdi. İmzalayıcının YOKLUĞUNU sınayan test kendi
// içinde açıkça söküyor.
func init() {
	DPoPSigner = func(method, url, accessToken string) (string, error) {
		return "test-proof-" + method, nil
	}
}

func TestREST_Do_MapsErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "insufficient_scope",
			"error_description": "This token is missing the required scope: projects:write",
			"status":            403,
			"request_id":        "req_err",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "pat_x")
	err := c.Do(context.Background(), http.MethodPost, "/api/v1/projects", map[string]any{}, nil)
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr), "error must carry the parsed envelope")
	require.Equal(t, "insufficient_scope", apiErr.Code)
	require.Equal(t, 403, apiErr.Status)
	require.Equal(t, "req_err", apiErr.RequestID)
	require.Contains(t, apiErr.Error(), "insufficient_scope")
	require.Contains(t, apiErr.Error(), "projects:write")
}

func TestREST_Do_NonJSONErrorBody(t *testing.T) {
	// A 502 from an upstream proxy (e.g. Kong) may not be a JSON envelope.
	// The transport must still surface a non-nil error with the status.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	defer srv.Close()

	c := New(srv.URL, "pat_x")
	err := c.Do(context.Background(), http.MethodGet, "/api/v1/projects", nil, nil)
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, http.StatusBadGateway, apiErr.Status)
}

func TestREST_Do_MissingPATFailsClosed(t *testing.T) {
	// No PAT → the transport must not attempt the call (it would 401).
	c := New("https://api.dev.palbase.studio", "")
	err := c.Do(context.Background(), http.MethodGet, "/api/v1/projects", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "PALBASE_ACCESS_TOKEN")
}

func TestREST_Do_NullDataOk(t *testing.T) {
	// DELETE-style responses with no meaningful data must not error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`null`))
	}))
	defer srv.Close()
	c := New(srv.URL, "pat_x")
	require.NoError(t, c.Do(context.Background(), http.MethodDelete, "/api/v1/projects/x/api-keys/1", nil, nil))
}

// THE REASON IS IN `fields`, AND IT USED TO BE THROWN AWAY.
//
// A validation refusal answers `{"error":"bad_request","error_description":"Bad
// request","fields":[…]}` — the description is a CATEGORY and the fields are the
// SENTENCE. Rendering only the description produced `bad_request (400): Bad
// request`, which says nothing a person can act on. Measured 2026-08-24: a push
// refused because the project's own tests failed reported exactly that, and the
// reason had to be read out of the tenant's log instead.
func TestAPIError_RendersTheFieldDetail(t *testing.T) {
	err := &APIError{
		Code: "bad_request", Status: 400, Description: "Bad request",
		Fields: []APIErrorField{
			{Field: "artifact", Message: "push refused (tests_failed) — the previous release keeps serving"},
		},
	}
	got := err.Error()
	if !strings.Contains(got, "tests_failed") {
		t.Fatalf("the reason is missing:\n%s", got)
	}
	if !strings.Contains(got, "artifact") {
		t.Fatalf("the field name is missing:\n%s", got)
	}
}

// AND THE FIELDS MUST SURVIVE THE PARSE. Rendering them is half the job; the
// other half is reading them off the wire, and only a real response proves it.
func TestREST_Do_ParsesTheFieldDetailOffTheWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "bad_request", "error_description": "Bad request", "status": 400,
			"request_id": "req_x",
			"fields": []map[string]string{
				{"field": "artifact", "message": "push refused (tests_failed)"},
			},
		})
	}))
	defer srv.Close()

	err := New(srv.URL, "tok").Do(context.Background(), http.MethodPost, "/v1/cloud/projects/x/push", map[string]any{}, nil)
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Len(t, apiErr.Fields, 1)
	require.Equal(t, "artifact", apiErr.Fields[0].Field)
	require.Contains(t, err.Error(), "tests_failed")
}

// AND THE OTHER SHAPE. The control plane's SDK wraps a data-first error, so the
// same detail lands under `data.fields`. Reading only the flat one dropped the
// reason for every refusal that came from the plane rather than from a tenant —
// which is exactly the case that sent somebody to the tenant's log.
func TestREST_Do_ParsesTheFieldDetailNestedUnderData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "bad_request", "error_description": "Bad request", "status": 400,
			"request_id": "req_x",
			"data": map[string]any{
				"fields": []map[string]string{
					{"field": "artifact", "message": "the body is not a gzip stream: unexpected EOF"},
				},
			},
		})
	}))
	defer srv.Close()

	err := New(srv.URL, "tok").Do(context.Background(), http.MethodPost, "/v1/cloud/projects/x/push", map[string]any{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a gzip stream")
}

// A refusal with no fields must read exactly as it always did — the change adds
// detail where there is detail, it does not decorate everything else.
func TestAPIError_WithoutFieldsIsUnchanged(t *testing.T) {
	err := &APIError{Code: "not_found", Status: 404, Description: "no such project"}
	if got, want := err.Error(), "not_found (404): no such project"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// MAKİNE KİMLİĞİ DPoP OLARAK SUNULUR — ve proof BU isteği bağlar.
//
// Bearer olarak sunmak, düzlemin tanımadığı bir şekil üretir ve doğru kimliği
// elinde tutan çağıran 401 alır. Bu paketin `pb_` anahtarında yaşadığı
// arızanın aynısı: "kimlik doğruydu, SUNUM yanlıştı".
func TestPATIsPresentedAsDPoPWithAFreshProof(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization")+"|"+r.Header.Get("DPoP"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	calls := 0
	DPoPSigner = func(method, url, accessToken string) (string, error) {
		calls++
		return "proof-" + method + "-" + accessToken[:4] + "-" + string(rune('a'+calls)), nil
	}
	saved := DPoPSigner
	defer func() { DPoPSigner = saved }()

	c := New(srv.URL, "pat_makine")
	if err := c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects", nil, nil); err != nil {
		t.Fatalf("istek düştü: %v", err)
	}
	if err := c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects", nil, nil); err != nil {
		t.Fatalf("ikinci istek düştü: %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("iki istek bekleniyordu, %d görüldü", len(seen))
	}
	for _, h := range seen {
		if !strings.HasPrefix(h, "DPoP pat_makine|") {
			t.Fatalf("Authorization %q — `DPoP pat_makine` bekleniyordu", h)
		}
	}
	// HER İSTEK KENDİ PROOF'UNU İMZALAR: sunucu tekrarı reddediyor, yani
	// yeniden kullanılan bir proof ikinci isteği kaybettirir.
	if seen[0] == seen[1] {
		t.Fatal("proof YENİDEN KULLANILDI — sunucu ikinci isteği tekrar sayar")
	}
}

// İmzalayıcı bağlı değilse SESLİ düş — sessizce Bearer'a düşmek, çağıranın
// anlamayacağı bir 401 üretirdi.
func TestPATWithoutASignerFailsLoudly(t *testing.T) {
	saved := DPoPSigner
	defer func() { DPoPSigner = saved }()
	DPoPSigner = nil
	c := New("https://x.test", "pat_makine")
	err := c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "DPoP") {
		t.Fatalf("imzalayıcısız PAT sessizce geçti: %v", err)
	}
}

// BUGÜNKÜ YOL DEĞİŞMİYOR: oturum jetonu hâlâ Bearer.
func TestSessionTokenStillBearer(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if err := New(srv.URL, "eyJhbGciOiJFUzI1NiJ9.x.y").
		Do(context.Background(), http.MethodGet, "/v1/cloud/me", nil, nil); err != nil {
		t.Fatalf("oturum isteği düştü: %v", err)
	}
	if auth != "Bearer eyJhbGciOiJFUzI1NiJ9.x.y" {
		t.Fatalf("oturum jetonunun sunumu DEĞİŞTİ: %q", auth)
	}
}

// --- FR-009 / FR-013 · ADLANDIRILMIŞ GEÇİCİ CEVAP BEKLENİR, HATA OLARAK BASILMAZ ---
//
// Canlı ölçüm (2026-09-15): yeni yaratılan bir projede `palbase plan` 90 saniye
// hata verdi. Düzlem adlandırılmış ve açıkça tekrar denenebilir bir cevap
// üretiyordu; onu tüketen CLI o adı hiç tanımıyordu.

// Test bütçeleri: gerçek sayılar dakikalarla ölçülüyor, testler beklemez.
func shrinkTransientBudget(t *testing.T, budget time.Duration) {
	t.Helper()
	ob, omin, omax := TransientBudget, TransientPollMin, TransientPollMax
	TransientBudget, TransientPollMin, TransientPollMax = budget, time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { TransientBudget, TransientPollMin, TransientPollMax = ob, omin, omax })
}

func transientBody(name string, status int) string {
	return `{"error":"` + name + `","error_description":"still starting","status":` +
		strconv.Itoa(status) + `,"request_id":"req_trans_1"}`
}

func TestREST_Do_WaitsWhileTheAnswerIsANamedTransient(t *testing.T) {
	shrinkTransientBudget(t, time.Minute)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(transientBody("tenant_unreachable", 503)))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok_session")
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects/p1/runtime/plan", nil, nil))
	require.Equal(t, 3, hits, "adlandırılmış geçici cevap beklenmeli, kullanıcıya basılmamalı")
}

// GÜVENSİZ YÖNTEMDE ASLA SESSİZ TEKRAR YOK.
//
// Düzlemde bu adlara giden yollardan ikisi cevabı vermeden ÖNCE yazıyor
// (`retire` `UPDATE cloud_cells` ve K8s `DELETE /tenants/{ref}`; push iki satır
// INSERT). Bir POST'u sessizce tekrarlamak orada veri bozar. Mutasyon yapan
// fiilin çaresi tekrar değil, mutasyondan ÖNCE güvenli bir yoklamadır (FR-009a).
func TestREST_Do_NeverWaitsOnAnUnsafeMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			shrinkTransientBudget(t, time.Minute)
			var hits int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits++
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(transientBody("tenant_unreachable", 503)))
			}))
			defer srv.Close()

			c := New(srv.URL, "tok_session")
			err := c.Do(context.Background(), method, "/v1/cloud/projects/p1/runtime", map[string]any{"x": 1}, nil)
			require.Error(t, err)
			require.Equal(t, 1, hits, "%s tekrarlanmamalı: istek ETKİ BIRAKMIŞ olabilir", method)
		})
	}
}

func TestREST_Do_DoesNotWaitForAnUnlistedName(t *testing.T) {
	shrinkTransientBudget(t, time.Minute)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(transientBody("delete_incomplete", 503)))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok_session")
	require.Error(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects/p1", nil, nil))
	require.Equal(t, 1, hits, "`delete_incomplete` tekrar denenebilir ama SESSİZCE değil — tekrarı çağıranın kararı")
}

// ARALIK SABİT DEĞİL ARTAN — düzlemin uyanış kilidine kuyruk bırakmamak için.
//
// Uyanışın advisory kilidi istek transaction'ı boyunca tutuluyor; sabit 2 sn ile
// 240 sn ≈ 120 deneme, yani düzleme sıraya giren derin bir kuyruk. Ölçülen soğuk
// uyanış 14,3 sn olduğuna göre ondan sık sormak zaten boşa.
func TestREST_Do_BacksOffInsteadOfHammering(t *testing.T) {
	shrinkTransientBudget(t, time.Minute)
	TransientPollMin, TransientPollMax = 2*time.Millisecond, 8*time.Millisecond

	var slept []time.Duration
	orig := sleep
	sleep = func(d time.Duration) <-chan time.Time {
		slept = append(slept, d)
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	t.Cleanup(func() { sleep = orig })

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits <= 5 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(transientBody("wake_in_progress", 429)))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok_session")
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects/p1", nil, nil))
	require.Equal(t, []time.Duration{
		2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond,
		8 * time.Millisecond, 8 * time.Millisecond,
	}, slept, "aralık artmalı ve tavanda durmalı")
}

// HER DENEME YENİDEN İMZALANIR: düzlem tekrarlanan bir DPoP proof'unu reddeder.
func TestREST_Do_ResignsEveryAttempt(t *testing.T) {
	shrinkTransientBudget(t, time.Minute)
	var signed int
	orig := DPoPSigner
	DPoPSigner = func(method, url, accessToken string) (string, error) {
		signed++
		return "proof", nil
	}
	t.Cleanup(func() { DPoPSigner = orig })

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(transientBody("cutover_in_progress", 409)))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	c := New(srv.URL, "pat_machine")
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects/p1", nil, nil))
	require.Equal(t, hits, signed, "her deneme TAZE proof imzalamalı; saklanan bir istek ikinci denemede 401 alırdı")
}

// FR-013 · PES EDERKEN: durumun ADI, GEÇEN süre, ne yapılabileceği, request_id.
//
// İKİ YOL da ölçülür — bütçe denemeler ARASINDA dolduğunda ve bir denemenin
// ORTASINDA dolduğunda. İkincisinde hata artık `*APIError` değil
// `context deadline exceeded`tir; onu olduğu gibi döndürmek, tam olarak bu
// koşunun kaldırdığı şeyi göstermek olurdu.
func TestREST_Do_GivingUpNamesTheStateElapsedAndRequestId(t *testing.T) {
	t.Run("bütçe denemeler arasında dolar", func(t *testing.T) {
		shrinkTransientBudget(t, 20*time.Millisecond)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(transientBody("tenant_unreachable", 503)))
		}))
		defer srv.Close()

		c := New(srv.URL, "tok_session")
		err := c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects/p1", nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "still starting after")
		require.Contains(t, err.Error(), "run the same command again")
		require.Contains(t, err.Error(), "req_trans_1", "request_id kullanıcıya görünmeli")
		require.NotContains(t, err.Error(), "context deadline exceeded")
	})

	t.Run("bütçe denemenin ortasında dolar", func(t *testing.T) {
		shrinkTransientBudget(t, 30*time.Millisecond)
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits++
			if hits == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(transientBody("tenant_unreachable", 503)))
				return
			}
			time.Sleep(300 * time.Millisecond) // bütçeyi denemenin İÇİNDE doldurur
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}))
		defer srv.Close()

		c := New(srv.URL, "tok_session")
		err := c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects/p1", nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "still starting after", "son adlandırılmış cevap saklanmalı")
		require.Contains(t, err.Error(), "req_trans_1")
	})
}
