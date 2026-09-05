package backend

// upgrade.go — `palbase upgrade`: bu projenin runtime'ını KANITLANMIŞ imaja
// taşır (FR-090/091).
//
// NEDEN VAR: filo imajı güncellendiğinde kiracıların runtime'ı eski kalıyordu.
// Operatör filoyu kendi hızında yuvarlıyor; bu komut, beklemek istemeyen proje
// sahibine kendi runtime'ını BUGÜN tazeleme yolunu veriyor. Bölünmüş politikanın
// müşteri tarafı bu.
//
// HEDEF SEÇİLEMEZ ve bu bilinçli: komut bir imaj argümanı ALMIYOR. Hedef her
// zaman filonun kanaryasından geçmiş imajdır. Bir imaj parametresi, müşteriye
// hiç kanıtlanmamış bir bayt dizisini kendi üretimine koşturma hakkı verirdi —
// ve o kesintiden dönüş yolu yok (ileri migrasyon geri dönüşü kapatıyor).
//
// KESİNTİSİZ: takas kenar TUTUCUSU altında koşuyor, istek tutuluyor ve yeni pod
// hazır olduğunda devrediliyor. Komut, düzlem takası bitirene kadar bekliyor.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"time"

	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/spf13/cobra"
)

// refPattern is the plane's own rule for a project ref, copied here for one
// purpose: reading a ref back out of a linked address. Kept narrow on purpose —
// anything that is not clearly a ref must come back empty rather than be sent
// at the cloud as a project id.
var refPattern = regexp.MustCompile(`^[a-z0-9]{4,24}$`)

// refFromTargetURL reads the project ref out of a linked checkout's address.
//
// `palbase link` writes only a URL, because that address is all the other
// target-relative verbs need: push, status and deploys talk to the stack
// directly. `upgrade` is different — it calls a CLOUD route that names the
// project — so it reads the ref back out rather than asking for a selection
// the user already made by linking.
//
// A LOCAL STACK YIELDS NOTHING: `localhost` and an IP are not refs, and
// guessing one would aim an upgrade at a project id that does not exist.
// Neither is a bare plane host like `api.palbase.studio` — a tenant address has
// the ref FIRST and at least one label after it.
func refFromTargetURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) < 3 || !refPattern.MatchString(labels[0]) {
		return ""
	}
	return labels[0]
}

// localStackUpgradeRefusal turns a dead end into a direction.
//
// `upgrade` calls a CLOUD route, so a checkout pointed at a stack on this
// machine has no project for it to move. That much was already right:
// refFromTargetURL yields "" for a loopback address rather than guessing a ref.
//
// What was wrong is what came next — the caller fell through to "run `palbase
// link <project>` first", and the checkout WAS linked, to a local stack. The
// reader is told to do the thing they already did, which is how a refusal
// becomes a dead end rather than an instruction.
//
// A local stack moves by DECLARING a different stack version and starting
// again, so the refusal names that path instead.
func localStackUpgradeRefusal(target Target) error {
	if !target.OnThisMachine() {
		return nil
	}
	// WHICH LOCAL STACK, AND HOW TO GET BACK TO THE CLOUD ONE.
	//
	// A checkout can be linked to a cloud project AND have a dev stack running:
	// ReadTarget prefers the local one, so this fires while the reader's project
	// is in the cloud. Telling them to edit stackVersion is then advice for a
	// situation they are not in — the same class this refusal exists to close.
	// So it names the running stack and the verb that puts it away.
	if linked, err := readLinkedProject(); err == nil && linked.Project != "" {
		return fmt.Errorf(
			"a stack is running on this machine (%s) and every verb targets it, so `upgrade` cannot "+
				"reach the cloud project %q.\n  Run `palbase stop` first, or move the local stack by "+
				"setting stackVersion in %s and running `palbase start`",
			target.URL, linked.Project, projectPath())
	}
	return fmt.Errorf(
		"this checkout points at a stack on this machine (%s), and `upgrade` moves a CLOUD "+
			"project's runtime.\n  To move this stack: set stackVersion in %s and run `palbase start`",
		target.URL, projectPath())
}

// upgradeResult, düzlemin cevabı.
//
// `Changed` AYRI BİR ALAN çünkü "zaten o imajdaydı" ile "taşındı" kullanıcı için
// farklı iki cümle: ilki hiçbir kesinti riski almadı, ikincisi bir takas
// penceresinden geçti. Tek bir "ok" ikisini de aynı gösterirdi.
type upgradeResult struct {
	Image   string `json:"image"`
	Changed bool   `json:"changed"`
}

