package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/cron"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/team"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/stretchr/testify/require"
)

func TestReadTUIClipboardFallsBackToText(t *testing.T) {
	previousReadClipboardImage := readClipboardImage
	previousReadClipboard := readClipboard
	readClipboardImage = func(context.Context) (clipboardImage, error) {
		return clipboardImage{}, errNoClipboardImage
	}
	readClipboard = func(context.Context) ([]byte, string, error) {
		return []byte("clipboard text"), "test-text-clipboard", nil
	}
	t.Cleanup(func() {
		readClipboardImage = previousReadClipboardImage
		readClipboard = previousReadClipboard
	})
	app := &App{Workspace: t.TempDir()}

	content, err := app.readTUIClipboard(context.Background())
	require.NoError(t, err)
	require.Equal(t, "clipboard text", content.Text)
	require.Empty(t, content.AttachmentPath)
}

func TestPinCommandSlashAndCompactPreservesPinnedMessages(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	for _, text := range []string{"one", "two", "three", "four", "five"} {
		require.NoError(t, store.Append("source", anthropic.TextMessage("user", text)))
	}
	sess, err := store.Open("source")
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Pin([]string{"1", "--session", "source", "--json"}, config.FlagOverrides{}))
	var report pinReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "pin", report.Action)
	require.Equal(t, 0, report.MessageIndex)
	require.Equal(t, 1, report.DisplayIndex)
	require.Equal(t, []int{0}, report.PinnedMessages)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"pin", "source", "2"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "pin", report.Action)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, 1, report.MessageIndex)
	require.Equal(t, []int{0, 1}, report.PinnedMessages)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"unpin", "source", "2"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "unpin", report.Action)
	require.Equal(t, []int{0}, report.PinnedMessages)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session pin 2 --json", sess))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "pin", report.Action)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, []int{0, 1}, report.PinnedMessages)
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session unpin 2 --json", sess))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "unpin", report.Action)
	require.Equal(t, []int{0}, report.PinnedMessages)
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/pin", []string{"3"}, config.FlagOverrides{Resume: "source"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "pin", report.Action)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, 2, report.MessageIndex)
	require.Equal(t, []int{0, 2}, report.PinnedMessages)
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/unpin", []string{"3"}, config.FlagOverrides{Resume: "source"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "unpin", report.Action)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, 2, report.MessageIndex)
	require.Equal(t, []int{0}, report.PinnedMessages)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/pin 2", sess))
	require.Empty(t, errOut.String())
	opened, err := store.Open("source")
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, opened.Metadata.PinnedMessages)
	out.Reset()

	require.NoError(t, app.ListSessionsWithActive([]string{"--json"}, "source"))
	var list sessionListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &list))
	require.Equal(t, []int{0, 1}, list.SessionDetails[0].PinnedMessages)
	out.Reset()

	require.NoError(t, app.SessionShow([]string{"source", "--json"}))
	var shown sessionShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &shown))
	require.Equal(t, []int{0, 1}, shown.PinnedMessages)
	out.Reset()

	require.NoError(t, app.Compact([]string{"--session", "source", "--keep", "2", "--json"}, config.FlagOverrides{}))
	opened, err = store.Open("source")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 5)
	require.Contains(t, opened.Messages[0].Content[0].Text, "auto-compacted")
	require.Equal(t, "one", opened.Messages[1].Content[0].Text)
	require.Equal(t, "two", opened.Messages[2].Content[0].Text)
	require.Equal(t, "four", opened.Messages[3].Content[0].Text)
	require.Equal(t, "five", opened.Messages[4].Content[0].Text)
	require.Equal(t, []int{1, 2}, opened.Metadata.PinnedMessages)
	out.Reset()

	require.NoError(t, app.Unpin([]string{"2", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "unpin", report.Action)
	require.Equal(t, []int{2}, report.PinnedMessages)
}

func TestBuildAgentCommandQuotesPrompt(t *testing.T) {
	command := buildAgentCommand("/tmp/codog", agentdefs.Definition{
		Name:   "reviewer",
		Model:  "mock-model",
		Prompt: "review carefully",
		Tools:  []string{"read_file", "grep"},
	}, "check '$HOME'")

	require.Contains(t, command, "'/tmp/codog'")
	require.Contains(t, command, "--model 'mock-model'")
	require.Contains(t, command, "--tools 'read_file,grep'")
	require.Contains(t, command, "prompt 'review carefully")
	require.Contains(t, command, "'\"'\"'$HOME'\"'\"'")
}

func TestSessionRuntimeOverridesLoadAgentsAndPluginSurfaces(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "missing.json")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "reviewer.json"), []byte(`{"name":"reviewer","description":"file reviewer","prompt":"file prompt"}`), 0o644))

	pluginRoot := filepath.Join(t.TempDir(), "session-plugin")
	for _, dir := range []string{"commands", filepath.Join("skills", "review"), "agents", "hooks"} {
		require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{
		"id":"session",
		"name":"Session Plugin",
		"version":"1.0.0",
		"description":"Session-only test plugin",
		"tools":[{"name":"session_tool","description":"Session tool","command":"printf","args":["ok"],"permission":"read-only"}],
		"commands":["commands"],
		"skills":["skills"],
		"agents":["agents"],
		"hooks":["hooks/hooks.json"],
		"mcp_servers":{"local":{"command":"cat"}}
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "commands", "hello.md"), []byte("Say hello from the session plugin.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "skills", "review", "SKILL.md"), []byte("---\nname: review\ndescription: Review from the session plugin\n---\nReview carefully.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "agents", "helper.json"), []byte(`{"description":"Session helper","prompt":"Help with the task.","tools":["read_file"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "hooks", "hooks.json"), []byte(`{"pre_tool_use":["echo session-hook"]}`), 0o644))

	inlineAgents := `{"reviewer":{"description":"CLI reviewer","prompt":"CLI prompt","model":"glm52","tools":["read_file","grep"]}}`
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"--agents", inlineAgents,
			"--plugin-dir", pluginRoot,
			"agents", "list", "--json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var agents agentsListReport
	require.NoError(t, json.Unmarshal([]byte(out), &agents))
	require.Equal(t, 2, agents.Count)
	require.True(t, agentDefinitionExists(agents.Agents, "reviewer", "cli"))
	require.True(t, agentDefinitionExists(agents.Agents, "session:helper", "plugin:session"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"--plugin-dir", pluginRoot,
			"capabilities", "--json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"name": "session_tool"`)

	for _, check := range []struct {
		args     []string
		contains string
	}{
		{args: []string{"commands", "list", "--json"}, contains: `"name": "session:hello"`},
		{args: []string{"skills", "list", "--json"}, contains: `"name": "session:review"`},
		{args: []string{"mcp", "list", "--json"}, contains: `"plugin:session:local"`},
	} {
		out, err = captureStdout(t, func() error {
			args := []string{"--config", configPath, "--cwd", workspace, "--plugin-dir", pluginRoot}
			return RunCLI(context.Background(), append(args, check.args...), config.FlagOverrides{})
		})
		require.NoError(t, err)
		require.Contains(t, out, check.contains)
	}
	require.NoDirExists(t, filepath.Join(workspace, ".codog", "plugins", "session"))
}

func agentDefinitionExists(definitions []agentdefs.Definition, name string, source string) bool {
	for _, definition := range definitions {
		if definition.Name == name && definition.Source == source {
			return true
		}
	}
	return false
}

func TestAgentsCommandAcceptsOutputFormatFlags(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "planner.json"), []byte(`{"name":"planner","description":"plans work","prompt":"plan"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "reviewer.json"), []byte(`{"name":"reviewer","description":"reviews code","prompt":"review"}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "agents", "migrated.md"), []byte(`---
description: migrated markdown agent
tools: read_file, grep
---
# Migrated
Use Claude Code-style markdown agent definitions.
`), 0o644))
	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.AgentsWithOverrides(nil, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Agents\n")
	require.Contains(t, out.String(), "planner")
	require.Contains(t, out.String(), "reviews code")
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"--output-format", "json", "list", "review"}, config.FlagOverrides{}))
	var listReport agentsListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "agents", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.Equal(t, 1, listReport.Count)
	require.Equal(t, []string{".json", ".md", ".markdown"}, listReport.AcceptedFormats)
	require.Equal(t, "reviewer", listReport.Agents[0].Name)
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"show", "migrated", "--json"}, config.FlagOverrides{}))
	var migratedReport agentShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &migratedReport))
	require.Equal(t, "migrated", migratedReport.Agent.Name)
	require.Equal(t, "claude", migratedReport.Agent.Source)
	require.Equal(t, "markdown", migratedReport.Agent.Format)
	require.Equal(t, []string{"read_file", "grep"}, migratedReport.Agent.Tools)
	require.Contains(t, migratedReport.Agent.Prompt, "Claude Code-style")
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"help", "--json"}, config.FlagOverrides{}))
	var helpReport agentsHelpReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &helpReport))
	require.Equal(t, "help", helpReport.Action)
	require.Contains(t, helpReport.AcceptedFormats, ".md")
	require.Contains(t, helpReport.Sources, ".claude/agents")
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"show", "planner", "--json"}, config.FlagOverrides{}))
	var showReport agentShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &showReport))
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "planner", showReport.Agent.Name)
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"info", "planner"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Agent\n")
	require.Contains(t, out.String(), "Name             planner")
	require.Contains(t, out.String(), "Description      plans work")
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"describe", "reviewer", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &showReport))
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "reviewer", showReport.Agent.Name)
	out.Reset()

	require.ErrorContains(t, app.AgentsWithOverrides([]string{"info", "missing", "--json"}, config.FlagOverrides{}), "agent_not_found")
	var errorReport actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "agents", errorReport.Kind)
	require.Equal(t, "show", errorReport.Action)
	require.Equal(t, "agent_not_found", errorReport.ErrorKind)
	require.Contains(t, errorReport.Hint, "codog agents list")
	out.Reset()

	require.ErrorContains(t, app.AgentsWithOverrides([]string{"describe", "--json"}, config.FlagOverrides{}), "missing_argument")
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "show", errorReport.Action)
	require.Equal(t, "missing_argument", errorReport.ErrorKind)
	require.Contains(t, errorReport.Hint, "codog agents show")
}

func TestAgentsCreateCommandCreatesWorkspaceDefinition(t *testing.T) {
	workspace := t.TempDir()
	agentDir := filepath.Join(workspace, ".codog", "agents")
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "existing.json"), []byte(`{"name":"existing","description":"Existing agent","prompt":"Keep working."}`), 0o644))
	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.AgentsWithOverrides([]string{"create", "Review Bot", "--json"}, config.FlagOverrides{}))
	var createReport agentCreateReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &createReport))
	require.Equal(t, "agents", createReport.Kind)
	require.Equal(t, "create", createReport.Action)
	require.Equal(t, "ok", createReport.Status)
	require.Equal(t, "created", createReport.Result)
	require.Equal(t, "review-bot", createReport.Name)
	require.Equal(t, "json", createReport.Format)
	require.Equal(t, filepath.Join(workspace, ".codog", "agents", "review-bot.json"), createReport.Path)
	require.Equal(t, "review-bot", createReport.Agent.Name)
	require.Equal(t, "workspace", createReport.Agent.Source)
	require.FileExists(t, createReport.Path)

	data, err := os.ReadFile(createReport.Path)
	require.NoError(t, err)
	var fileDef agentdefs.Definition
	require.NoError(t, json.Unmarshal(data, &fileDef))
	require.Equal(t, "review-bot", fileDef.Name)
	require.Contains(t, fileDef.Description, "Focused local subagent")
	require.Contains(t, fileDef.Prompt, "report verification results")

	out.Reset()
	require.NoError(t, app.AgentsWithOverrides([]string{"show", "review-bot", "--json"}, config.FlagOverrides{}))
	var showReport agentShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &showReport))
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "review-bot", showReport.Agent.Name)
	require.Equal(t, createReport.Path, showReport.Agent.Path)
	out.Reset()
	require.NoError(t, app.AgentsWithOverrides([]string{"list", "--json"}, config.FlagOverrides{}))
	var listReport agentsListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.True(t, agentDefinitionExists(listReport.Agents, "existing", "workspace"))
	require.True(t, agentDefinitionExists(listReport.Agents, "review-bot", "workspace"))

	out.Reset()
	require.ErrorContains(t, app.AgentsWithOverrides([]string{"create", "review-bot", "--json"}, config.FlagOverrides{}), "agent_already_exists")
	var errorReport actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "agents", errorReport.Kind)
	require.Equal(t, "create", errorReport.Action)
	require.Equal(t, "agent_already_exists", errorReport.ErrorKind)

	out.Reset()
	require.ErrorContains(t, app.AgentsWithOverrides([]string{"create", "$$$", "--json"}, config.FlagOverrides{}), "invalid_agent_name")
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "invalid_agent_name", errorReport.ErrorKind)

	out.Reset()
	require.ErrorContains(t, app.AgentsWithOverrides([]string{"create", "--json"}, config.FlagOverrides{}), "missing_argument")
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "missing_argument", errorReport.ErrorKind)
	require.Contains(t, errorReport.Hint, "codog agents create")

	out.Reset()
	require.Error(t, app.AgentsWithOverrides([]string{"create", "helper", "extra", "--json"}, config.FlagOverrides{}))
	var cliReport cliErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &cliReport))
	require.Equal(t, "unexpected_extra_args", cliReport.ErrorKind)
	require.Equal(t, "agents create", cliReport.Command)
	require.Equal(t, []string{"extra"}, cliReport.Args)
	require.Contains(t, cliReport.Hint, "codog agents create")

	out.Reset()
	require.NoError(t, app.AgentsWithOverrides([]string{"create", "/Upper_Path.Agent"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), "created upper_path.agent")
	require.FileExists(t, filepath.Join(workspace, ".codog", "agents", "upper_path.agent.json"))
}

func TestAgentsRunEmitsSubagentStartHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "reviewer.json"), []byte(`{"name":"reviewer","model":"agent-model","prompt":"Base review instructions"}`), 0o644))
	received := make(chan struct {
		Event     string `json:"event"`
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var payload struct {
			Event     string `json:"event"`
			AgentID   string `json:"agent_id"`
			AgentType string `json:"agent_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				SubagentStartCommands: []config.HookCommand{
					{Matcher: "reviewer", Type: "http", URL: server.URL},
				},
			},
		},
		Sessions:  session.NewStore(configHome),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.AgentsWithOverrides([]string{"run", "reviewer", "check auth"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"agent": "reviewer"`)
	require.Contains(t, out.String(), `"kind": "agent"`)
	var runReport agentRunReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &runReport))
	require.Equal(t, "agents", runReport.Kind)
	require.Equal(t, "run", runReport.Action)
	require.Equal(t, "ok", runReport.Status)
	require.NotEmpty(t, runReport.RunID)
	require.Equal(t, runReport.Task.ID, runReport.Run.TaskID)
	require.Equal(t, "reviewer", runReport.Run.Agent)
	require.Equal(t, "check auth", runReport.Run.Prompt)
	select {
	case payload := <-received:
		require.Equal(t, "subagent_start", payload.Event)
		require.NotEmpty(t, payload.AgentID)
		require.Equal(t, "reviewer", payload.AgentType)
	case <-time.After(2 * time.Second):
		t.Fatal("subagent start hook was not called")
	}
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"runs", "--json"}, config.FlagOverrides{}))
	var runsReport agentRunsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &runsReport))
	require.Equal(t, "agents", runsReport.Kind)
	require.Equal(t, "runs", runsReport.Action)
	require.Equal(t, 1, runsReport.Count)
	require.Equal(t, runReport.RunID, runsReport.Runs[0].Run.ID)
	require.NotEmpty(t, runsReport.Runs[0].CurrentStatus)
	require.NotEmpty(t, runsReport.Runs[0].Freshness)
	require.NotEmpty(t, runsReport.Runs[0].Health.State)
	out.Reset()

	require.NoError(t, app.Subagent([]string{"list", "--json"}, config.FlagOverrides{}))
	var subagentListReport agentRunsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &subagentListReport))
	require.Equal(t, "agents", subagentListReport.Kind)
	require.Equal(t, "runs", subagentListReport.Action)
	require.Equal(t, runReport.RunID, subagentListReport.Runs[0].Run.ID)
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"status", runReport.RunID, "--json"}, config.FlagOverrides{}))
	var statusReport agentRunsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusReport))
	require.Equal(t, "status", statusReport.Action)
	require.NotNil(t, statusReport.Run)
	require.Equal(t, runReport.RunID, statusReport.Run.Run.ID)
	require.Equal(t, runReport.Task.ID, statusReport.Run.Run.TaskID)
	require.NotEmpty(t, statusReport.Run.Health.Summary)
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"update", runReport.RunID, "more", "context", "--json"}, config.FlagOverrides{}))
	var updateReport agentRunActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &updateReport))
	require.Equal(t, "update", updateReport.Action)
	require.Equal(t, "more context", updateReport.Message)
	require.Len(t, updateReport.Task.Messages, 1)
	require.Equal(t, "more context", updateReport.Task.Messages[0].Message)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/subagent steer "+runReport.RunID+" slash context --json", &session.Session{ID: "session-1"}))
	var slashSteerReport agentRunActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &slashSteerReport))
	require.Equal(t, "update", slashSteerReport.Action)
	require.Equal(t, "slash context", slashSteerReport.Message)
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/subagent", []string{"status", runReport.RunID}, config.FlagOverrides{Resume: "session-1"}, "json"))
	var resumedSubagentStatus agentRunsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &resumedSubagentStatus))
	require.Equal(t, "agents", resumedSubagentStatus.Kind)
	require.Equal(t, "status", resumedSubagentStatus.Action)
	require.Equal(t, runReport.RunID, resumedSubagentStatus.Run.Run.ID)
	out.Reset()

	outputTask, err := background.NewStore(configHome).RunWithOptions("printf agent-output", workspace, background.RunOptions{Kind: "agent", AgentType: "reviewer", SessionID: "session-1"})
	require.NoError(t, err)
	outputRun, err := agentruns.NewStore(configHome).Save(agentruns.Run{
		ID:        "run-" + outputTask.ID,
		Agent:     "reviewer",
		Workspace: workspace,
		SessionID: "session-1",
		TaskID:    outputTask.ID,
		CreatedAt: outputTask.StartedAt,
		UpdatedAt: outputTask.StartedAt,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(outputTask.ID, 4096)
		return err == nil && strings.Contains(logs, "agent-output")
	}, 2*time.Second, 50*time.Millisecond)

	require.NoError(t, app.AgentsWithOverrides([]string{"output", outputRun.ID, "--bytes", "4096", "--json"}, config.FlagOverrides{}))
	var outputReport agentRunActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &outputReport))
	require.Equal(t, "output", outputReport.Action)
	require.Contains(t, outputReport.Output, "agent-output")
	out.Reset()

	require.NoError(t, app.Subagent([]string{"logs", outputRun.ID, "--bytes", "4096", "--json"}, config.FlagOverrides{}))
	var subagentLogsReport agentRunActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &subagentLogsReport))
	require.Equal(t, "output", subagentLogsReport.Action)
	require.Contains(t, subagentLogsReport.Output, "agent-output")
	out.Reset()

	observedAt := time.Now().UTC().Format(time.RFC3339)
	require.NoError(t, app.AgentsWithOverrides([]string{"heartbeat", outputRun.ID, "--status", "working", "--observed-at", observedAt, "--json"}, config.FlagOverrides{}))
	var heartbeatReport agentRunActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &heartbeatReport))
	require.Equal(t, "heartbeat", heartbeatReport.Action)
	require.NotNil(t, heartbeatReport.Task.Heartbeat)
	require.Equal(t, "working", heartbeatReport.Task.Heartbeat.Status)
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"board", "60", "--json"}, config.FlagOverrides{}))
	var boardReport agentRunBoardReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &boardReport))
	require.Equal(t, "board", boardReport.Action)
	require.True(t, agentRunBoardContains(boardReport, outputRun.ID))
	out.Reset()

	orphanRun, err := agentruns.NewStore(configHome).Save(agentruns.Run{
		ID:        "run-orphan",
		Agent:     "reviewer",
		Workspace: workspace,
		SessionID: "session-1",
		TaskID:    "missing-task",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, app.AgentsWithOverrides([]string{"prune", "--json"}, config.FlagOverrides{}))
	var pruneReport agentRunPruneReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &pruneReport))
	require.Contains(t, pruneReport.Removed, orphanRun.ID)
	out.Reset()

	longTask, err := background.NewStore(configHome).RunWithOptions("sleep 5", workspace, background.RunOptions{Kind: "agent", AgentType: "reviewer", SessionID: "session-1"})
	require.NoError(t, err)
	longRun, err := agentruns.NewStore(configHome).Save(agentruns.Run{
		ID:        "run-" + longTask.ID,
		Agent:     "reviewer",
		Workspace: workspace,
		SessionID: "session-1",
		TaskID:    longTask.ID,
		CreatedAt: longTask.StartedAt,
		UpdatedAt: longTask.StartedAt,
	})
	require.NoError(t, err)
	require.NoError(t, app.Subagent([]string{"kill", longRun.ID, "--json"}, config.FlagOverrides{}))
	var stopReport agentRunActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &stopReport))
	require.Equal(t, "stop", stopReport.Action)
	require.Equal(t, "stopped", stopReport.Task.Status)
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"run-remove", runReport.RunID, "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"removed": true`)
	out.Reset()
	require.NoError(t, app.AgentsWithOverrides([]string{"run-remove", outputRun.ID, "--json"}, config.FlagOverrides{}))
	out.Reset()
	require.NoError(t, app.AgentsWithOverrides([]string{"run-remove", longRun.ID, "--json"}, config.FlagOverrides{}))
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"runs", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &runsReport))
	require.Zero(t, runsReport.Count)
}

func agentRunBoardContains(report agentRunBoardReport, id string) bool {
	for _, entries := range [][]agentRunBoardEntry{report.Active, report.Blocked, report.Finished, report.Orphaned} {
		for _, entry := range entries {
			if entry.Run.ID == id {
				return true
			}
		}
	}
	return false
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	return workspace
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}

func runGitOutput(t *testing.T, workspace string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
	return string(data)
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	type readResult struct {
		Data []byte
		Err  error
	}
	readDone := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(reader)
		readDone <- readResult{Data: data, Err: err}
	}()
	os.Stdout = writer
	runErr := fn()
	os.Stdout = original
	require.NoError(t, writer.Close())
	result := <-readDone
	require.NoError(t, result.Err)
	require.NoError(t, reader.Close())
	return string(result.Data), runErr
}

func TestParseAgentRunArgs(t *testing.T) {
	req, err := parseAgentRunArgs([]string{"--worktree", "reviewer", "check", "this"})
	require.NoError(t, err)
	require.True(t, req.Worktree)
	require.Equal(t, "reviewer", req.Name)
	require.Equal(t, "check this", req.Prompt)

	_, err = parseAgentRunArgs([]string{"--worktree", "reviewer"})
	require.Error(t, err)
}

func TestNormalizeSubagentArgs(t *testing.T) {
	require.True(t, commandAcceptsGlobalOutputFormat("subagent"))

	args, err := normalizeSubagentArgs([]string{"--json"})
	require.NoError(t, err)
	require.Equal(t, []string{"runs", "--output-format", "json"}, args)

	args, err = normalizeSubagentArgs([]string{"steer", "run-1", "check", "auth", "--output-format", "json"})
	require.NoError(t, err)
	require.Equal(t, []string{"update", "run-1", "check", "auth", "--output-format", "json"}, args)

	args, err = normalizeSubagentArgs([]string{"kill", "run-1"})
	require.NoError(t, err)
	require.Equal(t, []string{"stop", "run-1", "--output-format", "text"}, args)

	args, err = normalizeSubagentArgs([]string{"status", "run-1"})
	require.NoError(t, err)
	require.Equal(t, []string{"status", "run-1", "--output-format", "text"}, args)

	args, err = normalizeSubagentArgs([]string{"logs", "run-1", "--bytes", "2048"})
	require.NoError(t, err)
	require.Equal(t, []string{"output", "run-1", "--bytes", "2048", "--output-format", "text"}, args)

	_, err = normalizeSubagentArgs([]string{"steer", "run-1"})
	require.ErrorContains(t, err, "requires a run id and message")
}

func TestBackgroundWatchCommandOutputsJSONLEvents(t *testing.T) {
	configHome := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.Run("echo cli-watch", t.TempDir())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(task.ID, 100)
		return err == nil && strings.Contains(logs, "cli-watch")
	}, 2*time.Second, 50*time.Millisecond)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.Background([]string{"watch", task.ID}))
	require.Contains(t, out.String(), `"type":"status"`)
	require.Contains(t, out.String(), `"type":"log"`)
	require.Contains(t, out.String(), "cli-watch")
}

func TestBackgroundGlobalOutputFormatListUsesReport(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "background", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "background", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Zero(t, report.Count)
	require.Empty(t, report.SessionID)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "background", "list", "session-1", "extra", "--json"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var errorReport cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "unexpected_extra_args", errorReport.ErrorKind)
	require.Equal(t, "background list", errorReport.Command)
	require.Equal(t, []string{"extra"}, errorReport.Args)
	require.Contains(t, errorReport.Hint, "codog background list [session-id]")

	for _, tc := range []struct {
		action string
	}{
		{action: "status"},
		{action: "stop"},
		{action: "restart"},
	} {
		out, err = captureStdout(t, func() error {
			return RunCLI(context.Background(), []string{"--config", configPath, "background", tc.action, "task-1", "extra", "--json"}, config.FlagOverrides{})
		})
		require.Error(t, err)
		require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
		require.Equal(t, "unexpected_extra_args", errorReport.ErrorKind)
		require.Equal(t, "background "+tc.action, errorReport.Command)
		require.Equal(t, []string{"extra"}, errorReport.Args)
		require.Contains(t, errorReport.Hint, "codog background list [session-id]")
	}

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "background", "supervise", "extra", "--json"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "unexpected_extra_args", errorReport.ErrorKind)
	require.Equal(t, "background supervise", errorReport.Command)
	require.Equal(t, []string{"extra"}, errorReport.Args)
	require.Contains(t, errorReport.Hint, "codog background list [session-id]")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "background", "nope", "--json"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "unexpected_extra_args", errorReport.ErrorKind)
	require.Equal(t, "background", errorReport.Command)
	require.Equal(t, []string{"nope"}, errorReport.Args)
	require.Contains(t, errorReport.Hint, "codog background list [session-id]")
}

