package backend

// stack_push.go — `palbase push` against a stack you run.
//
// One call ships everything (design-management-api.md §9): the stack applies the
// schema its declaration asks for and activates the code in the same request, so
// the two reach the same version together. Two calls would leave a window where
// the code is new and the tables are not, and that window is where a deploy
// looks successful and the first request does not.
//
// The stack does the work — there is no build service and no workflow engine on
// this path. What the CLI sends is the project; what it gets back is the digest,
// how many endpoints are actually serving, and what the schema did.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func newStackPushCmd() *cobra.Command {
	var acceptDataLoss bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Ship this project to the stack it is linked to",
		Long: `Send this project to the linked stack.

The stack plans and applies the schema its db/schema.ts declares, activates the
new code, and then CHECKS that it is serving: a push that ends with zero
endpoints is a failed push, not a successful one.

A schema change that would remove data is refused whole — the additive half
included — until you say that is what you mean:

    palbase push --accept-data-loss`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := ReadTarget()
			if err != nil {
				return err
			}
			token, err := LoadToken(target.URL)
			if err != nil {
				return err
			}
			return runStackPush(cmd.Context(), target, token, acceptDataLoss, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&acceptDataLoss, "accept-data-loss", false,
		"also run the schema changes that destroy data (the refusal names them)")
	return cmd
}

// pushResult mirrors the contract's PushResult.
type pushResult struct {
	Digest        string `json:"digest"`
	EndpointCount int    `json:"endpoint_count"`
	Schema        struct {
		Changed bool     `json:"changed"`
		Summary []string `json:"summary"`
	} `json:"schema"`
}

// pushRefusal mirrors the contract's PushRefusal.
type pushRefusal struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Destructive      []struct {
		Kind    string `json:"kind"`
		Table   string `json:"table"`
		Column  string `json:"column"`
		Rows    int    `json:"rows"`
		NonNull int    `json:"non_null"`
	} `json:"destructive"`
}

func runStackPush(ctx context.Context, target Target, token string, acceptDataLoss bool, w io.Writer) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	// The SDK this project RUNS, before anything is compiled against it. A
	// different major produces a bundle the runtime cannot execute, and the
	// failure arrives as a missing function three layers from its cause.
	if err := ensureProjectSDK(ctx, dir, target, token, w); err != nil {
		return err
	}

	// BUILD FIRST. A stack takes an artifact and cannot make one — bundling needs
	// this project's own node_modules, which live here. Shipping whatever a
	// previous build left on disk is how somebody edits a controller, pushes, and
	// deploys yesterday's code under today's commit message.
	if err := buildStackArtifact(ctx, dir, w); err != nil {
		return err
	}

	tarball, err := BuildStackTarball(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "sending %s (%d KB)\n", dir, len(tarball)/1024)

	url := target.URL + "/v1/management/push"
	if acceptDataLoss {
		url += "?accept-data-loss=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(tarball))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/gzip")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := stackClient(target).Do(req)
	if err != nil {
		return fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}

	switch res.StatusCode {
	case http.StatusOK:
		var out pushResult
		if err := json.Unmarshal(body, &out); err != nil {
			return fmt.Errorf("the stack answered 200 with something unexpected: %s", trimBody(body))
		}
		if out.Schema.Changed {
			fmt.Fprintln(w, "schema:")
			for _, line := range out.Schema.Summary {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
		fmt.Fprintf(w, "live: %d endpoint(s), %s\n", out.EndpointCount, out.Digest[:12])

		// The contract just changed — this is the moment, and the only moment,
		// when a committed client can be brought level with the stack without
		// anybody remembering to. A client one deploy behind is a compile error
		// at best and a 404 at worst.
		if err := RefreshSpec(ctx, w); err != nil {
			return fmt.Errorf("the push landed, but the client could not be regenerated: %w", err)
		}
		return nil

	case http.StatusConflict:
		// The one refusal that is a DECISION rather than a mistake, so it prints
		// what it would cost instead of a status code.
		var refusal pushRefusal
		if err := json.Unmarshal(body, &refusal); err != nil {
			return fmt.Errorf("refused: %s", trimBody(body))
		}
		fmt.Fprintln(w, "this push would remove data:")
		for _, d := range refusal.Destructive {
			if d.Column != "" {
				fmt.Fprintf(w, "  drop column  %s.%s (%d rows, %d non-null)\n", d.Table, d.Column, d.Rows, d.NonNull)
			} else {
				fmt.Fprintf(w, "  drop table   %s (%d rows)\n", d.Table, d.Rows)
			}
		}
		return fmt.Errorf("repeat with --accept-data-loss when that is what you mean")

	case http.StatusUnauthorized:
		return fmt.Errorf("that stack no longer accepts this session — run `palbase login`")
	case http.StatusForbidden:
		return fmt.Errorf("this account may not manage %s — ask whoever runs it for `palsvc --grant-management`", target.URL)
	default:
		return fmt.Errorf("push refused (%d): %s", res.StatusCode, trimBody(body))
	}
}
