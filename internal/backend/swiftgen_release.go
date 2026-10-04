package backend

// swiftgen_release.go — the generator released with the SDK version the app
// pins, downloaded once per version.
//
// Every palbackend-ios release carries the generator prebuilt for macOS
// (universal) and Linux (x86_64, arm64; fully static), plus a checksum file,
// all built from the release's own sources (palbackend-ios-src:
// scripts/build-swiftgen.sh). Running that binary needs neither Xcode nor a
// Swift toolchain, which is what lets `palbase link --platform ios` run in a
// Linux shell (palbase-cli#9).

import (
	"bufio"
	"crypto/sha256"
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
// `<sha256>  <asset>` line each (`shasum -a 256` output).
const swiftgenChecksumsAsset = "palbase-swiftgen.sha256"

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
	// The version is in the directory name, and a release asset never changes,
	// so "is this the right generator?" and "is it here?" are one question.
	dir := filepath.Join(cacheRoot, "swiftgen-v"+v.String())
	tool := filepath.Join(dir, asset)
	if isRegularFile(tool) {
		return tool, nil
	}

	fmt.Fprintf(w, "→ downloading the Swift generator released with palbackend-ios %s (one-time per SDK version) ...\n", v)
	sums, err := fetchReleaseAsset(v, swiftgenChecksumsAsset, 1<<20)
	if errors.Is(err, errSwiftgenNotPublished) {
		return "", fmt.Errorf("palbackend-ios %s has no prebuilt Swift generator on its release — "+
			"update the package to a newer release, then link again", v)
	}
	if err != nil {
		return "", fmt.Errorf("download the Swift generator for palbackend-ios %s: %w", v, err)
	}
	want, err := checksumOf(sums, asset)
	if err != nil {
		return "", fmt.Errorf("palbackend-ios %s: %w", v, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Written beside its final name and renamed only once verified, so an
	// interrupted or tampered download never becomes the cached generator.
	tmp, err := os.CreateTemp(dir, asset+".*.partial")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	got, err := downloadReleaseAsset(v, asset, tmp)
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
	if err := os.Rename(tmp.Name(), tool); err != nil {
		return "", err
	}
	return tool, nil
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

var swiftgenHTTP = &http.Client{Timeout: 5 * time.Minute}

func releaseAssetURL(v sdkVersion, asset string) string {
	return swiftgenReleaseBase + "/v" + v.String() + "/" + asset
}

func openReleaseAsset(v sdkVersion, asset string) (io.ReadCloser, error) {
	url := releaseAssetURL(v, asset)
	resp, err := swiftgenHTTP.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, errSwiftgenNotPublished
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

func fetchReleaseAsset(v sdkVersion, asset string, limit int64) ([]byte, error) {
	body, err := openReleaseAsset(v, asset)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, limit))
}

// downloadReleaseAsset streams an asset into dst and returns its sha256.
func downloadReleaseAsset(v sdkVersion, asset string, dst io.Writer) (string, error) {
	body, err := openReleaseAsset(v, asset)
	if err != nil {
		return "", err
	}
	defer body.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
