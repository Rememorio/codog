package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/bookmarks"
	"github.com/Rememorio/codog/internal/contextview"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/onboarding"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sessionsummary"
	"github.com/Rememorio/codog/internal/skills"
	localstatus "github.com/Rememorio/codog/internal/status"
	prompttemplates "github.com/Rememorio/codog/internal/templates"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/usage"
)

func configValidationStatusScenario() scenario {
	return scenario{
		name:     "config_validation_status_roundtrip",
		runLocal: configValidationStatusScenarioRunLocal,
	}
}

func editGlobLSScenario() scenario {
	return scenario{
		name:       "edit_glob_ls_roundtrip",
		permission: tools.PermissionWorkspace,
		turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{
				{
					ID:    "tool-1",
					Name:  "edit_file",
					Input: json.RawMessage(`{"path":"src/app.txt","old_string":"alpha","new_string":"beta"}`),
				},
				{
					ID:    "tool-2",
					Name:  "glob",
					Input: json.RawMessage(`{"pattern":"src/*.txt","limit":5}`),
				},
				{
					ID:    "tool-3",
					Name:  "ls",
					Input: json.RawMessage(`{"path":"src","limit":5}`),
				},
			}},
			{Text: "edit glob ls harness ok"},
		},
		prompt: "edit and inspect files",
		setup:  editGlobLSScenarioSetup,
		verify: editGlobLSScenarioVerify,
	}
}

func multiEditApplyPatchScenario() scenario {
	return scenario{
		name:       "multi_edit_apply_patch_roundtrip",
		permission: tools.PermissionWorkspace,
		turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{
				{
					ID:    "tool-1",
					Name:  "multi_edit",
					Input: json.RawMessage(`{"path":"src/app.txt","edits":[{"old_string":"title: alpha","new_string":"title: beta"},{"old_string":"count = 1","new_string":"count = 2"}]}`),
				},
				{
					ID:    "tool-2",
					Name:  "apply_patch",
					Input: json.RawMessage(`{"patch":"--- a/src/app.txt\n+++ b/src/app.txt\n@@ -1,3 +1,4 @@\n title: beta\n count = 2\n status: draft\n+status_detail: patched"}`),
				},
			}},
			{Text: "multi edit apply patch harness ok"},
		},
		prompt: "perform multi edit and patch",
		setup:  multiEditApplyPatchScenarioSetup,
		verify: multiEditApplyPatchScenarioVerify,
	}
}

func commandSkillTemplateScenario() scenario {
	return scenario{
		name:     "command_skill_template_roundtrip",
		runLocal: commandSkillTemplateScenarioRunLocal,
	}
}

func skillActivationScenario() scenario {
	return scenario{
		name:     "skill_activation_roundtrip",
		runLocal: skillActivationScenarioRunLocal,
	}
}

type skillActivationHarnessReport struct {
	Kind                string   `json:"kind"`
	Action              string   `json:"action"`
	Status              string   `json:"status"`
	Target              string   `json:"target,omitempty"`
	Path                string   `json:"path,omitempty"`
	EnabledSkills       []string `json:"enabled_skills"`
	Added               []string `json:"added,omitempty"`
	Removed             []string `json:"removed,omitempty"`
	Unchanged           []string `json:"unchanged,omitempty"`
	AvailableSkillCount int      `json:"available_skill_count,omitempty"`
	ResolvedSkills      []string `json:"resolved_skills,omitempty"`
	MissingSkills       []string `json:"missing_skills,omitempty"`
	Message             string   `json:"message,omitempty"`
}

func decodeSkillActivationHarnessReport(output string) (skillActivationHarnessReport, error) {
	var report skillActivationHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return report, err
	}
	return report, nil
}

func onboardingBookmarksScenario() scenario {
	return scenario{
		name:     "onboarding_bookmarks_roundtrip",
		runLocal: onboardingBookmarksScenarioRunLocal,
	}
}

func memoryLifecycleScenario() scenario {
	return scenario{
		name:     "memory_lifecycle_roundtrip",
		runLocal: memoryLifecycleScenarioRunLocal,
	}
}

func promptDirectoryReferenceScenario() scenario {
	return scenario{
		name:     "prompt_directory_reference_roundtrip",
		runLocal: promptDirectoryReferenceScenarioRunLocal,
	}
}

func sessionSummaryScenario() scenario {
	return scenario{
		name:     "session_summary_roundtrip",
		runLocal: sessionSummaryScenarioRunLocal,
	}
}

func contextViewScenario() scenario {
	return scenario{
		name:     "context_view_roundtrip",
		runLocal: contextViewScenarioRunLocal,
	}
}

func themeLifecycleScenario() scenario {
	return scenario{
		name:     "theme_lifecycle_roundtrip",
		runLocal: themeLifecycleScenarioRunLocal,
	}
}

type themeHarnessReport struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Theme     string   `json:"theme"`
	Previous  string   `json:"previous,omitempty"`
	Path      string   `json:"path,omitempty"`
	Available []string `json:"available"`
}

func decodeThemeHarnessReport(output string) (themeHarnessReport, error) {
	var report themeHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return themeHarnessReport{}, err
	}
	return report, nil
}

func interfacePreferencesScenario() scenario {
	return scenario{
		name:     "interface_preferences_roundtrip",
		runLocal: interfacePreferencesScenarioRunLocal,
	}
}

type languageHarnessReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Configured bool   `json:"configured"`
	Language   string `json:"language"`
	Previous   string `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
}

func decodeLanguageHarnessReport(output string) (languageHarnessReport, error) {
	var report languageHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return languageHarnessReport{}, err
	}
	return report, nil
}

type vimHarnessReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	EditorMode string `json:"editor_mode"`
	Previous   string `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
}

func decodeVimHarnessReport(output string) (vimHarnessReport, error) {
	var report vimHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return vimHarnessReport{}, err
	}
	return report, nil
}

func privacyKeybindingsScenario() scenario {
	return scenario{
		name:     "privacy_keybindings_roundtrip",
		runLocal: privacyKeybindingsScenarioRunLocal,
	}
}

type privacyHarnessReport struct {
	Kind     string          `json:"kind"`
	Action   string          `json:"action"`
	Status   string          `json:"status"`
	Settings map[string]bool `json:"settings"`
	Key      string          `json:"key,omitempty"`
	Value    *bool           `json:"value,omitempty"`
	Path     string          `json:"path,omitempty"`
}

func decodePrivacyHarnessReport(output string) (privacyHarnessReport, error) {
	var report privacyHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return privacyHarnessReport{}, err
	}
	return report, nil
}

type keybindingsHarnessReport struct {
	Kind              string                              `json:"kind"`
	Action            string                              `json:"action"`
	Status            string                              `json:"status"`
	EditorMode        string                              `json:"editor_mode"`
	VimMode           bool                                `json:"vim_mode"`
	KeybindingsPath   string                              `json:"keybindings_path,omitempty"`
	KeybindingsExists bool                                `json:"keybindings_exists"`
	UserBindings      *keybindingsValidationHarnessReport `json:"user_bindings,omitempty"`
	Sections          []keybindingsSectionHarnessReport   `json:"sections,omitempty"`
}

type keybindingsFileHarnessReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Exists  bool   `json:"exists"`
	Opened  bool   `json:"opened,omitempty"`
}

type keybindingsValidationHarnessReport struct {
	Kind         string                            `json:"kind"`
	Action       string                            `json:"action"`
	Status       string                            `json:"status"`
	Path         string                            `json:"path"`
	Exists       bool                              `json:"exists"`
	Valid        bool                              `json:"valid"`
	ContextCount int                               `json:"context_count"`
	BindingCount int                               `json:"binding_count"`
	Errors       []string                          `json:"errors,omitempty"`
	Sections     []keybindingsSectionHarnessReport `json:"sections,omitempty"`
}

type keybindingsSectionHarnessReport struct {
	Name     string                          `json:"name"`
	Entries  []keybindingsEntryHarnessReport `json:"entries"`
	Disabled bool                            `json:"disabled,omitempty"`
}

type keybindingsEntryHarnessReport struct {
	Key           string `json:"key"`
	NormalizedKey string `json:"normalized_key,omitempty"`
	Action        string `json:"action"`
	Mode          string `json:"mode,omitempty"`
	Description   string `json:"description,omitempty"`
}

