// Package logs wires `palbase logs` — read the SELECTED ENVIRONMENT's deployed
// backend logs from the terminal.
//
// Transport: the plane's panel surface, over REST —
// `GET /v1/panel/environments/{ref}/logs`, which reads the same ClickHouse the
// panel's log screen does. It used to be a tRPC query against the Studio, which
// proxied a `modules/logs` search this plane does not run; the store, the query
// and the door are all different now, and there is exactly one of each.
//
// WHAT THAT COSTS, said plainly: the store answers a WINDOW, newest first, with
// no cursor. So `--follow` re-reads the newest lines every 2s and prints what it
// has not printed before (followCursor), instead of paging forward from a
// cursor. A line older than the poll window is missed by a follower that was not
// running — which is what a follower means anyway.
//
// Logs belong to an Environment: override the target with --environment.
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/palgroup/palbase-cli/internal/backend"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/selection"
)

// REST is the management transport subset the logs command needs.
type REST interface {
	Do(ctx context.Context, method, path string, body any, out any) error
}

// Resolvers carries the lazily-built REST client + the shared selection
// resolver.
type Resolvers struct {
	REST      func() REST
	Selection func() *selection.Resolver
}

// logLine is one line as this command prints it.
type logLine struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// logsResponse is the panel surface's answer: newest first.
type logsResponse struct {
	Entries []struct {
		Timestamp string `json:"timestamp"`
		Severity  string `json:"severity"`
		Source    string `json:"source"`
		Body      string `json:"body"`
	} `json:"entries"`
}

// logsPath is where an Environment's lines are read.
func logsPath(ref string) string {
	return "/v1/panel/environments/" + url.PathEscape(ref) + "/logs"
}

// defaultWindow is what `--since` defaults to. An hour is the panel's default
// too, so the two screens answer the same question by default.
const defaultWindow = time.Hour

// parseWindow turns `--since 15m` into seconds.
//
// `d` is accepted on top of Go's units because people write `--since 2d` and
// time.ParseDuration refuses it — a refusal that reads as "logs are broken"
// rather than "that unit is not supported".
func parseWindow(since string) (int, error) {
	since = strings.TrimSpace(since)
	if since == "" {
		return int(defaultWindow.Seconds()), nil
	}
	if days, ok := strings.CutSuffix(since, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--since %q: expected a window like 15m, 2h or 7d", since)
		}
		return n * 24 * 3600, nil
	}
	d, err := time.ParseDuration(since)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--since %q: expected a window like 15m, 2h or 7d", since)
	}
	return int(d.Seconds()), nil
}

// followInterval is how often --follow re-polls. A var so tests can shrink it.
var followInterval = 2 * time.Second

// Cmd returns the `palbase logs` command.
func Cmd(r Resolvers) *cobra.Command {
	var (
		source  string
		levels  string
		since   string
		query   string
		limit   int
		follow  bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show (or --follow) the selected environment's backend logs",
		Long: `Fetch the selected environment's deployed logs (newest last). --follow keeps polling for
new lines every 2s — Ctrl-C to stop.

  palbase logs                          last 100 lines
  palbase logs --level error,warn       errors and warnings only
  palbase logs --since 15m -q timeout   free-text filter over the last 15m
  palbase logs --follow                 tail live`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A LINKED PROJECT HAS NO LOG SURFACE, and saying so beats resolving
			// a cloud environment nobody asked for. Measured: in a checkout
			// linked to a project, this silently ignored the link, resolved the
			// selected cloud environment instead, and printed ITS logs — or
			// refused with "run palbase project use" — a command that does not
			// exist — which is advice for a
			// different question entirely.
			// A LINKED STACK'S LOGS ARE ITS CONTAINERS', and this used to say so
			// and stop — pointing at `docker logs <project>-runtime-1` and
			// leaving the person to work out which of three containers held what
			// they came for. The management surface still has no log operation;
			// that was never the reason to decline, because the containers ARE
			// the store and this command already knows which stack a checkout
			// belongs to.
			if target, err := backend.ReadTarget(); err == nil {
				// BU MAKİNEDE OLMAYAN BİR YIĞININ KONTEYNERLERİ DE BURADA
				// DEĞİL. Ayrım yokken bu komut bulut projesinde
				// "No such container: <proje>-runtime-1" diyordu — hiç var
				// olmayacak bir konteynerin adını vererek (canlıda ölçüldü
				// 2026-08-21). Docker'ın hatası, sorunun ne olduğunu SÖYLEMİYOR.
				//
				// Uzak yığının yönetim yüzeyinde bir log işlemi HENÜZ YOK
				// (ölçüldü: /v1/management/logs, /admin/logs → 404). Doğru
				// davranış, olmayan bir şeyi aramak değil, ne olduğunu söylemek.
				if !target.OnThisMachine() {
					return fmt.Errorf(
						"%s does not run on this machine, so its logs are not here either.\n"+
							"A remote project's management surface has no log operation yet; "+
							"`palbase start` brings a stack up here if you want to watch one.",
						target.Describe())
				}
				if err := dockerAvailable(cmd.Context()); err != nil {
					return err
				}
				dir, err := os.Getwd()
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", target.Describe())
				return ShowLocal(cmd.Context(), backend.LocalStackProject(dir), LocalOptions{
					Service: source,
					Since:   since,
					Limit:   limit,
					Query:   query,
					Levels:  splitLevels(levels),
					Follow:  follow,
				}, cmd.OutOrStdout())
			}

			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", sel.Describe())

			windowSec, err := parseWindow(since)
			if err != nil {
				return err
			}
			read := func(window int) ([]logLine, error) {
				return readLines(cmd.Context(), r.REST(), sel.EnvironmentRef(),
					window, limit, query, levels, source)
			}

			out := cmd.OutOrStdout()
			// One shot: the newest `limit` lines in the window, oldest-first.
			lines, err := read(windowSec)
			if err != nil {
				return err
			}
			if jsonOut && !follow {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(lines)
			}
			printLines(out, lines, jsonOut)
			if !follow {
				if len(lines) == 0 {
					fmt.Fprintln(out, "(no log lines — is the backend deployed and receiving traffic?)")
				}
				return nil
			}

			// Follow: re-read the newest lines over a SHORT window and print what
			// has not been printed. The store answers a window rather than a
			// cursor, so there is nothing to page forward from; the cursor here
			// dedupes by timestamp+message, which is what kept equal-timestamp
			// lines from being dropped or repeated on the old transport too.
			//
			// The window is a few polls wide on purpose: exactly one interval
			// would lose a line to any hiccup between two reads.
			cursor := newFollowCursor(lines)
			const followWindow = 60
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(followInterval):
				}
				fresh, err := read(followWindow)
				if err != nil {
					return err
				}
				printLines(out, cursor.fresh(fresh), jsonOut)
			}
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "Only this source (e.g. backend)")
	cmd.Flags().StringVar(&levels, "level", "", "Comma-separated levels: debug,info,warn,error")
	cmd.Flags().StringVar(&since, "since", "", "Look-back window, e.g. 15m, 2h")
	cmd.Flags().StringVarP(&query, "query", "q", "", "Free-text line filter")
	cmd.Flags().IntVar(&limit, "limit", 100, "Max lines per fetch (1-500)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Keep polling for new lines (Ctrl-C to stop)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON (one array, or one object per line with --follow)")
	return cmd
}

