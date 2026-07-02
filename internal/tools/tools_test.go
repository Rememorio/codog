package tools

import (
	"bufio"
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

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/hookenv"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/undo"
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

	out, err := tool.Execute(context.Background(), []byte(`{"command":"Write-Output ok","timeout":10000}`))
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
	}, 5*time.Second, 50*time.Millisecond)
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
	workspace := t.TempDir()
	configHome := t.TempDir()
	sessionID := "session-1"
	dir := hookenv.Dir(configHome, sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessionstart-hook-1.sh"), []byte("export CODOG_TEST_HOOK_ENV=ready\n"), 0o600))

	ctx := ContextWithSessionID(context.Background(), sessionID)
	out, err := BashTool{Workspace: workspace, ConfigHome: configHome}.Execute(ctx, []byte(`{"command":"printf %s \"$CODOG_TEST_HOOK_ENV\""}`))
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
	require.Equal(t, "mcp__server__tool", CanonicalToolName("mcp__server__tool"))

	aliases := ClaudeToolAliases()
	require.Equal(t, "web_fetch", aliases["WebFetch"])
	require.Equal(t, "read_file", aliases["FileReadTool"])
	require.Equal(t, "apply_patch", aliases["ApplyPatchTool"])
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
		"EnterPlanModeTool",
		"EnterWorktreeTool",
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
		"PowerShellTool",
		"ReadMcpResourceTool",
		"RemoteTriggerTool",
		"SendMessageTool",
		"SkillTool",
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
	require.Contains(t, err.Error(), "bash validation")

	var prompt strings.Builder
	p = &Prompter{
		Mode: PermissionDanger,
		In:   strings.NewReader("n\n"),
		Err:  &prompt,
	}
	err = p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`))
	require.Error(t, err)
	require.Contains(t, prompt.String(), "Bash validation warning")

	p = &Prompter{Mode: PermissionAllow}
	require.NoError(t, p.Authorize("bash", PermissionDanger, []byte(`{"command":"rm -rf tmp"}`)))
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
	require.ElementsMatch(t, []string{"write_file"}, p.AllowRules)
	require.Len(t, decisions, 1)
	require.Equal(t, "user_approved_always", decisions[0].Reason)

	require.NoError(t, p.Authorize("write_file", PermissionWorkspace, []byte(`{"path":"b.txt"}`)))
	require.Len(t, decisions, 2)
	require.Equal(t, "allow_rule", decisions[1].Reason)
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
	require.Len(t, infos, 82)
	info, ok = registry.Info("bash")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
	info, ok = registry.Info("powershell")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
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
	info, ok = registry.Info("testing_permission")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
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
		SandboxStrategy: "none",
		AdditionalDirs:  []string{extra},
		ConfigHome:      configHome,
		MCPServers:      servers,
		QuestionIn:      questionIn,
		QuestionOut:     io.Discard,
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
		"BriefTool":                    "brief",
		"ConfigTool":                   "config",
		"CronCreate":                   "cron_create",
		"CronCreateTool":               "cron_create",
		"CronDelete":                   "cron_delete",
		"CronDeleteTool":               "cron_delete",
		"CronList":                     "cron_list",
		"CronListTool":                 "cron_list",
		"EnterPlanModeTool":            "enter_plan_mode",
		"EnterWorktree":                "enter_worktree",
		"EnterWorktreeTool":            "enter_worktree",
		"ExitPlanModeTool":             "exit_plan_mode",
		"ExitPlanModeV2Tool":           "exit_plan_mode",
		"ExitWorktree":                 "exit_worktree",
		"ExitWorktreeTool":             "exit_worktree",
		"BashTool":                     "bash",
		"EditTool":                     "edit_file",
		"FileEdit":                     "edit_file",
		"FileEditTool":                 "edit_file",
		"FileRead":                     "read_file",
		"FileReadTool":                 "read_file",
		"FileWrite":                    "write_file",
		"FileWriteTool":                "write_file",
		"BranchFreshness":              "branch_freshness",
		"BranchFreshnessTool":          "branch_freshness",
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
		"MCPTool":                      "mcp",
		"MultiEditTool":                "multi_edit",
		"NotebookEditTool":             "notebook_edit",
		"NotebookReadTool":             "notebook_read",
		"PowerShellTool":               "powershell",
		"ReadTool":                     "read_file",
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
		"RunTaskPacket":                "run_task_packet",
		"RunTaskPacketTool":            "run_task_packet",
		"SendMessage":                  "send_user_message",
		"SendMessageTool":              "send_user_message",
		"SkillTool":                    "skill",
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
		"TodoReadTool":                 "todo_read",
		"TodoWriteTool":                "todo_write",
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
	} {
		info, ok := registry.Info(alias)
		require.True(t, ok, alias)
		require.Equal(t, canonical, info.Name, alias)
	}

	out, err = registry.Execute(context.Background(), "TestingPermission", []byte(`{"target_tool":"Bash","input":{"command":"pwd"}}`), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"target_tool": "bash"`)
	require.Contains(t, out, `"known_tool": true`)
	require.Contains(t, out, `"required_permission": "danger-full-access"`)
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

