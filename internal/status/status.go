package status

import (
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
)

type Options struct {
	Version                    string
	Workspace                  string
	ConfigHome                 string
	Model                      string
	FastMode                   bool
	BaseURL                    string
	PermissionMode             string
	MaxTokens                  int
	MaxTurns                   int
	AutoCompactMessages        int
	AuthConfigured             bool
	MCPServerCount             int
	UserPromptSubmitHookCount  int
	SessionStartHookCount      int
	PreHookCount               int
	PostHookCount              int
	PostFailureHookCount       int
	PermissionRequestHookCount int
	PermissionDeniedHookCount  int
	StopHookCount              int
	PreCompactHookCount        int
	PostCompactHookCount       int
	NotificationHookCount      int
	SubagentStartHookCount     int
	SubagentStopHookCount      int
	EnabledSkillCount          int
	PlanActive                 bool
	PlanText                   string
	PlanUpdatedAt              string
	MemoryFiles                []MemoryFileStatus
	ToolNames                  []string
	SessionID                  string
	SessionPath                string
	SessionMessages            int
	SessionCount               int
	GitStatus                  string
	GitError                   string
	SandboxOS                  string
	SandboxDefault             string
	SandboxStrategies          []string
	SandboxAvailable           bool
	Executable                 string
}

type Snapshot struct {
	Kind      string          `json:"kind"`
	Action    string          `json:"action"`
	Status    string          `json:"status"`
	Version   string          `json:"version"`
	Workspace WorkspaceStatus `json:"workspace"`
	Config    ConfigStatus    `json:"config"`
	Session   SessionStatus   `json:"session"`
	Plan      PlanStatus      `json:"plan"`
	Tools     ToolsStatus     `json:"tools"`
	Git       GitStatus       `json:"git"`
	Sandbox   SandboxStatus   `json:"sandbox"`
	Runtime   RuntimeStatus   `json:"runtime"`
}

type WorkspaceStatus struct {
	Path            string             `json:"path"`
	Name            string             `json:"name"`
	MemoryFileCount int                `json:"memory_file_count"`
	MemoryFiles     []MemoryFileStatus `json:"memory_files,omitempty"`
}

type MemoryFileStatus struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Chars     int    `json:"chars"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ConfigStatus struct {
	ConfigHome                 string `json:"config_home"`
	Model                      string `json:"model"`
	FastMode                   bool   `json:"fast_mode"`
	BaseURL                    string `json:"base_url"`
	PermissionMode             string `json:"permission_mode"`
	MaxTokens                  int    `json:"max_tokens"`
	MaxTurns                   int    `json:"max_turns"`
	AutoCompactMessages        int    `json:"auto_compact_messages"`
	AuthConfigured             bool   `json:"auth_configured"`
	MCPServerCount             int    `json:"mcp_server_count"`
	UserPromptSubmitHookCount  int    `json:"user_prompt_submit_hook_count"`
	SessionStartHookCount      int    `json:"session_start_hook_count"`
	PreHookCount               int    `json:"pre_hook_count"`
	PostHookCount              int    `json:"post_hook_count"`
	PostFailureHookCount       int    `json:"post_tool_use_failure_hook_count"`
	PermissionRequestHookCount int    `json:"permission_request_hook_count"`
	PermissionDeniedHookCount  int    `json:"permission_denied_hook_count"`
	StopHookCount              int    `json:"stop_hook_count"`
	PreCompactHookCount        int    `json:"pre_compact_hook_count"`
	PostCompactHookCount       int    `json:"post_compact_hook_count"`
	NotificationHookCount      int    `json:"notification_hook_count"`
	SubagentStartHookCount     int    `json:"subagent_start_hook_count"`
	SubagentStopHookCount      int    `json:"subagent_stop_hook_count"`
	EnabledSkillCount          int    `json:"enabled_skill_count"`
}

type SessionStatus struct {
	Active       bool   `json:"active"`
	ID           string `json:"id,omitempty"`
	Path         string `json:"path,omitempty"`
	MessageCount int    `json:"message_count"`
	SavedCount   int    `json:"saved_count"`
}

type PlanStatus struct {
	Active    bool   `json:"active"`
	Text      string `json:"text,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type ToolsStatus struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}

