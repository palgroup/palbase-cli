package backend

// start.go — `palbase start`: a stack in front of you, and every verb pointed at it.
//
// This is a DEV SERVER, not a deployment. The distinction is the whole design:
// a deployment takes a bundle, activates a version and keeps history; this
// mounts your source and reloads it, so the loop between typing a line and
// seeing the answer is seconds and involves nobody's registry.
//
// What it leaves behind is three files in three different places, and each one
// is where it is for a reason:
//
//	.palbase/local.json          IN the checkout, gitignored — "right now, work
//	                             here". Every verb reads it, so switching back is
//	                             `palbase stop` rather than a flag on each command.
//	~/.palbase/credentials.json  MACHINE-WIDE — the app checkout in another
//	                             directory needs the same credential, and copying
//	                             a secret between repositories is how it ends up
//	                             committed in one of them.
//	~/.palbase/stacks/<group>/   the boot values the containers need. NOT in the
//	                             checkout: they are secrets, they are per-machine,
//	                             and a .env in a repository is a .env somebody
//	                             commits.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// StackDirEnv points at the stack this machine boots from: the directory holding
// docker-compose.dev.yml.
//
// It is a variable rather than a download because the local stack is a DEV RAIL
// rather than a product — the people running it have the repository, and a CLI
// that fetched a stack from somewhere would be shipping the thing we decided not
// to ship.
const StackDirEnv = "PALBASE_STACK_DIR"

const composeFile = "docker-compose.dev.yml"

// The images the dev compose expects to find already built, and the variables it
// reads them from — the same names, with the same defaults, so this refuses for
// the tag compose will actually look for.
//
// The runtime's default is `-dev` and that suffix is load-bearing: the shipped
// stack builds ITS runtime under the plain name, so writing a dev image over
// that tag would leave the production stack running a dev image the next time it
// is recreated. Reading the defaults from the wrong place is not a theoretical
// risk — this checked `palbase-runtime` while compose used `palbase-runtime-dev`
// and passed anyway, because another stack on the machine happened to have built
// the right one.
// HER İMAJ AYNI SÜRÜMDEN TÜRER — TEK SAYI (FR-003/006, D-015).
//
// Eskiden burada her imajın kendi `fallback` etiketi vardı ve o etiketler
// `0.43.0`'da donmuştu; ölçülen ayrışma BEŞ kolluydu (SDK 33.0.2 ·
// version.env 0.42.1 · stack-images.json 0.42.0 · control-plane 0.33.1 ·
// bu tablo 0.43.0). Artık sürüm tek yerden gelir: kurulu @palbase/backend.
//
// `repo` alanı sabittir, SÜRÜM değil — ve ayrım önemli: bir imajın ADI neredeyse
// hiç değişmez, sürümü her yayında değişir. Değişeni dışarı, sabiti içeri.
var stackImages = []stackImage{
	{env: "PBC_PALSVC_IMAGE", repo: "ghcr.io/palgroup/palbase/palsvc",
		build: "cd v2 && DOCKER_BUILDKIT=1 docker build -t palbase-palsvc -f Dockerfile ."},
	// RUNTIME'IN `-dev` HEDEFİ: bundler'ı ve TypeScript derleyici API'sini o
	// taşıyor; onsuz hiçbir proje derlenmez (v2/runtime/Dockerfile:83-140).
	// Son ek imajın ADININ parçası, sürümün değil.
	{env: "PBC_RUNTIME_IMAGE", repo: "ghcr.io/palgroup/palbase/runtime-dev",
		build: "cd v2/runtime && DOCKER_BUILDKIT=1 docker build --target dev -t palbase-runtime-dev -f Dockerfile ."},
	// The EDGE, and it belongs here for the same reason the other two do: it is
	// the one container that publishes, so a stack without it has no address at
	// all. Absent from this list, a missing edge image surfaced as docker's raw
	// pull error at `compose up` instead of this command's own refusal with the
	// line that builds it.
	{env: "PBC_EDGE_IMAGE", repo: "ghcr.io/palgroup/palbase/edge",
		build: "cd v2/deploy/envoy && DOCKER_BUILDKIT=1 docker build -t palbase-edge ."},
	// THE DATABASE: upstream, bizim sürüm hattımızda DEĞİL. `pinned` alanı tam
	// bunun için — core sürümü ona uygulanmaz.
	{env: "PBC_POSTGRES_IMAGE", repo: "pgvector/pgvector", pinned: "pg16",
		build: "docker pull pgvector/pgvector:pg16", upstream: true},
}

// stackImage is one container: the variable that carries it to compose, the
// repository it lives in, and the line that tells somebody how to build it.
type stackImage struct {
	env, repo, build string
	// pinned, upstream imajların KENDİ etiketi — core sürümü onlara uygulanmaz.
	pinned string
	// upstream marks an image somebody else publishes, so the core-version rule
	// does not apply to it.
	//
	// DECLARED, NOT GUESSED. The parity gate used to infer this from the ref's
	// prefix, which meant one of OUR images published to a different path
	// silently became "upstream" and left the equality check altogether —
	// measured: `ghcr.io/palgroup/palbase-edge:0.1.0` and
	// `docker.io/palgroup/palsvc:0.1.0` both passed a gate whose whole job is to
	// catch a stale core pin. An entry says what it IS.
	upstream bool
}

