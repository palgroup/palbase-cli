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
// Logs belong to an Environment, and this command reads WHICH ONE from the
// link — not from `--environment`. In a linked checkout that flag is refused
// rather than applied; it used to be dropped without a word, so a person who
// asked for staging read production and the banner agreed with them.
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/backend"

	"github.com/spf13/cobra"
)

// REST is the management transport subset the logs command needs.
type REST interface {
	Do(ctx context.Context, method, path string, body any, out any) error
}

// Resolvers carries the lazily-built REST client + the shared selection
// resolver.
type Resolvers struct {
	REST func() REST
	// CloudRef, bir yığın adresinin BULUT projesi olup olmadığını ve ref'ini
	// söyler. Adres şeklinden okunur, ağdan değil: ulaşılamayan bir düzlem,
	// bulut projesini "bulut değil" yapmaz.
	CloudRef func(url string) (string, bool)
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

// cloudRefOf, adresin BULUT projesi olup olmadığını ve ref'ini söyler.
//
// Çözücü verilmemişse "hayır" der ve çağıran self-host yoluna düşer: bir
// bağımlılığın yokluğu, bir adresi bulut projesi SAYMAK için gerekçe değildir.
func cloudRefOf(r Resolvers, url string) (string, bool) {
	if r.CloudRef == nil {
		return "", false
	}
	return r.CloudRef(url)
}

// showCloudOpts, bulut okumasının bayrakları — iki çağıran da aynı şeyi
// istiyor ve ikinci bir kopya, biri değiştiğinde sessizce ayrışırdı.
type showCloudOpts struct {
	source, levels, since, query string
	limit                        int
	follow, jsonOut              bool
}

// showCloud, düzlemin panel yüzeyinden bir ortamın loglarını okur.
//
// AYRI BİR FONKSİYON çünkü İKİ yol buraya geliyor: seçimle çözülen bir ortam ve
// BULUT projesine linkli bir checkout. İkisinin ayrı kopyaları olsaydı, cevap
// linkin varlığına göre değişmeye devam ederdi — düzeltilen kusur tam olarak
// oydu.
func showCloud(cmd *cobra.Command, r Resolvers, ref string, o showCloudOpts) error {
	source, levels, since, query := o.source, o.levels, o.since, o.query
	limit, follow, jsonOut := o.limit, o.follow, o.jsonOut
	windowSec, err := parseWindow(since)
	if err != nil {
		return err
	}
	read := func(window int) ([]logLine, error) {
		return readLines(cmd.Context(), r.REST(), ref,
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
			target, err := backend.ReadTarget()
			if err != nil {
				return err
			}
			if !target.OnThisMachine() {
				if ref, ok := cloudRefOf(r, target.URL); ok {
					fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", target.Describe())
					return showCloud(cmd, r, ref, showCloudOpts{
						source: source, levels: levels, since: since,
						query: query, limit: limit, follow: follow, jsonOut: jsonOut,
					})
				}
				return fmt.Errorf(
					"%s does not run on this machine, so its logs are not here either.\n"+
						"A self-hosted stack keeps its logs in its own containers; "+
						"`palbase start` brings a stack up here if you want to watch one",
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
