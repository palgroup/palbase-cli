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

	"github.com/palgroup/palbase-cli/internal/envname"
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
// CHECKOUT YİNE DE TOPLANIR — ama BURADA DEĞİL: bu CLI'ın bir ara sürümü
// stage'i checkout'un içine açıyordu, ve o sürümle yarıda kesilmiş bir koşunun
// kalıntısı hâlâ duruyor olabilir. Toplamayı `runLink` en başta, her erken
// redden ÖNCE yapar (FR-009): burada, stage açılırken yapılan toplama platform
// doğrulamasının ve eski düzen kapısının ARKASINDAYDI, ve o redlerden biriyle
// dönen her koşu kalıntıyı yerinde bırakıyordu. Toplama emekli yolların TEK
// listesinden geçer (`reapRetiredArtifacts`) — ikinci bir süpürücü, listeyle
// bir gün ayrışacak ikinci bir gerçek olurdu. Üst dizine DOKUNULMAZ: orası bu
// aracın alanı değil ve silmek de bir yazma fiilidir.
func newLinkStage() (string, error) {
	return os.MkdirTemp("", "palbase-link-*")
}

// gradleRootFiles mark the root of a Gradle build; gradleDirectoryFiles mark
// any directory Gradle builds, a root or one of its modules.
var (
	gradleRootFiles      = []string{"settings.gradle.kts", "settings.gradle"}
	gradleDirectoryFiles = []string{"build.gradle.kts", "build.gradle", "settings.gradle.kts", "settings.gradle"}
)

