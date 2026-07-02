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

func TestValidateBytesAcceptsAPIKeyHelper(t *testing.T) {
	result := ValidateBytes([]byte(`{"apiKeyHelper":"security find-generic-password -w -s anthropic"}`), "config.json")

	require.Equal(t, "ok", result.Status)
	require.Empty(t, result.Errors)
	require.Empty(t, result.Warnings)
}

func TestValidateBytesReportsAPIKeyHelperWrongType(t *testing.T) {
	result := ValidateBytes([]byte(`{"apiKeyHelper":true}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "apiKeyHelper", result.Errors[0].Field)
	require.Equal(t, "wrong_type", result.Errors[0].Kind)
	require.Equal(t, "a string", result.Errors[0].Expected)
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

func TestValidateBytesAcceptsRAGConfig(t *testing.T) {
	result := ValidateBytes([]byte(`{"rag_base_url":"http://127.0.0.1:8090","rag_timeout_seconds":15,"rag_top_k_max":8}`), "config.json")

	require.Equal(t, "ok", result.Status)
	require.Empty(t, result.Errors)
	require.Empty(t, result.Warnings)
}

func TestValidateBytesValidatesCleanupPeriodDaysType(t *testing.T) {
	result := ValidateBytes([]byte(`{"cleanupPeriodDays":"30"}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "cleanupPeriodDays", result.Errors[0].Field)
	require.Equal(t, "a number", result.Errors[0].Expected)
}

func TestValidateBytesValidatesForceLoginTypes(t *testing.T) {
	result := ValidateBytes([]byte(`{"forceLoginMethod":true,"forceLoginOrgUUID":42}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 2)
	errorsByField := map[string]string{}
	for _, diagnostic := range result.Errors {
		errorsByField[diagnostic.Field] = diagnostic.Expected
	}
	require.Equal(t, "a string", errorsByField["forceLoginMethod"])
	require.Equal(t, "a string", errorsByField["forceLoginOrgUUID"])
}

func TestValidateBytesValidatesPermissionsDefaultModeType(t *testing.T) {
	result := ValidateBytes([]byte(`{"permissions":{"defaultMode":42},"permission_rules":{"defaultMode":false}}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 2)
	errorsByField := map[string]string{}
	for _, diagnostic := range result.Errors {
		errorsByField[diagnostic.Field] = diagnostic.Expected
	}
	require.Equal(t, "a string", errorsByField["permissions.defaultMode"])
	require.Equal(t, "a string", errorsByField["permission_rules.defaultMode"])
}

func TestValidateBytesValidatesPermissionsAdditionalDirectoriesType(t *testing.T) {
	result := ValidateBytes([]byte(`{"permissions":{"additionalDirectories":[42]},"permission_rules":{"additionalDirectories":"../shared"}}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 2)
	errorsByField := map[string]string{}
	for _, diagnostic := range result.Errors {
		errorsByField[diagnostic.Field] = diagnostic.Expected
	}
	require.Equal(t, "an array of strings", errorsByField["permissions.additionalDirectories"])
	require.Equal(t, "an array of strings", errorsByField["permission_rules.additionalDirectories"])
}

func TestValidateBytesValidatesRespectGitignoreType(t *testing.T) {
	result := ValidateBytes([]byte(`{"respectGitignore":"false"}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "respectGitignore", result.Errors[0].Field)
	require.Equal(t, "a boolean", result.Errors[0].Expected)
}

func TestValidateBytesValidatesDisableAllHooksType(t *testing.T) {
	result := ValidateBytes([]byte(`{"disableAllHooks":"true"}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "disableAllHooks", result.Errors[0].Field)
	require.Equal(t, "a boolean", result.Errors[0].Expected)
}

func TestValidateBytesValidatesStatusLineTypes(t *testing.T) {
	result := ValidateBytes([]byte(`{"statusLine":{"type":42,"command":true,"padding":"2"}}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 3)
	errorsByField := map[string]string{}
	for _, diagnostic := range result.Errors {
		errorsByField[diagnostic.Field] = diagnostic.Expected
	}
	require.Equal(t, "a string", errorsByField["statusLine.type"])
	require.Equal(t, "a string", errorsByField["statusLine.command"])
	require.Equal(t, "a number", errorsByField["statusLine.padding"])
}

func TestValidateBytesValidatesStatusLineTypeValue(t *testing.T) {
	result := ValidateBytes([]byte(`{"statusLine":{"type":"inline","command":"echo ready"}}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "statusLine.type", result.Errors[0].Field)
	require.Equal(t, "invalid_value", result.Errors[0].Kind)
	require.Equal(t, `"command"`, result.Errors[0].Expected)
}

func TestValidateBytesRequiresStatusLineCommandShape(t *testing.T) {
	result := ValidateBytes([]byte(`{"statusLine":{"command":"   "}}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 2)
	errorsByField := map[string]string{}
	for _, diagnostic := range result.Errors {
		errorsByField[diagnostic.Field] = diagnostic.Kind
	}
	require.Equal(t, "missing_required", errorsByField["statusLine.type"])
	require.Equal(t, "missing_required", errorsByField["statusLine.command"])
}

func TestValidateBytesValidatesWorktreeTypes(t *testing.T) {
	result := ValidateBytes([]byte(`{"worktree":{"symlinkDirectories":"node_modules","sparsePaths":[42]}}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 2)
	errorsByField := map[string]string{}
	for _, diagnostic := range result.Errors {
		errorsByField[diagnostic.Field] = diagnostic.Expected
	}
	require.Equal(t, "an array of strings", errorsByField["worktree.symlinkDirectories"])
	require.Equal(t, "an array of strings", errorsByField["worktree.sparsePaths"])
}

func TestValidateBytesValidatesProjectMCPTrustTypes(t *testing.T) {
	result := ValidateBytes([]byte(`{"enableAllProjectMcpServers":"true","enabledMcpjsonServers":[42],"disabledMcpjsonServers":"demo"}`), "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 3)
	errorsByField := map[string]string{}
	for _, diagnostic := range result.Errors {
		errorsByField[diagnostic.Field] = diagnostic.Expected
	}
	require.Equal(t, "a boolean", errorsByField["enableAllProjectMcpServers"])
	require.Equal(t, "an array of strings", errorsByField["enabledMcpjsonServers"])
	require.Equal(t, "an array of strings", errorsByField["disabledMcpjsonServers"])
}

func TestValidateBytesAcceptsMCPEnvObject(t *testing.T) {
	result := ValidateBytes([]byte(`{"mcpServers":{"demo":{"command":"demo-mcp","env":{"TOKEN":"secret"}}}}`), "config.json")

	require.NotEqual(t, "error", result.Status)
	require.Empty(t, result.Errors)
}

func TestValidateBytesValidatesMCPServerObjects(t *testing.T) {
	source := []byte(`{"mcp_servers":{"demo":{"command":"uvx","args":["server"],"env":[42],"url":"https://mcp.example.test","headers":{"Authorization":"Bearer token"},"required":true,"extra":true}}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "mcp_servers.demo.env", result.Errors[0].Field)
	require.Len(t, result.Warnings, 1)
	require.Equal(t, "mcp_servers.demo.extra", result.Warnings[0].Field)
}

func TestValidateBytesValidatesMCPRemoteHeaders(t *testing.T) {
	source := []byte(`{"mcp_servers":{"remote":{"url":"https://mcp.example.test","headers":["Authorization=Bearer token"]}}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "mcp_servers.remote.headers", result.Errors[0].Field)
	require.Equal(t, "wrong_type", result.Errors[0].Kind)
	require.Equal(t, "an object", result.Errors[0].Expected)
	require.Empty(t, result.Warnings)
}

func TestValidateBytesAcceptsMCPRemoteServer(t *testing.T) {
	source := []byte(`{"mcp_servers":{"remote":{"url":"https://mcp.example.test","headers":{"Authorization":"Bearer token"},"headersHelper":"./headers-helper","toolCallTimeoutMs":15000,"required":true},"remote_snake":{"url":"https://mcp.example.test","headers_helper":"./headers-helper","tool_call_timeout_ms":25000}}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "ok", result.Status)
	require.Empty(t, result.Errors)
	require.Empty(t, result.Warnings)
}

func TestValidateBytesValidatesMCPRequiredType(t *testing.T) {
	source := []byte(`{"mcp_servers":{"remote":{"url":"https://mcp.example.test","required":"yes"}}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "mcp_servers.remote.required", result.Errors[0].Field)
	require.Equal(t, "a boolean", result.Errors[0].Expected)
}

func TestValidateBytesValidatesMCPHeadersHelperType(t *testing.T) {
	source := []byte(`{"mcp_servers":{"remote":{"url":"https://mcp.example.test","headersHelper":["bad"]}}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "mcp_servers.remote.headersHelper", result.Errors[0].Field)
	require.Equal(t, "a string", result.Errors[0].Expected)
}

func TestValidateBytesValidatesMCPToolCallTimeoutType(t *testing.T) {
	source := []byte(`{"mcp_servers":{"local":{"command":"demo","toolCallTimeoutMs":"fast"}}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "mcp_servers.local.toolCallTimeoutMs", result.Errors[0].Field)
	require.Equal(t, "a number", result.Errors[0].Expected)
}

func TestValidateBytesValidatesAPITimeoutTypes(t *testing.T) {
	source := []byte(`{"apiTimeout":{"connectTimeout":30,"requestTimeout":"slow","maxRetries":8}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "apiTimeout.requestTimeout", result.Errors[0].Field)
	require.Equal(t, "a number", result.Errors[0].Expected)
}

func TestValidateBytesValidatesTopLevelEnv(t *testing.T) {
	source := []byte(`{"env":{"A":"one","B":2}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "env", result.Errors[0].Field)
	require.Equal(t, "an object with string values", result.Errors[0].Expected)
}

func TestValidateBytesValidatesProviderFallbacks(t *testing.T) {
	source := []byte(`{"providerFallbacks":{"primary":"claude-primary","fallbacks":["claude-backup",7]}}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "providerFallbacks.fallbacks", result.Errors[0].Field)
	require.Equal(t, "an array of strings", result.Errors[0].Expected)
}

func TestValidateBytesValidatesTrustedRoots(t *testing.T) {
	source := []byte(`{"trustedRoots":["/repo",7]}`)

	result := ValidateBytes(source, "config.json")

	require.Equal(t, "error", result.Status)
	require.Len(t, result.Errors, 1)
	require.Equal(t, "trustedRoots", result.Errors[0].Field)
	require.Equal(t, "an array of strings", result.Errors[0].Expected)
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
