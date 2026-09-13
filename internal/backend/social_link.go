package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/palgroup/palbase-cli/internal/authcontract"
	"github.com/palgroup/palbase-cli/internal/sealedclient"
)

// socialSnapshotLimit bounds the auth snapshot this reads.
//
// The number is the one this call site already used; only the checking is new.
// It is the realistic overflow of the set — a project with several providers,
// each with several clients and their redirect URIs — and the site was already
// reading `limit+1` with nothing looking at the extra byte, which is the shape
// of a check somebody meant to write.
const socialSnapshotLimit = 256 * 1024

type OAuthSelection struct {
	ApplicationKey string   `json:"application_key"`
	Variant        string   `json:"variant"`
	ClientKeys     []string `json:"client_keys,omitempty"`
}

// sealing is what a request needs BEYOND itself in order to be sealed: which
// stack the address is (from the publishable key), and which root vouches for
// that stack's sealing key when it is one an operator hosts themselves.
//
// It is carried rather than derived here because both facts already travel with
// the environment an app is being linked against — the key it ships and the
// `sealed_root` the stack handed over at link time
// (internal/backend/app_environments.go:52).
type sealing struct {
	apiKey     string
	sealedRoot string
}

// do sends one request to a stack, sealed when the server refuses plaintext on
// that path and plain when it does not.
//
// ONE RULE, ASKED TWICE, NEVER WRITTEN TWICE. `sealedclient.Required` is the
// CLI's only copy of the server's `sealedRequired`
// (v2/internal/server/sealed.go:365) and `Client.Do` asks it too. It is asked
// here as well only because building a sealing client requires knowing WHICH
// stack this is, and the operator-facing read this function also serves
// (`/v1/management/auth/social-auth`) carries a session token rather than a
// publishable key — there is no ref in it to identify a stack with, and none is
// needed, because that path is not under a sealed prefix.
func (s sealing) do(target Target, client *http.Client, req *http.Request) (*http.Response, error) {
	if !sealedclient.Required(req.URL.Path) {
		return client.Do(req)
	}
	sealed, err := sealedclient.New(sealedclient.Config{
		BaseURL:      target.URL,
		APIKey:       s.apiKey,
		SelfHostRoot: s.sealedRoot,
		HTTPClient:   client,
	})
	if err != nil {
		return nil, err
	}
	return sealed.Do(req)
}

func socialRequest(ctx context.Context, target Target, path string, cred Credentials, seal sealing) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(target.URL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	cred.Apply(req)
	req.Header.Set("Palbase-Auth-Contract", "1")
	client := *stackClient(target)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := seal.do(target, &client, req)
	if err != nil {
		return nil, fmt.Errorf("read social auth config from %s: %w", target.URL, err)
	}
	defer res.Body.Close()
	raw, err := readCapped(res.Body, socialSnapshotLimit, req.URL.String())
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		var problem struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &problem)
		return nil, fmt.Errorf("social auth config from %s: HTTP %d %s %s", target.URL, res.StatusCode, problem.Error, problem.Description)
	}
	if res.Header.Get("Palbase-Auth-Contract") != "1" {
		return nil, fmt.Errorf("%s did not confirm social auth contract 1", target.URL)
	}
	return raw, nil
}