// installedSDKVersion, kurulu paketin TAM sürümünü verir — imajın etiketi budur.
//
// Eskiden bir TABLO okunuyordu (`stack-images.json`, SDK major'ı → dört imaj).
// Tablo, paketin ZATEN bildiği bir sayıyı ikinci kez yazmaktan başka bir şey
// yapmıyordu, ve ikinci kopya ayrıştı (ölçülen ayrışma beş kolluydu).
//
// OKUMAYI KENDİ YAZMIYOR: `installedBackendVersion` bu işi zaten yapıyordu
// (build.go). Bu sarmalayıcı yalnız SESSİZ boşluğu GÜRÜLTÜLÜ bir redde çeviriyor
// — o fonksiyon okuyamadığında "" döner ve çağıranı sessizce yanlış yola sokar.
//
// İKİ RED, İKİSİ DE ADIYLA: kurulu paket yoksa da, sürümsüz bir paket de
// reddedilir. Yuvarlama yok, varsayılan yok — kimsenin istemediği bir yığın
// sunmak, hiç sunmamaktan kötüdür.
func installedSDKVersion(projectDir string) (string, error) {
	v := strings.TrimSpace(installedBackendVersion(projectDir))
	if v == "" {
		return "", fmt.Errorf(
			"%s is not installed here, or declares no version (%s) — run `npm install %s`",
			backendPkg, filepath.Join(projectDir, "node_modules", "@palbase", "backend", "package.json"), backendPkg)
	}
	return v, nil
}

// ref, bu girdinin çekilebilir referansı.
//
// Upstream imajlar KENDİ pinlerini taşır (core sürümü onlara uygulanmaz);
// bizimkiler kurulu SDK'nın sürümünü alır. Ayrım `upstream` alanında AÇIKÇA
// bildirilir, ref'in önekinden tahmin EDİLMEZ — o tahmin bir kez sessizce
// yanıldı ve bayat bir core pinini kapıdan geçirdi.
func (i stackImage) ref(version string) string {
	if i.upstream {
		return i.repo + ":" + i.pinned
	}
	return i.repo + ":" + version
}

// runtimeIsHealthy reads `docker compose ps --format json` and answers the one
// question the edge cannot: has the RUNTIME loaded and started answering.
//
// `/readyz` on the edge routes to the palsvc cluster, so a banner built on it
// could say "ready" while the runtime was still refusing every bundle. The
// runtime distinguishes alive from LOADED AND ANSWERING and serves both on its
// own probe port; compose's healthcheck asks it, and this reads the verdict.
//
// A runtime ABSENT from the listing is not ready either: it has not reached the
// question yet, and calling that ready is the defect rather than the fix.
func runtimeIsHealthy(composePS []byte) (bool, error) {
	type service struct {
		Service string `json:"Service"`
		Health  string `json:"Health"`
	}
	trimmed := bytes.TrimSpace(composePS)
	var services []service
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &services); err != nil {
			return false, fmt.Errorf("read compose state: %w", err)
		}
	} else {
		// Older compose prints one JSON object per line.
		for _, line := range bytes.Split(trimmed, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var one service
			if err := json.Unmarshal(line, &one); err != nil {
				return false, fmt.Errorf("read compose state: %w", err)
			}
			services = append(services, one)
		}
	}
	for _, svc := range services {
		if svc.Service == "runtime" {
			return svc.Health == "healthy", nil
		}
	}
	return false, nil
}

// existingStackDirectory returns the directory holding the stack file WITHOUT
// writing one.
//
// `stop` used to call stackDirectory, which writes the vendored compose before
// handing back its path — so a CLI upgraded since `start` took the stack down
// with a DIFFERENT definition than the one that brought it up. Any service
// renamed in between simply stayed running, unreferenced by the file docker was
// handed.
func existingStackDirectory(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, composeFile)); err != nil {
		return "", fmt.Errorf("no %s in %s — nothing to take down here", composeFile, dir)
	}
	return dir, nil
}

