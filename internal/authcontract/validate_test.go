package authcontract

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestGeneratedSchemaRejectsWrongPlatformFieldsAndAmbiguousJSON(t *testing.T) {
	valid := `{"key":"google-ios","enabled":true,"application_key":"consumer","platform":"ios","variant":"release","bundle_id":"com.example.app","ios_client_id":"IOS.apps.googleusercontent.com","redirect_uri":"com.googleusercontent.apps.IOS:/oauthredirect"}`
	require.NoError(t, Validate("SocialGoogleAppleClient", []byte(valid)))
	for _, invalid := range []string{strings.Replace(valid, `"ios_client_id"`, `"web_client_id"`, 1), strings.Replace(valid, `"platform":"ios"`, `"platform":"android"`, 1), valid + " {}", strings.Replace(valid, `"enabled":true`, `"enabled":true,"enabled":false`, 1), strings.Replace(valid, `"enabled":true`, `"enabled":null`, 1)} {
		require.Error(t, Validate("SocialGoogleAppleClient", []byte(invalid)))
	}
}

func TestValidationErrorDoesNotEchoSecretValues(t *testing.T) {
	err := Validate("SocialCredentialInput", []byte(`{"provider":"wrong","kind":"oauth_client_secret","client_secret":"private-secret"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-secret")
}
