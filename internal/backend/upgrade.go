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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

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

// upgradeResult, düzlemin cevabı.
//
// `Changed` AYRI BİR ALAN çünkü "zaten o imajdaydı" ile "taşındı" kullanıcı için
// farklı iki cümle: ilki hiçbir kesinti riski almadı, ikincisi bir takas
// penceresinden geçti. Tek bir "ok" ikisini de aynı gösterirdi.
type upgradeResult struct {
	Image   string `json:"image"`
	Changed bool   `json:"changed"`
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
			if target, err := ReadTarget(); err == nil {
				if err := refuseCloudSelectionFlags(cmd, target); err != nil {
					return err
				}
			}
			// A LINKED CHECKOUT ALREADY NAMES ITS PROJECT — in its address.
			// Reading it back is what makes `upgrade` behave like `push` and
			// `status`: the checkout you linked is the project you act on.
			ref := ""
			if target, err := ReadTarget(); err == nil {
				ref = refFromTargetURL(target.URL)
			}
			if ref == "" {
				sel, err := r.resolve(cmd.Context())
				if err != nil {
					return unlinkedOrCloudError(err)
				}
				ref = sel.EnvironmentRef()
			}
			if ref == "" {
				return errors.New(
					"no project to upgrade: run `palbase link <project>` first — upgrade moves ONE project's runtime and needs to know which")
			}

			var out upgradeResult
			path := "/v1/cloud/projects/" + url.PathEscape(ref) + "/upgrade"
			if err := r.REST().Do(cmd.Context(), http.MethodPost, path, nil, &out); err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			// FR-091: çıktı HANGİ imaja geçildiğini söylemek ZORUNDA. "Tamam"
			// demek, kullanıcıya neyi koşturduğunu söylememektir.
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
