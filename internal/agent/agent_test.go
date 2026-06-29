package agent

import (
	"bytes"
	"testing"

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
