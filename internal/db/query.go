package db

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// queryResult is the stack's QueryResult. Rows are ARRAYS, not objects: SQL
// permits two columns of the same name and an object would silently drop one.
type queryResult struct {
	Columns    []string `json:"columns"`
	Rows       [][]any  `json:"rows"`
	RowCount   int      `json:"row_count"`
	Truncated  bool     `json:"truncated"`
	DurationMS float64  `json:"duration_ms"`
}

func queryCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "query <sql>",
		Args:  cobra.ExactArgs(1),
		Short: "Run one read-only statement against the local database",
		Long: `Run one statement against the local stack's database and print the rows.

READ ONLY, and the database enforces it: the statement runs inside a READ ONLY
transaction, so anything that would write is refused by Postgres itself. Change
the schema by editing db/public.ts and running ` + "`palbase db apply`" + ` — ad-hoc DDL
would put the database ahead of its declaration and the next push would refuse.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			stack, err := openLocal(cmd)
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]any{"sql": args[0], "limit": limit})
			if err != nil {
				return err
			}
			status, raw, err := stack.post(cmd.Context(), "/v1/management/sql", "application/json", body)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return apiError(status, raw)
			}
			var result queryResult
			if err := json.Unmarshal(raw, &result); err != nil {
				return fmt.Errorf("read the rows: %w", err)
			}
			renderRows(cmd.OutOrStdout(), result)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 200, "how many rows to bring back")
	return cmd
}

func renderRows(w io.Writer, result queryResult) {
	if len(result.Columns) == 0 {
		fmt.Fprintf(w, "%d row(s), %.0fms\n", result.RowCount, result.DurationMS)
		return
	}

	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, strings.Join(result.Columns, "\t"))
	for _, row := range result.Rows {
		cells := make([]string, len(row))
		for i, value := range row {
			cells[i] = renderCell(value)
		}
		fmt.Fprintln(table, strings.Join(cells, "\t"))
	}
	_ = table.Flush()

	fmt.Fprintf(w, "\n%d row(s), %.0fms", result.RowCount, result.DurationMS)
	if result.Truncated {
		// Said out loud: a reader who thinks they are seeing everything draws
		// conclusions from what is missing.
		fmt.Fprintf(w, " — cut at the limit, there are more")
	}
	fmt.Fprintln(w)
}

// renderCell prints a value the way a person reads it: NULL is distinct from an
// empty string, and a structure keeps its JSON rather than becoming map[a:1].
func renderCell(value any) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case string:
		return v
	case bool:
		return fmt.Sprintf("%t", v)
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	default:
		blob, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(blob)
	}
}
