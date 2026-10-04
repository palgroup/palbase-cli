package backend

// swiftgen_release.go — the generator released with the SDK version the app
// pins, downloaded once per version.
//
// Every palbackend-ios release carries the generator prebuilt for macOS
// (universal) and Linux (x86_64, arm64; fully static), plus a checksum file
// and its Ed25519 signature, all built from the release's own sources
// (palbackend-ios-src: scripts/build-swiftgen.sh). Running that binary needs
// neither Xcode nor a Swift toolchain, which is what lets `palbase link
// --platform ios` run in a Linux shell (palbase-cli#9).
//
// THE SIGNATURE IS THE TRUST, NOT THE CHECKSUM. A checksum served beside the
// binary proves the download is intact, and nothing more: whoever can replace
// one release asset can replace both. The binary runs on every machine that
// links an iOS app, so the checksum file must be signed by a key this CLI
// carries — its private half exists only as the SDK repo's release secret.
// The signed bytes start with the release's version, so one release's signed
// files do not verify when served as another's.

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// swiftgenReleaseBase is where release assets download from. A var only so
// tests can serve their own release.
var swiftgenReleaseBase = sdkRepository + "/releases/download"

// swiftgenChecksumsAsset lists every generator asset of a release, one
// `<sha256>  <asset>` line each (`shasum -a 256` output); its signature is the
// same name + ".sig", base64 of a 64-byte Ed25519 signature.
const swiftgenChecksumsAsset = "palbase-swiftgen.sha256"

// swiftgenSigningKeys are the Ed25519 public keys a checksum file may be signed
// by (palbackend-ios-src: scripts/swiftgen-signing.pub). A LIST so the key can
// be rotated: a new key is added here before releases start using it, and the
// old one stays while versions signed by it are still pinned. A var only so
// tests can sign their own releases.
var swiftgenSigningKeys = []string{
	"C0JBUFnQ+czMUvuymapVz/36S7BNmn85wV02dHdltcg=",
}

// swiftgenHost is the machine the generator will run on. A var only so tests
// can ask for another one.
var swiftgenHost = func() (goos, goarch string) { return runtime.GOOS, runtime.GOARCH }

// swiftgenAsset names the release asset that runs on this machine.
func swiftgenAsset() (string, error) {
	goos, goarch := swiftgenHost()
	switch {
	case goos == "darwin":
		// One universal file: the CLI also ships for Intel Macs.
		return "palbase-swiftgen-darwin-universal", nil
	case goos == "linux" && goarch == "amd64":
		return "palbase-swiftgen-linux-x86_64", nil
	case goos == "linux" && goarch == "arm64":
		return "palbase-swiftgen-linux-aarch64", nil
	}
	return "", fmt.Errorf("the Swift generator is published for macOS and for Linux on x86_64 and arm64, not for %s/%s — "+
		"run this on one of those", goos, goarch)
}

// errSwiftgenNotPublished is a release without the generator assets.
var errSwiftgenNotPublished = errors.New("not published")

// downloadSwiftgen returns the generator released with palbackend-ios v,
// downloading and verifying it on first use for that version.
func downloadSwiftgen(v sdkVersion, w io.Writer) (string, error) {
	asset, err := swiftgenAsset()
	if err != nil {
		return "", err
	}
	cacheRoot, err := swiftgenToolHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheRoot, "swiftgen-v"+v.String())
	tool := filepath.Join(dir, asset)
	// THE CACHE IS RE-VERIFIED, NOT TRUSTED BY NAME. The signed checksum file
	// is kept beside the binary, and every run checks the signature and the
	// binary's hash again (~0.2 s for the 60 MB Linux one): a file that a
	// crash truncated, that something else wrote there, or that no signature
	// ever covered is discarded and downloaded again rather than run.
	if isRegularFile(tool) {
		if err := verifyCachedSwiftgen(v, dir, asset); err == nil {
			return tool, nil
		}
		for _, f := range []string{tool, filepath.Join(dir, swiftgenChecksumsAsset), filepath.Join(dir, swiftgenChecksumsAsset+".sig")} {
			_ = os.Remove(f)
		}
	}

	fmt.Fprintf(w, "→ downloading the Swift generator released with palbackend-ios %s (one-time per SDK version) ...\n", v)
	sums, err := fetchReleaseAsset(v, swiftgenChecksumsAsset, 1<<20)
	if errors.Is(err, errSwiftgenNotPublished) {
		return "", fmt.Errorf("there is no prebuilt Swift generator for palbackend-ios %s: either no such release "+
			"exists (check the version this project asks for) or it predates them — pin a published release, then link again", v)
	}
	if err != nil {
		return "", fmt.Errorf("download the Swift generator for palbackend-ios %s: %w", v, err)
	}
	sig, err := fetchReleaseAsset(v, swiftgenChecksumsAsset+".sig", 1<<10)
	if errors.Is(err, errSwiftgenNotPublished) {
		return "", fmt.Errorf("palbackend-ios %s publishes no signature for its Swift generator, so it is not run — "+
			"update the package to a newer release, then link again", v)
	}
	if err != nil {
		return "", fmt.Errorf("download the Swift generator's signature for palbackend-ios %s: %w", v, err)
	}
	if err := verifySwiftgenSignature(v, sums, sig); err != nil {
		return "", fmt.Errorf("palbackend-ios %s: %w — nothing was installed", v, err)
	}
	want, err := checksumOf(sums, asset)
	if err != nil {
		return "", fmt.Errorf("palbackend-ios %s: %w", v, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	removeAbandonedDownloads(dir)
	// Written beside its final name and renamed only once verified, so an
	// interrupted or tampered download never becomes the cached generator.
	tmp, err := os.CreateTemp(dir, asset+".*"+partialSuffix)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	got, err := downloadReleaseAsset(v, asset, tmp)
	// Synced before the rename, so the final name never points at bytes a power
	// cut could still lose.
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("download %s for palbackend-ios %s: %w", asset, v, err)
	}
	if got != want {
		return "", fmt.Errorf("%s for palbackend-ios %s does not match its published checksum (got %s, want %s) — nothing was installed",
			asset, v, got, want)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	// The signed checksum file lands BEFORE the binary's name does, so the
	// binary is never under its final name without what verifies it.
	if err := writeFileAtomically(filepath.Join(dir, swiftgenChecksumsAsset), sums); err != nil {
		return "", err
	}
	if err := writeFileAtomically(filepath.Join(dir, swiftgenChecksumsAsset+".sig"), sig); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), tool); err != nil {
		return "", err
	}
	return tool, nil
}