func TestTestingPermissionToolReturnsReceipt(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	prompter := &Prompter{Mode: PermissionReadOnly}

	out, err := registry.Execute(context.Background(), "testing_permission", []byte(`{"target_tool":"bash","input":{"command":"pwd"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "permission_check"`)
	require.Contains(t, out, `"target_tool": "bash"`)
	require.Contains(t, out, `"allowed": true`)
	require.Contains(t, out, `"reason": "bash_validation_read_only"`)

	out, err = registry.Execute(context.Background(), "testing_permission", []byte(`{"target_tool":"bash","input":{"command":"pwd && touch created.txt"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"allowed": false`)
	require.Contains(t, out, `"reason": "bash_validation"`)
	require.Contains(t, out, `"message": "bash command is not read-only"`)

	prompter = &Prompter{Mode: PermissionAllow, DeniedTools: []string{"write_file"}}
	out, err = registry.Execute(context.Background(), "testing_permission", []byte(`{"target_tool":"write_file","input":{"path":"a.txt","content":"x"}}`), prompter)
	require.NoError(t, err)
	require.Contains(t, out, `"known_tool": true`)
	require.Contains(t, out, `"required_permission": "workspace-write"`)
	require.Contains(t, out, `"allowed": false`)
	require.Contains(t, out, `"reason": "denied_tools"`)

	_, err = TestingPermissionTool{}.Execute(context.Background(), []byte(`{"target_tool":"bash"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "through the tool registry")
}

func TestNotebookEditToolUpdatesNotebook(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "analysis.ipynb")
	require.NoError(t, os.WriteFile(path, []byte(`{"metadata":{"name":"kept"},"cells":[]}`), 0o644))

	out, err := NotebookEditTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"notebook_path":"analysis.ipynb","edit_mode":"insert","cell_type":"markdown","new_source":"# Title"}`))
	require.NoError(t, err)
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
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "messy.go"), []byte("package demo\n\nfunc messy(){return}\n"), 0o644))
	tool := LSPTool{Workspace: workspace}

	symbolsOut, err := tool.Execute(context.Background(), []byte(`{"action":"symbols","path":"demo.go"}`))
	require.NoError(t, err)
	require.Contains(t, symbolsOut, `"action": "symbols"`)
	require.Contains(t, symbolsOut, "BuildWidget")

	documentSymbolsOut, err := tool.Execute(context.Background(), []byte(`{"action":"document_symbols","path":"demo.go"}`))
	require.NoError(t, err)
	require.Contains(t, documentSymbolsOut, `"action": "symbols"`)
	require.Contains(t, documentSymbolsOut, "BuildWidget")

	definitionOut, err := tool.Execute(context.Background(), []byte(`{"action":"definition","query":"Widget"}`))
	require.NoError(t, err)
	require.Contains(t, definitionOut, `"found": true`)
	require.Contains(t, definitionOut, `"name": "Widget"`)

	gotoDefinitionOut, err := tool.Execute(context.Background(), []byte(`{"action":"goto_definition","query":"Widget"}`))
	require.NoError(t, err)
	require.Contains(t, gotoDefinitionOut, `"action": "definition"`)
	require.Contains(t, gotoDefinitionOut, `"found": true`)

	languageFallbackOut, err := tool.Execute(context.Background(), []byte(`{"action":"definition","query":"Widget","language":"go"}`))
	require.NoError(t, err)
	require.Contains(t, languageFallbackOut, `"action": "definition"`)
	require.Contains(t, languageFallbackOut, `"found": true`)

	_, err = tool.Execute(context.Background(), []byte(`{"action":"hover","path":"demo.go","line":4,"character":6,"use_server":true}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "config home is required")

	hoverOut, err := tool.Execute(context.Background(), []byte(`{"action":"hover","path":"demo.go","line":4,"character":6}`))
	require.NoError(t, err)
	require.Contains(t, hoverOut, `"query": "BuildWidget"`)
	require.Contains(t, hoverOut, `"found": true`)

	completionOut, err := tool.Execute(context.Background(), []byte(`{"action":"completion","query":"Build","limit":5}`))
	require.NoError(t, err)
	require.Contains(t, completionOut, `"action": "completion"`)
	require.Contains(t, completionOut, `"label": "BuildWidget"`)

	formatOut, err := tool.Execute(context.Background(), []byte(`{"action":"format","path":"messy.go"}`))
	require.NoError(t, err)
	require.Contains(t, formatOut, `"action": "format"`)
	require.Contains(t, formatOut, `"changed": true`)
	require.Contains(t, formatOut, `func messy()`)
}

func TestWorktreeToolsAllocateAndRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	runToolTestGit(t, workspace, "init", "-q")
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
	runToolTestGit(t, workspace, "init", "-q")
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

	blameOut, err := GitBlameTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"path":"notes.txt","start_line":1,"end_line":1}`))
	require.NoError(t, err)
	require.Contains(t, blameOut, "alpha")

	runToolTestGit(t, workspace, "restore", "notes.txt")
	runToolTestGit(t, workspace, "switch", "-q", "-c", "topic")
	runToolTestGit(t, workspace, "switch", "-q", "master")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runToolTestGit(t, workspace, "add", "fix.txt")
	runToolTestGit(t, workspace, "commit", "-q", "-m", "fix: main update")
	freshnessOut, err := BranchFreshnessTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"branch":"topic","base":"master"}`))
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
	}, 5*time.Second, 50*time.Millisecond)
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
	}, 5*time.Second, 50*time.Millisecond)

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

func TestToolSearchToolFindsRegisteredTools(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	out, err := ToolSearchTool{Registry: registry}.Execute(context.Background(), []byte(`{"query":"web fetch","max_results":3}`))
	require.NoError(t, err)
	require.Contains(t, out, `"query": "web fetch"`)
	require.Contains(t, out, `"normalized_query": "web fetch"`)
	require.Contains(t, out, `"name": "web_fetch"`)
	require.NotContains(t, out, `"name": "write_file"`)

	out, err = ToolSearchTool{Registry: registry}.Execute(context.Background(), []byte(`{"query":"select:Bash,Read,Nope","max_results":5}`))
	require.NoError(t, err)
	var selected struct {
		Query           string   `json:"query"`
		NormalizedQuery string   `json:"normalized_query"`
		MatchNames      []string `json:"match_names"`
		Matches         []struct {
			Name string `json:"name"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &selected))
	require.Equal(t, "select:Bash,Read,Nope", selected.Query)
	require.Equal(t, "selectbash read nope", selected.NormalizedQuery)
	require.Equal(t, []string{"bash", "read_file"}, selected.MatchNames)
	require.Len(t, selected.Matches, 2)
	require.Equal(t, "bash", selected.Matches[0].Name)
	require.Equal(t, "read_file", selected.Matches[1].Name)

	out, err = ToolSearchTool{Registry: registry}.Execute(context.Background(), []byte(`{"query":"select:Bash,Read","max_results":1}`))
	require.NoError(t, err)
	require.Contains(t, out, `"match_names": [
    "bash"
  ]`)
	require.NotContains(t, out, `"name": "read_file"`)

	info, ok := registry.Info("tool_search")
	require.True(t, ok)
	require.Equal(t, PermissionReadOnly, info.Permission)
}

