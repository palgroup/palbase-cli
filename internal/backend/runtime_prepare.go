package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// PlanRef, plan dosyasının TELDEKİ hâli (C-6). Sunucu ona güvenmez: parmak izini
// kendisi yeniden hesaplar ve `running`i canlı runtime'a sorar.
type PlanRef struct {
	Running          string `json:"running"`
	Target           string `json:"target"`
	SchemaPlanDigest string `json:"schemaPlanDigest"`
	BundleDigest     string `json:"bundleDigest"`
	Fingerprint      string `json:"fingerprint"`
}

// Ref, plan dosyasının sunucuya giden alt kümesi.
func (p PlanFile) Ref() PlanRef {
	return PlanRef{
		Running:          p.SDK.Running,
		Target:           p.SDK.Target,
		SchemaPlanDigest: p.SchemaPlanDigest,
		BundleDigest:     p.BundleDigest,
		Fingerprint:      p.Fingerprint,
	}
}

// Wired by the CLI's cloud composition root; a project key cannot act as an
// account credential against the control plane.
var CloudRuntimePreparer func(ctx context.Context, tenantURL, sdkVersion string, plan PlanRef) error

// requirePlan, plan kapısı (FR-046, FR-047).
//
// İKİ AYRI SORU, VE İKİSİ DE GEREKLİ. Plan VAR MI — yoksa `ErrNoPlan`, ve red
// kiracıya tek bir istek atmadan verilir. Plan HÂLÂ DOĞRU MU — şimdiki durumla
// yeniden hesaplanır ve ayrışan her şey ADIYLA söylenir. İkincisi olmadan plan
// bir tören olurdu: kullanıcı bir dünya için onay verir, push başkasına gider.
//
// YENİDEN HESAPLANAN ŞEY, PLANIN HESAPLADIĞININ AYNISI OLMAK ZORUNDA — aynı
// bundle özeti, aynı şema-planı BAYTININ özeti (ekrana basılan metnin değil).
// bundleRoot, BU PUSH'UN AZ ÖNCE DERLEDİĞİ ürünlerin kökü. Kapı checkout'u
// ölçemez: 0.61.1'den beri orada ölçülecek hiçbir ürün yok (BundleDigest'in
// kendi yorumu), yani checkout'u ölçen bir kapı planla ASLA eşleşmez.
func requirePlan(ctx context.Context, dir, bundleRoot string, target Target, cred Credentials) (PlanFile, error) {
	saved, err := ReadPlanFile(dir)
	if err != nil {
		return PlanFile{}, err
	}
	current := saved
	current.SDK.Target = installedBackendVersion(dir)
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	running, perr := projectSDKVersion(probeCtx, target, cred)
	cancel()
	if perr != nil {
		running = ""
	}
	if running != "" {
		current.SDK.Running = running
	}

	bundle, err := BundleDigest(bundleRoot)
	if err != nil {
		return PlanFile{}, err
	}
	current.BundleDigest = bundle

	// AYNI GEVŞETME, AYNI ÖLÇÜTLE (FR-063). Bu satır olmadan kapı 1/3/5'in
	// hepsi kullanıcının klavyesinden erişilemez kalıyordu: elle yazılmış doğru
	// bir plan dosyasıyla bile `push`, `prepareCloudRuntime`'a hiç varmadan tam
	// burada ölürdü. Karar ölçebilen tarafa devredilir.
	schema, serr := schemaPlanFromProject(ctx, dir, target, cred, running != "")
	if serr != nil {
		return PlanFile{}, fmt.Errorf("%w; no image or code was changed", serr)
	}
	sum := sha256.Sum256(schema.Body)
	current.SchemaPlanDigest = hex.EncodeToString(sum[:])

	if reasons := StaleReasons(saved, current); len(reasons) > 0 {
		return PlanFile{}, fmt.Errorf("plan is stale: %s; run `palbase plan`", strings.Join(reasons, "; "))
	}
	return saved, nil
}