func TestBackgroundJSONReportsAndWatchMaxEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.RunWithOptions("echo bg-json", workspace, background.RunOptions{SessionID: "session-1"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(task.ID, 1024)
		return err == nil && strings.Contains(logs, "bg-json")
	}, 2*time.Second, 50*time.Millisecond)

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: workspace,
		Out:       &out,
	}

	require.NoError(t, app.BackgroundWithOverrides([]string{"list", "--json"}, config.FlagOverrides{SessionID: "session-1"}))
	var report backgroundCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "background", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "session-1", report.SessionID)
	require.Len(t, report.Tasks, 1)
	require.Equal(t, task.ID, report.Tasks[0].ID)
	out.Reset()

	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"--output-format", "json",
			"tasks", "list",
		}, config.FlagOverrides{SessionID: "session-1"})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &report))
	require.Equal(t, "background", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "session-1", report.SessionID)
	require.Len(t, report.Tasks, 1)
	require.Equal(t, task.ID, report.Tasks[0].ID)

	require.NoError(t, app.BackgroundWithOverrides([]string{"status", task.ID, "--output-format", "json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "status", report.Action)
	require.Equal(t, task.ID, report.TaskID)
	require.NotNil(t, report.Task)
	require.Equal(t, task.ID, report.Task.ID)
	out.Reset()

	require.NoError(t, app.BackgroundWithOverrides([]string{"logs", task.ID, "--bytes", "1024", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "logs", report.Action)
	require.Equal(t, task.ID, report.TaskID)
	require.Contains(t, report.Log, "bg-json")
	require.Positive(t, report.Bytes)
	out.Reset()

	require.NoError(t, app.BackgroundWithOverrides([]string{"watch", task.ID, "--max-events", "1", "--json"}, config.FlagOverrides{}))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 1)
	var event background.WatchEvent
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &event))
	require.Equal(t, "status", event.Type)
	require.Equal(t, task.ID, event.ID)
}

func TestBackgroundRunAttachesSessionFromOverrides(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: t.TempDir(),
		Out:       &out,
	}

	require.NoError(t, app.BackgroundWithOverrides([]string{"run", "echo", "attached"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"session_id": "session-1"`)
	out.Reset()

	require.NoError(t, app.BackgroundWithOverrides([]string{"list"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"session_id": "session-1"`)
}

func TestBackgroundHeartbeatAndBoardCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.RunWithOptions("sleep 5", workspace, background.RunOptions{Kind: "agent", SessionID: "session-1"})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = store.Stop(task.ID) })

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: workspace,
		Out:       &out,
	}

	observedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, app.BackgroundWithOverrides([]string{
		"heartbeat", task.ID,
		"--status", "running",
		"--observed-at", observedAt.Format(time.RFC3339),
		"--source-kind", "health",
		"--environment", "dogfood",
		"--channel", "terminal",
		"--emitter", "operator",
		"--confidence", "high",
	}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"heartbeat": {`)
	require.Contains(t, out.String(), `"transport_alive": true`)
	require.Contains(t, out.String(), `"source_kind": "healthcheck"`)
	require.Contains(t, out.String(), `"environment": "dogfood"`)
	out.Reset()

	require.NoError(t, app.BackgroundWithOverrides([]string{"board", "3600"}, config.FlagOverrides{}))
	var board background.LaneBoard
	require.NoError(t, json.Unmarshal(out.Bytes(), &board))
	require.Len(t, board.Active, 1)
	require.Equal(t, task.ID, board.Active[0].TaskID)
	require.Equal(t, background.LaneFreshnessHealthy, board.Active[0].Freshness)
	require.Equal(t, "healthcheck", board.Active[0].Provenance.SourceKind)
	require.Equal(t, "operator", board.Active[0].Provenance.Emitter)
}

func TestResumedBackgroundLifecycleActions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.RunWithOptions("sleep 30", workspace, background.RunOptions{SessionID: "resume-bg"})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = store.Stop(task.ID) })

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: workspace,
		Out:       &out,
	}
	overrides := config.FlagOverrides{Resume: "resume-bg"}

	require.NoError(t, app.runResumedBackgroundSlash([]string{"heartbeat", task.ID, "--status", "working", "--json"}, overrides, "json"))
	var report backgroundCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "heartbeat", report.Action)
	require.Equal(t, task.ID, report.TaskID)
	require.NotNil(t, report.Task)
	require.Equal(t, "working", report.Task.Heartbeat.Status)
	out.Reset()

	require.NoError(t, app.runResumedBackgroundSlash([]string{"restart", task.ID, "--json"}, overrides, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "restart", report.Action)
	require.NotNil(t, report.Task)
	require.Equal(t, task.ID, report.Task.RestartedFrom)
	restartedID := report.Task.ID
	t.Cleanup(func() { _, _ = store.Stop(restartedID) })
	out.Reset()

	require.NoError(t, app.runResumedBackgroundSlash([]string{"stop", restartedID, "--json"}, overrides, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "stop", report.Action)
	require.Equal(t, restartedID, report.TaskID)
	require.NotNil(t, report.Task)
	require.Equal(t, "stopped", report.Task.Status)
	out.Reset()

	failed, err := store.RunWithOptions("false", workspace, background.RunOptions{
		SessionID:     "resume-bg",
		RestartPolicy: &background.RestartPolicy{Enabled: true, Mode: "on-failure", MaxAttempts: 1},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		current, err := store.Status(failed.ID)
		return err == nil && current.Status == "failed"
	}, 2*time.Second, 50*time.Millisecond)

	require.NoError(t, app.runResumedBackgroundSlash([]string{"supervise", "--json"}, overrides, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "supervise", report.Action)
	require.NotNil(t, report.Supervise)
	require.Len(t, report.Supervise.Restarted, 1)
	require.Equal(t, failed.ID, report.Supervise.Restarted[0].RestartedFrom)
	out.Reset()

	require.NoError(t, app.runResumedBackgroundSlash([]string{"prune", "0", "0", "--json"}, overrides, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "prune", report.Action)
	require.NotNil(t, report.Prune)
}

func TestResumedBackgroundWatchRequiresBoundedEvents(t *testing.T) {
	configHome := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.Run("echo resumed-watch", t.TempDir())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(task.ID, 1024)
		return err == nil && strings.Contains(logs, "resumed-watch")
	}, 2*time.Second, 50*time.Millisecond)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	overrides := config.FlagOverrides{Resume: "resume-bg"}

	require.NoError(t, app.runResumedBackgroundSlash([]string{"watch", task.ID, "--max-events", "1", "--json"}, overrides, "json"))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 1)
	var event background.WatchEvent
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &event))
	require.Equal(t, "status", event.Type)
	require.Equal(t, task.ID, event.ID)
	out.Reset()

	err = app.runResumedBackgroundSlash([]string{"watch", task.ID, "--json"}, overrides, "json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires --max-events")
}

func TestResumedTeamWatchRequiresBoundedEvents(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	backgroundStore := background.NewStore(configHome)
	task, err := backgroundStore.Run("echo resumed-team-watch", workspace)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := backgroundStore.Logs(task.ID, 1024)
		return err == nil && strings.Contains(logs, "resumed-team-watch")
	}, 2*time.Second, 50*time.Millisecond)
	teamEntry, err := team.NewStore(configHome).Create("watchers", []team.TaskSpec{{Prompt: "watch logs", TaskID: task.ID}}, []string{task.ID})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
	}

	require.NoError(t, app.runResumedTeamSlash([]string{"watch", teamEntry.ID, "--max-events", "1", "--json"}, "json"))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 1)
	var event teamWatchEvent
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &event))
	require.Equal(t, "team_watch", event.Kind)
	require.Equal(t, teamEntry.ID, event.TeamID)
	require.Equal(t, task.ID, event.TaskID)
	out.Reset()

	err = app.runResumedTeamSlash([]string{"watch", teamEntry.ID, "--json"}, "json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires --max-events")
}

func TestBackgroundRunEmitsNotificationHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	received := make(chan struct {
		Event            string `json:"event"`
		Message          string `json:"message"`
		Title            string `json:"title"`
		NotificationType string `json:"notification_type"`
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var payload struct {
			Event            string `json:"event"`
			Message          string `json:"message"`
			Title            string `json:"title"`
			NotificationType string `json:"notification_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				NotificationCommands: []config.HookCommand{
					{Matcher: "background_task_started", Type: "http", URL: server.URL},
				},
			},
		},
		Sessions:  session.NewStore(configHome),
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.BackgroundWithOverrides([]string{"run", "echo", "attached"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"session_id": "session-1"`)
	select {
	case payload := <-received:
		require.Equal(t, "notification", payload.Event)
		require.Equal(t, "Background task started", payload.Title)
		require.Equal(t, "background_task_started", payload.NotificationType)
		require.Contains(t, payload.Message, "started")
		require.Contains(t, payload.Message, "echo attached")
	case <-time.After(2 * time.Second):
		t.Fatal("notification hook was not called")
	}
	require.Empty(t, errOut.String())
}

func TestBackgroundStopEmitsSubagentStopHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.RunWithOptions("printf 'final line\\n'; sleep 5", workspace, background.RunOptions{Kind: "agent", AgentType: "reviewer", SessionID: "session-1"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(task.ID, 4096)
		return err == nil && strings.Contains(logs, "final line")
	}, 2*time.Second, 50*time.Millisecond)
	received := make(chan struct {
		Event          string `json:"event"`
		AgentID        string `json:"agent_id"`
		AgentType      string `json:"agent_type"`
		TranscriptPath string `json:"agent_transcript_path"`
		LastAssistant  string `json:"last_assistant_message"`
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var payload struct {
			Event          string `json:"event"`
			AgentID        string `json:"agent_id"`
			AgentType      string `json:"agent_type"`
			TranscriptPath string `json:"agent_transcript_path"`
			LastAssistant  string `json:"last_assistant_message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				SubagentStopCommands: []config.HookCommand{
					{Matcher: "reviewer", Type: "http", URL: server.URL},
				},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.BackgroundWithOverrides([]string{"stop", task.ID}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"status": "stopped"`)
	select {
	case payload := <-received:
		require.Equal(t, "subagent_stop", payload.Event)
		require.Equal(t, task.ID, payload.AgentID)
		require.Equal(t, "reviewer", payload.AgentType)
		require.Equal(t, task.LogPath, payload.TranscriptPath)
		require.Equal(t, "final line", payload.LastAssistant)
	case <-time.After(2 * time.Second):
		t.Fatal("subagent stop hook was not called")
	}
	require.Empty(t, errOut.String())
}

func TestBackgroundSlashAliases(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.True(t, app.handleSlash(context.Background(), "/bashes list", &session.Session{ID: "session-1"}))
	require.Equal(t, "[]\n", out.String())
	require.Empty(t, errOut.String())
}

func TestCronCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:     config.Config{ConfigHome: configHome},
		Workspace:  workspace,
		Executable: script,
		Out:        &out,
		Err:        &errOut,
	}

	require.NoError(t, app.Cron([]string{"create", "0 9 * * 1", "review", "weekly", "--description", "weekly review", "--json"}))
	var created cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &created))
	require.Equal(t, "cron", created.Kind)
	require.Equal(t, "create", created.Action)
	require.NotNil(t, created.Entry)
	require.Equal(t, "0 9 * * 1", created.Entry.Schedule)
	require.Equal(t, "review weekly", created.Entry.Prompt)
	require.Equal(t, "weekly review", created.Entry.Description)
	out.Reset()

	require.NoError(t, app.Cron([]string{"list", "--json"}))
	var listed cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listed))
	require.Equal(t, 1, listed.Count)
	require.Len(t, listed.Entries, 1)
	require.Equal(t, created.Entry.ID, listed.Entries[0].ID)
	out.Reset()

	require.NoError(t, app.Cron([]string{"show", created.Entry.ID, "--json"}))
	var shown cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &shown))
	require.Equal(t, "show", shown.Action)
	require.Equal(t, created.Entry.ID, shown.Entry.ID)
	out.Reset()
	require.NoError(t, app.Cron([]string{"get", created.Entry.ID}))
	require.Contains(t, out.String(), "Action           show")
	require.Contains(t, out.String(), created.Entry.ID)
	out.Reset()

	require.NoError(t, app.Cron([]string{"disable", created.Entry.ID, "--json"}))
	var disabled cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &disabled))
	require.Equal(t, "disable", disabled.Action)
	require.False(t, disabled.Entry.Enabled)
	out.Reset()

	require.NoError(t, app.Cron([]string{"enable", created.Entry.ID, "--json"}))
	var enabled cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &enabled))
	require.Equal(t, "enable", enabled.Action)
	require.True(t, enabled.Entry.Enabled)
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--cwd", workspace, "--output-format", "json", "cron", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cliListed cronCommandReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliListed))
	require.Equal(t, "cron", cliListed.Kind)
	require.Equal(t, "list", cliListed.Action)
	require.Equal(t, 1, cliListed.Count)
	require.Equal(t, created.Entry.ID, cliListed.Entries[0].ID)

	require.True(t, app.handleSlash(context.Background(), "/cron list --json", &session.Session{ID: "session-1"}))
	require.Contains(t, out.String(), `"kind": "cron"`)
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.Cron([]string{"add", "@every 1h", "check", "due", "--json"}))
	var dueCreated cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &dueCreated))
	require.Equal(t, "create", dueCreated.Action)
	require.NotNil(t, dueCreated.Entry)
	out.Reset()

	now := "2026-06-30T09:30:00Z"
	require.NoError(t, app.Cron([]string{"due", "--now", now, "--json"}))
	var due cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &due))
	require.Equal(t, "due", due.Action)
	require.Equal(t, 1, due.Count)
	require.Equal(t, dueCreated.Entry.ID, due.Entries[0].ID)
	out.Reset()

	require.NoError(t, app.Cron([]string{"run", "--now", now, "--json"}))
	var runDue cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &runDue))
	require.Equal(t, "run-due", runDue.Action)
	require.Equal(t, 1, runDue.Count)
	require.Len(t, runDue.Tasks, 1)
	require.Len(t, runDue.Entries, 1)
	require.Equal(t, 1, runDue.Entries[0].RunCount)
	out.Reset()

	require.NoError(t, app.Cron([]string{"list", "--json"}))
	var afterRun cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &afterRun))
	require.Len(t, afterRun.Entries, 2)
	var ran cron.Entry
	for _, entry := range afterRun.Entries {
		if entry.ID == dueCreated.Entry.ID {
			ran = entry
		}
	}
	require.Equal(t, 1, ran.RunCount)
	require.NotNil(t, ran.LastRunAt)
	out.Reset()

	require.NoError(t, app.Cron([]string{"delete", created.Entry.ID, "--json"}))
	var deleted cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleted))
	require.Equal(t, "delete", deleted.Action)
	require.Equal(t, created.Entry.ID, deleted.Entry.ID)
	out.Reset()

	require.NoError(t, app.Cron([]string{"rm", dueCreated.Entry.ID, "--json"}))
	out.Reset()

	require.NoError(t, app.Cron([]string{"list", "--json"}))
	var empty cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &empty))
	require.Zero(t, empty.Count)
	require.Empty(t, empty.Entries)
}

func TestTeamCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho team-log \"$@\"\nsleep 5\n"), 0o755))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:     config.Config{ConfigHome: configHome},
		Workspace:  workspace,
		Executable: script,
		Out:        &out,
		Err:        &errOut,
	}

	require.NoError(t, app.Team([]string{"add", "reviewers", "--task", "auth=check auth", "--task", "check tests", "--json"}))
	var created teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &created))
	require.Equal(t, "team", created.Kind)
	require.Equal(t, "create", created.Action)
	require.NotNil(t, created.Team)
	require.Equal(t, "reviewers", created.Team.Name)
	require.Len(t, created.Team.Tasks, 2)
	require.NotEmpty(t, created.Team.Tasks[0].TaskID)
	require.Len(t, created.Tasks, 2)
	out.Reset()

	require.NoError(t, app.Team([]string{"stat", created.Team.ID, "--json"}))
	var status teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "status", status.Action)
	require.Equal(t, created.Team.ID, status.Team.ID)
	require.Len(t, status.Tasks, 2)
	require.Equal(t, "running", status.Team.Status)
	out.Reset()

	require.Eventually(t, func() bool {
		out.Reset()
		require.NoError(t, app.Team([]string{"log", created.Team.ID, "--bytes", "4096", "--json"}))
		return strings.Contains(out.String(), "team-log")
	}, 5*time.Second, 50*time.Millisecond)
	var logs teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &logs))
	require.Equal(t, "logs", logs.Action)
	require.Len(t, logs.Logs, 2)
	require.Contains(t, logs.Logs[0].Log+logs.Logs[1].Log, "team-log")
	out.Reset()

	require.NoError(t, app.Team([]string{"tail", created.Team.ID, "--max-events", "2", "--json"}))
	require.Contains(t, out.String(), `"kind":"team_watch"`)
	require.Contains(t, out.String(), `"type":"status"`)
	out.Reset()

	require.NoError(t, app.Team([]string{"list", "--json"}))
	var listed teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listed))
	require.Equal(t, 1, listed.Count)
	require.Equal(t, created.Team.ID, listed.Teams[0].ID)
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--cwd", workspace, "--output-format", "json", "team", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cliListed teamCommandReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliListed))
	require.Equal(t, "team", cliListed.Kind)
	require.Equal(t, "list", cliListed.Action)
	require.Equal(t, 1, cliListed.Count)
	require.Equal(t, created.Team.ID, cliListed.Teams[0].ID)

	require.True(t, app.handleSlash(context.Background(), "/team ls --json", &session.Session{ID: "session-1"}))
	require.Contains(t, out.String(), `"kind": "team"`)
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.Team([]string{"show", created.Team.ID, "--json"}))
	var fetched teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &fetched))
	require.Equal(t, created.Team.ID, fetched.Team.ID)
	out.Reset()

	require.NoError(t, app.Team([]string{"rm", created.Team.ID, "--json"}))
	var deleted teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleted))
	require.Equal(t, "delete", deleted.Action)
	require.Equal(t, "deleted", deleted.Team.Status)
	require.Len(t, deleted.StoppedTasks, 2)
}

func TestManagementCommandErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name     string
		args     []string
		command  string
		kind     string
		expected string
	}{
		{name: "hooks unknown action", args: []string{"hooks", "bogus"}, command: "hooks", kind: "unknown_action", expected: `"bogus"`},
		{name: "hooks typo action", args: []string{"hooks", "healt"}, command: "hooks", kind: "unknown_action", expected: "Did you mean `hooks health`?"},
		{name: "hooks watch paths typo action", args: []string{"hooks", "watch-paths", "chek"}, command: "hooks watch-paths", kind: "unknown_action", expected: "Did you mean `hooks watch-paths check`?"},
		{name: "cron unknown action", args: []string{"cron", "bogus"}, command: "cron", kind: "unknown_action", expected: `"bogus"`},
		{name: "cron typo action", args: []string{"cron", "creat"}, command: "cron", kind: "unknown_action", expected: "Did you mean `cron create`?"},
		{name: "cron list extra", args: []string{"cron", "list", "bogus"}, command: "cron list", kind: "unexpected_extra_args", expected: `"bogus"`},
		{name: "team unknown action", args: []string{"team", "bogus"}, command: "team", kind: "unknown_action", expected: `"bogus"`},
		{name: "team typo action", args: []string{"team", "statuz"}, command: "team", kind: "unknown_action", expected: "Did you mean one of: stat, status?"},
		{name: "team list extra", args: []string{"team", "list", "bogus"}, command: "team list", kind: "unexpected_extra_args", expected: `"bogus"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.kind)
			require.Contains(t, out, fmt.Sprintf(`"command": "%s"`, tc.command))
			require.Contains(t, out, tc.expected)
		})
	}
}

func TestWorkflowCommandErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name     string
		args     []string
		kind     string
		contains []string
	}{
		{
			name:     "share missing session",
			args:     []string{"share", "--session"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "share"`, `"option": "--session"`},
		},
		{
			name:     "copy missing format",
			args:     []string{"copy", "--format"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "copy"`, `"option": "--format"`},
		},
		{
			name:     "paste missing max bytes",
			args:     []string{"paste", "--max-bytes"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "paste"`, `"option": "--max-bytes"`},
		},
		{
			name:     "pin missing output format",
			args:     []string{"pin", "--output-format"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "pin"`, `"option": "--output-format"`},
		},
		{
			name:     "branch missing name",
			args:     []string{"branch", "create"},
			kind:     "missing_argument",
			contains: []string{`"command": "branch create"`, `"argument": "NAME"`},
		},
		{
			name:     "branch unknown option",
			args:     []string{"branch", "--bogus"},
			kind:     "unknown_option",
			contains: []string{`"command": "branch"`, `"option": "--bogus"`},
		},
		{
			name:     "tag invalid limit",
			args:     []string{"tag", "--limit", "many"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--limit"`, `"value": "many"`},
		},
		{
			name:     "tag missing name",
			args:     []string{"tag", "show"},
			kind:     "missing_argument",
			contains: []string{`"command": "tag show"`, `"argument": "NAME"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.kind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
			require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
		})
	}
}

func TestWorkflowCommandFallbackErrorsAreStructured(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "branch lock missing file", args: []string{"branch-lock", "--file"}},
		{name: "stale base missing base", args: []string{"stale-base", "--base-commit"}},
		{name: "green contract missing required level", args: []string{"green-contract", "--required-level"}},
		{name: "g004 missing input", args: []string{"g004-conformance", "--input"}},
		{name: "report schema missing consumer", args: []string{"report-schema", "--consumer"}},
		{name: "trust unknown flag", args: []string{"trust", "--bogus"}},
		{name: "stash unknown action", args: []string{"stash", "bogus"}},
		{name: "changelog bad count", args: []string{"changelog", "0"}},
		{name: "release notes bad format", args: []string{"release-notes", "--format", "xml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			require.Error(t, err)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 1, exitErr.Code)
			require.True(t, exitErr.Silent)
			var report cliErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "error", report.Status)
			require.NotEmpty(t, report.Kind)
			require.NotEmpty(t, report.ErrorKind)
			require.NotEmpty(t, report.Message)
			require.NotEmpty(t, report.Hint)
			require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
		})
	}
}

func TestLocalCommandErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name     string
		args     []string
		kind     string
		contains []string
	}{
		{
			name:     "run missing command",
			args:     []string{"run"},
			kind:     "missing_argument",
			contains: []string{`"command": "run"`, `"argument": "COMMAND"`},
		},
		{
			name:     "run missing timeout",
			args:     []string{"run", "--timeout-ms"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "run"`, `"option": "--timeout-ms"`},
		},
		{
			name:     "run invalid timeout",
			args:     []string{"run", "--timeout-ms", "soon"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--timeout-ms"`, `"value": "soon"`},
		},
		{
			name:     "node missing command",
			args:     []string{"node"},
			kind:     "missing_argument",
			contains: []string{`"command": "node"`, `"argument": "COMMAND"`},
		},
		{
			name:     "files missing path",
			args:     []string{"files", "--path"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "files"`, `"option": "--path"`},
		},
		{
			name:     "files extra path",
			args:     []string{"files", "one", "two"},
			kind:     "unexpected_extra_args",
			contains: []string{`"command": "files"`, `"two"`},
		},
		{
			name:     "search missing pattern",
			args:     []string{"search"},
			kind:     "missing_argument",
			contains: []string{`"command": "search"`, `"argument": "PATTERN"`},
		},
		{
			name:     "search invalid limit",
			args:     []string{"search", "--limit", "many", "needle"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--limit"`, `"value": "many"`},
		},
		{
			name:     "setup missing shell",
			args:     []string{"setup", "--shell"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "setup"`, `"option": "--shell"`},
		},
		{
			name:     "terminal setup missing path",
			args:     []string{"terminal-setup", "--path"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "terminal-setup"`, `"option": "--path"`},
		},
		{
			name:     "security review invalid limit",
			args:     []string{"security-review", "--limit", "many"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--limit"`, `"value": "many"`},
		},
		{
			name:     "bughunter extra scope",
			args:     []string{"bughunter", "pkg", "extra"},
			kind:     "unexpected_extra_args",
			contains: []string{`"command": "bughunter"`, `"extra"`},
		},
		{
			name:     "context viz missing output",
			args:     []string{"ctx_viz", "--output"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "ctx_viz"`, `"option": "--output"`},
		},
		{
			name:     "init verifiers missing target",
			args:     []string{"init-verifiers", "--target"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "init-verifiers"`, `"option": "--target"`},
		},
		{
			name:     "onboarding missing path",
			args:     []string{"onboarding", "--path"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "onboarding"`, `"option": "--path"`},
		},
		{
			name:     "status unknown option",
			args:     []string{"status", "--bogus"},
			kind:     "unknown_option",
			contains: []string{`"command": "status"`, `"option": "--bogus"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.kind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
			require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
		})
	}
}

func TestLongTailCommandErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name     string
		args     []string
		kind     string
		contains []string
	}{
		{
			name:     "remote env invalid lease",
			args:     []string{"remote-env", "--lease-seconds", "many"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--lease-seconds"`, `"value": "many"`},
		},
		{
			name:     "remote env typo action",
			args:     []string{"remote-env", "statuz"},
			kind:     "unknown_action",
			contains: []string{`"command": "remote-env"`, "Did you mean `remote-env status`?"},
		},
		{
			name:     "remote setup missing addr",
			args:     []string{"remote-setup", "--addr"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "remote-setup"`, `"option": "--addr"`},
		},
		{
			name:     "remote setup typo action",
			args:     []string{"remote-setup", "statuz"},
			kind:     "unknown_action",
			contains: []string{`"command": "remote-setup"`, "Did you mean `remote-setup status`?"},
		},
		{
			name:     "ide unknown option",
			args:     []string{"ide", "--bogus"},
			kind:     "unknown_option",
			contains: []string{`"command": "ide"`, `"option": "--bogus"`},
		},
		{
			name:     "bridge kick missing format",
			args:     []string{"bridge-kick", "--output-format"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "bridge-kick"`, `"option": "--output-format"`},
		},
		{
			name:     "completion missing limit",
			args:     []string{"completion", "--limit"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "completion"`, `"option": "--limit"`},
		},
		{
			name:     "map invalid depth",
			args:     []string{"map", "--depth", "many"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--depth"`, `"value": "many"`},
		},
		{
			name:     "hover missing context",
			args:     []string{"hover", "--context"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "hover"`, `"option": "--context"`},
		},
		{
			name:     "format invalid write",
			args:     []string{"format", "--write=maybe"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--write"`, `"value": "maybe"`},
		},
		{
			name:     "debug tool call missing json",
			args:     []string{"debug-tool-call", "read_file"},
			kind:     "missing_argument",
			contains: []string{`"command": "debug-tool-call"`, `"argument": "JSON"`},
		},
		{
			name:     "debug tool call invalid json",
			args:     []string{"debug-tool-call", "read_file", "{bad"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "JSON"`, `"debug-tool-call input must be valid JSON"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.kind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
			require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
		})
	}
}

func TestLongTailCommandFallbackErrorsAreStructured(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name     string
		args     []string
		hintPart string
	}{
		{name: "marketplace install missing path", args: []string{"marketplace", "install"}},
		{name: "oauth refresh unknown option", args: []string{"oauth-refresh", "--bogus"}},
		{name: "bridge unknown action", args: []string{"bridge", "bogus"}},
		{name: "mobile unknown flag", args: []string{"mobile", "--bogus"}},
		{name: "desktop unknown flag", args: []string{"desktop", "--bogus"}},
		{name: "code intel unknown command", args: []string{"code-intel", "symbls"}, hintPart: "Did you mean `codog code-intel symbols`?"},
		{name: "notebook read missing path", args: []string{"notebook-read"}},
		{name: "tool details missing name", args: []string{"tool-details"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			require.Error(t, err)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 1, exitErr.Code)
			require.True(t, exitErr.Silent)
			var report cliErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "error", report.Status)
			require.NotEmpty(t, report.Kind)
			require.NotEmpty(t, report.ErrorKind)
			require.NotEmpty(t, report.Message)
			require.NotEmpty(t, report.Hint)
			if tc.hintPart != "" {
				require.Contains(t, report.Hint, tc.hintPart)
			}
			require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
		})
	}
}

func TestRemainingCommandErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name     string
		args     []string
		kind     string
		contains []string
	}{
		{
			name:     "history invalid limit",
			args:     []string{"history", "--limit", "many"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--limit"`, `"value": "many"`},
		},
		{
			name:     "summary missing session",
			args:     []string{"summary", "--session"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "summary"`, `"option": "--session"`},
		},
		{
			name:     "rewind invalid messages",
			args:     []string{"rewind", "--messages", "0"},
			kind:     "invalid_flag_value",
			contains: []string{`"option": "--messages"`, `"value": "0"`},
		},
		{
			name:     "focus missing output format",
			args:     []string{"focus", "--output-format"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "focus"`, `"option": "--output-format"`},
		},
		{
			name:     "add dir remove missing path",
			args:     []string{"add-dir", "remove"},
			kind:     "missing_argument",
			contains: []string{`"command": "add-dir remove"`, `"argument": "PATH"`},
		},
		{
			name:     "scope unknown action",
			args:     []string{"scope", "previe"},
			kind:     "unknown_action",
			contains: []string{`"command": "codog scope"`, `"action": "previe"`, "Did you mean `codog scope preview`?"},
		},
		{
			name:     "validation missing output format",
			args:     []string{"validation", "--output-format"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "validation"`, `"option": "--output-format"`},
		},
		{
			name:     "workspace set missing path",
			args:     []string{"workspace", "set"},
			kind:     "missing_argument",
			contains: []string{`"command": "workspace set"`, `"argument": "PATH"`},
		},
		{
			name:     "allowed tools add missing tool",
			args:     []string{"allowed-tools", "add"},
			kind:     "missing_argument",
			contains: []string{`"command": "allowed-tools add"`, `"argument": "TOOL"`},
		},
		{
			name:     "allowed tools typo action",
			args:     []string{"allowed-tools", "ad"},
			kind:     "unknown_action",
			contains: []string{`"command": "allowed-tools"`, `"action": "ad"`, "Did you mean `allowed-tools add`?"},
		},
		{
			name:     "clear missing output format",
			args:     []string{"clear", "--output-format"},
			kind:     "missing_flag_value",
			contains: []string{`"command": "clear"`, `"option": "--output-format"`},
		},
		{
			name:     "plan set missing text",
			args:     []string{"plan", "set"},
			kind:     "missing_argument",
			contains: []string{`"command": "plan set"`, `"argument": "TEXT"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.kind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
			require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
		})
	}
}

func TestRemainingCommandFallbackErrorsAreStructured(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "resume extra argument", args: []string{"resume", "one", "two"}},
		{name: "conversation missing export format", args: []string{"conversation", "--format"}},
		{name: "generate session name missing source", args: []string{"generate-session-name", "--source"}},
		{name: "rename missing session", args: []string{"rename", "--session"}},
		{name: "todos unknown action", args: []string{"todos", "bogus"}},
		{name: "mcp remove missing server", args: []string{"mcp", "remove"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			require.Error(t, err)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 1, exitErr.Code)
			require.True(t, exitErr.Silent)
			var report cliErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "error", report.Status)
			require.NotEmpty(t, report.Kind)
			require.NotEmpty(t, report.ErrorKind)
			require.NotEmpty(t, report.Message)
			require.NotEmpty(t, report.Hint)
			require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
		})
	}
}

func TestParseBackgroundRunArgsWithRestartPolicy(t *testing.T) {
	command, options, err := parseBackgroundRunArgs([]string{
		"--restart=always",
		"--restart-limit", "2",
		"--restart-delay", "5",
		"--owner", "ops",
		"--workflow-scope", "infra-health",
		"--watcher-action", "observe",
		"echo", "restart",
	})
	require.NoError(t, err)
	require.Equal(t, "echo restart", command)
	require.NotNil(t, options.RestartPolicy)
	require.True(t, options.RestartPolicy.Enabled)
	require.Equal(t, "always", options.RestartPolicy.Mode)
	require.Equal(t, 2, options.RestartPolicy.MaxAttempts)
	require.Equal(t, 5, options.RestartPolicy.DelaySeconds)
	require.Equal(t, "ops", options.ScopeBinding.Owner)
	require.Equal(t, "infra-health", options.ScopeBinding.WorkflowScope)
	require.Equal(t, "observe", options.ScopeBinding.WatcherAction)

	_, _, err = parseBackgroundRunArgs([]string{"--restart=never", "echo"})
	require.Error(t, err)
}