// holdsOneOf reports whether dir holds a regular file by one of names.
func holdsOneOf(dir string, names []string) bool {
	for _, name := range names {
		if isRegularFile(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

// LinkedCheckoutAbove is, for a Gradle directory, the nearest directory ABOVE
// it that holds palbase/project.json, or "". `palbase link` refuses such a
// directory and names that checkout (refuseInsideALinkedCheckout); `palbase
// doctor` sends people there too, rather than to a link it refuses.
//
// ONLY A GRADLE DIRECTORY IS ASKED (D-025) — one holding build.gradle(.kts) or
// settings.gradle(.kts). The Gradle plugin is what reads palbase/ from above
// the directory it builds (the module, the Gradle root and one level up,
// FR-205), so only there does a second copy change what a build reads. A
// directory with no Gradle build — a monorepo's web or iOS app, a docs/ folder
// — is linked where it is, as it was before this rule (20e5d7e).
//
// THE WALK IS WHERE THE DIRECTORY IS ON DISK. A shell that reached it through a
// symlink names it by the link, and the link's parent is not the directory the
// plugin reads: Gradle resolves a build's directories.
//
// A MODULE IS WALKED UP TO ITS GRADLE ROOT, whatever repository it sits in: the
// plugin reads a module's palbase/ together with its Gradle root's, and a `.git`
// between them — an app module kept as a submodule — bounds neither.
//
// THE WALK ENDS AT A GRADLE ROOT THAT IS A REPOSITORY OF ITS OWN — it carries
// `.git`, the directory or the file a worktree or submodule points with. The
// plugin looks above a root project only while it carries no `.git` (FR-205),
// so such a root is a checkout of its own, even inside another project's
// directory.
//
// AND AT A GRADLE ROOT WHOSE PARENT IS NOT LINKED. A linked directory right
// above a Gradle root — React Native's and Flutter's android/ (FR-015) — is the
// checkout that build reads, and a second copy below it would take its place;
// one further up — a monorepo whose root is linked to the backend, its app in
// apps/android — is out of that build's reach, and the app is linked where its
// Gradle root is.
func LinkedCheckoutAbove(dir string) string {
	at, err := filepath.Abs(dir)
	if err != nil || !holdsOneOf(at, gradleDirectoryFiles) {
		return ""
	}
	if onDisk, err := filepath.EvalSymlinks(at); err == nil {
		at = onDisk
	}
	for {
		gradleRoot := holdsOneOf(at, gradleRootFiles)
		if gradleRoot {
			if _, err := os.Lstat(filepath.Join(at, ".git")); err == nil {
				return ""
			}
		}
		parent := filepath.Dir(at)
		if parent == at {
			return ""
		}
		if isRegularFile(filepath.Join(parent, filepath.FromSlash(projectPath()))) {
			return parent
		}
		if gradleRoot {
			return ""
		}
		at = parent
	}
}

// refuseInsideALinkedCheckout is FR-016's refusal of a link in dir, or nil
// when dir is not a Gradle directory inside a linked checkout.
//
// WHAT A COPY HERE DOES TO A BUILD (plugin 2.5.0, FR-205): a Gradle root
// holding palbase/project.json is a checkout of its own to the plugin, which
// then never looks above it, so its copy is read INSTEAD of the checkout's
// own, without a word; anywhere else the plugin finds both copies in reach and
// refuses the build. Either way this directory's copy is the wrong one.
//
// A COPY ALREADY HERE IS NAMED. An older CLI linked wherever it ran — in a
// React Native checkout android/ was the only place it found Android (FR-015)
// — so the second copy this rule keeps from being born may be here already.
// Sent to link at the root with that copy left in place, the build would go on
// compiling it, in silence, or refuse without saying why the link was fine.
func refuseInsideALinkedCheckout(dir string) error {
	linked := LinkedCheckoutAbove(dir)
	if linked == "" {
		return nil
	}
	why := fmt.Sprintf("A %s/ written here would be a second copy: a build in this directory would read it instead of "+
		"the checkout's own, or find both and refuse", RootDir())
	if info, err := os.Stat(filepath.Join(dir, RootDir())); err == nil && info.IsDir() {
		why = fmt.Sprintf("The %s/ here, from an earlier link, is a second copy: a build in this directory reads it instead of "+
			"the checkout's own, or finds both and refuses — delete it and commit that deletion", RootDir())
	}
	return fmt.Errorf("%s is inside the checkout linked at %s (%s) — run `palbase link` there; "+
		"--platform names this app's platform if it is not found from there.\n  %s", dir, linked, projectPath(), why)
}

func runLink(ctx context.Context, o linkOpts, w io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	// THE SWEEP FIRST (FR-009): what an older CLI left in this checkout goes
	// before anything below can refuse. It sat inside newLinkStage, behind the
	// platform checks and the layout gate, so every refused link returned with
	// the litter still in place.
	//
	// What it KEEPS needs no line of its own here: the only entry it keeps is a
	// hidden root git tracks, and CarriesLegacyLayout refuses exactly that one,
	// by name, right below.
	reapRetiredArtifacts(root)
	// A GRADLE DIRECTORY INSIDE A LINKED CHECKOUT IS NOT ONE (FR-016). An Android
	// module has a build.gradle.kts, so detection finds an app in it, and a link
	// there wrote a second palbase/ that the Gradle plugin read before the
	// checkout's own — measured: a debug APK carried the module copy's stale
	// address, build green (verification D3a). Asked again here, after the
	// sweep and before every network call, for the auth refresh that reaches
	// runLink without `palbase link`'s own gate.
	if err := refuseInsideALinkedCheckout(root); err != nil {
		return err
	}
	// THE RETIRED LAYOUT IS REFUSED NEXT, BEFORE ANY SIDE EFFECT OF ITS OWN.
	//
	// The sweep above is not one: it removes only what an older CLI produced
	// and git does not track, which is exactly what this gate no longer counts.
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
	// AND THE NEW LAYOUT IS SPELLED `palbase`, EXACTLY — before the stage is
	// made, because the stage is what another spelling gets around.
	if err := refuseAnotherSpellingOfTheRoot(root); err != nil {
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
	// A WEB LINK'S OWN DIRECTORIES ARE SPELLED EXACTLY TOO, for the same reason
	// as `palbase` — and only a web link writes them.
	if slices.Contains(platforms, webPlatform) {
		if err := refuseAnotherSpellingOfAWebDirectory(root); err != nil {
			return err
		}
	}
	installWebSDK := slices.Contains(platforms, webPlatform) && !isRegularFile(filepath.Join(root, palbeGenBin))
	stage, err := newLinkStage()
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
		if err := releaseForProject(o, root, w); err != nil {
			return fmt.Errorf("link failed (%v); and the stack linked here by address could not be released: %w", workErr, err)
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
	if _, err := io.WriteString(w, strings.ReplaceAll(output.String(), stage, root)); err != nil {
		return err
	}
	// A COPY AN EARLIER LINK LEFT IN android/ IS SAID, NOT DELETED. The refusal
	// inside android/ sends people here, and a link here writes only the
	// checkout's own palbase/ — while that copy's project.json makes android/ the
	// checkout the Gradle plugin reads (secondCopyIn). Asked of the published
	// checkout, so the two copies compared are the ones a build finds.
	if copied := secondCopyIn(root, gradleRootOf(AndroidCheckout(root))); copied != "" {
		if _, err := fmt.Fprintf(w, "\n%s\n", copied); err != nil {
			return err
		}
	}
	return releaseForProject(o, root, w)
}

// refuseAnotherSpellingOfTheRoot refuses a checkout whose root holds a
// directory, or a link, that a disk ignoring letter case and Unicode form takes
// for RootDir() without its being spelled so — `Palbase/`, say.
//
// THE STAGE KNOWS ITS DIRECTORIES BY THE EXACT NAME. runLink keys `mutable` by
// RootDir(), so `Palbase/` was not copied into the stage but symlinked there,
// like every directory a link never writes; and on the disk the stage lives on
// — every Mac's — the stage's `palbase/` then IS that symlink. Measured in the
// final review of wave 1: the link wrote environments and swept them straight
// in the real checkout, and a link that failed left a half-written environment
// there while it said "previous client artifacts were preserved".
//
// DECIDED BY THE NAME, ON EVERY DISK. What resolves the stage's `palbase/` is
// the stage's disk, not the checkout's: measured with the checkout on a
// case-sensitive volume and the stage in the default temp directory, the link
// wrote into `Palbase/` all the same and then said to commit a `palbase/` that
// did not exist.
//
// NOT BY ADDING THE SPELLING TO `mutable`: collectArtifacts reads the stage by
// the on-disk name and skips every top-level directory `mutable` does not
// hold, so `after` would come back without a single file of `before`, and the
// publish would delete them all.
//
// Only a directory or a link counts — exactly what the stage symlinks. A plain
// file by that name is copied, and the link fails on it by itself.
func refuseAnotherSpellingOfTheRoot(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	exact, other := false, ""
	for _, e := range entries {
		name := e.Name()
		if name == RootDir() {
			exact = true
			continue
		}
		if other == "" && (e.IsDir() || e.Type()&os.ModeSymlink != 0) && envname.SameDirectory(name, RootDir()) {
			other = name
		}
	}
	switch {
	case other == "":
		return nil
	case exact:
		// Only a case-sensitive disk holds both; renaming onto the real one
		// would collide.
		return fmt.Errorf("this checkout holds both `%s` and `%s`, which are one directory on a Mac; "+
			"move `%s` aside and run `palbase link` again", envname.Label(other), RootDir(), envname.Label(other))
	}
	return fmt.Errorf("this checkout spells the palbase/ directory `%s`; rename it to `%s` and run `palbase link` again",
		envname.Label(other), RootDir())
}

// webWrittenDirs are the top-level directories a web link writes into — the
// web half of runLink's `mutable`.
var webWrittenDirs = []string{"src", "app", "pages", "public"}

// refuseAnotherSpellingOfAWebDirectory refuses a web checkout whose root holds
// one of webWrittenDirs spelled another way — `App/`, say — the way
// refuseAnotherSpellingOfTheRoot refuses `Palbase/`.
//
// THE SAME HOLE, ONE LEVEL OVER. `mutable` names these directories exactly, so
// `App/` was symlinked into the stage instead of copied; on a Mac's disk the
// stage's `app/` then IS that symlink, the wiring edited `App/layout.tsx` in the
// real checkout, and a link that failed afterwards left the edit behind while it
// said "previous client artifacts were preserved". Decided by the name, not the
// disk, because the stage's disk decides as much as the checkout's.
//
// WEB ONLY: an Xcode project's `App/` or a native `Src/` is written by no link,
// and the stage leaves it alone as it should.
func refuseAnotherSpellingOfAWebDirectory(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	exact := map[string]bool{}
	for _, e := range entries {
		exact[e.Name()] = true
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		for _, dir := range webWrittenDirs {
			if name == dir || !envname.SameDirectory(name, dir) {
				continue
			}
			if exact[dir] {
				// Only a case-sensitive disk holds both; renaming onto the real one
				// would collide.
				return fmt.Errorf("this checkout holds both `%s` and `%s`, which are one directory on a Mac; "+
					"move `%s` aside and run `palbase link` again", envname.Label(name), dir, envname.Label(name))
			}
			return fmt.Errorf("this web app spells its %s/ directory `%s`; rename it to `%s` and run `palbase link` again",
				dir, envname.Label(name), dir)
		}
	}
	return nil
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
	// THROUGH THE SAME REPLACEMENT AS EVERY OTHER WRITER OF THIS FILE. This wrote
	// with `os.WriteFile` while WriteTarget was made atomic, and measured in
	// review that left the defect the atomic write exists to remove — a write cut
	// short here produced `{"project":"prd_` and every later verb refused the
	// checkout as invalid JSON. `link` writes this file more often than the
	// migration does.
	return replaceFileAtomically(live, staged)
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

// publishArtifacts makes the checkout hold what the stage holds.
//
// A DIRECTORY RENAMED BY LETTER CASE ALONE GOES FIRST (FR-012). Publishing is
// file by file, and a case-only rename is not a file: on a case-insensitive
// disk `staging/openapi.json` is a path `Staging/openapi.json` already answers
// to, so the new path read as a file that appeared during the link and the
// whole publish was refused — measured on APFS. The directory is renamed, the
// files it held are carried to their new paths, and publishing goes on as for
// any other change; a publish that is refused takes the rename back.
func publishArtifacts(root string, before, after map[string]artifactFile) error {
	moved, undo, err := followCaseRenames(root, before, after)
	if err != nil {
		return err
	}
	if err := publishFiles(root, moved, after); err != nil {
		if undoErr := undo(); undoErr != nil {
			return fmt.Errorf("%w; and the directories renamed for it could not be named back: %v", err, undoErr)
		}
		return err
	}
	return nil
}

// followCaseRenames renames each environment directory of the checkout whose
// name the stage changed by letter case alone, and returns `before` as it reads
// after that, with the way back.
//
// ONE OLD SPELLING FOR ONE NEW ONE. Two directories that fold to one name can
// both exist only on a case-sensitive disk, and there their files publish as
// they always did: as removals and creations.
func followCaseRenames(root string, before, after map[string]artifactFile) (map[string]artifactFile, func() error, error) {
	base := filepath.Join(RootDir(), envSubdir)
	was, is := envDirsOf(base, before), envDirsOf(base, after)
	// EVERY DIRECTORY THE CHECKOUT HOLDS, NOT ONLY THOSE WITH FILES IN IT (T016
	// review). `before` names files, so an empty `Staging/` was no directory at
	// all here: on a Mac the link wrote staging's files into it and the stage
	// renamed it, while the checkout kept the old spelling under a line saying
	// it was renamed — until the next link.
	entries, err := os.ReadDir(filepath.Join(root, base))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			was[e.Name()] = true
		}
	}
	renames := map[string]string{}
	for old := range was {
		if is[old] {
			continue
		}
		news := foldedOnly(old, is, was)
		if len(news) != 1 || len(foldedOnly(news[0], was, is)) != 1 {
			continue
		}
		// TWO DIRECTORIES ARE NOT ONE RENAMED (T016 review). A rename is for one
		// directory under two spellings; where the new name is another
		// directory — a case-sensitive disk can hold an empty `staging/` beside
		// `Staging/` — it failed with "file exists", on every link. Those
		// publish as they did before a rename was ever made: as removals and
		// creations.
		if !oneDirectoryOrNone(filepath.Join(root, base, old), filepath.Join(root, base, news[0])) {
			continue
		}
		renames[old] = news[0]
	}
	var done []string
	undo := func() error {
		var errs []error
		for _, old := range done {
			errs = append(errs, os.Rename(filepath.Join(root, base, renames[old]), filepath.Join(root, base, old)))
		}
		return errors.Join(errs...)
	}
	for old, next := range renames {
		// ONE STEP IS ENOUGH: os.Rename lets a name through to itself in another
		// case when the disk says both are the same file (measured on APFS).
		if err := os.Rename(filepath.Join(root, base, old), filepath.Join(root, base, next)); err != nil {
			return nil, nil, errors.Join(err, undo())
		}
		done = append(done, old)
	}
	moved := make(map[string]artifactFile, len(before))
	for path, file := range before {
		if rest, ok := strings.CutPrefix(path, base+string(filepath.Separator)); ok {
			if env, inside, nested := strings.Cut(rest, string(filepath.Separator)); nested && renames[env] != "" {
				path = filepath.Join(base, renames[env], inside)
			}
		}
		moved[path] = file
	}
	return moved, undo, nil
}

// oneDirectoryOrNone reports whether `from` is a directory that can take the
// name `to`: nothing is there yet, or the disk says both names are one file —
// a spelling it does not tell apart.
func oneDirectoryOrNone(from, to string) bool {
	source, err := os.Lstat(from)
	if err != nil || !source.IsDir() {
		return false
	}
	target, err := os.Lstat(to)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	return err == nil && os.SameFile(source, target)
}

// envDirsOf names every environment directory a set of files lives in.
func envDirsOf(base string, files map[string]artifactFile) map[string]bool {
	dirs := map[string]bool{}
	for path := range files {
		if rest, ok := strings.CutPrefix(path, base+string(filepath.Separator)); ok {
			if env, _, nested := strings.Cut(rest, string(filepath.Separator)); nested {
				dirs[env] = true
			}
		}
	}
	return dirs
}

// foldedOnly names the entries of `in` that are `name` spelled in another
// letter case — or another Unicode form, which APFS ignores the same way
// (envname.SameDirectory) — and are not entries of `notIn`.
func foldedOnly(name string, in, notIn map[string]bool) []string {
	var out []string
	for other := range in {
		if other != name && !notIn[other] && envname.SameDirectory(other, name) {
			out = append(out, other)
		}
	}
	return out
}

func publishFiles(root string, before, after map[string]artifactFile) error {
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
	// A DIRECTORY THE SWEEP EMPTIED GOES WITH ITS FILES (FR-017). Publishing is
	// file by file, so a removed environment stayed in the checkout as an empty
	// directory under the line that said it was removed. Only inside the
	// environments directory, and only while a directory is empty.
	environments := filepath.Join(root, filepath.Dir(filepath.FromSlash(EnvDir("any"))))
	for _, path := range applied {
		if _, kept := after[path]; kept {
			continue
		}
		for dir := filepath.Dir(filepath.Join(root, path)); strings.HasPrefix(dir, environments+string(filepath.Separator)); dir = filepath.Dir(dir) {
			if os.Remove(dir) != nil {
				break
			}
		}
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
