package storage

import (
	"os"
	"strings"
	"testing"
)

// CLI YARDIM METNİ DE OKUNAN BİR YÜZEYDİR — ve bu paketin başlığı aylarca
// davranışıyla çelişti: "they read/write config/storage.ts … The CLI is the
// SOLE author of config/storage.ts" diyordu, oysa komutlar yığının yönetim
// API'sine gidiyordu ve o dosyaya hiç dokunmuyorlardı. Ölü bir `configPath`
// sabiti de yanında duruyordu. Hiçbir şey ölçmüyordu.
//
// Kural, JSDoc kapısıyla aynı: METNİN İDDİA ETTİĞİ ŞEYİN KODDA KARŞILIĞI
// OLACAK. Bir dosyadan söz ediyorsa o dosyayı okuyan/yazan kod bulunacak;
// "yığında yaşar" diyorsa yığın ucunu çağıran kod bulunacak.
func TestStorageHelpDescribesWhatTheCommandActuallyDoes(t *testing.T) {
	source, err := os.ReadFile("storage.go")
	if err != nil {
		t.Fatalf("kapı kaynağı okuyamadı: %v", err)
	}
	code := string(source)

	help := Cmd(Resolvers{}).Long
	if help == "" {
		t.Fatal("storage grubunun Long metni boş — kapı ölçecek bir şey bulamadı")
	}

	// İDDİA: "yığında yaşar". KARŞILIĞI: yığın ucunu çağıran kod.
	if strings.Contains(strings.ToUpper(help), "ON THE STACK") {
		if !strings.Contains(code, "/v1/management/storage/buckets") {
			t.Error("yardım 'ON THE STACK' diyor ama kod yığın ucunu çağırmıyor")
		}
	}

	// İDDİA: bir config dosyasını YAZDIĞI. Bu iddia yalnız geçmiş zamanda
	// ("used to be") meşru; şimdiki zamanda kod o dosyaya dokunmalı.
	mentionsConfigNow := strings.Contains(help, "config/storage.ts") &&
		!strings.Contains(help, "used to be")
	if mentionsConfigNow && !strings.Contains(code, "os.WriteFile") {
		t.Error("yardım config/storage.ts yazdığını ima ediyor ama kod hiçbir dosya yazmıyor")
	}

	// KODDA ÖLÜ SABİT KALMASIN: bir yola işaret eden ama kullanılmayan sabit,
	// yardım metnini yanlış hatırlayan bir sonraki okuyucunun kanıtı olur.
	if strings.Contains(code, `configPath = "config/storage.ts"`) &&
		strings.Count(code, "configPath") < 2 {
		t.Error("configPath sabiti tanımlı ama hiçbir yerde kullanılmıyor — ölü kablo")
	}
}
