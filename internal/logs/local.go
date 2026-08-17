package logs

// local.go — the logs of a stack running on this machine.
//
// This command used to refuse for a linked local stack and point at
// `docker logs <project>-runtime-1`. The refusal was honest — the management
// surface really has no log operation, and the runtime really writes to stdout —
// but it was also the wrong answer to the question. The person asked what their
// backend is printing. Telling them to go and ask docker, and to work out which
// of three containers holds it, is a tool declining to do the one thing they
// came for.
//
// So it reads them. There is no log STORE to build for this: the containers are
// the store, docker keeps the buffer, and the CLI already knows the compose
// project a checkout belongs to.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// stackService is one of the containers a local stack runs, in the order a
// person reads them: their own code first.
type stackService struct {
	name  string
	label string
}

// The runtime first because it is where a person's own controllers print, and
// postgres last because it is the one they almost never mean. palsvc sits
// between: platform errors (a refused key, a module that will not mount) surface
// there and are worth seeing without asking twice.
var stackServices = []stackService{
	{"runtime", "runtime"},
	{"palsvc", "palsvc"},
	{"postgres", "postgres"},
}

// LocalOptions is what the command asks for, in the vocabulary the flags use.
type LocalOptions struct {
	// Service limits the output to one container. Empty means all of them.
	Service string
	// Since is a docker duration ("15m"); empty means the whole buffer.
	Since string
	// Limit is the tail length per container when Since is not given.
	Limit int
	// Query keeps only lines containing it, matched case-insensitively.
	Query string
	// Levels keeps only lines whose level field is one of these.
	Levels []string
	// Follow streams until the context is cancelled.
	Follow bool
}

// ShowLocal prints what the stack for this checkout is saying.
func ShowLocal(ctx context.Context, project string, opts LocalOptions, out io.Writer) error {
	services := stackServices
	if opts.Service != "" {
		found := false
		for _, s := range stackServices {
			if s.name == opts.Service {
				services, found = []stackService{s}, true
				break
			}
		}
		if !found {
			names := make([]string, 0, len(stackServices))
			for _, s := range stackServices {
				names = append(names, s.name)
			}
			return fmt.Errorf("this stack has no %q container — it runs %s",
				opts.Service, strings.Join(names, ", "))
		}
	}

	// FOLLOW IS ONE STREAM PER CONTAINER, interleaved as they arrive, because a
	// person following logs is watching for something to happen and cannot know
	// in advance which container will say it.
	if opts.Follow {
		return followAll(ctx, project, services, opts, out)
	}

	for _, svc := range services {
		lines, err := readContainer(ctx, project, svc, opts)
		if err != nil {
			// A container that is not running is not an error worth stopping
			// for: `palbase logs` after a crash is exactly when its last lines
			// matter most, and the other two may still have them.
			fmt.Fprintf(out, "%s: %v\n", svc.label, err)
			continue
		}
		for _, line := range lines {
			fmt.Fprintf(out, "%-8s %s\n", svc.label, line)
		}
	}
	return nil
}

func dockerLogsArgs(project string, svc stackService, opts LocalOptions, follow bool) []string {
	args := []string{"logs"}
	if follow {
		args = append(args, "--follow")
	}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	} else if opts.Limit > 0 {
		args = append(args, "--tail", fmt.Sprint(opts.Limit))
	}
	return append(args, containerName(project, svc))
}

// containerName is what compose calls the container: <project>-<service>-1.
func containerName(project string, svc stackService) string {
	return project + "-" + svc.name + "-1"
}

func readContainer(ctx context.Context, project string, svc stackService, opts LocalOptions) ([]string, error) {
	cmd := exec.CommandContext(ctx, "docker", dockerLogsArgs(project, svc, opts, false)...)
	// docker writes a container's stderr to OUR stderr; a backend's own error
	// lines go there, and dropping them would hide the half worth reading.
	var combined strings.Builder
	cmd.Stdout, cmd.Stderr = &combined, &combined
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("not running (%s)", strings.TrimSpace(firstLine(combined.String())))
	}
	var kept []string
	for _, line := range strings.Split(combined.String(), "\n") {
		if line = strings.TrimRight(line, "\r"); line == "" {
			continue
		}
		if keepLine(line, opts) {
			kept = append(kept, line)
		}
	}
	return kept, nil
}

func followAll(ctx context.Context, project string, services []stackService, opts LocalOptions, out io.Writer) error {
	lines := make(chan string, 256)
	live := 0
	for _, svc := range services {
		cmd := exec.CommandContext(ctx, "docker", dockerLogsArgs(project, svc, opts, true)...)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			continue
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(out, "%s: not running\n", svc.label)
			continue
		}
		live++
		go func(svc stackService) {
			scanner := bufio.NewScanner(pipe)
			scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for scanner.Scan() {
				if line := scanner.Text(); keepLine(line, opts) {
					lines <- fmt.Sprintf("%-8s %s", svc.label, line)
				}
			}
			_ = cmd.Wait()
			lines <- ""
		}(svc)
	}
	if live == 0 {
		return fmt.Errorf("no container of this stack is running — `palbase start` brings it up")
	}
	for live > 0 {
		select {
		case <-ctx.Done():
			return nil
		case line := <-lines:
			if line == "" {
				live--
				continue
			}
			fmt.Fprintln(out, line)
		}
	}
	return nil
}

// keepLine applies --level and -q. Both are ANDed, which is what somebody
// narrowing a search expects: each flag they add shows them less.
func keepLine(line string, opts LocalOptions) bool {
	if opts.Query != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(opts.Query)) {
		return false
	}
	if len(opts.Levels) == 0 {
		return true
	}
	for _, level := range opts.Levels {
		// palsvc writes slog JSON (`"level":"ERROR"`); the runtime writes plain
		// lines that carry the word. One matcher for both rather than a parser
		// that only understands one of them.
		if strings.Contains(strings.ToUpper(line), strings.ToUpper(`"level":"`+level+`"`)) ||
			strings.Contains(strings.ToUpper(line), strings.ToUpper(level)) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// dockerAvailable is checked before the refusal is written, so "docker is not
// running" never masquerades as "this stack has no logs".
func dockerAvailable(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(c, "docker", "info").Run(); err != nil {
		return fmt.Errorf("docker is not running on this machine, and a local stack's logs live in its containers")
	}
	return nil
}
