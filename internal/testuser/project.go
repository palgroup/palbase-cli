package testuser

// project.go — test users on the project this checkout is linked to.
//
// `test-user` was the last verb that could only talk to the cloud: it went
// through the Studio client for every subcommand, so a person standing in a
// checkout bound to a stack on their own machine — the whole point of `palbase
// start` — could not mint one. Measured 2026-08-17: `palbase test-user create`
// in a linked project answered "no project selected", advice that cannot be
// followed there.
//
// The stack has had the surface all along. `POST /admin/test-users` is what a
// the management surface publishes for fixture accounts
// declares, so the accounts a push carries and the ones this verb mints come
// out of the same door — there is no second implementation of what a test user
// is.
//
// It has the SEEDING engine too, now. `--template` and `clone` used to refuse
// here by name — the stack could mint an account but not the rows the project
// declared it should own — and that refusal is gone: `POST /admin/test-users`
// takes a template name, `POST /admin/test-users/clone` copies an existing
// user's tree, and both answer with what landed in each table.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// The management surface's published paths — the same door the panel uses.
//
// They used to be the module's own /admin/* routes, reached directly. Going
// through /v1/management means one gate decides who may do this, for the panel
// and for here, instead of two places agreeing by accident.
const (
	adminTestUsers     = "/v1/management/test-users"
	adminTestUserClone = "/v1/management/test-users/clone"
	adminTemplates     = "/v1/management/test-users/templates"
)

// resolveProject is the project this verb acts on: the one this checkout is
// linked to, or the one the caller selected (backend.ResolveTarget knows both).
//
// It used to answer a BOOL, and a false sent the caller to a second arm that
// spoke tRPC to the Studio — a different protocol, a different door, and a
// different response shape for the same question. That arm is gone. One door
// means a failure to reach it is a failure to report, not a reason to try
// somewhere else, so this returns the error that says which of the two ways to
// name a project is missing.
func resolveProject(cmd *cobra.Command) (backend.Target, backend.Credentials, error) {
	target, err := backend.ReadTarget()
	if err != nil {
		return backend.Target{}, backend.Credentials{}, err
	}
	if target.URL == "" {
		// `--environment <ref>` was the alternative offered here, and it is not
		// one: the flag selects an environment of a project already named, and
		// a target with no address has not named one.
		return backend.Target{}, backend.Credentials{}, fmt.Errorf(
			"%s names no address — run `palbase link <project>` again, or `palbase link <ref>` for one environment of it",
			target.Describe())
	}
	cred, _, err := backend.Credential(target.URL)
	if err != nil {
		return backend.Target{}, backend.Credentials{}, err
	}
	return target, cred, nil
}

