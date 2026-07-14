package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/anttrace"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bookmarks"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mocklimits"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/promptrefs"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/slash"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/workerstate"
	"github.com/Rememorio/codog/internal/worktree"
)

func parseCompactKeep(raw string, flag string) (int, error) {
	value := strings.TrimSpace(raw)
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, invalidFlagValueError{
			Flag:    flag,
			Value:   raw,
			Message: "compact keep count must be a positive integer",
			Usage:   compactUsage,
		}
	}
	return parsed, nil
}

type rateLimitOptionsReport struct {
	Kind              string `json:"kind"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	MaxRetries        int    `json:"max_retries"`
	InitialBackoffMS  int    `json:"initial_backoff_ms"`
	MaxBackoffMS      int    `json:"max_backoff_ms"`
	RetryableStatuses []int  `json:"retryable_statuses"`
}

type rateLimitRequest struct {
	Action           string
	Format           string
	Target           string
	Path             string
	MaxRetries       *int
	InitialBackoffMS *int
	MaxBackoffMS     *int
}

type resetLimitsRequest struct {
	Format string
	Target string
	Path   string
}

type antTraceRequest struct {
	Format    string
	Message   string
	Model     string
	BaseURL   string
	Provider  string
	TimeoutMS int
	NoRequest bool
	Write     bool
	Output    string
}

type mockLimitsRequest struct {
	Action       string
	Format       string
	Addr         string
	Failures     int
	RetryAfterMS int
	Text         string
}

type rateLimitReport struct {
	Kind              string                  `json:"kind"`
	Action            string                  `json:"action"`
	Status            string                  `json:"status"`
	Path              string                  `json:"path,omitempty"`
	Target            string                  `json:"target,omitempty"`
	MaxRetries        int                     `json:"max_retries"`
	InitialBackoffMS  int                     `json:"initial_backoff_ms"`
	MaxBackoffMS      int                     `json:"max_backoff_ms"`
	RetryableStatuses []int                   `json:"retryable_statuses"`
	Previous          *rateLimitOptionsReport `json:"previous,omitempty"`
}

type resetLimitsReport struct {
	Kind     string                 `json:"kind"`
	Action   string                 `json:"action"`
	Status   string                 `json:"status"`
	Path     string                 `json:"path"`
	Target   string                 `json:"target,omitempty"`
	Previous rateLimitOptionsReport `json:"previous"`
	Current  rateLimitOptionsReport `json:"current"`
}

func (a *App) RateLimit(args []string) error {
	req, err := parseRateLimitArgs(args)
	if err != nil {
		return err
	}
	var previous *rateLimitOptionsReport
	path := ""
	switch req.Action {
	case "show":
	case "set":
		if !req.hasSetValues() {
			return requiredArgumentError{
				Command:  "rate-limit set",
				Argument: "VALUE",
				Usage:    rateLimitSetUsage,
			}
		}
		path, err = a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		snapshot := buildRateLimitOptionsReport(a.Config.RateLimit)
		previous = &snapshot
		if req.MaxRetries != nil {
			if _, err := config.SetFileValue(path, "rate_limit.max_retries", *req.MaxRetries); err != nil {
				return err
			}
			a.Config.RateLimit.MaxRetries = *req.MaxRetries
		}
		if req.InitialBackoffMS != nil {
			if _, err := config.SetFileValue(path, "rate_limit.initial_backoff_ms", *req.InitialBackoffMS); err != nil {
				return err
			}
			a.Config.RateLimit.InitialBackoffMS = *req.InitialBackoffMS
		}
		if req.MaxBackoffMS != nil {
			if _, err := config.SetFileValue(path, "rate_limit.max_backoff_ms", *req.MaxBackoffMS); err != nil {
				return err
			}
			a.Config.RateLimit.MaxBackoffMS = *req.MaxBackoffMS
		}
	case "reset":
		path, err = a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		snapshot := buildRateLimitOptionsReport(a.Config.RateLimit)
		previous = &snapshot
		if _, err := config.UnsetFileValue(path, "rate_limit"); err != nil {
			return err
		}
		a.Config.RateLimit = config.RateLimitConfig{}
	default:
		return fmt.Errorf("unknown rate-limit action %q", req.Action)
	}
	report := buildRateLimitReport(req.Action, req.Target, path, previous, a.Config.RateLimit)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderRateLimitReport(a.Out, report)
	return nil
}

func (req rateLimitRequest) hasSetValues() bool {
	return req.MaxRetries != nil || req.InitialBackoffMS != nil || req.MaxBackoffMS != nil
}

func (a *App) RateLimitOptions(args []string) error {
	format, err := parseSimpleOutputFormat("rate-limit-options", args)
	if err != nil {
		return err
	}
	report := buildRateLimitOptionsReport(a.Config.RateLimit)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderRateLimitOptionsReport(a.Out, report)
	return nil
}

func (a *App) AntTrace(ctx context.Context, args []string) error {
	req, err := parseAntTraceArgs(args)
	if err != nil {
		return err
	}
	model := firstNonEmpty(req.Model, a.Config.Model)
	baseURL := firstNonEmpty(req.BaseURL, a.Config.BaseURL)
	client := a.Client
	if client == nil || strings.TrimSpace(req.BaseURL) != "" {
		clientConfig := a.Config
		clientConfig.BaseURL = baseURL
		client = anthropicClientFromConfig(clientConfig)
	}
	if baseURL == "" && client != nil {
		baseURL = client.BaseURL
	}
	rateLimit := anthropicRateLimitOptionsFromConfig(a.Config).Report()
	if client != nil {
		rateLimit = client.RateLimit.Report()
	}
	report := anttrace.Run(ctx, anttrace.Options{
		Provider:        req.Provider,
		Model:           model,
		BaseURL:         baseURL,
		AuthConfigured:  antTraceAuthConfigured(a.Config, client),
		RateLimit:       rateLimit,
		Message:         req.Message,
		ReasoningEffort: a.Config.ReasoningEffort,
		Timeout:         time.Duration(req.TimeoutMS) * time.Millisecond,
		NoRequest:       req.NoRequest,
		Client:          client,
	})
	if req.Write || strings.TrimSpace(req.Output) != "" {
		path := a.antTraceOutputPath(req.Output, time.Now().UTC())
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := session.ValidateExportOutputPath(path); err != nil {
			return err
		}
		data := []byte(anttrace.RenderMarkdown(report))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		report.File = path
		report.Bytes = len(data)
	}
	if req.Format == "json" {
		return anttrace.RenderJSON(a.Out, report)
	}
	anttrace.RenderText(a.Out, report)
	return nil
}

func antTraceAuthConfigured(cfg config.Config, client *anthropic.Client) bool {
	if strings.TrimSpace(cfg.APIKey) != "" || strings.TrimSpace(cfg.AuthToken) != "" {
		return true
	}
	if client == nil {
		return false
	}
	return strings.TrimSpace(client.APIKey) != "" || strings.TrimSpace(client.AuthToken) != ""
}

const antTraceUsage = "codog ant-trace [--no-request] [--message TEXT] [--timeout-ms N] [--write|--output PATH] [--output-format text|json]"

func parseAntTraceArgs(args []string) (antTraceRequest, error) {
	req := antTraceRequest{Format: "text", TimeoutMS: 15000}
	messageParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "ant-trace",
					Flag:    arg,
					Usage:   antTraceUsage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--no-request" || arg == "--skip-request":
			req.NoRequest = true
		case arg == "--write":
			req.Write = true
		case arg == "--message":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "ant-trace",
					Flag:    arg,
					Usage:   antTraceUsage,
				}
			}
			req.Message = args[index]
		case strings.HasPrefix(arg, "--message="):
			req.Message = strings.TrimPrefix(arg, "--message=")
		case arg == "--timeout-ms":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "ant-trace",
					Flag:    arg,
					Usage:   antTraceUsage,
				}
			}
			value, err := parseAntTraceTimeoutMS(args[index])
			if err != nil {
				return req, err
			}
			req.TimeoutMS = value
		case strings.HasPrefix(arg, "--timeout-ms="):
			value, err := parseAntTraceTimeoutMS(strings.TrimPrefix(arg, "--timeout-ms="))
			if err != nil {
				return req, err
			}
			req.TimeoutMS = value
		case arg == "--model":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "ant-trace",
					Flag:    arg,
					Usage:   antTraceUsage,
				}
			}
			req.Model = args[index]
		case strings.HasPrefix(arg, "--model="):
			req.Model = strings.TrimPrefix(arg, "--model=")
		case arg == "--base-url":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "ant-trace",
					Flag:    arg,
					Usage:   antTraceUsage,
				}
			}
			req.BaseURL = args[index]
		case strings.HasPrefix(arg, "--base-url="):
			req.BaseURL = strings.TrimPrefix(arg, "--base-url=")
		case arg == "--provider":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "ant-trace",
					Flag:    arg,
					Usage:   antTraceUsage,
				}
			}
			req.Provider = args[index]
		case strings.HasPrefix(arg, "--provider="):
			req.Provider = strings.TrimPrefix(arg, "--provider=")
		case arg == "--output":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "ant-trace",
					Flag:    arg,
					Usage:   antTraceUsage,
				}
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{
				Command: "ant-trace",
				Option:  arg,
				Usage:   antTraceUsage,
			}
		default:
			messageParts = append(messageParts, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("ant-trace", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if strings.TrimSpace(req.Message) == "" && len(messageParts) > 0 {
		req.Message = strings.Join(messageParts, " ")
	}
	return req, nil
}

func parseAntTraceTimeoutMS(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, invalidFlagValueError{
			Flag:    "--timeout-ms",
			Value:   raw,
			Message: "ant-trace timeout must be a positive integer",
			Usage:   antTraceUsage,
		}
	}
	if parsed <= 0 {
		return 0, invalidFlagValueError{
			Flag:    "--timeout-ms",
			Value:   raw,
			Message: "ant-trace timeout must be positive",
			Usage:   antTraceUsage,
		}
	}
	return parsed, nil
}

func (a *App) antTraceOutputPath(output string, createdAt time.Time) string {
	filename := fmt.Sprintf("ant-trace-%s-%d.md", createdAt.Format("20060102T150405Z"), createdAt.UnixNano())
	if strings.TrimSpace(output) == "" {
		return filepath.Join(a.Workspace, ".codog", "traces", filename)
	}
	path := a.resolveOutputPath(output)
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path
	}
	return filepath.Join(path, filename)
}

func (a *App) MockLimits(args []string) error {
	req, err := parseMockLimitsArgs(args)
	if err != nil {
		return err
	}
	options := mocklimits.Options{
		Addr:         req.Addr,
		Failures:     req.Failures,
		RetryAfterMS: req.RetryAfterMS,
		Text:         req.Text,
	}
	report := mocklimits.BuildReport(req.Action, options)
	if req.Action == "serve" {
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
		} else {
			mocklimits.RenderText(a.Out, report)
		}
		return http.ListenAndServe(report.Addr, mocklimits.Handler(options))
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	mocklimits.RenderText(a.Out, report)
	return nil
}

func (a *App) MockParity(ctx context.Context, args []string) error {
	return runMockParityCommand(ctx, a.Out, args, "", "text")
}

type mockParityRequest struct {
	Action     string
	Format     string
	ReportPath string
}

func runMockParityCommand(ctx context.Context, out io.Writer, args []string, fallbackFormat string, defaultFormat string) error {
	req, err := parseMockParityArgs(args, fallbackFormat, defaultFormat)
	if err != nil {
		return err
	}
	if req.Action == "manifest" {
		manifest := harness.ScenarioManifest()
		if req.ReportPath != "" {
			if err := writeMockParityJSON(req.ReportPath, manifest); err != nil {
				return err
			}
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(manifest, "", "  ")
			fmt.Fprintln(out, string(data))
		} else {
			renderMockParityManifestText(out, manifest)
		}
		return nil
	}
	report, err := harness.Run(ctx)
	if err != nil {
		return err
	}
	if err := harness.ValidateReport(report); err != nil {
		return fmt.Errorf("mock parity report validation failed: %w", err)
	}
	if req.ReportPath != "" {
		if err := writeMockParityJSON(req.ReportPath, report); err != nil {
			return err
		}
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
	} else {
		renderMockParityText(out, report)
	}
	if !report.OK {
		return fmt.Errorf("mock parity harness failed: %d/%d scenarios passed", report.Passed, report.Total)
	}
	return nil
}

func parseMockParityArgs(args []string, fallbackFormat string, defaultFormat string) (mockParityRequest, error) {
	const usage = "codog mock-parity [run|check|manifest] [--report PATH] [--output-format text|json]"
	format := strings.TrimSpace(fallbackFormat)
	if format == "" {
		format = strings.TrimSpace(defaultFormat)
	}
	if format == "" {
		format = "text"
	}
	req := mockParityRequest{Action: "run", Format: format, ReportPath: strings.TrimSpace(os.Getenv("MOCK_PARITY_REPORT_PATH"))}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return mockParityRequest{}, missingFlagValueError{
					Command: "mock-parity",
					Flag:    arg,
					Usage:   usage,
				}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--report" || arg == "--report-path":
			index++
			if index >= len(args) {
				return mockParityRequest{}, missingFlagValueError{
					Command: "mock-parity",
					Flag:    arg,
					Usage:   usage,
				}
			}
			req.ReportPath = args[index]
		case strings.HasPrefix(arg, "--report="):
			req.ReportPath = strings.TrimPrefix(arg, "--report=")
		case strings.HasPrefix(arg, "--report-path="):
			req.ReportPath = strings.TrimPrefix(arg, "--report-path=")
		case strings.EqualFold(arg, "run") || strings.EqualFold(arg, "check"):
			req.Action = "run"
		case strings.EqualFold(arg, "manifest") || strings.EqualFold(arg, "list"):
			req.Action = "manifest"
		default:
			return mockParityRequest{}, unexpectedExtraArgsError{
				Command: "mock-parity",
				Args:    []string{arg},
				Usage:   usage,
			}
		}
	}
	normalized, err := normalizeOutputFormat("mock-parity", format, []string{"text", "json"})
	if err != nil {
		return mockParityRequest{}, err
	}
	req.Format = normalized
	req.ReportPath = strings.TrimSpace(req.ReportPath)
	return req, nil
}

func writeMockParityJSON(path string, payload any) error {
	cleanPath := filepath.Clean(path)
	dir := filepath.Dir(cleanPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("write mock parity report: %w", err)
		}
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("write mock parity report: %w", err)
	}
	if err := os.WriteFile(cleanPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write mock parity report: %w", err)
	}
	return nil
}

func renderMockParityManifestText(out io.Writer, manifest harness.Manifest) {
	fmt.Fprintln(out, "Mock Parity Manifest")
	if strings.TrimSpace(manifest.SchemaVersion) != "" {
		fmt.Fprintf(out, "  Schema        %s\n", manifest.SchemaVersion)
	}
	fmt.Fprintf(out, "  Scenarios     %d\n", manifest.ScenarioCount)
	fmt.Fprintf(out, "  Categories    %d\n", len(manifest.Categories))
	if len(manifest.CapabilityCoverage) > 0 {
		mapped, total := mockParityCapabilityCounts(manifest.CapabilityCoverage, "mapped")
		fmt.Fprintf(out, "  Capabilities  %d/%d mapped\n", mapped, total)
	}
	if len(manifest.Scenarios) == 0 {
		return
	}
	fmt.Fprintln(out, "  Cases")
	for _, scenario := range manifest.Scenarios {
		label := scenario.Name
		if strings.TrimSpace(scenario.Category) != "" {
			label += " [" + scenario.Category + "]"
		}
		if strings.TrimSpace(scenario.Description) != "" {
			label += " - " + scenario.Description
		}
		fmt.Fprintf(out, "    - %s\n", label)
	}
}

func renderMockParityText(out io.Writer, report harness.Report) {
	status := "ok"
	if !report.OK {
		status = "error"
	}
	fmt.Fprintln(out, "Mock Parity Harness")
	if strings.TrimSpace(report.SchemaVersion) != "" {
		fmt.Fprintf(out, "  Schema        %s\n", report.SchemaVersion)
	}
	fmt.Fprintf(out, "  Status        %s\n", status)
	fmt.Fprintf(out, "  Scenarios     %d/%d passed\n", report.Passed, report.Total)
	if len(report.Coverage) > 0 {
		fmt.Fprintf(out, "  Coverage      %d categories\n", len(report.Coverage))
	}
	if len(report.CapabilityCoverage) > 0 {
		passing, total := mockParityCapabilityCounts(report.CapabilityCoverage, "passing")
		fmt.Fprintf(out, "  Capabilities  %d/%d passing\n", passing, total)
	}
	fmt.Fprintf(out, "  Requests      %d\n", report.RequestCount)
	fmt.Fprintf(out, "  Tool calls    %d\n", report.ToolCalls)
	fmt.Fprintf(out, "  Messages      %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Tokens        %d\n", report.UsageSummary.TotalTokens)
	fmt.Fprintf(out, "  Cost          %.6f\n", report.EstimatedCost)
	if len(report.Scenarios) == 0 {
		return
	}
	fmt.Fprintln(out, "  Cases")
	for _, scenario := range report.Scenarios {
		caseStatus := "ok"
		if !scenario.OK {
			caseStatus = "error"
		}
		label := scenario.Name
		if strings.TrimSpace(scenario.Category) != "" {
			label += " [" + scenario.Category + "]"
		}
		if strings.TrimSpace(scenario.Description) != "" {
			label += " - " + scenario.Description
		}
		if scenario.Error != "" {
			fmt.Fprintf(out, "    - %s: %s (%s)\n", label, caseStatus, scenario.Error)
		} else {
			fmt.Fprintf(out, "    - %s: %s\n", label, caseStatus)
		}
	}
}

func mockParityCapabilityCounts(coverage []harness.CapabilityCoverage, passingStatus string) (int, int) {
	count := 0
	for _, item := range coverage {
		if strings.EqualFold(item.Status, passingStatus) {
			count++
		}
	}
	return count, len(coverage)
}

func parseMockLimitsArgs(args []string) (mockLimitsRequest, error) {
	const usage = "codog mock-limits [serve|ADDR] [--failures N] [--retry-after-ms N] [--addr ADDR] [--output-format text|json]"
	req := mockLimitsRequest{Action: "show", Format: "text", Addr: ":8089", Failures: 2, RetryAfterMS: 1000, Text: "mock response after rate limits"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "mock-limits",
					Flag:    arg,
					Usage:   usage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--addr":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "mock-limits",
					Flag:    arg,
					Usage:   usage,
				}
			}
			req.Addr = args[index]
		case strings.HasPrefix(arg, "--addr="):
			req.Addr = strings.TrimPrefix(arg, "--addr=")
		case arg == "--failures":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "mock-limits",
					Flag:    arg,
					Usage:   usage,
				}
			}
			value, err := strconv.Atoi(args[index])
			if err != nil {
				return req, invalidFlagValueError{
					Flag:    arg,
					Value:   args[index],
					Message: "mock-limits failures must be an integer",
					Usage:   usage,
				}
			}
			req.Failures = value
		case strings.HasPrefix(arg, "--failures="):
			raw := strings.TrimPrefix(arg, "--failures=")
			value, err := strconv.Atoi(raw)
			if err != nil {
				return req, invalidFlagValueError{
					Flag:    "--failures",
					Value:   raw,
					Message: "mock-limits failures must be an integer",
					Usage:   usage,
				}
			}
			req.Failures = value
		case arg == "--retry-after-ms":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "mock-limits",
					Flag:    arg,
					Usage:   usage,
				}
			}
			value, err := strconv.Atoi(args[index])
			if err != nil {
				return req, invalidFlagValueError{
					Flag:    arg,
					Value:   args[index],
					Message: "mock-limits retry-after must be an integer",
					Usage:   usage,
				}
			}
			req.RetryAfterMS = value
		case strings.HasPrefix(arg, "--retry-after-ms="):
			raw := strings.TrimPrefix(arg, "--retry-after-ms=")
			value, err := strconv.Atoi(raw)
			if err != nil {
				return req, invalidFlagValueError{
					Flag:    "--retry-after-ms",
					Value:   raw,
					Message: "mock-limits retry-after must be an integer",
					Usage:   usage,
				}
			}
			req.RetryAfterMS = value
		case arg == "--text":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "mock-limits",
					Flag:    arg,
					Usage:   usage,
				}
			}
			req.Text = args[index]
		case strings.HasPrefix(arg, "--text="):
			req.Text = strings.TrimPrefix(arg, "--text=")
		default:
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("mock-limits", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	for _, arg := range rest {
		normalized := strings.ToLower(strings.TrimSpace(arg))
		switch {
		case normalized == "show" || normalized == "status" || normalized == "plan":
			req.Action = "show"
		case normalized == "serve" || normalized == "server" || normalized == "start":
			req.Action = "serve"
		case strings.HasPrefix(arg, ":") || strings.Contains(arg, ":"):
			req.Action = "serve"
			req.Addr = arg
		default:
			return req, unexpectedExtraArgsError{
				Command: "mock-limits",
				Args:    []string{arg},
				Usage:   usage,
			}
		}
	}
	if req.Failures < 0 {
		return req, invalidFlagValueError{
			Flag:    "--failures",
			Value:   strconv.Itoa(req.Failures),
			Message: "mock-limits failures must be non-negative",
			Usage:   usage,
		}
	}
	if req.RetryAfterMS < 0 {
		return req, invalidFlagValueError{
			Flag:    "--retry-after-ms",
			Value:   strconv.Itoa(req.RetryAfterMS),
			Message: "mock-limits retry-after must be non-negative",
			Usage:   usage,
		}
	}
	return req, nil
}

func isOutputFormatFlag(arg string) bool {
	return arg == "--json" || arg == "--output-format" || arg == "-o" || strings.HasPrefix(arg, "--output-format=")
}

const (
	rateLimitUsage      = "codog rate-limit [status|set|reset] [--max-retries N] [--initial-backoff-ms N] [--max-backoff-ms N] [--target user|project|local] [--output-format text|json]"
	resetLimitsUsage    = "codog reset-limits [--target user|project|local] [--path PATH] [--output-format text|json]"
	rateLimitSetUsage   = "codog rate-limit set [max-retries N] [initial-backoff-ms N] [max-backoff-ms N]"
	rateLimitFieldUsage = "codog rate-limit set max-retries N | initial-backoff-ms N | max-backoff-ms N"
)

func parseRateLimitArgs(args []string) (rateLimitRequest, error) {
	parser := rateLimitArgParser{
		req: rateLimitRequest{Action: "show", Format: "text", Target: "user"},
	}
	return parser.parse(args)
}

type rateLimitArgParser struct {
	req  rateLimitRequest
	rest []string
}

func (p *rateLimitArgParser) parse(args []string) (rateLimitRequest, error) {
	for index := 0; index < len(args); index++ {
		handled, err := p.consumeOption(args, &index)
		if err != nil {
			return p.req, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(args[index], "-") {
			return p.req, unknownOptionError{Command: "rate-limit", Option: args[index], Usage: rateLimitUsage}
		}
		p.rest = append(p.rest, args[index])
	}
	return p.finish()
}

func (p *rateLimitArgParser) consumeOption(args []string, index *int) (bool, error) {
	arg := args[*index]
	if arg == "--json" {
		p.req.Format = "json"
		return true, nil
	}
	if name, value, inline := strings.Cut(arg, "="); inline {
		return p.consumeInlineOption(name, value)
	}
	switch arg {
	case "--output-format", "-o":
		return true, p.consumeString(args, index, arg, &p.req.Format, false)
	case "--target":
		return true, p.consumeString(args, index, arg, &p.req.Target, true)
	case "--path":
		return true, p.consumeString(args, index, arg, &p.req.Path, true)
	case "--max-retries", "--retries":
		return true, p.consumeNumber(args, index, arg, rateLimitMaxRetries)
	case "--initial-backoff-ms", "--initial-backoff":
		return true, p.consumeNumber(args, index, arg, rateLimitInitialBackoff)
	case "--max-backoff-ms", "--max-backoff":
		return true, p.consumeNumber(args, index, arg, rateLimitMaxBackoff)
	default:
		return false, nil
	}
}

func (p *rateLimitArgParser) consumeInlineOption(name, value string) (bool, error) {
	switch name {
	case "--output-format":
		p.req.Format = value
	case "--target":
		p.req.Target = value
	case "--path":
		p.req.Path = value
	case "--max-retries", "--retries":
		return true, p.setNumber(name, value, rateLimitMaxRetries)
	case "--initial-backoff-ms", "--initial-backoff":
		return true, p.setNumber(name, value, rateLimitInitialBackoff)
	case "--max-backoff-ms", "--max-backoff":
		return true, p.setNumber(name, value, rateLimitMaxBackoff)
	default:
		return false, nil
	}
	return true, nil
}

func (p *rateLimitArgParser) consumeString(args []string, index *int, flag string, target *string, rejectFormat bool) error {
	value, err := rateLimitOptionValue(args, index, flag, rejectFormat)
	if err != nil {
		return err
	}
	*target = value
	return nil
}

func rateLimitOptionValue(args []string, index *int, flag string, rejectFormat bool) (string, error) {
	(*index)++
	if *index >= len(args) || rejectFormat && isOutputFormatFlag(args[*index]) {
		return "", missingFlagValueError{Command: "rate-limit", Flag: flag, Usage: rateLimitUsage}
	}
	return args[*index], nil
}

type rateLimitNumberField int

const (
	rateLimitMaxRetries rateLimitNumberField = iota
	rateLimitInitialBackoff
	rateLimitMaxBackoff
)

func (p *rateLimitArgParser) consumeNumber(args []string, index *int, flag string, field rateLimitNumberField) error {
	value, err := rateLimitOptionValue(args, index, flag, true)
	if err != nil {
		return err
	}
	return p.setNumber(flag, value, field)
}

func (p *rateLimitArgParser) setNumber(flag, raw string, field rateLimitNumberField) error {
	description := rateLimitNumberDescription(field)
	value, err := parseRateLimitPositiveInt(raw, flag, description)
	if err != nil {
		return err
	}
	switch field {
	case rateLimitMaxRetries:
		p.req.MaxRetries = &value
	case rateLimitInitialBackoff:
		p.req.InitialBackoffMS = &value
	case rateLimitMaxBackoff:
		p.req.MaxBackoffMS = &value
	}
	return nil
}

func rateLimitNumberDescription(field rateLimitNumberField) string {
	switch field {
	case rateLimitMaxRetries:
		return "rate-limit max retries"
	case rateLimitInitialBackoff:
		return "rate-limit initial backoff"
	default:
		return "rate-limit max backoff"
	}
}

func (p *rateLimitArgParser) finish() (rateLimitRequest, error) {
	normalizedFormat, err := normalizeOutputFormat("rate-limit", p.req.Format, []string{"text", "json"})
	if err != nil {
		return p.req, err
	}
	p.req.Format = normalizedFormat
	if len(p.rest) == 0 {
		if p.req.hasSetValues() {
			p.req.Action = "set"
		}
		return p.req, nil
	}
	return p.applyAction()
}

func (p *rateLimitArgParser) applyAction() (rateLimitRequest, error) {
	action := strings.ToLower(strings.TrimSpace(p.rest[0]))
	switch action {
	case "status", "show", "options", "list":
		return p.applyShowAction()
	case "set", "configure":
		p.req.Action = "set"
		return p.req, parseRateLimitSetArgs(&p.req, p.rest[1:])
	case "reset", "clear":
		return p.applyResetAction()
	default:
		return p.applyImplicitSet()
	}
}

func (p *rateLimitArgParser) applyShowAction() (rateLimitRequest, error) {
	if len(p.rest) > 1 {
		return p.req, unexpectedExtraArgsError{Command: "rate-limit", Args: p.rest[1:], Usage: rateLimitUsage}
	}
	if p.req.hasSetValues() {
		return p.req, unexpectedExtraArgsError{Command: "rate-limit status", Args: rateLimitSetFlags(p.req), Usage: rateLimitUsage}
	}
	p.req.Action = "show"
	return p.req, nil
}

func (p *rateLimitArgParser) applyResetAction() (rateLimitRequest, error) {
	if len(p.rest) > 1 {
		return p.req, unexpectedExtraArgsError{Command: "rate-limit reset", Args: p.rest[1:], Usage: rateLimitUsage}
	}
	if p.req.hasSetValues() {
		return p.req, unexpectedExtraArgsError{Command: "rate-limit reset", Args: rateLimitSetFlags(p.req), Usage: rateLimitUsage}
	}
	p.req.Action = "reset"
	return p.req, nil
}

func (p *rateLimitArgParser) applyImplicitSet() (rateLimitRequest, error) {
	if len(p.rest) == 1 {
		value, err := parseRateLimitPositiveInt(p.rest[0], "max-retries", "rate-limit max retries")
		if err == nil {
			p.req.Action = "set"
			p.req.MaxRetries = &value
			return p.req, nil
		}
	}
	return p.req, unexpectedExtraArgsError{Command: "rate-limit", Args: []string{p.rest[0]}, Usage: rateLimitUsage}
}

func rateLimitSetFlags(req rateLimitRequest) []string {
	flags := []string{}
	if req.MaxRetries != nil {
		flags = append(flags, "--max-retries")
	}
	if req.InitialBackoffMS != nil {
		flags = append(flags, "--initial-backoff-ms")
	}
	if req.MaxBackoffMS != nil {
		flags = append(flags, "--max-backoff-ms")
	}
	if len(flags) == 0 {
		return nil
	}
	return flags
}

func parseRateLimitSetArgs(req *rateLimitRequest, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if len(args) == 1 {
		if isRateLimitSetField(args[0]) {
			return missingFlagValueError{
				Command: "rate-limit set",
				Flag:    args[0],
				Usage:   rateLimitSetUsage,
			}
		}
		value, err := parseRateLimitPositiveInt(args[0], "max-retries", "rate-limit max retries")
		if err != nil {
			return err
		}
		req.MaxRetries = &value
		return nil
	}
	for index := 0; index < len(args); index += 2 {
		if index+1 >= len(args) {
			return missingFlagValueError{
				Command: "rate-limit set",
				Flag:    args[index],
				Usage:   rateLimitSetUsage,
			}
		}
		if err := assignRateLimitSetValue(req, args[index], args[index+1]); err != nil {
			return err
		}
	}
	return nil
}

func isRateLimitSetField(raw string) bool {
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimLeft(raw, "-")))
	switch normalized {
	case "max-retries", "retries", "initial-backoff-ms", "initial-backoff", "max-backoff-ms", "max-backoff":
		return true
	default:
		return false
	}
}

func assignRateLimitSetValue(req *rateLimitRequest, key string, raw string) error {
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimLeft(key, "-")))
	switch normalized {
	case "max-retries", "retries":
		value, err := parseRateLimitPositiveInt(raw, key, "rate-limit max retries")
		if err != nil {
			return err
		}
		req.MaxRetries = &value
	case "initial-backoff-ms", "initial-backoff":
		value, err := parseRateLimitPositiveInt(raw, key, "rate-limit initial backoff")
		if err != nil {
			return err
		}
		req.InitialBackoffMS = &value
	case "max-backoff-ms", "max-backoff":
		value, err := parseRateLimitPositiveInt(raw, key, "rate-limit max backoff")
		if err != nil {
			return err
		}
		req.MaxBackoffMS = &value
	default:
		return unexpectedExtraArgsError{
			Command: "rate-limit set",
			Args:    []string{key},
			Usage:   rateLimitFieldUsage,
		}
	}
	return nil
}

func parseRateLimitPositiveInt(value string, flag string, label string) (int, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed <= 0 {
		return 0, invalidFlagValueError{
			Flag:    flag,
			Value:   value,
			Message: label + " must be a positive integer",
			Usage:   rateLimitUsage,
		}
	}
	return parsed, nil
}

func (a *App) ResetLimits(args []string) error {
	req, err := parseResetLimitsArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if _, err := config.UnsetFileValue(path, "rate_limit"); err != nil {
		return err
	}
	previous := buildRateLimitOptionsReport(a.Config.RateLimit)
	a.Config.RateLimit = config.RateLimitConfig{}
	report := resetLimitsReport{
		Kind:     "reset_limits",
		Action:   "reset",
		Status:   "ok",
		Path:     path,
		Target:   req.Target,
		Previous: previous,
		Current:  buildRateLimitOptionsReport(a.Config.RateLimit),
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderResetLimitsReport(a.Out, report)
	return nil
}

func parseResetLimitsArgs(args []string) (resetLimitsRequest, error) {
	req := resetLimitsRequest{Format: "text", Target: "user"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "reset-limits",
					Flag:    arg,
					Usage:   resetLimitsUsage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "reset-limits",
					Flag:    arg,
					Usage:   resetLimitsUsage,
				}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "reset-limits",
					Flag:    arg,
					Usage:   resetLimitsUsage,
				}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{
					Command: "reset-limits",
					Option:  arg,
					Usage:   resetLimitsUsage,
				}
			}
			return req, unexpectedExtraArgsError{
				Command: "reset-limits",
				Args:    []string{arg},
				Usage:   resetLimitsUsage,
			}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("reset-limits", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func renderResetLimitsReport(out io.Writer, report resetLimitsReport) {
	fmt.Fprintln(out, "Reset Limits")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	fmt.Fprintf(out, "  Previous retries %d\n", report.Previous.MaxRetries)
	fmt.Fprintf(out, "  Current retries  %d\n", report.Current.MaxRetries)
}

func buildRateLimitReport(action string, target string, path string, previous *rateLimitOptionsReport, cfg config.RateLimitConfig) rateLimitReport {
	current := buildRateLimitOptionsReport(cfg)
	if path == "" {
		target = ""
	}
	return rateLimitReport{
		Kind:              "rate_limit",
		Action:            action,
		Status:            "ok",
		Path:              path,
		Target:            target,
		MaxRetries:        current.MaxRetries,
		InitialBackoffMS:  current.InitialBackoffMS,
		MaxBackoffMS:      current.MaxBackoffMS,
		RetryableStatuses: append([]int(nil), current.RetryableStatuses...),
		Previous:          previous,
	}
}

func buildRateLimitOptionsReport(cfg config.RateLimitConfig) rateLimitOptionsReport {
	snapshot := anthropicRateLimitOptions(cfg).Report()
	return rateLimitOptionsReport{
		Kind:              "rate_limit_options",
		Action:            "show",
		Status:            "ok",
		MaxRetries:        snapshot.MaxRetries,
		InitialBackoffMS:  snapshot.InitialBackoffMS,
		MaxBackoffMS:      snapshot.MaxBackoffMS,
		RetryableStatuses: append([]int(nil), snapshot.RetryableStatuses...),
	}
}

func renderRateLimitReport(out io.Writer, report rateLimitReport) {
	fmt.Fprintln(out, "Rate Limit")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Previous != nil {
		fmt.Fprintf(out, "  Previous retries %d\n", report.Previous.MaxRetries)
	}
	fmt.Fprintf(out, "  Max retries      %d\n", report.MaxRetries)
	fmt.Fprintf(out, "  Initial backoff  %dms\n", report.InitialBackoffMS)
	fmt.Fprintf(out, "  Max backoff      %dms\n", report.MaxBackoffMS)
	fmt.Fprintf(out, "  Retry statuses   %s\n", joinInts(report.RetryableStatuses))
}

func renderRateLimitOptionsReport(out io.Writer, report rateLimitOptionsReport) {
	fmt.Fprintln(out, "Rate Limit Options")
	fmt.Fprintf(out, "  Max retries      %d\n", report.MaxRetries)
	fmt.Fprintf(out, "  Initial backoff  %dms\n", report.InitialBackoffMS)
	fmt.Fprintf(out, "  Max backoff      %dms\n", report.MaxBackoffMS)
	fmt.Fprintf(out, "  Retry statuses   %s\n", joinInts(report.RetryableStatuses))
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func (a *App) openSession(overrides config.FlagOverrides) (*session.Session, error) {
	id := overrides.SessionID
	resuming := strings.TrimSpace(overrides.Resume) != ""
	if strings.TrimSpace(overrides.FromPR) != "" {
		if strings.TrimSpace(overrides.Resume) != "" {
			return nil, errors.New("--from-pr cannot be combined with --resume or --continue")
		}
		if strings.TrimSpace(overrides.SessionID) != "" && !overrides.ForkSession {
			return nil, errors.New("--session-id can only be used with --from-pr when --fork-session is also specified")
		}
		resolved, err := a.resolveFromPRSession(overrides.FromPR)
		if err != nil {
			return nil, err
		}
		overrides.Resume = resolved
		id = resolved
		resuming = true
	}
	var sess *session.Session
	var err error
	if overrides.ForkSession {
		if strings.TrimSpace(overrides.Resume) == "" {
			return nil, errors.New("--fork-session requires --resume, --continue, or --from-pr")
		}
		id = overrides.Resume
		if id == "true" {
			id = "latest"
		}
		sess, err = a.Sessions.Fork(id, "")
		if err != nil {
			return nil, err
		}
		if customID := strings.TrimSpace(overrides.SessionID); customID != "" {
			if _, err := a.Sessions.Rename(sess.ID, customID); err != nil {
				return nil, err
			}
			sess, err = a.Sessions.OpenExisting(customID)
			if err != nil {
				return nil, err
			}
		}
		if err := a.restoreTodosFromSession(sess); err != nil {
			return nil, err
		}
		return a.applyResumeSessionAt(sess, overrides.ResumeSessionAt)
	}
	if overrides.Resume != "" {
		id = overrides.Resume
		if id == "true" {
			id = "latest"
		}
		sess, err = a.Sessions.OpenExisting(id)
	} else {
		sess, err = a.Sessions.Open(id)
	}
	if err != nil {
		return nil, err
	}
	if resuming || strings.TrimSpace(id) != "" {
		if err := a.restoreTodosFromSession(sess); err != nil {
			return nil, err
		}
	}
	return a.applyResumeSessionAt(sess, overrides.ResumeSessionAt)
}

func (a *App) resolveFromPRSession(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("--from-pr requires a pull request reference or a linked bookmark")
	}
	items, err := bookmarks.NewStore(a.Config.ConfigHome).List(bookmarks.ListOptions{Workspace: a.Workspace})
	if err != nil {
		return "", err
	}
	if strings.EqualFold(ref, "true") {
		for _, bookmark := range items {
			if bookmark.PRNumber > 0 && strings.TrimSpace(bookmark.SessionID) != "" {
				return bookmark.SessionID, nil
			}
		}
		return "", fmt.Errorf("no pull request bookmark found in workspace %s", a.Workspace)
	}
	query, err := parsePullRequestReference(ref)
	if err != nil {
		return "", err
	}
	matches := []bookmarks.Bookmark{}
	for _, bookmark := range items {
		if strings.TrimSpace(bookmark.SessionID) == "" || bookmark.PRNumber <= 0 {
			continue
		}
		if bookmark.PRNumber != query.Number {
			continue
		}
		if query.Repo != "" && !strings.EqualFold(strings.TrimSpace(bookmark.PRRepo), query.Repo) {
			continue
		}
		matches = append(matches, bookmark)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no session bookmark linked to pull request %s", ref)
	}
	if len(matches) > 1 && query.Repo == "" {
		return "", fmt.Errorf("pull request %s matches multiple bookmarks; use OWNER/REPO#%d or a pull request URL", ref, query.Number)
	}
	return matches[0].SessionID, nil
}

func (a *App) applyResumeSessionAt(sess *session.Session, messageID string) (*session.Session, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return sess, nil
	}
	if sess == nil {
		return nil, errors.New("session is required")
	}
	index := assistantMessageIndexByID(sess.Messages, messageID)
	if index < 0 {
		return nil, fmt.Errorf("assistant message id %q not found in resumed session %q", messageID, sess.ID)
	}
	kept := append([]anthropic.Message(nil), sess.Messages[:index+1]...)
	if a.Sessions != nil {
		if _, err := a.Sessions.ReplaceMessages(sess, kept); err != nil {
			return nil, err
		}
	}
	sess.Messages = kept
	return sess, nil
}

func assistantMessageIndexByID(messages []anthropic.Message, id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1
	}
	for index, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		if msg.ID == id {
			return index
		}
		for _, block := range msg.Content {
			if block.ID == id {
				return index
			}
		}
	}
	return -1
}

func (a *App) restoreTodosFromSession(sess *session.Session) error {
	items, ok := lastTodoWriteItems(sess.Messages)
	if !ok {
		return nil
	}
	if todoItemsAllCompletedForRestore(items) {
		items = nil
	}
	_, err := todos.Replace(a.Workspace, items)
	return err
}

func lastTodoWriteItems(messages []anthropic.Message) ([]todos.Item, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !strings.EqualFold(msg.Role, "assistant") {
			continue
		}
		for j := len(msg.Content) - 1; j >= 0; j-- {
			block := msg.Content[j]
			if block.Type != "tool_use" || tools.CanonicalToolName(block.Name) != "todo_write" {
				continue
			}
			var payload struct {
				Todos []todos.Item `json:"todos"`
			}
			if err := json.Unmarshal(block.Input, &payload); err != nil {
				return nil, false
			}
			return payload.Todos, true
		}
	}
	return nil, false
}

func todoItemsAllCompletedForRestore(items []todos.Item) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if strings.TrimSpace(item.Status) != "completed" {
			return false
		}
	}
	return true
}

func sessionStartSource(overrides config.FlagOverrides) string {
	if strings.TrimSpace(overrides.Resume) != "" || strings.TrimSpace(overrides.SessionID) != "" {
		return "resume"
	}
	return "startup"
}

func (a *App) lifecycleHookRunner() hooks.Runner {
	cfg := a.effectiveConfig()
	return hooks.Runner{
		Config:                 cfg.Hooks,
		Workspace:              a.Workspace,
		ConfigHome:             cfg.ConfigHome,
		Disabled:               cfg.EffectiveDisableAllHooks(),
		AllowedHTTPHookURLs:    cfg.AllowedHTTPHookURLs,
		HTTPHookAllowedEnvVars: cfg.HTTPHookAllowedEnvVars,
		PromptRunner:           a.hookPromptRunner(cfg),
	}
}

func (a *App) runNotificationHook(ctx context.Context, notificationType string, title string, message string) {
	if !notificationsEnabled(a.Config.Future.NotificationsEnabled) {
		return
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	report, err := a.lifecycleHookRunner().NotificationReport(ctx, notificationType, title, message)
	if err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "notification hook error: %v\n", err)
	}
	a.renderLifecycleHookFeedback("notification", report)
	if report.Denied && a.Err != nil {
		fmt.Fprintf(a.Err, "notification hook denied: %s\n", hookReportDeniedMessage(report))
	}
}

func (a *App) runSubagentStartHook(ctx context.Context, agentID string, agentType string) {
	if strings.TrimSpace(agentID) == "" {
		return
	}
	report, err := a.lifecycleHookRunner().SubagentStartReport(ctx, agentID, firstNonEmpty(agentType, "agent"))
	if err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "subagent start hook error: %v\n", err)
	}
	a.renderLifecycleHookFeedback("subagent start", report)
	if report.Denied && a.Err != nil {
		fmt.Fprintf(a.Err, "subagent start hook denied: %s\n", hookReportDeniedMessage(report))
	}
}

func (a *App) runWorktreeCreateHook(ctx context.Context, allocation worktree.Allocation, source string) error {
	input, err := worktreeHookInput(allocation, source, "")
	if err != nil {
		return err
	}
	report, err := a.lifecycleHookRunner().WorktreeCreateReport(ctx, allocation.ID, allocation.Path, allocation.Ref, input)
	if err != nil {
		return err
	}
	a.renderLifecycleHookFeedback("worktree create", report)
	if report.Denied {
		return fmt.Errorf("worktree create hook denied: %s", hookReportDeniedMessage(report))
	}
	return nil
}

func (a *App) runWorktreeRemoveHook(ctx context.Context, allocation worktree.Allocation, reason string) error {
	input, err := worktreeHookInput(allocation, "", reason)
	if err != nil {
		return err
	}
	report, err := a.lifecycleHookRunner().WorktreeRemoveReport(ctx, allocation.ID, allocation.Path, allocation.Ref, reason, input)
	if err != nil {
		return err
	}
	a.renderLifecycleHookFeedback("worktree remove", report)
	if report.Denied {
		return fmt.Errorf("worktree remove hook denied: %s", hookReportDeniedMessage(report))
	}
	return nil
}

func (a *App) removeAllocatedWorktree(ctx context.Context, allocation worktree.Allocation, reason string) error {
	removeErr := worktree.Remove(a.Workspace, allocation.ID)
	hookErr := a.runWorktreeRemoveHook(ctx, allocation, reason)
	if removeErr != nil {
		return removeErr
	}
	return hookErr
}

func worktreeHookInput(allocation worktree.Allocation, source string, reason string) (string, error) {
	payload := map[string]string{
		"worktree_id":   allocation.ID,
		"worktree_path": allocation.Path,
		"ref":           allocation.Ref,
	}
	if strings.TrimSpace(source) != "" {
		payload["source"] = strings.TrimSpace(source)
	}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = strings.TrimSpace(reason)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a *App) runTaskCreatedHook(ctx context.Context, task background.Task) {
	input, err := taskHookInput(task)
	if err != nil {
		if a.Err != nil {
			fmt.Fprintf(a.Err, "task created hook payload error: %v\n", err)
		}
		return
	}
	report, err := a.lifecycleHookRunner().TaskCreatedReport(ctx, task.ID, taskKindForHook(task), task.Status, input)
	if err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "task created hook error: %v\n", err)
	}
	a.renderLifecycleHookFeedback("task created", report)
	if report.Denied && a.Err != nil {
		fmt.Fprintf(a.Err, "task created hook denied: %s\n", hookReportDeniedMessage(report))
	}
}

func (a *App) runTaskCompletedHook(ctx context.Context, task background.Task, reason string) {
	input, err := taskHookInput(task)
	if err != nil {
		if a.Err != nil {
			fmt.Fprintf(a.Err, "task completed hook payload error: %v\n", err)
		}
		return
	}
	report, err := a.lifecycleHookRunner().TaskCompletedReport(ctx, task.ID, taskKindForHook(task), task.Status, reason, input)
	if err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "task completed hook error: %v\n", err)
	}
	a.renderLifecycleHookFeedback("task completed", report)
	if report.Denied && a.Err != nil {
		fmt.Fprintf(a.Err, "task completed hook denied: %s\n", hookReportDeniedMessage(report))
	}
}

func taskHookInput(task background.Task) (string, error) {
	data, err := json.Marshal(task)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func taskKindForHook(task background.Task) string {
	return firstNonEmpty(task.Kind, "background")
}

func (a *App) runSubagentStopHook(ctx context.Context, agentID string, agentType string, transcriptPath string, lastAssistant string, stopHookActive bool) {
	if strings.TrimSpace(agentID) == "" {
		return
	}
	report, err := a.lifecycleHookRunner().SubagentStopReport(ctx, agentID, firstNonEmpty(agentType, "agent"), transcriptPath, lastAssistant, stopHookActive)
	if err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "subagent stop hook error: %v\n", err)
	}
	a.renderLifecycleHookFeedback("subagent stop", report)
	if report.Denied && a.Err != nil {
		fmt.Fprintf(a.Err, "subagent stop hook denied: %s\n", hookReportDeniedMessage(report))
	}
}

func subagentTypeForTask(task background.Task) string {
	if strings.TrimSpace(task.AgentType) != "" {
		return strings.TrimSpace(task.AgentType)
	}
	if strings.TrimSpace(task.Kind) != "" {
		return strings.TrimSpace(task.Kind)
	}
	return "agent"
}

func lastBackgroundLogLine(store background.Store, task background.Task) string {
	logs, err := store.Logs(task.ID, 64*1024)
	if err != nil {
		return ""
	}
	lines := strings.Split(logs, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func (a *App) runSessionStartHook(ctx context.Context, sess *session.Session, source string) error {
	if sess == nil {
		return nil
	}
	data, err := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"source":          source,
		"session_id":      sess.ID,
		"transcript_path": sess.Path,
		"cwd":             a.Workspace,
		"permission_mode": a.Config.PermissionMode,
		"model":           a.Config.Model,
		"identity":        sess.Identity,
	})
	if err != nil {
		return err
	}
	runner := a.lifecycleHookRunner()
	runner.SessionID = sess.ID
	report, err := runner.SessionStartReport(ctx, string(data))
	if err != nil {
		return err
	}
	return a.applySessionStartHookOutput(sess, hooks.SessionStartOutputFromReport(report))
}

func (a *App) applySessionStartHookOutput(sess *session.Session, output hooks.SessionStartOutput) error {
	if sess == nil || a.Sessions == nil {
		return nil
	}
	if len(output.AdditionalContexts) > 0 {
		text := "SessionStart hook additional context:\n\n" + strings.Join(output.AdditionalContexts, "\n\n")
		if err := a.appendHookSessionMessage(sess, anthropic.TextMessage("user", text)); err != nil {
			return err
		}
	}
	for _, message := range output.InitialMessages {
		if err := a.appendHookSessionMessage(sess, anthropic.TextMessage("user", message)); err != nil {
			return err
		}
	}
	if len(output.WatchPaths) > 0 {
		if err := a.persistSessionStartWatchPaths(sess.ID, output.WatchPaths); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) appendHookSessionMessage(sess *session.Session, msg anthropic.Message) error {
	if err := a.Sessions.Append(sess.ID, msg); err != nil {
		return err
	}
	sess.Messages = append(sess.Messages, msg)
	return nil
}

type sessionStartWatchPathReport struct {
	Kind      string   `json:"kind"`
	SessionID string   `json:"session_id"`
	Paths     []string `json:"paths"`
}

type watchPathStateReport struct {
	Kind      string                       `json:"kind"`
	SessionID string                       `json:"session_id"`
	Files     map[string]watchPathSnapshot `json:"files"`
}

type watchPathSnapshot struct {
	Exists      bool   `json:"exists"`
	Size        int64  `json:"size"`
	ModUnixNano int64  `json:"mod_unix_nano"`
	SHA256      string `json:"sha256"`
}

func (a *App) persistSessionStartWatchPaths(sessionID string, paths []string) error {
	configHome := strings.TrimSpace(a.Config.ConfigHome)
	if configHome == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	clean := cleanedStrings(paths)
	if len(clean) == 0 {
		return nil
	}
	dir := filepath.Join(configHome, "hooks", "watch-paths")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sessionStartWatchPathReport{
		Kind:      "session_start_watch_paths",
		SessionID: sessionID,
		Paths:     clean,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, safeHookStateName(sessionID)+".json"), append(data, '\n'), 0o644)
}

func (a *App) runHooksWatchPaths(ctx context.Context, req hooksRequest) (hooksWatchPathsReport, error) {
	action := strings.TrimSpace(req.WatchAction)
	if action == "" {
		action = "list"
	}
	report := hooksWatchPathsReport{
		Kind:   "hooks_watch_paths",
		Action: action,
		Status: "ok",
	}
	sessions, err := a.loadSessionWatchPaths(req.SessionID)
	if err != nil {
		report.Status = "error"
		return report, err
	}
	if action == "list" {
		report.Sessions = sessions
		if strings.TrimSpace(req.SessionID) != "" {
			report.SessionID = req.SessionID
			if len(sessions) > 0 {
				report.Paths = sessions[0].Paths
			}
		}
		if len(sessions) == 0 {
			report.Status = "empty"
		}
		return report, nil
	}
	if action != "check" {
		report.Status = "error"
		return report, fmt.Errorf("unknown hooks watch-paths action %q", action)
	}
	if len(sessions) == 0 {
		report.Status = "empty"
		return report, nil
	}
	var firstErr error
	initialized := false
	for _, sess := range sessions {
		sessionReport, checkErr := a.checkSessionWatchPaths(ctx, req, sess)
		if report.SessionID == "" && len(sessions) == 1 {
			report.SessionID = sessionReport.SessionID
			report.Paths = sessionReport.Paths
		}
		report.Changes = append(report.Changes, sessionReport.Changes...)
		report.HookReports = append(report.HookReports, sessionReport.HookReports...)
		if sessionReport.Status == "initialized" {
			initialized = true
		}
		if checkErr != nil && firstErr == nil {
			firstErr = checkErr
		}
	}
	switch {
	case firstErr != nil:
		report.Status = "error"
	case len(report.Changes) > 0:
		report.Status = "changed"
	case initialized:
		report.Status = "initialized"
	default:
		report.Status = "unchanged"
	}
	return report, firstErr
}

func (a *App) loadSessionWatchPaths(sessionID string) ([]sessionWatchPaths, error) {
	configHome := strings.TrimSpace(a.Config.ConfigHome)
	if configHome == "" {
		return nil, nil
	}
	dir := filepath.Join(configHome, "hooks", "watch-paths")
	if strings.TrimSpace(sessionID) != "" {
		entry, err := a.readSessionWatchPathFile(filepath.Join(dir, safeHookStateName(sessionID)+".json"))
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []sessionWatchPaths{entry}, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]sessionWatchPaths, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		item, readErr := a.readSessionWatchPathFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		if len(item.Paths) > 0 {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

func (a *App) readSessionWatchPathFile(path string) (sessionWatchPaths, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sessionWatchPaths{}, err
	}
	var report sessionStartWatchPathReport
	if err := json.Unmarshal(data, &report); err != nil {
		return sessionWatchPaths{}, err
	}
	return sessionWatchPaths{
		SessionID: strings.TrimSpace(report.SessionID),
		Paths:     cleanedStrings(report.Paths),
	}, nil
}

func (a *App) checkSessionWatchPaths(ctx context.Context, req hooksRequest, watched sessionWatchPaths) (hooksWatchPathsReport, error) {
	report := hooksWatchPathsReport{
		Kind:      "hooks_watch_paths",
		Action:    "check",
		Status:    "ok",
		SessionID: watched.SessionID,
		Paths:     append([]string(nil), watched.Paths...),
	}
	current, err := snapshotWatchPaths(a.Workspace, watched.Paths)
	if err != nil {
		report.Status = "error"
		return report, err
	}
	statePath := a.watchPathStatePath(watched.SessionID)
	previous, existed, err := readWatchPathState(statePath)
	if err != nil {
		report.Status = "error"
		return report, err
	}
	if err := writeWatchPathState(statePath, watched.SessionID, current); err != nil {
		report.Status = "error"
		return report, err
	}
	if !existed {
		report.Status = "initialized"
		return report, nil
	}
	report.Changes = diffWatchPathSnapshots(previous.Files, current)
	if len(report.Changes) == 0 {
		report.Status = "unchanged"
		return report, nil
	}
	report.Status = "changed"
	runner := hooks.Runner{
		Config:                 a.Config.Hooks,
		Workspace:              a.Workspace,
		ConfigHome:             a.Config.ConfigHome,
		SessionID:              watched.SessionID,
		Timeout:                time.Duration(req.TimeoutMS) * time.Millisecond,
		Disabled:               a.Config.EffectiveDisableAllHooks(),
		AllowedHTTPHookURLs:    a.Config.AllowedHTTPHookURLs,
		HTTPHookAllowedEnvVars: a.Config.HTTPHookAllowedEnvVars,
		PromptRunner:           a.hookPromptRunner(a.effectiveConfig()),
	}
	var firstErr error
	for _, change := range report.Changes {
		inputData, err := json.Marshal(map[string]string{
			"path":       change.Path,
			"operation":  change.Operation,
			"session_id": watched.SessionID,
			"source":     "watch_paths",
		})
		if err != nil {
			report.Status = "error"
			return report, err
		}
		payload := hooks.Payload{
			Event:     "file_changed",
			Tool:      change.Operation,
			ToolName:  change.Operation,
			ToolInput: json.RawMessage(inputData),
			Input:     string(inputData),
			FilePath:  change.Path,
			Operation: change.Operation,
		}
		hookList := hooks.HooksForPayload(a.Config.Hooks, payload)
		hookReport, runErr := runner.RunHooks(ctx, hookList, payload)
		report.HookReports = append(report.HookReports, hookReport)
		if runErr != nil && firstErr == nil {
			firstErr = runErr
		}
	}
	if firstErr != nil {
		report.Status = "error"
	}
	return report, firstErr
}

func (a *App) watchPathStatePath(sessionID string) string {
	configHome := strings.TrimSpace(a.Config.ConfigHome)
	if configHome == "" {
		return ""
	}
	return filepath.Join(configHome, "hooks", "watch-state", safeHookStateName(sessionID)+".json")
}

func snapshotWatchPaths(workspace string, paths []string) (map[string]watchPathSnapshot, error) {
	out := map[string]watchPathSnapshot{}
	for _, path := range cleanedStrings(paths) {
		resolved := resolveWatchPath(workspace, path)
		info, err := os.Stat(resolved)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			err = filepath.WalkDir(resolved, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return nil
				}
				snapshot, err := snapshotFile(path, info)
				if err != nil {
					return err
				}
				out[displayWatchPath(workspace, path)] = snapshot
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		snapshot, err := snapshotFile(resolved, info)
		if err != nil {
			return nil, err
		}
		out[displayWatchPath(workspace, resolved)] = snapshot
	}
	return out, nil
}

func resolveWatchPath(workspace string, path string) string {
	path = strings.TrimSpace(path)
	if filepath.IsAbs(path) || strings.TrimSpace(workspace) == "" {
		return filepath.Clean(path)
	}
	return filepath.Join(workspace, path)
}

func displayWatchPath(workspace string, path string) string {
	path = filepath.Clean(path)
	if strings.TrimSpace(workspace) != "" {
		if rel, err := filepath.Rel(workspace, path); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func snapshotFile(path string, info os.FileInfo) (watchPathSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return watchPathSnapshot{}, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return watchPathSnapshot{}, err
	}
	return watchPathSnapshot{
		Exists:      true,
		Size:        info.Size(),
		ModUnixNano: info.ModTime().UnixNano(),
		SHA256:      hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func readWatchPathState(path string) (watchPathStateReport, bool, error) {
	if strings.TrimSpace(path) == "" {
		return watchPathStateReport{}, false, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return watchPathStateReport{}, false, nil
	}
	if err != nil {
		return watchPathStateReport{}, false, err
	}
	var report watchPathStateReport
	if err := json.Unmarshal(data, &report); err != nil {
		return watchPathStateReport{}, false, err
	}
	if report.Files == nil {
		report.Files = map[string]watchPathSnapshot{}
	}
	return report, true, nil
}

func writeWatchPathState(path string, sessionID string, files map[string]watchPathSnapshot) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	report := watchPathStateReport{
		Kind:      "hooks_watch_state",
		SessionID: sessionID,
		Files:     files,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func diffWatchPathSnapshots(previous map[string]watchPathSnapshot, current map[string]watchPathSnapshot) []watchPathChange {
	changes := []watchPathChange{}
	for path, snapshot := range current {
		old, ok := previous[path]
		switch {
		case !ok:
			changes = append(changes, watchPathChange{Path: path, Operation: "created"})
		case old.SHA256 != snapshot.SHA256 || old.Size != snapshot.Size:
			changes = append(changes, watchPathChange{Path: path, Operation: "changed"})
		}
	}
	for path := range previous {
		if _, ok := current[path]; !ok {
			changes = append(changes, watchPathChange{Path: path, Operation: "deleted"})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Operation < changes[j].Operation
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

func safeHookStateName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "session"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	name := strings.Trim(builder.String(), ".")
	if name == "" {
		return "session"
	}
	return name
}

func (a *App) ensureSessionIdentity(sess *session.Session, purpose string, input string, titleOverride string) error {
	if sess == nil || a.Sessions == nil {
		return nil
	}
	update := session.SessionIdentity{
		Workspace: a.Workspace,
		Worktree:  a.Workspace,
		Purpose:   strings.TrimSpace(purpose),
	}
	if title := strings.TrimSpace(titleOverride); title != "" {
		update.Title = title
	} else if title := sessionTitleFromInput(input); title != "" && sessionIdentityTitleCanAutoUpdate(sess) {
		update.Title = title
	}
	identity, err := a.Sessions.UpdateIdentity(sess.ID, update)
	if err != nil {
		return err
	}
	sess.Identity = identity
	return nil
}

func sessionIdentityTitleCanAutoUpdate(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	title := strings.TrimSpace(sess.Identity.Title)
	return title == "" || title == strings.TrimSpace(sess.ID)
}

func sessionTitleFromInput(input string) string {
	words := strings.Fields(input)
	if len(words) == 0 {
		return ""
	}
	title := strings.Join(words, " ")
	const maxTitleRunes = 80
	runes := []rune(title)
	if len(runes) <= maxTitleRunes {
		return title
	}
	return string(runes[:maxTitleRunes])
}

func (a *App) runSessionEndHook(ctx context.Context, sess *session.Session, reason string) error {
	if sess == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "exit"
	}
	data, err := json.Marshal(map[string]any{
		"hook_event_name": "SessionEnd",
		"reason":          reason,
		"session_id":      sess.ID,
		"transcript_path": sess.Path,
		"cwd":             a.Workspace,
		"permission_mode": a.Config.PermissionMode,
		"model":           a.Config.Model,
	})
	if err != nil {
		return err
	}
	runner := a.lifecycleHookRunner()
	runner.SessionID = sess.ID
	report, err := runner.SessionEndReport(ctx, string(data), reason)
	if err != nil {
		return err
	}
	a.renderLifecycleHookFeedback("session end", report)
	if report.Denied {
		return fmt.Errorf("session end hook denied: %s", hookReportDeniedMessage(report))
	}
	return nil
}

func (a *App) runInstructionsLoadedHooks(ctx context.Context, sessionID string, loadReason string) error {
	runner := a.lifecycleHookRunner()
	runner.SessionID = strings.TrimSpace(sessionID)
	if len(runner.Config.InstructionsLoaded) == 0 && len(runner.Config.InstructionsLoadedCommands) == 0 {
		return nil
	}
	files, err := memory.DiscoverWithRulesImport(a.Workspace, a.memoryRulesImportOptions())
	if err != nil {
		return err
	}
	loadReason = firstNonEmpty(loadReason, "session_start")
	messages := []string{}
	for _, file := range files {
		report, err := runner.InstructionsLoadedReport(ctx, file.Path, instructionsMemoryType(file), loadReason, nil, "", "")
		if err != nil {
			return err
		}
		if report.Denied {
			if detail := strings.TrimSpace(strings.Join(hooks.MessagesFromReport(report), "\n")); detail != "" {
				return fmt.Errorf("instructions_loaded hook denied: %s", detail)
			}
			return errors.New("instructions_loaded hook denied")
		}
		messages = append(messages, hooks.MessagesFromReport(report)...)
	}
	messages = compactHookSessionMessages(messages)
	if len(messages) > 0 {
		sess, err := a.Sessions.OpenExisting(sessionID)
		if err != nil {
			return err
		}
		text := "InstructionsLoaded hook feedback:\n\n" + strings.Join(messages, "\n\n")
		if err := a.appendHookSessionMessage(sess, anthropic.TextMessage("user", text)); err != nil {
			return err
		}
	}
	return nil
}

func instructionsMemoryType(file memory.File) string {
	return "Project"
}

func compactHookSessionMessages(messages []string) []string {
	out := make([]string, 0, len(messages))
	seen := map[string]struct{}{}
	for _, message := range messages {
		message = strings.TrimSpace(message)
		if message == "" {
			continue
		}
		if _, ok := seen[message]; ok {
			continue
		}
		seen[message] = struct{}{}
		out = append(out, message)
	}
	return out
}

func (a *App) runSetupHook(ctx context.Context, source string, status string) error {
	return runSetupHookPayload(ctx, a.lifecycleHookRunner(), a.Workspace, source, status)
}

func runSetupHookPayload(ctx context.Context, runner hooks.Runner, workspace string, source string, status string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "ok"
	}
	if runner.Workspace == "" {
		runner.Workspace = workspace
	}
	data, err := json.Marshal(map[string]string{
		"source":    source,
		"status":    status,
		"workspace": workspace,
	})
	if err != nil {
		return err
	}
	return runner.Setup(ctx, string(data))
}

func (a *App) writeWorkerState(mode string, status string, sess *session.Session, lastError string) {
	if sess == nil {
		return
	}
	cfg := a.effectiveConfig()
	state := workerstate.New(workerstate.Options{
		Version:        version,
		Mode:           mode,
		Status:         status,
		Workspace:      a.Workspace,
		SessionID:      sess.ID,
		SessionPath:    sess.Path,
		Model:          cfg.Model,
		PermissionMode: cfg.PermissionMode,
		LastError:      lastError,
	})
	if err := workerstate.SavePath(a.workerStatePath(), state); err != nil && a.Err != nil {
		fmt.Fprintln(a.Err, "state:", err)
	}
}

func (a *App) workerStatePath() string {
	path := strings.TrimSpace(a.Config.Future.BackgroundStatePath)
	if path == "" {
		return workerstate.Path(a.Workspace)
	}
	return a.resolveOutputPath(path)
}

func (a *App) sessionIDFromOverrides(overrides config.FlagOverrides) (string, error) {
	id := overrides.SessionID
	if overrides.Resume != "" {
		id = overrides.Resume
		if id == "true" {
			id = "latest"
		}
	}
	if session.IsSessionReferenceAlias(id) {
		return a.Sessions.LatestID()
	}
	return id, nil
}

func (a *App) prompter(sessionID string) *tools.Prompter {
	return a.prompterWithAllowedTools(sessionID, nil)
}

func (a *App) prompterWithSkill(sessionID string, activeSkill *skills.Skill) *tools.Prompter {
	if activeSkill == nil {
		return a.prompterWithAllowedTools(sessionID, nil)
	}
	return a.prompterWithAllowedTools(sessionID, activeSkill.AllowedTools)
}

func (a *App) prompterWithAllowedTools(sessionID string, allowedTools []string) *tools.Prompter {
	cfg := a.effectiveConfig()
	allowRules := append([]string(nil), cfg.PermissionRules.Allow...)
	if len(allowedTools) > 0 && !a.planModeActive() {
		allowRules = addRuleValues(allowRules, skillAllowedToolRules(allowedTools))
	}
	return &tools.Prompter{
		Mode:           tools.Permission(cfg.PermissionMode),
		AllowRules:     allowRules,
		DenyRules:      append([]string(nil), cfg.PermissionRules.Deny...),
		AskRules:       append([]string(nil), cfg.PermissionRules.Ask...),
		DeniedTools:    append([]string(nil), cfg.PermissionRules.DeniedTools...),
		Workspace:      a.Workspace,
		AdditionalDirs: currentEffectiveAdditionalDirs(a.Workspace, cfg.AdditionalDirs),
		DefaultShell:   cfg.DefaultShell,
		In:             a.In,
		Err:            a.Err,
		OnRequest:      a.permissionRequestHook(sessionID),
		OnDecision:     a.permissionDecisionHandler(sessionID),
	}
}

func currentEffectiveAdditionalDirs(workspace string, configDirs []string) []string {
	dirs, err := pathscope.EffectiveDirs(workspace, configDirs)
	if err != nil {
		return nil
	}
	return dirs
}

func (a *App) permissionRequestHook(sessionID string) func(tools.PermissionDecision) {
	return func(decision tools.PermissionDecision) {
		report, err := a.lifecycleHookRunner().PermissionRequestReport(context.Background(), decision.ToolName, []byte(decision.Input))
		if err != nil && a.Err != nil {
			fmt.Fprintf(a.Err, "permission request hook error: %v\n", err)
		}
		a.renderLifecycleHookFeedback("permission request", report)
		if report.Denied && a.Err != nil {
			fmt.Fprintf(a.Err, "permission request hook denied: %s\n", hookReportDeniedMessage(report))
		}
	}
}

func (a *App) permissionDecisionHandler(sessionID string) func(tools.PermissionDecision) {
	audit := a.auditPermissionDecision(sessionID)
	return func(decision tools.PermissionDecision) {
		if audit != nil {
			audit(decision)
		}
		if decision.Allowed {
			return
		}
		report, err := a.lifecycleHookRunner().PermissionDeniedReport(context.Background(), decision.ToolName, []byte(decision.Input), decision.Reason)
		if err != nil && a.Err != nil {
			fmt.Fprintf(a.Err, "permission denied hook error: %v\n", err)
		}
		a.renderLifecycleHookFeedback("permission denied", report)
		if report.Denied && a.Err != nil {
			fmt.Fprintf(a.Err, "permission denied hook denied: %s\n", hookReportDeniedMessage(report))
		}
	}
}

func (a *App) renderLifecycleHookFeedback(label string, report hooks.RunReport) {
	if a.Err == nil {
		return
	}
	messages := hooks.MessagesFromReport(report)
	if len(messages) == 0 {
		return
	}
	fmt.Fprintf(a.Err, "%s hook feedback:\n%s\n", label, strings.Join(messages, "\n"))
}

func hookReportDeniedMessage(report hooks.RunReport) string {
	messages := hooks.MessagesFromReport(report)
	if len(messages) > 0 {
		return strings.Join(messages, "\n")
	}
	return "hook denied"
}

func (a *App) validateGlobalToolRules(overrides config.FlagOverrides, format string) error {
	if err := a.validateGlobalToolRuleList("--allowed-tools", overrides.AllowedTools, format); err != nil {
		return err
	}
	if err := a.validateGlobalToolRuleList("--disallowed-tools", overrides.DisallowedTools, format); err != nil {
		return err
	}
	if overrides.ToolNamesSet {
		if err := a.validateGlobalToolRuleList("--tools", overrides.ToolNames, format); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) validateGlobalToolRuleList(argument string, rules []string, format string) error {
	if err := a.validateToolRuleValues(argument, rules); err != nil {
		return renderCLIError(a.Out, err, format)
	}
	return nil
}

func (a *App) validateToolRuleValues(argument string, rules []string) error {
	for _, rule := range rules {
		toolName := toolRuleName(rule)
		if a.validToolRuleName(toolName) {
			continue
		}
		return toolNameError{
			Argument:  argument,
			ToolName:  toolName,
			Available: a.availableToolNames(),
			Aliases:   tools.ClaudeToolAliases(),
		}
	}
	return nil
}

func (a *App) validToolRuleName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "*" {
		return true
	}
	if isMCPToolPattern(name) {
		return true
	}
	if strings.Contains(name, "*") {
		return a.toolWildcardMatchesRegisteredName(name)
	}
	registry := a.activeToolRegistry()
	if registry == nil {
		return tools.CanonicalToolName(name) != name
	}
	if _, ok := registry.Info(name); ok {
		return true
	}
	if canonical := tools.CanonicalToolName(name); canonical != name {
		_, ok := registry.Info(canonical)
		return ok
	}
	return false
}

func (a *App) toolWildcardMatchesRegisteredName(pattern string) bool {
	registry := a.activeToolRegistry()
	if registry == nil {
		return false
	}
	for _, info := range registry.Infos() {
		if permissionNamePatternMatches(pattern, info.Name) {
			return true
		}
	}
	return false
}

func (a *App) availableToolNames() []string {
	registry := a.activeToolRegistry()
	if registry == nil {
		return nil
	}
	infos := registry.Infos()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	sort.Strings(names)
	return names
}

func (a *App) activeToolRegistry() *tools.Registry {
	if a != nil && a.Tools != nil {
		return a.Tools
	}
	workspace := ""
	if a != nil {
		workspace = a.Workspace
	}
	return tools.NewRegistry(workspace)
}

func toolRuleName(rule string) string {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return ""
	}
	if open := strings.Index(rule, "("); open > 0 && strings.HasSuffix(rule, ")") {
		return strings.TrimSpace(rule[:open])
	}
	if toolName, _, ok := strings.Cut(rule, ":"); ok {
		return strings.TrimSpace(toolName)
	}
	return rule
}

func isMCPToolPattern(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(name, "mcp__") {
		return false
	}
	parts := strings.Split(name, "__")
	return len(parts) >= 3 && strings.TrimSpace(parts[1]) != "" && strings.TrimSpace(parts[2]) != ""
}

func permissionNamePatternMatches(pattern string, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" || value == "" {
		return false
	}
	if pattern == "*" || pattern == value {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		next := strings.Index(value[position:], part)
		if next < 0 {
			return false
		}
		if index == 0 && !strings.HasPrefix(pattern, "*") && next != 0 {
			return false
		}
		position += next + len(part)
	}
	if !strings.HasSuffix(pattern, "*") && len(parts) > 0 {
		last := parts[len(parts)-1]
		return strings.HasSuffix(value, last)
	}
	return true
}

func skillAllowedToolRules(values []string) []string {
	rules := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if value == "*" {
			rules = append(rules, value)
			continue
		}
		toolName := value
		inputNeedle := ""
		if open := strings.Index(value, "("); open > 0 && strings.HasSuffix(value, ")") {
			toolName = strings.TrimSpace(value[:open])
			inputNeedle = strings.TrimSpace(strings.TrimSuffix(value[open+1:], ")"))
			inputNeedle = strings.TrimSpace(strings.TrimSuffix(inputNeedle, "*"))
			inputNeedle = strings.TrimSpace(strings.TrimSuffix(inputNeedle, ":"))
		}
		canonical := strings.TrimSpace(tools.CanonicalToolName(toolName))
		if canonical == "" {
			continue
		}
		if inputNeedle != "" {
			rules = append(rules, canonical+":"+inputNeedle)
			continue
		}
		rules = append(rules, canonical)
	}
	return addRuleValues(nil, rules)
}

func firstWriter(values ...io.Writer) io.Writer {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (a *App) effectiveConfig() config.Config {
	cfg := a.Config
	if a.planModeActive() {
		cfg.PlanMode = true
		cfg.PermissionMode = string(tools.PermissionReadOnly)
		cfg.PermissionRules.Allow = nil
	}
	return cfg
}

func (a *App) planModeActive() bool {
	if a.Config.PlanMode {
		return true
	}
	state, err := planmode.Load(a.Workspace)
	return err == nil && state.Active
}

func (a *App) onToolUse(sessionID string) func(runloop.ToolCall) {
	audit := a.auditToolUse(sessionID)
	return func(call runloop.ToolCall) {
		a.recordToolContextPaths(call)
		if audit != nil {
			audit(call)
		}
	}
}

func (a *App) recordToolContextPaths(call runloop.ToolCall) {
	if a == nil || call.IsError {
		return
	}
	for _, path := range toolContextPaths(call) {
		a.dynamicSkillPaths = appendRecentUniquePath(a.dynamicSkillPaths, path, maxDynamicSkillContextPaths)
	}
}

func toolContextPaths(call runloop.ToolCall) []string {
	if strings.TrimSpace(call.Input) == "" {
		return nil
	}
	var payload struct {
		Path         string `json:"path"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Pattern      string `json:"pattern"`
	}
	if err := json.Unmarshal([]byte(call.Input), &payload); err != nil {
		return nil
	}
	switch tools.CanonicalToolName(call.Name) {
	case "read_file", "write_file", "edit_file", "multi_edit":
		return compactPathValues(firstNonEmpty(payload.Path, payload.FilePath))
	case "notebook_read", "notebook_edit":
		return compactPathValues(payload.NotebookPath)
	case "ls":
		return compactPathValues(firstNonEmpty(payload.Path, "."))
	case "glob":
		return compactPathValues(firstNonEmpty(payload.Path, globContextPath(payload.Pattern)))
	default:
		return nil
	}
}