// UZUN BİR TAKAS BİR KAPIYI AŞAR — ve o kesinti bir BAŞARISIZLIK DEĞİLDİR.
//
// Sunucu yükseltmeyi kenar tutucusunun ardında yapıyor ve yeni runtime servise
// girene kadar bekliyor: bu POST bir pod değişimini KAPSIYOR ve dakikalar
// sürebiliyor. Canlıda ölçüldü (2026-09-01): çağrı `http_error (503):
// connection termination` ile döndü, ama kiracı GERÇEKTEN taşınmıştı
// (`sha-f2a96b7b` → `sha-6a4eebea9`, healthz 200). Kullanıcıya yalan söylendi.
//
// İSTEK İDEMPOTENT — hedef her zaman kanıtlanmış imaj, seçenek yok — o yüzden
// doğru davranış taşıyıcıya inanmak değil SONUCU DOĞRULAMAK. Yalnız 5xx ve
// taşıma arızaları yeniden denenir: bir 4xx, düzlemin GEREKÇELİ reddidir ve
// tekrar sormak onu değiştirmez.
// upgradeDoer, bu yardımcının ihtiyaç duyduğu TEK fiil. `REST`'in tamamını
// istemek, çağıranı ilgisiz bir yükleme fiiline bağlardı.
// Takasın kendisi dakikalar sürebiliyor; dört deneme × 5 sn, kapının kestiği
// bir bağlantıdan sonra düzleme sormak için yeterli ve bir arızayı da uzun
// süre gizlemiyor.
const (
	upgradeAttempts   = 4
	upgradeRetryDelay = 5 * time.Second
)

type upgradeDoer interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

func upgradeWithConfirmation(
	ctx context.Context, rest upgradeDoer, path string, attempts int, delay time.Duration,
) (upgradeResult, bool, error) {
	var out upgradeResult
	dropped := false
	var lastErr error
	for i := 0; i < attempts; i++ {
		out = upgradeResult{}
		err := rest.Do(ctx, http.MethodPost, path, nil, &out)
		if err == nil {
			return out, dropped, nil
		}
		lastErr = err
		if !upgradeIsRetryable(err) {
			return upgradeResult{}, false, err
		}
		dropped = true
		if i+1 < attempts && delay > 0 {
			select {
			case <-ctx.Done():
				return upgradeResult{}, dropped, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return upgradeResult{}, dropped, lastErr
}

// upgradeIsRetryable: yalnız düzlemin CEVAP VEREMEDİĞİ hâller. Bir 4xx
// gerekçeli bir reddir; onu tekrarlamak yalnız aynı reddi bir kez daha alır.
func upgradeIsRetryable(err error) bool {
	var api *transport.APIError
	if errors.As(err, &api) {
		return api.Status == 0 || api.Status >= 500
	}
	// Taşıma katmanının kendi arızası (bağlantı kesildi, zaman aşımı):
	// düzlemin ne dediğini HİÇ öğrenemedik, o yüzden sormaya devam.
	return true
}

func newUpgradeCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Args:  cobra.NoArgs,
		Short: "Move this project's runtime onto the proven fleet image",
		Long: `Move this project's runtime onto the proven fleet image.

The target is not a choice: it is always the image the fleet's canary has
proven. There is no flag to pick another one, because a runtime that never
booted anywhere cannot be rolled back from — the schema moves forward first.

The swap runs behind the edge holder, so in-flight requests are held rather
than refused. The command waits until the plane reports the new runtime is
serving.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// HEDEF-GÖRELİ, `push` ve `status` gibi: bir projeye bağlı checkout
			// O projeyi yükseltir. Tek fiil, iki yol değil.
			//
			// ONE READ, THREE QUESTIONS. This used to call ReadTarget() twice in
			// a row, and the two answers are not guaranteed to be the same one:
			// a `palbase start` or `stop` between them writes or removes
			// `.palbase/local.json`, which is the file ReadTarget PREFERS. The
			// flag refusal would then be judging a different target from the one
			// the local-stack refusal names, and the message would describe a
			// checkout the reader is not in.
			ref := ""
			if target, err := ReadTarget(); err == nil {
				if err := refuseCloudSelectionFlags(cmd, target); err != nil {
					return err
				}
				// LOCAL FIRST: a loopback target is a linked checkout, not an
				// unlinked one, and the generic "link first" below is advice for
				// a situation this reader is not in.
				if err := localStackUpgradeRefusal(target); err != nil {
					return err
				}
				ref = refFromTargetURL(target.URL)
			}
			// FR-013: there is no selection to fall back on — a checkout says
			// which project it is in `.palbase/project.json`, and if it says
			// nothing the refusal below names what to type.
			if ref == "" {
				return errors.New(
					"no project to upgrade: run `palbase link <project>` first — upgrade moves ONE project's runtime and needs to know which")
			}

			path := "/v1/cloud/projects/" + url.PathEscape(ref) + "/upgrade"
			out, dropped, err := upgradeWithConfirmation(
				cmd.Context(), r.REST(), path, upgradeAttempts, upgradeRetryDelay)
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			// FR-091: çıktı HANGİ imaja geçildiğini söylemek ZORUNDA. "Tamam"
			// demek, kullanıcıya neyi koşturduğunu söylememektir.
			// BAĞLANTI DÜŞTÜYSE BUNU SÖYLE. Sessizce başarı yazmak, kullanıcıya
			// gördüğü duraksamayı açıklamaz; "başarısız" yazmak ise YALAN olurdu —
			// düzlem soruldu ve cevabı bu.
			if dropped {
				fmt.Fprintf(cmd.OutOrStdout(),
					"the connection dropped during the swap; the plane reports %s runs %s\n", ref, out.Image)
				return nil
			}
			if !out.Changed {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already runs the proven image %s\n", ref, out.Image)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s now runs %s\n", ref, out.Image)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the result as JSON")
	return cmd
}