func newStartCmd() *cobra.Command {
	var reset bool
	var lan bool
	cmd := &cobra.Command{
		Use:   "start",
		Args:  cobra.NoArgs,
		Short: "Bring up a stack on this machine and point this checkout at it",
		Long: `Run this project on this machine.

Your source is mounted rather than deployed: save a controller and the running
stack serves the new version, with no build, no artifact and no version history.

Everything after this — push, db, secret, spec, logs — acts on the local stack
until ` + "`palbase stop`" + `.

    palbase start --reset     throw the database away and build it from db/`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := RequireBackendPlane(dir); err != nil {
				return err
			}
			return runStart(cmd.Context(), dir, reset, lan, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&reset, "reset", false,
		"empty the local database first, then build it from db/")
	cmd.Flags().BoolVar(&lan, "lan", false,
		"publish the API on this machine's network address, so a phone on the same wifi can reach it")
	return cmd
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Args:  cobra.NoArgs,
		Short: "Shut the local stack down and point this checkout back at its project",
		Long: `Stop the stack ` + "`palbase start`" + ` brought up.

The database survives — a stop is not a reset — so starting again finds the data
you left. Every verb goes back to the project this checkout is linked to.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			return runStop(cmd.Context(), dir, cmd.OutOrStdout())
		},
	}
}

func runStart(ctx context.Context, dir string, reset, lan bool, out io.Writer) error {
	group := groupName(dir)
	stackDir, err := stackDirectory(group)
	if err != nil {
		return err
	}
	// WHICH STACK, AND SAY IT.
	//
	// This command printed the project name and nothing else, so "start ran but
	// the wrong stack came up" stayed invisible until something downstream
	// failed. The version comes from the project (T001) and the images from the
	// installed SDK's table — neither from this binary.
	version, err := stackVersion(dir)
	if err != nil {
		return err
	}
	sdkVersion, err := installedSDKVersion(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "▸ stack %s (SDK %s)\n", version, sdkVersion)
	for _, img := range stackImages {
		fmt.Fprintf(out, "  %s\n", img.ref(sdkVersion))
	}

	if err := imagesPresent(ctx, stackImages, sdkVersion); err != nil {
		return err
	}

	state, err := stackStateDir(group)
	if err != nil {
		return err
	}
	envFile := filepath.Join(state, ".env")
	envChanged, err := ensureBootValues(ctx, envFile, sdkVersion, out)
	if err != nil {
		return err
	}
	if err := recordStackImages(envFile, sdkVersion); err != nil {
		return err
	}

	httpPort, err := freePort()
	if err != nil {
		return err
	}
	// ONE port, because one container publishes. The database used to get one
	// too; it is not on the host any more, so allocating a number for it would
	// promise an address the stack does not open.
	//
	// The port lives in the same file as the rest of the boot values, so a
	// restart lands on the SAME address: a URL written into .palbase/local.json,
	// a generated client and an iOS build all point at a number, and picking a
	// fresh one every start would silently break each of them.
	settled, err := rememberPort(envFile, httpPort)
	if err != nil {
		return err
	}
	// THE ADDRESS THE STACK IS REACHED AT, and with --lan it is not loopback.
	//
	// It is written into .palbase/local.json and into the register, which is
	// where `palbase ios link` reads it — so a phone gets an address it can
	// actually resolve rather than 127.0.0.1, which on a phone is the phone.
	host := "127.0.0.1"
	bind := ""
	if lan {
		addr, err := lanAddress()
		if err != nil {
			return err
		}
		host, bind = addr, "0.0.0.0"
	}
	url := fmt.Sprintf("http://%s:%d", host, settled)

	project := "palbase-" + group
	if reset {
		fmt.Fprintln(out, "▸ removing the local database")
		// -v takes the volumes with it, which is the reset: the schema comes
		// back from db/ on the next boot rather than from a dump.
		if err := compose(ctx, stackDir, project, envFile, dir, bind, settled, out, "down", "-v"); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "▸ starting %s\n", group)

	// THE DATABASE FIRST, THEN ITS SCHEMA, THEN THE REST.
	//
	// palsvc refuses to serve a database that has not been migrated — correctly,
	// since the alternative is answering requests against half a schema — so a
	// plain `up -d` leaves it in a restart loop with the answer in a log nobody
	// is reading. The one-shot below is the same command the shipped stack uses
	// and it is idempotent, so it runs on every start rather than being
	// remembered once.
	// --wait, and it is not decoration: `up -d` returns when the container has
	// STARTED, while the migration below runs with --no-deps and so waits for
	// nothing. The two raced, and the loser was the migration — postgres was up
	// and not yet accepting connections. --wait blocks on the healthcheck the
	// compose file already declares.
	if err := compose(ctx, stackDir, project, envFile, dir, bind, settled, out, "up", "-d", "--wait", "postgres"); err != nil {
		return err
	}
	fmt.Fprintln(out, "▸ applying every module's schema")
	if err := compose(ctx, stackDir, project, envFile, dir, bind, settled, out,
		"run", "--rm", "--no-deps", "palsvc", "--migrate-only"); err != nil {
		return fmt.Errorf("migrate the local database: %w", err)
	}

	// FORCE-RECREATE when the .env changed under a stack that already exists.
	//
	// Compose does not re-read --env-file for containers it is not creating, so a
	// sealing chain written into the file is a chain the RUNNING stack cannot see.
	// Measured end-to-end: after the migration the .env carried all three
	// variables and `docker exec palsvc printenv | grep -c PALBASE_SEALED` printed
	// 0 — the stack still answered `sealed_unconfigured` and an iOS client still
	// could not sign in, while this command had already printed that it could.
	upArgs := []string{"up", "-d"}
	if envChanged {
		upArgs = append(upArgs, "--force-recreate")
	}
	if err := compose(ctx, stackDir, project, envFile, dir, bind, settled, out, upArgs...); err != nil {
		return err
	}
	if err := waitForStack(ctx, url, stackDir, project, envFile, 90*time.Second); err != nil {
		return fmt.Errorf("%w\nThe containers are still up — `docker compose -p %s logs` says what happened", err, project)
	}

	// NO CREDENTIAL IS WRITTEN HERE, and that is deliberate.
	//
	// This used to copy the stack's secret key into ~/.palbase/credentials.json
	// so every verb could find it. But the key already exists, once, in this
	// group's state directory — and a copy is a thing to keep in step: `stop`
	// left it behind, and `--reset` gave the stack a new key while the copy went
	// on claiming the old one. The resolver reads the original instead
	// (credentials.go, localStackKey), which is also what makes an app checkout
	// in another directory work with no extra step: the register says which
	// group owns this address, and the group's directory holds the key.
	if lan {
		announceLAN(out, url)
		deviceSetupNotice(out)
	}
	if err := WriteLocalTarget(Target{URL: url}); err != nil {
		return err
	}
	if err := ignoreLocalTarget(dir); err != nil {
		return err
	}
	if err := registerStack(group, url, project, dir); err != nil {
		return err
	}

	// The secrets LAST: the stack has to be up and this checkout has to be
	// credentialed before anything can be written into its vault.
	pullSecrets(ctx, group, Target{URL: url, Local: true}, out)

	for _, line := range startBanner(url) {
		fmt.Fprintln(out, line)
	}
	return nil
}

// startBanner is what a started stack tells you about itself.
//
// The schema line is here because its absence cost a round trip. `palbase push`
// refuses on a local stack — correctly: the dev runtime serves the DIRECTORY it
// mounted and never follows the deploy pointer, so a push would activate a
// version nothing loads. The refusal even names `db apply`. But it is only
// reachable by TRYING to push, and a person who has just run `start` has no
// reason to think a push is the wrong verb. So the answer moves to where the
// question forms.
func startBanner(url string) []string {
	return []string{
		fmt.Sprintf("▸ %s (local)", url),
		"  edit a controller and the running stack serves it — no build, no deploy",
		"  schema changes are applied with `palbase db apply` — `push` is for the cloud",
		"  `palbase stop` points this checkout back at its project",
	}
}

func runStop(ctx context.Context, dir string, out io.Writer) error {
	group := groupName(dir)
	project := "palbase-" + group

	state, err := stackStateDir(group)
	if err != nil {
		return err
	}
	// THE FILE THAT IS ALREADY THERE, not a fresh copy of this binary's.
	stackDir, err := existingStackDirectory(state)
	if err != nil {
		return err
	}
	envFile := filepath.Join(state, ".env")
	httpPort, err := readPort(envFile)
	if err != nil {
		return err
	}

	// The files come off FIRST. A stop that failed halfway used to leave
	// local.json behind pointing at a dead address, which every later verb then
	// tried to reach — and the error it gave was a connection refusal rather
	// than "there is no local stack".
	if err := os.Remove(localPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := deregisterStack(group); err != nil {
		return err
	}

	// Volumes stay: a stop is not a reset. `palbase start --reset` is the verb
	// that throws data away, and it says so.
	if err := compose(ctx, stackDir, project, envFile, dir, "", httpPort, out, "down"); err != nil {
		return err
	}
	fmt.Fprintln(out, "▸ stopped")

	if target, err := readLinkedProject(); err == nil {
		fmt.Fprintf(out, "  back to %s\n", target.Describe())
	}
	return nil
}

// stackDirectory finds the compose file this machine boots from.
//
// The binary carries one, so `palbase init` followed by `palbase start` works in
// a directory that has never heard of the palbase repository — which is every
// directory except the handful on the machines of the people who build this.
// PALBASE_STACK_DIR still wins when set: somebody editing v2/deploy wants their
// edit, not the copy compiled into the CLI they happen to have installed.
func stackDirectory(group string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv(StackDirEnv)); dir != "" {
		path := filepath.Join(dir, composeFile)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%s names %s, which has no %s in it", StackDirEnv, dir, composeFile)
		}
		return dir, nil
	}
	return writeVendoredStack(group)
}

// imagesPresent refuses BEFORE compose does, because compose's own failure for a
// missing image is a pull error from a registry that was never going to have it.
func imagesPresent(ctx context.Context, images []stackImage, version string) error {
	for _, want := range images {
		// EZME YOK. Burada bir `os.Getenv(want.env)` kaçamağı vardı: aynı
		// değişken adı, ama bu sefer sürüm kuralını sessizce delen bir ikinci
		// kaynak olarak. Kullanıcının kararı ve bu koşunun tezi aynı — imajı
		// belirleyen tek şey kurulu SDK'nın sürümü. Aynı adı CLI'ın KENDİSİ
		// yığının .env'ine yazıyor (recordStackImages); okunacak bir ezme değil,
		// yazılacak bir kayıt.
		image := want.ref(version)
		// A REGISTRY reference is compose's job — it pulls, and a pull is the
		// whole reason `palbase start` works on a machine that has never seen
		// this repository. Already on the machine, nothing to check.
		if isRegistryImage(image) {
			if exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil {
				continue
			}
			// Ask the registry BEFORE compose does, because compose's own answer
			// for an image it may not read is `manifest unknown` — which reads
			// like the tag is wrong when the tag is fine and the door is shut.
			if err := exec.CommandContext(ctx, "docker", "manifest", "inspect", image).Run(); err != nil {
				return fmt.Errorf(
					"%s cannot be pulled on this machine.\n"+
						"  If it is private, `docker login ghcr.io` first — or ask for the package to be made public.\n"+
						"  Building it yourself instead: %s\n"+
						"  and then tag it as %s — the version comes from the installed %s and nothing overrides it",
					image, want.build, image, backendPkg)
			}
			continue
		}
		cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf(
				"the %s image is not on this machine.\n  %s",
				image, want.build)
		}
	}
	return nil
}

// isRegistryImage says whether docker would go and FETCH this reference.
//
// A SLASH is the whole rule: `ghcr.io/palgroup/palbase/palsvc:0.43.0` and
// `pgvector/pgvector:pg16` are both fetched, the first from ghcr and the second
// from Docker Hub, while `palbase-palsvc` is a tag somebody built here.
//
// It used to demand a dot or a colon in the first segment, which excluded Docker
// Hub's `org/image` short form. The comment justified that with "nothing in this
// stack defaults to one" — true when it was written, and FALSE from the moment
// `postgres` joined stackImages with `pgvector/pgvector:pg16` as its default.
// Left alone, ensureImages would have called the database image local, found it
// absent on a fresh machine and refused to start a stack docker could have
// pulled in seconds.
//
// The lesson is the premise, not the predicate: a rule whose reason names what
// the codebase "does not do today" expires the day the codebase does it.
func isRegistryImage(image string) bool {
	_, _, hasSlash := strings.Cut(image, "/")
	return hasSlash
}

// groupName is what this stack is called on this machine: the linked project's
// group when there is one, and the directory's name otherwise.
//
// It keys the compose project, the state directory and the registry, so two
// checkouts of two projects never share a database — and two checkouts of the
// SAME project deliberately do.
func groupName(dir string) string {
	if target, err := readLinkedProject(); err == nil && target.Project != "" {
		return sanitiseGroup(target.Project)
	}
	return sanitiseGroup(filepath.Base(dir))
}

var unsafeInName = regexp.MustCompile(`[^a-z0-9_-]+`)

func sanitiseGroup(name string) string {
	cleaned := unsafeInName.ReplaceAllString(strings.ToLower(name), "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "project"
	}
	return cleaned
}

func stackStateDir(group string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".palbase", "stacks", group)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureBootValues generates the stack's keys and secrets ONCE per group.
//
// The generator is the stack's own `--init-env`, run in its image: a second
// implementation here would be a second opinion about what a valid key looks
// like, and the day they disagree is the day a stack boots with a key nothing
// else accepts.
// Returns whether it CHANGED the .env. The caller needs that: compose does not
// re-read --env-file for containers that already exist, so a chain written into
// the file is a chain the running stack cannot see until it is recreated.
func ensureBootValues(ctx context.Context, envFile, sdkVersion string, out io.Writer) (bool, error) {
	if _, err := os.Stat(envFile); err == nil {
		// .env VAR — ama bu "yapacak bir şey yok" demek değil. Mühürleme zinciri bu
		// dosyaya SONRADAN eklendi; onu taşımayan bir yığın onu buradan kazanır.
		return migrateSealingChainWithMint(ctx, envFile, sdkVersion, out)
	} else if !os.IsNotExist(err) {
		return false, err
	}

	fmt.Fprintln(out, "▸ generating this stack's keys")
	dir := filepath.Dir(envFile)
	palsvc := stackImages[0].ref(sdkVersion)
	runArgs := append([]string{"run", "--rm"}, dockerRunAsHostUser()...)
	runArgs = append(runArgs,
		"-v", dir+":/w", "-w", "/w", "--entrypoint", "/palsvc", palsvc,
		"--init-env", filepath.Base(envFile))
	cmd := exec.CommandContext(ctx, "docker", runArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("generate the stack's keys: %w\n%s", err, output)
	}
	// The generator now writes as the host user (dockerRunAsHostUser), so this
	// is a narrowing rather than a rescue: the file must be readable by its
	// owner and by nobody else.
	if err := os.Chmod(envFile, 0o600); err != nil {
		return false, err
	}
	// A brand-new .env: nothing to recreate, the containers do not exist yet.
	return false, nil
}

// recordStackImages, çözülen dört imajı yığının .env'ine yazar.
//
// Compose belgesi bu değişkenleri `${VAR:?…}` ile ZORUNLU okuyor: varsayılan
// yok, çünkü varsayılan sürümün ikinci kaynağıdır ve bayatlayan taraf odur
// (ölçüldü: Go sabiti 0.39.0'a taşındı, compose 0.36.1'de kaldı ve `start` eski
// imajı koşmaya devam etti). Değeri veren tek yer burası, kaynağı da tek:
// kurulu `@palbase/backend`.
//
// SÜREÇ ORTAMINA DEĞİL DOSYAYA. `compose` her fiili `--env-file` ile çağırıyor,
// yani `stop` ve `ps` de bu dosyayı okuyor — ve onların SDK'nın kurulu olmasına
// ihtiyacı kalmıyor. Bir yığını durdurmak için önce onu kurmak gerekseydi,
// `node_modules`'ı silen herkes yığınını durduramaz hâle gelirdi.
//
// Her `start` yeniden yazar: dosya bir kayıt, bir otorite değil.
func recordStackImages(envFile, sdkVersion string) error {
	values := make(map[string]string, len(stackImages))
	for _, img := range stackImages {
		values[img.env] = img.ref(sdkVersion)
	}
	return setEnvValues(envFile, values)
}

// rememberPort keeps the first address this group was given, for as long as
// this group exists.
//
// It does NOT check whether the port is free, and that check is exactly what had
// to go: the port is occupied precisely when OUR OWN stack is already up, which
// is every `palbase start` on a running stack — so the stack moved, and every
// xcconfig, app config and generated client written by an earlier `link` still
// named the old number. A port genuinely taken by something else surfaces as
// compose refusing to bind it, with the number in the message.
func rememberPort(envFile string, httpPort int) (int, error) {
	existing, err := readPort(envFile)
	if err != nil {
		return 0, err
	}
	if existing != 0 {
		return existing, nil
	}
	if err := setEnvValues(envFile, map[string]string{
		"PALBASE_HTTP_PORT": strconv.Itoa(httpPort),
	}); err != nil {
		return 0, err
	}
	return httpPort, nil
}

func readPort(envFile string) (int, error) {
	raw, err := valueFromEnvFile(envFile, "PALBASE_HTTP_PORT")
	if err != nil && !os.IsNotExist(err) {
		return 0, nil
	}
	h, _ := strconv.Atoi(raw)
	return h, nil
}

// composeEnv, yığının göreceği ortam — ve içinde yığının KENDİ ADRESİ var.
//
// `PALBASE_PUBLIC_ORIGIN` burada doğuyor çünkü portu seçen burası. Bir `@Upload`
// handler'ı cevabına `getPublicUrl` ile nesne URL'i koyuyor ve o URL cevabın
// gövdesinden ÇIKIYOR: bir <img>'e, bir e-postaya, telefondaki uygulamaya.
// Yığının içindeki adres (`http://palsvc:8080`) oralarda çözülmez; göreli bir
// yol da çözülmez — ölçüldü canlı 24.08.2026, iOS istemcisi `/v1/files/...`
// alınca NSURLErrorUnsupportedURL (-1002) verdi.
//
// `--lan` verilmişse İLAN EDİLEN ADRES DE ONDUR. Loopback ilan etmek, aynı
// wifi'daki telefona kendi makinesinde arayacağı bir URL vermek olurdu: istek
// 200 döner, resim yüklenmez, hiçbir yerde hata görünmez.
func composeEnv(projectDir, bind string, httpPort int) []string {
	host := bind
	if host == "" {
		// Compose dosyasının kendi varsayılanı: loopback.
		host = "127.0.0.1"
	}
	env := []string{
		"PALBASE_PROJECT_DIR=" + projectDir,
		"PALBASE_HTTP_PORT=" + strconv.Itoa(httpPort),
		"PALBASE_PUBLIC_ORIGIN=http://" + net.JoinHostPort(host, strconv.Itoa(httpPort)),
	}
	// Empty means the compose file's own default, which is loopback. Only
	// `--lan` widens it, and only for the HTTP port.
	if bind != "" {
		env = append(env, BindEnv+"="+bind)
	}
	return env
}

// compose runs one docker compose verb against this group's stack.
func compose(ctx context.Context, stackDir, project, envFile, projectDir, bind string, httpPort int, out io.Writer, args ...string) error {
	full := append([]string{
		"compose",
		"-f", filepath.Join(stackDir, composeFile),
		"-p", project,
		"--env-file", envFile,
	}, args...)

	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Dir = stackDir
	cmd.Env = append(os.Environ(), composeEnv(projectDir, bind, httpPort)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &prefixed{w: out, prefix: "  "}
	return cmd.Run()
}

// prefixed indents compose's own chatter so it reads as detail under the CLI's
// line rather than as a competing voice.
type prefixed struct {
	w      io.Writer
	prefix string
}

func (p *prefixed) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Fprintf(p.w, "%s%s\n", p.prefix, line)
	}
	return len(b), nil
}

// waitForStack blocks until the stack answers, or says what is still missing.
//
// IT ASKS /readyz, NOT /healthz, and the difference is the whole point. The edge
// answers /healthz ITSELF with a direct_response — it is a liveness route for
// whatever is in front of the stack, and it returns 200 the moment envoy has a
// listener, which is before palsvc has connected to a database or applied a
// migration. Waiting on it would report a stack as ready and hand the person a
// 503 on their first real request. /readyz routes to the palsvc cluster, so a
// 200 there is palsvc saying so.
func waitForStack(ctx context.Context, url, stackDir, project, envFile string, limit time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(limit)
	var last string
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/readyz", nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
			_ = res.Body.Close()
			// EXACTLY 200. `< 500` was right when this asked /healthz, where a
			// 404 still meant "something is answering"; it is wrong now. If the
			// /readyz route ever left the edge's table the request would fall to
			// the catch-all, the customer's runtime would answer 404, and this
			// would read that as a ready stack — the precise silence the move to
			// /readyz was made to end.
			if res.StatusCode == http.StatusOK {
				// THE EDGE ANSWERING IS NOT THE RUNTIME BEING READY.
				//
				// /readyz routes to the palsvc cluster, so this branch used to
				// print the banner while the runtime was still refusing every
				// bundle handed to it. The runtime knows a state nobody else
				// does — loaded and answering — and compose asks it; read the
				// verdict before saying ready.
				// `--env-file` BURADA DA: belge imaj değişkenlerini `:?` ile
				// zorunlu okuyor, yani onlarsız `ps` yorumlamada düşer. Düşen
				// `ps`'in tek sonucu bu dalın SESSİZCE atlanması olurdu —
				// yani banner'ın runtime yüklenmeden "hazır" demesi, ki bu
				// dalın var olma sebebi tam olarak odur.
				ps := exec.CommandContext(ctx, "docker", "compose",
					"-f", filepath.Join(stackDir, composeFile), "-p", project,
					"--env-file", envFile, "ps", "--format", "json")
				out, psErr := ps.Output()
				if psErr == nil {
					healthy, hErr := runtimeIsHealthy(out)
					if hErr == nil && !healthy {
						last = "the edge answers but the runtime has not loaded yet"
						time.Sleep(time.Second)
						continue
					}
				}
				return nil
			}
			last = fmt.Sprintf("%s answered %d", url, res.StatusCode)
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("the stack did not come up within %s (%s)", limit, last)
}

// freePort asks the operating system for one, then hands it back. The window
// between that and compose binding it is real but small, and the alternative —
// a fixed range this CLI scans — collides with whatever else on the machine
// happens to like the same numbers.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// valueFromEnvFile reads one KEY=value out of a dotenv-shaped file.
//
// This is the ONE dotenv reader in the CLI, and it reads a file the CLI itself
// generated in the user's state directory — not a project's .env, which does not
// exist (see internal/secret).
func valueFromEnvFile(path, key string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && name == key {
			return strings.Trim(value, `"'`), nil
		}
	}
	return "", fmt.Errorf("%s carries no %s", path, key)
}