type GitStatus struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Clean     bool   `json:"clean"`
	Staged    int    `json:"staged"`
	Unstaged  int    `json:"unstaged"`
	Untracked int    `json:"untracked"`
	Conflicts int    `json:"conflicts"`
	Raw       string `json:"raw,omitempty"`
}

type SandboxStatus struct {
	OS         string   `json:"os"`
	Default    string   `json:"default,omitempty"`
	Strategies []string `json:"strategies,omitempty"`
	Available  bool     `json:"available"`
}

type RuntimeStatus struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	GoVersion  string `json:"go_version"`
	Executable string `json:"executable,omitempty"`
}

func Build(opts Options) Snapshot {
	git := parseGitStatus(opts.GitStatus, opts.GitError)
	status := "ok"
	if !git.Available {
		status = "degraded"
	}
	return Snapshot{
		Kind:    "status",
		Action:  "show",
		Status:  status,
		Version: opts.Version,
		Workspace: WorkspaceStatus{
			Path:            opts.Workspace,
			Name:            filepath.Base(opts.Workspace),
			MemoryFileCount: len(opts.MemoryFiles),
			MemoryFiles:     append([]MemoryFileStatus(nil), opts.MemoryFiles...),
		},
		Config: ConfigStatus{
			ConfigHome:                 opts.ConfigHome,
			Model:                      opts.Model,
			FastMode:                   opts.FastMode,
			BaseURL:                    opts.BaseURL,
			PermissionMode:             opts.PermissionMode,
			MaxTokens:                  opts.MaxTokens,
			MaxTurns:                   opts.MaxTurns,
			AutoCompactMessages:        opts.AutoCompactMessages,
			AuthConfigured:             opts.AuthConfigured,
			MCPServerCount:             opts.MCPServerCount,
			UserPromptSubmitHookCount:  opts.UserPromptSubmitHookCount,
			SessionStartHookCount:      opts.SessionStartHookCount,
			PreHookCount:               opts.PreHookCount,
			PostHookCount:              opts.PostHookCount,
			PostFailureHookCount:       opts.PostFailureHookCount,
			PermissionRequestHookCount: opts.PermissionRequestHookCount,
			PermissionDeniedHookCount:  opts.PermissionDeniedHookCount,
			StopHookCount:              opts.StopHookCount,
			PreCompactHookCount:        opts.PreCompactHookCount,
			PostCompactHookCount:       opts.PostCompactHookCount,
			NotificationHookCount:      opts.NotificationHookCount,
			SubagentStartHookCount:     opts.SubagentStartHookCount,
			SubagentStopHookCount:      opts.SubagentStopHookCount,
			EnabledSkillCount:          opts.EnabledSkillCount,
		},
		Session: SessionStatus{
			Active:       opts.SessionID != "",
			ID:           opts.SessionID,
			Path:         opts.SessionPath,
			MessageCount: opts.SessionMessages,
			SavedCount:   opts.SessionCount,
		},
		Plan: PlanStatus{
			Active:    opts.PlanActive,
			Text:      strings.TrimSpace(opts.PlanText),
			UpdatedAt: opts.PlanUpdatedAt,
		},
		Tools: ToolsStatus{
			Count: len(opts.ToolNames),
			Names: append([]string(nil), opts.ToolNames...),
		},
		Git: git,
		Sandbox: SandboxStatus{
			OS:         opts.SandboxOS,
			Default:    opts.SandboxDefault,
			Strategies: append([]string(nil), opts.SandboxStrategies...),
			Available:  opts.SandboxAvailable,
		},
		Runtime: RuntimeStatus{
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			GoVersion:  runtime.Version(),
			Executable: opts.Executable,
		},
	}
}

