package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/reportconformance"
	"github.com/stretchr/testify/require"
)

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

func TestRegistryDefersModelToolsAndHidesProductOutputTools(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	names := func(definitions []anthropic.ToolDefinition) []string {
		out := make([]string, 0, len(definitions))
		for _, definition := range definitions {
			out = append(out, definition.Name)
		}
		return out
	}

	initial := names(registry.DefinitionsForModel(nil))
	require.Contains(t, initial, "bash")
	require.Contains(t, initial, "read_file")
	require.Contains(t, initial, "tool_search")
	require.NotContains(t, initial, "web_fetch")
	require.NotContains(t, initial, "brief")
	require.NotContains(t, initial, "send_user_message")
	require.NotContains(t, initial, "structured_output")
	require.Len(t, initial, len(eagerModelTools))
	require.Less(t, len(initial), len(registry.Definitions()))

	loaded := names(registry.DefinitionsForModel([]string{"WebFetch"}))
	require.Contains(t, loaded, "web_fetch")
	require.NotContains(t, loaded, "brief")

	deferred := registry.DeferredInfos()
	deferredNames := make([]string, 0, len(deferred))
	for _, info := range deferred {
		deferredNames = append(deferredNames, info.Name)
	}
	require.Contains(t, deferredNames, "web_fetch")
	require.NotContains(t, deferredNames, "read_file")
	require.NotContains(t, deferredNames, "brief")
}

func TestToolSearchExcludesProductOutputToolsFromDefaultDiscovery(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	out, err := ToolSearchTool{Registry: registry}.Execute(context.Background(), []byte(`{"query":"select:Brief,WebFetch,Read","max_results":5}`))
	require.NoError(t, err)

	var report struct {
		MatchNames         []string `json:"match_names"`
		TotalDeferredTools int      `json:"total_deferred_tools"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, []string{"web_fetch", "read_file"}, report.MatchNames)
	require.Positive(t, report.TotalDeferredTools)
	require.NotContains(t, out, `"name": "brief"`)
}

func TestToolSearchToolReportsMCPDiscoveryDegradation(t *testing.T) {
	registry := NewRegistryWithOptions(t.TempDir(), RegistryOptions{
		MCPServers: map[string]config.MCPServerConfig{
			"broken": {},
			"remote": {URL: "ftp://example.test/mcp"},
		},
	})

	out, err := ToolSearchTool{Registry: registry}.Execute(context.Background(), []byte(`{"query":"mcp","max_results":5}`))
	require.NoError(t, err)
	var report struct {
		PendingMCPServers []string `json:"pending_mcp_servers"`
		MCPDegraded       struct {
			FailedServers []struct {
				ServerName string `json:"server_name"`
				Phase      string `json:"phase"`
				Error      struct {
					Message     string            `json:"message"`
					Context     map[string]string `json:"context"`
					Recoverable bool              `json:"recoverable"`
				} `json:"error"`
			} `json:"failed_servers"`
		} `json:"mcp_degraded"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, []string{"broken", "remote"}, report.PendingMCPServers)
	require.Len(t, report.MCPDegraded.FailedServers, 2)
	require.Equal(t, "broken", report.MCPDegraded.FailedServers[0].ServerName)
	require.Equal(t, "server_registration", report.MCPDegraded.FailedServers[0].Phase)
	require.Equal(t, "missing command or url", report.MCPDegraded.FailedServers[0].Error.Message)
	require.False(t, report.MCPDegraded.FailedServers[0].Error.Recoverable)
	require.Equal(t, "remote", report.MCPDegraded.FailedServers[1].ServerName)
	require.Equal(t, "server_registration", report.MCPDegraded.FailedServers[1].Phase)
	require.Equal(t, "http", report.MCPDegraded.FailedServers[1].Error.Context["transport"])
	require.Equal(t, "ftp", report.MCPDegraded.FailedServers[1].Error.Context["scheme"])
}