// setEnvValues writes each key ONCE, replacing any line that already sets it.
//
// Appending produced a file with two PALBASE_HTTP_PORT lines and two answers to
// one question: the writer returned the new port while the reader — scanning
// top-down — kept returning the old one, so `stop` addressed a different stack
// than `start` had brought up.
func setEnvValues(path string, values map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		name, _, found := strings.Cut(strings.TrimSpace(line), "=")
		if found {
			if _, replacing := values[name]; replacing {
				continue
			}
		}
		kept = append(kept, line)
	}
	for key, value := range values {
		kept = append(kept, key+"="+value)
	}
	body := strings.Join(kept, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return writeFileAtomic(path, []byte(body), 0o600)
}

// ignoreLocalTarget keeps the local pointer out of git.
//
// It names the file rather than the directory: .palbase/project.json is
// COMMITTED on purpose, so `.palbase/` in a .gitignore would take the one file a
// colleague cloning this repository needs.
func ignoreLocalTarget(dir string) error {
	const entry = ".palbase/local.json"
	path := filepath.Join(dir, ".gitignore")

	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}

	body := string(raw)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += entry + "\n"
	return os.WriteFile(path, []byte(body), 0o644)
}

// ── the machine's register of running stacks ────────────────────────────────
//
// An app checkout in another directory has to find the stack a backend checkout
// started, and it has nothing to go on but the project group's name. So the
// stack records itself here, machine-wide, and `link` resolves `local` from it.