// stackMinted is what POST /admin/test-users answers with. Its field names are
// the stack's, not the Studio's — `user_id` here is `id` there — so the two
// wires are decoded by two types instead of one type with optional halves.
type stackMinted struct {
	Users []struct {
		UserID      string `json:"user_id"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		AccessToken string `json:"access_token"`
		// Name is what a TEMPLATE mint calls this identity; a plain mint sends
		// none and the position supplies one.
		Name string `json:"name"`
		// Inserted is how many rows landed in each table. Empty for a plain
		// mint, which declares no data.
		Inserted map[string]int `json:"inserted"`
	} `json:"users"`
}

// asIdentities renders a mint in the shape `createTestApi({ identities })`
// accepts, so the output of this command can be handed to the harness with
// nothing in between.
//
// WHY IT IS NOT THE STACK'S SHAPE. The stack answers
// `{"users":[{user_id,email,password,access_token}]}` — an array, snake_case,
// and `user_id` where the harness reads `id`. Nothing documented that, and
// nothing translated it, so every project wrote the same adapter: a measured
// customer run carried a mint script plus five lines copied into a test setup.
// Two spellings of one fact is a translation somebody has to keep correct
// forever, and the CLI is the only side that can end it.
//
// Names are positional (`user1`, `user2`, …) because a plain mint declares
// none; a template mint names them and that name is used.
func asIdentities(res stackMinted) map[string]any {
	identities := make(map[string]any, len(res.Users))
	for i, u := range res.Users {
		name := u.Name
		if name == "" {
			name = fmt.Sprintf("user%d", i+1)
		}
		entry := map[string]any{"id": u.UserID, "email": u.Email, "password": u.Password}
		if u.AccessToken != "" {
			entry["accessToken"] = u.AccessToken
		}
		identities[name] = entry
	}
	return map[string]any{"identities": identities}
}

// stackTemplates is what GET /admin/test-user-templates answers: the
// declarations this stack holds, without the definitions — a fixture's carries
// its password.
type stackTemplates struct {
	Templates []struct {
		Name   string   `json:"name"`
		Email  string   `json:"email"`
		Tables []string `json:"tables"`
	} `json:"templates"`
}

type stackListed struct {
	Users []struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	} `json:"users"`
}

// callProject sends one request and turns anything that is not a success into
// an error carrying the stack's own words — a refusal a stack took the trouble
// to explain is more use than "request failed".
func callProject(ctx context.Context, target backend.Target, cred backend.Credentials,
	method, path string, body any, out any) error {
	var payload []byte
	contentType := ""
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
		contentType = "application/json"
	}
	status, raw, err := backend.CallProject(ctx, target, cred, method, path, payload, contentType)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s answered %d: %s", target.Describe(), status, trimBody(raw))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func trimBody(raw []byte) string {
	const max = 400
	if len(raw) > max {
		return string(raw[:max]) + "…"
	}
	return string(raw)
}

// MintIdentities creates `count` test identities and returns the SAME payload
// `create --json` prints. Exported so `palbase test` mints through this path
// rather than shelling out to itself — one mint, one shape, one place to fix.
func MintIdentities(cmd *cobra.Command, count int) ([]byte, error) {
	target, cred, err := resolveProject(cmd)
	if err != nil {
		return nil, err
	}
	var res stackMinted
	body := map[string]any{"count": count, "with_tokens": true}
	if err := callProject(cmd.Context(), target, cred, http.MethodPost, adminTestUsers, body, &res); err != nil {
		return nil, err
	}
	return json.Marshal(asIdentities(res))
}

func createOnProject(ctx context.Context, target backend.Target, cred backend.Credentials,
	count int, template string, jsonOut bool, out io.Writer) error {
	body := map[string]any{"count": count, "with_tokens": true}
	if template != "" {
		// The stack looks the declaration up by name and writes the rows it
		// carries. `count` rides along: several instances of one template is a
		// normal thing to want, and each gets its own copy of the data.
		body["template"] = template
	}

	var res stackMinted
	if err := callProject(ctx, target, cred, http.MethodPost, adminTestUsers, body, &res); err != nil {
		return err
	}
	if jsonOut {
		return encodeJSON(out, asIdentities(res))
	}
	if template != "" {
		fmt.Fprintf(out, "✓ created %d test user(s) from template %q\n", len(res.Users), template)
	} else {
		fmt.Fprintf(out, "✓ created %d test user(s)\n", len(res.Users))
	}
	for _, u := range res.Users {
		printStackUser(out, u.UserID, u.Email, u.Password, u.AccessToken, u.Inserted)
	}
	// The stack returns them ONCE and cannot return them again; saying so is the
	// difference between a person copying the password and a person re-running
	// the command expecting it back.
	fmt.Fprintln(out, "  (creds shown once — store them now)")
	return nil
}

// printStackUser is one minted user, credentials first and then what they
// arrived holding — the rows are the reason a template exists, so they are
// printed beside the login rather than left for a query to discover.
func printStackUser(out io.Writer, id, email, password, token string, inserted map[string]int) {
	fmt.Fprintf(out, "  id:       %s\n", id)
	fmt.Fprintf(out, "  email:    %s\n", email)
	fmt.Fprintf(out, "  password: %s\n", password)
	if token != "" {
		fmt.Fprintf(out, "  token:    %s\n", token)
	}
	if len(inserted) == 0 {
		return
	}
	tables := make([]string, 0, len(inserted))
	for table := range inserted {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		parts = append(parts, fmt.Sprintf("%d %s", inserted[table], table))
	}
	fmt.Fprintf(out, "  rows:     %s\n", strings.Join(parts, ", "))
}

// putTemplatesOnProject replaces the stack's fixture-template set.
//
// The body arrives already marshalled because its SHAPE is the thing under
// test: the route takes `{templates: {...}}`, and the retired courier's habit of
// sending the evaluated document as-is is what made a 200 mean nothing was
// stored. Marshalling it here would hide that decision one layer down.
func putTemplatesOnProject(ctx context.Context, target backend.Target, cred backend.Credentials, body []byte) error {
	return callProject(ctx, target, cred, http.MethodPut, adminTemplates, json.RawMessage(body), nil)
}

// templatesOnProject lists what this stack has been told a test user can be.
func templatesOnProject(ctx context.Context, target backend.Target, cred backend.Credentials,
	jsonOut bool, out io.Writer) error {
	var res stackTemplates
	if err := callProject(ctx, target, cred, http.MethodGet, adminTemplates, nil, &res); err != nil {
		return err
	}
	if jsonOut {
		return encodeJSON(out, res)
	}
	if len(res.Templates) == 0 {
		// It used to say "Declare one in config/test-users.ts, then `palbase
		// push`" — and that file is gone (2026-08-29). The stack is the author
		// now, so the message names the verb that actually writes it.
		fmt.Fprintln(out, "No fixture accounts on this stack. Write a set with `palbase test-user templates set --file <path>`.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tFIXTURE EMAIL\tSEEDS")
	for _, t := range res.Templates {
		email := t.Email
		if email == "" {
			email = "-"
		}
		seeds := "-"
		if len(t.Tables) > 0 {
			seeds = strings.Join(t.Tables, ", ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, email, seeds)
	}
	return tw.Flush()
}

// cloneOnProject copies a test user's rows onto fresh accounts.
func cloneOnProject(ctx context.Context, target backend.Target, cred backend.Credentials,
	sourceUserID, email, password string, overrides map[string]map[string]any,
	jsonOut bool, out io.Writer) error {
	body := map[string]any{"source_user_id": sourceUserID, "with_tokens": true}
	if len(overrides) > 0 {
		body["set"] = overrides
	}
	// NAMED CREDENTIALS, when the caller gave them. This used to be refused here
	// by name — "not available against a local stack" — because the door did not
	// ask for them; it does now, so a fixture whose login is written in the test
	// that uses it is minted the same way everywhere.
	if email != "" {
		body["email"] = email
		body["password"] = password
	}

	var res stackMinted
	if err := callProject(ctx, target, cred, http.MethodPost, adminTestUserClone, body, &res); err != nil {
		return err
	}
	if jsonOut {
		return encodeJSON(out, res)
	}
	fmt.Fprintf(out, "✓ cloned %s\n", sourceUserID)
	for _, u := range res.Users {
		printStackUser(out, u.UserID, u.Email, u.Password, u.AccessToken, u.Inserted)
	}
	fmt.Fprintln(out, "  (creds shown once — store them now)")
	return nil
}

func listOnProject(ctx context.Context, target backend.Target, cred backend.Credentials,
	jsonOut bool, out io.Writer) error {
	var res stackListed
	if err := callProject(ctx, target, cred, http.MethodGet, adminTestUsers, nil, &res); err != nil {
		return err
	}
	if jsonOut {
		return encodeJSON(out, res)
	}
	if len(res.Users) == 0 {
		fmt.Fprintln(out, "No test users in this project.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tEMAIL\tVERIFIED")
	for _, u := range res.Users {
		verified := "no"
		if u.EmailVerified {
			verified = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", u.ID, u.Email, verified)
	}
	return tw.Flush()
}

func deleteOnProject(ctx context.Context, target backend.Target, cred backend.Credentials,
	userID string, out io.Writer) error {
	if err := callProject(ctx, target, cred, http.MethodDelete, adminTestUsers+"/"+userID, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ deleted %s\n", userID)
	return nil
}
