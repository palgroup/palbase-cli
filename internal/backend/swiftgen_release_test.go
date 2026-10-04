package backend

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeRelease is one palbackend-ios release serving its generator assets.
type fakeRelease struct {
	assetHits atomic.Int32
	// tamper, when set, is served in place of the asset — its checksum line
	// still describes the real one.
	tamper string
	// sums and sig are what the release serves for the checksum file and its
	// signature; a test edits them to forge or drop one. sig "" is a 404.
	sums, sig string
}

func (r *fakeRelease) downloads() int { return int(r.assetHits.Load()) }

// serveRelease publishes body as this host's generator asset of release v,
// with a checksum file listing all three platform assets the way
// `shasum -a 256` writes them.
func serveRelease(t *testing.T, v sdkVersion, body string) *fakeRelease {
	t.Helper()
	asset, err := swiftgenAsset()
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(body))
	sums := fmt.Sprintf("%s  palbase-swiftgen-darwin-universal\n%s  palbase-swiftgen-linux-x86_64\n%s  palbase-swiftgen-linux-aarch64\n",
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	sums = strings.Replace(sums, strings.Repeat(map[string]string{
		"palbase-swiftgen-darwin-universal": "a",
		"palbase-swiftgen-linux-x86_64":     "b",
		"palbase-swiftgen-linux-aarch64":    "c",
	}[asset], 64), hex.EncodeToString(sum[:]), 1)

	// Signed by a key the test makes the CLI trust — the release secret's
	// stand-in.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	prevKeys := swiftgenSigningKeys
	swiftgenSigningKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	t.Cleanup(func() { swiftgenSigningKeys = prevKeys })

	rel := &fakeRelease{sums: sums, sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums))) + "\n"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v" + v.String() + "/" + swiftgenChecksumsAsset:
			_, _ = w.Write([]byte(rel.sums))
		case "/v" + v.String() + "/" + swiftgenChecksumsAsset + ".sig":
			if rel.sig == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(rel.sig))
		case "/v" + v.String() + "/" + asset:
			rel.assetHits.Add(1)
			if rel.tamper != "" {
				_, _ = w.Write([]byte(rel.tamper))
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	prev := swiftgenReleaseBase
	swiftgenReleaseBase = srv.URL
	t.Cleanup(func() { swiftgenReleaseBase = prev })
	return rel
}

// useToolHome redirects the CLI's tool cache into the test.
func useToolHome(t *testing.T) string {
	t.Helper()
	cache := t.TempDir()
	prev := swiftgenToolHome
	swiftgenToolHome = func() (string, error) { return cache, nil }
	t.Cleanup(func() { swiftgenToolHome = prev })
	return cache
}

func TestDownloadSwiftgenVerifiesAndCachesPerVersion(t *testing.T) {
	v := sdkVersion{0, 66, 0}
	rel := serveRelease(t, v, "#!/bin/sh\necho generator\n")
	cache := useToolHome(t)

	var out bytes.Buffer
	tool, err := downloadSwiftgen(v, &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), "downloading the Swift generator released with palbackend-ios 0.66.0")
	require.Equal(t, filepath.Join(cache, "swiftgen-v0.66.0"), filepath.Dir(tool))
	info, err := os.Stat(tool)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&0o100, "the generator is executable")

	// Second call: the cache answers, nothing is downloaded or printed.
	out.Reset()
	again, err := downloadSwiftgen(v, &out)
	require.NoError(t, err)
	require.Equal(t, tool, again)
	require.Empty(t, out.String())
	require.Equal(t, 1, rel.downloads())

	leftovers, _ := filepath.Glob(filepath.Join(cache, "swiftgen-v0.66.0", "*.partial"))
	require.Empty(t, leftovers)
}