func globContextPath(pattern string) string {
	pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	if pattern == "" || strings.HasPrefix(pattern, "/") {
		return ""
	}
	parts := strings.Split(pattern, "/")
	fixed := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.ContainsAny(part, "*?[{") {
			break
		}
		fixed = append(fixed, part)
	}
	return strings.Join(fixed, "/")
}

func compactPathValues(values ...string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func appendRecentUniquePath(values []string, value string, limit int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	out := make([]string, 0, len(values)+1)
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			continue
		}
		out = append(out, existing)
	}
	out = append(out, value)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (a *App) auditToolUse(sessionID string) func(runloop.ToolCall) {
	store := audit.NewStore(a.Config.ConfigHome)
	return func(call runloop.ToolCall) {
		if err := store.Append(audit.Event{
			Type:      "tool_use",
			SessionID: sessionID,
			Workspace: a.Workspace,
			ToolName:  call.Name,
			Input:     audit.Clip(call.Input, 16*1024),
			Output:    audit.Clip(call.Output, 16*1024),
			IsError:   call.IsError,
		}); err != nil && a.Err != nil {
			fmt.Fprintln(a.Err, "audit:", err)
		}
	}
}

func (a *App) auditPermissionDecision(sessionID string) func(tools.PermissionDecision) {
	store := audit.NewStore(a.Config.ConfigHome)
	return func(decision tools.PermissionDecision) {
		if err := store.Append(audit.Event{
			Type:               "permission",
			SessionID:          sessionID,
			Workspace:          a.Workspace,
			ToolName:           decision.ToolName,
			Input:              audit.Clip(decision.Input, 16*1024),
			PermissionMode:     string(decision.Mode),
			RequiredPermission: string(decision.Required),
			Allowed:            audit.Bool(decision.Allowed),
			Reason:             decision.Reason,
			Feedback:           audit.Clip(decision.Feedback, 4*1024),
			PermissionRule:     audit.Clip(decision.Rule, 4*1024),
		}); err != nil && a.Err != nil {
			fmt.Fprintln(a.Err, "audit:", err)
		}
	}
}

