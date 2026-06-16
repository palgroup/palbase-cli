package backend

import "testing"

// TestParseUpload_ThreadsXPalbaseUpload locks the codegen end of the upload
// metadata threading: an operation carrying the x-palbase-upload extension
// parses into swiftOp.upload (bucket + pathTemplate — the only fields, since the
// size/type limits live on the bucket and are storage-enforced). If this
// regresses, the generated client silently loses upload-kind — the exact
// "endpoint silently disappears" class. The generator end is locked by
// TestGenerateSpec_Upload (backend runtime); together they prove the full
// SDK→openapi→codegen path.
func TestParseUpload_ThreadsXPalbaseUpload(t *testing.T) {
	const spec = `{
  "openapi":"3.1.0","info":{"title":"t","version":"1"},
  "paths":{
    "/docs":{"post":{"operationId":"docs.upload",
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"object",
        "properties":{"id":{"type":"string"}},"required":["id"]}}}}},
      "x-palbase-upload":{
        "bucket":"docs","pathTemplate":"{userId}/{uploadId}-{filename}"
      }
    }},
    "/todos":{"get":{"operationId":"todos.list",
      "responses":{"200":{"content":{"application/json":{"schema":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}}}}}
    }}
  }
}`
	ops, err := parseOpenAPIForSwift([]byte(spec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var upload, list *swiftOp
	for i := range ops {
		switch ops[i].operationID {
		case "docs.upload":
			upload = &ops[i]
		case "todos.list":
			list = &ops[i]
		}
	}
	if upload == nil {
		t.Fatal("docs.upload op not parsed")
	}
	if upload.upload == nil {
		t.Fatal("docs.upload lost its x-palbase-upload → swiftOp.upload is nil (upload-kind silently dropped)")
	}
	if upload.upload.bucket != "docs" {
		t.Errorf("bucket = %q, want docs", upload.upload.bucket)
	}
	if upload.upload.pathTemplate != "{userId}/{uploadId}-{filename}" {
		t.Errorf("pathTemplate = %q", upload.upload.pathTemplate)
	}

	// A normal route MUST NOT acquire an upload — absence is what keeps it a
	// non-upload op downstream.
	if list == nil {
		t.Fatal("todos.list op not parsed")
	}
	if list.upload != nil {
		t.Errorf("todos.list erroneously parsed an upload: %+v", list.upload)
	}
}

// TestParseUpload_MalformedDropsToNil: a malformed x-palbase-upload (missing
// bucket or pathTemplate) parses to nil rather than a broken PBUploadEndpoint —
// visible-fail over silent-wrong.
func TestParseUpload_MalformedDropsToNil(t *testing.T) {
	const spec = `{
  "openapi":"3.1.0","info":{"title":"t","version":"1"},
  "paths":{
    "/docs":{"post":{"operationId":"docs.upload",
      "responses":{"200":{"description":"ok"}},
      "x-palbase-upload":{"pathTemplate":"{uploadId}"}
    }}
  }
}`
	ops, err := parseOpenAPIForSwift([]byte(spec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("want 1 op, got %d", len(ops))
	}
	if ops[0].upload != nil {
		t.Errorf("malformed x-palbase-upload (no bucket) should parse to nil, got %+v", ops[0].upload)
	}
}
