package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/configvalidate"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/workerstate"
)

func (a *App) modelDiagnosticsBaseURL(provider string) string {
	if provider == effectiveConfiguredProvider(a.Config) && strings.TrimSpace(a.Config.BaseURL) != "" {
		return a.Config.BaseURL
	}
	switch provider {
	case modelrouting.ProviderOpenAI:
		if ollamaHost := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); ollamaHost != "" {
			return strings.TrimRight(ollamaHost, "/") + "/v1"
		}
		if baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); baseURL != "" {
			return baseURL
		}
		return modelrouting.DefaultOpenAIBaseURL
	case modelrouting.ProviderXAI:
		if baseURL := strings.TrimSpace(os.Getenv("XAI_BASE_URL")); baseURL != "" {
			return baseURL
		}
		return modelrouting.DefaultXAIBaseURL
	case modelrouting.ProviderDashScope:
		if baseURL := strings.TrimSpace(os.Getenv("DASHSCOPE_BASE_URL")); baseURL != "" {
			return baseURL
		}
		return modelrouting.DefaultDashScopeBaseURL
	default:
		if baseURL := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")); baseURL != "" {
			return baseURL
		}
		return config.DefaultBaseURL
	}
}

func effectiveConfiguredProvider(cfg config.Config) string {
	if provider := strings.TrimSpace(cfg.RuntimeProvider); provider != "" {
		return provider
	}
	return modelrouting.ProviderForModel(cfg.Model)
}

func requestedMatchesConfiguredModel(requested string, configured string) bool {
	if strings.TrimSpace(requested) == "" {
		return true
	}
	return strings.EqualFold(resolveModelAlias(requested), resolveModelAlias(configured))
}

func modelWireProtocol(provider string) string {
	if provider == modelrouting.ProviderOpenAI || provider == modelrouting.ProviderXAI || provider == modelrouting.ProviderDashScope {
		return "openai_chat_completions"
	}
	return "anthropic_messages"
}

func modelProviderEnv(provider string) (string, string) {
	switch provider {
	case modelrouting.ProviderOpenAI:
		return "OPENAI_API_KEY", "OPENAI_BASE_URL or OLLAMA_HOST"
	case modelrouting.ProviderXAI:
		return "XAI_API_KEY", "XAI_BASE_URL"
	case modelrouting.ProviderDashScope:
		return "DASHSCOPE_API_KEY", "DASHSCOPE_BASE_URL"
	default:
		return "ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"
	}
}

func modelAliasName(model string) string {
	model = strings.TrimSpace(model)
	for _, alias := range modelrouting.BuiltInAliases() {
		if strings.EqualFold(model, alias.Name) && !strings.EqualFold(alias.Name, alias.Model) {
			return alias.Name
		}
	}
	return ""
}

func resolveModelAlias(model string) string {
	return modelrouting.ResolveAlias(model)
}

func renderModelReport(out io.Writer, report modelReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintf(out, "model=%s\n", report.Model)
	if report.RequestedModel != "" {
		fmt.Fprintf(out, "requested_model=%s\n", report.RequestedModel)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "path=%s\n", report.Path)
	}
	return nil
}

func renderModelsReport(out io.Writer, report modelsReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Models")
	fmt.Fprintf(out, "  Default          %s\n", report.DefaultModel)
	aliasParts := make([]string, 0, len(report.Aliases))
	for _, alias := range report.Aliases {
		aliasParts = append(aliasParts, alias.Name+" -> "+alias.Model)
	}
	fmt.Fprintf(out, "  Built-in aliases %s\n", strings.Join(aliasParts, ", "))
	routeParts := make([]string, 0, len(report.Routes))
	for _, route := range report.Routes {
		routeParts = append(routeParts, route.Prefix+" -> "+route.Provider)
	}
	fmt.Fprintf(out, "  Routes           %s\n", strings.Join(routeParts, ", "))
	fmt.Fprintf(out, "  Config model     %s", report.ConfiguredModel)
	if report.ResolvedConfiguredModel != "" && report.ResolvedConfiguredModel != report.ConfiguredModel {
		fmt.Fprintf(out, " -> %s", report.ResolvedConfiguredModel)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  Usage            %s\n", report.ModelCommand)
	return nil
}

func renderModelAliasesInventoryReport(out io.Writer, report modelAliasesInventoryReport, format string) error {
	report.Count = len(report.Aliases)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Model Aliases")
	if len(report.Aliases) == 0 {
		fmt.Fprintln(out, "  none")
		return nil
	}
	for _, alias := range report.Aliases {
		limits := ""
		if alias.MaxOutputTokens > 0 || alias.ContextWindowTokens > 0 {
			limits = fmt.Sprintf(" max=%d context=%d", alias.MaxOutputTokens, alias.ContextWindowTokens)
		}
		fmt.Fprintf(out, "  %-14s -> %-28s provider=%s%s\n", alias.Name, alias.Model, alias.Provider, limits)
	}
	return nil
}

func renderModelRoutesInventoryReport(out io.Writer, report modelRoutesInventoryReport, format string) error {
	report.Count = len(report.Routes)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Model Routes")
	if len(report.Routes) == 0 {
		fmt.Fprintln(out, "  none")
		return nil
	}
	for _, route := range report.Routes {
		fmt.Fprintf(out, "  %-18s -> %-10s protocol=%s env=%s\n", route.Prefix, route.Provider, route.WireProtocol, route.AuthEnv)
	}
	return nil
}

func renderModelSearchReport(out io.Writer, report modelSearchReport, format string) error {
	report.MatchCount = len(report.Aliases) + len(report.Routes) + len(report.Models)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Model Search")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Matches          %d\n", report.MatchCount)
	if len(report.Aliases) != 0 {
		fmt.Fprintln(out, "  Aliases")
		for _, alias := range report.Aliases {
			fmt.Fprintf(out, "    %-14s -> %-28s provider=%s\n", alias.Name, alias.Model, alias.Provider)
		}
	}
	if len(report.Routes) != 0 {
		fmt.Fprintln(out, "  Routes")
		for _, route := range report.Routes {
			fmt.Fprintf(out, "    %-18s -> %-10s protocol=%s\n", route.Prefix, route.Provider, route.WireProtocol)
		}
	}
	if len(report.Models) != 0 {
		fmt.Fprintln(out, "  Models")
		for _, model := range report.Models {
			fmt.Fprintf(out, "    %-28s provider=%s wire=%s\n", model.ResolvedModel, model.Provider, model.WireModel)
		}
	}
	return nil
}

func renderModelDetailReport(out io.Writer, report modelDetailReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Model")
	fmt.Fprintf(out, "  Requested        %s\n", report.RequestedModel)
	if report.Alias != "" {
		fmt.Fprintf(out, "  Alias            %s\n", report.Alias)
	}
	fmt.Fprintf(out, "  Resolved         %s\n", report.ResolvedModel)
	fmt.Fprintf(out, "  Provider         %s\n", report.Provider)
	fmt.Fprintf(out, "  Wire protocol    %s\n", report.WireProtocol)
	fmt.Fprintf(out, "  Wire model       %s\n", report.WireModel)
	fmt.Fprintf(out, "  Base URL         %s\n", report.BaseURL)
	if report.MaxOutputTokens > 0 || report.ContextWindowTokens > 0 {
		fmt.Fprintf(out, "  Token limits     max=%d context=%d\n", report.MaxOutputTokens, report.ContextWindowTokens)
	}
	flags := []string{}
	if report.OpenAICompatible {
		flags = append(flags, "openai-compatible")
	}
	if report.ReasoningModel {
		flags = append(flags, "reasoning")
	}
	if report.UsesMaxCompletionTokens {
		flags = append(flags, "max-completion-tokens")
	}
	if report.StripsTuningParams {
		flags = append(flags, "strips-tuning")
	}
	if report.SupportsStreamUsage {
		flags = append(flags, "stream-usage")
	}
	if report.SupportsExtraBodyParams {
		flags = append(flags, "extra-body")
	}
	if report.RejectsToolResultIsErrorField {
		flags = append(flags, "no-tool-is-error")
	}
	if report.RequiresReasoningContentHistory {
		flags = append(flags, "reasoning-content-history")
	}
	if len(flags) != 0 {
		fmt.Fprintf(out, "  Compatibility    %s\n", strings.Join(flags, ", "))
	}
	for _, diagnostic := range report.Diagnostics {
		fmt.Fprintf(out, "  Diagnostic       %s %s\n", diagnostic.Severity, diagnostic.Code)
	}
	return nil
}

type apiKeyRequest struct {
	Action string
	Key    string
	Format string
	Target string
	Path   string
}

type apiKeyReport struct {
	Kind          string `json:"kind"`
	Action        string `json:"action"`
	Status        string `json:"status"`
	Configured    bool   `json:"configured"`
	RedactedValue string `json:"redacted_value,omitempty"`
	Source        string `json:"source,omitempty"`
	Path          string `json:"path,omitempty"`
	Message       string `json:"message,omitempty"`
}

func (a *App) APIKey(args []string) error {
	req, err := parseAPIKeyArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "api_key", req.Key); err != nil {
			return err
		}
		a.Config.APIKey = req.Key
		req.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "api_key"); err != nil {
			return err
		}
		_, envValue := apiKeyEnvValue(a.Config.Model)
		a.Config.APIKey = envValue
		req.Path = path
	default:
		return fmt.Errorf("unknown api-key command %q", req.Action)
	}
	report := a.apiKeyReport(req.Action, req.Path)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderAPIKeyReport(a.Out, report)
	return nil
}

func parseAPIKeyArgs(args []string) (apiKeyRequest, error) {
	req := apiKeyRequest{Action: "status", Format: "text", Target: "user"}
	rest, err := parseAPIKeyOptions(args, &req)
	if err != nil {
		return req, err
	}
	normalizedFormat, err := normalizeOutputFormat("api-key", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return finishAPIKeyRequest(req, rest)
}

const apiKeyUsage = "codog api-key [status|set|clear] [KEY] [--key KEY] [--target user|project|local] [--path PATH] [--output-format text|json]"

func parseAPIKeyOptions(args []string, req *apiKeyRequest) ([]string, error) {
	var rest []string
	missing := func(flag string) error {
		return missingFlagValueError{Command: "api-key", Flag: flag, Usage: apiKeyUsage}
	}
	stringOption := func(target *string, rejectOutputFormat bool, trim bool) valueOption {
		return valueOption{missing: missing, rejectOutputFormat: rejectOutputFormat, set: func(value string) error {
			if trim {
				value = strings.TrimSpace(value)
			}
			*target = value
			return nil
		}}
	}
	options := map[string]valueOption{
		"--output-format": stringOption(&req.Format, false, false),
		"-o":              stringOption(&req.Format, false, false),
		"--target":        stringOption(&req.Target, true, false),
		"--path":          stringOption(&req.Path, true, false),
		"--key":           stringOption(&req.Key, true, true),
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--json" {
			req.Format = "json"
			continue
		}
		handled, err := consumeValueOption(args, &index, options)
		if err != nil {
			return rest, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return rest, unknownOptionError{Command: "api-key", Option: arg, Usage: apiKeyUsage}
		}
		rest = append(rest, arg)
	}
	return rest, nil
}

func finishAPIKeyRequest(req apiKeyRequest, rest []string) (apiKeyRequest, error) {
	if len(rest) == 0 {
		if req.Key != "" {
			req.Action = "set"
		}
		return validateAPIKeyRequest(req, apiKeyUsage)
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "api-key " + strings.ToLower(rest[0]), Args: rest[1:], Usage: apiKeyUsage}
		}
	case "set":
		req.Action = "set"
		if req.Key == "" {
			if len(rest) != 2 {
				return req, requiredArgumentError{Command: "api-key set", Argument: "KEY", Usage: apiKeyUsage}
			}
			req.Key = strings.TrimSpace(rest[1])
		} else if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "api-key set", Args: rest[1:], Usage: apiKeyUsage}
		}
	case "clear", "unset", "reset", "remove":
		req.Action = "clear"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "api-key " + strings.ToLower(rest[0]), Args: rest[1:], Usage: apiKeyUsage}
		}
	default:
		req.Action = "set"
		if req.Key != "" {
			return req, unexpectedExtraArgsError{Command: "api-key", Args: rest, Usage: apiKeyUsage}
		}
		if len(rest) != 1 {
			return req, unexpectedExtraArgsError{Command: "api-key", Args: rest[1:], Usage: apiKeyUsage}
		}
		req.Key = strings.TrimSpace(rest[0])
	}
	return validateAPIKeyRequest(req, apiKeyUsage)
}

func validateAPIKeyRequest(req apiKeyRequest, usage string) (apiKeyRequest, error) {
	if req.Action == "set" && strings.TrimSpace(req.Key) == "" {
		return req, requiredArgumentError{Command: "api-key set", Argument: "KEY", Usage: usage}
	}
	if req.Action != "set" && strings.TrimSpace(req.Key) != "" {
		return req, unexpectedExtraArgsError{Command: "api-key " + req.Action, Args: []string{"KEY"}, Usage: usage}
	}
	return req, nil
}

func (a *App) apiKeyReport(action string, path string) apiKeyReport {
	key := strings.TrimSpace(a.Config.APIKey)
	envName, envValue := apiKeyEnvValue(a.Config.Model)
	source := ""
	if key != "" {
		if envValue != "" && key == strings.TrimSpace(envValue) {
			source = envName
		} else {
			source = "config"
		}
	}
	report := apiKeyReport{
		Kind:          "api_key",
		Action:        action,
		Status:        "ok",
		Configured:    key != "",
		RedactedValue: redact(key),
		Source:        source,
		Path:          path,
	}
	switch {
	case action == "set":
		report.Message = "API key preference saved. Command output redacts the stored value."
	case action == "clear" && key != "":
		report.Message = fmt.Sprintf("API key removed from config; %s is still active in the environment.", source)
	case action == "clear":
		report.Message = "API key removed from config."
	case key != "":
		report.Message = "API key is configured. Command output redacts the value."
	default:
		report.Message = "No API key is configured. Set ANTHROPIC_API_KEY or run `codog api-key set KEY`."
	}
	return report
}

