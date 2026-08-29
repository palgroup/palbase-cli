package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeREST struct {
	method, path, body string
	status             int
	answer             string
}

func (f *fakeREST) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.method, f.path, f.body = method, path, string(body)
	st := f.status
	if st == 0 {
		st = http.StatusOK
	}
	ans := f.answer
	if ans == "" {
		ans = "{}"
	}
	return st, []byte(ans), nil
}

func runStorage(t *testing.T, rest *fakeREST, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestBucketsLiveOnTheStackNotInAFile is the change this task exists for.
func TestBucketsLiveOnTheStackNotInAFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	rest := &fakeREST{}
	if _, err := runStorage(t, rest, "add", "avatars", "--public", "--max-size", "5MB"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "storage.ts")); !os.IsNotExist(err) {
		t.Fatal("a file was written: buckets have one home now, and it is the stack")
	}
	if rest.method != http.MethodPut || rest.path != "/v1/management/storage/buckets/avatars" {
		t.Fatalf("called %s %s", rest.method, rest.path)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(rest.body), &sent); err != nil {
		t.Fatalf("body is not JSON: %s", rest.body)
	}
	if sent["public"] != true {
		t.Fatalf("--public did not travel: %s", rest.body)
	}
	if _, ok := sent["fileSizeLimit"]; !ok {
		t.Fatalf("--max-size did not travel: %s", rest.body)
	}
}

// TestRemoveActuallyRemoves. It used to edit the file and leave the live bucket
// and its objects in place, so the command looked reversible while nothing moved.
func TestRemoveActuallyRemoves(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{status: http.StatusNoContent}

	if _, err := runStorage(t, rest, "remove", "avatars"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if rest.method != http.MethodDelete || rest.path != "/v1/management/storage/buckets/avatars" {
		t.Fatalf("called %s %s, want a DELETE", rest.method, rest.path)
	}
}

// TestListReadsTheStack.
func TestListReadsTheStack(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{answer: `[{"name":"avatars","public":true,"object_count":3,"total_bytes":900}]`}

	out, err := runStorage(t, rest, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rest.method != http.MethodGet || rest.path != "/v1/management/storage/buckets" {
		t.Fatalf("called %s %s", rest.method, rest.path)
	}
	if !strings.Contains(out, "avatars") || !strings.Contains(out, "public") {
		t.Fatalf("the stack's answer did not reach the person:\n%s", out)
	}
}

// TestAddRefusesLocallyWhatTheStackWouldRefuse keeps the checks that were worth
// keeping: a bad name, a bad MIME list or an unparseable size never leaves here.
func TestAddRefusesLocallyWhatTheStackWouldRefuse(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"add", "Bad Name"},
		{"add", "ok", "--mime", "notamime"},
		{"add", "ok", "--max-size", "5 bananas"},
	} {
		rest := &fakeREST{}
		if _, err := runStorage(t, rest, args...); err == nil {
			t.Fatalf("%v was accepted", args)
		}
		if rest.method != "" {
			t.Fatalf("%v reached the stack before being refused", args)
		}
	}
}

