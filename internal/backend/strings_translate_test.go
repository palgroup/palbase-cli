package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type translateRecord struct {
	dest string
	keys []string
}

// stubTranslation replaces the stack: answer computes each batch's reply.
func stubTranslation(t *testing.T, answer func(dest string, keys []string) ([]string, error)) *[]translateRecord {
	t.Helper()
	var calls []translateRecord
	prevTarget, prevCall := translateTargetFn, translateCallFn
	translateTargetFn = func(context.Context) (Target, Credentials, error) {
		return Target{URL: "https://stack.example"}, Credentials{Value: "svc", Kind: KindKey}, nil
	}
	translateCallFn = func(_ context.Context, _ Target, _ Credentials, source, dest string, keys []string) ([]string, error) {
		if source != "tr" {
			t.Errorf("source = %q, want tr", source)
		}
		calls = append(calls, translateRecord{dest: dest, keys: append([]string(nil), keys...)})
		return answer(dest, keys)
	}
	t.Cleanup(func() { translateTargetFn, translateCallFn = prevTarget, prevCall })
	return &calls
}

func tableWith(locales []string, keys ...string) *stringsTable {
	tab := &stringsTable{Version: 1, Source: "tr", Locales: locales, Strings: map[string]map[string]stringCell{}}
	for _, k := range keys {
		row := map[string]stringCell{}
		for _, loc := range locales[1:] {
			row[loc] = stringCell{State: cellMissing}
		}
		tab.Strings[k] = row
	}
	return tab
}

func TestTranslateMissing_WritesNeedsReviewBatchByBatch(t *testing.T) { // FR-017, FR-019
	dir := t.TempDir()
	calls := stubTranslation(t, func(dest string, keys []string) ([]string, error) {
		out := make([]string, len(keys))
		for i, k := range keys {
			out[i] = "[" + dest + "] " + k
		}
		return out, nil
	})
	tab := tableWith([]string{"tr", "de", "en"}, "Kart {{amount}} ₺", "Merhaba")
	var out bytes.Buffer
	require.NoError(t, translateMissing(context.Background(), dir, tab, &out))
	require.Len(t, *calls, 2)
	require.Equal(t, "de", (*calls)[0].dest, "languages in tag order")
	require.Equal(t, []string{"Kart {{amount}} ₺", "Merhaba"}, (*calls)[0].keys, "keys in byte order")
	en := readRel(t, dir, "palbase/strings/en.json")
	require.Contains(t, en, "\"value\": \"[en] Kart {{amount}} ₺\",\n    \"state\": \"needs_review\"")
	require.Contains(t, out.String(), "✓ translated 2 string(s) into de — written as needs_review in palbase/strings/de.json")
	require.Contains(t, out.String(), "✓ translated 2 string(s) into en — written as needs_review in palbase/strings/en.json")
}

func TestTranslateMissing_TheGateKeepsABadTranslationMissing(t *testing.T) { // FR-018, D-10
	dir := t.TempDir()
	stubTranslation(t, func(_ string, keys []string) ([]string, error) {
		out := make([]string, len(keys))
		for i, k := range keys {
			switch k {
			case "Kart {{amount}} ₺":
				out[i] = "Card ₺"
			case "Boş":
				out[i] = "  "
			case "Ad":
				out[i] = "Name {{extra}}"
			default:
				out[i] = "ok " + k
			}
		}
		return out, nil
	})
	tab := tableWith([]string{"tr", "en"}, "Kart {{amount}} ₺", "Boş", "Ad", "Merhaba")
	var out bytes.Buffer
	require.NoError(t, translateMissing(context.Background(), dir, tab, &out), "a refused translation is a result, not a failure (D-14)")
	s := out.String()
	require.Contains(t, s, `! en: "Kart {{amount}} ₺" was not written — the translation drops {{amount}}`)
	require.Contains(t, s, `! en: "Boş" was not written — the translation is empty`)
	require.Contains(t, s, `! en: "Ad" was not written — the translation adds {{extra}}`)
	require.Contains(t, s, "✓ translated 1 string(s) into en")
	require.Equal(t, stringCell{State: cellMissing}, tab.Strings["Kart {{amount}} ₺"]["en"])
	require.Equal(t, stringCell{Value: "ok Merhaba", State: cellNeedsReview}, tab.Strings["Merhaba"]["en"])
}

func TestTranslateMissing_AFailureStopsAndKeepsWhatLanded(t *testing.T) { // FR-019, FR-020
	dir := t.TempDir()
	keys := make([]string, 51)
	for i := range keys {
		keys[i] = fmt.Sprintf("Cümle %02d", i)
	}
	n := 0
	calls := stubTranslation(t, func(_ string, batch []string) ([]string, error) {
		n++
		if n == 2 {
			return nil, errors.New("the provider answered 500")
		}
		out := make([]string, len(batch))
		for i, k := range batch {
			out[i] = "T " + k
		}
		return out, nil
	})
	tab := tableWith([]string{"tr", "en", "fr"}, keys...)
	var out bytes.Buffer
	err := translateMissing(context.Background(), dir, tab, &out)
	require.Error(t, err)
	require.Len(t, *calls, 2, "no batch is sent after a failure")
	require.Contains(t, out.String(), "✗ translation stopped: the provider answered 500")
	require.Contains(t, out.String(), "  52 string(s) are still missing")
	en := readRel(t, dir, "palbase/strings/en.json")
	require.Equal(t, 50, strings.Count(en, `"needs_review"`), "the first batch was written before the second was sent")
}