type localStack struct {
	URL     string `json:"url"`
	Project string `json:"compose_project"`
	Dir     string `json:"directory"`
	Started string `json:"started_at"`
}

type stackRegistry struct {
	Stacks map[string]localStack `json:"stacks"`
}

func registryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".palbase", "local-stacks.json"), nil
}

func registerStack(group, url, project, dir string) error {
	return updateRegistry(func(reg *stackRegistry) {
		reg.Stacks[group] = localStack{
			URL:     url,
			Project: project,
			Dir:     dir,
			Started: time.Now().UTC().Format(time.RFC3339),
		}
	})
}

func deregisterStack(group string) error {
	return updateRegistry(func(reg *stackRegistry) { delete(reg.Stacks, group) })
}

// LookupLocalStack answers where a group's local stack is, or "" when none is
// running. Exported for `link`, which writes a `local` entry for app checkouts.
func LookupLocalStack(group string) string {
	path, err := registryPath()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var reg stackRegistry
	if json.Unmarshal(raw, &reg) != nil {
		return ""
	}
	return reg.Stacks[sanitiseGroup(group)].URL
}

// updateRegistry does the read-modify-write under the same lock the credential
// store uses, for the same reason: two `palbase start` runs in two panes are
// ordinary, and a torn registry loses a stack that is running.
func updateRegistry(change func(*stackRegistry)) error {
	path, err := registryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock, err := lockCredentials(path)
	if err != nil {
		return err
	}
	defer unlock()

	reg := stackRegistry{Stacks: map[string]localStack{}}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(raw, &reg)
		if reg.Stacks == nil {
			reg.Stacks = map[string]localStack{}
		}
	}
	change(&reg)

	blob, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(blob, '\n'), 0o600)
}

