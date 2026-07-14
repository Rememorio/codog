package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/hookenv"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/shellstate"
	"github.com/stretchr/testify/require"
)

func escapeJSONSubstring(value string) string {
	data, _ := json.Marshal(value)
	return strings.Trim(string(data), `"`)
}

func TestReadFileRejectsWorkspaceEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"path": outside})
	_, err := ReadFileTool{Workspace: workspace}.Execute(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
}

func TestFileToolsEnforceSizeLimits(t *testing.T) {
	workspace := t.TempDir()
	largeContent := strings.Repeat("a", int(maxFileToolBytes)+1)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "large.txt"), []byte(largeContent), 0o644))

	out, err := ReadFileTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"large.txt"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"truncated": true`)

	_, err = WriteFileTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"too-large.txt","content":"`+largeContent+`"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum file tool size")

	_, err = EditFileTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"large.txt","old_string":"a","new_string":"b"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum editable size")
}

func TestReadFileToolReportsLineWindowMetadata(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644))

	out, err := ReadFileTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"notes.txt","offset":1,"limit":1}`))
	require.NoError(t, err)
	var report struct {
		Type       string `json:"type"`
		Path       string `json:"path"`
		StartLine  int    `json:"start_line"`
		LineCount  int    `json:"line_count"`
		NextOffset int    `json:"next_offset"`
		Total      int    `json:"total"`
		TotalLines int    `json:"total_lines"`
		HasMore    bool   `json:"has_more"`
		Content    string `json:"content"`
		File       struct {
			FilePath   string `json:"file_path"`
			Content    string `json:"content"`
			NumLines   int    `json:"numLines"`
			StartLine  int    `json:"startLine"`
			TotalLines int    `json:"totalLines"`
		} `json:"file"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "text", report.Type)
	expectedPath, err := filepath.EvalSymlinks(filepath.Join(workspace, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, expectedPath, report.Path)
	require.Equal(t, "beta", report.Content)
	require.Equal(t, 2, report.StartLine)
	require.Equal(t, 1, report.LineCount)
	require.Equal(t, 2, report.NextOffset)
	require.Equal(t, 3, report.Total)
	require.Equal(t, 3, report.TotalLines)
	require.True(t, report.HasMore)
	require.Equal(t, report.Path, report.File.FilePath)
	require.Equal(t, "beta", report.File.Content)
	require.Equal(t, 1, report.File.NumLines)
	require.Equal(t, 2, report.File.StartLine)
	require.Equal(t, 3, report.File.TotalLines)

	out, err = ReadFileTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"notes.txt","offset":50}`))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "", report.Content)
	require.Equal(t, 4, report.StartLine)
	require.Equal(t, 0, report.LineCount)
	require.Equal(t, 3, report.NextOffset)
	require.False(t, report.HasMore)
	require.Equal(t, 0, report.File.NumLines)
	require.Equal(t, 4, report.File.StartLine)
}

func TestPowerShellToolExecutesForegroundAndBackground(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "pwsh-shim")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
printf 'ps:%s\n' "$*"
case "$*" in
  *"Exit 7"*) exit 7 ;;
  *"Start-Sleep"*) sleep 1 ;;
esac
`), 0o755))
	tool := PowerShellTool{Workspace: workspace, ConfigHome: configHome, Executable: script}

	out, err := tool.Execute(context.Background(), []byte(`{"command":"Write-Output ok","timeout":60000}`))
	require.NoError(t, err)
	require.Contains(t, out, `ps:-NoProfile -NonInteractive -Command Write-Output ok`)
	var foreground struct {
		Stdout                   string  `json:"stdout"`
		Stderr                   string  `json:"stderr"`
		ExitCode                 int     `json:"exit_code"`
		DurationMS               int64   `json:"duration_ms"`
		Interrupted              bool    `json:"interrupted"`
		ReturnCodeInterpretation *string `json:"returnCodeInterpretation"`
		NoOutputExpected         bool    `json:"noOutputExpected"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &foreground))
	require.Contains(t, foreground.Stdout, "Write-Output ok")
	require.Empty(t, foreground.Stderr)
	require.Equal(t, 0, foreground.ExitCode)
	require.GreaterOrEqual(t, foreground.DurationMS, int64(0))
	require.False(t, foreground.Interrupted)
	require.Nil(t, foreground.ReturnCodeInterpretation)
	require.False(t, foreground.NoOutputExpected)

	out, err = tool.Execute(context.Background(), []byte(`{"command":"Exit 7","timeout_ms":1000}`))
	require.NoError(t, err)
	var failed struct {
		ExitCode                 int    `json:"exit_code"`
		ReturnCodeInterpretation string `json:"returnCodeInterpretation"`
		Interrupted              bool   `json:"interrupted"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &failed))
	require.Equal(t, 7, failed.ExitCode)
	require.Equal(t, "exit_code:7", failed.ReturnCodeInterpretation)
	require.False(t, failed.Interrupted)

	out, err = tool.Execute(context.Background(), []byte(`{"command":"Start-Sleep slow","timeout_ms":20}`))
	require.NoError(t, err)
	var timedOut struct {
		ExitCode                 int              `json:"exit_code"`
		Interrupted              bool             `json:"interrupted"`
		ReturnCodeInterpretation string           `json:"returnCodeInterpretation"`
		StructuredContent        []map[string]any `json:"structuredContent"`
		Stderr                   string           `json:"stderr"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &timedOut))
	require.Equal(t, -1, timedOut.ExitCode)
	require.True(t, timedOut.Interrupted)
	require.Equal(t, "timeout", timedOut.ReturnCodeInterpretation)
	require.Contains(t, timedOut.Stderr, "Command exceeded timeout of 20 ms")
	require.Len(t, timedOut.StructuredContent, 1)
	require.Equal(t, "command.timeout", timedOut.StructuredContent[0]["event"])

	out, err = tool.Execute(context.Background(), []byte(`{"command":"Write-Output bg","run_in_background":true}`))
	require.NoError(t, err)
	require.Contains(t, out, `"background": true`)
	var payload struct {
		Task                      background.Task `json:"task"`
		BackgroundTaskID          string          `json:"backgroundTaskId"`
		BackgroundedByUser        bool            `json:"backgroundedByUser"`
		AssistantAutoBackgrounded bool            `json:"assistantAutoBackgrounded"`
		NoOutputExpected          bool            `json:"noOutputExpected"`
		Interrupted               bool            `json:"interrupted"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.NotEmpty(t, payload.Task.ID)
	require.Equal(t, payload.Task.ID, payload.BackgroundTaskID)
	require.True(t, payload.BackgroundedByUser)
	require.False(t, payload.AssistantAutoBackgrounded)
	require.True(t, payload.NoOutputExpected)
	require.False(t, payload.Interrupted)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(payload.Task.ID, 4096)
		return err == nil && strings.Contains(logs, `ps:-NoProfile -NonInteractive -Command Write-Output bg`)
	}, 20*time.Second, 50*time.Millisecond)
}

func TestDefaultShellPowerShellDelegatesBashTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "pwsh-shim")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
printf 'default-ps:%s\n' "$*"
`), 0o755))
	registry := NewRegistryWithOptions(workspace, RegistryOptions{
		ConfigHome:   configHome,
		DefaultShell: "powershell",
		PowerShell:   script,
	})

	out, err := registry.Execute(context.Background(), "bash", []byte(`{"command":"Write-Output ok","timeout":10000}`), nil)

	require.NoError(t, err)
	require.Contains(t, out, `default-ps:-NoProfile -NonInteractive -Command Write-Output ok`)
}

