package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverProviderFallsBackToOpenIDConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			http.NotFound(w, r)
		case "/.well-known/openid-configuration":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"` + serverURLPlaceholder + `","authorization_endpoint":"https://auth.example/authorize","token_endpoint":"https://auth.example/token","device_authorization_endpoint":"https://auth.example/device","code_challenge_methods_supported":["S256"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	metadata, err := DiscoverProvider(context.Background(), server.URL)
	require.NoError(t, err)
	require.Equal(t, "https://auth.example/authorize", metadata.AuthorizationEndpoint)
	require.Equal(t, "https://auth.example/token", metadata.TokenEndpoint)
	require.Equal(t, "https://auth.example/device", metadata.DeviceAuthorizationEndpoint)
	require.Equal(t, []string{"S256"}, metadata.CodeChallengeMethodsSupported)
	require.Equal(t, server.URL+"/.well-known/openid-configuration", metadata.SourceURL)
}

func TestDiscoverProviderRejectsMissingOAuthEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"example"}`))
	}))
	defer server.Close()

	_, err := DiscoverProvider(context.Background(), server.URL+"/.well-known/oauth-authorization-server")
	require.Error(t, err)
	require.Contains(t, err.Error(), "OAuth endpoints")
}

const serverURLPlaceholder = "https://issuer.example"