// groupOfLocalStack answers which project group serves this address, from the
// machine register, or false when no stack here does.
//
// By ADDRESS rather than by directory: the app checkout asking is usually not
// the backend checkout that started it, and the address is the one thing they
// both hold.
func groupOfLocalStack(url string) (string, bool) {
	path, err := registryPath()
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var reg stackRegistry
	if json.Unmarshal(raw, &reg) != nil {
		return "", false
	}
	for group, stack := range reg.Stacks {
		if stack.URL == url {
			return group, true
		}
	}
	return "", false
}

// LocalStackProject is the compose project a checkout's stack runs under.
//
// Exported because `palbase logs` reads that stack's containers and lives in
// another package. The name is formed in one place for the reason the group is:
// two checkouts of one project must reach the SAME stack, and two projects must
// never reach each other's.
func LocalStackProject(dir string) string { return "palbase-" + groupName(dir) }

// sealingChainVars, bir yığının mühürleme zincirini oluşturan üç değişken.
//
// ÜÇÜ BİRDEN ya da hiçbiri. Yarım bir zincir, zinciri olmayan bir yığından DAHA
// kötüdür: çalışıyormuş gibi görünür ve doğrulanamayan belgeler üretir.
var sealingChainVars = []string{
	"PALBASE_SEALED_SIGNING_SEED",
	"PALBASE_SEALED_BINDING",
	"PALBASE_SEALED_ROOT",
}