func TestToolSearchToolReportsMCPAvailableToolsForPartialDiscovery(t *testing.T) {
	server := config.MCPServerConfig{Command: "definitely-missing-codog-mcp-server"}
	registry := NewRegistryWithOptions(t.TempDir(), RegistryOptions{
		MCPServers: map[string]config.MCPServerConfig{
			"alpha":  {Command: os.Args[0]},
			"broken": server,
		},
	})
	registry.Register(MCPTool{
		Name:       NewMCPToolName("alpha", "echo"),
		ServerName: "alpha",
		Server:     config.MCPServerConfig{Command: os.Args[0]},
		RemoteName: "echo",
	})

	out, err := ToolSearchTool{Registry: registry}.Execute(context.Background(), []byte(`{"query":"alpha echo","max_results":5}`))
	require.NoError(t, err)
	var report struct {
		PendingMCPServers []string `json:"pending_mcp_servers"`
		MCPDegraded       struct {
			AvailableTools []string `json:"available_tools"`
			FailedServers  []struct {
				ServerName string `json:"server_name"`
				Phase      string `json:"phase"`
				Error      struct {
					Recoverable bool `json:"recoverable"`
				} `json:"error"`
			} `json:"failed_servers"`
		} `json:"mcp_degraded"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, []string{"broken"}, report.PendingMCPServers)
	require.Equal(t, []string{"mcp__alpha__echo"}, report.MCPDegraded.AvailableTools)
	require.Len(t, report.MCPDegraded.FailedServers, 1)
	require.Equal(t, "broken", report.MCPDegraded.FailedServers[0].ServerName)
	require.Equal(t, "spawn_connect", report.MCPDegraded.FailedServers[0].Phase)
	require.True(t, report.MCPDegraded.FailedServers[0].Error.Recoverable)
}

func TestAskUserQuestionToolReadsChoiceAndDefault(t *testing.T) {
	var out strings.Builder
	var requests []UserQuestionRequest
	tool := AskUserQuestionTool{
		In:  strings.NewReader("2\n"),
		Out: &out,
		OnRequest: func(request UserQuestionRequest) {
			requests = append(requests, request)
		},
	}
	properties := tool.Definition().InputSchema["properties"].(map[string]any)
	require.Contains(t, properties, "options")

	result, err := tool.Execute(context.Background(), []byte(`{"question":"Pick one","choices":["alpha","beta"],"default":"alpha"}`))
	require.NoError(t, err)
	require.Contains(t, out.String(), "Pick one")
	require.Contains(t, out.String(), "2. beta")
	require.Contains(t, result, `"answer": "beta"`)
	require.Equal(t, []UserQuestionRequest{{
		Question: "Pick one",
		Choices:  []string{"alpha", "beta"},
		Default:  "alpha",
	}}, requests)

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
	require.Equal(t, UserQuestionRequest{
		Question: "Use options?",
		Choices:  []string{"gamma", "delta"},
	}, requests[2])
}

func TestAskUserQuestionToolSupportsClaudeQuestionsShape(t *testing.T) {
	input := `{"questions":[` +
		`{"question":"Pick a lane?","header":"Lane","options":[` +
		`{"label":"Alpha","description":"Stable","preview":"alpha preview"},` +
		`{"label":"Beta","description":"Fast"}],"multiSelect":false},` +
		`{"question":"Enable features?","header":"Features","options":[` +
		`{"label":"Cache","description":"Reuse results"},` +
		`{"label":"Trace","description":"Record spans"}],"multiSelect":true}` +
		`]}`
	var request UserQuestionRequest
	tool := AskUserQuestionTool{
		In: strings.NewReader(`{"Pick a lane?":"Beta","Enable features?":"Cache, Trace"}` + "\n"),
		OnRequest: func(value UserQuestionRequest) {
			request = value
		},
	}

	result, err := tool.Execute(context.Background(), []byte(input))
	require.NoError(t, err)
	require.Len(t, request.Questions, 2)
	require.Equal(t, "Lane", request.Questions[0].Header)
	require.Equal(t, "alpha preview", request.Questions[0].Options[0].Preview)
	require.True(t, request.Questions[1].MultiSelect)
	require.Contains(t, result, `"Pick a lane?": "Beta"`)
	require.Contains(t, result, `"Enable features?": "Cache, Trace"`)

	definition := tool.Definition().InputSchema
	properties := definition["properties"].(map[string]any)
	require.Contains(t, properties, "questions")
	require.Contains(t, definition, "anyOf")
}

func TestAskUserQuestionToolReadsModernQuestionsWithoutTUI(t *testing.T) {
	input := `{"questions":[` +
		`{"question":"Pick?","header":"Pick","options":[{"label":"One","description":"First"},{"label":"Two","description":"Second"}]},` +
		`{"question":"Combine?","header":"Combine","options":[{"label":"Red","description":"R"},{"label":"Blue","description":"B"}],"multiSelect":true}` +
		`]}`
	var out strings.Builder
	tool := AskUserQuestionTool{In: strings.NewReader("2\n1,2\n"), Out: &out}

	result, err := tool.Execute(context.Background(), []byte(input))
	require.NoError(t, err)
	require.Contains(t, out.String(), "[1/2] Pick?")
	require.Contains(t, out.String(), "Select one or more")
	require.Contains(t, result, `"Pick?": "Two"`)
	require.Contains(t, result, `"Combine?": "Red, Blue"`)
}

func TestAskUserQuestionToolRejectsInvalidModernQuestions(t *testing.T) {
	validOptions := `[{"label":"One","description":"First"},{"label":"Two","description":"Second"}]`
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "mixed legacy", input: `{"question":"legacy","questions":[{"question":"Modern?","header":"Modern","options":` + validOptions + `}]}`, want: "cannot be combined"},
		{name: "long header", input: `{"questions":[{"question":"Modern?","header":"header-is-too-long","options":` + validOptions + `}]}`, want: "at most 12"},
		{name: "few options", input: `{"questions":[{"question":"Modern?","header":"Modern","options":[{"label":"One","description":"First"}]}]}`, want: "between 2 and 4"},
		{name: "duplicate options", input: `{"questions":[{"question":"Modern?","header":"Modern","options":[{"label":"One","description":"First"},{"label":"one","description":"Again"}]}]}`, want: "labels must be unique"},
		{name: "duplicate questions", input: `{"questions":[{"question":"Modern?","header":"One","options":` + validOptions + `},{"question":"modern?","header":"Two","options":` + validOptions + `}]}`, want: "question texts must be unique"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (AskUserQuestionTool{}).Execute(context.Background(), []byte(tc.input))
			require.ErrorContains(t, err, tc.want)
		})
	}
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

	_, err = BriefTool{Workspace: workspace}.Execute(context.Background(), []byte(`{"message":"Review ready","status":"proactiv"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown brief status "proactiv"`)
	require.Contains(t, err.Error(), `did you mean "proactive"?`)
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

	_, err = REPLTool{Workspace: t.TempDir()}.Execute(context.Background(), []byte(`{"language":"pyhton","code":"x"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported repl language "pyhton"`)
	require.Contains(t, err.Error(), `python`)
}

func TestREPLToolLoadsConfiguredEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	out, err := REPLTool{
		Workspace: t.TempDir(),
		ConfigEnv: map[string]string{"CODOG_REPL_ENV": "ready"},
	}.Execute(context.Background(), []byte(`{"language":"sh","code":"printf %s \"$CODOG_REPL_ENV\"","timeout_ms":1000}`))
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "ready"`)
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
	definition := tool.Definition()
	actionSchema, ok := definition.InputSchema["properties"].(map[string]any)["action"].(map[string]any)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"list", "ls", "search", "find", "show", "info", "describe", "view", "inspect", "read", "invoke", "run", "use", "load", "activate"}, actionSchema["enum"])

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

	out, err = tool.Execute(context.Background(), []byte(`{"action":"find","query":"review","max_results":5}`))
	require.NoError(t, err)
	require.Contains(t, out, `"action": "list"`)
	require.Contains(t, out, `"query": "review"`)
	require.Contains(t, out, `"name": "review"`)
	require.NotContains(t, out, `"name": "internal"`)

	out, err = tool.Execute(context.Background(), []byte(`{"action":"show","skill":"internal"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"action": "show"`)
	require.Contains(t, out, `"skill": "internal"`)
	require.Contains(t, out, "Internal body")

	out, err = tool.Execute(context.Background(), []byte(`{"action":"view","skill":"internal"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"action": "show"`)
	require.Contains(t, out, `"skill": "internal"`)

	out, err = tool.Execute(context.Background(), []byte(`{"skill":"$review","args":"check auth"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "skill"`)
	require.Contains(t, out, `"action": "invoke"`)
	require.Contains(t, out, `"skill": "review"`)
	require.Contains(t, out, `"args": "check auth"`)
	require.Contains(t, out, `"description": "Review code changes."`)
	require.Contains(t, out, "Review skill body")
	require.Contains(t, out, "User request: check auth")

	out, err = tool.Execute(context.Background(), []byte(`{"action":"activate","skill":"review","args":"check deploy"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"action": "invoke"`)
	require.Contains(t, out, `"args": "check deploy"`)
	require.Contains(t, out, "User request: check deploy")

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

	_, err = tool.Execute(context.Background(), []byte(`{"action":"invok","skill":"review"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown skill action "invok"`)
	require.Contains(t, err.Error(), `suggestions:`)
	require.Contains(t, err.Error(), `invoke`)
}

func TestConfigToolGetsAndSetsUserConfig(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(configHome, 0o755))
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":"old-model","api_key":"secret","sandbox":{"strategy":"detect"}}`), 0o644))
	tool := ConfigTool{Workspace: workspace, ConfigHome: configHome}

	getOut, err := tool.Execute(context.Background(), []byte(`{"setting":"model"}`))
	require.NoError(t, err)
	require.Contains(t, getOut, `"operation": "get"`)
	require.Contains(t, getOut, `"value": "old-model"`)

	secretOut, err := tool.Execute(context.Background(), []byte(`{"setting":"api_key"}`))
	require.NoError(t, err)
	require.Contains(t, secretOut, `[redacted]`)
	require.NotContains(t, secretOut, `secret`)

	setOut, err := tool.Execute(context.Background(), []byte(`{"setting":"sandbox.strategy","value":"sandbox-exec"}`))
	require.NoError(t, err)
	require.Contains(t, setOut, `"operation": "set"`)
	require.Contains(t, setOut, `"previous_value": "detect"`)
	require.Contains(t, setOut, `"new_value": "sandbox-exec"`)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"strategy": "sandbox-exec"`)
}

func TestTaskToolsManageBackgroundTasks(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	createOut, err := TaskCreateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"command":"printf task-output","kind":"test","session_id":"session-1","owner":"tools-bot","workflow_scope":"manual-operator","watcher_action":"ignore"}`))
	require.NoError(t, err)
	var task background.Task
	require.NoError(t, json.Unmarshal([]byte(createOut), &task))
	require.NotEmpty(t, task.ID)
	require.Equal(t, "tools-bot", task.ScopeBinding.Owner)
	require.Equal(t, "manual-operator", task.ScopeBinding.WorkflowScope)
	require.Equal(t, "ignore", task.ScopeBinding.WatcherAction)
	require.False(t, task.ScopeBinding.Actionable)

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
	}, 5*time.Second, 20*time.Millisecond)
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

	_, err = TaskUpdateTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"taskId":"`+task.ID+`","message":"   "}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "task update message is required")

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
	}, 20*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		task, err := background.NewStore(configHome).Status(report.TaskID)
		return err == nil && task.Status == "completed"
	}, 20*time.Second, 50*time.Millisecond)

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
	heartbeatOut, err := TaskHeartbeatTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"task_id":"`+task.ID+`","status":"running","transport_alive":true,"observed_at":"`+observedAt.Format(time.RFC3339)+`","source_kind":"test","environment":"tools","channel":"tool","emitter":"tools-test","confidence":"high"}`))
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
	require.Equal(t, "test", heartbeatView.Heartbeat.Provenance.SourceKind)
	require.Equal(t, "tools-test", heartbeatView.Heartbeat.Provenance.Emitter)
	require.NotNil(t, heartbeatView.Task.Heartbeat)

	boardOut, err := TaskLaneBoardTool{Workspace: workspace, ConfigHome: configHome}.Execute(context.Background(), []byte(`{"stalled_after_seconds":3600}`))
	require.NoError(t, err)
	var board background.LaneBoard
	require.NoError(t, json.Unmarshal([]byte(boardOut), &board))
	require.Len(t, board.Active, 1)
	require.Equal(t, "test", board.Active[0].Provenance.SourceKind)
	require.Equal(t, task.ID, board.Active[0].TaskID)
	require.Equal(t, background.LaneFreshnessHealthy, board.Active[0].Freshness)
	require.Empty(t, board.Blocked)
	require.Empty(t, board.Finished)
}

func TestNudgeToolClassifiesAndAcknowledgesCycles(t *testing.T) {
	configHome := t.TempDir()
	tool := NudgeTool{ConfigHome: configHome}

	firstOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","nudge_id":"dogfood","cycle_id":"cycle-1","prompt":"check status","delivered_at":"2026-07-07T12:00:00Z"}`))
	require.NoError(t, err)
	require.Contains(t, firstOut, `"state": "new_nudge"`)
	require.Contains(t, firstOut, `"delivery_count": 1`)

	retryOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","nudge_id":"dogfood","cycle_id":"cycle-1","prompt":"check status","delivered_at":"2026-07-07T12:01:00Z"}`))
	require.NoError(t, err)
	require.Contains(t, retryOut, `"state": "retry_nudge"`)
	require.Contains(t, retryOut, `"delivery_count": 2`)

	ackOut, err := tool.Execute(context.Background(), []byte(`{"action":"ack","nudge_id":"dogfood","cycle_id":"cycle-1","response_id":"response-1","delivered_at":"2026-07-07T12:02:00Z"}`))
	require.NoError(t, err)
	require.Contains(t, ackOut, `"acknowledged": true`)
	require.Contains(t, ackOut, `"response_id": "response-1"`)

	staleOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","nudge_id":"dogfood","cycle_id":"cycle-1","delivered_at":"2026-07-07T12:03:00Z"}`))
	require.NoError(t, err)
	require.Contains(t, staleOut, `"state": "stale_duplicate"`)
	require.Contains(t, staleOut, `"already_acknowledged": true`)

	listOut, err := tool.Execute(context.Background(), []byte(`{"action":"list"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "nudge_list"`)
	require.Contains(t, listOut, `"count": 1`)
}

