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
