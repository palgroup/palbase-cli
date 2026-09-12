package backend

// stack_spec.go — keeping the committed client the same age as the stack.
//
// One function, called from three places, because the contract changes at
// exactly three moments: when you link a checkout to a stack, when you sign in
// (the first moment the contract can be fetched at all), and when you push —
// which is the moment it changed. A person who has to remember a fourth command
// after every deploy is a person whose committed client is a version behind, and
// a client a version behind is a compile error at best and a 404 at worst.
//
// The contract comes from the MANAGEMENT surface, with the person's own session.
// It used to come from /admin/openapi.json with the stack's secret key, which
// meant linking an app required holding the credential that can read every
// secret in the stack — to fetch a document that lists routes.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// ErrNotSignedIn says the contract could not be fetched because nobody is signed
// in to this stack yet. A caller decides whether that is a failure: for `link`
// it is a next step, for `spec` it is the answer.
var ErrNotSignedIn = errors.New(
	"this project no longer accepts this session.\n" +
		"For one running on this machine, `palbase start` writes a fresh credential; for a cloud project, `palbase login`")

// RefreshSpec fetches the linked stack's contract and regenerates the client.
//
// Silent about platforms it was not asked for: what runs is read from the
// committed slot files the link wrote, so a fresh clone behaves like the machine
// that linked it.
func RefreshSpec(ctx context.Context, w io.Writer) error {
	return refreshSpec(ctx, w, true)
}

// RefreshSpecAfterPush is the same act as a SIDE EFFECT of `push`, and it stops
// where `link` stops: a checkout with no client generator gets no
// per-environment files.
//
// THIS IS WHERE THE CHURN ACTUALLY CAME FROM. FR-020/021 closed `link`, and
// `link` runs once; `push` runs all day. A backend-only checkout was still
// getting `palbase/environments/main/openapi.json` and `roles.json` rewritten
// on every deploy — the diff on every branch that this whole change exists to
// remove, produced by the verb nobody thought to check. Measured on the
// product: `palbase push` into a fresh `palbase init` checkout, which had just
// been told by `link` that there is "no client app here".
//
// `palbase spec` still writes them unconditionally (FR-023): that verb is
// somebody asking, and an answer they asked for is not churn.
func RefreshSpecAfterPush(ctx context.Context, w io.Writer) error {
	return refreshSpec(ctx, w, false)
}

func refreshSpec(ctx context.Context, w io.Writer, asked bool) error {
	if !asked && !writesPerEnvironmentArtifacts(detectPlatforms(".")) {
		return nil
	}
	// NO BANNER HERE: both callers have already announced where they act —
	// `palbase spec` through `PrintResolvedFor` and `push` through the same,
	// before it hands this function to `finishStackPush`. Announcing a second
	// time mid-run reads as a second destination, which is exactly the defect
	// `push` carried until this run (see RefreshSpecAfterPush and
	// push_banner_test.go).
	//
	// `link` is NOT a caller, whatever an earlier version of this comment said:
	// it writes the contract through `gatherEnvironments`, which applies the
	// same artifact rule for its own reasons.
	// RESOLVED, NOT READ. Which environment's contract this refreshes is the
	// resolver's answer — `--env` has to reach here or `palbase spec` would
	// silently refresh a different environment than the one a person named.
	// This also retires the last constant: `defaultEnvName` returned the
	// literal "main" for every cloud checkout, so two environments shared one
	// directory and the last refresh overwrote the first.
	resolved, err := Resolve(ctx)
	if err != nil {
		return err
	}
	target := resolved.Acting()
	// The resolver's refusal already names both ways in and which address it
	// looked for. Flattening it into the sentinel replaced all of that with four
	// words and left the person to guess.
	cred, _, err := Credential(target.URL)
	if err != nil {
		return err
	}

	spec, err := fetchStackSpec(ctx, target, cred)
	if err != nil {
		return err
	}

	// The environment this checkout is pointed at, by the name the app knows it
	// by — so a refresh updates the contract for THAT configuration and leaves
	// the others alone. Refreshing them all would mean reaching every
	// environment on every push, including production from a laptop.
	env := resolved.ArtifactEnv()
	if target.Local {
		env = localEnvName
	}
	if err := writeSpec(env, spec); err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ wrote %s (%d bytes)\n", specPath(env), len(spec))

	// THE ROLES ARE PART OF THE CONTRACT, so they are fetched by the same act.
	// A generated client learns which roles and permissions exist here and
	// nowhere else (FR-007); fetching them on a separate verb would mean the two
	// documents could sit a deploy apart, and a name the stack no longer defines
	// would go on compiling. refreshRoles never fails the round — see its note.
	if err := refreshRoles(ctx, target, cred, env, w); err != nil {
		return err
	}

	// NO SECOND COPY FOR WEB. `palbe-gen` used to read its own
	// `Palbase/openapi.json` and `Palbase/roles.json` — the same bytes as the
	// per-environment contract, committed twice (measured: 167 KB each, identical
	// sha256). It now reads the environment directory directly, so `spec` writes
	// the contract once and there is nothing to mirror.

	// Apple is the only platform with no build-time generator of its own. Every
	// environment is regenerated, not just the refreshed one: a client missing
	// for a configuration is a build that fails in whichever configuration
	// nobody was using today.
	if _, apple, _ := linkedPlatforms(); apple {
		envs, err := readAppEnvironments("ios")
		if err != nil {
			return err
		}
		if len(envs.Environments) > 0 {
			if err := generateForEnvironments(ctx, envs, w); err != nil {
				return err
			}
			reportStaleContracts(env, envs, w)
		}
	}
	return nil
}

