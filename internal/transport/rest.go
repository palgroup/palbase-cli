// Package transport provides the CLI's REST client for the Palbase control
// plane. Browser sessions use Bearer auth. Machine tokens use DPoP with a fresh
// proof bound to the request method, URL, and token.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client issues authenticated requests to the control plane.
type Client struct {
	// BaseURL is the control plane origin (e.g. https://api.palbase.studio).
	BaseURL string
	// Token is a browser session or machine token. An empty token prevents
	// requests from being sent.
	Token string
	// HTTPClient is initialized by New with a bounded request timeout.
	HTTPClient *http.Client
}

// New builds a client for the control plane at baseURL with an account
// credential. Machine tokens require DPoPSigner to be wired before use.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// APIError carries the parsed `{error, error_description, status,
// request_id}` envelope (CLAUDE.md "Error Response Format"). Callers can
// errors.As into it to switch on the machine-readable Code.
type APIError struct {
	Code        string
	Description string
	Status      int
	RequestID   string
	// Fields carries the per-field detail a validation refusal puts in the
	// envelope. See Error() — it is where the actual reason lives.
	Fields []APIErrorField
}

// APIErrorField is one entry of the envelope's `fields` array.
type APIErrorField struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// StatusCode exposes the HTTP status so callers can classify a failure without
// importing this package's concrete type — a "the tenant is still waking" 503
// is retryable while a 404 is not, and the difference is a status, not a string.
func (e *APIError) StatusCode() int { return e.Status }

// Error renders the envelope INCLUDING its per-field detail.
//
// THE REASON IS IN `fields`, AND IT WAS BEING THROWN AWAY. A validation refusal
// answers `{"error":"bad_request","error_description":"Bad request","fields":[…]}`
// — the description is a category and the fields are the sentence. Printing only
// the description gave `bad_request (400): Bad request`, which says nothing at
// all. Measured 2026-08-24: a push refused because the project's own tests failed
// reported exactly that, and the reason had to be read out of the tenant's log.
func (e *APIError) Error() string {
	head := fmt.Sprintf("%s (%d)", e.Code, e.Status)
	// `request_id` TELDEN GELİYORDU AMA HİÇ BASILMIYORDU: kullanıcı bir hata
	// bildirdiğinde onu düzlemin loglarına bağlayan tek ip budur.
	if e.RequestID != "" {
		head += " [request_id " + e.RequestID + "]"
	}
	if e.Description != "" {
		head += ": " + e.Description
	}
	if len(e.Fields) == 0 {
		return head
	}
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		switch {
		case f.Field != "" && f.Message != "":
			parts = append(parts, f.Field+": "+f.Message)
		case f.Message != "":
			parts = append(parts, f.Message)
		case f.Field != "":
			parts = append(parts, f.Field)
		}
	}
	if len(parts) == 0 {
		return head
	}
	return head + "\n  " + strings.Join(parts, "\n  ")
}

// errorEnvelope is the failure shape.
//
// `fields` ARRIVES IN TWO PLACES, and both are real. A tenant's own surface puts
// it at the top level; the control plane's SDK wraps a data-first error, so it
// lands under `data`. Reading only one of them silently drops the reason for
// half the refusals a person can hit — measured 2026-08-24, when the plane's
// push refusal parsed to nothing and the flat shape parsed fine.
type errorEnvelope struct {
	Code        string          `json:"error"`
	Description string          `json:"error_description"`
	Status      int             `json:"status"`
	RequestID   string          `json:"request_id"`
	Fields      []APIErrorField `json:"fields"`
	Data        struct {
		Fields []APIErrorField `json:"fields"`
	} `json:"data"`
}

// fields returns whichever of the two shapes carried the detail.
func (e errorEnvelope) fields() []APIErrorField {
	if len(e.Fields) > 0 {
		return e.Fields
	}
	return e.Data.Fields
}

