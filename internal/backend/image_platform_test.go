package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// palgroup/palbase#3 — yerel yığın, Apple Silicon'da PostgreSQL'i qemu altında
// koşturuyordu: bir kez önbelleğe girmiş amd64 imaj aylarca kullanıldı ve tek
// belirtisi backend'lerin `exit code 2` ile ölüp crash recovery'ye düşmesiydi
// (bir günde SEKİZ kez).
//
// İKİ KUSUR BİRLEŞİYORDU, ölçüldü (arm64 daemon, docker 24.0.2 / compose 5.5.1):
//
//	docker image inspect <amd64 imaj>              → exit 0   ("var")
//	docker image inspect --format {{.Architecture}} → amd64
//	compose (platform anahtarı YOK)                 → konteyner `x86_64` dedi
//	compose (platform: linux/arm64)                 → eşleşmeyeni kullanmadı, çekti
//
// Yani varlık denetimi mimariden habersizdi ve compose'un pinlenecek bir
// platformu yoktu.
func dockerStub(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the Docker test executable requires a POSIX shell")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

const bothPlatformsManifest = `{"manifests":[
  {"platform":{"os":"linux","architecture":"amd64"}},
  {"platform":{"os":"linux","architecture":"arm64"}}]}`

func TestStackImagesPinTheHostPlatformWhenTheCacheIsForeign(t *testing.T) {
	dockerStub(t, `
case "$1 $2" in
  "version --format") echo linux/arm64 ;;
  "image inspect") echo linux/amd64 ;;
  "manifest inspect") echo '`+bothPlatformsManifest+`' ;;
  *) exit 1 ;;
esac
`)
	var out bytes.Buffer
	platforms, err := resolveStackImages(context.Background(), stackImages, "41.3.1", &out)
	require.NoError(t, err)
	for _, img := range stackImages {
		require.Equal(t, "linux/arm64", platforms[img.env],
			"%s pinlenmedi — compose yabancı mimarili önbelleği sessizce koşturur", img.env)
	}
	require.Contains(t, out.String(), "pgvector/pgvector:pg16 is cached as linux/amd64 on a linux/arm64 host",
		"emülasyona düşecek önbellek kullanıcıya söylenmiyor")
}

// Önbellek zaten yerliyse tek bir fazladan tur bile atılmaz: `manifest inspect`
// ağ turudur ve her `palbase start`'a bedel yazmanın gerekçesi yok.
func TestStackImagesDoNotQueryTheRegistryWhenTheCacheIsNative(t *testing.T) {
	dir := dockerStub(t, `
printf '%s\n' "$1 $2" >> "$PALBASE_TEST_DOCKER_CALLS"
case "$1 $2" in
  "version --format") echo linux/arm64 ;;
  "image inspect") echo linux/arm64 ;;
  *) exit 1 ;;
esac
`)
	calls := filepath.Join(dir, "calls")
	t.Setenv("PALBASE_TEST_DOCKER_CALLS", calls)

	var out bytes.Buffer
	platforms, err := resolveStackImages(context.Background(), stackImages, "41.3.1", &out)
	require.NoError(t, err)
	require.Equal(t, "linux/arm64", platforms["PBC_POSTGRES_IMAGE"])
	raw, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "manifest inspect")
	require.Empty(t, out.String(), "yerli önbellek hakkında söylenecek bir şey yok")
}

// Etiket bu makinenin platformunu YAYIMLAMIYORSA emülasyon tek yoldur — ve o
// zaman pin YAZILMAZ (yazılsaydı compose çekemeyeceği bir varyant isterdi ve
// yığın hiç kalkmazdı), ama sessiz de kalınmaz.
func TestStackImagesWarnWhenNoNativeBuildIsPublished(t *testing.T) {
	dockerStub(t, `
case "$1 $2" in
  "version --format") echo linux/arm64 ;;
  "image inspect") echo linux/amd64 ;;
  "manifest inspect") echo '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}' ;;
  *) exit 1 ;;
esac
`)
	var out bytes.Buffer
	platforms, err := resolveStackImages(context.Background(), stackImages, "41.3.1", &out)
	require.NoError(t, err)
	require.Empty(t, platforms["PBC_POSTGRES_IMAGE"], "çekilemeyecek bir platform pinlendi")
	require.Contains(t, out.String(), "it will run under emulation")
}

// Daemon'ın platformu okunamıyorsa (eski ya da tuhaf bir uç) davranış BUGÜNKÜ
// davranıştır: pin yok, red yok. Yeni kapı, çalışan bir kurulumu bozmaz.
func TestStackImagesPinNothingWhenTheDaemonPlatformIsUnreadable(t *testing.T) {
	dockerStub(t, `
case "$1 $2" in
  "version --format") exit 1 ;;
  "image inspect") echo linux/amd64 ;;
  *) exit 1 ;;
esac
`)
	var out bytes.Buffer
	platforms, err := resolveStackImages(context.Background(), stackImages, "41.3.1", &out)
	require.NoError(t, err)
	for _, img := range stackImages {
		require.Empty(t, platforms[img.env])
	}
}

// Çözülemeyen platform .env'e BOŞ yazılır: bir önceki koşunun değeri dosyada
// kalsaydı, docker bağlamı başka mimarideki bir daemon'a taşındığında compose
// çekilemeyecek bir varyant ister ve yığın hiç kalkmazdı.
func TestRecordStackImagesClearsAStalePlatform(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, recordStackImages(envFile, "41.3.1",
		map[string]string{"PBC_POSTGRES_IMAGE": "linux/arm64"}))
	raw, err := os.ReadFile(envFile)
	require.NoError(t, err)
	require.Contains(t, string(raw), "PBC_POSTGRES_PLATFORM=linux/arm64")

	require.NoError(t, recordStackImages(envFile, "41.3.1", map[string]string{}))
	raw, err = os.ReadFile(envFile)
	require.NoError(t, err)
	require.Contains(t, string(raw), "PBC_POSTGRES_PLATFORM=\n")
	require.NotContains(t, string(raw), "PBC_POSTGRES_PLATFORM=linux/arm64")
}

// CLI'ın çözdüğü platformun BİR İŞE YARAMASI için compose'un onu okuması şart:
// iki yarım, iki dosyada. Kapı YORUMLARI atar — bir kuralı, o kuralı anlatan
// kendi yorumu tatmin edemez.
func TestVendoredComposeReadsEveryImagePlatform(t *testing.T) {
	var code []string
	for _, line := range strings.Split(string(stackCompose), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		code = append(code, line)
	}
	body := strings.Join(code, "\n")
	for _, img := range stackImages {
		require.Contains(t, body, "platform: ${"+img.platformEnv()+":-}",
			"%s compose'da okunmuyor — CLI platformu çözse de docker önbellekteki mimariyi koşturur (#3)", img.env)
	}
}
