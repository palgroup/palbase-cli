package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebConfigCarriesCurrentSealingRoot(t *testing.T) {
	inScratchCheckout(t)
	const root = "MSpMCEuCo76fF82x5Sa9d+9h8RRzNLC3/JiTe0WOvhI="
	for _, currentRoot := range []string{root, ""} {
		envs := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
			"main": {AppID: "project", BaseURL: "http://127.0.0.1:1234", APIKey: "pb_project_cPUBLISHABLE", SealedRoot: currentRoot},
		}}
		if _, err := writeWebArtifacts(envs, nil, &strings.Builder{}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if currentRoot == "" {
			if _, present := cfg["sealed_root"]; present {
				t.Fatal("relink retained a previous stack's sealing root")
			}
		} else if cfg["sealed_root"] != root {
			t.Fatal("web config lost the stack's sealing root")
		}
	}
}