func TestBashToolReportsExitCodeAndDuration(t *testing.T) {
	workspace := t.TempDir()
	out, err := BashTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"command":"printf ok; exit 7"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "ok"`)
	require.Contains(t, out, `"exit_code": 7`)
	require.Contains(t, out, `"duration_ms":`)
	require.Contains(t, out, `"error": "exit status 7"`)
	var first struct {
		SandboxStatus             sandbox.SandboxExecutionStatus `json:"sandboxStatus"`
		Interrupted               bool                           `json:"interrupted"`
		DangerouslyDisableSandbox bool                           `json:"dangerouslyDisableSandbox"`
		ReturnCodeInterpretation  *string                        `json:"returnCodeInterpretation"`
		NoOutputExpected          bool                           `json:"noOutputExpected"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &first))
	require.False(t, first.SandboxStatus.Enabled)
	require.False(t, first.SandboxStatus.Active)
	require.NotNil(t, first.SandboxStatus.AllowedMounts)
	require.NotNil(t, first.SandboxStatus.Requested.AllowedMounts)
	require.False(t, first.Interrupted)
	require.False(t, first.DangerouslyDisableSandbox)
	require.NotNil(t, first.ReturnCodeInterpretation)
	require.Equal(t, "exit_code:7", *first.ReturnCodeInterpretation)
	require.False(t, first.NoOutputExpected)

	out, err = BashTool{Workspace: workspace, SandboxStrategy: "detect"}.Execute(context.Background(), []byte(`{"command":"printf bypass","timeout":1000,"dangerouslyDisableSandbox":true}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "bypass"`)
	require.NotContains(t, out, `"sandbox":`)
	var bypass struct {
		SandboxStatus             sandbox.SandboxExecutionStatus `json:"sandboxStatus"`
		DangerouslyDisableSandbox bool                           `json:"dangerouslyDisableSandbox"`
		ReturnCodeInterpretation  *string                        `json:"returnCodeInterpretation"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &bypass))
	require.False(t, bypass.SandboxStatus.Enabled)
	require.Equal(t, "disabled", bypass.SandboxStatus.ResolutionStatus)
	require.True(t, bypass.SandboxStatus.Requested.NamespaceRestrictions)
	require.True(t, bypass.DangerouslyDisableSandbox)
	require.Nil(t, bypass.ReturnCodeInterpretation)
}

func TestBashToolReportsTimeoutAndTruncatesOutput(t *testing.T) {
	workspace := t.TempDir()
	out, err := BashTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"command":"sleep 1","timeout_ms":20}`))
	require.NoError(t, err)
	var timeoutPayload struct {
		Interrupted              bool             `json:"interrupted"`
		ExitCode                 int              `json:"exit_code"`
		ReturnCodeInterpretation string           `json:"returnCodeInterpretation"`
		StructuredContent        []map[string]any `json:"structuredContent"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &timeoutPayload))
	require.True(t, timeoutPayload.Interrupted)
	require.Equal(t, -1, timeoutPayload.ExitCode)
	require.Equal(t, "timeout", timeoutPayload.ReturnCodeInterpretation)
	require.Len(t, timeoutPayload.StructuredContent, 1)
	require.Equal(t, "command.timeout", timeoutPayload.StructuredContent[0]["event"])

	out, err = BashTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"command":"# pytest\nsleep 1","timeout_ms":20}`))
	require.NoError(t, err)
	var testTimeoutPayload struct {
		ReturnCodeInterpretation string           `json:"returnCodeInterpretation"`
		StructuredContent        []map[string]any `json:"structuredContent"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &testTimeoutPayload))
	require.Equal(t, "test.hung", testTimeoutPayload.ReturnCodeInterpretation)
	require.Len(t, testTimeoutPayload.StructuredContent, 1)
	require.Equal(t, "test.hung", testTimeoutPayload.StructuredContent[0]["event"])
	require.Equal(t, "test_hang", testTimeoutPayload.StructuredContent[0]["failureClass"])

	configHome := t.TempDir()
	out, err = BashTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"command":"yes x | head -c 20000"}`))
	require.NoError(t, err)
	var truncPayload struct {
		Stdout              string `json:"stdout"`
		PersistedOutputPath string `json:"persistedOutputPath"`
		PersistedOutputSize int64  `json:"persistedOutputSize"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &truncPayload))
	require.Less(t, len(truncPayload.Stdout), 20000)
	require.Contains(t, truncPayload.Stdout, "[output truncated - exceeded 16384 bytes]")
	require.NotEmpty(t, truncPayload.PersistedOutputPath)
	require.Greater(t, truncPayload.PersistedOutputSize, int64(20000))
	require.FileExists(t, truncPayload.PersistedOutputPath)
	require.True(t, strings.HasPrefix(truncPayload.PersistedOutputPath, filepath.Join(configHome, "bash-output")))
	data, err := os.ReadFile(truncPayload.PersistedOutputPath)
	require.NoError(t, err)
	var persisted struct {
		Kind            string   `json:"kind"`
		Stdout          string   `json:"stdout"`
		TruncatedFields []string `json:"truncated_fields"`
	}
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.Equal(t, "bash_output", persisted.Kind)
	require.Len(t, persisted.Stdout, 20000)
	require.Equal(t, []string{"stdout"}, persisted.TruncatedFields)
}

func TestBashToolAcceptsSandboxRequestAliases(t *testing.T) {
	workspace := t.TempDir()
	out, err := BashTool{Workspace: workspace}.Execute(context.Background(), []byte(`{
		"command":"printf ok",
		"namespace_restrictions":false,
		"isolate_network":true,
		"filesystem_mode":"allow-list",
		"allowed_mounts":["logs"]
	}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "ok"`)
	var payload struct {
		SandboxStatus sandbox.SandboxExecutionStatus `json:"sandboxStatus"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.False(t, payload.SandboxStatus.Enabled)
	require.False(t, payload.SandboxStatus.Requested.NamespaceRestrictions)
	require.True(t, payload.SandboxStatus.Requested.NetworkIsolation)
	require.Equal(t, sandbox.FilesystemIsolationAllowList, payload.SandboxStatus.Requested.FilesystemMode)
	require.Equal(t, []string{filepath.Join(workspace, "logs")}, payload.SandboxStatus.AllowedMounts)

	_, err = BashTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"command":"printf bad","filesystemMode":"invalid"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported filesystem isolation mode")
}

