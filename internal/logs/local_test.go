package logs

import (
	"context"
	"io"
	"strings"
	"testing"
)

// `palbase logs` used to refuse for a stack on this machine and point at
// `docker logs <project>-runtime-1`. The refusal was true about the management
// surface and wrong about the question: the containers ARE the store, and the
// command already knows which stack a checkout belongs to. These pin the parts
// that decide WHICH container and WHICH lines.

func TestTheContainerNameIsTheOneComposeMade(t *testing.T) {
	got := containerName("palbase-todoapp", stackService{name: "runtime"})
	if got != "palbase-todoapp-runtime-1" {
		t.Errorf("containerName = %q — docker would be asked for a container that does not exist", got)
	}
}

func TestTheRuntimeIsReadFirst(t *testing.T) {
	// A person running this wants their OWN code's output. palsvc is worth
	// seeing without asking twice (a refused key, a module that will not mount);
	// postgres almost never is.
	if stackServices[0].name != "runtime" {
		t.Errorf("the first container read is %q, not the one holding the person's controllers", stackServices[0].name)
	}
	if stackServices[len(stackServices)-1].name != "postgres" {
		t.Errorf("postgres is not last, so its checkpoint chatter buries the rest")
	}
}

// The edge is a container of this stack, so `palbase logs` reads it.
//
// It is where a request that never reached anybody's code shows up — a 404 the
// route table decided, a preflight the CORS policy refused, a 429 — and without
// it that request is invisible: the runtime never saw it and palsvc never saw
// it, so the two sources a person can read both say nothing happened.
func TestTheEdgeIsAReadableSource(t *testing.T) {
	var names []string
	for _, s := range stackServices {
		names = append(names, s.name)
	}
	joined := strings.Join(names, ",")
	if joined != "runtime,palsvc,envoy,postgres" {
		t.Fatalf("the sources are %s; want runtime,palsvc,envoy,postgres — your code first, "+
			"the platform, then the door, and the database last", joined)
	}

	// And it is selectable: `--source envoy` must not be refused as unknown.
	err := ShowLocal(context.Background(), "no-such-project",
		LocalOptions{Service: "envoy", Limit: 1}, io.Discard)
	if err != nil && strings.Contains(err.Error(), "has no \"envoy\" container") {
		t.Errorf("--source envoy was refused as unknown: %v", err)
	}
}

func TestSinceWinsOverTail(t *testing.T) {
	// Both would be a contradiction: --since asks for a window and --tail for a
	// count, and docker applies them together, which answers neither question.
	args := dockerLogsArgs("p", stackService{name: "runtime"}, LocalOptions{Since: "15m", Limit: 100}, false)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--since 15m") {
		t.Errorf("--since was dropped: %v", args)
	}
	if strings.Contains(joined, "--tail") {
		t.Errorf("--tail was sent alongside --since: %v", args)
	}
}

func TestFollowAsksDockerToFollow(t *testing.T) {
	args := dockerLogsArgs("p", stackService{name: "runtime"}, LocalOptions{Follow: true}, true)
	if !strings.Contains(strings.Join(args, " "), "--follow") {
		t.Errorf("a follow does not follow: %v", args)
	}
}

func TestTheFiltersNarrowTogether(t *testing.T) {
	// palsvc writes slog JSON and the runtime writes plain lines. One matcher
	// for both, because a person filtering does not know or care which container
	// is about to say the thing they are looking for.
	slogLine := `{"time":"...","level":"ERROR","msg":"fatal","error":"mount storage"}`
	plainLine := `[runtime] FATAL: zero controllers collected`

	for _, c := range []struct {
		name string
		line string
		opts LocalOptions
		want bool
	}{
		{"no filter keeps everything", slogLine, LocalOptions{}, true},
		{"level matches slog json", slogLine, LocalOptions{Levels: []string{"error"}}, true},
		{"level matches a plain line", plainLine, LocalOptions{Levels: []string{"fatal"}}, true},
		{"a level nobody logged drops it", slogLine, LocalOptions{Levels: []string{"warn"}}, false},
		{"query is case-insensitive", slogLine, LocalOptions{Query: "MOUNT STORAGE"}, true},
		{"query that matches nothing drops it", slogLine, LocalOptions{Query: "postgres"}, false},
		// ANDed: each flag a person adds shows them LESS. ORing them would make
		// adding a filter widen the output, which is the opposite of narrowing.
		{"query and level are both required", slogLine,
			LocalOptions{Query: "mount storage", Levels: []string{"warn"}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := keepLine(c.line, c.opts); got != c.want {
				t.Errorf("keepLine(%q) = %v, want %v", c.line, got, c.want)
			}
		})
	}
}

func TestLevelsArriveAsAList(t *testing.T) {
	// The flag is one comma-separated string, the shape the cloud arm forwards.
	// Only this side has to know what it means.
	got := splitLevels(" error , warn ,, ")
	if len(got) != 2 || got[0] != "error" || got[1] != "warn" {
		t.Errorf("splitLevels = %#v, want [error warn]", got)
	}
	if len(splitLevels("")) != 0 {
		t.Error("an empty flag produced a filter, which would drop every line")
	}
}
