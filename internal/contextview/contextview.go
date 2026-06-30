package contextview

import (
	"fmt"
	"io"
	"strings"

	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/memory"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/usage"
)

type Options struct {
	Status       localstatus.Snapshot
	Memory       memory.Report
	Focus        focus.Report
	TokenUsage   usage.Summary
	SystemPrompt string
	Warnings     []string
}

type Report struct {
	Kind          string                 `json:"kind"`
	Action        string                 `json:"action"`
	Status        string                 `json:"status"`
	Workspace     WorkspaceSummary       `json:"workspace"`
	Config        ConfigSummary          `json:"config"`
	Git           localstatus.GitStatus  `json:"git"`
	Session       SessionSummary         `json:"session"`
	Plan          localstatus.PlanStatus `json:"plan"`
	Tools         ToolsSummary           `json:"tools"`
	Memory        MemorySummary          `json:"memory"`
	Focus         FocusSummary           `json:"focus"`
	Prompt        PromptSummary          `json:"prompt"`
	TokenEstimate usage.Summary          `json:"token_estimate"`
	Signals       []string               `json:"signals,omitempty"`
}

type WorkspaceSummary struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type ConfigSummary struct {
	Model                      string `json:"model"`
	PermissionMode             string `json:"permission_mode"`
	MaxTokens                  int    `json:"max_tokens"`
	MaxTurns                   int    `json:"max_turns"`
	AutoCompactMessages        int    `json:"auto_compact_messages"`
	AuthConfigured             bool   `json:"auth_configured"`
	MCPServerCount             int    `json:"mcp_server_count"`
	EnabledSkillCount          int    `json:"enabled_skill_count"`
	UserPromptSubmitHookCount  int    `json:"user_prompt_submit_hook_count"`
	SessionStartHookCount      int    `json:"session_start_hook_count"`
	SessionEndHookCount        int    `json:"session_end_hook_count"`
	SetupHookCount             int    `json:"setup_hook_count"`
	PreHookCount               int    `json:"pre_hook_count"`
	PostHookCount              int    `json:"post_hook_count"`
	PostFailureHookCount       int    `json:"post_tool_use_failure_hook_count"`
	PermissionRequestHookCount int    `json:"permission_request_hook_count"`
	PermissionDeniedHookCount  int    `json:"permission_denied_hook_count"`
	StopHookCount              int    `json:"stop_hook_count"`
	StopFailureHookCount       int    `json:"stop_failure_hook_count"`
	PreCompactHookCount        int    `json:"pre_compact_hook_count"`
	PostCompactHookCount       int    `json:"post_compact_hook_count"`
	NotificationHookCount      int    `json:"notification_hook_count"`
	SubagentStartHookCount     int    `json:"subagent_start_hook_count"`
	SubagentStopHookCount      int    `json:"subagent_stop_hook_count"`
	WorktreeCreateHookCount    int    `json:"worktree_create_hook_count"`
	WorktreeRemoveHookCount    int    `json:"worktree_remove_hook_count"`
	TaskCreatedHookCount       int    `json:"task_created_hook_count"`
	TaskCompletedHookCount     int    `json:"task_completed_hook_count"`
	FileChangedHookCount       int    `json:"file_changed_hook_count"`
}

type SessionSummary struct {
	Active       bool   `json:"active"`
	ID           string `json:"id,omitempty"`
	Path         string `json:"path,omitempty"`
	MessageCount int    `json:"message_count"`
}

type ToolsSummary struct {
	Count int      `json:"count"`
	Names []string `json:"names,omitempty"`
}

type MemorySummary struct {
	InstructionFiles int              `json:"instruction_files"`
	TotalChars       int              `json:"total_chars"`
	Files            []memory.Summary `json:"files,omitempty"`
}

type FocusSummary struct {
	FocusedPaths int           `json:"focused_paths"`
	Entries      []focus.Entry `json:"entries,omitempty"`
}

type PromptSummary struct {
	Lines           int `json:"lines"`
	Chars           int `json:"chars"`
	EstimatedTokens int `json:"estimated_tokens"`
}

