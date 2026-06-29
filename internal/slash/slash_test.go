package slash

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderHelpIncludesCoreCommands(t *testing.T) {
	var out bytes.Buffer
	RenderHelp(&out)
	require.Contains(t, out.String(), "/status")
	require.Contains(t, out.String(), "/init")
	require.Contains(t, out.String(), "/state")
	require.Contains(t, out.String(), "/memory")
	require.Contains(t, out.String(), "/config")
	require.Contains(t, out.String(), "/model")
	require.Contains(t, out.String(), "/permissions")
	require.Contains(t, out.String(), "/doctor")
	require.Contains(t, out.String(), "/compact")
	require.Contains(t, out.String(), "/diff")
	require.Contains(t, out.String(), "/commit")
	require.Contains(t, out.String(), "/export")
	require.Contains(t, out.String(), "/session")
	require.Contains(t, out.String(), "/mcp")
}