func TestAskUserQuestionToolReadsChoiceAndDefault(t *testing.T) {
	var out strings.Builder
	tool := AskUserQuestionTool{
		In:  strings.NewReader("2\n"),
		Out: &out,
	}
	properties := tool.Definition().InputSchema["properties"].(map[string]any)
	require.Contains(t, properties, "options")

	result, err := tool.Execute(context.Background(), []byte(`{"question":"Pick one","choices":["alpha","beta"],"default":"alpha"}`))
	require.NoError(t, err)
	require.Contains(t, out.String(), "Pick one")
	require.Contains(t, out.String(), "2. beta")
	require.Contains(t, result, `"answer": "beta"`)

	out.Reset()
	tool.In = strings.NewReader("\n")
	result, err = tool.Execute(context.Background(), []byte(`{"question":"Continue?","default":"yes"}`))
	require.NoError(t, err)
	require.Contains(t, result, `"answer": "yes"`)

	out.Reset()
	tool.In = strings.NewReader("1\n")
	result, err = tool.Execute(context.Background(), []byte(`{"question":"Use options?","options":["gamma","delta"]}`))
	require.NoError(t, err)
	require.Contains(t, out.String(), "1. gamma")
	require.Contains(t, result, `"answer": "gamma"`)
}

func TestBriefToolReturnsAttachmentMetadata(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "image.png"), []byte("png"), 0o644))

	out, err := BriefTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"message":"Review ready","status":"normal","attachments":["image.png"]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"message": "Review ready"`)
	require.Contains(t, out, `"status": "normal"`)
	require.Contains(t, out, `"is_image": true`)
	require.Contains(t, out, `"size": 3`)

	out, err = SendUserMessageTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"message":"Heads up","status":"proactive","attachments":["image.png"]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"message": "Heads up"`)
	require.Contains(t, out, `"status": "proactive"`)
	require.Contains(t, out, `"is_image": true`)
}

func TestStructuredOutputToolReturnsPayload(t *testing.T) {
	out, err := StructuredOutputTool{}.Execute(context.Background(), []byte(`{"ok":true,"items":[1,2,3]}`))
	require.NoError(t, err)
	require.Contains(t, out, `"data": "Structured output provided successfully"`)
	require.Contains(t, out, `"ok": true`)

	_, err = StructuredOutputTool{}.Execute(context.Background(), []byte(`{}`))
	require.Error(t, err)
}

func TestSleepToolWaitsAndReportsDuration(t *testing.T) {
	out, err := SleepTool{}.Execute(context.Background(), []byte(`{"duration_ms":1}`))
	require.NoError(t, err)
	require.Contains(t, out, `"duration_ms": 1`)
	require.Contains(t, out, "Slept for 1ms")
}

