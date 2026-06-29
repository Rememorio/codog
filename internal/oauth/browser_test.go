package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildBrowserAuthorization(t *testing.T) {
	auth, err := BuildBrowserAuthorization(
		ProviderMetadata{AuthorizationEndpoint: "https://auth.example/authorize"},
		"client-1",
		"http://127.0.0.1:9999/oauth/callback",
		[]string{"profile", "email"},
		"state-1",
		PKCE{CodeVerifier: "verifier-1", CodeChallenge: "challenge-1", Method: "S256"},
	)
	require.NoError(t, err)
	parsed, err := url.Parse(auth.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "/authorize", parsed.Path)
	query := parsed.Query()
	require.Equal(t, "code", query.Get("response_type"))
	require.Equal(t, "client-1", query.Get("client_id"))
	require.Equal(t, "http://127.0.0.1:9999/oauth/callback", query.Get("redirect_uri"))
	require.Equal(t, "profile email", query.Get("scope"))
	require.Equal(t, "state-1", query.Get("state"))
	require.Equal(t, "challenge-1", query.Get("code_challenge"))
	require.Equal(t, "S256", query.Get("code_challenge_method"))
	require.Equal(t, "verifier-1", auth.CodeVerifier)
}

func TestExchangeAuthorizationCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/token", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "authorization_code", r.Form.Get("grant_type"))
		require.Equal(t, "code-1", r.Form.Get("code"))
		require.Equal(t, "client-1", r.Form.Get("client_id"))
		require.Equal(t, "verifier-1", r.Form.Get("code_verifier"))
		require.Equal(t, "http://127.0.0.1:9999/oauth/callback", r.Form.Get("redirect_uri"))
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"browser-access","refresh_token":"browser-refresh","expires_in":3600}`))
	}))
	defer server.Close()

	token, err := ExchangeAuthorizationCode(context.Background(), ProviderMetadata{TokenEndpoint: server.URL + "/token"}, "client-1", "code-1", "verifier-1", "http://127.0.0.1:9999/oauth/callback")
	require.NoError(t, err)
	require.Equal(t, "browser-access", token.AccessToken)
	require.Equal(t, "browser-refresh", token.RefreshToken)
	require.False(t, token.ExpiresAt.IsZero())
}

func TestStartBrowserCallbackServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	callback, err := StartBrowserCallbackServer(ctx, "127.0.0.1:0", "/oauth/callback", "state-1")
	require.NoError(t, err)
	defer callback.Close()

	resp, err := http.Get(callback.RedirectURI + "?code=code-1&state=state-1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	result := <-callback.Results
	require.NoError(t, result.Err)
	require.Equal(t, "code-1", result.Callback.Code)
	require.Equal(t, "state-1", result.Callback.State)
}

func TestStartBrowserCallbackServerRejectsStateMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	callback, err := StartBrowserCallbackServer(ctx, "127.0.0.1:0", "/oauth/callback", "state-1")
	require.NoError(t, err)
	defer callback.Close()

	resp, err := http.Get(callback.RedirectURI + "?code=code-1&state=wrong")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	result := <-callback.Results
	require.Error(t, result.Err)
	require.Contains(t, result.Err.Error(), "state mismatch")
}
