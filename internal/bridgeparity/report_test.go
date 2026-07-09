package bridgeparity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildReportsReadyWhenBridgeAuthAndRemoteSessionConfigured(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "session_token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("remote-token\n"), 0o600))
	env := map[string]string{
		"CLAUDE_CODE_REMOTE":            "1",
		"CLAUDE_CODE_REMOTE_SESSION_ID": "sess-123",
		"CCR_UPSTREAM_PROXY_ENABLED":    "1",
		"CCR_SESSION_TOKEN_PATH":        tokenPath,
	}

	report := Build(Options{
		RemoteAuthToken:   "remote-auth",
		EditorBridgeToken: "editor-auth",
		RemoteEnabled:     true,
		RemoteEnv:         env,
		RemoteProxyPort:   9911,
	})

	require.Equal(t, "ready", report.Status)
	require.Equal(t, len(RequiredBridgeMethods()), report.RequiredBridgeMethodCount)
	require.Equal(t, len(RequiredControlRoutes()), report.RequiredControlRouteCount)
	require.Empty(t, report.MissingBridgeMethods)
	require.Empty(t, report.MissingControlRoutes)
	require.True(t, report.RemoteAuthConfigured)
	require.True(t, report.EditorAuthConfigured)
	require.True(t, report.RemoteSessionConfigured)
	require.True(t, report.RemoteProxyReady)
	require.Empty(t, report.RemoteProxyMissing)
}

func TestBuildReportsDegradedWhenAuthIsMissing(t *testing.T) {
	report := Build(Options{RemoteEnv: map[string]string{}})

	require.Equal(t, "degraded", report.Status)
	require.False(t, report.RemoteAuthConfigured)
	require.False(t, report.EditorAuthConfigured)
	require.Empty(t, report.MissingBridgeMethods)
	require.Empty(t, report.MissingControlRoutes)
}

func TestRequiredListsAreStableCopies(t *testing.T) {
	methods := RequiredBridgeMethods()
	routes := RequiredControlRoutes()
	require.Contains(t, methods, "editor/identify")
	require.Contains(t, routes, "/editor/identify")

	methods[0] = "mutated"
	routes[0] = "/mutated"

	require.NotEqual(t, "mutated", RequiredBridgeMethods()[0])
	require.NotEqual(t, "/mutated", RequiredControlRoutes()[0])
}
