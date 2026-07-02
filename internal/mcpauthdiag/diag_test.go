package mcpauthdiag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestBuildReportsOAuthRecoveryActions(t *testing.T) {
	report := Build(mcp.AuthStatusResult{
		Server: "repo",
		Status: "error",
		Error:  "unauthorized",
	}, t.TempDir(), "work", time.Now().UTC())

	require.Equal(t, "repo", report.Server)
	require.NotNil(t, report.OAuthStatus)
	require.False(t, report.OAuthStatus.ProfileConfigured)
	require.Contains(t, actionCommands(report.NextActions), "codog mcp show repo")
	require.Contains(t, actionCommands(report.NextActions), "codog mcp auth repo")
	require.Contains(t, actionCommands(report.NextActions), "codog oauth provider save work ISSUER_URL CLIENT_ID [SCOPE...]")
	require.Contains(t, actionCommands(report.NextActions), "codog oauth browser login work")
}

func TestBuildSuggestsMCPAuthRefreshForExpiredRefreshableToken(t *testing.T) {
	configHome := t.TempDir()
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"token_endpoint":"` + serverURL(r) + `/token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "work", server.URL, "client-1", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access-token",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(-1 * time.Hour),
	})
	require.NoError(t, err)

	report := Build(mcp.AuthStatusResult{Server: "repo", Status: "ok"}, configHome, "work", now)
	require.Contains(t, actionCommands(report.NextActions), "codog mcp auth --refresh repo")
	require.Contains(t, actionCommands(report.NextActions), "codog oauth token refresh work")
}

func TestRefreshStoredOAuthToken(t *testing.T) {
	configHome := t.TempDir()
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"token_endpoint":"` + serverURL(r) + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			require.Equal(t, "refresh-1", r.Form.Get("refresh_token"))
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			_, _ = w.Write([]byte(`{"access_token":"new-access-token-1234","refresh_token":"refresh-2","token_type":"Bearer","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "work", server.URL, "client-1", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access-token",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(-1 * time.Hour),
		CreatedAt:    now.Add(-2 * time.Hour),
	})
	require.NoError(t, err)

	report := Refresh(context.Background(), mcp.AuthStatusResult{Server: "repo", Status: "ok"}, configHome, "work", now)
	require.True(t, report.Refreshed)
	require.Empty(t, report.RefreshError)
	require.NotNil(t, report.Token)
	require.Equal(t, "new-...1234", report.Token.AccessToken)
	require.NotNil(t, report.OAuthStatus)
	require.True(t, report.OAuthStatus.Ready)
	require.Contains(t, actionCommands(report.NextActions), "codog mcp tools repo")

	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "new-access-token-1234", loaded.AccessToken)
	require.Equal(t, "refresh-2", loaded.RefreshToken)
}

func actionCommands(actions []NextAction) []string {
	commands := make([]string, 0, len(actions))
	for _, action := range actions {
		commands = append(commands, action.Command)
	}
	return commands
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