func TestATamperedGeneratorIsNeverInstalled(t *testing.T) {
	v := sdkVersion{0, 66, 0}
	rel := serveRelease(t, v, "the real generator")
	rel.tamper = "something else"
	cache := useToolHome(t)

	_, err := downloadSwiftgen(v, &bytes.Buffer{})
	require.ErrorContains(t, err, "does not match its published checksum")
	entries, _ := filepath.Glob(filepath.Join(cache, "swiftgen-v0.66.0", "*"))
	require.Empty(t, entries, "neither the binary nor a partial file is left behind")

	// And the next run downloads again rather than trusting a cache entry.
	rel.tamper = ""
	_, err = downloadSwiftgen(v, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, 2, rel.downloads())
}

// TestOnlyASignedChecksumFileIsTrusted is the authenticity half: a checksum
// file proves a download is intact, not that it is ours, because whoever can
// replace the binary on a release can replace the checksum beside it. Each of
// these is that attacker — or a release that never got signed — and each must
// leave nothing installed and download nothing executable.
func TestOnlyASignedChecksumFileIsTrusted(t *testing.T) {
	v := sdkVersion{0, 66, 0}
	evil := "#!/bin/sh\necho pwned\n"
	evilSum := sha256.Sum256([]byte(evil))

	cases := map[string]func(rel *fakeRelease){
		"no signature": func(rel *fakeRelease) { rel.sig = "" },
		"checksum file swapped after signing": func(rel *fakeRelease) {
			asset, _ := swiftgenAsset()
			rel.sums = hex.EncodeToString(evilSum[:]) + "  " + asset + "\n"
			rel.tamper = evil
		},
		"signed by a key the CLI does not carry": func(rel *fakeRelease) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			rel.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(other, []byte(rel.sums)))
		},
		"not a signature": func(rel *fakeRelease) { rel.sig = "bm90IGEgc2lnbmF0dXJl\n" },
	}
	for name, forge := range cases {
		t.Run(name, func(t *testing.T) {
			rel := serveRelease(t, v, "the real generator")
			forge(rel)
			cache := useToolHome(t)

			_, err := downloadSwiftgen(v, &bytes.Buffer{})
			require.Error(t, err)
			entries, _ := filepath.Glob(filepath.Join(cache, "swiftgen-v0.66.0", "*"))
			require.Empty(t, entries, "nothing is installed")
			require.Zero(t, rel.downloads(), "the executable is not even fetched")
		})
	}
}

func TestAReleaseWithoutTheGeneratorIsNamed(t *testing.T) {
	serveRelease(t, sdkVersion{0, 66, 0}, "x")
	useToolHome(t)
	_, err := downloadSwiftgen(sdkVersion{0, 60, 0}, &bytes.Buffer{})
	require.ErrorContains(t, err, "palbackend-ios 0.60.0 has no prebuilt Swift generator on its release")
}

func TestTheAssetMatchesTheHost(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "palbase-swiftgen-darwin-universal"},
		{"darwin", "amd64", "palbase-swiftgen-darwin-universal"},
		{"linux", "amd64", "palbase-swiftgen-linux-x86_64"},
		{"linux", "arm64", "palbase-swiftgen-linux-aarch64"},
	}
	prev := swiftgenHost
	t.Cleanup(func() { swiftgenHost = prev })
	for _, c := range cases {
		swiftgenHost = func() (string, string) { return c.goos, c.goarch }
		got, err := swiftgenAsset()
		require.NoError(t, err)
		require.Equal(t, c.want, got)
	}
	swiftgenHost = func() (string, string) { return "windows", "amd64" }
	_, err := swiftgenAsset()
	require.ErrorContains(t, err, "not for windows/amd64")
}

func TestChecksumOf(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := []byte(sum + "  palbase-swiftgen-linux-x86_64\n" + sum + " *palbase-swiftgen-linux-aarch64\n")
	got, err := checksumOf(sums, "palbase-swiftgen-linux-aarch64")
	require.NoError(t, err)
	require.Equal(t, sum, got)
	_, err = checksumOf(sums, "palbase-swiftgen-darwin-universal")
	require.ErrorContains(t, err, "lists no palbase-swiftgen-darwin-universal")
	_, err = checksumOf([]byte("xyz  palbase-swiftgen-linux-x86_64\n"), "palbase-swiftgen-linux-x86_64")
	require.ErrorContains(t, err, "malformed checksum")
}
