package backend

// THE GATE, in the two forms the defect could take.
//
// The defect was `socialRequest` building a bare `http.NewRequestWithContext`
// for `/auth/oauth/config` — a path the server refuses plaintext on
// (v2/internal/server/sealed.go:361-380) — and sending it with an ordinary HTTP
// client. Nothing in this repository objected. The stack objected, in
// production, with a 415 and a code.
//
// So there are two things to hold:
//
//  1. AT RUNTIME: a plaintext request to a sealed-required path cannot leave
//     this process. sealedclient.Guard sits on the transport every stack client
//     already uses, so the refusal is local and names the path.
//  2. AT COMPILE TIME: every client that ADDRESSES a stack is wrapped in that
//     guard. A future package that builds its own `http.Client` for a Target
//     would otherwise walk straight past rule 1 — and three such clients
//     already existed when this was written, none of them wrong today and every
//     one of them a place the next `/auth/` read could have landed.
//
// Rule 2 has no exemption list, deliberately. Its scope is derived from the
// mechanism: a file that constructs an `http.Client` AND names `Target` is a
// file that talks to a stack. The control-plane clients in internal/auth and
// internal/transport name no Target — they address the panel, whose own
// `/auth/login` page is that product's route and not a stack's — so they are
// outside the rule rather than excused from it.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/sealedclient"
	"github.com/stretchr/testify/require"
)

func TestAPlaintextSealedPathCannotLeaveThisProcess(t *testing.T) {
	reached := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	client := stackClient(Target{URL: srv.URL})
	for _, path := range []string{"/auth/oauth/config?platform=ios", "/auth/login", "/auth/user"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
		require.NoError(t, err)
		_, err = client.Do(req)
		require.ErrorContains(t, err, "refusing to send", "a plaintext %s left the process", path)
		require.ErrorContains(t, err, "internal/sealedclient")
	}
	require.Zero(t, reached, "a request the guard refused still reached the server")

	// The exemptions and the paths outside the prefix go through untouched: a
	// guard that refused everything would be a guard nobody could keep.
	for _, path := range []string{"/auth/oauth/callback/google", "/v1/management/keys", "/readyz"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
		require.NoError(t, err)
		res, err := client.Do(req)
		require.NoError(t, err, "the guard refused %s, which the server does not require sealing on", path)
		require.NoError(t, res.Body.Close())
	}
	require.Equal(t, 3, reached)
}

func TestASealedRequestPassesTheGuard(t *testing.T) {
	var sawHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get(sealedclient.EnvelopeHeaderName)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/auth/user", nil)
	require.NoError(t, err)
	req.Header.Set(sealedclient.EnvelopeHeaderName, "an-envelope")
	res, err := stackClient(Target{URL: srv.URL}).Do(req)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())
	require.Equal(t, "an-envelope", sawHeader)
}

// Rule 2. A file that builds an http.Client and names Target is a file that
// talks to a stack, and every one of them must hand its transport to the guard.
func TestEveryStackClientIsGuarded(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "node_modules", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}

		buildsClient, namesTarget, guards := false, false, false
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CompositeLit:
				if sel, ok := v.Type.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" && sel.Sel.Name == "Client" {
						buildsClient = true
					}
				}
			case *ast.SelectorExpr:
				if pkg, ok := v.X.(*ast.Ident); ok {
					if pkg.Name == "sealedclient" && v.Sel.Name == "Guard" {
						guards = true
					}
					if pkg.Name == "backend" && v.Sel.Name == "Target" {
						namesTarget = true
					}
				}
			case *ast.Ident:
				if v.Name == "Target" && file.Name.Name == "backend" {
					namesTarget = true
				}
			}
			return true
		})
		if buildsClient && namesTarget && !guards {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, offenders,
		"these files build an HTTP client for a Palbase stack without sealedclient.Guard on its transport, "+
			"so a request under /auth/ from any of them would go out in the clear and come back 415")
}

// The rule the CLI mirrors must stay the server's rule. If this ever disagrees
// with v2/internal/server/sealed.go the CLI either seals what the stack cannot
// open or sends what the stack refuses.
func TestTheGuardAsksTheOneRule(t *testing.T) {
	require.True(t, sealedclient.Required("/auth/oauth/config"))
	require.False(t, sealedclient.Required("/v1/management/auth/social-auth"))
}