// namedTransients, düzlemin "HENÜZ DEĞİL, yeniden sor" anlamına gelen ADLARIDIR.
//
// ADA bakılır, statüye değil: aynı hâl bugün 409, 429 ve 503 ile geliyor ve
// sınıflandırmayı statüye bağlamak kusuru bir sonraki statüde yeniden doğurur.
//
// `delete_incomplete` bilerek YOK: tekrar denenebilir ama sessizce tekrarlanan
// bir silme kullanıcıyı şaşırtır; onun tekrarı çağıranın kararıdır.
var namedTransients = map[string]bool{
	"tenant_unreachable":  true,
	"wake_in_progress":    true,
	"cutover_in_progress": true,
	"no_admitting_cell":   true,
}

// safeMethod, otomatik beklemenin TEK kapısıdır: yalnız GET ve HEAD.
//
// GEREKÇE "GET YAZMAZ" DEĞİL — bu düzlemde yazar. Ölçüldü:
// `GET /projects/{ref}/runtime/plan` kiracı uyuyorsa uyanış başlatıyor, o da
// kaydı güncelliyor ve rota yayınlıyor. Gerekçe TEKRARIN ZARARSIZLIĞI: ilan
// idempotent, ve eşzamanlı ikinci deneme uyanışın advisory kilidinde
// `wake_in_progress` alıp yine beklenenler arasına düşüyor.
//
// POST ve DELETE DIŞARIDA, ve bunun ölçülmüş sebebi var: bu istemcinin 22 çağrı
// noktasının 5'i POST ve 3'ü DELETE. Düzlemde bu adları üretebilen yollardan
// ikisi cevabı vermeden ÖNCE yazıyor — `retire` `UPDATE cloud_cells` ve K8s
// `DELETE /tenants/{ref}` (transaction'la geri alınmaz), push iki satır INSERT.
// Bugün CLI'ın hiçbir POST/DELETE'i o noktalara ulaşmıyor, ama "bugün ulaşmıyor"
// bir yorumla korunamaz — kural yöntemin şeklinde durur.
//
// Bir mutasyonun beklemesi gerekirse çare bu kapıyı gevşetmek DEĞİL, mutasyondan
// ÖNCE güvenli bir istekle hazırlığı sormaktır (`main.go`, FR-009a).
func safeMethod(m string) bool {
	switch strings.ToUpper(m) {
	case http.MethodGet, http.MethodHead:
		return true
	}
	return false
}

// IsNamedTransient, bu cevabın BU YÖNTEM için beklenebilir olup olmadığını söyler.
func IsNamedTransient(method string, err error) bool {
	var api *APIError
	return errors.As(err, &api) && namedTransients[api.Code] && safeMethod(method)
}

// Beklemenin sınırları.
//
// BÜTÇE ÖLÇÜLEN BİR SAYIDAN TÜRER: düzlemin KENDİ en uzun geçici bütçesi
// `awaitAddress` = 180 sn; yeni proje penceresi canlıda 90 sn ölçüldü; soğuk
// uyanış 14,3 sn. 240 sn, düzlemin en uzun geçici hâlini artı bir tam yeniden
// uyanış turunu kapsar.
//
// ARALIK SABİT DEĞİL, ÜSTEL — çünkü bekleyen istek düzlemde BEDAVA DEĞİL:
// tekrarlanan istek tekrarlanan uyanış denemesi demek ve uyanışın advisory
// kilidi istek transaction'ı boyunca tutuluyor. 2 sn'den başlayıp 15 sn'ye çıkan
// bir aralık hızlı vakayı da yakalar, kuyruğu da ~20 denemeye indirir.
var (
	TransientBudget  = 240 * time.Second
	TransientPollMin = 2 * time.Second
	TransientPollMax = 15 * time.Second
)

// sleep, beklemenin tek zaman kaynağıdır — testler onu değiştirip uyku dizisini
// ölçüyor. Doğrudan `time.After` çağırmak backoff'u ölçülemez yapardı.
var sleep = time.After

// stillStarting, pes ederken söylenen cümledir: durumun ADI, GEÇEN süre
// (yapılandırılmış bütçe değil), kullanıcının ne yapabileceği — ve `%w` ile
// sarılan `*APIError` üzerinden `request_id`.
func stillStarting(started time.Time, cause error) error {
	return fmt.Errorf(
		"the environment is still starting after %s — it usually answers within a minute; run the same command again: %w",
		time.Since(started).Round(time.Second), cause)
}

