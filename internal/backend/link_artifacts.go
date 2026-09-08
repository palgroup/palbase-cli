package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

type artifactFile struct {
	data []byte
	mode fs.FileMode
}

// All network reads and generators finish in a sibling staging directory. The
// app sees the new config, spec and generated code only after the set is ready.
// Dependencies are read from the checkout; generated artifacts are never linked
// back to it. Failure before publication leaves the original files untouched.
// linkStagePrefix, hazırlık alanının adı. Süpürme de yaratma da bunu kullanır:
// iki yerde yazılan bir önek, bir gün ayrışır ve süpürücü kendi ürettiğini
// tanımaz hâle gelir.
const linkStagePrefix = ".palbase-link-"

// newLinkStage, hazırlık alanını CHECKOUT'UN İÇİNDE açar ve önce eskiyi süpürür.
//
// NEDEN İÇERİDE: eskiden `filepath.Dir(root)` kullanılıyordu, yani projenin BİR
// ÜSTÜ. Müşteride proje `smartex/palbase`, üstü git deposunun kökü; kalıntı
// orada `?? .palbase-link-1344746409/` olarak duruyordu — bir gün öncesinden,
// içinde `.env.local` kopyası (bir API anahtarı) ve canlı checkout'a
// symlink'ler. Bu dosyanın kendi kuralı bunu zaten yasaklıyor: kullanıcıdan
// gelen yollar için "link output and entry must belong to this checkout" diyor
// ve aracın kendisi o kurala uymuyordu.
//
// NEDEN OS TEMP DEĞİL: yayımlama `os.Rename(staged, live)` ile yapılıyor
// (`publishLinkArtifacts`) ve farklı dosya sistemleri arasında rename EXDEV ile
// düşer. Checkout'un içi aynı dosya sistemini garantiler; `/tmp` garantilemez —
// CI'da tmpfs olması olağandır.
//
// NEDEN SÜPÜRME: temizlik yalnız `defer os.RemoveAll(stage)` ile yapılıyor ve o
// defer SIGINT'te, SIGKILL'de, panikte ya da elektrik kesintisinde KOŞMAZ.
// Sinyal yakalamak da yetmez — SIGKILL yakalanamaz. Sağlam olan her koşunun
// BAŞINDA eskiyi toplamasıdır: hazırlık alanı koşu başına tekildir ve bu depoda
// bir checkout'un tek yazarı vardır, dolayısıyla önceden duran her şey ölüdür.
func newLinkStage(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), linkStagePrefix) {
			// Süpürme BEST-EFFORT: bir kalıntı silinemiyorsa (izin, açık dosya)
			// bu koşuyu düşürmek, düzeltilebilir bir çöp yüzünden yapılabilir bir
			// işi reddetmek olurdu.
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
	return os.MkdirTemp(root, linkStagePrefix+"*")
}

func runLink(ctx context.Context, o linkOpts, w io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := validatePlatforms(o.platforms); err != nil {
		return err
	}
	if err := refuseUnsupportedPlatforms(o.platforms); err != nil {
		return err
	}
	platforms := o.platforms
	if len(platforms) == 0 {
		platforms = detectPlatforms(root)
	}
	installWebSDK := slices.Contains(platforms, webPlatform) && !isRegularFile(filepath.Join(root, palbeGenBin))
	stage, err := newLinkStage(root)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	mutable := map[string]bool{".palbase": true, "Palbase": true, "src": true, "app": true, "pages": true, "public": true}
	for _, path := range []string{o.entry, o.out} {
		if path == "" {
			continue
		}
		if filepath.IsAbs(path) {
			path, err = filepath.Rel(root, path)
			if err != nil {
				return err
			}
		}
		if path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return fmt.Errorf("link output and entry must belong to this checkout")
		}
		mutable[strings.Split(filepath.Clean(path), string(filepath.Separator))[0]] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	before := map[string]artifactFile{}
	stageName := filepath.Base(stage)
	for _, entry := range entries {
		name := entry.Name()
		// HAZIRLIK ALANI KENDİNİ AYNALAYAMAZ. Stage artık checkout'un İÇİNDE
		// açılıyor, yani bu taramada görünür; atlanmazsa kendi içine symlink
		// kurar ve ağaç kendi kendini içerir.
		if name == stageName {
			continue
		}
		if name == "node_modules" && installWebSDK {
			// An installer must not follow a symlink into the live checkout.
			// Install the declared dependency set in the stage and publish it
			// together with the generated client only after generation succeeds.
			continue
		}
		if strings.HasSuffix(name, ".xcodeproj") || strings.HasSuffix(name, ".xcworkspace") {
			mutable[name] = true
		}
		if (entry.IsDir() || entry.Type()&os.ModeSymlink != 0) && !mutable[name] && name != ".gitignore" && name != "package.json" {
			if err := os.Symlink(filepath.Join(root, name), filepath.Join(stage, name)); err != nil {
				return err
			}
			continue
		}
		if err := copyArtifactTree(root, stage, name, before); err != nil {
			return err
		}
	}
	for _, path := range []*string{&o.entry, &o.out} {
		if filepath.IsAbs(*path) {
			relative, _ := filepath.Rel(root, *path)
			*path = filepath.Join(stage, relative)
		}
	}
	o.checkoutRoot = root
	var output bytes.Buffer
	if err := os.Chdir(stage); err != nil {
		return err
	}
	// The ordinary path checks restoration below; this also restores on panic.
	defer func() { _ = os.Chdir(root) }()
	workErr := runLinkPrepared(ctx, o, &output)
	if err := os.Chdir(root); err != nil {
		return err
	}
	if workErr != nil {
		return fmt.Errorf("link failed; previous client artifacts were preserved: %w", workErr)
	}
	after := map[string]artifactFile{}
	if err := collectArtifacts(stage, mutable, after); err != nil {
		return err
	}
	if err := publishLinkArtifacts(root, stage, before, after); err != nil {
		return err
	}
	_, err = io.WriteString(w, strings.ReplaceAll(output.String(), stage, root))
	return err
}