func (a *App) systemPrompt() string {
	return a.systemPromptForInput("")
}

func (a *App) systemPromptForInput(input string) string {
	base := "You are Codog, a Go-native coding agent CLI. Be concise, inspect before editing, and use tools when they materially help."
	if strings.TrimSpace(a.Config.SystemPrompt) != "" {
		base = strings.TrimSpace(a.Config.SystemPrompt)
	}
	var builder strings.Builder
	builder.WriteString(base)
	if strings.TrimSpace(a.Config.AppendSystemPrompt) != "" {
		builder.WriteString("\n\n")
		builder.WriteString(strings.TrimSpace(a.Config.AppendSystemPrompt))
	}
	if rendered := currentDatePrompt(time.Now()); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	if rendered := a.gitContextPrompt(); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	if rendered := a.activeIDEPrompt(); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	if language := normalizeConfiguredLanguage(a.Config.Language); language != "" {
		builder.WriteString("\n\n<codog_interface_language>")
		builder.WriteString(html.EscapeString(language))
		builder.WriteString("</codog_interface_language>")
	}
	if effort := strings.TrimSpace(a.Config.ReasoningEffort); effort != "" && !strings.EqualFold(effort, "disabled") {
		builder.WriteString("\n\n<codog_reasoning_effort>")
		builder.WriteString(effectiveEffort(effort))
		builder.WriteString("</codog_reasoning_effort>")
	}
	if fastModeEnabled(a.Config.FastMode) {
		builder.WriteString("\n\n<codog_fast_mode>enabled</codog_fast_mode>")
	}
	includedSkills := map[string]bool{}
	for _, name := range a.Config.EnabledSkills {
		skill, err := a.findRuntimeSkill(name)
		if err != nil {
			continue
		}
		if skill.DisableModelInvocation {
			continue
		}
		includedSkills[strings.ToLower(skill.Name)] = true
		builder.WriteString("\n\n")
		builder.WriteString(skills.RenderPromptBlock(skill))
	}
	for _, skill := range a.pathMatchedSkills(input) {
		key := strings.ToLower(skill.Name)
		if includedSkills[key] {
			continue
		}
		includedSkills[key] = true
		builder.WriteString("\n\n")
		builder.WriteString(skills.RenderPromptBlock(skill))
	}
	if rendered := outputstyle.RenderPrompt(a.Config.ConfigHome, a.Workspace); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	if state, err := planmode.Load(a.Workspace); err == nil {
		if rendered := planmode.RenderPrompt(state); rendered != "" {
			builder.WriteString("\n\n")
			builder.WriteString(rendered)
		}
	}
	if files, err := memory.DiscoverWithRulesImport(a.Workspace, a.memoryRulesImportOptions()); err == nil {
		if rendered := memory.Render(files); rendered != "" {
			builder.WriteString("\n\n")
			builder.WriteString(rendered)
		}
	}
	if rendered := focus.RenderPrompt(a.Workspace); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	if rendered := pathscope.RenderPrompt(a.Workspace, a.Config.AdditionalDirs); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	return builder.String()
}

