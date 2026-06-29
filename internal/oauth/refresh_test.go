package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRefreshTokenUsesRefreshGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/token", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		require.Equal(t, "refresh-1", r.Form.Get("refresh_token"))
		require.Equal(t, "client-1", r.Form.Get("client_id"))
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-2","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()

	token, err := RefreshToken(context.Background(), ProviderMetadata{TokenEndpoint: server.URL + "/token"}, "client-1", "refresh-1")
	require.NoError(t, err)
	require.Equal(t, "access-2", token.AccessToken)
	require.Equal(t, "refresh-1", token.RefreshToken)
	require.False(t, token.ExpiresAt.IsZero())
}

func TestRefreshStoredTokenUsesDefaultProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"token_endpoint":"` + serverURL(r) + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "refresh-1", r.Form.Get("refresh_token"))
			_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configHome := t.TempDir()
	_, err := SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = SaveToken(configHome, Token{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresAt: time.Now().UTC().Add(-time.Hour)})
	require.NoError(t, err)

	refreshed, err := RefreshStoredToken(context.Background(), configHome, "")
	require.NoError(t, err)
	require.Equal(t, "access-2", refreshed.AccessToken)
	require.Equal(t, "refresh-2", refreshed.RefreshToken)
	loaded, err := LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "access-2", loaded.AccessToken)
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