func TestParseBackgroundRunArgsBoundaries(t *testing.T) {
	command, options, err := parseBackgroundRunArgs([]string{
		"--restart",
		"--restart-limit=0",
		"--restart-delay=1",
		"--owner=ops",
		"--scope=deploy",
		"--watcher-action=observe",
		"--",
		"echo", "--restart=never",
	})
	require.NoError(t, err)
	require.Equal(t, "echo --restart=never", command)
	require.Equal(t, "on-failure", options.RestartPolicy.Mode)
	require.Zero(t, options.RestartPolicy.MaxAttempts)
	require.Equal(t, 1, options.RestartPolicy.DelaySeconds)
	require.Equal(t, "ops", options.ScopeBinding.Owner)
	require.Equal(t, "deploy", options.ScopeBinding.WorkflowScope)
	require.Equal(t, "observe", options.ScopeBinding.WatcherAction)

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing command", args: nil},
		{name: "missing restart limit", args: []string{"--restart-limit"}},
		{name: "invalid restart limit", args: []string{"--restart-limit=bad", "echo"}},
		{name: "negative restart limit", args: []string{"--restart-limit=-1", "echo"}},
		{name: "missing restart delay", args: []string{"--restart-delay"}},
		{name: "negative restart delay", args: []string{"--restart-delay=-1", "echo"}},
		{name: "missing owner", args: []string{"--owner"}},
		{name: "missing scope", args: []string{"--workflow-scope"}},
		{name: "missing watcher action", args: []string{"--watcher-action"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseBackgroundRunArgs(test.args)
			require.Error(t, err)
		})
	}
}

func TestCodeIntelLSPCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: t.TempDir(),
		Out:       &out,
	}

	require.NoError(t, app.CodeIntel([]string{"lsp", "discover"}))
	var discover codeIntelLSPDiscoverReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &discover))
	require.Equal(t, "lsp_discover", discover.Kind)
	require.Equal(t, "discover", discover.Action)
	require.Equal(t, "ok", discover.Status)
	require.GreaterOrEqual(t, discover.Count, 1)
	require.NotEmpty(t, discover.Candidates)
	require.True(t, lspCandidateExists(discover.Candidates, "go", "gopls"))
	require.Contains(t, discover.Message, "PATH")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "discover", "--output-format", "text"}))
	require.Contains(t, out.String(), "LSP Discover")
	require.Contains(t, out.String(), "go")
	require.Contains(t, out.String(), "gopls")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "actions"}))
	var actions codeIntelLSPActionsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &actions))
	require.Equal(t, "lsp_actions", actions.Kind)
	require.Equal(t, "actions", actions.Action)
	require.Equal(t, "ok", actions.Status)
	require.GreaterOrEqual(t, actions.Count, 5)
	require.True(t, lspActionExists(actions.Actions, "definition", "textDocument/definition"))
	require.True(t, lspActionExists(actions.Actions, "references", "textDocument/references"))
	out.Reset()

	fakeCommand := "CODOG_AGENT_FAKE_LSP=1 " + shellQuote(os.Args[0]) + " -test.run '^TestACPFakeLSPServer$'"
	require.NoError(t, app.CodeIntel([]string{"lsp", "start", "go", "sh", "-c", fakeCommand}))
	require.Contains(t, out.String(), `"language": "go"`)
	require.Contains(t, out.String(), `"status": "ready"`)
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "list"}))
	var list codeIntelLSPListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &list))
	require.Equal(t, "lsp_list", list.Kind)
	require.Equal(t, "list", list.Action)
	require.Equal(t, "ok", list.Status)
	require.Equal(t, 1, list.Count)
	require.Len(t, list.Servers, 1)
	require.Equal(t, "go", list.Servers[0].Language)
	require.Contains(t, list.Message, "recorded")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "list", "--output-format=text"}))
	require.Contains(t, out.String(), "LSP Servers")
	require.Contains(t, out.String(), "go")
	require.Contains(t, out.String(), "ready")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "stop", "go"}))
	require.Contains(t, out.String(), `"status": "stopped"`)
}

func TestCodeIntelLSPQueryArgumentBoundaries(t *testing.T) {
	language, req, err := parseCodeIntelLSPQueryArgs([]string{
		"go", "rename", "main.go", "2", "3", "renamed",
		"--apply", "--action-title=Apply rename",
	})
	require.NoError(t, err)
	require.Equal(t, "go", language)
	require.Equal(t, "rename", req.Action)
	require.Equal(t, "main.go", req.Path)
	require.Equal(t, 2, req.Line)
	require.Equal(t, 3, req.Character)
	require.Equal(t, "renamed", req.NewName)
	require.Equal(t, "Apply rename", req.CodeActionTitle)
	require.True(t, req.Apply)

	_, req, err = parseCodeIntelLSPQueryArgs([]string{
		"go", "code-action", "main.go", "--write", "--preview", "--code-action-title", "Quick fix",
	})
	require.NoError(t, err)
	require.False(t, req.Apply)
	require.Equal(t, "Quick fix", req.CodeActionTitle)

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing query args", args: []string{"go", "rename"}},
		{name: "missing action title", args: []string{"go", "code-action", "main.go", "--action-title"}},
		{name: "invalid line", args: []string{"go", "definition", "main.go", "line"}},
		{name: "invalid character", args: []string{"go", "definition", "main.go", "1", "column"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseCodeIntelLSPQueryArgs(test.args)
			require.Error(t, err)
		})
	}
}

func TestCodeIntelLSPRouterErrorBoundaries(t *testing.T) {
	store := codeintel.NewLSPStore(t.TempDir(), t.TempDir())
	app := &App{}

	for _, test := range []struct {
		name   string
		action string
		args   []string
	}{
		{name: "start missing language", action: "start"},
		{name: "status missing language", action: "status"},
		{name: "query missing arguments", action: "query"},
		{name: "stop missing language", action: "stop"},
		{name: "unknown action", action: "statuz"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := app.codeIntelLSPPayload(&store, test.action, test.args)
			require.Error(t, err)
		})
	}

	payload, err := app.codeIntelLSPPayload(&store, "capabilities", nil)
	require.NoError(t, err)
	require.IsType(t, codeIntelLSPActionsReport{}, payload)
}

func TestResumedCodeIntelLSPStartAndStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: t.TempDir(),
		Out:       &out,
	}

	fakeCommand := "CODOG_AGENT_FAKE_LSP=1 " + shellQuote(os.Args[0]) + " -test.run '^TestACPFakeLSPServer$'"
	require.NoError(t, app.runResumedCodeIntelLSPSlash([]string{"start", "go", "sh", "-c", fakeCommand}, "json"))
	var started codeintel.LSPServerStatus
	require.NoError(t, json.Unmarshal(out.Bytes(), &started))
	require.Equal(t, "go", started.Language)
	require.Equal(t, "ready", started.Task.Status)
	require.Empty(t, started.TaskID)
	out.Reset()

	require.NoError(t, app.runResumedCodeIntelLSPSlash([]string{"list"}, "json"))
	require.Contains(t, out.String(), `"language": "go"`)
	require.Contains(t, out.String(), `"status": "ready"`)
	out.Reset()

	require.NoError(t, app.runResumedCodeIntelLSPSlash([]string{"stop", "go"}, "json"))
	var stopped codeintel.LSPServerStatus
	require.NoError(t, json.Unmarshal(out.Bytes(), &stopped))
	require.Equal(t, started.TaskID, stopped.TaskID)
	require.Equal(t, "stopped", stopped.Task.Status)
}

func lspActionExists(actions []codeintel.LSPActionInfo, name string, method string) bool {
	for _, action := range actions {
		if action.Name == name && action.Method == method {
			return true
		}
	}
	return false
}

func lspCandidateExists(candidates []codeintel.LSPCandidate, language string, command string) bool {
	for _, candidate := range candidates {
		if candidate.Language == language && candidate.Command == command {
			return true
		}
	}
	return false
}

func TestOAuthTokenCommands(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"token"}))
	require.Contains(t, out.String(), `"token_present": false`)
	require.Contains(t, out.String(), `"issue": "no oauth token saved"`)
	out.Reset()

	err := app.OAuth([]string{"token", "save", "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var saveError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &saveError))
	require.Equal(t, "oauth", saveError.Kind)
	require.Equal(t, "token_save", saveError.Action)
	require.Equal(t, "missing_argument", saveError.ErrorKind)
	require.Equal(t, "access_token", saveError.Argument)
	out.Reset()

	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	require.NoError(t, app.OAuth([]string{"token", "save", "access-token-1234", "refresh-token-1234", expiresAt}))
	require.Contains(t, out.String(), `"access_token": "acce...1234"`)
	require.NotContains(t, out.String(), "access-token-1234")

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "show"}))
	require.Contains(t, out.String(), `"expired": false`)

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "status"}))
	var tokenStatus oauth.Status
	require.NoError(t, json.Unmarshal(out.Bytes(), &tokenStatus))
	require.Equal(t, "oauth", tokenStatus.Kind)
	require.Equal(t, "status", tokenStatus.Action)
	require.Equal(t, "ok", tokenStatus.Status)
	require.True(t, tokenStatus.TokenPresent)
	require.Contains(t, out.String(), `"token_present": true`)
	require.Contains(t, out.String(), `"access_token": "acce...1234"`)
	require.NotContains(t, out.String(), "access-token-1234")

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "delete"}))
	var deleted oauthTokenDeleteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleted))
	require.Equal(t, "oauth_token", deleted.Kind)
	require.Equal(t, "delete", deleted.Action)
	require.Equal(t, "ok", deleted.Status)
	require.True(t, deleted.Deleted)
}

func TestOAuthErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "unknown oauth action",
			args:      []string{"oauth", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "oauth"`, `"bogus"`},
		},
		{
			name:      "unknown provider action",
			args:      []string{"oauth", "provider", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "oauth provider"`, `"bogus"`},
		},
		{
			name:      "unknown token action",
			args:      []string{"oauth", "token", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "oauth token"`, `"bogus"`},
		},
		{
			name:      "unknown device action",
			args:      []string{"oauth", "device", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "oauth device"`, `"bogus"`},
		},
		{
			name:      "unknown browser action",
			args:      []string{"oauth", "browser", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "oauth browser"`, `"bogus"`},
		},
		{
			name:      "missing token",
			args:      []string{"oauth", "token", "show"},
			kind:      "oauth_token_missing",
			errorKind: "oauth_token_missing",
			contains:  []string{`"kind": "oauth_token_missing"`},
		},
		{
			name:      "missing output format value",
			args:      []string{"oauth", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "oauth"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"oauth", "status", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestLoginLogoutAliases(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	err := app.Login(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth browser login PROFILE")

	err = app.Login([]string{"device"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth device")

	err = app.Login([]string{"--console"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth device")

	err = app.Login([]string{"--claudeai"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth browser login PROFILE")

	app.Config.ForceLoginMethod = "console"
	err = app.Login([]string{"--claudeai"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth device")

	app.Config.ForceLoginMethod = "claudeai"
	err = app.Login([]string{"--console"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth browser login PROFILE")

	err = app.Login([]string{"--console", "--claudeai"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be used together")

	out.Reset()
	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "access-token-1234"})
	require.NoError(t, err)
	require.NoError(t, app.Logout(nil))
	require.Contains(t, out.String(), `"deleted": true`)
	var logoutReport oauth.LogoutResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &logoutReport))
	require.Equal(t, "oauth", logoutReport.Kind)
	require.Equal(t, "logout", logoutReport.Action)
	require.Equal(t, "ok", logoutReport.Status)
	require.True(t, logoutReport.Deleted)
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)

	out.Reset()
	require.Empty(t, errOut.String())
	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "slash-access-1234"})
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/logout", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"deleted": true`)
	require.NoError(t, json.Unmarshal(out.Bytes(), &logoutReport))
	require.Equal(t, "oauth", logoutReport.Kind)
	require.Equal(t, "logout", logoutReport.Action)
	require.Equal(t, "ok", logoutReport.Status)
	require.True(t, logoutReport.Deleted)
	require.Empty(t, errOut.String())
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)
}

func TestOAuthDiscoverCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://auth.example/authorize","token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{Out: &out}
	err := app.OAuth([]string{"discover", "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var missing actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &missing))
	require.Equal(t, "oauth", missing.Kind)
	require.Equal(t, "discover", missing.Action)
	require.Equal(t, "missing_argument", missing.ErrorKind)
	require.Equal(t, "issuer_url", missing.Argument)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"discover", server.URL}))
	require.Contains(t, out.String(), `"authorization_endpoint": "https://auth.example/authorize"`)
	require.Contains(t, out.String(), `"token_endpoint": "https://auth.example/token"`)
}

func TestOAuthDeviceCommands(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"device_authorization_endpoint":"` + server.URL + `/device","token_endpoint":"` + server.URL + `/token"}`))
		case "/device":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			require.Equal(t, "profile", r.Form.Get("scope"))
			_, _ = w.Write([]byte(`{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"` + server.URL + `/verify","expires_in":600,"interval":1}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, oauth.DeviceCodeGrantType, r.Form.Get("grant_type"))
			require.Equal(t, "device-1", r.Form.Get("device_code"))
			_, _ = w.Write([]byte(`{"access_token":"device-access-1234","refresh_token":"device-refresh-1234","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", []string{"profile"})
	require.NoError(t, err)

	for _, tc := range []struct {
		args     []string
		action   string
		argument string
	}{
		{[]string{"device", "start", "--json"}, "device_start", "profile_or_issuer"},
		{[]string{"device", "start", server.URL, "--json"}, "device_start", "client_id"},
		{[]string{"device", "poll", "--json"}, "device_poll", "profile_or_issuer"},
		{[]string{"device", "poll", "default", "--json"}, "device_poll", "device_code"},
		{[]string{"device", "poll", server.URL, "--json"}, "device_poll", "client_id"},
		{[]string{"device", "poll", server.URL, "client-1", "--json"}, "device_poll", "device_code"},
		{[]string{"device", "login", "--json"}, "device_login", "profile_or_issuer"},
		{[]string{"device", "login", server.URL, "--json"}, "device_login", "client_id"},
	} {
		err := app.OAuth(tc.args)
		require.Error(t, err)
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		require.Equal(t, 1, exitErr.Code)
		require.True(t, exitErr.Silent)
		var report actionErrorReport
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		require.Equal(t, "oauth", report.Kind)
		require.Equal(t, tc.action, report.Action)
		require.Equal(t, "missing_argument", report.ErrorKind)
		require.Equal(t, tc.argument, report.Argument)
		out.Reset()
	}

	require.NoError(t, app.OAuth([]string{"device"}))
	require.Contains(t, out.String(), `"flow": "device"`)
	require.Contains(t, out.String(), `"profile_count": 1`)
	require.Contains(t, out.String(), `"ready_count": 1`)
	require.Contains(t, out.String(), `"device_authorization": "`+server.URL+`/device"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "status", "default"}))
	require.Contains(t, out.String(), `"flow": "device"`)
	require.Contains(t, out.String(), `"name": "default"`)
	require.Contains(t, out.String(), `"ready": true`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "start", server.URL, "client-1", "profile"}))
	require.Contains(t, out.String(), `"user_code": "ABCD-EFGH"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "start", "default"}))
	require.Contains(t, out.String(), `"user_code": "ABCD-EFGH"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "poll", server.URL, "client-1", "device-1"}))
	require.Contains(t, out.String(), `"access_token": "devi...1234"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "poll", "default", "device-1"}))
	require.Contains(t, out.String(), `"access_token": "devi...1234"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "device-access-1234", loaded.AccessToken)
}

func TestOAuthProviderCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://auth.example/authorize","token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: t.TempDir()},
		Out:    &out,
	}
	for _, tc := range []struct {
		args     []string
		action   string
		argument string
	}{
		{[]string{"provider", "save", "--json"}, "provider_save", "profile"},
		{[]string{"provider", "show", "--json"}, "provider_show", "profile"},
		{[]string{"provider", "delete", "--json"}, "provider_delete", "profile"},
	} {
		err := app.OAuth(tc.args)
		require.Error(t, err)
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		require.Equal(t, 1, exitErr.Code)
		require.True(t, exitErr.Silent)
		var report actionErrorReport
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		require.Equal(t, "oauth", report.Kind)
		require.Equal(t, tc.action, report.Action)
		require.Equal(t, "missing_argument", report.ErrorKind)
		require.Equal(t, tc.argument, report.Argument)
		out.Reset()
	}

	require.NoError(t, app.OAuth([]string{"provider", "save", "default", server.URL, "client-1", "profile"}))
	require.Contains(t, out.String(), `"name": "default"`)
	require.Contains(t, out.String(), `"client_id": "client-1"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "list"}))
	require.Contains(t, out.String(), `"name": "default"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider"}))
	require.Contains(t, out.String(), `"name": "default"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "show", "default"}))
	require.Contains(t, out.String(), `"token_endpoint": "https://auth.example/token"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "delete", "default"}))
	require.Contains(t, out.String(), `"deleted": true`)
}

func TestProfileCommandSetsShowsAndClearsActiveOAuthProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-default", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "work", server.URL, "client-work", []string{"profile", "email"})
	require.NoError(t, err)

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "profile", "set", "work", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "profile"`)
	require.Contains(t, out, `"action": "set"`)
	require.Contains(t, out, `"active_profile": "work"`)
	require.Contains(t, out, `"client_id": "client-work"`)
	var setProfile profileReport
	require.NoError(t, json.Unmarshal([]byte(out), &setProfile))
	require.True(t, setProfile.ActiveConfigured)
	require.Equal(t, "work", setProfile.ResolvedProfile)
	require.Equal(t, "active", setProfile.ResolvedSource)
	require.Equal(t, 2, setProfile.ProfileCount)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"oauth_profile": "work"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "profile", "show"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"active_profile": "work"`)
	require.Contains(t, out, `"name": "work"`)
	var showProfile profileReport
	require.NoError(t, json.Unmarshal([]byte(out), &showProfile))
	require.True(t, showProfile.ActiveConfigured)
	require.Equal(t, "work", showProfile.ResolvedProfile)
	require.Equal(t, "active", showProfile.ResolvedSource)
	require.Equal(t, 2, showProfile.ProfileCount)
	require.NotNil(t, showProfile.OAuthStatus)
	require.True(t, commandAcceptsGlobalOutputFormat("profile"))

	var buffer bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, OAuthProfile: "work"},
		Out:    &buffer,
		Err:    &errOut,
	}
	require.True(t, app.handleSlash(context.Background(), "/profile clear --path "+configPath, &session.Session{ID: "session"}))
	require.Contains(t, buffer.String(), "Profile")
	require.Empty(t, errOut.String())
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"oauth_profile"`)
}

func TestProvidersStatusRedactsAuth(t *testing.T) {
	configHome := t.TempDir()
	_, err := oauth.SaveToken(configHome, oauth.Token{AccessToken: "stored-access-token"})
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			BaseURL:    config.DefaultBaseURL,
			Model:      "claude-sonnet-4-5",
			MaxTokens:  4096,
			MaxTurns:   8,
			APIKey:     "api-key-secret",
			AuthToken:  "stored-access-token",
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"name": "anthropic"`)
	require.Contains(t, out.String(), `"name": "xai"`)
	require.Contains(t, out.String(), `"base_url": "https://api.x.ai/v1"`)
	require.Contains(t, out.String(), `"supports_extra_body_params": true`)
	require.Contains(t, out.String(), `"supports_stream_usage": true`)
	require.Contains(t, out.String(), `"honors_proxy_env": true`)
	require.Contains(t, out.String(), `"protected_extra_body_keys"`)
	require.Contains(t, out.String(), `"name": "dashscope"`)
	require.Contains(t, out.String(), `"base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1"`)
	require.Contains(t, out.String(), `"stored_oauth"`)
	require.Contains(t, out.String(), `"api_key": true`)
	require.NotContains(t, out.String(), "api-key-secret")
	require.NotContains(t, out.String(), "stored-access-token")
}

func TestProvidersDegradeOnMalformedConfigFile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "providers"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report providersReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "providers", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "degraded", report.Status)
	require.Equal(t, "anthropic", report.Active.Name)
	require.Equal(t, config.DefaultModel, report.Active.Model)
	require.NotNil(t, report.ConfigLoadError)
	require.Contains(t, *report.ConfigLoadError, "broken.json")
	require.Contains(t, *report.ConfigLoadError, "unexpected end of JSON input")
	require.Equal(t, "config_load_failed", report.ConfigLoadErrorKind)
	require.NotNil(t, report.Active.ConfigLoadError)
	require.Equal(t, report.ConfigLoadErrorKind, report.Active.ConfigLoadErrorKind)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/providers"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "degraded", report.Status)
	require.NotNil(t, report.ConfigLoadError)
	require.True(t, commandAcceptsGlobalOutputFormat("providers"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "providers", "show", "current"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var active activeProviderReport
	require.NoError(t, json.Unmarshal([]byte(out), &active))
	require.Equal(t, "anthropic", active.Name)
	require.NotNil(t, active.ConfigLoadError)
	require.Equal(t, "config_load_failed", active.ConfigLoadErrorKind)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "text", "providers"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "Status: degraded")
	require.Contains(t, out, "Config load: degraded")
	require.Contains(t, out, "broken.json")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "providers", "set", "openai"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var errorReport cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "config_load_failed", errorReport.ErrorKind)
	require.NotContains(t, out, "config_load_error")
}

func TestProvidersDegradeOnInvalidExtraBodyEnv(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	t.Setenv("CODOG_EXTRA_BODY", `{`)

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "providers"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report providersReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "providers", report.Kind)
	require.Equal(t, "degraded", report.Status)
	require.Equal(t, "invalid_extra_body", report.ConfigLoadErrorKind)
	require.NotNil(t, report.ConfigLoadError)
	require.Contains(t, *report.ConfigLoadError, "CODOG_EXTRA_BODY")
	require.Equal(t, config.DefaultModel, report.Active.Model)
	require.NotNil(t, report.Active.ConfigLoadError)
	require.Equal(t, "invalid_extra_body", report.Active.ConfigLoadErrorKind)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "providers", "set", "openai"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var errorReport cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "invalid_extra_body", errorReport.ErrorKind)
	require.Contains(t, errorReport.Hint, "CODOG_EXTRA_BODY")
}

func TestProvidersSetWritesConfig(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "provider.json")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			BaseURL:    config.DefaultBaseURL,
			Model:      config.DefaultModel,
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"set", "custom", "--base-url", "http://127.0.0.1:8080", "--model", "claude-local", "--path", configPath, "--json"}))
	require.Contains(t, out.String(), `"provider": "custom"`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"base_url": "http://127.0.0.1:8080"`)
	require.Contains(t, string(data), `"model": "claude-local"`)
	out.Reset()

	openAIPath := filepath.Join(configHome, "openai-provider.json")
	require.NoError(t, app.Providers([]string{"set", "openai", "--path", openAIPath, "--json"}))
	require.Contains(t, out.String(), `"provider": "openai"`)
	data, err = os.ReadFile(openAIPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"base_url": "https://api.openai.com/v1"`)
	require.Contains(t, string(data), `"model": "openai/gpt-4o-mini"`)
	out.Reset()

	xaiPath := filepath.Join(configHome, "xai-provider.json")
	require.NoError(t, app.Providers([]string{"set", "xai", "--path", xaiPath, "--json"}))
	require.Contains(t, out.String(), `"provider": "xai"`)
	data, err = os.ReadFile(xaiPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"base_url": "https://api.x.ai/v1"`)
	require.Contains(t, string(data), `"model": "grok"`)
	out.Reset()

	dashScopePath := filepath.Join(configHome, "dashscope-provider.json")
	require.NoError(t, app.Providers([]string{"set", "dashscope", "--path", dashScopePath, "--json"}))
	require.Contains(t, out.String(), `"provider": "dashscope"`)
	data, err = os.ReadFile(dashScopePath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1"`)
	require.Contains(t, string(data), `"model": "qwen-plus"`)
	out.Reset()

	kimiPath := filepath.Join(configHome, "kimi-provider.json")
	require.NoError(t, app.Providers([]string{"set", "kimi", "--path", kimiPath, "--json"}))
	require.Contains(t, out.String(), `"provider": "dashscope"`)
	data, err = os.ReadFile(kimiPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1"`)
	require.Contains(t, string(data), `"model": "kimi"`)
}

func TestProvidersShowCurrent(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:      t.TempDir(),
			BaseURL:         "https://provider.example",
			Model:           "claude-compatible",
			MaxTokens:       2048,
			MaxTurns:        4,
			ExtraBody:       map[string]any{"metadata": map[string]any{"source": "test"}},
			ReasoningEffort: "high",
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"show", "current", "--json"}))
	require.Contains(t, out.String(), `"name": "custom"`)
	require.Contains(t, out.String(), `"base_url": "https://provider.example"`)
	require.Contains(t, out.String(), `"model": "claude-compatible"`)
	require.Contains(t, out.String(), `"supports_extra_body_params": false`)
	var customActive activeProviderReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &customActive))
	require.ElementsMatch(t, []string{"metadata"}, customActive.ExtraBodyIgnoredKeys)
	require.Contains(t, providerDiagnosticCodes(customActive.Diagnostics), "reasoning_effort_unsupported")
	require.Contains(t, providerDiagnosticCodes(customActive.Diagnostics), "extra_body_keys_ignored")
	out.Reset()

	app.Config.BaseURL = "https://api.openai.com/v1"
	app.Config.Model = "openai/o4-mini"
	app.Config.ExtraBody = map[string]any{"parallel_tool_calls": false, "model": "bad-override"}
	temperature := 0.3
	app.Config.Temperature = &temperature
	require.NoError(t, app.Providers([]string{"show", "current", "--json"}))
	require.Contains(t, out.String(), `"name": "openai"`)
	require.Contains(t, out.String(), `"protocol": "openai-compatible"`)
	require.Contains(t, out.String(), `"model": "openai/o4-mini"`)
	require.Contains(t, out.String(), `"reasoning_model": true`)
	require.Contains(t, out.String(), `"strips_tuning_params": true`)
	require.Contains(t, out.String(), `"supports_stream_usage": true`)
	require.Contains(t, out.String(), `"preserves_reasoning_content_in_history": false`)
	require.Contains(t, out.String(), `"supports_extra_body_params": true`)
	require.Contains(t, out.String(), `"extra_body_configured": true`)
	require.Contains(t, out.String(), `"preserves_slash_model_ids_on_custom_base_url": true`)
	require.Contains(t, out.String(), `"tool_choice"`)
	var openAIActive activeProviderReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &openAIActive))
	require.ElementsMatch(t, []string{"model", "parallel_tool_calls"}, openAIActive.ExtraBodyKeys)
	require.ElementsMatch(t, []string{"parallel_tool_calls"}, openAIActive.ExtraBodyForwardedKeys)
	require.ElementsMatch(t, []string{"model"}, openAIActive.ExtraBodyIgnoredKeys)
	require.Contains(t, providerDiagnosticCodes(openAIActive.Diagnostics), "reasoning_model_fixed_sampling")
	require.Contains(t, providerDiagnosticCodes(openAIActive.Diagnostics), "extra_body_keys_ignored")
	require.NotContains(t, providerDiagnosticCodes(openAIActive.Diagnostics), "reasoning_effort_unsupported")
	out.Reset()

	app.Config.BaseURL = "https://api.x.ai/v1"
	app.Config.Model = "grok"
	app.Config.ExtraBody = nil
	app.Config.Temperature = nil
	app.Config.ReasoningEffort = ""
	require.NoError(t, app.Providers([]string{"show", "current", "--json"}))
	require.Contains(t, out.String(), `"name": "xai"`)
	require.Contains(t, out.String(), `"protocol": "openai-compatible"`)
	require.Contains(t, out.String(), `"model": "grok"`)
	require.Contains(t, out.String(), `"supports_extra_body_params": true`)
	require.Contains(t, out.String(), `"extra_body_configured": false`)
	require.Contains(t, out.String(), `"supports_stream_usage": false`)
	out.Reset()

	app.Config.BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	app.Config.Model = "qwen-plus"
	require.NoError(t, app.Providers([]string{"show", "current", "--json"}))
	require.Contains(t, out.String(), `"name": "dashscope"`)
	require.Contains(t, out.String(), `"protocol": "openai-compatible"`)
	require.Contains(t, out.String(), `"model": "qwen-plus"`)
	require.Contains(t, out.String(), `"supports_extra_body_params": true`)
	require.Contains(t, out.String(), `"supports_stream_usage": true`)
}

func providerDiagnosticCodes(diagnostics []providerDiagnosticReport) []string {
	codes := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	return codes
}

func TestRuntimeConfigErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "permissions invalid mode",
			args:      []string{"permissions", "workspce-write"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "mode"`, `"value": "workspce-write"`, "Did you mean `codog permissions set workspace-write`?"},
		},
		{
			name:      "permissions set invalid mode",
			args:      []string{"permissions", "set", "workspce-write"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "mode"`, `"value": "workspce-write"`, "Did you mean `codog permissions set workspace-write`?"},
		},
		{
			name:      "sandbox-toggle invalid strategy",
			args:      []string{"sandbox-toggle", "sandbx-exec"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "strategy"`, `"value": "sandbx-exec"`, `did you mean \"sandbox-exec\"?`},
		},
		{
			name:      "providers missing show name",
			args:      []string{"providers", "show"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "providers show"`, `"argument": "NAME"`},
		},
		{
			name:      "providers unknown provider",
			args:      []string{"providers", "opneai"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "provider"`, `"value": "opneai"`, "Did you mean `codog providers show openai`?"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestOAuthBrowserCommands(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"authorization_endpoint":"` + server.URL + `/authorize","token_endpoint":"` + server.URL + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "authorization_code", r.Form.Get("grant_type"))
			require.Equal(t, "code-1", r.Form.Get("code"))
			require.Equal(t, "verifier-1", r.Form.Get("code_verifier"))
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			_, _ = w.Write([]byte(`{"access_token":"browser-access-1234","refresh_token":"browser-refresh-1234","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", []string{"profile"})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	for _, tc := range []struct {
		args     []string
		action   string
		argument string
	}{
		{[]string{"browser", "start", "--json"}, "browser_start", "profile"},
		{[]string{"browser", "start", "default", "--json"}, "browser_start", "redirect_uri"},
		{[]string{"browser", "exchange", "--json"}, "browser_exchange", "profile"},
		{[]string{"browser", "exchange", "default", "--json"}, "browser_exchange", "code"},
		{[]string{"browser", "exchange", "default", "code-1", "--json"}, "browser_exchange", "code_verifier"},
		{[]string{"browser", "exchange", "default", "code-1", "verifier-1", "--json"}, "browser_exchange", "redirect_uri"},
		{[]string{"browser", "login", "--json"}, "browser_login", "profile"},
	} {
		err := app.OAuth(tc.args)
		require.Error(t, err)
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		require.Equal(t, 1, exitErr.Code)
		require.True(t, exitErr.Silent)
		var report actionErrorReport
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		require.Equal(t, "oauth", report.Kind)
		require.Equal(t, tc.action, report.Action)
		require.Equal(t, "missing_argument", report.ErrorKind)
		require.Equal(t, tc.argument, report.Argument)
		out.Reset()
	}

	require.NoError(t, app.OAuth([]string{"browser"}))
	require.Contains(t, out.String(), `"flow": "browser"`)
	require.Contains(t, out.String(), `"profile_count": 1`)
	require.Contains(t, out.String(), `"ready_count": 1`)
	require.Contains(t, out.String(), `"authorization": "`+server.URL+`/authorize"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"browser", "status", "default"}))
	require.Contains(t, out.String(), `"flow": "browser"`)
	require.Contains(t, out.String(), `"name": "default"`)
	require.Contains(t, out.String(), `"ready": true`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"browser", "start", "default", "http://127.0.0.1:9999/oauth/callback"}))
	require.Contains(t, out.String(), `"authorization_url":`)
	require.Contains(t, out.String(), "client_id=client-1")
	require.Contains(t, out.String(), "scope=profile")
	require.Contains(t, out.String(), `"code_verifier":`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"browser", "exchange", "default", "code-1", "verifier-1", "http://127.0.0.1:9999/oauth/callback"}))
	require.Contains(t, out.String(), `"access_token": "brow...1234"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "browser-access-1234", loaded.AccessToken)
}

func TestOAuthTokenRefreshCommand(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"token", "refresh"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "refreshed-access", loaded.AccessToken)
	out.Reset()

	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, app.OAuthRefresh([]string{"--json"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
	out.Reset()

	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/oauth-refresh", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
}

func TestOAuthStatusCommand(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "status-access-1234",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"status"}))
	var statusReport oauth.Status
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusReport))
	require.Equal(t, "oauth", statusReport.Kind)
	require.Equal(t, "status", statusReport.Action)
	require.Equal(t, "ok", statusReport.Status)
	require.Contains(t, out.String(), `"profile_name": "default"`)
	require.Contains(t, out.String(), `"access_token": "stat...1234"`)
	require.Contains(t, out.String(), `"can_refresh": true`)
	require.Contains(t, out.String(), `"ready": true`)
	require.NotContains(t, out.String(), "status-access-1234")

	out.Reset()
	require.NoError(t, app.OAuth([]string{"status", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusReport))
	require.Equal(t, "oauth", statusReport.Kind)
	require.Equal(t, "status", statusReport.Action)
	require.Equal(t, "ok", statusReport.Status)
	require.Contains(t, out.String(), `"profile_name": "default"`)
	require.NotContains(t, out.String(), "status-access-1234")

	out.Reset()
	require.True(t, app.handleSlash(context.Background(), "/oauth status --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusReport))
	require.Equal(t, "oauth", statusReport.Kind)
	require.Equal(t, "status", statusReport.Action)
	require.Equal(t, "ok", statusReport.Status)
	require.Contains(t, out.String(), `"profile_name": "default"`)
	require.NotContains(t, out.String(), "status-access-1234")
}

func TestOAuthTokenRevokeAndLogoutCommands(t *testing.T) {
	server, revoked := oauthRevocationTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "access-1", RefreshToken: "refresh-1"})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"token", "revoke", "default", "refresh"}))
	var revokeResult oauthTokenRevokeReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &revokeResult))
	require.Equal(t, "oauth_token", revokeResult.Kind)
	require.Equal(t, "revoke", revokeResult.Action)
	require.Equal(t, "ok", revokeResult.Status)
	require.True(t, revokeResult.Revoked)
	require.Equal(t, "default", revokeResult.Profile)
	require.Equal(t, "refresh", revokeResult.Token)
	require.Contains(t, *revoked, "refresh_token:refresh-1")
	out.Reset()

	require.NoError(t, app.OAuth([]string{"logout"}))
	require.Contains(t, out.String(), `"deleted": true`)
	require.Contains(t, out.String(), `"access_revoked": true`)
	var logoutResult oauth.LogoutResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &logoutResult))
	require.Equal(t, "oauth", logoutResult.Kind)
	require.Equal(t, "logout", logoutResult.Action)
	require.Equal(t, "ok", logoutResult.Status)
	require.True(t, logoutResult.Deleted)
	require.Contains(t, *revoked, "access_token:access-1")
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)
}

func TestApplyStoredOAuthToken(t *testing.T) {
	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveToken(configHome, oauth.Token{
		AccessToken: "stored-token",
		ExpiresAt:   now.Add(time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "stored-token", cfg.AuthToken)

	cfg = config.Config{ConfigHome: configHome, AuthToken: "explicit-token"}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "explicit-token", cfg.AuthToken)
}

func TestApplyStoredOAuthTokenRefreshesExpiredToken(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(-time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "refreshed-access", cfg.AuthToken)
}

func TestApplyStoredOAuthTokenUsesSelectedProfile(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"token_endpoint":"` + server.URL + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			switch r.Form.Get("client_id") {
			case "client-work":
				_, _ = w.Write([]byte(`{"access_token":"work-access","refresh_token":"refresh-2","expires_in":3600}`))
			default:
				_, _ = w.Write([]byte(`{"access_token":"default-access","refresh_token":"refresh-2","expires_in":3600}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-default", nil)
	require.NoError(t, err)
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "work", server.URL, "client-work", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(-time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome, OAuthProfile: "work"}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "work-access", cfg.AuthToken)
}

func oauthRefreshTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"token_endpoint":"` + server.URL + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			require.Equal(t, "refresh-1", r.Form.Get("refresh_token"))
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			_, _ = w.Write([]byte(`{"access_token":"refreshed-access","refresh_token":"refresh-2","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func oauthRevocationTestServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	revoked := []string{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"revocation_endpoint":"` + server.URL + `/revoke","token_endpoint":"` + server.URL + `/token"}`))
		case "/revoke":
			require.NoError(t, r.ParseForm())
			revoked = append(revoked, r.Form.Get("token_type_hint")+":"+r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	return server, &revoked
}

func TestMarketplaceAcceptsOutputFormatFlags(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","name":"Demo","lifecycle":{"init":["./bin/init"],"shutdown":["./bin/stop"]}}`), 0o644))

	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out}
	require.NoError(t, app.Marketplace(nil))
	require.True(t, strings.HasPrefix(strings.TrimSpace(out.String()), "["))
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"--output-format", "json", "list"}))
	var report pluginsListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "plugin", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.Summary.Total)
	require.Equal(t, 1, report.Summary.Enabled)
	require.Equal(t, 0, report.Summary.Disabled)
	require.Equal(t, 1, report.Summary.LifecycleConfigured)
	require.Nil(t, report.ConfigLoadError)
	require.Empty(t, report.LoadFailures)
	require.Equal(t, "demo", report.Plugins[0].ID)
	require.Equal(t, []string{"./bin/init"}, report.Plugins[0].Lifecycle.Init)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"list", "--json"}))
	require.Contains(t, out.String(), `"summary"`)
}

func TestMarketplacePluginHealthReportsLifecycle(t *testing.T) {
	workspace := t.TempDir()
	writePluginManifest := func(id string, body string) {
		t.Helper()
		dir := filepath.Join(workspace, ".codog", "plugins", id)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(body), 0o644))
	}
	writePluginManifest("healthy", `{
		"id":"healthy",
		"name":"healthy",
		"version":"1.0.0",
		"description":"Healthy plugin",
		"lifecycle":{"init":["./bin/init"],"shutdown":["./bin/stop"]},
		"tools":[{"name":"healthy_tool","command":"echo ok","permission":"read-only"}]
	}`)
	writePluginManifest("degraded", `{
		"id":"degraded",
		"name":"degraded",
		"version":"1.0.0",
		"description":"Degraded plugin",
		"tools":[{"name":"degraded_tool","command":"echo ok","permission":"read-only"}],
		"mcp_servers":{"broken":{}}
	}`)
	writePluginManifest("failed", `{
		"id":"failed",
		"name":"failed",
		"version":"1.0.0",
		"description":"Failed plugin",
		"commands":[""]
	}`)

	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}
	require.NoError(t, app.Marketplace([]string{"health", "--json"}))
	var report pluginHealthReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "plugin_health", report.Kind)
	require.Equal(t, "health", report.Action)
	require.Equal(t, "failed", report.Status)
	require.Equal(t, 3, report.Total)
	require.Equal(t, 1, report.Healthy)
	require.Equal(t, 1, report.Degraded)
	require.Equal(t, 1, report.Failed)

	healthy := pluginHealthcheckByID(report.Plugins, "healthy")
	require.NotNil(t, healthy)
	require.Equal(t, "healthy", healthy.State)
	require.Equal(t, "ready", healthy.LifecycleState)
	require.True(t, healthy.Lifecycle.Configured)
	require.True(t, healthy.Lifecycle.Init.Configured)
	require.Equal(t, 1, healthy.Lifecycle.Init.CommandCount)
	require.True(t, healthy.Lifecycle.Shutdown.Configured)
	require.Equal(t, 1, healthy.Lifecycle.Shutdown.CommandCount)
	require.Equal(t, "startup_healthy", healthy.StartupEvent)
	require.Contains(t, healthy.Available, "tool:healthy_tool")
	require.Contains(t, healthy.Available, "lifecycle:init")
	require.Contains(t, healthy.Available, "lifecycle:shutdown")

	degraded := pluginHealthcheckByID(report.Plugins, "degraded")
	require.NotNil(t, degraded)
	require.Equal(t, "degraded", degraded.State)
	require.Equal(t, "startup_degraded", degraded.StartupEvent)
	require.NotNil(t, degraded.DegradedMode)
	require.Contains(t, degraded.Available, "tool:degraded_tool")
	require.Contains(t, degraded.Unavailable, "mcp:broken")

	failed := pluginHealthcheckByID(report.Plugins, "failed")
	require.NotNil(t, failed)
	require.Equal(t, "failed", failed.State)
	require.Equal(t, "startup_failed", failed.StartupEvent)
	require.NotEmpty(t, failed.Errors)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"lifecycle", "--json"}))
	var lifecycle pluginHealthReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &lifecycle))
	require.Equal(t, "lifecycle", lifecycle.Action)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"health"}))
	require.Contains(t, out.String(), "Plugin Health")
	require.Contains(t, out.String(), "startup_degraded")
	require.Contains(t, out.String(), "lifecycle=ready")
}

func TestMarketplacePluginLifecycleRunExecutesConfiguredCommand(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".codog", "plugins", "runner")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{
		"id":"runner",
		"name":"runner",
		"version":"1.0.0",
		"lifecycle":{"init":["echo init-ok > lifecycle.txt"],"shutdown":["echo shutdown-ok > shutdown.txt"]}
	}`), 0o644))

	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Marketplace([]string{"lifecycle", "run", "init", "runner", "--json"}))
	var report pluginLifecycleRunReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "plugin_lifecycle", report.Kind)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "init", report.Phase)
	require.Equal(t, "runner", report.PluginID)
	require.Len(t, report.Results, 1)
	require.Equal(t, "ok", report.Results[0].Status)
	require.Len(t, report.Results[0].Commands, 1)
	require.Equal(t, 0, report.Results[0].Commands[0].ExitCode)
	contents, err := os.ReadFile(filepath.Join(root, "lifecycle.txt"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "init-ok")
}

func TestMarketplacePluginLifecycleRunReportsCommandFailure(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".codog", "plugins", "runner")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{
		"id":"runner",
		"name":"runner",
		"version":"1.0.0",
		"lifecycle":{"init":["exit 7"]}
	}`), 0o644))

	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	err := app.Marketplace([]string{"lifecycle", "run", "init", "runner", "--json"})
	require.Error(t, err)
	var report pluginLifecycleRunReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "failed", report.Status)
	require.Len(t, report.Results, 1)
	require.Equal(t, "failed", report.Results[0].Status)
	require.Len(t, report.Results[0].Commands, 1)
	require.Equal(t, 7, report.Results[0].Commands[0].ExitCode)
}

func TestMarketplacePluginLifecycleRunSuggestsKnownPhase(t *testing.T) {
	var out bytes.Buffer
	app := &App{Workspace: t.TempDir(), Out: &out, Err: io.Discard}

	err := app.Marketplace([]string{"lifecycle", "run", "strtup", "--json"})
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported lifecycle phase "strtup"`)
	require.Contains(t, err.Error(), `did you mean "startup"`)
	require.Contains(t, out.String(), `"status": "error"`)
	require.Contains(t, out.String(), `unsupported lifecycle phase \"strtup\"`)
	require.Contains(t, out.String(), `did you mean \"startup\"`)
}

func pluginHealthcheckByID(checks []pluginHealthcheck, id string) *pluginHealthcheck {
	for index := range checks {
		if checks[index].PluginID == id {
			return &checks[index]
		}
	}
	return nil
}

