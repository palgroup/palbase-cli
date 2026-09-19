package backend

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
// KURAL: bir `devjs/*.js` betiğini gövdesinde ADIYLA (string literal, go/ast)
// anan her üretim Go fonksiyonu bir KOŞUCUDUR, ve çağrılmayan koşucu ölüdür —
// ya bir verb'e bağlanmalı ya silinmelidir. Bir betiği yalnız kendi `*.test.js`
// dosyası çağırıyorsa o betik ölüdür (S-1).
//
// Dizin olarak çıkarılan betikler (extractFS(buildCheckFS, "devjs", …)) bu
// kuralın DIŞINDA: onlar build-check.js'in require ettiği kütüphaneler, kendi
// başına koşan şeyler değil. İlk yazımı hepsini kapsıyordu ve dördü için
// yanlış kırmızı verdi — ve yanlış kırmızı gerçek kırmızıyı gizler, ki bu
// kapının bütün varlık sebebi tam olarak gerçek kırmızıyı görünür kılmak.
func TestEveryEmbeddedScriptHasAReachableRunner(t *testing.T) {
	findings, checked := reachabilityFindings(t, ".")
	// Kapının bir şeye baktığını kanıtla: hiç koşucu bulunmadıysa kural
	// sessizce hiçbir şey ölçmüyor demektir.
	if checked == 0 {
		t.Fatal("adıyla betik okuyan hiçbir fonksiyon bulunamadı — kapı ölçecek bir şey görmüyor")
	}
	for _, f := range findings {
		t.Error(f)
	}
}

// reachabilityFindings is the gate's judgement over root: every devjs script
// that is not a test must have a reachable runner — a production Go function
// that NAMES it in a string literal (`"x.js"` or `"devjs/x.js"`, found in the
// syntax tree, so a comment naming it does not count) and is called from
// somewhere — or be required by name by another PRODUCTION script (a library
// build-check.js loads). A test file is never the caller (S-1: strings_scan.js
// and build-check.js were green only through their own tests).
func reachabilityFindings(t *testing.T, root string) (findings []string, checked int) {
	t.Helper()
	scripts, err := filepath.Glob(filepath.Join(root, "devjs", "*.js"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("devjs betikleri bulunamadı: %v", err)
	}
	sort.Strings(scripts)
	goFiles, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(goFiles)
	sources := map[string]string{}
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, rErr := os.ReadFile(f)
		if rErr != nil {
			t.Fatal(rErr)
		}
		sources[f] = string(raw)
		file, pErr := parser.ParseFile(fset, f, raw, 0)
		if pErr != nil {
			t.Fatalf("%s: %v", f, pErr)
		}
		parsed[f] = file
	}
	for _, script := range scripts {
		base := filepath.Base(script)
		if strings.HasSuffix(base, ".test.js") {
			continue // the directory's own tests
		}
		checked++
		readerFn, readerFile := readerOf(goFiles, parsed, base)
		if readerFn == "" {
			if !requiredByAnotherScript(t, scripts, base) {
				findings = append(findings, fmt.Sprintf("%s'i ADIYLA anan bir üretim Go fonksiyonu YOK ve onu çağıran başka bir "+
					"üretim betiği de yok — gömülü, ama hiçbir yoldan koşulamıyor. Ya bir verb'e bağlayın ya silin.", base))
			}
			continue
		}
		if countCalls(sources, readerFn) == 0 {
			findings = append(findings, fmt.Sprintf("%s'i okuyan %s (%s) hiçbir yerden ÇAĞRILMIYOR — betik gömülü, fonksiyon "+
				"yazılı, test edilmiş, ve hiçbir komut onu koşturmuyor. Ya bir verb'e bağlayın ya silin.", base, readerFn, readerFile))
		}
	}
	return findings, checked
}

// readerOf is the first production function (files in order) whose BODY
// carries the script's name as a string literal — `"x.js"` or `"devjs/x.js"`.
func readerOf(goFiles []string, parsed map[string]*ast.File, base string) (string, string) {
	for _, f := range goFiles {
		file, ok := parsed[f]
		if !ok {
			continue
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil && (v == base || v == "devjs/"+base) {
						found = true
					}
				}
				return !found
			})
			if found {
				return fd.Name.Name, f
			}
		}
	}
	return "", ""
}

// requiredByAnotherScript, betiğin devjs içindeki bir BAŞKA ÜRETİM betiği
// tarafından adıyla çağrılıp çağrılmadığını söyler. Test dosyaları (`*.test.js`)
// SAYILMAZ: bir betiği yalnız kendi testi çağırıyorsa o betik hiçbir komuttan
// koşmuyor demektir (S-1).
//
// Ad TIRNAK İÇİNDE aranır: `require('./x')`, `require('./x.js')` ya da
// `path.join(__dirname, 'x.js')` (build-check.js `extract_meta.js`'i böyle
// koşturur). Yorum içinde geçen bir ad — build-check.js kendi kütüphanelerini
// düzyazıda sayar — kanıt değildir; çağıran bir ifade gerekir.
func requiredByAnotherScript(t *testing.T, scripts []string, base string) bool {
	t.Helper()
	stem := strings.TrimSuffix(base, ".js")
	for _, other := range scripts {
		name := filepath.Base(other)
		if name == base || strings.HasSuffix(name, ".test.js") {
			continue
		}
		raw, err := os.ReadFile(other)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, spelling := range []string{
			`'` + base + `'`, `"` + base + `"`,
			`'./` + stem + `'`, `"./` + stem + `"`,
			`'./` + base + `'`, `"./` + base + `"`,
		} {
			if strings.Contains(src, spelling) {
				return true
			}
		}
	}
	return false
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

// NEGATIVE CONTROL (FR-043): the gate must go red for the two shapes S-1 found
// it blind to — a script whose only caller is its own test file, and a reader
// that names the script in a literal (`filepath.Join(dir, "x.js")`, no
// ReadFile) and is never called — and stay green for a reader that is called.
func TestReachabilityGate_NegativeControl(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("devjs/orphan.js", "module.exports = 1;\n")
	write("devjs/lonely.js", "module.exports = 2;\n")
	write("devjs/lonely.test.js", "require('./lonely');\n")
	write("devjs/used.js", "module.exports = 3;\n")
	write("a.go", "package x\n\nimport \"path/filepath\"\n\n"+
		"// orphan.js is named here in a comment only for the reader below.\n"+
		"func runOrphan(dir string) string { return filepath.Join(dir, \"orphan.js\") }\n\n"+
		"func runUsed(dir string) string { return filepath.Join(dir, \"used.js\") }\n\n"+
		"func verb(dir string) string { return runUsed(dir) }\n")
	findings, checked := reachabilityFindings(t, root)
	joined := strings.Join(findings, "\n")
	if checked != 3 {
		t.Fatalf("checked = %d, want 3 (orphan, lonely, used): %s", checked, joined)
	}
	if !strings.Contains(joined, "orphan.js") || !strings.Contains(joined, "runOrphan") {
		t.Errorf("an uncalled reader found by its literal was not reported:\n%s", joined)
	}
	if !strings.Contains(joined, "lonely.js") {
		t.Errorf("a script whose only caller is its own test file was not reported:\n%s", joined)
	}
	if strings.Contains(joined, "used.js") {
		t.Errorf("a called reader was reported:\n%s", joined)
	}
}