func prepareStackRuntime(ctx context.Context, dir, bundleRoot string, target Target, cred Credentials, approve bool, out io.Writer) error {
	// TEK SORU: bu adres BU BULUTUN bir projesi mi? `OnThisMachine()` de
	// sorulurdu ve gereksizdi — `CloudProjectAddress` `<ref>.<PublicHost>`
	// eşleştiriyor, yani bir localhost adresi zaten hiçbir zaman eşleşmez. İki
	// soru sormak, ikincisinin cevabını birincisinin gölgelemesi demekti: bir
	// kiracıya makinesinden port-forward ile bakan biri, plan kapısını sessizce
	// atlardı.
	if !isCloudProjectAddress(target.URL) {
		return nil
	}
	plan, err := requirePlan(ctx, dir, bundleRoot, target, cred)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "applying plan %s\n", plan.Fingerprint[:12])
	return prepareCloudRuntime(ctx, dir, target, cred, approve, out, plan)
}

func prepareCloudRuntime(ctx context.Context, dir string, target Target, cred Credentials, approve bool, out io.Writer, plan PlanFile) error {
	want := installedBackendVersion(dir)
	if want == "" {
		return errors.New("cannot determine the SDK required by this checkout")
	}
	// ÖLÇÜLEMEYEN SÜRÜM BİR CEVAPTIR, HATA DEĞİL (FR-063) — VE BU SATIR
	// SUNUCUDAKİ ÜÇ DÜZELTMEYİ KULLANICININ KLAVYESİNDEN ERİŞİLEMEZ KILIYORDU.
	//
	// `projectSDKVersion` kiracının KENDİ `/.well-known/palbase.json`'ına gidiyor.
	// CrashLoop'taki bir kiracıda o adres kapalı, yani push platforma TEK BİR
	// İSTEK atmadan düşüyordu. Operatör ve düzlem tarafında servis etmeyen
	// kiracının yükseltilebilmesi için yapılan her şey, tam burada, çağrılmadan
	// önce ölüyordu.
	//
	// KARAR ÖLÇEBİLEN TARAFA DEVREDİLİR. Düzlem kiracının damgasını ve imaj
	// başlarını okuyabiliyor — koşan bir runtime istemeden. "Bilinmiyor" hâlinde
	// onu çağırmak güvenli: `ensureTenantImage` hedefteki kiracıya, ön kontrol
	// ve expand göçü geçene kadar DOKUNMUYOR.
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	running, err := projectSDKVersion(probeCtx, target, cred)
	cancel()
	if err != nil {
		running = ""
	}
	if running != "" && running == want {
		return nil
	}
	if running == "" {
		fmt.Fprintf(out, "runtime: this project is not answering, so its running SDK could not be measured; "+
			"the platform decides from the migration stamp\n")
	}
	// A schema refusal must not replace a healthy image as a side effect.
	//
	// VE ÖLÇÜLEMEYEN BİR ŞEMA PLANI BİR RED DEĞİLDİR (FR-063): bu çağrı da
	// kiracının kendi adresine gidiyordu ve hemen yukarıdaki gevşetmeden SONRA,
	// aynı ölü pod'a sorarak push'u düşürüyordu.
	if err := checkRuntimeSchemaPlan(ctx, dir, target, cred, approve, out, running != ""); err != nil {
		return err
	}
	if CloudRuntimePreparer == nil {
		return errors.New("this CLI cannot prepare cloud runtimes; update the CLI before pushing a different SDK")
	}
	fmt.Fprintf(out, "runtime: preparing @palbase/backend %s (currently %s)\n", want, running)
	swapCtx, cancelSwap := context.WithTimeout(ctx, 5*time.Minute)
	err = CloudRuntimePreparer(swapCtx, target.URL, want, plan.Ref())
	cancelSwap()
	if err != nil {
		// PLATFORMUN REDDİ KIRPILMADAN BASILIR (FR-049, FR-051). Ölçülmüş ders:
		// çerçeve başta, ASIL CÜMLE sonda — ve bir gövde kırpıcısından geçen
		// teşhis teşhis değildir. Rapor ekrana tam olarak gider; dönen hata
		// kısadır çünkü okunacak şey yukarıda.
		if strings.Contains(err.Error(), `{"target"`) {
			renderUpgradeReport(out, []byte(err.Error()))
			return errors.New("runtime preparation failed before code upload (see the report above)")
		}
		return fmt.Errorf("runtime preparation failed before code upload: %w", err)
	}
	// DOĞRULAMA YİNE ŞART: yükseltme BİTTİĞİNDE kiracı cevap veriyor olmalı.
	// Yukarıdaki gevşetme yalnız BAŞLAMADAN önceki ölçüme ait — sonrasında
	// "ölçemedim", yükseltmenin işe yaramadığı demektir.
	probeCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	running, err = projectSDKVersion(probeCtx, target, cred)
	cancel()
	if err != nil || running != want {
		return fmt.Errorf("runtime preparation was not verified: required %s, observed %q; no code was uploaded", want, running)
	}
	fmt.Fprintf(out, "runtime: verified @palbase/backend %s before code upload\n", running)
	return nil
}