// Resolve only an explicit binding or an unambiguous native identifier match.
// A sole web client in another application is not evidence that it is ours.
func linkedOAuth(ctx context.Context, target Target, platform, publishableKey, sealedRoot string, selection OAuthSelection) (*authcontract.SocialSnapshot, OAuthSelection, error) {
	cred, _, err := Credential(target.URL)
	if err != nil {
		return nil, selection, err
	}
	// The snapshot read below is under `/auth/`, which refuses a plaintext
	// request; the admin read is under `/v1/management/`, which does not. Both
	// go through the same function and the same rule decides.
	seal := sealing{apiKey: publishableKey, sealedRoot: sealedRoot}
	admin, err := socialRequest(ctx, target, "/v1/management/auth/social-auth", cred, seal)
	if err != nil {
		return nil, selection, err
	}
	if err := authcontract.Validate("SocialAdminConfig", admin); err != nil {
		return nil, selection, fmt.Errorf("invalid social auth config from %s: %w", target.URL, err)
	}
	var doc struct {
		Providers map[string]struct {
			Enabled        bool
			BrowserClients []json.RawMessage `json:"browser_clients"`
			NativeClients  []json.RawMessage `json:"native_clients"`
		}
	}
	if err := json.Unmarshal(admin, &doc); err != nil {
		return nil, selection, err
	}
	type candidate struct {
		Enabled        bool
		ApplicationKey string `json:"application_key"`
		Platform       string
		Variant        string
		BundleID       string `json:"bundle_id"`
		PackageName    string `json:"package_name"`
	}
	var available []candidate
	for _, provider := range doc.Providers {
		if !provider.Enabled {
			continue
		}
		for _, raw := range append(provider.BrowserClients, provider.NativeClients...) {
			var c candidate
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, selection, err
			}
			if c.Enabled && c.Platform == platform {
				available = append(available, c)
			}
		}
	}
	if selection.ApplicationKey == "" || selection.Variant == "" {
		if len(available) == 0 && selection.ApplicationKey == "" && selection.Variant == "" {
			return nil, selection, nil
		}
		identifiers := nativeIdentifiers(platform)
		matches := map[string]OAuthSelection{}
		for _, c := range available {
			identifier := c.BundleID
			if platform == "android" {
				identifier = c.PackageName
			}
			if identifier != "" && slices.Contains(identifiers, identifier) {
				matches[c.ApplicationKey+"/"+c.Variant] = OAuthSelection{ApplicationKey: c.ApplicationKey, Variant: c.Variant}
			}
		}
		if selection.ApplicationKey == "" && selection.Variant == "" && len(matches) == 1 {
			for _, match := range matches {
				selection = match
			}
		} else {
			return nil, selection, fmt.Errorf("social sign-in for %s needs application_key and variant in %s oauth.%s; "+
				"the checkout does not identify one configured target", platform, projectPath(), platform)
		}
	}
	query := url.Values{"application_key": {selection.ApplicationKey}, "platform": {platform}, "variant": {selection.Variant}}
	for _, key := range selection.ClientKeys {
		query.Add("client_key", key)
	}
	raw, err := socialRequest(ctx, target, "/auth/oauth/config?"+query.Encode(), Credentials{Kind: KindKey, Value: publishableKey}, seal)
	if err != nil {
		return nil, selection, err
	}
	if err := authcontract.Validate("SocialSnapshot", raw); err != nil {
		return nil, selection, fmt.Errorf("invalid %s auth snapshot: %w", platform, err)
	}
	var snapshot authcontract.SocialSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, selection, err
	}
	if snapshot.ApplicationKey != selection.ApplicationKey || snapshot.Variant != selection.Variant || string(snapshot.Platform) != platform {
		return nil, selection, fmt.Errorf("the stack returned an auth snapshot for a different application, platform or variant")
	}
	// A SELECTION THAT DOES NOT FIT THIS ENVIRONMENT. No client for the selected
	// application and variant, while this environment configures the app under
	// another pair: written, the app would lose its sign-in client with nothing
	// said.
	if len(snapshot.Clients) == 0 {
		identifiers := nativeIdentifiers(platform)
		others := map[string]bool{}
		for _, c := range available {
			identifier := c.BundleID
			if platform == "android" {
				identifier = c.PackageName
			}
			if identifier != "" && slices.Contains(identifiers, identifier) &&
				(c.ApplicationKey != selection.ApplicationKey || c.Variant != selection.Variant) {
				others[fmt.Sprintf("%q variant %q", c.ApplicationKey, c.Variant)] = true
			}
		}
		if len(others) > 0 {
			pairs := make([]string, 0, len(others))
			for pair := range others {
				pairs = append(pairs, pair)
			}
			slices.Sort(pairs)
			return nil, selection, selectionDoesNotFit{detail: fmt.Sprintf(
				"the selected application %q variant %q has no %s client here, and this environment configures the app as %s — "+
					"configure the app under the selected pair in this environment, or change oauth.%s in %s",
				selection.ApplicationKey, selection.Variant, platform, strings.Join(pairs, ", "), platform, projectPath())}
		}
	}
	return &snapshot, selection, nil
}

var appleIdentifier = regexp.MustCompile(`PRODUCT_BUNDLE_IDENTIFIER\s*=\s*"?([A-Za-z0-9.-]+)`)
var androidVariantConfiguration = regexp.MustCompile(`\b(?:applicationIdSuffix|productFlavors)\b`)

func nativeIdentifiers(platform string) []string {
	if platform == "android" {
		root, _ := os.Getwd()
		var identifiers []string
		for _, path := range []string{"app/build.gradle.kts", "app/build.gradle", "build.gradle.kts", "build.gradle"} {
			raw, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				continue
			}
			// A literal defaultConfig ID does not identify the final application
			// when Gradle applies flavors or build-type suffixes. Require the
			// explicit OAuth selection; the Gradle plugin checks the final ID.
			if androidVariantConfiguration.Match(raw) {
				return nil
			}
			for _, match := range androidApplicationIDPattern.FindAllSubmatch(raw, -1) {
				identifiers = append(identifiers, string(match[1]))
			}
		}
		if len(identifiers) == 1 && !strings.Contains(identifiers[0], "$") {
			return identifiers
		}
		return nil
	}
	if !isApplePlatform(platform) {
		return nil
	}
	paths, _ := filepath.Glob("*.xcodeproj/project.pbxproj")
	values := []string{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, match := range appleIdentifier.FindAllSubmatch(raw, -1) {
			value := string(match[1])
			if !slices.Contains(values, value) {
				values = append(values, value)
			}
		}
	}
	return values
}

