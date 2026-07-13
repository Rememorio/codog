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
	"/fast",
	"/output-style",
	"/sandbox",
	"/add-dir",
	"/rename",
	"/branch",
	"/btw",
	"/plan",
	"/stats",
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
	TUIFullScreenLayout       bool     `json:"tui_full_screen_layout"`
	TUIInlineLayout           bool     `json:"tui_inline_layout"`
	TUIDefaultInline          bool     `json:"tui_default_inline"`
	TUIResumePicker           bool     `json:"tui_resume_picker"`
	TUIWorkspaceTrustPrompt   bool     `json:"tui_workspace_trust_prompt"`
	TUITranscriptViewport     bool     `json:"tui_transcript_viewport"`
	TUILocalHelpPanel         bool     `json:"tui_local_help_panel"`
	TUISettingsTabs           bool     `json:"tui_settings_tabs"`
	TUIExtensionTabs          bool     `json:"tui_extension_tabs"`
	TUIRuntimeTabs            bool     `json:"tui_runtime_tabs"`
	TUIConversationTabs       bool     `json:"tui_conversation_tabs"`
	TUIMemorySelector         bool     `json:"tui_memory_selector"`
	TUIExportDialog           bool     `json:"tui_export_dialog"`
	TUITextInputDialog        bool     `json:"tui_text_input_dialog"`
	TUIPreferencePanels       bool     `json:"tui_preference_panels"`
	TUISideQuestionPanel      bool     `json:"tui_side_question_panel"`
	TUIStatusBar              bool     `json:"tui_status_bar"`
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
	helpPreview := tui.PreviewWithCandidates("/help", nil, 80, 24, false, false)
	inlinePreview := tui.PreviewInlineWithCandidates("", nil, 80, 24, false, false)
	resumePreview := tui.PreviewSessionPicker([]tui.SessionChoice{
		{ID: "resume-alpha", Title: "Alpha session"},
		{ID: "resume-beta", Title: "Beta session"},
	}, "beta", 80, 24, true)
	trustPreview := tui.PreviewWorkspaceTrust("/workspace/codog", 80)
	settingsPreview := tui.PreviewWithCommandView(tui.CommandView{
		Title: "Settings",
		Tabs: []tui.CommandViewTab{
			{Title: "Status", Lines: []string{"Workspace ready"}},
			{Title: "Config", Items: []tui.CommandViewItem{{Label: "Model", Value: "glm52", Action: "model"}}},
			{Title: "Usage", Lines: []string{"Tokens 42"}},
		},
	}, []string{"right"}, 80, 24)
	extensionsPreview := tui.PreviewWithCommandView(tui.CommandView{
		Title: "Extensions",
		Tabs: []tui.CommandViewTab{
			{Title: "Skills", Items: []tui.CommandViewItem{{Label: "review", Value: "enabled"}}},
			{Title: "MCP", Items: []tui.CommandViewItem{{Label: "local", Value: "stdio"}}},
			{Title: "Hooks", Lines: []string{"No hooks configured"}},
			{Title: "Plugins", Lines: []string{"No plugins installed"}},
			{Title: "Agents", Lines: []string{"No agents found"}},
		},
	}, []string{"right"}, 80, 24)
	runtimePreview := tui.PreviewWithCommandView(tui.CommandView{
		Title: "Runtime",
		Tabs: []tui.CommandViewTab{
			{Title: "Tasks", Items: []tui.CommandViewItem{{Label: "run tests", Value: "running · shell", SecondaryLabel: "stop", SecondaryCommand: "/tasks stop task-1", SecondaryKey: "x"}}, RefreshCommand: "/tasks"},
			{Title: "Teams", Items: []tui.CommandViewItem{{Label: "reviewers", Value: "running · 2 tasks"}}, RefreshCommand: "/team"},
			{Title: "Schedules", Items: []tui.CommandViewItem{{Label: "Daily review", Value: "@daily · enabled"}}, RefreshCommand: "/cron"},
			{Title: "Agent runs", Items: []tui.CommandViewItem{{Label: "@reviewer · inspect changes", Value: "running · healthy"}}, RefreshCommand: "/agents runs"},
		},
	}, []string{"right", "right", "right"}, 80, 24)
	conversationPreview := tui.PreviewWithCommandView(tui.CommandView{
		Title: "Conversation",
		Tabs: []tui.CommandViewTab{
			{Title: "History", Items: []tui.CommandViewItem{{Label: "inspect changes", Value: "user", Action: "prefill", Command: "inspect changes"}}, RefreshCommand: "/history"},
			{Title: "Sessions", Items: []tui.CommandViewItem{{Label: "Current review", Value: "current · 4 messages"}}, RefreshCommand: "/sessions"},
			{Title: "Bookmarks", Items: []tui.CommandViewItem{{Label: "before-review", Value: "session-1"}}, RefreshCommand: "/bookmarks"},
		},
	}, []string{"right", "right"}, 80, 24)
	memoryPreview := tui.PreviewWithCommandView(tui.CommandView{
		Title: "Memory",
		Tabs: []tui.CommandViewTab{{
			Title: "Files",
			Items: []tui.CommandViewItem{{
				Label:            "AGENTS.md",
				Value:            "project · 12 lines",
				Command:          "/memory edit AGENTS.md",
				SecondaryLabel:   "view",
				SecondaryCommand: "/memory show AGENTS.md",
				SecondaryKey:     "v",
			}},
			RefreshCommand: "/memory",
		}},
	}, nil, 80, 24)
	exportPreview := tui.PreviewWithExportDialog(tui.ExportDialog{DefaultFilename: "session.md"}, []string{"down", "enter"}, 80, 24)
	inputPreview := tui.PreviewWithTextInputDialog(tui.TextInputDialog{
		Title:  "Add working directory",
		Prompt: "Enter a directory path:",
		Action: "add-dir",
	}, nil, 80, 24)
	preferencePreview := tui.PreviewWithCommandView(tui.CommandView{
		Title:        "Fast mode",
		SelectedItem: 1,
		Tabs: []tui.CommandViewTab{{Title: "Preference", Items: []tui.CommandViewItem{
			{Label: "Enabled"},
			{Label: "Disabled", Value: "current"},
		}}},
	}, nil, 80, 24)
	sideQuestionPreview := tui.PreviewWithInformation(tui.InformationView{
		Title:            "/btw",
		Lines:            []string{"Why did this test fail?", "", "The fixture used the wrong path."},
		DismissOnConfirm: true,
	}, nil, 80, 24)
	report := Report{
		Status:                    "ready",
		SlashCommandCount:         len(specs),
		ResumeSafeSlashCount:      len(slash.ResumeSupportedNames()),
		RequiredCommandCount:      len(requiredInteractiveCommands),
		MissingRequiredCommands:   missing,
		DuplicateSlashCommands:    duplicates,
		TUISubmitSupported:        submitPreview.Submitted && submitPreview.Prompt == "summarize this repo",
		TUISlashCompletion:        completionPreview.Value == "/status " || len(completionPreview.Matches) > 0,
		TUIFullScreenLayout:       strings.Contains(submitPreview.View, "composer") && strings.Contains(submitPreview.View, "Codog TUI"),
		TUIInlineLayout:           strings.Contains(inlinePreview.View, "codog") && strings.Contains(inlinePreview.View, "❯") && !strings.Contains(inlinePreview.View, "composer"),
		TUIDefaultInline:          true,
		TUIResumePicker:           resumePreview.MatchCount == 1 && resumePreview.SelectedID == "resume-beta" && strings.Contains(resumePreview.View, "Beta session") && !strings.Contains(resumePreview.View, "Alpha session"),
		TUIWorkspaceTrustPrompt:   trustPreview.SelectedChoice == 0 && strings.Contains(trustPreview.View, "Accessing workspace:") && strings.Contains(trustPreview.View, "Yes, I trust this folder") && strings.Contains(trustPreview.View, "No, exit"),
		TUITranscriptViewport:     strings.Contains(submitPreview.View, "Interactive coding agent ready"),
		TUILocalHelpPanel:         helpPreview.HelpOpen && strings.Contains(helpPreview.View, "Core commands"),
		TUISettingsTabs:           settingsPreview.CommandView && strings.Contains(settingsPreview.View, "Status") && strings.Contains(settingsPreview.View, "Config") && strings.Contains(settingsPreview.View, "Usage") && strings.Contains(settingsPreview.View, "Model") && strings.Contains(settingsPreview.View, "glm52"),
		TUIExtensionTabs:          extensionsPreview.CommandView && strings.Contains(extensionsPreview.View, "Skills") && strings.Contains(extensionsPreview.View, "MCP") && strings.Contains(extensionsPreview.View, "Hooks") && strings.Contains(extensionsPreview.View, "Plugins") && strings.Contains(extensionsPreview.View, "Agents") && strings.Contains(extensionsPreview.View, "local"),
		TUIRuntimeTabs:            runtimePreview.CommandView && strings.Contains(runtimePreview.View, "Tasks") && strings.Contains(runtimePreview.View, "Teams") && strings.Contains(runtimePreview.View, "Schedules") && strings.Contains(runtimePreview.View, "Agent runs") && strings.Contains(runtimePreview.View, "@reviewer") && strings.Contains(runtimePreview.View, "R refresh"),
		TUIConversationTabs:       conversationPreview.CommandView && strings.Contains(conversationPreview.View, "History") && strings.Contains(conversationPreview.View, "Sessions") && strings.Contains(conversationPreview.View, "Bookmarks") && strings.Contains(conversationPreview.View, "before-review") && strings.Contains(conversationPreview.View, "R refresh"),
		TUIMemorySelector:         memoryPreview.CommandView && strings.Contains(memoryPreview.View, "AGENTS.md") && strings.Contains(memoryPreview.View, "V view") && strings.Contains(memoryPreview.View, "R refresh"),
		TUIExportDialog:           exportPreview.ExportDialog && strings.Contains(exportPreview.View, "Enter filename") && exportPreview.Value == "session.md",
		TUITextInputDialog:        inputPreview.TextInputDialog && strings.Contains(inputPreview.View, "Enter a directory path") && strings.Contains(inputPreview.View, "Enter confirm"),
		TUIPreferencePanels:       preferencePreview.CommandView && strings.Contains(preferencePreview.View, "fast mode") && strings.Contains(preferencePreview.View, "> Disabled  current"),
		TUISideQuestionPanel:      sideQuestionPreview.InformationView && strings.Contains(sideQuestionPreview.View, "The fixture used the wrong path") && strings.Contains(sideQuestionPreview.View, "Enter/Space/Esc close"),
		TUIStatusBar:              strings.Contains(submitPreview.View, "Enter send") && strings.Contains(submitPreview.View, "Tab") && strings.Contains(submitPreview.View, "Esc"),
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
		!report.TUIFullScreenLayout ||
		!report.TUIInlineLayout ||
		!report.TUIDefaultInline ||
		!report.TUIResumePicker ||
		!report.TUIWorkspaceTrustPrompt ||
		!report.TUITranscriptViewport ||
		!report.TUILocalHelpPanel ||
		!report.TUISettingsTabs ||
		!report.TUIExtensionTabs ||
		!report.TUIRuntimeTabs ||
		!report.TUIConversationTabs ||
		!report.TUIMemorySelector ||
		!report.TUIExportDialog ||
		!report.TUITextInputDialog ||
		!report.TUIPreferencePanels ||
		!report.TUISideQuestionPanel ||
		!report.TUIStatusBar ||
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