func (a *App) activeIDEPrompt() string {
	if a.ActiveIDE == nil || a.ActiveIDE.Identity == nil {
		return ""
	}
	state := a.ActiveIDE
	var builder strings.Builder
	builder.WriteString("<active_editor>\n")
	builder.WriteString("Editor: ")
	builder.WriteString(html.EscapeString(state.Identity.Editor))
	if state.Identity.Version != "" {
		builder.WriteString(" ")
		builder.WriteString(html.EscapeString(state.Identity.Version))
	}
	builder.WriteString("\n")
	if state.OpenFile != nil {
		builder.WriteString("Open file: ")
		builder.WriteString(html.EscapeString(state.OpenFile.Path))
		builder.WriteString("\n")
	}
	if state.Selection != nil {
		builder.WriteString(fmt.Sprintf("Selection: %s:%d:%d-%d:%d\n",
			html.EscapeString(state.Selection.Path),
			state.Selection.StartLine,
			state.Selection.StartColumn,
			state.Selection.EndLine,
			state.Selection.EndColumn,
		))
		if text := strings.TrimSpace(state.Selection.Text); text != "" {
			if len(text) > 8192 {
				text = text[:8192] + "\n... (truncated)"
			}
			builder.WriteString("Selected text:\n")
			builder.WriteString(html.EscapeString(text))
			builder.WriteString("\n")
		}
	}
	builder.WriteString("</active_editor>")
	return builder.String()
}