func TestREPLToolExecutesShellCode(t *testing.T) {
	out, err := REPLTool{Workspace: t.TempDir()}.Execute(context.Background(), []byte(`{"language":"sh","code":"printf repl-ok","timeout_ms":1000}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "repl-ok"`)
	require.Contains(t, out, `"exit_code": 0`)

	_, err = REPLTool{Workspace: t.TempDir()}.Execute(context.Background(), []byte(`{"language":"unknown","code":"x"}`))
	require.Error(t, err)
}

func TestSkillToolLoadsAndRendersSkill(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAW_CONFIG_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "skills", "internal"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte("---\ndescription: Review code changes.\n---\nReview skill body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "disabled.md"), []byte("---\ndescription: Hidden from model.\ndisable-model-invocation: true\n---\nDisabled body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "skills", "internal", "SKILL.md"), []byte("---\ndescription: Internal only.\nuser-invocable: false\n---\nInternal body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "deploy.md"), []byte("Deploy command body"), 0o644))
	tool := SkillTool{Workspace: workspace, ConfigHome: configHome}

	out, err := tool.Execute(context.Background(), []byte(`{"max_results":100}`))
	require.NoError(t, err)
	require.Contains(t, out, `"action": "list"`)
	require.Contains(t, out, `"name": "review"`)
	require.Contains(t, out, `"name": "internal"`)
	require.Contains(t, out, `"user_invocable": false`)
	require.NotContains(t, out, `"name": "disabled"`)
	require.NotContains(t, out, "Disabled body")

	out, err = tool.Execute(context.Background(), []byte(`{"action":"list","query":"internal","max_results":5}`))
	require.NoError(t, err)
	require.Contains(t, out, `"query": "internal"`)
	require.Contains(t, out, `"name": "internal"`)
	require.NotContains(t, out, `"name": "review"`)

	out, err = tool.Execute(context.Background(), []byte(`{"action":"show","skill":"internal"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"action": "show"`)
	require.Contains(t, out, `"skill": "internal"`)
	require.Contains(t, out, "Internal body")

	out, err = tool.Execute(context.Background(), []byte(`{"skill":"$review","args":"check auth"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "skill"`)
	require.Contains(t, out, `"action": "invoke"`)
	require.Contains(t, out, `"skill": "review"`)
	require.Contains(t, out, `"args": "check auth"`)
	require.Contains(t, out, `"description": "Review code changes."`)
	require.Contains(t, out, "Review skill body")
	require.Contains(t, out, "User request: check auth")

	out, err = tool.Execute(context.Background(), []byte(`{"skill":"/deploy","args":"production"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"skill": "deploy"`)
	require.Contains(t, out, `"source": "claude"`)
	require.Contains(t, out, "Deploy command body")
	require.Contains(t, out, "User request: production")

	out, err = tool.Execute(context.Background(), []byte(`{"skill":"verify","args":"recent change"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"skill": "verify"`)
	require.Contains(t, out, `"source": "bundled"`)
	require.Contains(t, out, "Choose and run validation")
	require.Contains(t, out, "User request: recent change")

	_, err = tool.Execute(context.Background(), []byte(`{"skill":"disabled"}`))
	require.ErrorContains(t, err, "disable-model-invocation")
}

func TestConfigToolGetsAndSetsUserConfig(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(configHome, 0o755))
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":"old-model","api_key":"secret","future":{"sandbox_strategy":"detect"}}`), 0o644))
	tool := ConfigTool{Workspace: workspace, ConfigHome: configHome}

	getOut, err := tool.Execute(context.Background(), []byte(`{"setting":"model"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, `"operation": "get"`)
	require.Contains(t, getOut, `"value": "old-model"`)

	secretOut, err := tool.Execute(context.Background(), []byte(`{"setting":"api_key"}`))
	require.NoError(t, err)
	require.Contains(t, secretOut, `[redacted]`)
	require.NotContains(t, secretOut, `secret`)

	setOut, err := tool.Execute(context.Background(), []byte(`{"setting":"future.sandbox_strategy","value":"sandbox-exec"}`))
	require.NoError(t, err)
	require.Contains(t, setOut, `"operation": "set"`)
	require.Contains(t, setOut, `"previous_value": "detect"`)
	require.Contains(t, setOut, `"new_value": "sandbox-exec"`)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"sandbox_strategy": "sandbox-exec"`)
}

func TestTaskToolsManageBackgroundTasks(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	createOut, err := TaskCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"command":"printf task-output","kind":"test","session_id":"session-1"}`))
	require.NoError(t, err)
	var task background.Task
	require.NoError(t, json.Unmarshal([]byte(createOut), &task))
	require.NotEmpty(t, task.ID)

	var completed background.Task
	require.Eventually(t, func() bool {
		statusOut, err := TaskStatusTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"taskId":"`+task.ID+`"}`))
		if err != nil {
			return false
		}
		if err := json.Unmarshal([]byte(statusOut), &completed); err != nil {
			return false
		}
		return completed.Status != "running" && completed.ExitCode != nil
	}, 2*time.Second, 20*time.Millisecond)
	require.NotNil(t, completed.ExitCode)
	require.Equal(t, 0, *completed.ExitCode)

	outputOut, err := TaskOutputTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+task.ID+`","block":true,"timeout":1000}`))
	require.NoError(t, err)
	require.Contains(t, outputOut, "task-output")
	require.Contains(t, outputOut, `"task_id": "`)
	require.Contains(t, outputOut, `"status": "completed"`)
	require.Contains(t, outputOut, `"exit_code": 0`)
	var completeOutput struct {
		Output        string          `json:"output"`
		Stdout        string          `json:"stdout"`
		HasOutput     bool            `json:"has_output"`
		RawOutputPath string          `json:"rawOutputPath"`
		Task          background.Task `json:"task"`
		LogSize       int64           `json:"logSize"`
		Truncated     bool            `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(outputOut), &completeOutput))
	require.Equal(t, "task-output", completeOutput.Output)
	require.Equal(t, completeOutput.Output, completeOutput.Stdout)
	require.True(t, completeOutput.HasOutput)
	require.Equal(t, task.ID, completeOutput.Task.ID)
	require.FileExists(t, completeOutput.RawOutputPath)
	require.Equal(t, int64(len("task-output")), completeOutput.LogSize)
	require.False(t, completeOutput.Truncated)
	outputOut, err = TaskOutputTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+task.ID+`","offset":0,"limit":4}`))
	require.NoError(t, err)
	var offsetOutput struct {
		Output              string `json:"output"`
		Offset              int64  `json:"offset"`
		NextOffset          int64  `json:"nextOffset"`
		BytesRead           int    `json:"bytesRead"`
		Truncated           bool   `json:"truncated"`
		PersistedOutputPath string `json:"persistedOutputPath"`
		PersistedOutputSize int64  `json:"persistedOutputSize"`
	}
	require.NoError(t, json.Unmarshal([]byte(outputOut), &offsetOutput))
	require.Equal(t, "task", offsetOutput.Output)
	require.Equal(t, int64(0), offsetOutput.Offset)
	require.Equal(t, int64(4), offsetOutput.NextOffset)
	require.Equal(t, 4, offsetOutput.BytesRead)
	require.True(t, offsetOutput.Truncated)
	require.Equal(t, completeOutput.RawOutputPath, offsetOutput.PersistedOutputPath)
	require.Equal(t, int64(len("task-output")), offsetOutput.PersistedOutputSize)

	delayedOut, err := TaskCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"command":"sleep 0.1; printf delayed-task","kind":"delayed","session_id":"session-2"}`))
	require.NoError(t, err)
	var delayedTask background.Task
	require.NoError(t, json.Unmarshal([]byte(delayedOut), &delayedTask))
	outputOut, err = TaskOutputTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+delayedTask.ID+`","offset":0,"limit":64,"block":true,"timeout_ms":2000}`))
	require.NoError(t, err)
	var blockedOutput struct {
		Output     string `json:"output"`
		NextOffset int64  `json:"nextOffset"`
		TimedOut   bool   `json:"timedOut"`
		TimeoutMS  int    `json:"timeoutMs"`
	}
	require.NoError(t, json.Unmarshal([]byte(outputOut), &blockedOutput))
	require.Equal(t, "delayed-task", blockedOutput.Output)
	require.Greater(t, blockedOutput.NextOffset, int64(0))
	require.False(t, blockedOutput.TimedOut)
	require.Equal(t, 2000, blockedOutput.TimeoutMS)

	updateOut, err := TaskUpdateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"taskId":"`+task.ID+`","message":"review logs"}`))
	require.NoError(t, err)
	require.Contains(t, updateOut, `"task_id": "`+task.ID+`"`)
	require.Contains(t, updateOut, `"taskId": "`+task.ID+`"`)
	require.Contains(t, updateOut, `"message_count": 1`)
	require.Contains(t, updateOut, `"last_message": "review logs"`)

	getOut, err := TaskGetTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"taskId":"`+task.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, `"messages": [`)
	require.Contains(t, getOut, "review logs")
	var getView struct {
		ID        string          `json:"id"`
		TaskID    string          `json:"task_id"`
		CreatedAt time.Time       `json:"created_at"`
		UpdatedAt time.Time       `json:"updated_at"`
		Task      background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &getView))
	require.Equal(t, task.ID, getView.ID)
	require.Equal(t, task.ID, getView.TaskID)
	require.Equal(t, task.ID, getView.Task.ID)
	require.False(t, getView.CreatedAt.IsZero())
	require.False(t, getView.UpdatedAt.IsZero())

	listOut, err := TaskListTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"session_id":"session-1","kind":"test"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, task.ID)
	require.Contains(t, listOut, `"total": 1`)
	var listed struct {
		Count int `json:"count"`
		Tasks []struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal([]byte(listOut), &listed))
	require.Equal(t, 1, listed.Count)
	require.Len(t, listed.Tasks, 1)
	require.Equal(t, task.ID, listed.Tasks[0].ID)
	require.Equal(t, task.ID, listed.Tasks[0].TaskID)

	stopOut, err := TaskStopTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+task.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, stopOut, task.ID)
	require.Contains(t, stopOut, `"task_id": "`+task.ID+`"`)
	require.Contains(t, stopOut, `"message": "Task stopped"`)

	registry := NewRegistryWithOptions(workspace, RegistryOptions{ConfigHome: configHome})
	info, ok := registry.Info("task_create")
	require.True(t, ok)
	require.Equal(t, PermissionDanger, info.Permission)
}

func TestTaskCreateToolAcceptsPromptContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
printf 'codog:%s\n' "$*"
`), 0o755))

	out, err := TaskCreateTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{"prompt":"check auth","description":"audit auth","session_id":"session-1"}`))
	require.NoError(t, err)
	var report struct {
		TaskID      string          `json:"task_id"`
		Status      string          `json:"status"`
		Prompt      string          `json:"prompt"`
		Description string          `json:"description"`
		CreatedAt   time.Time       `json:"created_at"`
		Task        background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.NotEmpty(t, report.TaskID)
	require.Equal(t, report.Task.ID, report.TaskID)
	require.Equal(t, "running", report.Status)
	require.Equal(t, "check auth", report.Prompt)
	require.Equal(t, "audit auth", report.Description)
	require.False(t, report.CreatedAt.IsZero())
	require.Equal(t, "task", report.Task.Kind)
	require.Equal(t, "session-1", report.Task.SessionID)
	require.Equal(t, "check auth", report.Task.Prompt)
	require.Equal(t, "audit auth", report.Task.Description)
	require.Contains(t, report.Task.Command, "prompt")

	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(report.TaskID, 4096)
		return err == nil && strings.Contains(logs, "Task: audit auth") && strings.Contains(logs, "check auth")
	}, 2*time.Second, 20*time.Millisecond)

	getOut, err := TaskGetTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+report.TaskID+`"}`))
	require.NoError(t, err)
	var fetched background.Task
	require.NoError(t, json.Unmarshal([]byte(getOut), &fetched))
	require.Equal(t, "check auth", fetched.Prompt)
	require.Equal(t, "audit auth", fetched.Description)
	var fetchedView struct {
		TaskID    string          `json:"task_id"`
		Prompt    string          `json:"prompt"`
		Task      background.Task `json:"task"`
		UpdatedAt time.Time       `json:"updated_at"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &fetchedView))
	require.Equal(t, report.TaskID, fetchedView.TaskID)
	require.Equal(t, "check auth", fetchedView.Prompt)
	require.Equal(t, "check auth", fetchedView.Task.Prompt)
	require.False(t, fetchedView.UpdatedAt.IsZero())

	_, err = TaskCreateTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{"command":"printf ok","prompt":"check auth"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot both be provided")
}

func TestTaskHeartbeatAndLaneBoardTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	createOut, err := TaskCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"command":"sleep 5","kind":"agent","session_id":"session-1"}`))
	require.NoError(t, err)
	var task background.Task
	require.NoError(t, json.Unmarshal([]byte(createOut), &task))
	t.Cleanup(func() {
		_, _ = background.NewStore(configHome).Stop(task.ID)
	})

	observedAt := time.Now().UTC().Truncate(time.Second)
	heartbeatOut, err := TaskHeartbeatTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+task.ID+`","status":"running","transport_alive":true,"observed_at":"`+observedAt.Format(time.RFC3339)+`"}`))
	require.NoError(t, err)
	var heartbeatView struct {
		TaskID    string                   `json:"task_id"`
		Heartbeat background.LaneHeartbeat `json:"heartbeat"`
		Task      background.Task          `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(heartbeatOut), &heartbeatView))
	require.Equal(t, task.ID, heartbeatView.TaskID)
	require.Equal(t, observedAt, heartbeatView.Heartbeat.ObservedAt)
	require.True(t, heartbeatView.Heartbeat.TransportAlive)
	require.Equal(t, "running", heartbeatView.Heartbeat.Status)
	require.NotNil(t, heartbeatView.Task.Heartbeat)

	boardOut, err := TaskLaneBoardTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"stalled_after_seconds":3600}`))
	require.NoError(t, err)
	var board background.LaneBoard
	require.NoError(t, json.Unmarshal([]byte(boardOut), &board))
	require.Len(t, board.Active, 1)
	require.Equal(t, task.ID, board.Active[0].TaskID)
	require.Equal(t, background.LaneFreshnessHealthy, board.Active[0].Freshness)
	require.Empty(t, board.Blocked)
	require.Empty(t, board.Finished)
}

func TestTaskSuperviseToolRestartsEligibleTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	createOut, err := TaskCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"command":"printf failed && exit 2","kind":"test","restart_policy":{"enabled":true,"mode":"on-failure","max_attempts":1}}`))
	require.NoError(t, err)
	var task background.Task
	require.NoError(t, json.Unmarshal([]byte(createOut), &task))
	require.NotNil(t, task.RestartPolicy)
	require.True(t, task.RestartPolicy.Enabled)

	store := background.NewStore(configHome)
	require.Eventually(t, func() bool {
		status, err := store.Status(task.ID)
		return err == nil && status.Status == "failed"
	}, 2*time.Second, 20*time.Millisecond)

	superviseOut, err := TaskSuperviseTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	var result background.SuperviseResult
	require.NoError(t, json.Unmarshal([]byte(superviseOut), &result))
	require.Len(t, result.Restarted, 1)
	require.Equal(t, task.ID, result.Restarted[0].RestartedFrom)
	require.Equal(t, 1, result.Restarted[0].RestartCount)
}

func TestRunTaskPacketToolCreatesPromptTask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'shim:%s\\n' \"$*\"\n"), 0o755))

	out, err := RunTaskPacketTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{
		"objective":"Update docs",
		"scope":"README only",
		"repo":"codog",
		"branch_policy":"use main",
		"acceptance_tests":["go test ./..."],
		"commit_policy":"commit changes",
		"reporting_contract":"summarize result",
		"escalation_policy":"ask if blocked"
	}`))
	require.NoError(t, err)
	require.Contains(t, out, `"task_packet": {`)
	require.Contains(t, out, `"objective": "Update docs"`)
	require.Contains(t, out, `"scope": "custom"`)
	require.Contains(t, out, `"scope_path": "README only"`)
	require.Contains(t, out, `"resolved_scope": {`)
	var payload struct {
		TaskID string          `json:"task_id"`
		Task   background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.NotEmpty(t, payload.TaskID)
	require.Equal(t, "Update docs", payload.Task.Description)
	require.Contains(t, payload.Task.Prompt, "Objective:")
	var persistedPacket map[string]any
	require.NoError(t, json.Unmarshal(payload.Task.TaskPacket, &persistedPacket))
	require.Equal(t, "Update docs", persistedPacket["objective"])
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(payload.TaskID, 4096)
		return err == nil && strings.Contains(logs, "shim:prompt") && strings.Contains(logs, "Update docs")
	}, 5*time.Second, 50*time.Millisecond)
}

func TestRunTaskPacketToolAcceptsRichPacket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "internal", "taskpacket"), 0o755))
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'rich:%s\\n' \"$*\"\n"), 0o755))

	out, err := RunTaskPacketTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{
		"objective":"Implement packet validation",
		"scope":"module",
		"scope_path":"internal/taskpacket",
		"repo":"codog",
		"worktree":"/tmp/codog-wt",
		"branch_policy":"main only",
		"acceptance_criteria":["validation rejects empty required groups"],
		"resources":[{"kind":"file","value":"internal/taskpacket/taskpacket.go"}],
		"model":"claude-test",
		"provider":"anthropic",
		"permission_profile":"workspace-write",
		"commit_policy":"single verified commit",
		"reporting_targets":["owner"],
		"recovery_policy":"retry once",
		"verification_plan":["go test ./internal/taskpacket"]
	}`))
	require.NoError(t, err)
	require.Contains(t, out, `"scope": "module"`)
	require.Contains(t, out, `"scope_path": "internal/taskpacket"`)
	require.Contains(t, out, `"acceptance_criteria": [`)
	require.Contains(t, out, `"resources": [`)
	require.Contains(t, out, `"reporting_targets": [`)
	require.Contains(t, out, `"recovery_policy": "retry once"`)
	require.Contains(t, out, `"verification_plan": [`)
	require.Contains(t, out, `"absolute_path": "`)
	require.Contains(t, out, "Acceptance criteria:")
	require.Contains(t, out, "Verification plan:")
}

func TestWorkerToolsManagePromptWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'worker:%s\\n' \"$*\"\n"), 0o755))

	createOut, err := WorkerCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"cwd":".","trusted_roots":["."],"auto_recover_prompt_misdelivery":false}`))
	require.NoError(t, err)
	var created struct {
		WorkerID string `json:"worker_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	require.NotEmpty(t, created.WorkerID)

	listOut, err := WorkerListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"status":"ready_for_prompt"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "worker_list"`)
	require.Contains(t, listOut, `"total": 1`)
	require.Contains(t, listOut, created.WorkerID)

	readyOut, err := WorkerAwaitReadyTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, readyOut, `"ready_for_prompt": true`)

	observeOut, err := WorkerObserveTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`","screen_text":"trust this folder?"}`))
	require.NoError(t, err)
	require.Contains(t, observeOut, `"status": "trust_prompt"`)

	resolveOut, err := WorkerResolveTrustTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, resolveOut, `"ready_for_prompt": true`)

	sendOut, err := WorkerSendPromptTool{Workspace: workspace, ConfigHome: configHome, Executable: script}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`","prompt":"implement worker tests","task_receipt":{"repo":"codog","task_kind":"test","source_surface":"tool","objective_preview":"implement worker tests"}}`))
	require.NoError(t, err)
	require.Contains(t, sendOut, `"status": "running"`)
	var sent struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(sendOut), &sent))
	require.NotEmpty(t, sent.TaskID)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(sent.TaskID, 4096)
		return err == nil && strings.Contains(logs, "worker:prompt") && strings.Contains(logs, "implement worker tests")
	}, 5*time.Second, 50*time.Millisecond)

	getOut, err := WorkerGetTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, sent.TaskID)
	require.Contains(t, getOut, `"task_status":`)

	runningListOut, err := WorkerListTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"status":"running"}`))
	require.NoError(t, err)
	require.Contains(t, runningListOut, sent.TaskID)
	require.Contains(t, runningListOut, `"total": 1`)

	restartOut, err := WorkerRestartTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, restartOut, `"status": "running"`)

	completeOut, err := WorkerObserveCompletionTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`","finish_reason":"stop","tokens_output":12}`))
	require.NoError(t, err)
	require.Contains(t, completeOut, `"status": "finished"`)

	terminateOut, err := WorkerTerminateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`"}`))
	require.NoError(t, err)
	require.Contains(t, terminateOut, `"status": "terminated"`)
}