func TestBashToolAppliesSandboxConfigDefaults(t *testing.T) {
	workspace := t.TempDir()
	enabled := false
	namespace := false
	network := true
	out, err := BashTool{
		Workspace: workspace,
		Sandbox: config.SandboxConfig{
			Enabled:               &enabled,
			NamespaceRestrictions: &namespace,
			NetworkIsolation:      &network,
			FilesystemMode:        "allow-list",
			AllowedMounts:         []string{"logs"},
		},
	}.Execute(context.Background(), []byte(`{"command":"printf ok"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "ok"`)
	var payload struct {
		SandboxStatus sandbox.SandboxExecutionStatus `json:"sandboxStatus"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.False(t, payload.SandboxStatus.Requested.Enabled)
	require.False(t, payload.SandboxStatus.Requested.NamespaceRestrictions)
	require.True(t, payload.SandboxStatus.Requested.NetworkIsolation)
	require.Equal(t, sandbox.FilesystemIsolationAllowList, payload.SandboxStatus.Requested.FilesystemMode)
	require.Equal(t, []string{filepath.Join(workspace, "logs")}, payload.SandboxStatus.AllowedMounts)

	enabled = true
	require.Equal(t, "detect", bashSandboxStrategy("", config.SandboxConfig{Enabled: &enabled}, false))
	require.Equal(t, "off", bashSandboxStrategy("off", config.SandboxConfig{Enabled: &enabled}, false))
	require.Equal(t, "off", bashSandboxStrategy("detect", config.SandboxConfig{Enabled: &enabled}, true))
}

func TestBashToolLoadsHookEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	sessionID := "session-1"
	dir := hookenv.Dir(configHome, sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessionstart-hook-1.sh"), []byte("export CODOG_TEST_HOOK_ENV=ready\n"), 0o600))

	ctx := ContextWithSessionID(context.Background(), sessionID)
	out, err := BashTool{Workspace: workspace, ConfigHome: configHome, ConfigEnv: map[string]string{"CODOG_TEST_HOOK_ENV": "config"}}.Execute(ctx, []byte(`{"command":"printf %s \"$CODOG_TEST_HOOK_ENV\""}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "ready"`)
}

func TestBashToolLoadsConfiguredEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	out, err := BashTool{
		Workspace: t.TempDir(),
		ConfigEnv: map[string]string{"CODOG_CONFIG_ENV": "ready"},
	}.Execute(context.Background(), []byte(`{"command":"printf %s \"$CODOG_CONFIG_ENV\""}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "ready"`)
}

func TestBashToolPersistsSessionCWD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	sessionID := "session-1"
	subdir := filepath.Join(workspace, "sub")
	require.NoError(t, os.Mkdir(subdir, 0o755))
	physicalSubdir, err := filepath.EvalSymlinks(subdir)
	require.NoError(t, err)
	ctx := ContextWithSessionID(context.Background(), sessionID)
	tool := BashTool{Workspace: workspace, ConfigHome: configHome}

	out, err := tool.Execute(ctx, []byte(`{"command":"cd sub"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"cwd_changed": true`)
	require.Contains(t, out, `"cwd": "`+escapeJSONSubstring(physicalSubdir)+`"`)

	out, err = tool.Execute(ctx, []byte(`{"command":"printf %s \"$PWD\""}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "`+escapeJSONSubstring(physicalSubdir)+`"`)

	registry := NewRegistryWithOptions(workspace, RegistryOptions{ConfigHome: configHome})
	out, err = registry.Execute(ctx, "Bash", []byte(`{"command":"printf %s \"$PWD\"","run_in_background":true}`), nil)
	require.NoError(t, err)
	var payload struct {
		Task background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Eventually(t, func() bool {
		output, err := registry.Execute(ctx, "BashOutput", []byte(`{"bash_id":"`+payload.Task.ID+`"}`), nil)
		return err == nil && strings.Contains(output, physicalSubdir)
	}, 5*time.Second, 50*time.Millisecond)
	out, err = registry.Execute(ctx, "KillBash", []byte(`{"bash_id":"`+payload.Task.ID+`"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"backgroundTaskId": "`+payload.Task.ID+`"`)
}

func TestBashToolPersistsSessionCWDUnderSandboxExec(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS sandbox-exec")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is unavailable")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	subdir := filepath.Join(workspace, "sub")
	require.NoError(t, os.Mkdir(subdir, 0o755))
	physicalSubdir, err := filepath.EvalSymlinks(subdir)
	require.NoError(t, err)
	ctx := ContextWithSessionID(context.Background(), "sandbox-session")
	tool := BashTool{Workspace: workspace, ConfigHome: configHome, SandboxStrategy: "sandbox-exec"}

	out, err := tool.Execute(ctx, []byte(`{"command":"printf LIVE_TOOL_OK && cd sub"}`))
	require.NoError(t, err)
	var result struct {
		Stdout        string                         `json:"stdout"`
		Stderr        string                         `json:"stderr"`
		CWD           string                         `json:"cwd"`
		SandboxStatus sandbox.SandboxExecutionStatus `json:"sandboxStatus"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "LIVE_TOOL_OK", result.Stdout)
	require.Empty(t, result.Stderr)
	require.Equal(t, physicalSubdir, result.CWD)
	require.Empty(t, result.SandboxStatus.InternalWritablePaths)
	require.NotContains(t, out, ".bash-cwd-")
	entries, err := os.ReadDir(shellstate.Dir(configHome, "sandbox-session"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "cwd", entries[0].Name())

	out, err = tool.Execute(ctx, []byte(`{"command":"printf %s \"$PWD\""}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "`+escapeJSONSubstring(physicalSubdir)+`"`)
}

func TestBashToolBackgroundOutputAndKillAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	sessionID := "session-1"
	dir := hookenv.Dir(configHome, sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessionstart-hook-0.sh"), []byte("CODOG_BG_HOOK_ENV=hook-bg\n"), 0o600))
	registry := NewRegistryWithOptions(workspace, RegistryOptions{ConfigHome: configHome})
	ctx := ContextWithSessionID(context.Background(), sessionID)
	out, err := registry.Execute(ctx, "Bash", []byte(`{"command":"printf \"bash-ready:%s\" \"$CODOG_BG_HOOK_ENV\"; sleep 5","run_in_background":true}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"background": true`)
	require.Contains(t, out, `"kind": "bash"`)
	var payload struct {
		Task                      background.Task `json:"task"`
		BackgroundTaskID          string          `json:"backgroundTaskId"`
		BackgroundedByUser        bool            `json:"backgroundedByUser"`
		AssistantAutoBackgrounded bool            `json:"assistantAutoBackgrounded"`
		NoOutputExpected          bool            `json:"noOutputExpected"`
		Interrupted               bool            `json:"interrupted"`
		DangerouslyDisableSandbox bool            `json:"dangerouslyDisableSandbox"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.NotEmpty(t, payload.Task.ID)
	require.Equal(t, payload.Task.ID, payload.BackgroundTaskID)
	require.False(t, payload.BackgroundedByUser)
	require.False(t, payload.AssistantAutoBackgrounded)
	require.True(t, payload.NoOutputExpected)
	require.False(t, payload.Interrupted)
	require.False(t, payload.DangerouslyDisableSandbox)
	require.Eventually(t, func() bool {
		output, err := registry.Execute(ctx, "BashOutput", []byte(`{"bash_id":"`+payload.Task.ID+`"}`), nil)
		return err == nil && strings.Contains(output, "bash-ready:hook-bg")
	}, 5*time.Second, 50*time.Millisecond)
	out, err = registry.Execute(ctx, "BashOutputTool", []byte(`{"bash_id":"`+payload.Task.ID+`"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"bash_id": "`+payload.Task.ID+`"`)
	require.Contains(t, out, `"kind": "bash"`)
	var outputPayload struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		RawOutputPath    string `json:"rawOutputPath"`
		Output           string `json:"output"`
		Stdout           string `json:"stdout"`
		Interrupted      bool   `json:"interrupted"`
		NoOutputExpected bool   `json:"noOutputExpected"`
		NextOffset       int64  `json:"nextOffset"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &outputPayload))
	require.Equal(t, payload.Task.ID, outputPayload.BackgroundTaskID)
	require.FileExists(t, outputPayload.RawOutputPath)
	require.Contains(t, outputPayload.Output, "bash-ready:hook-bg")
	require.Equal(t, outputPayload.Output, outputPayload.Stdout)
	require.False(t, outputPayload.Interrupted)
	require.False(t, outputPayload.NoOutputExpected)
	out, err = registry.Execute(ctx, "BashOutput", []byte(fmt.Sprintf(`{"bash_id":%q,"offset":%d,"block":true,"timeout":20}`, payload.Task.ID, outputPayload.NextOffset)), nil)
	require.NoError(t, err)
	var timedOutOutput struct {
		Stdout     string `json:"stdout"`
		Offset     int64  `json:"offset"`
		NextOffset int64  `json:"nextOffset"`
		BytesRead  int    `json:"bytesRead"`
		TimedOut   bool   `json:"timedOut"`
		TimeoutMS  int    `json:"timeoutMs"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &timedOutOutput))
	require.Empty(t, timedOutOutput.Stdout)
	require.Equal(t, outputPayload.NextOffset, timedOutOutput.Offset)
	require.Equal(t, outputPayload.NextOffset, timedOutOutput.NextOffset)
	require.Equal(t, 0, timedOutOutput.BytesRead)
	require.True(t, timedOutOutput.TimedOut)
	require.Equal(t, 20, timedOutOutput.TimeoutMS)
	out, err = registry.Execute(ctx, "BashOutput", []byte(`{"bash_id":"`+payload.Task.ID+`","offset":0,"limit":4}`), nil)
	require.NoError(t, err)
	var offsetOutput struct {
		Stdout     string `json:"stdout"`
		Offset     int64  `json:"offset"`
		NextOffset int64  `json:"nextOffset"`
		BytesRead  int    `json:"bytesRead"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &offsetOutput))
	require.Equal(t, "bash", offsetOutput.Stdout)
	require.Equal(t, int64(0), offsetOutput.Offset)
	require.Equal(t, int64(4), offsetOutput.NextOffset)
	require.Equal(t, 4, offsetOutput.BytesRead)
	out, err = registry.Execute(ctx, "Bash", []byte(`{"command":"sleep 0.1; printf delayed-bash","run_in_background":true}`), nil)
	require.NoError(t, err)
	var delayed struct {
		Task background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &delayed))
	out, err = registry.Execute(ctx, "BashOutput", []byte(`{"bash_id":"`+delayed.Task.ID+`","offset":0,"limit":64,"block":true,"timeout_ms":2000}`), nil)
	require.NoError(t, err)
	var blockedOutput struct {
		Stdout     string `json:"stdout"`
		NextOffset int64  `json:"nextOffset"`
		TimedOut   bool   `json:"timedOut"`
		TimeoutMS  int    `json:"timeoutMs"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &blockedOutput))
	require.Equal(t, "delayed-bash", blockedOutput.Stdout)
	require.Greater(t, blockedOutput.NextOffset, int64(0))
	require.False(t, blockedOutput.TimedOut)
	require.Equal(t, 2000, blockedOutput.TimeoutMS)
	out, err = registry.Execute(ctx, "BashOutput", []byte(`{"bash_id":"`+payload.Task.ID+`","limit_bytes":4}`), nil)
	require.NoError(t, err)
	var limitedOutput struct {
		PersistedOutputPath string `json:"persistedOutputPath"`
		PersistedOutputSize int64  `json:"persistedOutputSize"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &limitedOutput))
	require.Equal(t, outputPayload.RawOutputPath, limitedOutput.PersistedOutputPath)
	require.Greater(t, limitedOutput.PersistedOutputSize, int64(4))

	out, err = registry.Execute(ctx, "KillBash", []byte(`{"bash_id":"`+payload.Task.ID+`"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"status": "stopped"`)
	var killed struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		Interrupted      bool   `json:"interrupted"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &killed))
	require.Equal(t, payload.Task.ID, killed.BackgroundTaskID)
	require.True(t, killed.Interrupted)
}

func TestFileToolsAllowAdditionalDirs(t *testing.T) {
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))
	extraFile := filepath.Join(extra, "notes.txt")
	require.NoError(t, os.WriteFile(extraFile, []byte("alpha\nbeta\n"), 0o644))

	input, _ := json.Marshal(map[string]string{"path": extraFile})
	out, err := ReadFileTool{Workspace: workspace, AdditionalDirs: []string{extra}}.Execute(context.Background(), input)
	require.NoError(t, err)
	require.Contains(t, out, "alpha")

	writeInput, _ := json.Marshal(map[string]string{"path": filepath.Join(extra, "new", "created.txt"), "content": "created"})
	out, err = WriteFileTool{Workspace: workspace, AdditionalDirs: []string{extra}}.Execute(context.Background(), writeInput)
	require.NoError(t, err)
	require.Contains(t, out, "create")
	require.FileExists(t, filepath.Join(extra, "new", "created.txt"))

	grepInput, _ := json.Marshal(map[string]any{"pattern": "beta", "path": extra, "limit": 5})
	out, err = GrepTool{Workspace: workspace, AdditionalDirs: []string{extra}}.Execute(context.Background(), grepInput)
	require.NoError(t, err)
	require.Contains(t, out, extraFile)
}

func TestLSToolListsScopedDirectory(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "ignored-dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "main.go"), []byte("package pkg\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("docs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".secret"), []byte("hidden\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ignored.txt"), []byte("ignored\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "trace.log"), []byte("ignored\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("ignored.txt\n*.log\nignored-dir/\n"), 0o644))

	out, err := LSTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"ignore":["README.md"]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "ls"`)
	require.Contains(t, out, `"name": "pkg"`)
	require.Contains(t, out, `"type": "directory"`)
	require.NotContains(t, out, `"name": "README.md"`)
	require.NotContains(t, out, `.secret`)
	require.NotContains(t, out, `ignored.txt`)
	require.NotContains(t, out, `trace.log`)
	require.NotContains(t, out, `ignored-dir`)
	var report struct {
		Files      []string `json:"files"`
		Filenames  []string `json:"filenames"`
		NumFiles   int      `json:"numFiles"`
		NumFilesSN int      `json:"num_files"`
		NumEntries int      `json:"numEntries"`
		DurationMS int64    `json:"durationMs"`
		DurationMs int64    `json:"duration_ms"`
		Truncated  bool     `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, []string{"pkg"}, report.Files)
	require.Equal(t, report.Files, report.Filenames)
	require.Equal(t, 1, report.NumFiles)
	require.Equal(t, report.NumFiles, report.NumFilesSN)
	require.Equal(t, 1, report.NumEntries)
	require.GreaterOrEqual(t, report.DurationMS, int64(0))
	require.Equal(t, report.DurationMS, report.DurationMs)
	require.False(t, report.Truncated)

	out, err = NewRegistry(workspace).Execute(context.Background(), "LS", []byte(`{"path":".","hidden":true}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"hidden": true`)
	out, err = NewRegistry(workspace).Execute(context.Background(), "LS", []byte(`{"path":".","hidden":true,"limit":1}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"truncated": true`)
}

func TestLSToolUsesNestedIgnoreFiles(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg", "cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", ".clawignore"), []byte("cache/\n*.tmp\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "main.go"), []byte("package pkg\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "draft.tmp"), []byte("ignored\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "cache", "data.txt"), []byte("ignored\n"), 0o644))

	out, err := NewRegistry(workspace).Execute(context.Background(), "LS", []byte(`{"path":"pkg"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `main.go`)
	require.NotContains(t, out, `draft.tmp`)
	require.NotContains(t, out, `"name": "cache"`)
	var report struct {
		Files     []string `json:"files"`
		NumFiles  int      `json:"numFiles"`
		Truncated bool     `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, []string{filepath.ToSlash(filepath.Join("pkg", "main.go"))}, report.Files)
	require.Equal(t, 1, report.NumFiles)
	require.False(t, report.Truncated)
}

func TestGrepToolSupportsClaudeOutputModes(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "a.go"), []byte("Needle\nneedle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "b.py"), []byte("needle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "c.go"), []byte("nothing\n"), 0o644))

	registry := NewRegistry(workspace)
	definition := GrepTool{}.Definition()
	outputModeSchema, ok := definition.InputSchema["properties"].(map[string]any)["output_mode"].(map[string]any)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"content", "matches", "lines", "files_with_matches", "files", "paths", "filenames", "names", "count", "counts"}, outputModeSchema["enum"])

	out, err := registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"output_mode": "files_with_matches"`)
	require.Contains(t, out, `"filenames":`)
	require.Contains(t, out, "a.go")
	require.Contains(t, out, "b.py")
	require.NotContains(t, out, `"matches":`)
	var filesReport struct {
		Mode          string   `json:"mode"`
		Filenames     []string `json:"filenames"`
		NumFiles      int      `json:"numFiles"`
		Content       *string  `json:"content"`
		NumLines      *int     `json:"numLines"`
		NumMatches    *int     `json:"numMatches"`
		AppliedLimit  int      `json:"appliedLimit"`
		AppliedOffset int      `json:"appliedOffset"`
		DurationMS    int64    `json:"durationMs"`
		DurationMs    int64    `json:"duration_ms"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &filesReport))
	require.Equal(t, "files_with_matches", filesReport.Mode)
	require.Equal(t, []string{"a.go", "b.py"}, filesReport.Filenames)
	require.Equal(t, 2, filesReport.NumFiles)
	require.Nil(t, filesReport.Content)
	require.Nil(t, filesReport.NumLines)
	require.Nil(t, filesReport.NumMatches)
	require.Equal(t, 250, filesReport.AppliedLimit)
	require.Equal(t, 0, filesReport.AppliedOffset)
	require.GreaterOrEqual(t, filesReport.DurationMS, int64(0))
	require.Equal(t, filesReport.DurationMS, filesReport.DurationMs)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"paths"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"output_mode": "files_with_matches"`)
	require.Contains(t, out, `"mode": "files_with_matches"`)
	require.Contains(t, out, "a.go")
	require.Contains(t, out, "b.py")

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"files_with_matches","type":"go","-i":true,"head_limit":1}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"output_mode": "files_with_matches"`)
	require.Contains(t, out, "a.go")
	require.NotContains(t, out, "b.py")

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"files_with_matches","head_limit":0}`), nil)
	require.NoError(t, err)
	var unlimitedFilesReport struct {
		Filenames    []string `json:"filenames"`
		AppliedLimit *int     `json:"appliedLimit"`
		Truncated    bool     `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &unlimitedFilesReport))
	require.Equal(t, []string{"a.go", "b.py"}, unlimitedFilesReport.Filenames)
	require.Nil(t, unlimitedFilesReport.AppliedLimit)
	require.False(t, unlimitedFilesReport.Truncated)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"files_with_matches","limit":1}`), nil)
	require.NoError(t, err)
	var legacyLimitFilesReport struct {
		Filenames    []string `json:"filenames"`
		AppliedLimit int      `json:"appliedLimit"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &legacyLimitFilesReport))
	require.Equal(t, []string{"a.go"}, legacyLimitFilesReport.Filenames)
	require.Equal(t, 1, legacyLimitFilesReport.AppliedLimit)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"count","-i":true}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"output_mode": "count"`)
	require.Contains(t, out, "a.go")
	require.Contains(t, out, `"count": 2`)
	require.Contains(t, out, "b.py")
	var countReport struct {
		Mode       string   `json:"mode"`
		Filenames  []string `json:"filenames"`
		NumFiles   int      `json:"numFiles"`
		NumMatches int      `json:"numMatches"`
		Content    *string  `json:"content"`
		NumLines   *int     `json:"numLines"`
		DurationMS int64    `json:"durationMs"`
		DurationMs int64    `json:"duration_ms"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &countReport))
	require.Equal(t, "count", countReport.Mode)
	require.Equal(t, []string{"a.go", "b.py"}, countReport.Filenames)
	require.Equal(t, 2, countReport.NumFiles)
	require.Equal(t, 3, countReport.NumMatches)
	require.Nil(t, countReport.Content)
	require.Nil(t, countReport.NumLines)
	require.GreaterOrEqual(t, countReport.DurationMS, int64(0))
	require.Equal(t, countReport.DurationMS, countReport.DurationMs)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"counts","-i":true}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"output_mode": "count"`)
	require.Contains(t, out, `"mode": "count"`)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"content","offset":1,"head_limit":1}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"output_mode": "content"`)
	require.Contains(t, out, `"line": 1`)
	require.Contains(t, out, "b.py")
	require.NotContains(t, out, "a.go")
	var contentReport struct {
		Mode          string   `json:"mode"`
		Filenames     []string `json:"filenames"`
		NumFiles      int      `json:"numFiles"`
		Content       string   `json:"content"`
		NumLines      int      `json:"numLines"`
		AppliedLimit  int      `json:"appliedLimit"`
		AppliedOffset int      `json:"appliedOffset"`
		DurationMS    int64    `json:"durationMs"`
		DurationMs    int64    `json:"duration_ms"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &contentReport))
	require.Equal(t, "content", contentReport.Mode)
	require.Equal(t, []string{"b.py"}, contentReport.Filenames)
	require.Equal(t, 1, contentReport.NumFiles)
	require.Equal(t, "b.py:1:needle", contentReport.Content)
	require.Equal(t, 1, contentReport.NumLines)
	require.Equal(t, 1, contentReport.AppliedLimit)
	require.Equal(t, 1, contentReport.AppliedOffset)
	require.GreaterOrEqual(t, contentReport.DurationMS, int64(0))
	require.Equal(t, contentReport.DurationMS, contentReport.DurationMs)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"matches","head_limit":1}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"output_mode": "content"`)
	require.Contains(t, out, `"mode": "content"`)
	require.Contains(t, out, `"matches": [`)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "context.go"), []byte("before one\nmatch target\nafter one\nafter two\nafter three\n"), 0o644))
	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"target","output_mode":"content","-B":1,"-A":2}`), nil)
	require.NoError(t, err)
	var contextReport struct {
		Matches []struct {
			Path   string `json:"path"`
			Line   int    `json:"line"`
			Text   string `json:"text"`
			Before []struct {
				Line int    `json:"line"`
				Text string `json:"text"`
			} `json:"before"`
			After []struct {
				Line int    `json:"line"`
				Text string `json:"text"`
			} `json:"after"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &contextReport))
	require.Len(t, contextReport.Matches, 1)
	require.Equal(t, "context.go", contextReport.Matches[0].Path)
	require.Equal(t, 2, contextReport.Matches[0].Line)
	require.Equal(t, "match target", contextReport.Matches[0].Text)
	require.Equal(t, []struct {
		Line int    `json:"line"`
		Text string `json:"text"`
	}{{Line: 1, Text: "before one"}}, contextReport.Matches[0].Before)
	require.Equal(t, []struct {
		Line int    `json:"line"`
		Text string `json:"text"`
	}{{Line: 3, Text: "after one"}, {Line: 4, Text: "after two"}}, contextReport.Matches[0].After)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"target","output_mode":"content","context":1}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"before one"`)
	require.Contains(t, out, `"after one"`)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"target","output_mode":"content","-n":false}`), nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &contentReport))
	require.Equal(t, "context.go:match target", contentReport.Content)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "multi.go"), []byte("alpha start\nmiddle\nomega end\n"), 0o644))
	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"alpha.*omega","glob":"multi.go","output_mode":"content"}`), nil)
	require.NoError(t, err)
	var multiReport struct {
		Filenames []string `json:"filenames"`
		Content   string   `json:"content"`
		Matches   []struct {
			Line    int    `json:"line"`
			EndLine int    `json:"end_line"`
			Text    string `json:"text"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &multiReport))
	require.Empty(t, multiReport.Matches)
	require.Empty(t, multiReport.Content)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"alpha.*omega","glob":"multi.go","output_mode":"content","multiline":true}`), nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &multiReport))
	require.Equal(t, []string{"multi.go"}, multiReport.Filenames)
	require.Equal(t, "multi.go:1:alpha start\nmulti.go:2:middle\nmulti.go:3:omega end", multiReport.Content)
	require.Len(t, multiReport.Matches, 1)
	require.Equal(t, 1, multiReport.Matches[0].Line)
	require.Equal(t, 3, multiReport.Matches[0].EndLine)
	require.Equal(t, "alpha start\nmiddle\nomega", multiReport.Matches[0].Text)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"alpha.*omega","glob":"multi.go","output_mode":"count","multiline":true}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, "multi.go")
	require.Contains(t, out, `"count": 1`)
}

