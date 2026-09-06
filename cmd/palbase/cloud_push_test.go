package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestCloudPushUsesAccountAuthAndPreservesTheDeployment(t *testing.T) {
	oldResolved, oldAuth, oldSigner := resolved, authClient, transport.DPoPSigner
	t.Cleanup(func() { resolved, authClient, transport.DPoPSigner = oldResolved, oldAuth, oldSigner })
	t.Setenv("PALBASE_ACCESS_TOKEN", "pat_test_deploy")
	var bodies []backend.CloudPushRequest
	var keys []string
	var signedURL string
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/cloud/projects/testref9m/push", r.URL.Path)
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "DPoP pat_test_deploy", r.Header.Get("Authorization"))
		require.Equal(t, "test-proof", r.Header.Get("DPoP"))
		require.Empty(t, r.Header.Get("apikey"), "a project service key must not replace account authentication")
		var body backend.CloudPushRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		bodies = append(bodies, body)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"digest":"committed","endpointCount":12}`))
	}))
	defer server.Close()
	resolved.Endpoints.PlatformAPI = server.URL
	resolved.Endpoints.PublicHost = "cloud.example"
	authClient = auth.NewClient(auth.Config{AuthURL: server.URL}, io.Discard)
	transport.DPoPSigner = func(method, url, token string) (string, error) {
		require.Equal(t, http.MethodPost, method)
		require.Equal(t, "pat_test_deploy", token)
		signedURL = url
		return "test-proof", nil
	}
	input := backend.CloudPushRequest{Artifact: []byte{1, 2, 3}, SDKVersion: "36.0.0", AcceptDataLoss: true, AcceptBreaking: true}
	for i := 0; i < 2; i++ {
		result, err := pushCloudArtifact(context.Background(), "https://testref9m.cloud.example", input)
		require.NoError(t, err)
		require.Equal(t, backend.CloudPushResult{Digest: "committed", EndpointCount: 12}, result)
	}
	require.Equal(t, server.URL+"/v1/cloud/projects/testref9m/push", signedURL)
	require.Equal(t, input, bodies[0])
	require.Len(t, keys[0], 64)
	require.Equal(t, keys[0], keys[1], "the exact same deployment must keep its key")
	input.AcceptBreaking = false
	_, err := pushCloudArtifact(context.Background(), "https://testref9m.cloud.example", input)
	require.NoError(t, err)
	require.NotEqual(t, keys[1], keys[2], "changing approval must not replay another decision")
	status = http.StatusGatewayTimeout
	_, err = pushCloudArtifact(context.Background(), "https://testref9m.cloud.example", input)
	require.Error(t, err)
	require.Len(t, bodies, 4, "an ambiguous failure must not automatically resend the artifact")
	_, err = pushCloudArtifact(context.Background(), "https://selfhost.example", input)
	require.Error(t, err)
	require.Len(t, bodies, 4, "self-hosted uploads must not go to the cloud")
}