// verifyCachedSwiftgen checks a cached generator the way a download is checked:
// the stored checksum file signed for version v by a trusted key, and the
// binary matching it.
func verifyCachedSwiftgen(v sdkVersion, dir, asset string) error {
	sums, err := os.ReadFile(filepath.Join(dir, swiftgenChecksumsAsset))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, swiftgenChecksumsAsset+".sig"))
	if err != nil {
		return err
	}
	if err := verifySwiftgenSignature(v, sums, sig); err != nil {
		return err
	}
	want, err := checksumOf(sums, asset)
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(dir, asset))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fmt.Errorf("cached %s does not match its checksum", asset)
	}
	return nil
}

// writeFileAtomically writes data under path via a synced temporary file, so
// path never holds a partial write.
func writeFileAtomically(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*"+partialSuffix)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// partialSuffix marks a download that has not been verified yet.
const partialSuffix = ".partial"

// removeAbandonedDownloads deletes partial files a killed run left behind —
// each is a full ~60 MB on Linux. Only old ones: a younger file may belong to
// another CLI process downloading the same generator right now.
func removeAbandonedDownloads(dir string) {
	stale, _ := filepath.Glob(filepath.Join(dir, "*"+partialSuffix))
	for _, path := range stale {
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(path)
		}
	}
}

// swiftgenSignedPayload is what a release signs: the version it is, then its
// checksum file (palbackend-ios-src: scripts/swiftgen-signing.swift). Without
// the version, the signed files of one release would verify on any other, and
// whoever can write to the public repo could serve an older generator under a
// newer version.
func swiftgenSignedPayload(v sdkVersion, sums []byte) []byte {
	return append([]byte("palbase-swiftgen v"+v.String()+"\n"), sums...)
}

// verifySwiftgenSignature accepts a checksum file only when one of the keys
// this CLI carries signed it as release v's.
func verifySwiftgenSignature(v sdkVersion, sums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("%s.sig is not an Ed25519 signature", swiftgenChecksumsAsset)
	}
	payload := swiftgenSignedPayload(v, sums)
	for _, encoded := range swiftgenSigningKeys {
		pub, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && len(pub) == ed25519.PublicKeySize && ed25519.Verify(ed25519.PublicKey(pub), payload, raw) {
			return nil
		}
	}
	return fmt.Errorf("the Swift generator's checksum file is not signed for this release by a key this CLI trusts")
}

// checksumOf picks one asset's digest out of a checksum file.
func checksumOf(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			sum := strings.ToLower(fields[0])
			if _, err := hex.DecodeString(sum); err != nil || len(sum) != 64 {
				return "", fmt.Errorf("%s lists a malformed checksum for %s", swiftgenChecksumsAsset, asset)
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("%s lists no %s", swiftgenChecksumsAsset, asset)
}

// swiftgenIdleTimeout bounds SILENCE, not the download. A Linux generator is
// ~60 MB, so any deadline on the whole request fails every time on a link slow
// enough — and a failed download here ends in the committed client being
// deleted. What must not happen is waiting forever on a connection that went
// quiet. A var only so tests can shorten it.
var swiftgenIdleTimeout = 60 * time.Second

func releaseAssetURL(v sdkVersion, asset string) string {
	return swiftgenReleaseBase + "/v" + v.String() + "/" + asset
}

// idleBody cancels its request when no byte arrives for swiftgenIdleTimeout.
type idleBody struct {
	io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(swiftgenIdleTimeout)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.cancel()
	return b.ReadCloser.Close()
}

func openReleaseAsset(v sdkVersion, asset string) (io.ReadCloser, error) {
	url := releaseAssetURL(v, asset)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(swiftgenIdleTimeout, cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		timer.Stop()
		cancel()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("GET %s: no response for %s", url, swiftgenIdleTimeout)
		}
		return nil, err
	}
	timer.Reset(swiftgenIdleTimeout)
	body := &idleBody{ReadCloser: resp.Body, timer: timer, cancel: cancel}
	if resp.StatusCode == http.StatusNotFound {
		_ = body.Close()
		return nil, errSwiftgenNotPublished
	}
	if resp.StatusCode != http.StatusOK {
		_ = body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return body, nil
}

func fetchReleaseAsset(v sdkVersion, asset string, limit int64) ([]byte, error) {
	body, err := openReleaseAsset(v, asset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(io.LimitReader(body, limit))
}

// downloadReleaseAsset streams an asset into dst and returns its sha256.
func downloadReleaseAsset(v sdkVersion, asset string, dst io.Writer) (string, error) {
	body, err := openReleaseAsset(v, asset)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
