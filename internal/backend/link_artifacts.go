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
	"syscall"
)

type artifactFile struct {
	data []byte
	mode fs.FileMode
}

// All network reads and generators finish in a sibling staging directory. The
// app sees the new config, spec and generated code only after the set is ready.
// Dependencies are read from the checkout; generated artifacts are never linked
// back to it. Failure before publication leaves the original files untouched.
// moveTree, bir ağacı taşır — VE DOSYA SİSTEMİ SINIRINI GEÇEBİLİR.
//
// `os.Rename` yalnız aynı dosya sisteminde çalışır; sınırı geçince `EXDEV` ile
// düşer. Hazırlık alanı artık işletim sisteminin temp'inde (müşterinin projesine
// hiçbir şey yazılmaması için) ve temp, projeyle aynı FS'te olmak zorunda
// değil — Linux'ta `/tmp` çoğu zaman tmpfs'tir. O yüzden taşıma, ucuz yolu
// DENER ve gerekirse kopyaya düşer.
//
// Kopya bir "fallback flag" değil, bir errno'nun karşılanması: alternatif,
// müşterinin projesine dizin açmak ya da CI'da çalışmayan bir link.
// renameForMove, `moveTree`'nin ucuz yolu — ve testin ERİŞEBİLDİĞİ tek yer.
//
// EXDEV dalı, temp ile projenin aynı dosya sisteminde olduğu bir makinede HİÇ
// koşmaz; ölçüldü: dalı tamamen silen bir mutasyon hiçbir testi kırmadı. Bir
// errno dalını test edilebilir kılmanın yolu, onu üreten çağrıyı
// değiştirilebilir yapmaktır — aksi hâlde kopya yolu üretimde İLK KEZ koşar.
var renameForMove = os.Rename

func moveTree(from, to string) error {
	if err := renameForMove(from, to); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyTree(from, to); err != nil {
		return err
	}
	return os.RemoveAll(from)
}

// copyTree, ağacı İZİNLERİYLE kopyalar.
//
// İzinler korunmak zorunda: 0600'lük bir dosya kopyada 0644 olursa, sır taşıyan
// bir dosya kopyada okunabilir hâle gelir. Symlink'ler İZLENMEZ — bir bağımlılık
// ağacında checkout dışına işaret eden bir link, kopyayı projenin dışına
// taşırdı.
func copyTree(from, to string) error {
	return filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(from, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(to, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			dest, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			return os.Symlink(dest, target)
		case !info.Mode().IsRegular():
			// Soket, cihaz, FIFO: bir bağımlılık ağacında işi yok ve kopyalanamaz.
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			_ = dst.Close()
			return err
		}
		return dst.Close()
	})
}

// linkStagePrefix, hazırlık alanının adı. Süpürme de yaratma da bunu kullanır:
// iki yerde yazılan bir önek, bir gün ayrışır ve süpürücü kendi ürettiğini
// tanımaz hâle gelir.
const linkStagePrefix = ".palbase-link-"

// newLinkStage, hazırlık alanını İŞLETİM SİSTEMİNİN TEMP'İNDE açar.
//
// MÜŞTERİNİN PROJESİNE HİÇBİR ŞEY YAZILMAZ. Önce projenin BİR ÜSTÜNE
// yazılıyordu (müşteride git deposunun kökü; bir gün yaşayan, içinde bir API
// anahtarı taşıyan ve `.gitignore`'un kapsamadığı bir kalıntı orada bulundu),
// sonra checkout'un içine aldım ve her koşuda eskiyi süpürdüm. Kullanıcı bunu da
// reddetti ve haklıydı: "süpürülüyor değil, hiç oluşmasın". Bir link sürerken
// projede `.palbase-link-XXXX/` görünmesi, kalıntı bırakmasa bile onun görmek
// istemediği şey.
//
// Buna engel `os.Rename` sanılıyordu — yayımlama stage'den checkout'a taşıma
// yapıyor ve farklı dosya sistemleri arasında rename EXDEV ile düşer. Engel
// gerçek ama çaresi stage'i projeye sokmak değil, taşımanın EXDEV'i
// karşılaması (`moveTree`).
//
// CHECKOUT YİNE DE TOPLANIR: bu CLI'ın bir ara sürümü stage'i checkout'un
// içine açıyordu, ve o sürümle yarıda kesilmiş bir koşunun kalıntısı hâlâ
// duruyor olabilir. Toplama emekli yolların TEK listesinden geçer
// (`reapRetiredArtifacts`) — burada ikinci bir süpürücü yazmak, listeyle bir
// gün ayrışacak ikinci bir gerçek yazmak olurdu. Üst dizine DOKUNULMAZ: orası
// bu aracın alanı değil ve silmek de bir yazma fiilidir.
func newLinkStage(root string) (string, error) {
	reapRetiredArtifacts(root)
	return os.MkdirTemp("", "palbase-link-*")
}