func TestMarketplaceSourcesManageConfigAndBrowse(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	index := plugins.MarketplaceIndex{
		Name: "Test Marketplace",
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", Name: "Demo", Version: "0.1.0", URL: "demo.zip", SHA256: strings.Repeat("a", 64)},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/index.json", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(index))
	}))
	defer server.Close()
	indexURL := server.URL + "/index.json"

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"sources", "add", indexURL, "public-key", "--target", "project", "--json"}))
	var addReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &addReport))
	require.Equal(t, "marketplace", addReport.Kind)
	require.Equal(t, "sources_add", addReport.Action)
	require.Equal(t, "ok", addReport.Status)
	require.Equal(t, "project", addReport.Target)
	require.True(t, addReport.Added)
	require.Equal(t, indexURL, addReport.URL)
	require.Equal(t, filepath.Join(workspace, ".codog.json"), addReport.Path)
	require.Len(t, addReport.Sources, 1)
	require.True(t, addReport.Sources[0].PublicKeyConfigured)
	require.Equal(t, []string{indexURL}, app.Config.Future.PluginMarketplaces)
	require.Equal(t, "public-key", app.Config.Future.PluginMarketplaceKeys[indexURL])
	configData, err := os.ReadFile(filepath.Join(workspace, ".codog.json"))
	require.NoError(t, err)
	require.Contains(t, string(configData), `"marketplace"`)
	require.Contains(t, string(configData), `"sources"`)
	require.Contains(t, string(configData), `"public_keys"`)
	require.NotContains(t, string(configData), "plugin_marketplaces")
	require.Contains(t, string(configData), indexURL)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"sources", "--json"}))
	var listReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "sources_list", listReport.Action)
	require.Len(t, listReport.Sources, 1)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"settings", "--json"}))
	var settingsReport marketplaceSettingsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &settingsReport))
	require.Equal(t, "settings", settingsReport.Action)
	require.Equal(t, plugins.Root(workspace), settingsReport.PluginRoot)
	require.Len(t, settingsReport.Sources, 1)
	out.Reset()

	app.Config.Future.PluginMarketplaceKeys = nil
	require.NoError(t, app.Marketplace([]string{"remote", "--json"}))
	var remoteList marketplaceRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &remoteList))
	require.Equal(t, "marketplace", remoteList.Kind)
	require.Equal(t, "remote_list", remoteList.Action)
	require.Equal(t, "ok", remoteList.Status)
	require.Equal(t, 1, remoteList.Total)
	require.Len(t, remoteList.Plugins, 1)
	require.Equal(t, "demo", remoteList.Plugins[0].ID)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"browse"}))
	var indexes []plugins.MarketplaceIndex
	require.NoError(t, json.Unmarshal(out.Bytes(), &indexes))
	require.Len(t, indexes, 1)
	require.Equal(t, "demo", indexes[0].Plugins[0].ID)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"remote", "search", "demo", "--per-page", "1", "--json"}))
	var remoteSearch marketplaceRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &remoteSearch))
	require.Equal(t, "marketplace", remoteSearch.Kind)
	require.Equal(t, "remote_search", remoteSearch.Action)
	require.Equal(t, "ok", remoteSearch.Status)
	require.Equal(t, "demo", remoteSearch.Query)
	require.Equal(t, 1, remoteSearch.Total)
	require.Len(t, remoteSearch.Plugins, 1)
	require.Equal(t, "demo", remoteSearch.Plugins[0].ID)
	require.Equal(t, indexURL, remoteSearch.Plugins[0].MarketplaceURL)
	require.Equal(t, server.URL+"/demo.zip", remoteSearch.Plugins[0].ResolvedURL)
	require.Equal(t, "codog marketplace install-remote demo", remoteSearch.Plugins[0].InstallCommand)
	require.NotNil(t, remoteSearch.Pagination)
	require.Equal(t, 1, remoteSearch.Pagination.PerPage)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"remote", "show", "demo", "--json"}))
	var remoteShow marketplaceRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &remoteShow))
	require.Equal(t, "remote_show", remoteShow.Action)
	require.NotNil(t, remoteShow.Plugin)
	require.Equal(t, "demo", remoteShow.Plugin.ID)
	require.Empty(t, remoteShow.Plugins)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"sources", "remove", indexURL, "--target", "project", "--json"}))
	var removeReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &removeReport))
	require.True(t, removeReport.Removed)
	require.Empty(t, removeReport.Sources)
	require.Empty(t, app.Config.Future.PluginMarketplaces)
}

func TestMarketplaceRemoteWithoutSourcesReportsStatus(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: t.TempDir(),
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"remote", "--json"}))
	var report marketplaceRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "marketplace", report.Kind)
	require.Equal(t, "remote_list", report.Action)
	require.Equal(t, "needs_source", report.Status)
	require.Equal(t, "No marketplace sources are configured.", report.Message)
	require.Equal(t, "codog marketplace sources add URL [PUBLIC_KEY]", report.NextCommand)
	require.Empty(t, report.Sources)
	require.Empty(t, report.Plugins)
	require.Zero(t, report.Total)
	require.NotNil(t, report.Pagination)
}

func TestMarketplaceCommands(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.json")
	index := plugins.MarketplaceIndex{
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", URL: "demo.zip", SHA256: strings.Repeat("b", 64)},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/index.json", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(index))
	}))
	defer server.Close()
	indexURL := server.URL + "/index.json"

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "marketplace", "sources", "add", indexURL, "--path", configPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var addReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal([]byte(out), &addReport))
	require.Equal(t, "sources_add", addReport.Action)
	require.True(t, addReport.Added)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "marketplace", "sources"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var listReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal([]byte(out), &listReport))
	require.Equal(t, "sources_list", listReport.Action)
	require.Len(t, listReport.Sources, 1)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "marketplace", "browse"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var indexes []plugins.MarketplaceIndex
	require.NoError(t, json.Unmarshal([]byte(out), &indexes))
	require.Equal(t, "demo", indexes[0].Plugins[0].ID)

	pluginDir := filepath.Join(workspace, "source-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"source-plugin","name":"source-plugin","tools":[{"name":"source_tool","command":"echo ok","permission":"read-only"}]}`), 0o644))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "marketplace", "validate", pluginDir}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var validation pluginValidationReport
	require.NoError(t, json.Unmarshal([]byte(out), &validation))
	require.Equal(t, "validate", validation.Action)
	require.Equal(t, "ok", validation.Status)
	require.True(t, validation.Success)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "marketplace", "settings"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var settings marketplaceSettingsReport
	require.NoError(t, json.Unmarshal([]byte(out), &settings))
	require.Equal(t, "settings", settings.Action)
	require.Len(t, settings.Sources, 1)
}

func TestMarketplaceDisableSkipsPluginToolRegistration(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat"}]}`), 0o644))

	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"disable", "demo"}))
	require.Contains(t, out.String(), `"enabled": false`)

	require.NoError(t, app.RegisterPluginTools())
	require.False(t, app.Tools.Has("demo_tool"))
}

func TestRegisterPluginToolsRejectsUnknownPermission(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat","permission":"root"}]}`), 0o644))

	app := &App{
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Out:       io.Discard,
	}
	err := app.RegisterPluginTools()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported permission")
	require.False(t, app.Tools.Has("demo_tool"))
}

func TestReloadPluginsRebuildsCurrentToolRegistry(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat","permission":"read-only"}]}`), 0o644))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(t.TempDir(), workspace),
		Out:       &out,
		Err:       &errOut,
	}
	require.False(t, app.Tools.Has("demo_tool"))

	require.NoError(t, app.ReloadPlugins([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "reload_plugins"`)
	require.Contains(t, out.String(), `"plugins": 1`)
	require.Contains(t, out.String(), `"plugin_tools": 1`)
	require.True(t, app.Tools.Has("demo_tool"))
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": app.Config.ConfigHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--cwd", workspace, "--output-format", "json", "reload-plugins"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cliReport reloadPluginsReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliReport))
	require.Equal(t, "reload_plugins", cliReport.Kind)
	require.Equal(t, 1, cliReport.Plugins)
	require.Equal(t, 1, cliReport.PluginTools)

	require.NoError(t, app.ReloadPlugins(nil))
	require.Contains(t, out.String(), "Plugins Reloaded")
	require.True(t, app.Tools.Has("demo_tool"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/reload-plugins", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Plugins Reloaded")
	require.Empty(t, errOut.String())
}

func TestMarketplaceInstallRemoteCommandUsesConfiguredMarketplace(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	workspace := t.TempDir()
	archive := makeAgentPluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo","version":"0.1.0"}`,
		"demo/tool.sh":     "echo ok\n",
	})
	sum := sha256.Sum256(archive)
	index := plugins.MarketplaceIndex{
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", URL: "demo.zip", SHA256: hex.EncodeToString(sum[:])},
		},
	}
	payload, err := json.Marshal(index)
	require.NoError(t, err)
	index.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			require.NoError(t, json.NewEncoder(w).Encode(index))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	indexURL := server.URL + "/index.json"
	app := &App{
		Config: config.Config{Future: config.FutureConfig{
			PluginMarketplaces:    []string{indexURL},
			PluginMarketplaceKeys: map[string]string{indexURL: base64.StdEncoding.EncodeToString(publicKey)},
		}},
		Workspace: workspace,
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"install-remote", "demo"}))
	require.Contains(t, out.String(), `"checksum_valid": true`)
	require.Contains(t, out.String(), `"signature_valid": true`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "plugins", "demo", "tool.sh"))
}

func TestMarketplaceUpdateCommandUsesConfiguredMarketplace(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","name":"Demo","version":"0.1.0"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tool.sh"), []byte("echo old\n"), 0o755))
	archive := makeAgentPluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo","version":"0.2.0"}`,
		"demo/tool.sh":     "echo new\n",
	})
	sum := sha256.Sum256(archive)
	index := plugins.MarketplaceIndex{
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", URL: "demo.zip", Version: "0.2.0", SHA256: hex.EncodeToString(sum[:])},
		},
	}
	payload, err := json.Marshal(index)
	require.NoError(t, err)
	index.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			require.NoError(t, json.NewEncoder(w).Encode(index))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	indexURL := server.URL + "/index.json"
	app := &App{
		Config: config.Config{Future: config.FutureConfig{
			PluginMarketplaces:    []string{indexURL},
			PluginMarketplaceKeys: map[string]string{indexURL: base64.StdEncoding.EncodeToString(publicKey)},
		}},
		Workspace: workspace,
	}

	var out bytes.Buffer
	app.Out = &out
	require.NoError(t, app.Marketplace([]string{"updates"}))
	require.Contains(t, out.String(), `"latest_version": "0.2.0"`)

	out.Reset()
	require.NoError(t, app.Marketplace([]string{"update", "demo"}))
	require.Contains(t, out.String(), `"updated": true`)
	require.Contains(t, out.String(), `"signature_valid": true`)
	data, err := os.ReadFile(filepath.Join(dir, "tool.sh"))
	require.NoError(t, err)
	require.Equal(t, "echo new\n", string(data))
}

func TestUpdaterInstallAndRollbackCommands(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "codog-new")
	target := filepath.Join(dir, "codog")
	require.NoError(t, os.WriteFile(artifact, []byte("new"), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.Updater(context.Background(), []string{"install", artifact, target}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "new", string(data))

	out.Reset()
	require.NoError(t, app.Updater(context.Background(), []string{"rollback", target}))
	require.Contains(t, out.String(), `"rolled_back": true`)
	data, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))

	aliasArtifact := filepath.Join(dir, "codog-alias-new")
	aliasTarget := filepath.Join(dir, "codog-alias")
	require.NoError(t, os.WriteFile(aliasArtifact, []byte("alias-new"), 0o755))
	require.NoError(t, os.WriteFile(aliasTarget, []byte("alias-old"), 0o755))
	out.Reset()
	require.NoError(t, app.Install(context.Background(), []string{"--json", aliasArtifact, aliasTarget}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err = os.ReadFile(aliasTarget)
	require.NoError(t, err)
	require.Equal(t, "alias-new", string(data))

	upgradeArtifact := filepath.Join(dir, "codog-upgrade-new")
	upgradeTarget := filepath.Join(dir, "codog-upgrade")
	require.NoError(t, os.WriteFile(upgradeArtifact, []byte("upgrade-new"), 0o755))
	require.NoError(t, os.WriteFile(upgradeTarget, []byte("upgrade-old"), 0o755))
	out.Reset()
	require.NoError(t, app.Upgrade(context.Background(), []string{"install", upgradeArtifact, upgradeTarget}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err = os.ReadFile(upgradeTarget)
	require.NoError(t, err)
	require.Equal(t, "upgrade-new", string(data))
}

func TestRollbackCommandDelegatesToUpdater(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "codog")
	backup := target + ".bak"
	require.NoError(t, os.WriteFile(target, []byte("current"), 0o755))
	require.NoError(t, os.WriteFile(backup, []byte("previous"), 0o755))

	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "rollback", target}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "updater"`)
	require.Contains(t, out, `"action": "rollback"`)
	require.Contains(t, out, `"rolled_back": true`)

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "previous", string(data))
}

func TestUpdaterStatusDefaults(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "codog")
	require.NoError(t, os.WriteFile(target, []byte("current"), 0o755))
	require.NoError(t, os.WriteFile(target+".bak", []byte("previous"), 0o755))
	updateDir := filepath.Join(dir, "updater")
	require.NoError(t, os.MkdirAll(updateDir, 0o755))
	artifactPath := filepath.Join(updateDir, "codog-0.2.0-test")
	require.NoError(t, os.WriteFile(artifactPath, []byte("downloaded"), 0o755))

	var out bytes.Buffer
	app := &App{
		Config:     config.Config{ConfigHome: dir, Future: config.FutureConfig{UpdaterManifestURL: "https://updates.example/manifest.json"}},
		Executable: target,
		Out:        &out,
	}
	require.NoError(t, app.Updater(context.Background(), nil))
	var report struct {
		Kind          string   `json:"kind"`
		Action        string   `json:"action"`
		Status        string   `json:"status"`
		SchemaVersion int      `json:"schema_version"`
		OutputFields  []string `json:"output_fields"`
		StatusValues  []string `json:"status_values"`
		Result        struct {
			CurrentVersion string `json:"current_version"`
			Platform       string `json:"platform"`
			Executable     string `json:"executable"`
			ConfigHome     string `json:"config_home"`
			UpdateDir      string `json:"update_dir"`
			DefaultTarget  string `json:"default_target"`
			BackupPath     string `json:"backup_path"`
			BackupPresent  bool   `json:"backup_present"`
			TargetPresent  bool   `json:"target_present"`
			ManifestURL    string `json:"manifest_url"`
			ManifestSet    bool   `json:"manifest_configured"`
			Artifacts      []struct {
				Name       string `json:"name"`
				Path       string `json:"path"`
				Size       int64  `json:"size"`
				Executable bool   `json:"executable"`
			} `json:"artifacts"`
			ArtifactCount int      `json:"artifact_count"`
			Commands      []string `json:"commands"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "updater", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.SchemaVersion)
	require.Contains(t, report.OutputFields, "result")
	require.Contains(t, report.StatusValues, "ok")
	require.Contains(t, report.StatusValues, "error")
	require.Equal(t, version, report.Result.CurrentVersion)
	require.Equal(t, updater.PlatformKey(), report.Result.Platform)
	require.Equal(t, target, report.Result.Executable)
	require.Equal(t, dir, report.Result.ConfigHome)
	require.Equal(t, filepath.Join(dir, "updater"), report.Result.UpdateDir)
	require.Equal(t, target, report.Result.DefaultTarget)
	require.Equal(t, target+".bak", report.Result.BackupPath)
	require.True(t, report.Result.BackupPresent)
	require.True(t, report.Result.TargetPresent)
	require.Equal(t, "https://updates.example/manifest.json", report.Result.ManifestURL)
	require.True(t, report.Result.ManifestSet)
	require.Equal(t, 1, report.Result.ArtifactCount)
	require.Len(t, report.Result.Artifacts, 1)
	require.Equal(t, "codog-0.2.0-test", report.Result.Artifacts[0].Name)
	require.Equal(t, artifactPath, report.Result.Artifacts[0].Path)
	require.Equal(t, int64(len("downloaded")), report.Result.Artifacts[0].Size)
	require.True(t, report.Result.Artifacts[0].Executable)
	require.Contains(t, report.Result.Commands, "check")
	require.Contains(t, report.Result.Commands, "rollback")

	out.Reset()
	require.NoError(t, app.Updater(context.Background(), []string{"--output-format", "json"}))
	require.Contains(t, out.String(), `"action": "status"`)

	out.Reset()
	require.NoError(t, app.Upgrade(context.Background(), []string{"--json"}))
	require.Contains(t, out.String(), `"kind": "updater"`)
	require.Contains(t, out.String(), `"action": "status"`)
}
