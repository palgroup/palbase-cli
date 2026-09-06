package backend

// pull_bound_test.go — `palbase pull` obeys the project this checkout is bound
// to, the way push, status, deploys and apikey already do.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarGzWith builds the archive the project serves for its deployed source.
func tarGzWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// TestPullFollowsTheBoundProject.
//
// ÖLÇÜLDÜ 25.08.2026 (palaicloud): `palbase pull` ve `palbase clone` bir projeye
// bağlı bir checkout'ta `not_found (404): böyle bir proje yok` diyordu — ay­nı
// bağlantı üzerinden `status`, `deploys` ve `apikey` sorunsuz çalışırken.
//
// Sebep: aktarımın kendisi zaten hedefe göre (`pullBundle` yığının kendi
// yönetim API'sini çağırır), ama HANGİ ref sorusu bulut seçiminden geçiyordu ve
// o seçim bir YÖNETİM ID'si istiyor — CLI'ın hiçbir yerde göstermediği,
// kullanıcının elinde olmayan bir değer. Bağlı bir checkout'ta sorulacak soru
// zaten yok: bağlandığı proje, çekeceği projedir.
func TestPullFollowsTheBoundProject(t *testing.T) {
	inScratchCheckout(t)

	archive := tarGzWith(t, "controllers/todo.controller.ts", "// pulled\n")
	var sourceHits int
	srv := stackServing(t, "pb_project_c01234567890123456789", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SourcePath("latest") {
			if r.Header.Get("Authorization") == "" && r.Header.Get("apikey") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			sourceHits++
			w.Header().Set("content-type", "application/gzip")
			_, _ = w.Write(archive)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	if err := WriteTarget(Target{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	linkedAs(t, srv.URL, "a-credential")

	// The cloud resolver is deliberately absent: reaching for it fails loudly
	// instead of passing by accident.
	cmd := newPullCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("pull in a bound checkout: %v\n%s", err, out.String())
	}

	if sourceHits != 1 {
		t.Errorf("the bound project was asked for its source %d time(s), want 1\n%s", sourceHits, out.String())
	}
	body, err := os.ReadFile(filepath.Join("controllers", "todo.controller.ts"))
	if err != nil {
		t.Fatalf("the deployed source did not land: %v\n%s", err, out.String())
	}
	if !strings.Contains(string(body), "pulled") {
		t.Errorf("what landed is not what the project served:\n%s", body)
	}
}
