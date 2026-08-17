// Package db is `palbase db`: the schema of the stack IN FRONT OF YOU.
//
// This group used to act on a cloud environment through Studio, and that is
// exactly the job `palbase push` took over — code, schema, config and secrets
// travel as one change set, so a second path that moves the schema alone could
// only ever disagree with it.
//
// What is left is the reason to keep the group at all: trying things out on the
// stack running on this machine. Turning on an extension, watching what a policy
// does to a query, taking a plan apart before it becomes a push. The data here is
// disposable, which is what makes it the right place to be wrong.
//
// So every verb here requires a running local stack and refuses without one. It
// is not a safety belt on a cloud command — there is no cloud command; it is the
// group saying what it is.
package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Work on the schema of the stack running on this machine",
		Long: `Try schema changes against your local stack before they become a push.

  palbase db plan    What it would take to make the local database match db/schema.ts
  palbase db apply   Do it
  palbase db query   Run one read-only statement and see the rows

There are no migration files: db/schema.ts is the declaration, the plan is
computed against the database as it is right now, and ` + "`palbase push`" + ` is what
carries it — with the code, the config and the secrets — to a cloud environment.

To start over, ` + "`palbase start --reset`" + ` throws the whole local database away and
brings it back empty; nothing here does that, because at that point the schema is
not the thing you are resetting.`,
	}
	cmd.AddCommand(planCmd(), applyCmd(), queryCmd())
	return cmd
}

// local is one resolved local stack: where it is, and how to be somebody on it.
type local struct {
	target backend.Target
	token  string
	client *http.Client
}

// openLocal resolves the stack in front of you, or refuses.
//
// The refusal names BOTH ways out, because the person who hit it wanted one of
// two different things: to try the change here (start the stack), or to make it
// somewhere real (push). Naming only the first would send somebody who meant
// production to a local database, which is the more expensive mistake — they
// would watch it succeed and believe it.
func openLocal(cmd *cobra.Command) (local, error) {
	target, err := backend.ReadTarget()
	if err == nil && !target.Local {
		err = fmt.Errorf("this checkout is linked to %s", target.Describe())
	}
	if err != nil {
		return local{}, fmt.Errorf(
			"`palbase db` works on the stack running on this machine, and there is not one.\n" +
				"  palbase start   bring one up here, then try the change against it\n" +
				"  palbase push    send db/schema.ts, the code and the config to the linked project",
		)
	}

	token, _, err := backend.Credential(target.URL)
	if err != nil {
		return local{}, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", target.Describe())
	return local{target: target, token: token, client: backend.HTTPClient(target)}, nil
}

// post sends one management request and returns the status and body. It does not
// decide what a status MEANS: plan, apply and query each have their own, and a
// shared "is this ok" would have to know all of them.
func (l local) post(ctx context.Context, path, contentType string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(l.target.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+l.token)
	req.Header.Set("Content-Type", contentType)

	res, err := l.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("reach %s: %w", l.target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return res.StatusCode, raw, err
}

// readSchema loads db/schema.ts, which is the whole input to plan and apply.
func readSchema() (string, error) {
	path := filepath.Join("db", "schema.ts")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("no %s in this directory — `palbase db` reads the schema this project declares", path)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// apiError renders the stack's error envelope, falling back to the raw body when
// it is not one — a body that could not be parsed is still evidence.
func apiError(status int, body []byte) error {
	var env struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &env) == nil && env.Description != "" {
		return fmt.Errorf("%s (%d)", env.Description, status)
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 500 {
		trimmed = trimmed[:500] + "…"
	}
	if trimmed == "" {
		return fmt.Errorf("the stack answered %d with no detail", status)
	}
	return fmt.Errorf("%s (%d)", trimmed, status)
}