func reverse(in []logLine) []logLine {
	out := make([]logLine, 0, len(in))
	for i := len(in) - 1; i >= 0; i-- {
		out = append(out, in[i])
	}
	return out
}

func printLines(w interface{ Write([]byte) (int, error) }, lines []logLine, jsonOut bool) {
	for _, l := range lines {
		if jsonOut {
			raw, _ := json.Marshal(l)
			fmt.Fprintln(w, string(raw))
			continue
		}
		fmt.Fprintf(w, "%s %-5s %-8s %s\n", l.Timestamp, l.Level, l.Source, l.Message)
	}
}

// followCursor tracks the last seen timestamp plus the messages already
// printed AT that timestamp, so a FORWARD poll starting from lastTS neither
// re-prints nor drops equal-timestamp lines.
type followCursor struct {
	lastTS     string
	seenAtLast map[string]bool
}

func newFollowCursor(printed []logLine) *followCursor {
	c := &followCursor{seenAtLast: map[string]bool{}}
	for _, l := range printed {
		c.observe(l)
	}
	return c
}

func (c *followCursor) observe(l logLine) {
	if l.Timestamp != c.lastTS {
		if l.Timestamp > c.lastTS {
			c.lastTS = l.Timestamp
			c.seenAtLast = map[string]bool{}
		}
	}
	if l.Timestamp == c.lastTS {
		c.seenAtLast[l.Message] = true
	}
}

// fresh filters a FORWARD poll result down to the not-yet-printed lines and
// advances the cursor over them.
func (c *followCursor) fresh(lines []logLine) []logLine {
	out := []logLine{}
	for _, l := range lines {
		if l.Timestamp < c.lastTS {
			continue
		}
		if l.Timestamp == c.lastTS && c.seenAtLast[l.Message] {
			continue
		}
		out = append(out, l)
		c.observe(l)
	}
	return out
}

// splitLevels turns the comma-separated --level flag into the list the local
// reader filters on. The cloud arm forwards the string as-is, so the flag keeps
// one shape and only this side has to know what it means.
func splitLevels(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// readLines fetches one window's newest lines and returns them oldest-first,
// which is the order a terminal reads in.
//
// The filters travel as query parameters, and every one of them is APPLIED by
// the store — see buildLogsQuery. A filter the server ignores is worse than no
// filter: the command answers, the lines look plausible, and nobody learns that
// `--level error` showed them everything.
func readLines(ctx context.Context, rest REST, ref string,
	windowSec, limit int, query, levels, source string) ([]logLine, error) {
	q := url.Values{}
	q.Set("window_seconds", strconv.Itoa(windowSec))
	q.Set("limit", strconv.Itoa(limit))
	if query != "" {
		q.Set("search", query)
	}
	if levels != "" {
		q.Set("level", levels)
	}
	if source != "" {
		q.Set("source", source)
	}

	var resp logsResponse
	if err := rest.Do(ctx, http.MethodGet, logsPath(ref)+"?"+q.Encode(), nil, &resp); err != nil {
		return nil, fmt.Errorf("read logs: %w", err)
	}
	// Newest first on the wire, oldest first on the screen.
	lines := make([]logLine, 0, len(resp.Entries))
	for i := len(resp.Entries) - 1; i >= 0; i-- {
		e := resp.Entries[i]
		lines = append(lines, logLine{
			Timestamp: e.Timestamp,
			Source:    e.Source,
			Level:     e.Severity,
			Message:   e.Body,
		})
	}
	return lines, nil
}
