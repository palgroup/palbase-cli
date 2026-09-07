package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBundleDigestIsDeterministicAndContentBound(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, ".palbase", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("esm/main.js", "export const a = 1;")
	write("jobs/nightly.js", "export default () => {};")
	a, err := BundleDigest(dir)
	if err != nil || len(a) != 64 {
		t.Fatalf("digest: %q %v", a, err)
	}
	b, _ := BundleDigest(dir)
	if a != b {
		t.Fatal("aynı içerik aynı digest vermeli (NFR-007)")
	}
	write("esm/main.js", "export const a = 2;")
	if c, _ := BundleDigest(dir); c == a {
		t.Fatal("içerik değişince digest değişmeli")
	}
}

func TestFingerprintMatchesTheServerFormula(t *testing.T) {
	// Sunucu (T024) aynı JSON'u aynı anahtar sırasıyla hash'ler:
	// {"bundleDigest":"b","sdk":{"running":"36.0.2","target":"37.0.2"},"schemaPlanDigest":"s"}
	got := Fingerprint("b", "36.0.2", "37.0.2", "s")
	if len(got) != 64 {
		t.Fatalf("parmak izi 64 hex olmalı: %q", got)
	}
	if Fingerprint("b", "36.0.2", "37.0.2", "s") != got || Fingerprint("b2", "36.0.2", "37.0.2", "s") == got {
		t.Fatal("parmak izi determinist ve bileşene bağlı olmalı")
	}
}

func TestPlanFileRoundTripsAndNamesWhatChanged(t *testing.T) {
	dir := t.TempDir()
	p := PlanFile{Version: 1, CreatedAt: time.Now().UTC(), Target: PlanTarget{URL: "https://abc12345m.palbase.studio", Ref: "abc12345m"},
		BundleDigest: "b", SDK: PlanSDK{Running: "36.0.2", Target: "37.0.2"}, SchemaPlanDigest: "s"}
	p.Fingerprint = Fingerprint(p.BundleDigest, p.SDK.Running, p.SDK.Target, p.SchemaPlanDigest)
	if err := WritePlanFile(dir, p); err != nil {
		t.Fatal(err)
	}
	back, err := ReadPlanFile(dir)
	if err != nil || back.Fingerprint != p.Fingerprint || back.Target.Ref != "abc12345m" {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	now := p
	now.SDK.Running = "37.0.2"
	now.BundleDigest = "b2"
	reasons := StaleReasons(p, now)
	if len(reasons) != 2 || reasons[0] != "runtime moved 36.0.2 → 37.0.2" || reasons[1] != "bundle changed" {
		t.Fatalf("bayatlık sebepleri adlandırılmalı: %v", reasons)
	}
	if _, err := ReadPlanFile(t.TempDir()); err != ErrNoPlan {
		t.Fatalf("plan yokken ErrNoPlan: %v", err)
	}
}