func RenderText(w io.Writer, snapshot Snapshot) {
	fmt.Fprintln(w, "Status")
	fmt.Fprintf(w, "  Version          %s\n", snapshot.Version)
	fmt.Fprintf(w, "  Status           %s\n", snapshot.Status)
	fmt.Fprintf(w, "  Workspace        %s\n", snapshot.Workspace.Path)
	fmt.Fprintf(w, "  Memory files     %d\n", snapshot.Workspace.MemoryFileCount)
	fmt.Fprintf(w, "  Model            %s\n", snapshot.Config.Model)
	fmt.Fprintf(w, "  Fast mode        %t\n", snapshot.Config.FastMode)
	fmt.Fprintf(w, "  Permission       %s\n", snapshot.Config.PermissionMode)
	if snapshot.Plan.Active {
		fmt.Fprintln(w, "  Plan             active")
	} else {
		fmt.Fprintln(w, "  Plan             inactive")
	}
	fmt.Fprintf(w, "  Auth configured  %t\n", snapshot.Config.AuthConfigured)
	if snapshot.Session.Active {
		fmt.Fprintf(w, "  Session          %s (%d messages)\n", snapshot.Session.ID, snapshot.Session.MessageCount)
	} else {
		fmt.Fprintf(w, "  Session          none (%d saved)\n", snapshot.Session.SavedCount)
	}
	if snapshot.Git.Available {
		fmt.Fprintf(w, "  Git              branch=%s clean=%t staged=%d unstaged=%d untracked=%d conflicts=%d\n",
			snapshot.Git.Branch,
			snapshot.Git.Clean,
			snapshot.Git.Staged,
			snapshot.Git.Unstaged,
			snapshot.Git.Untracked,
			snapshot.Git.Conflicts,
		)
	} else {
		fmt.Fprintf(w, "  Git              unavailable: %s\n", snapshot.Git.Error)
	}
	fmt.Fprintf(w, "  Sandbox          available=%t default=%s\n", snapshot.Sandbox.Available, snapshot.Sandbox.Default)
	fmt.Fprintf(w, "  Tools            %d\n", snapshot.Tools.Count)
}

func parseGitStatus(raw string, errText string) GitStatus {
	raw = strings.TrimSpace(raw)
	if strings.TrimSpace(errText) != "" {
		return GitStatus{Available: false, Error: strings.TrimSpace(errText), Raw: raw}
	}
	if raw == "" {
		return GitStatus{Available: true, Clean: true}
	}
	status := GitStatus{Available: true, Raw: raw}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "## ") {
			status.Branch = parseBranch(line)
			continue
		}
		if len(line) < 2 {
			continue
		}
		index := line[0]
		worktree := line[1]
		if index == '?' && worktree == '?' {
			status.Untracked++
			continue
		}
		if isConflict(index, worktree) {
			status.Conflicts++
			continue
		}
		if index != ' ' {
			status.Staged++
		}
		if worktree != ' ' {
			status.Unstaged++
		}
	}
	status.Clean = status.Staged == 0 && status.Unstaged == 0 && status.Untracked == 0 && status.Conflicts == 0
	return status
}

func parseBranch(line string) string {
	branch := strings.TrimSpace(strings.TrimPrefix(line, "## "))
	if branch == "" {
		return ""
	}
	if strings.HasPrefix(branch, "No commits yet on ") {
		return strings.TrimPrefix(branch, "No commits yet on ")
	}
	if before, _, ok := strings.Cut(branch, "..."); ok {
		return before
	}
	if before, _, ok := strings.Cut(branch, " "); ok {
		return before
	}
	return branch
}

func isConflict(index byte, worktree byte) bool {
	return index == 'U' || worktree == 'U' ||
		(index == 'A' && worktree == 'A') ||
		(index == 'D' && worktree == 'D')
}