func TestNormalizeGrepOutputMode(t *testing.T) {
	cases := map[string]string{
		"":                   "files_with_matches",
		"files":              "files_with_matches",
		"paths":              "files_with_matches",
		"file-paths":         "files_with_matches",
		"files with matches": "files_with_matches",
		"matches":            "content",
		"lines":              "content",
		"contents":           "content",
		"counts":             "count",
		"count-matches":      "count",
		"bad":                "",
	}
	for input, want := range cases {
		require.Equal(t, want, normalizeGrepOutputMode(input), input)
	}

	_, err := GrepTool{Workspace: t.TempDir()}.Execute(context.Background(), []byte(`{"pattern":"needle","output_mode":"contnet"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported grep output_mode "contnet"`)
	require.Contains(t, err.Error(), `did you mean "content"?`)
}

func TestGrepToolReportsDurationAndRealTruncation(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("needle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "b.txt"), []byte("needle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "c.txt"), []byte("needle\n"), 0o644))

	registry := NewRegistry(workspace)
	out, err := registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"files_with_matches","head_limit":3}`), nil)
	require.NoError(t, err)
	var filesReport struct {
		Filenames  []string `json:"filenames"`
		NumFiles   int      `json:"numFiles"`
		DurationMS int64    `json:"durationMs"`
		DurationMs int64    `json:"duration_ms"`
		Truncated  bool     `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &filesReport))
	require.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, filesReport.Filenames)
	require.Equal(t, 3, filesReport.NumFiles)
	require.GreaterOrEqual(t, filesReport.DurationMS, int64(0))
	require.Equal(t, filesReport.DurationMS, filesReport.DurationMs)
	require.False(t, filesReport.Truncated)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"files_with_matches","head_limit":2}`), nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &filesReport))
	require.Equal(t, []string{"a.txt", "b.txt"}, filesReport.Filenames)
	require.Equal(t, 2, filesReport.NumFiles)
	require.True(t, filesReport.Truncated)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"content","head_limit":3}`), nil)
	require.NoError(t, err)
	var contentReport struct {
		Content    string `json:"content"`
		NumLines   int    `json:"numLines"`
		DurationMS int64  `json:"durationMs"`
		DurationMs int64  `json:"duration_ms"`
		Truncated  bool   `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &contentReport))
	require.Equal(t, "a.txt:1:needle\nb.txt:1:needle\nc.txt:1:needle", contentReport.Content)
	require.Equal(t, 3, contentReport.NumLines)
	require.GreaterOrEqual(t, contentReport.DurationMS, int64(0))
	require.Equal(t, contentReport.DurationMS, contentReport.DurationMs)
	require.False(t, contentReport.Truncated)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"content","head_limit":2}`), nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &contentReport))
	require.Equal(t, "a.txt:1:needle\nb.txt:1:needle", contentReport.Content)
	require.Equal(t, 2, contentReport.NumLines)
	require.True(t, contentReport.Truncated)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"count","head_limit":3}`), nil)
	require.NoError(t, err)
	var countReport struct {
		Counts     []map[string]any `json:"counts"`
		NumFiles   int              `json:"numFiles"`
		DurationMS int64            `json:"durationMs"`
		DurationMs int64            `json:"duration_ms"`
		Truncated  bool             `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &countReport))
	require.Len(t, countReport.Counts, 3)
	require.Equal(t, 3, countReport.NumFiles)
	require.GreaterOrEqual(t, countReport.DurationMS, int64(0))
	require.Equal(t, countReport.DurationMS, countReport.DurationMs)
	require.False(t, countReport.Truncated)

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"count","head_limit":2}`), nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &countReport))
	require.Len(t, countReport.Counts, 2)
	require.Equal(t, 2, countReport.NumFiles)
	require.True(t, countReport.Truncated)
}

func TestGrepAndGlobSupportRecursiveGlobstar(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "root.go"), []byte("needle root\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "main.go"), []byte("needle main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "pkg", "nested.go"), []byte("needle nested\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "pkg", "notes.md"), []byte("needle docs\n"), 0o644))

	registry := NewRegistry(workspace)
	out, err := registry.Execute(context.Background(), "Glob", []byte(`{"pattern":"**/*.go"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, "root.go")
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "main.go")))
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "pkg", "nested.go")))
	require.NotContains(t, out, "notes.md")

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","glob":"src/**/*.go","output_mode":"files_with_matches"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "main.go")))
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "pkg", "nested.go")))
	require.NotContains(t, out, "root.go")
	require.NotContains(t, out, "notes.md")

	out, err = registry.Execute(context.Background(), "Glob", []byte(`{"pattern":"src/**/*.{go,md}"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "main.go")))
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "pkg", "nested.go")))
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "pkg", "notes.md")))

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","glob":"src/**/*.{go,md}","output_mode":"files_with_matches"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "main.go")))
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "pkg", "nested.go")))
	require.Contains(t, out, filepath.ToSlash(filepath.Join("src", "pkg", "notes.md")))
}

func TestGrepAndGlobRespectGitignoreWhenConfigured(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "ignored-dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("ignored.txt\n*.log\nignored-dir/\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "kept.txt"), []byte("needle kept\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ignored.txt"), []byte("needle ignored\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "trace.log"), []byte("needle log\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ignored-dir", "nested.txt"), []byte("needle nested\n"), 0o644))

	registry := NewRegistryWithOptions(workspace, RegistryOptions{RespectGitignore: true})
	out, err := registry.Execute(context.Background(), "Glob", []byte(`{"pattern":"**/*"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, "kept.txt")
	require.NotContains(t, out, "ignored.txt")
	require.NotContains(t, out, "trace.log")
	require.NotContains(t, out, "ignored-dir")

	out, err = registry.Execute(context.Background(), "Grep", []byte(`{"pattern":"needle","output_mode":"files_with_matches"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, "kept.txt")
	require.NotContains(t, out, "ignored.txt")
	require.NotContains(t, out, "trace.log")
	require.NotContains(t, out, "ignored-dir")

	registry = NewRegistryWithOptions(workspace, RegistryOptions{RespectGitignore: false})
	out, err = registry.Execute(context.Background(), "Glob", []byte(`{"pattern":"**/*"}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, "ignored.txt")
	require.Contains(t, out, "trace.log")
	require.Contains(t, out, filepath.ToSlash(filepath.Join("ignored-dir", "nested.txt")))
}

func TestGlobToolReportsCompatibilityMetadataAndRealTruncation(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "a.go"), []byte("package a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "b.go"), []byte("package b\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "c.go"), []byte("package c\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("# notes\n"), 0o644))

	registry := NewRegistry(workspace)
	out, err := registry.Execute(context.Background(), "Glob", []byte(`{"pattern":"*.go","limit":2}`), nil)
	require.NoError(t, err)
	var report struct {
		Files      []string `json:"files"`
		Filenames  []string `json:"filenames"`
		NumFiles   int      `json:"numFiles"`
		DurationMS int64    `json:"durationMs"`
		DurationMs int64    `json:"duration_ms"`
		Truncated  bool     `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, []string{"a.go", "b.go"}, report.Files)
	require.Equal(t, report.Files, report.Filenames)
	require.Equal(t, 2, report.NumFiles)
	require.GreaterOrEqual(t, report.DurationMS, int64(0))
	require.Equal(t, report.DurationMS, report.DurationMs)
	require.True(t, report.Truncated)

	out, err = registry.Execute(context.Background(), "Glob", []byte(`{"pattern":"*.md","limit":1}`), nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, []string{"notes.md"}, report.Files)
	require.Equal(t, 1, report.NumFiles)
	require.False(t, report.Truncated)
}

func TestDeriveGlobWalkRootUsesFixedPrefix(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src", "pkg"), 0o755))

	require.Equal(t, filepath.Join(workspace, "src", "pkg"), deriveGlobWalkRoot(workspace, "src/pkg/**/*.go"))
	require.Equal(t, filepath.Join(workspace, "src"), deriveGlobWalkRoot(workspace, "src/**/*.{go,md}"))
	require.Equal(t, workspace, deriveGlobWalkRoot(workspace, "**/*.go"))
	require.Equal(t, workspace, deriveGlobWalkRoot(workspace, "../*.go"))
	require.Equal(t, workspace, deriveGlobWalkRoot(workspace, "missing/**/*.go"))
}

func TestEditFileRequiresUniqueMatch(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "a.txt")
	if err := os.WriteFile(path, []byte("one\none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"path":       "a.txt",
		"old_string": "one",
		"new_string": "two",
	})
	_, err := EditFileTool{Workspace: workspace}.Execute(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "appears 2 times")
}

func TestMultiEditAppliesAtomically(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "a.txt")
	require.NoError(t, os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644))

	out, err := MultiEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"a.txt","edits":[{"old_string":"one","new_string":"1"},{"old_string":"two","new_string":"2"}]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"replacements": 2`)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "1\n2\nthree\n", string(data))

	_, err = MultiEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"a.txt","edits":[{"old_string":"1","new_string":"one"},{"old_string":"missing","new_string":"x"}]}`))
	require.Error(t, err)
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "1\n2\nthree\n", string(data))
}

func TestPrompterRules(t *testing.T) {
	p := &Prompter{
		Mode:      PermissionAllow,
		DenyRules: []string{"bash:rm -rf"},
	}
	require.Error(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`)))

	p = &Prompter{
		Mode:       PermissionReadOnly,
		AllowRules: []string{"bash:go test"},
	}
	require.NoError(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"go test ./..."}`)))

	p = &Prompter{
		Mode:      PermissionAllow,
		DenyRules: []string{"Bash(rm -rf:*)"},
	}
	require.Error(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`)))

	p = &Prompter{
		Mode:       PermissionReadOnly,
		AllowRules: []string{"Bash(go test:*)"},
	}
	require.NoError(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"go test ./..."}`)))

	p = &Prompter{
		Mode:        PermissionAllow,
		DeniedTools: []string{"bash"},
	}
	require.Error(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"pwd"}`)))

	p = &Prompter{
		Mode:        PermissionAllow,
		DeniedTools: []string{"Read"},
	}
	require.Error(t, p.Authorize("read_file", PermissionReadOnly, []byte(`{"path":"README.md"}`)))

	p = &Prompter{
		Mode:        PermissionAllow,
		DeniedTools: []string{"mcp__playwright__*"},
	}
	require.Error(t, p.Authorize("mcp__playwright__click", PermissionReadOnly, []byte(`{}`)))
}