func apiKeyEnvValue(model string) (string, string) {
	name := ""
	value := ""
	if envValue := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); envValue != "" {
		name = "ANTHROPIC_API_KEY"
		value = envValue
	}
	if envValue := strings.TrimSpace(os.Getenv("CODOG_API_KEY")); envValue != "" {
		name = "CODOG_API_KEY"
		value = envValue
	}
	if strings.HasPrefix(strings.TrimSpace(model), "openai/") && value == "" {
		if envValue := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); envValue != "" {
			name = "OPENAI_API_KEY"
			value = envValue
		}
	}
	return name, value
}

func renderAPIKeyReport(out io.Writer, report apiKeyReport) {
	fmt.Fprintln(out, "API Key")
	fmt.Fprintf(out, "  Configured       %t\n", report.Configured)
	if report.RedactedValue != "" {
		fmt.Fprintf(out, "  Value            %s\n", report.RedactedValue)
	}
	if report.Source != "" {
		fmt.Fprintf(out, "  Source           %s\n", report.Source)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) Advisor(args []string) error {
	req, err := parseAdvisorArgs(args)
	if err != nil {
		return err
	}
	report := advisorReport{
		Kind:      "advisor",
		Action:    req.Action,
		Status:    "ok",
		Model:     a.Config.AdvisorModel,
		MainModel: a.Config.Model,
	}
	switch req.Action {
	case "show":
		if report.Model == "" {
			report.Message = "Advisor is not set. Use advisor MODEL to enable it."
		} else {
			report.Message = "Advisor model preference is set."
		}
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "advisor_model", req.Model); err != nil {
			return err
		}
		a.Config.AdvisorModel = req.Model
		report.Model = req.Model
		report.Path = path
		report.Message = "Advisor model preference saved."
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "advisor_model"); err != nil {
			return err
		}
		previous := a.Config.AdvisorModel
		a.Config.AdvisorModel = ""
		report.Model = ""
		report.Path = path
		if previous == "" {
			report.Message = "Advisor was already unset."
		} else {
			report.Message = "Advisor model preference cleared."
		}
	default:
		return fmt.Errorf("unknown advisor command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderAdvisorReport(a.Out, report)
	return nil
}

func parseAdvisorArgs(args []string) (advisorRequest, error) {
	req := advisorRequest{Action: "show", Format: "text", Target: "user"}
	const usage = "codog advisor [show|set|clear] [MODEL] [--target user|project|local] [--path PATH] [--output-format text|json]"
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "advisor", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "advisor", Flag: arg, Usage: usage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "advisor", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "advisor", Option: arg, Usage: usage}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("advisor", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "show", "status":
		req.Action = "show"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "advisor " + strings.ToLower(rest[0]), Args: rest[1:], Usage: usage}
		}
	case "unset", "off", "disable", "disabled", "clear", "reset":
		req.Action = "clear"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "advisor " + strings.ToLower(rest[0]), Args: rest[1:], Usage: usage}
		}
	case "set":
		req.Action = "set"
		if len(rest) < 2 {
			return req, requiredArgumentError{Command: "advisor set", Argument: "MODEL", Usage: usage}
		}
		req.Model = strings.TrimSpace(strings.Join(rest[1:], " "))
	default:
		req.Action = "set"
		req.Model = strings.TrimSpace(strings.Join(rest, " "))
	}
	if req.Action == "set" && req.Model == "" {
		return req, requiredArgumentError{Command: "advisor set", Argument: "MODEL", Usage: usage}
	}
	return req, nil
}

func renderAdvisorReport(out io.Writer, report advisorReport) {
	fmt.Fprintln(out, "Advisor")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.MainModel != "" {
		fmt.Fprintf(out, "  Main model       %s\n", report.MainModel)
	}
	if report.Model != "" {
		fmt.Fprintf(out, "  Advisor model    %s\n", report.Model)
	} else {
		fmt.Fprintln(out, "  Advisor model    unset")
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) handleModelSlash(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(a.Err, "model=%s\n", a.Config.Model)
		return
	}
	model := strings.TrimSpace(strings.Join(args, " "))
	if model == "" {
		fmt.Fprintln(a.Err, "usage: /model [name]")
		return
	}
	a.Config.Model = model
	fmt.Fprintf(a.Err, "model=%s\n", a.Config.Model)
}

type budgetRequest struct {
	Action    string
	Format    string
	Target    string
	Path      string
	MaxTokens *int
	MaxTurns  *int
}

type budgetSnapshot struct {
	MaxTokens int `json:"max_tokens"`
	MaxTurns  int `json:"max_turns"`
}

type budgetReport struct {
	Kind      string          `json:"kind"`
	Action    string          `json:"action"`
	Status    string          `json:"status"`
	MaxTokens int             `json:"max_tokens"`
	MaxTurns  int             `json:"max_turns"`
	Path      string          `json:"path,omitempty"`
	Target    string          `json:"target,omitempty"`
	Previous  *budgetSnapshot `json:"previous,omitempty"`
	Message   string          `json:"message,omitempty"`
}

func (a *App) Budget(args []string) error {
	req, err := parseBudgetArgs(args)
	if err != nil {
		return err
	}
	var previous *budgetSnapshot
	switch req.Action {
	case "show":
	case "set":
		if !req.hasSetValues() {
			return requiredArgumentError{Command: "budget set", Argument: "VALUE", Usage: budgetUsage}
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		req.Path = path
		snapshot := effectiveBudget(a.Config)
		previous = &snapshot
		if req.MaxTokens != nil {
			if _, err := config.SetFileValue(path, "max_tokens", *req.MaxTokens); err != nil {
				return err
			}
			a.Config.MaxTokens = *req.MaxTokens
		}
		if req.MaxTurns != nil {
			if _, err := config.SetFileValue(path, "max_turns", *req.MaxTurns); err != nil {
				return err
			}
			a.Config.MaxTurns = *req.MaxTurns
		}
	case "reset":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		req.Path = path
		snapshot := effectiveBudget(a.Config)
		previous = &snapshot
		if _, err := config.UnsetFileValue(path, "max_tokens"); err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "max_turns"); err != nil {
			return err
		}
		a.Config.MaxTokens = 0
		a.Config.MaxTurns = 0
	default:
		return fmt.Errorf("unknown budget action %q", req.Action)
	}
	report := buildBudgetReport(req, previous, a.Config)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBudgetReport(a.Out, report)
	return nil
}

const budgetUsage = "codog budget [status|show|ls|set|use|reset|clear|off] [--max-tokens N] [--max-turns N] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (req budgetRequest) hasSetValues() bool {
	return req.MaxTokens != nil || req.MaxTurns != nil
}

func parseBudgetArgs(args []string) (budgetRequest, error) {
	req := budgetRequest{Action: "show", Format: "text", Target: "user"}
	rest, err := parseBudgetOptions(args, &req)
	if err != nil {
		return req, err
	}
	normalizedFormat, err := normalizeOutputFormat("budget", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return applyBudgetAction(req, rest)
}

func parseBudgetOptions(args []string, req *budgetRequest) ([]string, error) {
	var rest []string
	for index := 0; index < len(args); index++ {
		handled, err := consumeBudgetOption(args, &index, req)
		if err != nil {
			return nil, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(args[index], "-") {
			return nil, unknownOptionError{Command: "budget", Option: args[index], Usage: budgetUsage}
		}
		rest = append(rest, args[index])
	}
	return rest, nil
}

func consumeBudgetOption(args []string, index *int, req *budgetRequest) (bool, error) {
	arg := args[*index]
	if arg == "--json" {
		req.Format = "json"
		return true, nil
	}
	if name, value, inline := strings.Cut(arg, "="); inline {
		return consumeInlineBudgetOption(name, value, req)
	}
	switch arg {
	case "--output-format", "-o":
		return true, consumeBudgetString(args, index, arg, &req.Format, false)
	case "--target":
		return true, consumeBudgetString(args, index, arg, &req.Target, true)
	case "--path":
		return true, consumeBudgetString(args, index, arg, &req.Path, true)
	case "--max-tokens", "--tokens":
		return true, consumeBudgetNumber(args, index, arg, &req.MaxTokens)
	case "--max-turns", "--turns":
		return true, consumeBudgetNumber(args, index, arg, &req.MaxTurns)
	default:
		return false, nil
	}
}

func consumeInlineBudgetOption(name, value string, req *budgetRequest) (bool, error) {
	switch name {
	case "--output-format":
		req.Format = value
	case "--target":
		req.Target = value
	case "--path":
		req.Path = value
	case "--max-tokens", "--tokens":
		return true, setBudgetNumber(name, value, &req.MaxTokens)
	case "--max-turns", "--turns":
		return true, setBudgetNumber(name, value, &req.MaxTurns)
	default:
		return false, nil
	}
	return true, nil
}

func consumeBudgetString(args []string, index *int, flag string, target *string, rejectFormat bool) error {
	value, err := budgetOptionValue(args, index, flag, rejectFormat)
	if err != nil {
		return err
	}
	*target = value
	return nil
}

func consumeBudgetNumber(args []string, index *int, flag string, target **int) error {
	value, err := budgetOptionValue(args, index, flag, true)
	if err != nil {
		return err
	}
	return setBudgetNumber(flag, value, target)
}

func budgetOptionValue(args []string, index *int, flag string, rejectFormat bool) (string, error) {
	(*index)++
	if *index >= len(args) || rejectFormat && isOutputFormatFlag(args[*index]) {
		return "", missingFlagValueError{Command: "budget", Flag: flag, Usage: budgetUsage}
	}
	return args[*index], nil
}

func setBudgetNumber(flag, raw string, target **int) error {
	value, err := parsePositiveIntOption(raw, flag, budgetUsage)
	if err != nil {
		return err
	}
	*target = &value
	return nil
}

func applyBudgetAction(req budgetRequest, rest []string) (budgetRequest, error) {
	if len(rest) == 0 {
		if req.hasSetValues() {
			req.Action = "set"
		}
		return req, nil
	}
	rawAction := strings.ToLower(strings.TrimSpace(rest[0]))
	action := normalizeBudgetAction(rawAction)
	switch action {
	case "show":
		return applyBudgetShowAction(req, rawAction, rest)
	case "set":
		req.Action = "set"
		return req, parseBudgetSetArgs(&req, rest[1:])
	case "reset":
		return applyBudgetResetAction(req, rawAction, rest)
	default:
		return applyImplicitBudgetSet(req, rest)
	}
}

func applyBudgetShowAction(req budgetRequest, rawAction string, rest []string) (budgetRequest, error) {
	if len(rest) > 1 {
		return req, unexpectedExtraArgsError{Command: "budget " + rawAction, Args: rest[1:], Usage: budgetUsage}
	}
	if req.hasSetValues() {
		return req, unexpectedExtraArgsError{Command: "budget " + rawAction, Args: budgetSetValueArgs(req), Usage: budgetUsage}
	}
	req.Action = "show"
	return req, nil
}

func applyBudgetResetAction(req budgetRequest, rawAction string, rest []string) (budgetRequest, error) {
	if len(rest) > 1 {
		return req, unexpectedExtraArgsError{Command: "budget " + rawAction, Args: rest[1:], Usage: budgetUsage}
	}
	if req.hasSetValues() {
		return req, unexpectedExtraArgsError{Command: "budget " + rawAction, Args: budgetSetValueArgs(req), Usage: budgetUsage}
	}
	req.Action = "reset"
	return req, nil
}

func applyImplicitBudgetSet(req budgetRequest, rest []string) (budgetRequest, error) {
	if len(rest) == 1 {
		value, err := parsePositiveIntOption(rest[0], "max-tokens", budgetUsage)
		if err == nil {
			req.Action = "set"
			req.MaxTokens = &value
			return req, nil
		}
	}
	return req, unexpectedExtraArgsError{Command: "budget", Args: []string{rest[0]}, Usage: budgetUsage}
}

func normalizeBudgetAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "status", "show", "list", "ls", "current", "view", "get":
		return "show"
	case "set", "use", "update", "configure", "config":
		return "set"
	case "reset", "clear", "default", "unset", "disable", "off":
		return "reset"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

func parseBudgetSetArgs(req *budgetRequest, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if len(args) == 1 {
		value, err := parsePositiveIntOption(args[0], "max-tokens", budgetUsage)
		if err != nil {
			return err
		}
		req.MaxTokens = &value
		return nil
	}
	for index := 0; index < len(args); index += 2 {
		if index+1 >= len(args) {
			return missingFlagValueError{Command: "budget set", Flag: args[index], Usage: budgetUsage}
		}
		if err := assignBudgetSetValue(req, args[index], args[index+1]); err != nil {
			return err
		}
	}
	return nil
}

func assignBudgetSetValue(req *budgetRequest, key string, raw string) error {
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimLeft(key, "-")))
	switch normalized {
	case "max-tokens", "tokens":
		value, err := parsePositiveIntOption(raw, key, budgetUsage)
		if err != nil {
			return err
		}
		req.MaxTokens = &value
	case "max-turns", "turns":
		value, err := parsePositiveIntOption(raw, key, budgetUsage)
		if err != nil {
			return err
		}
		req.MaxTurns = &value
	default:
		return unknownOptionError{Command: "budget set", Option: key, Usage: budgetUsage}
	}
	return nil
}

func budgetSetValueArgs(req budgetRequest) []string {
	args := []string{}
	if req.MaxTokens != nil {
		args = append(args, "--max-tokens")
	}
	if req.MaxTurns != nil {
		args = append(args, "--max-turns")
	}
	return args
}

func buildBudgetReport(req budgetRequest, previous *budgetSnapshot, cfg config.Config) budgetReport {
	current := effectiveBudget(cfg)
	target := req.Target
	if req.Path == "" {
		target = ""
	}
	report := budgetReport{
		Kind:      "budget",
		Action:    req.Action,
		Status:    "ok",
		MaxTokens: current.MaxTokens,
		MaxTurns:  current.MaxTurns,
		Path:      req.Path,
		Target:    target,
		Previous:  previous,
	}
	switch req.Action {
	case "set":
		report.Message = "Token budget preference saved."
	case "reset":
		report.Message = "Token budget preference cleared; defaults or higher-priority config will be used."
	default:
		report.Message = "Effective token budget limits."
	}
	return report
}