func TestWorkerStartupTimeoutToolRecordsEvidence(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()

	createOut, err := WorkerCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"cwd":"."}`))
	require.NoError(t, err)
	var created struct {
		WorkerID string `json:"worker_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))

	_, err = WorkerObserveTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"worker_id":"`+created.WorkerID+`","screen_text":"Do you trust this folder?"}`))
	require.NoError(t, err)

	input, err := json.Marshal(map[string]any{
		"worker_id":             created.WorkerID,
		"pane_command":          "codog repl",
		"transport_healthy":     true,
		"mcp_healthy":           true,
		"elapsed_seconds":       42,
		"trust_prompt_detected": true,
	})
	require.NoError(t, err)
	out, err := WorkerStartupTimeoutTool{ConfigHome: configHome}.Execute(context.Background(), input)
	require.NoError(t, err)

	var result struct {
		Status            string `json:"status"`
		LastError         string `json:"last_error"`
		StartupNoEvidence struct {
			Classification string `json:"classification"`
			Evidence       struct {
				LastLifecycleState  string `json:"last_lifecycle_state"`
				PaneCommand         string `json:"pane_command"`
				TrustPromptDetected bool   `json:"trust_prompt_detected"`
				TransportHealth     string `json:"transport_health"`
				MCPHealth           string `json:"mcp_health"`
			} `json:"evidence"`
		} `json:"startup_no_evidence"`
		Events []struct {
			Type           string         `json:"type"`
			LaneEvent      string         `json:"lane_event"`
			Classification string         `json:"classification"`
			Evidence       map[string]any `json:"evidence"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "startup_no_evidence: trust_required", result.LastError)
	require.Equal(t, "trust_required", result.StartupNoEvidence.Classification)
	require.Equal(t, "trust_prompt", result.StartupNoEvidence.Evidence.LastLifecycleState)
	require.Equal(t, "codog repl", result.StartupNoEvidence.Evidence.PaneCommand)
	require.True(t, result.StartupNoEvidence.Evidence.TrustPromptDetected)
	require.Equal(t, "transport:healthy", result.StartupNoEvidence.Evidence.TransportHealth)
	require.Equal(t, "mcp:healthy", result.StartupNoEvidence.Evidence.MCPHealth)
	require.NotEmpty(t, result.Events)
	event := result.Events[len(result.Events)-1]
	require.Equal(t, "worker.startup_no_evidence", event.Type)
	require.Equal(t, "lane.blocked", event.LaneEvent)
	require.Equal(t, "trust_required", event.Classification)
	require.Equal(t, "trust_prompt", event.Evidence["last_lifecycle_state"])
}

func TestRecoveryToolsRecordLedger(t *testing.T) {
	configHome := t.TempDir()

	recipeOut, err := RecoveryRecipeTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"scenario":"stale_branch"}`))
	require.NoError(t, err)
	require.Contains(t, recipeOut, `"kind": "recovery_recipe"`)
	require.Contains(t, recipeOut, `"kind": "merge_forward_branch"`)

	statusOut, err := RecoveryStatusTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"scenario":"stale_branch"}`))
	require.NoError(t, err)
	require.Contains(t, statusOut, `"attempted": false`)
	require.Contains(t, statusOut, `"attempts_remaining": 1`)

	firstOut, err := RecoveryAttemptTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"scenario":"stale_branch"}`))
	require.NoError(t, err)
	var first struct {
		Result struct {
			Kind       string `json:"kind"`
			StepsTaken int    `json:"steps_taken"`
		} `json:"result"`
		Entry struct {
			State        string `json:"state"`
			AttemptCount int    `json:"attempt_count"`
		} `json:"entry"`
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(firstOut), &first))
	require.Equal(t, "recovered", first.Result.Kind)
	require.Equal(t, 2, first.Result.StepsTaken)
	require.Equal(t, "succeeded", first.Entry.State)
	require.Equal(t, 1, first.Entry.AttemptCount)
	require.Equal(t, "recovery.succeeded", first.Events[len(first.Events)-1].Type)

	secondOut, err := RecoveryAttemptTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"scenario":"stale_branch"}`))
	require.NoError(t, err)
	require.Contains(t, secondOut, `"kind": "escalation_required"`)
	require.Contains(t, secondOut, `"state": "exhausted"`)
	require.Contains(t, secondOut, "max recovery attempts")

	listOut, err := RecoveryStatusTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "recovery_ledger"`)
	require.Contains(t, listOut, `"scenario": "stale_branch"`)
}

func TestRecoveryAttemptToolRecordsFailedStep(t *testing.T) {
	configHome := t.TempDir()

	out, err := RecoveryAttemptTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"scenario":"partial_plugin_startup","failure_summary":"mcp still unhealthy","failed_step_index":1}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "partial_recovery"`)
	require.Contains(t, out, `"state": "failed"`)
	require.Contains(t, out, `"kind": "restart_plugin"`)
	require.Contains(t, out, `"kind": "retry_mcp_handshake"`)
	require.Contains(t, out, `"last_failure_summary": "mcp still unhealthy"`)
}