func TestCanonicalToolNameAcceptsClaudeStyleAliases(t *testing.T) {
	require.Equal(t, "read_file", CanonicalToolName("Read"))
	require.Equal(t, "read_file", CanonicalToolName("read_file"))
	require.Equal(t, "write_file", CanonicalToolName("Write"))
	require.Equal(t, "multi_edit", CanonicalToolName("MultiEdit"))
	require.Equal(t, "apply_patch", CanonicalToolName("ApplyPatch"))
	require.Equal(t, "bash_output", CanonicalToolName("BashOutput"))
	require.Equal(t, "bash_output", CanonicalToolName("BashOutputTool"))
	require.Equal(t, "retrieve_context", CanonicalToolName("RetrieveContextTool"))
	require.Equal(t, "enter_plan_mode", CanonicalToolName("EnterPlanMode"))
	require.Equal(t, "exit_plan_mode", CanonicalToolName("ExitPlanMode"))
	require.Equal(t, "mcp", CanonicalToolName("MCP"))
	require.Equal(t, "read_file", CanonicalToolName("ReadFile"))
	require.Equal(t, "structured_output", CanonicalToolName("StructuredOutputTool"))
	require.Equal(t, "report_backpressure", CanonicalToolName("ReportBackpressureTool"))
	require.Equal(t, "report_schema", CanonicalToolName("ReportSchemaTool"))
	require.Equal(t, "provisional_status", CanonicalToolName("ProvisionalStatusTool"))
	require.Equal(t, "tool_search", CanonicalToolName("ToolSearch"))
	require.Equal(t, "sleep", CanonicalToolName("SleepTool"))
	require.Equal(t, "repl", CanonicalToolName("REPLTool"))
	require.Equal(t, "permission_check", CanonicalToolName("PermissionCheckTool"))
	require.Equal(t, "git_diff", CanonicalToolName("GitDiffTool"))
	require.Equal(t, "git_log", CanonicalToolName("GitLogTool"))
	require.Equal(t, "mcp__server__tool", CanonicalToolName("mcp__server__tool"))

	aliases := ClaudeToolAliases()
	require.Equal(t, "web_fetch", aliases["WebFetch"])
	require.Equal(t, "retrieve_context", aliases["RetrieveContextTool"])
	require.Equal(t, "mcp", aliases["MCP"])
	require.Equal(t, "brief", aliases["Brief"])
	require.Equal(t, "config", aliases["Config"])
	require.Equal(t, "read_file", aliases["ReadFile"])
	require.Equal(t, "read_file", aliases["FileReadTool"])
	require.Equal(t, "write_file", aliases["WriteFile"])
	require.Equal(t, "edit_file", aliases["EditFile"])
	require.Equal(t, "multi_edit", aliases["MultiEditFile"])
	require.Equal(t, "apply_patch", aliases["ApplyPatchTool"])
	require.Equal(t, "enter_plan_mode", aliases["EnterPlanMode"])
	require.Equal(t, "exit_plan_mode", aliases["ExitPlanMode"])
	require.Equal(t, "exit_plan_mode", aliases["ExitPlanModeV2"])
	require.Equal(t, "send_user_message", aliases["SendUserMessageTool"])
	require.Equal(t, "skill", aliases["Skill"])
	require.Equal(t, "structured_output", aliases["StructuredOutputTool"])
	require.Equal(t, "report_backpressure", aliases["ReportBackpressureTool"])
	require.Equal(t, "report_schema", aliases["ReportSchemaTool"])
	require.Equal(t, "provisional_status", aliases["ProvisionalStatusTool"])
	require.Equal(t, "permission_check", aliases["PermissionCheck"])
	require.Equal(t, "permission_check", aliases["TestingPermission"])
	require.Equal(t, "tool_search", aliases["ToolSearch"])
	require.Equal(t, "sleep", aliases["SleepTool"])
	require.Equal(t, "repl", aliases["REPLTool"])
	require.Equal(t, "git_blame", aliases["GitBlameTool"])
	require.Equal(t, "git_diff", aliases["GitDiffTool"])
	require.Equal(t, "git_log", aliases["GitLogTool"])
	require.Equal(t, "git_show", aliases["GitShowTool"])
	require.Equal(t, "git_status", aliases["GitStatus"])
	aliases["WebFetch"] = "changed"
	require.Equal(t, "web_fetch", ClaudeToolAliases()["WebFetch"])
}

