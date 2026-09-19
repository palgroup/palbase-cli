package backend

// strings_translate.go — `palbase build --translate`, the CLI half (C-7).
//
// The CLI never holds the OpenAI key (D-7): it sends the sentences whose cell
// is `missing` to the linked stack's translate door, which calls the provider
// with the project's own vault key and answers with the translations. What
// lands in the file is decided HERE — the placeholder gate (D-10) — and it
// lands as needs_review (J-49), batch by batch (FR-019): a later failure never
// takes back a translation somebody already paid for.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	translatePath = "/v1/management/strings/translate"
	// translateTimeout, D-13: longer than the stack's 120 s budget, shorter
	// than the cloud edge's 240 s management route.
	translateTimeout = 180 * time.Second
	// D-12: a batch is at most this many sentences and this many bytes; the
	// stack's own ceiling is 100 and 64 KiB.
	translateBatchKeys   = 50
	translateBatchBytes  = 32 << 10
	translateMaxKeyBytes = 64 << 10
)

// translateTargetFn resolves the stack and its credential once per build — a
// seam, because the flow is measured without a real link.
var translateTargetFn = func(ctx context.Context) (Target, Credentials, error) {
	resolved, err := Resolve(ctx)
	if err != nil {
		return Target{}, Credentials{}, errors.New("this checkout is not linked to a stack, and translation runs on the stack — link it first: palbase link <url>")
	}
	target := resolved.Acting()
	cred, _, err := credentialFn(target.URL)
	if err != nil {
		return Target{}, Credentials{}, fmt.Errorf("no credential for %s", target.Describe())
	}
	return target, cred, nil
}

// translateCallFn is the seam one batch is measured through.
var translateCallFn = translateCall

// translateCall sends one batch and returns the translations in the order the
// sentences were sent.
func translateCall(ctx context.Context, target Target, cred Credentials, source, dest string, keys []string) ([]string, error) {
	body, err := json.Marshal(map[string]any{"source": source, "target": dest, "strings": keys})
	if err != nil {
		return nil, err
	}
	status, raw, err := managementCallWithin(ctx, target, cred, http.MethodPost, translatePath, body, "application/json", translateTimeout)
	if err != nil {
		return nil, fmt.Errorf("the stack could not be reached: %w", err)
	}
	switch status {
	case http.StatusOK:
		var answer struct {
			Translations []string `json:"translations"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			return nil, fmt.Errorf("the stack's answer is not a translation: %w", err)
		}
		if len(answer.Translations) != len(keys) {
			return nil, fmt.Errorf("the stack answered %d translation(s) for %d sentence(s)", len(answer.Translations), len(keys))
		}
		return answer.Translations, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("this stack has no translation door (404 on %s) — it predates palbase build --translate; update the stack", translatePath)
	}
	var refusal struct {
		Code        string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &refusal)
	msg := refusal.Description
	if msg == "" {
		msg = fmt.Sprintf("%d: %s", status, strings.TrimSpace(string(raw)))
	}
	if refusal.Code == "missing_openai_key" && !strings.Contains(msg, "palbase secret set") {
		msg += " — set it with: palbase secret set OPENAI_API_KEY --stdin"
	}
	return nil, errors.New(msg)
}

// translateBatches cuts one language's sentences into D-12's batches, in the
// order given. A sentence over translateBatchBytes travels alone.
func translateBatches(keys []string) [][]string {
	var batches [][]string
	var cur []string
	size := 0
	for _, k := range keys {
		if len(cur) > 0 && (len(cur) == translateBatchKeys || size+len(k) > translateBatchBytes) {
			batches = append(batches, cur)
			cur, size = nil, 0
		}
		cur = append(cur, k)
		size += len(k)
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// translateMissing is FR-017..FR-021: every `missing` cell of every language,
// language by language in tag order, batch by batch, each batch written before
// the next is sent.
func translateMissing(ctx context.Context, cwd string, t *stringsTable, out io.Writer) error {
	if len(t.Locales) <= 1 {
		fmt.Fprintf(out, "✓ nothing to translate — %s/ has no language besides %s; add one with: palbase build --add <language>\n", StringsDir(), t.Source)
		return nil
	}
	type pending struct {
		loc  string
		keys []string
	}
	var work []pending
	total := 0
	for _, loc := range t.Locales {
		if loc == t.Source {
			continue
		}
		var keys []string
		for key, row := range t.Strings {
			if c, ok := row[loc]; !ok || c.State == cellMissing {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			work = append(work, pending{loc: loc, keys: keys})
			total += len(keys)
		}
	}
	if total == 0 {
		fmt.Fprintf(out, "✓ nothing to translate — every language in %s/ is filled in\n", StringsDir())
		return nil
	}
	target, cred, err := translateTargetFn(ctx)
	if err != nil {
		// FR-020 names "not linked" and "no credential" among the request
		// failures: C-8 row 9, with what is still missing.
		fmt.Fprintf(out, "✗ translation stopped: %v\n", err)
		fmt.Fprintf(out, "  %d string(s) are still missing\n", total)
		return err
	}
	landed := 0
	for _, p := range work {
		written := 0
		for _, batch := range translateBatches(p.keys) {
			if len(batch) == 1 && len(batch[0]) > translateMaxKeyBytes {
				fmt.Fprintf(out, "! %s: %q was not written — the sentence is over 64 KiB and cannot be sent\n", p.loc, batch[0])
				continue
			}
			texts, err := translateCallFn(ctx, target, cred, t.Source, p.loc, batch)
			if err != nil {
				fmt.Fprintf(out, "✗ translation stopped: %v\n", err)
				fmt.Fprintf(out, "  %d string(s) are still missing\n", total-landed)
				return err
			}
			for i, key := range batch {
				text := texts[i]
				dropped, added := placeholderDiff(key, text)
				reason := ""
				switch {
				case strings.TrimSpace(text) == "":
					reason = "the translation is empty"
				case len(dropped) > 0:
					reason = "the translation drops " + braces(dropped)
				case len(added) > 0:
					reason = "the translation adds " + braces(added)
				}
				if reason != "" {
					fmt.Fprintf(out, "! %s: %q was not written — %s\n", p.loc, key, reason)
					continue
				}
				t.Strings[key][p.loc] = stringCell{Value: text, State: cellNeedsReview}
				written++
				landed++
			}
			if _, err := writeTableDir(cwd, t); err != nil {
				fmt.Fprintf(out, "✗ translation stopped: %v\n", err)
				fmt.Fprintf(out, "  %d string(s) are still missing\n", total-landed)
				return err
			}
		}
		if written > 0 {
			fmt.Fprintf(out, "✓ translated %d string(s) into %s — written as needs_review in %s/%s.json\n", written, p.loc, StringsDir(), p.loc)
		}
	}
	return nil
}
