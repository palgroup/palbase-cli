package backend

// plan.go — `palbase plan`: what a push would do, before it does it.
//
// A push carries FOUR things and always has: the code, the schema the code
// declares, the configuration beside it, and the secrets it needs to run. They
// travel together because they fail together — code that reads a flag which has
// not been declared, or a credential that has not been set, is code that deploys
// green and 500s on its first request.
//
// So this shows all four, and touches nothing. The schema half is computed by
// the project itself (the same computation the push runs, stopping before it
// writes), which is what makes it a plan rather than a guess: a differ written
// here would have its own opinion about what a type change costs.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newPlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Args:  cobra.NoArgs,
		Short: "Show what `palbase push` would change",
		Long: `Show the whole change set — code and schema — and apply none of it.

The config and secret sections are gone with config/ itself (23.0.0); this
help promised them for one release after they stopped being printed, which is
the shape of stale text this CLI exists not to ship.

Nothing is written to the target: the schema half is computed by the project
itself, which is the same computation the push runs, stopped before it writes.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := PrintTargetFor(cmd)
			if err != nil {
				return err
			}
			cred, _, err := Credential(target.URL)
			if err != nil {
				return err
			}
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := RequireBackendPlane(dir); err != nil {
				return err
			}
			return runPlan(cmd.Context(), dir, target, cred, cmd.OutOrStdout())
		},
	}
}

func runPlan(ctx context.Context, dir string, target Target, cred Credentials, out io.Writer) error {
	// CODE. Building is how "would this even deploy" gets answered here rather
	// than on the target, and it is THE SAME BUILD the push runs — the same
	// function, not merely a build of the same sources.
	//
	// It used to call runBuild, which is the esbuild path `palbase build` and the
	// cloud deploy take, while a push to a stack goes through buildStackArtifact,
	// which is bun. The comment here claimed they were the same build and they
	// were two bundlers with two opinions about how a decorator lowers — which is
	// the exact difference stack_bundle.go says the bun choice exists to remove.
	// A plan that goes green on code the push then refuses is worse than no plan:
	// it is a check whose passing means nothing.
	fmt.Fprintln(out, "code")
	uses, _, err := buildStackArtifact(ctx, dir, indent(out))
	// `plan` answers a question and ships nothing, so the bundle it just built
	// has no reader at all — leaving it would put a stale artifact where the
	// next push would find one and have to distrust it.
	defer removeBundleOutput(dir)
	if err != nil {
		return err
	}
	// An @Upload naming a bucket the stack does not have is a push that will be
	// refused, so a plan that stayed quiet about it would be a plan that missed
	// the one thing it is for.
	if len(uses) > 0 {
		have, bucketErr := stackBuckets(ctx, target)
		if bucketErr != nil {
			return bucketErr
		}
		if bucketErr := unknownUploadBuckets(uses, bucketNames(have)); bucketErr != nil {
			return bucketErr
		}
		fmt.Fprintf(indent(out), "%d @Upload route(s), every bucket exists\n", len(uses))
	}

	// IMAGE, before the schema, because it is the coarser change: a pod replaced
	// is a bigger thing to know about than a column added, and a plan reads
	// top-down. Both sides are read here rather than inside the writer so the
	// writer stays a pure formatter with a test that needs no network.
	//
	// The target is the SDK INSTALLED IN THIS CHECKOUT — the same number
	// `palbase start` resolves locally, which is the whole point: what you tested
	// against is what the push carries. The current is what the project SAYS it
	// runs; silent when it will not say, and the writer treats that silence as
	// "unknown", not as "changing".
	var (
		running, installed string
		runtimeSection     json.RawMessage
		// TOLERANS POZİTİF KANIT İSTER. Varsayılan KATI: yalnız probe gerçekten
		// koşup bir sürüm getiremediğinde gevşer. Yerel checkout'un bozuk olması
		// (`installedSDKVersion` düşerse) sağlıklı bir kiracıyı "cevap vermiyor"
		// saymaz — o yol hiç ölçüm yapmadı.
		answering = true
	)
	if v, err := installedSDKVersion(dir); err == nil {
		installed = v
		sdkCtx, cancelSDK := context.WithTimeout(ctx, 10*time.Second)
		probed, sdkErr := projectSDKVersion(sdkCtx, target, cred)
		cancelSDK()
		if sdkErr == nil {
			running = probed
		}
		answering = running != ""
	}
	// RUNTIME BÖLÜMÜ: yükseltme NE YAPACAK (FR-045).
	//
	// Yalnız sürüm gerçekten değişecekse ve hedef bu bulutun bir projesiyse
	// sorulur. Diğer her durumda eski satır basılır — kendi kendine barındırılan
	// bir yığın için göç planı isteyecek bir kontrol düzlemi YOKTUR, ve olmayan
	// bir şeyi sormak planı hatayla düşürmek olurdu.
	// ÖLÇÜLEMEYEN SÜRÜM PLANLAYICIYA GİDER (FR-063). `running == ""` "değişen bir
	// şey yok" demek DEĞİL, "kiracı cevap veremiyor" demek — ve tam da o kiracı
	// yükseltilmeye muhtaç. Eskiden bu dal sessizce `writeImagePlan`'e düşüyor,
	// o da `current == ""` görüp hiçbir şey basmıyordu: plan dosyası yazılıyor
	// ama İÇİ BOŞ, ve push bir şey anlatmayan bir planı taşıyordu.
	switch {
	case running == installed || CloudRuntimePlanner == nil:
		writeImagePlan(out, running, installed)
	default:
		section, err := CloudRuntimePlanner(ctx, target.URL, installed)
		switch {
		case errors.Is(err, ErrNotACloudProject):
			writeImagePlan(out, running, installed)
		case err != nil:
			return fmt.Errorf("runtime plan: %w", err)
		default:
			runtimeSection = section
			writeRuntimePlan(out, section)
		}
	}

	// SCHEMA, computed by the project against its own database.
	//
	// EVERY declaration goes. It used to send the public file alone and print a
	// note naming the siblings — but a plan narrower than the project is a plan
	// somebody reads as complete, and the note was the admission that it was not.
	fmt.Fprintln(out, "schema")
	// ŞEMA CEVABININ BAYTI TUTULUR: plan dosyasının parmak izi onun ÖZETİNİ
	// taşıyor, ve sunucu push anında aynı özeti bekliyor. Ekrana basılan metni
	// değil, cevabın kendisini hash'lemek zorunda — biçimlendirme değişirse
	// parmak izi kaymasın.
	schema, err := schemaPlanFromProject(ctx, dir, target, cred, answering)
	if err != nil {
		return err
	}
	var unmeasured []string
	switch {
	case !schema.Declared:
		fmt.Fprintf(out, "  no %s — this project declares no tables\n", PublicSchemaFile)
	case !schema.Measured:
		fmt.Fprintf(out, "  %s\n", unmeasuredNote)
		unmeasured = append(unmeasured, unmeasuredNote)
	default:
		renderSchemaPlan(out, schema.Body)
	}
	schemaBody := schema.Body

	// NO CONFIG SECTION, and its absence is the point.
	//
	// There used to be one, and it could not tell the truth: the target had no
	// route that reported what it currently held, so the line said what would be
	// SENT rather than what would CHANGE — and for a while it was not even true
	// that the sections were applied at all.
	//
	// Settings are written directly now, by whoever changes them, so a plan has
	// nothing to say about them: they are already in effect. What a push carries
	// is code and schema, and that is what this shows.

	// VE PLAN DOSYASI YAZILIR (FR-045, D-014). Bu satır olmadan `palbase push`
	// koşamaz — sunucu da koşturmaz. Kullanıcının cümlesi buydu: "plansız push
	// yapılamaması lazım, ben her push denediğimde direkt gidiyo o zaman planın
	// ne işi var".
	bundle, err := BundleDigest(dir)
	if err != nil {
		return err
	}
	schemaSum := sha256.Sum256(schemaBody)
	p := PlanFile{
		Version:          1,
		CreatedAt:        time.Now().UTC(),
		Target:           PlanTarget{URL: target.URL, Ref: refOfURL(target.URL)},
		BundleDigest:     bundle,
		SDK:              PlanSDK{Running: running, Target: installed},
		SchemaPlanDigest: hex.EncodeToString(schemaSum[:]),
		Runtime:          runtimeSection,
		Destructive:      destructiveOf(schemaBody),
		Breaking:         []string{},
		Unmeasured:       unmeasured,
	}
	p.Fingerprint = Fingerprint(p.BundleDigest, p.SDK.Running, p.SDK.Target, p.SchemaPlanDigest)
	if err := WritePlanFile(dir, p); err != nil {
		return err
	}
	fmt.Fprintf(out, "plan written: .palbase/plan.json (%s)\n", p.Fingerprint[:12])
	return nil
}

// refOfURL, hedef adresinin ilk host etiketi — bulut projelerinde ref budur.
// Plan dosyasında yalnız İNSAN için: kapı ref'i değil parmak izini ölçer.
func refOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if i := strings.Index(host, "."); i > 0 {
		return host[:i]
	}
	return host
}

// destructiveOf, şema planının veri kaybettiren kalemlerini plan dosyasına
// okunur satırlar olarak taşır.
func destructiveOf(body []byte) []string {
	var plan schemaPlanWire
	if err := json.Unmarshal(body, &plan); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(plan.Destructive))
	for _, d := range plan.Destructive {
		out = append(out, fmt.Sprintf("%s %s.%s (%d rows)", d.Kind, d.Table, d.Column, d.Rows))
	}
	return out
}

// A plan can compare the observed runtime with the checkout's requirement; it
// cannot promise a migration. Schema compatibility can stop the push, and the
// platform can independently refuse an unknown or unpullable image.
func writeImagePlan(w io.Writer, current, target string) {
	if current == "" || current == target {
		return
	}
	fmt.Fprintf(w, "image\n  running %s; required by this checkout: %s\n  this plan changes nothing; the platform must complete and verify the image migration\n",
		current, target)
}

// schemaPlanWire is the project's SchemaPlan.
type schemaPlanWire struct {
	InSync      bool     `json:"in_sync"`
	Changes     []string `json:"changes"`
	Destructive []struct {
		Kind    string `json:"kind"`
		Table   string `json:"table"`
		Column  string `json:"column"`
		Rows    int64  `json:"rows"`
		NonNull *int64 `json:"non_null"`
	} `json:"destructive"`
	Unsupported  []string `json:"unsupported"`
	Incompatible []string `json:"incompatible"`
}

func renderSchemaPlan(out io.Writer, body []byte) {
	var plan schemaPlanWire
	if err := json.Unmarshal(body, &plan); err != nil {
		fmt.Fprintf(out, "  (unreadable plan: %s)\n", trimBody(body))
		return
	}
	if plan.InSync && len(plan.Changes) == 0 && len(plan.Destructive) == 0 &&
		len(plan.Unsupported) == 0 && len(plan.Incompatible) == 0 {
		fmt.Fprintln(out, "  in sync")
		return
	}
	for _, change := range plan.Changes {
		fmt.Fprintf(out, "  %s\n", change)
	}
	// The server's formatted changes already describe drops and which require
	// approval. Printing the structured list again both duplicates them and
	// incorrectly labels empty columns/tables as requiring data-loss approval.
	if len(plan.Changes) == 0 {
		for _, drop := range plan.Destructive {
			needsApproval := drop.Rows > 0
			if drop.Column != "" {
				if drop.NonNull != nil {
					needsApproval = *drop.NonNull > 0
					fmt.Fprintf(out, "  drop %s.%s — %d value(s) in %d row(s)",
						drop.Table, drop.Column, *drop.NonNull, drop.Rows)
				} else {
					fmt.Fprintf(out, "  drop %s.%s — %d row(s), value count unknown",
						drop.Table, drop.Column, drop.Rows)
				}
			} else {
				fmt.Fprintf(out, "  drop table %s — %d row(s)", drop.Table, drop.Rows)
			}
			if needsApproval {
				fmt.Fprint(out, " — needs --approve")
			}
			fmt.Fprintln(out)
		}
	}
	for _, item := range plan.Unsupported {
		fmt.Fprintf(out, "  not applied by this rail: %s\n", item)
	}
	if len(plan.Incompatible) > 0 {
		fmt.Fprintln(out, "  push blocked while the current release is serving:")
		for _, reason := range plan.Incompatible {
			fmt.Fprintf(out, "    %s\n", reason)
		}
		fmt.Fprintln(out, "  --approve does not bypass release compatibility; deploy a compatible transition first")
	}
}

// THE SECRET AND CONFIG PLAN IS GONE, and its absence is the change.
//
// `palbase plan` used to read `.palbase/config.json` and report what a push
// would do to the target's secrets, plus which config kinds the project
// declared. Both halves went with `config/` (2026-08-29):
//
//   - the secret plan compared config/secrets.ts against the target's vault.
//     The check moved EARLIER and got stricter: a name a controller may spell
//     comes from `palbase-stack.d.ts`, generated off the stack, so reading a
//     secret nobody set does not compile.
//   - `declaredConfigKinds` printed "config: flags, storage, …" for declarations
//     the deploy applied to NOTHING — the report the management contract calls
//     "silent AND untrue" (measured 2026-08-17). It had zero callers by the end.
//
// `localSource` stayed: `palbase secret set` still carries values from the stack
// on this machine, and stack_push.go uses it.

func indent(w io.Writer) io.Writer { return &prefixed{w: w, prefix: "  "} }

// ─────────────────────────────────────────────────────────────────────────
// ŞEMA PLANI, VE ONU SORAMAMANIN BİR CEVAP OLMASI (FR-063)
//
// Şema planını yalnız kiracının KENDİSİ hesaplayabilir: sorgu onun kendi
// veritabanına karşı koşar. Bunun bir sonucu var ve o sonuç bu koşunun hedefi
// olan kiracıyı tam olarak kurtarılamaz yapıyordu — servis etmeyen bir kiracı
// şemasını da planlayamaz, ve bu adrese giden DÖRT çağrının her biri
// `palbase plan` ile `palbase push`u ölü kiracıda düşürüyordu:
//
//	plan.go        · plan dosyası HİÇ yazılmıyordu (imaj bölümü başarıyla
//	                 koşup hemen ardından çöpe gidiyordu)
//	requirePlan    · elle yazılmış bir plan dosyasıyla bile push burada ölürdü
//	checkRuntimeSchemaPlan · ölçülemeyen sürüm gevşetmesinden HEMEN SONRA
//	                 aynı ölü pod'a soruyordu
//
// ÖLÇÜT DAR VE BEDAVA: yalnız kiracının KOŞAN SÜRÜMÜ de ölçülemediğinde. İki
// soru aynı pod'a gidiyor; biri cevapsızsa diğerinin sessizliği ikinci bir
// arıza değil, aynı arızanın ikinci yüzüdür. Cevap verebilen bir kiracının
// reddi ise gerçek bir reddir ve tolere EDİLMEZ.
//
// ÖLÇÜLDÜ 2026-09-07, penny (`na1m7lt2m`): kapı gövdesiz bir **HTTP 503**
// döndürüyor — `upstream connect error ... Connection refused` — TAŞIMA HATASI
// DEĞİL. Yalnız `err != nil`'i tolere eden bir gevşetme bu kiracıda atıl
// kalırdı; bu yüzden gevşetme `status != 200` dalını da kapsıyor.
// ─────────────────────────────────────────────────────────────────────────

// SchemaUnmeasured, "bu projeye şemasını soramadım" cevabının BAYTIDIR.
//
// `{}` DEĞİL, ve fark taşıyıcıdır: `{}` "bu proje hiç tablo bildirmiyor"
// demek — ölçülmüş bir SIFIR. İkisini aynı özete katlamak, plan dosyasının
// parmak izini iki AYRI dünya için aynı yapardı, ve push plan yazıldığında
// verilmemiş bir onayı taşırdı.
var SchemaUnmeasured = []byte(`{"unmeasured":true}`)

// schemaPlanResult, şema planı sorusunun üç ayrı cevabını AYRI TUTAR.
type schemaPlanResult struct {
	Body     []byte // parmak izine giren baytlar
	Declared bool   // proje tablo bildiriyor mu
	Measured bool   // cevap gerçekten projeden mi geldi
}

// schemaPlanFromProject, projeye kendi veritabanına karşı şema planını
// hesaplatır. `answering`, kiracının koşan sürümünün ÖLÇÜLEBİLDİĞİ anlamına
// gelir ve toleransın tek anahtarıdır.
func schemaPlanFromProject(ctx context.Context, dir string, target Target, cred Credentials, answering bool) (schemaPlanResult, error) {
	sources, err := ReadSchemaSources(dir)
	switch {
	case errors.Is(err, ErrNoSchema):
		return schemaPlanResult{Body: []byte("{}")}, nil
	case err != nil:
		return schemaPlanResult{}, err
	}
	payload, err := SchemaSourcesBody(sources)
	if err != nil {
		return schemaPlanResult{}, err
	}
	status, body, err := managementCall(ctx, target, cred, http.MethodPost,
		"/v1/management/schema/plan", payload, "application/json")
	switch {
	case err == nil && status == http.StatusOK:
		return schemaPlanResult{Body: body, Declared: true, Measured: true}, nil
	case !answering:
		return schemaPlanResult{Body: SchemaUnmeasured, Declared: true}, nil
	case err != nil:
		return schemaPlanResult{}, err
	default:
		return schemaPlanResult{}, fmt.Errorf("%s answered %d when asked to plan the schema: %s",
			target.Describe(), status, trimBody(body))
	}
}

// unmeasuredNote, plan dosyasına ve ekrana giden TEK cümledir.
//
// Kullanıcı "şema kontrol edilmedi" sanmasın diye ne YAPILACAĞINI da söyler:
// şemanın kendisi kod yüklenirken, proje geri geldikten sonra, yine projenin
// kendi kapısından geçer — burada kaybolan yalnız ÖNİZLEME.
const unmeasuredNote = "schema: this project is not answering, so its schema plan could not be previewed; " +
	"the schema is still checked by the project itself when the code is uploaded"
