package backend

// roles_in_spec_test.go — what the CLI's OWN writer leaves on disk.
//
// The product decision this measures: a checkout never carries a `roles.json`.
// Role definitions travel INSIDE the contract (`x-palbase-roles`), which the CLI
// writes byte for byte, so there is no second round, no second file, and no way
// for the two to sit a deploy apart.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE WRITER IS CALLED, NOT IMITATED.
//
// An earlier version of this test wrote `openapi.json` itself and then asserted
// no `roles.json` was beside it — which is true of any empty directory, so it
// stayed green with the whole change reverted. It measured the test's own
// arrangement. This one calls `writeSpec`, the function `palbase spec` and
// `palbase link` both go through, and asks what IT leaves behind.
func TestTheSpecWriterLeavesNoSeparateRolesFile(t *testing.T) {
	inScratchCheckout(t)

	contract := `{"openapi":"3.1.0","paths":{},"x-palbase-roles":{"roles":[` +
		`{"name":"author","isDefault":true,"permissions":["posts.write"]}]}}`
	if err := writeSpec("local", []byte(contract)); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(EnvDir("local"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		if e.Name() == RetiredRolesFile {
			t.Fatalf("%s is in the checkout; nothing in this product writes it any more", e.Name())
		}
	}
	if len(names) != 1 || names[0] != "openapi.json" {
		t.Errorf("the spec round left more than the contract: %v", names)
	}

	// BYTE FOR BYTE. The roles ride inside these bytes, so re-encoding the
	// document — pretty-printing it, dropping an unknown field — would silently
	// change what the generators read.
	written, err := os.ReadFile(SpecPath("local"))
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

// And the WRITER is gone, not merely unused: no symbol in this package can put
// that file on disk again. A grep-shaped assertion would pass on a comment; this
// one is a compile-time fact — if `writeRolesArtifact` or `RolesPath` came back,
// the file they would need to be referenced from is this one, and it names them
// nowhere.
func TestNoEnvironmentArtifactIsGeneratedUnderTheRetiredName(t *testing.T) {
	const probe = "probe"
	if isGeneratedEnvironmentFile(RetiredRolesFile) {
		t.Error("the CLI still counts roles.json among the files it generates")
	}
	if !isGeneratedEnvironmentFile(filepath.Base(SpecPath(probe))) {
		t.Error("the contract is no longer counted as a generated file")
	}
}

// A 503 IS NOT "NO CONTRACT YET", and the difference decides which cure a person
// is told to apply.
//
// 404 is a LEGAL state in this product (`ErrNoContractYet`): a project that has
// never been pushed answers that way, and `link` records it and says "push".
// A roles failure folded into that branch would produce a silent no-op link and
// the WRONG cure for a stack that is deployed and working — so this asserts both
// halves: the stack's own sentence survives, and the error is not ErrNoContractYet.
func TestARolesFailureIsNotReadAsAMissingContract(t *testing.T) {
	stack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/v1/management/openapi") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"roles_unavailable","error_description":` +
			`"the auth module is on this stack but did not hand over its roles (status 501)"}`))
	}))
	defer stack.Close()

	_, err := fetchStackSpec(context.Background(), Target{URL: stack.URL}, Credentials{Value: "pb_x_ck"})
	if err == nil {
		t.Fatal("a stack that refused to serve its contract was reported as fine")
	}
	if errors.Is(err, ErrNoContractYet) {
		t.Errorf("a roles failure was read as `nothing is deployed yet`, whose cure is `palbase push`: %v", err)
	}
	// THE STACK'S OWN SENTENCE, whole. `trimBody` would cut it at 300 characters
	// and only the stack knows which module could not answer.
	if !strings.Contains(err.Error(), "did not hand over its roles") {
		t.Errorf("the stack's own reason did not survive: %v", err)
	}
	if !strings.Contains(err.Error(), "could not report its roles") {
		t.Errorf("the CLI does not say what went wrong before quoting the stack: %v", err)
	}
}