type keybindingsResolveHarnessReport struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	Context       string   `json:"context"`
	Key           string   `json:"key"`
	NormalizedKey string   `json:"normalized_key"`
	Found         bool     `json:"found"`
	Source        string   `json:"source,omitempty"`
	BindingAction string   `json:"binding_action,omitempty"`
	Section       string   `json:"section,omitempty"`
	Disabled      bool     `json:"disabled,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

func decodeKeybindingsHarnessReport(output string) (keybindingsHarnessReport, error) {
	var report keybindingsHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return keybindingsHarnessReport{}, err
	}
	return report, nil
}

func decodeKeybindingsFileHarnessReport(output string) (keybindingsFileHarnessReport, error) {
	var report keybindingsFileHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return keybindingsFileHarnessReport{}, err
	}
	return report, nil
}

func decodeKeybindingsValidationHarnessReport(output string) (keybindingsValidationHarnessReport, error) {
	var report keybindingsValidationHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return keybindingsValidationHarnessReport{}, err
	}
	return report, nil
}

func decodeKeybindingsResolveHarnessReport(output string) (keybindingsResolveHarnessReport, error) {
	var report keybindingsResolveHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return keybindingsResolveHarnessReport{}, err
	}
	return report, nil
}

func browserNotificationsScenario() scenario {
	return scenario{
		name:     "browser_notifications_roundtrip",
		runLocal: browserNotificationsScenarioRunLocal,
	}
}

func configValidationStatusScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configPath := filepath.Join(workspace, "config-warning.json")
	if err := os.WriteFile(configPath, []byte(`{"model":"claude-status","permission_mode":"workspace-write","modle":"typo"}`), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	var snapshot localstatus.Snapshot
	statusOut, err := decodeHarnessOutput(&snapshot, func() (string, error) {
		return runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "status")
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if snapshot.ConfigValidation.Status != "warning" ||
		snapshot.ConfigValidation.FileCount != 1 ||
		snapshot.ConfigValidation.PresentCount != 1 ||
		snapshot.ConfigValidation.ErrorCount != 0 ||
		snapshot.ConfigValidation.WarningCount != 1 {
		return localScenarioResult{}, fmt.Errorf("unexpected config validation summary: %#v", snapshot.ConfigValidation)
	}
	if len(snapshot.ConfigValidation.Paths) != 1 || snapshot.ConfigValidation.Paths[0] != configPath {
		return localScenarioResult{}, fmt.Errorf("unexpected config validation paths: %v", snapshot.ConfigValidation.Paths)
	}
	textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "status")
	if err != nil {
		return localScenarioResult{}, err
	}
	expected := "Config validation status=warning files=1 present=1 errors=0 warnings=1"
	if !strings.Contains(textOut, expected) {
		return localScenarioResult{}, fmt.Errorf("missing status text config validation summary %q in %s", expected, textOut)
	}
	return localScenarioResult{
		Output:       statusOut,
		FinalMessage: "config validation status harness ok",
	}, nil
}

func editGlobLSScenarioSetup(workspace string) error {
	srcDir := filepath.Join(workspace, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(srcDir, "app.txt"), []byte("alpha\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(srcDir, "notes.md"), []byte("notes\n"), 0o644)
}

func editGlobLSScenarioVerify(workspace string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "edit glob ls harness ok") {
		return fmt.Errorf("missing edit/glob/ls final response")
	}
	if err := expectToolCalls(result, 3, false); err != nil {
		return err
	}
	edited, err := os.ReadFile(filepath.Join(workspace, "src", "app.txt"))
	if err != nil {
		return err
	}
	if string(edited) != "beta\n" {
		return fmt.Errorf("edit_file did not persist edit, got %q", string(edited))
	}
	outputs := map[string]string{}
	for _, call := range result.ToolCalls {
		outputs[call.Name] = call.Output
	}
	for _, expected := range []string{`"replacements": 1`, `"oldString": "alpha"`, `"newString": "beta"`} {
		if !strings.Contains(outputs["edit_file"], expected) {
			return fmt.Errorf("edit_file output missing %s: %s", expected, outputs["edit_file"])
		}
	}
	for _, expected := range []string{`"files":`, `"filenames":`, `"numFiles": 1`, "src/app.txt"} {
		if !strings.Contains(outputs["glob"], expected) {
			return fmt.Errorf("glob output missing %s: %s", expected, outputs["glob"])
		}
	}
	for _, expected := range []string{`"kind": "ls"`, `"name": "app.txt"`, `"type": "file"`} {
		if !strings.Contains(outputs["ls"], expected) {
			return fmt.Errorf("ls output missing %s: %s", expected, outputs["ls"])
		}
	}
	return nil
}

func multiEditApplyPatchScenarioSetup(workspace string) error {
	srcDir := filepath.Join(workspace, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(srcDir, "app.txt"), []byte("title: alpha\ncount = 1\nstatus: draft\n"), 0o644)
}

func multiEditApplyPatchScenarioVerify(workspace string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "multi edit apply patch harness ok") {
		return fmt.Errorf("missing multi_edit/apply_patch final response")
	}
	if err := expectToolCalls(result, 2, false); err != nil {
		return err
	}
	updated, err := os.ReadFile(filepath.Join(workspace, "src", "app.txt"))
	if err != nil {
		return err
	}
	want := "title: beta\ncount = 2\nstatus: draft\nstatus_detail: patched\n"
	if string(updated) != want {
		return fmt.Errorf("unexpected patched file content: %q", string(updated))
	}
	outputs := map[string]string{}
	for _, call := range result.ToolCalls {
		outputs[call.Name] = call.Output
	}
	for _, expected := range []string{`"edits": 2`, `"replacements": 2`, `"undo_available": true`} {
		if !strings.Contains(outputs["multi_edit"], expected) {
			return fmt.Errorf("multi_edit output missing %s: %s", expected, outputs["multi_edit"])
		}
	}
	for _, expected := range []string{`"kind": "apply_patch"`, `"files_changed": 1`, `"operation": "update"`, `"path": "src/app.txt"`} {
		if !strings.Contains(outputs["apply_patch"], expected) {
			return fmt.Errorf("apply_patch output missing %s: %s", expected, outputs["apply_patch"])
		}
	}
	return nil
}

func commandSkillTemplateScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	commandDir := filepath.Join(workspace, ".codog", "commands")
	skillDir := filepath.Join(workspace, ".codog", "skills", "review")
	templateDir := filepath.Join(workspace, ".codog", "templates")
	for _, dir := range []string{commandDir, skillDir, templateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return localScenarioResult{}, err
		}
	}

	commandDoc := `---
description: Review a target file.
argument-hint: TARGET
allowed-tools: read_file, grep
arguments: TARGET
---
Review $TARGET for session ${CLAUDE_SESSION_ID}.`
	if err := os.WriteFile(filepath.Join(commandDir, "review.md"), []byte(commandDoc), 0o644); err != nil {
		return localScenarioResult{}, err
	}

	skillDoc := `---