func TestParseSize_BinaryUnits(t *testing.T) {
	cases := map[string]int64{
		"5MB":   5 * 1024 * 1024,
		"20MB":  20 * 1024 * 1024,
		"1GB":   1024 * 1024 * 1024,
		"500kb": 500 * 1024,
		"1024":  1024,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

// The body has to be in the SPELLING the storage module reads.
//
// It was not. The CLI sent `file_size_limit` and `allowed_mime_types`; the
// module's bucketDeclaration reads `fileSizeLimit` and `allowedMimeTypes`. The
// management layer forwards raw bytes, so nothing complained — the bucket was
// created with no size limit and no type list, and the only way to notice was
// to upload a file that should have been refused.
func TestTheBucketBodyIsSpelledTheWayTheModuleReadsIt(t *testing.T) {
	rest := &fakeREST{}
	if _, err := runStorage(t, rest, "add", "posts", "--public", "--max-size", "10mb",
		"--mime", "image/png,image/jpeg"); err != nil {
		t.Fatal(err)
	}
	sent := rest.body
	for _, want := range []string{`"fileSizeLimit"`, `"allowedMimeTypes"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("the body does not carry %s — the module will ignore it:\n%s", want, sent)
		}
	}
	for _, gone := range []string{`"file_size_limit"`, `"allowed_mime_types"`} {
		if strings.Contains(sent, gone) {
			t.Errorf("the body still uses %s, which nothing reads:\n%s", gone, sent)
		}
	}
}

// SUNUCUDAKİ RENDER MAKİNESİ ULAŞILAMAZDI. Storage modülü variant'ları tam
// destekliyor — bildirimi doğruluyor, görseli render ediyor, imzalı URL
// üretiyor, move/copy ile taşıyor (handler.go upsertBucket + imaging) — ama
// onları BİLDİREBİLECEK hiçbir istemci yoktu: `storage add`'in üç bayrağı
// vardı (--public/--max-size/--mime), panelde arayüz yok, ve config/storage.ts
// yolu kaldırıldı. Bir müşteri görsel varyantlarını tasarımdan çıkarmak
// zorunda kaldı.
//
// ŞEKİL SUNUCUDAN: handler.go `variant{Width,Height,Fit,Format,Quality}`,
// fit ∈ cover|contain|inside, format yığının render edebildikleriyle sınırlı.
func TestStorageAddSendsDeclaredVariants(t *testing.T) {
	rest := &fakeREST{answer: `{"bucket":"posts"}`}
	if _, err := runStorage(t, rest, "add", "posts", "--public",
		"--variant", "card=640x480:cover:webp:82",
		"--variant", "thumb=160x160:cover:webp"); err != nil {
		t.Fatalf("add --variant: %v", err)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(rest.body), &sent); err != nil {
		t.Fatalf("gövde JSON değil: %v — %s", err, rest.body)
	}
	variants, ok := sent["variants"].(map[string]any)
	if !ok {
		t.Fatalf("variants gövdede yok: %s", rest.body)
	}
	card, ok := variants["card"].(map[string]any)
	if !ok {
		t.Fatalf("card variant'ı yok: %s", rest.body)
	}
	for key, want := range map[string]any{
		"width": float64(640), "height": float64(480),
		"fit": "cover", "format": "webp", "quality": float64(82),
	} {
		if card[key] != want {
			t.Errorf("card.%s = %v, beklenen %v", key, card[key], want)
		}
	}
	// Kalite verilmediyse gönderilmez — sunucunun kendi varsayılanı geçerli olsun.
	thumb := variants["thumb"].(map[string]any)
	if _, present := thumb["quality"]; present {
		t.Errorf("kalite verilmemişken gönderildi: %v", thumb)
	}
}

func TestStorageAddRefusesAMalformedVariant(t *testing.T) {
	for _, bad := range []string{
		"card",                     // = yok
		"card=640",                 // boyut yok
		"card=640x480",             // fit/format yok
		"card=640x480:squish:webp", // geçersiz fit
		"card=640x480:cover:webp:x", // kalite sayı değil
	} {
		t.Run(bad, func(t *testing.T) {
			rest := &fakeREST{answer: `{"bucket":"posts"}`}
			if _, err := runStorage(t, rest, "add", "posts", "--variant", bad); err == nil {
				t.Errorf("bozuk variant %q kabul edildi — yığına gitmeden reddedilmeliydi", bad)
			}
		})
	}
}

// Bildirdiğin variant'ı göremezsen bildirip bildirmediğini bilemezsin — ve
// bu, `add`den hemen sonra sorulan soru.
func TestStorageListShowsVariants(t *testing.T) {
	rest := &fakeREST{answer: `[{"name":"posts","public":true,"object_count":2,"total_bytes":10,
		"variants":[{"name":"thumb"},{"name":"card"}]}]`}
	out, err := runStorage(t, rest, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "variants: card, thumb") {
		t.Errorf("liste variant adlarını göstermiyor:\n%s", out)
	}
	// NEGATİF KONTROL: variant'sız kova fazladan satır basmaz.
	plain := &fakeREST{answer: `[{"name":"docs","public":false,"object_count":0,"total_bytes":0}]`}
	out2, _ := runStorage(t, plain, "list")
	if strings.Contains(out2, "variants:") {
		t.Errorf("variant'sız kova için variant satırı basıldı:\n%s", out2)
	}
}
