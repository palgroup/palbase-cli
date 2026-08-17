package backend

// start_secrets.go — the local stack starts with the secrets its project has.
//
// Without this, the first thing anybody does after `palbase start` is set every
// credential again by hand, and the second thing is discover that they typed one
// of them wrong. The values move VAULT TO VAULT: read from the linked project,
// written sealed into the local one, never through a file and never through the
// terminal.
//
// What it must not do is undo local work. Somebody who pointed SENTRY_DSN at a
// throwaway project so their dev traffic stops paging the team has made a
// deliberate change, and a start that overwrote it every morning would be a tool
// people learn to work around. So the last pulled value is remembered by HASH —
// not by value, which would be a third copy of every secret on disk — and a key
// whose local value has moved since is kept.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// pullSecrets copies the linked project's secrets into the local stack.
//
// It never fails a start: the stack is up and usable either way, and a project
// that cannot be reached right now is a reason to say so, not to refuse the
// thing that already worked.
func pullSecrets(ctx context.Context, group string, local Target, out io.Writer) {
	source, err := readLinkedProject()
	if err != nil || source.Project == "" {
		// NOTHING TO PULL FROM, and this is the ordinary case today rather than
		// an edge one: a checkout linked to an ADDRESS is pointed at one stack,
		// and that stack is the one that just started. The source of a pull is a
		// project GROUP with other environments in it, which is a cloud
		// concept — `Target.Project` is written by the cloud link, and until
		// that lands nothing in production sets it.
		//
		// Said rather than skipped: a feature that quietly does nothing is a
		// feature nobody can tell is missing.
		if err == nil && source.URL != "" {
			fmt.Fprintln(out, "  secrets: this checkout is linked to an address, so there is no environment to pull from")
		}
		return
	}
	sourceCred, _, err := Credential(source.URL)
	if err != nil {
		fmt.Fprintf(out, "  secrets: not pulled — no credential for %s\n", source.Describe())
		return
	}
	localCred, _, err := Credential(local.URL)
	if err != nil {
		return
	}

	names, err := secretNames(ctx, source, sourceCred)
	if err != nil {
		fmt.Fprintf(out, "  secrets: not pulled — %v\n", err)
		return
	}
	if len(names) == 0 {
		return
	}

	seen, err := readPulled(group)
	if err != nil {
		fmt.Fprintf(out, "  secrets: not pulled — %v\n", err)
		return
	}

	var pulled, kept int
	for _, name := range names {
		value, err := secretValue(ctx, source, sourceCred, name)
		if err != nil {
			fmt.Fprintf(out, "  secrets: %s not pulled — %v\n", name, err)
			continue
		}
		if changedLocally(ctx, local, localCred, name, seen[name]) {
			kept++
			continue
		}
		if err := putSecret(ctx, local, localCred, name, value); err != nil {
			fmt.Fprintf(out, "  secrets: %s not written — %v\n", name, err)
			continue
		}
		seen[name] = hashOf(value)
		pulled++
	}
	if err := writePulled(group, seen); err != nil {
		fmt.Fprintf(out, "  secrets: the pull record was not saved — %v\n", err)
	}

	// The names are not printed, let alone the values: the count is what an
	// operator needs, and a list of every credential a project holds is a list
	// worth reading over somebody's shoulder.
	line := fmt.Sprintf("  secrets: %d pulled", pulled)
	if kept > 0 {
		line += fmt.Sprintf(" · %d kept (changed here since)", kept)
	}
	fmt.Fprintln(out, line)
}

// changedLocally reports whether this stack's copy has moved since it was pulled.
//
// A name the local stack does not hold has not been changed — it has never been
// set — so it is pulled. A name whose value no longer matches the recorded hash
// was edited here, and is kept.
func changedLocally(ctx context.Context, local Target, cred Credentials, name, pulledHash string) bool {
	if pulledHash == "" {
		return false
	}
	current, err := secretValue(ctx, local, cred, name)
	if err != nil {
		// COULD NOT READ IS NOT UNCHANGED. This answers "may I overwrite it",
		// and the honest answer when the stack is restarting, or answers 500, or
		// has not materialised that name yet, is no. Saying "unchanged" here
		// overwrote exactly the value this function exists to protect.
		return true
	}
	return hashOf(current) != pulledHash
}

func secretNames(ctx context.Context, target Target, cred Credentials) ([]string, error) {
	status, body, err := managementCall(ctx, target, cred, http.MethodGet, "/v1/management/secrets", nil, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d when asked for its secrets", target.Describe(), status)
	}
	var answer struct {
		Secrets []struct {
			Name string `json:"name"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(answer.Secrets))
	for _, s := range answer.Secrets {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names, nil
}

func secretValue(ctx context.Context, target Target, cred Credentials, name string) (string, error) {
	status, body, err := managementCall(ctx, target, cred, http.MethodGet,
		"/v1/management/secrets/"+url.PathEscape(name)+"/value", nil, "")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s answered %d", target.Describe(), status)
	}
	return string(body), nil
}

func putSecret(ctx context.Context, target Target, cred Credentials, name, value string) error {
	body, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return err
	}
	status, raw, err := managementCall(ctx, target, cred, http.MethodPut,
		"/v1/management/secrets/"+url.PathEscape(name), body, "application/json")
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent && status != http.StatusCreated {
		return fmt.Errorf("%d: %s", status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// managementCall is one request to a project's management surface.
func managementCall(ctx context.Context, target Target, cred Credentials, method, path string, body []byte, contentType string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(target.URL, "/")+path, reader)
	if err != nil {
		return 0, nil, err
	}
	cred.Apply(req)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := stackClient(target)
	client.Timeout = 30 * time.Second

	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, raw, err
}

func hashOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// readPulled/writePulled keep the record of what was last pulled, beside the
// stack's other machine-local state. Hashes only: a file of values would be the
// dotenv this design removed, under a different name.
func pulledPath(group string) (string, error) {
	dir, err := stackStateDir(group)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pulled-secrets.json"), nil
}

func readPulled(group string) (map[string]string, error) {
	path, err := pulledPath(group)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]string{}
	if err := json.Unmarshal(raw, &seen); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return seen, nil
}

func writePulled(group string, seen map[string]string) error {
	path, err := pulledPath(group)
	if err != nil {
		return err
	}
	blob, err := json.MarshalIndent(seen, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(blob, '\n'), 0o600)
}
