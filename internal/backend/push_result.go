package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

func finishStackPush(ctx context.Context, w io.Writer, out pushResult, refresh func(context.Context, io.Writer) error) error {
	if out.Schema.Changed {
		fmt.Fprintln(w, "schema:")
		for _, line := range out.Schema.Summary {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if out.Unchanged && !out.Schema.Changed {
		fmt.Fprintf(w, "already live: %s — no changes to push\n", short(out.Digest))
		return nil
	}
	fmt.Fprintf(w, "live: %d endpoint(s), %s\n", out.EndpointCount, short(out.Digest))
	if err := refresh(ctx, w); err != nil {
		return fmt.Errorf("the push landed, but the client could not be regenerated: %w", err)
	}
	return nil
}

func writePushRuntime(w io.Writer, builtWith, running string, readErr error) {
	if builtWith == "" {
		return
	}
	if readErr != nil || running == "" {
		fmt.Fprintln(w, "runtime: could not verify the running SDK after the code upload; image migration is unconfirmed")
		return
	}
	if running != builtWith {
		fmt.Fprintf(w, "runtime: still @palbase/backend %s; code was built with %s — image migration is NOT complete\n", running, builtWith)
		return
	}
	fmt.Fprintf(w, "runtime: verified @palbase/backend %s\n", running)
}

// renderUpgradeReport, platformun 409 gövdesindeki yükseltme raporunu (C-1)
// KIRPMADAN basar (FR-049, FR-051).
//
// NEDEN KIRPILMIYOR. Ölçülmüş bir ders: aletin hata çıktısını bir gövde
// kırpıcısından geçirmek teşhisi imkânsız kılıyor — çerçeve cümlesi başta durur,
// asıl sebep sonda kalır ve tam da o kesilir. Bir göç hatası PostgreSQL'in kendi
// cümlesini taşır ve o cümle bazen uzundur; kısaltmak, sorunu bir daha üretmeden
// anlamayı imkânsız kılar.
//
// VE ÇARE CÜMLESİ PLATFORM YANIDIR. Ön kontrolü düşen bir yükseltme bizim
// kusurumuzdur; müşteriden envanter çıkarması, sıfırlama koşması ya da bir
// dosyayı elle düzeltmesi istenmez — penny'nin iki günü tam olarak buydu.
func renderUpgradeReport(w io.Writer, body []byte) {
	text := string(body)
	at := strings.LastIndex(text, `{"target"`)
	if at < 0 {
		fmt.Fprintln(w, strings.TrimSpace(text))
		return
	}
	if head := strings.TrimSpace(text[:at]); head != "" {
		fmt.Fprintln(w, head)
	}
	var rep struct {
		Outcome  string `json:"outcome"`
		ReportID string `json:"reportId"`
		Items    []struct {
			Module    string `json:"module"`
			Migration string `json:"migration"`
			Severity  string `json:"severity"`
			Message   string `json:"message"`
			Count     int64  `json:"count"`
		} `json:"items"`
		Ledger map[string]struct {
			Version  uint `json:"version"`
			Dirty    bool `json:"dirty"`
			Repaired bool `json:"repaired"`
		} `json:"ledger"`
	}
	if err := json.Unmarshal([]byte(text[at:]), &rep); err != nil {
		// ÇÖZÜLEMEYEN RAPOR DA BASILIR, ham hâliyle: okunamayan bir cevabı
		// yutmak, okunamadığını da gizlerdi.
		fmt.Fprintln(w, text[at:])
		return
	}
	for _, it := range rep.Items {
		fmt.Fprintf(w, "  %s · %s/%s · %d · %s\n", it.Severity, it.Module, it.Migration, it.Count, it.Message)
	}
	// DEFTER SIRALI BASILIR: map yinelemesi her koşuda başka bir sıra verir ve
	// aynı arızanın çıktısı her seferinde farklı görünürdü.
	modules := make([]string, 0, len(rep.Ledger))
	for m := range rep.Ledger {
		modules = append(modules, m)
	}
	sort.Strings(modules)
	for _, m := range modules {
		l := rep.Ledger[m]
		fmt.Fprintf(w, "  ledger %s: version %d dirty=%v repaired=%v\n", m, l.Version, l.Dirty, l.Repaired)
	}
	fmt.Fprintf(w, "platform-side check failed (%s, report %s); the platform team has been notified — "+
		"no customer action is needed\n", rep.Outcome, rep.ReportID)
}