func publishLinkArtifacts(root, stage string, before, after map[string]artifactFile) error {
	staged := filepath.Join(stage, "node_modules")
	info, err := os.Lstat(staged)
	if os.IsNotExist(err) || err == nil && info.Mode()&os.ModeSymlink != 0 {
		return publishArtifacts(root, before, after)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("the dependency installer did not produce a node_modules directory")
	}
	live := filepath.Join(root, "node_modules")
	// Yedek de checkout'un İÇİNDE: `os.Rename(live, backup)` ile taşındığı için
	// aynı dosya sisteminde olmak zorunda, ve dışarıya yazmak burada da yanlış.
	backupDir, err := os.MkdirTemp(root, linkStagePrefix+"dependencies-")
	if err != nil {
		return err
	}
	// Remove only an empty directory on failure. If restoring the original
	// dependencies fails, their backup must survive the stage's cleanup.
	defer func() { _ = os.Remove(backupDir) }()
	backup := filepath.Join(backupDir, "node_modules")
	_, err = os.Lstat(live)
	hadDependencies := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if hadDependencies {
		if err := os.Rename(live, backup); err != nil {
			return fmt.Errorf("preserve previous dependencies: %w", err)
		}
	}
	restore := func() error {
		if hadDependencies {
			return os.Rename(backup, live)
		}
		return nil
	}
	if err := os.Rename(staged, live); err != nil {
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("publish dependencies: %w; restore failed: %v; previous dependencies remain at %s", err, restoreErr, backup)
		}
		return err
	}
	if err := publishArtifacts(root, before, after); err != nil {
		// Move only the directory this invocation installed back into its
		// stage before restoring the original directory (or workspace link).
		if moveErr := os.Rename(live, staged); moveErr != nil {
			return fmt.Errorf("%w; dependency rollback failed: %v; previous dependencies remain at %s", err, moveErr, backup)
		}
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("%w; dependency restore failed: %v; previous dependencies remain at %s", err, restoreErr, backup)
		}
		return err
	}
	if hadDependencies {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("client artifacts published; could not remove previous dependency cache at %s: %w", backup, err)
		}
	}
	return nil
}

func copyArtifactTree(root, stage, path string, before map[string]artifactFile) error {
	return filepath.WalkDir(filepath.Join(root, path), func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		dest := filepath.Join(stage, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			// Following a generated-file symlink would mutate the real checkout
			// while it is meant to be staged. Require ordinary owned artifacts.
			return fmt.Errorf("link cannot stage writable path %s because it is a symlink", relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		before[relative] = artifactFile{raw, info.Mode().Perm()}
		return os.WriteFile(dest, raw, info.Mode().Perm())
	})
}

func collectArtifacts(root string, mutable map[string]bool, result map[string]artifactFile) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if !strings.Contains(relative, string(filepath.Separator)) && !mutable[relative] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[relative] = artifactFile{raw, info.Mode().Perm()}
		return nil
	})
}

func publishArtifacts(root string, before, after map[string]artifactFile) error {
	changed := map[string]bool{}
	for path, next := range after {
		old, exists := before[path]
		if !exists || old.mode != next.mode || !bytes.Equal(old.data, next.data) {
			changed[path] = true
		}
	}
	for path := range before {
		if _, exists := after[path]; !exists {
			changed[path] = true
		}
	}
	paths := make([]string, 0, len(changed))
	for path := range changed {
		actual, err := os.ReadFile(filepath.Join(root, path))
		old, existed := before[path]
		if existed && (err != nil || sha256.Sum256(actual) != sha256.Sum256(old.data)) || !existed && !os.IsNotExist(err) {
			return fmt.Errorf("%s changed during link; no generated files were published", path)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var applied []string
	for _, path := range paths {
		next, exists := after[path]
		err := replaceArtifact(filepath.Join(root, path), next, exists)
		if err != nil {
			var rollback []string
			for i := len(applied) - 1; i >= 0; i-- {
				old, existed := before[applied[i]]
				if restoreErr := replaceArtifact(filepath.Join(root, applied[i]), old, existed); restoreErr != nil {
					rollback = append(rollback, applied[i])
				}
			}
			if len(rollback) > 0 {
				return fmt.Errorf("publishing %s failed: %w; restore failed for %s", path, err, strings.Join(rollback, ", "))
			}
			return fmt.Errorf("publishing %s failed; previous artifacts restored: %w", path, err)
		}
		applied = append(applied, path)
	}
	return nil
}

func replaceArtifact(path string, file artifactFile, exists bool) error {
	if !exists {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".palbase-artifact-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(file.data); err != nil {
		return errors.Join(err, temp.Close())
	}
	if err := temp.Chmod(file.mode); err != nil {
		return errors.Join(err, temp.Close())
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
