package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionContextFromEnvReadsRemoteState(t *testing.T) {
	context := SessionContextFromEnv(map[string]string{
		"CLAUDE_CODE_REMOTE":            "true",
		"CLAUDE_CODE_REMOTE_SESSION_ID": "session-123",
		"ANTHROPIC_BASE_URL":            "https://remote.test",
	})

	require.True(t, context.Enabled)
	require.Equal(t, "session-123", context.SessionID)
	require.Equal(t, "https://remote.test", context.BaseURL)
}

func TestBootstrapRequiresRemoteSessionTokenAndProxyFlag(t *testing.T) {
	bootstrap := BootstrapFromEnv(map[string]string{
		"CLAUDE_CODE_REMOTE":         "1",
		"CCR_UPSTREAM_PROXY_ENABLED": "true",
	})

	require.False(t, bootstrap.ShouldEnable())
	require.Contains(t, bootstrap.Missing, "CLAUDE_CODE_REMOTE_SESSION_ID")
	require.Contains(t, bootstrap.Missing, "session_token")
	require.False(t, bootstrap.StateForPort(8080).Enabled)
}

func TestBootstrapDerivesProxyStateAndRedactedReport(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "session_token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("secret-token\n"), 0o600))
	caPath := filepath.Join(root, "ca-bundle.crt")
	env := map[string]string{
		"CLAUDE_CODE_REMOTE":            "1",
		"CCR_UPSTREAM_PROXY_ENABLED":    "true",
		"CLAUDE_CODE_REMOTE_SESSION_ID": "session-123",
		"ANTHROPIC_BASE_URL":            "https://remote.test",
		"CCR_SESSION_TOKEN_PATH":        tokenPath,
		"CCR_CA_BUNDLE_PATH":            caPath,
	}

	report := InspectEnv(env, 9443)

	require.True(t, report.UpstreamProxy.Ready)
	require.True(t, report.UpstreamProxy.TokenConfigured)
	require.Equal(t, "wss://remote.test/v1/code/upstreamproxy/ws", report.UpstreamProxy.WebSocketURL)
	require.Equal(t, "http://127.0.0.1:9443", report.UpstreamProxy.ProxyURL)
	require.Equal(t, caPath, report.SubprocessEnv["SSL_CERT_FILE"])
	require.Contains(t, report.UpstreamProxy.SubprocessEnvKeyList, "HTTPS_PROXY")
	require.NotContains(t, report.UpstreamProxy.TokenPath, "secret-token")
	require.Empty(t, report.UpstreamProxy.Missing)
}

func TestReadTokenTrimsAndHandlesMissingFiles(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "session_token")
	require.NoError(t, os.WriteFile(tokenPath, []byte(" abc123 \n"), 0o600))

	token, err := ReadToken(tokenPath)
	require.NoError(t, err)
	require.Equal(t, "abc123", token)

	token, err = ReadToken(filepath.Join(root, "missing"))
	require.NoError(t, err)
	require.Empty(t, token)
}

func TestInheritedProxyEnvRequiresProxyAndCA(t *testing.T) {
	inherited := InheritedProxyEnv(map[string]string{
		"HTTPS_PROXY":   "http://127.0.0.1:8888",
		"SSL_CERT_FILE": "/tmp/ca-bundle.crt",
		"NO_PROXY":      "localhost",
	})

	require.Equal(t, map[string]string{
		"HTTPS_PROXY":   "http://127.0.0.1:8888",
		"SSL_CERT_FILE": "/tmp/ca-bundle.crt",
		"NO_PROXY":      "localhost",
	}, inherited)
	require.Empty(t, InheritedProxyEnv(map[string]string{}))
}

func TestMergeEnvOverlaysProxyValuesWithoutDroppingBase(t *testing.T) {
	merged := MergeEnv([]string{"PATH=/bin", "HTTPS_PROXY=http://old", "KEEP=yes"}, map[string]string{
		"HTTPS_PROXY":   "http://127.0.0.1:9443",
		"SSL_CERT_FILE": "/tmp/ca-bundle.crt",
	})

	require.Equal(t, []string{
		"PATH=/bin",
		"HTTPS_PROXY=http://127.0.0.1:9443",
		"KEEP=yes",
		"SSL_CERT_FILE=/tmp/ca-bundle.crt",
	}, merged)
}

func TestHelperOutputsMatchRemoteContract(t *testing.T) {
	require.Equal(t, "ws://localhost:3000/v1/code/upstreamproxy/ws", UpstreamProxyWebSocketURL("http://localhost:3000/"))
	require.Contains(t, NoProxyList(), "anthropic.com")
	require.Contains(t, NoProxyList(), "github.com")
	require.Equal(t, map[string]string{}, UpstreamProxyState{}.SubprocessEnv())
}
