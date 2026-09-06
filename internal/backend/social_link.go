package backend

import (
	"context"
	"encoding/json"
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
)

type OAuthSelection struct {
	ApplicationKey string   `json:"application_key"`
	Variant        string   `json:"variant"`
	ClientKeys     []string `json:"client_keys,omitempty"`
}

func socialRequest(ctx context.Context, target Target, path string, cred Credentials) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(target.URL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	cred.Apply(req)
	req.Header.Set("Palbase-Auth-Contract", "1")
	client := *stackClient(target)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read social auth config from %s: %w", target.URL, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 256*1024+1))
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
func linkedOAuth(ctx context.Context, target Target, platform, publishableKey string, selection OAuthSelection) (*authcontract.SocialSnapshot, OAuthSelection, error) {
	cred, _, err := Credential(target.URL)
	if err != nil {
		return nil, selection, err
	}
	admin, err := socialRequest(ctx, target, "/v1/management/auth/social-auth", cred)
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
			return nil, selection, fmt.Errorf("social sign-in for %s needs application_key and variant in .palbase/project.json oauth.%s; the checkout does not identify one configured target", platform, platform)
		}
	}
	query := url.Values{"application_key": {selection.ApplicationKey}, "platform": {platform}, "variant": {selection.Variant}}
	for _, key := range selection.ClientKeys {
		query.Add("client_key", key)
	}
	raw, err := socialRequest(ctx, target, "/auth/oauth/config?"+query.Encode(), Credentials{Kind: KindKey, Value: publishableKey})
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

func platformEnvironments(ctx context.Context, target *Target, platform string, source appEnvironments) (appEnvironments, error) {
	result := appEnvironments{Default: source.Default, Environments: map[string]appEnvironment{}}
	for _, name := range source.names() {
		env := source.Environments[name]
		if env.APIKey == "" {
			if name != source.Default {
				// The existing optional local slot is unavailable until start.
				// It carries no auth snapshot and cannot be selected by a build.
				env.OAuth = nil
				result.Environments[name] = env
				continue
			}
			return result, fmt.Errorf("%s: cannot refresh complete platform config while the environment is unavailable", name)
		}
		remote := Target{URL: env.BaseURL, Insecure: target.Insecure}
		snapshot, selection, err := linkedOAuth(ctx, remote, platform, env.APIKey, target.OAuth[platform])
		if err != nil {
			return result, fmt.Errorf("%s/%s: %w", name, platform, err)
		}
		env.OAuth = snapshot
		if snapshot != nil {
			parts := strings.SplitN(env.APIKey, "_", 3)
			if len(parts) == 3 && parts[0] == "pb" && snapshot.EnvironmentRef != parts[1] {
				return result, fmt.Errorf("%s/%s: auth snapshot environment differs from the publishable key", name, platform)
			}
			if target.OAuth == nil {
				target.OAuth = map[string]OAuthSelection{}
			}
			target.OAuth[platform] = selection
		}
		result.Environments[name] = env
	}
	return result, nil
}

// Called after a client configuration mutation, through the existing link path.
func RefreshLinkedClients(ctx context.Context, w io.Writer) error {
	web, apple, android := linkedPlatforms()
	if !web && !apple && !android {
		return nil
	}
	target, err := ReadTarget()
	if err != nil {
		return err
	}
	return runLink(ctx, linkOpts{url: target.URL, insecure: target.Insecure}, w)
}
