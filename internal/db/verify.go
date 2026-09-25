package db

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// schemaVerify is the stack's SchemaVerify: whether the database is what the
// applied-schema record — the record every push plans against — says it is.
type schemaVerify struct {
	Status      string   `json:"status"` // match | drift | unrecorded
	Reason      string   `json:"reason"`
	Differences []string `json:"differences"`
	Record      *struct {
		ID            int64  `json:"id"`
		AppliedAt     string `json:"applied_at"`
		ReleaseDigest string `json:"release_digest"`
		WriterVersion string `json:"writer_version"`
		Hash          string `json:"hash"`
	} `json:"record"`
	Epoch *struct {
		Recorded int64 `json:"recorded"`
		Current  int64 `json:"current"`
	} `json:"epoch"`
	Pending []pendingStep `json:"pending"`
}

type pendingStep struct {
	Kind  string `json:"kind"`
	Table string `json:"table"`
	Name  string `json:"name"`
}

// The exit codes are diff(1)'s, because that is what this is: 0 the two
// agree, 1 they differ, 2 there was nothing to compare — no record, or the
// stack could not be asked. A script that treats "could not check" as "fine"
// is the one this split exists for.
const (
	exitVerifyDrift        = 1
	exitVerifyUnverifiable = 2
)

type verifyExit struct {
	code int
	err  error
}

func (e verifyExit) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}
func (e verifyExit) ExitCode() int { return e.code }

// DeliberateExitStatus: a status chosen, not an accident (see changesError).
func (verifyExit) DeliberateExitStatus() {}

func verifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Args:  cobra.NoArgs,
		Short: "Check that the database is what its applied-schema record says it is",
		Long: `Every push plans against a record the database keeps of the last push it
took — not against the database's catalog. This compares that record with the
catalog, which is the one other place the catalog is read.

Exits 0 when they agree, 1 when they differ (the differences are listed — DDL
someone ran outside palbase, most often), 2 when there is nothing to compare:
no push has been recorded on this database yet, or the stack could not be
asked.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stack, err := openLocal(cmd)
			if err != nil {
				return verifyExit{code: exitVerifyUnverifiable, err: err}
			}
			result, err := askVerify(cmd.Context(), stack)
			if err != nil {
				return verifyExit{code: exitVerifyUnverifiable, err: err}
			}
			renderVerify(cmd.OutOrStdout(), result)
			switch result.Status {
			case "match":
				return nil
			case "drift":
				return verifyExit{code: exitVerifyDrift}
			default:
				return verifyExit{code: exitVerifyUnverifiable}
			}
		},
	}
}

func askVerify(ctx context.Context, stack local) (schemaVerify, error) {
	status, body, err := stack.get(ctx, "/v1/management/schema/verify")
	if err != nil {
		return schemaVerify{}, err
	}
	if status != http.StatusOK {
		return schemaVerify{}, apiError(status, body)
	}
	var out schemaVerify
	if err := json.Unmarshal(body, &out); err != nil {
		return schemaVerify{}, fmt.Errorf("read the verification: %w", err)
	}
	return out, nil
}

func renderVerify(w io.Writer, v schemaVerify) {
	switch v.Status {
	case "match":
		fmt.Fprintf(w, "✓ the database is what its applied-schema record says it is")
	case "drift":
		fmt.Fprintf(w, "✗ the database is NOT what its applied-schema record says it is")
	default:
		fmt.Fprintf(w, "? nothing to compare")
	}
	if v.Record != nil {
		fmt.Fprintf(w, " (record %d, %s", v.Record.ID, v.Record.AppliedAt)
		if v.Record.ReleaseDigest != "" {
			fmt.Fprintf(w, ", release %s", shortDigest(v.Record.ReleaseDigest))
		}
		fmt.Fprint(w, ")")
	}
	fmt.Fprintln(w)
	if v.Reason != "" {
		fmt.Fprintf(w, "  %s\n", v.Reason)
	}
	for _, d := range v.Differences {
		fmt.Fprintf(w, "  %s\n", d)
	}
	if len(v.Pending) > 0 {
		fmt.Fprintln(w, "the last push still owes, and the next push runs:")
		for _, p := range v.Pending {
			fmt.Fprintf(w, "  %s %s.%s\n", p.Kind, p.Table, p.Name)
		}
	}
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
