package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestResolveProviderProfileUsesDefaultOrSingleProfile(t *testing.T) {
	configHome := t.TempDir()
	defaultProfile := ProviderProfile{Name: "default", ClientID: "client-default"}
	otherProfile := ProviderProfile{Name: "other", ClientID: "client-other"}
	require.NoError(t, saveProviderProfileForTest(configHome, defaultProfile))
	require.NoError(t, saveProviderProfileForTest(configHome, otherProfile))

	resolved, err := ResolveProviderProfile(configHome, "")
	require.NoError(t, err)
	require.Equal(t, "default", resolved.Name)

	configHome = t.TempDir()
	require.NoError(t, saveProviderProfileForTest(configHome, ProviderProfile{Name: "only", ClientID: "client-only"}))
	resolved, err = ResolveProviderProfile(configHome, "")
	require.NoError(t, err)
	require.Equal(t, "only", resolved.Name)
}

func TestProviderProfileRejectsUnsafeName(t *testing.T) {
	_, err := SaveProviderProfile(context.Background(), t.TempDir(), "../bad", "https://issuer.example", "client", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider profile name")
}

func saveProviderProfileForTest(configHome string, profile ProviderProfile) error {
	path := providerProfilePath(configHome, profile.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