func TestClaudeToolAliasesCoverArchivedToolEntries(t *testing.T) {
	archivedToolEntries := []string{
		"AgentTool",
		"AskUserQuestionTool",
		"BashTool",
		"BriefTool",
		"ConfigTool",
		"CronCreateTool",
		"CronDeleteTool",
		"CronListTool",
		"EnterPlanMode",
		"EnterPlanModeTool",
		"EnterWorktreeTool",
		"ExitPlanMode",
		"ExitPlanModeV2Tool",
		"ExitWorktreeTool",
		"FileEditTool",
		"FileReadTool",
		"FileWriteTool",
		"GlobTool",
		"GrepTool",
		"LSPTool",
		"ListMcpResourcesTool",
		"MCPTool",
		"McpAuthTool",
		"NotebookEditTool",
		"PermissionCheckTool",
		"PowerShellTool",
		"ReadMcpResourceTool",
		"RemoteTriggerTool",
		"SendMessageTool",
		"SkillTool",
		"SleepTool",
		"REPLTool",
		"SyntheticOutputTool",
		"TaskCreateTool",
		"TaskGetTool",
		"TaskListTool",
		"TaskOutputTool",
		"TaskStopTool",
		"TaskUpdateTool",
		"TeamCreateTool",
		"TeamDeleteTool",
		"TestingPermissionTool",
		"TodoWriteTool",
		"ToolSearchTool",
		"WebFetchTool",
		"WebSearchTool",
	}

	aliases := ClaudeToolAliases()
	registry := NewRegistry(t.TempDir())
	for _, alias := range archivedToolEntries {
		canonical, ok := aliases[alias]
		require.True(t, ok, alias)
		info, ok := registry.Info(alias)
		require.True(t, ok, alias)
		require.Equal(t, canonical, info.Name, alias)
	}
}

