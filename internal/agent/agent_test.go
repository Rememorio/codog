package agent

import (
	"bytes"
	"testing"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/config"
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
