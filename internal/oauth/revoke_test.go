package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRevokeTokenPostsTokenHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/revoke", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "token-1", r.Form.Get("token"))
		require.Equal(t, "client-1", r.Form.Get("client_id"))
		require.Equal(t, "refresh_token", r.Form.Get("token_type_hint"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := RevokeToken(context.Background(), ProviderMetadata{RevocationEndpoint: server.URL + "/revoke"}, "client-1", "token-1", "refresh_token")
	require.NoError(t, err)
}

func TestLogoutRevokesAndDeletesToken(t *testing.T) {
	var revoked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"revocation_endpoint":"` + serverURL(r) + `/revoke","token_endpoint":"` + serverURL(r) + `/token"}`))
		case "/revoke":
			require.NoError(t, r.ParseForm())
			revoked = append(revoked, r.Form.Get("token_type_hint")+":"+r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configHome := t.TempDir()
	_, err := SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = SaveToken(configHome, Token{AccessToken: "access-1", RefreshToken: "refresh-1"})
	require.NoError(t, err)

	result, err := Logout(context.Background(), configHome, "")
	require.NoError(t, err)
	require.True(t, result.Deleted)
	require.True(t, result.AccessRevoked)
	require.True(t, result.RefreshRevoked)
	require.Equal(t, "revoked", result.Revocation)
	require.ElementsMatch(t, []string{"access_token:access-1", "refresh_token:refresh-1"}, revoked)
	_, err = LoadToken(configHome)
	require.ErrorIs(t, err, ErrNoToken)
}
