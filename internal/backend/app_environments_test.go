package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// YETİM ORTAM KLASÖRÜ BUILD'İ KIRAR — ve bu, kaldırılan tek dosyanın öteki
// biçimidir. Xcode 16 senkronize grupları `Palbase/Generated` altındaki HER
// dosyayı derliyor, yani bir ortam yeniden adlandırıldığında eskisinin klasörü
// aynı sembolleri ikinci kez üretiyor: "Multiple commands produce …
// PalbaseGenerated.stringsdata". Gerçek müşteri uygulamasında ölçüldü
// (07.09.2026, centauri): `Palbase/Generated/centauri` bir yeniden adlandırmayı
// aşmıştı; klasör kenara alınınca build GEÇTİ.
func TestOrphanedEnvironmentFolderIsRemovedButForeignFilesAreNot(t *testing.T) {
	root := t.TempDir()
	gen := filepath.Join(root, "palbase", "environments")
	for _, env := range []string{"main", "centauri", "handwritten"} {
		if err := os.MkdirAll(filepath.Join(gen, env), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gen, env, "PalbaseGenerated.swift"), []byte("// generated"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Bu klasörde BİZİM yazmadığımız bir dosya var: dokunulmamalı.
	if err := os.WriteFile(filepath.Join(gen, "handwritten", "Extra.swift"), []byte("// mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := removeStaleEnvironmentDirs(root, []string{"main"}, &out); err != nil {
		t.Fatalf("removeStaleEnvironmentDirs: %v", err)
	}

	if _, err := os.Stat(filepath.Join(gen, "main")); err != nil {
		t.Fatal("bildirilen ortam silindi — projenin kendi istemcisi gitti")
	}
	if _, err := os.Stat(filepath.Join(gen, "centauri")); !os.IsNotExist(err) {
		t.Fatal("yetim ortam klasörü DURUYOR — build 'Multiple commands produce' ile kırılmaya devam eder")
	}
	if _, err := os.Stat(filepath.Join(gen, "handwritten", "Extra.swift")); err != nil {
		t.Fatal("bizim yazmadığımız dosya SİLİNDİ — yazıcı üretemediğini silmemeli")
	}
	if !strings.Contains(out.String(), "handwritten") {
		t.Fatalf("dokunulmayan klasör ADLANDIRILMADI: %q", out.String())
	}
}

// TEMİZLİĞİN ÜRETİMDE BİR ÇAĞIRANI OLMALI.
//
// Fonksiyonun kendisini sınamak yetmez: çağrılmayan bir temizlik, yazılmamış
// bir temizliktir. Bu kapı, üretim yolunun onu gerçekten çağırdığını KAYNAKTAN
// okur — negatif kontrolde çağrı kaldırıldığında kırmızı olur.
func TestOrphanCleanupHasAProductionCaller(t *testing.T) {
	b, err := os.ReadFile("app_environments.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, line := range strings.Split(string(b), "\n") {
		code, _, _ := strings.Cut(line, "//")
		if strings.Contains(code, "removeStaleEnvironmentDirs(") && !strings.Contains(code, "func removeStaleEnvironmentDirs") {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("removeStaleEnvironmentDirs'ın üretim çağıranı YOK — yetim ortam klasörü build'i kırmaya devam eder")
	}
}

// APPLE CHECKOUT'UNUN KANITI YAPILANDIRMA DOSYASI DEĞİL, XCODE PROJESİDİR.
//
// `apple` bir zamanlar yalnız "ios yuva dosyası var mı" diye soruyordu ve bir
// BACKEND deposu o dosyayı meşru biçimde taşıyabilir: biri orada bir kez
// `palbase link --platform ios` koşmuş ve commit'lemiştir. Gerçek müşteri
// deposunda ölçüldü (08.09.2026, centauri): her `palbase push`
//
//	the push landed, but the client could not be regenerated: … the
//	palbackend-ios checkout is not resolved for this project yet
//
// ile bitiyordu — çünkü uygulama AYRI bir depoda ve burada çözülecek bir Xcode
// projesi hiç yok. Üretici, asla koşamayacağı yerde koşmaya zorlanıyordu.
func TestApplePlatformNeedsAnXcodeProjectNotJustAConfig(t *testing.T) {
	// `t.Chdir` dizini değiştirir VE testin sonunda geri alır — elle bir
	// `defer os.Chdir(wd)` yazmak, dönüşü kontrol edilmeyen bir çağrı bırakır.
	t.Chdir(t.TempDir())

	// Yuva dosyası VAR, Xcode projesi YOK — bir backend deposunun hâli.
	if err := os.MkdirAll(EnvDir("main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath("main", "ios"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, apple, _ := linkedPlatforms(); apple {
		t.Fatal("Xcode projesi olmayan bir checkout Apple sayıldı — her push kusur satırıyla biter")
	}

	// Aynı checkout'a bir Xcode projesi koy: ARTIK Apple checkout'udur ve
	// çözülmemiş bir proje GERÇEK bir hatadır ("build once in Xcode").
	if err := os.MkdirAll("white-label.xcodeproj", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, apple, _ := linkedPlatforms(); !apple {
		t.Fatal("Xcode projesi olan bir checkout Apple SAYILMADI — istemci hiç üretilmez")
	}
}

// AN APPLE LINK MUST NOT DELETE THE WEB CHECKOUT'S COMMITTED CLIENT.
//
// `removeStaleEnvironmentDirs` runs on the Apple branch and deletes any
// environment directory holding only files Palbase generated — and the web
// client and its config ARE Palbase-generated. `local` leaves the caller's set
// as soon as the stack is down (`palbase stop` is enough), so a checkout that is
// both a web and an iOS one would lose `local/palbe.gen.ts` to an `ios` link,
// with `palbase/client.ts` still re-exporting it.
func TestAnAppleSweepKeepsTheLocalEnvironmentsWebClient(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, filepath.FromSlash(EnvDir(localEnvName)))
	require.NoError(t, os.MkdirAll(local, 0o755))
	for _, name := range []string{"palbe.gen.ts", "web-config.json", "openapi.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(local, name), []byte("x"), 0o644))
	}

	// The caller's set after `palbase stop`: the cloud environment only.
	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main"}, &out))

	require.FileExists(t, filepath.Join(local, "palbe.gen.ts"),
		"an iOS link deleted the web client of an environment it simply could not see")

	// NEGATIVE CONTROL: a cloud environment the project really dropped still goes,
	// or this guard would just disable the sweep.
	gone := filepath.Join(root, filepath.FromSlash(EnvDir("retired-env")))
	require.NoError(t, os.MkdirAll(gone, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gone, "openapi.json"), []byte("x"), 0o644))
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main"}, &out))
	require.NoDirExists(t, gone, "an environment the project no longer has survived")
}

// A CUSTOM `--out` NAME MUST NOT MAKE A STALE ENVIRONMENT UNDELETABLE.
//
// `isGeneratedEnvironmentFile` derived its list from the layout's fixed names, so
// a client written under a name the person chose (`--out api.gen.ts`) read as
// "not ours" and protected the whole directory. Two environments' clients then
// sat under `palbase/environments` — the "Multiple commands produce" failure the
// sweep exists to prevent.
func TestASweepRemovesAnEnvironmentWithACustomClientName(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, filepath.FromSlash(EnvDir("gone")))
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "openapi.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "api.gen.ts"), []byte("export {}"), 0o644))

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main"}, &out))
	require.NoDirExists(t, stale, "a stale environment survived because its client had a custom name")

	// NEGATIVE CONTROL: something that is NOT ours still protects the directory —
	// a writer must not delete what it cannot reproduce.
	theirs := filepath.Join(root, filepath.FromSlash(EnvDir("mine")))
	require.NoError(t, os.MkdirAll(theirs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(theirs, "NOTES.md"), []byte("mine"), 0o644))
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main"}, &out))
	require.DirExists(t, theirs, "a directory holding somebody's own file was deleted")
}
