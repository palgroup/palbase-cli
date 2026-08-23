package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeREST struct {
	method, path, body string
	status             int
	answer             string
}

func (f *fakeREST) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.method, f.path, f.body = method, path, string(body)
	st := f.status
	if st == 0 {
		st = http.StatusOK
	}
	ans := f.answer
	if ans == "" {
		ans = "{}"
	}
	return st, []byte(ans), nil
}

func runStorage(t *testing.T, rest *fakeREST, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestBucketsLiveOnTheStackNotInAFile is the change this task exists for.
func TestBucketsLiveOnTheStackNotInAFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	rest := &fakeREST{}
	if _, err := runStorage(t, rest, "add", "avatars", "--public", "--max-size", "5MB"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "storage.ts")); !os.IsNotExist(err) {
		t.Fatal("a file was written: buckets have one home now, and it is the stack")
	}
	if rest.method != http.MethodPut || rest.path != "/v1/management/storage/buckets/avatars" {
		t.Fatalf("called %s %s", rest.method, rest.path)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(rest.body), &sent); err != nil {
		t.Fatalf("body is not JSON: %s", rest.body)
	}
	if sent["public"] != true {
		t.Fatalf("--public did not travel: %s", rest.body)
	}
	if _, ok := sent["fileSizeLimit"]; !ok {
		t.Fatalf("--max-size did not travel: %s", rest.body)
	}
}

// TestRemoveActuallyRemoves. It used to edit the file and leave the live bucket
// and its objects in place, so the command looked reversible while nothing moved.
func TestRemoveActuallyRemoves(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{status: http.StatusNoContent}

	if _, err := runStorage(t, rest, "remove", "avatars"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if rest.method != http.MethodDelete || rest.path != "/v1/management/storage/buckets/avatars" {
		t.Fatalf("called %s %s, want a DELETE", rest.method, rest.path)
	}
}

// TestListReadsTheStack.
func TestListReadsTheStack(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{answer: `[{"name":"avatars","public":true,"object_count":3,"total_bytes":900}]`}

	out, err := runStorage(t, rest, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rest.method != http.MethodGet || rest.path != "/v1/management/storage/buckets" {
		t.Fatalf("called %s %s", rest.method, rest.path)
	}
	if !strings.Contains(out, "avatars") || !strings.Contains(out, "public") {
		t.Fatalf("the stack's answer did not reach the person:\n%s", out)
	}
}

// TestAddRefusesLocallyWhatTheStackWouldRefuse keeps the checks that were worth
// keeping: a bad name, a bad MIME list or an unparseable size never leaves here.
func TestAddRefusesLocallyWhatTheStackWouldRefuse(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"add", "Bad Name"},
		{"add", "ok", "--mime", "notamime"},
		{"add", "ok", "--max-size", "5 bananas"},
	} {
		rest := &fakeREST{}
		if _, err := runStorage(t, rest, args...); err == nil {
			t.Fatalf("%v was accepted", args)
		}
		if rest.method != "" {
			t.Fatalf("%v reached the stack before being refused", args)
		}
	}
}

func TestParseSize_BinaryUnits(t *testing.T) {
	cases := map[string]int64{
		"5MB":   5 * 1024 * 1024,
		"20MB":  20 * 1024 * 1024,
		"1GB":   1024 * 1024 * 1024,
		"500kb": 500 * 1024,
		"1024":  1024,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

// The body has to be in the SPELLING the storage module reads.
//
// It was not. The CLI sent `file_size_limit` and `allowed_mime_types`; the
// module's bucketDeclaration reads `fileSizeLimit` and `allowedMimeTypes`. The
// management layer forwards raw bytes, so nothing complained — the bucket was
// created with no size limit and no type list, and the only way to notice was
// to upload a file that should have been refused.
func TestTheBucketBodyIsSpelledTheWayTheModuleReadsIt(t *testing.T) {
	rest := &fakeREST{}
	if _, err := runStorage(t, rest, "add", "posts", "--public", "--max-size", "10mb",
		"--mime", "image/png,image/jpeg"); err != nil {
		t.Fatal(err)
	}
	sent := rest.body
	for _, want := range []string{`"fileSizeLimit"`, `"allowedMimeTypes"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("the body does not carry %s — the module will ignore it:\n%s", want, sent)
		}
	}
	for _, gone := range []string{`"file_size_limit"`, `"allowed_mime_types"`} {
		if strings.Contains(sent, gone) {
			t.Errorf("the body still uses %s, which nothing reads:\n%s", gone, sent)
		}
	}
}
