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
var stackImages = []struct{ env, fallback, build string }{
	{"PALBASE_PALSVC_IMAGE", "ghcr.io/palgroup/palbase/palsvc:0.36.5",
		"cd v2 && DOCKER_BUILDKIT=1 docker build -t palbase-palsvc -f Dockerfile ."},
	{"PALBASE_RUNTIME_IMAGE", "ghcr.io/palgroup/palbase/runtime-dev:0.36.5",
		"cd v2/runtime && DOCKER_BUILDKIT=1 docker build --target dev -t palbase-runtime-dev -f Dockerfile ."},
	// The EDGE, and it belongs here for the same reason the other two do: it is
	// the one container that publishes, so a stack without it has no address at
	// all. Absent from this list, a missing edge image surfaced as docker's raw
	// pull error at `compose up` instead of this command's own refusal with the
	// line that builds it.
	{"PALBASE_EDGE_IMAGE", "ghcr.io/palgroup/palbase/edge:0.36.5",
		"cd v2/deploy/envoy && DOCKER_BUILDKIT=1 docker build -t palbase-edge ."},
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
	if err := imagesPresent(ctx); err != nil {
		return err
	}

	state, err := stackStateDir(group)
	if err != nil {
		return err
	}
	envFile := filepath.Join(state, ".env")
	envChanged, err := ensureBootValues(ctx, envFile, out)
	if err != nil {
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
	if err := waitForStack(ctx, url, 90*time.Second); err != nil {
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

	stackDir, err := stackDirectory(group)
	if err != nil {
		return err
	}
	state, err := stackStateDir(group)
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
func imagesPresent(ctx context.Context) error {
	for _, want := range stackImages {
		image := want.fallback
		if override := strings.TrimSpace(os.Getenv(want.env)); override != "" {
			image = override
		}
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
						"  and then: %s=<your tag>",
					image, want.build, want.env)
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
// The rule docker itself uses: a name whose first path segment carries a dot or
// a colon — or is `localhost` — is a registry host. `palbase-palsvc` is a local
// tag; `ghcr.io/palgroup/palbase/palsvc:0.36.5` is not.
func isRegistryImage(image string) bool {
	head, _, hasSlash := strings.Cut(image, "/")
	if !hasSlash {
		return false
	}
	return strings.ContainsAny(head, ".:") || head == "localhost"
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
func ensureBootValues(ctx context.Context, envFile string, out io.Writer) (bool, error) {
	if _, err := os.Stat(envFile); err == nil {
		// .env VAR — ama bu "yapacak bir sey yok" demek degil. Muhurleme zinciri bu
		// dosyaya SONRADAN eklendi; onu tasimayan bir yigin onu buradan kazanir.
		return migrateSealingChainWithMint(ctx, envFile, out)
	} else if !os.IsNotExist(err) {
		return false, err
	}

	fmt.Fprintln(out, "▸ generating this stack's keys")
	dir := filepath.Dir(envFile)
	palsvc := stackImages[0].fallback
	if override := strings.TrimSpace(os.Getenv(stackImages[0].env)); override != "" {
		palsvc = override
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-v", dir+":/w", "-w", "/w", "--entrypoint", "/palsvc", palsvc,
		"--init-env", filepath.Base(envFile))
	if output, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("generate the stack's keys: %w\n%s", err, output)
	}
	// The generator writes as root inside the container; the operator has to be
	// able to read their own secrets afterwards.
	if err := os.Chmod(envFile, 0o600); err != nil {
		return false, err
	}
	// A brand-new .env: nothing to recreate, the containers do not exist yet.
	return false, nil
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
func waitForStack(ctx context.Context, url string, limit time.Duration) error {
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

// sealingChainVars, bir yiginin muhurleme zincirini olusturan uc degisken.
//
// UCU BIRDEN ya da hicbiri. Yarim bir zincir, zinciri olmayan bir yigindan DAHA
// kotudur: calisiyormus gibi gorunur ve dogrulanamayan belgeler uretir.
var sealingChainVars = []string{
	"PALBASE_SEALED_SIGNING_SEED",
	"PALBASE_SEALED_BINDING",
	"PALBASE_SEALED_ROOT",
}

// sealingChainState, .env'in uc zincir degiskeninden kacini tasidigini soyler.
//
// Dosya yoksa (0, nil): cagiran bunu "yeni yigin" olarak okur, hata olarak degil —
// ensureBootValues'in normal uretim yolu tam olarak o durumdur.
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
// Uc sonuc: tam zincir (dokunulmaz), hic zincir yok (mint edilebilir), ya da YARIM —
// ve yarim, yazilmamasi gereken tek durumdur. Eslesmeyen bir SEED'in yanina ikinci bir
// BINDING eklemek, cogu .env okuyucusu icin son-deger-kazanir demektir; yani baska bir
// kilikta uzerine yazma. Operatore soyleyip durmak, sessizce bozmaktan iyidir.
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
			"  Remove %s from %s, then run `palbase start` again to mint a fresh one.",
		present, strings.Join(sealingChainVars, ", "), envFile)
}

// migrateSealingChainWithMint, yargilar ve gerekiyorsa zinciri GERCEKTEN ekler.
//
// Uretici yiginin kendisi (`--init-env`): burada ikinci bir uygulama yazmak, gecerli bir
// anahtarin nasil gorundugu konusunda ikinci bir gorus olurdu, ve ikisinin anlasmadigi
// gun bir yigin hicbir seyin kabul etmedigi bir anahtarla acilir.
func migrateSealingChainWithMint(ctx context.Context, envFile string, out io.Writer) (bool, error) {
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
	defer os.RemoveAll(tmp)

	palsvc := stackImages[0].fallback
	if override := strings.TrimSpace(os.Getenv(stackImages[0].env)); override != "" {
		palsvc = override
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-v", tmp+":/w", "-w", "/w", "--entrypoint", "/palsvc", palsvc,
		"--init-env", ".env")
	if output, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("mint this stack's sealing chain: %w\n%s", err, output)
	}
	minted, err := os.ReadFile(filepath.Join(tmp, ".env"))
	if err != nil {
		return false, err
	}

	// UCU BIRDEN, tek yazimda: yarim bir ekleme, yukarida reddettigimiz durumu kendi
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
// Ayri bir fonksiyon, cunku bozulma tam olarak BURADA oluyordu ve buna bir test
// yazmak icin docker'a gitmeyen bir giris noktasi gerekiyor.
func appendSealingChain(envFile, chain string) error {
	existing, err := os.ReadFile(envFile)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// Dosya newline ile bitmiyorsa, eklenen ilk satir onceki satirin DEVAMI olur
	// ve iki degisken birden yok olur.
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(chain)
	return err
}