func TestProvisionalStatusToolSuppressesInFlightDuplicates(t *testing.T) {
	configHome := t.TempDir()
	tool := ProvisionalStatusTool{ConfigHome: configHome}

	firstOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","message":"working on it","observed_at":"2026-07-07T17:00:00Z","window_seconds":300}`))
	require.NoError(t, err)
	require.Contains(t, firstOut, `"kind": "provisional_status"`)
	require.Contains(t, firstOut, `"decision": "new_provisional"`)
	require.Contains(t, firstOut, `"exposed": true`)
	var first struct {
		Fingerprint string `json:"fingerprint"`
		Event       struct {
			EventID string `json:"event_id"`
			Status  string `json:"status"`
		} `json:"event"`
	}
	require.NoError(t, json.Unmarshal([]byte(firstOut), &first))
	require.Equal(t, "in_flight", first.Event.Status)

	secondOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","message":"please wait","observed_at":"2026-07-07T17:01:00Z","window_seconds":300}`))
	require.NoError(t, err)
	require.Contains(t, secondOut, `"decision": "suppressed_duplicate"`)
	require.Contains(t, secondOut, `"exposed": false`)
	var second struct {
		Fingerprint string `json:"fingerprint"`
		Event       struct {
			EventID string `json:"event_id"`
		} `json:"event"`
		State struct {
			RawEventCount   int `json:"raw_event_count"`
			SuppressedCount int `json:"suppressed_count"`
		} `json:"state"`
	}
	require.NoError(t, json.Unmarshal([]byte(secondOut), &second))
	require.Equal(t, first.Fingerprint, second.Fingerprint)
	require.NotEqual(t, first.Event.EventID, second.Event.EventID)
	require.Equal(t, 2, second.State.RawEventCount)
	require.Equal(t, 1, second.State.SuppressedCount)

	changedOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","blocker":"waiting for CI","message":"working on it","observed_at":"2026-07-07T17:02:00Z","window_seconds":300}`))
	require.NoError(t, err)
	require.Contains(t, changedOut, `"decision": "material_change"`)
	require.Contains(t, changedOut, `"exposed": true`)

	statusOut, err := tool.Execute(context.Background(), []byte(`{"action":"get","channel":"dogfood"}`))
	require.NoError(t, err)
	require.Contains(t, statusOut, `"kind": "provisional_status_state"`)
	require.Contains(t, statusOut, `"blocker": "waiting for CI"`)

	listOut, err := tool.Execute(context.Background(), []byte(`{"action":"list"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "provisional_status_list"`)
	require.Contains(t, listOut, `"count": 1`)
}

func TestProvisionalStatusToolEscalatesStaleInFlightStatus(t *testing.T) {
	configHome := t.TempDir()
	tool := ProvisionalStatusTool{ConfigHome: configHome}

	firstOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","message":"working on it","observed_at":"2026-07-07T17:00:00Z","window_seconds":300,"timeout_seconds":120,"timeout_policy":"dogfood-fast-ttl"}`))
	require.NoError(t, err)
	require.Contains(t, firstOut, `"decision": "new_provisional"`)
	require.Contains(t, firstOut, `"stale": false`)

	freshOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","message":"please wait","observed_at":"2026-07-07T17:01:00Z","window_seconds":300,"timeout_seconds":120,"timeout_policy":"dogfood-fast-ttl"}`))
	require.NoError(t, err)
	require.Contains(t, freshOut, `"decision": "suppressed_duplicate"`)
	require.Contains(t, freshOut, `"exposed": false`)
	require.Contains(t, freshOut, `"stale": false`)

	staleOut, err := tool.Execute(context.Background(), []byte(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","message":"working on it","observed_at":"2026-07-07T17:03:00Z","window_seconds":300,"timeout_seconds":120,"timeout_policy":"dogfood-fast-ttl"}`))
	require.NoError(t, err)
	require.Contains(t, staleOut, `"decision": "stale_provisional"`)
	require.Contains(t, staleOut, `"exposed": true`)
	require.Contains(t, staleOut, `"stale": true`)
	require.Contains(t, staleOut, `"kind": "provisional_status_stale"`)
	require.Contains(t, staleOut, `"signal": "blocker"`)
	require.Contains(t, staleOut, `"id": "dogfood-fast-ttl"`)
	require.Contains(t, staleOut, `"stale_for_seconds": 60`)
	var stale struct {
		Escalation struct {
			Policy struct {
				DeadlineAt time.Time `json:"deadline_at"`
			} `json:"policy"`
		} `json:"escalation"`
		State struct {
			Stale           bool `json:"stale"`
			EscalationCount int  `json:"escalation_count"`
		} `json:"state"`
	}
	require.NoError(t, json.Unmarshal([]byte(staleOut), &stale))
	require.Equal(t, time.Date(2026, 7, 7, 17, 2, 0, 0, time.UTC), stale.Escalation.Policy.DeadlineAt)
	require.True(t, stale.State.Stale)
	require.Equal(t, 1, stale.State.EscalationCount)
}

func TestRoadmapPinpointToolFilesAndUpdatesLifecycle(t *testing.T) {
	configHome := t.TempDir()
	tool := RoadmapPinpointTool{ConfigHome: configHome}

	fileOut, err := tool.Execute(context.Background(), []byte(`{"action":"file","title":"stable roadmap ids","description":"pinpoints need ids","priority":"p1","severity":"high","impact":"user_facing_breakage","priority_reason":{"blast_radius":"all dogfood reports","reproducibility":"always","automation_breakage":"blocks queue ranking","merge_risk":"low","rationale":"fresh blocker"},"evidence":[{"role":"symptom","type":"session","reference":"session-1","preview":"first observed in dogfood"}],"now":"2026-07-07T13:00:00Z"}`))
	require.NoError(t, err)
	require.Contains(t, fileOut, `"action": "new_roadmap_filing"`)
	require.Contains(t, fileOut, `"priority": "p1"`)
	require.Contains(t, fileOut, `"severity": "high"`)
	require.Contains(t, fileOut, `"impact": "user_facing_breakage"`)
	require.Contains(t, fileOut, `"rationale": "fresh blocker"`)
	require.Contains(t, fileOut, `"evidence": [`)
	require.Contains(t, fileOut, `"reference": "session-1"`)
	var filed struct {
		ItemID string `json:"item_id"`
		Item   struct {
			State    string `json:"state"`
			Evidence []struct {
				Role      string `json:"role"`
				Reference string `json:"reference"`
			} `json:"evidence"`
		} `json:"item"`
	}
	require.NoError(t, json.Unmarshal([]byte(fileOut), &filed))
	require.NotEmpty(t, filed.ItemID)
	require.Equal(t, "filed", filed.Item.State)
	require.Len(t, filed.Item.Evidence, 1)
	require.Equal(t, "symptom", filed.Item.Evidence[0].Role)

	updateInput := `{"action":"update","id":"` + filed.ItemID + `","title":"stable roadmap ids after edits","state":"in_progress","related":["rp-related"],"report_id":"report-1","priority":"p0","severity":"critical","impact":"operator_friction","priority_reason":{"blast_radius":"implementation queue","reproducibility":"reproducible","automation_breakage":"blocks queue ranking","merge_risk":"medium"},"handoff":{"objective":"Implement stable roadmap ids","suspected_scope":["internal/roadmap","internal/tools"],"suggested_verification":["go test ./internal/roadmap ./internal/tools"],"readiness":"implementation_ready","metadata":{"owner":"queue"}},"implementation":[{"lane_id":"lane-1","task_id":"task-1","worktree_id":"wt-1","worktree_path":"worktrees/wt-1","pr_url":"https://github.com/Rememorio/codog/pull/1","pr_number":1,"status":"running"}],"execution_results":[{"lane_id":"lane-1","status":"running","summary":"started from pinpoint"}],"evidence":[{"role":"verification","type":"commit","reference":"abc1234","preview":"go test ./..."}],"now":"2026-07-07T14:00:00Z"}`
	updateOut, err := tool.Execute(context.Background(), []byte(updateInput))
	require.NoError(t, err)
	require.Contains(t, updateOut, `"action": "roadmap_update"`)
	require.Contains(t, updateOut, `"item_id": "`+filed.ItemID+`"`)
	require.Contains(t, updateOut, `"state": "in_progress"`)
	require.Contains(t, updateOut, `"priority": "p0"`)
	require.Contains(t, updateOut, `"severity": "critical"`)
	require.Contains(t, updateOut, `"impact": "operator_friction"`)
	require.Contains(t, updateOut, `"readiness": "implementation_ready"`)
	require.Contains(t, updateOut, `"lane_id": "lane-1"`)
	require.Contains(t, updateOut, `"reference": "abc1234"`)

	handoffOut, err := tool.Execute(context.Background(), []byte(`{"action":"handoff","id":"`+filed.ItemID+`"}`))
	require.NoError(t, err)
	require.Contains(t, handoffOut, `"kind": "roadmap_pinpoint_handoff"`)
	require.Contains(t, handoffOut, `"objective": "Implement stable roadmap ids"`)
	require.Contains(t, handoffOut, `"suspected_scope": [`)
	require.Contains(t, handoffOut, `"implementation": [`)

	statusOut, err := tool.Execute(context.Background(), []byte(`{"action":"get","id":"`+filed.ItemID+`"}`))
	require.NoError(t, err)
	require.Contains(t, statusOut, `"kind": "roadmap_pinpoint_status"`)
	require.Contains(t, statusOut, `"report_id": "report-1"`)
	require.Contains(t, statusOut, `"priority": "p0"`)
	require.Contains(t, statusOut, `"reference": "session-1"`)
	require.Contains(t, statusOut, `"reference": "abc1234"`)

	listOut, err := tool.Execute(context.Background(), []byte(`{"action":"list"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "roadmap_pinpoint_list"`)
	require.Contains(t, listOut, `"count": 1`)
}

func TestStateAndReportToolsSuggestUnknownActions(t *testing.T) {
	configHome := t.TempDir()
	cases := []struct {
		name       string
		run        func() error
		unknown    string
		suggestion string
	}{
		{
			name: "nudge",
			run: func() error {
				_, err := NudgeTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"action":"acknwoledge","nudge_id":"dogfood","cycle_id":"cycle-1"}`))
				return err
			},
			unknown:    `unknown nudge action "acknwoledge"`,
			suggestion: `did you mean "acknowledge"?`,
		},
		{
			name: "provisional_status",
			run: func() error {
				_, err := ProvisionalStatusTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"action":"stauts","channel":"dogfood"}`))
				return err
			},
			unknown:    `unknown provisional_status action "stauts"`,
			suggestion: `did you mean "status"?`,
		},
		{
			name: "roadmap",
			run: func() error {
				_, err := RoadmapPinpointTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"action":"updte"}`))
				return err
			},
			unknown:    `unknown roadmap action "updte"`,
			suggestion: `did you mean "update"?`,
		},
		{
			name: "report_schema",
			run: func() error {
				_, err := ReportSchemaTool{}.Execute(context.Background(), []byte(`{"action":"regsitry"}`))
				return err
			},
			unknown:    `unknown report_schema action "regsitry"`,
			suggestion: `did you mean "registry"?`,
		},
		{
			name: "report_backpressure",
			run: func() error {
				_, err := ReportBackpressureTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"action":"snapshott"}`))
				return err
			},
			unknown:    `unknown report_backpressure action "snapshott"`,
			suggestion: `did you mean "snapshot"?`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.unknown)
			require.Contains(t, err.Error(), tc.suggestion)
		})
	}
}