name: review
description: Review project changes.
allowed-tools: read_file, grep
arguments: TARGET
paths: src/**, docs
---
Review skill body for $TARGET during ${CLAUDE_SESSION_ID}.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
		return localScenarioResult{}, err
	}

	templateDoc := `Release {{version}} for {{project}}.`
	if err := os.WriteFile(filepath.Join(templateDir, "release.md"), []byte(templateDoc), 0o644); err != nil {
		return localScenarioResult{}, err
	}

	command, err := customcommands.Find(configHome, workspace, "review")
	if err != nil {
		return localScenarioResult{}, err
	}
	renderedCommand := customcommands.RenderWithSession(command, "src/main.go", "session-123")
	if renderedCommand.Source != "workspace" {
		return localScenarioResult{}, fmt.Errorf("unexpected command source %q", renderedCommand.Source)
	}
	if renderedCommand.Rendered != "Review src/main.go for session session-123." {
		return localScenarioResult{}, fmt.Errorf("unexpected rendered command: %s", renderedCommand.Rendered)
	}
	for _, expected := range []string{"read_file", "grep"} {
		if !slices.Contains(renderedCommand.AllowedTools, expected) {
			return localScenarioResult{}, fmt.Errorf("command allowed tools missing %s: %v", expected, renderedCommand.AllowedTools)
		}
	}

	skill, err := skills.Find(configHome, workspace, "review")
	if err != nil {
		return localScenarioResult{}, err
	}
	renderedSkill := skills.RenderInvocationWithSession(skill, "src/main.go", "session-123")
	for _, expected := range []string{`<skill name="review"`, "Review skill body for src/main.go during session-123.", "User request: src/main.go"} {
		if !strings.Contains(renderedSkill, expected) {
			return localScenarioResult{}, fmt.Errorf("rendered skill missing %s", expected)
		}
	}
	if !skills.MatchesAnyPath(skill, []string{"src/main.go"}) {
		return localScenarioResult{}, fmt.Errorf("skill paths did not match src/main.go")
	}
	if skills.MatchesAnyPath(skill, []string{"test/main.go"}) {
		return localScenarioResult{}, fmt.Errorf("skill paths unexpectedly matched test/main.go")
	}

	template, err := prompttemplates.Find(configHome, workspace, "release")
	if err != nil {
		return localScenarioResult{}, err
	}
	renderedTemplate, err := prompttemplates.Render(template, map[string]string{
		"project": "codog",
		"version": "1.0.0",
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if renderedTemplate.Rendered != "Release 1.0.0 for codog." {
		return localScenarioResult{}, fmt.Errorf("unexpected rendered template: %s", renderedTemplate.Rendered)
	}

	report := map[string]any{
		"kind": "command_skill_template",
		"command": map[string]any{
			"name":          renderedCommand.Name,
			"source":        renderedCommand.Source,
			"allowed_tools": renderedCommand.AllowedTools,
			"rendered":      renderedCommand.Rendered,
		},
		"skill": map[string]any{
			"name":          skill.Name,
			"source":        skill.Source,
			"allowed_tools": skill.AllowedTools,
			"paths":         skill.Paths,
			"matches_src":   skills.MatchesAnyPath(skill, []string{"src/main.go"}),
		},
		"template": map[string]any{
			"name":     renderedTemplate.Name,
			"source":   renderedTemplate.Source,
			"rendered": renderedTemplate.Rendered,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "command skill template harness ok",
		RequestCount: 3,
		MessageCount: 1,
	}, nil
}

func skillActivationScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	skillDir := filepath.Join(workspace, ".codog", "skills", "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	skillDoc := `---
name: review
description: Review project changes.
allowed-tools: read_file, grep
---
# Review

Review the requested change with repository context.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	configPath := filepath.Join(workspace, "codog-config.json")
	configData, err := json.Marshal(map[string]any{"config_home": configHome})
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		return localScenarioResult{}, err
	}

	initialOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "skills", "status")
	if err != nil {
		return localScenarioResult{}, err
	}
	initial, err := decodeSkillActivationHarnessReport(initialOut)
	if err != nil {
		return localScenarioResult{}, err
	}
	if initial.Kind != "skills" || initial.Action != "status" || initial.Status != "ok" || len(initial.EnabledSkills) != 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected initial skills status: %#v", initial)
	}
	if initial.AvailableSkillCount == 0 {
		return localScenarioResult{}, fmt.Errorf("expected at least one available skill in initial status")
	}

	enableOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "skills", "enable", "review", "--path", configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	enabled, err := decodeSkillActivationHarnessReport(enableOut)
	if err != nil {
		return localScenarioResult{}, err
	}
	if enabled.Action != "enable" || !slices.Contains(enabled.EnabledSkills, "review") || !slices.Contains(enabled.Added, "review") {
		return localScenarioResult{}, fmt.Errorf("unexpected skills enable report: %#v", enabled)
	}
	if enabled.Path == "" || !strings.HasSuffix(enabled.Path, "codog-config.json") {
		return localScenarioResult{}, fmt.Errorf("unexpected skills enable path: %q", enabled.Path)
	}
	configData, err = os.ReadFile(configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(string(configData), `"enabled_skills":`, `"review"`) {
		return localScenarioResult{}, fmt.Errorf("enabled skills config did not persist review: %s", string(configData))
	}

	statusOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "skills", "status")
	if err != nil {
		return localScenarioResult{}, err
	}
	status, err := decodeSkillActivationHarnessReport(statusOut)
	if err != nil {
		return localScenarioResult{}, err
	}
	if status.Action != "status" || status.Status != "ok" || !slices.Contains(status.EnabledSkills, "review") || !slices.Contains(status.ResolvedSkills, "review") || len(status.MissingSkills) != 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected persisted skills status: %#v", status)
	}

	textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "skills", "status")
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(textOut, "Skill Status", "Enabled skills   review", "All enabled skills resolved.") {
		return localScenarioResult{}, fmt.Errorf("skills status text missing expected values: %s", textOut)
	}

	disableOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "skills", "disable", "review", "--path", configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	disabled, err := decodeSkillActivationHarnessReport(disableOut)
	if err != nil {
		return localScenarioResult{}, err
	}
	if disabled.Action != "disable" || len(disabled.EnabledSkills) != 0 || !slices.Contains(disabled.Removed, "review") {
		return localScenarioResult{}, fmt.Errorf("unexpected skills disable report: %#v", disabled)
	}
	configData, err = os.ReadFile(configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	if strings.Contains(string(configData), `"enabled_skills"`) {
		return localScenarioResult{}, fmt.Errorf("enabled skills config still present after disable: %s", string(configData))
	}

	report := map[string]any{
		"kind": "skill_activation",
		"skills": map[string]any{
			"available":        initial.AvailableSkillCount,
			"enabled":          enabled.EnabledSkills,
			"added":            enabled.Added,
			"resolved":         status.ResolvedSkills,
			"missing":          status.MissingSkills,
			"removed":          disabled.Removed,
			"final_enabled":    disabled.EnabledSkills,
			"path_persisted":   enabled.Path != "" && strings.HasSuffix(enabled.Path, "codog-config.json"),
			"text_rendered":    strings.Contains(textOut, "Enabled skills   review"),
			"config_unset":     !strings.Contains(string(configData), `"enabled_skills"`),
			"status_message":   status.Message,
			"disabled_message": disabled.Message,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "skill activation harness ok",
		RequestCount: 5,
		MessageCount: 1,
	}, nil
}

func onboardingBookmarksScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/onboarding\n\ngo 1.25\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "main_test.go"), []byte("package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) {}\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use focused changes.\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		return localScenarioResult{}, err
	}

	onboardingReport, err := onboarding.Analyze(onboarding.Options{Workspace: workspace})
	if err != nil {
		return localScenarioResult{}, err
	}
	if onboardingReport.Status != "needs_setup" || !onboardingReport.HasReadme || !onboardingReport.HasTests || onboardingReport.PrimaryLanguage != "Go" {
		return localScenarioResult{}, fmt.Errorf("unexpected onboarding report: %#v", onboardingReport)
	}
	if !onboardingReport.GitRepository || !slices.Contains(onboardingReport.ReadmeFiles, "README.md") || !slices.Contains(onboardingReport.InstructionFiles, "AGENTS.md") {
		return localScenarioResult{}, fmt.Errorf("onboarding report missed repository files: %#v", onboardingReport)
	}
	if len(onboardingReport.Recommendations) == 0 {
		return localScenarioResult{}, fmt.Errorf("expected onboarding recommendation for missing codog config")
	}

	configHome := filepath.Join(workspace, "config-home")
	store := bookmarks.NewStore(configHome)
	messageIndex := 2
	created, err := store.Add(bookmarks.Bookmark{
		Name:         "review-start",
		Workspace:    workspace,
		SessionID:    "session-abc",
		MessageIndex: &messageIndex,
		PRRepo:       "Rememorio/codog",
		PRNumber:     42,
		Note:         "resume review",
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if created.ID == "" || created.Name != "review-start" || created.SessionID != "session-abc" {
		return localScenarioResult{}, fmt.Errorf("unexpected created bookmark: %#v", created)
	}
	listed, err := store.List(bookmarks.ListOptions{Workspace: workspace})
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		return localScenarioResult{}, fmt.Errorf("unexpected bookmark list: %#v", listed)
	}
	shown, err := store.Get("review-start")
	if err != nil {
		return localScenarioResult{}, err
	}
	if shown.ID != created.ID || shown.PRNumber != 42 || shown.Note != "resume review" {
		return localScenarioResult{}, fmt.Errorf("unexpected shown bookmark: %#v", shown)
	}
	deleted, err := store.Delete(created.ID)
	if err != nil {
		return localScenarioResult{}, err
	}
	if deleted.ID != created.ID {
		return localScenarioResult{}, fmt.Errorf("unexpected deleted bookmark: %#v", deleted)
	}
	remaining, err := store.List(bookmarks.ListOptions{Workspace: workspace, All: true})
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(remaining) != 0 {
		return localScenarioResult{}, fmt.Errorf("expected no bookmarks after delete, got %#v", remaining)
	}

	report := map[string]any{
		"kind": "onboarding_bookmarks",
		"onboarding": map[string]any{
			"status":           onboardingReport.Status,
			"primary_language": onboardingReport.PrimaryLanguage,
			"has_readme":       onboardingReport.HasReadme,
			"has_tests":        onboardingReport.HasTests,
			"git_repository":   onboardingReport.GitRepository,
			"recommendations":  len(onboardingReport.Recommendations),
		},
		"bookmarks": map[string]any{
			"created":         created.Name,
			"listed":          len(listed),
			"shown":           shown.Name,
			"deleted":         deleted.Name,
			"remaining":       len(remaining),
			"message_index":   messageIndex,
			"pull_request":    shown.PRNumber,
			"config_home_set": configHome != "",
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "onboarding bookmarks harness ok",
		RequestCount: 2,
		MessageCount: 1,
	}, nil
}

func memoryLifecycleScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Prefer focused tests.\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	initial, err := memory.BuildReport(workspace)
	if err != nil {
		return localScenarioResult{}, err
	}
	if initial.Kind != "memory" || initial.Action != "list" || initial.InstructionFiles != 1 || initial.Files[0].Name != "AGENTS.md" {
		return localScenarioResult{}, fmt.Errorf("unexpected initial memory report: %#v", initial)
	}

	appendReport, err := memory.Append(workspace, "Remember to cite verification commands.")
	if err != nil {
		return localScenarioResult{}, err
	}
	if appendReport.Kind != "memory" || appendReport.Action != "add" || appendReport.Bytes == 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected memory append report: %#v", appendReport)
	}

	search, err := memory.Search(workspace, "verification commands", 5)
	if err != nil {
		return localScenarioResult{}, err
	}
	if search.MatchCount != 1 || len(search.Matches) != 1 || search.Matches[0].Name != "AGENTS.md" {
		return localScenarioResult{}, fmt.Errorf("unexpected memory search report: %#v", search)
	}

	show, err := memory.Show(workspace, "AGENTS.md")
	if err != nil {
		return localScenarioResult{}, err
	}
	if show.File.Name != "AGENTS.md" || !strings.Contains(show.Body, "Remember to cite verification commands.") {
		return localScenarioResult{}, fmt.Errorf("unexpected memory show report: %#v", show)
	}

	selection, err := memory.Select(workspace, ".codog/instructions.md")
	if err != nil {
		return localScenarioResult{}, err
	}
	if selection.Kind != "memory" || selection.OptionCount < 2 || !strings.HasSuffix(filepath.ToSlash(selection.Selected), ".codog/instructions.md") {
		return localScenarioResult{}, fmt.Errorf("unexpected memory selection report: %#v", selection)
	}

	ensured, err := memory.Ensure(workspace, ".codog/instructions.md")
	if err != nil {
		return localScenarioResult{}, err
	}
	if ensured.Kind != "memory" || ensured.Action != "ensure" || !ensured.Created {
		return localScenarioResult{}, fmt.Errorf("unexpected memory ensure report: %#v", ensured)
	}

	reset, err := memory.Reset(workspace, memory.ResetOptions{Target: "AGENTS.md", Confirm: true})
	if err != nil {
		return localScenarioResult{}, err
	}
	if reset.Kind != "memory" || reset.ResetCount != 1 || reset.Files[0].Name != "AGENTS.md" || reset.Files[0].BytesRemoved == 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected memory reset report: %#v", reset)
	}
	cleared, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(cleared) != 0 {
		return localScenarioResult{}, fmt.Errorf("expected AGENTS.md to be reset, got %q", string(cleared))
	}

	report := map[string]any{
		"kind": "memory_lifecycle",
		"list": map[string]any{
			"instruction_files": initial.InstructionFiles,
			"name":              initial.Files[0].Name,
		},
		"append": map[string]any{
			"bytes": appendReport.Bytes,
			"path":  filepath.ToSlash(appendReport.Path),
		},
		"search": map[string]any{
			"query":       search.Query,
			"match_count": search.MatchCount,
			"line":        search.Matches[0].Line,
		},
		"show": map[string]any{
			"name":     show.File.Name,
			"contains": strings.Contains(show.Body, "Remember to cite verification commands."),
		},
		"select": map[string]any{
			"option_count": selection.OptionCount,
			"selected":     filepath.ToSlash(selection.Selected),
		},
		"ensure": map[string]any{
			"created": ensured.Created,
			"path":    filepath.ToSlash(ensured.Path),
		},
		"reset": map[string]any{
			"reset_count":   reset.ResetCount,
			"bytes_removed": reset.Files[0].BytesRemoved,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "memory lifecycle harness ok",
		RequestCount: 6,
		MessageCount: 1,
	}, nil
}

func promptDirectoryReferenceScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	captured := make(chan json.RawMessage, 1)
	server := httptest.NewServer(mockanthropic.Server{
		Text: "directory reference harness ok",
		OnRequest: func(raw json.RawMessage) {
			select {
			case captured <- append(json.RawMessage(nil), raw...):
			default:
			}
		},
	}.Handler())
	defer server.Close()

	configHome := filepath.Join(workspace, "config-home")
	docsDir := filepath.Join(workspace, "docs", "nested")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "README.md"), []byte("# Reference Docs\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(docsDir, "guide.txt"), []byte("reference guide\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "binary.bin"), []byte{0xff, 0x00, 0x01}, 0o644); err != nil {
		return localScenarioResult{}, err
	}
	configPath := filepath.Join(workspace, "codog-config.json")
	configData, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		return localScenarioResult{}, err
	}

	var promptReport struct {
		Response string `json:"response"`
	}
	_, err = decodeHarnessOutput(&promptReport, func() (string, error) {
		return runHarnessCodog(ctx, workspace, "--config", configPath, "prompt", "Summarize @docs", "--output-format", "json")
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if promptReport.Response != "directory reference harness ok" {
		return localScenarioResult{}, fmt.Errorf("unexpected directory reference response: %q", promptReport.Response)
	}
	var raw json.RawMessage
	select {
	case raw = <-captured:
	default:
		return localScenarioResult{}, fmt.Errorf("expected provider request for directory reference")
	}
	var body struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return localScenarioResult{}, err
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Content) == 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected directory reference content: %s", string(raw))
	}
	text := body.Messages[0].Content[0].Text
	for _, expected := range []string{
		"Summarize @docs",
		"<codog_file_references>",
		`<directory path="docs" files="2"`,
		`<file path="README.md"`,
		"# Reference Docs",
		`<file path="nested/guide.txt"`,
		"reference guide",
		"<skipped>",
		"binary.bin",
	} {
		if !strings.Contains(text, expected) {
			return localScenarioResult{}, fmt.Errorf("directory reference missing %s: %s", expected, text)
		}
	}
	report := map[string]any{
		"kind": "prompt_directory_reference",
		"reference": map[string]any{
			"files":          2,
			"has_directory":  strings.Contains(text, `<directory path="docs"`),
			"has_nested":     strings.Contains(text, `nested/guide.txt`),
			"skipped_binary": strings.Contains(text, "binary.bin"),
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "directory reference harness ok",
		RequestCount: 1,
		MessageCount: 1,
	}, nil
}

func sessionSummaryScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	sessionPath := filepath.Join(workspace, ".codog", "sessions", "summary-session.jsonl")
	messages := []anthropic.Message{
		anthropic.TextMessage("user", "investigate failing tests in internal/runloop and keep the summary actionable"),
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type:  "tool_use",
				ID:    "tool-1",
				Name:  "bash",
				Input: json.RawMessage(`{"command":"go test ./internal/runloop"}`),
			}},
		},
		anthropic.ToolResultMessage("tool-1", "package internal/runloop failed", true),
		anthropic.TextMessage("assistant", "The failure is in internal/runloop compaction handling."),
		anthropic.TextMessage("user", "also check session resume summaries before the next edit"),
	}
	report := sessionsummary.Build("summary-session", sessionPath, "claude-summary", messages)
	if report.Kind != "summary" || report.Action != "show" || report.Status != "ok" {
		return localScenarioResult{}, fmt.Errorf("unexpected session summary identity: %#v", report)
	}
	if report.MessageCount != len(messages) || report.UserMessages != 3 || report.AssistantMessages != 2 {
		return localScenarioResult{}, fmt.Errorf("unexpected session summary counts: %#v", report)
	}
	if report.ToolUses != 1 || report.ToolResults != 1 || report.ToolErrors != 1 {
		return localScenarioResult{}, fmt.Errorf("unexpected session summary tool counts: %#v", report)
	}
	if report.FirstUser == nil || !strings.Contains(report.FirstUser.Text, "investigate failing tests") {
		return localScenarioResult{}, fmt.Errorf("unexpected first user preview: %#v", report.FirstUser)
	}
	if report.LastUser == nil || !strings.Contains(report.LastUser.Text, "session resume summaries") {
		return localScenarioResult{}, fmt.Errorf("unexpected last user preview: %#v", report.LastUser)
	}
	if report.LastAssistant == nil || !strings.Contains(report.LastAssistant.Text, "compaction handling") {
		return localScenarioResult{}, fmt.Errorf("unexpected last assistant preview: %#v", report.LastAssistant)
	}
	if report.TokenEstimate.TotalTokens <= 0 {
		return localScenarioResult{}, fmt.Errorf("missing token estimate: %#v", report.TokenEstimate)
	}

	var text bytes.Buffer
	sessionsummary.RenderText(&text, report)
	textOutput := text.String()
	for _, expected := range []string{"Summary", "Session          summary-session", "Tool use         calls=1 results=1 errors=1", "session resume summaries"} {
		if !strings.Contains(textOutput, expected) {
			return localScenarioResult{}, fmt.Errorf("session summary text missing %q: %s", expected, textOutput)
		}
	}

	compaction := sessionsummary.BuildCompactionSummary(messages, 2)
	for _, expected := range []string{
		"auto-compacted",
		"- Current work: also check session resume summaries before the next edit",
		"- Last assistant response: The failure is in internal/runloop compaction handling.",
		"- Tools mentioned: bash",
		"- Tool results: 1 result message(s), 1 error result(s).",
	} {
		if !strings.Contains(compaction.Summary, expected) {
			return localScenarioResult{}, fmt.Errorf("compaction summary missing %q: %s", expected, compaction.Summary)
		}
	}
	if compaction.OriginalLines == 0 || compaction.CompressedLines == 0 || compaction.CompressedChars == 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected compaction metrics: %#v", compaction)
	}

	output := map[string]any{
		"kind": "session_summary",
		"summary": map[string]any{
			"session_id":         report.SessionID,
			"message_count":      report.MessageCount,
			"user_messages":      report.UserMessages,
			"assistant_messages": report.AssistantMessages,
			"tool_uses":          report.ToolUses,
			"tool_results":       report.ToolResults,
			"tool_errors":        report.ToolErrors,
			"token_total":        report.TokenEstimate.TotalTokens,
			"text_rendered":      strings.Contains(textOutput, "Tool use         calls=1 results=1 errors=1"),
		},
		"compaction": map[string]any{
			"compressed_lines":       compaction.CompressedLines,
			"omitted_lines":          compaction.OmittedLines,
			"truncated":              compaction.Truncated,
			"has_current_work":       strings.Contains(compaction.Summary, "- Current work:"),
			"has_last_assistant":     strings.Contains(compaction.Summary, "- Last assistant response:"),
			"has_tool_summary":       strings.Contains(compaction.Summary, "- Tools mentioned: bash"),
			"has_tool_result_counts": strings.Contains(compaction.Summary, "- Tool results: 1 result message(s), 1 error result(s)."),
		},
	}
	data, err := json.Marshal(output)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "session summary harness ok",
		RequestCount: 2,
		MessageCount: 1,
	}, nil
}

func contextViewScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	notesPath := filepath.Join(workspace, "notes.md")
	if err := os.WriteFile(notesPath, []byte("review context\nkeep tests focused\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	agentsPath := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("Prefer focused tests.\nMention validation.\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}

	focusReport, err := focus.Add(workspace, []string{"notes.md"})
	if err != nil {
		return localScenarioResult{}, err
	}
	if focusReport.Kind != "focus" || focusReport.Action != "add" || focusReport.Total != 1 {
		return localScenarioResult{}, fmt.Errorf("unexpected focus report: %#v", focusReport)
	}

	statusReport := localstatus.Build(localstatus.Options{
		Version:               "dev",
		Workspace:             workspace,
		Model:                 "claude-test",
		PermissionMode:        "workspace-write",
		MaxTokens:             4096,
		MaxTurns:              8,
		AuthConfigured:        true,
		PlanActive:            true,
		PlanText:              "review context before edits",
		ToolNames:             []string{"bash", "read_file", "write_file"},
		SessionID:             "session-context",
		SessionMessages:       4,
		GitStatus:             "## main\n M notes.md\n",
		EnabledSkillCount:     2,
		SetupHookCount:        1,
		PreHookCount:          1,
		PostHookCount:         1,
		MemoryFiles:           []localstatus.MemoryFileStatus{{Path: "AGENTS.md", Name: "AGENTS.md", Scope: "workspace", Chars: 40, Lines: 2}},
		AllowedToolSource:     "default",
		AllowedToolEntries:    []string{"read_file", "write_file"},
		SandboxOS:             "darwin",
		SandboxDefault:        "seatbelt",
		SandboxAvailable:      true,
		RuntimeProvider:       "anthropic",
		RuntimeProviderSource: "config",
	})
	memoryReport := memory.Report{
		Kind:             "memory",
		Action:           "list",
		Status:           "ok",
		WorkingDirectory: workspace,
		InstructionFiles: 1,
		Files: []memory.Summary{{
			Path:      "AGENTS.md",
			Name:      "AGENTS.md",
			Scope:     "workspace",
			Lines:     2,
			Words:     5,
			Chars:     40,
			SizeBytes: 40,
			Preview:   "Prefer focused tests.",
		}},
	}
	contextReport := contextview.Build(contextview.Options{
		Status:       statusReport,
		Memory:       memoryReport,
		Focus:        focusReport,
		TokenUsage:   usage.Summary{InputTokens: 120, OutputTokens: 30, TotalTokens: 150, EstimatedUSD: 0.00042, Source: "actual"},
		SystemPrompt: "system line one\nsystem line two",
		Warnings:     []string{"context budget near threshold"},
	})
	if contextReport.Kind != "context" || contextReport.Action != "show" || contextReport.Status != "degraded" {
		return localScenarioResult{}, fmt.Errorf("unexpected context report identity: %#v", contextReport)
	}
	if contextReport.Memory.InstructionFiles != 1 || contextReport.Focus.FocusedPaths != 1 || contextReport.Prompt.Lines != 2 {
		return localScenarioResult{}, fmt.Errorf("unexpected context counts: %#v", contextReport)
	}
	for _, expected := range []string{
		"context budget near threshold",
		"git working tree has local changes",
		"plan mode is active; tool permissions are read-only",
	} {
		if !slices.Contains(contextReport.Signals, expected) {
			return localScenarioResult{}, fmt.Errorf("context report missing signal %q: %#v", expected, contextReport.Signals)
		}
	}

	var text bytes.Buffer
	contextview.RenderText(&text, contextReport)
	textOutput := text.String()
	for _, expected := range []string{"Context", "Plan             active", "Memory files     1", "Focused paths    1", "notes.md"} {
		if !strings.Contains(textOutput, expected) {
			return localScenarioResult{}, fmt.Errorf("context text missing %q: %s", expected, textOutput)
		}
	}
	htmlOutput := contextview.RenderHTML(contextReport)
	for _, expected := range []string{"<!doctype html>", "Codog Context", "Estimated tokens", "context budget near threshold"} {
		if !strings.Contains(htmlOutput, expected) {
			return localScenarioResult{}, fmt.Errorf("context html missing %q", expected)
		}
	}

	report := map[string]any{
		"kind": "context_view",
		"context": map[string]any{
			"status":            contextReport.Status,
			"workspace_name":    contextReport.Workspace.Name,
			"memory_files":      contextReport.Memory.InstructionFiles,
			"focused_paths":     contextReport.Focus.FocusedPaths,
			"prompt_lines":      contextReport.Prompt.Lines,
			"signals":           len(contextReport.Signals),
			"plan_active":       contextReport.Plan.Active,
			"token_total":       contextReport.TokenEstimate.TotalTokens,
			"text_rendered":     strings.Contains(textOutput, "Focused paths    1"),
			"html_rendered":     strings.Contains(htmlOutput, "Codog Context"),
			"git_dirty_signal":  slices.Contains(contextReport.Signals, "git working tree has local changes"),
			"plan_mode_signal":  slices.Contains(contextReport.Signals, "plan mode is active; tool permissions are read-only"),
			"warning_preserved": slices.Contains(contextReport.Signals, "context budget near threshold"),
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "context view harness ok",
		RequestCount: 4,
		MessageCount: 1,
	}, nil
}

func themeLifecycleScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configPath, err := createHarnessConfigFile(workspace, ".codog-home", nil)
	if err != nil {
		return localScenarioResult{}, err
	}
	initialOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "theme")
	if err != nil {
		return localScenarioResult{}, err
	}
	initial, err := decodeThemeHarnessReport(initialOut)
	if err != nil {
		return localScenarioResult{}, err
	}
	if initial.Kind != "theme" || initial.Action != "status" || initial.Theme != "default" {
		return localScenarioResult{}, fmt.Errorf("unexpected initial theme report: %#v", initial)
	}
	if !slices.Contains(initial.Available, "dark") || !slices.Contains(initial.Available, "light") {
		return localScenarioResult{}, fmt.Errorf("theme list missing expected values: %#v", initial.Available)
	}

	setOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "theme", "set", "dark", "--path", configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	setReport, err := decodeThemeHarnessReport(setOut)
	if err != nil {
		return localScenarioResult{}, err
	}
	if setReport.Action != "set" || setReport.Theme != "dark" || setReport.Previous != "default" {
		return localScenarioResult{}, fmt.Errorf("unexpected set theme report: %#v", setReport)
	}
	if err := verifyHarnessFileContainsAny(configPath, `"theme": "dark"`, `"theme":"dark"`); err != nil {
		return localScenarioResult{}, err
	}

	statusOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "theme")
	if err != nil {
		return localScenarioResult{}, err
	}
	statusReport, err := decodeThemeHarnessReport(statusOut)
	if err != nil {
		return localScenarioResult{}, err
	}
	if statusReport.Action != "status" || statusReport.Theme != "dark" {
		return localScenarioResult{}, fmt.Errorf("unexpected persisted theme status: %#v", statusReport)
	}

	textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "theme", "list")
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(textOut, "Theme", "Available", "dark") {
		return localScenarioResult{}, fmt.Errorf("theme text output missing expected values: %s", textOut)
	}

	clearOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "theme", "clear", "--path", configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	clearReport, err := decodeThemeHarnessReport(clearOut)
	if err != nil {
		return localScenarioResult{}, err
	}
	if clearReport.Action != "clear" || clearReport.Theme != "default" || clearReport.Previous != "dark" {
		return localScenarioResult{}, fmt.Errorf("unexpected clear theme report: %#v", clearReport)
	}
	if err := verifyHarnessFileOmits(configPath, `"theme"`); err != nil {
		return localScenarioResult{}, err
	}

	report := map[string]any{
		"kind": "theme_lifecycle",
		"theme": map[string]any{
			"initial":        initial.Theme,
			"set":            setReport.Theme,
			"previous":       setReport.Previous,
			"status":         statusReport.Theme,
			"cleared":        clearReport.Theme,
			"clear_previous": clearReport.Previous,
			"path_persisted": setReport.Path != "" && strings.HasSuffix(setReport.Path, "codog-config.json"),
			"text_rendered":  strings.Contains(textOut, "Available"),
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "theme lifecycle harness ok",
		RequestCount: 5,
		MessageCount: 1,
	}, nil
}

func interfacePreferencesScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configPath, err := createHarnessConfigFile(workspace, ".codog-home", nil)
	if err != nil {
		return localScenarioResult{}, err
	}
	language, err := interfaceLanguagePhase(ctx, workspace, configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	vim, err := interfaceVimPhase(ctx, workspace, configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	data, err := json.Marshal(map[string]any{"kind": "interface_preferences", "language": language, "vim": vim})
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{Output: string(data), FinalMessage: "interface preferences harness ok", RequestCount: 8, MessageCount: 1}, nil
}

func privacyKeybindingsScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configPath, err := createHarnessConfigFile(workspace, ".codog-home", map[string]any{"editorMode": "vim"})
	if err != nil {
		return localScenarioResult{}, err
	}
	privacy, err := privacySettingsPhase(ctx, workspace, configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	keybindings, err := keybindingsInitPhase(ctx, workspace, configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	validated, err := keybindingsValidationPhase(ctx, workspace, configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	for key, value := range validated {
		keybindings[key] = value
	}
	data, err := json.Marshal(map[string]any{"kind": "privacy_keybindings", "privacy": privacy, "keybindings": keybindings})
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{Output: string(data), FinalMessage: "privacy keybindings harness ok", RequestCount: 12, MessageCount: 1}, nil
}

func browserNotificationsScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configPath, err := createHarnessConfigFile(workspace, ".codog-home", nil)
	if err != nil {
		return localScenarioResult{}, err
	}
	chrome, err := browserChromePhase(ctx, workspace, configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	notifications, err := browserNotificationsPhase(ctx, workspace, configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	telemetry, err := browserTelemetryPhase(ctx, workspace, configPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := verifyBrowserNotificationConfigCleared(configPath); err != nil {
		return localScenarioResult{}, err
	}
	data, err := json.Marshal(map[string]any{"kind": "browser_notifications", "chrome": chrome, "notifications": notifications, "telemetry": telemetry})
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{Output: string(data), FinalMessage: "browser notifications harness ok", RequestCount: 13, MessageCount: 1}, nil
}

func browserChromePhase(ctx context.Context, workspace string, configPath string) (map[string]any, error) {
	initialChromeOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "chrome")
	if err != nil {
		return nil, err
	}
	initialChrome, err := decodeChromeHarnessReport(initialChromeOut)
	if err != nil {
		return nil, err
	}
	if initialChrome.Kind != "chrome" || initialChrome.Action != "status" || initialChrome.Enabled || initialChrome.Configured {
		return nil, fmt.Errorf("unexpected initial chrome report: %#v", initialChrome)
	}

	setChromeOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "chrome", "on", "--path", configPath)
	if err != nil {
		return nil, err
	}
	setChrome, err := decodeChromeHarnessReport(setChromeOut)
	if err != nil {
		return nil, err
	}
	if setChrome.Action != "set" || !setChrome.Enabled || !setChrome.Configured || setChrome.MCPServer == "" {
		return nil, fmt.Errorf("unexpected set chrome report: %#v", setChrome)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(configData), `"chrome_default_enabled": true`) && !strings.Contains(string(configData), `"chrome_default_enabled":true`) {
		return nil, fmt.Errorf("chrome config did not persist enabled state: %s", string(configData))
	}

	statusChromeOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "chrome", "status")
	if err != nil {
		return nil, err
	}
	statusChrome, err := decodeChromeHarnessReport(statusChromeOut)
	if err != nil {
		return nil, err
	}
	if statusChrome.Action != "status" || !statusChrome.Enabled || !statusChrome.Configured {
		return nil, fmt.Errorf("unexpected persisted chrome report: %#v", statusChrome)
	}

	chromeText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "chrome", "permissions")
	if err != nil {
		return nil, err
	}
	if !harnessContainsAll(chromeText, "Chrome", "Permissions URL", "https://clau.de/chrome/permissions") {
		return nil, fmt.Errorf("chrome text output missing expected values: %s", chromeText)
	}

	clearChromeOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "chrome", "clear", "--path", configPath)
	if err != nil {
		return nil, err
	}
	clearChrome, err := decodeChromeHarnessReport(clearChromeOut)
	if err != nil {
		return nil, err
	}
	if clearChrome.Action != "clear" || clearChrome.Enabled || clearChrome.Configured || !clearChrome.Previous {
		return nil, fmt.Errorf("unexpected clear chrome report: %#v", clearChrome)
	}

	return map[string]any{"initial_enabled": initialChrome.Enabled, "set": setChrome.Enabled, "status": statusChrome.Enabled, "cleared": clearChrome.Enabled, "mcp_server": setChrome.MCPServer, "path_persisted": setChrome.Path != "" && strings.HasSuffix(setChrome.Path, "codog-config.json"), "text_rendered": strings.Contains(chromeText, "Permissions URL")}, nil
}
func browserNotificationsPhase(ctx context.Context, workspace string, configPath string) (map[string]any, error) {
	initialNotificationsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "notifications")
	if err != nil {
		return nil, err
	}
	initialNotifications, err := decodeNotificationsHarnessReport(initialNotificationsOut)
	if err != nil {
		return nil, err
	}
	if initialNotifications.Kind != "notifications" || initialNotifications.Action != "status" || !initialNotifications.Enabled || initialNotifications.Configured {
		return nil, fmt.Errorf("unexpected initial notifications report: %#v", initialNotifications)
	}

	setNotificationsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "notifications", "off", "--path", configPath)
	if err != nil {
		return nil, err
	}
	setNotifications, err := decodeNotificationsHarnessReport(setNotificationsOut)
	if err != nil {
		return nil, err
	}
	if setNotifications.Action != "set" || setNotifications.Enabled || !setNotifications.Configured || !setNotifications.Previous {
		return nil, fmt.Errorf("unexpected set notifications report: %#v", setNotifications)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(configData), `"notifications_enabled": false`) && !strings.Contains(string(configData), `"notifications_enabled":false`) {
		return nil, fmt.Errorf("notifications config did not persist disabled state: %s", string(configData))
	}

	notificationsText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "notifications")
	if err != nil {
		return nil, err
	}
	if !harnessContainsAll(notificationsText, "Notifications", "Enabled          false") {
		return nil, fmt.Errorf("notifications text output missing expected values: %s", notificationsText)
	}

	clearNotificationsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "notifications", "clear", "--path", configPath)
	if err != nil {
		return nil, err
	}
	clearNotifications, err := decodeNotificationsHarnessReport(clearNotificationsOut)
	if err != nil {
		return nil, err
	}
	if clearNotifications.Action != "clear" || !clearNotifications.Enabled || clearNotifications.Configured || clearNotifications.Previous {
		return nil, fmt.Errorf("unexpected clear notifications report: %#v", clearNotifications)
	}

	return map[string]any{"initial_enabled": initialNotifications.Enabled, "set": setNotifications.Enabled, "cleared": clearNotifications.Enabled, "path_persisted": setNotifications.Path != "" && strings.HasSuffix(setNotifications.Path, "codog-config.json"), "text_rendered": strings.Contains(notificationsText, "Enabled          false")}, nil
}
func browserTelemetryPhase(ctx context.Context, workspace string, configPath string) (map[string]any, error) {
	initialTelemetryOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "telemetry")
	if err != nil {
		return nil, err
	}
	initialTelemetry, err := decodeTelemetryHarnessReport(initialTelemetryOut)
	if err != nil {
		return nil, err
	}
	if initialTelemetry.Kind != "telemetry" || initialTelemetry.Action != "status" || initialTelemetry.Enabled || initialTelemetry.Configured {
		return nil, fmt.Errorf("unexpected initial telemetry report: %#v", initialTelemetry)
	}

	setTelemetryOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "telemetry", "on", "--path", configPath)
	if err != nil {
		return nil, err
	}
	setTelemetry, err := decodeTelemetryHarnessReport(setTelemetryOut)
	if err != nil {
		return nil, err
	}
	if setTelemetry.Action != "set" || !setTelemetry.Enabled || !setTelemetry.Configured {
		return nil, fmt.Errorf("unexpected set telemetry report: %#v", setTelemetry)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(configData), `"telemetry_enabled": true`) && !strings.Contains(string(configData), `"telemetry_enabled":true`) {
		return nil, fmt.Errorf("telemetry config did not persist enabled state: %s", string(configData))
	}

	telemetryText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "telemetry")
	if err != nil {
		return nil, err
	}
	if !harnessContainsAll(telemetryText, "Telemetry", "Enabled          true") {
		return nil, fmt.Errorf("telemetry text output missing expected values: %s", telemetryText)
	}

	clearTelemetryOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "telemetry", "clear", "--path", configPath)
	if err != nil {
		return nil, err
	}
	clearTelemetry, err := decodeTelemetryHarnessReport(clearTelemetryOut)
	if err != nil {
		return nil, err
	}
	if clearTelemetry.Action != "clear" || clearTelemetry.Enabled || clearTelemetry.Configured || !clearTelemetry.Previous {
		return nil, fmt.Errorf("unexpected clear telemetry report: %#v", clearTelemetry)
	}

	return map[string]any{"initial_enabled": initialTelemetry.Enabled, "set": setTelemetry.Enabled, "cleared": clearTelemetry.Enabled, "path_persisted": setTelemetry.Path != "" && strings.HasSuffix(setTelemetry.Path, "codog-config.json"), "text_rendered": strings.Contains(telemetryText, "Enabled          true")}, nil
}
func verifyBrowserNotificationConfigCleared(configPath string) error {
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	for _, clearedKey := range []string{`"chrome_default_enabled"`, `"notifications_enabled"`, `"telemetry_enabled"`} {
		if strings.Contains(string(configData), clearedKey) {
			return fmt.Errorf("config still contains %s after clear: %s", clearedKey, string(configData))
		}
	}

	return nil
}

func interfaceLanguagePhase(ctx context.Context, workspace string, configPath string) (map[string]any, error) {
	initialLanguageOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "language")
	if err != nil {
		return nil, err
	}
	initialLanguage, err := decodeLanguageHarnessReport(initialLanguageOut)
	if err != nil {
		return nil, err
	}
	if initialLanguage.Kind != "language" || initialLanguage.Action != "status" || initialLanguage.Configured || initialLanguage.Language != "" {
		return nil, fmt.Errorf("unexpected initial language report: %#v", initialLanguage)
	}

	setLanguageOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "language", "use", "Japanese", "--path", configPath)
	if err != nil {
		return nil, err
	}
	setLanguage, err := decodeLanguageHarnessReport(setLanguageOut)
	if err != nil {
		return nil, err
	}
	if setLanguage.Action != "set" || !setLanguage.Configured || setLanguage.Language != "Japanese" {
		return nil, fmt.Errorf("unexpected set language report: %#v", setLanguage)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(configData), `"language": "Japanese"`) && !strings.Contains(string(configData), `"language":"Japanese"`) {
		return nil, fmt.Errorf("language config did not persist: %s", string(configData))
	}

	statusLanguageOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "language", "view")
	if err != nil {
		return nil, err
	}
	statusLanguage, err := decodeLanguageHarnessReport(statusLanguageOut)
	if err != nil {
		return nil, err
	}
	if statusLanguage.Action != "status" || !statusLanguage.Configured || statusLanguage.Language != "Japanese" {
		return nil, fmt.Errorf("unexpected persisted language report: %#v", statusLanguage)
	}

	languageText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "language", "view")
	if err != nil {
		return nil, err
	}
	if !harnessContainsAll(languageText, "Language", "Japanese") {
		return nil, fmt.Errorf("language text output missing expected values: %s", languageText)
	}

	clearLanguageOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "language", "clear", "--path", configPath)
	if err != nil {
		return nil, err
	}
	clearLanguage, err := decodeLanguageHarnessReport(clearLanguageOut)
	if err != nil {
		return nil, err
	}
	if clearLanguage.Action != "clear" || clearLanguage.Configured || clearLanguage.Language != "" || clearLanguage.Previous != "Japanese" {
		return nil, fmt.Errorf("unexpected clear language report: %#v", clearLanguage)
	}

	return map[string]any{"initial_configured": initialLanguage.Configured, "set": setLanguage.Language, "status": statusLanguage.Language, "cleared": clearLanguage.Language, "clear_previous": clearLanguage.Previous, "path_persisted": setLanguage.Path != "" && strings.HasSuffix(setLanguage.Path, "codog-config.json"), "text_rendered": strings.Contains(languageText, "Japanese")}, nil
}
func interfaceVimPhase(ctx context.Context, workspace string, configPath string) (map[string]any, error) {
	initialVimOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "vim", "status")
	if err != nil {
		return nil, err
	}
	initialVim, err := decodeVimHarnessReport(initialVimOut)
	if err != nil {
		return nil, err
	}
	if initialVim.Kind != "vim" || initialVim.Action != "status" || initialVim.Enabled || initialVim.EditorMode != "default" {
		return nil, fmt.Errorf("unexpected initial vim report: %#v", initialVim)
	}

	setVimOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "vim", "on", "--path", configPath)
	if err != nil {
		return nil, err
	}
	setVim, err := decodeVimHarnessReport(setVimOut)
	if err != nil {
		return nil, err
	}
	if setVim.Action != "set" || !setVim.Enabled || setVim.EditorMode != "vim" || setVim.Previous != "default" {
		return nil, fmt.Errorf("unexpected set vim report: %#v", setVim)
	}
	if err := verifyHarnessFileContainsAny(configPath, `"editorMode": "vim"`, `"editorMode":"vim"`); err != nil {
		return nil, err
	}

	statusVimOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "vim", "status")
	if err != nil {
		return nil, err
	}
	statusVim, err := decodeVimHarnessReport(statusVimOut)
	if err != nil {
		return nil, err
	}
	if statusVim.Action != "status" || !statusVim.Enabled || statusVim.EditorMode != "vim" {
		return nil, fmt.Errorf("unexpected persisted vim report: %#v", statusVim)
	}

	vimText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "vim", "status")
	if err != nil {
		return nil, err
	}
	if !harnessContainsAll(vimText, "Vim", "Editor mode", "vim") {
		return nil, fmt.Errorf("vim text output missing expected values: %s", vimText)
	}

	clearVimOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "vim", "clear", "--path", configPath)
	if err != nil {
		return nil, err
	}
	clearVim, err := decodeVimHarnessReport(clearVimOut)
	if err != nil {
		return nil, err
	}
	if clearVim.Action != "clear" || clearVim.Enabled || clearVim.EditorMode != "default" || clearVim.Previous != "vim" {
		return nil, fmt.Errorf("unexpected clear vim report: %#v", clearVim)
	}
	if err := verifyHarnessFileOmits(configPath, `"language"`, `"editorMode"`); err != nil {
		return nil, err
	}

	return map[string]any{"initial_enabled": initialVim.Enabled, "set": setVim.EditorMode, "status": statusVim.EditorMode, "cleared": clearVim.EditorMode, "clear_previous": clearVim.Previous, "path_persisted": setVim.Path != "" && strings.HasSuffix(setVim.Path, "codog-config.json"), "text_rendered": strings.Contains(vimText, "Editor mode")}, nil
}

func privacySettingsPhase(ctx context.Context, workspace, configPath string) (map[string]any, error) {
	initialPrivacyOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "privacy-settings")
	if err != nil {
		return nil, err
	}
	initialPrivacy, err := decodePrivacyHarnessReport(initialPrivacyOut)
	if err != nil {
		return nil, err
	}
	if initialPrivacy.Kind != "privacy_settings" || initialPrivacy.Action != "show" || !initialPrivacy.Settings["prompt_history_enabled"] {
		return nil, fmt.Errorf("unexpected initial privacy report: %#v", initialPrivacy)
	}

	setPrivacyOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "privacy-settings", "set", "prompt-history", "off", "--path", configPath)
	if err != nil {
		return nil, err
	}
	setPrivacy, err := decodePrivacyHarnessReport(setPrivacyOut)
	if err != nil {
		return nil, err
	}
	if setPrivacy.Action != "set" || setPrivacy.Key != "prompt_history_enabled" || setPrivacy.Value == nil || *setPrivacy.Value || setPrivacy.Settings["prompt_history_enabled"] {
		return nil, fmt.Errorf("unexpected set privacy report: %#v", setPrivacy)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(configData), `"prompt_history_enabled": false`) && !strings.Contains(string(configData), `"prompt_history_enabled":false`) {
		return nil, fmt.Errorf("privacy config did not persist prompt-history setting: %s", string(configData))
	}

	statusPrivacyOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "privacy-settings", "show")
	if err != nil {
		return nil, err
	}
	statusPrivacy, err := decodePrivacyHarnessReport(statusPrivacyOut)
	if err != nil {
		return nil, err
	}
	if statusPrivacy.Action != "show" || statusPrivacy.Settings["prompt_history_enabled"] {
		return nil, fmt.Errorf("unexpected persisted privacy report: %#v", statusPrivacy)
	}

	privacyText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "privacy-settings")
	if err != nil {
		return nil, err
	}
	if !harnessContainsAll(privacyText, "Privacy Settings", "Prompt history", "disabled") {
		return nil, fmt.Errorf("privacy text output missing expected values: %s", privacyText)
	}

	clearPrivacyOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "privacy-settings", "clear", "prompt-history", "--path", configPath)
	if err != nil {
		return nil, err
	}
	clearPrivacy, err := decodePrivacyHarnessReport(clearPrivacyOut)
	if err != nil {
		return nil, err
	}
	if clearPrivacy.Action != "clear" || clearPrivacy.Key != "prompt_history_enabled" || !clearPrivacy.Settings["prompt_history_enabled"] {
		return nil, fmt.Errorf("unexpected clear privacy report: %#v", clearPrivacy)
	}

	return map[string]any{"initial_prompt_history": initialPrivacy.Settings["prompt_history_enabled"], "set_prompt_history": setPrivacy.Settings["prompt_history_enabled"], "status_prompt_history": statusPrivacy.Settings["prompt_history_enabled"], "cleared_prompt_history": clearPrivacy.Settings["prompt_history_enabled"], "path_persisted": setPrivacy.Path != "" && strings.HasSuffix(setPrivacy.Path, "codog-config.json"), "text_rendered": strings.Contains(privacyText, "Prompt history")}, nil
}
func keybindingsInitPhase(ctx context.Context, workspace, configPath string) (map[string]any, error) {
	configHome := filepath.Join(workspace, ".codog-home")
	initialKeybindingsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings")
	if err != nil {
		return nil, err
	}
	initialKeybindings, err := decodeKeybindingsHarnessReport(initialKeybindingsOut)
	if err != nil {
		return nil, err
	}
	if initialKeybindings.Kind != "keybindings" || initialKeybindings.Action != "show" || !initialKeybindings.VimMode || initialKeybindings.KeybindingsExists {
		return nil, fmt.Errorf("unexpected initial keybindings report: %#v", initialKeybindings)
	}

	pathOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings", "path")
	if err != nil {
		return nil, err
	}
	pathReport, err := decodeKeybindingsFileHarnessReport(pathOut)
	if err != nil {
		return nil, err
	}
	if pathReport.Action != "path" || pathReport.Path != filepath.Join(configHome, "keybindings.json") || pathReport.Exists {
		return nil, fmt.Errorf("unexpected keybindings path report: %#v", pathReport)
	}

	initOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings", "init")
	if err != nil {
		return nil, err
	}
	initReport, err := decodeKeybindingsFileHarnessReport(initOut)
	if err != nil {
		return nil, err
	}
	if initReport.Action != "init" || initReport.Status != "created" || !initReport.Created || !initReport.Exists {
		return nil, fmt.Errorf("unexpected keybindings init report: %#v", initReport)
	}
	keybindingsData, err := os.ReadFile(pathReport.Path)
	if err != nil {
		return nil, err
	}
	if !harnessContainsAll(string(keybindingsData), `"context": "repl"`, `"ctrl+r"`, `"shift+enter"`, `"ctrl+s"`, `"ctrl+g"`, `"ctrl+x ctrl+e"`, `"ctrl+x ctrl+k"`, `"ctrl+x ctrl+c"`, `"ctrl+x ctrl+u"`, `"ctrl+x ctrl+s"`, `"ctrl+x ctrl+y"`, `"ctrl+x backspace"`, `"ctrl+_"`, `"ctrl+shift+-"`, `"ctrl+v"`, `"ctrl+shift+p"`, `"ctrl+p"`, `"ctrl+shift+f"`, `"ctrl+f"`, `"alt+m"`, `"meta+m"`, `"alt+p"`, `"alt+o"`, `"alt+t"`, `"shift+up"`, `"ctrl+o"`, `"ctrl+l"`, `"ctrl+u"`, `"ctrl+k"`, `"home"`, `"ctrl+a"`, `"end"`, `"ctrl+d"`, `"ctrl+b"`, `"ctrl+t"`, `"ctrl+shift+t"`, `"up"`) {
		return nil, fmt.Errorf("keybindings template missing expected entries: %s", string(keybindingsData))
	}
	if !harnessContainsAll(string(keybindingsData), `"context": "tui-modal"`, `"j"`, `"shift+down"`, `"ctrl+down"`) {
		return nil, fmt.Errorf("keybindings template missing modal navigation entries: %s", string(keybindingsData))
	}
	if !harnessContainsAll(string(keybindingsData), `"context": "tui-attachments"`, `"right"`, `"backspace"`, `"delete"`) {
		return nil, fmt.Errorf("keybindings template missing attachment navigation entries: %s", string(keybindingsData))
	}
	if !harnessContainsAll(string(keybindingsData), `"context": "tui-diff"`, `"enter"`, `"left"`, `"right"`) {
		return nil, fmt.Errorf("keybindings template missing diff dialog entries: %s", string(keybindingsData))
	}

	return map[string]any{"initial_exists": initialKeybindings.KeybindingsExists, "path": strings.HasSuffix(pathReport.Path, "keybindings.json"), "created": initReport.Created}, nil
}
func keybindingsValidationPhase(ctx context.Context, workspace, configPath string) (map[string]any, error) {
	configHome := filepath.Join(workspace, ".codog-home")
	keybindingsPath := filepath.Join(configHome, "keybindings.json")
	editorLog := filepath.Join(workspace, "keybindings-editor.log")
	editorScript := filepath.Join(workspace, "keybindings-editor.sh")
	if err := os.WriteFile(editorScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" > \"$1\"\n"), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(keybindingsPath); err != nil {
		return nil, err
	}
	openOut, err := runHarnessCodogWithEnv(ctx, workspace, []string{"VISUAL=" + editorScript + " " + editorLog}, "--config", configPath, "--output-format", "json", "keybindings", "open")
	if err != nil {
		return nil, err
	}
	openReport, err := decodeKeybindingsFileHarnessReport(openOut)
	if err != nil {
		return nil, err
	}
	openedPath, err := os.ReadFile(editorLog)
	if err != nil {
		return nil, err
	}
	if openReport.Action != "open" || openReport.Status != "created_opened" || !openReport.Created || !openReport.Opened || string(openedPath) != keybindingsPath+"\n" {
		return nil, fmt.Errorf("unexpected keybindings open report: %#v opened=%q", openReport, string(openedPath))
	}
	keybindingsData, err := os.ReadFile(keybindingsPath)
	if err != nil {
		return nil, err
	}

	validateOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings", "validate")
	if err != nil {
		return nil, err
	}
	validateReport, err := decodeKeybindingsValidationHarnessReport(validateOut)
	if err != nil {
		return nil, err
	}
	if validateReport.Action != "validate" || !validateReport.Valid || validateReport.ContextCount != 7 || validateReport.BindingCount != 86 {
		return nil, fmt.Errorf("unexpected keybindings validate report: %#v", validateReport)
	}

	resolveOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings", "resolve", "repl", "Control-R")
	if err != nil {
		return nil, err
	}
	resolveReport, err := decodeKeybindingsResolveHarnessReport(resolveOut)
	if err != nil {
		return nil, err
	}
	if resolveReport.Action != "resolve" || !resolveReport.Found || resolveReport.Source != "user" || resolveReport.NormalizedKey != "ctrl+r" || resolveReport.BindingAction != "reverse search prompt history" {
		return nil, fmt.Errorf("unexpected keybindings resolve report: %#v", resolveReport)
	}

	keybindingsText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "keybindings")
	if err != nil {
		return nil, err
	}
	if !harnessContainsAll(keybindingsText, "Keybindings", "Editor mode      vim", "User valid       true") {
		return nil, fmt.Errorf("keybindings text output missing expected values: %s", keybindingsText)
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(configData), `"prompt_history_enabled"`) {
		return nil, fmt.Errorf("privacy config still contains prompt_history_enabled after clear: %s", string(configData))
	}

	return map[string]any{
		"opened": openReport.Opened, "valid": validateReport.Valid, "contexts": validateReport.ContextCount, "bindings": validateReport.BindingCount,
		"shift_enter": strings.Contains(string(keybindingsData), `"shift+enter"`), "prompt_stash_key": strings.Contains(string(keybindingsData), `"ctrl+s"`),
		"external_editor": strings.Contains(string(keybindingsData), `"ctrl+g"`), "external_editor_chord": strings.Contains(string(keybindingsData), `"ctrl+x ctrl+e"`),
		"kill_agents_chord": strings.Contains(string(keybindingsData), `"ctrl+x ctrl+k"`), "compact_chord": strings.Contains(string(keybindingsData), `"ctrl+x ctrl+c"`),
		"undo_change_chord": strings.Contains(string(keybindingsData), `"ctrl+x ctrl+u"`), "export_chord": strings.Contains(string(keybindingsData), `"ctrl+x ctrl+s"`), "copy_chord": strings.Contains(string(keybindingsData), `"ctrl+x ctrl+y"`),
		"attachment_remove_key": strings.Contains(string(keybindingsData), `"ctrl+x backspace"`), "composer_undo_key": harnessContainsAll(string(keybindingsData), `"ctrl+_"`, `"ctrl+shift+-"`),
		"clipboard_paste_key": strings.Contains(string(keybindingsData), `"ctrl+v"`), "quick_open_key": harnessContainsAll(string(keybindingsData), `"ctrl+shift+p"`, `"ctrl+p"`),
		"global_search_key": harnessContainsAll(string(keybindingsData), `"ctrl+shift+f"`, `"ctrl+f"`), "mode_cycle_keys": harnessContainsAll(string(keybindingsData), `"alt+m"`, `"meta+m"`),
		"modal_navigation_keys": harnessContainsAll(string(keybindingsData), `"context": "tui-modal"`, `"j"`, `"k"`, `"shift+down"`),
		"attachment_nav_keys":   harnessContainsAll(string(keybindingsData), `"context": "tui-attachments"`, `"right"`, `"backspace"`, `"delete"`),
		"diff_dialog_keys":      harnessContainsAll(string(keybindingsData), `"context": "tui-diff"`, `"enter"`, `"left"`, `"right"`),
		"runtime_control_keys":  harnessContainsAll(string(keybindingsData), `"alt+p"`, `"alt+o"`, `"alt+t"`),
		"message_actions_key":   strings.Contains(string(keybindingsData), `"shift+up"`), "transcript_key": strings.Contains(string(keybindingsData), `"ctrl+o"`),
		"terminal_keys": harnessContainsAll(string(keybindingsData), `"ctrl+l"`, `"ctrl+d"`), "background_key": strings.Contains(string(keybindingsData), `"ctrl+b"`),
		"todo_panel_key": strings.Contains(string(keybindingsData), `"ctrl+t"`), "task_board_key": strings.Contains(string(keybindingsData), `"ctrl+shift+t"`), "queue_edit_key": strings.Contains(string(keybindingsData), `"up"`),
		"resolved": resolveReport.Found, "source": resolveReport.Source, "text_rendered": strings.Contains(keybindingsText, "User valid"),
	}, nil
}