func checkRuntimeSchemaPlan(ctx context.Context, dir string, target Target, cred Credentials, approve bool, out io.Writer, answering bool) error {
	schema, err := schemaPlanFromProject(ctx, dir, target, cred, answering)
	if err != nil {
		return fmt.Errorf("%w; no image or code was changed", err)
	}
	if !schema.Declared || !schema.Measured {
		// SORULAMAYAN ŞEMA ENGEL DEĞİLDİR. Yıkıcı bir değişiklik varsa onu
		// projenin KENDİ kapısı kod yüklenirken yakalar — ki o an proje geri
		// gelmiş olur. Burada engellemek, yalnız ölü kiracıyı diriltmeyi
		// engellerdi.
		return nil
	}
	body := schema.Body
	var plan schemaPlanWire
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields["in_sync"] == nil {
		return errors.New("schema preflight did not return a schema plan; no image or code was changed")
	}
	if err := json.Unmarshal(body, &plan); err != nil {
		return fmt.Errorf("invalid schema preflight; no image or code was changed: %w", err)
	}
	blocked := len(plan.Incompatible) > 0 || len(plan.Unsupported) > 0
	if !approve {
		for _, drop := range plan.Destructive {
			if drop.Rows > 0 && (drop.Column == "" || drop.NonNull == nil || *drop.NonNull > 0) {
				blocked = true
			}
		}
	}
	if blocked {
		renderSchemaPlan(out, body)
		return errors.New("schema preflight refused the push; no image or code was changed")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────
// PLANIN RUNTIME BÖLÜMÜ (FR-045)
//
// `palbase plan`, kiracının SDK sürümü değişecekse yükseltmenin NE YAPACAĞINI
// sorar: hangi modül hangi göçe çıkacak, ve ön kontroller ne diyor. Soru
// PLATFORMA gider çünkü cevabı yalnız o verebilir — göçler hedef imajın kendi
// binary'siyle, servis eden pod'un içinde koşuyor.
//
// BİR DEĞİŞKEN, ÇÜNKÜ BAĞ TERS YÖNDE: `internal/backend` bulut kontrol
// düzlemini tanımaz ve tanımamalı (aynı kod kendi kendine barındırılan bir
// yığına da push atıyor). Bağlamayı `cmd/palbase` yapar; bağlamazsa plan eski
// satırını basar ve hiçbir şey kırılmaz.
// ─────────────────────────────────────────────────────────────────────────

// ErrNotACloudProject: hedef bu bulutun bir projesi değil (kendi kendine
// barındırılan bir yığın, ya da `palbase start` ile koşan yerel bir kopya).
//
// HATA, SESSİZ BİR NİL DEĞİL: "sormadım" ile "sordum, değişen bir şey yok"
// ayrı cevaplardır ve ikincisi planın gövdesine girer.
var ErrNotACloudProject = errors.New("not a project on this cloud")

// CloudRuntimePlanner, hedef sürüme geçişin planını platformdan alır.
var CloudRuntimePlanner func(ctx context.Context, tenantURL, sdkVersion string) (json.RawMessage, error)

// writeRuntimePlan, planın runtime bölümünü basar.
//
// SAF BİÇİMLEYİCİ: ağ yok, karar yok. `changed` false ise hiçbir şey basmaz —
// zaten hedefte olan bir kiracıya yükseltme anlatmak, planı gürültüye çevirirdi.
func writeRuntimePlan(w io.Writer, section json.RawMessage) {
	var rp struct {
		Running string `json:"running"`
		Target  string `json:"target"`
		Changed bool   `json:"changed"`
		Modules []struct {
			Module       string `json:"module"`
			ExpandFrom   int    `json:"expandFrom"`
			ExpandTo     int    `json:"expandTo"`
			ContractFrom int    `json:"contractFrom"`
			ContractTo   int    `json:"contractTo"`
		} `json:"modules"`
		Prechecks *struct {
			Outcome string `json:"outcome"`
			Items   []struct {
				Module    string `json:"module"`
				Migration string `json:"migration"`
				Severity  string `json:"severity"`
				Message   string `json:"message"`
				Count     int64  `json:"count"`
			} `json:"items"`
		} `json:"prechecks"`
	}
	if err := json.Unmarshal(section, &rp); err != nil || !rp.Changed {
		return
	}
	fmt.Fprintln(w, "runtime")
	// CÜMLE MAKİNEYİ ADLANDIRMAZ. İlk hâli "inside the running pod" diyordu ve
	// docs kapısı onu yakaladı (`cloud-boundary.test.ts`): "pod" bulutun
	// dağıtım birimi, ve kullanıcı yüzeyi bulutun nasıl çalıştığını anlatmaz.
	// Kullanıcının bilmesi gereken şey makine değil GÜVENCE: göç, proje HÂLÂ
	// SERVİS EDERKEN ve yeni sürüm devralmadan ÖNCE koşar; devir sırasında
	// istekler düşmez, BEKLER.
	//
	// SÜRE ÖLÇÜLDÜ, TAHMİN EDİLMEDİ. Cümle "a restart of seconds" diyordu;
	// canlıda iki kiracıda ölçülen devir 73 ve 82 saniye, dışarıdan bekleyen
	// en uzun istek 101,8 saniye (07.09.2026, 1 istek/sn). "Saniyeler" demek,
	// müşteriye bir dakikayı saniye diye satmaktı — ve o cümleye bakıp bakım
	// penceresi planlayan biri yanılırdı. Sayı MİMARİDEN geliyor: kiracının
	// veritabanı da aynı birimde yaşıyor, yani devir onu da yeniden başlatır.
	fmt.Fprintf(w, "  %s → %s (migrations run against the live project before the new version takes over; "+
		"the switch restarts it — measured about a minute and a half — and requests wait rather than fail)\n", rp.Running, rp.Target)
	for _, m := range rp.Modules {
		fmt.Fprintf(w, "  %s: expand %d→%d, contract %d→%d\n", m.Module, m.ExpandFrom, m.ExpandTo, m.ContractFrom, m.ContractTo)
	}
	if rp.Prechecks == nil {
		return
	}
	for _, it := range rp.Prechecks.Items {
		fmt.Fprintf(w, "  %s · %s/%s · %d · %s\n", it.Severity, it.Module, it.Migration, it.Count, it.Message)
	}
	// ENGEL BİR PLATFORM KUSURUDUR VE ÖYLE SÖYLENİR. Müşteriden eylem
	// istenmez: çare bizim tarafımızdadır ve bu satır onu yazan tek yerdir.
	if rp.Prechecks.Outcome == "blocked" {
		fmt.Fprintln(w, "  BLOCKED: a platform-side check refused this upgrade; nothing was changed and "+
			"the platform team has been notified — there is no customer action")
	}
}
