package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `palbase start` düştü, iki kez, ve iki kez de sebebi docker'ın kendisiydi:
// önce `docker-credential-desktop not found` (Colima kurulumunda ~/.docker/config.json
// hâlâ Docker Desktop'ın credsStore'unu işaret ediyordu), sonra
// `docker: unknown command: docker compose` (compose eklentisi yok). İkisi de
// start'ın içinden docker'ın ham hatası olarak çıktı; doctor — ortam teşhisi için
// var olan komut — ikisini de yoklamıyordu. (Ölçüldü 2026-08-29, müşteri koşusu.)
func TestDockerProbesNameEachPrerequisiteThatCanFailAStart(t *testing.T) {
	cfgWithCreds := func(t *testing.T, store string) string {
		t.Helper()
		dir := t.TempDir()
		if store != "" {
			if err := os.MkdirAll(filepath.Join(dir, ".docker"), 0o755); err != nil {
				t.Fatal(err)
			}
			body := `{"credsStore":"` + store + `"}`
			if err := os.WriteFile(filepath.Join(dir, ".docker", "config.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	found := func(names ...string) lookupFunc {
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		return func(name string) (string, error) {
			if set[name] {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
	}

	cases := []struct {
		name      string
		look      lookupFunc
		composeOK bool
		home      string
		wantBad   string
		wantHint  string
	}{
		{
			name: "docker yok", look: found(), composeOK: false,
			home: cfgWithCreds(t, ""), wantBad: "docker", wantHint: "install",
		},
		{
			name: "compose eklentisi yok", look: found("docker"), composeOK: false,
			home: cfgWithCreds(t, ""), wantBad: "compose", wantHint: "compose",
		},
		{
			name: "credsStore yardımcısı yok", look: found("docker"), composeOK: true,
			home: cfgWithCreds(t, "desktop"), wantBad: "creds", wantHint: "docker-credential-desktop",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(context.Context, string, ...string) ([]byte, error) {
				if tc.composeOK {
					return []byte("Docker Compose version v2.29.0"), nil
				}
				return nil, errors.New("unknown command: docker compose")
			}
			lines := dockerProbes(context.Background(), tc.look, run, tc.home)

			var bad *probeLine
			for i := range lines {
				if !lines[i].ok && lines[i].label == tc.wantBad {
					bad = &lines[i]
				}
			}
			if bad == nil {
				t.Fatalf("%q kusuru bildirilmedi; satırlar: %+v", tc.wantBad, lines)
			}
			if !strings.Contains(strings.ToLower(bad.detail), strings.ToLower(tc.wantHint)) {
				t.Errorf("satır eyleme dönüştürülebilir değil, %q geçmiyor: %q", tc.wantHint, bad.detail)
			}
		})
	}

	// NEGATİF KONTROL: her şey yerindeyken hiçbir satır kırmızı olmamalı.
	t.Run("hepsi iyi", func(t *testing.T) {
		run := func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Docker Compose version v2.29.0"), nil
		}
		lines := dockerProbes(context.Background(), found("docker", "docker-credential-osxkeychain"),
			run, cfgWithCreds(t, "osxkeychain"))
		if len(lines) != 3 {
			t.Fatalf("üç satır bekleniyordu, %d geldi: %+v", len(lines), lines)
		}
		for _, l := range lines {
			if !l.ok {
				t.Errorf("sağlıklı ortamda kırmızı satır: %+v", l)
			}
		}
	})
}

// `palbase push` düştü ve sebebi `bun is not installed` idi
// (`internal/backend/stack_bundle.go:59-61`): bir backend'i bu runtime için
// bundle eden BUN, ve `bunVersion()` da (stack_bundle.go:977) build yolunda
// ona bağlı. Ortam teşhisi için var olan komut — `doctor` — node'u yokluyordu,
// bun'ı hiç anmıyordu; yani "neden çalışmıyor" sorusunun cevabı doctor'ın
// çıktısında YOKTU ve push'un ham hatası olarak geldi.
func TestToolchainProbesNameBunBecausePushBundlesWithIt(t *testing.T) {
	found := func(names ...string) lookupFunc {
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		return func(name string) (string, error) {
			if set[name] {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
	}
	version := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("1.3.9\n"), nil
	}

	lineFor := func(t *testing.T, lines []probeLine, label string) probeLine {
		t.Helper()
		for _, l := range lines {
			if l.label == label {
				return l
			}
		}
		t.Fatalf("%q satırı hiç basılmadı; satırlar: %+v", label, lines)
		return probeLine{}
	}

	t.Run("bun yok", func(t *testing.T) {
		lines := toolchainProbes(context.Background(), found("node"), version)
		bun := lineFor(t, lines, "bun")
		if bun.ok {
			t.Fatalf("bun PATH'te yokken satır yeşil: %+v", bun)
		}
		// Eyleme dönüştürülebilirlik: satır kurulum yolunu söylemeli.
		if !strings.Contains(bun.detail, "bun.sh") {
			t.Errorf("kurulum tavsiyesi yok: %q", bun.detail)
		}
		// Ve NEDEN gerektiğini — push'un bundle'ı bun'la ürettiğini.
		if !strings.Contains(strings.ToLower(bun.detail), "push") {
			t.Errorf("satır bun'ın neden gerektiğini söylemiyor: %q", bun.detail)
		}
	})

	t.Run("bun var", func(t *testing.T) {
		lines := toolchainProbes(context.Background(), found("node", "bun"), version)
		bun := lineFor(t, lines, "bun")
		if !bun.ok {
			t.Fatalf("bun PATH'teyken satır kırmızı: %+v", bun)
		}
		if !strings.Contains(bun.detail, "1.3.9") {
			t.Errorf("satır sürümü taşımıyor: %q", bun.detail)
		}
	})

	// NEGATİF KONTROL: node satırı korunur — bun onun YANINA eklendi, yerine
	// değil.
	t.Run("node satırı duruyor", func(t *testing.T) {
		lines := toolchainProbes(context.Background(), found("bun"), version)
		node := lineFor(t, lines, "node")
		if node.ok {
			t.Fatalf("node PATH'te yokken satır yeşil: %+v", node)
		}
		if !strings.Contains(node.detail, "palbase build") {
			t.Errorf("node satırı neyin düşeceğini söylemiyor: %q", node.detail)
		}
	})
}