// ErrPlaneUnreachable, KONTROL DÜZLEMİNİN CEVAP VEREMEDİĞİNİ söyler.
//
// ALLOWLIST, DENYLIST DEĞİL. Çağıran "bu bir düzlem arızası mı" sorusunu hata
// METNİNDEN ya da "statüsü yok, o hâlde arızadır" çıkarımından okumamalı:
// ikincisi denenmişti ve kuralın tersini açtı — kimlik yokluğu ("not
// authenticated — run `palbase login`") ve eksik DPoP anahtarı da statüsüzdür,
// ve onlar KALICI, kullanıcının düzeltebileceği redlerdir. Onları "soramadım,
// tekrar dene" diye sunmak, bu paketin düzlemde kapattığı yalanın aynası olur.
//
// Bu yüzden işaret KAYNAKTA konur ve yalnız taşıma katmanının kendi arıza
// yolları taşır. İşareti taşımayan hiçbir şey düzlem arızası DEĞİLDİR.
var ErrPlaneUnreachable = errors.New("the control plane did not answer")

// Do performs one control-plane request, WAITING OUT the plane's named transient
// answers on safe methods.
//
// Bekleme BURADA, çünkü bu istemcinin 22 çağrı noktası var ve kuralı fiil fiil
// yamamak 22 ayrı karar üretirdi — eklenen 23'üncü fiil ise kuralın dışında
// kalırdı. Tam olarak bu desen bu değişikliği doğurdu: bir katman doğru ve
// adlandırılmış bir cevap üretiyor, tüketen katman o adı tanımıyor.
//
// HER DENEME YENİDEN İMZALANIR: makine kimliği DPoP taşır ve düzlem tekrarlanan
// bir proof'u reddeder, yani isteği saklayıp yeniden göndermek ikinci denemede
// 401 üretirdi. `doOnce` gövdeyi de her denemede yeniden kurar.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	started := time.Now()
	var deadline time.Time // ilk geçici cevaba kadar KURULMAZ
	var lastNamed error    // pes ederken cümleyi ve request_id'yi taşıyan
	wait := TransientPollMin
	for {
		err := c.doOnce(ctx, method, path, body, out)

		if IsNamedTransient(method, err) {
			lastNamed = err
			if deadline.IsZero() {
				// BÜTÇE YALNIZ UYKULARI DEĞİL DENEMELERİ DE SARAR. Tek bir
				// denemenin kendi bütçesi `plan` için 3 dakika, runtime
				// hazırlığı için 5 dakika; yalnız uykuları toplasaydık en
				// kötü hâlde dakikalarca SESSİZLİK üretirdik — yazılmış ama
				// yürürlükte olmayan bir bütçe.
				//
				// İlk denemeyi bilerek sarmıyoruz: uzun ama MEŞRU bir ilk
				// istek kesilmemeli. Bütçe ancak düzlem "henüz değil"
				// dedikten SONRA başlar. Çağıranın kendi son tarihi daha
				// yakınsa `WithDeadline` onu korur — erken olan kazanır.
				deadline = time.Now().Add(TransientBudget)
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, deadline)
				defer cancel()
			}
			if !time.Now().Before(deadline) {
				return stillStarting(started, lastNamed)
			}
			select {
			case <-ctx.Done():
				return stillStarting(started, lastNamed)
			case <-sleep(wait):
			}
			if wait *= 2; wait > TransientPollMax {
				wait = TransientPollMax
			}
			continue
		}

		// BÜTÇE BİR DENEMENİN ORTASINDA DOLABİLİR — ve o hâlde hata artık
		// adlandırılmış cevap DEĞİL, `context deadline exceeded`tir. Onu olduğu
		// gibi döndürmek, kullanıcıya tam olarak bu değişikliğin kaldırdığı şeyi
		// göstermek olurdu: platformun kendi geçici hâli, anlamsız bir taşıma
		// hatası kılığında.
		if lastNamed != nil && errors.Is(err, context.DeadlineExceeded) {
			return stillStarting(started, lastNamed)
		}
		return err
	}
}