// sealingChainState, .env'in üç zincir değişkeninden kaçını taşıdığını söyler.
//
// Dosya yoksa (0, nil): çağıran bunu "yeni yığın" olarak okur, hata olarak değil —
// ensureBootValues'in normal üretim yolu tam olarak o durumdur.
func sealingChainState(envFile string) (int, error) {
	body, err := os.ReadFile(envFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	lines := strings.Split(string(body), "\n")
	present := 0
	for _, v := range sealingChainVars {
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), v+"=") {
				present++
				break
			}
		}
	}
	return present, nil
}

// migrateSealingChain, mevcut bir .env'in zincir durumunu YARGILAR.
//
// Üç sonuç: tam zincir (dokunulmaz), hiç zincir yok (mint edilebilir), ya da YARIM —
// ve yarım, yazılmaması gereken tek durumdur. Eşleşmeyen bir SEED'in yanına ikinci bir
// BINDING eklemek, çoğu .env okuyucusu için son-değer-kazanır demektir; yani başka bir
// kılıkta üzerine yazma. Operatöre söyleyip durmak, sessizce bozmaktan iyidir.
func migrateSealingChain(ctx context.Context, envFile string, out io.Writer) error {
	present, err := sealingChainState(envFile)
	if err != nil {
		return err
	}
	if present == 0 || present == 3 {
		return nil
	}
	return fmt.Errorf(
		"this stack's .env carries %d of 3 sealing variables — half a chain is not a chain.\n"+
			"  Remove %s from %s, then run `palbase start` again to mint a fresh one",
		present, strings.Join(sealingChainVars, ", "), envFile)
}