func Build(options Options) Report {
	memoryChars := 0
	for _, file := range options.Memory.Files {
		memoryChars += file.Chars
	}
	prompt := PromptSummary{
		Lines:           countLines(options.SystemPrompt),
		Chars:           len([]rune(options.SystemPrompt)),
		EstimatedTokens: estimateTokens(options.SystemPrompt),
	}
	signals := append([]string(nil), options.Warnings...)
	if !options.Status.Config.AuthConfigured {
		signals = append(signals, "auth is not configured")
	}
	if options.Status.Git.Available && !options.Status.Git.Clean {
		signals = append(signals, "git working tree has local changes")
	}
	if options.Status.Git.Conflicts > 0 {
		signals = append(signals, "git working tree has conflicts")
	}
	if options.Memory.InstructionFiles == 0 {
		signals = append(signals, "no project memory files loaded")
	}
	if options.Focus.Total == 0 {
		signals = append(signals, "no focused paths selected")
	}
	if !options.Status.Session.Active {
		signals = append(signals, "no active session selected")
	}
	if options.Status.Plan.Active {
		signals = append(signals, "plan mode is active; tool permissions are read-only")
	}
	status := options.Status.Status
	if status == "" {
		status = "ok"
	}
	if len(options.Warnings) != 0 && status == "ok" {
		status = "degraded"
	}
	return Report{
		Kind:   "context",
		Action: "show",
		Status: status,
		Workspace: WorkspaceSummary{
			Path: options.Status.Workspace.Path,
			Name: options.Status.Workspace.Name,
		},
		Config: ConfigSummary{
			Model:                      options.Status.Config.Model,
			PermissionMode:             options.Status.Config.PermissionMode,
			MaxTokens:                  options.Status.Config.MaxTokens,
			MaxTurns:                   options.Status.Config.MaxTurns,
			AutoCompactMessages:        options.Status.Config.AutoCompactMessages,
			AuthConfigured:             options.Status.Config.AuthConfigured,
			MCPServerCount:             options.Status.Config.MCPServerCount,
			EnabledSkillCount:          options.Status.Config.EnabledSkillCount,
			UserPromptSubmitHookCount:  options.Status.Config.UserPromptSubmitHookCount,
			SessionStartHookCount:      options.Status.Config.SessionStartHookCount,
			SessionEndHookCount:        options.Status.Config.SessionEndHookCount,
			SetupHookCount:             options.Status.Config.SetupHookCount,
			PreHookCount:               options.Status.Config.PreHookCount,
			PostHookCount:              options.Status.Config.PostHookCount,
			PostFailureHookCount:       options.Status.Config.PostFailureHookCount,
			PermissionRequestHookCount: options.Status.Config.PermissionRequestHookCount,
			PermissionDeniedHookCount:  options.Status.Config.PermissionDeniedHookCount,
			StopHookCount:              options.Status.Config.StopHookCount,
			StopFailureHookCount:       options.Status.Config.StopFailureHookCount,
			PreCompactHookCount:        options.Status.Config.PreCompactHookCount,
			PostCompactHookCount:       options.Status.Config.PostCompactHookCount,
			NotificationHookCount:      options.Status.Config.NotificationHookCount,
			SubagentStartHookCount:     options.Status.Config.SubagentStartHookCount,
			SubagentStopHookCount:      options.Status.Config.SubagentStopHookCount,
			WorktreeCreateHookCount:    options.Status.Config.WorktreeCreateHookCount,
			WorktreeRemoveHookCount:    options.Status.Config.WorktreeRemoveHookCount,
			TaskCreatedHookCount:       options.Status.Config.TaskCreatedHookCount,
			TaskCompletedHookCount:     options.Status.Config.TaskCompletedHookCount,
			FileChangedHookCount:       options.Status.Config.FileChangedHookCount,
		},
		Git: options.Status.Git,
		Session: SessionSummary{
			Active:       options.Status.Session.Active,
			ID:           options.Status.Session.ID,
			Path:         options.Status.Session.Path,
			MessageCount: options.Status.Session.MessageCount,
		},
		Plan: options.Status.Plan,
		Tools: ToolsSummary{
			Count: options.Status.Tools.Count,
			Names: append([]string(nil), options.Status.Tools.Names...),
		},
		Memory: MemorySummary{
			InstructionFiles: options.Memory.InstructionFiles,
			TotalChars:       memoryChars,
			Files:            append([]memory.Summary(nil), options.Memory.Files...),
		},
		Focus: FocusSummary{
			FocusedPaths: options.Focus.Total,
			Entries:      append([]focus.Entry(nil), options.Focus.Entries...),
		},
		Prompt:        prompt,
		TokenEstimate: options.TokenUsage,
		Signals:       signals,
	}
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Context")
	fmt.Fprintf(w, "  Status           %s\n", report.Status)
	fmt.Fprintf(w, "  Workspace        %s\n", report.Workspace.Path)
	fmt.Fprintf(w, "  Model            %s\n", valueOrNone(report.Config.Model))
	fmt.Fprintf(w, "  Permission       %s\n", valueOrNone(report.Config.PermissionMode))
	if report.Plan.Active {
		fmt.Fprintln(w, "  Plan             active")
	} else {
		fmt.Fprintln(w, "  Plan             inactive")
	}
	fmt.Fprintf(w, "  Tools            %d\n", report.Tools.Count)
	if report.Git.Available {
		fmt.Fprintf(w, "  Git              branch=%s clean=%t staged=%d unstaged=%d untracked=%d conflicts=%d\n",
			valueOrNone(report.Git.Branch),
			report.Git.Clean,
			report.Git.Staged,
			report.Git.Unstaged,
			report.Git.Untracked,
			report.Git.Conflicts,
		)
	} else {
		fmt.Fprintf(w, "  Git              unavailable: %s\n", valueOrNone(report.Git.Error))
	}
	if report.Session.Active {
		fmt.Fprintf(w, "  Session          %s (%d messages)\n", report.Session.ID, report.Session.MessageCount)
	} else {
		fmt.Fprintln(w, "  Session          none")
	}
	fmt.Fprintf(w, "  Tokens           input=%d output=%d total=%d estimated_usd=%.5f\n",
		report.TokenEstimate.InputTokens,
		report.TokenEstimate.OutputTokens,
		report.TokenEstimate.TotalTokens,
		report.TokenEstimate.EstimatedUSD,
	)
	fmt.Fprintf(w, "  System prompt    lines=%d chars=%d approx_tokens=%d\n",
		report.Prompt.Lines,
		report.Prompt.Chars,
		report.Prompt.EstimatedTokens,
	)
	fmt.Fprintf(w, "  Memory files     %d chars=%d\n", report.Memory.InstructionFiles, report.Memory.TotalChars)
	for _, file := range report.Memory.Files {
		truncated := ""
		if file.Truncated {
			truncated = " truncated=true"
		}
		fmt.Fprintf(w, "    - %s lines=%d chars=%d%s\n", file.Path, file.Lines, file.Chars, truncated)
	}
	fmt.Fprintf(w, "  Focused paths    %d\n", report.Focus.FocusedPaths)
	for _, entry := range report.Focus.Entries {
		state := "missing"
		if entry.Exists {
			state = "ok"
		}
		fmt.Fprintf(w, "    - %s %s %s\n", entry.Kind, state, entry.Path)
	}
	if len(report.Signals) == 0 {
		return
	}
	fmt.Fprintln(w, "Signals")
	for _, signal := range report.Signals {
		fmt.Fprintf(w, "  - %s\n", signal)
	}
}

func countLines(body string) int {
	if body == "" {
		return 0
	}
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return 1
	}
	return strings.Count(body, "\n") + 1
}

func estimateTokens(body string) int {
	chars := len([]rune(body))
	if chars == 0 {
		return 0
	}
	return chars/4 + 1
}

func valueOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}