func TestTranslateMissing_NothingToTranslateSendsNothing(t *testing.T) { // FR-021
	calls := stubTranslation(t, func(string, []string) ([]string, error) { return nil, errors.New("not called") })
	resolved := false
	translateTargetFn = func(context.Context) (Target, Credentials, error) {
		resolved = true
		return Target{}, Credentials{}, nil
	}
	var out bytes.Buffer
	full := tableWith([]string{"tr", "en"}, "Merhaba")
	full.Strings["Merhaba"]["en"] = stringCell{Value: "Hello", State: cellTranslated}
	require.NoError(t, translateMissing(context.Background(), t.TempDir(), full, &out))
	require.Contains(t, out.String(), "✓ nothing to translate — every language in palbase/strings/ is filled in")
	out.Reset()
	require.NoError(t, translateMissing(context.Background(), t.TempDir(), tableWith([]string{"tr"}, "Merhaba"), &out))
	require.Contains(t, out.String(), "✓ nothing to translate — palbase/strings/ has no language besides tr; add one with: palbase build --add <language>")
	require.Empty(t, *calls)
	require.False(t, resolved, "no stack is asked when there is nothing to send")
}

func TestTranslateMissing_AnUnlinkedCheckoutFails(t *testing.T) { // FR-020
	stubTranslation(t, func(string, []string) ([]string, error) { return nil, nil })
	translateTargetFn = func(context.Context) (Target, Credentials, error) {
		return Target{}, Credentials{}, errors.New("this checkout is not linked to a stack")
	}
	var out bytes.Buffer
	require.Error(t, translateMissing(context.Background(), t.TempDir(), tableWith([]string{"tr", "en"}, "Merhaba"), &out))
	require.Contains(t, out.String(), "✗ translation stopped: this checkout is not linked to a stack")
	require.Contains(t, out.String(), "  1 string(s) are still missing", "FR-020: C-8 row 9, like every stack failure")
}

func TestTranslateMissing_AnOverlongSentenceIsNamedAndNotSent(t *testing.T) { // D-12
	long := strings.Repeat("ş", 40<<10)
	calls := stubTranslation(t, func(_ string, keys []string) ([]string, error) { return keys, nil })
	var out bytes.Buffer
	require.NoError(t, translateMissing(context.Background(), t.TempDir(), tableWith([]string{"tr", "en"}, long, "Merhaba"), &out))
	require.Contains(t, out.String(), "was not written — the sentence is over 64 KiB and cannot be sent")
	// A 64 KiB sentence printed whole is not a diagnostic line, it is the file
	// again (bitiş incelemesi M4): name it by its head and its size.
	require.Less(t, out.Len(), 1<<10, "teşhis satırı cümlenin tamamını basmaz")
	require.Contains(t, out.String(), "…")
	for _, c := range *calls {
		for _, k := range c.keys {
			require.NotEqual(t, long, k)
		}
	}
}

func TestTranslateBatches(t *testing.T) { // D-12
	keys := make([]string, 120)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%03d", i)
	}
	sizes := []int{}
	for _, b := range translateBatches(keys) {
		sizes = append(sizes, len(b))
	}
	require.Equal(t, []int{50, 50, 20}, sizes)
	big := strings.Repeat("x", 20<<10)
	sizes = sizes[:0]
	for _, b := range translateBatches([]string{big, big, "small"}) {
		sizes = append(sizes, len(b))
	}
	require.Equal(t, []int{1, 2}, sizes, "a batch closes before it crosses 32 KiB")
}

func TestTranslateCall_SpeaksTheStacksContract(t *testing.T) { // C-5, FR-020
	var seen struct {
		path, apikey string
		body         map[string]any
	}
	reply := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	var respond func(w http.ResponseWriter)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.path, seen.apikey = r.Method+" "+r.URL.Path, r.Header.Get("apikey")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seen.body)
		respond(w)
	}))
	defer srv.Close()
	target, cred := Target{URL: srv.URL}, Credentials{Value: "svc", Kind: KindKey}
	ctx := context.Background()

	respond = func(w http.ResponseWriter) { reply(w, 200, `{"model":"gpt-5.6-luna","translations":["Hello","Bye"]}`) }
	got, err := translateCall(ctx, target, cred, "tr", "en", []string{"Merhaba", "Hoşça kal"})
	require.NoError(t, err)
	require.Equal(t, []string{"Hello", "Bye"}, got)
	require.Equal(t, "POST /v1/management/strings/translate", seen.path)
	require.Equal(t, "svc", seen.apikey)
	require.Equal(t, map[string]any{"source": "tr", "target": "en", "strings": []any{"Merhaba", "Hoşça kal"}}, seen.body)

	respond = func(w http.ResponseWriter) {
		reply(w, 422, `{"error":"missing_openai_key","error_description":"this project's vault holds no OPENAI_API_KEY","status":422}`)
	}
	_, err = translateCall(ctx, target, cred, "tr", "en", []string{"Merhaba"})
	require.ErrorContains(t, err, "palbase secret set OPENAI_API_KEY --stdin")

	respond = func(w http.ResponseWriter) { reply(w, 404, `404 page not found`) }
	_, err = translateCall(ctx, target, cred, "tr", "en", []string{"Merhaba"})
	require.ErrorContains(t, err, "predates palbase build --translate")

	respond = func(w http.ResponseWriter) { reply(w, 200, `{"model":"m","translations":["only one"]}`) }
	_, err = translateCall(ctx, target, cred, "tr", "en", []string{"Bir", "İki"})
	require.ErrorContains(t, err, "answered 1 translation(s) for 2 sentence(s)")

	respond = func(w http.ResponseWriter) {
		reply(w, 429, `{"error":"rate_limited","error_description":"the provider answered 429 rate_limit_exceeded","status":429,"retry_after":3}`)
	}
	_, err = translateCall(ctx, target, cred, "tr", "en", []string{"Merhaba"})
	require.ErrorContains(t, err, "the provider answered 429")
}
