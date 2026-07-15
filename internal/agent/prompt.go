package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bookmarks"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/contextview"
	"github.com/Rememorio/codog/internal/cron"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/promptrefs"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/slash"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/team"
	"github.com/Rememorio/codog/internal/terminalsetup"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/usage"
)

func renderCodeIntelLSPPayload(out io.Writer, format string, payload any) error {
	if format == "json" {
		data, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	switch report := payload.(type) {
	case codeIntelLSPActionsReport:
		renderCodeIntelLSPActionsText(out, report)
	case codeIntelLSPDiscoverReport:
		renderCodeIntelLSPDiscoverText(out, report)
	case codeIntelLSPListReport:
		renderCodeIntelLSPListText(out, report)
	case codeintel.LSPServerStatus:
		renderCodeIntelLSPServerStatusText(out, report)
	case codeintel.LSPQueryResult:
		renderCodeIntelLSPQueryText(out, report)
	default:
		data, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Fprintln(out, string(data))
	}
	return nil
}

func renderCodeIntelLSPActionsText(out io.Writer, report codeIntelLSPActionsReport) {
	fmt.Fprintln(out, "LSP Actions")
	fmt.Fprintf(out, "  Count            %d\n", report.Count)
	for _, action := range report.Actions {
		method := action.Method
		if method == "" {
			method = "notification"
		}
		fmt.Fprintf(out, "  %-18s %-34s %s\n", action.Name, method, action.Description)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func renderCodeIntelLSPDiscoverText(out io.Writer, report codeIntelLSPDiscoverReport) {
	fmt.Fprintln(out, "LSP Discover")
	fmt.Fprintf(out, "  Count            %d\n", report.Count)
	for _, candidate := range report.Candidates {
		state := "missing"
		if candidate.Installed {
			state = "installed"
		}
		command := strings.TrimSpace(strings.Join(append([]string{candidate.Command}, candidate.Args...), " "))
		if candidate.Path != "" {
			command = candidate.Path
			if len(candidate.Args) > 0 {
				command += " " + strings.Join(candidate.Args, " ")
			}
		}
		fmt.Fprintf(out, "  %-12s %-9s %-36s %s\n", candidate.Language, state, command, candidate.Description)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func renderCodeIntelLSPListText(out io.Writer, report codeIntelLSPListReport) {
	fmt.Fprintln(out, "LSP Servers")
	fmt.Fprintf(out, "  Count            %d\n", report.Count)
	if len(report.Servers) == 0 {
		fmt.Fprintln(out, "  Servers          none")
	} else {
		for _, server := range report.Servers {
			fmt.Fprintf(out, "  %-12s %-10s task=%s command=%s\n", server.Language, server.Task.Status, server.TaskID, server.Command)
		}
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func renderCodeIntelLSPServerStatusText(out io.Writer, status codeintel.LSPServerStatus) {
	fmt.Fprintln(out, "LSP Server")
	fmt.Fprintf(out, "  Language         %s\n", status.Language)
	fmt.Fprintf(out, "  Status           %s\n", status.Task.Status)
	fmt.Fprintf(out, "  Task             %s\n", status.TaskID)
	fmt.Fprintf(out, "  Workspace        %s\n", status.Workspace)
	fmt.Fprintf(out, "  Command          %s\n", status.Command)
}

func renderCodeIntelLSPQueryText(out io.Writer, result codeintel.LSPQueryResult) {
	fmt.Fprintln(out, "LSP Query")
	fmt.Fprintf(out, "  Language         %s\n", result.Language)
	fmt.Fprintf(out, "  Action           %s\n", result.Action)
	fmt.Fprintf(out, "  Method           %s\n", result.Method)
	fmt.Fprintf(out, "  Path             %s\n", result.Path)
	fmt.Fprintf(out, "  Changed          %t\n", result.Changed)
	fmt.Fprintf(out, "  Text edits       %d\n", result.TextEdits)
	fmt.Fprintf(out, "  File edits       %d\n", result.FileEdits)
}

func lspListMessage(statuses []codeintel.LSPServerStatus) string {
	if len(statuses) == 0 {
		return "No language servers are recorded for this workspace."
	}
	return "Language servers are recorded for this workspace."
}

type codeIntelLSPDiscoverReport struct {
	Kind       string                   `json:"kind"`
	Action     string                   `json:"action"`
	Status     string                   `json:"status"`
	Count      int                      `json:"count"`
	Candidates []codeintel.LSPCandidate `json:"candidates"`
	Message    string                   `json:"message,omitempty"`
}

type codeIntelLSPListReport struct {
	Kind    string                      `json:"kind"`
	Action  string                      `json:"action"`
	Status  string                      `json:"status"`
	Count   int                         `json:"count"`
	Servers []codeintel.LSPServerStatus `json:"servers"`
	Message string                      `json:"message,omitempty"`
}

type codeIntelLSPActionsReport struct {
	Kind    string                    `json:"kind"`
	Action  string                    `json:"action"`
	Status  string                    `json:"status"`
	Count   int                       `json:"count"`
	Actions []codeintel.LSPActionInfo `json:"actions"`
	Message string                    `json:"message,omitempty"`
}

func (a *App) Prompt(ctx context.Context, input string, overrides config.FlagOverrides) error {
	return a.PromptWithOutput(ctx, input, overrides, "text")
}

func (a *App) PromptWithOutput(ctx context.Context, input string, overrides config.FlagOverrides, format string) error {
	return a.promptWithOutput(ctx, input, overrides, format, false)
}

func (a *App) promptWithOutput(ctx context.Context, input string, overrides config.FlagOverrides, format string, compact bool) error {
	return a.promptWithOutputOptions(ctx, input, overrides, format, compact, turnOptions{})
}

func (a *App) promptWithOutputOptions(ctx context.Context, input string, overrides config.FlagOverrides, format string, compact bool, opts turnOptions) error {
	format, err := normalizePromptOutputFormat(format)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt is empty")
	}
	if err := a.RegisterMCPTools(ctx); err != nil {
		return err
	}
	restoreSessions := a.disableSessionPersistenceForPrompt(overrides.NoSessionPersistence)
	if restoreSessions != nil {
		defer restoreSessions()
	}
	sess, err := a.openPromptSession(ctx, input, overrides, format)
	if err != nil {
		return err
	}
	priorMessageCount := len(sess.Messages)
	var streamCapture bytes.Buffer
	turnOut, err := a.promptTurnOutput(format, compact, opts, sess, &streamCapture)
	if err != nil {
		return err
	}
	turnOpts := opts
	turnOpts.Out = turnOut
	runErr := a.runPromptTurn(ctx, sess, input, overrides, turnOpts)
	endReason, err := a.finishPromptSession(ctx, sess, runErr)
	if err != nil {
		return err
	}
	if runErr != nil {
		return a.renderPromptRunError(runErr, format)
	}
	if err := a.validatePromptResponse(sess, streamCapture.String(), opts.JSONSchema, format); err != nil {
		return err
	}
	return a.renderPromptResult(sess, streamCapture.String(), overrides, format, compact, endReason, priorMessageCount, opts.Verbose)
}

func normalizePromptOutputFormat(format string) (string, error) {
	format = strings.TrimSpace(strings.ToLower(format))
	if format == "" {
		return "text", nil
	}
	switch format {
	case "text", "json", "stream-json":
		return format, nil
	default:
		return "", fmt.Errorf("unknown prompt output format %q", format)
	}
}

func (a *App) openPromptSession(ctx context.Context, input string, overrides config.FlagOverrides, format string) (*session.Session, error) {
	sess, err := a.openSession(overrides)
	if err != nil {
		if strings.TrimSpace(overrides.Resume) == "" {
			return nil, err
		}
		return nil, renderSessionRestoreError(a.Out, "prompt", overrides.Resume, err, format)
	}
	if err := a.ensureSessionIdentity(sess, "prompt", input, overrides.SessionName); err != nil {
		return nil, err
	}
	if err := a.runSessionStartHook(ctx, sess, sessionStartSource(overrides)); err != nil {
		return nil, err
	}
	return sess, nil
}

func (a *App) promptTurnOutput(format string, compact bool, opts turnOptions, sess *session.Session, capture *bytes.Buffer) (io.Writer, error) {
	if compact || format == "json" {
		return capture, nil
	}
	if format != "stream-json" {
		return a.Out, nil
	}
	writer := promptStreamJSONWriter{Out: a.Out, IncludeDeltas: opts.IncludePartialMessages || opts.Verbose}
	if err := writer.Event("start", map[string]any{"session_id": sess.ID, "mode": "prompt"}); err != nil {
		return nil, err
	}
	if err := writePromptReplayEvents(writer, sess.ID, opts.ReplayUserMessages); err != nil {
		return nil, err
	}
	return writer, nil
}

func writePromptReplayEvents(writer promptStreamJSONWriter, sessionID string, messages []promptStreamJSONReplayMessage) error {
	for _, message := range messages {
		if err := writer.Event("user", map[string]any{
			"session_id":         sessionID,
			"message":            message.Message,
			"parent_tool_use_id": message.ParentToolUseID,
			"isReplay":           true,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runPromptTurn(ctx context.Context, sess *session.Session, input string, overrides config.FlagOverrides, opts turnOptions) error {
	if overrides.MaxBudgetUSD != nil && *overrides.MaxBudgetUSD > 0 {
		opts.MaxBudgetUSD = *overrides.MaxBudgetUSD
		if priorCost, ok := a.sessionActualCostUSD(sess.ID, a.Config.Model); ok {
			opts.PriorCostUSD = priorCost
			if priorCost >= opts.MaxBudgetUSD {
				return runloop.BudgetExceededError{LimitUSD: opts.MaxBudgetUSD, CostUSD: priorCost}
			}
		}
	}
	return a.runSessionTurnWithOptions(ctx, "prompt", sess, input, "completed", opts)
}

func (a *App) finishPromptSession(ctx context.Context, sess *session.Session, runErr error) (string, error) {
	reason := "completed"
	if runErr != nil {
		reason = "error"
	}
	endErr := a.runSessionEndHook(ctx, sess, reason)
	if endErr == nil {
		return reason, nil
	}
	if runErr == nil {
		return reason, endErr
	}
	if a.Err != nil {
		fmt.Fprintf(a.Err, "session end hook error: %v\n", endErr)
	}
	return reason, runErr
}

func (a *App) renderPromptRunError(runErr error, format string) error {
	if format == "json" {
		return renderCLIError(a.Out, runErr, format)
	}
	return runErr
}

func (a *App) validatePromptResponse(sess *session.Session, streamed string, rawSchema string, format string) error {
	if strings.TrimSpace(rawSchema) == "" {
		return nil
	}
	response := strings.TrimSpace(streamed)
	if response == "" {
		response = strings.TrimSpace(lastAssistantText(sess.Messages))
	}
	if err := validatePromptJSONSchema(response, rawSchema); err != nil {
		return a.renderPromptSchemaError(err, format)
	}
	return nil
}

func (a *App) renderPromptSchemaError(err error, format string) error {
	if format == "stream-json" {
		writer := promptStreamJSONWriter{Out: a.Out}
		if writeErr := writer.Event("error", buildCLIErrorReport(err)); writeErr != nil {
			return writeErr
		}
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	if format == "json" {
		return renderCLIError(a.Out, err, format)
	}
	return err
}

func (a *App) renderPromptResult(sess *session.Session, streamed string, overrides config.FlagOverrides, format string, compact bool, endReason string, priorMessageCount int, verbose bool) error {
	if compact {
		report := promptCompactOutputReport(a.Sessions, sess, a.Config.Model, priorMessageCount)
		return a.renderCompactPromptResult(report, format)
	}
	if format == "json" || format == "stream-json" {
		report := promptOutputReportWithOptions(a.Sessions, sess, a.Config.Model, streamed, endReason, verbose)
		if format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		writer := promptStreamJSONWriter{Out: a.Out}
		return writer.Event("result", report)
	}
	if !overrides.NoSessionPersistence {
		fmt.Fprintf(a.Err, "\n\nsession: %s\n", sess.ID)
	}
	return nil
}

func (a *App) renderCompactPromptResult(report promptCompactReport, format string) error {
	switch format {
	case "json":
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "stream-json":
		return (promptStreamJSONWriter{Out: a.Out}).Event("result", report)
	default:
		fmt.Fprintln(a.Out, report.Message)
		return nil
	}
}

func (a *App) disableSessionPersistenceForPrompt(disabled bool) func() {
	if !disabled || a.Sessions == nil {
		return nil
	}
	previous := a.Sessions
	ephemeral := *previous
	ephemeral.PersistenceDisabled = true
	a.Sessions = &ephemeral
	return func() {
		a.Sessions = previous
	}
}

type promptReport struct {
	Kind         string              `json:"kind"`
	Action       string              `json:"action"`
	Status       string              `json:"status"`
	SessionID    string              `json:"session_id"`
	MessageCount int                 `json:"message_count"`
	Response     string              `json:"response"`
	Usage        usage.Summary       `json:"usage"`
	CostUSD      float64             `json:"cost_usd"`
	Messages     []anthropic.Message `json:"messages,omitempty"`
}

type promptCompactReport struct {
	Message string             `json:"message"`
	Compact bool               `json:"compact"`
	Model   string             `json:"model"`
	Usage   promptCompactUsage `json:"usage"`
}

type promptCompactUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

func promptOutputReportWithOptions(store *session.Store, sess *session.Session, model string, streamed string, status string, verbose bool) promptReport {
	response := strings.TrimSpace(streamed)
	if response == "" && sess != nil {
		response = strings.TrimSpace(lastAssistantText(sess.Messages))
	}
	messageCount := 0
	sessionID := ""
	messages := []anthropic.Message(nil)
	sessionMessages := []anthropic.Message(nil)
	if sess != nil {
		messageCount = len(sess.Messages)
		sessionID = sess.ID
		sessionMessages = sess.Messages
		if verbose {
			messages = append([]anthropic.Message(nil), sess.Messages...)
		}
	}
	summary := promptOutputUsageSummary(store, sessionID, model, sessionMessages)
	return promptReport{
		Kind:         "prompt",
		Action:       "run",
		Status:       status,
		SessionID:    sessionID,
		MessageCount: messageCount,
		Response:     response,
		Usage:        summary,
		CostUSD:      summary.EstimatedUSD,
		Messages:     messages,
	}
}

func promptOutputUsageSummary(store *session.Store, sessionID string, model string, messages []anthropic.Message) usage.Summary {
	if store != nil && strings.TrimSpace(sessionID) != "" {
		entries, err := store.Usage(sessionID)
		if err == nil {
			actual := make([]anthropic.Usage, 0, len(entries))
			for _, entry := range entries {
				actual = append(actual, entry.Usage)
			}
			if summary, ok := usage.ActualSummary(actual, model); ok {
				return summary
			}
		}
	}
	return usage.Estimate(messages, model)
}

func promptCompactOutputReport(store *session.Store, sess *session.Session, model string, priorMessageCount int) promptCompactReport {
	message := ""
	messages := []anthropic.Message{}
	sessionID := ""
	if sess != nil {
		message = strings.TrimSpace(lastAssistantText(sess.Messages))
		messages = sess.Messages
		sessionID = sess.ID
	}
	return promptCompactReport{
		Message: message,
		Compact: true,
		Model:   strings.TrimSpace(model),
		Usage:   promptCompactUsageForTurn(store, sessionID, model, messages, priorMessageCount),
	}
}

func promptCompactUsageForTurn(store *session.Store, sessionID string, model string, messages []anthropic.Message, priorMessageCount int) promptCompactUsage {
	if store != nil && strings.TrimSpace(sessionID) != "" {
		entries, err := store.Usage(sessionID)
		if err == nil {
			actual := []anthropic.Usage{}
			for _, entry := range entries {
				if entry.MessageIndex >= priorMessageCount {
					actual = append(actual, entry.Usage)
				}
			}
			if summary, ok := usage.ActualSummary(actual, model); ok {
				return promptCompactUsageFromSummary(summary)
			}
		}
	}
	if priorMessageCount < 0 || priorMessageCount > len(messages) {
		priorMessageCount = 0
	}
	summary := usage.Estimate(messages[priorMessageCount:], model)
	return promptCompactUsageFromSummary(summary)
}

func promptCompactUsageFromSummary(summary usage.Summary) promptCompactUsage {
	return promptCompactUsage{
		InputTokens:              summary.InputTokens,
		OutputTokens:             summary.OutputTokens,
		CacheCreationInputTokens: summary.CacheCreationInputTokens,
		CacheReadInputTokens:     summary.CacheReadInputTokens,
	}
}

func lastAssistantText(messages []anthropic.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "assistant" {
			continue
		}
		var builder strings.Builder
		for _, block := range messages[index].Content {
			if block.Type == "text" {
				builder.WriteString(block.Text)
			}
		}
		return builder.String()
	}
	return ""
}

type promptJSONSchemaValidationError struct {
	Path   string
	Reason string
}

func (e promptJSONSchemaValidationError) Error() string {
	path := strings.TrimSpace(e.Path)
	if path == "" {
		path = "$"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "response does not match schema"
	}
	return fmt.Sprintf("json_schema_validation_failed: %s: %s", path, reason)
}

func validatePromptJSONSchema(response string, rawSchema string) error {
	var schema any
	if err := decodeJSONValue(rawSchema, &schema); err != nil {
		return invalidFlagValueError{
			Flag:    "--json-schema",
			Value:   "",
			Message: "--json-schema must be valid JSON",
			Usage:   "codog -p --json-schema '{\"type\":\"object\"}' --output-format json",
		}
	}
	var value any
	if err := decodeJSONValue(response, &value); err != nil {
		return promptJSONSchemaValidationError{Path: "$", Reason: "response is not valid JSON"}
	}
	return validateJSONSchemaValue(value, schema, "$")
}

func decodeJSONValue(raw string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func validateJSONSchemaValue(value any, schema any, path string) error {
	switch typed := schema.(type) {
	case bool:
		if typed {
			return nil
		}
		return promptJSONSchemaValidationError{Path: path, Reason: "schema rejects all values"}
	case map[string]any:
		return validateJSONSchemaObject(value, typed, path)
	default:
		return promptJSONSchemaValidationError{Path: path, Reason: "schema must be a JSON object or boolean"}
	}
}

func validateJSONSchemaObject(value any, schema map[string]any, path string) error {
	validators := []func(any, map[string]any, string) error{
		validateJSONSchemaEnum,
		validateJSONSchemaType,
		validateJSONSchemaRequired,
		validateJSONSchemaProperties,
		validateJSONSchemaItems,
	}
	for _, validate := range validators {
		if err := validate(value, schema, path); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONSchemaEnum(value any, schema map[string]any, path string) error {
	raw, exists := schema["enum"]
	if !exists {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		return promptJSONSchemaValidationError{Path: path, Reason: "schema enum must be an array"}
	}
	for _, candidate := range values {
		if jsonValuesEqual(value, candidate) {
			return nil
		}
	}
	return promptJSONSchemaValidationError{Path: path, Reason: "value is not in enum"}
}

func validateJSONSchemaType(value any, schema map[string]any, path string) error {
	raw, exists := schema["type"]
	if !exists {
		return nil
	}
	allowed, err := schemaTypeNames(raw)
	if err != nil {
		return promptJSONSchemaValidationError{Path: path, Reason: err.Error()}
	}
	if jsonValueMatchesAnyType(value, allowed) {
		return nil
	}
	reason := fmt.Sprintf("expected %s, got %s", strings.Join(allowed, " or "), jsonValueTypeName(value))
	return promptJSONSchemaValidationError{Path: path, Reason: reason}
}

func validateJSONSchemaRequired(value any, schema map[string]any, path string) error {
	raw, exists := schema["required"]
	if !exists {
		return nil
	}
	objectValue, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	required, err := schemaStringArray(raw, "required")
	if err != nil {
		return promptJSONSchemaValidationError{Path: path, Reason: err.Error()}
	}
	for _, name := range required {
		if _, exists := objectValue[name]; !exists {
			return promptJSONSchemaValidationError{Path: joinJSONPath(path, name), Reason: "required property is missing"}
		}
	}
	return nil
}

func validateJSONSchemaProperties(value any, schema map[string]any, path string) error {
	raw, exists := schema["properties"]
	if !exists {
		return nil
	}
	objectValue, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	properties, ok := raw.(map[string]any)
	if !ok {
		return promptJSONSchemaValidationError{Path: path, Reason: "schema properties must be an object"}
	}
	if err := validateJSONSchemaPropertyValues(objectValue, properties, path); err != nil {
		return err
	}
	return validateJSONSchemaAdditionalProperties(objectValue, properties, schema, path)
}

func validateJSONSchemaPropertyValues(value map[string]any, properties map[string]any, path string) error {
	for name, propertySchema := range properties {
		propertyValue, exists := value[name]
		if !exists {
			continue
		}
		if err := validateJSONSchemaValue(propertyValue, propertySchema, joinJSONPath(path, name)); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONSchemaAdditionalProperties(value map[string]any, properties map[string]any, schema map[string]any, path string) error {
	additional, explicitlyBoolean := schema["additionalProperties"].(bool)
	if !explicitlyBoolean || additional {
		return nil
	}
	for name := range value {
		if _, known := properties[name]; !known {
			return promptJSONSchemaValidationError{Path: joinJSONPath(path, name), Reason: "additional property is not allowed"}
		}
	}
	return nil
}

func validateJSONSchemaItems(value any, schema map[string]any, path string) error {
	itemsSchema, exists := schema["items"]
	if !exists {
		return nil
	}
	arrayValue, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, item := range arrayValue {
		if err := validateJSONSchemaValue(item, itemsSchema, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func schemaTypeNames(raw any) ([]string, error) {
	switch typed := raw.(type) {
	case string:
		return []string{typed}, nil
	case []any:
		values := []string{}
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				return nil, errors.New("schema type array must contain strings")
			}
			values = append(values, name)
		}
		return values, nil
	default:
		return nil, errors.New("schema type must be a string or string array")
	}
}

func schemaStringArray(raw any, field string) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("schema %s must be an array", field)
	}
	values := []string{}
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("schema %s must contain strings", field)
		}
		values = append(values, value)
	}
	return values, nil
}

func jsonValueMatchesAnyType(value any, allowed []string) bool {
	for _, name := range allowed {
		switch strings.TrimSpace(name) {
		case "null":
			if value == nil {
				return true
			}
		case "boolean":
			_, ok := value.(bool)
			if ok {
				return true
			}
		case "object":
			_, ok := value.(map[string]any)
			if ok {
				return true
			}
		case "array":
			_, ok := value.([]any)
			if ok {
				return true
			}
		case "number":
			if _, ok := value.(json.Number); ok {
				return true
			}
		case "integer":
			if number, ok := value.(json.Number); ok && jsonNumberIsInteger(number) {
				return true
			}
		case "string":
			_, ok := value.(string)
			if ok {
				return true
			}
		}
	}
	return false
}

func jsonNumberIsInteger(number json.Number) bool {
	value := number.String()
	if strings.ContainsAny(value, ".eE") {
		floatValue, err := strconv.ParseFloat(value, 64)
		return err == nil && math.Trunc(floatValue) == floatValue
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func jsonValueTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case json.Number:
		return "number"
	case string:
		return "string"
	default:
		return "unknown"
	}
}

func jsonValuesEqual(left any, right any) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return reflect.DeepEqual(left, right)
	}
	return bytes.Equal(leftData, rightData)
}

func joinJSONPath(path string, key string) string {
	if path == "" {
		path = "$"
	}
	if isJSONPathIdentifier(key) {
		return path + "." + key
	}
	encoded, _ := json.Marshal(key)
	return path + "[" + string(encoded) + "]"
}

func isJSONPathIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if index == 0 {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' {
				continue
			}
			return false
		}
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

type promptStreamJSONWriter struct {
	Out           io.Writer
	IncludeDeltas bool
}

func (w promptStreamJSONWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !w.IncludeDeltas {
		return len(p), nil
	}
	if err := w.Event("assistant_delta", map[string]any{"delta": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w promptStreamJSONWriter) Event(event string, payload any) error {
	if w.Out == nil {
		return nil
	}
	data, err := json.Marshal(map[string]any{
		"type":    event,
		"payload": payload,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w.Out, string(data))
	return err
}

type btwRequest struct {
	Question  string
	SessionID string
	Format    string
}

type btwReport struct {
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	SessionID       string `json:"session_id"`
	SourceSessionID string `json:"source_session_id,omitempty"`
	Question        string `json:"question"`
	Output          string `json:"output,omitempty"`
}

func (a *App) BTW(ctx context.Context, args []string, overrides config.FlagOverrides, active *session.Session) error {
	req, err := parseBTWArgs(args, overrides)
	if err != nil {
		return err
	}
	if a.Sessions == nil {
		return errors.New("session store is unavailable")
	}
	if a.Tools == nil {
		return errors.New("tool registry is not initialized")
	}
	if err := a.RegisterMCPTools(ctx); err != nil {
		return err
	}
	source, err := a.btwSourceSession(req.SessionID, active)
	if err != nil {
		return err
	}
	side, err := a.btwSideSession(source)
	if err != nil {
		return err
	}
	if err := a.ensureSessionIdentity(side, "btw", req.Question, ""); err != nil {
		return err
	}
	turnOut := a.Out
	var bufferedTurnOut bytes.Buffer
	if req.Format == "json" {
		turnOut = &bufferedTurnOut
	}
	if err := a.runSessionTurnWithOptions(ctx, "btw", side, req.Question, "completed", turnOptions{Out: turnOut}); err != nil {
		return err
	}
	if req.Format == "json" {
		sourceID := ""
		if source != nil {
			sourceID = source.ID
		}
		report := btwReport{
			Kind:            "btw",
			Action:          "run",
			Status:          "completed",
			SessionID:       side.ID,
			SourceSessionID: sourceID,
			Question:        req.Question,
			Output:          strings.TrimSpace(bufferedTurnOut.String()),
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintf(a.Err, "\n\nbtw session: %s\n", side.ID)
	if source != nil && strings.TrimSpace(source.ID) != "" {
		fmt.Fprintf(a.Err, "source session: %s\n", source.ID)
	}
	return nil
}

func parseBTWArgs(args []string, overrides config.FlagOverrides) (btwRequest, error) {
	req := btwRequest{SessionID: firstNonEmpty(overrides.Resume, overrides.SessionID), Format: "text"}
	questionParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			questionParts = append(questionParts, args[index+1:]...)
			index = len(args)
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("btw output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case len(questionParts) == 0 && arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("btw session id is required")
			}
			req.SessionID = args[index]
		case len(questionParts) == 0 && strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case len(questionParts) == 0 && arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("btw resume session id is required")
			}
			req.SessionID = args[index]
		case len(questionParts) == 0 && strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case len(questionParts) == 0 && strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown btw flag %q", arg)
		default:
			questionParts = append(questionParts, arg)
		}
	}
	req.Question = strings.TrimSpace(strings.Join(questionParts, " "))
	if req.Question == "" {
		return req, errors.New("usage: codog btw QUESTION [--session ID|--resume ID]")
	}
	if req.SessionID == "true" {
		req.SessionID = "latest"
	}
	format, err := normalizeOutputFormat("btw", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = format
	return req, nil
}

func (a *App) btwSourceSession(sessionID string, active *session.Session) (*session.Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" && active != nil && strings.TrimSpace(active.ID) != "" {
		return active, nil
	}
	if sessionID == "" {
		return nil, nil
	}
	if session.IsSessionReferenceAlias(sessionID) {
		latest, err := a.Sessions.LatestID()
		if err != nil {
			return nil, err
		}
		sessionID = latest
	}
	if active != nil && active.ID == sessionID {
		return active, nil
	}
	return a.Sessions.Open(sessionID)
}

func (a *App) btwSideSession(source *session.Session) (*session.Session, error) {
	if source != nil && strings.TrimSpace(source.ID) != "" {
		exists, err := a.Sessions.Exists(source.ID)
		if err != nil {
			return nil, err
		}
		if exists {
			forked, err := a.Sessions.Fork(source.ID, "btw")
			if err != nil {
				return nil, err
			}
			forked.Messages = append([]anthropic.Message(nil), source.Messages...)
			return forked, nil
		}
	}
	side, err := a.Sessions.Open("")
	if err != nil {
		return nil, err
	}
	if source != nil {
		side.Messages = append([]anthropic.Message(nil), source.Messages...)
	}
	return side, nil
}

type turnOptions struct {
	Skill                  *skills.Skill
	Attachments            []string
	AllowedTools           []string
	ReplayUserMessages     []promptStreamJSONReplayMessage
	IncludePartialMessages bool
	Verbose                bool
	JSONSchema             string
	MaxBudgetUSD           float64
	PriorCostUSD           float64
	Out                    io.Writer
	ConfigurePrompter      func(*tools.Prompter)
	OnToolStart            func(runloop.ToolCall)
	OnToolUse              func(runloop.ToolCall)
}

type promptCLIRequest struct {
	Prompt                 string
	Format                 string
	InputFormat            string
	ReplayUserMessages     bool
	IncludePartialMessages bool
	Verbose                bool
	JSONSchema             string
	MaxBudgetUSD           *float64
	PromptProvided         bool
	Compact                bool
	UseStdin               bool
	Attachments            []string
}

func parsePromptArgs(args []string) (promptCLIRequest, error) {
	parser := promptArgParser{req: promptCLIRequest{Format: "text", InputFormat: "text"}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if parser.consumeTerminator(args, &index) || parser.consumeBoolean(arg) {
			continue
		}
		handled, err := consumeValueOption(args, &index, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		parser.req.PromptProvided = true
		parser.parts = append(parser.parts, arg)
	}
	if err := parser.finish(); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

type promptArgParser struct {
	req   promptCLIRequest
	parts []string
}

func (p *promptArgParser) consumeTerminator(args []string, index *int) bool {
	if args[*index] != "--" {
		return false
	}
	remaining := args[*index+1:]
	p.req.PromptProvided = len(remaining) > 0
	p.parts = append(p.parts, remaining...)
	*index = len(args)
	return true
}

func (p *promptArgParser) consumeBoolean(arg string) bool {
	switch arg {
	case "--json":
		p.req.Format = "json"
	case "--replay-user-messages":
		p.req.ReplayUserMessages = true
	case "--include-partial-messages":
		p.req.IncludePartialMessages = true
	case "--verbose", "-v":
		p.req.Verbose = true
	case "--compact":
		p.req.Compact = true
	case "--stdin", "--prompt-stdin":
		p.req.UseStdin = true
	default:
		return false
	}
	return true
}

func (p *promptArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format": stringValueOption(&p.req.Format, "prompt output format is required"),
		"-o":              stringValueOption(&p.req.Format, "prompt output format is required"),
		"--input-format":  p.stringOption(&p.req.InputFormat, promptInputFormatMissing),
		"--json-schema":   p.stringOption(&p.req.JSONSchema, promptJSONSchemaMissing),
		"--max-budget-usd": {
			missing: promptBudgetMissing,
			set:     p.setBudget,
		},
		"--attach":     p.attachmentOption(),
		"--attachment": p.attachmentOption(),
		"--file":       p.attachmentOption(),
	}
}

func (p *promptArgParser) stringOption(target *string, missing func(string) error) valueOption {
	return valueOption{missing: missing, set: func(value string) error {
		*target = value
		return nil
	}}
}

func promptInputFormatMissing(flag string) error {
	return missingFlagValueError{Command: "prompt", Flag: flag, Usage: "codog -p --input-format text|stream-json --output-format stream-json"}
}

func promptJSONSchemaMissing(flag string) error {
	return missingFlagValueError{Command: "prompt", Flag: flag, Usage: "codog -p --json-schema '{\"type\":\"object\"}' --output-format json"}
}

func promptBudgetMissing(flag string) error {
	return missingFlagValueError{Command: "prompt", Flag: flag, Usage: "codog -p --max-budget-usd 1.50 \"<prompt>\""}
}

func (p *promptArgParser) setBudget(value string) error {
	budget, err := parsePromptMaxBudgetUSD(value)
	if err != nil {
		return err
	}
	p.req.MaxBudgetUSD = &budget
	return nil
}

func (p *promptArgParser) attachmentOption() valueOption {
	return valueOption{missing: func(string) error { return errors.New("prompt attachment path is required") }, set: func(value string) error {
		p.req.Attachments = append(p.req.Attachments, value)
		return nil
	}}
}

func (p *promptArgParser) finish() error {
	p.req.Prompt = strings.TrimSpace(strings.Join(p.parts, " "))
	normalized, err := normalizeOutputFormat("prompt", p.req.Format, []string{"text", "json", "stream-json"})
	if err != nil {
		return err
	}
	p.req.Format = normalized
	inputFormat, err := normalizePromptInputFormat(p.req.InputFormat)
	if err != nil {
		return err
	}
	p.req.InputFormat = inputFormat
	return p.validateModes()
}

func (p *promptArgParser) validateModes() error {
	if p.req.InputFormat == "stream-json" && p.req.Format != "stream-json" {
		return invalidFlagValueError{
			Flag:    "--input-format",
			Value:   p.req.InputFormat,
			Message: "--input-format=stream-json requires --output-format=stream-json",
			Usage:   "codog -p --input-format stream-json --output-format stream-json",
		}
	}
	if p.req.ReplayUserMessages && (p.req.InputFormat != "stream-json" || p.req.Format != "stream-json") {
		return invalidFlagValueError{
			Flag:    "--replay-user-messages",
			Value:   "",
			Message: "--replay-user-messages requires --input-format=stream-json and --output-format=stream-json",
			Usage:   "codog -p --input-format stream-json --output-format stream-json --replay-user-messages",
		}
	}
	if p.req.IncludePartialMessages && p.req.Format != "stream-json" {
		return invalidFlagValueError{
			Flag:    "--include-partial-messages",
			Value:   p.req.Format,
			Message: "--include-partial-messages requires --output-format=stream-json",
			Usage:   "codog -p --output-format stream-json --include-partial-messages \"<prompt>\"",
		}
	}
	if p.req.Compact && strings.TrimSpace(p.req.JSONSchema) != "" {
		return invalidFlagValueError{
			Flag:    "--json-schema",
			Value:   "",
			Message: "--json-schema cannot be used with --compact",
			Usage:   "codog -p --json-schema '{\"type\":\"object\"}' --output-format json",
		}
	}
	return nil
}

func parsePromptMaxBudgetUSD(value string) (float64, error) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= 0 {
		return 0, invalidFlagValueError{
			Flag:    "--max-budget-usd",
			Value:   value,
			Message: "--max-budget-usd must be greater than 0",
			Usage:   "codog -p --max-budget-usd 1.50 \"<prompt>\"",
		}
	}
	return parsed, nil
}

func normalizePromptInputFormat(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "text", nil
	}
	lower := strings.ToLower(value)
	switch lower {
	case "text", "stream-json":
		return lower, nil
	default:
		return "", invalidFlagValueError{
			Flag:    "--input-format",
			Value:   value,
			Message: fmt.Sprintf("unknown prompt input format %q; expected text or stream-json", value),
			Usage:   "codog -p --input-format text|stream-json",
		}
	}
}

func parseAttachSlashArgs(args []string) (string, []string, error) {
	attachments := []string{}
	promptParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if arg == "--" {
			promptParts = append(promptParts, args[index+1:]...)
			break
		}
		if strings.HasPrefix(arg, "--") {
			return "", nil, fmt.Errorf("unknown /attach option %q", arg)
		}
		attachments = append(attachments, arg)
	}
	if len(attachments) == 0 {
		return "", nil, errors.New("usage: /attach PATH [PATH...] [-- PROMPT]")
	}
	prompt := strings.TrimSpace(strings.Join(promptParts, " "))
	if prompt == "" {
		prompt = "Inspect the attached file(s) and summarize what matters."
	}
	return prompt, attachments, nil
}

func readPromptInput(in io.Reader) (string, error) {
	input, _, err := readPromptInputState(in)
	return input, err
}

func readPromptInputState(in io.Reader) (string, bool, error) {
	if in == nil {
		return "", false, nil
	}
	nonTerminal := true
	if file, ok := in.(*os.File); ok {
		info, err := file.Stat()
		if err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return "", false, nil
		}
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return "", nonTerminal, err
	}
	return string(data), nonTerminal, nil
}

type promptStreamJSONInput struct {
	Prompt         string
	ReplayMessages []promptStreamJSONReplayMessage
}

type promptStreamJSONReplayMessage struct {
	Message         anthropic.Message `json:"message"`
	ParentToolUseID *string           `json:"parent_tool_use_id"`
	IsReplay        bool              `json:"isReplay"`
}

func readPromptStreamJSONInput(in io.Reader) (string, error) {
	state, err := readPromptStreamJSONInputState(in)
	if err != nil {
		return "", err
	}
	return state.Prompt, nil
}

func readPromptStreamJSONInputState(in io.Reader) (promptStreamJSONInput, error) {
	if in == nil {
		return promptStreamJSONInput{}, nil
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	parts := []string{}
	replayMessages := []promptStreamJSONReplayMessage{}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		message, ok, err := promptMessageFromSDKUserMessageLine([]byte(line))
		if err != nil {
			return promptStreamJSONInput{}, fmt.Errorf("invalid stream-json input line %d: %w", lineNumber, err)
		}
		text := promptTextFromAnthropicContent(message.Content)
		if ok && strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
			replayMessages = append(replayMessages, promptStreamJSONReplayMessage{
				Message:  message,
				IsReplay: true,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return promptStreamJSONInput{}, err
	}
	return promptStreamJSONInput{Prompt: strings.Join(parts, "\n\n"), ReplayMessages: replayMessages}, nil
}

func promptMessageFromSDKUserMessageLine(line []byte) (anthropic.Message, bool, error) {
	var msg struct {
		Type            string          `json:"type"`
		Message         json.RawMessage `json:"message"`
		ParentToolUseID *string         `json:"parent_tool_use_id"`
		IsSynthetic     bool            `json:"isSynthetic"`
		IsReplay        bool            `json:"isReplay"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return anthropic.Message{}, false, err
	}
	if msg.Type != "user" || msg.ParentToolUseID != nil || msg.IsSynthetic || msg.IsReplay {
		return anthropic.Message{}, false, nil
	}
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(msg.Message, &message); err != nil {
		return anthropic.Message{}, false, err
	}
	if message.Role != "" && message.Role != "user" {
		return anthropic.Message{}, false, nil
	}
	content, ok, err := promptContentFromSDKContent(message.Content)
	if err != nil || !ok {
		return anthropic.Message{}, ok, err
	}
	return anthropic.Message{Role: "user", Content: content}, true, nil
}

func promptContentFromSDKContent(content json.RawMessage) ([]anthropic.ContentBlock, bool, error) {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return []anthropic.ContentBlock{{Type: "text", Text: text}}, true, nil
	}
	var blocks []anthropic.ContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, false, err
	}
	filtered := []anthropic.ContentBlock{}
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			filtered = append(filtered, block)
		}
	}
	if len(filtered) == 0 {
		return nil, false, nil
	}
	return filtered, true, nil
}

func promptTextFromAnthropicContent(content []anthropic.ContentBlock) string {
	parts := []string{}
	for _, block := range content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

func mergePromptWithStdin(prompt string, stdin string) string {
	prompt = strings.TrimSpace(prompt)
	stdin = strings.TrimSpace(stdin)
	if stdin == "" {
		return prompt
	}
	if prompt == "" {
		return stdin
	}
	return prompt + "\n\n" + stdin
}

const (
	maxPromptAttachmentBytes          int64 = 5 * 1024 * 1024
	maxPromptDirectoryAttachmentBytes int64 = 5 * 1024 * 1024
	maxPromptDirectoryAttachmentFiles       = 64
)

func (a *App) promptContentBlocks(prompt string, attachmentPaths []string) ([]anthropic.ContentBlock, error) {
	blocks := []anthropic.ContentBlock{{Type: "text", Text: prompt}}
	for _, attachmentPath := range attachmentPaths {
		block, err := a.promptAttachmentBlock(attachmentPath)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func (a *App) promptAttachmentBlock(attachmentPath string) (anthropic.ContentBlock, error) {
	displayPath := strings.TrimSpace(attachmentPath)
	if displayPath == "" {
		return anthropic.ContentBlock{}, errors.New("prompt attachment path is required")
	}
	path := displayPath
	if !filepath.IsAbs(path) && strings.TrimSpace(a.Workspace) != "" {
		path = filepath.Join(a.Workspace, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return anthropic.ContentBlock{}, fmt.Errorf("read prompt attachment %q: %w", displayPath, err)
	}
	if info.IsDir() {
		return a.promptDirectoryAttachmentBlock(displayPath, path)
	}
	if info.Size() > maxPromptAttachmentBytes {
		return anthropic.ContentBlock{}, fmt.Errorf("prompt attachment %q is too large: %d bytes exceeds %d", displayPath, info.Size(), maxPromptAttachmentBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return anthropic.ContentBlock{}, fmt.Errorf("read prompt attachment %q: %w", displayPath, err)
	}
	mediaType := promptAttachmentMediaType(path, data)
	if isPromptImageMediaType(mediaType) {
		return anthropic.ContentBlock{
			Type:  "image",
			Title: displayPath,
			Source: &anthropic.ContentSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      base64.StdEncoding.EncodeToString(data),
			},
		}, nil
	}
	if mediaType == "application/pdf" {
		return anthropic.ContentBlock{
			Type:  "document",
			Title: displayPath,
			Source: &anthropic.ContentSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      base64.StdEncoding.EncodeToString(data),
			},
		}, nil
	}
	if !utf8.Valid(data) {
		return anthropic.ContentBlock{}, fmt.Errorf("prompt attachment %q is not a supported text, image, or PDF file", displayPath)
	}
	text := fmt.Sprintf("<attachment path=%q media_type=%q bytes=%d>\n%s\n</attachment>", displayPath, mediaType, len(data), string(data))
	return anthropic.ContentBlock{Type: "text", Text: text, Title: displayPath}, nil
}

func (a *App) promptDirectoryAttachmentBlock(displayPath string, path string) (anthropic.ContentBlock, error) {
	entries := []promptDirectoryAttachmentEntry{}
	skipped := []string{}
	totalBytes := int64(0)
	err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		rel, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") {
				skipped = append(skipped, rel+"/")
				return filepath.SkipDir
			}
			return nil
		}
		if len(entries) >= maxPromptDirectoryAttachmentFiles {
			skipped = append(skipped, rel)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxPromptAttachmentBytes {
			skipped = append(skipped, rel)
			return nil
		}
		if totalBytes+info.Size() > maxPromptDirectoryAttachmentBytes {
			skipped = append(skipped, rel)
			return nil
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			skipped = append(skipped, rel)
			return nil
		}
		totalBytes += int64(len(data))
		entries = append(entries, promptDirectoryAttachmentEntry{
			Path:      rel,
			MediaType: promptAttachmentMediaType(current, data),
			Bytes:     len(data),
			Content:   string(data),
		})
		return nil
	})
	if err != nil {
		return anthropic.ContentBlock{}, fmt.Errorf("read prompt attachment directory %q: %w", displayPath, err)
	}
	if len(entries) == 0 {
		return anthropic.ContentBlock{}, fmt.Errorf("prompt attachment directory %q has no supported text files", displayPath)
	}
	text := renderPromptDirectoryAttachment(displayPath, entries, skipped, totalBytes)
	return anthropic.ContentBlock{Type: "text", Text: text, Title: displayPath}, nil
}

type promptDirectoryAttachmentEntry struct {
	Path      string
	MediaType string
	Bytes     int
	Content   string
}

func renderPromptDirectoryAttachment(displayPath string, entries []promptDirectoryAttachmentEntry, skipped []string, totalBytes int64) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "<attachment_directory path=%q files=%d bytes=%d", displayPath, len(entries), totalBytes)
	if len(skipped) > 0 {
		fmt.Fprintf(&builder, " skipped=%d", len(skipped))
	}
	builder.WriteString(">\n")
	for _, entry := range entries {
		fmt.Fprintf(&builder, "<file path=%q media_type=%q bytes=%d>\n", entry.Path, entry.MediaType, entry.Bytes)
		builder.WriteString(entry.Content)
		if !strings.HasSuffix(entry.Content, "\n") {
			builder.WriteByte('\n')
		}
		builder.WriteString("</file>\n")
	}
	if len(skipped) > 0 {
		builder.WriteString("<skipped>\n")
		for _, path := range skipped {
			fmt.Fprintf(&builder, "%s\n", path)
		}
		builder.WriteString("</skipped>\n")
	}
	builder.WriteString("</attachment_directory>")
	return builder.String()
}

func promptAttachmentMediaType(path string, data []byte) string {
	if mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); mediaType != "" {
		return cleanMediaType(mediaType)
	}
	if len(data) > 0 {
		return cleanMediaType(http.DetectContentType(data))
	}
	return "text/plain"
}

func cleanMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		mediaType = value
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		return "application/octet-stream"
	}
	return mediaType
}

func isPromptImageMediaType(mediaType string) bool {
	switch cleanMediaType(mediaType) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func (a *App) runSessionTurn(ctx context.Context, mode string, sess *session.Session, input string, successStatus string) error {
	return a.runSessionTurnWithOptions(ctx, mode, sess, input, successStatus, turnOptions{})
}

func (a *App) runSessionTurnWithOptions(ctx context.Context, mode string, sess *session.Session, input string, successStatus string, opts turnOptions) error {
	if err := a.ensureSessionIdentity(sess, mode, input, ""); err != nil {
		return err
	}
	if a.promptHistoryEnabled() {
		if err := a.Sessions.AppendInput(sess.ID, input); err != nil {
			return err
		}
	} else {
		if err := a.Sessions.AppendPromptHistoryDisabled(sess.ID); err != nil {
			return err
		}
	}
	modelInput := input
	activeSkill := opts.Skill
	if activeSkill == nil {
		modelInput, activeSkill = a.expandSkillInvocationWithSkill(input, sess.ID)
	}
	allowedTools := append([]string(nil), opts.AllowedTools...)
	if activeSkill != nil {
		allowedTools = append(allowedTools, activeSkill.AllowedTools...)
	}
	modelInput = a.expandPromptReferences(modelInput)
	userContent := []anthropic.ContentBlock{{Type: "text", Text: modelInput}}
	if len(opts.Attachments) > 0 {
		var err error
		userContent, err = a.promptContentBlocks(modelInput, opts.Attachments)
		if err != nil {
			return err
		}
	}
	a.writeWorkerState(mode, "running", sess, "")
	if err := a.runInstructionsLoadedHooks(ctx, sess.ID, "session_start"); err != nil {
		a.writeWorkerState(mode, "error", sess, err.Error())
		return err
	}
	effectiveConfig := a.effectiveConfig()
	onToolUse := a.onToolUse(sess.ID)
	if opts.OnToolUse != nil {
		baseOnToolUse := onToolUse
		onToolUse = func(call runloop.ToolCall) {
			baseOnToolUse(call)
			opts.OnToolUse(call)
		}
	}
	prompter := a.prompterWithAllowedTools(sess.ID, allowedTools)
	if opts.ConfigurePrompter != nil {
		opts.ConfigurePrompter(prompter)
	}
	runner := runloop.Runner{
		Config:           effectiveConfig,
		Client:           a.Client,
		Tools:            a.Tools,
		Prompter:         prompter,
		HookPromptRunner: a.hookPromptRunner(effectiveConfig),
		Workspace:        a.Workspace,
		SessionID:        sess.ID,
		Out:              firstWriter(opts.Out, a.Out),
		System:           a.systemPromptForInput(input),
		OnToolStart:      opts.OnToolStart,
		OnToolUse:        onToolUse,
		MaxBudgetUSD:     opts.MaxBudgetUSD,
		PriorCostUSD:     opts.PriorCostUSD,
	}
	result, err := runner.RunWithUserContent(ctx, sess.Messages, userContent, modelInput)
	if appendErr := a.appendTurnResult(sess, result); appendErr != nil {
		a.writeWorkerState(mode, "error", sess, appendErr.Error())
		return appendErr
	}
	if err != nil {
		a.writeWorkerState(mode, "error", sess, err.Error())
		return err
	}
	a.writeWorkerState(mode, successStatus, sess, "")
	return nil
}

func (a *App) appendTurnResult(sess *session.Session, result runloop.TurnResult) error {
	if sess == nil || len(result.Messages) <= len(sess.Messages) {
		return nil
	}
	messageUsages := usageByMessageIndex(result.MessageUsages)
	for index, msg := range result.Messages[len(sess.Messages):] {
		messageIndex := len(sess.Messages) + index
		if providerUsage, ok := messageUsages[messageIndex]; ok {
			if err := a.Sessions.AppendWithUsage(sess.ID, msg, &providerUsage); err != nil {
				return err
			}
			continue
		}
		if err := a.Sessions.Append(sess.ID, msg); err != nil {
			return err
		}
	}
	sess.Messages = result.Messages
	return nil
}

func (a *App) hookPromptRunner(cfg config.Config) hooks.PromptRunner {
	return func(ctx context.Context, req hooks.PromptRequest) (string, error) {
		if a.Client == nil {
			return "", errors.New("missing model client")
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return "", errors.New("hook prompt is empty")
		}
		model := firstNonEmpty(strings.TrimSpace(req.Model), cfg.Model, config.DefaultModel)
		maxTokens := cfg.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 1024
		}
		var streamed strings.Builder
		assistant, err := a.Client.Stream(ctx, anthropic.Request{
			Model:           model,
			MaxTokens:       maxTokens,
			Temperature:     cfg.Temperature,
			ReasoningEffort: cfg.ReasoningEffort,
			ExtraBody:       cfg.ExtraBody,
			System:          "You are executing a Codog hook. Evaluate the hook prompt and return a concise result for the calling process.",
			Messages:        []anthropic.Message{anthropic.TextMessage("user", prompt)},
		}, func(delta string) {
			streamed.WriteString(delta)
		})
		if err != nil {
			return "", err
		}
		output := strings.TrimSpace(streamed.String())
		if output != "" {
			return output, nil
		}
		var builder strings.Builder
		for _, block := range assistant.Blocks {
			if block.Type == "text" {
				builder.WriteString(block.Text)
			}
		}
		return strings.TrimSpace(builder.String()), nil
	}
}

func usageByMessageIndex(usages []runloop.MessageUsage) map[int]anthropic.Usage {
	out := make(map[int]anthropic.Usage, len(usages))
	for _, usage := range usages {
		out[usage.MessageIndex] = usage.Usage
	}
	return out
}

func (a *App) expandPromptReferences(input string) string {
	additionalDirs, err := pathscope.EffectiveDirs(a.Workspace, a.Config.AdditionalDirs)
	if err != nil {
		additionalDirs = nil
	}
	return promptrefs.Expand(input, a.Workspace, additionalDirs)
}

func (a *App) expandSkillInvocation(input string) string {
	rendered, _ := a.expandSkillInvocationWithSkill(input, "")
	return rendered
}

func (a *App) expandSkillInvocationWithSkill(input string, sessionID string) (string, *skills.Skill) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return input, nil
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return input, nil
	}
	skill, err := a.findRuntimeSkill(fields[0])
	if err != nil {
		return input, nil
	}
	if !skill.UserInvocable {
		return input, nil
	}
	args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
	rendered := skills.RenderInvocationWithSession(skill, args, sessionID)
	return rendered, &skill
}

func (a *App) REPL(ctx context.Context, overrides config.FlagOverrides) error {
	if err := a.RegisterMCPTools(ctx); err != nil {
		return err
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	if err := a.ensureSessionIdentity(sess, "repl", "", overrides.SessionName); err != nil {
		return err
	}
	if err := a.runSessionStartHook(ctx, sess, sessionStartSource(overrides)); err != nil {
		return err
	}
	a.writeWorkerState("repl", "idle", sess, "")
	a.renderDeepLinkBanner(overrides)
	fmt.Fprintf(a.Err, "Codog %s (%s). Type /help for commands, Tab for completions, /exit to quit.\n", version, sess.ID)
	if rl, ok, err := a.newLineReader(sess.ID); err != nil {
		return err
	} else if ok {
		defer func() { _ = rl.Close() }()
		return a.finishREPL(ctx, sess, a.replReadline(ctx, sess, rl, overrides.Prefill))
	}
	return a.finishREPL(ctx, sess, a.replScanner(ctx, sess))
}

func (a *App) TUI(ctx context.Context, overrides config.FlagOverrides) error {
	if err := a.RegisterMCPTools(ctx); err != nil {
		return err
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	if err := a.ensureSessionIdentity(sess, "tui", "", overrides.SessionName); err != nil {
		return err
	}
	if err := a.runSessionStartHook(ctx, sess, sessionStartSource(overrides)); err != nil {
		return err
	}
	a.writeWorkerState("tui", "idle", sess, "")
	entries := tuiSessionEntries(sess)
	if banner := buildDeepLinkBanner(a.Workspace, overrides, time.Now()); banner != "" {
		entries = append(entries, tui.Entry{Role: "system", Text: banner})
	}
	history := a.tuiPromptHistory(sess.ID)
	fileCandidates := a.tuiFileReferenceCandidates()
	permissionAnswers := make(chan string, 1)
	questionAnswers := make(chan string, 1)
	modeState := newTUIModeState(a.Config)
	if a.planModeActive() {
		a.enterTUIPlanMode(modeState)
	}
	gitBranch, _ := gitops.Branch(a.Workspace)
	submitter := tuiTurnSubmitter{
		app: a, sess: sess, modeState: modeState,
		permissionAnswers: permissionAnswers, questionAnswers: questionAnswers,
	}
	slashHandler := a.tuiSlashHandler(sess, modeState)
	loopErr := tui.Shell(ctx, tui.ShellOptions{
		Candidates:              a.slashMenuCandidates(sess.ID),
		FileCandidates:          fileCandidates,
		Prefill:                 overrides.Prefill,
		InitialPrompt:           overrides.InitialPrompt,
		InitialAttachments:      overrides.InitialAttachments,
		History:                 history,
		Entries:                 entries,
		SubmitStreamAttachments: submitter.submit,
		Slash:                   slashHandler,
		SubmitTextInput: func(ctx context.Context, action string, value string) (tui.RuntimeControlResult, error) {
			return a.submitTUITextInput(ctx, action, value)
		},
		ExternalEditor: func(ctx context.Context, value string) (string, error) {
			return a.editTUIComposer(ctx, value)
		},
		Paste: func(ctx context.Context) (tui.PasteContent, error) {
			return a.readTUIClipboard(ctx)
		},
		Background: func(ctx context.Context, prompt string) (string, error) {
			return a.startTUIBackgroundPrompt(ctx, sess.ID, prompt)
		},
		TaskBoard: func(ctx context.Context) (string, error) {
			return a.renderTUITaskBoard(ctx)
		},
		Todos: func(ctx context.Context) ([]tui.TodoItem, error) {
			return a.readTUITodos(ctx)
		},
		ModelOptions: a.tuiModelOptions(),
		CurrentModel: strings.TrimSpace(a.Config.Model),
		Version:      version,
		Workspace:    a.Workspace,
		GitBranch:    gitBranch,
		ShowWelcome:  len(sess.Messages) == 0,
		SelectModel: func(ctx context.Context, model string) (tui.RuntimeControlResult, error) {
			return a.selectTUIModel(ctx, model)
		},
		SelectPermissionMode: func(ctx context.Context, mode string) (tui.RuntimeControlResult, error) {
			return a.selectTUIPermissionMode(ctx, mode, modeState)
		},
		Theme: effectiveTUITheme(a.Config.Theme),
		SelectTheme: func(ctx context.Context, theme string) (tui.RuntimeControlResult, error) {
			return a.selectTUITheme(ctx, theme)
		},
		ToggleFast: func(ctx context.Context) (tui.RuntimeControlResult, error) {
			return a.toggleTUIFast(ctx)
		},
		ToggleThinking: func(ctx context.Context) (tui.RuntimeControlResult, error) {
			return a.toggleTUIThinking(ctx)
		},
		ToggleVim: func(ctx context.Context) (tui.RuntimeControlResult, error) {
			return a.toggleTUIVim(ctx)
		},
		StopBackground: func(ctx context.Context) (tui.RuntimeControlResult, error) {
			return a.stopTUIBackground(ctx)
		},
		CompactSession: func(ctx context.Context) (tui.RuntimeControlResult, error) {
			return a.compactTUISession(ctx, sess)
		},
		UndoLast: func(ctx context.Context) (tui.RuntimeControlResult, error) {
			return a.undoTUIChange(ctx)
		},
		ExportConversation: func(ctx context.Context) (tui.RuntimeControlResult, error) {
			return a.exportTUIConversation(ctx, sess)
		},
		ExportConversationTo: func(ctx context.Context, filename string) (tui.RuntimeControlResult, error) {
			return a.exportTUIConversationTo(ctx, sess, filename)
		},
		CopyConversation: func(ctx context.Context) (tui.RuntimeControlResult, error) {
			return a.copyTUIConversation(ctx, sess)
		},
		RestoreConversation: func(ctx context.Context, keepMessages int) (tui.RuntimeControlResult, error) {
			return a.restoreTUIConversation(ctx, sess, keepMessages)
		},
		ForkConversation: func(ctx context.Context, keepMessages int) (tui.RuntimeControlResult, error) {
			return a.forkTUIConversation(ctx, sess, keepMessages)
		},
		SummarizeConversation: func(ctx context.Context, keepMessages int) (tui.RuntimeControlResult, error) {
			return a.summarizeTUIConversation(ctx, sess, keepMessages)
		},
		SummarizeUpToConversation: func(ctx context.Context, keepMessages int) (tui.RuntimeControlResult, error) {
			return a.summarizeUpToTUIConversation(ctx, sess, keepMessages)
		},
		CopyMessage: func(ctx context.Context, text string) (tui.RuntimeControlResult, error) {
			return a.copyTUIMessage(ctx, text)
		},
		ModeLabel:          modeState.Label(),
		RuntimeBadges:      a.tuiRuntimeBadges(),
		VimMode:            a.readlineVimMode(),
		Keybindings:        a.tuiKeybindings(),
		ContextKeybindings: a.tuiContextKeybindings(),
		CycleMode: func() string {
			label := modeState.Cycle()
			modeState.Apply(&a.Config)
			return label
		},
		ReadModeLabel: func() string {
			modeState.Sync(a.Config)
			return modeState.Label()
		},
		PermissionRespond: func(response tui.PermissionResponse) {
			select {
			case permissionAnswers <- encodeTUIPermissionResponse(response):
			case <-ctx.Done():
			}
		},
		QuestionAnswer: func(answer string) {
			select {
			case questionAnswers <- answer + "\n":
			case <-ctx.Done():
			}
		},
	})
	return a.finishREPL(ctx, sess, loopErr)
}

type tuiTurnSubmitter struct {
	app               *App
	sess              *session.Session
	modeState         *tuiModeState
	permissionAnswers chan string
	questionAnswers   chan string
}

func (s tuiTurnSubmitter) submit(ctx context.Context, prompt string, attachments []string, emit func(tui.Entry)) (string, error) {
	turn := tuiTurnSubmission{submitter: s, ctx: ctx, emit: emit}
	return turn.run(prompt, attachments)
}

type tuiTurnSubmission struct {
	submitter      tuiTurnSubmitter
	ctx            context.Context
	emit           func(tui.Entry)
	out            bytes.Buffer
	streamOut      tuiStreamWriter
	toolCalls      []runloop.ToolCall
	liveToolEvents bool
}

func (s *tuiTurnSubmission) run(prompt string, attachments []string) (string, error) {
	s.streamOut = tuiStreamWriter{buffer: &s.out, emit: s.emit}
	s.submitter.modeState.Apply(&s.submitter.app.Config)
	restoreQuestionInteractions := s.installQuestionInteractions()
	defer restoreQuestionInteractions()
	defer func() { _ = s.submitter.app.refreshBuiltinToolScope() }()
	err := s.submitter.app.runSessionTurnWithOptions(s.ctx, "tui", s.submitter.sess, prompt, "idle", turnOptions{
		Out: &s.streamOut, Attachments: attachments,
		ConfigurePrompter: s.configurePrompter,
		OnToolStart:       s.onToolStart,
		OnToolUse:         s.onToolUse,
	})
	return s.response(err)
}

func (s *tuiTurnSubmission) installQuestionInteractions() func() {
	questionTool := tools.AskUserQuestionTool{
		In: &lineAnswerReader{answers: s.submitter.questionAnswers, done: s.ctx.Done()}, Out: io.Discard, OnRequest: s.onQuestion,
	}
	s.submitter.app.Tools.Register(questionTool)
	previousMCPOptions := s.submitter.app.Tools.MCPClientOptions()
	interactiveMCPOptions := previousMCPOptions
	interactiveMCPOptions.Elicit = tools.NewMCPElicitationHandler(questionTool)
	s.submitter.app.Tools.SetMCPClientOptions(interactiveMCPOptions)
	return func() { s.submitter.app.Tools.SetMCPClientOptions(previousMCPOptions) }
}

func (s *tuiTurnSubmission) onQuestion(request tools.UserQuestionRequest) {
	s.liveToolEvents = true
	s.emit(tui.Entry{
		Role: "question", Text: renderTUIQuestionRequest(request),
		Question: &tui.QuestionRequest{
			Question: request.Question, Choices: append([]string(nil), request.Choices...),
			Default: request.Default, Questions: tuiQuestions(request.Questions),
		},
	})
}

func (s *tuiTurnSubmission) configurePrompter(prompter *tools.Prompter) {
	prompter.In = &lineAnswerReader{answers: s.submitter.permissionAnswers, done: s.ctx.Done()}
	prompter.Err = io.Discard
	wrapTUIPermissionEvents(prompter, func(entry tui.Entry) {
		s.liveToolEvents = true
		s.emit(entry)
	})
}

func (s *tuiTurnSubmission) onToolStart(call runloop.ToolCall) {
	if summary := renderTUIToolStart(call); summary != "" {
		s.liveToolEvents = true
		s.emit(tui.Entry{Role: "tool", Text: summary, Tool: tuiToolActivity(call, "running")})
	}
}

func (s *tuiTurnSubmission) onToolUse(call runloop.ToolCall) {
	s.toolCalls = append(s.toolCalls, call)
	summary := renderTUIToolSummary([]runloop.ToolCall{call})
	if summary == "" {
		return
	}
	s.liveToolEvents = true
	status := "success"
	if call.IsError {
		status = "error"
	}
	s.emit(tui.Entry{Role: "tool", Text: summary, Tool: tuiToolActivity(call, status)})
}

func (s *tuiTurnSubmission) response(err error) (string, error) {
	response := strings.TrimSpace(s.out.String())
	if response == "" {
		response = strings.TrimSpace(lastAssistantText(s.submitter.sess.Messages))
	}
	if toolSummary := renderTUIToolSummary(s.toolCalls); toolSummary != "" && !s.liveToolEvents {
		if s.streamOut.Emitted() {
			return toolSummary, err
		}
		response = strings.TrimSpace(strings.Join([]string{toolSummary, response}, "\n\n"))
	}
	if s.streamOut.Emitted() || s.liveToolEvents {
		return "", err
	}
	return response, err
}

func resumeSessionChoices(sessions []session.Session) []tui.SessionChoice {
	choices := make([]tui.SessionChoice, 0, len(sessions))
	for _, sess := range sessions {
		title := strings.TrimSpace(sess.Identity.Title)
		if title == "" || title == sess.ID {
			for _, message := range sess.Messages {
				if message.Role != "user" {
					continue
				}
				if text := firstMessageText(message); text != "" {
					title = trimSingleLine(text, 72)
					break
				}
			}
		}
		if title == "" {
			title = sess.ID
		}
		updatedAt := sess.Metadata.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = sess.Metadata.ModifiedAt
		}
		choices = append(choices, tui.SessionChoice{
			ID:           sess.ID,
			Title:        title,
			Tag:          sess.Identity.Tag,
			BranchName:   sess.Metadata.BranchName,
			Workspace:    sess.Identity.Workspace,
			MessageCount: len(sess.Messages),
			UpdatedAt:    updatedAt,
		})
	}
	return choices
}

func (a *App) tuiSlashHandler(sess *session.Session, modeState *tuiModeState) tui.SlashFunc {
	return func(ctx context.Context, line string) (tui.SlashResult, error) {
		return a.handleTUISlash(ctx, line, sess, modeState)
	}
}

func (a *App) handleTUISlash(ctx context.Context, line string, sess *session.Session, modeState *tuiModeState) (tui.SlashResult, error) {
	if result, handled, err := a.tuiInteractiveSlashResult(ctx, line, sess, modeState); handled {
		return result, err
	}
	if isBareTUIResumeCommand(line) {
		return a.tuiResumeSlashResult(sess)
	}
	result, revision := a.executeTUISlash(ctx, line, sess, modeState)
	a.decorateTUISlashResult(&result, line, sess)
	a.syncTUISlashSession(&result, line, sess, revision)
	return result, nil
}

func (a *App) tuiResumeSlashResult(sess *session.Session) (tui.SlashResult, error) {
	choices, err := a.tuiResumeSessionChoices(sess.ID)
	if err != nil {
		return tui.SlashResult{Handled: true}, err
	}
	if len(choices) == 0 {
		return tui.SlashResult{Output: "No conversations found to resume.", Handled: true}, nil
	}
	return tui.SlashResult{Handled: true, SessionChoices: choices}, nil
}

func (a *App) executeTUISlash(ctx context.Context, line string, sess *session.Session, modeState *tuiModeState) (tui.SlashResult, string) {
	revision := ""
	if tuiSlashMayChangeSession(line) {
		revision = tuiSessionRevision(sess)
	}
	var out bytes.Buffer
	oldOut, oldErr := a.Out, a.Err
	a.Out, a.Err = &out, &out
	handled := a.handleSlash(ctx, line, sess)
	a.Out, a.Err = oldOut, oldErr
	result := tui.SlashResult{Output: strings.TrimSpace(out.String()), Handled: handled}
	if handled && !tuiSlashOutputIsError(result.Output) {
		a.syncTUIPlanModeAfterSlash(line, modeState)
	}
	return result, revision
}

func (a *App) decorateTUISlashResult(result *tui.SlashResult, line string, sess *session.Session) {
	if !result.Handled || result.Output == "" || tuiSlashOutputIsError(result.Output) {
		return
	}
	if a.setTUISlashCommandView(result, line, sess) {
		return
	}
	setTUISlashInformation(result, line)
}

func (a *App) setTUISlashCommandView(result *tui.SlashResult, line string, sess *session.Session) bool {
	if view, ok := a.tuiPreferenceRefreshView(line); ok {
		result.CommandView = view
	} else if selectedTab, ok := tuiRuntimeRefreshTab(line); ok {
		view := a.tuiRuntimeCommandView(sess, selectedTab)
		result.CommandView = &view
	} else if selectedTab, ok := tuiExtensionsRefreshTab(line); ok {
		view := a.tuiExtensionsCommandView(selectedTab)
		result.CommandView = &view
	} else if selectedTab, ok := tuiConversationRefreshTab(line); ok {
		view := a.tuiConversationCommandView(sess, selectedTab)
		result.CommandView = &view
	} else if !a.setTUISlashWorkspaceView(result, line) {
		return false
	}
	result.Output = ""
	return true
}

func (a *App) setTUISlashWorkspaceView(result *tui.SlashResult, line string) bool {
	if tuiMemoryRefresh(line) {
		view, err := a.tuiMemoryCommandView()
		if err == nil {
			result.CommandView = view
		}
		return err == nil
	}
	if tuiIDERefresh(line) {
		view, err := a.tuiIDECommandView()
		if err == nil {
			result.CommandView = view
		}
		return err == nil
	}
	return false
}

func setTUISlashInformation(result *tui.SlashResult, line string) {
	if view, ok := tuiSideQuestionInformation(line, result.Output); ok {
		result.Information = &view
		result.Output = ""
		return
	}
	if title, ok := tuiExtensionInformationTitle(line); ok {
		view := tui.InformationView{Title: title, Lines: tuiReportLines(result.Output, title)}
		result.Information = &view
		result.Output = ""
	}
}

func (a *App) syncTUISlashSession(result *tui.SlashResult, line string, sess *session.Session, before string) {
	if !result.Handled || before == "" || before == tuiSessionRevision(sess) {
		return
	}
	state := a.tuiSessionState(sess)
	result.Session = &state
	if tuiSlashOutputIsPersisted(line) {
		result.Output = ""
	}
}

func tuiSlashOutputIsError(output string) bool {
	return strings.HasPrefix(strings.ToLower(output), "error:")
}

type tuiSlashRequest struct {
	command string
	args    []string
	rawArgs string
}

func parseTUISlashRequest(line string) (tuiSlashRequest, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return tuiSlashRequest{}, false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	return tuiSlashRequest{
		command: command,
		args:    fields[1:],
		rawArgs: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0])),
	}, true
}

func (a *App) tuiInteractiveSlashResult(ctx context.Context, line string, sess *session.Session, modeState *tuiModeState) (tui.SlashResult, bool, error) {
	req, ok := parseTUISlashRequest(line)
	if !ok {
		return tui.SlashResult{}, false, nil
	}
	if result, handled, err := a.tuiNavigationSlashResult(req, sess, modeState); handled {
		return result, true, err
	}
	if result, handled, err := a.tuiWorkspaceSlashResult(req, sess); handled {
		return result, true, err
	}
	return a.tuiSessionControlSlashResult(ctx, req, sess, modeState)
}

func (a *App) tuiNavigationSlashResult(req tuiSlashRequest, sess *session.Session, modeState *tuiModeState) (tui.SlashResult, bool, error) {
	switch req.command {
	case "/status", "/config", "/usage":
		if len(req.args) == 0 {
			selectedTab := tuiSettingsTab(req.command)
			view := a.tuiSettingsCommandView(sess, modeState, selectedTab)
			return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
		}
	case "/skill", "/skills", "/mcp", "/hooks", "/plugin", "/plugins", "/marketplace":
		if len(req.args) == 0 {
			selectedTab := tuiExtensionTab(req.command)
			view := a.tuiExtensionsCommandView(selectedTab)
			return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
		}
	case "/agents":
		return a.tuiAgentsSlashResult(req.args, sess)
	case "/subagent":
		return a.tuiSubagentSlashResult(req.args, sess)
	case "/background", "/tasks", "/bashes":
		return a.tuiBackgroundSlashResult(req.args, sess)
	case "/team":
		return a.tuiTeamSlashResult(req.args, sess)
	case "/cron":
		return a.tuiCronSlashResult(req.args, sess)
	case "/history":
		if len(req.args) == 0 {
			view := a.tuiConversationCommandView(sess, 0)
			return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
		}
	case "/sessions":
		if len(req.args) == 0 || (len(req.args) == 1 && normalizeSessionAction(req.args[0]) == "list") {
			view := a.tuiConversationCommandView(sess, 1)
			return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
		}
	case "/bookmarks":
		return a.tuiBookmarksSlashResult(req.args, sess)
	case "/rewind":
		if len(req.args) == 0 {
			return tui.SlashResult{Handled: true, OpenMessageActions: true}, true, nil
		}
	}
	return tui.SlashResult{}, false, nil
}

func tuiSettingsTab(command string) int {
	switch command {
	case "/config":
		return 1
	case "/usage":
		return 2
	default:
		return 0
	}
}

func (a *App) tuiWorkspaceSlashResult(req tuiSlashRequest, sess *session.Session) (tui.SlashResult, bool, error) {
	switch req.command {
	case "/memory":
		return a.tuiMemorySlashResult(req.args)
	case "/doctor":
		if len(req.args) == 0 {
			view, err := a.tuiDoctorInformation()
			return tui.SlashResult{Handled: true, Information: view}, true, err
		}
	case "/statusline":
		if !tuiSlashRequestsJSON(req.args) {
			return tui.SlashResult{Handled: true, Query: a.tuiStatuslineSetupQuery(req.rawArgs)}, true, nil
		}
	case "/files":
		if len(req.args) == 0 {
			view := a.tuiFilesInformation(sess)
			return tui.SlashResult{Handled: true, Information: &view}, true, nil
		}
	case "/terminal-setup":
		if len(req.args) == 0 {
			view, err := a.tuiTerminalSetupCommandView()
			return tui.SlashResult{Handled: true, CommandView: view}, true, err
		}
	case "/keybindings":
		if len(req.args) == 0 {
			view, err := a.tuiKeybindingsCommandView()
			return tui.SlashResult{Handled: true, CommandView: view}, true, err
		}
	case "/ide":
		if len(req.args) == 0 || (len(req.args) == 1 && strings.EqualFold(strings.TrimSpace(req.args[0]), "status")) {
			view, err := a.tuiIDECommandView()
			return tui.SlashResult{Handled: true, CommandView: view}, true, err
		}
	case "/export":
		if len(req.args) == 0 {
			filename := "conversation.md"
			if sess != nil && strings.TrimSpace(sess.ID) != "" {
				filename = safeTUIExportName(sess.ID) + ".md"
			}
			dialog := tui.ExportDialog{DefaultFilename: filename}
			return tui.SlashResult{Handled: true, ExportDialog: &dialog}, true, nil
		}
	case "/add-dir":
		if len(req.args) == 0 {
			dialog := tui.TextInputDialog{
				Title:  "Add working directory",
				Prompt: "Enter an absolute or workspace-relative directory path:",
				Action: "add-dir",
			}
			return tui.SlashResult{Handled: true, TextInputDialog: &dialog}, true, nil
		}
	}
	return tui.SlashResult{}, false, nil
}

func (a *App) tuiSessionControlSlashResult(ctx context.Context, req tuiSlashRequest, sess *session.Session, modeState *tuiModeState) (tui.SlashResult, bool, error) {
	if result, handled, err := a.tuiDisplayControlSlashResult(req, sess, modeState); handled {
		return result, true, err
	}
	return a.tuiConversationActionSlashResult(ctx, req, sess, modeState)
}

func (a *App) tuiDisplayControlSlashResult(req tuiSlashRequest, sess *session.Session, modeState *tuiModeState) (tui.SlashResult, bool, error) {
	switch req.command {
	case "/theme", "/color":
		if len(req.args) == 0 {
			return tui.SlashResult{Handled: true, OpenThemePicker: true}, true, nil
		}
	case "/fast":
		if len(req.args) == 0 {
			view := a.tuiFastCommandView()
			return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
		}
	case "/output-style":
		if len(req.args) == 0 {
			view, err := a.tuiOutputStyleCommandView()
			return tui.SlashResult{Handled: true, CommandView: view}, true, err
		}
	case "/sandbox":
		if len(req.args) == 0 {
			view := a.tuiSandboxCommandView()
			return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
		}
	case "/stats":
		if len(req.args) == 0 {
			view := a.tuiSettingsCommandView(sess, modeState, 2)
			return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
		}
	case "/model":
		if len(req.args) == 0 {
			return tui.SlashResult{Handled: true, OpenModelPicker: true}, true, nil
		}
	case "/todos":
		if len(req.args) == 0 {
			return tui.SlashResult{Handled: true, OpenTodos: true}, true, nil
		}
	case "/permissions":
		if len(req.args) == 0 {
			settings := a.tuiPermissionSettings(modeState)
			return tui.SlashResult{Handled: true, PermissionSettings: &settings}, true, nil
		}
	case "/context":
		if len(req.args) == 0 {
			view := a.tuiContextInformation(sess)
			return tui.SlashResult{Handled: true, Information: &view}, true, nil
		}
	}
	return tui.SlashResult{}, false, nil
}

func (a *App) tuiConversationActionSlashResult(ctx context.Context, req tuiSlashRequest, sess *session.Session, modeState *tuiModeState) (tui.SlashResult, bool, error) {
	switch req.command {
	case "/rename":
		if !tuiSlashHasStructuredFlags(req.args) {
			return a.renameTUISession(sess, req.rawArgs)
		}
	case "/branch":
		if !tuiSlashHasStructuredFlags(req.args) {
			return a.branchTUISession(ctx, sess, req.rawArgs)
		}
	case "/tag":
		return a.tagTUISession(sess, req.rawArgs)
	case "/conversation-tag":
		return a.completeTUISessionTag(sess, req.args)
	case "/plan", "/ultraplan":
		return a.tuiPlanSlashResult(req.rawArgs, modeState)
	case "/compact":
		if len(req.args) == 0 {
			return tui.SlashResult{Handled: true, RuntimeAction: "compact"}, true, nil
		}
	case "/copy":
		if len(req.args) == 0 {
			return tui.SlashResult{Handled: true, RuntimeAction: "copy"}, true, nil
		}
	case "/diff":
		diffReq, err := parseDiffArgs(req.args)
		if err != nil {
			return tui.SlashResult{Handled: true}, true, err
		}
		if diffReq.Format == "json" {
			return tui.SlashResult{}, false, nil
		}
		view, err := a.tuiDiffView(diffReq)
		if err != nil {
			return tui.SlashResult{Handled: true}, true, err
		}
		return tui.SlashResult{Handled: true, Diff: &view}, true, nil
	}
	return tui.SlashResult{}, false, nil
}

func tuiSlashMayChangeSession(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	switch command {
	case "/attach", "/clear", "/compact", "/conversation", "/generatesessionname", "/rename", "/resume", "/rewind", "/sessions":
		return true
	}
	_, builtIn := slash.Lookup(command)
	return !builtIn
}

func tuiSlashOutputIsPersisted(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	if command == "/attach" {
		return true
	}
	_, builtIn := slash.Lookup(command)
	return !builtIn
}

func isBareTUIResumeCommand(line string) bool {
	fields := strings.Fields(line)
	if len(fields) != 1 {
		return false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	return command == "/resume"
}

func (a *App) tuiResumeSessionChoices(activeSessionID string) ([]tui.SessionChoice, error) {
	if a.Sessions == nil {
		return nil, errors.New("session store is not configured")
	}
	sessions, err := a.Sessions.List()
	if err != nil {
		return nil, err
	}
	filtered := sessions[:0]
	for _, candidate := range sessions {
		if candidate.ID == activeSessionID || len(candidate.Messages) == 0 {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return resumeSessionChoices(filtered), nil
}

func (a *App) tuiSessionState(sess *session.Session) tui.SessionState {
	if sess == nil {
		return tui.SessionState{}
	}
	return tui.SessionState{
		ID:         sess.ID,
		Entries:    tuiSessionEntries(sess),
		History:    a.tuiPromptHistory(sess.ID),
		Candidates: a.slashMenuCandidates(sess.ID),
	}
}

func tuiSessionRevision(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	data, _ := json.Marshal(struct {
		Messages []anthropic.Message     `json:"messages"`
		Identity session.SessionIdentity `json:"identity"`
	}{Messages: sess.Messages, Identity: sess.Identity})
	digest := sha256.Sum256(data)
	return sess.ID + ":" + hex.EncodeToString(digest[:])
}

func tuiSessionEntries(sess *session.Session) []tui.Entry {
	if sess == nil {
		return nil
	}
	builder := tuiSessionEntryBuilder{
		entries:     []tui.Entry{{Role: "system", Text: tuiSessionLabel(sess)}},
		toolEntries: map[string]int{},
	}
	for _, message := range sess.Messages {
		builder.addMessage(message)
	}
	return builder.entries
}

func tuiSessionLabel(sess *session.Session) string {
	label := "Session " + sess.ID
	if title := strings.TrimSpace(sess.Identity.Title); title != "" && title != sess.ID {
		label += " · " + title
	}
	if branch := strings.TrimSpace(sess.Metadata.BranchName); branch != "" {
		label += " · " + branch
	}
	if tag := strings.TrimSpace(sess.Identity.Tag); tag != "" {
		label += " · #" + tag
	}
	return label
}

type tuiSessionEntryBuilder struct {
	entries     []tui.Entry
	toolEntries map[string]int
	role        string
	text        []string
}

func (b *tuiSessionEntryBuilder) addMessage(message anthropic.Message) {
	b.role = strings.ToLower(strings.TrimSpace(message.Role))
	if b.role == "" {
		b.role = "system"
	}
	b.text = b.text[:0]
	for _, block := range message.Content {
		b.addBlock(block)
	}
	b.flushText()
}

func (b *tuiSessionEntryBuilder) addBlock(block anthropic.ContentBlock) {
	switch strings.ToLower(strings.TrimSpace(block.Type)) {
	case "text":
		if value := strings.TrimSpace(block.Text); value != "" {
			b.text = append(b.text, value)
		}
	case "image", "document":
		b.text = append(b.text, tuiAttachmentLabel(block))
	case "tool_use":
		b.addToolUse(block)
	case "tool_result":
		b.addToolResult(block)
	}
}

func tuiAttachmentLabel(block anthropic.ContentBlock) string {
	label := "Image attachment"
	if strings.EqualFold(strings.TrimSpace(block.Type), "document") {
		label = "Document attachment"
	}
	if block.Source != nil && strings.TrimSpace(block.Source.MediaType) != "" {
		label += " (" + strings.TrimSpace(block.Source.MediaType) + ")"
	}
	return label
}

func (b *tuiSessionEntryBuilder) addToolUse(block anthropic.ContentBlock) {
	b.flushText()
	activity := &tui.ToolActivity{
		ID: strings.TrimSpace(block.ID), Name: strings.TrimSpace(block.Name),
		Input: strings.TrimSpace(string(block.Input)), Status: "running",
	}
	b.entries = append(b.entries, tui.Entry{Role: "tool", Tool: activity})
	if activity.ID != "" {
		b.toolEntries[activity.ID] = len(b.entries) - 1
	}
}

func (b *tuiSessionEntryBuilder) addToolResult(block anthropic.ContentBlock) {
	b.flushText()
	output := strings.TrimSpace(block.Content)
	status := "success"
	if block.IsError {
		status = "error"
	}
	if index, ok := b.toolEntries[strings.TrimSpace(block.ToolUseID)]; ok && b.entries[index].Tool != nil {
		b.entries[index].Tool.Output = output
		b.entries[index].Tool.Status = status
		b.entries[index].Tool.IsError = block.IsError
		return
	}
	b.entries = append(b.entries, tui.Entry{Role: "tool", Tool: &tui.ToolActivity{
		ID: strings.TrimSpace(block.ToolUseID), Name: "tool", Output: output, Status: status, IsError: block.IsError,
	}})
}

func (b *tuiSessionEntryBuilder) flushText() {
	joined := strings.TrimSpace(strings.Join(b.text, "\n"))
	if joined != "" {
		b.entries = append(b.entries, tui.Entry{Role: b.role, Text: joined})
	}
	b.text = b.text[:0]
}

func (a *App) tuiPermissionSettings(modeState *tuiModeState) tui.PermissionSettings {
	settings := tui.PermissionSettings{
		Allow: append([]string(nil), a.Config.PermissionRules.Allow...),
		Ask:   append([]string(nil), a.Config.PermissionRules.Ask...),
		Deny:  append([]string(nil), a.Config.PermissionRules.Deny...),
	}
	settings.Deny = addRuleValues(settings.Deny, a.Config.PermissionRules.DeniedTools)
	if modeState == nil {
		modeState = newTUIModeState(a.Config)
	}
	for index, option := range modeState.options {
		settings.Modes = append(settings.Modes, tui.PermissionModeOption{
			Name:        option.Label,
			Label:       option.Label,
			Description: tuiPermissionModeDescription(option),
			Current:     index == modeState.index,
		})
	}
	return settings
}

func tuiPermissionModeDescription(option tuiModeOption) string {
	if option.PlanMode {
		return "Plan the work without modifying files or running write-capable tools"
	}
	switch option.PermissionMode {
	case "read-only":
		return "Read and search without modifying the workspace"
	case "workspace-write":
		return "Apply edits inside the workspace and ask for broader access"
	case "allow":
		return "Allow tool calls without confirmation"
	case "danger-full-access":
		return "Allow unrestricted local tool access"
	default:
		return "Ask before tool calls that require approval"
	}
}

func (a *App) tuiContextInformation(sess *session.Session) tui.InformationView {
	report := a.buildContextReport(sess)
	var out bytes.Buffer
	contextview.RenderText(&out, report)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) > 0 && strings.EqualFold(strings.TrimSpace(lines[0]), "context") {
		lines = lines[1:]
	}
	return tui.InformationView{Title: "Context", Lines: lines}
}

func (a *App) tuiSettingsCommandView(sess *session.Session, modeState *tuiModeState, selectedTab int) tui.CommandView {
	var statusOut bytes.Buffer
	localstatus.RenderText(&statusOut, a.statusSnapshot(sess))

	sessionID := ""
	messages := []anthropic.Message(nil)
	if sess != nil {
		sessionID = sess.ID
		messages = sess.Messages
	}
	actual, _ := a.sessionUsageValues(sessionID)
	usageReport := usage.BuildReportWithUsage(sessionID, a.Config.Model, messages, actual)
	var usageOut bytes.Buffer
	usage.RenderText(&usageOut, usageReport)

	permissionMode := a.Config.PermissionMode
	if modeState != nil {
		permissionMode = modeState.Label()
	}
	theme := effectiveTUITheme(a.Config.Theme)
	fast := onOff(fastModeEnabled(a.Config.FastMode))
	thinking := effectiveEffort(a.Config.ReasoningEffort)
	vim := onOff(editorModeIsVim(a.Config.EditorMode))
	outputStyle := "default"
	if report, err := outputstyle.List(a.Config.ConfigHome, a.Workspace); err == nil && strings.TrimSpace(report.Active) != "" {
		outputStyle = strings.TrimSpace(report.Active)
	}

	return tui.CommandView{
		Title:       "Settings",
		SelectedTab: selectedTab,
		Tabs: []tui.CommandViewTab{
			{Title: "Status", Lines: tuiReportLines(statusOut.String(), "status")},
			{
				Title: "Config",
				Items: []tui.CommandViewItem{
					{Label: "Model", Value: emptyAsNone(a.Config.Model), Description: "Select the model used for subsequent turns", Action: "model"},
					{Label: "Permission mode", Value: emptyAsNone(permissionMode), Description: "Choose how tool calls are approved", Command: "/permissions"},
					{Label: "Theme", Value: theme, Description: "Preview and apply a terminal color theme", Action: "theme"},
					{Label: "Output style", Value: outputStyle, Description: "Choose how responses are written", Command: "/output-style"},
					{Label: "Fast mode", Value: fast, Description: "Toggle the fast response preference", Action: "fast"},
					{Label: "Thinking", Value: thinking, Description: "Cycle the reasoning effort for future turns", Action: "thinking"},
					{Label: "Vim mode", Value: vim, Description: "Toggle Vim editing in the prompt composer", Action: "vim"},
				},
			},
			{Title: "Usage", Lines: tuiReportLines(usageOut.String(), "usage")},
		},
	}
}

func (a *App) tuiFastCommandView() tui.CommandView {
	enabled := fastModeEnabled(a.Config.FastMode)
	selected := 1
	if enabled {
		selected = 0
	}
	return tui.CommandView{
		Title:        "Fast mode",
		SelectedItem: selected,
		Tabs: []tui.CommandViewTab{{
			Title: "Preference",
			Lines: []string{"Choose the response speed preference for subsequent turns."},
			Items: []tui.CommandViewItem{
				{Label: "Enabled", Value: currentMarker(enabled), Description: "Prefer the provider's faster response path when available", Command: "/fast on"},
				{Label: "Disabled", Value: currentMarker(!enabled), Description: "Use the standard response path", Command: "/fast off"},
			},
			RefreshCommand: "/fast",
		}},
	}
}

func (a *App) tuiOutputStyleCommandView() (*tui.CommandView, error) {
	report, err := outputstyle.List(a.Config.ConfigHome, a.Workspace)
	if err != nil {
		return nil, err
	}
	selected := 0
	items := []tui.CommandViewItem{{
		Label:       "Default",
		Value:       currentMarker(strings.TrimSpace(report.Active) == ""),
		Description: "Use the standard response style without an additional style prompt",
		Command:     "/output-style clear",
	}}
	for _, style := range report.Styles {
		value := style.Source
		if style.Active {
			value += " · current"
			selected = len(items)
		}
		command := tuiNamedSlashCommand("/output-style set", style.Name)
		description := trimSingleLine(style.Preview, 96)
		if !style.Effective {
			command = ""
			value = style.Source + " · shadowed"
			if strings.TrimSpace(style.ShadowedBy) != "" {
				description = "Shadowed by " + strings.TrimSpace(style.ShadowedBy)
			}
		}
		items = append(items, tui.CommandViewItem{
			Label:       style.Name,
			Value:       value,
			Description: description,
			Command:     command,
		})
	}
	return &tui.CommandView{
		Title:        "Output style",
		SelectedItem: selected,
		Tabs: []tui.CommandViewTab{{
			Title:          "Styles",
			Lines:          []string{fmt.Sprintf("%d styles discovered", len(report.Styles))},
			Items:          items,
			RefreshCommand: "/output-style",
		}},
	}, nil
}

func (a *App) tuiSandboxCommandView() tui.CommandView {
	req := sandboxToggleRequest{Action: "status", Format: "text", Target: "user"}
	report := buildSandboxToggleReport(req, a.Config.Future.SandboxStrategy)
	var out bytes.Buffer
	renderSandboxToggleReport(&out, report)
	configured := strings.ToLower(strings.TrimSpace(report.ConfiguredStrategy))
	selected := 0
	items := []tui.CommandViewItem{
		{Label: "Automatic", Value: currentMarker(configured == "" || configured == "detect"), Description: "Select the best supported sandbox strategy for this platform", Command: "/sandbox-toggle on"},
		{Label: "Disabled", Value: currentMarker(configured == "off"), Description: "Run commands without Codog's process sandbox wrapper", Command: "/sandbox-toggle off"},
	}
	if configured == "off" {
		selected = 1
	}
	seen := map[string]bool{"detect": true, "off": true}
	for _, strategy := range report.Strategies {
		strategy = strings.ToLower(strings.TrimSpace(strategy))
		if strategy == "" || seen[strategy] {
			continue
		}
		seen[strategy] = true
		if strategy == configured {
			selected = len(items)
		}
		items = append(items, tui.CommandViewItem{
			Label:       strategy,
			Value:       currentMarker(strategy == configured),
			Description: "Use this sandbox implementation when available",
			Command:     "/sandbox-toggle " + strategy,
		})
	}
	items = append(items, tui.CommandViewItem{
		Label:       "Clear override",
		Description: "Remove the persisted strategy and return to configuration defaults",
		Command:     "/sandbox-toggle clear",
	})
	return tui.CommandView{
		Title:        "Sandbox",
		SelectedItem: selected,
		Tabs: []tui.CommandViewTab{{
			Title:          "Configuration",
			Lines:          tuiReportLines(out.String(), "Sandbox Toggle"),
			Items:          items,
			RefreshCommand: "/sandbox",
		}},
	}
}

func currentMarker(current bool) string {
	if current {
		return "current"
	}
	return ""
}

func tuiExtensionTab(command string) int {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "/mcp":
		return 1
	case "/hooks":
		return 2
	case "/plugin", "/plugins", "/marketplace":
		return 3
	case "/agents":
		return 4
	default:
		return 0
	}
}

func (a *App) tuiExtensionsCommandView(selectedTab int) tui.CommandView {
	return tui.CommandView{
		Title:       "Extensions",
		SelectedTab: selectedTab,
		Tabs: []tui.CommandViewTab{
			a.tuiSkillsCommandTab(),
			a.tuiMCPCommandTab(),
			a.tuiHooksCommandTab(),
			a.tuiPluginsCommandTab(),
			a.tuiAgentsCommandTab(),
		},
	}
}

func (a *App) tuiSkillsCommandTab() tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Skills", RefreshCommand: "/skills"}
	all, err := a.runtimeSkills()
	if err != nil {
		tab.Lines = []string{"Skills unavailable: " + err.Error()}
		return tab
	}
	tab.Lines = []string{fmt.Sprintf("%d discovered", len(all))}
	for _, skill := range all {
		state := "available"
		description := firstNonEmpty(trimSingleLine(skill.Description, 96), skill.Source)
		command := tuiNamedSlashCommand("/skills show", skill.Name)
		if containsFold(a.Config.EnabledSkills, skill.Name) {
			state = "enabled"
		}
		secondaryLabel := "enable"
		secondaryCommand := tuiNamedSlashCommand("/skills enable", skill.Name)
		if state == "enabled" {
			secondaryLabel = "disable"
			secondaryCommand = tuiNamedSlashCommand("/skills disable", skill.Name)
		}
		if !skill.Active {
			state = "shadowed"
			command = ""
			secondaryLabel = ""
			secondaryCommand = ""
			if strings.TrimSpace(skill.ShadowedBy) != "" {
				description = "Shadowed by " + strings.TrimSpace(skill.ShadowedBy)
			}
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:            skill.Name,
			Value:            state,
			Description:      description,
			Command:          command,
			SecondaryLabel:   secondaryLabel,
			SecondaryCommand: secondaryCommand,
		})
	}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No skills found."}
	}
	return tab
}

func (a *App) tuiMCPCommandTab() tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "MCP", RefreshCommand: "/mcp"}
	names := sortedMCPServerNames(a.Config.MCPServers)
	tab.Lines = []string{fmt.Sprintf("%d configured", len(names))}
	for _, name := range names {
		server := a.Config.MCPServers[name]
		transport := "stdio"
		if strings.TrimSpace(server.URL) != "" {
			transport = "http"
		}
		if server.Required {
			transport += " · required"
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:       name,
			Value:       transport,
			Description: tuiMCPServerDescription(server),
			Command:     tuiNamedSlashCommand("/mcp show", name),
		})
	}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No MCP servers configured."}
	}
	return tab
}

func tuiMCPServerDescription(server config.MCPServerConfig) string {
	if rawURL := strings.TrimSpace(server.URL); rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err == nil {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.Fragment = ""
			return trimSingleLine(parsed.String(), 96)
		}
		return "Configured HTTP endpoint"
	}
	command := filepath.Base(strings.TrimSpace(server.Command))
	if command == "." || command == "" {
		return "Configured stdio command"
	}
	if len(server.Args) > 0 {
		return fmt.Sprintf("%s · %d args", command, len(server.Args))
	}
	return command
}

func (a *App) tuiHooksCommandTab() tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Hooks", RefreshCommand: "/hooks"}
	configured := 0
	for _, event := range allHookEvents() {
		count := hookConfiguredCount(a.Config.Hooks, event)
		configured += count
		value := "not configured"
		if count > 0 {
			value = fmt.Sprintf("%d configured", count)
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:       event,
			Value:       value,
			Description: "Inspect matcher and command health for this event",
			Command:     "/hooks health " + event,
		})
	}
	tab.Lines = []string{fmt.Sprintf("%d configured across %d events", configured, len(tab.Items))}
	return tab
}

func (a *App) tuiPluginsCommandTab() tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Plugins", RefreshCommand: "/plugins"}
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		tab.Lines = []string{"Plugins unavailable: " + err.Error()}
		return tab
	}
	sort.Slice(manifests, func(i, j int) bool { return strings.ToLower(manifests[i].ID) < strings.ToLower(manifests[j].ID) })
	tab.Lines = []string{fmt.Sprintf("%d installed", len(manifests))}
	for _, manifest := range manifests {
		state := "disabled"
		secondaryLabel := "enable"
		secondaryCommand := tuiNamedSlashCommand("/plugins enable", manifest.ID)
		if manifest.Enabled {
			state = "enabled"
			secondaryLabel = "disable"
			secondaryCommand = tuiNamedSlashCommand("/plugins disable", manifest.ID)
		}
		value := state
		if strings.TrimSpace(manifest.Version) != "" {
			value += " · " + strings.TrimSpace(manifest.Version)
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:            firstNonEmpty(manifest.Name, manifest.ID),
			Value:            value,
			Description:      firstNonEmpty(trimSingleLine(manifest.Description, 96), manifest.ID),
			Command:          tuiNamedSlashCommand("/plugins show", manifest.ID),
			SecondaryLabel:   secondaryLabel,
			SecondaryCommand: secondaryCommand,
		})
	}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No plugins installed."}
	}
	return tab
}

func (a *App) tuiAgentsCommandTab() tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Agents", RefreshCommand: "/agents"}
	tab.Items = append(tab.Items, tui.CommandViewItem{
		Label:       "Create new agent",
		Value:       "workspace",
		Description: "Create a reusable agent definition in this workspace",
		Action:      "prefill",
		Command:     "/agents create ",
	})
	definitions, err := a.runtimeAgentDefinitions()
	if err != nil {
		tab.Lines = []string{"Agents unavailable: " + err.Error()}
		return tab
	}
	sort.Slice(definitions, func(i, j int) bool {
		return strings.ToLower(definitions[i].Name) < strings.ToLower(definitions[j].Name)
	})
	tab.Lines = []string{fmt.Sprintf("%d available", len(definitions))}
	for _, definition := range definitions {
		value := firstNonEmpty(definition.Model, definition.Source, "default model")
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:            definition.Name,
			Value:            value,
			Description:      trimSingleLine(definition.Description, 96),
			Command:          tuiNamedSlashCommand("/agents show", definition.Name),
			SecondaryLabel:   "run",
			SecondaryAction:  "prefill",
			SecondaryCommand: tuiNamedSlashCommandPrefix("/agents run", definition.Name),
		})
	}
	return tab
}

func tuiNamedSlashCommand(prefix string, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return ""
	}
	return strings.TrimSpace(prefix) + " " + name
}

func tuiNamedSlashCommandPrefix(prefix string, name string) string {
	command := tuiNamedSlashCommand(prefix, name)
	if command == "" {
		return ""
	}
	return command + " "
}

func tuiReportLines(text string, heading string) []string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > 0 && strings.EqualFold(strings.TrimSpace(lines[0]), strings.TrimSpace(heading)) {
		lines = lines[1:]
	}
	return lines
}

func tuiExtensionInformationTitle(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || tuiSlashRequestsJSON(fields[1:]) {
		return "", false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	switch command {
	case "/skill", "/skills":
		return "Skills", true
	case "/mcp":
		return "MCP", true
	case "/hooks":
		return "Hooks", true
	case "/plugin", "/plugins", "/marketplace":
		return "Plugins", true
	case "/agents":
		return "Agents", true
	default:
		return "", false
	}
}

func tuiSlashRequestsJSON(args []string) bool {
	for index, arg := range args {
		switch {
		case arg == "--json", strings.EqualFold(arg, "--output-format=json"):
			return true
		case (arg == "--output-format" || arg == "-o") && index+1 < len(args):
			return strings.EqualFold(strings.TrimSpace(args[index+1]), "json")
		}
	}
	return false
}

func (a *App) tuiAgentsSlashResult(args []string, sess *session.Session) (tui.SlashResult, bool, error) {
	cleanArgs, format, err := stripJSONOnlyOutputFormat("agents", args)
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if format == "json" {
		return tui.SlashResult{}, false, nil
	}
	action := "list"
	if len(cleanArgs) > 0 {
		action = normalizeAgentsAction(cleanArgs[0])
	}
	switch action {
	case "list":
		if len(cleanArgs) > 1 {
			return tui.SlashResult{}, false, nil
		}
		view := a.tuiExtensionsCommandView(4)
		return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
	case "show":
		if len(cleanArgs) != 2 {
			return tui.SlashResult{}, false, nil
		}
		view, err := a.tuiAgentDefinitionInformation(cleanArgs[1])
		return tui.SlashResult{Handled: true, Information: &view}, true, err
	case "runs":
		if len(cleanArgs) > 1 {
			return tui.SlashResult{}, false, nil
		}
		view := a.tuiRuntimeCommandView(sess, 3)
		return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
	case "status":
		if len(cleanArgs) != 2 {
			return tui.SlashResult{}, false, nil
		}
		view, err := a.tuiAgentRunInformation(cleanArgs[1], 32*1024)
		return tui.SlashResult{Handled: true, Information: &view}, true, err
	case "output":
		id, limit, err := parseAgentRunOutputArgs(cleanArgs[1:])
		if err != nil {
			return tui.SlashResult{Handled: true}, true, err
		}
		view, err := a.tuiAgentRunInformation(id, limit)
		return tui.SlashResult{Handled: true, Information: &view}, true, err
	default:
		return tui.SlashResult{}, false, nil
	}
}

func (a *App) tuiSubagentSlashResult(args []string, sess *session.Session) (tui.SlashResult, bool, error) {
	mapped, err := normalizeSubagentArgs(args)
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	return a.tuiAgentsSlashResult(mapped, sess)
}

func (a *App) tuiBookmarksSlashResult(args []string, sess *session.Session) (tui.SlashResult, bool, error) {
	overrides := config.FlagOverrides{}
	if sess != nil {
		overrides.SessionID = sess.ID
	}
	req, err := parseBookmarksArgs(args, overrides)
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if req.Format == "json" {
		return tui.SlashResult{}, false, nil
	}
	switch req.Action {
	case "list":
		if req.All {
			return tui.SlashResult{}, false, nil
		}
		view := a.tuiConversationCommandView(sess, 2)
		return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
	case "show":
		view, err := a.tuiBookmarkInformation(req.Ref)
		return tui.SlashResult{Handled: true, Information: &view}, true, err
	default:
		return tui.SlashResult{}, false, nil
	}
}

func (a *App) tuiConversationCommandView(sess *session.Session, selectedTab int) tui.CommandView {
	return tui.CommandView{
		Title:       "Conversation",
		SelectedTab: selectedTab,
		Tabs: []tui.CommandViewTab{
			a.tuiHistoryCommandTab(sess),
			a.tuiSessionsCommandTab(sess),
			a.tuiBookmarksCommandTab(),
		},
	}
}

func (a *App) tuiHistoryCommandTab(sess *session.Session) tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "History", RefreshCommand: "/history"}
	if a.Sessions == nil || sess == nil {
		tab.Lines = []string{"Prompt history is unavailable."}
		return tab
	}
	entries, err := a.Sessions.PromptHistory(sess.ID)
	if err != nil {
		tab.Lines = []string{"Prompt history unavailable: " + err.Error()}
		return tab
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		description := fmt.Sprintf("message %d", entry.Index+1)
		if !entry.Time.IsZero() {
			description += " · " + entry.Time.Local().Format("2006-01-02 15:04:05")
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:       trimSingleLine(entry.Text, 80),
			Value:       firstNonEmpty(entry.Role, "user"),
			Description: description,
			Action:      "prefill",
			Command:     entry.Text,
		})
	}
	tab.Lines = []string{fmt.Sprintf("%d prompts · newest first", len(entries))}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No prompt history for this conversation."}
	}
	return tab
}

func (a *App) tuiSessionsCommandTab(active *session.Session) tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Sessions", RefreshCommand: "/sessions"}
	if a.Sessions == nil {
		tab.Lines = []string{"Session store is unavailable."}
		return tab
	}
	sessions, err := a.Sessions.List()
	if err != nil {
		tab.Lines = []string{"Sessions unavailable: " + err.Error()}
		return tab
	}
	activeID := ""
	if active != nil {
		activeID = active.ID
	}
	for _, choice := range resumeSessionChoices(sessions) {
		valueParts := []string{}
		if branch := strings.TrimSpace(choice.BranchName); branch != "" {
			valueParts = append(valueParts, branch)
		}
		if tag := strings.TrimSpace(choice.Tag); tag != "" {
			valueParts = append(valueParts, "#"+tag)
		}
		valueParts = append(valueParts, fmt.Sprintf("%d messages", choice.MessageCount))
		value := strings.Join(valueParts, " · ")
		if choice.ID == activeID {
			value = "current · " + value
		}
		description := choice.ID
		if !choice.UpdatedAt.IsZero() {
			description += " · " + choice.UpdatedAt.Local().Format("2006-01-02 15:04:05")
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:       choice.Title,
			Value:       value,
			Description: description,
			Command:     tuiNamedSlashCommand("/resume", choice.ID),
		})
	}
	tab.Lines = []string{fmt.Sprintf("%d conversations", len(sessions))}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No saved conversations."}
	}
	return tab
}

func (a *App) tuiBookmarksCommandTab() tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Bookmarks", RefreshCommand: "/bookmarks"}
	tab.Items = append(tab.Items, tui.CommandViewItem{
		Label:       "Add bookmark",
		Value:       "current message",
		Description: "Save a named pointer to the current conversation",
		Action:      "prefill",
		Command:     "/bookmarks add ",
	})
	items, err := bookmarks.NewStore(a.Config.ConfigHome).List(bookmarks.ListOptions{Workspace: a.Workspace})
	if err != nil {
		tab.Lines = []string{"Bookmarks unavailable: " + err.Error()}
		return tab
	}
	for _, item := range items {
		value := firstNonEmpty(item.SessionID, "workspace")
		if item.PRNumber > 0 {
			value = fmt.Sprintf("PR #%d", item.PRNumber)
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:       item.Name,
			Value:       value,
			Description: firstNonEmpty(trimSingleLine(item.Note, 96), item.ID),
			Command:     tuiNamedSlashCommand("/bookmarks show", item.ID),
		})
	}
	tab.Lines = []string{fmt.Sprintf("%d saved", len(items))}
	return tab
}

func (a *App) tuiBookmarkInformation(ref string) (tui.InformationView, error) {
	item, err := bookmarks.NewStore(a.Config.ConfigHome).Get(ref)
	if err != nil {
		return tui.InformationView{}, err
	}
	lines := []string{
		tuiInformationLine("ID", item.ID),
		tuiInformationLine("Name", item.Name),
	}
	if item.SessionID != "" {
		lines = append(lines, tuiInformationLine("Session", item.SessionID))
	}
	if item.MessageIndex != nil {
		lines = append(lines, tuiInformationLine("Message", strconv.Itoa(*item.MessageIndex+1)))
	}
	if item.PRURL != "" {
		lines = append(lines, tuiInformationLine("Pull request", item.PRURL))
	}
	if item.Note != "" {
		lines = append(lines, tuiInformationLine("Note", item.Note))
	}
	if command := bookmarkResumeCommand(item); command != "" {
		lines = append(lines, "", command)
	}
	return tui.InformationView{Title: "Bookmark", Lines: lines}, nil
}

func (a *App) tuiMemorySlashResult(args []string) (tui.SlashResult, bool, error) {
	req, err := parseMemoryArgs(args)
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if req.Format == "json" {
		return tui.SlashResult{}, false, nil
	}
	switch req.Action {
	case "list", "select":
		view, err := a.tuiMemoryCommandView()
		return tui.SlashResult{Handled: true, CommandView: view}, true, err
	case "show":
		view, err := a.tuiMemoryInformation(strings.Join(req.Rest, " "))
		return tui.SlashResult{Handled: true, Information: view}, true, err
	default:
		return tui.SlashResult{}, false, nil
	}
}

func (a *App) tuiMemoryCommandView() (*tui.CommandView, error) {
	report, err := memory.BuildReportWithRulesImport(a.Workspace, a.memoryRulesImportOptions())
	if err != nil {
		return nil, err
	}
	tab := tui.CommandViewTab{
		Title:          "Files",
		Lines:          []string{fmt.Sprintf("%d instruction files", report.InstructionFiles)},
		RefreshCommand: "/memory",
	}
	for _, file := range report.Files {
		path := tuiWorkspaceRelativePath(a.Workspace, file.Path)
		item := tui.CommandViewItem{
			Label:            path,
			Value:            fmt.Sprintf("%s · %d lines", firstNonEmpty(file.Scope, "project"), file.Lines),
			Description:      firstNonEmpty(trimSingleLine(file.Preview, 96), file.Name),
			Command:          tuiNamedSlashCommand("/memory edit", path),
			SecondaryLabel:   "view",
			SecondaryCommand: tuiNamedSlashCommand("/memory show", path),
			SecondaryKey:     "v",
		}
		if item.Command == "" {
			item.Action = "prefill"
			item.Command = "/memory edit " + path
			item.SecondaryAction = "prefill"
			item.SecondaryCommand = "/memory show " + path
		}
		tab.Items = append(tab.Items, item)
	}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No instruction files found. Select AGENTS.md to create one."}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:       "AGENTS.md",
			Value:       "new · project",
			Description: "Create the workspace instruction file and open it in the configured editor",
			Command:     "/memory edit AGENTS.md",
		})
	}
	return &tui.CommandView{Title: "Memory", Tabs: []tui.CommandViewTab{tab}}, nil
}

func (a *App) tuiMemoryInformation(target string) (*tui.InformationView, error) {
	report, err := memory.ShowWithRulesImport(a.Workspace, target, a.memoryRulesImportOptions())
	if err != nil {
		return nil, err
	}
	lines := []string{
		tuiInformationLine("Path", tuiWorkspaceRelativePath(a.Workspace, report.File.Path)),
		tuiInformationLine("Scope", firstNonEmpty(report.File.Scope, "project")),
		tuiInformationLine("Size", fmt.Sprintf("%d bytes", report.File.SizeBytes)),
	}
	if !report.File.ModifiedAt.IsZero() {
		lines = append(lines, tuiInformationLine("Modified", report.File.ModifiedAt.Local().Format(time.RFC3339)))
	}
	lines = append(lines, "", "Contents")
	if strings.TrimSpace(report.Body) == "" {
		lines = append(lines, "Empty file.")
	} else {
		lines = append(lines, strings.Split(strings.TrimRight(report.Body, "\n"), "\n")...)
	}
	return &tui.InformationView{Title: "Memory", Lines: lines}, nil
}

func tuiWorkspaceRelativePath(workspace string, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	base := firstNonEmpty(strings.TrimSpace(workspace), ".")
	if absolute, err := filepath.Abs(base); err == nil {
		base = absolute
	}
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if rel, err := filepath.Rel(base, path); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func (a *App) tuiStatuslineSetupQuery(request string) string {
	request = strings.TrimSpace(request)
	if request == "" {
		request = "Configure my status line from my shell PS1 configuration."
	}
	configPath := "the active Codog config"
	if path, err := a.preferenceConfigPath("user", ""); err == nil && strings.TrimSpace(path) != "" {
		configPath = path
	}
	return strings.Join([]string{
		"Set up Codog's status line UI.",
		request,
		"Inspect the current shell prompt configuration and " + configPath + ".",
		"Preserve unrelated settings. A custom statusLine command receives Codog status JSON on stdin; verify the result with `codog statusline`.",
	}, " ")
}

func (a *App) tuiFilesInformation(sess *session.Session) tui.InformationView {
	paths := sessionContextFilePaths(sess)
	if len(paths) == 0 {
		return tui.InformationView{Title: "Files in context", Lines: []string{"No files in context"}}
	}
	lines := make([]string, 0, len(paths))
	for _, path := range paths {
		lines = append(lines, tuiContextFilePath(a.Workspace, path))
	}
	return tui.InformationView{Title: "Files in context", Lines: lines}
}

func sessionContextFilePaths(sess *session.Session) []string {
	if sess == nil {
		return nil
	}
	failedToolUses := map[string]bool{}
	for _, message := range sess.Messages {
		for _, block := range message.Content {
			if strings.EqualFold(strings.TrimSpace(block.Type), "tool_result") && block.IsError && strings.TrimSpace(block.ToolUseID) != "" {
				failedToolUses[strings.TrimSpace(block.ToolUseID)] = true
			}
		}
	}
	paths := []string{}
	for _, message := range sess.Messages {
		for _, block := range message.Content {
			if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_use") || failedToolUses[strings.TrimSpace(block.ID)] {
				continue
			}
			switch tools.CanonicalToolName(block.Name) {
			case "read_file", "write_file", "edit_file", "multi_edit", "notebook_read", "notebook_edit":
			default:
				continue
			}
			call := runloop.ToolCall{Name: block.Name, Input: string(block.Input)}
			for _, path := range toolContextPaths(call) {
				paths = appendRecentUniquePath(paths, path, maxDynamicSkillContextPaths)
			}
		}
	}
	return paths
}

func tuiContextFilePath(workspace string, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(filepath.Clean(path))
	}
	base := firstNonEmpty(strings.TrimSpace(workspace), ".")
	if absolute, err := filepath.Abs(base); err == nil {
		base = absolute
	}
	for _, candidate := range []string{base, resolvedTUIPath(base)} {
		if rel, err := filepath.Rel(candidate, path); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return tuiWorkspaceRelativePath(workspace, path)
}

func resolvedTUIPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

func (a *App) tuiTerminalSetupCommandView() (*tui.CommandView, error) {
	report, err := terminalsetup.Run(terminalsetup.Options{Action: "status"})
	if err != nil {
		return nil, err
	}
	lines := []string{
		tuiInformationLine("Shell", report.Shell),
		tuiInformationLine("Path", report.Path),
		tuiInformationLine("Status", firstNonEmpty(report.Message, report.Status)),
	}
	items := []tui.CommandViewItem{}
	if report.Installed {
		items = append(items, tui.CommandViewItem{
			Label:       "Remove shell integration",
			Value:       report.Shell,
			Description: report.Path,
			Command:     "/terminal-setup uninstall --target shell",
		})
	} else {
		items = append(items, tui.CommandViewItem{
			Label:       "Install shell integration",
			Value:       report.Shell,
			Description: report.Path,
			Command:     "/terminal-setup install --target shell",
		})
	}
	items = append(items, tui.CommandViewItem{
		Label:       "Show installation snippet",
		Value:       report.Shell,
		Description: "Preview without modifying the shell profile",
		Command:     "/terminal-setup snippet --target shell",
	})
	view := tui.CommandView{Title: "Terminal setup", Tabs: []tui.CommandViewTab{{
		Title:          "Shell",
		Lines:          lines,
		Items:          items,
		RefreshCommand: "/terminal-setup",
	}}}
	return &view, nil
}

func (a *App) tuiKeybindingsCommandView() (*tui.CommandView, error) {
	path, err := a.keybindingsPath()
	if err != nil {
		return nil, err
	}
	exists := fileExists(path)
	state := "not created"
	if exists {
		state = "ready"
	}
	items := []tui.CommandViewItem{{
		Label:       "Open in editor",
		Value:       state,
		Description: path,
		Command:     "/keybindings open",
	}}
	if !exists {
		items = append(items, tui.CommandViewItem{
			Label:       "Create template",
			Value:       "default bindings",
			Description: path,
			Command:     "/keybindings init",
		})
	}
	items = append(items,
		tui.CommandViewItem{
			Label:       "Validate bindings",
			Value:       state,
			Description: "Check contexts, actions, and key chords",
			Command:     "/keybindings validate",
		},
		tui.CommandViewItem{
			Label:       "Show active bindings",
			Value:       "runtime",
			Description: "Inspect defaults and user overrides",
			Command:     "/keybindings show",
		},
	)
	view := tui.CommandView{Title: "Keybindings", Tabs: []tui.CommandViewTab{{
		Title: "Config",
		Lines: []string{
			tuiInformationLine("Path", path),
			tuiInformationLine("Status", state),
		},
		Items:          items,
		RefreshCommand: "/keybindings",
	}}}
	return &view, nil
}

func (a *App) tuiBackgroundSlashResult(args []string, sess *session.Session) (tui.SlashResult, bool, error) {
	if tuiSlashRequestsJSON(args) {
		return tui.SlashResult{}, false, nil
	}
	cleanArgs, _, err := parseBackgroundOutputFormat(args, "text")
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if len(cleanArgs) == 0 || (len(cleanArgs) == 1 && normalizeBackgroundAction(cleanArgs[0]) == "list") {
		view := a.tuiRuntimeCommandView(sess, 0)
		return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
	}
	switch normalizeBackgroundAction(cleanArgs[0]) {
	case "status":
		if len(cleanArgs) != 2 {
			return tui.SlashResult{}, false, nil
		}
		view, err := a.tuiTaskInformation(cleanArgs[1], 32*1024, false)
		return tui.SlashResult{Handled: true, Information: &view}, true, err
	case "logs":
		id, limit, err := parseBackgroundLogsArgs(cleanArgs[1:])
		if err != nil {
			return tui.SlashResult{Handled: true}, true, err
		}
		view, err := a.tuiTaskInformation(id, limit, true)
		return tui.SlashResult{Handled: true, Information: &view}, true, err
	default:
		return tui.SlashResult{}, false, nil
	}
}

func (a *App) tuiTeamSlashResult(args []string, sess *session.Session) (tui.SlashResult, bool, error) {
	req, err := parseTeamArgsWithDefault(args, "text")
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if req.Format == "json" {
		return tui.SlashResult{}, false, nil
	}
	switch req.Action {
	case "list":
		view := a.tuiRuntimeCommandView(sess, 1)
		return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
	case "get", "status", "logs":
		view, err := a.tuiTeamInformation(req.ID, req.Action != "get", req.Action == "logs", req.Limit)
		return tui.SlashResult{Handled: true, Information: &view}, true, err
	default:
		return tui.SlashResult{}, false, nil
	}
}

func (a *App) tuiCronSlashResult(args []string, sess *session.Session) (tui.SlashResult, bool, error) {
	req, err := parseCronArgsWithDefault(args, "text")
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if req.Format == "json" {
		return tui.SlashResult{}, false, nil
	}
	switch req.Action {
	case "list":
		view := a.tuiRuntimeCommandView(sess, 2)
		return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
	case "show":
		view, err := a.tuiCronInformation(req.ID)
		return tui.SlashResult{Handled: true, Information: &view}, true, err
	default:
		return tui.SlashResult{}, false, nil
	}
}

func (a *App) tuiRuntimeCommandView(sess *session.Session, selectedTab int) tui.CommandView {
	return tui.CommandView{
		Title:       "Runtime",
		SelectedTab: selectedTab,
		Tabs: []tui.CommandViewTab{
			a.tuiTasksCommandTab(sess),
			a.tuiTeamsCommandTab(),
			a.tuiSchedulesCommandTab(),
			a.tuiAgentRunsCommandTab(sess),
		},
	}
}

func (a *App) tuiTasksCommandTab(sess *session.Session) tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Tasks", RefreshCommand: "/tasks"}
	tasks, err := background.NewStore(a.Config.ConfigHome).List()
	if err != nil {
		tab.Lines = []string{"Tasks unavailable: " + err.Error()}
		return tab
	}
	if sess != nil {
		tasks = background.FilterBySession(tasks, sess.ID)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		activeI := background.IsActiveStatus(tasks[i].Status)
		activeJ := background.IsActiveStatus(tasks[j].Status)
		if activeI != activeJ {
			return activeI
		}
		return tasks[i].StartedAt.After(tasks[j].StartedAt)
	})
	active := 0
	for _, task := range tasks {
		if background.IsActiveStatus(task.Status) {
			active++
		}
		label := firstNonEmpty(task.Description, task.Prompt, task.Command, task.ID)
		value := firstNonEmpty(task.Status, "unknown") + " · " + firstNonEmpty(task.Kind, "shell")
		description := task.ID
		if !task.StartedAt.IsZero() {
			description += " · " + task.StartedAt.Local().Format("2006-01-02 15:04:05")
		}
		item := tui.CommandViewItem{
			Label:       trimSingleLine(label, 72),
			Value:       value,
			Description: description,
			Command:     tuiNamedSlashCommand("/tasks status", task.ID),
		}
		if background.IsActiveStatus(task.Status) {
			item.SecondaryLabel = "stop"
			item.SecondaryCommand = tuiNamedSlashCommand("/tasks stop", task.ID)
			item.SecondaryKey = "x"
		}
		tab.Items = append(tab.Items, item)
	}
	tab.Lines = []string{fmt.Sprintf("%d active · %d total", active, len(tasks))}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No background tasks for this session."}
	}
	return tab
}

func (a *App) tuiTeamsCommandTab() tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Teams", RefreshCommand: "/team"}
	teams, err := team.NewStore(a.Config.ConfigHome).List()
	if err != nil {
		tab.Lines = []string{"Teams unavailable: " + err.Error()}
		return tab
	}
	active := 0
	for _, item := range teams {
		if strings.EqualFold(item.Status, "running") {
			active++
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:            firstNonEmpty(item.Name, item.ID),
			Value:            fmt.Sprintf("%s · %d tasks", firstNonEmpty(item.Status, "unknown"), len(item.Tasks)),
			Description:      item.ID,
			Command:          tuiNamedSlashCommand("/team status", item.ID),
			SecondaryLabel:   "logs",
			SecondaryCommand: tuiNamedSlashCommand("/team logs", item.ID),
			SecondaryKey:     "l",
		})
	}
	tab.Lines = []string{fmt.Sprintf("%d running · %d total", active, len(teams))}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No teams created."}
	}
	return tab
}

func (a *App) tuiSchedulesCommandTab() tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Schedules", RefreshCommand: "/cron"}
	entries, err := cron.NewStore(a.Config.ConfigHome).List()
	if err != nil {
		tab.Lines = []string{"Schedules unavailable: " + err.Error()}
		return tab
	}
	enabled := 0
	now := time.Now().UTC()
	for _, entry := range entries {
		state := "disabled"
		secondaryLabel := "enable"
		secondaryCommand := tuiNamedSlashCommand("/cron enable", entry.ID)
		if entry.Enabled {
			enabled++
			state = "enabled"
			secondaryLabel = "disable"
			secondaryCommand = tuiNamedSlashCommand("/cron disable", entry.ID)
		}
		if cron.IsDue(entry, now) {
			state = "due"
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:            trimSingleLine(firstNonEmpty(entry.Description, entry.Prompt, entry.ID), 72),
			Value:            entry.Schedule + " · " + state,
			Description:      fmt.Sprintf("%s · %d runs", entry.ID, entry.RunCount),
			Command:          tuiNamedSlashCommand("/cron show", entry.ID),
			SecondaryLabel:   secondaryLabel,
			SecondaryCommand: secondaryCommand,
		})
	}
	tab.Lines = []string{fmt.Sprintf("%d enabled · %d total", enabled, len(entries))}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No schedules configured."}
	}
	return tab
}

func (a *App) tuiAgentRunsCommandTab(sess *session.Session) tui.CommandViewTab {
	tab := tui.CommandViewTab{Title: "Agent runs", RefreshCommand: "/agents runs"}
	runs, err := agentruns.NewStore(a.Config.ConfigHome).List()
	if err != nil {
		tab.Lines = []string{"Agent runs unavailable: " + err.Error()}
		return tab
	}
	if sess != nil {
		filtered := runs[:0]
		for _, run := range runs {
			if run.SessionID == sess.ID {
				filtered = append(filtered, run)
			}
		}
		runs = filtered
	}
	taskStore := background.NewStore(a.Config.ConfigHome)
	statuses := make([]agentruns.Status, 0, len(runs))
	for _, run := range runs {
		statuses = append(statuses, agentruns.StatusForTask(taskStore, run))
	}
	sort.SliceStable(statuses, func(i, j int) bool {
		activeI := background.IsActiveStatus(statuses[i].CurrentStatus)
		activeJ := background.IsActiveStatus(statuses[j].CurrentStatus)
		if activeI != activeJ {
			return activeI
		}
		return statuses[i].Run.CreatedAt.After(statuses[j].Run.CreatedAt)
	})
	active := 0
	for _, status := range statuses {
		run := status.Run
		if background.IsActiveStatus(status.CurrentStatus) {
			active++
		}
		label := "@" + firstNonEmpty(run.Agent, "agent")
		if run.Prompt != "" {
			label += " · " + trimSingleLine(run.Prompt, 64)
		}
		item := tui.CommandViewItem{
			Label:       label,
			Value:       firstNonEmpty(status.CurrentStatus, "unknown") + " · " + firstNonEmpty(status.Health.State, "unknown"),
			Description: run.ID,
			Command:     tuiNamedSlashCommand("/agents status", run.ID),
		}
		if background.IsActiveStatus(status.CurrentStatus) {
			item.SecondaryLabel = "stop"
			item.SecondaryCommand = tuiNamedSlashCommand("/agents stop", run.ID)
			item.SecondaryKey = "x"
		}
		tab.Items = append(tab.Items, item)
	}
	tab.Lines = []string{fmt.Sprintf("%d active · %d total", active, len(statuses))}
	if len(tab.Items) == 0 {
		tab.Lines = []string{"No agent runs for this session."}
	}
	return tab
}

func (a *App) tuiAgentDefinitionInformation(name string) (tui.InformationView, error) {
	definitions, err := a.runtimeAgentDefinitions()
	if err != nil {
		return tui.InformationView{}, err
	}
	for _, definition := range definitions {
		if !strings.EqualFold(definition.Name, name) {
			continue
		}
		lines := []string{
			tuiInformationLine("Name", definition.Name),
			tuiInformationLine("Model", firstNonEmpty(definition.Model, "default")),
			tuiInformationLine("Source", firstNonEmpty(definition.Source, "unknown")),
		}
		if definition.Description != "" {
			lines = append(lines, tuiInformationLine("Description", definition.Description))
		}
		if len(definition.Tools) > 0 {
			lines = append(lines, tuiInformationLine("Tools", strings.Join(definition.Tools, ", ")))
		}
		if definition.Path != "" {
			lines = append(lines, tuiInformationLine("Path", definition.Path))
		}
		if definition.Prompt != "" {
			lines = append(lines, "", "Prompt")
			lines = append(lines, strings.Split(strings.TrimSpace(definition.Prompt), "\n")...)
		}
		return tui.InformationView{Title: "Agent", Lines: lines}, nil
	}
	return tui.InformationView{}, fmt.Errorf("agent %q was not found", name)
}

func (a *App) tuiAgentRunInformation(id string, logLimit int64) (tui.InformationView, error) {
	run, err := agentruns.NewStore(a.Config.ConfigHome).Get(id)
	if err != nil {
		return tui.InformationView{}, err
	}
	taskStore := background.NewStore(a.Config.ConfigHome)
	status := agentruns.StatusForTask(taskStore, run)
	lines := []string{
		tuiInformationLine("Run", run.ID),
		tuiInformationLine("Agent", run.Agent),
		tuiInformationLine("Status", firstNonEmpty(status.CurrentStatus, "unknown")),
		tuiInformationLine("Health", firstNonEmpty(status.Health.State, "unknown")),
		tuiInformationLine("Task", run.TaskID),
	}
	if run.Prompt != "" {
		lines = append(lines, tuiInformationLine("Prompt", trimSingleLine(run.Prompt, 200)))
	}
	if status.Health.Summary != "" {
		lines = append(lines, tuiInformationLine("Summary", status.Health.Summary))
	}
	if status.Health.RecommendedAction != "" {
		lines = append(lines, tuiInformationLine("Next", status.Health.RecommendedAction))
	}
	if status.Error != "" {
		lines = append(lines, tuiInformationLine("Error", status.Error))
	}
	lines = append(lines, "", "Output")
	log, logErr := taskStore.Logs(run.TaskID, logLimit)
	switch {
	case logErr != nil:
		lines = append(lines, "Output unavailable: "+logErr.Error())
	case strings.TrimSpace(log) == "":
		lines = append(lines, "No output yet.")
	default:
		lines = append(lines, strings.Split(strings.TrimRight(log, "\n"), "\n")...)
	}
	return tui.InformationView{Title: "Agent run", Lines: lines}, nil
}

func (a *App) tuiTaskInformation(id string, logLimit int64, logsOnly bool) (tui.InformationView, error) {
	store := background.NewStore(a.Config.ConfigHome)
	task, err := store.Status(id)
	if err != nil {
		return tui.InformationView{}, err
	}
	log, logErr := store.Logs(id, logLimit)
	if logsOnly && logErr != nil {
		return tui.InformationView{}, logErr
	}
	lines := []string{tuiInformationLine("ID", task.ID)}
	if !logsOnly {
		lines = append(lines,
			tuiInformationLine("Status", firstNonEmpty(task.Status, "unknown")),
			tuiInformationLine("Type", firstNonEmpty(task.Kind, "shell")),
			tuiInformationLine("Command", trimSingleLine(task.Command, 160)),
		)
		if task.Description != "" {
			lines = append(lines, tuiInformationLine("Description", trimSingleLine(task.Description, 160)))
		}
		if task.SessionID != "" {
			lines = append(lines, tuiInformationLine("Session", task.SessionID))
		}
		if task.PID > 0 {
			lines = append(lines, tuiInformationLine("PID", strconv.Itoa(task.PID)))
		}
		if !task.StartedAt.IsZero() {
			lines = append(lines, tuiInformationLine("Started", task.StartedAt.Local().Format(time.RFC3339)))
		}
		if task.CompletedAt != nil {
			lines = append(lines, tuiInformationLine("Completed", task.CompletedAt.Local().Format(time.RFC3339)))
		}
		if task.ExitCode != nil {
			lines = append(lines, tuiInformationLine("Exit code", strconv.Itoa(*task.ExitCode)))
		}
		if task.Error != "" {
			lines = append(lines, tuiInformationLine("Error", trimSingleLine(task.Error, 160)))
		}
	}
	lines = append(lines, "", "Output")
	if logErr != nil {
		lines = append(lines, "Output unavailable: "+logErr.Error())
	} else if strings.TrimSpace(log) == "" {
		lines = append(lines, "No output yet.")
	} else {
		lines = append(lines, strings.Split(strings.TrimRight(log, "\n"), "\n")...)
	}
	return tui.InformationView{Title: "Task", Lines: lines}, nil
}

func (a *App) tuiTeamInformation(id string, refresh bool, includeLogs bool, limit int64) (tui.InformationView, error) {
	teamStore := team.NewStore(a.Config.ConfigHome)
	taskStore := background.NewStore(a.Config.ConfigHome)
	item, err := teamStore.Get(id)
	if err != nil {
		return tui.InformationView{}, err
	}
	var tasks []background.Task
	var missing []string
	if refresh {
		item, tasks, missing, err = refreshTeamStatus(teamStore, taskStore, id)
	} else {
		tasks, missing = loadTeamTasks(taskStore, item.TaskIDs, false)
	}
	if err != nil {
		return tui.InformationView{}, err
	}
	lines := []string{
		tuiInformationLine("ID", item.ID),
		tuiInformationLine("Name", item.Name),
		tuiInformationLine("Status", item.Status),
		tuiInformationLine("Tasks", strconv.Itoa(len(item.Tasks))),
	}
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("  %s  %s  %s", task.ID, firstNonEmpty(task.Status, "unknown"), trimSingleLine(firstNonEmpty(task.Description, task.Prompt, task.Command), 96)))
	}
	for _, taskID := range missing {
		lines = append(lines, "  "+taskID+"  missing")
	}
	if includeLogs {
		for _, entry := range readTeamLogs(taskStore, item, limit) {
			lines = append(lines, "", "Output · "+entry.TaskID)
			switch {
			case entry.Error != "":
				lines = append(lines, "Output unavailable: "+entry.Error)
			case strings.TrimSpace(entry.Log) == "":
				lines = append(lines, "No output yet.")
			default:
				lines = append(lines, strings.Split(strings.TrimRight(entry.Log, "\n"), "\n")...)
			}
		}
	}
	return tui.InformationView{Title: "Team", Lines: lines}, nil
}

func (a *App) tuiCronInformation(id string) (tui.InformationView, error) {
	entry, err := cron.NewStore(a.Config.ConfigHome).Get(id)
	if err != nil {
		return tui.InformationView{}, err
	}
	state := "disabled"
	if entry.Enabled {
		state = "enabled"
	}
	lines := []string{
		tuiInformationLine("ID", entry.ID),
		tuiInformationLine("Schedule", entry.Schedule),
		tuiInformationLine("Status", state),
		tuiInformationLine("Prompt", trimSingleLine(entry.Prompt, 200)),
		tuiInformationLine("Runs", strconv.Itoa(entry.RunCount)),
	}
	if entry.Description != "" {
		lines = append(lines, tuiInformationLine("Description", trimSingleLine(entry.Description, 200)))
	}
	if entry.LastRunAt != nil {
		lines = append(lines, tuiInformationLine("Last run", entry.LastRunAt.Local().Format(time.RFC3339)))
	}
	return tui.InformationView{Title: "Schedule", Lines: lines}, nil
}

func tuiInformationLine(label string, value string) string {
	return fmt.Sprintf("%-14s%s", strings.TrimSpace(label), strings.TrimSpace(value))
}

func tuiRuntimeRefreshTab(line string) (int, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || tuiSlashRequestsJSON(fields[1:]) {
		return 0, false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	action := fields[1]
	switch command {
	case "/background", "/tasks", "/bashes":
		switch normalizeBackgroundAction(action) {
		case "run", "heartbeat", "stop", "restart", "prune", "supervise":
			return 0, true
		}
	case "/team":
		switch normalizeTeamAction(action) {
		case "create", "delete":
			return 1, true
		}
	case "/cron":
		switch normalizeCronAction(action) {
		case "create", "delete", "enable", "disable", "mark-run", "run-due":
			return 2, true
		}
	case "/agents":
		switch normalizeAgentsAction(action) {
		case "run", "stop", "update", "heartbeat", "run-remove", "prune":
			return 3, true
		}
	case "/subagent":
		switch strings.ToLower(strings.TrimSpace(action)) {
		case "steer", "message", "update", "kill", "stop":
			return 3, true
		}
	}
	return 0, false
}

func tuiExtensionsRefreshTab(line string) (int, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || tuiSlashRequestsJSON(fields[1:]) {
		return 0, false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	if command == "/agents" && normalizeAgentsAction(fields[1]) == "create" {
		return 4, true
	}
	return 0, false
}

func tuiConversationRefreshTab(line string) (int, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || tuiSlashRequestsJSON(fields[1:]) {
		return 0, false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	if command != "/bookmarks" {
		return 0, false
	}
	switch normalizeBookmarksAction(fields[1]) {
	case "add", "delete", "clear":
		return 2, true
	default:
		return 0, false
	}
}

func (a *App) tuiPreferenceRefreshView(line string) (*tui.CommandView, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || tuiSlashRequestsJSON(fields[1:]) {
		return nil, false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	switch command {
	case "/fast":
		req, err := parseFastArgs(fields[1:])
		if err != nil || req.Action == "status" {
			return nil, false
		}
		view := a.tuiFastCommandView()
		return &view, true
	case "/output-style":
		req, err := parseOutputStyleArgs(fields[1:])
		if err != nil || (req.Action != "set" && req.Action != "clear") {
			return nil, false
		}
		view, err := a.tuiOutputStyleCommandView()
		return view, err == nil
	case "/sandbox-toggle":
		req, err := parseSandboxToggleArgs(fields[1:])
		if err != nil || req.Action == "status" {
			return nil, false
		}
		view := a.tuiSandboxCommandView()
		return &view, true
	case "/terminal-setup":
		req, err := parseTerminalSetupArgs(fields[1:])
		if err != nil || (req.Action != "install" && req.Action != "uninstall") {
			return nil, false
		}
		view, err := a.tuiTerminalSetupCommandView()
		return view, err == nil
	case "/keybindings":
		req, err := parseKeybindingsArgs(fields[1:])
		if err != nil || req.Action != "init" {
			return nil, false
		}
		view, err := a.tuiKeybindingsCommandView()
		return view, err == nil
	default:
		return nil, false
	}
}