func TestPrompterEmitsDecision(t *testing.T) {
	var decision PermissionDecision
	p := &Prompter{
		Mode:       PermissionAllow,
		DenyRules:  []string{"bash:rm -rf"},
		OnDecision: func(next PermissionDecision) { decision = next },
	}
	require.Error(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`)))
	require.Equal(t, "bash", decision.ToolName)
	require.False(t, decision.Allowed)
	require.Equal(t, "deny_rule", decision.Reason)
}

func TestPrompterBashValidation(t *testing.T) {
	var decision PermissionDecision
	p := &Prompter{
		Mode:       PermissionReadOnly,
		OnDecision: func(next PermissionDecision) { decision = next },
	}
	require.NoError(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"pwd"}`)))
	require.True(t, decision.Allowed)
	require.Equal(t, "bash_validation_read_only", decision.Reason)

	p = &Prompter{Mode: PermissionReadOnly}
	err := p.Authorize("bash", PermissionDanger, []byte(`{"command":"touch file.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool validation")

	var prompt strings.Builder
	p = &Prompter{
		Mode: PermissionDanger,
		In:   strings.NewReader("n\n"),
		Err:  &prompt,
	}
	err = p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`))
	require.Error(t, err)
	require.Contains(t, prompt.String(), "Tool validation warning")

	p = &Prompter{Mode: PermissionAllow}
	require.NoError(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`)))
}

func TestPrompterPowerShellValidation(t *testing.T) {
	workspace := t.TempDir()
	insideFile := filepath.Join(workspace, "README.md")
	require.NoError(t, os.WriteFile(insideFile, []byte("readme"), 0o644))
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("secret"), 0o644))

	var decision PermissionDecision
	p := &Prompter{
		Mode:      PermissionReadOnly,
		Workspace: workspace,
		OnDecision: func(next PermissionDecision) {
			decision = next
		},
	}
	require.NoError(t, p.Authorize("powershell", PermissionDanger, []byte(`{"command":"Get-Content `+insideFile+`"}`)))
	require.True(t, decision.Allowed)
	require.Equal(t, "powershell_validation_read_only", decision.Reason)

	p = &Prompter{Mode: PermissionReadOnly, Workspace: workspace}
	err := p.Authorize("powershell", PermissionDanger, []byte(`{"command":"Set-Content notes.txt ok"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool validation")

	p = &Prompter{Mode: PermissionReadOnly, Workspace: workspace}
	err = p.Authorize("powershell", PermissionDanger, []byte(`{"command":"Get-Content `+outsideFile+`"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside workspace")

	p = &Prompter{Mode: PermissionReadOnly, Workspace: workspace, AdditionalDirs: []string{outside}}
	require.NoError(t, p.Authorize("powershell", PermissionDanger, []byte(`{"command":"Get-Content `+outsideFile+`"}`)))

	var prompt strings.Builder
	p = &Prompter{
		Mode: PermissionDanger,
		In:   strings.NewReader("n\n"),
		Err:  &prompt,
	}
	err = p.Authorize("powershell", PermissionDanger, []byte(`{"command":"Remove-Item -Recurse -Force tmp"}`))
	require.Error(t, err)
	require.Contains(t, prompt.String(), "Tool validation warning")
}

func TestPrompterUsesDefaultPowerShellForBashToolValidation(t *testing.T) {
	p := &Prompter{
		Mode:         PermissionReadOnly,
		DefaultShell: "powershell",
	}

	err := p.Authorize("bash", PermissionDanger, []byte(`{"command":"Set-Content notes.txt ok"}`))

	require.Error(t, err)
	decision := p.Decide("bash", PermissionDanger, []byte(`{"command":"Set-Content notes.txt ok"}`))
	require.Equal(t, "powershell_validation", decision.Reason)
	require.Contains(t, decision.Message, "not read-only")
}

func TestPrompterTaskCreateCommandValidation(t *testing.T) {
	p := &Prompter{Mode: PermissionReadOnly}
	err := p.Authorize("task_create", PermissionDanger, []byte(`{"command":"touch file.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool validation")

	var prompt strings.Builder
	p = &Prompter{
		Mode: PermissionDanger,
		In:   strings.NewReader("n\n"),
		Err:  &prompt,
	}
	err = p.Authorize("task_create", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`))
	require.Error(t, err)
	require.Contains(t, prompt.String(), "Tool validation warning")

	var decision PermissionDecision
	p = &Prompter{
		Mode: PermissionAllow,
		OnDecision: func(next PermissionDecision) {
			decision = next
		},
	}
	require.NoError(t, p.Authorize("task_create", PermissionDanger, []byte(`{"prompt":"review auth flow"}`)))
	require.True(t, decision.Allowed)
	require.Equal(t, "permission_mode", decision.Reason)
}

func TestPrompterREPLShellValidation(t *testing.T) {
	var decision PermissionDecision
	p := &Prompter{
		Mode: PermissionReadOnly,
		OnDecision: func(next PermissionDecision) {
			decision = next
		},
	}
	require.NoError(t, p.Authorize("repl", PermissionDanger, []byte(`{"language":"sh","code":"pwd"}`)))
	require.True(t, decision.Allowed)
	require.Equal(t, "repl_validation_read_only", decision.Reason)

	p = &Prompter{Mode: PermissionReadOnly}
	err := p.Authorize("repl", PermissionDanger, []byte(`{"language":"bash","code":"touch file.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool validation")

	var prompt strings.Builder
	p = &Prompter{
		Mode: PermissionDanger,
		In:   strings.NewReader("n\n"),
		Err:  &prompt,
	}
	err = p.Authorize("repl", PermissionDanger, []byte(`{"language":"shell","code":"rm -rf tmp"}`))
	require.Error(t, err)
	require.Contains(t, prompt.String(), "Tool validation warning")

	decision = PermissionDecision{}
	p = &Prompter{Mode: PermissionReadOnly}
	decision = p.Decide("repl", PermissionDanger, []byte(`{"language":"python","code":"print('x')"}`))
	require.False(t, decision.Allowed)
	require.Equal(t, "requires_confirmation", decision.Reason)
}

func TestPrompterAlwaysAllowAddsSessionRule(t *testing.T) {
	var prompt strings.Builder
	var decisions []PermissionDecision
	p := &Prompter{
		Mode: PermissionPrompt,
		In:   strings.NewReader("a\n"),
		Err:  &prompt,
		OnDecision: func(next PermissionDecision) {
			decisions = append(decisions, next)
		},
	}

	require.NoError(t, p.Authorize("write_file", PermissionWorkspace, []byte(`{"path":"a.txt"}`)))
	require.Contains(t, prompt.String(), "always for session")
	require.ElementsMatch(t, []string{"write_file(a.txt)"}, p.AllowRules)
	require.Len(t, decisions, 1)
	require.Equal(t, "user_approved_always", decisions[0].Reason)
	require.Equal(t, "write_file(a.txt)", decisions[0].Rule)

	require.NoError(t, p.Authorize("write_file", PermissionWorkspace, []byte(`{"path":"a.txt"}`)))
	require.Len(t, decisions, 2)
	require.Equal(t, "allow_rule", decisions[1].Reason)

	p.In = strings.NewReader("n\n")
	require.Error(t, p.Authorize("write_file", PermissionWorkspace, []byte(`{"path":"b.txt"}`)))
	require.Len(t, decisions, 3)
	require.Equal(t, "user_denied", decisions[2].Reason)
}

func TestPrompterAcceptsStructuredPermissionResponses(t *testing.T) {
	t.Run("allow once with feedback", func(t *testing.T) {
		p := &Prompter{
			Mode: PermissionPrompt,
			In:   strings.NewReader(`{"decision":"allow_once","feedback":"run focused tests next"}` + "\n"),
			Err:  io.Discard,
		}

		decision, err := p.AuthorizeDecision("bash", PermissionDanger, []byte(`{"command":"go test ./internal/tui"}`))
		require.NoError(t, err)
		require.True(t, decision.Allowed)
		require.Equal(t, "run focused tests next", decision.Feedback)
	})

	t.Run("deny with feedback", func(t *testing.T) {
		p := &Prompter{
			Mode: PermissionPrompt,
			In:   strings.NewReader(`{"decision":"deny","feedback":"use the read tool instead"}` + "\n"),
			Err:  io.Discard,
		}

		decision, err := p.AuthorizeDecision("bash", PermissionDanger, []byte(`{"command":"cat secret"}`))
		require.Error(t, err)
		require.False(t, decision.Allowed)
		require.Equal(t, "use the read tool instead", decision.Feedback)
		require.Contains(t, err.Error(), "user feedback: use the read tool instead")
	})

	t.Run("always wraps edited shell prefix", func(t *testing.T) {
		p := &Prompter{
			Mode: PermissionPrompt,
			In:   strings.NewReader(`{"decision":"allow_always","rule":"go test:*"}` + "\n"),
			Err:  io.Discard,
		}

		decision, err := p.AuthorizeDecision("bash", PermissionDanger, []byte(`{"command":"go test ./..."}`))
		require.NoError(t, err)
		require.Equal(t, "bash(go test:*)", decision.Rule)
		require.Equal(t, []string{"bash(go test:*)"}, p.AllowRules)
		require.True(t, p.Decide("bash", PermissionDanger, []byte(`{"command":"go test ./internal/tui"}`)).Allowed)
	})

	t.Run("always rejects another tool rule", func(t *testing.T) {
		p := &Prompter{
			Mode: PermissionPrompt,
			In:   strings.NewReader(`{"decision":"allow_always","rule":"write_file(anywhere)"}` + "\n"),
			Err:  io.Discard,
		}

		decision, err := p.AuthorizeDecision("bash", PermissionDanger, []byte(`{"command":"go test ./..."}`))
		require.NoError(t, err)
		require.Equal(t, "bash(go test)", decision.Rule)
		require.Equal(t, []string{"bash(go test)"}, p.AllowRules)
	})
}

func TestSuggestedPermissionRuleNarrowsKnownInputs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "bash", input: `{"command":"go test ./... && git status"}`, want: "bash(go test)"},
		{name: "powershell", input: `{"command":"Get-ChildItem -Force | Select-Object Name"}`, want: "powershell(Get-ChildItem -Force)"},
		{name: "write_file", input: `{"path":"nested/out.txt"}`, want: "write_file(nested/out.txt)"},
		{name: "multi_edit", input: `{"edits":[{"file_path":"first.go"}]}`, want: "multi_edit(first.go)"},
		{name: "grep", input: `{"pattern":"TODO","path":"internal"}`, want: "grep(TODO)"},
		{name: "web_fetch", input: `{"url":"https://example.com/page"}`, want: "web_fetch(https://example.com/page)"},
		{name: "web_search", input: `{"query":"Go releases"}`, want: "web_search(Go releases)"},
		{name: "custom_tool", input: `{"id":"item-1"}`, want: "custom_tool(item-1)"},
		{name: "custom_tool", input: "raw scope", want: "custom_tool(raw scope)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, SuggestedPermissionRule(tc.name, tc.input))
		})
	}
	require.Equal(t, "go", shellPermissionPrefix("go | tee output"))
}

func TestPermissionResponseParsingDefaultsUnknownAndMalformedAnswersToDeny(t *testing.T) {
	require.Equal(t, PermissionResponse{Decision: "allow_once"}, parsePermissionResponse("yes\n"))
	require.Equal(t, PermissionResponse{Decision: "allow_always"}, parsePermissionResponse("always\n"))
	require.Equal(t, PermissionResponse{Decision: "deny"}, parsePermissionResponse("unexpected\n"))
	require.Equal(t, PermissionResponse{Decision: "deny"}, parsePermissionResponse(`{"decision":`))
	require.Equal(t, "bash(go test:*)", sessionAllowRule("bash", `{"command":"go test ./..."}`, "bash(go test:*)"))
	require.Equal(t, "tool", SuggestedPermissionRule("", `{}`))
	require.Equal(t, "custom_tool", SuggestedPermissionRule("custom_tool", `{}`))
	require.Len(t, []rune(truncatePermissionRuleNeedle(strings.Repeat("x", 300))), 240)
	require.True(t, permissionRulesContain([]string{" Bash(go test:*) "}, "bash(go test:*)"))
	require.False(t, permissionRulesContain([]string{"bash(go test:*)"}, "bash(git status)"))
}