func TestReportBackpressureToolCollapsesRepeatedRoadmapReports(t *testing.T) {
	configHome := t.TempDir()
	roadmapTool := RoadmapPinpointTool{ConfigHome: configHome}
	reportTool := ReportBackpressureTool{ConfigHome: configHome}

	fileOut, err := roadmapTool.Execute(context.Background(), []byte(`{"action":"file","title":"collapse unchanged reports","description":"avoid repeated backlog spam","priority":"p1","severity":"high","impact":"observability_debt","evidence":[{"role":"root_cause_hint","type":"log","reference":"log:dogfood","preview":"repeated backlog likely comes from missing cursor collapse"}],"now":"2026-07-07T13:00:00Z"}`))
	require.NoError(t, err)
	var filed struct {
		ItemID string `json:"item_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(fileOut), &filed))

	firstOut, err := reportTool.Execute(context.Background(), []byte(`{"action":"generate","channel":"dogfood","now":"2026-07-07T13:01:00Z"}`))
	require.NoError(t, err)
	require.Contains(t, firstOut, `"schema_version": "codog.reporting.report.v1"`)
	require.Contains(t, firstOut, `"schema_compatibility": {`)
	require.Contains(t, firstOut, `"policy": "codog.reporting.compatibility.v1"`)
	require.Contains(t, firstOut, `"minimal_stable_core": [`)
	require.Contains(t, firstOut, `"kind": "report_backpressure"`)
	require.Contains(t, firstOut, `"outcome": "new"`)
	require.Contains(t, firstOut, `"new_items": [`)
	require.Contains(t, firstOut, `"age_seconds": 60`)
	require.Contains(t, firstOut, `"freshness": "current"`)
	require.Contains(t, firstOut, `"observation_source": "carried_forward"`)
	require.Contains(t, firstOut, `"kind": "hypothesis"`)
	require.Contains(t, firstOut, `"confidence": "medium"`)
	require.Contains(t, firstOut, `"unchanged_count": 0`)
	require.Contains(t, firstOut, `"full_snapshot_stored": true`)
	var first struct {
		SnapshotID string `json:"snapshot_id"`
		ReportID   string `json:"report_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(firstOut), &first))
	require.NotEmpty(t, first.SnapshotID)

	secondOut, err := reportTool.Execute(context.Background(), []byte(`{"action":"generate","channel":"dogfood","trigger_id":"nudge-cycle-1","checked_surfaces":["roadmap","sessions"],"checked_window":"2026-07-07T13:01:00Z/2026-07-07T13:02:00Z","freshness_ttl_seconds":30,"now":"2026-07-07T13:02:00Z"}`))
	require.NoError(t, err)
	require.Contains(t, secondOut, `"outcome": "no_change"`)
	require.Contains(t, secondOut, `"trigger_id": "nudge-cycle-1"`)
	require.Contains(t, secondOut, `"checked": true`)
	require.Contains(t, secondOut, `"no_change": true`)
	require.Contains(t, secondOut, `"checked_surfaces": [`)
	require.Contains(t, secondOut, `"freshness_counts": {`)
	require.Contains(t, secondOut, `"stale": 1`)
	require.Contains(t, secondOut, `"last_meaningful_report_id": "`+first.ReportID+`"`)
	require.Contains(t, secondOut, `"unchanged_count": 1`)
	require.Contains(t, secondOut, `"collapsed": true`)
	require.NotContains(t, secondOut, `"new_items": [`)
	require.Contains(t, secondOut, `"previous_report_id": "`+first.ReportID+`"`)
	require.Contains(t, secondOut, `"negative_evidence": [`)
	require.Contains(t, secondOut, `"query": "no_new_delta"`)
	require.Contains(t, secondOut, `"status": "not_observed_in_checked_scope"`)
	require.Contains(t, secondOut, `"window": "2026-07-07T13:01:00Z/2026-07-07T13:02:00Z"`)
	require.Contains(t, secondOut, `"field_deltas": [`)
	require.Contains(t, secondOut, `"field": "report.delta"`)
	require.Contains(t, secondOut, `"state": "cleared"`)

	_, err = roadmapTool.Execute(context.Background(), []byte(`{"action":"update","id":"`+filed.ItemID+`","priority":"p0","severity":"critical","evidence":[{"role":"verification","type":"test","reference":"go-test","preview":"cursor collapse test passes"}],"now":"2026-07-07T13:03:00Z"}`))
	require.NoError(t, err)
	thirdOut, err := reportTool.Execute(context.Background(), []byte(`{"action":"generate","channel":"dogfood","now":"2026-07-07T13:04:00Z"}`))
	require.NoError(t, err)
	require.Contains(t, thirdOut, `"changed_items": [`)
	require.Contains(t, thirdOut, `"priority": "p0"`)
	require.Contains(t, thirdOut, `"promoted_from": "hypothesis"`)
	require.Contains(t, thirdOut, `"invalidates_negative_evidence": [`)
	require.Contains(t, thirdOut, `"state": "carried_forward"`)

	snapshotOut, err := reportTool.Execute(context.Background(), []byte(`{"action":"snapshot","snapshot_id":"`+first.SnapshotID+`"}`))
	require.NoError(t, err)
	require.Contains(t, snapshotOut, `"kind": "report_backpressure_snapshot"`)
	require.Contains(t, snapshotOut, `"schema_version": "codog.reporting.snapshot.v1"`)
}

func TestReportBackpressureToolProjectsForConsumerCapabilities(t *testing.T) {
	configHome := t.TempDir()
	roadmapTool := RoadmapPinpointTool{ConfigHome: configHome}
	reportTool := ReportBackpressureTool{ConfigHome: configHome}

	_, err := roadmapTool.Execute(context.Background(), []byte(`{"action":"file","title":"project consumer report","priority":"p1","evidence":[{"role":"root_cause_hint","type":"log","reference":"log:projection","preview":"legacy consumer needs reduced payload"}],"now":"2026-07-07T14:00:00Z"}`))
	require.NoError(t, err)

	out, err := reportTool.Execute(context.Background(), []byte(`{"action":"generate","channel":"dogfood","consumer":"legacy-claw","schema_versions":["legacy.report.v1"],"field_families":["claims","field_deltas"],"projection_view":"legacy","max_sensitivity":"public","now":"2026-07-07T14:01:00Z"}`))
	require.NoError(t, err)
	var projection struct {
		SchemaVersion string `json:"schema_version"`
		Provenance    struct {
			Downgraded           bool     `json:"downgraded"`
			OmittedFieldFamilies []string `json:"omitted_field_families"`
			SourceContentHash    string   `json:"source_content_hash"`
			SourceChanged        bool     `json:"source_changed"`
			RenderingChanged     bool     `json:"rendering_changed"`
			LatestCompatible     bool     `json:"latest_compatible"`
			StaleCached          bool     `json:"stale_cached"`
			CacheKey             string   `json:"cache_key"`
			Redactions           []struct {
				FieldPath string `json:"field_path"`
				Reason    string `json:"reason"`
			} `json:"redactions"`
			Consumer struct {
				Consumer       string `json:"consumer"`
				MaxSensitivity string `json:"max_sensitivity"`
			} `json:"consumer"`
		} `json:"provenance"`
		Payload         map[string]any `json:"payload"`
		CanonicalReport map[string]any `json:"canonical_report"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &projection))

	require.Equal(t, "codog.reporting.report.v1", projection.SchemaVersion)
	require.True(t, projection.Provenance.Downgraded)
	require.NotEmpty(t, projection.Provenance.SourceContentHash)
	require.False(t, projection.Provenance.SourceChanged)
	require.True(t, projection.Provenance.RenderingChanged)
	require.True(t, projection.Provenance.LatestCompatible)
	require.False(t, projection.Provenance.StaleCached)
	require.NotEmpty(t, projection.Provenance.CacheKey)
	require.Equal(t, "legacy-claw", projection.Provenance.Consumer.Consumer)
	require.Equal(t, "public", projection.Provenance.Consumer.MaxSensitivity)
	require.NotEmpty(t, projection.Provenance.Redactions)
	require.Contains(t, projection.Provenance.Redactions[0].Reason, "sensitivity")
	hasClaimRedaction := false
	for _, redaction := range projection.Provenance.Redactions {
		if strings.HasPrefix(redaction.FieldPath, "claims[") && strings.HasSuffix(redaction.FieldPath, "].text") {
			hasClaimRedaction = true
		}
	}
	require.True(t, hasClaimRedaction)
	require.Contains(t, projection.Provenance.OmittedFieldFamilies, "items")
	require.Contains(t, projection.Provenance.OmittedFieldFamilies, "negative_evidence")
	require.Contains(t, projection.Payload, "claims")
	require.Contains(t, projection.Payload, "field_deltas")
	require.NotContains(t, projection.Payload, "new_items")
	require.NotContains(t, projection.Payload, "negative_evidence")
	claims, ok := projection.Payload["claims"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(claims), 2)
	redactedClaimSeen := false
	for _, claimValue := range claims {
		claim, ok := claimValue.(map[string]any)
		if !ok {
			continue
		}
		if claim["text"] == "<redacted>" && claim["sensitivity"] == "internal" && claim["evidence"] == nil {
			redactedClaimSeen = true
		}
	}
	require.True(t, redactedClaimSeen)
	require.Contains(t, projection.CanonicalReport, "new_items")
	require.Contains(t, projection.CanonicalReport, "schema_compatibility")

	briefOut, err := reportTool.Execute(context.Background(), []byte(`{"action":"generate","channel":"dogfood","consumer":"clawhip","schema_versions":["codog.reporting.report.v1"],"projection_view":"delta_brief","projection_verbosity":"brief","now":"2026-07-07T14:02:00Z"}`))
	require.NoError(t, err)
	var brief struct {
		View       string         `json:"view"`
		Verbosity  string         `json:"verbosity"`
		Payload    map[string]any `json:"payload"`
		Provenance struct {
			SourceReportID    string `json:"source_report_id"`
			SourceContentHash string `json:"source_content_hash"`
			View              string `json:"view"`
			Verbosity         string `json:"verbosity"`
			SourceChanged     bool   `json:"source_changed"`
			RenderingChanged  bool   `json:"rendering_changed"`
			LatestCompatible  bool   `json:"latest_compatible"`
			StaleCached       bool   `json:"stale_cached"`
			CacheKey          string `json:"cache_key"`
			Downgraded        bool   `json:"downgraded"`
		} `json:"provenance"`
		CanonicalReport map[string]any `json:"canonical_report"`
	}
	require.NoError(t, json.Unmarshal([]byte(briefOut), &brief))
	require.Equal(t, "delta_brief", brief.View)
	require.Equal(t, "brief", brief.Verbosity)
	require.True(t, brief.Provenance.Downgraded)
	require.NotEmpty(t, brief.Provenance.SourceContentHash)
	require.Equal(t, "delta_brief", brief.Provenance.View)
	require.Equal(t, "brief", brief.Provenance.Verbosity)
	require.False(t, brief.Provenance.SourceChanged)
	require.True(t, brief.Provenance.RenderingChanged)
	require.True(t, brief.Provenance.LatestCompatible)
	require.False(t, brief.Provenance.StaleCached)
	require.NotEmpty(t, brief.Provenance.CacheKey)
	require.Contains(t, brief.Payload, "summary")
	require.Contains(t, brief.Payload, "identity")
	require.Contains(t, brief.Payload, "top_items")
	require.NotContains(t, brief.Payload, "new_items")
	require.Contains(t, brief.CanonicalReport, "schema_compatibility")
	require.Contains(t, brief.CanonicalReport, "report_id")
	require.Contains(t, brief.CanonicalReport, "identity")
	require.NotEmpty(t, brief.Provenance.SourceReportID)
}

func TestReportSchemaToolFiltersRegistry(t *testing.T) {
	out, err := ReportSchemaTool{}.Execute(context.Background(), []byte(`{"action":"registry","report":"report_backpressure","schema_version":"codog.reporting.report.v1","field_family":"field_deltas"}`))
	require.NoError(t, err)

	var response struct {
		Kind     string `json:"kind"`
		Action   string `json:"action"`
		Status   string `json:"status"`
		Registry struct {
			Reports []struct {
				ID            string `json:"id"`
				SchemaVersion string `json:"schema_version"`
			} `json:"reports"`
			Fields []struct {
				ID         string   `json:"id"`
				EnumValues []string `json:"enum_values"`
				Deprecated bool     `json:"deprecated"`
			} `json:"fields"`
		} `json:"registry"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &response))

	require.Equal(t, "report_schema", response.Kind)
	require.Equal(t, "registry", response.Action)
	require.Equal(t, "ok", response.Status)
	require.Len(t, response.Registry.Reports, 1)
	require.Equal(t, "report_backpressure", response.Registry.Reports[0].ID)
	require.Len(t, response.Registry.Fields, 2)
	require.Equal(t, "field_deltas[]", response.Registry.Fields[0].ID)
	require.Equal(t, "field_deltas[].state", response.Registry.Fields[1].ID)
	require.Contains(t, response.Registry.Fields[1].EnumValues, "carried_forward")
	require.False(t, response.Registry.Fields[1].Deprecated)

	out, err = ReportSchemaTool{}.Execute(context.Background(), []byte(`{"action":"conformance_fixtures"}`))
	require.NoError(t, err)
	var fixtures struct {
		Kind       string                           `json:"kind"`
		Action     string                           `json:"action"`
		Status     string                           `json:"status"`
		FixtureSet string                           `json:"fixture_set"`
		Cases      []reportconformance.RequiredCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &fixtures))
	require.Equal(t, "report_schema", fixtures.Kind)
	require.Equal(t, "conformance_fixtures", fixtures.Action)
	require.Equal(t, "ok", fixtures.Status)
	require.Equal(t, reportconformance.FixtureSetVersion, fixtures.FixtureSet)
	require.Len(t, fixtures.Cases, len(reportconformance.RequiredCases()))

	input, err := json.Marshal(map[string]string{
		"action": "conformance",
		"input":  reportSchemaToolConformanceBundleJSON(t),
	})
	require.NoError(t, err)
	out, err = ReportSchemaTool{}.Execute(context.Background(), input)
	require.NoError(t, err)
	var conformance struct {
		Kind        string `json:"kind"`
		Action      string `json:"action"`
		Status      string `json:"status"`
		Conformance struct {
			Valid          bool `json:"valid"`
			ParsePassed    bool `json:"parse_passed"`
			SemanticPassed bool `json:"semantic_passed"`
			LastPassed     *struct {
				Consumer string `json:"consumer"`
				Version  string `json:"version"`
			} `json:"last_passed"`
		} `json:"conformance"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &conformance))
	require.Equal(t, "report_schema", conformance.Kind)
	require.Equal(t, "conformance", conformance.Action)
	require.Equal(t, "ok", conformance.Status)
	require.True(t, conformance.Conformance.Valid)
	require.True(t, conformance.Conformance.ParsePassed)
	require.True(t, conformance.Conformance.SemanticPassed)
	require.NotNil(t, conformance.Conformance.LastPassed)
	require.Equal(t, "tool-consumer", conformance.Conformance.LastPassed.Consumer)
}

func reportSchemaToolConformanceBundleJSON(t *testing.T) string {
	t.Helper()
	cases := make([]reportconformance.CaseResult, 0, len(reportconformance.RequiredCases()))
	for _, required := range reportconformance.RequiredCases() {
		cases = append(cases, reportconformance.CaseResult{
			Name:         required.Name,
			ProjectionID: required.ProjectionID,
			Parsed:       true,
			SemanticChecks: reportconformance.SemanticChecks{
				CanonicalIdentityCorrelated: true,
				RedactedFieldsHandled:       true,
				MissingFieldsDistinguished:  true,
				DowngradeHandled:            true,
				NoChangeHandled:             true,
				FreshnessHandled:            true,
			},
		})
	}
	data, err := json.Marshal(reportconformance.Bundle{
		SchemaVersion: reportconformance.BundleSchemaVersion,
		FixtureSet:    reportconformance.FixtureSetVersion,
		Consumer: reportconformance.ConsumerIdentity{
			Name:    "tool-consumer",
			Version: "1.0.0",
		},
		PassedAt: "2026-07-07T16:30:00Z",
		Cases:    cases,
	})
	require.NoError(t, err)
	return string(data)
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
	}, 20*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		task, err := background.NewStore(configHome).Status(payload.TaskID)
		return err == nil && task.Status == "completed"
	}, 2*time.Second, 20*time.Millisecond)
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

	createOut, err := WorkerCreateTool{Workspace: workspace, ConfigHome: configHome, TrustedRoots: []string{"repo-default", "shared"}}.Execute(context.Background(), []byte(`{"cwd":".","trusted_roots":["shared","."],"auto_recover_prompt_misdelivery":false}`))
	require.NoError(t, err)
	var created struct {
		WorkerID     string   `json:"worker_id"`
		TrustedRoots []string `json:"trusted_roots"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	require.NotEmpty(t, created.WorkerID)
	require.Equal(t, []string{"repo-default", "shared", "."}, created.TrustedRoots)

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
	}, 20*time.Second, 50*time.Millisecond)

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
			Classification string   `json:"classification"`
			NextActions    []string `json:"next_actions"`
			Evidence       struct {
				LastLifecycleState  string `json:"last_lifecycle_state"`
				PaneCommand         string `json:"pane_command"`
				TrustPromptDetected bool   `json:"trust_prompt_detected"`
				TransportHealth     string `json:"transport_health"`
				MCPHealth           string `json:"mcp_health"`
				Transport           struct {
					Name    string `json:"name"`
					Checked bool   `json:"checked"`
					Status  string `json:"status"`
					Source  string `json:"source"`
					Summary string `json:"summary"`
				} `json:"transport"`
				MCP struct {
					Name   string `json:"name"`
					Status string `json:"status"`
					Source string `json:"source"`
				} `json:"mcp"`
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
	require.Contains(t, result.StartupNoEvidence.NextActions, "resolve the workspace trust prompt in the worker pane")
	require.Equal(t, "trust_prompt", result.StartupNoEvidence.Evidence.LastLifecycleState)
	require.Equal(t, "codog repl", result.StartupNoEvidence.Evidence.PaneCommand)
	require.True(t, result.StartupNoEvidence.Evidence.TrustPromptDetected)
	require.Equal(t, "transport:healthy", result.StartupNoEvidence.Evidence.TransportHealth)
	require.Equal(t, "mcp:healthy", result.StartupNoEvidence.Evidence.MCPHealth)
	require.Equal(t, "transport", result.StartupNoEvidence.Evidence.Transport.Name)
	require.True(t, result.StartupNoEvidence.Evidence.Transport.Checked)
	require.Equal(t, "healthy", result.StartupNoEvidence.Evidence.Transport.Status)
	require.Equal(t, "inferred", result.StartupNoEvidence.Evidence.Transport.Source)
	require.Equal(t, "transport:healthy", result.StartupNoEvidence.Evidence.Transport.Summary)
	require.Equal(t, "mcp", result.StartupNoEvidence.Evidence.MCP.Name)
	require.Equal(t, "healthy", result.StartupNoEvidence.Evidence.MCP.Status)
	require.Equal(t, "inferred", result.StartupNoEvidence.Evidence.MCP.Source)
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

	_, err = RecoveryRecipeTool{ConfigHome: configHome}.Execute(context.Background(), []byte(`{"scenario":"stale-brnch"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown recovery scenario "stale_brnch"`)
	require.Contains(t, err.Error(), `did you mean "stale_branch"`)
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

func TestPolicyEvaluateToolReturnsPolicyBlockedHandoff(t *testing.T) {
	out, err := PolicyEvaluateTool{}.Execute(context.Background(), []byte(`{
		"lane_id":"lane-main",
		"requested_action":"git push origin main",
		"repository":"owner/repo",
		"branch":"main",
		"actor":"release-bot",
		"actor_scope":"automation",
		"policy_source":"AGENTS.md"
	}`))
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "policy_evaluation"`)
	require.Contains(t, out, `"kind": "block"`)
	require.Contains(t, out, `"rule_id": "policy-blocked-handoff"`)
	require.Contains(t, out, `"blocked_handoff": {`)
	require.Contains(t, out, `"kind": "policy_blocked_handoff"`)
	require.Contains(t, out, `"status": "blocked_by_policy"`)
	require.Contains(t, out, `"reason": "main_push_forbidden"`)
	require.Contains(t, out, `"policy_source": "AGENTS.md"`)
	require.Contains(t, out, `"actor_scope": "automation"`)
	require.Contains(t, out, `"technical_failure": false`)
	require.Contains(t, out, `"kind": "create_branch"`)
	require.Contains(t, out, `"kind": "open_pr"`)
	require.NotContains(t, out, `"kind": "merge_to_dev"`)
}

func TestApprovalTokenToolPersistsAndConsumesGrant(t *testing.T) {
	configHome := t.TempDir()
	tool := ApprovalTokenTool{ConfigHome: configHome}

	grantOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"grant",
		"token":"tok-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"},
		"approving_actor":"owner",
		"requesting_actor":"release-lead",
		"approved_executor":"release-bot",
		"max_uses":1,
		"delegation_chain":[{"actor":"owner","session_id":"session-owner","reason":"owner approval"}]
	}`))
	require.NoError(t, err)
	require.Contains(t, grantOut, `"kind": "approval_token"`)
	require.Contains(t, grantOut, `"status": "approval_granted"`)
	require.Contains(t, grantOut, `"commit": "abc123"`)
	require.Contains(t, grantOut, `"replay_prevention_nonce": "codog-replay-`)

	verifyOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"verify",
		"token":"tok-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"},
		"executing_actor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, verifyOut, `"status": "ok"`)
	require.Contains(t, verifyOut, `"requesting_actor": "release-lead"`)
	require.Contains(t, verifyOut, `"executing_actor": "release-bot"`)
	require.Contains(t, verifyOut, `"execution_mode": "delegated_execution"`)
	require.Contains(t, verifyOut, `"delegated_execution": true`)
	require.Contains(t, verifyOut, `"replay_prevention_nonce": "codog-replay-`)

	consumeOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"consume",
		"token":"tok-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"},
		"executing_actor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, consumeOut, `"status": "approval_consumed"`)
	require.Contains(t, consumeOut, `"uses": 1`)

	replayOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"consume",
		"token":"tok-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"},
		"executing_actor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, replayOut, `"status": "denied"`)
	require.Contains(t, replayOut, `"error_kind": "approval_already_consumed"`)

	listOut, err := tool.Execute(context.Background(), []byte(`{"action":"list"}`))
	require.NoError(t, err)
	require.Contains(t, listOut, `"kind": "approval_token_ledger"`)
	require.Contains(t, listOut, `"token": "tok-main"`)
	require.Contains(t, listOut, `"commit": "abc123"`)
	require.Contains(t, listOut, `"state": "consumed"`)
	require.Contains(t, listOut, `"usable": false`)
	require.Contains(t, listOut, `"remaining_uses": 0`)
	require.Contains(t, listOut, `"replay_prevention_nonce": "codog-replay-`)
}