func effectiveBudget(cfg config.Config) budgetSnapshot {
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 8
	}
	return budgetSnapshot{MaxTokens: maxTokens, MaxTurns: maxTurns}
}

func renderBudgetReport(out io.Writer, report budgetReport) {
	fmt.Fprintln(out, "Budget")
	fmt.Fprintf(out, "  Max tokens       %d\n", report.MaxTokens)
	fmt.Fprintf(out, "  Max turns        %d\n", report.MaxTurns)
	if report.Previous != nil {
		fmt.Fprintf(out, "  Previous tokens  %d\n", report.Previous.MaxTokens)
		fmt.Fprintf(out, "  Previous turns   %d\n", report.Previous.MaxTurns)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type maxTokensRequest struct {
	Format string
	Value  *int
}

type maxTokensReport struct {
	Kind               string `json:"kind"`
	Action             string `json:"action"`
	Status             string `json:"status"`
	MaxTokens          int    `json:"max_tokens"`
	PreviousMaxTokens  *int   `json:"previous_max_tokens,omitempty"`
	RequestedMaxTokens *int   `json:"requested_max_tokens,omitempty"`
}

func (a *App) MaxTokens(args []string) error {
	req, err := parseMaxTokensArgs(args)
	if err != nil {
		return err
	}
	action := "show"
	var previous *int
	if req.Value != nil {
		action = "set"
		value := a.Config.MaxTokens
		previous = &value
		a.Config.MaxTokens = *req.Value
	}
	report := maxTokensReport{
		Kind:              "max_tokens",
		Action:            action,
		Status:            "ok",
		MaxTokens:         a.Config.MaxTokens,
		PreviousMaxTokens: previous,
	}
	return renderMaxTokensReport(a.Out, report, req.Format)
}

func (a *App) ResumedMaxTokens(args []string) error {
	req, err := parseMaxTokensArgs(args)
	if err != nil {
		return err
	}
	report := maxTokensReport{
		Kind:               "max_tokens",
		Action:             "show",
		Status:             "ok",
		MaxTokens:          a.Config.MaxTokens,
		RequestedMaxTokens: req.Value,
	}
	return renderMaxTokensReport(a.Out, report, req.Format)
}

func parseMaxTokensArgs(args []string) (maxTokensRequest, error) {
	req := maxTokensRequest{Format: "text"}
	const usage = "codog max-tokens [COUNT] [--output-format text|json]"
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "max-tokens", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "max-tokens", Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("max-tokens", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) > 1 {
		return req, unexpectedExtraArgsError{Command: "max-tokens", Args: positionals[1:], Usage: usage}
	}
	if len(positionals) == 1 {
		value, err := parsePositiveIntOption(positionals[0], "COUNT", usage)
		if err != nil {
			return req, err
		}
		req.Value = &value
	}
	return req, nil
}

func renderMaxTokensReport(out io.Writer, report maxTokensReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintf(out, "max_tokens=%d\n", report.MaxTokens)
	if report.RequestedMaxTokens != nil {
		fmt.Fprintf(out, "requested_max_tokens=%d\n", *report.RequestedMaxTokens)
	}
	return nil
}

func (a *App) handleMaxTokensSlash(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(a.Err, "max_tokens=%d\n", a.Config.MaxTokens)
		return
	}
	if len(args) != 1 {
		fmt.Fprintln(a.Err, "usage: /max-tokens [count]")
		return
	}
	value, err := strconv.Atoi(args[0])
	if err != nil || value <= 0 {
		fmt.Fprintln(a.Err, "max_tokens must be a positive integer")
		return
	}
	a.Config.MaxTokens = value
	fmt.Fprintf(a.Err, "max_tokens=%d\n", a.Config.MaxTokens)
}

type temperatureRequest struct {
	Action string
	Value  float64
	Format string
	Target string
	Path   string
}

type temperatureReport struct {
	Kind        string   `json:"kind"`
	Action      string   `json:"action"`
	Status      string   `json:"status"`
	Configured  bool     `json:"configured"`
	Temperature *float64 `json:"temperature,omitempty"`
	Path        string   `json:"path,omitempty"`
	Message     string   `json:"message,omitempty"`
}

func (a *App) Temperature(args []string) error {
	req, err := parseTemperatureArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "temperature", req.Value); err != nil {
			return err
		}
		value := req.Value
		a.Config.Temperature = &value
		req.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "temperature"); err != nil {
			return err
		}
		a.Config.Temperature = nil
		req.Path = path
	default:
		return fmt.Errorf("unknown temperature command %q", req.Action)
	}
	report := a.temperatureReport(req.Action, req.Path)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTemperatureReport(a.Out, report)
	return nil
}

func parseTemperatureArgs(args []string) (temperatureRequest, error) {
	req := temperatureRequest{Action: "status", Format: "text", Target: "user"}
	rest, err := parsePreferenceOptions(args, "temperature", temperatureUsage, &req.Format, &req.Target, &req.Path)
	if err != nil {
		return req, err
	}
	normalizedFormat, err := normalizeOutputFormat("temperature", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return finishTemperatureRequest(req, rest)
}

const temperatureUsage = "codog temperature [status|set|clear] [VALUE] [--target user|project|local] [--path PATH] [--output-format text|json]"

func finishTemperatureRequest(req temperatureRequest, rest []string) (temperatureRequest, error) {
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "temperature " + strings.ToLower(rest[0]), Args: rest[1:], Usage: temperatureUsage}
		}
	case "clear", "unset", "reset", "default":
		req.Action = "clear"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "temperature " + strings.ToLower(rest[0]), Args: rest[1:], Usage: temperatureUsage}
		}
	case "set":
		req.Action = "set"
		if len(rest) != 2 {
			if len(rest) < 2 {
				return req, requiredArgumentError{Command: "temperature set", Argument: "VALUE", Usage: temperatureUsage}
			}
			return req, unexpectedExtraArgsError{Command: "temperature set", Args: rest[2:], Usage: temperatureUsage}
		}
		value, err := parseTemperatureValue(rest[1], temperatureUsage)
		if err != nil {
			return req, err
		}
		req.Value = value
	default:
		req.Action = "set"
		if len(rest) != 1 {
			return req, unexpectedExtraArgsError{Command: "temperature", Args: rest[1:], Usage: temperatureUsage}
		}
		value, err := parseTemperatureValue(rest[0], temperatureUsage)
		if err != nil {
			return req, err
		}
		req.Value = value
	}
	return req, nil
}

func parseTemperatureValue(raw string, usage string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, invalidFlagValueError{
			Flag:    "temperature",
			Value:   raw,
			Message: "temperature must be a number between 0 and 1",
			Usage:   usage,
		}
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return 0, invalidFlagValueError{
			Flag:    "temperature",
			Value:   raw,
			Message: "temperature must be between 0 and 1",
			Usage:   usage,
		}
	}
	return value, nil
}

func (a *App) temperatureReport(action string, path string) temperatureReport {
	report := temperatureReport{
		Kind:        "temperature",
		Action:      action,
		Status:      "ok",
		Configured:  a.Config.Temperature != nil,
		Temperature: a.Config.Temperature,
		Path:        path,
	}
	switch {
	case action == "set":
		report.Message = "Sampling temperature preference saved."
	case action == "clear":
		report.Message = "Sampling temperature preference cleared; provider defaults will be used."
	case report.Configured:
		report.Message = "Sampling temperature preference is configured."
	default:
		report.Message = "Sampling temperature is unset; provider defaults will be used."
	}
	return report
}

