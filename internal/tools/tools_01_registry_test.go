package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/stretchr/testify/require"
)

func requireTaskIDAliasRequirement(t *testing.T, schema map[string]any, aliases ...string) {
	t.Helper()
	options, ok := schema["anyOf"].([]map[string]any)
	require.True(t, ok)

	seen := map[string]bool{}
	for _, option := range options {
		required, ok := option["required"].([]string)
		require.True(t, ok)
		if len(required) == 1 {
			seen[required[0]] = true
		}
	}
	for _, alias := range aliases {
		require.True(t, seen[alias], "missing task id alias %q", alias)
	}
}

func TestRegistryExecutesClaudeToolAliases(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha\n"), 0o644))
	registry := NewRegistry(workspace)

	out, err := registry.Execute(context.Background(), "Read", []byte(`{"path":"notes.txt"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, "alpha")

	out, err = registry.Execute(context.Background(), "Read", []byte(`{"file_path":"notes.txt"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, "alpha")

	out, err = registry.Execute(context.Background(), "Bash", []byte(`{"command":"printf alias-ok"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "alias-ok"`)

	for alias, canonical := range map[string]string{
		"AgentTool":                    "agent",
		"ApprovalToken":                "approval_token",
		"ApprovalTokenTool":            "approval_token",
		"AskUserQuestionTool":          "ask_user_question",
		"Brief":                        "brief",
		"BriefTool":                    "brief",
		"Config":                       "config",
		"ConfigTool":                   "config",
		"CronCreate":                   "cron_create",
		"CronCreateTool":               "cron_create",
		"CronDelete":                   "cron_delete",
		"CronDeleteTool":               "cron_delete",
		"CronList":                     "cron_list",
		"CronListTool":                 "cron_list",
		"EnterPlanMode":                "enter_plan_mode",
		"EnterPlanModeTool":            "enter_plan_mode",
		"EnterWorktree":                "enter_worktree",
		"EnterWorktreeTool":            "enter_worktree",
		"ExitPlanMode":                 "exit_plan_mode",
		"ExitPlanModeTool":             "exit_plan_mode",
		"ExitPlanModeV2":               "exit_plan_mode",
		"ExitPlanModeV2Tool":           "exit_plan_mode",
		"ExitWorktree":                 "exit_worktree",
		"ExitWorktreeTool":             "exit_worktree",
		"BashTool":                     "bash",
		"EditTool":                     "edit_file",
		"EditFile":                     "edit_file",
		"FileEdit":                     "edit_file",
		"FileEditTool":                 "edit_file",
		"FileRead":                     "read_file",
		"FileReadTool":                 "read_file",
		"FileWrite":                    "write_file",
		"FileWriteTool":                "write_file",
		"BranchFreshness":              "branch_freshness",
		"BranchFreshnessTool":          "branch_freshness",
		"GitBlame":                     "git_blame",
		"GitBlameTool":                 "git_blame",
		"GitDiff":                      "git_diff",
		"GitDiffTool":                  "git_diff",
		"GitLog":                       "git_log",
		"GitLogTool":                   "git_log",
		"GitShow":                      "git_show",
		"GitShowTool":                  "git_show",
		"GitStatus":                    "git_status",
		"GitStatusTool":                "git_status",
		"PolicyEvaluate":               "policy_evaluate",
		"PolicyEvaluateTool":           "policy_evaluate",
		"GlobTool":                     "glob",
		"GlobSearch":                   "glob",
		"GlobSearchTool":               "glob",
		"GrepTool":                     "grep",
		"GrepSearch":                   "grep",
		"GrepSearchTool":               "grep",
		"LSPTool":                      "lsp",
		"LSTool":                       "ls",
		"MCP":                          "mcp",
		"MCPTool":                      "mcp",
		"MultiEditFile":                "multi_edit",
		"MultiEditTool":                "multi_edit",
		"NotebookEditTool":             "notebook_edit",
		"NotebookReadTool":             "notebook_read",
		"Nudge":                        "nudge",
		"NudgeTool":                    "nudge",
		"PowerShellTool":               "powershell",
		"ReadFile":                     "read_file",
		"ReadTool":                     "read_file",
		"WriteFile":                    "write_file",
		"WriteTool":                    "write_file",
		"AgentOutputTool":              "task_output",
		"BashOutputTool":               "bash_output",
		"GetMcpPromptTool":             "get_mcp_prompt",
		"KillShell":                    "task_stop",
		"ListMcpPromptsTool":           "list_mcp_prompts",
		"ListMcpResourcesTool":         "list_mcp_resources",
		"ListMcpResourceTemplatesTool": "list_mcp_resource_templates",
		"McpAuthTool":                  "mcp_auth",
		"ReadMcpResourceTool":          "read_mcp_resource",
		"RemoteTrigger":                "remote_trigger",
		"RemoteTriggerTool":            "remote_trigger",
		"ReportBackpressure":           "report_backpressure",
		"ReportBackpressureTool":       "report_backpressure",
		"RunTaskPacket":                "run_task_packet",
		"RunTaskPacketTool":            "run_task_packet",
		"SendMessage":                  "send_user_message",
		"SendMessageTool":              "send_user_message",
		"SendUserMessage":              "send_user_message",
		"SendUserMessageTool":          "send_user_message",
		"Skill":                        "skill",
		"SkillTool":                    "skill",
		"SleepTool":                    "sleep",
		"REPLTool":                     "repl",
		"StructuredOutput":             "structured_output",
		"StructuredOutputTool":         "structured_output",
		"SyntheticOutputTool":          "structured_output",
		"TaskCreate":                   "task_create",
		"TaskCreateTool":               "task_create",
		"TaskGet":                      "task_get",
		"TaskGetTool":                  "task_get",
		"TaskHeartbeat":                "task_heartbeat",
		"TaskHeartbeatTool":            "task_heartbeat",
		"TaskLaneBoard":                "task_lane_board",
		"TaskLaneBoardTool":            "task_lane_board",
		"TaskList":                     "task_list",
		"TaskListTool":                 "task_list",
		"TaskOutput":                   "task_output",
		"TaskOutputTool":               "task_output",
		"TaskStatus":                   "task_status",
		"TaskStatusTool":               "task_status",
		"TaskStop":                     "task_stop",
		"TaskStopTool":                 "task_stop",
		"TaskSupervise":                "task_supervise",
		"TaskSuperviseTool":            "task_supervise",
		"TaskUpdate":                   "task_update",
		"TaskUpdateTool":               "task_update",
		"TeamCreate":                   "team_create",
		"TeamCreateTool":               "team_create",
		"TeamDelete":                   "team_delete",
		"TeamDeleteTool":               "team_delete",
		"TeamGet":                      "team_get",
		"TeamGetTool":                  "team_get",
		"TeamList":                     "team_list",
		"TeamListTool":                 "team_list",
		"PermissionCheck":              "permission_check",
		"PermissionCheckTool":          "permission_check",
		"TestingPermission":            "permission_check",
		"TestingPermissionTool":        "permission_check",
		"TodoReadTool":                 "todo_read",
		"TodoWriteTool":                "todo_write",
		"ToolSearch":                   "tool_search",
		"ToolSearchTool":               "tool_search",
		"WebFetchTool":                 "web_fetch",
		"WebSearchTool":                "web_search",
		"WorkerAwaitReady":             "worker_await_ready",
		"WorkerAwaitReadyTool":         "worker_await_ready",
		"WorkerCreate":                 "worker_create",
		"WorkerCreateTool":             "worker_create",
		"WorkerGet":                    "worker_get",
		"WorkerGetTool":                "worker_get",
		"WorkerList":                   "worker_list",
		"WorkerListTool":               "worker_list",
		"WorkerObserve":                "worker_observe",
		"WorkerObserveTool":            "worker_observe",
		"WorkerObserveCompletion":      "worker_observe_completion",
		"WorkerObserveCompletionTool":  "worker_observe_completion",
		"WorkerResolveTrust":           "worker_resolve_trust",
		"WorkerResolveTrustTool":       "worker_resolve_trust",
		"WorkerRestart":                "worker_restart",
		"WorkerRestartTool":            "worker_restart",
		"WorkerSendPrompt":             "worker_send_prompt",
		"WorkerSendPromptTool":         "worker_send_prompt",
		"WorkerStartupTimeout":         "worker_startup_timeout",
		"WorkerStartupTimeoutTool":     "worker_startup_timeout",
		"WorkerTerminate":              "worker_terminate",
		"WorkerTerminateTool":          "worker_terminate",
		"RecoveryRecipe":               "recovery_recipe",
		"RecoveryRecipeTool":           "recovery_recipe",
		"RecoveryAttempt":              "recovery_attempt",
		"RecoveryAttemptTool":          "recovery_attempt",
		"RecoveryStatus":               "recovery_status",
		"RecoveryStatusTool":           "recovery_status",
		"RoadmapPinpoint":              "roadmap_pinpoint",
		"RoadmapPinpointTool":          "roadmap_pinpoint",
	} {
		info, ok := registry.Info(alias)
		require.True(t, ok, alias)
		require.Equal(t, canonical, info.Name, alias)
	}

	out, err = registry.Execute(context.Background(), "PermissionCheck", []byte(`{"target_tool":"Bash","input":{"command":"pwd"}}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"target_tool": "bash"`)
	require.Contains(t, out, `"known_tool": true`)
	require.Contains(t, out, `"required_permission": "danger-full-access"`)
}

func TestRegistryUnknownToolSuggestsCanonicalName(t *testing.T) {
	registry := NewRegistry(t.TempDir())

	_, err := registry.Execute(context.Background(), "read_fil", []byte(`{}`), nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown tool "read_fil"`)
	require.Contains(t, err.Error(), `did you mean "read_file"`)
}

func TestFileToolsAcceptClaudeFilePathParameter(t *testing.T) {
	workspace := t.TempDir()
	registry := NewRegistry(workspace)

	out, err := registry.Execute(context.Background(), "Write", []byte(`{"file_path":"notes.txt","content":"alpha beta alpha\n"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "create"`)

	out, err = registry.Execute(context.Background(), "Edit", []byte(`{"file_path":"notes.txt","old_string":"beta","new_string":"gamma"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"replacements": 1`)

	out, err = registry.Execute(context.Background(), "MultiEdit", []byte(`{"file_path":"notes.txt","edits":[{"old_string":"alpha","new_string":"delta","replace_all":true}]}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"replacements": 2`)

	data, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, "delta gamma delta\n", string(data))
}

func TestFileWriteAndEditReturnStructuredPatchMetadata(t *testing.T) {
	workspace := t.TempDir()
	registry := NewRegistry(workspace)

	type patchHunk struct {
		OldStart int      `json:"oldStart"`
		OldLines int      `json:"oldLines"`
		NewStart int      `json:"newStart"`
		NewLines int      `json:"newLines"`
		Lines    []string `json:"lines"`
	}
	type writeReport struct {
		Kind            string      `json:"kind"`
		Type            string      `json:"type"`
		FilePath        string      `json:"filePath"`
		Content         string      `json:"content"`
		OriginalFile    *string     `json:"originalFile"`
		StructuredPatch []patchHunk `json:"structuredPatch"`
	}
	type editReport struct {
		FilePath        string      `json:"filePath"`
		OldString       string      `json:"oldString"`
		NewString       string      `json:"newString"`
		OriginalFile    string      `json:"originalFile"`
		StructuredPatch []patchHunk `json:"structuredPatch"`
		UserModified    bool        `json:"userModified"`
		ReplaceAll      bool        `json:"replaceAll"`
		Replacements    int         `json:"replacements"`
	}
	type multiEditReport struct {
		FilePath        string      `json:"filePath"`
		OriginalFile    string      `json:"originalFile"`
		StructuredPatch []patchHunk `json:"structuredPatch"`
		Edits           int         `json:"edits"`
		Replacements    int         `json:"replacements"`
	}

	out, err := registry.Execute(context.Background(), "Write", []byte(`{"file_path":"notes.txt","content":"alpha\nbeta\n"}`), nil)
	require.NoError(t, err)
	expectedPath, err := filepath.EvalSymlinks(filepath.Join(workspace, "notes.txt"))
	require.NoError(t, err)
	var created writeReport
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	require.Equal(t, "create", created.Kind)
	require.Equal(t, "create", created.Type)
	require.Equal(t, expectedPath, created.FilePath)
	require.Equal(t, "alpha\nbeta\n", created.Content)
	require.Nil(t, created.OriginalFile)
	require.Equal(t, []patchHunk{{
		OldStart: 1,
		OldLines: 0,
		NewStart: 1,
		NewLines: 2,
		Lines:    []string{"+alpha", "+beta"},
	}}, created.StructuredPatch)

	out, err = registry.Execute(context.Background(), "Write", []byte(`{"file_path":"notes.txt","content":"gamma\n"}`), nil)
	require.NoError(t, err)
	var updated writeReport
	require.NoError(t, json.Unmarshal([]byte(out), &updated))
	require.Equal(t, "update", updated.Kind)
	require.Equal(t, "update", updated.Type)
	require.Equal(t, expectedPath, updated.FilePath)
	require.NotNil(t, updated.OriginalFile)
	require.Equal(t, "alpha\nbeta\n", *updated.OriginalFile)
	require.Equal(t, []patchHunk{{
		OldStart: 1,
		OldLines: 2,
		NewStart: 1,
		NewLines: 1,
		Lines:    []string{"-alpha", "-beta", "+gamma"},
	}}, updated.StructuredPatch)

	out, err = registry.Execute(context.Background(), "Edit", []byte(`{"file_path":"notes.txt","old_string":"gamma","new_string":"delta"}`), nil)
	require.NoError(t, err)
	var edited editReport
	require.NoError(t, json.Unmarshal([]byte(out), &edited))
	require.Equal(t, expectedPath, edited.FilePath)
	require.Equal(t, "gamma", edited.OldString)
	require.Equal(t, "delta", edited.NewString)
	require.Equal(t, "gamma\n", edited.OriginalFile)
	require.False(t, edited.UserModified)
	require.False(t, edited.ReplaceAll)
	require.Equal(t, 1, edited.Replacements)
	require.Equal(t, []patchHunk{{
		OldStart: 1,
		OldLines: 1,
		NewStart: 1,
		NewLines: 1,
		Lines:    []string{"-gamma", "+delta"},
	}}, edited.StructuredPatch)

	out, err = registry.Execute(context.Background(), "MultiEdit", []byte(`{"file_path":"notes.txt","edits":[{"old_string":"delta","new_string":"omega"},{"old_string":"omega","new_string":"done"}]}`), nil)
	require.NoError(t, err)
	var multiEdited multiEditReport
	require.NoError(t, json.Unmarshal([]byte(out), &multiEdited))
	require.Equal(t, expectedPath, multiEdited.FilePath)
	require.Equal(t, "delta\n", multiEdited.OriginalFile)
	require.Equal(t, 2, multiEdited.Edits)
	require.Equal(t, 2, multiEdited.Replacements)
	require.Equal(t, []patchHunk{{
		OldStart: 1,
		OldLines: 1,
		NewStart: 1,
		NewLines: 1,
		Lines:    []string{"-delta", "+done"},
	}}, multiEdited.StructuredPatch)
}

func TestFileToolsRecordUndoSnapshots(t *testing.T) {
	workspace := t.TempDir()
	registry := NewRegistry(workspace)

	out, err := registry.Execute(context.Background(), "Write", []byte(`{"file_path":"created.txt","content":"created\n"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"undo_available": true`)
	require.Contains(t, out, `"undo_id":`)
	report, err := undo.RestoreLast(workspace)
	require.NoError(t, err)
	require.True(t, report.Removed)
	require.NoFileExists(t, filepath.Join(workspace, "created.txt"))

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha beta alpha\n"), 0o644))
	out, err = registry.Execute(context.Background(), "Edit", []byte(`{"file_path":"notes.txt","old_string":"beta","new_string":"gamma"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"undo_id":`)
	report, err = undo.RestoreLast(workspace)
	require.NoError(t, err)
	require.True(t, report.Restored)
	data, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, "alpha beta alpha\n", string(data))

	out, err = registry.Execute(context.Background(), "MultiEdit", []byte(`{"file_path":"notes.txt","edits":[{"old_string":"alpha","new_string":"delta","replace_all":true}]}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"undo_id":`)
	report, err = undo.RestoreLast(workspace)
	require.NoError(t, err)
	require.True(t, report.Restored)
	data, err = os.ReadFile(filepath.Join(workspace, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, "alpha beta alpha\n", string(data))
}

func TestApplyPatchToolAppliesUnifiedDiffAndRecordsUndo(t *testing.T) {
	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha\nbeta\nomega\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "remove.txt"), []byte("gone\n"), 0o644))

	patch := strings.Join([]string{
		"--- a/notes.txt",
		"+++ b/notes.txt",
		"@@ -1,3 +1,3 @@",
		" alpha",
		"-beta",
		"+gamma",
		" omega",
		"--- /dev/null",
		"+++ b/new.txt",
		"@@ -0,0 +1,2 @@",
		"+new",
		"+file",
		"--- a/remove.txt",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-gone",
	}, "\n")
	input, err := json.Marshal(map[string]string{"patch": patch})
	require.NoError(t, err)

	out, err := registry.Execute(context.Background(), "ApplyPatch", input, nil)
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "apply_patch"`)
	require.Contains(t, out, `"files_changed": 3`)
	require.Contains(t, out, `"operation": "update"`)
	require.Contains(t, out, `"operation": "create"`)
	require.Contains(t, out, `"operation": "delete"`)
	require.Contains(t, out, `"undo_id":`)

	data, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, "alpha\ngamma\nomega\n", string(data))
	data, err = os.ReadFile(filepath.Join(workspace, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "new\nfile\n", string(data))
	require.NoFileExists(t, filepath.Join(workspace, "remove.txt"))

	report, err := undo.RestoreLast(workspace)
	require.NoError(t, err)
	require.True(t, report.Restored)
	data, err = os.ReadFile(filepath.Join(workspace, "remove.txt"))
	require.NoError(t, err)
	require.Equal(t, "gone\n", string(data))
}

func TestApplyPatchToolRejectsEscapingPathWithoutChangingWorkspace(t *testing.T) {
	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	outside := filepath.Join(filepath.Dir(workspace), "outside.txt")

	patch := strings.Join([]string{
		"--- /dev/null",
		"+++ " + filepath.ToSlash(outside),
		"@@ -0,0 +1 @@",
		"+nope",
	}, "\n")
	input, err := json.Marshal(map[string]string{"patch": patch})
	require.NoError(t, err)

	_, err = registry.Execute(context.Background(), "apply_patch", input, nil)
	require.Error(t, err)
	require.NoFileExists(t, outside)
}

func TestApplyPatchToolHandlesHunkLinesThatLookLikeHeaders(t *testing.T) {
	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("-- heading\nkeep\n"), 0o644))

	patch := strings.Join([]string{
		"--- a/notes.txt",
		"+++ b/notes.txt",
		"@@ -1,2 +1,2 @@",
		"--- heading",
		"+++ heading",
		" keep",
	}, "\n")
	input, err := json.Marshal(map[string]string{"patch": patch})
	require.NoError(t, err)

	_, err = registry.Execute(context.Background(), "apply_patch", input, nil)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, "++ heading\nkeep\n", string(data))
}

func TestApplyPatchToolHandlesPathsWithSpaces(t *testing.T) {
	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "file with spaces.txt"), []byte("old\n"), 0o644))

	patch := strings.Join([]string{
		"--- a/file with spaces.txt",
		"+++ b/file with spaces.txt",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n")
	input, err := json.Marshal(map[string]string{"patch": patch})
	require.NoError(t, err)

	_, err = registry.Execute(context.Background(), "apply_patch", input, nil)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(workspace, "file with spaces.txt"))
	require.NoError(t, err)
	require.Equal(t, "new\n", string(data))
}

func TestReadFileToolReadsImages(t *testing.T) {
	workspace := t.TempDir()
	imageData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pixel.png"), imageData, 0o644))

	out, err := NewRegistry(workspace).Execute(context.Background(), "Read", []byte(`{"file_path":"pixel.png"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "image"`)
	require.Contains(t, out, `"media_type": "image/png"`)
	require.Contains(t, out, `"encoding": "base64"`)
	require.Contains(t, out, `"width": 1`)
	require.Contains(t, out, `"height": 1`)
	require.Contains(t, out, base64.StdEncoding.EncodeToString(imageData))
}

func TestTodoToolsReadAndWriteWorkspaceTodos(t *testing.T) {
	workspace := t.TempDir()
	writeOut, err := TodoWriteTool{Workspace: workspace}.Execute(context.Background(), []byte(`{
		"todos": [
			{
				"content": "write tests",
				"activeForm": "writing tests",
				"status": "pending",
				"priority": "high"
			}
		]
	}`))
	require.NoError(t, err)
	var writeReport struct {
		Kind     string `json:"kind"`
		Total    int    `json:"total"`
		OldTodos []struct {
			Content string `json:"content"`
		} `json:"oldTodos"`
		NewTodos []struct {
			Content    string `json:"content"`
			ActiveForm string `json:"activeForm"`
			Status     string `json:"status"`
		} `json:"newTodos"`
		VerificationNudgeNeeded bool `json:"verificationNudgeNeeded"`
	}
	require.NoError(t, json.Unmarshal([]byte(writeOut), &writeReport))
	require.Equal(t, "todos", writeReport.Kind)
	require.Equal(t, 1, writeReport.Total)
	require.Empty(t, writeReport.OldTodos)
	require.Len(t, writeReport.NewTodos, 1)
	require.Equal(t, "write tests", writeReport.NewTodos[0].Content)
	require.Equal(t, "writing tests", writeReport.NewTodos[0].ActiveForm)
	require.False(t, writeReport.VerificationNudgeNeeded)
	var writeRaw map[string]any
	require.NoError(t, json.Unmarshal([]byte(writeOut), &writeRaw))
	require.NotContains(t, writeRaw, "verificationNudgeNeeded")

	readOut, err := TodoReadTool{Workspace: workspace}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, readOut, "write tests")

	clearOut, err := TodoWriteTool{Workspace: workspace}.Execute(context.Background(), []byte(`{
		"todos": [
			{
				"content": "write tests",
				"activeForm": "writing tests",
				"status": "completed",
				"priority": "high"
			},
			{
				"content": "fix errors",
				"activeForm": "fixing errors",
				"status": "completed",
				"priority": "medium"
			},
			{
				"content": "ship branch",
				"activeForm": "shipping branch",
				"status": "completed",
				"priority": "low"
			}
		]
	}`))
	require.NoError(t, err)
	var clearReport struct {
		Total    int `json:"total"`
		OldTodos []struct {
			Content string `json:"content"`
		} `json:"oldTodos"`
		NewTodos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"newTodos"`
		VerificationNudgeNeeded bool `json:"verificationNudgeNeeded"`
	}
	require.NoError(t, json.Unmarshal([]byte(clearOut), &clearReport))
	require.Equal(t, 0, clearReport.Total)
	require.Len(t, clearReport.OldTodos, 1)
	require.Equal(t, "write tests", clearReport.OldTodos[0].Content)
	require.Len(t, clearReport.NewTodos, 3)
	require.Equal(t, "completed", clearReport.NewTodos[2].Status)
	require.True(t, clearReport.VerificationNudgeNeeded)
	var clearRaw map[string]any
	require.NoError(t, json.Unmarshal([]byte(clearOut), &clearRaw))
	require.Equal(t, true, clearRaw["verificationNudgeNeeded"])
	readOut, err = TodoReadTool{Workspace: workspace}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.NotContains(t, readOut, "write tests")

	registry := NewRegistry(workspace)
	info, ok := registry.Info("todo_write")
	require.True(t, ok)
	require.Equal(t, PermissionWorkspace, info.Permission)
	info, ok = registry.Info("todo_read")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
}

func TestTodoWriteRejectsInvalidPayloads(t *testing.T) {
	workspace := t.TempDir()
	tool := TodoWriteTool{Workspace: workspace}

	_, err := tool.Execute(context.Background(), []byte(`{"todos":[]}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "todos must not be empty")

	_, err = tool.Execute(context.Background(), []byte(`{
		"todos": [
			{"content": "   ", "activeForm": "Doing it", "status": "pending"}
		]
	}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "todo content must not be empty")

	_, err = tool.Execute(context.Background(), []byte(`{
		"todos": [
			{"content": "Do it", "activeForm": "   ", "status": "pending"}
		]
	}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "todo activeForm must not be empty")

	out, err := tool.Execute(context.Background(), []byte(`{
		"todos": [
			{"content": "One", "activeForm": "Doing one", "status": "in_progress"},
			{"content": "Two", "activeForm": "Doing two", "status": "in_progress"}
		]
	}`))
	require.NoError(t, err)
	require.Contains(t, out, `"total": 2`)
}

func TestWebToolsFetchAndSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>Local</title></head><body><p>Hello web tool.</p></body></html>`)
		case "/search":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<a class="result__a" href="https://example.com/result">Example Result</a><div class="result__snippet">A local search summary.</div>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("CODOG_WEB_SEARCH_BASE_URL", server.URL+"/search")

	fetchOut, err := WebFetchTool{}.Execute(context.Background(), []byte(`{"url":"`+server.URL+`/page","prompt":"title"}`))
	require.NoError(t, err)
	require.Contains(t, fetchOut, `"title": "Local"`)
	require.Contains(t, fetchOut, `"summary": "Title: Local"`)
	require.Contains(t, fetchOut, `"code": 200`)
	require.Contains(t, fetchOut, `"codeText": "OK"`)
	require.Contains(t, fetchOut, `"result": "Title: Local"`)
	require.Contains(t, fetchOut, `"durationMs":`)

	_, err = WebFetchTool{}.Execute(context.Background(), []byte(`{"url":"`+server.URL+`/page"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "prompt is required")

	searchOut, err := WebSearchTool{}.Execute(context.Background(), []byte(`{"query":"local result"}`))
	require.NoError(t, err)
	require.Contains(t, searchOut, `"title": "Example Result"`)
	require.Contains(t, searchOut, `"url": "https://example.com/result"`)
	require.Contains(t, searchOut, `"snippet": "A local search summary."`)
	require.Contains(t, searchOut, `"tool_use_id": "web_search_1"`)
	require.Contains(t, searchOut, `"hits":`)
	require.Contains(t, searchOut, `"durationSeconds":`)
	var searchReport struct {
		Results []json.RawMessage `json:"results"`
		Hits    []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"hits"`
	}
	require.NoError(t, json.Unmarshal([]byte(searchOut), &searchReport))
	require.Len(t, searchReport.Results, 2)
	var commentary string
	require.NoError(t, json.Unmarshal(searchReport.Results[0], &commentary))
	require.Contains(t, commentary, "Search results for")
	require.Contains(t, commentary, "Include a Sources section")
	var searchBlock struct {
		ToolUseID string `json:"tool_use_id"`
		Content   []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(searchReport.Results[1], &searchBlock))
	require.Equal(t, "web_search_1", searchBlock.ToolUseID)
	require.Equal(t, "Example Result", searchBlock.Content[0].Title)
	require.Equal(t, len(searchBlock.Content), len(searchReport.Hits))
	require.Equal(t, searchBlock.Content[0].Title, searchReport.Hits[0].Title)
	require.Equal(t, searchBlock.Content[0].URL, searchReport.Hits[0].URL)

	_, err = WebSearchTool{}.Execute(context.Background(), []byte(`{"query":"x"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least 2 characters")

	_, err = WebSearchTool{}.Execute(context.Background(), []byte(`{"query":"local result","allowed_domains":["example.com"],"blocked_domains":["example.org"]}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "allowed_domains")

	registry := NewRegistry(t.TempDir())
	info, ok := registry.Info("web_fetch")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	require.ElementsMatch(t, []string{"url", "prompt"}, info.InputSchema["required"])
	info, ok = registry.Info("web_search")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	properties := info.InputSchema["properties"].(map[string]any)
	querySchema := properties["query"].(map[string]any)
	require.Equal(t, 2, querySchema["minLength"])
}

func TestRetrieveContextToolQueriesConfiguredRAGService(t *testing.T) {
	var received struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/query", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		fmt.Fprint(w, `{"phase":"1-sqlite","hits":[{"path":"internal/tools/tools.go","score":0.875,"snippet":"type RetrieveContextTool struct{}\nfunc example() {}"}]}`)
	}))
	defer server.Close()

	tool := RetrieveContextTool{BaseURL: server.URL + "/", TopKMax: 5, Timeout: time.Second}
	out, err := tool.Execute(context.Background(), []byte(`{"query":" where is RAG implemented? ","top_k":99}`))
	require.NoError(t, err)
	require.Equal(t, "where is RAG implemented?", received.Query)
	require.Equal(t, 5, received.TopK)
	require.Contains(t, out, "phase: 1-sqlite")
	require.Contains(t, out, "score=0.8750 path=internal/tools/tools.go")
	require.Contains(t, out, "    type RetrieveContextTool struct{}")

	_, err = tool.Execute(context.Background(), []byte(`{"query":"   "}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "query is required")
}

func TestRetrieveContextToolRejectsUnknownRAGPhase(t *testing.T) {
	_, err := formatRAGQueryJSONForModel([]byte(`{"phase":"3-drifted","hits":[]}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown_bootstrap_phase")
	require.Contains(t, err.Error(), "3-drifted")
}

func TestRegistryRegistersRetrieveContextWhenRAGConfigured(t *testing.T) {
	t.Setenv("RAG_BASE_URL", "")
	registry := NewRegistry(t.TempDir())
	require.False(t, registry.Has("retrieve_context"))

	registry = NewRegistryWithOptions(t.TempDir(), RegistryOptions{RAGBaseURL: "http://127.0.0.1:1234", RAGTopKMax: 3})
	info, ok := registry.Info("RetrieveContextTool")
	require.True(t, ok)
	require.Equal(t, "retrieve_context", info.Name)
	require.Equal(t, PermissionReadOnly, info.Permission)
	require.ElementsMatch(t, []string{"query"}, info.InputSchema["required"])
}

func TestRemoteTriggerToolCallsWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			fmt.Fprint(w, "abcdef")
			return
		}
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "token", r.Header.Get("x-test"))
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "payload", string(data))
		w.Header().Set("x-result", "ok")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()

	out, err := RemoteTriggerTool{}.Execute(context.Background(), []byte(`{"url":"`+server.URL+`","method":"POST","headers":{"x-test":"token"},"body":"payload"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"status_code": 200`)
	require.Contains(t, out, `"body": "{\"ok\":true}"`)
	require.Contains(t, out, `"X-Result": [`)
	require.Contains(t, out, `"truncated": false`)

	out, err = RemoteTriggerTool{}.Execute(context.Background(), []byte(`{"url":"`+server.URL+`/large","max_bytes":3}`))
	require.NoError(t, err)
	require.Contains(t, out, `"body": "abc"`)
	require.Contains(t, out, `"bytes": 3`)
	require.Contains(t, out, `"truncated": true`)

	_, err = RemoteTriggerTool{}.Execute(context.Background(), []byte(`{"url":"file:///etc/passwd"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "http or https")
}

func TestPermissionCheckToolReturnsReceipt(t *testing.T) {
	workspace := t.TempDir()
	registry := NewRegistry(workspace)
	prompter := &Prompter{Mode: PermissionReadOnly}

	out, err := registry.Execute(context.Background(), "permission_check", []byte(`{"target_tool":"bash","input":{"command":"pwd"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "permission_check"`)
	require.Contains(t, out, `"source": "registry_permission_policy"`)
	require.Contains(t, out, `"requested_tool": "bash"`)
	require.Contains(t, out, `"target_tool": "bash"`)
	require.Contains(t, out, `"canonical_tool": "bash"`)
	require.Contains(t, out, `"allowed": true`)
	require.Contains(t, out, `"reason": "bash_validation_read_only"`)
	require.Contains(t, out, `"permission_source": "tool_definition"`)
	require.Contains(t, out, `"input_json": {`)
	require.Contains(t, out, `"decision": {`)

	out, err = registry.Execute(context.Background(), "permission_check", []byte(`{"target_tool":"bash","input":{"command":"pwd && touch created.txt"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"allowed": false`)
	require.Contains(t, out, `"reason": "bash_validation"`)
	require.Contains(t, out, `"message": "bash command is not read-only"`)

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644))
	prompter.Workspace = workspace
	out, err = registry.Execute(context.Background(), "permission_check", []byte(`{"target_tool":"bash","input":{"command":"cat `+filepath.Join(outside, "secret.txt")+`"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"allowed": false`)
	require.Contains(t, out, `"reason": "bash_validation"`)
	require.Contains(t, out, `"message": "path resolves outside workspace scope"`)

	prompter.AdditionalDirs = []string{outside}
	out, err = registry.Execute(context.Background(), "permission_check", []byte(`{"target_tool":"bash","input":{"command":"cat `+filepath.Join(outside, "secret.txt")+`"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"allowed": true`)
	require.Contains(t, out, `"reason": "bash_validation_read_only"`)

	prompter = &Prompter{Mode: PermissionAllow, DeniedTools: []string{"write_file"}}
	out, err = registry.Execute(context.Background(), "permission_check", []byte(`{"target_tool":"write_file","input":{"path":"a.txt","content":"x"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"known_tool": true`)
	require.Contains(t, out, `"required_permission": "workspace-write"`)
	require.Contains(t, out, `"allowed": false`)
	require.Contains(t, out, `"reason": "denied_tools"`)

	out, err = registry.Execute(context.Background(), "TestingPermission", []byte(`{"target_tool":"unknown-tool","required_permission":"read-only"}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"requested_tool": "unknown-tool"`)
	require.Contains(t, out, `"target_tool": "unknown-tool"`)
	require.Contains(t, out, `"known_tool": false`)
	require.Contains(t, out, `"required_permission": "read-only"`)
	require.Contains(t, out, `"permission_source": "request_override"`)

	_, err = registry.Execute(context.Background(), "TestingPermission", []byte(`{"target_tool":"unknown-tool","required_permission":"workspace-wirte"}`), prompter)
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported required_permission "workspace-wirte"`)
	require.Contains(t, err.Error(), `did you mean "workspace-write"?`)

	out, err = registry.Execute(context.Background(), "permission_check", []byte(`{"target_tool":"write_fil"}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"known_tool": false`)
	require.Contains(t, out, `"permission_source": "unknown_tool_default"`)
	require.Contains(t, out, `"suggestions": [`)
	require.Contains(t, out, `"write_file"`)

	out, err = registry.Execute(context.Background(), "permission_check", []byte(`{"target_tool":"unknown-tool"}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"known_tool": false`)
	require.Contains(t, out, `"required_permission": "danger-full-access"`)
	require.Contains(t, out, `"permission_source": "unknown_tool_default"`)

	_, err = TestingPermissionTool{}.Execute(context.Background(), []byte(`{"target_tool":"bash"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission_check")
}

func TestMCPAuthToolReportsRecoveryActions(t *testing.T) {
	configHome := t.TempDir()
	tool := MCPAuthTool{Servers: map[string]config.MCPServerConfig{}, ConfigHome: configHome, OAuthProfile: "work"}
	properties := tool.Definition().InputSchema["properties"].(map[string]any)
	require.Contains(t, properties, "action")
	actionSchema := properties["action"].(map[string]any)
	require.Contains(t, actionSchema["enum"], "login")
	require.Contains(t, actionSchema["enum"], "auth")
	require.Contains(t, actionSchema["enum"], "signout")
	require.Contains(t, actionSchema["enum"], "reset")

	out, err := tool.Execute(context.Background(), []byte(`{"server":"missing"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"server": "missing"`)
	require.Contains(t, out, `"status": "unknown"`)
	require.Contains(t, out, `"oauth_profile": "work"`)
	require.Contains(t, out, `"profile_configured": false`)
	require.Contains(t, out, `"command": "codog oauth provider save work ISSUER_URL CLIENT_ID [SCOPE...]"`)

	out, err = tool.Execute(context.Background(), []byte(`{"server":"missing","action":"login"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"server": "missing"`)
	require.Contains(t, out, `"refresh_error": "no oauth token saved"`)

	out, err = tool.Execute(context.Background(), []byte(`{"server":"missing","action":"signout"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"server": "missing"`)
	require.Contains(t, out, `"cleared": true`)

	_, err = tool.Execute(context.Background(), []byte(`{"server":"missing","action":"refesh"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported mcp_auth action "refesh"`)
	require.Contains(t, err.Error(), `did you mean "refresh"?`)
}

func TestNotebookEditToolUpdatesNotebook(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "analysis.ipynb")
	require.NoError(t, os.WriteFile(path, []byte(`{"metadata":{"name":"kept"},"cells":[]}`), 0o644))

	out, err := NotebookEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"notebook_path":"analysis.ipynb","edit_mode":"insert","cell_type":"markdown","new_source":"# Title"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "notebook_edit"`)
	require.Contains(t, out, `"cell_type": "markdown"`)
	require.Contains(t, out, `"cell_id": "cell-1"`)
	require.Contains(t, out, `"cell_count": 1`)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"name": "kept"`)
	require.Contains(t, string(data), `"id": "cell-1"`)
	require.Contains(t, string(data), "# Title")

	out, err = NotebookEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"notebook_path":"analysis.ipynb","cell_id":"cell-1","cell_type":"markdown","new_source":"# Renamed"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"index": 0`)
	require.Contains(t, out, `"cell_id": "cell-1"`)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "# Renamed")
	require.NotContains(t, string(data), "# Title")

	out, err = NotebookEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"notebook_path":"analysis.ipynb","cell_id":"cell-1","edit_mode":"insert","cell_type":"code","new_source":"print(1)\n"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"index": 1`)
	require.Contains(t, out, `"cell_type": "code"`)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"outputs": []`)
	require.Contains(t, string(data), `"execution_count": null`)

	out, err = NotebookEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"notebook_path":"analysis.ipynb","new_source":"print(2)\n"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"index": 1`)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "print(2)")

	out, err = NotebookEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"notebook_path":"analysis.ipynb","edit_mode":"delete"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"mode": "delete"`)
	require.Contains(t, out, `"cell_count": 1`)

	_, err = NotebookEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"notebook_path":"analysis.ipynb","edit_mode":"insert"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "new_source is required")

	registry := NewRegistry(workspace)
	info, ok := registry.Info("notebook_edit")
	require.True(t, ok)
	require.Equal(t, PermissionWorkspace, info.Permission)
}

func TestNotebookReadToolReadsCellsAndOutputs(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "analysis.ipynb"), []byte(`{
  "cells": [
    {"cell_type":"markdown","source":["# Title\n","notes"],"metadata":{}},
    {"cell_type":"code","execution_count":1,"source":["print('hi')\n"],"outputs":[{"output_type":"stream","name":"stdout","text":["hi\n"]}]}
  ],
  "metadata": {}
}`), 0o644))
	registry := NewRegistry(workspace)
	out, err := registry.Execute(context.Background(), "NotebookRead", []byte(`{"notebook_path":"analysis.ipynb","cell_index":1,"include_outputs":true}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "notebook_read"`)
	require.Contains(t, out, `"cell_count": 2`)
	require.Contains(t, out, `"index": 1`)
	require.Contains(t, out, `"source": "print('hi')\n"`)
	require.Contains(t, out, `"output_count": 1`)
	require.Contains(t, out, `"outputs": [`)

	info, ok := registry.Info("notebook_read")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
}

func TestLSPToolQueriesCodeIntel(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/tools\n\ngo 1.26\n"), 0o644))
	source := strings.Join([]string{
		"package demo",
		"",
		"type Widget struct{}",
		"",
		"func BuildWidget() Widget {",
		"	return Widget{}",
		"}",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "demo.go"), []byte(source), 0o644))
	foldSource := "package demo\n\nfunc FoldOnly() {\n\tprintln(\"fold\")\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fold.go"), []byte(foldSource), 0o644))
	linkSource := "package demo\n\n// Docs: https://example.test/docs.\nconst Link = \"https://example.test/api\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "links.go"), []byte(linkSource), 0o644))
	colorSource := "package demo\n\nconst Accent = \"#336699\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "colors.go"), []byte(colorSource), 0o644))
	hintSource := "package demo\n\nfunc Build(name string, count int) int { return count }\nfunc UseBuild() { _ = Build(\"codog\", 2) }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "hints.go"), []byte(hintSource), 0o644))
	hierarchySource := "package demo\n\ntype WidgetBase struct{}\ntype WidgetChild struct{ WidgetBase }\ntype WidgetContract interface { Build() }\nfunc (WidgetChild) Build() {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "hierarchy.go"), []byte(hierarchySource), 0o644))
	brokenSource := "package demo\n\nfunc Broken() { MissingSymbol() }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "broken.go"), []byte(brokenSource), 0o644))
	inlineSource := "package demo\n\nconst InlineAnswer = 42\n\nfunc InlineValuesDemo() {\n\tlocal := \"codog\"\n\t_ = local\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "inline.go"), []byte(inlineSource), 0o644))
	hintArgChar := strings.Index(strings.Split(hintSource, "\n")[3], `"codog"`)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "messy.go"), []byte("package demo\n\nfunc messy(){return}\n"), 0o644))
	importsSource := "package demo\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n\t\"bytes\"\n\t\"fmt\"\n)\n\nfunc ImportsDemo(){ fmt.Println(strings.TrimSpace(\" hi \")) }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "imports.go"), []byte(importsSource), 0o644))
	tool := LSPTool{Workspace: workspace}
	definition := tool.Definition()
	properties := definition.InputSchema["properties"].(map[string]any)
	actionSchema := properties["action"].(map[string]any)
	actionEnum := actionSchema["enum"].([]string)
	for _, action := range codeintel.SupportedLSPActions() {
		require.Contains(t, actionEnum, action.Name)
		for _, alias := range action.Aliases {
			require.Contains(t, actionEnum, alias)
		}
	}
	require.Contains(t, actionSchema["enum"], "rename")
	require.Contains(t, actionSchema["enum"], "workspace_symbol")
	require.Contains(t, actionSchema["enum"], "workspace_symbol_resolve")
	require.Contains(t, actionSchema["enum"], "execute_command")
	require.Contains(t, actionSchema["enum"], "document_diagnostic")
	require.Contains(t, actionSchema["enum"], "workspace_diagnostic")
	require.Contains(t, actionSchema["enum"], "prepare_rename")
	require.Contains(t, actionSchema["enum"], "code_action")
	require.Contains(t, actionSchema["enum"], "code_action_resolve")
	require.Contains(t, actionSchema["enum"], "code_lens")
	require.Contains(t, actionSchema["enum"], "code_lens_resolve")
	require.Contains(t, actionSchema["enum"], "prepare_call_hierarchy")
	require.Contains(t, actionSchema["enum"], "incoming_calls")
	require.Contains(t, actionSchema["enum"], "outgoing_calls")
	require.Contains(t, actionSchema["enum"], "prepare_type_hierarchy")
	require.Contains(t, actionSchema["enum"], "supertypes")
	require.Contains(t, actionSchema["enum"], "subtypes")
	require.Contains(t, actionSchema["enum"], "implementation")
	require.Contains(t, actionSchema["enum"], "selection_range")
	require.Contains(t, actionSchema["enum"], "folding_range")
	require.Contains(t, actionSchema["enum"], "document_link")
	require.Contains(t, actionSchema["enum"], "document_link_resolve")
	require.Contains(t, actionSchema["enum"], "document_color")
	require.Contains(t, actionSchema["enum"], "color_presentation")
	require.Contains(t, actionSchema["enum"], "inlay_hint")
	require.Contains(t, actionSchema["enum"], "inlay_hint_resolve")
	require.Contains(t, actionSchema["enum"], "inline_value")
	require.Contains(t, actionSchema["enum"], "linked_editing_range")
	require.Contains(t, actionSchema["enum"], "moniker")
	require.Contains(t, actionSchema["enum"], "semantic_tokens")
	require.Contains(t, actionSchema["enum"], "semantic_tokens_range")
	require.Contains(t, actionSchema["enum"], "semantic_tokens_delta")
	require.Contains(t, actionSchema["enum"], "completion_resolve")
	require.Contains(t, actionSchema["enum"], "range_format")
	require.Contains(t, actionSchema["enum"], "on_type_format")
	require.Contains(t, actionSchema["enum"], "will_save")
	require.Contains(t, properties, "arguments")
	require.Contains(t, properties, "new_name")

	_, err := tool.Execute(context.Background(), []byte(`{"action":"defnition","query":"Widget"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown lsp action "defnition"`)
	require.Contains(t, err.Error(), `did you mean "definition"?`)

	symbolsOut, err := tool.Execute(context.Background(), []byte(`{"action":"symbols","path":"demo.go"}`))
	require.NoError(t, err)
	require.Contains(t, symbolsOut, `"action": "symbols"`)
	require.Contains(t, symbolsOut, "BuildWidget")

	documentSymbolsOut, err := tool.Execute(context.Background(), []byte(`{"action":"document_symbols","path":"demo.go"}`))
	require.NoError(t, err)
	require.Contains(t, documentSymbolsOut, `"action": "symbols"`)
	require.Contains(t, documentSymbolsOut, "BuildWidget")

	workspaceSymbolsOut, err := tool.Execute(context.Background(), []byte(`{"action":"workspace_symbol","query":"widget","limit":2}`))
	require.NoError(t, err)
	require.Contains(t, workspaceSymbolsOut, `"action": "workspace-symbol"`)
	require.Contains(t, workspaceSymbolsOut, `"source": "static"`)
	require.Contains(t, workspaceSymbolsOut, `"query": "widget"`)
	require.Contains(t, workspaceSymbolsOut, `"name": "Widget"`)
	require.Contains(t, workspaceSymbolsOut, `"name": "BuildWidget"`)
	require.Contains(t, workspaceSymbolsOut, `"total": 2`)

	workspaceSymbolResolveOut, err := tool.Execute(context.Background(), []byte(`{"action":"workspace_symbol_resolve","query":"BuildWidget"}`))
	require.NoError(t, err)
	require.Contains(t, workspaceSymbolResolveOut, `"action": "workspace-symbol-resolve"`)
	require.Contains(t, workspaceSymbolResolveOut, `"source": "static"`)
	require.Contains(t, workspaceSymbolResolveOut, `"found": true`)
	require.Contains(t, workspaceSymbolResolveOut, `"name": "BuildWidget"`)
	require.Contains(t, workspaceSymbolResolveOut, `"symbol": "BuildWidget"`)
	require.Contains(t, workspaceSymbolResolveOut, `"snippet": [`)

	definitionOut, err := tool.Execute(context.Background(), []byte(`{"action":"definition","query":"Widget"}`))
	require.NoError(t, err)
	require.Contains(t, definitionOut, `"source": "static"`)
	require.Contains(t, definitionOut, `"found": true`)
	require.Contains(t, definitionOut, `"name": "Widget"`)

	_, err = tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"demo.go","query":"Formt Go file"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown static code action "Formt Go file"`)
	require.Contains(t, err.Error(), `did you mean "Format Go file"?`)

	_, err = tool.Execute(context.Background(), []byte(`{"action":"execute_command","query":"formt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported static execute command "formt"`)
	require.Contains(t, err.Error(), `suggestions:`)
	require.Contains(t, err.Error(), `format`)

	gotoDefinitionOut, err := tool.Execute(context.Background(), []byte(`{"action":"goto_definition","query":"Widget"}`))
	require.NoError(t, err)
	require.Contains(t, gotoDefinitionOut, `"action": "definition"`)
	require.Contains(t, gotoDefinitionOut, `"found": true`)

	declarationOut, err := tool.Execute(context.Background(), []byte(`{"action":"declaration","query":"Widget"}`))
	require.NoError(t, err)
	require.Contains(t, declarationOut, `"action": "declaration"`)
	require.Contains(t, declarationOut, `"source": "static"`)
	require.Contains(t, declarationOut, `"found": true`)
	require.Contains(t, declarationOut, `"name": "Widget"`)

	typeDefinitionOut, err := tool.Execute(context.Background(), []byte(`{"action":"type_definition","query":"Widget"}`))
	require.NoError(t, err)
	require.Contains(t, typeDefinitionOut, `"action": "type-definition"`)
	require.Contains(t, typeDefinitionOut, `"source": "static"`)
	require.Contains(t, typeDefinitionOut, `"found": true`)
	require.Contains(t, typeDefinitionOut, `"name": "Widget"`)

	documentHighlightOut, err := tool.Execute(context.Background(), []byte(`{"action":"document_highlight","path":"demo.go","query":"Widget","limit":3}`))
	require.NoError(t, err)
	require.Contains(t, documentHighlightOut, `"action": "document-highlight"`)
	require.Contains(t, documentHighlightOut, `"source": "static"`)
	require.Contains(t, documentHighlightOut, `"query": "Widget"`)
	require.Contains(t, documentHighlightOut, `"path": "demo.go"`)
	require.Contains(t, documentHighlightOut, `"character": 5`)
	require.Contains(t, documentHighlightOut, `"total": 3`)

	foldingRangeOut, err := tool.Execute(context.Background(), []byte(`{"action":"folding_range","path":"fold.go","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, foldingRangeOut, `"action": "folding-range"`)
	require.Contains(t, foldingRangeOut, `"source": "static"`)
	require.Contains(t, foldingRangeOut, `"path": "fold.go"`)
	require.Contains(t, foldingRangeOut, `"startLine": 2`)
	require.Contains(t, foldingRangeOut, `"endLine": 4`)
	require.Contains(t, foldingRangeOut, `"total": 1`)

	selectionRangeOut, err := tool.Execute(context.Background(), []byte(`{"action":"selection_range","path":"demo.go","line":4,"character":6,"limit":5}`))
	require.NoError(t, err)
	require.Contains(t, selectionRangeOut, `"action": "selection-range"`)
	require.Contains(t, selectionRangeOut, `"source": "static"`)
	require.Contains(t, selectionRangeOut, `"path": "demo.go"`)
	require.Contains(t, selectionRangeOut, `"kind": "Ident"`)
	require.Contains(t, selectionRangeOut, `"character": 5`)

	monikerOut, err := tool.Execute(context.Background(), []byte(`{"action":"moniker","query":"BuildWidget"}`))
	require.NoError(t, err)
	require.Contains(t, monikerOut, `"action": "moniker"`)
	require.Contains(t, monikerOut, `"source": "static"`)
	require.Contains(t, monikerOut, `"scheme": "gomod"`)
	require.Contains(t, monikerOut, `"identifier": "example.test/tools.BuildWidget"`)
	require.Contains(t, monikerOut, `"kind": "export"`)
	require.Contains(t, monikerOut, `"unique": "project"`)

	linkedEditingOut, err := tool.Execute(context.Background(), []byte(`{"action":"linked_editing_range","path":"demo.go","query":"Widget","limit":3}`))
	require.NoError(t, err)
	require.Contains(t, linkedEditingOut, `"action": "linked-editing-range"`)
	require.Contains(t, linkedEditingOut, `"source": "static"`)
	require.Contains(t, linkedEditingOut, `"query": "Widget"`)
	require.Contains(t, linkedEditingOut, `"path": "demo.go"`)
	require.Contains(t, linkedEditingOut, `"wordPattern": "[A-Za-z_][A-Za-z0-9_]*"`)
	require.Contains(t, linkedEditingOut, `"total": 3`)

	documentLinkOut, err := tool.Execute(context.Background(), []byte(`{"action":"document_link","path":"links.go","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, documentLinkOut, `"action": "document-link"`)
	require.Contains(t, documentLinkOut, `"source": "static"`)
	require.Contains(t, documentLinkOut, `"path": "links.go"`)
	require.Contains(t, documentLinkOut, `"target": "https://example.test/docs"`)
	require.Contains(t, documentLinkOut, `"character": 9`)
	require.Contains(t, documentLinkOut, `"total": 2`)

	documentLinkResolveOut, err := tool.Execute(context.Background(), []byte(`{"action":"document_link_resolve","path":"links.go","line":2,"character":12}`))
	require.NoError(t, err)
	require.Contains(t, documentLinkResolveOut, `"action": "document-link-resolve"`)
	require.Contains(t, documentLinkResolveOut, `"source": "static"`)
	require.Contains(t, documentLinkResolveOut, `"found": true`)
	require.Contains(t, documentLinkResolveOut, `"target": "https://example.test/docs"`)

	documentColorOut, err := tool.Execute(context.Background(), []byte(`{"action":"document_color","path":"colors.go","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, documentColorOut, `"action": "document-color"`)
	require.Contains(t, documentColorOut, `"source": "static"`)
	require.Contains(t, documentColorOut, `"path": "colors.go"`)
	require.Contains(t, documentColorOut, `"text": "#336699"`)
	require.Contains(t, documentColorOut, `"red": 0.2`)
	require.Contains(t, documentColorOut, `"total": 1`)

	colorPresentationOut, err := tool.Execute(context.Background(), []byte(`{"action":"color_presentation","path":"colors.go","line":2,"character":18}`))
	require.NoError(t, err)
	require.Contains(t, colorPresentationOut, `"action": "color-presentation"`)
	require.Contains(t, colorPresentationOut, `"source": "static"`)
	require.Contains(t, colorPresentationOut, `"found": true`)
	require.Contains(t, colorPresentationOut, `"label": "#336699"`)
	require.Contains(t, colorPresentationOut, `"label": "rgb(51, 102, 153)"`)

	inlayHintOut, err := tool.Execute(context.Background(), []byte(`{"action":"inlay_hint","path":"hints.go","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, inlayHintOut, `"action": "inlay-hint"`)
	require.Contains(t, inlayHintOut, `"source": "static"`)
	require.Contains(t, inlayHintOut, `"path": "hints.go"`)
	require.Contains(t, inlayHintOut, `"label": "name:"`)
	require.Contains(t, inlayHintOut, `"label": "count:"`)
	require.Contains(t, inlayHintOut, `"kind": "parameter"`)
	require.Contains(t, inlayHintOut, `"total": 2`)

	inlayHintResolveInput := fmt.Sprintf(`{"action":"inlay_hint_resolve","path":"hints.go","line":3,"character":%d}`, hintArgChar)
	inlayHintResolveOut, err := tool.Execute(context.Background(), []byte(inlayHintResolveInput))
	require.NoError(t, err)
	require.Contains(t, inlayHintResolveOut, `"action": "inlay-hint-resolve"`)
	require.Contains(t, inlayHintResolveOut, `"source": "static"`)
	require.Contains(t, inlayHintResolveOut, `"found": true`)
	require.Contains(t, inlayHintResolveOut, `"label": "name:"`)
	require.Contains(t, inlayHintResolveOut, `"tooltip": "Build parameter 1"`)

	signatureHelpInput := fmt.Sprintf(`{"action":"signature_help","path":"hints.go","line":3,"character":%d}`, hintArgChar)
	signatureHelpOut, err := tool.Execute(context.Background(), []byte(signatureHelpInput))
	require.NoError(t, err)
	require.Contains(t, signatureHelpOut, `"action": "signature-help"`)
	require.Contains(t, signatureHelpOut, `"source": "static"`)
	require.Contains(t, signatureHelpOut, `"found": true`)
	require.Contains(t, signatureHelpOut, `"function": "Build"`)
	require.Contains(t, signatureHelpOut, `"label": "Build(name string, count int) int"`)
	require.Contains(t, signatureHelpOut, `"activeParameter": 0`)

	codeLensOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_lens","path":"demo.go","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, codeLensOut, `"action": "code-lens"`)
	require.Contains(t, codeLensOut, `"source": "static"`)
	require.Contains(t, codeLensOut, `"path": "demo.go"`)
	require.Contains(t, codeLensOut, `"symbol": "Widget"`)
	require.Contains(t, codeLensOut, `"command": "codog.references"`)
	require.Contains(t, codeLensOut, `"total": 2`)

	codeLensResolveOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_lens_resolve","path":"demo.go","line":2,"character":6}`))
	require.NoError(t, err)
	require.Contains(t, codeLensResolveOut, `"action": "code-lens-resolve"`)
	require.Contains(t, codeLensResolveOut, `"source": "static"`)
	require.Contains(t, codeLensResolveOut, `"found": true`)
	require.Contains(t, codeLensResolveOut, `"symbol": "Widget"`)
	require.Contains(t, codeLensResolveOut, `"command": "codog.references"`)

	semanticTokensOut, err := tool.Execute(context.Background(), []byte(`{"action":"semantic_tokens","path":"demo.go","limit":50}`))
	require.NoError(t, err)
	require.Contains(t, semanticTokensOut, `"action": "semantic-tokens"`)
	require.Contains(t, semanticTokensOut, `"source": "static"`)
	require.Contains(t, semanticTokensOut, `"legend": [`)
	require.Contains(t, semanticTokensOut, `"text": "Widget"`)
	require.Contains(t, semanticTokensOut, `"type": "type"`)
	require.Contains(t, semanticTokensOut, `"text": "BuildWidget"`)
	require.Contains(t, semanticTokensOut, `"type": "function"`)

	semanticTokensRangeOut, err := tool.Execute(context.Background(), []byte(`{"action":"semantic_tokens_range","path":"demo.go","line":2,"limit":10}`))
	require.NoError(t, err)
	require.Contains(t, semanticTokensRangeOut, `"action": "semantic-tokens-range"`)
	require.Contains(t, semanticTokensRangeOut, `"source": "static"`)
	require.Contains(t, semanticTokensRangeOut, `"text": "Widget"`)
	require.Contains(t, semanticTokensRangeOut, `"line": 2`)

	semanticTokensDeltaOut, err := tool.Execute(context.Background(), []byte(`{"action":"semantic_tokens_delta","path":"demo.go","query":"previous-result","limit":50}`))
	require.NoError(t, err)
	require.Contains(t, semanticTokensDeltaOut, `"action": "semantic-tokens-delta"`)
	require.Contains(t, semanticTokensDeltaOut, `"source": "static"`)
	require.Contains(t, semanticTokensDeltaOut, `"previousResultId": "previous-result"`)
	require.Contains(t, semanticTokensDeltaOut, `"edits": []`)

	prepareRenameOut, err := tool.Execute(context.Background(), []byte(`{"action":"prepare_rename","path":"demo.go","line":2,"character":6}`))
	require.NoError(t, err)
	require.Contains(t, prepareRenameOut, `"action": "prepare-rename"`)
	require.Contains(t, prepareRenameOut, `"source": "static"`)
	require.Contains(t, prepareRenameOut, `"found": true`)
	require.Contains(t, prepareRenameOut, `"symbol": "Widget"`)
	require.Contains(t, prepareRenameOut, `"current_name": "Widget"`)
	require.Contains(t, prepareRenameOut, `"placeholder": "Widget"`)

	renameOut, err := tool.Execute(context.Background(), []byte(`{"action":"rename","query":"Widget","new_name":"Gadget","limit":20}`))
	require.NoError(t, err)
	require.Contains(t, renameOut, `"action": "rename"`)
	require.Contains(t, renameOut, `"source": "static"`)
	require.Contains(t, renameOut, `"query": "Widget"`)
	require.Contains(t, renameOut, `"newName": "Gadget"`)
	require.Contains(t, renameOut, `"text_edits": 3`)
	require.Contains(t, renameOut, `"file_edits": 1`)
	require.Contains(t, renameOut, `type Gadget struct{}`)

	callHierarchyOut, err := tool.Execute(context.Background(), []byte(`{"action":"prepare_call_hierarchy","query":"Build"}`))
	require.NoError(t, err)
	require.Contains(t, callHierarchyOut, `"action": "prepare-call-hierarchy"`)
	require.Contains(t, callHierarchyOut, `"source": "static"`)
	require.Contains(t, callHierarchyOut, `"name": "Build"`)
	require.Contains(t, callHierarchyOut, `"kind": "function"`)
	require.Contains(t, callHierarchyOut, `"total": 1`)

	incomingCallsOut, err := tool.Execute(context.Background(), []byte(`{"action":"incoming_calls","query":"Build","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, incomingCallsOut, `"action": "call-hierarchy-incoming"`)
	require.Contains(t, incomingCallsOut, `"source": "static"`)
	require.Contains(t, incomingCallsOut, `"query": "Build"`)
	require.Contains(t, incomingCallsOut, `"name": "UseBuild"`)
	require.Contains(t, incomingCallsOut, `"name": "Build"`)
	require.Contains(t, incomingCallsOut, `"total": 1`)

	outgoingCallsOut, err := tool.Execute(context.Background(), []byte(`{"action":"outgoing_calls","query":"UseBuild","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, outgoingCallsOut, `"action": "call-hierarchy-outgoing"`)
	require.Contains(t, outgoingCallsOut, `"source": "static"`)
	require.Contains(t, outgoingCallsOut, `"query": "UseBuild"`)
	require.Contains(t, outgoingCallsOut, `"name": "Build"`)
	require.Contains(t, outgoingCallsOut, `"total": 1`)

	typeHierarchyOut, err := tool.Execute(context.Background(), []byte(`{"action":"prepare_type_hierarchy","query":"WidgetBase"}`))
	require.NoError(t, err)
	require.Contains(t, typeHierarchyOut, `"action": "prepare-type-hierarchy"`)
	require.Contains(t, typeHierarchyOut, `"source": "static"`)
	require.Contains(t, typeHierarchyOut, `"name": "WidgetBase"`)
	require.Contains(t, typeHierarchyOut, `"kind": "struct"`)
	require.Contains(t, typeHierarchyOut, `"total": 1`)

	supertypesOut, err := tool.Execute(context.Background(), []byte(`{"action":"supertypes","query":"WidgetChild","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, supertypesOut, `"action": "type-hierarchy-supertypes"`)
	require.Contains(t, supertypesOut, `"source": "static"`)
	require.Contains(t, supertypesOut, `"query": "WidgetChild"`)
	require.Contains(t, supertypesOut, `"name": "WidgetBase"`)
	require.Contains(t, supertypesOut, `"total": 1`)

	subtypesOut, err := tool.Execute(context.Background(), []byte(`{"action":"subtypes","query":"WidgetBase","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, subtypesOut, `"action": "type-hierarchy-subtypes"`)
	require.Contains(t, subtypesOut, `"source": "static"`)
	require.Contains(t, subtypesOut, `"query": "WidgetBase"`)
	require.Contains(t, subtypesOut, `"name": "WidgetChild"`)
	require.Contains(t, subtypesOut, `"total": 1`)

	interfaceSubtypesOut, err := tool.Execute(context.Background(), []byte(`{"action":"subtypes","query":"WidgetContract","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, interfaceSubtypesOut, `"action": "type-hierarchy-subtypes"`)
	require.Contains(t, interfaceSubtypesOut, `"query": "WidgetContract"`)
	require.Contains(t, interfaceSubtypesOut, `"name": "WidgetChild"`)

	implementationOut, err := tool.Execute(context.Background(), []byte(`{"action":"implementation","query":"WidgetContract","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, implementationOut, `"action": "implementation"`)
	require.Contains(t, implementationOut, `"source": "static"`)
	require.Contains(t, implementationOut, `"query": "WidgetContract"`)
	require.Contains(t, implementationOut, `"name": "WidgetChild"`)
	require.Contains(t, implementationOut, `"total": 1`)

	languageFallbackOut, err := tool.Execute(context.Background(), []byte(`{"action":"definition","query":"Widget","language":"go"}`))
	require.NoError(t, err)
	require.Contains(t, languageFallbackOut, `"action": "definition"`)
	require.Contains(t, languageFallbackOut, `"source": "static"`)
	require.Contains(t, languageFallbackOut, `"fallback": {`)
	require.Contains(t, languageFallbackOut, `"from": "lsp"`)
	require.Contains(t, languageFallbackOut, `"to": "static"`)
	require.Contains(t, languageFallbackOut, `"reason": "lsp_server_unavailable"`)
	require.Contains(t, languageFallbackOut, `"error": "config home is required for lsp server queries"`)
	require.Contains(t, languageFallbackOut, `"found": true`)

	_, err = tool.Execute(context.Background(), []byte(`{"action":"hover","path":"demo.go","line":4,"character":6,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"workspace_symbol","query":"Widget","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"workspace_symbol_resolve","query":"Widget","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"document_highlight","path":"demo.go","query":"Widget","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"folding_range","path":"fold.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"selection_range","path":"demo.go","line":4,"character":6,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"moniker","query":"Widget","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"linked_editing_range","path":"demo.go","query":"Widget","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"document_link","path":"links.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"document_link_resolve","path":"links.go","line":2,"character":12,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"document_color","path":"colors.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"color_presentation","path":"colors.go","line":2,"character":18,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"inlay_hint","path":"hints.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	inlayHintResolveServerInput := fmt.Sprintf(`{"action":"inlay_hint_resolve","path":"hints.go","line":3,"character":%d,"use_server":true}`, hintArgChar)
	_, err = tool.Execute(context.Background(), []byte(inlayHintResolveServerInput))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	signatureHelpServerInput := fmt.Sprintf(`{"action":"signature_help","path":"hints.go","line":3,"character":%d,"use_server":true}`, hintArgChar)
	_, err = tool.Execute(context.Background(), []byte(signatureHelpServerInput))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"code_lens","path":"demo.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"code_lens_resolve","path":"demo.go","line":2,"character":6,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"semantic_tokens","path":"demo.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"semantic_tokens_range","path":"demo.go","line":2,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"semantic_tokens_delta","path":"demo.go","query":"previous-result","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"prepare_rename","path":"demo.go","line":2,"character":6,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"rename","query":"Widget","new_name":"Gadget","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"prepare_call_hierarchy","query":"Build","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"incoming_calls","query":"Build","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"outgoing_calls","query":"UseBuild","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"prepare_type_hierarchy","query":"WidgetBase","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"supertypes","query":"WidgetChild","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"subtypes","query":"WidgetBase","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"completion_resolve","query":"BuildWidget","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"range_format","path":"messy.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"on_type_format","path":"messy.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	_, err = tool.Execute(context.Background(), []byte(`{"action":"will_save","path":"messy.go","use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	codeActionOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action","path":"messy.go","line":2,"character":10}`))
	require.NoError(t, err)
	require.Contains(t, codeActionOut, `"action": "code-action"`)
	require.Contains(t, codeActionOut, `"source": "static"`)
	require.Contains(t, codeActionOut, `"title": "Format Go file"`)
	require.Contains(t, codeActionOut, `"kind": "source.format"`)
	require.Contains(t, codeActionOut, `"title": "Fix all Go source"`)
	require.Contains(t, codeActionOut, `"kind": "source.fixAll"`)
	require.Contains(t, codeActionOut, `"total": 2`)

	organizeActionOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action","path":"imports.go","line":2,"character":10}`))
	require.NoError(t, err)
	require.Contains(t, organizeActionOut, `"action": "code-action"`)
	require.Contains(t, organizeActionOut, `"title": "Organize Go imports"`)
	require.Contains(t, organizeActionOut, `"kind": "source.organizeImports"`)
	require.Contains(t, organizeActionOut, `"removed_imports": [`)
	require.Contains(t, organizeActionOut, `"bytes"`)
	require.Contains(t, organizeActionOut, `"duplicate_imports": [`)
	require.Contains(t, organizeActionOut, `"fmt"`)

	codeActionResolveOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"messy.go","query":"Format Go file"}`))
	require.NoError(t, err)
	require.Contains(t, codeActionResolveOut, `"action": "code-action-resolve"`)
	require.Contains(t, codeActionResolveOut, `"source": "static"`)
	require.Contains(t, codeActionResolveOut, `"selected": "Format Go file"`)
	require.Contains(t, codeActionResolveOut, `"title": "Format Go file"`)
	require.Contains(t, codeActionResolveOut, `func messy()`)

	organizeResolveOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"imports.go","query":"source.organizeImports"}`))
	require.NoError(t, err)
	require.Contains(t, organizeResolveOut, `"action": "code-action-resolve"`)
	require.Contains(t, organizeResolveOut, `"selected": "source.organizeImports"`)
	require.Contains(t, organizeResolveOut, `"title": "Organize Go imports"`)
	require.Contains(t, organizeResolveOut, `"kind": "organize_imports"`)
	require.Contains(t, organizeResolveOut, `"removed_imports": [`)

	organizeGoKindOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"imports.go","query":"source.organizeImports.go"}`))
	require.NoError(t, err)
	require.Contains(t, organizeGoKindOut, `"selected": "source.organizeImports.go"`)
	require.Contains(t, organizeGoKindOut, `"title": "Organize Go imports"`)

	fixAllResolveOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"imports.go","query":"source.fixAll"}`))
	require.NoError(t, err)
	require.Contains(t, fixAllResolveOut, `"action": "code-action-resolve"`)
	require.Contains(t, fixAllResolveOut, `"selected": "source.fixAll"`)
	require.Contains(t, fixAllResolveOut, `"title": "Fix all Go source"`)
	require.Contains(t, fixAllResolveOut, `"kind": "fix_all"`)
	require.Contains(t, fixAllResolveOut, `"actions": [`)
	require.Contains(t, fixAllResolveOut, `"source.organizeImports"`)

	fixAllGoKindOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"imports.go","query":"source.fixAll.go"}`))
	require.NoError(t, err)
	require.Contains(t, fixAllGoKindOut, `"selected": "source.fixAll.go"`)
	require.Contains(t, fixAllGoKindOut, `"title": "Fix all Go source"`)

	gofmtResolveOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"messy.go","query":"gofmt"}`))
	require.NoError(t, err)
	require.Contains(t, gofmtResolveOut, `"selected": "gofmt"`)
	require.Contains(t, gofmtResolveOut, `"title": "Format Go file"`)

	formatDocumentOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"messy.go","query":"Format Document"}`))
	require.NoError(t, err)
	require.Contains(t, formatDocumentOut, `"selected": "Format Document"`)
	require.Contains(t, formatDocumentOut, `"title": "Format Go file"`)

	addMissingImportsOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"imports.go","query":"Add missing imports"}`))
	require.NoError(t, err)
	require.Contains(t, addMissingImportsOut, `"selected": "Add missing imports"`)
	require.Contains(t, addMissingImportsOut, `"title": "Organize Go imports"`)

	fixAllSourceActionOut, err := tool.Execute(context.Background(), []byte(`{"action":"code_action_resolve","path":"imports.go","query":"Source Action: Fix All"}`))
	require.NoError(t, err)
	require.Contains(t, fixAllSourceActionOut, `"selected": "Source Action: Fix All"`)
	require.Contains(t, fixAllSourceActionOut, `"title": "Fix all Go source"`)

	inlineValueOut, err := tool.Execute(context.Background(), []byte(`{"action":"inline_value","path":"inline.go","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, inlineValueOut, `"action": "inline-value"`)
	require.Contains(t, inlineValueOut, `"source": "static"`)
	require.Contains(t, inlineValueOut, `"name": "InlineAnswer"`)
	require.Contains(t, inlineValueOut, `"text": "local = \"codog\""`)
	require.Contains(t, inlineValueOut, `"total": 2`)

	executeCommandOut, err := tool.Execute(context.Background(), []byte(`{"action":"execute_command","query":"format","path":"messy.go"}`))
	require.NoError(t, err)
	require.Contains(t, executeCommandOut, `"action": "execute-command"`)
	require.Contains(t, executeCommandOut, `"source": "static"`)
	require.Contains(t, executeCommandOut, `"command": "format"`)
	require.Contains(t, executeCommandOut, `func messy()`)

	organizeCommandOut, err := tool.Execute(context.Background(), []byte(`{"action":"execute_command","query":"source.organizeImports","path":"imports.go"}`))
	require.NoError(t, err)
	require.Contains(t, organizeCommandOut, `"action": "execute-command"`)
	require.Contains(t, organizeCommandOut, `"command": "source.organizeimports"`)
	require.Contains(t, organizeCommandOut, `"organize_imports": {`)
	require.Contains(t, organizeCommandOut, `"removed_imports": [`)
	require.Contains(t, organizeCommandOut, `"bytes"`)
	data, err := os.ReadFile(filepath.Join(workspace, "imports.go"))
	require.NoError(t, err)
	require.Equal(t, importsSource, string(data))

	fixAllCommandOut, err := tool.Execute(context.Background(), []byte(`{"action":"execute_command","query":"source.fixAll","path":"imports.go"}`))
	require.NoError(t, err)
	require.Contains(t, fixAllCommandOut, `"action": "execute-command"`)
	require.Contains(t, fixAllCommandOut, `"command": "source.fixall"`)
	require.Contains(t, fixAllCommandOut, `"fix_all": {`)
	require.Contains(t, fixAllCommandOut, `"kind": "fix_all"`)
	require.Contains(t, fixAllCommandOut, `"source.organizeImports"`)
	data, err = os.ReadFile(filepath.Join(workspace, "imports.go"))
	require.NoError(t, err)
	require.Equal(t, importsSource, string(data))

	_, err = tool.Execute(context.Background(), []byte(`{"action":"execute_command","query":"unsupported.command"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported static execute command")

	hoverOut, err := tool.Execute(context.Background(), []byte(`{"action":"hover","path":"demo.go","line":4,"character":6}`))
	require.NoError(t, err)
	require.Contains(t, hoverOut, `"query": "BuildWidget"`)
	require.Contains(t, hoverOut, `"found": true`)

	completionOut, err := tool.Execute(context.Background(), []byte(`{"action":"completion","query":"Build","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, completionOut, `"action": "completion"`)
	require.Contains(t, completionOut, `"label": "BuildWidget"`)

	completionResolveOut, err := tool.Execute(context.Background(), []byte(`{"action":"completion_resolve","query":"BuildWidget"}`))
	require.NoError(t, err)
	require.Contains(t, completionResolveOut, `"action": "completion-item-resolve"`)
	require.Contains(t, completionResolveOut, `"source": "static"`)
	require.Contains(t, completionResolveOut, `"found": true`)
	require.Contains(t, completionResolveOut, `"label": "BuildWidget"`)
	require.Contains(t, completionResolveOut, `"kind": "function"`)

	rangeFormatOut, err := tool.Execute(context.Background(), []byte(`{"action":"range_format","path":"messy.go","line":2,"character":10}`))
	require.NoError(t, err)
	require.Contains(t, rangeFormatOut, `"action": "range-format"`)
	require.Contains(t, rangeFormatOut, `"source": "static"`)
	require.Contains(t, rangeFormatOut, `"path": "messy.go"`)
	require.Contains(t, rangeFormatOut, `"changed": true`)
	require.Contains(t, rangeFormatOut, `func messy()`)

	onTypeFormatOut, err := tool.Execute(context.Background(), []byte(`{"action":"on_type_format","path":"messy.go","line":2,"character":18}`))
	require.NoError(t, err)
	require.Contains(t, onTypeFormatOut, `"action": "on-type-format"`)
	require.Contains(t, onTypeFormatOut, `"source": "static"`)
	require.Contains(t, onTypeFormatOut, `"path": "messy.go"`)
	require.Contains(t, onTypeFormatOut, `"changed": true`)

	willSaveOut, err := tool.Execute(context.Background(), []byte(`{"action":"will_save","path":"messy.go"}`))
	require.NoError(t, err)
	require.Contains(t, willSaveOut, `"action": "will-save"`)
	require.Contains(t, willSaveOut, `"source": "static"`)
	require.Contains(t, willSaveOut, `"edits": true`)

	documentDiagnosticOut, err := tool.Execute(context.Background(), []byte(`{"action":"document_diagnostic","path":"broken.go"}`))
	require.NoError(t, err)
	require.Contains(t, documentDiagnosticOut, `"action": "document-diagnostic"`)
	require.Contains(t, documentDiagnosticOut, `"source": "static"`)
	require.Contains(t, documentDiagnosticOut, `"path": "broken.go"`)
	require.Contains(t, documentDiagnosticOut, "MissingSymbol")
	require.Contains(t, documentDiagnosticOut, `"total": 2`)

	workspaceDiagnosticOut, err := tool.Execute(context.Background(), []byte(`{"action":"workspace_diagnostic"}`))
	require.NoError(t, err)
	require.Contains(t, workspaceDiagnosticOut, `"action": "workspace-diagnostic"`)
	require.Contains(t, workspaceDiagnosticOut, `"source": "static"`)
	require.Contains(t, workspaceDiagnosticOut, `"path": "broken.go"`)
	require.Contains(t, workspaceDiagnosticOut, "MissingSymbol")

	formatOut, err := tool.Execute(context.Background(), []byte(`{"action":"format","path":"messy.go"}`))
	require.NoError(t, err)
	require.Contains(t, formatOut, `"action": "format"`)
	require.Contains(t, formatOut, `"changed": true`)
	require.Contains(t, formatOut, `func messy()`)
}

func TestNormalizeStaticCodeActionTitle(t *testing.T) {
	cases := map[string]string{
		"Format Document":                 "format",
		"source.format.go":                "format",
		"Source Action: Organize Imports": "organize-imports",
		"Add missing imports":             "organize-imports",
		"source.removeUnusedImports.go":   "organize-imports",
		"Source Action: Fix All":          "fix-all",
		"source.fixAll.gopls":             "fix-all",
		"Quick Fix":                       "diagnostics",
		"custom action":                   "custom action",
	}
	for input, want := range cases {
		require.Equal(t, want, normalizeStaticCodeActionTitle(input), input)
	}
}

func TestWorktreeToolsAllocateAndRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	runToolTestGit(t, workspace, "init", "-q")
	runToolTestGit(t, workspace, "branch", "-M", "main")
	runToolTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runToolTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o644))
	runToolTestGit(t, workspace, "add", "README.md")
	runToolTestGit(t, workspace, "commit", "-q", "-m", "init")

	enterOut, err := EnterWorktreeTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"name":"reviewer"}`))
	require.NoError(t, err)
	require.Contains(t, enterOut, `"operation": "enter"`)
	var payload struct {
		Allocation struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"allocation"`
	}
	require.NoError(t, json.Unmarshal([]byte(enterOut), &payload))
	require.NotEmpty(t, payload.Allocation.ID)
	require.FileExists(t, filepath.Join(payload.Allocation.Path, "README.md"))

	exitOut, err := ExitWorktreeTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"id":"`+payload.Allocation.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, exitOut, `"removed": true`)
	require.NoDirExists(t, payload.Allocation.Path)
}

func TestGitToolsReadRepositoryState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	baseBranch := "main"
	runToolTestGit(t, workspace, "init", "-q")
	runToolTestGit(t, workspace, "branch", "-M", baseBranch)
	runToolTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runToolTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha\n"), 0o644))
	runToolTestGit(t, workspace, "add", "notes.txt")
	runToolTestGit(t, workspace, "commit", "-q", "-m", "initial notes")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha\nbeta\n"), 0o644))

	statusOut, err := GitStatusTool{Workspace: workspace}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, statusOut, `"output"`)
	require.Contains(t, statusOut, "notes.txt")

	diffOut, err := GitDiffTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"notes.txt"}`))
	require.NoError(t, err)
	require.Contains(t, diffOut, "+beta")

	logOut, err := GitLogTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"count":1,"oneline":true}`))
	require.NoError(t, err)
	require.Contains(t, logOut, "initial notes")

	showOut, err := GitShowTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"commit":"HEAD","format":"metadata"}`))
	require.NoError(t, err)
	require.Contains(t, showOut, "initial notes")

	_, err = GitShowTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"commit":"HEAD","format":"metdata"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown git_show format "metdata"`)
	require.Contains(t, err.Error(), `did you mean "metadata"?`)

	blameOut, err := GitBlameTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"notes.txt","start_line":1,"end_line":1}`))
	require.NoError(t, err)
	require.Contains(t, blameOut, "alpha")

	runToolTestGit(t, workspace, "restore", "notes.txt")
	runToolTestGit(t, workspace, "switch", "-q", "-c", "topic")
	runToolTestGit(t, workspace, "switch", "-q", baseBranch)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runToolTestGit(t, workspace, "add", "fix.txt")
	runToolTestGit(t, workspace, "commit", "-q", "-m", "fix: main update")
	freshnessOut, err := BranchFreshnessTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"branch":"topic","base":"`+baseBranch+`"}`))
	require.NoError(t, err)
	require.Contains(t, freshnessOut, `"kind": "branch_freshness"`)
	require.Contains(t, freshnessOut, `"status": "stale"`)
	require.Contains(t, freshnessOut, `"verification_blocked": true`)
	require.Contains(t, freshnessOut, `"lane_event": "branch.stale_against_main"`)
	require.Contains(t, freshnessOut, `"recovery_scenario": "stale_branch"`)

	_, err = GitDiffTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"../outside.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")

	_, err = GitShowTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"commit":"--help"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "safe git ref")
}

func runToolTestGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestPlanModeToolsEnterAndExit(t *testing.T) {
	workspace := t.TempDir()
	enterTool := EnterPlanModeTool{Workspace: workspace}
	exitTool := ExitPlanModeTool{Workspace: workspace}

	require.Equal(t, PermissionReadOnly, enterTool.Permission())
	require.Equal(t, PermissionReadOnly, exitTool.Permission())

	enterOut, err := enterTool.Execute(context.Background(), []byte(`{"plan":"inspect first"}`))
	require.NoError(t, err)
	require.Contains(t, enterOut, `"action": "enter"`)
	require.Contains(t, enterOut, `"status": "active"`)
	require.Contains(t, enterOut, "inspect first")

	state, err := planmode.Load(workspace)
	require.NoError(t, err)
	require.True(t, state.Active)
	require.Equal(t, "inspect first", state.Plan)

	exitOut, err := exitTool.Execute(context.Background(), []byte(`{"plan":"ship final plan"}`))
	require.NoError(t, err)
	require.Contains(t, exitOut, `"action": "exit"`)
	require.Contains(t, exitOut, `"status": "inactive"`)
	require.Contains(t, exitOut, "ship final plan")

	state, err = planmode.Load(workspace)
	require.NoError(t, err)
	require.False(t, state.Active)
	require.Equal(t, "ship final plan", state.Plan)
}

func TestAgentToolLaunchesBackgroundAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "reviewer.json"), []byte(`{"name":"reviewer","model":"agent-model","prompt":"Base review instructions"}`), 0o644))
	script := filepath.Join(t.TempDir(), "agent-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))

	out, err := AgentTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{"description":"review code","prompt":"check auth flow","subagent_type":"reviewer","session_id":"session-1"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "agent"`)
	require.Contains(t, out, `"agent": "reviewer"`)
	var payload struct {
		Task background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.NotEmpty(t, payload.Task.ID)
	require.Equal(t, "agent", payload.Task.Kind)
	require.Equal(t, "reviewer", payload.Task.AgentType)
	require.Equal(t, "session-1", payload.Task.SessionID)

	store := background.NewStore(configHome)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(payload.Task.ID, 4096)
		return err == nil && strings.Contains(logs, "agent-model") && strings.Contains(logs, "Base review instructions") && strings.Contains(logs, "check auth flow")
	}, 20*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		completed, err := store.Status(payload.Task.ID)
		return err == nil && !background.IsActiveStatus(completed.Status)
	}, 20*time.Second, 50*time.Millisecond)
}

func TestBuildAgentToolCommandPreservesDefinitionScope(t *testing.T) {
	command := buildAgentToolCommandWithPluginDirs("/tmp/codog", agentdefs.Definition{
		Prompt: "Review carefully.",
		Tools:  []string{"read_file", "grep"},
	}, "review", "check auth", "glm52", []string{"./plugin one", "./plugin-two"})

	require.Contains(t, command, "--model 'glm52'")
	require.Contains(t, command, "--tools 'read_file,grep'")
	require.Contains(t, command, "--plugin-dir './plugin one'")
	require.Contains(t, command, "--plugin-dir './plugin-two'")
	require.Contains(t, command, "Review carefully.")
}

func TestCronToolsCreateListAndDeleteEntries(t *testing.T) {
	configHome := t.TempDir()

	createOut, err := CronCreateTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"schedule":"0 9 * * 1","prompt":"review weekly status","description":"weekly review"}`))
	require.NoError(t, err)
	require.Contains(t, createOut, `"schedule": "0 9 * * 1"`)
	require.Contains(t, createOut, `"prompt": "review weekly status"`)
	var entry struct {
		ID string `json:"cron_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &entry))
	require.NotEmpty(t, entry.ID)

	listOut, err := CronListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"count": 1`)
	require.Contains(t, listOut, entry.ID)

	deleteOut, err := CronDeleteTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"cron_id":"`+entry.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, deleteOut, `"status": "deleted"`)
	listOut, err = CronListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"count": 0`)
}

func TestTeamToolsCreateAndDeleteBackgroundTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "team-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))

	createOut, err := TeamCreateTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{"name":"review","session_id":"session-1","tasks":[{"description":"auth","prompt":"check auth"},{"prompt":"check tests"}]}`))
	require.NoError(t, err)
	require.Contains(t, createOut, `"name": "review"`)
	require.Contains(t, createOut, `"task_count": 2`)
	var created struct {
		ID      string   `json:"team_id"`
		TaskIDs []string `json:"task_ids"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	require.NotEmpty(t, created.ID)
	require.Len(t, created.TaskIDs, 2)

	store := background.NewStore(configHome)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(created.TaskIDs[0], 4096)
		return err == nil && strings.Contains(logs, "Task: auth") && strings.Contains(logs, "check auth")
	}, 20*time.Second, 50*time.Millisecond)

	listOut, err := TeamListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"status":"running"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "team_list"`)
	require.Contains(t, listOut, `"total": 1`)
	require.Contains(t, listOut, created.ID)
	require.Contains(t, listOut, `"task_statuses": [`)

	getOut, err := TeamGetTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"team_id":"`+created.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, `"kind": "team"`)
	require.Contains(t, getOut, `"tasks": [`)
	require.Contains(t, getOut, created.TaskIDs[0])

	deleteOut, err := TeamDeleteTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"team_id":"`+created.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, deleteOut, `"status": "deleted"`)
	require.Contains(t, deleteOut, `"message": "Team deleted"`)
}
