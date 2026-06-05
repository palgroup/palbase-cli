package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credentials holds the stored authentication tokens.
type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	User         UserInfo  `json:"user"`
}

// UserInfo holds basic user information from the token.
type UserInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// IsExpired returns true if the access token has expired.
func (c *Credentials) IsExpired() bool {
	return time.Now().After(c.ExpiresAt)
}

// ExpiresSoon returns true if the access token has less than `within` of life
// left (including already-expired). It is the refresh-AHEAD counterpart to
// IsExpired: callers that must never hand out a soon-to-die token (e.g. a
// periodic token-file refresher) refresh on ExpiresSoon(skew) rather than
// waiting for IsExpired. A non-positive `within` collapses to IsExpired.
func (c *Credentials) ExpiresSoon(within time.Duration) bool {
	return time.Until(c.ExpiresAt) < within
}

func credentialsPath(mode string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	if mode == "" {
		mode = "prod"
	}
	return filepath.Join(home, ".palbase", fmt.Sprintf("credentials-%s.json", mode)), nil
}

// SaveCredentials writes credentials to ~/.palbase/credentials-{mode}.json with 0600 permissions.
func SaveCredentials(mode string, creds *Credentials) error {
	path, err := credentialsPath(mode)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	return os.WriteFile(path, data, 0600)
}

// LoadCredentials reads credentials from ~/.palbase/credentials-{mode}.json.
func LoadCredentials(mode string) (*Credentials, error) {
	path, err := credentialsPath(mode)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not logged in (%s) — run: palbase login", mode)
		}
		return nil, fmt.Errorf("read credentials: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	return &creds, nil
}

// DeleteCredentials removes the credentials file for the given mode.
func DeleteCredentials(mode string) error {
	path, err := credentialsPath(mode)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove credentials: %w", err)
	}

	return nil
}