func runLink(ctx context.Context, o linkOpts, w io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	// THE RETIRED LAYOUT IS REFUSED FIRST, BEFORE ANY SIDE EFFECT.
	//
	// There is no migration and there will not be one: a half-old, half-new tree
	// carries two contracts and two clients, and nothing can say which one the
	// build read. Deleting the old files is a commit somebody makes and reviews,
	// not something a tool does to their repository behind a progress line.
	//
	// Measured by CONTENT, never by directory name (D-008): `palbase` and
	// `Palbase` are ONE directory on macOS and Windows, so a name-based check
	// would refuse every checkout that already carries the new layout.
	if found := CarriesLegacyLayout(root); len(found) > 0 {
		return fmt.Errorf("this checkout still carries the retired layout: %s.\n"+
			"  Delete it and commit that deletion, then run `palbase link` again — "+
			"everything here is regenerated from the project.\n"+
			"  There is no migration: a tree holding both layouts has two contracts "+
			"and two clients, and no way to tell which one a build read",
			strings.Join(found, ", "))
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
	// `palbase` is the ONE directory this CLI owns in a checkout. It replaced
	// the pair `.palbase` (hidden) and `Palbase` (visible); neither is written
	// any more, and `link` refuses a checkout that still carries one — so
	// neither belongs in the set of directories a link may create.
	mutable := map[string]bool{RootDir(): true, "src": true, "app": true, "pages": true, "public": true}
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
	for _, entry := range entries {
		name := entry.Name()
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
		// THE ADDRESS IS NOT A CLIENT ARTIFACT, AND IT MUST SURVIVE THIS.
		//
		// A project that has never been pushed to answers its contract route
		// with 404 `spec_unavailable`, so the link refuses — correctly: there is
		// no specification, so there is no client to generate. But everything
		// this run learned lived in the stage, INCLUDING the project's identity,
		// and the stage is thrown away. The person was then holding a checkout
		// bound to nothing, and `palbase push` — the ONE act that ends the state
		// the refusal is complaining about — reads exactly that binding.
		//
		// So a refusal whose cure is a push cannot also delete the address the
		// push needs. Everything else stays behind; the contract is published.
		if err := publishProjectContract(root, stage); err != nil {
			return fmt.Errorf("link failed (%v); and the project's address could not be kept: %w", workErr, err)
		}
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

// publishProjectContract copies the linked project's identity out of a stage a
// failed link is about to discard.
//
// ONLY that file. The stage also holds half-written client artifacts, and those
// are what "previous client artifacts were preserved" promises to leave alone.
// The contract is a different thing: it is not generated FROM the project, it
// says WHICH project — and it was already verified by the time it was written
// (the address resolved, the credential answered, the publishable key came
// back). Nothing is overwritten if the run never got that far.
func publishProjectContract(root, stage string) error {
	// `projectPath()` is the ONE declaration of where the contract lives. Naming
	// the directory again here would be a second truth about it, and the two
	// would drift the first time the layout moved.
	rel := projectPath()
	staged, err := os.ReadFile(filepath.Join(stage, rel))
	if os.IsNotExist(err) {
		// The link refused before it knew which project this is. There is
		// nothing true to keep.
		return nil
	}
	if err != nil {
		return err
	}
	live := filepath.Join(root, rel)
	if existing, err := os.ReadFile(live); err == nil && bytes.Equal(existing, staged) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		return err
	}
	return os.WriteFile(live, staged, 0o644)
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
	// Yedek de temp'te: müşterinin projesine hiçbir şey yazılmıyor. Aynı FS'te
	// ise taşıma anlıktır; değilse `moveTree` kopyaya düşer — bu dal yalnız web
	// SDK'sının İLK kurulumunda koşar.
	backupDir, err := os.MkdirTemp("", "palbase-link-dependencies-")
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
		if err := moveTree(live, backup); err != nil {
			return fmt.Errorf("preserve previous dependencies: %w", err)
		}
	}
	restore := func() error {
		if hadDependencies {
			return moveTree(backup, live)
		}
		return nil
	}
	if err := moveTree(staged, live); err != nil {
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("publish dependencies: %w; restore failed: %v; previous dependencies remain at %s", err, restoreErr, backup)
		}
		return err
	}
	if err := publishArtifacts(root, before, after); err != nil {
		// Move only the directory this invocation installed back into its
		// stage before restoring the original directory (or workspace link).
		if moveErr := moveTree(live, staged); moveErr != nil {
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