func TestApprovalTokenToolApprovesPendingGrant(t *testing.T) {
	configHome := t.TempDir()
	tool := ApprovalTokenTool{ConfigHome: configHome}

	pendingOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"pending",
		"token":"tok-pending-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"},
		"approving_actor":"owner",
		"requesting_actor":"release-lead",
		"approved_executor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, pendingOut, `"action": "pending"`)
	require.Contains(t, pendingOut, `"status": "approval_pending"`)

	deniedOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"verify",
		"token":"tok-pending-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"},
		"executing_actor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, deniedOut, `"status": "denied"`)
	require.Contains(t, deniedOut, `"error_kind": "approval_pending"`)

	approveOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"approve",
		"token":"tok-pending-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"},
		"approving_actor":"owner",
		"requesting_actor":"release-lead",
		"approved_executor":"release-bot",
		"max_uses":2,
		"delegation_chain":[{"actor":"owner","session_id":"session-owner","reason":"owner approval"}]
	}`))
	require.NoError(t, err)
	require.Contains(t, approveOut, `"action": "approve"`)
	require.Contains(t, approveOut, `"status": "ok"`)
	require.Contains(t, approveOut, `"status": "approval_granted"`)
	require.Contains(t, approveOut, `"max_uses": 2`)
	require.Contains(t, approveOut, `"commit": "abc123"`)

	verifyOut, err := tool.Execute(context.Background(), []byte(`{
		"action":"verify",
		"token":"tok-pending-main",
		"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"},
		"executing_actor":"release-bot"
	}`))
	require.NoError(t, err)
	require.Contains(t, verifyOut, `"status": "ok"`)
	require.Contains(t, verifyOut, `"status": "approval_granted"`)
	require.Contains(t, verifyOut, `"requesting_actor": "release-lead"`)
	require.Contains(t, verifyOut, `"execution_mode": "delegated_execution"`)
	require.Contains(t, verifyOut, `"delegated_execution": true`)
}

func TestApprovalTokenToolSuggestsUnknownAction(t *testing.T) {
	tool := ApprovalTokenTool{ConfigHome: t.TempDir()}

	_, err := tool.Execute(context.Background(), []byte(`{"action":"verfy"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown approval_token action "verfy"`)
	require.Contains(t, err.Error(), `did you mean "verify"?`)
}

func TestCommandToolPermissionDefaultsToDanger(t *testing.T) {
	require.Equal(t, PermissionDanger, CommandTool{}.Permission())
	require.Equal(t, PermissionReadOnly, CommandTool{Required: PermissionReadOnly}.Permission())
	require.Equal(t, PermissionWorkspace, CommandTool{Required: PermissionWorkspace}.Permission())
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

func TestCommandToolLoadsConfiguredEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	out, err := CommandTool{
		Name:      "env_echo",
		Command:   "sh",
		Args:      []string{"-c", `printf %s "$CODOG_COMMAND_ENV"`},
		Workspace: t.TempDir(),
		ConfigEnv: map[string]string{"CODOG_COMMAND_ENV": "ready"},
	}.Execute(context.Background(), nil)
	require.NoError(t, err)
	require.Contains(t, out, `"stdout": "ready"`)
}

func TestBashToolRejectsInvalidSandboxStrategy(t *testing.T) {
	_, err := BashTool{
		Workspace:       t.TempDir(),
		SandboxStrategy: "sandbx-exec",
	}.Execute(context.Background(), []byte(`{"command":"pwd"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported sandbox strategy "sandbx-exec"`)
	require.Contains(t, err.Error(), `did you mean "sandbox-exec"`)
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

	_, err = MCPDispatchTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"tes","tool":"echo"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown MCP server "tes"`)
	require.Contains(t, err.Error(), `did you mean "test"`)

	authOut, err := MCPAuthTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"test"}`))
	require.NoError(t, err)
	require.Contains(t, authOut, `"status": "ok"`)
	require.Contains(t, authOut, `"tool_count": 1`)

	authOut, err = MCPAuthTool{Servers: map[string]config.MCPServerConfig{"test": server}}.Execute(context.Background(), []byte(`{"server":"missing"}`))
	require.NoError(t, err)
	require.Contains(t, authOut, `"status": "unknown"`)
}

func TestMCPAuthToolReportsOAuthReadiness(t *testing.T) {
	configHome := t.TempDir()
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		_, _ = fmt.Fprintf(w, `{"authorization_endpoint":"%s/authorize","token_endpoint":"%s/token"}`, "https://auth.example", "https://auth.example")
	}))
	defer issuer.Close()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "work", issuer.URL, "client-1", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "access-token-1234", RefreshToken: "refresh-token-1234"})
	require.NoError(t, err)

	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPToolHelperProcess"},
		Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
	}
	out, err := MCPAuthTool{
		Servers:      map[string]config.MCPServerConfig{"test": server},
		ConfigHome:   configHome,
		OAuthProfile: "work",
	}.Execute(context.Background(), []byte(`{"server":"test"}`))
	require.NoError(t, err)

	var report struct {
		Server       string `json:"server"`
		Status       string `json:"status"`
		OAuthProfile string `json:"oauth_profile"`
		OAuthStatus  struct {
			ProfileName       string `json:"profile_name"`
			ProfileConfigured bool   `json:"profile_configured"`
			TokenPresent      bool   `json:"token_present"`
			Ready             bool   `json:"ready"`
			Token             struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"token"`
		} `json:"oauth_status"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "test", report.Server)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "work", report.OAuthProfile)
	require.Equal(t, "work", report.OAuthStatus.ProfileName)
	require.True(t, report.OAuthStatus.ProfileConfigured)
	require.True(t, report.OAuthStatus.TokenPresent)
	require.True(t, report.OAuthStatus.Ready)
	require.NotContains(t, out, "access-token-1234")
	require.NotContains(t, out, "refresh-token-1234")
	require.Contains(t, report.OAuthStatus.Token.AccessToken, "acce")
	require.Contains(t, report.OAuthStatus.Token.RefreshToken, "refr")
}

func TestMCPAuthToolClearsStoredOAuthToken(t *testing.T) {
	configHome := t.TempDir()
	_, err := oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "access-token-1234",
		RefreshToken: "refresh-token-1234",
	})
	require.NoError(t, err)
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPToolHelperProcess"},
		Env:     []string{"CODOG_MCP_TOOL_HELPER=1"},
	}

	out, err := MCPAuthTool{
		Servers:      map[string]config.MCPServerConfig{"test": server},
		ConfigHome:   configHome,
		OAuthProfile: "work",
	}.Execute(context.Background(), []byte(`{"server":"test","action":"clear"}`))

	require.NoError(t, err)
	require.Contains(t, out, `"cleared": true`)
	require.Contains(t, out, `"deleted": true`)
	require.Contains(t, out, `"token_present": false`)
	require.NotContains(t, out, "access-token-1234")
	require.NotContains(t, out, "refresh-token-1234")
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)
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

	_, err = ReadMCPResourceTool{Servers: servers}.Execute(context.Background(), []byte(`{"server":"tes","uri":"codog://note"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown MCP server "tes"`)
	require.Contains(t, err.Error(), `did you mean "test"`)
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
