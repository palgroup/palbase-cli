package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimePreparationAutomaticallyMigratesAndVerifies(t *testing.T) {
	for _, tc := range []struct {
		name, initial, final, plan, wantError string
		withSchema, approve                   bool
		prepares                              int
	}{
		{"SDK change", "33.0.2", "36.0.2", "", "", false, false, 1},
		{"patch change", "36.0.1", "36.0.2", "", "", false, false, 1},
		{"already current", "36.0.2", "36.0.2", "", "", false, false, 0},
		{"false platform success", "33.0.2", "33.0.2", "", "not verified", false, false, 1},
		{"incompatible schema", "33.0.2", "36.0.2", `{"in_sync":false,"incompatible":["source_key is still used"]}`, "schema preflight refused", true, false, 0},
		{"approve cannot bypass compatibility", "33.0.2", "36.0.2", `{"in_sync":false,"incompatible":["source_key is still used"]}`, "schema preflight refused", true, true, 0},
		{"missing approval", "33.0.2", "36.0.2", `{"in_sync":false,"destructive":[{"table":"places","rows":1}]}`, "schema preflight refused", true, false, 0},
		{"compatible transition", "33.0.2", "36.0.2", `{"in_sync":true}`, "", true, false, 1},
		{"invalid plan", "33.0.2", "36.0.2", `{}`, "did not return a schema plan", true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProjectSDK(t, dir, "36.0.2", "36.0.2")
			if tc.withSchema {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "public.ts"), []byte("export default {}"), 0644))
			}
			running := tc.initial
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/palbase.json":
					fmt.Fprintf(w, `{"hosting":"project","sdk_version":%q}`, running)
				case "/v1/management/schema/plan":
					fmt.Fprint(w, tc.plan)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			target := Target{URL: server.URL}
			originalPrepare := CloudRuntimePreparer
			t.Cleanup(func() { CloudRuntimePreparer = originalPrepare })
			prepares := 0
			// PLAN, HAZIRLAYICIYA KADAR TAŞINIR: sunucu parmak izini yeniden
			// hesaplayacak, o yüzden bu testin planı da gerçek üreticiden gelir.
			var carried PlanRef
			CloudRuntimePreparer = func(_ context.Context, _, _ string, plan PlanRef) error {
				prepares++
				carried = plan
				running = tc.final
				return nil
			}
			plan := PlanFile{SDK: PlanSDK{Running: "35.0.0", Target: "36.0.2"},
				BundleDigest: strings.Repeat("a", 64), SchemaPlanDigest: strings.Repeat("b", 64)}
			plan.Fingerprint = Fingerprint(plan.BundleDigest, plan.SDK.Running, plan.SDK.Target, plan.SchemaPlanDigest)
			var out bytes.Buffer
			err := prepareCloudRuntime(context.Background(), dir, target, Credentials{}, tc.approve, &out, plan)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
			require.Equal(t, tc.prepares, prepares)
			if tc.prepares == 1 && tc.wantError == "" {
				require.Contains(t, out.String(), "verified @palbase/backend 36.0.2 before code upload")
				require.Equal(t, plan.Fingerprint, carried.Fingerprint,
					"planın parmak izi platforma DEĞİŞMEDEN gitmeli; sunucu onu yeniden hesaplayıp karşılaştırıyor")
			}
		})
	}
}

// KAPI 5 · SUNUCUNUN DÜZELTMELERİ KULLANICININ KLAVYESİNDEN ERİŞİLEBİLİR OLMALI.
//
// `projectSDKVersion` kiracının KENDİ well-known belgesine gidiyor ve
// CrashLoop'taki bir kiracıda o adres kapalı. Bu satır hata dönünce push
// platforma TEK BİR İSTEK atmadan düşüyordu — yani operatör ve düzlem tarafında
// servis etmeyen kiracının yükseltilebilmesi için yapılan her şey burada,
// çağrılmadan önce ölüyordu. Canlıda ölçüldü: penny `na1m7lt2m`, 98 restart.
func TestCloudRuntimeIsPreparedWhenTheProjectCannotReportItsVersion(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	buildableBackend(t, dir)

	// Kiracı CEVAP VERMİYOR — CrashLoop'un tanımı.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	orig := CloudRuntimePreparer
	t.Cleanup(func() { CloudRuntimePreparer = orig })
	var asked int
	CloudRuntimePreparer = func(context.Context, string, string, PlanRef) error {
		asked++
		return errors.New("platform reached")
	}

	var out bytes.Buffer
	err := prepareCloudRuntime(context.Background(), dir, Target{URL: srv.URL},
		Credentials{Value: "k", Kind: KindKey}, false, &out, PlanFile{})
	if asked != 1 {
		t.Fatalf("platform ÇAĞRILMALI — karar ölçebilen tarafa devredilir; asked=%d err=%v\n%s", asked, err, out.String())
	}
	if !strings.Contains(out.String(), "could not be measured") {
		t.Fatalf("kullanıcı neden platforma devredildiğini görmeli:\n%s", out.String())
	}
}
