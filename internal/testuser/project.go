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
// deploy already calls to materialise the fixtures `config/test-users.ts`
// declares, so the accounts a push carries and the ones this verb mints come
// out of the same door — there is no second implementation of what a test user
// is.
//
// WHAT THE STACK DOES NOT HAVE is the seeding engine: a template's declared
// rows need the project's schema to know what a `todos` row is, and the apply
// path says so out loud when it skips them. So --template and `clone` refuse
// here by name rather than pretending, and the refusal says which half is
// missing.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// adminTestUsers is the stack route a deploy already uses for the same job.
const adminTestUsers = "/admin/test-users"

// linkedProject is the project this checkout is bound to, when it is bound to
// one. Absent, the caller falls through to the cloud arm — the same shape every
// other target-relative verb uses, so `test-user` needs no new rule about which
// half answers.
func linkedProject() (backend.Target, backend.Credentials, bool) {
	target, err := backend.ReadTarget()
	if err != nil || target.URL == "" {
		return backend.Target{}, backend.Credentials{}, false
	}
	cred, _, err := backend.Credential(target.URL)
	if err != nil {
		return backend.Target{}, backend.Credentials{}, false
	}
	return target, cred, true
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
	} `json:"users"`
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

func createOnProject(ctx context.Context, target backend.Target, cred backend.Credentials,
	count int, template string, jsonOut bool, out io.Writer) error {
	if template != "" {
		return fmt.Errorf(
			"this project cannot seed a template's rows.\n"+
				"  The ACCOUNT can be minted here — `palbase test-user create` without --template does that —\n"+
				"  but the rows %q declares need the project's schema to know what they are, and that engine\n"+
				"  lives in the cloud control plane rather than in a stack. A deploy reports the same gap.",
			template)
	}

	var res stackMinted
	if err := callProject(ctx, target, cred, http.MethodPost, adminTestUsers,
		map[string]any{"count": count, "with_tokens": true}, &res); err != nil {
		return err
	}
	if jsonOut {
		return encodeJSON(out, res)
	}
	fmt.Fprintf(out, "✓ created %d test user(s)\n", len(res.Users))
	for _, u := range res.Users {
		fmt.Fprintf(out, "  id:       %s\n", u.UserID)
		fmt.Fprintf(out, "  email:    %s\n", u.Email)
		fmt.Fprintf(out, "  password: %s\n", u.Password)
		if u.AccessToken != "" {
			fmt.Fprintf(out, "  token:    %s\n", u.AccessToken)
		}
	}
	// The stack returns them ONCE and cannot return them again; saying so is the
	// difference between a person copying the password and a person re-running
	// the command expecting it back.
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
