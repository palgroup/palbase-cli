package backend

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ULAŞILABİLİRLİK KAPISI: bir BİLDİRİM, onu koşturan bir çağrı olmadan yalnızca
// bir dosyadır.
//
// Bu koşuda aynı kusur beş kez çıktı ve hiçbirini var olan bir kapı yakalamadı:
//
//   - `defineStorage` export ediliyor, dokümanda anlatılıyor, llms-full.txt'e
//     giriyor — hiçbir şey okumuyordu (doküman kapısı ANILDIĞINI ölçüyor,
//     ULAŞILDIĞINI değil);
//   - `stack-gen.ts` variant birliği üretiyordu, Go tarafı variant göndermiyordu;
//   - `ratelimit.LoginByAccount` yapılandırılmış, hiçbir rotaya mount edilmemiş;
//   - `ratelimit.SetAccountKey`'in hiçbir çağıranı yok;
//   - ve bu kapının kendi kırmızısı: `generateStackTypes` gömülü bir betiği
//     okuyordu, testleri vardı, ve HİÇBİR KOMUT onu çağırmıyordu — yani üretilen
//     `palbase-stack.d.ts` bir müşteride hiç oluşmuyordu. Kapı onu bulduğu gün
//     bir istisnayla beyan edildi; `palbase build`'e bağlanınca kapı İSTİSNANIN
//     SİLİNMESİNİ istedi ve silindi. Borç ödendi, beyan hayatta kalmadı.
//
// KURAL: bir `devjs/*.js` betiğini ADIYLA okuyan her Go fonksiyonu bir
// KOŞUCUDUR, ve çağrılmayan koşucu ölüdür — ya bir verb'e bağlanmalı ya
// silinmelidir.
//
// Dizin olarak çıkarılan betikler (extractFS(buildCheckFS, "devjs", …)) bu
// kuralın DIŞINDA: onlar build-check.js'in require ettiği kütüphaneler, kendi
// başına koşan şeyler değil. İlk yazımı hepsini kapsıyordu ve dördü için
// yanlış kırmızı verdi — ve yanlış kırmızı gerçek kırmızıyı gizler, ki bu
// kapının bütün varlık sebebi tam olarak gerçek kırmızıyı görünür kılmak.
func TestEveryEmbeddedScriptHasAReachableRunner(t *testing.T) {
	scripts, err := filepath.Glob("devjs/*.js")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("devjs betikleri bulunamadı: %v", err)
	}

	// Paketin üretim kaynağı (testler hariç): hem okuyucuyu bulmak hem
	// çağıranı aramak için.
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, rErr := os.ReadFile(f)
		if rErr != nil {
			t.Fatal(rErr)
		}
		sources[f] = string(raw)
	}

	funcDecl := regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?([A-Za-z_][A-Za-z0-9_]*)\(`)

	checked := 0
	for _, script := range scripts {
		base := filepath.Base(script)
		// Betiği ADIYLA okuyan fonksiyonu bul. Yoksa bu betik bir koşucu değil,
		// dizinle birlikte seyahat eden bir kütüphanedir — kuralın dışında.
		var readerFn, readerFile string
		for file, src := range sources {
			at := strings.Index(src, `ReadFile("devjs/`+base+`")`)
			if at < 0 {
				continue
			}
			for _, m := range funcDecl.FindAllStringSubmatchIndex(src[:at], -1) {
				readerFn = src[m[2]:m[3]]
			}
			readerFile = file
			break
		}
		if readerFn == "" {
			continue
		}
		checked++

		t.Run(base, func(t *testing.T) {

			if calls := countCalls(sources, readerFn); calls == 0 {
				t.Errorf(
					"%s'i okuyan %s (%s) hiçbir yerden ÇAĞRILMIYOR — betik gömülü, fonksiyon yazılı, "+
						"test edilmiş, ve hiçbir komut onu koşturmuyor. Ya bir verb'e bağlayın ya silin.",
					base, readerFn, readerFile,
				)
			}
		})
	}

	// Kapının bir şeye baktığını kanıtla: hiç koşucu bulunmadıysa kural
	// sessizce hiçbir şey ölçmüyor demektir.
	if checked == 0 {
		t.Fatal("adıyla betik okuyan hiçbir fonksiyon bulunamadı — kapı ölçecek bir şey görmüyor")
	}
}

// countCalls, bir fonksiyonun kendi TANIMI dışındaki çağrılarını sayar.
//
// Tanımı ayırt etmek için SATIRIN başına değil, adın HEMEN ÖNÜNE bakar. İlk
// yazımı "satır `func` ile başlıyorsa tanımdır" diyordu ve tek satırlık bir
// fonksiyon (`func wire() error { return runner(...) }`) o kuralda çağrı
// SAYILMIYORDU — kapının negatif kontrolü bunu ortaya çıkardı. Gerçek bir
// tek-satır bağlama da aynı şekilde gözden kaçardı, yani kapı çözülmüş bir
// borcu çözülmemiş göstermeye devam ederdi.
func countCalls(sources map[string]string, fn string) int {
	calls := 0
	callRE := regexp.MustCompile(`\b` + regexp.QuoteMeta(fn) + `\(`)
	// `func Ad(` ya da `func (r T) Ad(` — yalnız bunlar tanımdır.
	declRE := regexp.MustCompile(`func\s+(?:\([^)]*\)\s*)?$`)
	for _, src := range sources {
		for _, loc := range callRE.FindAllStringIndex(src, -1) {
			from := loc[0] - 40
			if from < 0 {
				from = 0
			}
			if declRE.MatchString(src[from:loc[0]]) {
				continue
			}
			calls++
		}
	}
	return calls
}
