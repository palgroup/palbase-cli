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
	"time"
)

// pushResult mirrors the contract's PushResult.
//
// There is no `config` field, because the contract has no such field: a push
// carries code and schema. Mirroring one anyway is how a client keeps reporting
// a section the server stopped sending.
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

// How long a push waits for a project that is still coming up, and how often it
// looks. The window measured on a fresh project was under a minute; the budget
// is several times that so a slow cell is covered, and the interval is long
// enough that waiting costs a handful of requests rather than a flood.
// VAR, sabit DEĞİL: yalnız testler kısaltıyor. Bir testin üç dakika beklemesi,
// ölçtüğü şeyin mesaj olduğu yerde saf israftır.
var (
	stackReadyWait       = 3 * time.Minute
	stackReadyRetryEvery = 6 * time.Second
)

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

	// THE PLANE FIRST, before anything is downloaded or written. Run in an app
	// checkout this used to install the project's SDK into it — node_modules and
	// all — and only then discover there were no controllers to send. A refusal
	// that arrives after a side effect is not a refusal.
	if err := RequireBackendPlane(dir); err != nil {
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
	uses, err := buildStackArtifact(ctx, dir, w)
	if err != nil {
		return err
	}

	// @Upload names a bucket that must EXIST — storage will not create one on
	// demand, so the route would compile, deploy, activate, and 404 the first
	// file somebody uploads. The list to compare against is the stack's own:
	// buckets are created there, by `palbase storage add` or by the panel.
	if len(uses) > 0 {
		have, bucketErr := stackBuckets(ctx, target)
		if bucketErr != nil {
			return bucketErr
		}
		if bucketErr := unknownUploadBuckets(uses, bucketNames(have)); bucketErr != nil {
			return bucketErr
		}
	}

	// SECRETS AND FIXTURES ARE NOT CARRIED BY THE PUSH ANY MORE.
	//
	// They used to be: `carrySecrets` read config/secrets.ts and filled the
	// target's gaps (stopping the push when a required name was set nowhere), and
	// `carryTestUsers` PUT config/test-users.ts at the stack. Both files are gone
	// (2026-08-29) and both jobs moved to where the setting lives:
	//
	//   secrets     `palbase secret set NAME --stdin`
	//   fixtures    `palbase test-user templates set --file <path>`
	//
	// The stop the secret carrier provided is not lost, it MOVED EARLIER: the name
	// a controller may spell now comes from `palbase-stack.d.ts`, generated off the
	// stack, so code reading a secret nobody set does not compile. That is a
	// keystroke, not a push.

	tarball, err := BuildStackTarball(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "sending %s (%d KB)\n", dir, len(tarball)/1024)

	url := target.URL + "/v1/management/push"
	if approve {
		url += "?accept-data-loss=true"
	}
	status, body, err := sendWaitingForReady(ctx, stackClient(target), func() (*http.Request, error) {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(tarball))
		if rerr != nil {
			return nil, rerr
		}
		req.Header.Set("content-type", "application/gzip")
		cred.Apply(req)
		return req, nil
	}, w, stackReadyWait, stackReadyRetryEvery)
	if err != nil {
		return fmt.Errorf("reach %s: %w", target.URL, err)
	}

	switch status {
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
		// There is no config line, because a push carries no configuration. It
		// used to, and the line reported which KINDS had travelled — which read
		// as though they had landed, and until 2026-08-17 none of them had.
		// Settings are written directly now, by whoever changes them.
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
		return renderPushRefusal(w, status, body)
	}
}

// readableRefusals are the ones whose whole value is the text they carry.
//
// A failing suite's output names the assertion; an incompatible schema names the
// objects to split a migration on. Both are multi-line and both are what the
// person does next — so they are printed as themselves rather than as 300
// characters of escaped JSON, which is what the generic path would show.
var readableRefusals = map[string]bool{
	"tests_failed":                true,
	"tests_timed_out":             true,
	"schema_incompatible":         true,
	"candidate_failed":            true,
	"test_identities_unavailable": true,
}

func renderPushRefusal(w io.Writer, status int, body []byte) error {
	var refusal pushRefusal
	if err := json.Unmarshal(body, &refusal); err == nil && readableRefusals[refusal.Error] {
		fmt.Fprintln(w, refusal.ErrorDescription)
		// The live release is untouched in every one of these, and saying so is
		// the difference between "my deploy failed" and "my site is down".
		return fmt.Errorf("push refused (%s) — nothing was swapped, the previous release keeps serving", refusal.Error)
	}
	return fmt.Errorf("push refused (%d): %s", status, trimBody(body))
}

// sendWaitingForReady sends the request, and treats a 503 as "ask again".
//
// A BRAND-NEW PROJECT IS NOT REFUSING — IT IS STILL COMING UP. 503 "no healthy
// upstream" comes from the cell's edge rather than from the stack, and it means
// exactly what the status code says. Measured on a fresh project (2026-08-22):
// the pod reports Ready and the public route still answers 503 for another ~26
// seconds while the edge converges, and a first push landing in that window
// failed roughly one time in five.
//
// Retrying here is the honest handling rather than a workaround, and two facts
// make it safe. The push is content-addressed, so sending the same tarball twice
// is the same deployment. And the wait is BOUNDED and SAID OUT LOUD, so a
// project that is genuinely unreachable produces a message about that instead of
// a silent loop.
//
// EVERY OTHER STATUS IS ANSWERED ON THE FIRST TRY. A 4xx is a decision — a
// refusal to overwrite data, a rejected key, an unreadable bundle — and asking
// again would only make the same answer slower.
func sendWaitingForReady(
	ctx context.Context,
	client *http.Client,
	newRequest func() (*http.Request, error),
	w io.Writer,
	budget time.Duration,
	every time.Duration,
) (int, []byte, error) {
	deadline := time.Now().Add(budget)
	for attempt := 1; ; attempt++ {
		req, err := newRequest()
		if err != nil {
			return 0, nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		body, rerr := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		_ = res.Body.Close()
		if rerr != nil {
			return 0, nil, rerr
		}

		if res.StatusCode != http.StatusServiceUnavailable || !time.Now().Before(deadline) {
			return res.StatusCode, body, nil
		}
		if attempt == 1 {
			fmt.Fprintf(w, "the project is not serving yet — waiting for it (up to %s)\n", budget)
		}
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-time.After(every):
		}
	}
}
