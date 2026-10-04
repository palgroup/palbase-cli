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
	"time"

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
	// key is the release secret's stand-in, for a test that signs something else.
	key ed25519.PrivateKey
	// serve, when set, writes the asset's body itself (a slow or stalled one).
	serve func(w http.ResponseWriter, body string)
}

// signAs signs sums the way a release of version v does.
func (r *fakeRelease) signAs(v sdkVersion, sums string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(r.key, swiftgenSignedPayload(v, []byte(sums)))) + "\n"
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

	rel := &fakeRelease{sums: sums, key: priv}
	rel.sig = rel.signAs(v, sums)
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
			if rel.serve != nil {
				rel.serve(w, body)
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

// TestACachedGeneratorIsReverifiedNotTrustedByName: the cache is where a
// verified download lives, so whatever sits there is checked again before it
// runs — a file a crash truncated, one something else put there, or one no
// signature ever covered is replaced by a fresh, verified download.
func TestACachedGeneratorIsReverifiedNotTrustedByName(t *testing.T) {
	v := sdkVersion{0, 66, 0}
	cases := map[string]func(t *testing.T, dir, tool string){
		"truncated binary": func(t *testing.T, dir, tool string) {
			require.NoError(t, os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755))
		},
		"binary without its signed checksum file": func(t *testing.T, dir, tool string) {
			require.NoError(t, os.Remove(filepath.Join(dir, swiftgenChecksumsAsset+".sig")))
		},
		"checksum file rewritten to match a swapped binary": func(t *testing.T, dir, tool string) {
			evil := []byte("#!/bin/sh\necho pwned\n")
			sum := sha256.Sum256(evil)
			asset, _ := swiftgenAsset()
			require.NoError(t, os.WriteFile(tool, evil, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, swiftgenChecksumsAsset),
				[]byte(hex.EncodeToString(sum[:])+"  "+asset+"\n"), 0o644))
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			rel := serveRelease(t, v, "the real generator")
			useToolHome(t)
			tool, err := downloadSwiftgen(v, &bytes.Buffer{})
			require.NoError(t, err)

			// Intact, the cache answers without the network.
			_, err = downloadSwiftgen(v, &bytes.Buffer{})
			require.NoError(t, err)
			require.Equal(t, 1, rel.downloads())

			spoil(t, filepath.Dir(tool), tool)
			var out bytes.Buffer
			again, err := downloadSwiftgen(v, &out)
			require.NoError(t, err)
			require.Contains(t, out.String(), "downloading", "a spoiled cache is downloaded again, not run")
			got, err := os.ReadFile(again)
			require.NoError(t, err)
			require.Equal(t, "the real generator", string(got))
			require.Equal(t, 2, rel.downloads())
		})
	}
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
			rel.key = other
			rel.sig = rel.signAs(v, rel.sums)
		},
		// The replay: every byte is genuinely ours and genuinely signed — for
		// ANOTHER release. Served under this one, it would run an older
		// generator for a newer SDK.
		"genuinely signed, for another release": func(rel *fakeRelease) {
			rel.sig = rel.signAs(sdkVersion{0, 59, 0}, rel.sums)
		},
		// What the first design signed: the checksum file alone.
		"signed without the version": func(rel *fakeRelease) {
			rel.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(rel.key, []byte(rel.sums)))
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
	require.ErrorContains(t, err, "there is no prebuilt Swift generator for palbackend-ios 0.60.0")
	require.ErrorContains(t, err, "no such release exists", "a mistyped version gets the same 404 and must not be told to upgrade")
}

// TestADownloadIsBoundedBySilenceNotBySize: the Linux generator is ~60 MB, so a
// deadline on the whole request fails on every slow link — and a failed
// download ends with the committed client deleted. A slow download that keeps
// moving finishes; a connection that goes quiet is given up on.
func TestADownloadIsBoundedBySilenceNotBySize(t *testing.T) {
	prev := swiftgenIdleTimeout
	swiftgenIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { swiftgenIdleTimeout = prev })
	v := sdkVersion{0, 66, 0}

	t.Run("slow but moving outlives the idle limit", func(t *testing.T) {
		rel := serveRelease(t, v, strings.Repeat("generator ", 8))
		rel.serve = func(w http.ResponseWriter, body string) {
			for i := 0; i < len(body); i += 10 {
				_, _ = w.Write([]byte(body[i : i+10]))
				w.(http.Flusher).Flush()
				time.Sleep(120 * time.Millisecond) // 8 × 120 ms: three idle limits in total
			}
		}
		useToolHome(t)
		_, err := downloadSwiftgen(v, &bytes.Buffer{})
		require.NoError(t, err)
	})

	t.Run("a stalled connection is abandoned", func(t *testing.T) {
		rel := serveRelease(t, v, "generator")
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		rel.serve = func(w http.ResponseWriter, body string) {
			_, _ = w.Write([]byte(body[:3]))
			w.(http.Flusher).Flush()
			<-release
		}
		cache := useToolHome(t)
		started := time.Now()
		_, err := downloadSwiftgen(v, &bytes.Buffer{})
		require.Error(t, err)
		require.Less(t, time.Since(started), 5*time.Second)
		entries, _ := filepath.Glob(filepath.Join(cache, "swiftgen-v0.66.0", "*"))
		require.Empty(t, entries, "the half-written file is not left behind")
	})
}

func TestAbandonedDownloadsAreRemoved(t *testing.T) {
	v := sdkVersion{0, 66, 0}
	serveRelease(t, v, "generator")
	cache := useToolHome(t)
	dir := filepath.Join(cache, "swiftgen-v0.66.0")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	old := filepath.Join(dir, "palbase-swiftgen-linux-x86_64.123.partial")
	young := filepath.Join(dir, "palbase-swiftgen-linux-x86_64.456.partial")
	for _, f := range []string{old, young} {
		require.NoError(t, os.WriteFile(f, []byte("half"), 0o644))
	}
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(old, twoHoursAgo, twoHoursAgo))

	_, err := downloadSwiftgen(v, &bytes.Buffer{})
	require.NoError(t, err)
	require.NoFileExists(t, old, "a killed run's leftover is cleaned up")
	require.FileExists(t, young, "another process may be writing this one right now")
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