func currentDatePrompt(now time.Time) string {
	return "Today's date is " + now.Format("2006-01-02") + "."
}

func (a *App) gitContextPrompt() string {
	if strings.TrimSpace(a.Workspace) == "" {
		return ""
	}
	inside, err := gitops.Run(a.Workspace, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return ""
	}
	status, err := gitops.Status(a.Workspace)
	if err != nil {
		return ""
	}
	if len(status) > maxSystemGitStatusChars {
		status = status[:maxSystemGitStatusChars] + "\n... (truncated because git status exceeds 2000 characters)"
	}
	branch, _ := gitops.Run(a.Workspace, "branch", "--show-current")
	if strings.TrimSpace(branch) == "" {
		branch, _ = gitops.Run(a.Workspace, "rev-parse", "--abbrev-ref", "HEAD")
	}
	log, _ := gitops.Log(a.Workspace, 5)
	var builder strings.Builder
	builder.WriteString("<git_context>\n")
	builder.WriteString("This is the git status at the start of the conversation. It is a snapshot and will not update automatically.\n")
	if branch = strings.TrimSpace(branch); branch != "" {
		builder.WriteString("\nCurrent branch: ")
		builder.WriteString(branch)
		builder.WriteString("\n")
	}
	builder.WriteString("\nStatus:\n")
	if strings.TrimSpace(status) == "" {
		builder.WriteString("(clean)\n")
	} else {
		builder.WriteString(status)
		builder.WriteString("\n")
	}
	if log = strings.TrimSpace(log); log != "" {
		builder.WriteString("\nRecent commits:\n")
		builder.WriteString(log)
		builder.WriteString("\n")
	}
	builder.WriteString("</git_context>")
	return builder.String()
}

