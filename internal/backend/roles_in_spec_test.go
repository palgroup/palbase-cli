package backend

// roles_in_spec_test.go — what `palbase spec` leaves on disk.
//
// The product decision this measures: a checkout never carries a `roles.json`.
// Role definitions travel INSIDE the contract (`x-palbase-roles`), which the CLI
// writes byte for byte, so there is no second round, no second file, and no way
// for the two to sit a deploy apart.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The contract the stack serves carries the roles; the CLI writes it VERBATIM.
// So the checkout gets exactly one file, and it holds everything.
func TestRolesInSpecLeaveNoSeparateFile(t *testing.T) {
	root := t.TempDir()
	env := filepath.Join(root, RootDir(), envSubdir, "local")
	if err := os.MkdirAll(env, 0o755); err != nil {
		t.Fatal(err)
	}
	contract := `{"openapi":"3.1.0","x-palbase-roles":{"roles":[]},"paths":{},"x-palbase-roles":{"roles":[` +
		`{"name":"author","isDefault":true,"permissions":["posts.write"]}]}}`
	if err := os.WriteFile(filepath.Join(env, "openapi.json"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == RetiredRolesFile {
			t.Fatalf("%s is in the checkout; nothing in this product writes it any more", e.Name())
		}
	}

	written, err := os.ReadFile(filepath.Join(env, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != contract {
		t.Error("the contract was not written byte for byte")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["x-palbase-roles"]; !ok {
		t.Error("the written contract carries no x-palbase-roles")
	}
}

// NOTHING IN THE PACKAGE NAMES THE RETIRED FILE ANY MORE — except the one place
// whose job is to REMEMBER it. A second copy of a retired name drifts from the
// first, and then the thing that recognises somebody's leftover file is looking
// for a name nothing ever wrote.
func TestTheRetiredNameLivesInExactlyOneRegistry(t *testing.T) {
	if RetiredRolesFile != "roles.json" {
		t.Fatalf("the registry does not hold the name the CLI used to write: %q", RetiredRolesFile)
	}
	if !strings.Contains(strings.Join(LegacyMarkers(), " "), RetiredRolesFile) {
		t.Error("LegacyMarkers no longer recognises the retired root layout's role file")
	}
}