// reportStaleContracts names the environments this refresh did NOT reach.
//
// `spec` asks ONE project — the one this checkout is pointed at — and
// regenerates every environment's client from the contracts on disk. For the
// others that means regenerating from a contract fetched some time ago, and the
// ✓ beside them says "written", which a reader takes for "current". Measured:
// after a route was added to the local stack and `spec` run, the local client
// was byte-identical and carried none of it, while the line above it said the
// file had been written.
//
// So the others are named, with the age of what they were built from, and the
// verb that refreshes them all.
func reportStaleContracts(refreshed string, envs appEnvironments, w io.Writer) {
	var stale []string
	for _, name := range envs.names() {
		if name == refreshed {
			continue
		}
		age := "never fetched"
		if info, err := os.Stat(specPath(name)); err == nil {
			age = "from " + info.ModTime().Local().Format("2006-01-02 15:04")
		}
		stale = append(stale, fmt.Sprintf("%s (%s)", name, age))
	}
	if len(stale) == 0 {
		return
	}
	fmt.Fprintf(w, "\nonly %s was refreshed. The others still describe what they last served:\n  %s\n",
		refreshed, strings.Join(stale, "\n  "))
	fmt.Fprintln(w, "  `palbase link <project>` fetches every environment's contract; `palbase link <ref>` then `palbase spec` refreshes one.")
}

// ErrNoContractYet is the project saying it has no specification to give.
//
// A LEGAL STATE, NOT A FAILURE. A project that has never been pushed to answers
// its contract route with 404 `spec_unavailable`, and so does one whose runtime
// could not build a document from the artifact it holds. Both end the same way
// — someone pushes a backend — and `link` is the verb that makes that push
// possible, because `push` reads the binding `link` writes.
//
// Treating it as a failure closed that loop on itself: link refused because
// nothing was deployed, and nothing could be deployed because the link refused.
// Callers that NEED a contract still refuse; `link` records the state and says
// what ends it.
var ErrNoContractYet = errors.New("no contract yet")

// fetchStackSpec asks the management surface what the stack is serving.
func fetchStackSpec(ctx context.Context, target Target, cred Credentials) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		target.URL+"/v1/management/openapi", nil)
	if err != nil {
		return nil, err
	}
	cred.Apply(req)

	res, err := stackClient(target).Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := readCapped(res.Body, 64<<20, req.URL.String())
	if err != nil {
		return nil, err
	}

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, ErrNotSignedIn
	case http.StatusForbidden:
		return nil, fmt.Errorf(
			"this account may not manage %s — ask whoever runs it for `palsvc --grant-management`", target.URL)
	case http.StatusNotFound:
		// THE PROJECT'S OWN SENTENCE WINS. It answers `spec_unavailable` with a
		// description — "nothing is deployed yet", or the actual reason its
		// runtime could not build a spec — and only that side knows which.
		//
		// Ölçüldü 25.08.2026 (palai-cloud): tek bir `z.lazy` şeması bütün belgeyi
		// düşürmüştü; bu satır ise saniyeler önce başarıyla deploy edilmiş bir
		// projeye "önce bir backend push et" diyordu — yani araç, az önce
		// yapılan şeyin yapılmasını istedi ve teşhis saatler sürdü.
		//
		// Only the ENVELOPE is trusted, never a raw body: a proxy's HTML 404 page
		// is not an explanation and printing it would replace one unhelpful
		// sentence with a worse one.
		var envelope struct {
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &envelope) == nil &&
			strings.TrimSpace(envelope.Description) != "" {
			// THE PROJECT'S SENTENCE, AND THEN WHAT TO DO ABOUT IT.
			//
			// The branch below — the one with LESS information — names the cure;
			// this one, which knows more, used to name none, so the better
			// diagnosis produced the worse message. Every reason a runtime
			// cannot build a spec ends the same way: the code is fixed and
			// pushed. Saying so does not displace the project's own sentence,
			// which is still what a person reads first.
			return nil, fmt.Errorf("%w: %s cannot describe itself: %s — a backend is what "+
				"makes a contract, so this ends with `palbase push`",
				ErrNoContractYet, target.URL, strings.TrimSpace(envelope.Description))
		}
		return nil, fmt.Errorf(
			"%w: %s has nothing to describe yet — push a backend to it first (palbase push)",
			ErrNoContractYet, target.URL)
	default:
		return nil, fmt.Errorf("the stack's contract came back %d: %s", res.StatusCode, trimBody(body))
	}

	// Parsed before it is written: a document that is not JSON would land in a
	// committed file and fail later, in a generator, about a file nobody
	// remembers writing.
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("the stack's contract did not parse: %w", err)
	}
	if len(probe) == 0 {
		return nil, errors.New("the stack answered with an empty contract")
	}
	return body, nil
}

// GetManagementJSON reads a JSON path on the linked project's OWN management
// surface, using whatever credential answers for it.
//
// Exported because commands outside this package need the same door: the
// project's key resolution, its TLS quirks (a stack on this machine may still
// serve its first-boot certificate), and its error envelope all live here. A
// second HTTP path would be a second place for those three things to be
// slightly different.
func GetManagementJSON(ctx context.Context, target Target, path string, out any) error {
	cred, _, err := Credential(target.URL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL+path, nil)
	if err != nil {
		return err
	}
	cred.Apply(req)
	req.Header.Set("Accept", "application/json")

	res, err := stackClient(target).Do(req)
	if err != nil {
		return fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := readCapped(res.Body, 1<<20, req.URL.String())
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var env struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &env) == nil && env.Error != "" {
			return fmt.Errorf("%s: %s — %s", path, env.Error, env.Description)
		}
		return fmt.Errorf("%s: HTTP %d", path, res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}