func (a *App) pathMatchedSkills(input string) []skills.Skill {
	paths := a.skillContextPaths(input)
	if len(paths) == 0 {
		return nil
	}
	contextual, err := a.runtimeContextualSkills(paths)
	if err != nil {
		return nil
	}
	return contextual
}

func (a *App) skillContextPaths(input string) []string {
	paths := []string{}
	paths = append(paths, promptrefs.References(input)...)
	paths = append(paths, a.dynamicSkillPaths...)
	if state, err := focus.Load(a.Workspace); err == nil {
		for _, entry := range state.Entries {
			paths = append(paths, entry.Path)
		}
	}
	return addRuleValues(nil, paths)
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func (a *App) slashCompletionCandidates(activeSessionID string) []string {
	return slash.AllCandidates(a.slashCandidateOptions(activeSessionID))
}

func (a *App) slashMenuCandidates(activeSessionID string) []string {
	return slash.MenuCandidates(a.slashCandidateOptions(activeSessionID))
}

func (a *App) slashCandidateOptions(activeSessionID string) slash.CandidateOptions {
	recent := []string{}
	if a.Sessions != nil {
		if sessions, err := a.Sessions.List(); err == nil {
			for _, sess := range sessions {
				recent = append(recent, sess.ID)
				if len(recent) >= 10 {
					break
				}
			}
		}
	}
	extra := a.customSlashCompletionCandidates()
	return slash.CandidateOptions{
		Model:            a.Config.Model,
		ActiveSessionID:  activeSessionID,
		RecentSessionIDs: recent,
		Extra:            extra,
	}
}

func (a *App) customSlashCompletionCandidates() []string {
	candidates := []string{}
	if commands, err := a.runtimeCustomCommands(); err == nil {
		for _, command := range commands {
			if !command.Active {
				continue
			}
			candidates = append(candidates, "/"+strings.ReplaceAll(command.Name, ":", "/")+" ")
		}
	}
	if loadedSkills, err := a.runtimeSkills(); err == nil {
		for _, skill := range loadedSkills {
			if !skill.Active || !skill.UserInvocable {
				continue
			}
			candidates = append(candidates, "/"+strings.ReplaceAll(skill.Name, ":", "/")+" ")
		}
	}
	return candidates
}

type runtimeSlashHelpEntry struct {
	Usage       string `json:"usage"`
	Description string `json:"description"`
}

type slashCommandReport struct {
	Kind              string                  `json:"kind"`
	Action            string                  `json:"action"`
	Status            string                  `json:"status"`
	Query             string                  `json:"query,omitempty"`
	CommandCount      int                     `json:"command_count"`
	Commands          []capabilitySlash       `json:"commands"`
	RuntimeCount      int                     `json:"runtime_count"`
	Runtime           []runtimeSlashHelpEntry `json:"runtime,omitempty"`
	CandidateCount    int                     `json:"candidate_count"`
	Candidates        []string                `json:"candidates,omitempty"`
	ResumeSafeCount   int                     `json:"resume_safe_count"`
	ResumeSafe        []string                `json:"resume_safe,omitempty"`
	CompletionExample string                  `json:"completion_example,omitempty"`
}

func (a *App) Slash(args []string) error {
	req, err := parseSlashArgs(args)
	if err != nil {
		return err
	}
	report := a.slashCommandReport(req)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if req.Action == "candidates" {
		for _, candidate := range report.Candidates {
			fmt.Fprintln(a.Out, candidate)
		}
		return nil
	}
	if req.Action == "show" && req.Query != "" {
		renderSlashCommandShow(a.Out, report)
		return nil
	}
	a.renderSlashHelp(a.Out)
	return nil
}

type slashCommandRequest struct {
	Action string
	Query  string
	Format string
}

func parseSlashArgs(args []string) (slashCommandRequest, error) {
	req := slashCommandRequest{Action: "list", Format: "text"}
	positionals := []string{}
	usage := "codog slash [list|show COMMAND|candidates PREFIX] [--json|--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
			continue
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "slash", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case isHelpFlag(arg):
			return req, flag.ErrHelp
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "slash", Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "slash"); err != nil {
		return req, err
	}
	if len(positionals) == 0 {
		return req, nil
	}
	action := strings.ToLower(strings.TrimSpace(positionals[0]))
	switch action {
	case "list", "ls", "commands", "show-all":
		if len(positionals) > 1 {
			return req, unexpectedExtraArgsError{Command: "slash " + action, Args: positionals[1:], Usage: usage}
		}
		req.Action = "list"
	case "show", "info", "describe":
		if len(positionals) != 2 {
			return req, requiredArgumentError{Command: "slash " + action, Argument: "COMMAND", Usage: usage}
		}
		req.Action = "show"
		req.Query = positionals[1]
	case "candidates", "complete", "completion":
		if len(positionals) != 2 {
			return req, requiredArgumentError{Command: "slash " + action, Argument: "PREFIX", Usage: usage}
		}
		req.Action = "candidates"
		req.Query = positionals[1]
	default:
		if strings.HasPrefix(positionals[0], "/") {
			if len(positionals) > 1 {
				return req, unexpectedExtraArgsError{Command: "slash show", Args: positionals[1:], Usage: usage}
			}
			req.Action = "show"
			req.Query = positionals[0]
			break
		}
		return req, unknownOptionError{Command: "slash", Option: positionals[0], Usage: usage}
	}
	return req, nil
}

