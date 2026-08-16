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
func projectPublishableKey(ctx context.Context, target Target) (string, error) {
	token, _, err := Credential(target.URL)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(target.URL, "/")+"/v1/management/keys", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := stackClient(target).Do(req)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		// The credential exists and the project will not take it. Say which
		// project, because the usual cause is a stale one for an address that
		// was rebuilt since.
		return "", fmt.Errorf(
			"%s did not accept this credential (%d).\n"+
				"For a project on this machine, `palbase start` writes a fresh one; for a cloud project, `palbase login`",
			target.Describe(), res.StatusCode)
	default:
		return "", fmt.Errorf("%s answered %d when asked for its keys: %s",
			target.Describe(), res.StatusCode, trimBody(body))
	}

	var keys struct {
		Publishable string `json:"publishable"`
	}
	if err := json.Unmarshal(body, &keys); err != nil || keys.Publishable == "" {
		return "", fmt.Errorf("%s answered without a publishable key: %s", target.Describe(), trimBody(body))
	}
	return keys.Publishable, nil
}