// migrateSealingChainWithMint, yargilar ve gerekiyorsa zinciri GERCEKTEN ekler.
//
// Uretici yiginin kendisi (`--init-env`): burada ikinci bir uygulama yazmak, gecerli bir
// anahtarin nasil gorundugu konusunda ikinci bir gorus olurdu, ve ikisinin anlasmadigi
// gün bir yığın hiçbir şeyin kabul etmediği bir anahtarla açılır.
func migrateSealingChainWithMint(ctx context.Context, envFile, sdkVersion string, out io.Writer) (bool, error) {
	if err := migrateSealingChain(ctx, envFile, out); err != nil {
		return false, err
	}
	present, err := sealingChainState(envFile)
	if err != nil || present == 3 {
		return false, err
	}

	fmt.Fprintln(out, "▸ this stack predates sealing — minting its chain")
	tmp, err := os.MkdirTemp("", "palbase-chain-")
	if err != nil {
		return false, err
	}
	defer removeTemp(tmp)

	palsvc := stackImages[0].ref(sdkVersion)
	mintArgs := append([]string{"run", "--rm"}, dockerRunAsHostUser()...)
	mintArgs = append(mintArgs,
		"-v", tmp+":/w", "-w", "/w", "--entrypoint", "/palsvc", palsvc,
		"--init-env", ".env")
	cmd := exec.CommandContext(ctx, "docker", mintArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("mint this stack's sealing chain: %w\n%s", err, output)
	}
	minted, err := os.ReadFile(filepath.Join(tmp, ".env"))
	if err != nil {
		return false, err
	}

	// ÜÇÜ BİRDEN, tek yazımda: yarım bir ekleme, yukarıda reddettiğimiz durumu kendi
	// elimizle yaratmak olurdu.
	var chain strings.Builder
	found := 0
	for _, line := range strings.Split(string(minted), "\n") {
		for _, v := range sealingChainVars {
			if strings.HasPrefix(strings.TrimSpace(line), v+"=") {
				chain.WriteString(strings.TrimSpace(line) + "\n")
				found++
			}
		}
	}
	if found != 3 {
		return false, fmt.Errorf(
			"the stack image minted %d of 3 sealing variables — it is too old to seal.\n"+
				"  Upgrade it: `palbase upgrade`", found)
	}

	// A NEWLINE FIRST, when the file does not end in one.
	//
	// This appends, and a .env whose last line has no trailing newline turns the
	// first appended line into a continuation of it. Measured: a file ending
	// `STACK_ROOT_KEY=xyz` came back as
	//
	//	STACK_ROOT_KEY=xyzPALBASE_SEALED_SIGNING_SEED=a
	//
	// which destroys TWO variables at once — the stack's own root key becomes
	// garbage and the signing seed is never seen. POSIX text files end in a
	// newline, but a .env is written by whatever wrote it, and "usually" is not
	// a property to append against.
	if err := appendSealingChain(envFile, chain.String()); err != nil {
		return false, err
	}
	fmt.Fprintln(out, "▸ sealing chain added — recreating the stack so it takes effect")
	return true, nil
}

// appendSealingChain, zinciri .env'in SONUNA ekler ve son satiri bozmaz.
//
// Ayrı bir fonksiyon, çünkü bozulma tam olarak BURADA oluyordu ve buna bir test
// yazmak için docker'a gitmeyen bir giriş noktası gerekiyor.
// openEnvForAppend is the seam that lets the Close-error path have a test.
//
// Close() returning an error is real (delayed-allocation and network file
// systems report ENOSPC exactly there) but there is no portable way to PROVOKE
// it from a test — so the branch that refuses to swallow it had no red of its
// own, and a mutation that dropped the check stayed green. An independent
// review said so and was right.
//
// A package-level opener is how the rest of this codebase already handles that
// problem (transport.DPoPSigner, backend.CloudKeyFetcher): production keeps the
// real one, the test substitutes a WriteCloser whose Close fails.
var openEnvForAppend = func(name string) (io.WriteCloser, error) {
	return os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0o600)
}

func appendSealingChain(envFile, chain string) error {
	existing, err := os.ReadFile(envFile)
	if err != nil {
		return err
	}
	f, err := openEnvForAppend(envFile)
	if err != nil {
		return err
	}
	// KAPANIŞ HATASI YUTULMAZ. `defer f.Close()` idi ve bu YAZILAN bir dosya.
	//
	// Gerekçenin doğrusu: `os.File` tamponlamaz, yani buradaki `WriteString`
	// zaten syscall'a iner — ilk yazdığımda "flush hatası ancak Close()'ta
	// görünür" demiştim, o `bufio.Writer` için doğru, bunun için değil. Close'un
	// hata döndürmesi yine de gerçek bir olaydır: ENOSPC'yi gecikmeli bildiren
	// dosya sistemleri ve ağ bağlı olanlar hatayı tam orada verir. Yutulduğunda
	// .env SESSİZCE yarım mühürlü kalıyordu — mührün eksik olduğu bir yığın,
	// hatası ancak çalışma anında ortaya çıkan bir yığındır.

	closed := false
	closeErr := func() error {
		if closed {
			return nil
		}
		closed = true
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("close %s after writing the sealing chain: %w", envFile, cerr)
		}
		return nil
	}
	defer func() { _ = closeErr() }()

	// Dosya newline ile bitmiyorsa, eklenen ilk satır önceki satırın DEVAMI olur
	// ve iki değişken birden yok olur.
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		if _, err := io.WriteString(f, "\n"); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(f, chain); err != nil {
		return err
	}
	return closeErr()
}

// dockerRunAsHostUser answers the `--user` arguments that make a container
// write as the person who owns the directory it is writing into.
//
// Without it the generator runs as the container's root, and on a daemon with
// user-namespace remapping — GitHub's runners, rootless Docker — that root maps
// to an unprivileged host uid which cannot write into a 0700 directory owned by
// somebody else. Measured: the stack's key minting died with
// `write .env: open .env: permission denied` (CI run 33859855884) while the
// same command worked on a developer's machine.
//
// Loosening the directory instead would be the wrong repair: it holds the
// stack's service-role key, and 0700 is why the rest of the machine cannot read
// it.
func dockerRunAsHostUser() []string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return nil // not a POSIX host; leave docker's default alone
	}
	return []string{"--user", strconv.Itoa(uid) + ":" + strconv.Itoa(gid)}
}