func (a *App) slashCommandReport(req slashCommandRequest) slashCommandReport {
	allCommands := slashCapabilities()
	allRuntime := a.runtimeSlashHelpEntries()
	commands := allCommands
	runtime := allRuntime
	candidates := []string{}
	if req.Action == "candidates" {
		query := req.Query
		if !strings.HasPrefix(query, "/") {
			query = "/" + query
		}
		candidates = slash.FilterCandidates(query, a.slashCompletionCandidates(""))
		commands = nil
		runtime = nil
	}
	if req.Action == "show" && req.Query != "" {
		query := req.Query
		if !strings.HasPrefix(query, "/") {
			query = "/" + query
		}
		filtered := []capabilitySlash{}
		for _, command := range allCommands {
			if strings.EqualFold(command.Name, query) {
				filtered = append(filtered, command)
			}
		}
		commands = filtered
		runtime = nil
	}
	return slashCommandReport{
		Kind:              "slash",
		Action:            req.Action,
		Status:            "ok",
		Query:             req.Query,
		CommandCount:      len(commands),
		Commands:          commands,
		RuntimeCount:      len(runtime),
		Runtime:           runtime,
		CandidateCount:    len(candidates),
		Candidates:        candidates,
		ResumeSafeCount:   len(slash.ResumeSupportedNames()),
		ResumeSafe:        slash.ResumeSupportedNames(),
		CompletionExample: "Type / in the TUI or REPL, then press Tab to complete.",
	}
}