func TestRegistryAddsApprovalFeedbackToModelVisibleToolResult(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("hello\n"), 0o644))
	registry := NewRegistry(workspace)
	prompter := &Prompter{
		Mode: PermissionPrompt,
		In:   strings.NewReader(`{"decision":"allow_once","feedback":"summarize this after reading"}` + "\n"),
		Err:  io.Discard,
	}

	output, err := registry.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"note.txt"}`), prompter)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &payload))
	require.Equal(t, "summarize this after reading", payload["permission_feedback"])
	require.Contains(t, output, "hello")

	prompter.In = strings.NewReader(`{"decision":"allow_once","feedback":"recover with glob"}` + "\n")
	_, err = registry.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"missing.txt"}`), prompter)
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission feedback: recover with glob")
}

func TestAppendPermissionFeedbackPreservesTextAndEmptyResults(t *testing.T) {
	require.Equal(t,
		"plain output\n\nPermission feedback: continue carefully",
		appendPermissionFeedback("plain output", "continue carefully"),
	)
	require.JSONEq(t,
		`{"permission_feedback":"continue carefully"}`,
		appendPermissionFeedback("", "continue carefully"),
	)
}

func TestRegistryInfoReportsToolPermissionAndSchema(t *testing.T) {
	registry := NewRegistry(t.TempDir())

	info, ok := registry.Info("BASH")
	require.True(t, ok)
	require.Equal(t, "bash", info.Name)
	require.Equal(t, PermissionDanger, info.Permission)
	required, ok := info.InputSchema["required"].([]string)
	require.True(t, ok)
	require.Contains(t, required, "command")

	infos := registry.Infos()
	require.Len(t, infos, 87)
	info, ok = registry.Info("bash")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("powershell")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("ProvisionalStatus")
	require.True(t, ok)
	require.Equal(t, "provisional_status", info.Name)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("BashOutput")
	require.True(t, ok)
	require.Equal(t, "bash_output", info.Name)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("KillBash")
	require.True(t, ok)
	require.Equal(t, "kill_bash", info.Name)
	require.Equal(t, PermissionWorkspace, info.Permission)
	info, ok = registry.Info("Read")
	require.True(t, ok)
	require.Equal(t, "read_file", info.Name)
	info, ok = registry.Info("ApplyPatch")
	require.True(t, ok)
	require.Equal(t, "apply_patch", info.Name)
	require.Equal(t, PermissionWorkspace, info.Permission)
	info, ok = registry.Info("LS")
	require.True(t, ok)
	require.Equal(t, "ls", info.Name)
	info, ok = registry.Info("TodoWrite")
	require.True(t, ok)
	require.Equal(t, "todo_write", info.Name)
	require.True(t, registry.Has("MultiEdit"))
	_, ok = registry.Info("ask_user_question")
	require.True(t, ok)
	_, ok = registry.Info("notebook_edit")
	require.True(t, ok)
	info, ok = registry.Info("NotebookRead")
	require.True(t, ok)
	require.Equal(t, "notebook_read", info.Name)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("lsp")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("enter_worktree")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("exit_worktree")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("agent")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("cron_create")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("cron_delete")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("cron_list")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	for _, name := range []string{"approval_token", "policy_evaluate", "recovery_recipe", "recovery_attempt", "recovery_status"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionReadOnly, info.Permission)
	}
	info, ok = registry.Info("team_create")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	for _, name := range []string{"team_list", "team_get"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionReadOnly, info.Permission)
	}
	info, ok = registry.Info("team_delete")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	for _, name := range []string{"worker_create", "worker_resolve_trust", "worker_send_prompt", "worker_restart", "worker_terminate"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionDanger, info.Permission)
	}
	for _, name := range []string{"worker_list", "worker_get", "worker_observe", "worker_await_ready", "worker_observe_completion", "worker_startup_timeout"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionReadOnly, info.Permission)
	}
	_, ok = registry.Info("multi_edit")
	require.True(t, ok)
	_, ok = registry.Info("task_create")
	require.True(t, ok)
	info, ok = registry.Info("run_task_packet")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("task_get")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("task_heartbeat")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("task_lane_board")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	_, ok = registry.Info("task_output")
	require.True(t, ok)
	info, ok = registry.Info("task_supervise")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("task_update")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	_, ok = registry.Info("web_fetch")
	require.True(t, ok)
	_, ok = registry.Info("web_search")
	require.True(t, ok)
	_, ok = registry.Info("tool_search")
	require.True(t, ok)
	info, ok = registry.Info("brief")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("send_user_message")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("structured_output")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("sleep")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("repl")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("remote_trigger")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("permission_check")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("testing_permission")
	require.True(t, ok)
	require.Equal(t, "permission_check", info.Name)
	info, ok = registry.Info("skill")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("config")
	require.True(t, ok)
	require.Equal(t, PermissionWorkspace, info.Permission)
	info, ok = registry.Info("mcp")
	require.True(t, ok)
	require.Equal(t, PermissionWorkspace, info.Permission)
	info, ok = registry.Info("mcp_auth")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	for _, name := range []string{"git_status", "branch_freshness", "git_diff", "git_log", "git_show", "git_blame"} {
		info, ok = registry.Info(name)
		require.True(t, ok)
		require.Equal(t, PermissionReadOnly, info.Permission)
	}
	info, ok = registry.Info("enter_plan_mode")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("exit_plan_mode")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("list_mcp_resources")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("read_mcp_resource")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("list_mcp_resource_templates")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("list_mcp_prompts")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
	info, ok = registry.Info("get_mcp_prompt")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
}

func TestUpdateBuiltinScopeRefreshesCompleteBuiltinRegistry(t *testing.T) {
	workspace := t.TempDir()
	extra := t.TempDir()
	configHome := t.TempDir()
	servers := map[string]config.MCPServerConfig{
		"demo": {Command: "demo-mcp"},
	}
	questionIn := strings.NewReader("answer\n")
	registry := &Registry{}
	registry.Register(CommandTool{
		Name:        "plugin_demo",
		Description: "plugin tool",
		Required:    PermissionReadOnly,
		Workspace:   workspace,
	})

	registry.UpdateBuiltinScope(workspace, RegistryOptions{
		SandboxStrategy:  "none",
		AdditionalDirs:   []string{extra},
		ConfigHome:       configHome,
		TrustedRoots:     []string{"repo-default"},
		RespectGitignore: true,
		MCPServers:       servers,
		QuestionIn:       questionIn,
		QuestionOut:      io.Discard,
	})

	require.True(t, registry.Has("plugin_demo"))
	require.Len(t, registry.Infos(), len(NewRegistryWithOptions(workspace, RegistryOptions{}).Infos())+1)
	for _, name := range []string{
		"powershell",
		"list_mcp_resource_templates",
		"list_mcp_prompts",
		"get_mcp_prompt",
		"tool_search",
		"agent",
		"task_create",
		"apply_patch",
		"ask_user_question",
	} {
		require.True(t, registry.Has(name), "missing %s", name)
	}

	_, tool, ok := registry.resolve("task_create")
	require.True(t, ok)
	require.Equal(t, configHome, tool.(TaskCreateTool).ConfigHome)
	_, tool, ok = registry.resolve("worker_create")
	require.True(t, ok)
	require.Equal(t, []string{"repo-default"}, tool.(WorkerCreateTool).TrustedRoots)
	_, tool, ok = registry.resolve("grep")
	require.True(t, ok)
	require.True(t, tool.(GrepTool).RespectGitignore)
	_, tool, ok = registry.resolve("glob")
	require.True(t, ok)
	require.True(t, tool.(GlobTool).RespectGitignore)
	_, tool, ok = registry.resolve("list_mcp_prompts")
	require.True(t, ok)
	require.Equal(t, servers, tool.(ListMCPPromptsTool).Servers)
	_, tool, ok = registry.resolve("ask_user_question")
	require.True(t, ok)
	questionTool := tool.(AskUserQuestionTool)
	require.Same(t, questionIn, questionTool.In)
	require.Equal(t, io.Discard, questionTool.Out)
	_, tool, ok = registry.resolve("tool_search")
	require.True(t, ok)
	require.Same(t, registry, tool.(ToolSearchTool).Registry)
}

func TestFileToolSchemasAllowClaudeFilePathAlias(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	tests := []struct {
		name     string
		required []string
	}{
		{name: "Read"},
		{name: "Write", required: []string{"content"}},
		{name: "Edit", required: []string{"old_string", "new_string"}},
		{name: "MultiEdit", required: []string{"edits"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := registry.Info(tt.name)
			require.True(t, ok)
			requirePathAliasRequirement(t, info.InputSchema)

			required, ok := info.InputSchema["required"].([]string)
			if len(tt.required) == 0 {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.ElementsMatch(t, tt.required, required)
			require.NotContains(t, required, "path")
			require.NotContains(t, required, "file_path")
		})
	}
}

func requirePathAliasRequirement(t *testing.T, schema map[string]any) {
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
	require.True(t, seen["path"])
	require.True(t, seen["file_path"])
}

func TestTaskToolSchemasDeclareAcceptedTaskIDAliases(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	tests := []struct {
		name     string
		aliases  []string
		required []string
	}{
		{name: "task_status", aliases: []string{"id", "task_id", "taskId"}},
		{name: "task_get", aliases: []string{"id", "task_id", "taskId"}},
		{name: "task_output", aliases: []string{"id", "task_id", "taskId"}},
		{name: "task_update", aliases: []string{"id", "task_id", "taskId"}, required: []string{"message"}},
		{name: "task_stop", aliases: []string{"id", "task_id", "taskId", "shell_id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := registry.Info(tt.name)
			require.True(t, ok)
			requireTaskIDAliasRequirement(t, info.InputSchema, tt.aliases...)
			required, ok := info.InputSchema["required"].([]string)
			if len(tt.required) == 0 {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.ElementsMatch(t, tt.required, required)
			for _, alias := range tt.aliases {
				require.NotContains(t, required, alias)
			}
		})
	}
}