func renderTemperatureReport(out io.Writer, report temperatureReport) {
	fmt.Fprintln(out, "Temperature")
	fmt.Fprintf(out, "  Configured       %t\n", report.Configured)
	if report.Temperature != nil {
		fmt.Fprintf(out, "  Value            %s\n", formatFloatValue(*report.Temperature))
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func formatFloatValue(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

type maxTurnsRequest struct {
	Format string
	Value  *int
}

type maxTurnsReport struct {
	Kind              string `json:"kind"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	MaxTurns          int    `json:"max_turns"`
	PreviousMaxTurns  *int   `json:"previous_max_turns,omitempty"`
	RequestedMaxTurns *int   `json:"requested_max_turns,omitempty"`
}

func (a *App) MaxTurns(args []string) error {
	req, err := parseMaxTurnsArgs(args)
	if err != nil {
		return err
	}
	action := "show"
	var previous *int
	if req.Value != nil {
		action = "set"
		value := a.Config.MaxTurns
		previous = &value
		a.Config.MaxTurns = *req.Value
	}
	report := maxTurnsReport{
		Kind:             "max_turns",
		Action:           action,
		Status:           "ok",
		MaxTurns:         a.Config.MaxTurns,
		PreviousMaxTurns: previous,
	}
	return renderMaxTurnsReport(a.Out, report, req.Format)
}

func (a *App) ResumedMaxTurns(args []string) error {
	req, err := parseMaxTurnsArgs(args)
	if err != nil {
		return err
	}
	report := maxTurnsReport{
		Kind:              "max_turns",
		Action:            "show",
		Status:            "ok",
		MaxTurns:          a.Config.MaxTurns,
		RequestedMaxTurns: req.Value,
	}
	return renderMaxTurnsReport(a.Out, report, req.Format)
}

func parseMaxTurnsArgs(args []string) (maxTurnsRequest, error) {
	req := maxTurnsRequest{Format: "text"}
	const usage = "codog max-turns [COUNT] [--output-format text|json]"
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "max-turns", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "max-turns", Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("max-turns", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) > 1 {
		return req, unexpectedExtraArgsError{Command: "max-turns", Args: positionals[1:], Usage: usage}
	}
	if len(positionals) == 1 {
		value, err := parsePositiveIntOption(positionals[0], "COUNT", usage)
		if err != nil {
			return req, err
		}
		req.Value = &value
	}
	return req, nil
}

func renderMaxTurnsReport(out io.Writer, report maxTurnsReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintf(out, "max_turns=%d\n", report.MaxTurns)
	if report.RequestedMaxTurns != nil {
		fmt.Fprintf(out, "requested_max_turns=%d\n", *report.RequestedMaxTurns)
	}
	return nil
}

func (a *App) handleMaxTurnsSlash(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(a.Err, "max_turns=%d\n", a.Config.MaxTurns)
		return
	}
	if len(args) != 1 {
		fmt.Fprintln(a.Err, "usage: /max-turns [count]")
		return
	}
	value, err := strconv.Atoi(args[0])
	if err != nil || value <= 0 {
		fmt.Fprintln(a.Err, "max_turns must be a positive integer")
		return
	}
	a.Config.MaxTurns = value
	fmt.Fprintf(a.Err, "max_turns=%d\n", a.Config.MaxTurns)
}

func (a *App) handleToolDetailsSlash(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(a.Err, "usage: /tool-details TOOL")
		return
	}
	if a.Tools == nil {
		fmt.Fprintln(a.Err, "error: tool registry is not initialized")
		return
	}
	info, ok := a.Tools.Info(args[0])
	if !ok {
		fmt.Fprintf(a.Err, "unknown tool: %s\n", args[0])
		return
	}
	renderToolInfo(a.Out, info)
}

func renderToolInfo(out io.Writer, info tools.ToolInfo) {
	fmt.Fprintln(out, "Tool")
	fmt.Fprintf(out, "  Name             %s\n", info.Name)
	fmt.Fprintf(out, "  Permission       %s\n", info.Permission)
	fmt.Fprintf(out, "  Description      %s\n", info.Description)
	data, _ := json.MarshalIndent(info.InputSchema, "  ", "  ")
	fmt.Fprintln(out, "  Input schema")
	fmt.Fprintln(out, string(data))
}

type debugToolCallRequest struct {
	Tool      string
	Input     json.RawMessage
	Format    string
	SessionID string
}

type debugToolCallReport struct {
	Kind       string           `json:"kind"`
	Tool       string           `json:"tool"`
	Permission tools.Permission `json:"permission"`
	Success    bool             `json:"success"`
	DurationMS int64            `json:"duration_ms"`
	Output     string           `json:"output,omitempty"`
	Error      string           `json:"error,omitempty"`
}

func (a *App) DebugToolCall(ctx context.Context, args []string, overrides config.FlagOverrides) error {
	req, err := parseDebugToolCallArgs(args, overrides)
	if err != nil {
		return err
	}
	if a.Tools == nil {
		return errors.New("tool registry is not initialized")
	}
	info, ok := a.Tools.Info(req.Tool)
	if !ok && !a.mcpToolsLoaded && len(a.Config.MCPServers) > 0 {
		if err := a.RegisterMCPTools(ctx); err != nil {
			return err
		}
		info, ok = a.Tools.Info(req.Tool)
	}
	if !ok {
		return fmt.Errorf("unknown tool %q", req.Tool)
	}
	start := time.Now()
	output, execErr := a.Tools.Execute(tools.ContextWithSessionID(ctx, req.SessionID), req.Tool, req.Input, a.prompter(req.SessionID))
	report := debugToolCallReport{
		Kind:       "debug_tool_call",
		Tool:       info.Name,
		Permission: info.Permission,
		Success:    execErr == nil,
		DurationMS: time.Since(start).Milliseconds(),
		Output:     output,
	}
	if execErr != nil {
		report.Error = execErr.Error()
	}
	a.onToolUse(req.SessionID)(runloop.ToolCall{
		Name:    info.Name,
		Input:   string(req.Input),
		Output:  output,
		IsError: execErr != nil,
	})
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDebugToolCallReport(a.Out, report)
	return nil
}

func parseDebugToolCallArgs(args []string, overrides config.FlagOverrides) (debugToolCallRequest, error) {
	const usage = "codog debug-tool-call TOOL JSON [--session ID|--resume ID] [--json|--output-format text|json]"
	req := debugToolCallRequest{Format: "text", SessionID: firstNonEmpty(overrides.Resume, overrides.SessionID)}
	inputParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "debug-tool-call", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "debug-tool-call", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "debug-tool-call", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-") && req.Tool == "":
			return req, unknownOptionError{Command: "debug-tool-call", Option: arg, Usage: usage}
		default:
			if req.Tool == "" {
				req.Tool = arg
				continue
			}
			inputParts = append(inputParts, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("debug-tool-call", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if strings.TrimSpace(req.Tool) == "" {
		return req, requiredArgumentError{Command: "debug-tool-call", Argument: "TOOL", Usage: usage}
	}
	input := strings.TrimSpace(strings.Join(inputParts, " "))
	if input == "" {
		return req, requiredArgumentError{Command: "debug-tool-call", Argument: "JSON", Usage: usage}
	}
	if !json.Valid([]byte(input)) {
		return req, invalidFlagValueError{Flag: "JSON", Value: input, Message: "debug-tool-call input must be valid JSON", Usage: usage}
	}
	req.Input = json.RawMessage(input)
	return req, nil
}

func renderDebugToolCallReport(out io.Writer, report debugToolCallReport) {
	fmt.Fprintln(out, "Tool Call")
	fmt.Fprintf(out, "  Tool             %s\n", report.Tool)
	fmt.Fprintf(out, "  Permission       %s\n", report.Permission)
	fmt.Fprintf(out, "  Success          %t\n", report.Success)
	fmt.Fprintf(out, "  Duration         %dms\n", report.DurationMS)
	if report.Error != "" {
		fmt.Fprintf(out, "  Error            %s\n", report.Error)
	}
	if report.Output != "" {
		fmt.Fprintln(out, "Output:")
		fmt.Fprintln(out, report.Output)
	}
}

type permissionsRequest struct {
	Action string
	Format string
	Mode   string
	Target string
	Path   string
}

type permissionModeReport struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Current     bool   `json:"current"`
}

type permissionsReport struct {
	Kind            string                 `json:"kind"`
	Action          string                 `json:"action"`
	Status          string                 `json:"status"`
	PermissionMode  string                 `json:"permission_mode"`
	PermissionRules config.PermissionRules `json:"permission_rules"`
	PreviousMode    string                 `json:"previous_mode,omitempty"`
	Path            string                 `json:"path,omitempty"`
	Modes           []permissionModeReport `json:"modes"`
}

func (a *App) Permissions(args []string) error {
	req, err := parsePermissionsArgs(args)
	if err != nil {
		return err
	}
	previous := a.Config.PermissionMode
	path := ""
	switch req.Action {
	case "set":
		path, err = a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "permission_mode", req.Mode); err != nil {
			return err
		}
		a.Config.PermissionMode = req.Mode
	case "clear":
		path, err = a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "permission_mode"); err != nil {
			return err
		}
		a.Config.PermissionMode = "workspace-write"
	}
	report := permissionsReport{
		Kind:            "permissions",
		Action:          req.Action,
		Status:          "ok",
		PermissionMode:  a.Config.PermissionMode,
		PermissionRules: a.Config.PermissionRules,
		Path:            path,
		Modes:           permissionModeReports(a.Config.PermissionMode),
	}
	if req.Action == "set" || req.Action == "clear" {
		report.PreviousMode = previous
	}
	return renderPermissionsReport(a.Out, report, req.Format)
}

func parsePermissionsArgs(args []string) (permissionsRequest, error) {
	parser := permissionsArgParser{req: permissionsRequest{Action: "show", Target: "user"}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--json" {
			parser.req.Format = "json"
			continue
		}
		handled, err := consumeValueOption(args, &index, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return parser.req, fmt.Errorf("unknown permissions flag %q", arg)
		}
		parser.positionals = append(parser.positionals, arg)
	}
	if err := parser.finish(); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

const permissionsUsage = "codog permissions [show|MODE|set MODE|clear] [--target user|project|local] [--json|--output-format text|json]"
const permissionsSetUsage = "codog permissions set MODE [--target user|project|local] [--json|--output-format text|json]"

type permissionsArgParser struct {
	req         permissionsRequest
	positionals []string
}

func (p *permissionsArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format": stringValueOption(&p.req.Format, "permissions output format is required"),
		"-o":              stringValueOption(&p.req.Format, "permissions output format is required"),
		"--target":        stringValueOption(&p.req.Target, "permissions target is required"),
		"--path":          stringValueOption(&p.req.Path, "permissions config path is required"),
	}
}

func (p *permissionsArgParser) finish() error {
	if len(p.positionals) == 0 {
		p.req.Format = firstNonEmpty(p.req.Format, "json")
		return validateTextOrJSON(p.req.Format, "permissions")
	}
	if err := p.applyAction(); err != nil {
		return err
	}
	if p.req.Action == "set" && !validPermissionMode(p.req.Mode) {
		return invalidFlagValueError{
			Flag:    "mode",
			Value:   p.req.Mode,
			Message: fmt.Sprintf("unknown permission mode: %s", p.req.Mode),
			Hint:    unknownPermissionModeHint(p.req.Mode),
			Usage:   permissionsUsage,
		}
	}
	if p.req.Action == "set" {
		p.req.Mode, _ = config.NormalizePermissionModeLabel(p.req.Mode)
	}
	if p.req.Format == "" {
		p.req.Format = "text"
		if p.req.Action == "show" {
			p.req.Format = "json"
		}
	}
	return validateTextOrJSON(p.req.Format, "permissions")
}

func (p *permissionsArgParser) applyAction() error {
	switch strings.ToLower(strings.TrimSpace(p.positionals[0])) {
	case "show", "status", "list":
		if len(p.positionals) > 1 {
			return unexpectedExtraArgsError{
				Command: "permissions show",
				Args:    append([]string(nil), p.positionals[1:]...),
				Usage:   permissionsUsage,
			}
		}
		p.req.Action = "show"
	case "mode", "set":
		if len(p.positionals) < 2 {
			return requiredArgumentError{Command: "permissions set", Argument: "MODE", Usage: permissionsSetUsage}
		}
		if len(p.positionals) > 2 {
			return unexpectedExtraArgsError{
				Command: "permissions set",
				Args:    append([]string(nil), p.positionals[2:]...),
				Usage:   permissionsSetUsage,
			}
		}
		p.req.Action = "set"
		p.req.Mode = p.positionals[1]
	case "clear", "reset", "unset", "default":
		if len(p.positionals) > 1 {
			return unexpectedExtraArgsError{
				Command: "permissions clear",
				Args:    append([]string(nil), p.positionals[1:]...),
				Usage:   "codog permissions clear [--target user|project|local] [--json|--output-format text|json]",
			}
		}
		p.req.Action = "clear"
	default:
		if len(p.positionals) > 1 {
			return unexpectedExtraArgsError{
				Command: "permissions",
				Args:    append([]string(nil), p.positionals[1:]...),
				Usage:   permissionsUsage,
			}
		}
		p.req.Action = "set"
		p.req.Mode = p.positionals[0]
	}
	return nil
}

func renderPermissionsReport(out io.Writer, report permissionsReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	if report.Action == "set" || report.Action == "clear" {
		fmt.Fprintln(out, "Permissions updated")
		fmt.Fprintf(out, "  Result           mode %s\n", report.Action)
		if report.PreviousMode != "" {
			fmt.Fprintf(out, "  Previous mode    %s\n", report.PreviousMode)
		}
		fmt.Fprintf(out, "  Active mode      %s\n", report.PermissionMode)
		if report.Path != "" {
			fmt.Fprintf(out, "  Config path      %s\n", report.Path)
		}
		fmt.Fprintln(out, "  Applies to       subsequent tool calls")
		return nil
	}
	fmt.Fprintln(out, "Permissions")
	fmt.Fprintf(out, "  Active mode      %s\n", report.PermissionMode)
	fmt.Fprintln(out, "  Mode status      live session default")
	if len(report.PermissionRules.Allow) > 0 {
		fmt.Fprintf(out, "  Allow rules      %s\n", strings.Join(report.PermissionRules.Allow, ", "))
	}
	if len(report.PermissionRules.Deny) > 0 {
		fmt.Fprintf(out, "  Deny rules       %s\n", strings.Join(report.PermissionRules.Deny, ", "))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Modes")
	for _, mode := range report.Modes {
		marker := "○ available"
		if mode.Current {
			marker = "● current"
		}
		fmt.Fprintf(out, "  %-18s %-11s %s\n", mode.Name, marker, mode.Description)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage")
	fmt.Fprintln(out, "  Inspect current mode with /permissions")
	fmt.Fprintln(out, "  Switch modes with /permissions <mode>")
	return nil
}

func permissionModeReports(current string) []permissionModeReport {
	modes := []permissionModeReport{
		{Name: string(tools.PermissionReadOnly), Description: "Read/search tools only"},
		{Name: string(tools.PermissionWorkspace), Description: "Edit files inside the workspace"},
		{Name: string(tools.PermissionDanger), Description: "Unrestricted tool access"},
		{Name: string(tools.PermissionPrompt), Description: "Ask before tool calls that need approval"},
		{Name: string(tools.PermissionAllow), Description: "Allow tool calls without prompting"},
	}
	for index := range modes {
		modes[index].Current = strings.EqualFold(modes[index].Name, strings.TrimSpace(current))
	}
	return modes
}

func (a *App) handlePermissionsSlash(args []string) {
	if err := a.Permissions(args); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

type allowedToolsRequest struct {
	Action string
	Format string
	Tools  []string
	Target string
	Path   string
}

type allowedToolsReport struct {
	Kind   string   `json:"kind"`
	Action string   `json:"action"`
	Status string   `json:"status"`
	Count  int      `json:"count"`
	Rules  []string `json:"rules"`
	Target string   `json:"target,omitempty"`
	Path   string   `json:"path,omitempty"`
}

var allowedToolsActionCandidates = []string{"list", "show", "add", "remove", "rm", "delete", "clear"}

func (a *App) AllowedTools(args []string) error {
	req, err := parseAllowedToolsArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "list", "show":
	case "add":
		if err := a.validateToolRuleValues("allowed-tools add", req.Tools); err != nil {
			return err
		}
		a.Config.PermissionRules.Allow = addRuleValues(a.Config.PermissionRules.Allow, req.Tools)
	case "remove":
		a.Config.PermissionRules.Allow = removeRuleValues(a.Config.PermissionRules.Allow, req.Tools)
	case "clear":
		a.Config.PermissionRules.Allow = nil
	default:
		return fmt.Errorf("unknown allowed-tools action: %s", req.Action)
	}
	path := ""
	if req.Action == "add" || req.Action == "remove" || req.Action == "clear" {
		var err error
		path, err = a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if len(a.Config.PermissionRules.Allow) == 0 {
			if _, err := config.UnsetFileValue(path, "permission_rules.allow"); err != nil {
				return err
			}
		} else if _, err := config.SetFileValue(path, "permission_rules.allow", a.Config.PermissionRules.Allow); err != nil {
			return err
		}
	}
	report := allowedToolsReport{
		Kind:   "allowed_tools",
		Action: req.Action,
		Status: "ok",
		Count:  len(a.Config.PermissionRules.Allow),
		Rules:  append([]string(nil), a.Config.PermissionRules.Allow...),
		Target: req.Target,
		Path:   path,
	}
	return renderAllowedToolsReport(a.Out, report, req.Format)
}

func parseAllowedToolsArgs(args []string) (allowedToolsRequest, error) {
	const usage = "codog allowed-tools [list|add TOOL...|remove TOOL...|clear] [--target user|project|local] [--path PATH] [--json|--output-format text|json]"
	req := allowedToolsRequest{Action: "list", Format: "text", Target: "user"}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "allowed-tools", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "allowed-tools", Flag: arg, Usage: usage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "allowed-tools", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "allowed-tools", Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("allowed-tools", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) == 0 {
		return req, nil
	}
	switch strings.ToLower(strings.TrimSpace(positionals[0])) {
	case "list", "show":
		req.Action = strings.ToLower(strings.TrimSpace(positionals[0]))
		if len(positionals) > 1 {
			return req, unexpectedExtraArgsError{Command: "allowed-tools " + req.Action, Args: positionals[1:], Usage: usage}
		}
	case "add":
		req.Action = "add"
		req.Tools = append([]string(nil), positionals[1:]...)
		if len(req.Tools) == 0 {
			return req, requiredArgumentError{Command: "allowed-tools add", Argument: "TOOL", Usage: usage}
		}
	case "remove", "rm", "delete":
		req.Action = "remove"
		req.Tools = append([]string(nil), positionals[1:]...)
		if len(req.Tools) == 0 {
			return req, requiredArgumentError{Command: "allowed-tools remove", Argument: "TOOL", Usage: usage}
		}
	case "clear":
		req.Action = "clear"
		if len(positionals) > 1 {
			return req, unexpectedExtraArgsError{Command: "allowed-tools clear", Args: positionals[1:], Usage: usage}
		}
	default:
		action := strings.TrimSpace(positionals[0])
		return req, unknownActionError{
			Command:     "allowed-tools",
			Action:      action,
			Expected:    append([]string(nil), allowedToolsActionCandidates...),
			Suggestions: toolnames.Suggestions(action, allowedToolsActionCandidates, 4),
			Usage:       usage,
		}
	}
	return req, nil
}

func renderAllowedToolsReport(out io.Writer, report allowedToolsReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	renderAllowedTools(out, report.Rules)
	return nil
}

func (a *App) handleAllowedToolsSlash(args []string) {
	action := "list"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "list", "show":
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /allowed-tools add TOOL [TOOL...]")
			return
		}
		if err := a.validateToolRuleValues("/allowed-tools add", args[1:]); err != nil {
			fmt.Fprintln(a.Err, err)
			return
		}
		a.Config.PermissionRules.Allow = addRuleValues(a.Config.PermissionRules.Allow, args[1:])
	case "remove", "rm", "delete":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /allowed-tools remove TOOL [TOOL...]")
			return
		}
		a.Config.PermissionRules.Allow = removeRuleValues(a.Config.PermissionRules.Allow, args[1:])
	case "clear":
		a.Config.PermissionRules.Allow = nil
	default:
		fmt.Fprintf(a.Err, "unknown /allowed-tools action: %s\n", args[0])
		if suggestions := toolnames.Suggestions(args[0], allowedToolsActionCandidates, 4); len(suggestions) > 0 {
			fmt.Fprintf(a.Err, "Did you mean: /allowed-tools %s?\n", strings.Join(suggestions, ", /allowed-tools "))
		}
		return
	}
	renderAllowedTools(a.Out, a.Config.PermissionRules.Allow)
}

func renderAllowedTools(out io.Writer, rules []string) {
	fmt.Fprintln(out, "Allowed tools")
	if len(rules) == 0 {
		fmt.Fprintln(out, "  Result           no allow rules configured")
		return
	}
	fmt.Fprintf(out, "  Count            %d\n", len(rules))
	fmt.Fprintln(out)
	for index, rule := range rules {
		fmt.Fprintf(out, "  %d. %s\n", index+1, rule)
	}
}

func addRuleValues(current []string, values []string) []string {
	next := append([]string(nil), current...)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || containsFold(next, value) {
			continue
		}
		next = append(next, value)
	}
	return next
}

func removeRuleValues(current []string, values []string) []string {
	remove := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			remove[value] = struct{}{}
		}
	}
	next := make([]string, 0, len(current))
	for _, value := range current {
		if _, ok := remove[strings.ToLower(value)]; ok {
			continue
		}
		next = append(next, value)
	}
	return next
}

func (a *App) runtimeConfigPayload(args []string) (any, error) {
	cfg := redactedConfig(a.Config)
	return configSectionPayload(cfg, args)
}

type configLoadReport struct {
	Kind                string                       `json:"kind"`
	Action              string                       `json:"action"`
	Status              string                       `json:"status"`
	ErrorKind           string                       `json:"error_kind"`
	Message             string                       `json:"message"`
	Hint                string                       `json:"hint"`
	ConfigLoadError     string                       `json:"config_load_error"`
	ConfigLoadErrorKind string                       `json:"config_load_error_kind"`
	Paths               []string                     `json:"paths"`
	Files               []configFileInspectionReport `json:"files,omitempty"`
	Config              config.Config                `json:"config"`
}

func buildConfigLoadReport(cfg config.Config, paths []string, command string, args []string, loadErr error) configLoadReport {
	action := configActionFromArgs(args)
	if strings.HasPrefix(strings.TrimSpace(command), "/") && action == "show" {
		action = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(command)), "/")
		if action == "settings" {
			action = "show"
		}
		if action == "config" {
			action = "show"
		}
	}
	kind := buildCLIErrorReport(loadErr).ErrorKind
	if strings.TrimSpace(kind) == "" {
		kind = "config_load_failed"
	}
	message := strings.TrimSpace(loadErr.Error())
	return configLoadReport{
		Kind:                "config",
		Action:              action,
		Status:              "error",
		ErrorKind:           kind,
		Message:             message,
		Hint:                "Fix or remove the listed config file, then rerun `codog config paths` or `codog doctor`.",
		ConfigLoadError:     message,
		ConfigLoadErrorKind: kind,
		Paths:               append([]string(nil), paths...),
		Files:               inspectConfigFiles(paths),
		Config:              cfg,
	}
}

func configActionFromArgs(args []string) string {
	if len(args) == 0 {
		return "show"
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "", "show":
		return "show"
	case "get":
		if len(args) > 1 {
			return "get:" + strings.ToLower(strings.TrimSpace(args[1]))
		}
		return "get"
	default:
		return action
	}
}

func renderConfigLoadReport(out io.Writer, format string, report configLoadReport) error {
	if format == "text" {
		fmt.Fprintln(out, "Config")
		fmt.Fprintf(out, "  Status           %s\n", report.Status)
		fmt.Fprintf(out, "  Action           %s\n", report.Action)
		fmt.Fprintf(out, "  Error kind       %s\n", report.ErrorKind)
		fmt.Fprintf(out, "  Config load      %s\n", report.ConfigLoadError)
		if len(report.Paths) != 0 {
			fmt.Fprintf(out, "  Paths            %s\n", strings.Join(report.Paths, ", "))
		}
		fmt.Fprintf(out, "  Model            %s\n", report.Config.Model)
		fmt.Fprintf(out, "  Permission mode  %s\n", report.Config.PermissionMode)
		fmt.Fprintf(out, "  Hint             %s\n", report.Hint)
		return nil
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(out, string(data))
	return nil
}

func renderConfigInspection(out io.Writer, cfg config.Config, paths []string, args []string) error {
	req, err := parseConfigInspectionArgs(args)
	if err != nil {
		return err
	}
	args = req.Args
	if len(args) == 0 {
		return renderConfigInspectionPayload(out, req.Format, configInspectionEnvelope("show", cfg, paths))
	}
	if strings.EqualFold(args[0], "help") {
		if len(args) > 1 {
			return renderCLIError(out, unexpectedExtraArgsError{
				Command: "config help",
				Args:    append([]string(nil), args[1:]...),
				Usage:   "codog config help [--json|--output-format text|json]",
			}, req.Format)
		}
		return renderConfigInspectionPayload(out, req.Format, buildConfigHelpReport())
	}
	if strings.EqualFold(args[0], "reset") {
		report, err := resetConfigFileCommand(args, paths)
		if err != nil {
			return err
		}
		return renderConfigInspectionPayload(out, req.Format, report)
	}
	if strings.EqualFold(args[0], "set") || strings.EqualFold(args[0], "unset") {
		report, err := mutateConfigFile(args, paths)
		if err != nil {
			return err
		}
		return renderConfigInspectionPayload(out, req.Format, report)
	}
	if strings.EqualFold(args[0], "paths") {
		return renderConfigInspectionPayload(out, req.Format, configPathsInspectionEnvelope(paths))
	}
	if strings.EqualFold(args[0], "validate") {
		report, err := buildConfigValidationReport(paths, args[1:])
		if err != nil {
			return err
		}
		return renderConfigValidationReport(out, req.Format, report)
	}
	if strings.EqualFold(args[0], "show") || strings.EqualFold(args[0], "inspect") {
		if len(args) > 1 {
			action := strings.ToLower(args[0])
			return renderCLIError(out, unexpectedExtraArgsError{
				Command: "config " + action,
				Args:    append([]string(nil), args[1:]...),
				Usage:   "codog config " + action + " [--json|--output-format text|json]",
			}, req.Format)
		}
		return renderConfigInspectionPayload(out, req.Format, configInspectionEnvelope(strings.ToLower(args[0]), cfg, paths))
	}
	if strings.EqualFold(args[0], "get") {
		if len(args) < 2 {
			return errors.New("usage: codog config get SECTION")
		}
		args = args[1:]
	}
	payload, err := configSectionPayload(cfg, args)
	if err != nil {
		return err
	}
	return renderConfigInspectionPayload(out, req.Format, payload)
}

func renderConfigValidateWithoutLoadedConfig(out io.Writer, args []string, overrides config.FlagOverrides) (bool, error) {
	if !configValidateRequested(args) {
		return false, nil
	}
	req, err := parseConfigInspectionArgs(args)
	if err != nil {
		return true, err
	}
	if len(req.Args) == 0 || !strings.EqualFold(req.Args[0], "validate") {
		return false, nil
	}
	paths, err := config.InspectionPaths(overrides)
	if err != nil {
		return true, err
	}
	report, err := buildConfigValidationReport(paths, req.Args[1:])
	if err != nil {
		return true, err
	}
	return true, renderConfigValidationReport(out, req.Format, report)
}

func configValidateRequested(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--output-format" || arg == "-o":
			index++
		case strings.HasPrefix(arg, "--output-format="), arg == "--json":
		case strings.HasPrefix(arg, "-"):
		default:
			return strings.EqualFold(arg, "validate")
		}
	}
	return false
}

func configInspectionEnvelope(action string, cfg config.Config, paths []string) map[string]any {
	mcpValidation := buildMCPValidation(cfg.MCPServers)
	hookValidation := buildHookValidation(cfg.Hooks)
	status := "ok"
	if mcpValidation.InvalidCount > 0 || hookValidation.InvalidCount > 0 {
		status = "degraded"
	}
	return map[string]any{
		"kind":            "config",
		"action":          strings.TrimSpace(action),
		"status":          status,
		"config":          cfg,
		"paths":           append([]string(nil), paths...),
		"files":           inspectConfigFiles(paths),
		"mcp_validation":  mcpValidation,
		"hook_validation": hookValidation,
	}
}

func configPathsInspectionEnvelope(paths []string) map[string]any {
	return map[string]any{
		"kind":   "config",
		"action": "paths",
		"status": "ok",
		"paths":  append([]string(nil), paths...),
		"files":  inspectConfigFiles(paths),
	}
}

type configFileInspectionReport struct {
	Path             string                      `json:"path"`
	Source           string                      `json:"source"`
	PrecedenceRank   int                         `json:"precedence_rank"`
	Status           string                      `json:"status"`
	Present          bool                        `json:"present"`
	Loaded           bool                        `json:"loaded"`
	Reason           string                      `json:"reason,omitempty"`
	Detail           string                      `json:"detail,omitempty"`
	KeyCount         int                         `json:"key_count,omitempty"`
	Keys             []string                    `json:"keys,omitempty"`
	KeyPaths         []string                    `json:"key_paths,omitempty"`
	WinsForKeys      []string                    `json:"wins_for_keys,omitempty"`
	ShadowedKeys     []string                    `json:"shadowed_keys,omitempty"`
	ValidationStatus string                      `json:"validation_status,omitempty"`
	ErrorCount       int                         `json:"error_count,omitempty"`
	WarningCount     int                         `json:"warning_count,omitempty"`
	Errors           []configvalidate.Diagnostic `json:"errors,omitempty"`
	Warnings         []configvalidate.Diagnostic `json:"warnings,omitempty"`
	ErrorKind        string                      `json:"error_kind,omitempty"`
	Error            string                      `json:"error,omitempty"`
}

func inspectConfigFiles(paths []string) []configFileInspectionReport {
	reports := make([]configFileInspectionReport, 0, len(paths))
	lastKeyOwner := map[string]int{}
	for index, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		report := configFileInspectionReport{
			Path:           path,
			Source:         configFileInspectionSource(path, len(paths)),
			PrecedenceRank: index + 1,
			Status:         "not_found",
			Reason:         "not_found",
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				reports = append(reports, report)
				continue
			}
			report.Status = "load_error"
			report.Present = true
			report.Reason = "read_error"
			report.Detail = err.Error()
			report.ErrorKind = "read_error"
			report.Error = err.Error()
			reports = append(reports, report)
			continue
		}
		report.Present = true
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			report.Status = "load_error"
			report.Reason = "parse_error"
			report.Detail = err.Error()
			report.ErrorKind = "parse_error"
			report.Error = err.Error()
			reports = append(reports, report)
			continue
		}
		report.Status = "loaded"
		report.Loaded = true
		report.Keys = sortedMapKeys(raw)
		report.KeyPaths = collectConfigKeyPaths(raw)
		report.KeyCount = len(report.KeyPaths)
		validation := configvalidate.ValidateBytes(data, path)
		report.ValidationStatus = validation.Status
		report.ErrorCount = validation.ErrorCount
		report.WarningCount = validation.WarningCount
		report.Errors = append([]configvalidate.Diagnostic(nil), validation.Errors...)
		report.Warnings = append([]configvalidate.Diagnostic(nil), validation.Warnings...)
		reports = append(reports, report)
		reportIndex := len(reports) - 1
		for _, key := range report.KeyPaths {
			lastKeyOwner[key] = reportIndex
		}
	}
	for index := range reports {
		if !reports[index].Loaded {
			continue
		}
		for _, key := range reports[index].KeyPaths {
			if lastKeyOwner[key] == index {
				reports[index].WinsForKeys = append(reports[index].WinsForKeys, key)
			} else {
				reports[index].ShadowedKeys = append(reports[index].ShadowedKeys, key)
			}
		}
	}
	return reports
}

func configFileInspectionSource(path string, pathCount int) string {
	if pathCount == 1 {
		return "explicit"
	}
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	switch {
	case clean == ".codog.local.json",
		strings.HasSuffix(clean, "/.codog.local.json"),
		configPathInDir(clean, ".claude", "settings.local.json"),
		configPathInDir(clean, ".claw", "settings.local.json"),
		configPathInDir(clean, ".omc", "settings.local.json"):
		return "local"
	case clean == ".codog.json",
		strings.HasSuffix(clean, "/.codog.json"),
		configPathInDir(clean, ".claude", "settings.json"),
		configPathInDir(clean, ".claw", "settings.json"),
		configPathInDir(clean, ".omc", "settings.json"),
		configPathInDir(clean, ".claw", "config.json"),
		configPathInDir(clean, ".omc", "config.json"):
		return "project"
	case filepath.IsAbs(path) && strings.HasSuffix(clean, "/config.json"):
		return "user"
	}
	return "explicit"
}

func configPathInDir(clean, dir, name string) bool {
	target := dir + "/" + name
	return clean == target || strings.HasSuffix(clean, "/"+target)
}

func collectConfigKeyPaths(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for _, key := range sortedMapKeys(values) {
		collectConfigKeyPathsForValue(key, values[key], &keys)
	}
	return keys
}

func collectConfigKeyPathsForValue(prefix string, raw json.RawMessage, keys *[]string) {
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err == nil && len(nested) > 0 {
		for _, key := range sortedMapKeys(nested) {
			collectConfigKeyPathsForValue(prefix+"."+key, nested[key], keys)
		}
		return
	}
	*keys = append(*keys, prefix)
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type configHelpReport struct {
	Kind              string   `json:"kind"`
	Action            string   `json:"action"`
	Status            string   `json:"status"`
	Section           string   `json:"section"`
	AvailableSections []string `json:"available_sections"`
	Message           string   `json:"message"`
	Hint              string   `json:"hint"`
}

func buildConfigHelpReport() configHelpReport {
	return configHelpReport{
		Kind:              "config",
		Action:            "show",
		Status:            "ok",
		Section:           "help",
		AvailableSections: availableConfigSections(),
		Message:           "Configuration sections available for `codog config get SECTION`.",
		Hint:              "Use `codog config inspect` to inspect effective settings, `codog config get SECTION` to inspect one section, or `codog config paths` to inspect config files.",
	}
}

func availableConfigSections() []string {
	sections := []string{"auth", "background", "compatibility", "editor_bridge", "enterprise", "hooks", "interface", "marketplace", "mcp", "model", "permissions", "preferences", "privacy", "sandbox", "skills", "updater"}
	sort.Strings(sections)
	return sections
}

type configValidationRequest struct {
	Paths  []string
	Target string
}

func buildConfigValidationReport(defaultPaths []string, args []string) (configvalidate.Report, error) {
	req, err := parseConfigValidationArgs(args)
	if err != nil {
		return configvalidate.Report{}, err
	}
	paths, err := configValidationPaths(defaultPaths, req)
	if err != nil {
		return configvalidate.Report{}, err
	}
	return configvalidate.ValidateFiles(paths), nil
}

func parseConfigValidationArgs(args []string) (configValidationRequest, error) {
	req := configValidationRequest{Target: "all"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("config validate target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("config validate path is required")
			}
			req.Paths = append(req.Paths, args[index])
		case strings.HasPrefix(arg, "--path="):
			req.Paths = append(req.Paths, strings.TrimPrefix(arg, "--path="))
		default:
			if strings.HasPrefix(arg, "-") {
				return req, fmt.Errorf("unknown config validate flag %q", arg)
			}
			req.Paths = append(req.Paths, arg)
		}
	}
	return req, nil
}

func configValidationPaths(defaultPaths []string, req configValidationRequest) ([]string, error) {
	if len(req.Paths) > 0 {
		out := make([]string, 0, len(req.Paths))
		for _, path := range req.Paths {
			path = strings.TrimSpace(path)
			if path == "" {
				return nil, errors.New("config validate path is required")
			}
			out = append(out, path)
		}
		return out, nil
	}
	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target == "" {
		target = "all"
	}
	switch target {
	case "all":
		return append([]string(nil), defaultPaths...), nil
	case "user":
		if len(defaultPaths) == 0 || strings.TrimSpace(defaultPaths[0]) == "" {
			return nil, errors.New("user config path is unavailable")
		}
		return []string{defaultPaths[0]}, nil
	case "project":
		return []string{".codog.json"}, nil
	case "local":
		return []string{".codog.local.json"}, nil
	default:
		return nil, fmt.Errorf("unknown config validate target %q", req.Target)
	}
}

func renderConfigValidationReport(out io.Writer, format string, report configvalidate.Report) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		if report.ErrorCount > 0 {
			return &ExitError{Code: 1, Err: fmt.Errorf("config validation failed with %d error(s)", report.ErrorCount), Silent: true}
		}
		return nil
	}
	fmt.Fprintln(out, "Config Validation")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Files            %d checked, %d present\n", report.FileCount, report.PresentCount)
	fmt.Fprintf(out, "  Diagnostics      %d error(s), %d warning(s)\n", report.ErrorCount, report.WarningCount)
	for _, result := range report.Results {
		fmt.Fprintf(out, "  Path             %s [%s]\n", result.Path, result.Status)
		if !result.Present {
			continue
		}
		diagnostics := configvalidate.FormatDiagnostics(result)
		if diagnostics == "" {
			continue
		}
		for _, line := range strings.Split(diagnostics, "\n") {
			fmt.Fprintf(out, "    %s\n", line)
		}
	}
	if report.ErrorCount > 0 {
		return &ExitError{Code: 1, Err: fmt.Errorf("config validation failed with %d error(s)", report.ErrorCount)}
	}
	return nil
}

type configInspectionRequest struct {
	Format string
	Args   []string
}

func parseConfigInspectionArgs(args []string) (configInspectionRequest, error) {
	req := configInspectionRequest{Format: "json"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("config output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		default:
			req.Args = append(req.Args, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "config")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	return req, nil
}

func renderConfigInspectionPayload(out io.Writer, format string, payload any) error {
	if format == "text" {
		renderConfigInspectionText(out, payload)
		return nil
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(out, string(data))
	return nil
}

func renderConfigInspectionText(out io.Writer, payload any) {
	fmt.Fprintln(out, "Config")
	switch value := payload.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(out, "  %-16s %s\n", key, configTextValue(value[key]))
		}
	case config.MutationReport:
		fmt.Fprintf(out, "  Status           %s\n", value.Status)
		fmt.Fprintf(out, "  Action           %s\n", value.Action)
		fmt.Fprintf(out, "  Path             %s\n", value.Path)
		fmt.Fprintf(out, "  Key              %s\n", value.Key)
	case resetReport:
		_ = renderResetReport(out, "text", value)
	default:
		data, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintf(out, "  %s\n", strings.ReplaceAll(string(data), "\n", "\n  "))
	}
}

func configTextValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []string:
		return strings.Join(typed, ", ")
	default:
		data, _ := json.Marshal(typed)
		return string(data)
	}
}

type configMutationRequest struct {
	Action string
	Key    string
	Value  any
	Path   string
	Target string
}

func mutateConfigFile(args []string, paths []string) (config.MutationReport, error) {
	req, err := parseConfigMutationArgs(args)
	if err != nil {
		return config.MutationReport{}, err
	}
	path, err := configMutationPath(req, paths)
	if err != nil {
		return config.MutationReport{}, err
	}
	switch req.Action {
	case "set":
		return config.SetFileValue(path, req.Key, req.Value)
	case "unset":
		return config.UnsetFileValue(path, req.Key)
	default:
		return config.MutationReport{}, fmt.Errorf("unknown config action %q", req.Action)
	}
}

func parseConfigMutationArgs(args []string) (configMutationRequest, error) {
	if len(args) == 0 {
		return configMutationRequest{}, errors.New("config action is required")
	}
	req := configMutationRequest{Action: strings.ToLower(args[0])}
	var positionals []string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--target":
			if i+1 >= len(args) {
				return req, errors.New("config target is required")
			}
			i++
			req.Target = args[i]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			if i+1 >= len(args) {
				return req, errors.New("config path is required")
			}
			i++
			req.Path = args[i]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			positionals = append(positionals, arg)
		}
	}
	switch req.Action {
	case "set":
		if len(positionals) < 2 {
			return req, errors.New("usage: codog config set KEY VALUE [--target user|project|local|--path PATH]")
		}
		req.Key = positionals[0]
		req.Value = config.ParseConfigValue(strings.Join(positionals[1:], " "))
	case "unset":
		if len(positionals) != 1 {
			return req, errors.New("usage: codog config unset KEY [--target user|project|local|--path PATH]")
		}
		req.Key = positionals[0]
	default:
		return req, fmt.Errorf("unknown config action %q", req.Action)
	}
	return req, nil
}

func configMutationPath(req configMutationRequest, paths []string) (string, error) {
	if req.Path != "" {
		return req.Path, nil
	}
	target := strings.ToLower(strings.TrimSpace(req.Target))
	switch target {
	case "", "user":
		if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
			return "", errors.New("user config path is unavailable")
		}
		return paths[0], nil
	case "project":
		return ".codog.json", nil
	case "local":
		return ".codog.local.json", nil
	default:
		return "", fmt.Errorf("unknown config target %q", req.Target)
	}
}

func resetConfigFileCommand(args []string, paths []string) (resetReport, error) {
	req, err := parseResetArgs(args[1:])
	if err != nil {
		return resetReport{}, err
	}
	path, err := configMutationPath(configMutationRequest{Path: req.Path, Target: req.Target}, paths)
	if err != nil {
		return resetReport{}, err
	}
	report, _, err := resetConfigAtPath(path, req.Section, req.Action, req.Confirm)
	return report, err
}

type resetRequest struct {
	Action  string
	Section string
	Format  string
	Target  string
	Path    string
	Confirm bool
}

type resetReport struct {
	Kind              string                  `json:"kind"`
	Action            string                  `json:"action"`
	Status            string                  `json:"status"`
	Section           string                  `json:"section"`
	Path              string                  `json:"path,omitempty"`
	ConfirmRequired   bool                    `json:"confirm_required,omitempty"`
	ResetKeys         []string                `json:"reset_keys,omitempty"`
	AvailableSections []string                `json:"available_sections,omitempty"`
	Changes           []config.MutationReport `json:"changes,omitempty"`
	Message           string                  `json:"message,omitempty"`
}

var resetSectionKeys = map[string][]string{
	"auth":          {"api_key", "auth_token", "oauth_profile", "base_url"},
	"background":    backgroundResetKeys,
	"compatibility": compatibilityResetKeys,
	"editor-bridge": editorBridgeResetKeys,
	"enterprise":    enterpriseResetKeys,
	"future":        {"future"},
	"hooks":         {"hooks"},
	"interface":     {"language", "theme", "editorMode"},
	"marketplace":   marketplaceResetKeys,
	"mcp":           {"mcp_servers"},
	"model":         {"model", "advisor_model", "subagentModel", "max_tokens", "max_turns", "temperature", "reasoning_effort", "fast_mode"},
	"permissions":   {"permission_mode", "permission_rules"},
	"preferences":   preferencesResetKeys,
	"privacy":       {"privacy_settings"},
	"rag":           {"rag_base_url", "rag_timeout_seconds", "rag_top_k_max"},
	"rate-limit":    {"rate_limit"},
	"remote":        remoteResetKeys,
	"sandbox":       sandboxResetKeys,
	"skills":        {"enabled_skills"},
	"updater":       updaterResetKeys,
	"voice":         {"voice_enabled", "voice_command", "speech_command"},
}

var resetSectionAliases = map[string]string{
	"all":               "all",
	"auth":              "auth",
	"authentication":    "auth",
	"background":        "background",
	"defaults":          "all",
	"bridge":            "editor-bridge",
	"compat":            "compatibility",
	"compatibility":     "compatibility",
	"editor_bridge":     "editor-bridge",
	"editor-bridge":     "editor-bridge",
	"everything":        "all",
	"ide":               "editor-bridge",
	"enterprise":        "enterprise",
	"enterprise-policy": "enterprise",
	"policy":            "enterprise",
	"future":            "future",
	"hooks":             "hooks",
	"interface":         "interface",
	"marketplace":       "marketplace",
	"marketplaces":      "marketplace",
	"mcp":               "mcp",
	"model":             "model",
	"models":            "model",
	"permission":        "permissions",
	"permissions":       "permissions",
	"pref":              "preferences",
	"preference":        "preferences",
	"preferences":       "preferences",
	"privacy":           "privacy",
	"privacy-settings":  "privacy",
	"rag":               "rag",
	"retrieve-context":  "rag",
	"retrieve_context":  "rag",
	"rate_limit":        "rate-limit",
	"rate-limit":        "rate-limit",
	"remote":            "remote",
	"sandbox":           "sandbox",
	"skills":            "skills",
	"ui":                "interface",
	"updater":           "updater",
	"upgrade":           "updater",
	"voice":             "voice",
}

func (a *App) Reset(args []string) error {
	req, err := parseResetArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	report, changed, err := resetConfigAtPath(path, req.Section, req.Action, req.Confirm)
	if err != nil {
		return err
	}
	if changed {
		a.applyConfigReset(report.Section)
	}
	return renderResetReport(a.Out, req.Format, report)
}

func parseResetArgs(args []string) (resetRequest, error) {
	req := resetRequest{Action: "status", Section: "all", Format: "text", Target: "user"}
	const usage = "codog reset [status|SECTION] [--confirm] [--target user|project|local] [--path PATH] [--output-format text|json]"
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "reset", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "reset", Flag: arg, Usage: usage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "reset", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--confirm" || arg == "--yes" || arg == "-y":
			req.Confirm = true
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "reset", Option: arg, Usage: usage}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("reset", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		if req.Confirm {
			req.Action = "reset"
		}
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show", "list":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "reset " + strings.ToLower(rest[0]), Args: rest[1:], Usage: usage}
		}
		req.Action = "status"
	default:
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "reset", Args: rest[1:], Usage: usage}
		}
		req.Action = "reset"
		req.Section = rest[0]
	}
	return req, nil
}

func resetConfigAtPath(path string, section string, action string, confirm bool) (resetReport, bool, error) {
	canonical, keys, err := resolveResetSection(section)
	if err != nil {
		return resetReport{}, false, err
	}
	report := resetReport{
		Kind:              "reset",
		Action:            action,
		Status:            "ok",
		Section:           canonical,
		Path:              path,
		ResetKeys:         append([]string(nil), keys...),
		AvailableSections: availableResetSections(),
	}
	if action == "status" {
		report.ConfirmRequired = canonical == "all"
		report.Message = "Choose a section to reset, or use `reset all --confirm` to remove the selected config file."
		return report, false, nil
	}
	if canonical == "all" {
		report.ResetKeys = []string{"*"}
		if !confirm {
			report.ConfirmRequired = true
			report.Action = "status"
			report.Message = "Whole-file reset requires --confirm."
			return report, false, nil
		}
		change, err := config.ResetFile(path)
		if err != nil {
			return resetReport{}, false, err
		}
		report.Changes = []config.MutationReport{change}
		report.Message = "Configuration file reset to defaults."
		return report, true, nil
	}
	for _, key := range keys {
		change, err := config.UnsetFileValue(path, key)
		if err != nil {
			return resetReport{}, false, err
		}
		report.Changes = append(report.Changes, change)
	}
	report.Message = fmt.Sprintf("Configuration section %q reset to defaults.", canonical)
	return report, true, nil
}

func resolveResetSection(section string) (string, []string, error) {
	normalized := strings.ToLower(strings.TrimSpace(section))
	if normalized == "" {
		normalized = "all"
	}
	canonical, ok := resetSectionAliases[normalized]
	if !ok {
		return "", nil, invalidFlagValueError{
			Flag:    "section",
			Value:   section,
			Message: "reset section is not recognized",
			Usage:   "codog reset [status|SECTION] [--confirm] [--target user|project|local] [--path PATH] [--output-format text|json]",
		}
	}
	if canonical == "all" {
		return canonical, nil, nil
	}
	keys := resetSectionKeys[canonical]
	return canonical, append([]string(nil), keys...), nil
}

func availableResetSections() []string {
	sections := make([]string, 0, len(resetSectionKeys)+1)
	sections = append(sections, "all")
	for section := range resetSectionKeys {
		sections = append(sections, section)
	}
	sort.Strings(sections)
	return sections
}

func renderResetReport(out io.Writer, format string, report resetReport) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Reset")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Section          %s\n", report.Section)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.ConfirmRequired {
		fmt.Fprintln(out, "  Confirm required true")
	}
	if len(report.ResetKeys) != 0 {
		fmt.Fprintf(out, "  Reset keys       %s\n", strings.Join(report.ResetKeys, ", "))
	}
	if len(report.AvailableSections) != 0 && report.Action == "status" {
		fmt.Fprintf(out, "  Sections         %s\n", strings.Join(report.AvailableSections, ", "))
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
	return nil
}

func (a *App) applyConfigReset(section string) {
	defaults := defaultResetConfig(a.Config.ConfigHome)
	switch section {
	case "all":
		a.Config = defaults
	case "auth":
		a.Config.APIKey = ""
		a.Config.AuthToken = ""
		a.Config.OAuthProfile = ""
		a.Config.BaseURL = defaults.BaseURL
	case "background":
		a.Config.Future.BackgroundStatePath = ""
	case "compatibility":
		a.Config.Future.SlackAppInstallCount = 0
		a.Config.Future.StickerOrderCount = 0
		a.Config.Future.ExtraUsageVisitCount = 0
		a.Config.Future.GuestPassReferralURL = ""
		a.Config.Future.GuestPassVisitCount = 0
	case "editor-bridge":
		a.Config.Future.EditorBridgeSocket = ""
		a.Config.Future.EditorBridgeToken = ""
	case "enterprise":
		a.Config.Future.EnterprisePolicy = ""
		a.Config.Future.EnterprisePolicyPublicKey = ""
	case "future":
		a.Config.Future = config.FutureConfig{}
	case "hooks":
		a.Config.Hooks = config.HookConfig{}
	case "interface":
		a.Config.Language = ""
		a.Config.Theme = ""
		a.Config.EditorMode = ""
	case "marketplace":
		a.Config.Future.PluginMarketplaces = nil
		a.Config.Future.PluginMarketplaceKeys = nil
	case "mcp":
		a.Config.MCPServers = map[string]config.MCPServerConfig{}
	case "model":
		a.Config.Model = defaults.Model
		a.Config.AdvisorModel = ""
		a.Config.MaxTokens = defaults.MaxTokens
		a.Config.MaxTurns = defaults.MaxTurns
		a.Config.Temperature = nil
		a.Config.ReasoningEffort = ""
		a.Config.FastMode = nil
	case "permissions":
		a.Config.PermissionMode = defaults.PermissionMode
		a.Config.PermissionRules = config.PermissionRules{}
	case "preferences":
		a.Config.Future.ChromeDefaultEnabled = nil
		a.Config.Future.NotificationsEnabled = nil
		a.Config.Future.UltraReviewEnabled = nil
	case "privacy":
		a.Config.Privacy = config.PrivacyConfig{}
	case "rag":
		a.Config.RAGBaseURL = ""
		a.Config.RAGTimeoutSeconds = 0
		a.Config.RAGTopKMax = 0
	case "rate-limit":
		a.Config.RateLimit = defaults.RateLimit
	case "remote":
		a.Config.Future.RemoteEnabled = false
		a.Config.Future.RemoteAuthToken = ""
		a.Config.Future.RemoteLeaseSeconds = 0
	case "sandbox":
		a.Config.Future.SandboxStrategy = ""
		a.Config.Future.Sandbox = config.SandboxConfig{}
	case "skills":
		a.Config.EnabledSkills = nil
	case "updater":
		a.Config.Future.UpdaterManifestURL = ""
	case "voice":
		a.Config.VoiceEnabled = nil
		a.Config.VoiceCommand = ""
		a.Config.SpeechCommand = ""
	}
}

func defaultResetConfig(configHome string) config.Config {
	return config.Config{
		BaseURL:             config.DefaultBaseURL,
		Model:               config.DefaultModel,
		MaxTokens:           4096,
		MaxTurns:            8,
		PermissionMode:      "workspace-write",
		AutoCompactMessages: 40,
		ConfigHome:          configHome,
		RateLimit:           config.DefaultRateLimitConfig(),
		MCPServers:          map[string]config.MCPServerConfig{},
	}
}

func configSectionPayload(cfg config.Config, args []string) (any, error) {
	if len(args) == 0 {
		return cfg, nil
	}
	switch strings.ToLower(args[0]) {
	case "model":
		return map[string]any{"model": cfg.Model, "advisor_model": cfg.AdvisorModel, "subagentModel": cfg.AdvisorModel, "max_tokens": cfg.MaxTokens, "max_turns": cfg.MaxTurns, "temperature": cfg.Temperature, "reasoning_effort": cfg.ReasoningEffort, "fast_mode": fastModeEnabled(cfg.FastMode)}, nil
	case "interface", "ui":
		return map[string]any{"language": cfg.Language, "theme": cfg.Theme, "editorMode": cfg.EditorMode}, nil
	case "privacy", "privacy-settings":
		return map[string]any{"privacy_settings": cfg.Privacy}, nil
	case "permissions", "permission":
		return map[string]any{"permission_mode": cfg.PermissionMode, "permission_rules": cfg.PermissionRules}, nil
	case "background":
		return map[string]any{
			"state_path":        cfg.Future.BackgroundStatePath,
			"state_configured":  strings.TrimSpace(cfg.Future.BackgroundStatePath) != "",
			"default_filename":  workerstate.FileName,
			"default_directory": ".codog",
		}, nil
	case "compatibility", "compat":
		return map[string]any{
			"slack_app_install_count": cfg.Future.SlackAppInstallCount,
			"sticker_order_count":     cfg.Future.StickerOrderCount,
			"extra_usage_visit_count": cfg.Future.ExtraUsageVisitCount,
			"guest_pass_referral_url": cfg.Future.GuestPassReferralURL,
			"guest_pass_visit_count":  cfg.Future.GuestPassVisitCount,
		}, nil
	case "preferences", "pref":
		return map[string]any{
			"chrome_default_enabled":   boolPtrEnabled(cfg.Future.ChromeDefaultEnabled),
			"chrome_configured":        cfg.Future.ChromeDefaultEnabled != nil,
			"notifications_enabled":    notificationsEnabled(cfg.Future.NotificationsEnabled),
			"notifications_configured": cfg.Future.NotificationsEnabled != nil,
			"ultrareview_enabled":      enabledByDefault(cfg.Future.UltraReviewEnabled),
			"ultrareview_configured":   cfg.Future.UltraReviewEnabled != nil,
		}, nil
	case "editor_bridge", "editor-bridge", "bridge", "ide":
		return map[string]any{
			"socket":           cfg.Future.EditorBridgeSocket,
			"token_configured": strings.TrimSpace(cfg.Future.EditorBridgeToken) != "",
		}, nil
	case "enterprise", "enterprise-policy", "policy":
		return map[string]any{
			"policy":                cfg.Future.EnterprisePolicy,
			"public_key_configured": strings.TrimSpace(cfg.Future.EnterprisePolicyPublicKey) != "",
		}, nil
	case "mcp":
		return cfg.MCPServers, nil
	case "marketplace", "marketplaces":
		return map[string]any{
			"sources":     append([]string(nil), cfg.Future.PluginMarketplaces...),
			"public_keys": redactStringMapValues(cfg.Future.PluginMarketplaceKeys),
		}, nil
	case "remote":
		return map[string]any{
			"enabled":               cfg.Future.RemoteEnabled,
			"auth_token_configured": strings.TrimSpace(cfg.Future.RemoteAuthToken) != "",
			"lease_seconds":         cfg.Future.RemoteLeaseSeconds,
		}, nil
	case "sandbox":
		return map[string]any{"strategy": cfg.Future.SandboxStrategy, "settings": cfg.Future.Sandbox}, nil
	case "updater", "upgrade":
		return map[string]any{
			"manifest_url":        cfg.Future.UpdaterManifestURL,
			"manifest_configured": strings.TrimSpace(cfg.Future.UpdaterManifestURL) != "",
		}, nil
	case "rag", "retrieve-context", "retrieve_context":
		return map[string]any{
			"rag_base_url":            cfg.RAGBaseURL,
			"rag_timeout_seconds":     cfg.RAGTimeoutSeconds,
			"rag_top_k_max":           cfg.RAGTopKMax,
			"env_fallback":            "RAG_BASE_URL",
			"tool":                    "retrieve_context",
			"enabled_when_configured": true,
		}, nil
	case "hooks":
		return cfg.Hooks, nil
	case "skills":
		return map[string]any{"enabled_skills": cfg.EnabledSkills}, nil
	case "auth":
		return map[string]any{"api_key": cfg.APIKey, "auth_token": cfg.AuthToken, "base_url": cfg.BaseURL}, nil
	default:
		return nil, fmt.Errorf("unknown config section %q", args[0])
	}
}

func redactedConfig(cfg config.Config) config.Config {
	cfg.APIKey = redact(cfg.APIKey)
	cfg.AuthToken = redact(cfg.AuthToken)
	cfg.Future.RemoteAuthToken = redact(cfg.Future.RemoteAuthToken)
	cfg.Future.EditorBridgeToken = redact(cfg.Future.EditorBridgeToken)
	return cfg
}

func redactStringMapValues(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = redact(value)
	}
	return out
}

func validPermissionMode(mode string) bool {
	_, ok := config.NormalizePermissionModeLabel(mode)
	return ok
}

var permissionModeCandidates = []string{"read-only", "workspace-write", "danger-full-access", "prompt", "allow"}

func unknownPermissionModeHint(mode string) string {
	suggestions := toolnames.Suggestions(mode, permissionModeCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog permissions set %s`? Use `codog permissions` to inspect available modes.", suggestions[0])
	case 0:
		return "Use one of: read-only, workspace-write, danger-full-access, prompt, allow."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog permissions` to inspect available modes.", strings.Join(suggestions, ", "))
	}
}

func (a *App) Git(args []string) error {
	var err error
	args, err = rewriteLeadingGitOutputFormat(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: codog git status [--json|--output-format text|json] | git diff [--staged] [PATH...] [--json|--output-format text|json] | git log [count] [--json|--output-format text|json] | git changelog [count] [--json|--output-format text|json] | git blame FILE [line] [--json|--output-format text|json] | git branch [ARGS...] | git tag [ARGS...] | git stash [list|push|apply|pop] | git commit [--all] MESSAGE [--json|--output-format text|json]")
	}
	switch args[0] {
	case "status":
		return a.GitStatus(args[1:])
	case "diff":
		return a.Diff(args[1:])
	case "log":
		return a.GitLog(args[1:])
	case "changelog":
		return a.Changelog(args[1:])
	case "blame":
		return a.GitBlame(args[1:])
	case "stash":
		return a.Stash(args[1:])
	case "commit":
		return a.GitCommit(args[1:], "json")
	case "branch":
		return a.Branch(args[1:])
	case "tag":
		return a.Tag(args[1:])
	default:
		return fmt.Errorf("unknown git command %q", args[0])
	}
}

func rewriteLeadingGitOutputFormat(args []string) ([]string, error) {
	format := ""
	rest := args
	for len(rest) > 0 {
		arg := rest[0]
		switch {
		case arg == "--json":
			format = "json"
			rest = rest[1:]
		case arg == "--output-format" || arg == "-o":
			if len(rest) < 2 {
				return nil, errors.New("git output format is required")
			}
			format = rest[1]
			rest = rest[2:]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
			rest = rest[1:]
		default:
			if format == "" {
				return args, nil
			}
			normalized, err := normalizeTextOrJSON(format, "git")
			if err != nil {
				return nil, err
			}
			out := append([]string(nil), rest...)
			if gitSubcommandAcceptsOutputFormat(out[0]) && !argsHaveOutputFormat(out[1:]) {
				out = append(out, "--output-format", normalized)
			}
			return out, nil
		}
	}
	return rest, nil
}

func gitSubcommandAcceptsOutputFormat(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "status", "diff", "log", "changelog", "blame", "branch", "tag", "stash", "commit":
		return true
	default:
		return false
	}
}

func normalizeTextOrJSON(format, command string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if err := validateTextOrJSON(normalized, command); err != nil {
		return "", err
	}
	return normalized, nil
}

type gitStatusRequest struct {
	Format string
}

type gitStatusEntry struct {
	Code         string `json:"code"`
	Index        string `json:"index"`
	Worktree     string `json:"worktree"`
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
}

type gitStatusReport struct {
	Kind       string           `json:"kind"`
	Action     string           `json:"action"`
	Status     string           `json:"status"`
	Clean      bool             `json:"clean"`
	BranchLine string           `json:"branch_line,omitempty"`
	Branch     string           `json:"branch,omitempty"`
	Entries    []gitStatusEntry `json:"entries"`
	Raw        string           `json:"raw"`
}

func (a *App) GitStatus(args []string) error {
	req, err := parseGitStatusArgs(args)
	if err != nil {
		return err
	}
	raw, err := gitops.Status(a.Workspace)
	if err != nil {
		return err
	}
	report := buildGitStatusReport(raw)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, raw)
	return nil
}

func parseGitStatusArgs(args []string) (gitStatusRequest, error) {
	req := gitStatusRequest{Format: "text"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("git status output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--short" || arg == "-s" || arg == "--branch" || arg == "-b" || arg == "-sb" || arg == "-bs" || arg == "--porcelain":
		case strings.HasPrefix(arg, "--porcelain="):
		default:
			return req, fmt.Errorf("unknown git status flag %q", arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "git status")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	return req, nil
}

func buildGitStatusReport(raw string) gitStatusReport {
	report := gitStatusReport{
		Kind:    "git_status",
		Action:  "show",
		Status:  "ok",
		Entries: []gitStatusEntry{},
		Raw:     raw,
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			report.BranchLine = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			report.Branch = parseGitStatusBranch(report.BranchLine)
			continue
		}
		if entry, ok := parseGitStatusEntry(line); ok {
			report.Entries = append(report.Entries, entry)
		}
	}
	report.Clean = len(report.Entries) == 0
	return report
}

func parseGitStatusBranch(line string) string {
	branch := strings.TrimSpace(line)
	if index := strings.Index(branch, "..."); index >= 0 {
		branch = branch[:index]
	}
	if index := strings.Index(branch, " ["); index >= 0 {
		branch = branch[:index]
	}
	return strings.TrimSpace(branch)
}

func parseGitStatusEntry(line string) (gitStatusEntry, bool) {
	if len(line) < 3 {
		return gitStatusEntry{}, false
	}
	code := line[:2]
	path := strings.TrimSpace(line[3:])
	if path == "" {
		return gitStatusEntry{}, false
	}
	entry := gitStatusEntry{
		Code:     code,
		Index:    strings.TrimSpace(string(code[0])),
		Worktree: strings.TrimSpace(string(code[1])),
		Path:     path,
	}
	if before, after, ok := strings.Cut(path, " -> "); ok {
		entry.OriginalPath = strings.TrimSpace(before)
		entry.Path = strings.TrimSpace(after)
	}
	return entry, true
}

type diffRequest struct {
	Format string
	Staged bool
	Paths  []string
}

type diffReport struct {
	Kind             string   `json:"kind"`
	Action           string   `json:"action"`
	Status           string   `json:"status"`
	Result           string   `json:"result,omitempty"`
	ErrorKind        string   `json:"error_kind,omitempty"`
	Message          string   `json:"message,omitempty"`
	Hint             string   `json:"hint,omitempty"`
	Staged           bool     `json:"staged"`
	Empty            bool     `json:"empty"`
	Bytes            int      `json:"bytes"`
	Paths            []string `json:"paths,omitempty"`
	ChangedFileCount int      `json:"changed_file_count"`
	ChangedFiles     []string `json:"changed_files,omitempty"`
	Diff             string   `json:"diff"`
}

func (a *App) Diff(args []string) error {
	req, err := parseDiffArgs(args)
	if err != nil {
		return err
	}
	report, err := a.buildDiffReport(req)
	if err != nil {
		if req.Format == "json" && gitops.IsNoGitRepoError(err) {
			report = buildDiffNoGitRepoReport(req)
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, report.Diff)
	return nil
}

func (a *App) buildDiffReport(req diffRequest) (diffReport, error) {
	options := gitops.DiffOptions{Staged: req.Staged, Paths: req.Paths}
	diff, err := gitops.DiffWithOptions(a.Workspace, options)
	if err != nil {
		return diffReport{}, err
	}
	changedFiles, err := gitops.DiffChangedFilesWithOptions(a.Workspace, options)
	if err != nil {
		return diffReport{}, err
	}
	return diffReport{
		Kind:             "diff",
		Action:           "show",
		Status:           "ok",
		Staged:           req.Staged,
		Empty:            diff == "",
		Bytes:            len(diff),
		Paths:            append([]string(nil), req.Paths...),
		ChangedFileCount: len(changedFiles),
		ChangedFiles:     changedFiles,
		Diff:             diff,
	}, nil
}

func buildDiffNoGitRepoReport(req diffRequest) diffReport {
	return diffReport{
		Kind:             "diff",
		Action:           "show",
		Status:           "error",
		Result:           "no_git_repo",
		ErrorKind:        "no_git_repo",
		Message:          "not inside a git repository",
		Hint:             "Run `git init` in this workspace or run `codog diff` from an existing git repository.",
		Staged:           req.Staged,
		Empty:            true,
		Paths:            append([]string(nil), req.Paths...),
		ChangedFileCount: 0,
		Diff:             "",
	}
}

func parseDiffArgs(args []string) (diffRequest, error) {
	req := diffRequest{Format: "text"}
	usage := "codog diff [--staged] [PATH...] [--output-format text|json]"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, missingFlagValueError{Command: "diff", Flag: arg, Usage: usage}
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--staged" || arg == "--cached":
			req.Staged = true
		case arg == "--":
			req.Paths = append(req.Paths, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "diff", Option: arg, Usage: usage}
		default:
			req.Paths = append(req.Paths, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "diff")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	return req, nil
}

type gitLogRequest struct {
	Format string
	Limit  int
}

type gitLogReport struct {
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Status  string            `json:"status"`
	Limit   int               `json:"limit"`
	Count   int               `json:"count"`
	Entries []gitops.LogEntry `json:"entries"`
	Raw     string            `json:"raw"`
}

func (a *App) GitLog(args []string) error {
	req, err := parseGitLogArgs(args)
	if err != nil {
		return err
	}
	raw, err := gitops.Log(a.Workspace, req.Limit)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		entries, err := gitops.LogEntries(a.Workspace, req.Limit)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(gitLogReport{
			Kind:    "git_log",
			Action:  "show",
			Status:  "ok",
			Limit:   req.Limit,
			Count:   len(entries),
			Entries: entries,
			Raw:     raw,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, raw)
	return nil
}

func parseGitLogArgs(args []string) (gitLogRequest, error) {
	req := gitLogRequest{Format: "text", Limit: 20}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("git log output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown git log flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "git log")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 {
		return req, nil
	}
	if len(positionals) > 1 {
		return req, errors.New("usage: codog git log [count] [--json|--output-format text|json]")
	}
	limit, err := strconv.Atoi(positionals[0])
	if err != nil || limit <= 0 {
		return req, errors.New("git log count must be a positive integer")
	}
	req.Limit = limit
	return req, nil
}

type branchRequest struct {
	Format     string
	Action     string
	Name       string
	NewName    string
	Base       string
	StartPoint string
	Switch     bool
	Force      bool
}

type branchReport struct {
	Kind      string                  `json:"kind"`
	Action    string                  `json:"action"`
	Status    string                  `json:"status"`
	Current   string                  `json:"current"`
	Branches  []gitops.BranchInfo     `json:"branches,omitempty"`
	Freshness *gitops.BranchFreshness `json:"freshness,omitempty"`
	Output    string                  `json:"output,omitempty"`
}

func (a *App) Branch(args []string) error {
	req, err := parseBranchArgs(args)
	if err != nil {
		return err
	}
	report := branchReport{Kind: "branch", Action: req.Action, Status: "ok"}
	switch req.Action {
	case "list":
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "current":
		current, err := gitops.Branch(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = current
	case "create":
		output, err := gitops.CreateBranch(a.Workspace, req.Name, req.StartPoint, req.Switch)
		if err != nil {
			return err
		}
		report.Output = output
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "switch":
		output, err := gitops.SwitchBranch(a.Workspace, req.Name)
		if err != nil {
			return err
		}
		report.Output = output
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "delete":
		output, err := gitops.DeleteBranch(a.Workspace, req.Name, req.Force)
		if err != nil {
			return err
		}
		report.Output = output
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "rename":
		output, err := gitops.RenameBranch(a.Workspace, req.Name, req.NewName)
		if err != nil {
			return err
		}
		report.Output = output
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "freshness":
		freshness, err := gitops.CheckBranchFreshness(a.Workspace, req.Name, req.Base)
		if err != nil {
			return err
		}
		report.Current = freshness.Branch
		report.Freshness = &freshness
	default:
		return fmt.Errorf("unknown branch action %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBranchReport(a.Out, report)
	return nil
}

func parseBranchArgs(args []string) (branchRequest, error) {
	const usage = "codog branch [list|current|create NAME [START]|switch NAME|delete NAME|rename [OLD] NEW|freshness [BRANCH] [BASE]] [--json|--output-format text|json]"
	parser := branchArgParser{
		req:   branchRequest{Format: "text", Action: "list"},
		usage: usage,
	}
	for i := 0; i < len(args); i++ {
		if err := parser.consume(args, &i); err != nil {
			return parser.req, err
		}
	}
	return parser.finish()
}

type branchArgParser struct {
	req         branchRequest
	positionals []string
	usage       string
}

func (p *branchArgParser) consume(args []string, index *int) error {
	arg := args[*index]
	switch {
	case arg == "--json":
		p.req.Format = "json"
	case arg == "--output-format" || arg == "-o":
		*index++
		if *index >= len(args) {
			return missingFlagValueError{Command: "branch", Flag: arg, Usage: p.usage}
		}
		p.req.Format = args[*index]
	case strings.HasPrefix(arg, "--output-format="):
		p.req.Format = strings.TrimPrefix(arg, "--output-format=")
	case arg == "--switch" || arg == "--checkout":
		p.req.Switch = true
	case arg == "--force" || arg == "-f":
		p.req.Force = true
	case arg == "--base":
		*index++
		if *index >= len(args) {
			return missingFlagValueError{Command: "branch", Flag: arg, Usage: p.usage}
		}
		p.req.Base = args[*index]
	case strings.HasPrefix(arg, "--base="):
		p.req.Base = strings.TrimPrefix(arg, "--base=")
	case strings.HasPrefix(arg, "-"):
		return unknownOptionError{Command: "branch", Option: arg, Usage: p.usage}
	default:
		p.positionals = append(p.positionals, arg)
	}
	return nil
}

func (p *branchArgParser) finish() (branchRequest, error) {
	format, err := normalizeOutputFormat("branch", p.req.Format, []string{"text", "json"})
	if err != nil {
		return p.req, err
	}
	p.req.Format = format
	if len(p.positionals) == 0 {
		return p.req, nil
	}
	return p.parseAction()
}

func (p *branchArgParser) parseAction() (branchRequest, error) {
	p.req.Action = strings.ToLower(p.positionals[0])
	rest := p.positionals[1:]
	switch p.req.Action {
	case "list", "show":
		p.req.Action = "list"
	case "current":
	case "create", "new":
		p.req.Action = "create"
		if len(rest) == 0 {
			return p.req, requiredArgumentError{Command: "branch create", Argument: "NAME", Usage: p.usage}
		}
		p.req.Name = rest[0]
		if len(rest) > 1 {
			p.req.StartPoint = rest[1]
		}
	case "switch", "checkout":
		p.req.Action = "switch"
		if len(rest) == 0 {
			return p.req, requiredArgumentError{Command: "branch switch", Argument: "NAME", Usage: p.usage}
		}
		p.req.Name = rest[0]
	case "delete", "del", "remove", "rm":
		p.req.Action = "delete"
		if len(rest) == 0 {
			return p.req, requiredArgumentError{Command: "branch delete", Argument: "NAME", Usage: p.usage}
		}
		p.req.Name = rest[0]
	case "rename", "mv":
		return p.parseRename(rest)
	case "freshness", "fresh", "stale":
		p.req.Action = "freshness"
		if len(rest) > 0 {
			p.req.Name = rest[0]
		}
		if len(rest) > 1 {
			p.req.Base = rest[1]
		}
	default:
		return p.req, unexpectedExtraArgsError{Command: "branch", Args: []string{p.positionals[0]}, Usage: p.usage}
	}
	return p.req, nil
}

func (p *branchArgParser) parseRename(rest []string) (branchRequest, error) {
	p.req.Action = "rename"
	switch len(rest) {
	case 0:
		return p.req, requiredArgumentError{Command: "branch rename", Argument: "NEW", Usage: p.usage}
	case 1:
		p.req.NewName = rest[0]
	default:
		p.req.Name = rest[0]
		p.req.NewName = rest[1]
	}
	return p.req, nil
}

func renderBranchReport(out io.Writer, report branchReport) {
	fmt.Fprintln(out, "Branches")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Current          %s\n", report.Current)
	if strings.TrimSpace(report.Output) != "" {
		fmt.Fprintf(out, "  Output           %s\n", strings.ReplaceAll(strings.TrimSpace(report.Output), "\n", "\n                   "))
	}
	if report.Freshness != nil {
		freshness := report.Freshness
		fmt.Fprintf(out, "  Base             %s\n", freshness.Base)
		fmt.Fprintf(out, "  Freshness        %s\n", freshness.Status)
		fmt.Fprintf(out, "  Ahead            %d\n", freshness.Ahead)
		fmt.Fprintf(out, "  Behind           %d\n", freshness.Behind)
		if freshness.VerificationBlocked {
			fmt.Fprintln(out, "  Verification     blocked until branch is updated")
		}
		if freshness.RecoveryScenario != "" {
			fmt.Fprintf(out, "  Recovery         %s\n", freshness.RecoveryScenario)
		}
		if freshness.Event != nil {
			fmt.Fprintf(out, "  Event            %s\n", freshness.Event.LaneEvent)
		}
		if len(freshness.SuggestedCommands) > 0 {
			fmt.Fprintln(out, "  Suggested commands")
			for _, command := range freshness.SuggestedCommands {
				fmt.Fprintf(out, "    - %s\n", command)
			}
		}
		if len(freshness.MissingFixes) > 0 {
			fmt.Fprintln(out, "  Missing commits")
			for _, subject := range freshness.MissingFixes {
				fmt.Fprintf(out, "    - %s\n", subject)
			}
		}
	}
	if len(report.Branches) == 0 {
		return
	}
	fmt.Fprintf(out, "  Count            %d\n", len(report.Branches))
	fmt.Fprintln(out)
	for _, branch := range report.Branches {
		marker := " "
		if branch.Current {
			marker = "*"
		}
		detail := branch.Commit
		if branch.Subject != "" {
			detail = strings.TrimSpace(detail + " " + branch.Subject)
		}
		if branch.Upstream != "" {
			detail = strings.TrimSpace(detail + " upstream=" + branch.Upstream)
		}
		fmt.Fprintf(out, "  %s %s", marker, branch.Name)
		if detail != "" {
			fmt.Fprintf(out, "  %s", detail)
		}
		fmt.Fprintln(out)
	}
}

type branchLockRequest struct {
	Format string
	Action string
	File   string
	Input  string
	Stdin  bool
}
