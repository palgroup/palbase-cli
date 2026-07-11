package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/apps"
)

func stubTarget(url, key string) specTargetLookup {
	return func(context.Context, string, string) (backendTarget, error) {
		return backendTarget{URL: url, APIKey: key}, nil
	}
}

func stubFetch(body string, capture *string) remoteSpecFetch {
	return func(_ context.Context, specURL, _ string, _ io.Writer) ([]byte, error) {
		if capture != nil {
			*capture = specURL
		}
		return []byte(body), nil
	}
}

func TestPullSpec_FlagDefaults(t *testing.T) {
	cmd := newSpecCmd(Resolvers{})
	require.Equal(t, "spec", cmd.Name())
	for _, tc := range []struct{ name, def string }{
		{"ref", ""}, {"branch", ""}, {"out-dir", "./.palbase"},
	} {
		flag := cmd.Flags().Lookup(tc.name)
		require.NotNil(t, flag)
		require.Equal(t, tc.def, flag.DefValue)
	}
	var flags []string
	cmd.Flags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag.Name) })
	require.Equal(t, []string{"branch", "out-dir", "ref"}, flags)
}

func TestRunPullSpec_EmptyAppWritesOnlySharedSpec(t *testing.T) {
	dir := t.TempDir()
	var fetchedURL string
	err := runPullSpec(
		context.Background(), stubTarget("https://generic.example", "pb_generic"),
		stubFetch(`{"openapi":"3.1.0"}`, &fetchedURL),
		func(context.Context, string) ([]AppBinding, error) {
			t.Fatal("must not list bindings without an app")
			return nil, nil
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			t.Fatal("must not fetch config without an app")
			return apps.ConfigArtifact{}, nil
		},
		"prodref", "main", dir, filepath.Join(dir, "ios"), "", io.Discard,
	)
	require.NoError(t, err)
	require.Equal(t, "https://generic.example/openapi.json", fetchedURL)
	_, err = os.Stat(filepath.Join(dir, "openapi.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "ios", "palbase-config.json"))
	require.True(t, os.IsNotExist(err))
}

func TestRunPullSpec_AppConfigSeparatesOutputs(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "ios")
	var gotURL, gotKey string
	err := runPullSpec(
		context.Background(), stubTarget("https://generic.example", "pb_generic"),
		func(_ context.Context, specURL, apiKey string, _ io.Writer) ([]byte, error) {
			gotURL, gotKey = specURL, apiKey
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{ProjectRef: "prodref", EnvPreset: "production"}}, nil
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{
				AppID: "app_android", ProjectRef: "prodref", Platform: "android",
				EnvPreset: "production", BaseURL: "https://app-bound.example", APIKey: "pb_app",
				OAuth:     &apps.OAuthConfig{Apple: &apps.OAuthApple{Enabled: true}},
				Integrity: &apps.IntegrityConfig{CloudProjectNumber: 123456789},
				Notifications: &apps.NotificationsConfig{FCM: apps.FCMConfig{
					ProjectID: "pb-prodref", ApplicationID: "1:123456789:android:abc",
					APIKey: "public-firebase-key", SenderID: "123456789",
					PackageName: "io.palbase.todo",
				}},
			}, nil
		},
		"prodref", "main", root, configDir, "app_android", io.Discard,
	)
	require.NoError(t, err)
	require.Equal(t, "https://app-bound.example/openapi.json", gotURL)
	require.Equal(t, "pb_app", gotKey)
	_, err = os.Stat(filepath.Join(root, "openapi.json"))
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(configDir, "palbase-config.json"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "app_android", got["app_id"])
	require.Equal(t, "https://app-bound.example", got["base_url"])
	require.Equal(t, "pb_app", got["api_key"])
	require.Equal(t, float64(123456789), got["integrity"].(map[string]any)["cloud_project_number"])
	fcm := got["notifications"].(map[string]any)["fcm"].(map[string]any)
	require.Equal(t, "io.palbase.todo", fcm["package_name"])
	require.ElementsMatch(t, []string{
		"api_key", "app_id", "base_url", "env_preset", "oauth", "integrity", "notifications",
	}, mapKeys(got))
	_, err = os.Stat(filepath.Join(root, "palbase-config.json"))
	require.True(t, os.IsNotExist(err), "native config must exist only in its platform slot")
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

func TestRunPullSpec_MissingBindingWritesNothing(t *testing.T) {
	root := t.TempDir()
	err := runPullSpec(
		context.Background(), stubTarget("https://generic.example", "pb_generic"),
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			t.Fatal("tenant fetch must not run")
			return nil, errors.New("unreachable")
		},
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{ProjectRef: "other"}}, nil
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			t.Fatal("config fetch must not run")
			return apps.ConfigArtifact{}, nil
		},
		"prodref", "main", root, filepath.Join(root, "ios"), "app_ios", io.Discard,
	)
	require.ErrorContains(t, err, "not bound to project ref")
	_, statErr := os.Stat(filepath.Join(root, "openapi.json"))
	require.True(t, os.IsNotExist(statErr))
}
