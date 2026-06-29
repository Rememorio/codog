package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderProfileLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://auth.example/authorize","token_endpoint":"https://auth.example/token","device_authorization_endpoint":"https://auth.example/device"}`))
	}))
	defer server.Close()
	configHome := t.TempDir()

	profile, err := SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", []string{"profile", "email"})
	require.NoError(t, err)
	require.Equal(t, "default", profile.Name)
	require.Equal(t, "client-1", profile.ClientID)
	require.Equal(t, []string{"profile", "email"}, profile.Scopes)
	require.Equal(t, "https://auth.example/token", profile.Metadata.TokenEndpoint)
	require.FileExists(t, filepath.Join(configHome, "oauth", "providers", "default.json"))

	loaded, err := LoadProviderProfile(configHome, "default")
	require.NoError(t, err)
	require.Equal(t, profile.Name, loaded.Name)

	profiles, err := ListProviderProfiles(configHome)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Equal(t, "default", profiles[0].Name)

	require.NoError(t, DeleteProviderProfile(configHome, "default"))
	profiles, err = ListProviderProfiles(configHome)
	require.NoError(t, err)
	require.Empty(t, profiles)
}

func TestProviderProfileRejectsUnsafeName(t *testing.T) {
	_, err := SaveProviderProfile(context.Background(), t.TempDir(), "../bad", "https://issuer.example", "client", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider profile name")
}
