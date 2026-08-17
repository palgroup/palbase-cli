package backend

// stackfiles.go — the stack `palbase start` brings up, carried by the binary.
//
// It used to be found through PALBASE_STACK_DIR, pointed at the palbase
// repository's v2/deploy, on the reasoning that "the people running it have the
// repository". That stopped being true the moment `palbase init` shipped: a
// person installs the CLI from brew, scaffolds a project, types `palbase start`
// in it, and is told to point an environment variable at a directory they have
// never heard of and do not have. The two commands are one sentence in the docs
// and only the first of them worked.
//
// So the compose document travels IN the binary and is written out beside the
// stack's own state. The environment variable still wins when it is set, because
// somebody editing that file in the palbase repository wants their edit and not
// this copy.

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// stackCompose is v2/deploy/docker-compose.dev.yml, vendored.
//
// A copy, and copies drift — so TestTheVendoredComposeMatchesTheRepository holds
// it against the original whenever the palbase repository is beside this
// checkout, which is every development machine and no CI runner.
//
//go:embed stackfiles/docker-compose.dev.yml
var stackCompose []byte

// writeVendoredStack puts the embedded compose document where a stack can be
// brought up from it, and answers with the directory.
//
// Beside the group's own state rather than in a temp dir: `docker compose` reads
// this file again on every subsequent command — `stop`, a recreate, a logs
// follow — and a stack whose compose file evaporated is one docker can no longer
// be told anything about.
func writeVendoredStack(group string) (string, error) {
	dir, err := stackStateDir(group)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, composeFile)
	if err := os.WriteFile(path, stackCompose, 0o644); err != nil {
		return "", fmt.Errorf("write the stack definition: %w", err)
	}
	return dir, nil
}