// Do performs one control-plane request. The path is appended to BaseURL
// (e.g. "/v1/cloud/projects"). A non-nil body is JSON-encoded. On success the
// response is decoded directly into out; nil discards it. Non-2xx responses
// are parsed into an *APIError.
func (c *Client) doOnce(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(raw)
	}

	req, err := c.newSignedRequest(ctx, method, path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("management API request: %w: %w", ErrPlaneUnreachable, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w: %w", ErrPlaneUnreachable, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseError(raw, resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	// SUCCESS IS THE VALUE ITSELF — there is no `data` wrapper.
	//
	// Measured live (2026-08-21): the v2 control plane answers
	// GET /v1/cloud/projects with a bare `[]`, and a decoder expecting a
	// wrapper failed with "cannot unmarshal array into okEnvelope". Worse was
	// the shape that DID parse: `create` returned an object, the wrapper's
	// absent `data` decoded to nothing, and the command cheerfully printed
	// "Created  (, cell )" — a success message about a project it never read.
	//
	// Failures stay enveloped (error / error_description / status /
	// request_id); that path is handled above.
	if string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w (body=%s)", err, truncate(raw, 240))
	}
	return nil
}

// newSignedRequest builds an authenticated control-plane request.
//
// One place, every verb: JSON calls and multipart uploads alike come through
// here, so "how does this CLI authenticate" has exactly one answer.
//
// Fails closed when there is no token. Sending the request anonymously would
// earn a 401 that reads like a server problem rather than the truth, which is
// that nobody signed in.
func (c *Client) newSignedRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("not authenticated — run `palbase login` " +
			"(or, for headless use, export PALBASE_ACCESS_TOKEN)")
	}

	method = strings.ToUpper(method)
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	// MAKİNE KİMLİĞİ TAŞIYICI-BAĞLIDIR — ve sunumu da öyle.
	//
	// `pat_` bir DPoP token'ı (RFC 9449): `Authorization: DPoP <token>` ile
	// gider ve yanında BU isteğin metodunu, URL'sini ve token'ının özetini
	// imzalayan bir proof taşır. Bearer olarak sunmak, düzlemin tanımadığı bir
	// şekil üretir ve doğru kimliği elinde tutan çağıran 401 alır.
	//
	// Proof BURADA üretiliyor çünkü htm/htu bu isteğin kendisi — bir katman
	// yukarıda üretilseydi, yeniden yönlendirilen ya da yeniden denenen bir
	// istek yanlış bir proof taşırdı. Ve her çağrı TAZE bir proof imzalıyor:
	// sunucu tekrarları reddediyor.
	if strings.HasPrefix(c.Token, patPrefix) {
		if DPoPSigner == nil {
			return nil, fmt.Errorf(
				"cannot present a machine identity: no DPoP signer is wired")
		}
		proof, perr := DPoPSigner(method, c.BaseURL+path, c.Token)
		if perr != nil {
			// SESLİ DÜŞ: proof'suz göndermek, kimliği Bearer'a düşürüp
			// anlaşılmaz bir 401 almak olurdu.
			return nil, fmt.Errorf("could not produce a DPoP proof: %w", perr)
		}
		req.Header.Set("Authorization", "DPoP "+c.Token)
		req.Header.Set("DPoP", proof)
		return req, nil
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)
	return req, nil
}

// patPrefix, kontrol düzleminin bastığı makine kimliklerinin ön ekidir.
const patPrefix = "pat_"

// DPoPSigner, bir istek için RFC 9449 proof'u üretir.
//
// `main.go`'dan bağlanıyor, bu paket `internal/auth`'a bağımlı olmasın diye —
// aynı sebeple `backend.CloudKeyFetcher` de orada bağlanıyor. Nil ise makine
// kimliği taşıyan bir istek SESLİ düşer; sessizce Bearer'a düşmek, çağıranın
// anlamayacağı bir 401 üretirdi.
var DPoPSigner func(method, url, accessToken string) (string, error)

func parseError(raw []byte, status int) error {
	var env errorEnvelope
	if json.Unmarshal(raw, &env) == nil && env.Code != "" {
		st := env.Status
		if st == 0 {
			st = status
		}
		return &APIError{
			Code:        env.Code,
			Description: env.Description,
			Status:      st,
			RequestID:   env.RequestID,
			Fields:      env.fields(),
		}
	}
	return &APIError{
		Code:        "http_error",
		Description: truncate(raw, 240),
		Status:      status,
	}
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
