package backend

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var defaultIgnoreDirs = map[string]bool{
	".git":         true,
	".palbase":     true,
	"node_modules": true,
	".next":        true, // Next.js build output — bloat, never part of the backend bundle
	// `palbase build` stages a return-binding-injected COPY of controllers/ here
	// (it must sit beside controllers/ so the copies' ../models relative imports
	// still resolve). It is removed on exit, but a SIGKILL leaves it behind — and
	// an orphan in the project root would otherwise ride along in the next deploy
	// tarball as a second, shadow copy of every controller.
	stagedControllersDir: true,
	// `palbase build` stages a return-binding-injected COPY of controllers/ here
	// (it must sit beside controllers/ so the copies' ../models relative imports
	// still resolve). It is removed on exit, but a SIGKILL leaves it behind — and
	// an orphan in the project root would otherwise ride along in the next deploy
	// tarball as a second, shadow copy of every controller.
}

// stagedControllersDir is the in-project staging tree build-check.js writes.
// Duplicated as a Go constant (the JS side owns the real definition) so the
// tarball walk can skip it; archive_test pins the two spellings together.
const stagedControllersDir = ".palbase-build-controllers"

// hasIgnoredSegment reports whether ANY path segment of rel is an ignored dir.
// The walk must skip an ignored dir at EVERY depth, not just the top level: a
// platform-mode project commonly nests a web app (e.g. `web/node_modules`,
// `web/.next`) under the backend dir, and matching only the first segment let
// those nested trees through — a real deploy shipped a 357MB `web/node_modules`,
// the base64 tarball blew past Temporal's 4MB gRPC arg cap, and the deploy died
// with an opaque RESOURCE_EXHAUSTED (the silent-deploy-hang outsiders hit).
func hasIgnoredSegment(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if defaultIgnoreDirs[seg] {
			return true
		}
	}
	return false
}

// defaultIgnoreFiles are filename globs ALWAYS excluded from the deploy bundle,
// regardless of .palignore — they carry local secrets that must never leave the
// developer's machine inside a tarball the running tenant code (or anyone with
// deploy-bundle read) could recover. `palbase secret pull` writes decrypted
// branch secrets to .env.local, so .env* must be excluded by default (CWE-538).
var defaultIgnoreFiles = []string{
	".env", ".env.*", "*.env",
	"*.pem", "*.key", "*.p8", "*.p12", "*.pfx",
	"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
}

