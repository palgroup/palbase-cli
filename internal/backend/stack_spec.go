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
	"path/filepath"
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
	// No banner here: RefreshSpec runs INSIDE `push` and `link`, which have
	// already said where they are acting. Announcing it a second time mid-run
	// reads as a second destination.
	target, err := ReadTarget()
	if err != nil {
		return err
	}
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

	web, apple, android := linkedPlatforms()
	dirs := []string{}
	if web {
		dirs = append(dirs, webArtifactsDir)
	}
	if apple || android {
		dirs = append(dirs, nativeArtifactsDir)
	}
	if len(dirs) == 0 {
		// Linked to a stack but no app platform chosen — a backend-only
		// checkout. The contract still belongs on disk: it is what `palbase
		// spec` would have written, and what a client generator will read the
		// day one is added.
		dirs = append(dirs, nativeArtifactsDir)
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		path := filepath.Join(dir, "openapi.json")
		if err := os.WriteFile(path, spec, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(w, "✓ wrote %s (%d bytes)\n", path, len(spec))
	}

	// Apple is the only platform with no build-time generator of its own.
	if apple {
		return generateAppleClient(nativeArtifactsDir, w)
	}
	return nil
}

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
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
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
		return nil, fmt.Errorf(
			"%s has nothing to describe yet — push a backend to it first (palbase push)", target.URL)
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
