package backend

// pull_large_test.go — `palbase pull` brings back the WHOLE deployed source,
// not the first megabyte of it.
//
// ÖLÇÜLDÜ 07.09.2026 (centauri-backdoor, 8bbwb2pbm): `palbase pull` düştü —
//
//	extract bundle: read tar entry: unexpected EOF
//
// Kiracının servis ettiği arşiv 3.574.188 bayt, 225 girdili, geçerli bir
// .tar.gz. Onu tam 1.048.576 bayta kesip `tar -tzf` koşturmak aynı yerde
// "Truncated input file" veriyor — yani gövde CLI'a eksik ULAŞMIYOR, CLI onu
// eksik OKUYOR.
//
// Sebep: indirme `managementCall`'dan geçiyordu ve o kapının son satırı
// `io.ReadAll(io.LimitReader(res.Body, 1<<20))`. `io.LimitReader` tavana
// varınca TEMİZ bir EOF döner, `io.ReadAll` da nil hata döner: kırpma
// BAŞARI gibi görünür. Tavan 17.08'de küçük JSON okumaları (secrets, flags)
// için kondu; `pull` 24.08'de "her fiil tek kapıdan REST konuşur" ile aynı
// kapıya taşındı ve bir kaynak ağacı, bir ayar okuması için yazılmış sınırı
// miras aldı. 1 MiB'ı aşan hiçbir proje o günden beri çekilemedi.
//
// Bu testin fikstürü SIKIŞTIRILAMAZ olmak zorunda: sıkıştırılabilir bir gövde
// tavanın altına iner ve test yeşil yalan söyler.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// incompressibleTarGz builds a MANY-ENTRY archive whose gzipped size exceeds
// want, and returns it with the contents it wrote, keyed by path.
//
// Many entries rather than one big file, because that is the shape of a real
// bundle (centauri-backdoor's is 225 entries) and it is what makes the last
// entries land PAST the cap. Deterministic bytes, so a failure is
// reproducible; random-looking, so gzip cannot shrink the fixture back under
// the cap this test exists to cross.
//
// Where the cut falls inside the truncated stream is an accident of
// compression — a header boundary gives "read tar entry: unexpected EOF" (the
// sentence in the 07.09 report), a body gives "write file ...: unexpected
// EOF". Both are the one defect, so this test asserts the whole tree lands
// rather than pinning a message.
func incompressibleTarGz(t *testing.T, dir string, entries, each, want int) ([]byte, map[string][]byte) {
	t.Helper()
	src := rand.New(rand.NewSource(1))
	written := make(map[string][]byte, entries)
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for i := range entries {
		body := make([]byte, each)
		if _, err := src.Read(body); err != nil {
			t.Fatal(err)
		}
		name := filepath.ToSlash(filepath.Join(dir, "entry"+strconv.Itoa(i)+".bin"))
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
		written[name] = body
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if raw.Len() <= want {
		t.Fatalf("fixture compressed to %d bytes, which does not cross the %d cap this test measures", raw.Len(), want)
	}
	return raw.Bytes(), written
}

// TestPullExtractsAnArchiveLargerThanAConfigRead.
func TestPullExtractsAnArchiveLargerThanAConfigRead(t *testing.T) {
	inScratchCheckout(t)

	// 40 × 48 KiB ≈ 1.9 MiB of incompressible payload: comfortably past the
	// 1 MiB a config read was sized for, and shaped like a source tree.
	archive, want := incompressibleTarGz(t, "controllers", 40, 48<<10, 1<<20)

	srv := stackServing(t, "pb_project_c01234567890123456789", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == SourcePath("latest") {
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

	cmd := newPullCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("pull of a %d-byte archive: %v\n%s", len(archive), err, out.String())
	}

	// EVERY entry, byte for byte. A truncating read loses the tail, so
	// checking the first file only would pass on the bug this covers.
	for name, body := range want {
		got, err := os.ReadFile(filepath.FromSlash(name))
		if err != nil {
			t.Fatalf("%s did not land: %v\n%s", name, err, out.String())
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("%s landed %d bytes, the project served %d", name, len(got), len(body))
		}
	}
}
