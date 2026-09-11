package backend

// web_wiring_dir_test.go — THE WEB CLIENT IS GENERATED FROM THE ENVIRONMENT
// DIRECTORY, AND THE APP IMPORTS ONE STABLE LINE.
//
// `@palbase/web` 10 moved its generator onto the per-environment layout: it
// reads `<dir>/<env>/{openapi.json, roles.json, web-config.json}`, writes that
// environment's client inside the same directory, and writes `palbase/client.ts`
// as the single line an application imports. The environment is chosen by
// `PALBASE_ENV`, so switching environments never edits the app's own source.
//
// This is also the outage this task closes: the reader shipped before the
// writer, so `palbase link --platform web` has been failing with a bare
// `palbe-gen: exit status 1` for every user on @palbase/web 10.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubPalbeGen10 imitates the published generator's CONTRACT: it refuses unless
// the environment directory it was pointed at holds a web config, and it writes
// both products — the environment's client and the barrel.
func stubPalbeGen10(t *testing.T, argvLog string) {
	t.Helper()
	stubInstall(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(palbeGenBin), 0o755))
	script := `#!/bin/sh
printf '%s\n' "$@" >> "` + argvLog + `"
dir=palbase/environments
out=palbe.gen.ts
while [ $# -gt 0 ]; do
  case "$1" in
    --dir) dir="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
env="${PALBASE_ENV:-local}"
if [ ! -f "$dir/$env/web-config.json" ]; then
  echo "error: $dir/$env/web-config.json is required in dir mode" >&2
  exit 1
fi
mkdir -p "$dir/$env"
echo '// generated' > "$dir/$env/$out"
mkdir -p "$(dirname "$dir")"
echo "export * from './environments/$env/$out'" > "$(dirname "$dir")/client.ts"
`
	require.NoError(t, os.WriteFile(palbeGenBin, []byte(script), 0o755))
}

func TestWebWiringGeneratesFromTheEnvironmentDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	argv := filepath.Join(t.TempDir(), "argv")
	stubPalbeGen10(t, argv)
	writePkgJSON(t, minimalPkgJSON())

	// The environment as `link` leaves it: its own directory, carrying the web
	// configuration the generator reads.
	env := "main"
	require.NoError(t, os.MkdirAll(filepath.FromSlash(EnvDir(env)), 0o755))
	require.NoError(t, os.WriteFile(filepath.FromSlash(ConfigPath(env, webPlatform)),
		[]byte(`{"app_id":"project","base_url":"https://x.palbase.studio","api_key":"pb_project_cKEY"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.FromSlash(SpecPath(env)), []byte(`{"openapi":"3.1.0"}`), 0o600))

	runWebLink(t)

	// THE ARGUMENTS NAME THE NEW DIRECTORY. Without `--dir` the generator falls
	// back to its own default, which is only right by coincidence — and a
	// coincidence is not a wiring.
	args, err := os.ReadFile(argv)
	require.NoError(t, err)
	require.Contains(t, string(args), "--dir", "the CLI did not tell the generator where to read")
	require.Contains(t, string(args), filepath.ToSlash(filepath.Dir(filepath.FromSlash(EnvDir(env)))))

	// AND BOTH PRODUCTS EXIST: the environment's client, and the ONE line the
	// application imports.
	require.FileExists(t, filepath.FromSlash(GeneratedPath(env, webPlatform)))
	barrel, err := os.ReadFile(filepath.FromSlash(ClientBarrelPath()))
	require.NoError(t, err, "no %s — the application has nothing stable to import", ClientBarrelPath())
	require.Equal(t, 1, len(strings.Split(strings.TrimSpace(string(barrel)), "\n")),
		"the barrel is more than one line: %q", barrel)
	require.Contains(t, string(barrel), env, "the barrel does not name the environment it re-exports")
}