// BuildTarball walks dir and returns a gzip-compressed tar with paths relative
// to dir (no wrapper directory), matching what /internal/push expects. It skips
// defaultIgnoreDirs and any glob in an optional .palignore file at the root.
func BuildTarball(dir string) ([]byte, error) {
	patterns, err := loadPalignore(filepath.Join(dir, ".palignore"))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			// Skip an ignored dir at ANY depth (e.g. top-level node_modules AND a
			// nested web/node_modules) — SkipDir prunes the whole subtree.
			if hasIgnoredSegment(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		// Never follow symlinks (CWE-61): filepath.Walk uses Lstat, so a symlink
		// reports as a non-dir and would otherwise reach writeTarFile, which used
		// to os.Stat/os.Open the TARGET — packing files OUTSIDE the project tree
		// (e.g. a `creds -> ~/.palbase/credentials.json` entry in a cloned starter
		// template). Skip any non-regular file entirely.
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		if hasIgnoredSegment(rel) ||
			matchesAny(filepath.ToSlash(rel), defaultIgnoreFiles) ||
			matchesAny(filepath.ToSlash(rel), patterns) {
			return nil
		}
		return writeTarFile(tw, dir, rel)
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeTarFile(tw *tar.Writer, dir, rel string) error {
	full := filepath.Join(dir, rel)
	// Lstat (not Stat) so a symlink is never followed — defense in depth behind
	// the walk-level skip. Only regular files are packed; anything else (symlink,
	// device, socket) is silently dropped rather than dereferenced.
	fi, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(rel)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func loadPalignore(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var pats []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pats = append(pats, line)
	}
	return pats, sc.Err()
}

// extractTarGz unpacks a gzip-compressed tar stream from r into dst, creating
// dst if it does not exist. Entries are restricted to regular files (symlinks,
// devices, sockets are dropped). Path traversal is blocked: any entry whose
// cleaned path escapes dst — i.e. starts with ".." after cleaning — is rejected
// (CWE-22). Safe to call on bundles from the server; any malformed entry is an
// error, not a silent skip.
// isDeployArtifact reports whether a bundle entry is something the DEPLOY
// produced rather than something the developer wrote.
//
// A deployed bundle carries both. `.palbase/` holds the compiled CJS/ESM output
// and `<controller>.ts.meta.json` holds the route metadata the pod's router
// reads beside each source file. Both are regenerated from scratch on every
// deploy and neither is editable input, so unpacking them into a checkout only
// adds files a developer has to learn to ignore. The pod still gets them — this
// filter applies to `pull`/`clone`/`create`, not to what the server stores.
func isDeployArtifact(rel string) bool {
	slash := filepath.ToSlash(rel)
	return slash == ".palbase" ||
		strings.HasPrefix(slash, ".palbase/") ||
		strings.HasSuffix(slash, ".meta.json") ||
		// The bundle carries the platform's OWN repository — one "initial
		// template" commit plus palbase-active-version / palbase-fastpath-version
		// bookkeeping. Writing it into the destination is wrong twice over.
		//
		// On `pull` the destination is the developer's checkout, so this
		// overwrites their git history with the platform's. It did not corrupt
		// anything only by accident: git stores loose objects 0444, so the second
		// pull in any directory died with "permission denied" on the first
		// read-only object it had already written. An idempotence bug was
		// standing in for a data-loss bug.
		//
		// On `create` inside an existing repository the same entries produce the
		// nested .git that `ensureGitRepo` had just printed "no nested .git
		// created" about.
		//
		// Deploy version history lives on the server — `palbase deploys` — not in
		// a .git the platform ships to you.
		slash == ".git" ||
		strings.HasPrefix(slash, ".git/")
}

// extractSourceTree unpacks a deployed bundle into dst, keeping only what the
// developer authored (see isDeployArtifact).
func extractSourceTree(dst string, r io.Reader) error {
	return extractTarGzFiltered(dst, r, isDeployArtifact)
}

func extractTarGz(dst string, r io.Reader) error {
	return extractTarGzFiltered(dst, r, nil)
}

// extractTarGzFiltered is extractTarGz with an optional skip predicate applied
// to each entry's cleaned relative path.
func extractTarGzFiltered(dst string, r io.Reader, skip func(string) bool) error {
	// Resolve dst to an absolute path BEFORE the containment check below uses
	// it as a prefix. That check compares strings, so a relative dst breaks it:
	// with dst ".", filepath.Join(".", ".git") is ".git", which is neither
	// prefixed by "./" nor equal to ".", and every entry looks like an escape.
	// `project create` extracts into the working directory and hit exactly that.
	// Abs+Clean gives a stable root, and the traversal guard keeps its meaning.
	root, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("resolve destination %q: %w", dst, err)
	}
	dst = root
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		// Normalize and guard against path traversal (CWE-22).
		// filepath.Clean collapses "../../evil" → "../../evil"; we then
		// check whether joining with dst yields something under dst.
		clean := filepath.Clean(hdr.Name)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("tar entry %q would escape destination (path traversal rejected)", hdr.Name)
		}
		// After the traversal guard, so a filtered entry is still validated
		// rather than waved through on the strength of its name.
		if skip != nil && skip(clean) {
			continue
		}

		target := filepath.Join(dst, clean)
		// Belt-and-suspenders: after Join+Clean, verify the result is still
		// rooted under dst. filepath.Join already calls Clean, but an absolute
		// hdr.Name (e.g. "/etc/passwd") would still be caught here.
		if !strings.HasPrefix(target, dst+string(filepath.Separator)) && target != dst {
			return fmt.Errorf("tar entry %q escapes destination (path traversal rejected)", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create directory %q: %w", clean, err)
			}
		case tar.TypeReg, 0: // TypeReg is '\x00' in old bundles
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create parent for %q: %w", clean, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
			if err != nil {
				return fmt.Errorf("create file %q: %w", clean, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("write file %q: %w", clean, err)
			}
			f.Close()
		default:
			// Skip symlinks, hard links, devices, etc. — same policy as BuildTarball.
		}
	}
	return nil
}

func matchesAny(rel string, patterns []string) bool {
	base := filepath.Base(rel)
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}
