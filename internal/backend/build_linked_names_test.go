package backend

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// namedStack answers the four name reads `palbase build` makes, holding the
// given secrets and flags — one environment's vault, as the management surface
// serves it.
func namedStack(t *testing.T, secrets, flags []string) *httptest.Server {
	t.Helper()
	list := func(key, field string, names []string) string {
		rows := make([]string, 0, len(names))
		for _, n := range names {
			rows = append(rows, `{"`+field+`":"`+n+`"}`)
		}
		return `{"` + key + `":[` + strings.Join(rows, ",") + `]}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/v1/management/secrets":
			_, _ = w.Write([]byte(list("secrets", "name", secrets)))
		case "/v1/management/flags":
			_, _ = w.Write([]byte(list("flags", "key", flags)))
		case "/v1/management/storage/buckets":
			_, _ = w.Write([]byte(`{"buckets":[]}`))
		default:
			// /admin/roles: a stack that defines no roles answers 404, and
			// fetchStackRoles reads that as an empty list.
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// THE TYPES COME FROM THE LINKED ENVIRONMENT, NOT FROM THE STACK `start` RAN
// (palgroup/palbase#19).
//
// A cloud project's checkout with `palbase start` running: every verb acts on
// the local stack, and `build` used to read the stack names from it too. The
// local vault holds none of the cloud's secrets, so the build rewrote the
// committed palbase-env.d.ts with an empty `Secrets` — and `tsc` failed on
// every `Secrets.get()` in the project, in a file nobody may edit by hand.
//
// Driven through runBuild with the real SDK's renderer: the environment is the
// one this machine selected (a two-environment project), the file carries its
// names, `tsc` compiles a read of one of them, and a secret set only on the
// local stack is named with the command that puts it where the types come
// from. Then the negative control: the same checkout linked to NOTHING reads
// the local stack, because then it is the only stack there is.
func TestBuildTypesTheLinkedEnvironmentWhileALocalStackRuns(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, err := os.Getwd()
	require.NoError(t, err)
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	useTestParserCache(t)
	// The scaffold's compiler options, minus the `types` it installs separately:
	// skipLibCheck is what keeps the SDK's own node imports out of the verdict.
	mustWrite(t, dir, "tsconfig.json", `{"compilerOptions":{"target":"ES2022","module":"ESNext",`+
		`"moduleResolution":"Bundler","strict":true,"skipLibCheck":true,"noEmit":true,`+
		`"experimentalDecorators":true,"emitDecoratorMetadata":true},"include":["**/*.ts"],"exclude":["node_modules"]}`)
	mustWrite(t, dir, "modules/billing/billing.module.ts", `
import { Module } from "@palbase/backend";
import { BillingService } from "./billing.service";

@Module({ providers: [BillingService] })
export class BillingModule {}
`)
	mustWrite(t, dir, "modules/billing/billing.service.ts", `
import { Injectable, Secrets } from "@palbase/backend";

@Injectable()
export class BillingService {
  key(): Promise<string | null> {
    return Secrets.get("STRIPE_SECRET_KEY");
  }
}
`)

	resolverRig(t, []Environment{
		{Name: "main", Ref: "mainref000", Status: "Running"},
		{Name: "staging", Ref: "stagref000", Status: "Running"},
	})
	main := namedStack(t, []string{"OPENAI_API_KEY"}, []string{"newCheckout"})
	staging := namedStack(t, []string{"STRIPE_SECRET_KEY"}, []string{"newCheckout"})
	local := namedStack(t, []string{"LOCAL_ONLY"}, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	linkedAs(t, local.URL, "local-token")
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	require.NoError(t, WriteSelection(dir, Selection{Project: "prd_a", Env: "staging", Ref: "stagref000"}))
	require.NoError(t, WriteLocalTarget(Target{URL: local.URL}))
	acting, err := Resolve(context.Background())
	require.NoError(t, err)
	require.True(t, acting.Target.Local, "the local stack is not what verbs act on — this test measures nothing")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	require.NoError(t, runBuild(ctx, dir, &out), out.String())

	envFile := filepath.Join(dir, filepath.FromSlash(EnvTypesPath()))
	landed, err := os.ReadFile(envFile)
	require.NoError(t, err)
	require.Contains(t, string(landed), "STRIPE_SECRET_KEY",
		"the file does not type the selected environment's secret:\n%s", landed)
	require.NotContains(t, string(landed), "LOCAL_ONLY", "the file was typed from the local stack")
	require.NotContains(t, string(landed), "OPENAI_API_KEY", "the file was typed from an environment nobody selected")
	require.Contains(t, out.String(), "stack names from todoapp/staging", "the build did not say where the names came from")
	require.Contains(t, out.String(),
		"! LOCAL_ONLY is set on the local stack only — the types come from todoapp/staging, so "+
			`Secrets.get("LOCAL_ONLY") does not compile; set it there: palbase secret set LOCAL_ONLY --stdin --env staging`,
		"a secret only the local stack holds was not named with the way to fix it:\n%s", out.String())

	tsc := exec.CommandContext(ctx, filepath.Join(dir, "node_modules", ".bin", "tsc"), "--noEmit", "-p", dir)
	tsc.Dir = dir
	diagnostics, tscErr := tsc.CombinedOutput()
	require.NoError(t, tscErr, "a read of a secret the linked environment holds does not compile:\n%s", diagnostics)

	// LINKED TO NOTHING, the local stack is the stack.
	require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(projectPath()))))
	out.Reset()
	require.NoError(t, runBuild(ctx, dir, &out), out.String())
	landed, err = os.ReadFile(envFile)
	require.NoError(t, err)
	require.Contains(t, string(landed), "LOCAL_ONLY", "an unlinked checkout did not read the stack running here")
	require.Contains(t, out.String(), "stack names from "+local.URL+" (local)")
	require.NotContains(t, out.String(), "is set on the local stack only",
		"with the local stack as the source there is nothing only it holds")
}

// A LINKED CHECKOUT WHOSE ENVIRONMENT CANNOT BE NAMED NEVER FALLS BACK TO THE
// LOCAL STACK. Two environments and none selected is a question only the
// person can answer; reading the stack `start` brought up instead is exactly
// the answer palgroup/palbase#19 removed. The names are unavailable, the
// reason is the resolver's own listing, and the local stack is never asked —
// so landEnvTypes keeps the block the file already carries.
func TestBuildNamesNeverFallBackToTheLocalStack(t *testing.T) {
	inScratchCheckout(t)
	dir, err := os.Getwd()
	require.NoError(t, err)
	resolverRig(t, twoEnvs)
	local := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("the local stack was asked for %s", r.URL.Path)
	}))
	t.Cleanup(local.Close)
	linkedAs(t, local.URL, "local-token")
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	require.NoError(t, WriteLocalTarget(Target{URL: local.URL}))

	var out bytes.Buffer
	got := namesForBuild(context.Background(), dir, &out)
	require.Equal(t, namesUnavailable, got.Source, "names were read while no environment was selected")
	require.ErrorContains(t, got.Why, "has 2 environments and none is selected")
	require.Empty(t, out.String())
}
