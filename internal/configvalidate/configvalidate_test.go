package configvalidate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateBytesReportsUnknownKeyWithSuggestion(t *testing.T) {
	result := ValidateBytes([]byte(`{"modle":"opus"}`), "config.json")

	require.Equal(t, "warning", result.Status)
	require.Empty(t, result.Errors)
	require.Len(t, result.Warnings, 1)
	require.Equal(t, "unknown_key", result.Warnings[0].Kind)
	require.Equal(t, "modle", result.Warnings[0].Field)
	require.Equal(t, "model", result.Warnings[0].Suggestion)
	require.Contains(t, result.Warnings[0].Message, `Did you mean "model"?`)
}

func TestValidateBytesReportsWrongTypesAndLineNumbers(t *testing.T) {
	source := []byte("{\n  \"model\": 42,\n  \"future\": {\"sandbox\": {\"enabled\": \"yes\"}}\n}")

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 2)
	require.Equal(t, "model", result.Errors[0].Field)
	require.Equal(t, "wrong_type", result.Errors[0].Kind)
	require.Equal(t, "a string", result.Errors[0].Expected)
	require.Equal(t, "a number", result.Errors[0].Got)
	require.NotNil(t, result.Errors[0].Line)
	require.Equal(t, 2, *result.Errors[0].Line)
	require.Equal(t, "future.sandbox.enabled", result.Errors[1].Field)
	require.Equal(t, "a boolean", result.Errors[1].Expected)
	require.Equal(t, "a string", result.Errors[1].Got)
}

func TestValidateBytesReportsDeprecatedCompatibilityAliases(t *testing.T) {
	result := ValidateBytes([]byte(`{"permissionMode":"plan","mcpServers":{}}`), "config.json")

	require.Equal(t, "warning", result.Status)
	require.Empty(t, result.Errors)
	require.Len(t, result.Warnings, 2)
	require.Equal(t, "permissionMode", result.Warnings[0].Field)
	require.Equal(t, "deprecated", result.Warnings[0].Kind)
	require.Equal(t, "permission_mode", result.Warnings[0].Replacement)
	require.Equal(t, "mcpServers", result.Warnings[1].Field)
	require.Equal(t, "mcp_servers", result.Warnings[1].Replacement)
}

func TestValidateBytesValidatesMCPServerObjects(t *testing.T) {
	source := []byte(`{"mcp_servers":{"demo":{"command":"uvx","args":["server"],"env":[42],"extra":true}}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "mcp_servers.demo.env", result.Errors[0].Field)
	require.Len(t, result.Warnings, 1)
	require.Equal(t, "mcp_servers.demo.extra", result.Warnings[0].Field)
}

func TestValidateFileRejectsTOMLAndReportSummarizes(t *testing.T) {
	dir := t.TempDir()
	goodPath := filepath.Join(dir, "config.json")
	tomlPath := filepath.Join(dir, "settings.toml")
	missingPath := filepath.Join(dir, "missing.json")
	require.NoError(t, os.WriteFile(goodPath, []byte(`{"model":"opus","hooks":{"PreToolUse":["echo ok"]}}`), 0o644))
	require.NoError(t, os.WriteFile(tomlPath, []byte(`model = "opus"`), 0o644))

	report := ValidateFiles([]string{goodPath, tomlPath, missingPath})

	require.Equal(t, "error", report.Status)
	require.Equal(t, 3, report.FileCount)
	require.Equal(t, 2, report.PresentCount)
	require.Equal(t, 1, report.ErrorCount)
	require.Equal(t, "ok", report.Results[0].Status)
	require.Equal(t, "error", report.Results[1].Status)
	require.Equal(t, "unsupported_format", report.Results[1].Errors[0].Kind)
	require.Equal(t, "missing", report.Results[2].Status)
}
