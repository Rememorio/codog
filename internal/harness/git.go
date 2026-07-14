package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/tools"
)

func gitPreserveStateScenario() scenario {
	return scenario{
		name:     "git_preserve_state_roundtrip",
		runLocal: gitPreserveStateScenarioRunLocal,
	}
}

func worktreeLifecycleScenario() scenario {
	return scenario{
		name:     "worktree_lifecycle_roundtrip",
		runLocal: worktreeLifecycleScenarioRunLocal,
	}
}

func runHarnessGit(workspace string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func planTodoScenario() scenario {
	return scenario{
		name:     "plan_todo_roundtrip",
		runLocal: planTodoScenarioRunLocal,
	}
}

func todoCompletionVerificationScenario() scenario {
	return scenario{
		name:     "todo_completion_verification_roundtrip",
		runLocal: todoCompletionVerificationScenarioRunLocal,
	}
}

func lspStaticScenario() scenario {
	return scenario{
		name:     "lsp_static_roundtrip",
		runLocal: lspStaticScenarioRunLocal,
	}
}

func lspCLIMetadataScenario() scenario {
	return scenario{
		name:     "lsp_cli_metadata_roundtrip",
		runLocal: lspCLIMetadataScenarioRunLocal,
	}
}

type lspActionsHarnessReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type lspDiscoverHarnessReport struct {
	Kind       string                `json:"kind"`
	Action     string                `json:"action"`
	Status     string                `json:"status"`
	Count      int                   `json:"count"`
	Candidates []lspHarnessCandidate `json:"candidates"`
}

type lspListHarnessReport struct {
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Status  string            `json:"status"`
	Count   int               `json:"count"`
	Servers []json.RawMessage `json:"servers"`
}

type lspHarnessCandidate struct {
	Language string `json:"language"`
	Command  string `json:"command"`
}

func lspHarnessCandidateExists(candidates []lspHarnessCandidate, language string, command string) bool {
	for _, candidate := range candidates {
		if candidate.Language == language && candidate.Command == command {
			return true
		}
	}
	return false
}

func pluginLifecycleScenario() scenario {
	state := &pluginLifecycleState{}
	return scenario{
		name:   "plugin_lifecycle_roundtrip",
		turns:  []mockanthropic.Turn{{Text: "plugin lifecycle harness ok"}},
		prompt: "verify plugin lifecycle",
		setup:  state.setup,
		verify: state.verify,
	}
}

type pluginLifecycleState struct {
	installedRoot string
	disabledRoot  string
}

func (s *pluginLifecycleState) setup(workspace string) error {
	source := filepath.Join(workspace, "plugin-source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		return err
	}
	manifest := `{"id":"lifecycle","name":"lifecycle","version":"1.0.0","description":"Lifecycle harness plugin","lifecycle":{"init":["echo init-ok > lifecycle-init.txt"],"shutdown":["echo shutdown-ok > lifecycle-shutdown.txt"]},"tools":[{"name":"lifecycle_tool","command":"cat","permission":"read-only"}]}`
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(manifest), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(source, "tool.sh"), []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		return err
	}
	installed, err := plugins.Install(workspace, source)
	if err != nil {
		return err
	}
	s.installedRoot = installed.Root
	if err := verifyInstalledPlugin(installed); err != nil {
		return err
	}
	disabled, err := plugins.Disable(workspace, installed.ID)
	if err != nil {
		return err
	}
	s.disabledRoot = disabled.Root
	if err := verifyDisabledPlugin(disabled); err != nil {
		return err
	}
	enabled, err := plugins.Enable(workspace, installed.ID)
	if err != nil {
		return err
	}
	if err := verifyEnabledPlugin(enabled); err != nil {
		return err
	}
	return plugins.Remove(workspace, installed.ID)
}

func verifyInstalledPlugin(installed plugins.Manifest) error {
	if !installed.Enabled {
		return fmt.Errorf("installed plugin is disabled")
	}
	if err := verifyPluginLifecycleEvent(installed, "init", "lifecycle-init.txt", "init-ok"); err != nil {
		return err
	}
	return verifyPluginLifecycleEvent(installed, "shutdown", "lifecycle-shutdown.txt", "shutdown-ok")
}

func verifyPluginLifecycleEvent(installed plugins.Manifest, event string, markerName string, expected string) error {
	run := plugins.RunLifecycle(context.Background(), installed, event, 5*time.Second)
	if run.Status != "ok" {
		return fmt.Errorf("%s lifecycle failed: %s", event, run.Message)
	}
	marker, err := os.ReadFile(filepath.Join(installed.Root, markerName))
	if err != nil {
		return err
	}
	if !strings.Contains(string(marker), expected) {
		return fmt.Errorf("%s lifecycle marker mismatch: %q", event, string(marker))
	}
	return nil
}

func verifyDisabledPlugin(disabled plugins.Manifest) error {
	if disabled.Enabled {
		return fmt.Errorf("disabled plugin still reports enabled")
	}
	_, err := os.Stat(filepath.Join(disabled.Root, plugins.DisabledMarker))
	return err
}

func verifyEnabledPlugin(enabled plugins.Manifest) error {
	if !enabled.Enabled {
		return fmt.Errorf("enabled plugin still reports disabled")
	}
	if _, err := os.Stat(filepath.Join(enabled.Root, plugins.DisabledMarker)); !os.IsNotExist(err) {
		return fmt.Errorf("disabled marker still present after enable: %v", err)
	}
	return nil
}

func (s *pluginLifecycleState) verify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "plugin lifecycle harness ok") {
		return fmt.Errorf("missing plugin lifecycle final response")
	}
	if err := expectToolCalls(result, 0, false); err != nil {
		return err
	}
	for _, root := range []string{s.installedRoot, s.disabledRoot} {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("missing lifecycle plugin root")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			return fmt.Errorf("plugin root still exists after remove: %s", root)
		}
	}
	return nil
}

func taskLifecycleScenario() scenario {
	return scenario{
		name:     "task_lifecycle_roundtrip",
		runLocal: taskLifecycleScenarioRunLocal,
	}
}

func taskPacketRoundtripScenario() scenario {
	return scenario{
		name:     "task_packet_roundtrip",
		runLocal: taskPacketRoundtripScenarioRunLocal,
	}
}

func teamCronLifecycleScenario() scenario {
	return scenario{
		name:     "team_cron_lifecycle_roundtrip",
		runLocal: teamCronLifecycleScenarioRunLocal,
	}
}

func workerLifecycleScenario() scenario {
	return scenario{
		name:     "worker_lifecycle_roundtrip",
		runLocal: workerLifecycleScenarioRunLocal,
	}
}

func gitPreserveStateScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	remote := filepath.Join(workspace, "origin.git")
	if err := runHarnessGit(workspace, "init", "-q", "--bare", remote); err != nil {
		return localScenarioResult{}, err
	}
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	if err := runHarnessGit(repo, "init", "-q", "-b", "main"); err != nil {
		return localScenarioResult{}, err
	}
	for _, args := range [][]string{
		{"config", "user.email", "codog@example.test"},
		{"config", "user.name", "Codog Test"},
	} {
		if err := runHarnessGit(repo, args...); err != nil {
			return localScenarioResult{}, err
		}
	}
	notesPath := filepath.Join(repo, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("base\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	for _, args := range [][]string{
		{"add", "notes.txt"},
		{"commit", "-q", "-m", "chore: base"},
		{"remote", "add", "origin", remote},
		{"push", "-q", "-u", "origin", "main"},
	} {
		if err := runHarnessGit(repo, args...); err != nil {
			return localScenarioResult{}, err
		}
	}
	baseSHA, err := gitops.Run(repo, "rev-parse", "HEAD")
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	for _, args := range [][]string{
		{"add", "feature.txt"},
		{"commit", "-q", "-m", "feat: preserve state"},
	} {
		if err := runHarnessGit(repo, args...); err != nil {
			return localScenarioResult{}, err
		}
	}
	if err := os.WriteFile(notesPath, []byte("base\nworktree\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}

	state, err := gitops.PreserveStateForIssue(repo)
	if err != nil {
		return localScenarioResult{}, err
	}
	if state == nil {
		return localScenarioResult{}, errors.New("git preserve state returned nil")
	}
	if state.RemoteBase != "origin/main" || state.RemoteBaseSHA != baseSHA || state.BranchName != "main" {
		return localScenarioResult{}, fmt.Errorf("unexpected preserved git identity: %#v", state)
	}
	for _, expected := range []string{"+feature", "+worktree"} {
		if !strings.Contains(state.Patch, expected) {
			return localScenarioResult{}, fmt.Errorf("preserved patch missing %s", expected)
		}
	}
	if !strings.Contains(state.FormatPatch, "feat: preserve state") {
		return localScenarioResult{}, fmt.Errorf("format patch missing commit subject")
	}
	if len(state.UntrackedFiles) != 1 || state.UntrackedFiles[0].Path != "scratch.txt" || state.UntrackedFiles[0].Content != "scratch\n" {
		return localScenarioResult{}, fmt.Errorf("unexpected untracked preservation: %#v", state.UntrackedFiles)
	}
	output, err := json.Marshal(map[string]any{
		"kind":              "git_preserve_state",
		"remote_base":       state.RemoteBase,
		"remote_base_sha":   state.RemoteBaseSHA,
		"branch_name":       state.BranchName,
		"patch_has_feature": strings.Contains(state.Patch, "+feature"),
		"patch_has_dirty":   strings.Contains(state.Patch, "+worktree"),
		"format_patch":      strings.TrimSpace(state.FormatPatch) != "",
		"share_sidecar":     true,
		"untracked_files":   len(state.UntrackedFiles),
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(output),
		FinalMessage: "git preserve state harness ok",
		RequestCount: 1,
	}, nil
}

func worktreeLifecycleScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	if err := runHarnessGit(workspace, "init", "-q", "-b", "main"); err != nil {
		return localScenarioResult{}, err
	}
	for _, args := range [][]string{
		{"config", "user.email", "codog@example.test"},
		{"config", "user.name", "Codog Test"},
	} {
		if err := runHarnessGit(workspace, args...); err != nil {
			return localScenarioResult{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("worktree parity\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"commit", "-q", "-m", "init worktree parity"},
	} {
		if err := runHarnessGit(workspace, args...); err != nil {
			return localScenarioResult{}, err
		}
	}

	registry := tools.NewRegistry(workspace)
	var entered struct {
		Kind       string `json:"kind"`
		Operation  string `json:"operation"`
		Allocation struct {
			ID   string `json:"id"`
			Path string `json:"path"`
			Ref  string `json:"ref"`
		} `json:"allocation"`
	}
	enterOut, err := decodeHarnessOutput(&entered, func() (string, error) {
		return registry.Execute(ctx, "EnterWorktreeTool", json.RawMessage(`{"name":"reviewer"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if entered.Kind != "worktree" || entered.Operation != "enter" || entered.Allocation.ID == "" || entered.Allocation.Path == "" || entered.Allocation.Ref == "" {
		return localScenarioResult{}, fmt.Errorf("unexpected enter worktree output: %s", enterOut)
	}
	removed := false
	defer func() {
		if !removed {
			_, _ = registry.Execute(ctx, "ExitWorktreeTool", json.RawMessage(fmt.Sprintf(`{"id":%q}`, entered.Allocation.ID)), nil)
		}
	}()
	checkoutReadme := filepath.Join(entered.Allocation.Path, "README.md")
	data, err := os.ReadFile(checkoutReadme)
	if err != nil {
		return localScenarioResult{}, err
	}
	if string(data) != "worktree parity\n" {
		return localScenarioResult{}, fmt.Errorf("unexpected checkout README content: %q", string(data))
	}
	metadataPath := filepath.Join(workspace, ".codog", "worktrees", "metadata", entered.Allocation.ID+".json")
	if _, err := os.Stat(metadataPath); err != nil {
		return localScenarioResult{}, fmt.Errorf("missing worktree metadata: %w", err)
	}

	var exited struct {
		Kind      string `json:"kind"`
		Operation string `json:"operation"`
		ID        string `json:"id"`
		Removed   bool   `json:"removed"`
	}
	exitOut, err := decodeHarnessOutput(&exited, func() (string, error) {
		return registry.Execute(ctx, "exit_worktree", json.RawMessage(fmt.Sprintf(`{"id":%q}`, entered.Allocation.ID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if exited.Kind != "worktree" || exited.Operation != "exit" || exited.ID != entered.Allocation.ID || !exited.Removed {
		return localScenarioResult{}, fmt.Errorf("unexpected exit worktree output: %s", exitOut)
	}
	removed = true
	if _, err := os.Stat(entered.Allocation.Path); !os.IsNotExist(err) {
		return localScenarioResult{}, fmt.Errorf("worktree path still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(metadataPath); !os.IsNotExist(err) {
		return localScenarioResult{}, fmt.Errorf("worktree metadata still exists or stat failed: %v", err)
	}
	return localScenarioResult{
		Output:       strings.Join([]string{enterOut, exitOut, "worktree lifecycle harness ok"}, "\n"),
		FinalMessage: "worktree lifecycle harness ok",
		ToolCalls:    2,
		ToolUses:     []string{"enter_worktree", "exit_worktree"},
		RequestCount: 2,
	}, nil
}

func planTodoScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	enterOut, err := tools.EnterPlanModeTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"plan":"1. Inspect workspace\n2. Update tests"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "plan"`, `"action": "enter"`, `"status": "active"`, "Inspect workspace"} {
		if !strings.Contains(enterOut, expected) {
			return localScenarioResult{}, fmt.Errorf("plan enter output missing %s", expected)
		}
	}

	editorLog := filepath.Join(workspace, "plan-editor.log")
	editorScript := filepath.Join(workspace, "plan-editor.sh")
	if err := os.WriteFile(editorScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" > \"$1\"\n"), 0o755); err != nil {
		return localScenarioResult{}, err
	}
	var opened struct {
		Action string `json:"action"`
		Status string `json:"status"`
		Opened bool   `json:"opened"`
	}
	openOut, err := decodeHarnessOutput(&opened, func() (string, error) {
		return runHarnessCodogWithEnv(ctx, workspace, []string{"VISUAL=" + editorScript + " " + editorLog}, "plan", "open", "--output-format", "json")
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if opened.Action != "open" || opened.Status != "opened" || !opened.Opened {
		return localScenarioResult{}, fmt.Errorf("unexpected plan open output: %s", openOut)
	}
	openedPath, err := os.ReadFile(editorLog)
	if err != nil {
		return localScenarioResult{}, err
	}
	actualPlanPath, err := filepath.EvalSymlinks(strings.TrimSpace(string(openedPath)))
	if err != nil {
		return localScenarioResult{}, err
	}
	expectedPlanPath, err := filepath.EvalSymlinks(filepath.Join(workspace, ".codog", "plan.json"))
	if err != nil {
		return localScenarioResult{}, err
	}
	if actualPlanPath != expectedPlanPath {
		return localScenarioResult{}, fmt.Errorf("plan editor opened unexpected path: %s", string(openedPath))
	}

	writeOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "write focused parity test",
						"activeForm": "writing focused parity test",
						"status": "in_progress",
						"priority": "high"
					},
					{
						"content": "run smoke",
						"activeForm": "running smoke",
						"status": "pending",
						"priority": "medium"
					}
				]
			}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 2`, `"content": "write focused parity test"`, `"status": "in_progress"`, `"newTodos": [`} {
		if !strings.Contains(writeOut, expected) {
			return localScenarioResult{}, fmt.Errorf("todo write output missing %s", expected)
		}
	}

	readOut, err := tools.TodoReadTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "todos"`, `"action": "list"`, `"total": 2`, `"content": "run smoke"`} {
		if !strings.Contains(readOut, expected) {
			return localScenarioResult{}, fmt.Errorf("todo read output missing %s", expected)
		}
	}

	exitOut, err := tools.ExitPlanModeTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"plan":"Final plan: implement, test, smoke"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "plan"`, `"action": "exit"`, `"status": "inactive"`, "Final plan: implement, test, smoke"} {
		if !strings.Contains(exitOut, expected) {
			return localScenarioResult{}, fmt.Errorf("plan exit output missing %s", expected)
		}
	}

	planData, err := os.ReadFile(filepath.Join(workspace, ".codog", "plan.json"))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(string(planData), `"active": false`, "Final plan: implement, test, smoke") {
		return localScenarioResult{}, fmt.Errorf("persisted plan state was not finalized: %s", string(planData))
	}
	todoData, err := os.ReadFile(filepath.Join(workspace, ".codog", "todos.json"))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(string(todoData), `"kind": "todos"`, "write focused parity test") {
		return localScenarioResult{}, fmt.Errorf("persisted todo state missing active items: %s", string(todoData))
	}

	return localScenarioResult{
		Output:       strings.Join([]string{enterOut, openOut, writeOut, readOut, exitOut}, "\n"),
		FinalMessage: "plan todo harness ok",
		ToolCalls:    4,
		ToolUses:     []string{"enter_plan_mode", "todo_write", "todo_read", "exit_plan_mode"},
		RequestCount: 5,
	}, nil
}

func todoCompletionVerificationScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	initialOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "draft implementation",
						"activeForm": "drafting implementation",
						"status": "in_progress",
						"priority": "high"
					}
				]
			}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 1`, `"content": "draft implementation"`} {
		if !strings.Contains(initialOut, expected) {
			return localScenarioResult{}, fmt.Errorf("initial todo write output missing %s", expected)
		}
	}

	completedOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "draft implementation",
						"activeForm": "drafting implementation",
						"status": "completed",
						"priority": "high"
					},
					{
						"content": "update tests",
						"activeForm": "updating tests",
						"status": "completed",
						"priority": "medium"
					},
					{
						"content": "prepare summary",
						"activeForm": "preparing summary",
						"status": "completed",
						"priority": "low"
					}
				]
			}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 0`, `"oldTodos": [`, `"content": "draft implementation"`, `"verificationNudgeNeeded": true`} {
		if !strings.Contains(completedOut, expected) {
			return localScenarioResult{}, fmt.Errorf("completed todo write output missing %s", expected)
		}
	}

	readOut, err := tools.TodoReadTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "todos"`, `"action": "list"`, `"total": 0`} {
		if !strings.Contains(readOut, expected) {
			return localScenarioResult{}, fmt.Errorf("todo read output missing %s", expected)
		}
	}
	if strings.Contains(readOut, "draft implementation") || strings.Contains(readOut, "update tests") {
		return localScenarioResult{}, fmt.Errorf("completed todos were not cleared from read output: %s", readOut)
	}

	todoData, err := os.ReadFile(filepath.Join(workspace, ".codog", "todos.json"))
	if err != nil {
		return localScenarioResult{}, err
	}
	if strings.Contains(string(todoData), "draft implementation") || strings.Contains(string(todoData), "update tests") {
		return localScenarioResult{}, fmt.Errorf("completed todos were not cleared from persisted state: %s", string(todoData))
	}

	return localScenarioResult{
		Output:       strings.Join([]string{initialOut, completedOut, readOut}, "\n"),
		FinalMessage: "todo completion verification harness ok",
		ToolCalls:    3,
		ToolUses:     []string{"todo_write", "todo_write", "todo_read"},
		RequestCount: 3,
	}, nil
}

func lspStaticScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	pkgDir := filepath.Join(workspace, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/harness\n\ngo 1.25\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	source := "package pkg\n\ntype Runner struct{}\n\nfunc RunFast() Runner { return Runner{} }\n\nfunc UseRunner() Runner { return RunFast() }\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "runner.go"), []byte(source), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	messy := "package pkg\n\nfunc messy(){println(\"hi\")}\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "messy.go"), []byte(messy), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	broken := "package pkg\n\nfunc Broken() { MissingSymbol() }\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "broken.go"), []byte(broken), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	foldSource := "package pkg\n\nfunc FoldOnly() {\n\tprintln(\"fold\")\n}\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "fold.go"), []byte(foldSource), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	linkSource := "package pkg\n\n// Docs: https://example.test/docs.\nconst Link = \"https://example.test/api\"\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "links.go"), []byte(linkSource), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	colorSource := "package pkg\n\nconst Accent = \"#336699\"\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "colors.go"), []byte(colorSource), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	hintSource := "package pkg\n\nfunc Build(name string, count int) int { return count }\nfunc UseBuild() { _ = Build(\"codog\", 2) }\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "hints.go"), []byte(hintSource), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	hierarchySource := "package pkg\n\ntype TypeBase struct{}\ntype TypeChild struct{ TypeBase }\ntype TypeContract interface { Build() }\nfunc (TypeChild) Build() {}\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "hierarchy.go"), []byte(hierarchySource), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	inlineSource := "package pkg\n\nconst InlineAnswer = 42\n\nfunc InlineValuesDemo() {\n\tlocal := \"codog\"\n\t_ = local\n}\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "inline.go"), []byte(inlineSource), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	importsSource := "package pkg\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n\t\"bytes\"\n\t\"fmt\"\n)\n\nfunc ImportsDemo(){ fmt.Println(strings.TrimSpace(\" hi \")) }\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "imports.go"), []byte(importsSource), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	hintArgChar := strings.Index(strings.Split(hintSource, "\n")[3], `"codog"`)
	tool := tools.LSPTool{Workspace: workspace}

	outputs, err := executeLSPStaticCases(ctx, tool, lspStaticCases(hintArgChar))
	if err != nil {
		return localScenarioResult{}, err
	}

	data, err := os.ReadFile(filepath.Join(pkgDir, "messy.go"))
	if err != nil {
		return localScenarioResult{}, err
	}
	if string(data) != messy {
		return localScenarioResult{}, fmt.Errorf("lsp format unexpectedly modified file")
	}

	toolUses := make([]string, len(outputs))
	for i := range toolUses {
		toolUses[i] = "lsp"
	}
	return localScenarioResult{
		Output:       strings.Join(outputs, "\n"),
		FinalMessage: "lsp static harness ok",
		ToolCalls:    len(outputs),
		ToolUses:     toolUses,
		RequestCount: len(outputs),
	}, nil
}

func lspCLIMetadataScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	if err := os.MkdirAll(configHome, 0o755); err != nil {
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

	var actions lspActionsHarnessReport
	actionsOut, err := decodeHarnessOutput(&actions, func() (string, error) {
		return runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "actions", "--json")
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if actions.Kind != "lsp_actions" || actions.Action != "actions" || actions.Status != "ok" || actions.Count < 40 {
		return localScenarioResult{}, fmt.Errorf("unexpected lsp actions report: %#v", actions)
	}
	if !harnessContainsAll(actionsOut, `"name": "definition"`, `"method": "textDocument/definition"`, `"name": "references"`) {
		return localScenarioResult{}, fmt.Errorf("lsp actions output missing expected actions: %s", actionsOut)
	}

	var discover lspDiscoverHarnessReport
	_, err = decodeHarnessOutput(&discover, func() (string, error) {
		return runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "discover", "--json")
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if discover.Kind != "lsp_discover" || discover.Action != "discover" || discover.Status != "ok" || discover.Count < 5 {
		return localScenarioResult{}, fmt.Errorf("unexpected lsp discover report: %#v", discover)
	}
	if !lspHarnessCandidateExists(discover.Candidates, "go", "gopls") || !lspHarnessCandidateExists(discover.Candidates, "rust", "rust-analyzer") {
		return localScenarioResult{}, fmt.Errorf("lsp discover candidates missing expected defaults: %#v", discover.Candidates)
	}

	var list lspListHarnessReport
	_, err = decodeHarnessOutput(&list, func() (string, error) {
		return runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "list", "--json")
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if list.Kind != "lsp_list" || list.Action != "list" || list.Status != "ok" || list.Count != 0 || len(list.Servers) != 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected lsp list report: %#v", list)
	}

	textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "discover", "--output-format", "text")
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(textOut, "LSP Discover", "gopls", "rust-analyzer") {
		return localScenarioResult{}, fmt.Errorf("lsp discover text output missing expected values: %s", textOut)
	}

	report := map[string]any{
		"kind": "lsp_cli_metadata",
		"lsp": map[string]any{
			"actions":       actions.Count,
			"candidates":    discover.Count,
			"servers":       list.Count,
			"has_go":        lspHarnessCandidateExists(discover.Candidates, "go", "gopls"),
			"has_rust":      lspHarnessCandidateExists(discover.Candidates, "rust", "rust-analyzer"),
			"text_rendered": strings.Contains(textOut, "LSP Discover"),
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "lsp cli metadata harness ok",
		RequestCount: 4,
		MessageCount: 1,
	}, nil
}

func taskLifecycleScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})

	var created struct {
		TaskID string          `json:"task_id"`
		Kind   string          `json:"kind"`
		Task   background.Task `json:"task"`
	}
	createOut, err := decodeHarnessOutput(&created, func() (string, error) {
		return registry.Execute(ctx, "TaskCreateTool", json.RawMessage(`{
				"command": "printf task-output",
				"kind": "parity",
				"session_id": "session-task"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if created.TaskID == "" || created.Task.ID != created.TaskID || created.Kind != "parity" {
		return localScenarioResult{}, fmt.Errorf("unexpected task create output: %s", createOut)
	}

	var status struct {
		TaskID string `json:"task_id"`
		Kind   string `json:"kind"`
	}
	statusOut, err := decodeHarnessOutput(&status, func() (string, error) {
		return registry.Execute(ctx, "task_status", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if status.TaskID != created.TaskID || status.Kind != "parity" {
		return localScenarioResult{}, fmt.Errorf("unexpected task status output: %s", statusOut)
	}

	var output struct {
		TaskID        string `json:"task_id"`
		Status        string `json:"status"`
		Stdout        string `json:"stdout"`
		HasOutput     bool   `json:"has_output"`
		RawOutputPath string `json:"rawOutputPath"`
	}
	outputOut, err := decodeHarnessOutput(&output, func() (string, error) {
		return registry.Execute(ctx, "TaskOutputTool", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 2000
			}`, created.TaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if output.TaskID != created.TaskID || !output.HasOutput || output.Stdout != "task-output" {
		return localScenarioResult{}, fmt.Errorf("unexpected task output: %s", outputOut)
	}
	if _, err := os.Stat(output.RawOutputPath); err != nil {
		return localScenarioResult{}, fmt.Errorf("task raw output path missing: %w", err)
	}

	var updated struct {
		TaskID       string `json:"task_id"`
		MessageCount int    `json:"message_count"`
		LastMessage  string `json:"last_message"`
	}
	updateOut, err := decodeHarnessOutput(&updated, func() (string, error) {
		return registry.Execute(ctx, "task_update", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"message": "review logs"
			}`, created.TaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if updated.TaskID != created.TaskID || updated.MessageCount != 1 || updated.LastMessage != "review logs" {
		return localScenarioResult{}, fmt.Errorf("unexpected task update output: %s", updateOut)
	}

	var fetched struct {
		TaskID string `json:"task_id"`
		Task   struct {
			Messages []background.TaskMessage `json:"messages"`
		} `json:"task"`
	}
	getOut, err := decodeHarnessOutput(&fetched, func() (string, error) {
		return registry.Execute(ctx, "task_get", json.RawMessage(fmt.Sprintf(`{"id":%q}`, created.TaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if fetched.TaskID != created.TaskID || len(fetched.Task.Messages) != 1 || fetched.Task.Messages[0].Message != "review logs" {
		return localScenarioResult{}, fmt.Errorf("unexpected task get output: %s", getOut)
	}

	var listed struct {
		Total int `json:"total"`
		Tasks []struct {
			TaskID string `json:"task_id"`
		} `json:"tasks"`
	}
	listOut, err := decodeHarnessOutput(&listed, func() (string, error) {
		return registry.Execute(ctx, "task_list", json.RawMessage(`{"session_id":"session-task","kind":"parity"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if listed.Total != 1 || len(listed.Tasks) != 1 || listed.Tasks[0].TaskID != created.TaskID {
		return localScenarioResult{}, fmt.Errorf("unexpected task list output: %s", listOut)
	}

	var stopCreated struct {
		TaskID string `json:"task_id"`
	}
	stopCreateOut, err := decodeHarnessOutput(&stopCreated, func() (string, error) {
		return registry.Execute(ctx, "task_create", json.RawMessage(`{
				"command": "printf task-stop-ready; sleep 5",
				"kind": "parity",
				"session_id": "session-task"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if stopCreated.TaskID == "" {
		return localScenarioResult{}, fmt.Errorf("unexpected stoppable task output: %s", stopCreateOut)
	}
	defer func() {
		_, _ = registry.Execute(ctx, "task_stop", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, stopCreated.TaskID)), nil)
	}()

	stopReadyOut, err := registry.Execute(ctx, "task_output", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 2000
			}`, stopCreated.TaskID)), nil)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !strings.Contains(stopReadyOut, "task-stop-ready") {
		return localScenarioResult{}, fmt.Errorf("stoppable task did not produce readiness output: %s", stopReadyOut)
	}
	var stopped struct {
		TaskID      string `json:"task_id"`
		Status      string `json:"status"`
		Message     string `json:"message"`
		Interrupted bool   `json:"interrupted"`
	}
	stopOut, err := decodeHarnessOutput(&stopped, func() (string, error) {
		return registry.Execute(ctx, "TaskStopTool", json.RawMessage(fmt.Sprintf(`{"shell_id":%q}`, stopCreated.TaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if stopped.TaskID != stopCreated.TaskID || stopped.Status != "stopped" || stopped.Message != "Task stopped" {
		return localScenarioResult{}, fmt.Errorf("unexpected task stop output: %s", stopOut)
	}

	report := map[string]any{
		"kind": "task_lifecycle",
		"task": map[string]any{
			"id":           created.TaskID,
			"status":       output.Status,
			"stdout":       output.Stdout,
			"message":      updated.LastMessage,
			"listed_total": listed.Total,
			"raw_output":   filepath.Base(output.RawOutputPath),
		},
		"stopped": map[string]any{
			"id":          stopped.TaskID,
			"status":      stopped.Status,
			"interrupted": stopped.Interrupted,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "task lifecycle harness ok",
		RequestCount: 9,
		MessageCount: 1,
		ToolCalls:    9,
		ToolUses: []string{
			"task_create",
			"task_status",
			"task_output",
			"task_update",
			"task_get",
			"task_list",
			"task_create",
			"task_output",
			"task_stop",
		},
	}, nil
}

func taskPacketRoundtripScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	moduleDir := filepath.Join(workspace, "internal", "taskpacket")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "taskpacket.go"), []byte("package taskpacket\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	shim := filepath.Join(workspace, "task-packet-shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'packet:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
		return localScenarioResult{}, err
	}
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
		ConfigHome: configHome,
		Executable: shim,
	})

	var created struct {
		TaskID        string `json:"task_id"`
		Status        string `json:"status"`
		Description   string `json:"description"`
		Prompt        string `json:"prompt"`
		ResolvedScope struct {
			Scope        string `json:"scope"`
			Path         string `json:"path"`
			AbsolutePath string `json:"absolute_path"`
		} `json:"resolved_scope"`
		TaskPacket struct {
			Objective          string   `json:"objective"`
			Scope              string   `json:"scope"`
			ScopePath          string   `json:"scope_path"`
			Repo               string   `json:"repo"`
			Worktree           string   `json:"worktree"`
			AcceptanceCriteria []string `json:"acceptance_criteria"`
			Model              string   `json:"model"`
			Provider           string   `json:"provider"`
			PermissionProfile  string   `json:"permission_profile"`
			ReportingTargets   []string `json:"reporting_targets"`
			VerificationPlan   []string `json:"verification_plan"`
		} `json:"task_packet"`
		Task background.Task `json:"task"`
	}
	runOut, err := decodeHarnessOutput(&created, func() (string, error) {
		return registry.Execute(ctx, "RunTaskPacketTool", json.RawMessage(`{
				"objective": "Implement typed task packet parity",
				"scope": "module",
				"scope_path": "internal/taskpacket",
				"repo": "codog",
				"worktree": "reviewer",
				"branch_policy": "main only",
				"acceptance_tests": ["go test ./internal/taskpacket"],
				"acceptance_criteria": ["packet validates", "packet persists"],
				"resources": [{"kind": "module", "value": "internal/taskpacket"}],
				"model": "claude-test",
				"provider": "anthropic",
				"permission_profile": "workspace-write",
				"commit_policy": "single focused commit",
				"reporting_targets": ["leader"],
				"recovery_policy": "retry once with narrowed scope",
				"verification_plan": ["go test ./internal/taskpacket"]
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if created.TaskID == "" || created.Status != "running" || created.Description != "Implement typed task packet parity" {
		return localScenarioResult{}, fmt.Errorf("unexpected task packet create output: %s", runOut)
	}
	if created.Task.Kind != "task_packet" || created.Task.ID != created.TaskID || len(created.Task.TaskPacket) == 0 {
		return localScenarioResult{}, fmt.Errorf("task packet metadata was not persisted on task: %s", runOut)
	}
	if created.ResolvedScope.Scope != "module" || created.ResolvedScope.Path != "internal/taskpacket" || filepath.Clean(created.ResolvedScope.AbsolutePath) != filepath.Clean(moduleDir) {
		return localScenarioResult{}, fmt.Errorf("unexpected task packet scope resolution: %s", runOut)
	}
	if created.TaskPacket.Objective != "Implement typed task packet parity" ||
		created.TaskPacket.Scope != "module" ||
		created.TaskPacket.Repo != "codog" ||
		created.TaskPacket.Worktree != "reviewer" ||
		created.TaskPacket.Model != "claude-test" ||
		created.TaskPacket.Provider != "anthropic" ||
		created.TaskPacket.PermissionProfile != "workspace-write" ||
		!slices.Equal(created.TaskPacket.AcceptanceCriteria, []string{"packet validates", "packet persists"}) ||
		!slices.Equal(created.TaskPacket.ReportingTargets, []string{"leader"}) ||
		!slices.Equal(created.TaskPacket.VerificationPlan, []string{"go test ./internal/taskpacket"}) {
		return localScenarioResult{}, fmt.Errorf("unexpected task packet payload: %s", runOut)
	}
	var persisted map[string]any
	if err := json.Unmarshal(created.Task.TaskPacket, &persisted); err != nil {
		return localScenarioResult{}, err
	}
	if persisted["objective"] != "Implement typed task packet parity" || persisted["scope"] != "module" || persisted["provider"] != "anthropic" {
		return localScenarioResult{}, fmt.Errorf("unexpected persisted task packet: %#v", persisted)
	}

	outputOut, err := registry.Execute(ctx, "task_output", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 10000
			}`, created.TaskID)), nil)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(outputOut, "packet:prompt", "Implement typed task packet parity", "Verification plan:") {
		return localScenarioResult{}, fmt.Errorf("unexpected task packet output: %s", outputOut)
	}

	var fetched struct {
		TaskID string          `json:"task_id"`
		Task   background.Task `json:"task"`
	}
	getOut, err := decodeHarnessOutput(&fetched, func() (string, error) {
		return registry.Execute(ctx, "task_get", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if fetched.TaskID != created.TaskID || fetched.Task.Kind != "task_packet" || len(fetched.Task.TaskPacket) == 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected fetched task packet task: %s", getOut)
	}

	var stopped struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	stopOut, err := decodeHarnessOutput(&stopped, func() (string, error) {
		return registry.Execute(ctx, "task_stop", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if stopped.TaskID != created.TaskID || stopped.Status != "stopped" {
		return localScenarioResult{}, fmt.Errorf("unexpected task packet stop output: %s", stopOut)
	}

	report := map[string]any{
		"kind": "task_packet_roundtrip",
		"task_packet": map[string]any{
			"task_id":            created.TaskID,
			"scope":              created.TaskPacket.Scope,
			"scope_path":         created.TaskPacket.ScopePath,
			"repo":               created.TaskPacket.Repo,
			"model":              created.TaskPacket.Model,
			"provider":           created.TaskPacket.Provider,
			"permission_profile": created.TaskPacket.PermissionProfile,
			"criteria":           len(created.TaskPacket.AcceptanceCriteria),
			"verification_steps": len(created.TaskPacket.VerificationPlan),
			"stopped":            stopped.Status,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "task packet harness ok",
		RequestCount: 4,
		MessageCount: 1,
		ToolCalls:    4,
		ToolUses: []string{
			"run_task_packet",
			"task_output",
			"task_get",
			"task_stop",
		},
	}, nil
}

func teamCronLifecycleScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	shim := filepath.Join(workspace, "team-shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'team-shim:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
		return localScenarioResult{}, err
	}
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
		ConfigHome: configHome,
		Executable: shim,
	})

	var createdTeam struct {
		ID        string   `json:"team_id"`
		Name      string   `json:"name"`
		TaskCount int      `json:"task_count"`
		TaskIDs   []string `json:"task_ids"`
		Status    string   `json:"status"`
	}
	teamCreateOut, err := decodeHarnessOutput(&createdTeam, func() (string, error) {
		return registry.Execute(ctx, "TeamCreateTool", json.RawMessage(`{
				"name": "review",
				"session_id": "session-team",
				"tasks": [
					{"description": "auth", "prompt": "check auth flow"},
					{"description": "tests", "prompt": "check test suite"}
				]
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if createdTeam.ID == "" || createdTeam.Name != "review" || createdTeam.TaskCount != 2 || len(createdTeam.TaskIDs) != 2 || createdTeam.Status != "running" {
		return localScenarioResult{}, fmt.Errorf("unexpected team create output: %s", teamCreateOut)
	}
	defer func() {
		_, _ = registry.Execute(ctx, "team_delete", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
	}()

	taskStore := background.NewStore(configHome)
	if _, err := waitForBackgroundLogs(ctx, taskStore, createdTeam.TaskIDs[0], "Task: auth", 10*time.Second); err != nil {
		return localScenarioResult{}, err
	}
	if _, err := waitForBackgroundLogs(ctx, taskStore, createdTeam.TaskIDs[1], "check test suite", 10*time.Second); err != nil {
		return localScenarioResult{}, err
	}

	var listedTeams struct {
		Kind  string `json:"kind"`
		Total int    `json:"total"`
		Teams []struct {
			ID           string           `json:"team_id"`
			TaskStatuses []map[string]any `json:"task_statuses"`
		} `json:"teams"`
	}
	teamListOut, err := decodeHarnessOutput(&listedTeams, func() (string, error) {
		return registry.Execute(ctx, "team_list", json.RawMessage(`{"status":"running"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if listedTeams.Kind != "team_list" || listedTeams.Total != 1 || len(listedTeams.Teams) != 1 || listedTeams.Teams[0].ID != createdTeam.ID || len(listedTeams.Teams[0].TaskStatuses) != 2 {
		return localScenarioResult{}, fmt.Errorf("unexpected team list output: %s", teamListOut)
	}

	var fetchedTeam struct {
		Kind      string `json:"kind"`
		ID        string `json:"team_id"`
		TaskCount int    `json:"task_count"`
		Tasks     []struct {
			Description string `json:"description"`
			Prompt      string `json:"prompt"`
		} `json:"tasks"`
	}
	teamGetOut, err := decodeHarnessOutput(&fetchedTeam, func() (string, error) {
		return registry.Execute(ctx, "TeamGetTool", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if fetchedTeam.Kind != "team" || fetchedTeam.ID != createdTeam.ID || fetchedTeam.TaskCount != 2 || len(fetchedTeam.Tasks) != 2 || fetchedTeam.Tasks[0].Description != "auth" {
		return localScenarioResult{}, fmt.Errorf("unexpected team get output: %s", teamGetOut)
	}

	var deletedTeam struct {
		ID           string   `json:"team_id"`
		Status       string   `json:"status"`
		StoppedTasks []string `json:"stopped_tasks"`
		Message      string   `json:"message"`
	}
	teamDeleteOut, err := decodeHarnessOutput(&deletedTeam, func() (string, error) {
		return registry.Execute(ctx, "team_delete", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if deletedTeam.ID != createdTeam.ID || deletedTeam.Status != "deleted" || deletedTeam.Message != "Team deleted" || len(deletedTeam.StoppedTasks) != 2 {
		return localScenarioResult{}, fmt.Errorf("unexpected team delete output: %s", teamDeleteOut)
	}

	var createdCron struct {
		ID          string `json:"cron_id"`
		Schedule    string `json:"schedule"`
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
		Enabled     bool   `json:"enabled"`
	}
	cronCreateOut, err := decodeHarnessOutput(&createdCron, func() (string, error) {
		return registry.Execute(ctx, "CronCreateTool", json.RawMessage(`{
				"schedule": "0 9 * * 1",
				"prompt": "review weekly status",
				"description": "weekly review"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if createdCron.ID == "" || createdCron.Schedule != "0 9 * * 1" || createdCron.Prompt != "review weekly status" || createdCron.Description != "weekly review" || !createdCron.Enabled {
		return localScenarioResult{}, fmt.Errorf("unexpected cron create output: %s", cronCreateOut)
	}

	var listedCrons struct {
		Count int `json:"count"`
		Crons []struct {
			ID       string `json:"cron_id"`
			Schedule string `json:"schedule"`
		} `json:"crons"`
	}
	cronListOut, err := decodeHarnessOutput(&listedCrons, func() (string, error) { return registry.Execute(ctx, "cron_list", json.RawMessage(`{}`), nil) })
	if err != nil {
		return localScenarioResult{}, err
	}
	if listedCrons.Count != 1 || len(listedCrons.Crons) != 1 || listedCrons.Crons[0].ID != createdCron.ID || listedCrons.Crons[0].Schedule != createdCron.Schedule {
		return localScenarioResult{}, fmt.Errorf("unexpected cron list output: %s", cronListOut)
	}

	var deletedCron struct {
		ID      string `json:"cron_id"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	cronDeleteOut, err := decodeHarnessOutput(&deletedCron, func() (string, error) {
		return registry.Execute(ctx, "CronDeleteTool", json.RawMessage(fmt.Sprintf(`{"cron_id":%q}`, createdCron.ID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if deletedCron.ID != createdCron.ID || deletedCron.Status != "deleted" || deletedCron.Message != "Cron entry removed" {
		return localScenarioResult{}, fmt.Errorf("unexpected cron delete output: %s", cronDeleteOut)
	}

	report := map[string]any{
		"kind": "team_cron_lifecycle",
		"team": map[string]any{
			"id":            createdTeam.ID,
			"task_count":    createdTeam.TaskCount,
			"listed_total":  listedTeams.Total,
			"deleted":       deletedTeam.Status,
			"stopped_tasks": len(deletedTeam.StoppedTasks),
		},
		"cron": map[string]any{
			"id":       createdCron.ID,
			"schedule": createdCron.Schedule,
			"listed":   listedCrons.Count,
			"deleted":  deletedCron.Status,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "team cron lifecycle harness ok",
		RequestCount: 7,
		MessageCount: 1,
		ToolCalls:    7,
		ToolUses: []string{
			"team_create",
			"team_list",
			"team_get",
			"team_delete",
			"cron_create",
			"cron_list",
			"cron_delete",
		},
	}, nil
}

func workerLifecycleScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	shim := filepath.Join(workspace, "worker-shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'worker:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
		return localScenarioResult{}, err
	}
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
		ConfigHome:   configHome,
		Executable:   shim,
		TrustedRoots: []string{"repo-default", "shared"},
	})

	var created struct {
		WorkerID                     string   `json:"worker_id"`
		Status                       string   `json:"status"`
		ReadyForPrompt               bool     `json:"ready_for_prompt"`
		TrustedRoots                 []string `json:"trusted_roots"`
		AutoRecoverPromptMisdelivery bool     `json:"auto_recover_prompt_misdelivery"`
	}
	createOut, err := decodeHarnessOutput(&created, func() (string, error) {
		return registry.Execute(ctx, "WorkerCreateTool", json.RawMessage(`{
				"cwd": ".",
				"trusted_roots": ["shared", "."],
				"auto_recover_prompt_misdelivery": false
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if created.WorkerID == "" || created.Status != "ready_for_prompt" || !created.ReadyForPrompt || created.AutoRecoverPromptMisdelivery || !slices.Equal(created.TrustedRoots, []string{"repo-default", "shared", "."}) {
		return localScenarioResult{}, fmt.Errorf("unexpected worker create output: %s", createOut)
	}
	defer func() {
		_, _ = registry.Execute(ctx, "worker_terminate", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
	}()

	var listed struct {
		Kind    string `json:"kind"`
		Total   int    `json:"total"`
		Workers []struct {
			WorkerID string `json:"worker_id"`
		} `json:"workers"`
	}
	listOut, err := decodeHarnessOutput(&listed, func() (string, error) {
		return registry.Execute(ctx, "worker_list", json.RawMessage(`{"status":"ready_for_prompt"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if listed.Kind != "worker_list" || listed.Total != 1 || len(listed.Workers) != 1 || listed.Workers[0].WorkerID != created.WorkerID {
		return localScenarioResult{}, fmt.Errorf("unexpected worker list output: %s", listOut)
	}

	var ready struct {
		WorkerID       string `json:"worker_id"`
		Status         string `json:"status"`
		ReadyForPrompt bool   `json:"ready_for_prompt"`
	}
	readyOut, err := decodeHarnessOutput(&ready, func() (string, error) {
		return registry.Execute(ctx, "worker_await_ready", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if ready.WorkerID != created.WorkerID || ready.Status != "ready_for_prompt" || !ready.ReadyForPrompt {
		return localScenarioResult{}, fmt.Errorf("unexpected worker ready output: %s", readyOut)
	}

	var observed struct {
		Status         string `json:"status"`
		ReadyForPrompt bool   `json:"ready_for_prompt"`
	}
	observeOut, err := decodeHarnessOutput(&observed, func() (string, error) {
		return registry.Execute(ctx, "WorkerObserveTool", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"screen_text": "Do you trust this folder?"
			}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if observed.Status != "trust_prompt" || observed.ReadyForPrompt {
		return localScenarioResult{}, fmt.Errorf("unexpected worker observe output: %s", observeOut)
	}

	var resolved struct {
		Status         string `json:"status"`
		ReadyForPrompt bool   `json:"ready_for_prompt"`
		TrustResolved  bool   `json:"trust_resolved"`
	}
	resolveOut, err := decodeHarnessOutput(&resolved, func() (string, error) {
		return registry.Execute(ctx, "worker_resolve_trust", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if resolved.Status != "ready_for_prompt" || !resolved.ReadyForPrompt || !resolved.TrustResolved {
		return localScenarioResult{}, fmt.Errorf("unexpected worker trust resolution output: %s", resolveOut)
	}

	var sent struct {
		Status      string `json:"status"`
		TaskID      string `json:"task_id"`
		TaskReceipt struct {
			Repo             string `json:"repo"`
			TaskKind         string `json:"task_kind"`
			SourceSurface    string `json:"source_surface"`
			ObjectivePreview string `json:"objective_preview"`
		} `json:"task_receipt"`
	}
	sendOut, err := decodeHarnessOutput(&sent, func() (string, error) {
		return registry.Execute(ctx, "worker_send_prompt", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"prompt": "implement worker tests",
				"task_receipt": {
					"repo": "codog",
					"task_kind": "test",
					"source_surface": "tool",
					"objective_preview": "implement worker tests"
				}
			}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if sent.Status != "running" || sent.TaskID == "" || sent.TaskReceipt.Repo != "codog" || sent.TaskReceipt.ObjectivePreview != "implement worker tests" {
		return localScenarioResult{}, fmt.Errorf("unexpected worker send output: %s", sendOut)
	}
	if _, err := waitForBackgroundLogs(ctx, background.NewStore(configHome), sent.TaskID, "implement worker tests", 10*time.Second); err != nil {
		return localScenarioResult{}, err
	}

	var fetched struct {
		WorkerID   string `json:"worker_id"`
		Status     string `json:"status"`
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
	}
	getOut, err := decodeHarnessOutput(&fetched, func() (string, error) {
		return registry.Execute(ctx, "WorkerGetTool", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if fetched.WorkerID != created.WorkerID || fetched.Status != "running" || fetched.TaskID != sent.TaskID || fetched.TaskStatus == "" {
		return localScenarioResult{}, fmt.Errorf("unexpected worker get output: %s", getOut)
	}

	var restarted struct {
		Status string `json:"status"`
		TaskID string `json:"task_id"`
	}
	restartOut, err := decodeHarnessOutput(&restarted, func() (string, error) {
		return registry.Execute(ctx, "worker_restart", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if restarted.Status != "running" || restarted.TaskID == "" || restarted.TaskID == sent.TaskID {
		return localScenarioResult{}, fmt.Errorf("unexpected worker restart output: %s", restartOut)
	}

	var completed struct {
		Status string `json:"status"`
	}
	completeOut, err := decodeHarnessOutput(&completed, func() (string, error) {
		return registry.Execute(ctx, "worker_observe_completion", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"finish_reason": "stop",
				"tokens_output": 12
			}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if completed.Status != "finished" {
		return localScenarioResult{}, fmt.Errorf("unexpected worker completion output: %s", completeOut)
	}

	var timedOut struct {
		Status            string `json:"status"`
		LastError         string `json:"last_error"`
		StartupNoEvidence struct {
			Classification string `json:"classification"`
		} `json:"startup_no_evidence"`
	}
	timeoutOut, err := decodeHarnessOutput(&timedOut, func() (string, error) {
		return registry.Execute(ctx, "worker_startup_timeout", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"last_lifecycle_state": "trust_prompt",
				"pane_command": "codog repl",
				"transport_healthy": true,
				"mcp_healthy": true,
				"elapsed_seconds": 42,
				"trust_prompt_detected": true
			}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if timedOut.Status != "failed" || timedOut.LastError != "startup_no_evidence: trust_required" || timedOut.StartupNoEvidence.Classification != "trust_required" {
		return localScenarioResult{}, fmt.Errorf("unexpected worker startup timeout output: %s", timeoutOut)
	}

	var terminated struct {
		Status string `json:"status"`
	}
	terminateOut, err := decodeHarnessOutput(&terminated, func() (string, error) {
		return registry.Execute(ctx, "WorkerTerminateTool", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if terminated.Status != "terminated" {
		return localScenarioResult{}, fmt.Errorf("unexpected worker terminate output: %s", terminateOut)
	}

	report := map[string]any{
		"kind": "worker_lifecycle",
		"worker": map[string]any{
			"id":                created.WorkerID,
			"trusted_roots":     created.TrustedRoots,
			"trust_resolved":    resolved.TrustResolved,
			"prompt_task":       sent.TaskID,
			"restarted_task":    restarted.TaskID,
			"completion_status": completed.Status,
			"startup_failure":   timedOut.StartupNoEvidence.Classification,
			"terminal_status":   terminated.Status,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "worker lifecycle harness ok",
		RequestCount: 11,
		MessageCount: 1,
		ToolCalls:    11,
		ToolUses: []string{
			"worker_create",
			"worker_list",
			"worker_await_ready",
			"worker_observe",
			"worker_resolve_trust",
			"worker_send_prompt",
			"worker_get",
			"worker_restart",
			"worker_observe_completion",
			"worker_startup_timeout",
			"worker_terminate",
		},
	}, nil
}

type lspStaticCase struct {
	name     string
	input    json.RawMessage
	expected []string
}

func lspStaticCases(hintArgChar int) []lspStaticCase {
	return []lspStaticCase{
		{name: "symbols", input: json.RawMessage(`{"action":"symbols","path":"pkg/runner.go"}`), expected: []string{`"action": "symbols"`, `"source": "static"`, `"name": "Runner"`, `"name": "RunFast"`, `"total": 3`}},
		{name: "workspaceSymbols", input: json.RawMessage(`{"action":"workspace_symbol","query":"run","limit":3}`), expected: []string{`"action": "workspace-symbol"`, `"source": "static"`, `"query": "run"`, `"name": "Runner"`, `"name": "RunFast"`, `"total": 3`}},
		{name: "workspaceSymbolResolve", input: json.RawMessage(`{"action":"workspace_symbol_resolve","query":"RunFast"}`), expected: []string{`"action": "workspace-symbol-resolve"`, `"source": "static"`, `"found": true`, `"name": "RunFast"`, `"symbol": "RunFast"`, `"snippet": [`}},
		{name: "definition", input: json.RawMessage(`{"action":"definition","query":"RunFast"}`), expected: []string{`"action": "definition"`, `"found": true`, `"name": "RunFast"`, `"path": "pkg/runner.go"`}},
		{name: "declaration", input: json.RawMessage(`{"action":"declaration","query":"Runner"}`), expected: []string{`"action": "declaration"`, `"source": "static"`, `"found": true`, `"name": "Runner"`, `"path": "pkg/runner.go"`}},
		{name: "typeDefinition", input: json.RawMessage(`{"action":"type_definition","query":"Runner"}`), expected: []string{`"action": "type-definition"`, `"source": "static"`, `"found": true`, `"name": "Runner"`, `"path": "pkg/runner.go"`}},
		{name: "documentHighlight", input: json.RawMessage(`{"action":"document_highlight","path":"pkg/runner.go","query":"Runner","limit":3}`), expected: []string{`"action": "document-highlight"`, `"source": "static"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"character": 5`, `"total": 3`}},
		{name: "foldingRange", input: json.RawMessage(`{"action":"folding_range","path":"pkg/fold.go","limit":5}`), expected: []string{`"action": "folding-range"`, `"source": "static"`, `"path": "pkg/fold.go"`, `"startLine": 2`, `"endLine": 4`, `"total": 1`}},
		{name: "selectionRange", input: json.RawMessage(`{"action":"selection_range","path":"pkg/runner.go","line":4,"character":6,"limit":5}`), expected: []string{`"action": "selection-range"`, `"source": "static"`, `"path": "pkg/runner.go"`, `"kind": "Ident"`, `"character": 5`}},
		{name: "moniker", input: json.RawMessage(`{"action":"moniker","query":"RunFast"}`), expected: []string{`"action": "moniker"`, `"source": "static"`, `"scheme": "gomod"`, `"identifier": "example.test/harness/pkg.RunFast"`, `"kind": "export"`, `"unique": "project"`}},
		{name: "linkedEditing", input: json.RawMessage(`{"action":"linked_editing_range","path":"pkg/runner.go","query":"Runner","limit":3}`), expected: []string{`"action": "linked-editing-range"`, `"source": "static"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"wordPattern": "[A-Za-z_][A-Za-z0-9_]*"`, `"total": 3`}},
		{name: "documentLink", input: json.RawMessage(`{"action":"document_link","path":"pkg/links.go","limit":5}`), expected: []string{`"action": "document-link"`, `"source": "static"`, `"path": "pkg/links.go"`, `"target": "https://example.test/docs"`, `"character": 9`, `"total": 2`}},
		{name: "documentLinkResolve", input: json.RawMessage(`{"action":"document_link_resolve","path":"pkg/links.go","line":2,"character":12}`), expected: []string{`"action": "document-link-resolve"`, `"source": "static"`, `"found": true`, `"target": "https://example.test/docs"`}},
		{name: "documentColor", input: json.RawMessage(`{"action":"document_color","path":"pkg/colors.go","limit":5}`), expected: []string{`"action": "document-color"`, `"source": "static"`, `"path": "pkg/colors.go"`, `"text": "#336699"`, `"red": 0.2`, `"total": 1`}},
		{name: "colorPresentation", input: json.RawMessage(`{"action":"color_presentation","path":"pkg/colors.go","line":2,"character":18}`), expected: []string{`"action": "color-presentation"`, `"source": "static"`, `"found": true`, `"label": "#336699"`, `"label": "rgb(51, 102, 153)"`}},
		{name: "inlayHint", input: json.RawMessage(`{"action":"inlay_hint","path":"pkg/hints.go","limit":5}`), expected: []string{`"action": "inlay-hint"`, `"source": "static"`, `"path": "pkg/hints.go"`, `"label": "name:"`, `"label": "count:"`, `"kind": "parameter"`, `"total": 2`}},
		{name: "inlayHintResolve", input: json.RawMessage(fmt.Sprintf(`{"action":"inlay_hint_resolve","path":"pkg/hints.go","line":3,"character":%d}`, hintArgChar)), expected: []string{`"action": "inlay-hint-resolve"`, `"source": "static"`, `"found": true`, `"label": "name:"`, `"tooltip": "Build parameter 1"`}},
		{name: "signatureHelp", input: json.RawMessage(fmt.Sprintf(`{"action":"signature_help","path":"pkg/hints.go","line":3,"character":%d}`, hintArgChar)), expected: []string{`"action": "signature-help"`, `"source": "static"`, `"found": true`, `"function": "Build"`, `"label": "Build(name string, count int) int"`, `"activeParameter": 0`}},
		{name: "codeLens", input: json.RawMessage(`{"action":"code_lens","path":"pkg/runner.go","limit":5}`), expected: []string{`"action": "code-lens"`, `"source": "static"`, `"path": "pkg/runner.go"`, `"symbol": "Runner"`, `"command": "codog.references"`, `"total": 3`}},
		{name: "codeLensResolve", input: json.RawMessage(`{"action":"code_lens_resolve","path":"pkg/runner.go","line":2,"character":6}`), expected: []string{`"action": "code-lens-resolve"`, `"source": "static"`, `"found": true`, `"symbol": "Runner"`, `"command": "codog.references"`}},
		{name: "semanticTokens", input: json.RawMessage(`{"action":"semantic_tokens","path":"pkg/runner.go","limit":80}`), expected: []string{`"action": "semantic-tokens"`, `"source": "static"`, `"legend": [`, `"text": "Runner"`, `"type": "type"`, `"text": "RunFast"`, `"type": "function"`}},
		{name: "semanticTokensRange", input: json.RawMessage(`{"action":"semantic_tokens_range","path":"pkg/runner.go","line":2,"limit":20}`), expected: []string{`"action": "semantic-tokens-range"`, `"source": "static"`, `"text": "Runner"`, `"line": 2`}},
		{name: "semanticTokensDelta", input: json.RawMessage(`{"action":"semantic_tokens_delta","path":"pkg/runner.go","query":"previous-result","limit":80}`), expected: []string{`"action": "semantic-tokens-delta"`, `"source": "static"`, `"previousResultId": "previous-result"`, `"edits": []`}},
		{name: "prepareRename", input: json.RawMessage(`{"action":"prepare_rename","path":"pkg/runner.go","line":2,"character":6}`), expected: []string{`"action": "prepare-rename"`, `"source": "static"`, `"found": true`, `"symbol": "Runner"`, `"placeholder": "Runner"`}},
		{name: "rename", input: json.RawMessage(`{"action":"rename","query":"Runner","new_name":"RunnerRenamed","limit":20}`), expected: []string{`"action": "rename"`, `"source": "static"`, `"query": "Runner"`, `"newName": "RunnerRenamed"`, `"file_edits": 1`, "type RunnerRenamed struct{}"}},
		{name: "callHierarchy", input: json.RawMessage(`{"action":"prepare_call_hierarchy","query":"Build"}`), expected: []string{`"action": "prepare-call-hierarchy"`, `"source": "static"`, `"name": "Build"`, `"kind": "function"`, `"total": 1`}},
		{name: "incomingCalls", input: json.RawMessage(`{"action":"incoming_calls","query":"Build","limit":5}`), expected: []string{`"action": "call-hierarchy-incoming"`, `"source": "static"`, `"query": "Build"`, `"name": "UseBuild"`, `"name": "Build"`, `"total": 1`}},
		{name: "outgoingCalls", input: json.RawMessage(`{"action":"outgoing_calls","query":"UseBuild","limit":5}`), expected: []string{`"action": "call-hierarchy-outgoing"`, `"source": "static"`, `"query": "UseBuild"`, `"name": "Build"`, `"total": 1`}},
		{name: "typeHierarchy", input: json.RawMessage(`{"action":"prepare_type_hierarchy","query":"TypeBase"}`), expected: []string{`"action": "prepare-type-hierarchy"`, `"source": "static"`, `"name": "TypeBase"`, `"kind": "struct"`, `"total": 1`}},
		{name: "typeHierarchySupertypes", input: json.RawMessage(`{"action":"supertypes","query":"TypeChild","limit":5}`), expected: []string{`"action": "type-hierarchy-supertypes"`, `"source": "static"`, `"query": "TypeChild"`, `"name": "TypeBase"`, `"total": 1`}},
		{name: "typeHierarchySubtypes", input: json.RawMessage(`{"action":"subtypes","query":"TypeBase","limit":5}`), expected: []string{`"action": "type-hierarchy-subtypes"`, `"source": "static"`, `"query": "TypeBase"`, `"name": "TypeChild"`, `"total": 1`}},
		{name: "implementation", input: json.RawMessage(`{"action":"implementation","query":"TypeContract","limit":5}`), expected: []string{`"action": "implementation"`, `"source": "static"`, `"query": "TypeContract"`, `"name": "TypeChild"`, `"total": 1`}},
		{name: "references", input: json.RawMessage(`{"action":"references","query":"Runner","limit":10}`), expected: []string{`"action": "references"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"total": 3`}},
		{name: "hover", input: json.RawMessage(`{"action":"hover","query":"RunFast"}`), expected: []string{`"action": "hover"`, `"found": true`, `"kind": "function"`, `"symbol": "RunFast"`}},
		{name: "completion", input: json.RawMessage(`{"action":"completion","query":"Run","limit":5}`), expected: []string{`"action": "completion"`, `"label": "RunFast"`, `"kind": "function"`}},
		{name: "completionResolve", input: json.RawMessage(`{"action":"completion_resolve","query":"RunFast"}`), expected: []string{`"action": "completion-item-resolve"`, `"source": "static"`, `"found": true`, `"label": "RunFast"`, `"kind": "function"`}},
		{name: "rangeFormat", input: json.RawMessage(`{"action":"range_format","path":"pkg/messy.go","line":2,"character":10}`), expected: []string{`"action": "range-format"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"changed": true`, "func messy()"}},
		{name: "onTypeFormat", input: json.RawMessage(`{"action":"on_type_format","path":"pkg/messy.go","line":2,"character":18}`), expected: []string{`"action": "on-type-format"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"changed": true`}},
		{name: "willSave", input: json.RawMessage(`{"action":"will_save","path":"pkg/messy.go"}`), expected: []string{`"action": "will-save"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"edits": true`}},
		{name: "codeAction", input: json.RawMessage(`{"action":"code_action","path":"pkg/messy.go","line":2,"character":10}`), expected: []string{`"action": "code-action"`, `"source": "static"`, `"title": "Format Go file"`, `"kind": "source.format"`, `"title": "Fix all Go source"`, `"kind": "source.fixAll"`, `"total": 2`}},
		{name: "organizeAction", input: json.RawMessage(`{"action":"code_action","path":"pkg/imports.go","line":2,"character":10}`), expected: []string{`"action": "code-action"`, `"source": "static"`, `"title": "Organize Go imports"`, `"kind": "source.organizeImports"`, `"removed_imports": [`, `"bytes"`, `"duplicate_imports": [`, `"fmt"`}},
		{name: "codeActionResolve", input: json.RawMessage(`{"action":"code_action_resolve","path":"pkg/messy.go","query":"Format Go file"}`), expected: []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "Format Go file"`, `"title": "Format Go file"`, "func messy()"}},
		{name: "organizeResolve", input: json.RawMessage(`{"action":"code_action_resolve","path":"pkg/imports.go","query":"source.organizeImports"}`), expected: []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "source.organizeImports"`, `"title": "Organize Go imports"`, `"kind": "organize_imports"`, `"removed_imports": [`}},
		{name: "fixAllResolve", input: json.RawMessage(`{"action":"code_action_resolve","path":"pkg/imports.go","query":"source.fixAll"}`), expected: []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "source.fixAll"`, `"title": "Fix all Go source"`, `"kind": "fix_all"`, `"source.organizeImports"`}},
		{name: "inlineValue", input: json.RawMessage(`{"action":"inline_value","path":"pkg/inline.go","limit":5}`), expected: []string{`"action": "inline-value"`, `"source": "static"`, `"name": "InlineAnswer"`, `"text": "local = \"codog\""`, `"total": 2`}},
		{name: "executeCommand", input: json.RawMessage(`{"action":"execute_command","query":"format","path":"pkg/messy.go"}`), expected: []string{`"action": "execute-command"`, `"source": "static"`, `"command": "format"`, `"path": "pkg/messy.go"`, "func messy()"}},
		{name: "organizeCommand", input: json.RawMessage(`{"action":"execute_command","query":"source.organizeImports","path":"pkg/imports.go"}`), expected: []string{`"action": "execute-command"`, `"source": "static"`, `"command": "source.organizeimports"`, `"path": "pkg/imports.go"`, `"organize_imports": {`, `"removed_imports": [`}},
		{name: "fixAllCommand", input: json.RawMessage(`{"action":"execute_command","query":"source.fixAll","path":"pkg/imports.go"}`), expected: []string{`"action": "execute-command"`, `"source": "static"`, `"command": "source.fixall"`, `"path": "pkg/imports.go"`, `"fix_all": {`, `"kind": "fix_all"`, `"source.organizeImports"`}},
		{name: "documentDiagnostic", input: json.RawMessage(`{"action":"document_diagnostic","path":"pkg/broken.go"}`), expected: []string{`"action": "document-diagnostic"`, `"source": "static"`, `"path": "pkg/broken.go"`, `"total": 2`, "MissingSymbol"}},
		{name: "workspaceDiagnostic", input: json.RawMessage(`{"action":"workspace_diagnostic"}`), expected: []string{`"action": "workspace-diagnostic"`, `"source": "static"`, `"path": "pkg/broken.go"`, "MissingSymbol"}},
		{name: "diagnostics", input: json.RawMessage(`{"action":"diagnostics"}`), expected: []string{`"action": "diagnostics"`, `"path": "pkg/broken.go"`, `"line": 3`, "MissingSymbol"}},
		{name: "format", input: json.RawMessage(`{"action":"format","path":"pkg/messy.go"}`), expected: []string{`"action": "format"`, `"kind": "format"`, `"path": "pkg/messy.go"`, `"changed": true`, "func messy()"}},
	}
}

func executeLSPStaticCases(ctx context.Context, tool tools.LSPTool, cases []lspStaticCase) ([]string, error) {
	outputs := make([]string, 0, len(cases))
	for _, item := range cases {
		output, err := tool.Execute(ctx, item.input)
		if err != nil {
			return nil, fmt.Errorf("lsp %s: %w", item.name, err)
		}
		for _, expected := range item.expected {
			if !strings.Contains(output, expected) {
				return nil, fmt.Errorf("lsp %s output missing %s", item.name, expected)
			}
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}
