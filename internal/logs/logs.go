// Package logs wires `palbase logs` — tail the deployed backend's logs from
// the terminal. Transport: Studio tRPC `logs.entries.search` (Studio validates
// project membership, then proxies modules/logs' /logs/v1/search). One-shot by
// default (newest lines, printed oldest-first); --follow polls the same search
// FORWARD from the last seen timestamp every 2s — no SSE/apikey plumbing, the
// session token authorizes every poll.
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// Studio is the tRPC transport subset the logs command needs.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries the lazily-built Studio client (apps.Resolvers pattern).
type Resolvers struct {
	Studio func() Studio
}

// logLine mirrors the Studio logs router's LogLine wire type.
type logLine struct {
	Timestamp string            `json:"timestamp"`
	Source    string            `json:"source"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Labels    map[string]string `json:"labels"`
}

type searchResponse struct {
	Lines      []logLine `json:"lines"`
	NextCursor *string   `json:"next_cursor"`
}

// followInterval is how often --follow re-polls. A var so tests can shrink it.
var followInterval = 2 * time.Second

// Cmd returns the `palbase logs` command.
func Cmd(r Resolvers) *cobra.Command {
	var (
		refFlag   string
		branch    string
		source    string
		container string
		levels    string
		since     string
		query     string
		limit     int
		follow    bool
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show (or --follow) the deployed backend's logs",
		Long: `Fetch the project's deployed logs (newest last). --follow keeps polling for
new lines every 2s — Ctrl-C to stop.

  palbase logs                          last 100 lines
  palbase logs --level error,warn       errors and warnings only
  palbase logs --since 15m -q timeout   free-text filter over the last 15m
  palbase logs --follow                 tail live`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := auth.ResolveProjectRef(refFlag)
			if err != nil {
				return err
			}
			if branch == "" {
				if cfg, cfgErr := auth.LoadProjectConfig(); cfgErr == nil && cfg.DefaultEnv != "" && cfg.DefaultEnv != "main" {
					branch = cfg.DefaultEnv
				}
			}

			base := map[string]any{"ref": ref, "limit": limit}
			if branch != "" {
				base["branch"] = branch
			}
			if source != "" {
				base["source"] = source
			}
			if container != "" {
				base["container"] = container
			}
			if levels != "" {
				base["level"] = strings.Split(levels, ",")
			}
			if since != "" {
				base["since"] = since
			}
			if query != "" {
				base["q"] = query
			}

			out := cmd.OutOrStdout()
			search := func(extra map[string]any) (searchResponse, error) {
				input := map[string]any{}
				maps.Copy(input, base)
				maps.Copy(input, extra)
				var resp searchResponse
				err := r.Studio().Query(cmd.Context(), "logs.entries.search", input, &resp)
				return resp, err
			}

			// One shot: newest `limit` lines, printed oldest-first.
			resp, err := search(map[string]any{"direction": "BACKWARD"})
			if err != nil {
				return fmt.Errorf("logs.entries.search: %w", err)
			}
			lines := reverse(resp.Lines)
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

			// Follow: poll FORWARD from the last seen timestamp. Lines sharing
			// the last timestamp are deduped by message so a poll boundary
			// neither drops nor repeats them.
			cursor := newFollowCursor(lines)
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(followInterval):
				}
				extra := map[string]any{"direction": "FORWARD"}
				if cursor.lastTS != "" {
					// modules/logs requires from and to TOGETHER (a lone from is a
					// 400). `to` slightly in the future is fine — it only bounds the
					// window; a skewed client clock at worst delays a line to the
					// next poll, never drops it (from stays at the cursor).
					extra["from"] = cursor.lastTS
					extra["to"] = time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
				} else if since == "" {
					extra["since"] = "1m"
				}
				resp, err := search(extra)
				if err != nil {
					return fmt.Errorf("logs.entries.search: %w", err)
				}
				printLines(out, cursor.fresh(resp.Lines), jsonOut)
			}
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch (defaults to the active branch; omit for main)")
	cmd.Flags().StringVar(&source, "source", "", "Only this source (e.g. backend)")
	cmd.Flags().StringVar(&container, "container", "", "Only this container")
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