func renderSlashCommandShow(out io.Writer, report slashCommandReport) {
	if len(report.Commands) == 0 {
		fmt.Fprintf(out, "Slash command not found: %s\n", report.Query)
		return
	}
	for _, command := range report.Commands {
		fmt.Fprintf(out, "%s\n", command.Name)
		fmt.Fprintf(out, "  Usage            %s\n", command.Usage)
		fmt.Fprintf(out, "  Description      %s\n", command.Description)
		fmt.Fprintf(out, "  Resume supported %t\n", command.ResumeSupported)
	}
}

func (a *App) renderSlashHelp(out io.Writer) {
	slash.RenderHelp(out)
	entries := a.runtimeSlashHelpEntries()
	if len(entries) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Runtime slash commands:")
	for _, entry := range entries {
		fmt.Fprintf(out, "%-24s %s\n", entry.Usage, entry.Description)
	}
}

func (a *App) runtimeSlashHelpEntries() []runtimeSlashHelpEntry {
	entries := []runtimeSlashHelpEntry{}
	seen := map[string]bool{}
	add := func(name string, argumentHint string, description string) {
		usage := runtimeSlashUsage(name, argumentHint)
		if usage == "" {
			return
		}
		key := strings.ToLower(strings.Fields(usage)[0])
		if seen[key] {
			return
		}
		seen[key] = true
		if description = strings.TrimSpace(description); description == "" {
			description = "Run a workspace, user, or plugin slash command."
		}
		entries = append(entries, runtimeSlashHelpEntry{Usage: usage, Description: description})
	}
	if commands, err := a.runtimeCustomCommands(); err == nil {
		for _, command := range commands {
			if !command.Active {
				continue
			}
			description := command.Description
			if strings.TrimSpace(description) == "" {
				description = command.Preview
			}
			add(command.Name, command.ArgumentHint, description)
		}
	}
	if loadedSkills, err := a.runtimeSkills(); err == nil {
		for _, skill := range loadedSkills {
			if !skill.Active || !skill.UserInvocable {
				continue
			}
			description := skill.Description
			if strings.TrimSpace(description) == "" {
				description = skill.WhenToUse
			}
			add(skill.Name, skill.ArgumentHint, description)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Usage) < strings.ToLower(entries[j].Usage)
	})
	return entries
}

func runtimeSlashUsage(name string, argumentHint string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	path := "/" + strings.ReplaceAll(strings.TrimPrefix(name, "/"), ":", "/")
	if _, ok := slash.Lookup(path); ok {
		return ""
	}
	argumentHint = strings.TrimSpace(argumentHint)
	if argumentHint == "" {
		return path
	}
	return path + " " + argumentHint
}

type stringListFlag []string

func (v *stringListFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*v = append(*v, part)
		}
	}
	return nil
}

func (v *stringListFlag) String() string {
	if v == nil {
		return ""
	}
	return strings.Join(*v, ",")
}

type appendStringFlag []string

func (v *appendStringFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value != "" {
		*v = append(*v, value)
	}
	return nil
}

func (v *appendStringFlag) String() string {
	if v == nil {
		return ""
	}
	return strings.Join(*v, ",")
}

type trackedStringListFlag struct {
	values *[]string
	set    *bool
}

func (f trackedStringListFlag) Set(value string) error {
	if f.set != nil {
		*f.set = true
	}
	if f.values == nil {
		return nil
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*f.values = append(*f.values, part)
		}
	}
	return nil
}

func (f trackedStringListFlag) String() string {
	if f.values == nil {
		return ""
	}
	return strings.Join(*f.values, ",")
}

type toolSelectionFlag struct {
	values *[]string
	set    *bool
}

func (f toolSelectionFlag) Set(value string) error {
	if f.set != nil {
		*f.set = true
	}
	if f.values == nil {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		*f.values = nil
		return nil
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.EqualFold(part, "default") {
			*f.values = nil
			if f.set != nil {
				*f.set = false
			}
			continue
		}
		*f.values = append(*f.values, part)
	}
	return nil
}

func (f toolSelectionFlag) String() string {
	if f.values == nil {
		return ""
	}
	return strings.Join(*f.values, ",")
}

type optionalFloatFlag struct {
	value **float64
}

func (f optionalFloatFlag) Set(raw string) error {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return err
	}
	*f.value = &value
	return nil
}

func (f optionalFloatFlag) String() string {
	if f.value == nil || *f.value == nil {
		return ""
	}
	return strconv.FormatFloat(**f.value, 'f', -1, 64)
}

func parseFlags(args []string, base config.FlagOverrides) (config.FlagOverrides, string, []string, error) {
	prepared, err := prepareGlobalFlagArgs(args)
	if err != nil {
		return base, "", nil, err
	}
	return newGlobalFlagParser(base).parse(prepared)
}

const interactiveResumeValue = "__codog_interactive_resume__"

func normalizeOptionalResumeFlag(args []string) []string {
	normalized := make([]string, 0, len(args)+1)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		trimmed := strings.TrimSpace(arg)
		if trimmed == "--" || !strings.HasPrefix(trimmed, "-") {
			normalized = append(normalized, args[index:]...)
			break
		}
		if trimmed == "--resume=" || trimmed == "-r=" {
			normalized = append(normalized, strings.TrimSuffix(arg, "=")+"="+interactiveResumeValue)
			continue
		}
		if trimmed != "--resume" && trimmed != "-r" {
			normalized = append(normalized, arg)
			if globalFlagConsumesNext(trimmed) && !strings.Contains(trimmed, "=") && index+1 < len(args) {
				normalized = append(normalized, args[index+1])
				index++
			}
			continue
		}
		if index+1 >= len(args) {
			normalized = append(normalized, arg, interactiveResumeValue)
			continue
		}
		next := strings.TrimSpace(args[index+1])
		if next == "" || strings.HasPrefix(next, "-") || optionalResumeFollowedByCommand(next) {
			normalized = append(normalized, arg, interactiveResumeValue)
			continue
		}
		normalized = append(normalized, arg, args[index+1])
		index++
	}
	return normalized
}

func optionalResumeFollowedByCommand(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if !strings.HasPrefix(value, "/") {
		return looksLikeCommandName(value)
	}
	if _, err := os.Stat(value); err == nil {
		return false
	}
	return true
}

func normalizeOptionalFromPRFlag(args []string) []string {
	normalized := make([]string, 0, len(args)+1)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg != "--from-pr" && arg != "-from-pr" {
			normalized = append(normalized, arg)
			continue
		}
		if index+1 >= len(args) || !looksLikeFromPRValue(args[index+1]) {
			normalized = append(normalized, arg+"=true")
			continue
		}
		normalized = append(normalized, arg, args[index+1])
		index++
	}
	return normalized
}

func normalizeVariadicAddDirFlag(args []string) []string {
	normalized := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg != "--add-dir" && arg != "-add-dir" {
			normalized = append(normalized, arg)
			continue
		}
		normalized = append(normalized, arg)
		index++
		if index >= len(args) || strings.HasPrefix(strings.TrimSpace(args[index]), "-") || looksLikeCommandName(args[index]) {
			index--
			continue
		}
		normalized = append(normalized, args[index])
		for index+1 < len(args) {
			next := strings.TrimSpace(args[index+1])
			if next == "" || strings.HasPrefix(next, "-") || looksLikeCommandName(next) {
				break
			}
			normalized = append(normalized, "--add-dir", args[index+1])
			index++
		}
	}
	return normalized
}

func normalizeVariadicPluginDirFlag(args []string) []string {
	normalized := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg != "--plugin-dir" && arg != "-plugin-dir" {
			normalized = append(normalized, arg)
			continue
		}
		normalized = append(normalized, arg)
		index++
		if index >= len(args) || strings.HasPrefix(strings.TrimSpace(args[index]), "-") || looksLikeCommandName(args[index]) {
			index--
			continue
		}
		normalized = append(normalized, args[index])
		for index+1 < len(args) {
			next := strings.TrimSpace(args[index+1])
			if next == "" || strings.HasPrefix(next, "-") || looksLikeCommandName(next) {
				break
			}
			normalized = append(normalized, "--plugin-dir", args[index+1])
			index++
		}
	}
	return normalized
}
