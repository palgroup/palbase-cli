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
	{"PALBASE_PALSVC_IMAGE", "ghcr.io/palgroup/palbase-palsvc:0.29.0",
		"cd v2 && DOCKER_BUILDKIT=1 docker build -t palbase-palsvc -f Dockerfile ."},
	{"PALBASE_RUNTIME_IMAGE", "ghcr.io/palgroup/palbase-runtime-dev:0.29.0",
		"cd v2/runtime && DOCKER_BUILDKIT=1 docker build --target dev -t palbase-runtime-dev -f Dockerfile ."},
}

func newStartCmd() *cobra.Command {
	var reset bool
	cmd := &cobra.Command{
		Use:   "start",
		Args:  cobra.NoArgs,
		Short: "Bring up a stack on this machine and point this checkout at it",
		Long: `Run this project on this machine.

Your source is mounted rather than deployed: save a controller and the running
stack serves the new version, with no build, no artifact and no version history.

Everything after this — push, db, secret, spec, logs — acts on the local stack
until ` + "`palbase stop`" + `.

    palbase start --reset     throw the database away and build it from db/schema.ts`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := RequireBackendPlane(dir); err != nil {
				return err
			}
			return runStart(cmd.Context(), dir, reset, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&reset, "reset", false,
		"empty the local database first, then build it from db/schema.ts")
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

func runStart(ctx context.Context, dir string, reset bool, out io.Writer) error {
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
	if err := ensureBootValues(ctx, envFile, out); err != nil {
		return err
	}

	httpPort, err := freePort()
	if err != nil {
		return err
	}
	pgPort, err := freePort()
	if err != nil {
		return err
	}
	// The ports live in the same file as the rest of the boot values, so a
	// restart lands on the SAME address: a URL written into .palbase/local.json,
	// a generated client and an iOS build all point at a number, and picking a
	// fresh one every start would silently break each of them.
	settled, err := rememberPorts(envFile, httpPort, pgPort)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", settled.http)

	project := "palbase-" + group
	if reset {
		fmt.Fprintln(out, "▸ removing the local database")
		// -v takes the volumes with it, which is the reset: the schema comes
		// back from db/schema.ts on the next boot rather than from a dump.
		if err := compose(ctx, stackDir, project, envFile, dir, settled, out, "down", "-v"); err != nil {
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
	if err := compose(ctx, stackDir, project, envFile, dir, settled, out, "up", "-d", "--wait", "postgres"); err != nil {
		return err
	}
	fmt.Fprintln(out, "▸ applying every module's schema")
	if err := compose(ctx, stackDir, project, envFile, dir, settled, out,
		"run", "--rm", "--no-deps", "palsvc", "--migrate-only"); err != nil {
		return fmt.Errorf("migrate the local database: %w", err)
	}

	if err := compose(ctx, stackDir, project, envFile, dir, settled, out, "up", "-d"); err != nil {
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

	fmt.Fprintf(out, "▸ %s (local)\n", url)
	fmt.Fprintln(out, "  edit a controller and the running stack serves it — no build, no deploy")
	fmt.Fprintln(out, "  `palbase stop` points this checkout back at its project")
	return nil
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
	ports, err := readPorts(envFile)
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
	if err := compose(ctx, stackDir, project, envFile, dir, ports, out, "down"); err != nil {
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
// tag; `ghcr.io/palgroup/palbase-palsvc:0.29.0` is not.
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
func ensureBootValues(ctx context.Context, envFile string, out io.Writer) error {
	if _, err := os.Stat(envFile); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
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
		return fmt.Errorf("generate the stack's keys: %w\n%s", err, output)
	}
	// The generator writes as root inside the container; the operator has to be
	// able to read their own secrets afterwards.
	if err := os.Chmod(envFile, 0o600); err != nil {
		return err
	}
	return nil
}

type ports struct{ http, pg int }

// rememberPorts keeps the first pair this group was given, for as long as this
// group exists.
//
// It does NOT check whether the port is free, and that check is exactly what had
// to go: the port is occupied precisely when OUR OWN stack is already up, which
// is every `palbase start` on a running stack — so the stack moved, and every
// xcconfig, app config and generated client written by an earlier `link` still
// named the old number. A port genuinely taken by something else surfaces as
// compose refusing to bind it, with the number in the message.
func rememberPorts(envFile string, httpPort, pgPort int) (ports, error) {
	existing, err := readPorts(envFile)
	if err != nil {
		return ports{}, err
	}
	if existing.http != 0 && existing.pg != 0 {
		return existing, nil
	}
	chosen := ports{http: httpPort, pg: pgPort}
	if err := setEnvValues(envFile, map[string]string{
		"PALBASE_HTTP_PORT": strconv.Itoa(chosen.http),
		"PALBASE_PG_PORT":   strconv.Itoa(chosen.pg),
	}); err != nil {
		return ports{}, err
	}
	return chosen, nil
}

func readPorts(envFile string) (ports, error) {
	httpPort, err := valueFromEnvFile(envFile, "PALBASE_HTTP_PORT")
	if err != nil && !os.IsNotExist(err) {
		return ports{}, nil
	}
	pgPort, _ := valueFromEnvFile(envFile, "PALBASE_PG_PORT")
	h, _ := strconv.Atoi(httpPort)
	p, _ := strconv.Atoi(pgPort)
	return ports{http: h, pg: p}, nil
}

// compose runs one docker compose verb against this group's stack.
func compose(ctx context.Context, stackDir, project, envFile, projectDir string, p ports, out io.Writer, args ...string) error {
	full := append([]string{
		"compose",
		"-f", filepath.Join(stackDir, composeFile),
		"-p", project,
		"--env-file", envFile,
	}, args...)

	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Dir = stackDir
	cmd.Env = append(os.Environ(),
		"PALBASE_PROJECT_DIR="+projectDir,
		"PALBASE_HTTP_PORT="+strconv.Itoa(p.http),
		"PALBASE_PG_PORT="+strconv.Itoa(p.pg),
	)
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
func waitForStack(ctx context.Context, url string, limit time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(limit)
	var last string
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
			_ = res.Body.Close()
			if res.StatusCode < 500 {
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
