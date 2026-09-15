package backend

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// KULLANICIYA BASILAN HER DİZE BUGÜNKÜ ŞEKLİ ÖĞRETİR.
//
// Bir ajan, dokümandan önce hata mesajını okur: `palbase build` bir controller'ı
// reddettiğinde ekrana bastığı cümle, o an ajanın elindeki TEK talimattır.
// Ölçüldü (2026-09-15): `palbase build` "must default-export a @Controller class
// (a controllers/* file decorated with @Controller)" diyordu, `include` reddi
// kullanıcıya `jobs/**/*.ts`i eklemesini öneriyordu — ikisi de bundler'ın
// okumadığı bir düzen (`stack_bundle.go:556`: tek glob, `*.module.ts`). Ve CLI
// tarafında mesaj METNİNİ ölçen tek bir test yoktu.
//
// Ölçülen şey DİZE LİTERALLERİ, yorumlar değil: bir yorum eski düzeni anlatarak
// neden değiştiğini söyleyebilir; kullanıcıya basılan bir dize bunu yapamaz.
//
//   - Go dosyaları `go/parser` ile okunur — yalnız `*ast.BasicLit` STRING'ler.
//   - `devjs/*.js` için Go'da bir JS ayrıştırıcısı yok; satır başı yorum
//     (`//`, `*`) atlanır ve kalan satırların tırnaklı parçaları ölçülür. Kaba
//     ama tek yönde hatalı: bir yorumu dize sanabilir (sahte kırmızı), bir
//     dizeyi kaçırmaz.

// retiredShape — bundler'ın okumadığı düzeni TARİF eden işaretler.
// Dizin adının önünde boşluk, tırnak, parantez ya da köşeli parantez olmalı: önünde
// `/` olan gerçek bir URL yolu (`/webhooks/{name}`) dizin değildir.
var retiredShape = regexp.MustCompile("default-export|(^|[\\s\"'`(\\[])(controllers|jobs|hooks|webhooks)/|\\bas Token\\b")

// saysItIsGone — yasak adı ANLATAN (öğretmeyen) cümle.
var saysItIsGone = regexp.MustCompile(`(?i)there is no|there are no|no longer|retired|removed|nothing reads|no bundler reads|is not discovered|does not exist`)

// jsQuoted — bir JS satırındaki tek/çift/backtick tırnaklı parçalar.
var jsQuoted = regexp.MustCompile("'[^'\\n]*'|\"[^\"\\n]*\"|`[^`\\n]*`")

type shapeHit struct {
	where string
	text  string
}

func goStringLiterals(t *testing.T, path string) (hits []shapeHit, count int) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0) // yorumlar OKUNMAZ
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		count++
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			value = lit.Value
		}
		if retiredShape.MatchString(value) && !saysItIsGone.MatchString(value) {
			hits = append(hits, shapeHit{
				where: fset.Position(lit.Pos()).String(),
				text:  value,
			})
		}
		return true
	})
	return hits, count
}

func jsStringLiterals(t *testing.T, path string) (hits []shapeHit, count int) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for i, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		for _, q := range jsQuoted.FindAllString(line, -1) {
			count++
			inner := q[1 : len(q)-1]
			if retiredShape.MatchString(inner) && !saysItIsGone.MatchString(inner) {
				hits = append(hits, shapeHit{where: path + ":" + strconv.Itoa(i+1), text: inner})
			}
		}
	}
	return hits, count
}

func TestUserFacingMessagesTeachTheModuleShape(t *testing.T) {
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	jsFiles, err := filepath.Glob(filepath.Join("devjs", "*.js"))
	if err != nil {
		t.Fatal(err)
	}

	var all []shapeHit
	scannedFiles, literals := 0, 0
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		hits, n := goStringLiterals(t, f)
		all, literals, scannedFiles = append(all, hits...), literals+n, scannedFiles+1
	}
	for _, f := range jsFiles {
		if strings.HasSuffix(f, ".test.js") {
			continue
		}
		hits, n := jsStringLiterals(t, f)
		all, literals, scannedFiles = append(all, hits...), literals+n, scannedFiles+1
	}

	// SESSİZ SIFIR DEĞİL: hiçbir dosya okunmadıysa boş liste bir yeşil değildir.
	if scannedFiles < 20 || literals < 500 {
		t.Fatalf("scanned %d files / %d string literals — too few to mean anything; the glob or the parser is broken", scannedFiles, literals)
	}

	for _, h := range all {
		t.Errorf("%s teaches a retired layout to the user: %q", h.where, h.text)
	}
}

// Kapının kendi ölçüsü: yalanı yakalar, URL'i ve "bu yok" cümlesini geçirir.
func TestTheMessageShapeGuardItself(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"must default-export a @Controller class (a controllers/* file)", true},
		{"Add to \"include\": jobs/**/*.ts", true},
		{"@Module({ controllers: [TodosController as Token] })", true},
		{"bundled 2 webhook(s) → /webhooks/{stripe, acme}", false},
		{"there is no jobs/ directory and nothing reads one", false},
		{"export the class by name and list it in a module's controllers", false},
	} {
		got := retiredShape.MatchString(tc.text) && !saysItIsGone.MatchString(tc.text)
		if got != tc.want {
			t.Errorf("guard(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
