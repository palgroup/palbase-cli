package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// C-6 · plan dosyası: `palbase plan` yazar, `palbase push` onsuz koşmaz (D-9).
type PlanFile struct {
	Version          int             `json:"version"`
	CreatedAt        time.Time       `json:"createdAt"`
	Target           PlanTarget      `json:"target"`
	BundleDigest     string          `json:"bundleDigest"`
	SDK              PlanSDK         `json:"sdk"`
	SchemaPlanDigest string          `json:"schemaPlanDigest"`
	Runtime          json.RawMessage `json:"runtime,omitempty"`
	Destructive      []string        `json:"destructive"`
	Breaking         []string        `json:"breaking"`
	// Unmeasured, planın SORAMADIĞI şeyleri kullanıcının okuyabileceği cümlelerle
	// taşır (FR-063). Parmak izinin DIŞINDA, çünkü ölçülemeyen dünya zaten
	// `schemaPlanDigest`'in içinde ayrı bir sentinel olarak duruyor — bu alan o
	// olgunun insan için yazılmış hâli, tıpkı `destructive` gibi.
	Unmeasured  []string `json:"unmeasured,omitempty"`
	Fingerprint string   `json:"fingerprint"`
}

type PlanTarget struct {
	URL string `json:"url"`
	Ref string `json:"ref"`
}

type PlanSDK struct {
	Running string `json:"running"`
	Target  string `json:"target"`
}

var ErrNoPlan = errors.New("no plan for this checkout; run `palbase plan`")

// planFilePath is where THIS MACHINE's plan for this checkout lives — and it is
// not in the checkout.
//
// The file records a measurement made on this machine, right now, against the
// environment selected here: a `createdAt`, the target's URL, a bundle digest.
// The only thing that reads it is the `push` that follows, and it recomputes the
// fingerprint. Committed, two developers' plans overwrite each other; ignored,
// it is one more file the repository must carry a rule for forever. It moved
// beside the credentials, where this machine's own state already lived.
func planFilePath(dir string) (string, error) { return PlanStatePath(dir) }

func WritePlanFile(dir string, p PlanFile) error {
	path, err := planFilePath(dir)
	if err != nil {
		return err
	}
	// A WRITE KNOWS IT IS A WRITE. Asking where the plan goes creates nothing;
	// putting one there creates the directory, and records which checkout it
	// belongs to so a deleted project's state can be recognised and swept.
	if err := ensureMachineStateDir(path); err != nil {
		return err
	}
	rememberOrigin(filepath.Dir(path), dir)
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ReadPlanFile(dir string) (PlanFile, error) {
	path, err := planFilePath(dir)
	if err != nil {
		return PlanFile{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return PlanFile{}, ErrNoPlan
	}
	if err != nil {
		return PlanFile{}, err
	}
	var p PlanFile
	if err := json.Unmarshal(b, &p); err != nil {
		return PlanFile{}, fmt.Errorf("plan file is unreadable: %w", err)
	}
	if p.Version != 1 || p.Fingerprint == "" {
		return PlanFile{}, ErrNoPlan
	}
	return p, nil
}

// BundleDigest: .palbase/{esm,jobs,hooks} altındaki dosyaların sıralı yol +
// içerik sha256'sı. Tarball başlıkları değil İÇERİK hash'lenir (NFR-007).
//
// ARGÜMAN BUNDLE KÖKÜDÜR, MÜŞTERİNİN CHECKOUT'U DEĞİL — ve bu ayrım 0.61.1'den
// beri bulut push'unu imkânsız kılmıştı. O sürümde ürünler geçici bir köke
// taşındı (`retiredProjectPaths`: ".palbase/esm — built into a temp bundle root
// since 0.61.1"); `plan` yeni kökü ölçmeye geçti, `push`un kapısı checkout'u
// ölçmeye devam etti. Checkout'ta ise artık HİÇBİR ürün yok — üstelik her
// derleme `reapRetiredArtifacts` ile eskisini de siliyor — yani kapı her
// seferinde BOŞ KÜMENİN özetini hesaplıyor, planınkiyle karşılaştırıyor ve
// "bundle changed" diyordu. Yeni bir plan da aynı yere düşüyordu: kullanıcının
// gördüğü, çıkışı olmayan bir plan→push döngüsüydü.
//
// Parametrenin ADI `dir`di ve çağıranı checkout vermeye davet ediyordu.
func BundleDigest(bundleRoot string) (string, error) {
	h := sha256.New()
	var paths []string
	for _, sub := range bundleOutputDirs {
		root := filepath.Join(bundleRoot, ".palbase", sub)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(filepath.Join(bundleRoot, ".palbase"), path)
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	sort.Strings(paths)
	// FAIL-CLOSED: BOŞ KÜMENİN ÖZETİ BİR ÖLÇÜM DEĞİLDİR.
	//
	// Boş bir kök `sha256("")` döndürüyordu — 64 haneli, geçerli GÖRÜNEN, her
	// çağrıda aynı çıkan bir değer. Sessizliğin sebebi buydu: yanlış kökü ölçen
	// kapı hata vermedi, yalnızca hiçbir zaman eşleşmeyen bir sabit üretti. Bir
	// artefaktın kodsuz gidemeyeceğini söyleyen `BuildStackTarball` ile aynı
	// gerekçe, aynı yerde: kapı, çağıranın testinde değil fiilin kendisinde.
	if len(paths) == 0 {
		return "", fmt.Errorf("no build output under %s/.palbase/%v — the digest of nothing "+
			"is not a measurement of a bundle", bundleRoot, bundleOutputDirs)
	}
	for _, rel := range paths {
		f, err := os.Open(filepath.Join(bundleRoot, ".palbase", rel))
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(h, rel+"\x00")
		if _, err := io.Copy(h, f); err != nil {
			_ = f.Close()
			return "", err
		}
		_ = f.Close()
		_, _ = io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Fingerprint: sunucuyla (cloud.controller planFingerprint) AYNI kanonik JSON.
func Fingerprint(bundleDigest, running, target, schemaDigest string) string {
	b, _ := json.Marshal(struct {
		BundleDigest     string  `json:"bundleDigest"`
		SDK              PlanSDK `json:"sdk"`
		SchemaPlanDigest string  `json:"schemaPlanDigest"`
	}{bundleDigest, PlanSDK{Running: running, Target: target}, schemaDigest})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// StaleReasons neyin değiştiğini ADIYLA söyler (FR-047).
func StaleReasons(saved, current PlanFile) []string {
	var out []string
	if saved.SDK.Running != current.SDK.Running {
		out = append(out, fmt.Sprintf("runtime moved %s → %s", saved.SDK.Running, current.SDK.Running))
	}
	if saved.SDK.Target != current.SDK.Target {
		out = append(out, fmt.Sprintf("target SDK changed %s → %s", saved.SDK.Target, current.SDK.Target))
	}
	if saved.BundleDigest != current.BundleDigest {
		out = append(out, "bundle changed")
	}
	if saved.SchemaPlanDigest != current.SchemaPlanDigest {
		out = append(out, "schema plan changed")
	}
	if saved.Target.URL != current.Target.URL {
		out = append(out, "target changed")
	}
	return out
}
