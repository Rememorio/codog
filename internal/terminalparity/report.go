package terminalparity

import (
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/slash"
	"github.com/Rememorio/codog/internal/tui"
)

var requiredInteractiveCommands = []string{
	"/help",
	"/status",
	"/statusline",
	"/permissions",
	"/model",
	"/resume",
	"/compact",
	"/diff",
	"/review",
	"/skills",
	"/mcp",
	"/hooks",
	"/vim",
	"/theme",
	"/cost",
	"/context",
	"/exit",
}

// Report is the JSON-safe terminal interaction parity summary exposed by
// capabilities and parity harnesses.
type Report struct {
	Status                    string   `json:"status"`
	SlashCommandCount         int      `json:"slash_command_count"`
	ResumeSafeSlashCount      int      `json:"resume_safe_slash_count"`
	RequiredCommandCount      int      `json:"required_command_count"`
	MissingRequiredCommands   []string `json:"missing_required_commands,omitempty"`
	DuplicateSlashCommands    []string `json:"duplicate_slash_commands,omitempty"`
	TUISubmitSupported        bool     `json:"tui_submit_supported"`
	TUISlashCompletion        bool     `json:"tui_slash_completion"`
	TUIPreviewWidth           int      `json:"tui_preview_width"`
	TUIPreviewHeight          int      `json:"tui_preview_height"`
	PermissionCommandsPresent bool     `json:"permission_commands_present"`
	StatusCommandsPresent     bool     `json:"status_commands_present"`
	SessionCommandsPresent    bool     `json:"session_commands_present"`
}

// Build inspects the interactive command registry and deterministic TUI preview
// model to report whether the local terminal experience has the core surfaces
// users expect from a Claude-Code-style coding agent.
func Build() Report {
	specs := slash.Specs()
	names := slashNameSet(specs)
	missing := missingRequiredCommands(names)
	duplicates := duplicateSlashCommands(specs)
	submitPreview := tui.PreviewWithCandidates("summarize this repo", nil, 80, 24, false, true)
	completionPreview := tui.PreviewWithCandidates("/sta", nil, 80, 24, true, false)
	report := Report{
		Status:                    "ready",
		SlashCommandCount:         len(specs),
		ResumeSafeSlashCount:      len(slash.ResumeSupportedNames()),
		RequiredCommandCount:      len(requiredInteractiveCommands),
		MissingRequiredCommands:   missing,
		DuplicateSlashCommands:    duplicates,
		TUISubmitSupported:        submitPreview.Submitted && submitPreview.Prompt == "summarize this repo",
		TUISlashCompletion:        completionPreview.Value == "/status " || len(completionPreview.Matches) > 0,
		TUIPreviewWidth:           80,
		TUIPreviewHeight:          24,
		PermissionCommandsPresent: names["/permissions"] && names["/approve"] && names["/deny"],
		StatusCommandsPresent:     names["/status"] && names["/statusline"] && names["/doctor"],
		SessionCommandsPresent:    names["/resume"] && names["/session"] && names["/history"],
	}
	if len(report.MissingRequiredCommands) > 0 ||
		len(report.DuplicateSlashCommands) > 0 ||
		!report.TUISubmitSupported ||
		!report.TUISlashCompletion ||
		!report.PermissionCommandsPresent ||
		!report.StatusCommandsPresent ||
		!report.SessionCommandsPresent {
		report.Status = "gap"
	}
	return report
}

// RequiredInteractiveCommands returns the stable command checklist used by
// Build.
func RequiredInteractiveCommands() []string {
	return append([]string(nil), requiredInteractiveCommands...)
}

func slashNameSet(specs []slash.Spec) map[string]bool {
	names := make(map[string]bool, len(specs))
	for _, spec := range specs {
		names[strings.ToLower(strings.TrimSpace(spec.Name))] = true
	}
	return names
}

func missingRequiredCommands(names map[string]bool) []string {
	var missing []string
	for _, command := range requiredInteractiveCommands {
		if !names[strings.ToLower(command)] {
			missing = append(missing, command)
		}
	}
	return missing
}

func duplicateSlashCommands(specs []slash.Spec) []string {
	counts := map[string]int{}
	for _, spec := range specs {
		name := strings.ToLower(strings.TrimSpace(spec.Name))
		if name != "" {
			counts[name]++
		}
	}
	var duplicates []string
	for name, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, name)
		}
	}
	sort.Strings(duplicates)
	return duplicates
}
