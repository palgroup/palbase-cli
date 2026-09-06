package authadmin

import (
	"encoding/json"
	"fmt"
	"github.com/palgroup/palbase-cli/internal/authcontract"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

const socialBase = base + "/social-auth"
const contractHeader = "Palbase-Auth-Contract"

func socialProvider(name string) bool {
	switch name {
	case "google", "apple", "microsoft", "github":
		return true
	}
	return false
}

func socialError(status int, raw []byte) error {
	var result struct {
		Error       string          `json:"error"`
		Description string          `json:"error_description"`
		Fields      json.RawMessage `json:"field_errors"`
	}
	if json.Unmarshal(raw, &result) == nil && result.Error != "" {
		if len(result.Fields) > 0 {
			return fmt.Errorf("%s: %s\n%s", result.Error, result.Description, result.Fields)
		}
		return fmt.Errorf("%s: %s", result.Error, result.Description)
	}
	return fmt.Errorf("the social auth API answered %d", status)
}

func readSocial(rest REST, cmd *cobra.Command) ([]byte, string, error) {
	status, raw, headers, err := rest.DoWithHeaders(cmd.Context(), http.MethodGet, socialBase, nil, http.Header{contractHeader: {"1"}})
	if err != nil {
		return nil, "", err
	}
	if status != 200 {
		return nil, "", socialError(status, raw)
	}
	if headers.Get(contractHeader) != "1" || len(headers.Values(contractHeader)) != 1 {
		return nil, "", fmt.Errorf("the stack did not confirm social auth contract 1")
	}
	if err := authcontract.Validate("SocialAdminConfig", raw); err != nil {
		return nil, "", fmt.Errorf("invalid social config returned by the stack: %w", err)
	}
	etag := headers.Get("ETag")
	if len(etag) < 2 || etag[0] != '"' || etag[len(etag)-1] != '"' || strings.HasPrefix(etag, "W/") || len(headers.Values("ETag")) != 1 {
		return nil, "", fmt.Errorf("the stack did not return a strong social config ETag")
	}
	return raw, etag, nil
}

func listProviders(r Resolvers, cmd *cobra.Command) error {
	rest, err := r.REST(cmd)
	if err != nil {
		return err
	}
	status, ordinary, err := rest.Do(cmd.Context(), http.MethodGet, base+"/providers", nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return socialError(status, ordinary)
	}
	social, _, err := readSocial(rest, cmd)
	if err != nil {
		return err
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(ordinary, &result); err != nil {
		return fmt.Errorf("invalid provider response: %w", err)
	}
	if result == nil {
		return fmt.Errorf("invalid provider response: expected an object")
	}
	result["social_auth"] = social
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return emit(cmd, raw)
}

func changeSocialConfig(r Resolvers, cmd *cobra.Command, method, path string, body []byte, refresh bool) error {
	rest, err := r.REST(cmd)
	if err != nil {
		return err
	}
	_, etag, err := readSocial(rest, cmd)
	if err != nil {
		return err
	}
	return writeSocial(r, rest, cmd, method, path, etag, body, refresh)
}

func writeSocial(r Resolvers, rest REST, cmd *cobra.Command, method, path, etag string, body []byte, refresh bool) error {
	status, raw, headers, err := rest.DoWithHeaders(cmd.Context(), method, socialBase+path, body, http.Header{contractHeader: {"1"}, "If-Match": {etag}})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return socialError(status, raw)
	}
	if headers.Get(contractHeader) != "1" || headers.Get("ETag") == "" {
		return fmt.Errorf("the stack accepted the change but omitted its contract or revision; read the config before retrying")
	}
	if err := emit(cmd, raw); err != nil {
		return err
	}
	if refresh && r.RefreshClientConfig != nil {
		if err := r.RefreshClientConfig(cmd); err != nil {
			return fmt.Errorf("server configuration saved; local client generation failed: %w. Run palbase link to refresh local files; the server change is already saved", err)
		}
	}
	return nil
}

func deleteSocialCredential(r Resolvers, cmd *cobra.Command, provider, key string) error {
	rest, err := r.REST(cmd)
	if err != nil {
		return err
	}
	raw, etag, err := readSocial(rest, cmd)
	if err != nil {
		return err
	}
	var document struct {
		Credentials []struct{ Key, Provider string } `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("invalid social config response: %w", err)
	}
	for _, credential := range document.Credentials {
		if credential.Key == key {
			if credential.Provider != provider {
				return fmt.Errorf("credential %q belongs to %s", key, credential.Provider)
			}
			return writeSocial(r, rest, cmd, http.MethodDelete, "/credentials/"+url.PathEscape(key), etag, nil, false)
		}
	}
	return fmt.Errorf("credential %q does not exist", key)
}
