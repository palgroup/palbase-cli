package backend

// project_keys.go — asking a project for the key its apps ship.
//
// This used to be a public fact: the project's well-known document carried the
// publishable key, and `link` read it with no credential at all. That was
// convenient and wrong — knowing an address was enough to be handed a working
// client credential, which is enough to sign up, spend rate limit and reach
// whatever row-level security lets an anonymous caller reach.
//
// So the key moved behind the management surface, and linking became something
// you do as somebody. The credential is whatever the resolver finds: the token
// `palbase start` wrote for a stack on this machine, a cloud session, or
// PALBASE_ACCESS_TOKEN in a container.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// projectPublishableKey asks the project for the key an app ships.
// PublishableKey is projectPublishableKey for callers outside this package —
// `palbase test` needs the key it puts in PALBASE_TEST_API_KEY.
func PublishableKey(ctx context.Context, target Target) (string, error) {
	return projectPublishableKey(ctx, target)
}

func projectPublishableKey(ctx context.Context, target Target) (string, error) {
	key, _, err := projectKeys(ctx, target)
	return key, err
}

// projectKeys asks the project for the key an app ships AND the root its
// sealing chain hangs from.
//
// One call for both because an app needs both at the same moment, and because
// the root has nowhere else to come from: a client verifies the chain
// root-first, so a root fetched anonymously from the stack it is meant to
// authenticate would prove nothing. Here the caller already holds a credential
// this stack accepts, which is the same trust that hands over the key.
//
// The root is OPTIONAL. A stack older than the generator has no chain, and the
// answer omits the field rather than sending an empty one — an app configured
// with "" looks configured and fails at the first sealed request.
func projectKeys(ctx context.Context, target Target) (publishable, sealedRoot string, err error) {
	cred, _, err := Credential(target.URL)
	if err != nil {
		return "", "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(target.URL, "/")+"/v1/management/keys", nil)
	if err != nil {
		return "", "", err
	}
	cred.Apply(req)

	res, err := stackClient(target).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		// The credential exists and the project will not take it. Say which
		// project, because the usual cause is a stale one for an address that
		// was rebuilt since.
		return "", "", fmt.Errorf(
			"%s did not accept this credential (%d).\n"+
				"For a project on this machine, `palbase start` writes a fresh one; for a cloud project, `palbase login`;\n"+
				"for a stack you host yourself, `palbase link <url> --token-stdin`",
			target.Describe(), res.StatusCode)
	default:
		return "", "", fmt.Errorf("%s answered %d when asked for its keys: %s",
			target.Describe(), res.StatusCode, trimBody(body))
	}

	var keys struct {
		Publishable string `json:"publishable"`
		SealedRoot  string `json:"sealed_root"`
	}
	if err := json.Unmarshal(body, &keys); err != nil || keys.Publishable == "" {
		return "", "", fmt.Errorf("%s answered without a publishable key: %s", target.Describe(), trimBody(body))
	}
	return keys.Publishable, strings.TrimSpace(keys.SealedRoot), nil
}