func TestPolicyEvaluateToolReturnsActions(t *testing.T) {
	out, err := PolicyEvaluateTool{}.Execute(context.Background(), []byte(`{
		"lane_id":"lane-7",
		"green_level":3,
		"green_contract_satisfied":true,
		"review_status":"approved",
		"diff_scope":"scoped",
		"branch_status":"stale",
		"branch_behind":2,
		"verification_blocked":true,
		"completed":true
	}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "policy_evaluation"`)
	require.Contains(t, out, `"kind": "merge_forward"`)
	require.Contains(t, out, `"kind": "closeout_lane"`)
	require.Contains(t, out, `"kind": "cleanup_session"`)
	require.Contains(t, out, `"rule_id": "stale-branch-merge-forward"`)
	require.Contains(t, out, `"rule_id": "lane-completed-closeout"`)
	require.NotContains(t, out, `"kind": "merge_to_dev"`)
}

func TestApprovalTokenToolPersistsAndConsumesGrant(t *testing.T) {
	configHome := t.TempDir()
	tool := ApprovalTokenTool{ConfigHome: configHome}

	grantOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"grant",
		"token":"tok-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main"},
		"approving_actor":"owner",
		"approved_executor":"release-bot",
		"max_uses":1,
		"delegation_chain":[{"actor":"owner","session_id":"session-owner","reason":"owner approval"}]
	}`))
	require.NoError(t, err)
	require.Contains(t, grantOut, `"kind": "approval_token"`)
	require.Contains(t, grantOut, `"status": "approval_granted"`)

	verifyOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"verify",
		"token":"tok-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main"},
		"executing_actor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, verifyOut, `"status": "ok"`)
	require.Contains(t, verifyOut, `"delegated_execution": true`)

	consumeOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"consume",
		"token":"tok-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main"},
		"executing_actor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, consumeOut, `"status": "approval_consumed"`)
	require.Contains(t, consumeOut, `"uses": 1`)

	replayOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"consume",
		"token":"tok-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main"},
		"executing_actor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, replayOut, `"status": "denied"`)
	require.Contains(t, replayOut, `"error_kind": "approval_already_consumed"`)

	listOut, err := tool.Execute(context.Background(), []byte(`{"action":"list"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "approval_token_ledger"`)
	require.Contains(t, listOut, `"token": "tok-main"`)
}

func TestCommandToolExecutesWithJSONStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX cat")
	}
	out, err := CommandTool{
		Name:      "echo_json",
		Command:   "cat",
		Workspace: t.TempDir(),
	}.Execute(context.Background(), []byte(`{"ok":true}`))
	require.NoError(t, err)
	require.Contains(t, out, `ok`)
}

func TestBashToolRejectsUnavailableSandbox(t *testing.T) {
	_, err := BashTool{
		Workspace:       t.TempDir(),
		SandboxStrategy: "codog-missing-sandbox",
	}.Execute(context.Background(), []byte(`{"command":"pwd"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available")
}

func TestMCPToolCallsRemoteTool(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPToolHelperProcess"},
		Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
	}
	out, err := MCPTool{
		Name:       NewMCPToolName("test server", "echo"),
		ServerName: "test server",
		Server:     server,
		RemoteName: "echo",
	}.Execute(context.Background(), []byte(`{"text":"hi"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"text":"echo"`)

	out, err = MCPDispatchTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"test","tool":"echo","arguments":{"text":"hi"}}`))
	require.NoError(t, err)
	require.Contains(t, out, `"text":"echo"`)

	_, err = MCPDispatchTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"missing","tool":"echo"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown MCP server")

	authOut, err := MCPAuthTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"test"}`))
	require.NoError(t, err)
	require.Contains(t, authOut, `"status": "ok"`)
	require.Contains(t, authOut, `"tool_count": 1`)

	authOut, err = MCPAuthTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"missing"}`))
	require.NoError(t, err)
	require.Contains(t, authOut, `"status": "unknown"`)
}

func TestMCPResourceToolsListAndReadRemoteResources(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"test": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestMCPToolHelperProcess"},
			Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
		},
	}

	listAllOut, err := ListMCPResourcesTool{Servers: servers}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, listAllOut, `"kind": "mcp_resources"`)
	require.Contains(t, listAllOut, `"server": "test"`)
	require.Contains(t, listAllOut, "codog://note")

	listOut, err := ListMCPResourcesTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"test"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"server": "test"`)
	require.Contains(t, listOut, "codog://note")

	readOut, err := ReadMCPResourceTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"test","uri":"codog://note"}`))
	require.NoError(t, err)
	require.Contains(t, readOut, `"uri": "codog://note"`)
	require.Contains(t, readOut, "note body")

	_, err = ReadMCPResourceTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"missing","uri":"codog://note"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown MCP server")
}

func TestMCPPromptAndTemplateTools(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"test": {
			Command: os.Args[0],
			Args:    []string{"-test.run=TestMCPToolHelperProcess"},
			Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
		},
	}

	templatesOut, err := ListMCPResourceTemplatesTool{Servers: servers}.Execute(context.Background(), []byte(`{}`))
	require.NoError(t, err)
	require.Contains(t, templatesOut, `"kind": "mcp_resource_templates"`)
	require.Contains(t, templatesOut, `"uriTemplate": "codog://{name}"`)

	promptsOut, err := ListMCPPromptsTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"test"}`))
	require.NoError(t, err)
	require.Contains(t, promptsOut, `"name": "review"`)

	promptOut, err := GetMCPPromptTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"test","prompt":"review","arguments":{"topic":"tools"}}`))
	require.NoError(t, err)
	require.Contains(t, promptOut, `"prompt": "review"`)
	require.Contains(t, promptOut, `"Review tools"`)
}

func TestNewMCPToolNameUsesCompatibilityNormalization(t *testing.T) {
	require.Equal(t, "mcp__github_com__tool_name_", NewMCPToolName("github.com", "tool name!"))
	require.Equal(t, "mcp__claude_ai_Example_Server__weather_tool", NewMCPToolName("claude.ai Example Server", "weather tool"))
}

func TestMCPToolHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_MCP_TOOL_HELPER") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var req map[string]any
		if err := json.Unmarshal([]byte(reader.Text()), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			writeMCPResponse(id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "test", "version": "0.0.0"},
			})
		case "tools/list":
			writeMCPResponse(id, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text.",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			name, _ := params["name"].(string)
			writeMCPResponse(id, map[string]any{"content": []map[string]any{{"type": "text", "text": name}}})
		case "resources/list":
			writeMCPResponse(id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note", "mimeType": "text/plain"}}})
		case "resources/templates/list":
			writeMCPResponse(id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://{name}",
				"name":        "named note",
			}}})
		case "resources/read":
			params, _ := req["params"].(map[string]any)
			uri, _ := params["uri"].(string)
			writeMCPResponse(id, map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "text/plain", "text": "note body"}}})
		case "prompts/list":
			writeMCPResponse(id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a topic.",
			}}})
		case "prompts/get":
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			topic, _ := args["topic"].(string)
			writeMCPResponse(id, map[string]any{"messages": []map[string]any{{
				"role":    "user",
				"content": map[string]any{"type": "text", "text": "Review " + topic},
			}}})
		}
	}
	os.Exit(0)
}

func writeMCPResponse(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(strings.TrimSpace(string(data)))
}
