package configvalidate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FieldType string

const (
	FieldString              FieldType = "string"
	FieldBool                FieldType = "bool"
	FieldObject              FieldType = "object"
	FieldStringArray         FieldType = "string_array"
	FieldHookArray           FieldType = "hook_array"
	FieldNumber              FieldType = "number"
	FieldStringOrStringArray FieldType = "string_or_string_array"
	FieldStringArrayOrMap    FieldType = "string_array_or_map"
	FieldStringMap           FieldType = "string_map"
	FieldRulesImport         FieldType = "rules_import"
)

type Diagnostic struct {
	Path        string `json:"path"`
	Field       string `json:"field"`
	Line        *int   `json:"line,omitempty"`
	Kind        string `json:"kind"`
	Message     string `json:"message"`
	Expected    string `json:"expected,omitempty"`
	Got         string `json:"got,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
	Replacement string `json:"replacement,omitempty"`
}

type Result struct {
	Kind         string       `json:"kind"`
	Path         string       `json:"path"`
	Present      bool         `json:"present"`
	Status       string       `json:"status"`
	ErrorCount   int          `json:"error_count"`
	WarningCount int          `json:"warning_count"`
	Errors       []Diagnostic `json:"errors,omitempty"`
	Warnings     []Diagnostic `json:"warnings,omitempty"`
}

type Report struct {
	Kind         string   `json:"kind"`
	Status       string   `json:"status"`
	FileCount    int      `json:"file_count"`
	PresentCount int      `json:"present_count"`
	ErrorCount   int      `json:"error_count"`
	WarningCount int      `json:"warning_count"`
	Paths        []string `json:"paths"`
	Results      []Result `json:"results"`
}

type fieldSpec struct {
	Name     string
	Expected FieldType
}

type deprecatedField struct {
	Name        string
	Replacement string
}

var topLevelFields = []fieldSpec{
	{"$schema", FieldString},
	{"api_key", FieldString},
	{"apiKeyHelper", FieldString},
	{"auth_token", FieldString},
	{"oauth_profile", FieldString},
	{"forceLoginMethod", FieldString},
	{"forceLoginOrgUUID", FieldString},
	{"base_url", FieldString},
	{"model", FieldString},
	{"advisor_model", FieldString},
	{"subagentModel", FieldString},
	{"system_prompt", FieldString},
	{"append_system_prompt", FieldString},
	{"language", FieldString},
	{"theme", FieldString},
	{"editorMode", FieldString},
	{"defaultShell", FieldString},
	{"reasoning_effort", FieldString},
	{"fast_mode", FieldBool},
	{"voice_enabled", FieldBool},
	{"voice_command", FieldString},
	{"speech_command", FieldString},
	{"max_tokens", FieldNumber},
	{"max_turns", FieldNumber},
	{"temperature", FieldNumber},
	{"permission_mode", FieldString},
	{"permissionMode", FieldString},
	{"permission_rules", FieldObject},
	{"permissions", FieldObject},
	{"allowedTools", FieldStringArray},
	{"disallowedTools", FieldStringArray},
	{"background", FieldObject},
	{"privacy_settings", FieldObject},
	{"auto_compact_messages", FieldNumber},
	{"cleanupPeriodDays", FieldNumber},
	{"respectGitignore", FieldBool},
	{"disableAllHooks", FieldBool},
	{"allowManagedHooksOnly", FieldBool},
	{"allowedHttpHookUrls", FieldStringArray},
	{"httpHookAllowedEnvVars", FieldStringArray},
	{"statusLine", FieldObject},
	{"worktree", FieldObject},
	{"enableAllProjectMcpServers", FieldBool},
	{"enabledMcpjsonServers", FieldStringArray},
	{"disabledMcpjsonServers", FieldStringArray},
	{"rate_limit", FieldObject},
	{"apiTimeout", FieldObject},
	{"providerFallbacks", FieldObject},
	{"rulesImport", FieldRulesImport},
	{"compatibility", FieldObject},
	{"editor_bridge", FieldObject},
	{"enterprise", FieldObject},
	{"env", FieldStringMap},
	{"preferences", FieldObject},
	{"trustedRoots", FieldStringArray},
	{"remote", FieldObject},
	{"rag_base_url", FieldString},
	{"rag_timeout_seconds", FieldNumber},
	{"rag_top_k_max", FieldNumber},
	{"additional_dirs", FieldStringArray},
	{"enabled_skills", FieldStringArray},
	{"enabledPlugins", FieldObject},
	{"hooks", FieldObject},
	{"marketplace", FieldObject},
	{"mcp", FieldObject},
	{"mcp_servers", FieldObject},
	{"mcpServers", FieldObject},
	{"future", FieldObject},
	{"sandbox", FieldObject},
	{"updater", FieldObject},
}

var deprecatedTopLevelFields = []deprecatedField{
	{"permissionMode", "permission_mode"},
	{"permissions", "permission_rules"},
	{"allowedTools", "permission_rules.allow"},
	{"disallowedTools", "permission_rules.denied_tools"},
	{"mcpServers", "mcp_servers"},
	{"enabledPlugins", "enabled_skills"},
}

var permissionRuleFields = []fieldSpec{
	{"defaultMode", FieldString},
	{"additionalDirectories", FieldStringArray},
	{"allow", FieldStringArray},
	{"deny", FieldStringArray},
	{"ask", FieldStringArray},
	{"denied_tools", FieldStringArray},
	{"deniedTools", FieldStringArray},
}

var privacyFields = []fieldSpec{
	{"telemetry_enabled", FieldBool},
	{"crash_reports_enabled", FieldBool},
	{"prompt_history_enabled", FieldBool},
}

var rateLimitFields = []fieldSpec{
	{"max_retries", FieldNumber},
	{"initial_backoff_ms", FieldNumber},
	{"max_backoff_ms", FieldNumber},
}

var apiTimeoutFields = []fieldSpec{
	{"connectTimeout", FieldNumber},
	{"requestTimeout", FieldNumber},
	{"maxRetries", FieldNumber},
}

var providerFallbackFields = []fieldSpec{
	{"primary", FieldString},
	{"fallbacks", FieldStringArray},
}

var statusLineFields = []fieldSpec{
	{"type", FieldString},
	{"command", FieldString},
	{"padding", FieldNumber},
}

var worktreeFields = []fieldSpec{
	{"symlinkDirectories", FieldStringArray},
	{"sparsePaths", FieldStringArray},
}

var hookFields = []fieldSpec{
	{"pre_tool_use", FieldHookArray},
	{"PreToolUse", FieldHookArray},
	{"post_tool_use", FieldHookArray},
	{"PostToolUse", FieldHookArray},
	{"post_tool_use_failure", FieldHookArray},
	{"PostToolUseFailure", FieldHookArray},
	{"permission_request", FieldHookArray},
	{"PermissionRequest", FieldHookArray},
	{"permission_denied", FieldHookArray},
	{"PermissionDenied", FieldHookArray},
	{"user_prompt_submit", FieldHookArray},
	{"UserPromptSubmit", FieldHookArray},
	{"session_start", FieldHookArray},
	{"SessionStart", FieldHookArray},
	{"session_end", FieldHookArray},
	{"SessionEnd", FieldHookArray},
	{"setup", FieldHookArray},
	{"Setup", FieldHookArray},
	{"stop", FieldHookArray},
	{"Stop", FieldHookArray},
	{"stop_failure", FieldHookArray},
	{"StopFailure", FieldHookArray},
	{"pre_compact", FieldHookArray},
	{"PreCompact", FieldHookArray},
	{"post_compact", FieldHookArray},
	{"PostCompact", FieldHookArray},
	{"notification", FieldHookArray},
	{"Notification", FieldHookArray},
	{"subagent_start", FieldHookArray},
	{"SubagentStart", FieldHookArray},
	{"subagent_stop", FieldHookArray},
	{"SubagentStop", FieldHookArray},
	{"worktree_create", FieldHookArray},
	{"WorktreeCreate", FieldHookArray},
	{"worktree_remove", FieldHookArray},
	{"WorktreeRemove", FieldHookArray},
	{"cwd_changed", FieldHookArray},
	{"CwdChanged", FieldHookArray},
	{"task_created", FieldHookArray},
	{"TaskCreated", FieldHookArray},
	{"task_completed", FieldHookArray},
	{"TaskCompleted", FieldHookArray},
	{"instructions_loaded", FieldHookArray},
	{"InstructionsLoaded", FieldHookArray},
	{"file_changed", FieldHookArray},
	{"FileChanged", FieldHookArray},
}

var mcpServerFields = []fieldSpec{
	{"command", FieldString},
	{"args", FieldStringArray},
	{"env", FieldStringArrayOrMap},
	{"url", FieldString},
	{"headers", FieldObject},
	{"headers_helper", FieldString},
	{"headersHelper", FieldString},
	{"oauth", FieldObject},
	{"tool_call_timeout_ms", FieldNumber},
	{"toolCallTimeoutMs", FieldNumber},
	{"required", FieldBool},
}

var mcpOAuthFields = []fieldSpec{
	{"clientId", FieldString},
	{"client_id", FieldString},
	{"callbackPort", FieldNumber},
	{"callback_port", FieldNumber},
	{"authServerMetadataUrl", FieldString},
	{"auth_server_metadata_url", FieldString},
	{"xaa", FieldBool},
}

var mcpFields = []fieldSpec{
	{"servers", FieldObject},
}

var futureFields = []fieldSpec{
	{"remote_enabled", FieldBool},
	{"remote_auth_token", FieldString},
	{"remote_lease_seconds", FieldNumber},
	{"enterprise_policy", FieldString},
	{"enterprise_policy_public_key", FieldString},
	{"plugin_marketplaces", FieldStringArray},
	{"plugin_marketplace_public_keys", FieldObject},
	{"sandbox_strategy", FieldString},
	{"sandbox", FieldObject},
	{"updater_manifest_url", FieldString},
	{"editor_bridge_socket", FieldString},
	{"editor_bridge_token", FieldString},
	{"background_state_path", FieldString},
	{"chrome_default_enabled", FieldBool},
	{"notifications_enabled", FieldBool},
	{"ultrareview_enabled", FieldBool},
	{"slack_app_install_count", FieldNumber},
	{"sticker_order_count", FieldNumber},
	{"extra_usage_visit_count", FieldNumber},
	{"guest_pass_referral_url", FieldString},
	{"guest_pass_visit_count", FieldNumber},
	{"guest_pass_eligibility_cache", FieldObject},
	{"has_visited_passes", FieldBool},
	{"passes_upsell_seen_count", FieldNumber},
	{"passes_last_seen_remaining", FieldNumber},
}

var enterpriseFields = []fieldSpec{
	{"policy", FieldString},
	{"policy_public_key", FieldString},
	{"policyPublicKey", FieldString},
	{"public_key", FieldString},
	{"publicKey", FieldString},
}

var editorBridgeFields = []fieldSpec{
	{"socket", FieldString},
	{"path", FieldString},
	{"token", FieldString},
	{"auth_token", FieldString},
	{"authToken", FieldString},
}

var remoteFields = []fieldSpec{
	{"enabled", FieldBool},
	{"auth_token", FieldString},
	{"authToken", FieldString},
	{"lease_seconds", FieldNumber},
	{"leaseSeconds", FieldNumber},
}

var marketplaceFields = []fieldSpec{
	{"sources", FieldStringArray},
	{"public_keys", FieldObject},
	{"publicKeys", FieldObject},
}

var backgroundFields = []fieldSpec{
	{"state_path", FieldString},
	{"statePath", FieldString},
	{"worker_state_path", FieldString},
	{"workerStatePath", FieldString},
}

var compatibilityFields = []fieldSpec{
	{"slack_app_install_count", FieldNumber},
	{"slackAppInstallCount", FieldNumber},
	{"sticker_order_count", FieldNumber},
	{"stickerOrderCount", FieldNumber},
	{"extra_usage_visit_count", FieldNumber},
	{"extraUsageVisitCount", FieldNumber},
	{"guest_pass_referral_url", FieldString},
	{"guestPassReferralURL", FieldString},
	{"guest_pass_visit_count", FieldNumber},
	{"guestPassVisitCount", FieldNumber},
	{"guest_pass_eligibility_cache", FieldObject},
	{"guestPassEligibilityCache", FieldObject},
	{"passes_eligibility_cache", FieldObject},
	{"passesEligibilityCache", FieldObject},
	{"has_visited_passes", FieldBool},
	{"hasVisitedPasses", FieldBool},
	{"passes_upsell_seen_count", FieldNumber},
	{"passesUpsellSeenCount", FieldNumber},
	{"passes_last_seen_remaining", FieldNumber},
	{"passesLastSeenRemaining", FieldNumber},
}

var preferencesFields = []fieldSpec{
	{"chrome_default_enabled", FieldBool},
	{"chromeDefaultEnabled", FieldBool},
	{"notifications_enabled", FieldBool},
	{"notificationsEnabled", FieldBool},
	{"ultrareview_enabled", FieldBool},
	{"ultrareviewEnabled", FieldBool},
	{"ultraReviewEnabled", FieldBool},
}

var updaterFields = []fieldSpec{
	{"manifest_url", FieldString},
	{"manifestURL", FieldString},
	{"url", FieldString},
}

var sandboxFields = []fieldSpec{
	{"strategy", FieldString},
	{"enabled", FieldBool},
	{"namespace_restrictions", FieldBool},
	{"namespaceRestrictions", FieldBool},
	{"network_isolation", FieldBool},
	{"networkIsolation", FieldBool},
	{"isolate_network", FieldBool},
	{"isolateNetwork", FieldBool},
	{"filesystem_mode", FieldString},
	{"filesystemMode", FieldString},
	{"allowed_mounts", FieldStringArray},
	{"allowedMounts", FieldStringArray},
}

func ValidateFiles(paths []string) Report {
	report := Report{
		Kind:    "config_validation",
		Status:  "ok",
		Paths:   append([]string(nil), paths...),
		Results: make([]Result, 0, len(paths)),
	}
	for _, path := range paths {
		result := ValidateFile(path)
		report.Results = append(report.Results, result)
		report.FileCount++
		if result.Present {
			report.PresentCount++
		}
		report.ErrorCount += result.ErrorCount
		report.WarningCount += result.WarningCount
	}
	if report.ErrorCount > 0 {
		report.Status = "error"
	} else if report.WarningCount > 0 {
		report.Status = "warning"
	}
	return report
}

func ValidateFile(path string) Result {
	result := Result{
		Kind:    "config_validation",
		Path:    path,
		Present: true,
		Status:  "ok",
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		result.Present = false
		result.Status = "missing"
		return result
	}
	if strings.EqualFold(filepath.Ext(trimmed), ".toml") {
		if _, err := os.Stat(trimmed); err != nil {
			if os.IsNotExist(err) {
				result.Present = false
				result.Status = "missing"
				return result
			}
			result.addError(Diagnostic{
				Path:    path,
				Field:   "$",
				Kind:    "read_error",
				Message: err.Error(),
			})
			return result
		}
		result.addError(Diagnostic{
			Path:     path,
			Field:    "$",
			Kind:     "unsupported_format",
			Message:  fmt.Sprintf("%s: TOML config files are not supported. Use JSON instead.", path),
			Expected: "JSON config file",
			Got:      "TOML config file",
		})
		return result
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		if os.IsNotExist(err) {
			result.Present = false
			result.Status = "missing"
			return result
		}
		result.addError(Diagnostic{
			Path:    path,
			Field:   "$",
			Kind:    "read_error",
			Message: err.Error(),
		})
		return result
	}
	return ValidateBytes(data, path)
}

func ValidateBytes(data []byte, path string) Result {
	result := Result{
		Kind:    "config_validation",
		Path:    path,
		Present: true,
		Status:  "ok",
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return result
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		result.addError(Diagnostic{
			Path:    path,
			Field:   "$",
			Kind:    "parse_error",
			Message: err.Error(),
		})
		return result
	}
	object, ok := value.(map[string]any)
	if !ok {
		result.addError(Diagnostic{
			Path:     path,
			Field:    "$",
			Kind:     "wrong_type",
			Message:  "config root must be an object",
			Expected: "an object",
			Got:      jsonTypeLabel(value),
		})
		return result
	}
	validateObject(&result, object, topLevelFields, "", data, path)
	addDeprecatedWarnings(&result, object, deprecatedTopLevelFields, "", data, path)
	validateDefaultShellValue(&result, object, data, path)
	validateKnownNestedObjects(&result, object, data, path)
	if result.ErrorCount > 0 {
		result.Status = "error"
	} else if result.WarningCount > 0 {
		result.Status = "warning"
	}
	return result
}

func (r *Result) addError(diagnostic Diagnostic) {
	if strings.TrimSpace(diagnostic.Message) == "" {
		diagnostic.Message = diagnosticText(diagnostic)
	}
	r.Errors = append(r.Errors, diagnostic)
	r.ErrorCount = len(r.Errors)
	r.Status = "error"
}

func (r *Result) addWarning(diagnostic Diagnostic) {
	if strings.TrimSpace(diagnostic.Message) == "" {
		diagnostic.Message = diagnosticText(diagnostic)
	}
	r.Warnings = append(r.Warnings, diagnostic)
	r.WarningCount = len(r.Warnings)
	if r.Status == "ok" {
		r.Status = "warning"
	}
}

func validateKnownNestedObjects(result *Result, object map[string]any, source []byte, path string) {
	if nested, ok := objectAt(object, "permission_rules"); ok {
		validateObject(result, nested, permissionRuleFields, "permission_rules", source, path)
	}
	if nested, ok := objectAt(object, "permissions"); ok {
		validateObject(result, nested, permissionRuleFields, "permissions", source, path)
	}
	if background, ok := objectAt(object, "background"); ok {
		validateObject(result, background, backgroundFields, "background", source, path)
	}
	if nested, ok := objectAt(object, "privacy_settings"); ok {
		validateObject(result, nested, privacyFields, "privacy_settings", source, path)
	}
	if nested, ok := objectAt(object, "rate_limit"); ok {
		validateObject(result, nested, rateLimitFields, "rate_limit", source, path)
	}
	if nested, ok := objectAt(object, "apiTimeout"); ok {
		validateObject(result, nested, apiTimeoutFields, "apiTimeout", source, path)
	}
	if nested, ok := objectAt(object, "providerFallbacks"); ok {
		validateObject(result, nested, providerFallbackFields, "providerFallbacks", source, path)
	}
	if compatibility, ok := objectAt(object, "compatibility"); ok {
		validateObject(result, compatibility, compatibilityFields, "compatibility", source, path)
	}
	if bridge, ok := objectAt(object, "editor_bridge"); ok {
		validateObject(result, bridge, editorBridgeFields, "editor_bridge", source, path)
	}
	if enterprise, ok := objectAt(object, "enterprise"); ok {
		validateObject(result, enterprise, enterpriseFields, "enterprise", source, path)
	}
	if nested, ok := objectAt(object, "statusLine"); ok {
		validateObject(result, nested, statusLineFields, "statusLine", source, path)
		validateStatusLineObject(result, nested, source, path)
	}
	if nested, ok := objectAt(object, "worktree"); ok {
		validateObject(result, nested, worktreeFields, "worktree", source, path)
	}
	if nested, ok := objectAt(object, "hooks"); ok {
		validateObject(result, nested, hookFields, "hooks", source, path)
	}
	if nested, ok := objectAt(object, "mcp"); ok {
		validateObject(result, nested, mcpFields, "mcp", source, path)
		if servers, ok := objectAt(nested, "servers"); ok {
			validateMCPServers(result, servers, source, path, "mcp.servers")
		}
	}
	if nested, ok := objectAt(object, "mcp_servers"); ok {
		validateMCPServers(result, nested, source, path, "mcp_servers")
	}
	if nested, ok := objectAt(object, "mcpServers"); ok {
		validateMCPServers(result, nested, source, path, "mcpServers")
	}
	if marketplace, ok := objectAt(object, "marketplace"); ok {
		validateObject(result, marketplace, marketplaceFields, "marketplace", source, path)
	}
	if preferences, ok := objectAt(object, "preferences"); ok {
		validateObject(result, preferences, preferencesFields, "preferences", source, path)
	}
	if updater, ok := objectAt(object, "updater"); ok {
		validateObject(result, updater, updaterFields, "updater", source, path)
	}
	if nested, ok := objectAt(object, "future"); ok {
		validateObject(result, nested, futureFields, "future", source, path)
		if sandbox, ok := objectAt(nested, "sandbox"); ok {
			validateObject(result, sandbox, sandboxFields, "future.sandbox", source, path)
		}
	}
	if remote, ok := objectAt(object, "remote"); ok {
		validateObject(result, remote, remoteFields, "remote", source, path)
	}
	if sandbox, ok := objectAt(object, "sandbox"); ok {
		validateObject(result, sandbox, sandboxFields, "sandbox", source, path)
	}
}

func validateObject(result *Result, object map[string]any, knownFields []fieldSpec, prefix string, source []byte, path string) {
	known := map[string]fieldSpec{}
	names := make([]string, 0, len(knownFields))
	for _, spec := range knownFields {
		known[spec.Name] = spec
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := object[key]
		field := joinField(prefix, key)
		spec, ok := known[key]
		if !ok {
			suggestion := suggestField(key, names)
			result.addWarning(Diagnostic{
				Path:       path,
				Field:      field,
				Line:       findKeyLine(source, key),
				Kind:       "unknown_key",
				Suggestion: suggestion,
			})
			continue
		}
		if !matchesFieldType(spec.Expected, value) {
			result.addError(Diagnostic{
				Path:     path,
				Field:    field,
				Line:     findKeyLine(source, key),
				Kind:     "wrong_type",
				Expected: spec.Expected.label(),
				Got:      jsonTypeLabel(value),
			})
		}
	}
}

func addDeprecatedWarnings(result *Result, object map[string]any, fields []deprecatedField, prefix string, source []byte, path string) {
	for _, field := range fields {
		if _, ok := object[field.Name]; !ok {
			continue
		}
		result.addWarning(Diagnostic{
			Path:        path,
			Field:       joinField(prefix, field.Name),
			Line:        findKeyLine(source, field.Name),
			Kind:        "deprecated",
			Replacement: field.Replacement,
		})
	}
}

func validateDefaultShellValue(result *Result, object map[string]any, source []byte, path string) {
	value, ok := object["defaultShell"]
	if !ok {
		return
	}
	shell, ok := value.(string)
	if !ok {
		return
	}
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "", "bash", "powershell":
		return
	default:
		result.addError(Diagnostic{
			Path:     path,
			Field:    "defaultShell",
			Line:     findKeyLine(source, "defaultShell"),
			Kind:     "invalid_value",
			Expected: `"bash" or "powershell"`,
			Got:      fmt.Sprintf("%q", shell),
		})
	}
}

func validateMCPServers(result *Result, servers map[string]any, source []byte, path string, prefix string) {
	keys := make([]string, 0, len(servers))
	for name := range servers {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		serverObject, ok := servers[name].(map[string]any)
		if !ok {
			result.addError(Diagnostic{
				Path:     path,
				Field:    joinField(prefix, name),
				Line:     findKeyLine(source, name),
				Kind:     "wrong_type",
				Expected: "an object",
				Got:      jsonTypeLabel(servers[name]),
			})
			continue
		}
		validateObject(result, serverObject, mcpServerFields, joinField(prefix, name), source, path)
		if oauthObject, ok := objectAt(serverObject, "oauth"); ok {
			validateObject(result, oauthObject, mcpOAuthFields, joinField(joinField(prefix, name), "oauth"), source, path)
		}
	}
}

func validateStatusLineObject(result *Result, statusLine map[string]any, source []byte, path string) {
	value, ok := statusLine["type"]
	if !ok {
		result.addError(Diagnostic{
			Path:     path,
			Field:    "statusLine.type",
			Kind:     "missing_required",
			Expected: `"command"`,
			Got:      "missing",
		})
	} else if typ, ok := value.(string); ok {
		if typ != "command" {
			result.addError(Diagnostic{
				Path:     path,
				Field:    "statusLine.type",
				Line:     findKeyLine(source, "type"),
				Kind:     "invalid_value",
				Expected: `"command"`,
				Got:      fmt.Sprintf("%q", typ),
			})
		}
	}
	command, ok := statusLine["command"]
	if !ok {
		result.addError(Diagnostic{
			Path:     path,
			Field:    "statusLine.command",
			Kind:     "missing_required",
			Expected: "a non-empty string",
			Got:      "missing",
		})
		return
	}
	commandText, ok := command.(string)
	if !ok {
		return
	}
	if strings.TrimSpace(commandText) == "" {
		result.addError(Diagnostic{
			Path:     path,
			Field:    "statusLine.command",
			Line:     findKeyLine(source, "command"),
			Kind:     "missing_required",
			Expected: "a non-empty string",
			Got:      "empty string",
		})
	}
}

func objectAt(object map[string]any, key string) (map[string]any, bool) {
	value, ok := object[key]
	if !ok {
		return nil, false
	}
	nested, ok := value.(map[string]any)
	return nested, ok
}

func matchesFieldType(expected FieldType, value any) bool {
	switch expected {
	case FieldString:
		_, ok := value.(string)
		return ok
	case FieldBool:
		_, ok := value.(bool)
		return ok
	case FieldObject:
		_, ok := value.(map[string]any)
		return ok
	case FieldStringMap:
		values, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, item := range values {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	case FieldStringArray:
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range values {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	case FieldHookArray:
		_, ok := value.([]any)
		return ok
	case FieldNumber:
		switch value.(type) {
		case json.Number, float64, int, int64:
			return true
		default:
			return false
		}
	case FieldStringOrStringArray:
		if _, ok := value.(string); ok {
			return true
		}
		return matchesFieldType(FieldStringArray, value)
	case FieldStringArrayOrMap:
		if matchesFieldType(FieldStringArray, value) {
			return true
		}
		return matchesFieldType(FieldStringMap, value)
	case FieldRulesImport:
		if value, ok := value.(string); ok {
			value = strings.ToLower(strings.TrimSpace(value))
			return value == "auto" || value == "none"
		}
		return matchesFieldType(FieldStringArray, value)
	default:
		return true
	}
}

func (t FieldType) label() string {
	switch t {
	case FieldString:
		return "a string"
	case FieldBool:
		return "a boolean"
	case FieldObject:
		return "an object"
	case FieldStringMap:
		return "an object with string values"
	case FieldStringArray:
		return "an array of strings"
	case FieldHookArray:
		return "an array of strings or hook objects"
	case FieldNumber:
		return "a number"
	case FieldStringOrStringArray:
		return "a string or an array of strings"
	case FieldStringArrayOrMap:
		return "an array of strings or an object with string values"
	case FieldRulesImport:
		return `"auto", "none", or an array of framework names`
	default:
		return "a valid value"
	}
}

func jsonTypeLabel(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case json.Number, float64, int, int64:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return "a value"
	}
}

func diagnosticText(d Diagnostic) string {
	location := ""
	if d.Line != nil {
		location = fmt.Sprintf(" (line %d)", *d.Line)
	}
	switch d.Kind {
	case "unknown_key":
		if d.Suggestion != "" {
			return fmt.Sprintf("%s: unknown key %q%s. Did you mean %q?", d.Path, d.Field, location, d.Suggestion)
		}
		return fmt.Sprintf("%s: unknown key %q%s", d.Path, d.Field, location)
	case "wrong_type":
		return fmt.Sprintf("%s: field %q must be %s, got %s%s", d.Path, d.Field, d.Expected, d.Got, location)
	case "deprecated":
		return fmt.Sprintf("%s: field %q is deprecated%s. Use %q instead.", d.Path, d.Field, location, d.Replacement)
	default:
		if d.Message != "" {
			return d.Message
		}
		return fmt.Sprintf("%s: %s at %s%s", d.Path, d.Kind, d.Field, location)
	}
}

func FormatDiagnostics(result Result) string {
	lines := make([]string, 0, result.WarningCount+result.ErrorCount)
	for _, warning := range result.Warnings {
		lines = append(lines, "warning: "+warning.Message)
	}
	for _, err := range result.Errors {
		lines = append(lines, "error: "+err.Message)
	}
	return strings.Join(lines, "\n")
}

func suggestField(input string, candidates []string) string {
	normalized := strings.ToLower(input)
	best := ""
	bestDistance := 4
	for _, candidate := range candidates {
		distance := editDistance(normalized, strings.ToLower(candidate))
		if distance < bestDistance {
			bestDistance = distance
			best = candidate
		}
	}
	if bestDistance <= 3 {
		return best
	}
	return ""
}

func editDistance(left, right string) int {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	if len(leftRunes) == 0 {
		return len(rightRunes)
	}
	if len(rightRunes) == 0 {
		return len(leftRunes)
	}
	previous := make([]int, len(rightRunes)+1)
	current := make([]int, len(rightRunes)+1)
	for i := range previous {
		previous[i] = i
	}
	for leftIndex, leftRune := range leftRunes {
		current[0] = leftIndex + 1
		for rightIndex, rightRune := range rightRunes {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[rightIndex+1] = min(previous[rightIndex+1]+1, current[rightIndex]+1, previous[rightIndex]+cost)
		}
		copy(previous, current)
	}
	return previous[len(rightRunes)]
}

func findKeyLine(source []byte, key string) *int {
	needle, _ := json.Marshal(key)
	index := 0
	for {
		offset := bytes.Index(source[index:], needle)
		if offset < 0 {
			return nil
		}
		absolute := index + offset
		after := absolute + len(needle)
		rest := bytes.TrimLeft(source[after:], " \t\r\n")
		if len(rest) > 0 && rest[0] == ':' {
			line := bytes.Count(source[:absolute], []byte("\n")) + 1
			return &line
		}
		index = after
	}
}

func joinField(prefix, key string) string {
	prefix = strings.TrimSpace(prefix)
	key = strings.TrimSpace(key)
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}
	return prefix + "." + key
}
