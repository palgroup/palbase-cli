package db

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

// schemaApplied is the stack's SchemaApplied.
type schemaApplied struct {
	Changed bool     `json:"changed"`
	Summary []string `json:"summary"`
}

func applyCmd() *cobra.Command {
	var approve bool
	cmd := &cobra.Command{
		Use:   "apply",
		Args:  cobra.NoArgs,
		Short: "Make the local database match db/public.ts",
		Long: `Apply db/public.ts to the local stack's database, in one transaction.

Changes that take data away are refused, and the refusal lists them with their
row counts. --approve runs those too.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stack, err := openLocal(cmd)
			if err != nil {
				return err
			}
			sources, err := readSchema()
			if err != nil {
				return err
			}
			payload, err := schemaBody(sources)
			if err != nil {
				return err
			}

			path := "/v1/management/schema/apply"
			if approve {
				path += "?approve=true"
			}
			status, body, err := stack.post(cmd.Context(), path, "application/json", payload)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			switch status {
			case http.StatusOK:
				var applied schemaApplied
				if err := json.Unmarshal(body, &applied); err != nil {
					return fmt.Errorf("read the result: %w", err)
				}
				if !applied.Changed {
					fmt.Fprintln(out, "✓ the database already matches db/public.ts")
					return nil
				}
				for _, line := range applied.Summary {
					fmt.Fprintf(out, "  %s\n", line)
				}
				fmt.Fprintln(out, "✓ applied")
				return nil

			case http.StatusConflict:
				// The refusal IS the plan: the same document `db plan` prints,
				// so the person deciding sees the counts rather than a sentence
				// telling them a count exists.
				var plan schemaPlan
				if err := json.Unmarshal(body, &plan); err != nil {
					return apiError(status, body)
				}
				renderPlan(out, plan)
				return fmt.Errorf("refused: this would take data away — run it again with --approve if that is what you mean")

			default:
				return apiError(status, body)
			}
		},
	}
	cmd.Flags().BoolVar(&approve, "approve", false, "also run the changes that take data away")
	return cmd
}
