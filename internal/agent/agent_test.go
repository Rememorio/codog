package agent

import (
	"bytes"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestEnterpriseAuditListsEvents(t *testing.T) {
	configHome := t.TempDir()
	require.NoError(t, audit.NewStore(configHome).Append(audit.Event{
		Type:      "permission",
		ToolName:  "bash",
		Allowed:   audit.Bool(false),
		SessionID: "session-1",
	}))

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.Enterprise([]string{"audit", "10"}))
	require.Contains(t, out.String(), `"type": "permission"`)
	require.Contains(t, out.String(), `"allowed": false`)
}

func TestBuildAgentCommandQuotesPrompt(t *testing.T) {
	command := buildAgentCommand("/tmp/codog", agentdefs.Definition{
		Name:   "reviewer",
		Model:  "mock-model",
		Prompt: "review carefully",
	}, "check '$HOME'")

	require.Contains(t, command, "'/tmp/codog'")
	require.Contains(t, command, "--model 'mock-model'")
	require.Contains(t, command, "prompt 'review carefully")
	require.Contains(t, command, "'\"'\"'$HOME'\"'\"'")
}

func TestOAuthTokenCommands(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	require.NoError(t, app.OAuth([]string{"token", "save", "access-token-1234", "refresh-token-1234", expiresAt}))
	require.Contains(t, out.String(), `"access_token": "acce...1234"`)
	require.NotContains(t, out.String(), "access-token-1234")

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "show"}))
	require.Contains(t, out.String(), `"expired": false`)

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "delete"}))
	require.Contains(t, out.String(), `"deleted":true`)
}

func TestApplyStoredOAuthToken(t *testing.T) {
	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveToken(configHome, oauth.Token{
		AccessToken: "stored-token",
		ExpiresAt:   now.Add(time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "stored-token", cfg.AuthToken)

	cfg = config.Config{ConfigHome: configHome, AuthToken: "explicit-token"}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "explicit-token", cfg.AuthToken)
}
