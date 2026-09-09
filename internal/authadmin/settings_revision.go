package authadmin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var settingsRevisionPattern = regexp.MustCompile(`^"auth-settings-[a-f0-9]{64}"$`)

func validSettingsRevision(headers http.Header) bool {
	return len(headers.Values("ETag")) == 1 && settingsRevisionPattern.MatchString(headers.Get("ETag"))
}

func readSettingsRevision(rest REST, cmd *cobra.Command) (string, error) {
	status, raw, headers, err := rest.DoWithHeaders(cmd.Context(), http.MethodGet, base+"/settings", nil, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("could not read the current settings (%d): %s", status, strings.TrimSpace(string(raw)))
	}
	var current map[string]any
	if err := json.Unmarshal(raw, &current); err != nil || current == nil {
		return "", fmt.Errorf("the current settings did not parse as an object")
	}
	if !validSettingsRevision(headers) {
		return "", fmt.Errorf("this environment does not support versioned auth settings yet; update the runtime before editing")
	}
	return headers.Get("ETag"), nil
}
