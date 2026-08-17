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
	"slices"
	"strings"
)

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

func runStackPush(ctx context.Context, target Target, cred Credentials, approve bool, w io.Writer) error {
	// Where this is going, before anything goes. Both push paths funnel through
	// here — the linked-project one and the probe in the cloud command — so one
	// line here covers both without either being able to forget it.
	fmt.Fprintf(w, "▸ %s\n", target.Describe())

	// A LOCAL STACK IS NOT A PLACE TO PUBLISH TO, and the reason is measured
	// rather than stylistic: the dev runtime serves the DIRECTORY it has mounted
	// and never follows the deploy pointer, so a push here builds an artifact
	// and activates a version nothing will ever load. The code it carries is
	// already running — that is what `palbase start` did — so the one thing push
	// exists for has already happened by other means.
	//
	// It refuses rather than warning, because the failure it prevents is silent:
	// "I pushed" would be true, "it shipped" would not, and nothing downstream
	// would say so.
	if target.Local {
		return fmt.Errorf(
			"this checkout is pointed at the stack running on this machine, which already serves this directory — a push here would activate a version nothing loads.\n" +
				"  palbase stop       point it back at the project, then push\n" +
				"  palbase db apply   if it was the schema you wanted applied here")
	}

	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	// The SDK this project RUNS, before anything is compiled against it. A
	// different major produces a bundle the runtime cannot execute, and the
	// failure arrives as a missing function three layers from its cause.
	if err := ensureProjectSDK(ctx, dir, target, cred, w); err != nil {
		return err
	}

	// BUILD FIRST. A stack takes an artifact and cannot make one — bundling needs
	// this project's own node_modules, which live here. Shipping whatever a
	// previous build left on disk is how somebody edits a controller, pushes, and
	// deploys yesterday's code under today's commit message.
	if err := buildStackArtifact(ctx, dir, w); err != nil {
		return err
	}

	// SECRETS BEFORE CODE, and the order is the feature. Code that reads a
	// credential nobody set deploys green and 500s on its first request — which
	// is exactly what todoapp's /graph/* routes did — so a declared `required`
	// secret with no value anywhere stops the push before anything ships.
	//
	// What it does NOT do is overwrite. A name already set on the target is left
	// alone and reported, because the value there is usually the production one
	// and the value here is usually not.
	if err := carrySecrets(ctx, dir, target, cred, approve, w); err != nil {
		return err
	}

	tarball, err := BuildStackTarball(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "sending %s (%d KB)\n", dir, len(tarball)/1024)

	url := target.URL + "/v1/management/push"
	if approve {
		url += "?accept-data-loss=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(tarball))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/gzip")
	cred.Apply(req)

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
		// short(), not [:12]: this line runs AFTER the code has shipped, so a
		// stack that answers 200 with a short or empty digest would turn a
		// successful push into a Go stack trace.
		fmt.Fprintf(w, "live: %d endpoint(s), %s\n", out.EndpointCount, short(out.Digest))

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
		return fmt.Errorf("repeat with --approve when that is what you mean")

	case http.StatusUnauthorized:
		return fmt.Errorf("that stack no longer accepts this session — run `palbase login`")
	case http.StatusForbidden:
		return fmt.Errorf("this account may not manage %s — ask whoever runs it for `palsvc --grant-management`", target.URL)
	default:
		return fmt.Errorf("push refused (%d): %s", res.StatusCode, trimBody(body))
	}
}

// carrySecrets makes the target hold every secret this project's code declares.
//
// FILL, don't replace. The gap is the interesting case — a fresh environment
// that has never been given a credential — and the already-set case is the
// dangerous one: the value there is usually production's and the value here is
// usually not. So a name the target already holds is skipped and reported, and
// --approve is what replaces it.
//
// Values come from the stack running on this machine and from nowhere else.
// There is no file to read (there are none) and no prompt to answer (a push is
// not an interview), so a required name with no value anywhere stops the push —
// before the code ships, because code that reads a credential nobody set
// deploys green and fails on its first request.
func carrySecrets(ctx context.Context, dir string, target Target, cred Credentials, approve bool, w io.Writer) error {
	plan, err := planSecrets(ctx, dir, target, cred)
	if err != nil {
		return err
	}
	if len(plan.MissingRequired) > 0 {
		return fmt.Errorf(
			"%s is required by config/secrets.ts and set nowhere — nothing was sent.\n"+
				"Set it on the target with `palbase secret set %s --stdin`, or start a local stack and set it there for the push to carry",
			strings.Join(plan.MissingRequired, ", "), plan.MissingRequired[0])
	}

	toWrite := plan.Fill
	if approve {
		// Replacing is a decision, and --approve is where it is made. The
		// already-set names are only replaceable when they are ALSO available
		// here — there is nothing to replace them with otherwise.
		source, sourceCred, ok := localSource(target)
		if ok {
			if available, err := secretNames(ctx, source, sourceCred); err == nil {
				held := map[string]bool{}
				for _, name := range available {
					held[name] = true
				}
				for _, name := range plan.Present {
					if held[name] {
						toWrite = append(toWrite, name)
					}
				}
			}
		}
	}
	if len(toWrite) == 0 {
		for _, name := range plan.Present {
			fmt.Fprintf(w, "secret %s: already set there, left alone\n", name)
		}
		return nil
	}

	source, sourceCred, ok := localSource(target)
	if !ok {
		return fmt.Errorf("the values for %s are on the stack this machine runs, and it is not up — `palbase start`",
			strings.Join(toWrite, ", "))
	}
	for _, name := range toWrite {
		value, err := secretValue(ctx, source, sourceCred, name)
		if err != nil {
			return fmt.Errorf("read %s from the local stack: %w", name, err)
		}
		if err := putSecret(ctx, target, cred, name, value); err != nil {
			return fmt.Errorf("set %s on %s: %w", name, target.Describe(), err)
		}
		// The NAME. Never the value, and never a length or a prefix either —
		// those are how somebody confirms a guess.
		fmt.Fprintf(w, "secret %s: set\n", name)
	}
	for _, name := range plan.Present {
		if !slices.Contains(toWrite, name) {
			fmt.Fprintf(w, "secret %s: already set there, left alone\n", name)
		}
	}
	return nil
}