func platformEnvironments(ctx context.Context, target *Target, platform string, source appEnvironments) (appEnvironments, map[string]error, error) {
	result := appEnvironments{Default: source.Default, Environments: map[string]appEnvironment{}}
	dropped := map[string]error{}
	// ONE SELECTION PER CHECKOUT: the committed one or, when nothing is
	// committed, the one the DEFAULT environment's read learns. The default is
	// read first and every other environment with that selection, so the first
	// link writes what every later link writes. Each environment learning its
	// own used to record the default's and read the others with it the NEXT
	// time: their sign-in clients vanished on the second link.
	readWith := target.OAuth[platform]
	var learned *OAuthSelection
	for _, name := range defaultFirst(source) {
		env := source.Environments[name]
		if env.APIKey == "" {
			if name != source.Default {
				// The existing optional local slot is unavailable until start.
				// It carries no auth snapshot and cannot be selected by a build.
				env.OAuth = nil
				result.Environments[name] = env
				continue
			}
			return result, dropped, fmt.Errorf("%s: cannot refresh complete platform config while the environment is unavailable", name)
		}
		// A NON-DEFAULT ENVIRONMENT THAT CANNOT BE READ IS DROPPED, NOT FATAL.
		// One link describes every environment now; a social-auth read that
		// fails for staging must not take down the link of main. The default
		// environment is what a build without a choice talks to, so for it the
		// failure still fails the link.
		drop := func(reason error) bool {
			if name == source.Default {
				return false
			}
			dropped[name] = reason
			return true
		}
		remote := Target{URL: env.BaseURL, Insecure: target.Insecure}
		snapshot, selection, err := linkedOAuth(ctx, remote, platform, env.APIKey, env.SealedRoot, readWith)
		if err != nil {
			reason := fmt.Errorf("%s/%s: %w", name, platform, err)
			if drop(reason) {
				continue
			}
			return result, dropped, reason
		}
		env.OAuth = snapshot
		if snapshot != nil {
			parts := strings.SplitN(env.APIKey, "_", 3)
			if len(parts) == 3 && parts[0] == "pb" && snapshot.EnvironmentRef != parts[1] {
				reason := fmt.Errorf("%s/%s: auth snapshot environment %q differs from the publishable key's %q", name, platform, snapshot.EnvironmentRef, parts[1])
				if drop(reason) {
					continue
				}
				return result, dropped, reason
			}
			if name == source.Default {
				chosen := selection
				learned = &chosen
				if readWith.ApplicationKey == "" && readWith.Variant == "" {
					readWith = chosen
				}
			}
		}
		result.Environments[name] = env
	}
	if learned != nil {
		if target.OAuth == nil {
			target.OAuth = map[string]OAuthSelection{}
		}
		target.OAuth[platform] = *learned
	}
	return result, dropped, nil
}

// Called after a client configuration mutation, through the existing link path.
func RefreshLinkedClients(ctx context.Context, w io.Writer) error {
	web, apple, android := linkedPlatforms()
	if !web && !apple && !android {
		return nil
	}
	resolved, err := Resolve(ctx)
	target := resolved.Acting()
	if err != nil {
		return err
	}
	// A CHECKOUT BOUND TO A PROJECT IS REFRESHED AS THAT PROJECT. Handing the
	// link the resolved ADDRESS alone wrote the retired record shape and
	// fetched one environment, so an auth write left every other
	// environment's config behind (FR-027). A stack linked on this machine by
	// address is not that project: the auth write went to it, because Resolve
	// prefers it, so it is what gets refreshed.
	if record, recErr := readLinkedProject(); recErr == nil && record.Project != "" && !resolved.Target.SelfHost {
		o, err := linkOptsForRecord(ctx, record, resolved.Env)
		if err != nil {
			return err
		}
		o.insecure = target.Insecure
		return runLink(ctx, o, w)
	}
	return runLink(ctx, linkOpts{url: target.URL, insecure: target.Insecure}, w)
}

// defaultFirst is source's environment names with the default one first and
// the rest in name order.
func defaultFirst(source appEnvironments) []string {
	names := source.names()
	for i, name := range names {
		if name == source.Default && i > 0 {
			rest := append(names[:i:i], names[i+1:]...)
			return append([]string{name}, rest...)
		}
	}
	return names
}

// selectionDoesNotFit is an environment that configures the app under another
// application or variant than the checkout's selection. It answered, so reading
// it again changes nothing: its line names its own remedy (FR-013).
type selectionDoesNotFit struct{ detail string }

func (e selectionDoesNotFit) Error() string { return e.detail }

// droppedEnvironmentLine is what a link says about an environment it leaves as
// it is. One that did not answer is worth asking again; one whose configuration
// does not fit the checkout's selection is not.
func droppedEnvironmentLine(name string, reason error) string {
	var misfit selectionDoesNotFit
	if errors.As(reason, &misfit) {
		return fmt.Sprintf("%s is left as it is: %v\n", name, reason)
	}
	return fmt.Sprintf("%s could not be read (%v) — its files are left as they are; run `palbase link` again once it answers\n", name, reason)
}
