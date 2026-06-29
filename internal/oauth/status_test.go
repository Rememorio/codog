package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInspectStatusReportsRefreshableExpiredToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()
	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = SaveToken(configHome, Token{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(-time.Hour),
	})
	require.NoError(t, err)

	status := InspectStatus(configHome, "", now)
	require.True(t, status.ProfileConfigured)
	require.Equal(t, "default", status.ProfileName)
	require.True(t, status.TokenPresent)
	require.True(t, status.Expired)
	require.True(t, status.CanRefresh)
	require.True(t, status.Ready)
	require.Empty(t, status.Issue)
	require.Equal(t, "expi...cess", status.Token.AccessToken)
}

func TestInspectStatusReportsMissingToken(t *testing.T) {
	status := InspectStatus(t.TempDir(), "", time.Now().UTC())
	require.False(t, status.TokenPresent)
	require.False(t, status.Ready)
	require.Equal(t, "no oauth token saved", status.Issue)
}
