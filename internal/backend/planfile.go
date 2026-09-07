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

func planFilePath(dir string) string { return filepath.Join(dir, ".palbase", "plan.json") }

func WritePlanFile(dir string, p PlanFile) error {
	if err := os.MkdirAll(filepath.Join(dir, ".palbase"), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := planFilePath(dir) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, planFilePath(dir))
}

func ReadPlanFile(dir string) (PlanFile, error) {
	b, err := os.ReadFile(planFilePath(dir))
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
func BundleDigest(dir string) (string, error) {
	h := sha256.New()
	var paths []string
	for _, sub := range bundleOutputDirs {
		root := filepath.Join(dir, ".palbase", sub)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(filepath.Join(dir, ".palbase"), path)
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	sort.Strings(paths)
	for _, rel := range paths {
		f, err := os.Open(filepath.Join(dir, ".palbase", rel))
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
