package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bashvalidation"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/cron"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/hookenv"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/shellstate"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/team"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/Rememorio/codog/internal/webaccess"
	"github.com/Rememorio/codog/internal/workers"
	"github.com/Rememorio/codog/internal/worktree"
)

type Permission string

const (
	PermissionReadOnly  Permission = "read-only"
	PermissionWorkspace Permission = "workspace-write"
	PermissionDanger    Permission = "danger-full-access"
	PermissionPrompt    Permission = "prompt"
	PermissionAllow     Permission = "allow"
	maxFileToolBytes    int64      = 2_000_000
	maxRemoteBodyBytes  int64      = 2_000_000
)

type Tool interface {
	Definition() anthropic.ToolDefinition
	Permission() Permission
	Execute(context.Context, json.RawMessage) (string, error)
}

type CommandTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Required    Permission
	Command     string
	Args        []string
	Workspace   string
}

type MCPTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Required    Permission
	ServerName  string
	Server      config.MCPServerConfig
	RemoteName  string
}

type Registry struct {
	tools map[string]Tool
}

type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Permission  Permission     `json:"permission"`
	InputSchema map[string]any `json:"input_schema"`
}

type RegistryOptions struct {
	SandboxStrategy string
	AdditionalDirs  []string
	ConfigHome      string
	MCPServers      map[string]config.MCPServerConfig
	PowerShell      string
	QuestionIn      io.Reader
	QuestionOut     io.Writer
}

var claudeToolAliases = map[string]string{
	"agenttool":                    "agent",
	"askuserquestion":              "ask_user_question",
	"askuserquestiontool":          "ask_user_question",
	"agentoutputtool":              "task_output",
	"bash":                         "bash",
	"bashtool":                     "bash",
	"bashoutput":                   "bash_output",
	"bashoutputtool":               "bash_output",
	"brief":                        "brief",
	"brieftool":                    "brief",
	"config":                       "config",
	"configtool":                   "config",
	"croncreate":                   "cron_create",
	"croncreatetool":               "cron_create",
	"crondelete":                   "cron_delete",
	"crondeletetool":               "cron_delete",
	"cronlist":                     "cron_list",
	"cronlisttool":                 "cron_list",
	"edit":                         "edit_file",
	"editfile":                     "edit_file",
	"edittool":                     "edit_file",
	"enterplanmode":                "enter_plan_mode",
	"enterplanmodetool":            "enter_plan_mode",
	"enterworktree":                "enter_worktree",
	"enterworktreetool":            "enter_worktree",
	"exitplanmode":                 "exit_plan_mode",
	"exitplanmodetool":             "exit_plan_mode",
	"exitplanmodev2":               "exit_plan_mode",
	"exitplanmodev2tool":           "exit_plan_mode",
	"exitworktree":                 "exit_worktree",
	"exitworktreetool":             "exit_worktree",
	"fileedit":                     "edit_file",
	"fileedittool":                 "edit_file",
	"fileread":                     "read_file",
	"filereadtool":                 "read_file",
	"filewrite":                    "write_file",
	"filewritetool":                "write_file",
	"getmcpprompt":                 "get_mcp_prompt",
	"getmcpprompttool":             "get_mcp_prompt",
	"gitblame":                     "git_blame",
	"gitblametool":                 "git_blame",
	"gitdiff":                      "git_diff",
	"gitdifftool":                  "git_diff",
	"gitlog":                       "git_log",
	"gitlogtool":                   "git_log",
	"gitshow":                      "git_show",
	"gitshowtool":                  "git_show",
	"gitstatus":                    "git_status",
	"gitstatustool":                "git_status",
	"glob":                         "glob",
	"globsearch":                   "glob",
	"globsearchtool":               "glob",
	"globtool":                     "glob",
	"grep":                         "grep",
	"grepsearch":                   "grep",
	"grepsearchtool":               "grep",
	"greptool":                     "grep",
	"listmcpprompts":               "list_mcp_prompts",
	"listmcppromptstool":           "list_mcp_prompts",
	"listmcpresources":             "list_mcp_resources",
	"listmcpresourcestool":         "list_mcp_resources",
	"listmcpresourcetemplates":     "list_mcp_resource_templates",
	"listmcpresourcetemplatestool": "list_mcp_resource_templates",
	"killbash":                     "kill_bash",
	"killbashtool":                 "kill_bash",
	"killshell":                    "task_stop",
	"ls":                           "ls",
	"lstool":                       "ls",
	"lsptool":                      "lsp",
	"mcp":                          "mcp",
	"mcptool":                      "mcp",
	"mcpauth":                      "mcp_auth",
	"mcpauthtool":                  "mcp_auth",
	"multiedit":                    "multi_edit",
	"multieditfile":                "multi_edit",
	"multiedittool":                "multi_edit",
	"notebookedit":                 "notebook_edit",
	"notebookedittool":             "notebook_edit",
	"notebookread":                 "notebook_read",
	"notebookreadtool":             "notebook_read",
	"powershell":                   "powershell",
	"powershelltool":               "powershell",
	"read":                         "read_file",
	"readfile":                     "read_file",
	"readtool":                     "read_file",
	"readmcpresource":              "read_mcp_resource",
	"readmcpresourcetool":          "read_mcp_resource",
	"repl":                         "repl",
	"remotetrigger":                "remote_trigger",
	"remotetriggertool":            "remote_trigger",
	"runtaskpacket":                "run_task_packet",
	"runtaskpackettool":            "run_task_packet",
	"sendmessage":                  "send_user_message",
	"sendmessagetool":              "send_user_message",
	"sendusermessage":              "send_user_message",
	"sendusermessagetool":          "send_user_message",
	"skill":                        "skill",
	"skilltool":                    "skill",
	"sleep":                        "sleep",
	"sleeptool":                    "sleep",
	"structuredoutput":             "structured_output",
	"structuredoutputtool":         "structured_output",
	"syntheticoutputtool":          "structured_output",
	"task":                         "agent",
	"taskcreate":                   "task_create",
	"taskcreatetool":               "task_create",
	"taskget":                      "task_get",
	"taskgettool":                  "task_get",
	"tasklist":                     "task_list",
	"tasklisttool":                 "task_list",
	"taskoutput":                   "task_output",
	"taskoutputtool":               "task_output",
	"taskstatus":                   "task_status",
	"taskstatustool":               "task_status",
	"taskstop":                     "task_stop",
	"taskstoptool":                 "task_stop",
	"tasksupervise":                "task_supervise",
	"tasksupervisetool":            "task_supervise",
	"taskupdate":                   "task_update",
	"taskupdatetool":               "task_update",
	"teamcreate":                   "team_create",
	"teamcreatetool":               "team_create",
	"teamdelete":                   "team_delete",
	"teamdeletetool":               "team_delete",
	"teamget":                      "team_get",
	"teamgettool":                  "team_get",
	"teamlist":                     "team_list",
	"teamlisttool":                 "team_list",
	"testingpermission":            "testing_permission",
	"testingpermissiontool":        "testing_permission",
	"todowrite":                    "todo_write",
	"todowritetool":                "todo_write",
	"todoread":                     "todo_read",
	"todoreadtool":                 "todo_read",
	"toolsearch":                   "tool_search",
	"toolsearchtool":               "tool_search",
	"webfetch":                     "web_fetch",
	"webfetchtool":                 "web_fetch",
	"websearch":                    "web_search",
	"websearchtool":                "web_search",
	"workerawaitready":             "worker_await_ready",
	"workerawaitreadytool":         "worker_await_ready",
	"workercreate":                 "worker_create",
	"workercreatetool":             "worker_create",
	"workerget":                    "worker_get",
	"workergettool":                "worker_get",
	"workerlist":                   "worker_list",
	"workerlisttool":               "worker_list",
	"workerobserve":                "worker_observe",
	"workerobservetool":            "worker_observe",
	"workerobservecompletion":      "worker_observe_completion",
	"workerobservecompletiontool":  "worker_observe_completion",
	"workerresolvetrust":           "worker_resolve_trust",
	"workerresolvetrusttool":       "worker_resolve_trust",
	"workerrestart":                "worker_restart",
	"workerrestarttool":            "worker_restart",
	"workersendprompt":             "worker_send_prompt",
	"workersendprompttool":         "worker_send_prompt",
	"workerterminate":              "worker_terminate",
	"workerterminatetool":          "worker_terminate",
	"write":                        "write_file",
	"writefile":                    "write_file",
	"writetool":                    "write_file",
}

var claudeToolAliasDisplay = map[string]string{
	"Agent":                        "agent",
	"AgentOutputTool":              "task_output",
	"AgentTool":                    "agent",
	"AskUserQuestion":              "ask_user_question",
	"AskUserQuestionTool":          "ask_user_question",
	"Bash":                         "bash",
	"BashOutput":                   "bash_output",
	"BashOutputTool":               "bash_output",
	"BashTool":                     "bash",
	"BriefTool":                    "brief",
	"ConfigTool":                   "config",
	"CronCreate":                   "cron_create",
	"CronCreateTool":               "cron_create",
	"CronDelete":                   "cron_delete",
	"CronDeleteTool":               "cron_delete",
	"CronList":                     "cron_list",
	"CronListTool":                 "cron_list",
	"Edit":                         "edit_file",
	"EditTool":                     "edit_file",
	"EnterPlanModeTool":            "enter_plan_mode",
	"EnterWorktree":                "enter_worktree",
	"EnterWorktreeTool":            "enter_worktree",
	"ExitPlanModeTool":             "exit_plan_mode",
	"ExitPlanModeV2Tool":           "exit_plan_mode",
	"ExitWorktree":                 "exit_worktree",
	"ExitWorktreeTool":             "exit_worktree",
	"FileEdit":                     "edit_file",
	"FileEditTool":                 "edit_file",
	"FileRead":                     "read_file",
	"FileReadTool":                 "read_file",
	"FileWrite":                    "write_file",
	"FileWriteTool":                "write_file",
	"GetMcpPrompt":                 "get_mcp_prompt",
	"GetMcpPromptTool":             "get_mcp_prompt",
	"GitStatusTool":                "git_status",
	"Glob":                         "glob",
	"GlobSearch":                   "glob",
	"GlobSearchTool":               "glob",
	"GlobTool":                     "glob",
	"Grep":                         "grep",
	"GrepSearch":                   "grep",
	"GrepSearchTool":               "grep",
	"GrepTool":                     "grep",
	"KillBash":                     "kill_bash",
	"KillBashTool":                 "kill_bash",
	"KillShell":                    "task_stop",
	"LS":                           "ls",
	"LSPTool":                      "lsp",
	"LSTool":                       "ls",
	"ListMcpPrompts":               "list_mcp_prompts",
	"ListMcpPromptsTool":           "list_mcp_prompts",
	"ListMcpResourceTemplates":     "list_mcp_resource_templates",
	"ListMcpResourceTemplatesTool": "list_mcp_resource_templates",
	"ListMcpResources":             "list_mcp_resources",
	"ListMcpResourcesTool":         "list_mcp_resources",
	"MCPTool":                      "mcp",
	"McpAuth":                      "mcp_auth",
	"McpAuthTool":                  "mcp_auth",
	"MultiEdit":                    "multi_edit",
	"MultiEditTool":                "multi_edit",
	"NotebookEdit":                 "notebook_edit",
	"NotebookEditTool":             "notebook_edit",
	"NotebookRead":                 "notebook_read",
	"NotebookReadTool":             "notebook_read",
	"PowerShell":                   "powershell",
	"PowerShellTool":               "powershell",
	"Read":                         "read_file",
	"ReadMcpResource":              "read_mcp_resource",
	"ReadMcpResourceTool":          "read_mcp_resource",
	"ReadTool":                     "read_file",
	"RemoteTrigger":                "remote_trigger",
	"RemoteTriggerTool":            "remote_trigger",
	"RunTaskPacket":                "run_task_packet",
	"RunTaskPacketTool":            "run_task_packet",
	"SendMessage":                  "send_user_message",
	"SendMessageTool":              "send_user_message",
	"SkillTool":                    "skill",
	"SyntheticOutputTool":          "structured_output",
	"Task":                         "agent",
	"TaskCreate":                   "task_create",
	"TaskCreateTool":               "task_create",
	"TaskGet":                      "task_get",
	"TaskGetTool":                  "task_get",
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
	"TestingPermissionTool":        "testing_permission",
	"TodoRead":                     "todo_read",
	"TodoReadTool":                 "todo_read",
	"TodoWrite":                    "todo_write",
	"TodoWriteTool":                "todo_write",
	"ToolSearchTool":               "tool_search",
	"WebFetch":                     "web_fetch",
	"WebFetchTool":                 "web_fetch",
	"WebSearch":                    "web_search",
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
	"WorkerObserveCompletion":      "worker_observe_completion",
	"WorkerObserveCompletionTool":  "worker_observe_completion",
	"WorkerObserveTool":            "worker_observe",
	"WorkerResolveTrust":           "worker_resolve_trust",
	"WorkerResolveTrustTool":       "worker_resolve_trust",
	"WorkerRestart":                "worker_restart",
	"WorkerRestartTool":            "worker_restart",
	"WorkerSendPrompt":             "worker_send_prompt",
	"WorkerSendPromptTool":         "worker_send_prompt",
	"WorkerTerminate":              "worker_terminate",
	"WorkerTerminateTool":          "worker_terminate",
	"Write":                        "write_file",
	"WriteTool":                    "write_file",
}

func ClaudeToolAliases() map[string]string {
	aliases := make(map[string]string, len(claudeToolAliasDisplay))
	for alias, canonical := range claudeToolAliasDisplay {
		aliases[alias] = canonical
	}
	return aliases
}

func CanonicalToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if canonical := claudeToolAliases[toolAliasKey(name)]; canonical != "" {
		return canonical
	}
	return name
}

func toolAliasKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

type Prompter struct {
	Mode        Permission
	AllowRules  []string
	DenyRules   []string
	AskRules    []string
	DeniedTools []string
	Workspace   string
	In          io.Reader
	Err         io.Writer
	OnRequest   func(PermissionDecision)
	OnDecision  func(PermissionDecision)
}

type PermissionDecision struct {
	ToolName    string
	Required    Permission
	Mode        Permission
	Input       string
	Allowed     bool
	WouldPrompt bool
	Reason      string
	Message     string
}

func NewRegistry(workspace string) *Registry {
	return NewRegistryWithOptions(workspace, RegistryOptions{})
}

func NewRegistryWithOptions(workspace string, opts RegistryOptions) *Registry {
	reg := &Registry{tools: map[string]Tool{}}
	reg.registerBuiltinTools(workspace, opts)
	return reg
}

func (r *Registry) Register(tool Tool) {
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	r.tools[tool.Definition().Name] = tool
}

func (r *Registry) UpdateBuiltinScope(workspace string, opts RegistryOptions) {
	r.registerBuiltinTools(workspace, opts)
}

func (r *Registry) registerBuiltinTools(workspace string, opts RegistryOptions) {
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	r.Register(BashTool{Workspace: workspace, ConfigHome: opts.ConfigHome, SandboxStrategy: opts.SandboxStrategy})
	r.Register(PowerShellTool{Workspace: workspace, ConfigHome: opts.ConfigHome, Executable: opts.PowerShell})
	r.Register(BashOutputTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(KillBashTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(ReadFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(WriteFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(EditFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(MultiEditTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(GrepTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(GlobTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(LSTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(WebFetchTool{})
	r.Register(WebSearchTool{})
	r.Register(RemoteTriggerTool{})
	r.Register(TestingPermissionTool{})
	r.Register(NotebookReadTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(NotebookEditTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(LSPTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs, ConfigHome: opts.ConfigHome})
	r.Register(EnterWorktreeTool{Workspace: workspace})
	r.Register(ExitWorktreeTool{Workspace: workspace})
	r.Register(EnterPlanModeTool{Workspace: workspace})
	r.Register(ExitPlanModeTool{Workspace: workspace})
	r.Register(AgentTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(CronCreateTool{ConfigHome: opts.ConfigHome})
	r.Register(CronDeleteTool{ConfigHome: opts.ConfigHome})
	r.Register(CronListTool{ConfigHome: opts.ConfigHome})
	r.Register(TeamCreateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TeamListTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TeamGetTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TeamDeleteTool{ConfigHome: opts.ConfigHome})
	r.Register(WorkerCreateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerListTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerGetTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerObserveTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerResolveTrustTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerAwaitReadyTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerSendPromptTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerRestartTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerTerminateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerObserveCompletionTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskCreateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(RunTaskPacketTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskListTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskStatusTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskGetTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskUpdateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskStopTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskOutputTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskSuperviseTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TodoReadTool{Workspace: workspace})
	r.Register(TodoWriteTool{Workspace: workspace})
	r.Register(BriefTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(SendUserMessageTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(StructuredOutputTool{})
	r.Register(SleepTool{})
	r.Register(REPLTool{Workspace: workspace})
	r.Register(SkillTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(ConfigTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(MCPDispatchTool{Servers: opts.MCPServers})
	r.Register(MCPAuthTool{Servers: opts.MCPServers})
	r.Register(ListMCPResourcesTool{Servers: opts.MCPServers})
	r.Register(ReadMCPResourceTool{Servers: opts.MCPServers})
	r.Register(ListMCPResourceTemplatesTool{Servers: opts.MCPServers})
	r.Register(ListMCPPromptsTool{Servers: opts.MCPServers})
	r.Register(GetMCPPromptTool{Servers: opts.MCPServers})
	r.Register(GitStatusTool{Workspace: workspace})
	r.Register(GitDiffTool{Workspace: workspace})
	r.Register(GitLogTool{Workspace: workspace})
	r.Register(GitShowTool{Workspace: workspace})
	r.Register(GitBlameTool{Workspace: workspace})
	r.Register(AskUserQuestionTool{In: opts.QuestionIn, Out: opts.QuestionOut})
	r.Register(ToolSearchTool{Registry: r})
}

func (r *Registry) Has(name string) bool {
	_, _, ok := r.resolve(name)
	return ok
}

func (r *Registry) Definitions() []anthropic.ToolDefinition {
	defs := make([]anthropic.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func (r *Registry) DefinitionsForPlanMode() []anthropic.ToolDefinition {
	defs := make([]anthropic.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		def := tool.Definition()
		if ToolVisibleInPlanMode(def.Name, tool.Permission()) {
			defs = append(defs, def)
		}
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func ToolVisibleInPlanMode(name string, permission Permission) bool {
	if permission == PermissionReadOnly {
		return true
	}
	return CanonicalToolName(name) == "bash"
}

func ToolAllowedInPlanMode(name string, permission Permission) bool {
	return ToolVisibleInPlanMode(name, permission)
}

func ReadOnlyPrompter(base *Prompter, workspace string) *Prompter {
	if base == nil {
		return &Prompter{Mode: PermissionReadOnly, Workspace: workspace}
	}
	next := *base
	next.Mode = PermissionReadOnly
	next.AllowRules = nil
	if next.Workspace == "" {
		next.Workspace = workspace
	}
	return &next
}

func (r *Registry) Infos() []ToolInfo {
	infos := make([]ToolInfo, 0, len(r.tools))
	for _, tool := range r.tools {
		def := tool.Definition()
		infos = append(infos, ToolInfo{
			Name:        def.Name,
			Description: def.Description,
			Permission:  tool.Permission(),
			InputSchema: def.InputSchema,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

func (r *Registry) Info(name string) (ToolInfo, bool) {
	_, tool, ok := r.resolve(name)
	if !ok {
		return ToolInfo{}, false
	}
	def := tool.Definition()
	return ToolInfo{
		Name:        def.Name,
		Description: def.Description,
		Permission:  tool.Permission(),
		InputSchema: def.InputSchema,
	}, true
}

func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage, prompter *Prompter) (string, error) {
	canonical, tool, ok := r.resolve(name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if strings.EqualFold(canonical, "testing_permission") {
		return r.executeTestingPermission(input, prompter)
	}
	if prompter != nil {
		if err := prompter.Authorize(canonical, tool.Permission(), input); err != nil {
			return "", err
		}
	}
	return tool.Execute(ctx, input)
}

func (r *Registry) resolve(name string) (string, Tool, bool) {
	if r == nil {
		return "", nil, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, false
	}
	if tool := r.tools[name]; tool != nil {
		return name, tool, true
	}
	if canonical := CanonicalToolName(name); canonical != name {
		if tool := r.tools[canonical]; tool != nil {
			return canonical, tool, true
		}
	}
	for candidate, tool := range r.tools {
		if strings.EqualFold(candidate, name) {
			return candidate, tool, true
		}
	}
	return "", nil, false
}

func (p *Prompter) Authorize(name string, required Permission, input json.RawMessage) error {
	decision := p.Decide(name, required, input)
	if decision.Allowed {
		p.emitDecision(decision)
		return nil
	}
	if !decision.WouldPrompt {
		p.emitDecision(decision)
		return permissionDecisionError(decision)
	}
	if p.In == nil {
		p.In = os.Stdin
	}
	if p.Err == nil {
		p.Err = os.Stderr
	}
	if decision.Message != "" {
		fmt.Fprintf(p.Err, "\nBash validation warning: %s\n", decision.Message)
	}
	p.emitRequest(decision)
	fmt.Fprintf(p.Err, "\nTool %s requires %s permission.\nInput: %s\nAllow? [y/N/a=always for session] ", name, required, string(input))
	reader := bufio.NewReader(p.In)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "y" || answer == "yes" {
		p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: decision.Mode, Input: decision.Input, Allowed: true, Reason: "user_approved"})
		return nil
	}
	if answer == "a" || answer == "always" {
		if !ruleMatchesTool(p.AllowRules, name) {
			p.AllowRules = append(p.AllowRules, name)
		}
		p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: decision.Mode, Input: decision.Input, Allowed: true, Reason: "user_approved_always"})
		return nil
	}
	p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: decision.Mode, Input: decision.Input, Allowed: false, Reason: "user_denied"})
	return fmt.Errorf("permission denied for tool %s", name)
}

func (p *Prompter) Decide(name string, required Permission, input json.RawMessage) PermissionDecision {
	mode := p.Mode
	if mode == "" {
		mode = PermissionWorkspace
	}
	inputText := string(input)
	decision := PermissionDecision{ToolName: name, Required: required, Mode: mode, Input: inputText}
	if ruleMatchesTool(p.DeniedTools, name) {
		decision.Reason = "denied_tools"
		return decision
	}
	if ruleMatches(p.DenyRules, name, inputText) {
		decision.Reason = "deny_rule"
		return decision
	}
	if ruleMatches(p.AllowRules, name, inputText) {
		decision.Allowed = true
		decision.Reason = "allow_rule"
		return decision
	}
	validationWarning := ""
	if strings.EqualFold(name, "bash") {
		result := bashvalidation.Validate(bashvalidation.CommandFromInput(input), string(mode), p.Workspace)
		switch result.Severity {
		case bashvalidation.SeverityBlock:
			decision.Reason = "bash_validation"
			decision.Message = result.Reason
			return decision
		case bashvalidation.SeverityConfirm:
			validationWarning = result.Reason
		case bashvalidation.SeverityAllow:
			if mode == PermissionReadOnly && result.Intent == bashvalidation.IntentReadOnly && !ruleMatches(p.AskRules, name, inputText) {
				decision.Allowed = true
				decision.Reason = "bash_validation_read_only"
				return decision
			}
		}
	}
	ask := mode == PermissionPrompt || ruleMatches(p.AskRules, name, inputText)
	if validationWarning != "" && mode != PermissionAllow {
		ask = true
	}
	if !ask && (mode == PermissionAllow || permissionRank(mode) >= permissionRank(required)) {
		decision.Allowed = true
		decision.Reason = "permission_mode"
		return decision
	}
	decision.WouldPrompt = true
	decision.Reason = "requires_confirmation"
	decision.Message = validationWarning
	return decision
}

func permissionDecisionError(decision PermissionDecision) error {
	switch decision.Reason {
	case "denied_tools":
		return fmt.Errorf("permission denied for tool %s by denied_tools", decision.ToolName)
	case "deny_rule":
		return fmt.Errorf("permission denied for tool %s by deny rule", decision.ToolName)
	case "bash_validation":
		if decision.Message != "" {
			return fmt.Errorf("permission denied for tool %s by bash validation: %s", decision.ToolName, decision.Message)
		}
		return fmt.Errorf("permission denied for tool %s by bash validation", decision.ToolName)
	default:
		return fmt.Errorf("permission denied for tool %s", decision.ToolName)
	}
}

func (p *Prompter) emitDecision(decision PermissionDecision) {
	if p.OnDecision != nil {
		p.OnDecision(decision)
	}
}

func (p *Prompter) emitRequest(decision PermissionDecision) {
	if p.OnRequest != nil {
		p.OnRequest(decision)
	}
}

func ruleMatches(rules []string, toolName, input string) bool {
	for _, rule := range rules {
		toolRule, needle := parsePermissionRule(rule)
		if !permissionToolMatches(toolRule, toolName) {
			continue
		}
		if needle == "" || strings.Contains(input, needle) {
			return true
		}
	}
	return false
}

func ruleMatchesTool(rules []string, toolName string) bool {
	for _, rule := range rules {
		toolRule, _ := parsePermissionRule(rule)
		if permissionToolMatches(toolRule, toolName) {
			return true
		}
	}
	return false
}

func parsePermissionRule(rule string) (string, string) {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return "", ""
	}
	if open := strings.Index(rule, "("); open > 0 && strings.HasSuffix(rule, ")") {
		tool := strings.TrimSpace(rule[:open])
		needle := normalizePermissionNeedle(rule[open+1 : len(rule)-1])
		return tool, needle
	}
	if tool, needle, ok := strings.Cut(rule, ":"); ok {
		return strings.TrimSpace(tool), normalizePermissionNeedle(needle)
	}
	return rule, ""
}

func normalizePermissionNeedle(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, "*")
	value = strings.TrimSuffix(value, ":")
	return strings.TrimSpace(value)
}

func permissionToolMatches(ruleTool string, toolName string) bool {
	ruleTool = strings.TrimSpace(ruleTool)
	toolName = strings.TrimSpace(toolName)
	if ruleTool == "" || toolName == "" {
		return false
	}
	if ruleTool == "*" {
		return true
	}
	candidates := []string{
		ruleTool,
		CanonicalToolName(ruleTool),
	}
	targets := []string{
		toolName,
		CanonicalToolName(toolName),
	}
	for _, candidate := range candidates {
		for _, target := range targets {
			if permissionNameMatches(candidate, target) {
				return true
			}
		}
	}
	return false
}

func permissionNameMatches(pattern string, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" || value == "" {
		return false
	}
	if pattern == "*" || pattern == value {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		next := strings.Index(value[position:], part)
		if next < 0 {
			return false
		}
		if index == 0 && !strings.HasPrefix(pattern, "*") && next != 0 {
			return false
		}
		position += next + len(part)
	}
	if !strings.HasSuffix(pattern, "*") && len(parts) > 0 {
		last := parts[len(parts)-1]
		if last != "" && !strings.HasSuffix(value, last) {
			return false
		}
	}
	return true
}

func permissionRank(p Permission) int {
	switch p {
	case PermissionReadOnly:
		return 1
	case PermissionWorkspace:
		return 2
	case PermissionDanger:
		return 3
	case PermissionAllow:
		return 4
	default:
		return 0
	}
}

func (t CommandTool) Definition() anthropic.ToolDefinition {
	schema := t.Schema
	if schema == nil {
		schema = map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}
	}
	return anthropic.ToolDefinition{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: schema,
	}
}

func (t CommandTool) Permission() Permission {
	if t.Required == "" {
		return PermissionWorkspace
	}
	return t.Required
}

func (t CommandTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if strings.TrimSpace(t.Command) == "" {
		return "", fmt.Errorf("plugin tool %s has no command", t.Name)
	}
	cmd := exec.CommandContext(ctx, t.Command, t.Args...)
	cmd.Dir = t.Workspace
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := map[string]any{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return pretty(result), nil
}

func NewMCPToolName(serverName, toolName string) string {
	return "mcp__" + toolNameComponent(serverName, "server") + "__" + toolNameComponent(toolName, "tool")
}

func toolNameComponent(value, fallback string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	component := strings.Trim(builder.String(), "_-")
	if component == "" {
		return fallback
	}
	return component
}

func (t MCPTool) Definition() anthropic.ToolDefinition {
	schema := t.Schema
	if schema == nil {
		schema = map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}
	}
	description := t.Description
	if description == "" {
		description = fmt.Sprintf("Call MCP tool %s on server %s.", t.RemoteName, t.ServerName)
	}
	return anthropic.ToolDefinition{
		Name:        t.Name,
		Description: description,
		InputSchema: schema,
	}
}

func (t MCPTool) Permission() Permission {
	if t.Required == "" {
		return PermissionWorkspace
	}
	return t.Required
}

func (t MCPTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	result := mcp.CallTool(ctx, t.ServerName, t.Server, t.RemoteName, input)
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	if len(result.Result) == 0 {
		return "{}", nil
	}
	return string(result.Result), nil
}

type MCPDispatchTool struct {
	Servers map[string]config.MCPServerConfig
}

type mcpDispatchInput struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func (MCPDispatchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "mcp",
		Description: "Call a tool on a configured MCP server by server and tool name.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Configured MCP server name.",
				},
				"tool": map[string]any{
					"type":        "string",
					"description": "Remote MCP tool name to call.",
				},
				"arguments": map[string]any{
					"type":                 "object",
					"description":          "Arguments passed to the remote MCP tool.",
					"additionalProperties": true,
				},
			},
			"required": []string{"server", "tool"},
		},
	}
}

func (MCPDispatchTool) Permission() Permission {
	return PermissionWorkspace
}

func (t MCPDispatchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload mcpDispatchInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Server) == "" {
		return "", errors.New("server is required")
	}
	if strings.TrimSpace(payload.Tool) == "" {
		return "", errors.New("tool is required")
	}
	server, ok := t.Servers[payload.Server]
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q", payload.Server)
	}
	if len(payload.Arguments) == 0 {
		payload.Arguments = json.RawMessage(`{}`)
	}
	result := mcp.CallTool(ctx, payload.Server, server, payload.Tool, payload.Arguments)
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	if len(result.Result) == 0 {
		return "{}", nil
	}
	return string(result.Result), nil
}

type MCPAuthTool struct {
	Servers map[string]config.MCPServerConfig
}

type mcpAuthInput struct {
	Server string `json:"server"`
}

func (MCPAuthTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "mcp_auth",
		Description: "Inspect authentication and readiness status for a configured MCP server.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Configured MCP server name.",
				},
			},
			"required": []string{"server"},
		},
	}
}

func (MCPAuthTool) Permission() Permission {
	return PermissionDanger
}

func (t MCPAuthTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload mcpAuthInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Server) == "" {
		return "", errors.New("server is required")
	}
	server, ok := t.Servers[payload.Server]
	if !ok {
		return pretty(map[string]any{
			"server": payload.Server,
			"status": "unknown",
			"error":  "server is not configured",
		}), nil
	}
	return pretty(mcp.InspectAuth(ctx, payload.Server, server)), nil
}

type ListMCPResourcesTool struct {
	Servers map[string]config.MCPServerConfig
}

type listMCPResourcesInput struct {
	Server string `json:"server,omitempty"`
}

func (t ListMCPResourcesTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "list_mcp_resources",
		Description: "List resources exposed by configured MCP servers. Pass server to query one server, or omit it to query all configured servers.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Optional MCP server name. When omitted, all configured servers are queried.",
				},
			},
		},
	}
}

func (ListMCPResourcesTool) Permission() Permission {
	return PermissionReadOnly
}

func (t ListMCPResourcesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload listMCPResourcesInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	if payload.Server != "" {
		server, ok := t.Servers[payload.Server]
		if !ok {
			return "", fmt.Errorf("unknown MCP server %q", payload.Server)
		}
		result := mcp.ListResources(ctx, payload.Server, server)
		if result.Error != "" {
			return "", errors.New(result.Error)
		}
		return pretty(result), nil
	}

	names := sortedMCPServerNames(t.Servers)
	results := make([]mcp.ResourceListResult, 0, len(names))
	for _, name := range names {
		results = append(results, mcp.ListResources(ctx, name, t.Servers[name]))
	}
	return pretty(map[string]any{
		"kind":    "mcp_resources",
		"servers": results,
		"total":   len(results),
	}), nil
}

type ReadMCPResourceTool struct {
	Servers map[string]config.MCPServerConfig
}

type readMCPResourceInput struct {
	Server string `json:"server"`
	URI    string `json:"uri"`
}

func (t ReadMCPResourceTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "read_mcp_resource",
		Description: "Read a resource URI exposed by a configured MCP server.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Configured MCP server name.",
				},
				"uri": map[string]any{
					"type":        "string",
					"description": "Resource URI returned by list_mcp_resources.",
				},
			},
			"required": []string{"server", "uri"},
		},
	}
}

func (ReadMCPResourceTool) Permission() Permission {
	return PermissionReadOnly
}

func (t ReadMCPResourceTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload readMCPResourceInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Server) == "" {
		return "", errors.New("server is required")
	}
	if strings.TrimSpace(payload.URI) == "" {
		return "", errors.New("uri is required")
	}
	server, ok := t.Servers[payload.Server]
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q", payload.Server)
	}
	result := mcp.ReadResource(ctx, payload.Server, server, payload.URI)
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	return pretty(result), nil
}

type ListMCPResourceTemplatesTool struct {
	Servers map[string]config.MCPServerConfig
}

func (t ListMCPResourceTemplatesTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "list_mcp_resource_templates",
		Description: "List resource templates exposed by configured MCP servers. Pass server to query one server, or omit it to query all configured servers.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Optional MCP server name. When omitted, all configured servers are queried.",
				},
			},
		},
	}
}

func (ListMCPResourceTemplatesTool) Permission() Permission { return PermissionReadOnly }

func (t ListMCPResourceTemplatesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload listMCPResourcesInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	if payload.Server != "" {
		server, ok := t.Servers[payload.Server]
		if !ok {
			return "", fmt.Errorf("unknown MCP server %q", payload.Server)
		}
		result := mcp.ListResourceTemplates(ctx, payload.Server, server)
		if result.Error != "" {
			return "", errors.New(result.Error)
		}
		return pretty(result), nil
	}
	names := sortedMCPServerNames(t.Servers)
	results := make([]mcp.ResourceTemplateListResult, 0, len(names))
	for _, name := range names {
		results = append(results, mcp.ListResourceTemplates(ctx, name, t.Servers[name]))
	}
	return pretty(map[string]any{"kind": "mcp_resource_templates", "servers": results, "total": len(results)}), nil
}

type ListMCPPromptsTool struct {
	Servers map[string]config.MCPServerConfig
}

func (t ListMCPPromptsTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "list_mcp_prompts",
		Description: "List prompts exposed by configured MCP servers. Pass server to query one server, or omit it to query all configured servers.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Optional MCP server name. When omitted, all configured servers are queried.",
				},
			},
		},
	}
}

func (ListMCPPromptsTool) Permission() Permission { return PermissionReadOnly }

func (t ListMCPPromptsTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload listMCPResourcesInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	if payload.Server != "" {
		server, ok := t.Servers[payload.Server]
		if !ok {
			return "", fmt.Errorf("unknown MCP server %q", payload.Server)
		}
		result := mcp.ListPrompts(ctx, payload.Server, server)
		if result.Error != "" {
			return "", errors.New(result.Error)
		}
		return pretty(result), nil
	}
	names := sortedMCPServerNames(t.Servers)
	results := make([]mcp.PromptListResult, 0, len(names))
	for _, name := range names {
		results = append(results, mcp.ListPrompts(ctx, name, t.Servers[name]))
	}
	return pretty(map[string]any{"kind": "mcp_prompts", "servers": results, "total": len(results)}), nil
}

type GetMCPPromptTool struct {
	Servers map[string]config.MCPServerConfig
}

func (t GetMCPPromptTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "get_mcp_prompt",
		Description: "Read a prompt exposed by a configured MCP server.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server":    map[string]any{"type": "string"},
				"prompt":    map[string]any{"type": "string"},
				"arguments": map[string]any{"type": "object", "additionalProperties": true},
			},
			"required": []string{"server", "prompt"},
		},
	}
}

func (GetMCPPromptTool) Permission() Permission { return PermissionReadOnly }

func (t GetMCPPromptTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Server    string          `json:"server"`
		Prompt    string          `json:"prompt"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Server) == "" {
		return "", errors.New("server is required")
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	server, ok := t.Servers[payload.Server]
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q", payload.Server)
	}
	result := mcp.GetPrompt(ctx, payload.Server, server, payload.Prompt, payload.Arguments)
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	return pretty(result), nil
}

func sortedMCPServerNames(servers map[string]config.MCPServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type GitStatusTool struct {
	Workspace string
}

func (GitStatusTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_status",
		Description: "Show working tree status with structured JSON output.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"short": map[string]any{"type": "boolean", "description": "Use --short --branch output. Defaults to true."},
			},
		},
	}
}

func (GitStatusTool) Permission() Permission { return PermissionReadOnly }

func (t GitStatusTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Short *bool `json:"short,omitempty"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	args := []string{"status"}
	if payload.Short == nil || *payload.Short {
		args = append(args, "--short", "--branch")
	}
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

type GitDiffTool struct {
	Workspace string
}

func (GitDiffTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_diff",
		Description: "Show git diff output for the working tree, index, commits, and optional path filters.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"staged":  map[string]any{"type": "boolean"},
				"commit":  map[string]any{"type": "string"},
				"commit2": map[string]any{"type": "string"},
			},
		},
	}
}

func (GitDiffTool) Permission() Permission { return PermissionReadOnly }

func (t GitDiffTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path    string `json:"path,omitempty"`
		Staged  bool   `json:"staged,omitempty"`
		Commit  string `json:"commit,omitempty"`
		Commit2 string `json:"commit2,omitempty"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	args := []string{"diff"}
	if payload.Staged {
		args = append(args, "--cached")
	}
	if strings.TrimSpace(payload.Commit) != "" {
		commit, err := safeGitRef(payload.Commit, "commit")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(payload.Commit2) != "" {
			commit2, err := safeGitRef(payload.Commit2, "commit2")
			if err != nil {
				return "", err
			}
			args = append(args, commit+"..."+commit2)
		} else {
			args = append(args, commit)
		}
	}
	if strings.TrimSpace(payload.Path) != "" {
		path, err := gitPathArg(t.Workspace, payload.Path, true)
		if err != nil {
			return "", err
		}
		args = append(args, "--", path)
	}
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

type GitLogTool struct {
	Workspace string
}

func (GitLogTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_log",
		Description: "Show commit history with optional count, author, date, and path filters.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"count":   map[string]any{"type": "integer", "minimum": 1},
				"oneline": map[string]any{"type": "boolean"},
				"author":  map[string]any{"type": "string"},
				"since":   map[string]any{"type": "string"},
				"until":   map[string]any{"type": "string"},
			},
		},
	}
}

func (GitLogTool) Permission() Permission { return PermissionReadOnly }

func (t GitLogTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path    string `json:"path,omitempty"`
		Count   int    `json:"count,omitempty"`
		Oneline bool   `json:"oneline,omitempty"`
		Author  string `json:"author,omitempty"`
		Since   string `json:"since,omitempty"`
		Until   string `json:"until,omitempty"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	count := payload.Count
	if count <= 0 {
		count = 20
	}
	args := []string{"log", fmt.Sprintf("-n%d", count)}
	if payload.Oneline {
		args = append(args, "--oneline")
	}
	if strings.TrimSpace(payload.Author) != "" {
		args = append(args, "--author="+payload.Author)
	}
	if strings.TrimSpace(payload.Since) != "" {
		args = append(args, "--since="+payload.Since)
	}
	if strings.TrimSpace(payload.Until) != "" {
		args = append(args, "--until="+payload.Until)
	}
	if strings.TrimSpace(payload.Path) != "" {
		path, err := gitPathArg(t.Workspace, payload.Path, true)
		if err != nil {
			return "", err
		}
		args = append(args, "--", path)
	}
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

type GitShowTool struct {
	Workspace string
}

func (GitShowTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_show",
		Description: "Show a commit, tag, or tree object in patch, stat, or metadata format.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"commit": map[string]any{"type": "string"},
				"path":   map[string]any{"type": "string"},
				"stat":   map[string]any{"type": "boolean"},
				"format": map[string]any{"type": "string", "enum": []string{"patch", "stat", "metadata"}},
			},
			"required": []string{"commit"},
		},
	}
}

func (GitShowTool) Permission() Permission { return PermissionReadOnly }

func (t GitShowTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Commit string `json:"commit"`
		Path   string `json:"path,omitempty"`
		Stat   bool   `json:"stat,omitempty"`
		Format string `json:"format,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	commit, err := safeGitRef(payload.Commit, "commit")
	if err != nil {
		return "", err
	}
	args := []string{"show"}
	switch strings.TrimSpace(payload.Format) {
	case "metadata":
		if strings.TrimSpace(payload.Path) != "" {
			return "", errors.New(`git_show format "metadata" cannot be combined with path`)
		}
		args = append(args, "--format=medium", "--no-patch")
	case "stat":
		args = append(args, "--stat")
	case "", "patch":
		if payload.Format == "" && payload.Stat {
			args = append(args, "--stat")
		}
	default:
		return "", fmt.Errorf("unknown git_show format %q", payload.Format)
	}
	if strings.TrimSpace(payload.Path) != "" {
		path, err := gitPathArg(t.Workspace, payload.Path, true)
		if err != nil {
			return "", err
		}
		args = append(args, commit+":"+path)
	} else {
		args = append(args, commit)
	}
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

type GitBlameTool struct {
	Workspace string
}

func (GitBlameTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "git_blame",
		Description: "Show revision and author information for each line of a file.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":       map[string]any{"type": "string"},
				"start_line": map[string]any{"type": "integer", "minimum": 1},
				"end_line":   map[string]any{"type": "integer", "minimum": 1},
			},
			"required": []string{"path"},
		},
	}
}

func (GitBlameTool) Permission() Permission { return PermissionReadOnly }

func (t GitBlameTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line,omitempty"`
		EndLine   int    `json:"end_line,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := gitPathArg(t.Workspace, payload.Path, false)
	if err != nil {
		return "", err
	}
	args := []string{"blame"}
	if payload.StartLine > 0 && payload.EndLine > 0 {
		if payload.EndLine < payload.StartLine {
			return "", errors.New("end_line must be greater than or equal to start_line")
		}
		args = append(args, fmt.Sprintf("-L%d,%d", payload.StartLine, payload.EndLine))
	}
	args = append(args, "--", path)
	output, err := gitops.Run(t.Workspace, args...)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"output": output}), nil
}

func safeGitRef(value, field string) (string, error) {
	ref := strings.TrimSpace(value)
	if ref == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsRune(ref, '\x00') {
		return "", fmt.Errorf("%s is not a safe git ref", field)
	}
	return ref, nil
}

func gitPathArg(workspace, requested string, allowMissing bool) (string, error) {
	path, err := safePath(workspace, requested, allowMissing)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace scope: %s", requested)
	}
	return filepath.ToSlash(rel), nil
}

type BashTool struct {
	Workspace       string
	ConfigHome      string
	SandboxStrategy string
}

func (BashTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "bash",
		Description: "Execute a shell command in the current workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":           map[string]any{"type": "string"},
				"timeout":           map[string]any{"type": "integer", "minimum": 1},
				"timeout_ms":        map[string]any{"type": "integer", "minimum": 1},
				"description":       map[string]any{"type": "string"},
				"run_in_background": map[string]any{"type": "boolean"},
				"dangerouslyDisableSandbox": map[string]any{
					"type":        "boolean",
					"description": "Claude-compatible per-call sandbox bypass.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (BashTool) Permission() Permission { return PermissionDanger }

func toolEnvironment(ctx context.Context, configHome string) ([]string, error) {
	env := os.Environ()
	hookEnv, err := hookenv.Load(configHome, SessionIDFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return hookenv.Merge(env, hookEnv), nil
}

func toolCWD(ctx context.Context, configHome string, workspace string) (string, error) {
	return shellstate.CurrentCWD(configHome, SessionIDFromContext(ctx), workspace)
}

func wrapCommandWithCWDProbe(command string, cwdFile string) string {
	cwdFile = strings.TrimSpace(cwdFile)
	if cwdFile == "" {
		return command
	}
	return command + "\n__codog_status=$?\npwd -P > " + shellQuoteToolArg(cwdFile) + "\nexit $__codog_status"
}

func (t BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command                   string `json:"command"`
		Timeout                   int    `json:"timeout"`
		TimeoutMS                 int    `json:"timeout_ms"`
		RunInBackground           bool   `json:"run_in_background"`
		DangerouslyDisableSandbox bool   `json:"dangerouslyDisableSandbox"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Command) == "" {
		return "", errors.New("command is required")
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	commandText := payload.Command
	cwdProbePath := ""
	if SessionIDFromContext(ctx) != "" && strings.TrimSpace(t.ConfigHome) != "" && !payload.RunInBackground {
		probe, err := os.CreateTemp("", "codog-cwd-*.txt")
		if err != nil {
			return "", err
		}
		cwdProbePath = probe.Name()
		_ = probe.Close()
		defer os.Remove(cwdProbePath)
		commandText = wrapCommandWithCWDProbe(commandText, cwdProbePath)
	}
	strategy := t.SandboxStrategy
	if payload.DangerouslyDisableSandbox {
		strategy = "off"
	}
	command, args, effectiveSandbox, err := sandbox.ShellCommand(strategy, t.Workspace, commandText)
	if err != nil {
		return "", err
	}
	if payload.RunInBackground {
		env, err := toolEnvironment(ctx, t.ConfigHome)
		if err != nil {
			return "", err
		}
		task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(shellCommandLine(command, args), cwd, background.RunOptions{Kind: "bash", Env: env})
		if err != nil {
			return "", err
		}
		result := map[string]any{"background": true, "task": task}
		if effectiveSandbox != "" {
			result["sandbox"] = effectiveSandbox
		}
		return pretty(result), nil
	}
	timeoutMS := payload.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = payload.Timeout
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = cwd
	env, err := toolEnvironment(ctx, t.ConfigHome)
	if err != nil {
		return "", err
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	finalCWD := cwd
	cwdChanged := false
	if cwdProbePath != "" {
		if data, readErr := os.ReadFile(cwdProbePath); readErr == nil {
			if saved, saveErr := shellstate.SaveCWD(t.ConfigHome, SessionIDFromContext(ctx), strings.TrimSpace(string(data))); saveErr == nil && saved != "" {
				finalCWD = saved
				cwdChanged = finalCWD != cwd
			}
		}
	}
	result := map[string]any{
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"exit_code":   exitCode(err),
		"duration_ms": time.Since(started).Milliseconds(),
		"cwd":         finalCWD,
	}
	if cwdChanged {
		result["old_cwd"] = cwd
		result["cwd_changed"] = true
	}
	if effectiveSandbox != "" {
		result["sandbox"] = effectiveSandbox
	}
	if ctx.Err() == context.DeadlineExceeded {
		result["interrupted"] = true
		result["error"] = "timeout"
		result["exit_code"] = -1
		return pretty(result), nil
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return pretty(result), nil
}

type BashOutputTool struct {
	Workspace  string
	ConfigHome string
}

func (BashOutputTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "bash_output",
		Description: "Read recent output from a background bash task started by the bash tool.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bash_id":     map[string]any{"type": "string"},
				"task_id":     map[string]any{"type": "string"},
				"id":          map[string]any{"type": "string"},
				"limit_bytes": map[string]any{"type": "integer", "minimum": 1},
			},
			"additionalProperties": false,
		},
	}
}

func (BashOutputTool) Permission() Permission { return PermissionReadOnly }

func (t BashOutputTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		BashID     string `json:"bash_id"`
		TaskID     string `json:"task_id"`
		ID         string `json:"id"`
		LimitBytes int64  `json:"limit_bytes"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id, err := bashTaskID(payload.BashID, payload.TaskID, payload.ID)
	if err != nil {
		return "", err
	}
	if payload.LimitBytes <= 0 {
		payload.LimitBytes = 64 * 1024
	}
	store := taskStore(t.ConfigHome, t.Workspace)
	task, err := store.Status(id)
	if err != nil {
		return "", err
	}
	if err := requireBashTask(task); err != nil {
		return "", err
	}
	output, err := store.Logs(id, payload.LimitBytes)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"bash_id": id,
		"id":      id,
		"status":  task.Status,
		"output":  output,
		"task":    task,
	}), nil
}

type KillBashTool struct {
	Workspace  string
	ConfigHome string
}

func (KillBashTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "kill_bash",
		Description: "Stop a running background bash task by id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bash_id": map[string]any{"type": "string"},
				"task_id": map[string]any{"type": "string"},
				"id":      map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
}

func (KillBashTool) Permission() Permission { return PermissionWorkspace }

func (t KillBashTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		BashID string `json:"bash_id"`
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id, err := bashTaskID(payload.BashID, payload.TaskID, payload.ID)
	if err != nil {
		return "", err
	}
	store := taskStore(t.ConfigHome, t.Workspace)
	task, err := store.Status(id)
	if err != nil {
		return "", err
	}
	if err := requireBashTask(task); err != nil {
		return "", err
	}
	task, err = store.Stop(id)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"bash_id": id,
		"id":      id,
		"status":  task.Status,
		"task":    task,
	}), nil
}

func bashTaskID(values ...string) (string, error) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value, nil
		}
	}
	return "", errors.New("bash_id is required")
}

func requireBashTask(task background.Task) error {
	if task.Kind != "bash" {
		return fmt.Errorf("task %s is not a bash task", task.ID)
	}
	return nil
}

func shellCommandLine(command string, args []string) string {
	parts := []string{shellQuote(command)}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

type PowerShellTool struct {
	Workspace  string
	ConfigHome string
	Executable string
}

func (PowerShellTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "powershell",
		Description: "Execute a PowerShell command in the current workspace, optionally as a background task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":           map[string]any{"type": "string"},
				"timeout":           map[string]any{"type": "integer", "minimum": 1},
				"description":       map[string]any{"type": "string"},
				"run_in_background": map[string]any{"type": "boolean"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (PowerShellTool) Permission() Permission { return PermissionDanger }

func (t PowerShellTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command         string `json:"command"`
		TimeoutSeconds  int    `json:"timeout"`
		RunInBackground bool   `json:"run_in_background"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Command) == "" {
		return "", errors.New("command is required")
	}
	executable, err := t.powerShellExecutable()
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	if payload.RunInBackground {
		command := strings.Join([]string{shellQuoteToolArg(executable), "-NoProfile", "-Command", shellQuoteToolArg(payload.Command)}, " ")
		env, err := toolEnvironment(ctx, t.ConfigHome)
		if err != nil {
			return "", err
		}
		task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(command, cwd, background.RunOptions{Kind: "powershell", Env: env})
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"background": true, "task": task}), nil
	}
	timeout := time.Duration(payload.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if timeout > 30*time.Minute {
		return "", errors.New("timeout must be 1800 seconds or less")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, executable, "-NoProfile", "-Command", payload.Command)
	cmd.Dir = cwd
	env, err := toolEnvironment(ctx, t.ConfigHome)
	if err != nil {
		return "", err
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	result := map[string]any{
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"exit_code":   exitCode(err),
		"duration_ms": time.Since(started).Milliseconds(),
	}
	if ctx.Err() == context.DeadlineExceeded {
		result["interrupted"] = true
		result["error"] = "timeout"
		result["exit_code"] = -1
		return pretty(result), nil
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return pretty(result), nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (t PowerShellTool) powerShellExecutable() (string, error) {
	if strings.TrimSpace(t.Executable) != "" {
		return t.Executable, nil
	}
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("powershell"); err == nil {
		return path, nil
	}
	return "", errors.New("PowerShell executable not found (expected `pwsh` or `powershell` in PATH)")
}

type ReadFileTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (ReadFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Claude-compatible alias for path.",
				},
				"offset": map[string]any{"type": "integer", "minimum": 0},
				"limit":  map[string]any{"type": "integer", "minimum": 1},
			},
			"anyOf":                pathOrFilePathRequirement(),
			"additionalProperties": false,
		},
	}
}

func (ReadFileTool) Permission() Permission { return PermissionReadOnly }

func (t ReadFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, firstNonEmpty(payload.Path, payload.FilePath), false)
	if err != nil {
		return "", err
	}
	data, truncated, err := readFileLimited(path, maxFileToolBytes)
	if err != nil {
		return "", err
	}
	if mediaType, ok := imageMediaType(path, data); ok {
		if truncated {
			return "", fmt.Errorf("image exceeds maximum readable size of %d bytes", maxFileToolBytes)
		}
		return pretty(imageReadResult(path, data, mediaType)), nil
	}
	if bytes.Contains(data[:min(len(data), 8192)], []byte{0}) {
		return "", errors.New("file appears to be binary")
	}
	lines := strings.Split(string(data), "\n")
	start := min(max(payload.Offset, 0), len(lines))
	end := len(lines)
	if payload.Limit > 0 {
		end = min(start+payload.Limit, len(lines))
	}
	return pretty(map[string]any{
		"path":       path,
		"start_line": start + 1,
		"total":      len(lines),
		"bytes":      len(data),
		"truncated":  truncated,
		"content":    strings.Join(lines[start:end], "\n"),
	}), nil
}

type WriteFileTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (WriteFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "write_file",
		Description: "Create or overwrite a UTF-8 text file in the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Claude-compatible alias for path.",
				},
				"content": map[string]any{"type": "string"},
			},
			"required":             []string{"content"},
			"anyOf":                pathOrFilePathRequirement(),
			"additionalProperties": false,
		},
	}
}

func (WriteFileTool) Permission() Permission { return PermissionWorkspace }

func (t WriteFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if int64(len(payload.Content)) > maxFileToolBytes {
		return "", fmt.Errorf("content exceeds maximum file tool size of %d bytes", maxFileToolBytes)
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, firstNonEmpty(payload.Path, payload.FilePath), true)
	if err != nil {
		return "", err
	}
	existed, original, undoAvailable, err := fileUndoSnapshot(path)
	if err != nil {
		return "", err
	}
	undoID := ""
	if undoAvailable {
		record, err := undo.Push(t.Workspace, "write_file", path, existed, original)
		if err != nil {
			return "", err
		}
		undoID = record.ID
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	kind := "update"
	if !existed {
		kind = "create"
	}
	if err := os.WriteFile(path, []byte(payload.Content), 0o644); err != nil {
		return "", err
	}
	result := map[string]any{"path": path, "kind": kind, "bytes": len(payload.Content)}
	addUndoFields(result, undoAvailable, undoID)
	return pretty(result), nil
}

type EditFileTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (EditFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace text in a workspace file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Claude-compatible alias for path.",
				},
				"old_string":  map[string]any{"type": "string"},
				"new_string":  map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			},
			"required":             []string{"old_string", "new_string"},
			"anyOf":                pathOrFilePathRequirement(),
			"additionalProperties": false,
		},
	}
}

func (EditFileTool) Permission() Permission { return PermissionWorkspace }

func (t EditFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path       string `json:"path"`
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.OldString == "" {
		return "", errors.New("old_string is required")
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, firstNonEmpty(payload.Path, payload.FilePath), false)
	if err != nil {
		return "", err
	}
	data, truncated, err := readFileLimited(path, maxFileToolBytes)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", fmt.Errorf("file exceeds maximum editable size of %d bytes", maxFileToolBytes)
	}
	content := string(data)
	count := strings.Count(content, payload.OldString)
	if count == 0 {
		return "", errors.New("old_string not found")
	}
	if !payload.ReplaceAll && count > 1 {
		return "", fmt.Errorf("old_string appears %d times; set replace_all to true or provide more context", count)
	}
	next := strings.Replace(content, payload.OldString, payload.NewString, 1)
	if payload.ReplaceAll {
		next = strings.ReplaceAll(content, payload.OldString, payload.NewString)
	}
	record, err := undo.Push(t.Workspace, "edit_file", path, true, data)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return "", err
	}
	replaced := 1
	if payload.ReplaceAll {
		replaced = count
	}
	return pretty(map[string]any{"path": path, "replacements": replaced, "undo_available": true, "undo_id": record.ID}), nil
}

type MultiEditTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (MultiEditTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "multi_edit",
		Description: "Apply multiple text replacements to one workspace file atomically.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Claude-compatible alias for path.",
				},
				"edits": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"old_string":  map[string]any{"type": "string"},
							"new_string":  map[string]any{"type": "string"},
							"replace_all": map[string]any{"type": "boolean"},
						},
						"required":             []string{"old_string", "new_string"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"edits"},
			"anyOf":                pathOrFilePathRequirement(),
			"additionalProperties": false,
		},
	}
}

func (MultiEditTool) Permission() Permission { return PermissionWorkspace }

func (t MultiEditTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Edits    []struct {
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if len(payload.Edits) == 0 {
		return "", errors.New("edits are required")
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, firstNonEmpty(payload.Path, payload.FilePath), false)
	if err != nil {
		return "", err
	}
	data, truncated, err := readFileLimited(path, maxFileToolBytes)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", fmt.Errorf("file exceeds maximum editable size of %d bytes", maxFileToolBytes)
	}
	content := string(data)
	total := 0
	for index, edit := range payload.Edits {
		if edit.OldString == "" {
			return "", fmt.Errorf("edits[%d].old_string is required", index)
		}
		count := strings.Count(content, edit.OldString)
		if count == 0 {
			return "", fmt.Errorf("edits[%d].old_string not found", index)
		}
		if !edit.ReplaceAll && count > 1 {
			return "", fmt.Errorf("edits[%d].old_string appears %d times; set replace_all to true or provide more context", index, count)
		}
		replacements := 1
		if edit.ReplaceAll {
			replacements = count
			content = strings.ReplaceAll(content, edit.OldString, edit.NewString)
		} else {
			content = strings.Replace(content, edit.OldString, edit.NewString, 1)
		}
		total += replacements
	}
	record, err := undo.Push(t.Workspace, "multi_edit", path, true, data)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return pretty(map[string]any{"path": path, "edits": len(payload.Edits), "replacements": total, "undo_available": true, "undo_id": record.ID}), nil
}

func fileUndoSnapshot(path string) (bool, []byte, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil, true, nil
		}
		return false, nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileToolBytes {
		return true, nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil, false, err
	}
	return true, data, true, nil
}

func addUndoFields(result map[string]any, available bool, id string) {
	result["undo_available"] = available
	if id != "" {
		result["undo_id"] = id
	}
}

func pathOrFilePathRequirement() []map[string]any {
	return []map[string]any{
		{"required": []string{"path"}},
		{"required": []string{"file_path"}},
	}
}

type GrepTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (GrepTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents with a regular expression.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string"},
				"path":        map[string]any{"type": "string"},
				"glob":        map[string]any{"type": "string"},
				"output_mode": map[string]any{"type": "string", "enum": []string{"content", "files_with_matches", "count"}},
				"-B":          map[string]any{"type": "integer", "minimum": 0},
				"-A":          map[string]any{"type": "integer", "minimum": 0},
				"-C":          map[string]any{"type": "integer", "minimum": 0},
				"context":     map[string]any{"type": "integer", "minimum": 0},
				"-n":          map[string]any{"type": "boolean"},
				"-i":          map[string]any{"type": "boolean"},
				"ignore_case": map[string]any{"type": "boolean"},
				"type":        map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer", "minimum": 1},
				"head_limit":  map[string]any{"type": "integer", "minimum": 0},
				"offset":      map[string]any{"type": "integer", "minimum": 0},
				"multiline":   map[string]any{"type": "boolean"},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (GrepTool) Permission() Permission { return PermissionReadOnly }

func (t GrepTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Pattern        string `json:"pattern"`
		Path           string `json:"path"`
		Glob           string `json:"glob"`
		OutputMode     string `json:"output_mode"`
		Before         int    `json:"-B"`
		After          int    `json:"-A"`
		ContextShort   int    `json:"-C"`
		Context        int    `json:"context"`
		LineNumbers    *bool  `json:"-n"`
		DashIgnoreCase bool   `json:"-i"`
		IgnoreCase     bool   `json:"ignore_case"`
		Type           string `json:"type"`
		Limit          int    `json:"limit"`
		HeadLimit      *int   `json:"head_limit"`
		Offset         int    `json:"offset"`
		Multiline      bool   `json:"multiline"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	pattern := payload.Pattern
	flags := ""
	if payload.IgnoreCase || payload.DashIgnoreCase {
		flags += "i"
	}
	if payload.Multiline {
		flags += "s"
	}
	if flags != "" {
		pattern = "(?" + flags + ")" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	root := t.Workspace
	if payload.Path != "" {
		root, err = safePathInScope(t.Workspace, t.AdditionalDirs, payload.Path, false)
		if err != nil {
			return "", err
		}
	}
	mode := strings.TrimSpace(payload.OutputMode)
	if mode == "" {
		mode = "files_with_matches"
	}
	if mode != "content" && mode != "files_with_matches" && mode != "count" {
		return "", fmt.Errorf("unsupported grep output_mode %q", payload.OutputMode)
	}
	limit, unlimited := grepLimit(payload.HeadLimit, payload.Limit)
	offset := max(payload.Offset, 0)
	contextLines := max(payload.Context, 0)
	if contextLines == 0 {
		contextLines = max(payload.ContextShort, 0)
	}
	beforeLines := max(payload.Before, 0)
	if beforeLines == 0 {
		beforeLines = contextLines
	}
	afterLines := max(payload.After, 0)
	if afterLines == 0 {
		afterLines = contextLines
	}
	lineNumbers := true
	if payload.LineNumbers != nil {
		lineNumbers = *payload.LineNumbers
	}
	seenFiles := map[string]bool{}
	counts := map[string]int{}
	var files []string
	contentFiles := map[string]bool{}
	var contentFilenames []string
	var contentLines []string
	var matches []map[string]any
	seen := 0
	walkRoot := root
	if payload.Glob != "" {
		walkRoot = deriveGlobWalkRoot(root, payload.Glob)
	}
	err = filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if mode == "content" && !unlimited && len(matches) >= limit {
			return filepath.SkipAll
		}
		if mode == "files_with_matches" && !unlimited && len(files) >= limit {
			return filepath.SkipAll
		}
		if mode == "count" && !unlimited && len(counts) >= offset+limit && limit > 0 {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if ignoredDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if payload.Glob != "" {
			rel, _ := filepath.Rel(root, path)
			if !globPatternMatches(payload.Glob, rel, filepath.Base(path)) {
				return nil
			}
		}
		if payload.Type != "" && !matchesGrepType(path, payload.Type) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.Contains(data[:min(len(data), 4096)], []byte{0}) {
			return nil
		}
		if payload.Multiline {
			display := displayPath(t.Workspace, path)
			text := string(data)
			locations := re.FindAllStringIndex(text, -1)
			if len(locations) == 0 {
				return nil
			}
			switch mode {
			case "files_with_matches":
				if !seenFiles[display] {
					seenFiles[display] = true
					if seen >= offset {
						files = append(files, display)
					}
					seen++
				}
				return nil
			case "count":
				counts[display] += len(locations)
				return nil
			default:
				lines := strings.Split(text, "\n")
				lineStarts := grepLineStartOffsets(text)
				for _, location := range locations {
					if seen >= offset {
						if !contentFiles[display] {
							contentFiles[display] = true
							contentFilenames = append(contentFilenames, display)
						}
						startLine := grepLineForOffset(lineStarts, location[0])
						endLine := grepLineForOffset(lineStarts, max(location[1]-1, location[0]))
						matchText := text[location[0]:location[1]]
						match := map[string]any{"path": display, "line": startLine + 1, "end_line": endLine + 1, "text": matchText}
						if beforeLines > 0 {
							before := grepContextLines(lines, startLine-beforeLines, startLine)
							match["before"] = before
							for _, entry := range before {
								contentLines = append(contentLines, formatGrepContentLine(display, entry.Line, entry.Text, lineNumbers))
							}
						}
						for _, entry := range grepContextLines(lines, startLine, endLine+1) {
							contentLines = append(contentLines, formatGrepContentLine(display, entry.Line, entry.Text, lineNumbers))
						}
						if afterLines > 0 {
							after := grepContextLines(lines, endLine+1, endLine+afterLines+2)
							match["after"] = after
							for _, entry := range after {
								contentLines = append(contentLines, formatGrepContentLine(display, entry.Line, entry.Text, lineNumbers))
							}
						}
						matches = append(matches, match)
					}
					seen++
					if !unlimited && len(matches) >= limit {
						return filepath.SkipAll
					}
				}
				return nil
			}
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				display := displayPath(t.Workspace, path)
				switch mode {
				case "files_with_matches":
					if !seenFiles[display] {
						seenFiles[display] = true
						if seen >= offset {
							files = append(files, display)
						}
						seen++
					}
					return nil
				case "count":
					counts[display]++
				default:
					if seen >= offset {
						match := map[string]any{"path": display, "line": i + 1, "text": line}
						if !contentFiles[display] {
							contentFiles[display] = true
							contentFilenames = append(contentFilenames, display)
						}
						if beforeLines > 0 {
							before := grepContextLines(lines, i-beforeLines, i)
							match["before"] = before
							for _, entry := range before {
								contentLines = append(contentLines, formatGrepContentLine(display, entry.Line, entry.Text, lineNumbers))
							}
						}
						contentLines = append(contentLines, formatGrepContentLine(display, i+1, line, lineNumbers))
						if afterLines > 0 {
							after := grepContextLines(lines, i+1, i+afterLines+1)
							match["after"] = after
							for _, entry := range after {
								contentLines = append(contentLines, formatGrepContentLine(display, entry.Line, entry.Text, lineNumbers))
							}
						}
						matches = append(matches, match)
					}
					seen++
					if !unlimited && len(matches) >= limit {
						return filepath.SkipAll
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	switch mode {
	case "files_with_matches":
		sort.Strings(files)
		return pretty(map[string]any{
			"output_mode":   mode,
			"mode":          mode,
			"files":         files,
			"filenames":     files,
			"num_files":     len(files),
			"numFiles":      len(files),
			"content":       nil,
			"numLines":      nil,
			"numMatches":    nil,
			"appliedLimit":  grepAppliedLimit(limit, unlimited),
			"appliedOffset": offset,
			"truncated":     !unlimited && len(files) >= limit,
			"offset":        offset,
		}), nil
	case "count":
		entries := grepCountEntries(counts, offset, limit)
		filenames := grepCountFilenames(entries)
		totalMatches := grepCountTotal(counts)
		return pretty(map[string]any{
			"output_mode":   mode,
			"mode":          mode,
			"counts":        entries,
			"filenames":     filenames,
			"numFiles":      len(filenames),
			"content":       nil,
			"numLines":      nil,
			"numMatches":    totalMatches,
			"appliedLimit":  grepAppliedLimit(limit, unlimited),
			"appliedOffset": offset,
			"truncated":     !unlimited && len(counts) >= offset+limit,
			"offset":        offset,
		}), nil
	default:
		sort.Strings(contentFilenames)
		return pretty(map[string]any{
			"output_mode":   mode,
			"mode":          mode,
			"matches":       matches,
			"filenames":     contentFilenames,
			"numFiles":      len(contentFilenames),
			"content":       strings.Join(contentLines, "\n"),
			"numLines":      len(contentLines),
			"appliedLimit":  grepAppliedLimit(limit, unlimited),
			"appliedOffset": offset,
			"truncated":     !unlimited && len(matches) >= limit,
			"offset":        offset,
		}), nil
	}
}

func grepLimit(headLimit *int, legacyLimit int) (int, bool) {
	if headLimit != nil {
		if *headLimit <= 0 {
			return 0, true
		}
		return *headLimit, false
	}
	if legacyLimit > 0 {
		return legacyLimit, false
	}
	return 250, false
}

func grepAppliedLimit(limit int, unlimited bool) any {
	if unlimited {
		return nil
	}
	return limit
}

type grepContextLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

func grepContextLines(lines []string, start int, end int) []grepContextLine {
	start = max(start, 0)
	end = min(max(end, 0), len(lines))
	if start >= end {
		return []grepContextLine{}
	}
	out := make([]grepContextLine, 0, end-start)
	for index := start; index < end; index++ {
		out = append(out, grepContextLine{Line: index + 1, Text: lines[index]})
	}
	return out
}

func grepLineStartOffsets(text string) []int {
	offsets := []int{0}
	for index, r := range text {
		if r == '\n' {
			offsets = append(offsets, index+1)
		}
	}
	return offsets
}

func grepLineForOffset(lineStarts []int, offset int) int {
	if len(lineStarts) == 0 {
		return 0
	}
	offset = max(offset, 0)
	index := sort.Search(len(lineStarts), func(i int) bool {
		return lineStarts[i] > offset
	}) - 1
	if index < 0 {
		return 0
	}
	return min(index, len(lineStarts)-1)
}

func formatGrepContentLine(path string, line int, text string, lineNumbers bool) string {
	if lineNumbers {
		return fmt.Sprintf("%s:%d:%s", path, line, text)
	}
	return fmt.Sprintf("%s:%s", path, text)
}

func matchesGrepType(path string, fileType string) bool {
	typ := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileType)), ".")
	if typ == "" {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	aliases := map[string][]string{
		"c":          {"c", "h"},
		"cpp":        {"cc", "cpp", "cxx", "hpp", "hh", "hxx"},
		"go":         {"go"},
		"java":       {"java"},
		"js":         {"js", "mjs", "cjs"},
		"json":       {"json"},
		"jsx":        {"jsx"},
		"markdown":   {"md", "markdown"},
		"md":         {"md", "markdown"},
		"py":         {"py"},
		"python":     {"py"},
		"rs":         {"rs"},
		"rust":       {"rs"},
		"sh":         {"sh", "bash", "zsh"},
		"shell":      {"sh", "bash", "zsh"},
		"swift":      {"swift"},
		"toml":       {"toml"},
		"ts":         {"ts", "mts", "cts"},
		"tsx":        {"tsx"},
		"typescript": {"ts", "tsx", "mts", "cts"},
		"yaml":       {"yaml", "yml"},
		"yml":        {"yaml", "yml"},
	}
	if values := aliases[typ]; len(values) != 0 {
		for _, value := range values {
			if ext == value {
				return true
			}
		}
		return false
	}
	return ext == typ
}

func grepCountEntries(counts map[string]int, offset int, limit int) []map[string]any {
	paths := make([]string, 0, len(counts))
	for path := range counts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	start := min(max(offset, 0), len(paths))
	end := len(paths)
	if limit > 0 {
		end = min(start+limit, len(paths))
	}
	entries := make([]map[string]any, 0, end-start)
	for _, path := range paths[start:end] {
		entries = append(entries, map[string]any{"path": path, "count": counts[path]})
	}
	return entries
}

func grepCountFilenames(entries []map[string]any) []string {
	filenames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if path, ok := entry["path"].(string); ok {
			filenames = append(filenames, path)
		}
	}
	return filenames
}

func grepCountTotal(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

type GlobTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (GlobTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "glob",
		Description: "Find workspace files by glob pattern.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (GlobTool) Permission() Permission { return PermissionReadOnly }

func (t GlobTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	root := t.Workspace
	var err error
	if payload.Path != "" {
		root, err = safePathInScope(t.Workspace, t.AdditionalDirs, payload.Path, false)
		if err != nil {
			return "", err
		}
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 200
	}
	var files []string
	walkRoot := deriveGlobWalkRoot(root, payload.Pattern)
	err = filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(files) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if ignoredDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if globPatternMatches(payload.Pattern, rel, filepath.Base(path)) {
			files = append(files, displayPath(t.Workspace, path))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	return pretty(map[string]any{"files": files, "truncated": len(files) >= limit}), nil
}

func globPatternMatches(pattern string, rel string, base string) bool {
	for _, expanded := range expandBracePatterns(pattern, 64) {
		if globPatternMatchesSingle(expanded, rel, base) {
			return true
		}
	}
	return false
}

func globPatternMatchesSingle(pattern string, rel string, base string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "./"))
	base = filepath.ToSlash(base)
	if pattern == "" {
		return true
	}
	if ok, _ := pathMatch(pattern, rel); ok {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if ok, _ := pathMatch(pattern, base); ok {
			return true
		}
	}
	re, err := regexp.Compile(globPatternRegexp(pattern))
	if err != nil {
		return false
	}
	return re.MatchString(rel)
}

func expandBracePatterns(pattern string, limit int) []string {
	if limit <= 0 {
		return []string{pattern}
	}
	start := strings.Index(pattern, "{")
	if start < 0 {
		return []string{pattern}
	}
	end := strings.Index(pattern[start+1:], "}")
	if end < 0 {
		return []string{pattern}
	}
	end += start + 1
	parts := strings.Split(pattern[start+1:end], ",")
	if len(parts) <= 1 {
		return []string{pattern}
	}
	out := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		next := pattern[:start] + part + pattern[end+1:]
		for _, expanded := range expandBracePatterns(next, limit-len(out)) {
			out = append(out, expanded)
			if len(out) >= limit {
				return out
			}
		}
	}
	if len(out) == 0 {
		return []string{pattern}
	}
	return out
}

func deriveGlobWalkRoot(root string, pattern string) string {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" || filepath.IsAbs(pattern) {
		return root
	}
	parts := strings.Split(pattern, "/")
	fixed := []string{}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.ContainsAny(part, "*?[{") {
			break
		}
		fixed = append(fixed, part)
	}
	if len(fixed) == 0 {
		return root
	}
	candidate := filepath.Join(append([]string{root}, fixed...)...)
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return root
	}
	return candidate
}

func pathMatch(pattern string, value string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	value = filepath.ToSlash(value)
	return path.Match(pattern, value)
}

func globPatternRegexp(pattern string) string {
	var builder strings.Builder
	builder.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					builder.WriteString("(?:.*/)?")
				} else {
					builder.WriteString(".*")
				}
				continue
			}
			builder.WriteString("[^/]*")
		case '?':
			builder.WriteString("[^/]")
		default:
			builder.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	builder.WriteString("$")
	return builder.String()
}

type LSTool struct {
	Workspace      string
	AdditionalDirs []string
}

type lsEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	Hidden bool   `json:"hidden,omitempty"`
}

func (LSTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "ls",
		Description: "List files and directories in a workspace-scoped directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"ignore": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"hidden": map[string]any{"type": "boolean"},
				"limit":  map[string]any{"type": "integer", "minimum": 1},
			},
			"additionalProperties": false,
		},
	}
}

func (LSTool) Permission() Permission { return PermissionReadOnly }

func (t LSTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path   string   `json:"path"`
		Ignore []string `json:"ignore"`
		Hidden bool     `json:"hidden"`
		Limit  int      `json:"limit"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	requested := strings.TrimSpace(payload.Path)
	if requested == "" {
		requested = "."
	}
	dir, err := safePathInScope(t.Workspace, t.AdditionalDirs, requested, false)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path must be a directory")
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 200
	}
	children, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	sort.Slice(children, func(i, j int) bool {
		left, right := children[i], children[j]
		if left.IsDir() != right.IsDir() {
			return left.IsDir()
		}
		return strings.ToLower(left.Name()) < strings.ToLower(right.Name())
	})
	entries := make([]lsEntry, 0, min(len(children), limit))
	truncated := false
	for _, child := range children {
		name := child.Name()
		hidden := strings.HasPrefix(name, ".")
		if hidden && !payload.Hidden {
			continue
		}
		childPath := filepath.Join(dir, name)
		if ignoredLSEntry(t.Workspace, childPath, name, payload.Ignore) {
			continue
		}
		if len(entries) >= limit {
			truncated = true
			break
		}
		childInfo, err := child.Info()
		if err != nil {
			return "", err
		}
		kind := "file"
		switch {
		case childInfo.IsDir():
			kind = "directory"
		case childInfo.Mode()&os.ModeSymlink != 0:
			kind = "symlink"
		}
		entries = append(entries, lsEntry{
			Name:   name,
			Path:   displayPath(t.Workspace, childPath),
			Type:   kind,
			Size:   childInfo.Size(),
			Hidden: hidden,
		})
	}
	return pretty(map[string]any{
		"kind":      "ls",
		"path":      displayPath(t.Workspace, dir),
		"entries":   entries,
		"truncated": truncated,
	}), nil
}

func ignoredLSEntry(workspace string, fullPath string, name string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	display := filepath.ToSlash(displayPath(workspace, fullPath))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
		if ok, _ := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(display)); ok {
			return true
		}
	}
	return false
}

type WebFetchTool struct{}

func (WebFetchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch an HTTP or HTTPS URL and return extracted text, metadata, and a bounded summary.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":        map[string]any{"type": "string"},
				"prompt":     map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
				"max_bytes":  map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"url", "prompt"},
			"additionalProperties": false,
		},
	}
}

func (WebFetchTool) Permission() Permission { return PermissionReadOnly }

func (WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		URL       string  `json:"url"`
		Prompt    *string `json:"prompt"`
		TimeoutMS int     `json:"timeout_ms,omitempty"`
		MaxBytes  int64   `json:"max_bytes,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.Prompt == nil {
		return "", errors.New("prompt is required")
	}
	result, err := webaccess.Fetch(ctx, webaccess.FetchInput{
		URL:       payload.URL,
		Prompt:    *payload.Prompt,
		TimeoutMS: payload.TimeoutMS,
		MaxBytes:  payload.MaxBytes,
	})
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

type WebSearchTool struct{}

func (WebSearchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "web_search",
		Description: "Search the web using the configured search endpoint and return result titles, URLs, and snippets.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":           map[string]any{"type": "string", "minLength": 2},
				"max_results":     map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
				"allowed_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"blocked_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"timeout_ms":      map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}

func (WebSearchTool) Permission() Permission { return PermissionReadOnly }

func (WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload webaccess.SearchInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	result, err := webaccess.Search(ctx, payload)
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

type RemoteTriggerTool struct{}

func (RemoteTriggerTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "remote_trigger",
		Description: "Trigger a remote HTTP action or webhook endpoint.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":     map[string]any{"type": "string"},
				"method":  map[string]any{"type": "string", "enum": []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD"}},
				"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
				"body":    map[string]any{"type": "string"},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Optional request timeout in milliseconds. Defaults to 30000.",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Maximum response body bytes to return, capped at 2000000.",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
	}
}

func (RemoteTriggerTool) Permission() Permission { return PermissionDanger }

func (RemoteTriggerTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		URL       string            `json:"url"`
		Method    string            `json:"method"`
		Headers   map[string]string `json:"headers"`
		Body      string            `json:"body"`
		TimeoutMS int               `json:"timeout_ms"`
		MaxBytes  int64             `json:"max_bytes"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	requestURL, err := validateRemoteTriggerURL(payload.URL)
	if err != nil {
		return "", err
	}
	method := strings.ToUpper(strings.TrimSpace(payload.Method))
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead:
	default:
		return "", fmt.Errorf("unsupported HTTP method: %s", method)
	}
	timeout := 30 * time.Second
	if payload.TimeoutMS > 0 {
		timeout = time.Duration(payload.TimeoutMS) * time.Millisecond
	}
	limit := payload.MaxBytes
	if limit <= 0 {
		limit = 1024 * 1024
	}
	if limit > maxRemoteBodyBytes {
		limit = maxRemoteBodyBytes
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), strings.NewReader(payload.Body))
	if err != nil {
		return "", err
	}
	for key, value := range payload.Headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	if payload.Body != "" && req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "text/plain; charset=utf-8")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return pretty(map[string]any{
		"url":         requestURL.String(),
		"final_url":   resp.Request.URL.String(),
		"method":      method,
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"headers":     resp.Header,
		"bytes":       len(data),
		"truncated":   truncated,
		"body":        string(data),
		"duration_ms": time.Since(started).Milliseconds(),
	}), nil
}

func validateRemoteTriggerURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("url must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("url host is required")
	}
	return parsed, nil
}

type TestingPermissionTool struct{}

func (TestingPermissionTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "testing_permission",
		Description: "Dry-run the current permission policy for a target tool without executing that tool.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target_tool": map[string]any{"type": "string"},
				"tool":        map[string]any{"type": "string"},
				"required_permission": map[string]any{
					"type": "string",
					"enum": []string{string(PermissionReadOnly), string(PermissionWorkspace), string(PermissionDanger), string(PermissionPrompt), string(PermissionAllow)},
				},
				"input":  map[string]any{"type": "object", "additionalProperties": true},
				"action": map[string]any{"type": "string", "description": "Deprecated compatibility alias used as the target label when target_tool is omitted."},
			},
		},
	}
}

func (TestingPermissionTool) Permission() Permission { return PermissionReadOnly }

func (TestingPermissionTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", errors.New("testing_permission must be executed through the tool registry")
}

type testingPermissionInput struct {
	TargetTool         string          `json:"target_tool"`
	Tool               string          `json:"tool"`
	RequiredPermission Permission      `json:"required_permission"`
	Input              json.RawMessage `json:"input"`
	Action             string          `json:"action"`
}

func (r *Registry) executeTestingPermission(input json.RawMessage, prompter *Prompter) (string, error) {
	var payload testingPermissionInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	target := strings.TrimSpace(payload.TargetTool)
	if target == "" {
		target = strings.TrimSpace(payload.Tool)
	}
	if target == "" {
		target = strings.TrimSpace(payload.Action)
	}
	if target == "" {
		return "", errors.New("target_tool is required")
	}
	targetTool, canonical, found := r.toolByName(target)
	required := payload.RequiredPermission
	if required != "" {
		if !validPermission(required) {
			return "", fmt.Errorf("unsupported required_permission %q", required)
		}
	} else if found {
		required = targetTool.Permission()
	} else {
		required = PermissionDanger
	}
	targetInput := payload.Input
	if len(targetInput) == 0 || string(targetInput) == "null" {
		targetInput = json.RawMessage(`{}`)
	}
	if prompter == nil {
		prompter = &Prompter{Mode: PermissionWorkspace}
	}
	decision := prompter.Decide(canonicalOrTarget(canonical, target), required, targetInput)
	return pretty(map[string]any{
		"kind":                "permission_check",
		"target_tool":         canonicalOrTarget(canonical, target),
		"known_tool":          found,
		"required_permission": string(required),
		"mode":                string(decision.Mode),
		"input":               string(targetInput),
		"allowed":             decision.Allowed,
		"would_prompt":        decision.WouldPrompt,
		"reason":              decision.Reason,
		"message":             decision.Message,
	}), nil
}

func (r *Registry) toolByName(name string) (Tool, string, bool) {
	canonical, tool, ok := r.resolve(name)
	if ok {
		return tool, canonical, true
	}
	return nil, "", false
}

func canonicalOrTarget(canonical string, target string) string {
	if canonical != "" {
		return canonical
	}
	return target
}

func validPermission(permission Permission) bool {
	switch permission {
	case PermissionReadOnly, PermissionWorkspace, PermissionDanger, PermissionPrompt, PermissionAllow:
		return true
	default:
		return false
	}
}

type NotebookReadTool struct {
	Workspace      string
	AdditionalDirs []string
}

type notebookReadCell struct {
	Index          int    `json:"index"`
	CellType       string `json:"cell_type"`
	Source         string `json:"source"`
	ExecutionCount any    `json:"execution_count,omitempty"`
	OutputCount    int    `json:"output_count,omitempty"`
	Outputs        any    `json:"outputs,omitempty"`
}

func (NotebookReadTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "notebook_read",
		Description: "Read cell sources and optional outputs from a Jupyter .ipynb notebook inside the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"notebook_path":   map[string]any{"type": "string"},
				"cell_index":      map[string]any{"type": "integer", "minimum": 0},
				"limit":           map[string]any{"type": "integer", "minimum": 1},
				"include_outputs": map[string]any{"type": "boolean"},
			},
			"required":             []string{"notebook_path"},
			"additionalProperties": false,
		},
	}
}

func (NotebookReadTool) Permission() Permission { return PermissionReadOnly }

func (t NotebookReadTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		NotebookPath   string `json:"notebook_path"`
		CellIndex      *int   `json:"cell_index"`
		Limit          int    `json:"limit"`
		IncludeOutputs bool   `json:"include_outputs"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, payload.NotebookPath, false)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".ipynb") {
		return "", errors.New("notebook_path must point to a .ipynb file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var notebook map[string]any
	if err := json.Unmarshal(data, &notebook); err != nil {
		return "", err
	}
	rawCells, ok := notebook["cells"].([]any)
	if !ok {
		return "", errors.New("notebook cells array not found")
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 100
	}
	start, end := 0, len(rawCells)
	if payload.CellIndex != nil {
		if *payload.CellIndex < 0 || *payload.CellIndex >= len(rawCells) {
			return "", errors.New("cell index out of range")
		}
		start = *payload.CellIndex
		end = start + 1
	}
	cells := make([]notebookReadCell, 0, min(end-start, limit))
	truncated := false
	for index := start; index < end; index++ {
		if len(cells) >= limit {
			truncated = true
			break
		}
		cell, ok := rawCells[index].(map[string]any)
		if !ok {
			return "", errors.New("notebook cell is not an object")
		}
		entry := notebookReadCell{
			Index:          index,
			CellType:       stringValue(cell["cell_type"]),
			Source:         notebookSourceText(cell["source"]),
			ExecutionCount: cell["execution_count"],
		}
		if outputs, ok := cell["outputs"].([]any); ok {
			entry.OutputCount = len(outputs)
			if payload.IncludeOutputs {
				entry.Outputs = outputs
			}
		}
		cells = append(cells, entry)
	}
	return pretty(map[string]any{
		"kind":       "notebook_read",
		"path":       displayPath(t.Workspace, path),
		"cell_count": len(rawCells),
		"cells":      cells,
		"truncated":  truncated,
	}), nil
}

func notebookSourceText(value any) string {
	switch source := value.(type) {
	case string:
		return source
	case []any:
		var builder strings.Builder
		for _, line := range source {
			builder.WriteString(stringValue(line))
		}
		return builder.String()
	default:
		return ""
	}
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

type NotebookEditTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (NotebookEditTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "notebook_edit",
		Description: "Replace, insert, or delete a cell in a Jupyter .ipynb notebook inside the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"notebook_path": map[string]any{"type": "string"},
				"cell_index":    map[string]any{"type": "integer", "minimum": 0},
				"cell_id":       map[string]any{"type": "string"},
				"cell_type":     map[string]any{"type": "string", "enum": []string{"code", "markdown", "raw"}},
				"new_source":    map[string]any{"type": "string"},
				"edit_mode":     map[string]any{"type": "string", "enum": []string{"replace", "insert", "delete"}},
			},
			"required":             []string{"notebook_path"},
			"additionalProperties": false,
		},
	}
}

func (NotebookEditTool) Permission() Permission { return PermissionWorkspace }

func (t NotebookEditTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		NotebookPath string `json:"notebook_path"`
		CellIndex    *int   `json:"cell_index"`
		CellID       string `json:"cell_id"`
		CellType     string `json:"cell_type"`
		NewSource    string `json:"new_source"`
		EditMode     string `json:"edit_mode"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, payload.NotebookPath, false)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".ipynb") {
		return "", errors.New("notebook_path must point to a .ipynb file")
	}
	index, err := resolveNotebookEditIndex(path, payload.CellIndex, payload.CellID, payload.EditMode)
	if err != nil {
		return "", err
	}
	result, err := codeintel.EditNotebook(path, codeintel.NotebookEditOptions{
		Index:    index,
		CellType: payload.CellType,
		Source:   payload.NewSource,
		Mode:     payload.EditMode,
	})
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

func resolveNotebookEditIndex(path string, cellIndex *int, cellID string, mode string) (int, error) {
	if cellIndex != nil {
		if *cellIndex < 0 {
			return 0, errors.New("cell_index must be non-negative")
		}
		return *cellIndex, nil
	}
	cellID = strings.TrimSpace(cellID)
	if cellID == "" {
		if strings.EqualFold(strings.TrimSpace(mode), "insert") {
			return 0, nil
		}
		return 0, errors.New("cell_index or cell_id is required")
	}
	if index, err := strconv.Atoi(cellID); err == nil && index >= 0 {
		return index, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var notebook map[string]any
	if err := json.Unmarshal(data, &notebook); err != nil {
		return 0, err
	}
	rawCells, ok := notebook["cells"].([]any)
	if !ok {
		return 0, errors.New("notebook cells array not found")
	}
	for index, raw := range rawCells {
		cell, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(cell["id"]) == cellID {
			return index, nil
		}
	}
	return 0, fmt.Errorf("cell_id %q not found", cellID)
}

type LSPTool struct {
	Workspace      string
	AdditionalDirs []string
	ConfigHome     string
}

func (LSPTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "lsp",
		Description: "Query code intelligence for Go symbols, references, diagnostics, definitions, hover context, completions, and formatting.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"symbols", "document_symbols", "references", "find_references", "diagnostics", "definition", "goto_definition", "hover", "completion", "completions", "format", "formatting"},
				},
				"path":      map[string]any{"type": "string"},
				"line":      map[string]any{"type": "integer", "minimum": 0},
				"character": map[string]any{"type": "integer", "minimum": 0},
				"query":     map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer", "minimum": 1},
				"language":  map[string]any{"type": "string"},
				"use_server": map[string]any{
					"type":        "boolean",
					"description": "Use a configured stdio LSP server from codog code-intel lsp start/query metadata instead of the static fallback.",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func (LSPTool) Permission() Permission {
	return PermissionReadOnly
}

func (t LSPTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Action    string `json:"action"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
		Language  string `json:"language"`
		UseServer bool   `json:"use_server"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action, err := codeintel.NormalizeLSPAction(payload.Action)
	if err != nil {
		return "", err
	}
	if payload.UseServer || strings.TrimSpace(payload.Language) != "" {
		result, err := t.executeServerLSP(ctx, action, payload.Language, payload.Path, payload.Line, payload.Character)
		if err == nil {
			return pretty(map[string]any{"action": action, "source": "lsp", "lsp": result}), nil
		}
		if payload.UseServer {
			return "", err
		}
	}
	switch action {
	case "symbols":
		symbols, err := codeintel.GoSymbols(t.Workspace)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(payload.Path) != "" {
			rel, err := scopedRelativePath(t.Workspace, t.AdditionalDirs, payload.Path)
			if err != nil {
				return "", err
			}
			filtered := symbols[:0]
			for _, symbol := range symbols {
				if filepath.ToSlash(symbol.Path) == rel {
					filtered = append(filtered, symbol)
				}
			}
			symbols = filtered
		}
		return pretty(map[string]any{"action": action, "symbols": symbols, "total": len(symbols)}), nil
	case "definition":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		definition, found, err := codeintel.Definition(t.Workspace, query)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"action": action, "query": query, "found": found, "definition": definition}), nil
	case "references":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		refs, err := codeintel.References(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"action": action, "query": query, "references": refs, "total": len(refs)}), nil
	case "hover":
		query, err := t.lspQuery(payload.Query, payload.Path, payload.Line, payload.Character)
		if err != nil {
			return "", err
		}
		hover, err := codeintel.HoverInfo(t.Workspace, query, 2)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"action": action, "query": query, "hover": hover}), nil
	case "completion":
		query := strings.TrimSpace(payload.Query)
		if query == "" && strings.TrimSpace(payload.Path) != "" {
			var err error
			query, err = symbolAtPosition(t.Workspace, t.AdditionalDirs, payload.Path, payload.Line, payload.Character)
			if err != nil {
				return "", err
			}
		}
		completions, err := codeintel.Completions(t.Workspace, query, payload.Limit)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"action": "completion", "query": query, "completions": completions, "total": len(completions)}), nil
	case "format":
		if strings.TrimSpace(payload.Path) == "" {
			return "", errors.New("path is required for lsp format")
		}
		result, err := codeintel.FormatGoFile(t.Workspace, payload.Path, false)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"action": "format", "format": result}), nil
	case "diagnostics":
		patterns := []string{}
		if strings.TrimSpace(payload.Path) != "" {
			patterns = append(patterns, payload.Path)
		} else if strings.TrimSpace(payload.Query) != "" {
			patterns = append(patterns, payload.Query)
		}
		diagnostics, err := codeintel.GoDiagnostics(ctx, t.Workspace, patterns)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"action": action, "diagnostics": diagnostics, "total": len(diagnostics)}), nil
	default:
		return "", fmt.Errorf("unknown lsp action %q", payload.Action)
	}
}

func (t LSPTool) lspQuery(query string, path string, line int, character int) (string, error) {
	query = strings.TrimSpace(query)
	if query != "" {
		return query, nil
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("query or path position is required")
	}
	return symbolAtPosition(t.Workspace, t.AdditionalDirs, path, line, character)
}

func (t LSPTool) executeServerLSP(ctx context.Context, action string, language string, path string, line int, character int) (codeintel.LSPQueryResult, error) {
	if strings.TrimSpace(t.ConfigHome) == "" {
		return codeintel.LSPQueryResult{}, errors.New("config home is required for lsp server queries")
	}
	if strings.TrimSpace(path) == "" {
		return codeintel.LSPQueryResult{}, errors.New("path is required for lsp server queries")
	}
	language = strings.TrimSpace(language)
	if language == "" {
		language = codeintel.InferLanguageID(path)
	}
	return codeintel.NewLSPStore(t.ConfigHome, t.Workspace).Query(ctx, language, codeintel.LSPQueryRequest{
		Action:    action,
		Path:      path,
		Line:      line,
		Character: character,
	})
}

func scopedRelativePath(workspace string, additionalDirs []string, requested string) (string, error) {
	path, err := safePathInScope(workspace, additionalDirs, requested, false)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspace, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Clean(requested)), nil
	}
	return filepath.ToSlash(rel), nil
}

func symbolAtPosition(workspace string, additionalDirs []string, requested string, line int, character int) (string, error) {
	path, err := safePathInScope(workspace, additionalDirs, requested, false)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if line < 0 || line >= len(lines) {
		return "", fmt.Errorf("line %d is out of range", line)
	}
	text := lines[line]
	if character < 0 {
		character = 0
	}
	if character > len(text) {
		character = len(text)
	}
	start := character
	for start > 0 && isIdentifierByte(text[start-1]) {
		start--
	}
	end := character
	for end < len(text) && isIdentifierByte(text[end]) {
		end++
	}
	if start == end {
		return "", errors.New("no symbol found at position")
	}
	return text[start:end], nil
}

func isIdentifierByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

type EnterWorktreeTool struct {
	Workspace string
}

func (EnterWorktreeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "enter_worktree",
		Description: "Allocate a detached git worktree for isolated agent or verification work.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required":             []string{"name"},
			"additionalProperties": false,
		},
	}
}

func (EnterWorktreeTool) Permission() Permission { return PermissionDanger }

func (t EnterWorktreeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	allocation, err := worktree.Allocate(t.Workspace, payload.Name)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"kind":       "worktree",
		"operation":  "enter",
		"allocation": allocation,
	}), nil
}

type ExitWorktreeTool struct {
	Workspace string
}

func (ExitWorktreeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "exit_worktree",
		Description: "Remove a Codog-managed git worktree allocation by id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":      map[string]any{"type": "string"},
				"task_id": map[string]any{"type": "string"},
				"taskId":  map[string]any{"type": "string"},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (ExitWorktreeTool) Permission() Permission { return PermissionDanger }

func (t ExitWorktreeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if err := worktree.Remove(t.Workspace, payload.ID); err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"kind":      "worktree",
		"operation": "exit",
		"id":        payload.ID,
		"removed":   true,
	}), nil
}

type EnterPlanModeTool struct {
	Workspace string
}

type planModeInput struct {
	Plan string `json:"plan,omitempty"`
}

func (EnterPlanModeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "enter_plan_mode",
		Description: "Enter plan mode and optionally persist the current implementation plan. While plan mode is active, future tool permission checks are read-only until exit_plan_mode is called or the user exits plan mode.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"plan": map[string]any{
					"type":        "string",
					"description": "Optional plan text to store with the active plan-mode state.",
				},
			},
		},
	}
}

func (EnterPlanModeTool) Permission() Permission {
	return PermissionReadOnly
}

func (t EnterPlanModeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload planModeInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	report, err := planmode.Enter(t.Workspace, payload.Plan)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type ExitPlanModeTool struct {
	Workspace string
}

func (ExitPlanModeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "exit_plan_mode",
		Description: "Exit plan mode. Include the final implementation plan to persist it before returning to normal tool permissions on the next user turn.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"plan": map[string]any{
					"type":        "string",
					"description": "Optional final plan text to store before leaving plan mode.",
				},
			},
		},
	}
}

func (ExitPlanModeTool) Permission() Permission {
	return PermissionReadOnly
}

func (t ExitPlanModeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload planModeInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(payload.Plan) != "" {
		if _, err := planmode.Set(t.Workspace, payload.Plan); err != nil {
			return "", err
		}
	}
	report, err := planmode.Exit(t.Workspace)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type AgentTool struct {
	Workspace  string
	ConfigHome string
	Executable string
}

func (AgentTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "agent",
		Description: "Launch a specialized Codog agent task in the background and return its task metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description":   map[string]any{"type": "string"},
				"prompt":        map[string]any{"type": "string"},
				"subagent_type": map[string]any{"type": "string"},
				"name":          map[string]any{"type": "string"},
				"model":         map[string]any{"type": "string"},
				"session_id":    map[string]any{"type": "string"},
			},
			"required":             []string{"description", "prompt"},
			"additionalProperties": false,
		},
	}
}

func (AgentTool) Permission() Permission {
	return PermissionDanger
}

func (t AgentTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		SubagentType string `json:"subagent_type"`
		Name         string `json:"name"`
		Model        string `json:"model"`
		SessionID    string `json:"session_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Description = strings.TrimSpace(payload.Description)
	payload.Prompt = strings.TrimSpace(payload.Prompt)
	if payload.Description == "" {
		return "", errors.New("description is required")
	}
	if payload.Prompt == "" {
		return "", errors.New("prompt is required")
	}
	def, found, err := findAgentDefinition(t.Workspace, payload.Name, payload.SubagentType)
	if err != nil {
		return "", err
	}
	agentName := strings.TrimSpace(payload.Name)
	if agentName == "" {
		agentName = strings.TrimSpace(payload.SubagentType)
	}
	if found {
		agentName = def.Name
		if strings.TrimSpace(payload.Model) == "" {
			payload.Model = def.Model
		}
	}
	if agentName == "" {
		agentName = payload.Description
	}
	executable := strings.TrimSpace(t.Executable)
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	command := buildAgentToolCommand(executable, def, payload.Description, payload.Prompt, payload.Model)
	env, err := toolEnvironment(ctx, t.ConfigHome)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(command, cwd, background.RunOptions{
		Kind:      "agent",
		AgentType: agentName,
		SessionID: payload.SessionID,
		Env:       env,
	})
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"kind":          "agent",
		"agent":         agentName,
		"description":   payload.Description,
		"subagent_type": strings.TrimSpace(payload.SubagentType),
		"definition":    found,
		"task":          task,
	}), nil
}

func findAgentDefinition(workspace string, name string, subagentType string) (agentdefs.Definition, bool, error) {
	defs, err := agentdefs.Load(workspace)
	if err != nil {
		return agentdefs.Definition{}, false, err
	}
	candidates := []string{strings.TrimSpace(name), strings.TrimSpace(subagentType)}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, def := range defs {
			if strings.EqualFold(def.Name, candidate) {
				return def, true, nil
			}
		}
	}
	return agentdefs.Definition{}, false, nil
}

func buildAgentToolCommand(executable string, def agentdefs.Definition, description string, prompt string, model string) string {
	parts := []string{}
	if strings.TrimSpace(description) != "" {
		parts = append(parts, "Task: "+strings.TrimSpace(description))
	}
	if strings.TrimSpace(def.Prompt) != "" {
		parts = append(parts, strings.TrimSpace(def.Prompt))
	}
	parts = append(parts, strings.TrimSpace(prompt))
	args := []string{shellQuoteToolArg(executable)}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", shellQuoteToolArg(strings.TrimSpace(model)))
	}
	args = append(args, "prompt", shellQuoteToolArg(strings.Join(parts, "\n\n")))
	return strings.Join(args, " ")
}

func shellQuoteToolArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type CronCreateTool struct {
	ConfigHome string
}

func (CronCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "cron_create",
		Description: "Create a scheduled recurring Codog task registry entry.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"schedule":    map[string]any{"type": "string"},
				"prompt":      map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			},
			"required":             []string{"schedule", "prompt"},
			"additionalProperties": false,
		},
	}
}

func (CronCreateTool) Permission() Permission { return PermissionDanger }

func (t CronCreateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Schedule    string `json:"schedule"`
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	entry, err := cron.NewStore(t.ConfigHome).Create(payload.Schedule, payload.Prompt, payload.Description)
	if err != nil {
		return "", err
	}
	return pretty(entry), nil
}

type CronDeleteTool struct {
	ConfigHome string
}

func (CronDeleteTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "cron_delete",
		Description: "Delete a scheduled recurring Codog task by cron id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cron_id": map[string]any{"type": "string"},
			},
			"required":             []string{"cron_id"},
			"additionalProperties": false,
		},
	}
}

func (CronDeleteTool) Permission() Permission { return PermissionDanger }

func (t CronDeleteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		CronID string `json:"cron_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	entry, err := cron.NewStore(t.ConfigHome).Delete(payload.CronID)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"cron_id":  entry.ID,
		"schedule": entry.Schedule,
		"status":   "deleted",
		"message":  "Cron entry removed",
	}), nil
}

type CronListTool struct {
	ConfigHome string
}

func (CronListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "cron_list",
		Description: "List scheduled recurring Codog tasks.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}
}

func (CronListTool) Permission() Permission { return PermissionReadOnly }

func (t CronListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	if len(input) != 0 {
		var payload map[string]any
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	entries, err := cron.NewStore(t.ConfigHome).List()
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"crons": entries, "count": len(entries)}), nil
}

type TeamCreateTool struct {
	Workspace  string
	ConfigHome string
	Executable string
}

func (TeamCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "team_create",
		Description: "Create a team of background Codog sub-agent tasks for parallel execution.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"tasks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"prompt":      map[string]any{"type": "string"},
							"description": map[string]any{"type": "string"},
						},
						"required":             []string{"prompt"},
						"additionalProperties": false,
					},
				},
				"session_id": map[string]any{"type": "string"},
			},
			"required":             []string{"name", "tasks"},
			"additionalProperties": false,
		},
	}
}

func (TeamCreateTool) Permission() Permission { return PermissionDanger }

func (t TeamCreateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Name      string          `json:"name"`
		Tasks     []team.TaskSpec `json:"tasks"`
		SessionID string          `json:"session_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Name) == "" {
		return "", errors.New("name is required")
	}
	if len(payload.Tasks) == 0 {
		return "", errors.New("tasks are required")
	}
	executable := strings.TrimSpace(t.Executable)
	var err error
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	store := taskStore(t.ConfigHome, t.Workspace)
	env, err := toolEnvironment(ctx, t.ConfigHome)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	taskIDs := make([]string, 0, len(payload.Tasks))
	for _, task := range payload.Tasks {
		prompt := strings.TrimSpace(task.Prompt)
		if prompt == "" {
			return "", errors.New("task prompt is required")
		}
		description := strings.TrimSpace(task.Description)
		if description != "" {
			prompt = "Task: " + description + "\n\n" + prompt
		}
		started, err := store.RunWithOptions(buildTeamTaskCommand(executable, prompt), cwd, background.RunOptions{
			Kind:      "team",
			SessionID: payload.SessionID,
			Env:       env,
		})
		if err != nil {
			return "", err
		}
		taskIDs = append(taskIDs, started.ID)
	}
	created, err := team.NewStore(t.ConfigHome).Create(payload.Name, payload.Tasks, taskIDs)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"team_id":    created.ID,
		"name":       created.Name,
		"task_count": len(created.TaskIDs),
		"task_ids":   created.TaskIDs,
		"status":     created.Status,
		"created_at": created.CreatedAt,
	}), nil
}

type TeamListTool struct {
	Workspace  string
	ConfigHome string
}

func (TeamListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "team_list",
		Description: "List team task groups and summarize their background task states.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status": map[string]any{"type": "string"},
			},
		},
	}
}

func (TeamListTool) Permission() Permission { return PermissionReadOnly }

func (t TeamListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Status string `json:"status"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	status := strings.TrimSpace(payload.Status)
	teams, err := team.NewStore(t.ConfigHome).List()
	if err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(teams))
	for _, item := range teams {
		if status != "" && !strings.EqualFold(item.Status, status) {
			continue
		}
		out = append(out, teamSummary(t.ConfigHome, item))
	}
	return pretty(map[string]any{
		"kind":   "team_list",
		"total":  len(out),
		"status": status,
		"teams":  out,
	}), nil
}

type TeamGetTool struct {
	Workspace  string
	ConfigHome string
}

func (TeamGetTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "team_get",
		Description: "Fetch a team task group with task prompts and current background task states.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"team_id": map[string]any{"type": "string"},
			},
			"required": []string{"team_id"},
		},
	}
}

func (TeamGetTool) Permission() Permission { return PermissionReadOnly }

func (t TeamGetTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TeamID string `json:"team_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	item, err := team.NewStore(t.ConfigHome).Get(payload.TeamID)
	if err != nil {
		return "", err
	}
	summary := teamSummary(t.ConfigHome, item)
	summary["kind"] = "team"
	summary["tasks"] = item.Tasks
	return pretty(summary), nil
}

type TeamDeleteTool struct {
	ConfigHome string
}

func (TeamDeleteTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "team_delete",
		Description: "Delete a team and stop all background tasks associated with it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"team_id": map[string]any{"type": "string"},
			},
			"required":             []string{"team_id"},
			"additionalProperties": false,
		},
	}
}

func (TeamDeleteTool) Permission() Permission { return PermissionDanger }

func (t TeamDeleteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TeamID string `json:"team_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	teamStore := team.NewStore(t.ConfigHome)
	existing, err := teamStore.Get(payload.TeamID)
	if err != nil {
		return "", err
	}
	stopped := []string{}
	taskStore := background.NewStore(t.ConfigHome)
	for _, id := range existing.TaskIDs {
		if task, err := taskStore.Stop(id); err == nil {
			stopped = append(stopped, task.ID)
		}
	}
	deleted, err := teamStore.MarkDeleted(payload.TeamID)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"team_id":       deleted.ID,
		"name":          deleted.Name,
		"status":        deleted.Status,
		"stopped_tasks": stopped,
		"message":       "Team deleted",
	}), nil
}

func teamSummary(configHome string, item team.Team) map[string]any {
	return map[string]any{
		"team_id":       item.ID,
		"name":          item.Name,
		"status":        item.Status,
		"task_count":    len(item.Tasks),
		"task_ids":      item.TaskIDs,
		"task_statuses": teamTaskStatuses(configHome, item.TaskIDs),
		"created_at":    item.CreatedAt,
		"updated_at":    item.UpdatedAt,
	}
}

func teamTaskStatuses(configHome string, ids []string) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	store := background.NewStore(configHome)
	for _, id := range ids {
		status := map[string]any{"id": id, "status": "unknown"}
		task, err := store.Status(id)
		if err != nil {
			status["error"] = err.Error()
		} else {
			status["status"] = task.Status
			status["kind"] = task.Kind
			status["exit_code"] = task.ExitCode
			status["started_at"] = task.StartedAt
			status["completed_at"] = task.CompletedAt
		}
		out = append(out, status)
	}
	return out
}

func buildTeamTaskCommand(executable string, prompt string) string {
	return strings.Join([]string{shellQuoteToolArg(executable), "prompt", shellQuoteToolArg(prompt)}, " ")
}

type WorkerCreateTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_create",
		Description: "Create a coding worker control record ready for prompt delivery.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"cwd":                             map[string]any{"type": "string"},
				"trusted_roots":                   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"auto_recover_prompt_misdelivery": map[string]any{"type": "boolean"},
			},
			"required": []string{"cwd"},
		},
	}
}

func (WorkerCreateTool) Permission() Permission { return PermissionDanger }

func (t WorkerCreateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		CWD                          string   `json:"cwd"`
		TrustedRoots                 []string `json:"trusted_roots"`
		AutoRecoverPromptMisdelivery *bool    `json:"auto_recover_prompt_misdelivery,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	cwd, err := safePath(t.Workspace, payload.CWD, false)
	if err != nil {
		return "", err
	}
	autoRecover := true
	if payload.AutoRecoverPromptMisdelivery != nil {
		autoRecover = *payload.AutoRecoverPromptMisdelivery
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).Create(cwd, payload.TrustedRoots, autoRecover)
	if err != nil {
		return "", err
	}
	return pretty(worker), nil
}

type WorkerListTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_list",
		Description: "List coding worker control records with optional status filters.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status":      map[string]any{"type": "string"},
				"task_status": map[string]any{"type": "string"},
			},
		},
	}
}

func (WorkerListTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Status     string `json:"status"`
		TaskStatus string `json:"task_status"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	status := strings.TrimSpace(payload.Status)
	taskStatus := strings.TrimSpace(payload.TaskStatus)
	list, err := workerStore(t.ConfigHome, t.Workspace).List()
	if err != nil {
		return "", err
	}
	out := make([]workers.Worker, 0, len(list))
	getter := WorkerGetTool{Workspace: t.Workspace, ConfigHome: t.ConfigHome}
	for _, worker := range list {
		worker = getter.withTaskStatus(worker)
		if status != "" && !strings.EqualFold(worker.Status, status) {
			continue
		}
		if taskStatus != "" && !strings.EqualFold(worker.TaskStatus, taskStatus) {
			continue
		}
		out = append(out, worker)
	}
	return pretty(map[string]any{
		"kind":        "worker_list",
		"total":       len(out),
		"status":      status,
		"task_status": taskStatus,
		"workers":     out,
	}), nil
}

type WorkerGetTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerGetTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_get", "Fetch the current worker state and event history.")
}

func (WorkerGetTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerGetTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).Get(id)
	if err != nil {
		return "", err
	}
	worker = t.withTaskStatus(worker)
	return pretty(worker), nil
}

type WorkerObserveTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerObserveTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_observe",
		Description: "Feed a terminal snapshot into worker state detection.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id":   map[string]any{"type": "string"},
				"screen_text": map[string]any{"type": "string"},
			},
			"required": []string{"worker_id", "screen_text"},
		},
	}
}

func (WorkerObserveTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerObserveTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID   string `json:"worker_id"`
		ScreenText string `json:"screen_text"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).Observe(payload.WorkerID, payload.ScreenText)
	if err != nil {
		return "", err
	}
	worker = WorkerGetTool{Workspace: t.Workspace, ConfigHome: t.ConfigHome}.withTaskStatus(worker)
	return pretty(worker), nil
}

type WorkerResolveTrustTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerResolveTrustTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_resolve_trust", "Resolve a detected worker trust prompt.")
}

func (WorkerResolveTrustTool) Permission() Permission { return PermissionDanger }

func (t WorkerResolveTrustTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).ResolveTrust(id)
	if err != nil {
		return "", err
	}
	worker = WorkerGetTool{Workspace: t.Workspace, ConfigHome: t.ConfigHome}.withTaskStatus(worker)
	return pretty(worker), nil
}

type WorkerAwaitReadyTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerAwaitReadyTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_await_ready", "Return the current ready-for-prompt verdict for a worker.")
}

func (WorkerAwaitReadyTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerAwaitReadyTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	snapshot, err := workerStore(t.ConfigHome, t.Workspace).AwaitReady(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(snapshot.TaskID) != "" {
		if task, err := taskStore(t.ConfigHome, t.Workspace).Status(snapshot.TaskID); err == nil {
			snapshot.TaskStatus = task.Status
		}
	}
	return pretty(snapshot), nil
}

type WorkerSendPromptTool struct {
	Workspace  string
	ConfigHome string
	Executable string
}

func (WorkerSendPromptTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_send_prompt",
		Description: "Send a task prompt to a ready worker and run it as a background Codog prompt.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id": map[string]any{"type": "string"},
				"prompt":    map[string]any{"type": "string"},
				"task_receipt": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"repo":               map[string]any{"type": "string"},
						"task_kind":          map[string]any{"type": "string"},
						"source_surface":     map[string]any{"type": "string"},
						"expected_artifacts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"objective_preview":  map[string]any{"type": "string"},
					},
					"required": []string{"repo", "task_kind", "source_surface", "objective_preview"},
				},
			},
			"required": []string{"worker_id"},
		},
	}
}

func (WorkerSendPromptTool) Permission() Permission { return PermissionDanger }

func (t WorkerSendPromptTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID    string               `json:"worker_id"`
		Prompt      string               `json:"prompt"`
		TaskReceipt *workers.TaskReceipt `json:"task_receipt"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" && payload.TaskReceipt != nil {
		prompt = strings.TrimSpace(payload.TaskReceipt.ObjectivePreview)
	}
	if prompt == "" {
		return "", errors.New("prompt or task_receipt.objective_preview is required")
	}
	if err := validateWorkerReceipt(payload.TaskReceipt); err != nil {
		return "", err
	}
	store := workerStore(t.ConfigHome, t.Workspace)
	snapshot, err := store.AwaitReady(payload.WorkerID)
	if err != nil {
		return "", err
	}
	if !snapshot.ReadyForPrompt {
		return "", fmt.Errorf("worker %s is not ready for prompt", payload.WorkerID)
	}
	executable := strings.TrimSpace(t.Executable)
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	env, err := toolEnvironment(ctx, t.ConfigHome)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(buildTeamTaskCommand(executable, prompt), cwd, background.RunOptions{Kind: "worker", Env: env})
	if err != nil {
		return "", err
	}
	worker, err := store.SendPrompt(payload.WorkerID, prompt, payload.TaskReceipt, task.ID)
	if err != nil {
		return "", err
	}
	return pretty(worker), nil
}

type WorkerRestartTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerRestartTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_restart", "Restart the background task attached to a worker.")
}

func (WorkerRestartTool) Permission() Permission { return PermissionDanger }

func (t WorkerRestartTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	store := workerStore(t.ConfigHome, t.Workspace)
	worker, err := store.Get(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(worker.TaskID) == "" {
		return "", errors.New("worker has no task to restart")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Restart(worker.TaskID, worker.CWD)
	if err != nil {
		return "", err
	}
	worker, err = store.Restart(id, task.ID)
	if err != nil {
		return "", err
	}
	return pretty(worker), nil
}

type WorkerTerminateTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerTerminateTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_terminate", "Terminate a worker and stop its attached task when present.")
}

func (WorkerTerminateTool) Permission() Permission { return PermissionDanger }

func (t WorkerTerminateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	store := workerStore(t.ConfigHome, t.Workspace)
	worker, err := store.Get(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(worker.TaskID) != "" {
		_, _ = taskStore(t.ConfigHome, t.Workspace).Stop(worker.TaskID)
	}
	worker, err = store.Terminate(id)
	if err != nil {
		return "", err
	}
	return pretty(worker), nil
}

type WorkerObserveCompletionTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerObserveCompletionTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_observe_completion",
		Description: "Record worker session completion and classify the finish reason.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id":     map[string]any{"type": "string"},
				"finish_reason": map[string]any{"type": "string"},
				"tokens_output": map[string]any{"type": "integer", "minimum": 0},
			},
			"required": []string{"worker_id", "finish_reason", "tokens_output"},
		},
	}
}

func (WorkerObserveCompletionTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerObserveCompletionTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID     string `json:"worker_id"`
		FinishReason string `json:"finish_reason"`
		TokensOutput int64  `json:"tokens_output"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.TokensOutput < 0 {
		return "", errors.New("tokens_output must be non-negative")
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).Complete(payload.WorkerID, payload.FinishReason, payload.TokensOutput)
	if err != nil {
		return "", err
	}
	worker = WorkerGetTool{Workspace: t.Workspace, ConfigHome: t.ConfigHome}.withTaskStatus(worker)
	return pretty(worker), nil
}

func (t WorkerGetTool) withTaskStatus(worker workers.Worker) workers.Worker {
	if strings.TrimSpace(worker.TaskID) == "" {
		return worker
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Status(worker.TaskID)
	if err != nil {
		worker.TaskStatus = "unknown"
		if worker.LastError == "" {
			worker.LastError = err.Error()
		}
		return worker
	}
	worker.TaskStatus = task.Status
	return worker
}

func workerIDToolDefinition(name string, description string) anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        name,
		Description: description,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id": map[string]any{"type": "string"},
			},
			"required": []string{"worker_id"},
		},
	}
}

func parseWorkerID(input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.WorkerID) == "" {
		return "", errors.New("worker_id is required")
	}
	return strings.TrimSpace(payload.WorkerID), nil
}

func validateWorkerReceipt(receipt *workers.TaskReceipt) error {
	if receipt == nil {
		return nil
	}
	required := map[string]string{
		"repo":              receipt.Repo,
		"task_kind":         receipt.TaskKind,
		"source_surface":    receipt.SourceSurface,
		"objective_preview": receipt.ObjectivePreview,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("task_receipt.%s is required", field)
		}
	}
	return nil
}

func workerStore(configHome string, workspace string) workers.Store {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		if workspace == "" {
			workspace = "."
		}
		configHome = filepath.Join(workspace, ".codog")
	}
	return workers.NewStore(configHome)
}

type TaskCreateTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_create",
		Description: "Start a background shell task in the workspace and return its task metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string"},
				"kind":       map[string]any{"type": "string"},
				"session_id": map[string]any{"type": "string"},
				"restart_policy": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"enabled":       map[string]any{"type": "boolean"},
						"mode":          map[string]any{"type": "string", "enum": []string{"on-failure", "always"}},
						"max_attempts":  map[string]any{"type": "integer", "minimum": 0},
						"delay_seconds": map[string]any{"type": "integer", "minimum": 0},
					},
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (TaskCreateTool) Permission() Permission { return PermissionDanger }

func (t TaskCreateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command   string                    `json:"command"`
		Kind      string                    `json:"kind"`
		SessionID string                    `json:"session_id"`
		Restart   *background.RestartPolicy `json:"restart_policy"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	env, err := toolEnvironment(ctx, t.ConfigHome)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(payload.Command, cwd, background.RunOptions{
		Kind:          payload.Kind,
		SessionID:     payload.SessionID,
		RestartPolicy: payload.Restart,
		Env:           env,
	})
	if err != nil {
		return "", err
	}
	return pretty(task), nil
}

type RunTaskPacketTool struct {
	Workspace  string
	ConfigHome string
	Executable string
}

type taskPacketInput struct {
	Objective         string   `json:"objective"`
	Scope             string   `json:"scope"`
	Repo              string   `json:"repo"`
	BranchPolicy      string   `json:"branch_policy"`
	AcceptanceTests   []string `json:"acceptance_tests"`
	CommitPolicy      string   `json:"commit_policy"`
	ReportingContract string   `json:"reporting_contract"`
	EscalationPolicy  string   `json:"escalation_policy"`
}

func (RunTaskPacketTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "run_task_packet",
		Description: "Create a background task from a structured task packet.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"objective":          map[string]any{"type": "string"},
				"scope":              map[string]any{"type": "string"},
				"repo":               map[string]any{"type": "string"},
				"branch_policy":      map[string]any{"type": "string"},
				"acceptance_tests":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"commit_policy":      map[string]any{"type": "string"},
				"reporting_contract": map[string]any{"type": "string"},
				"escalation_policy":  map[string]any{"type": "string"},
			},
			"required": []string{
				"objective",
				"scope",
				"repo",
				"branch_policy",
				"acceptance_tests",
				"commit_policy",
				"reporting_contract",
				"escalation_policy",
			},
		},
	}
}

func (RunTaskPacketTool) Permission() Permission { return PermissionDanger }

func (t RunTaskPacketTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var packet taskPacketInput
	if err := json.Unmarshal(input, &packet); err != nil {
		return "", err
	}
	if err := validateTaskPacket(packet); err != nil {
		return "", err
	}
	executable := strings.TrimSpace(t.Executable)
	var err error
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	prompt := renderTaskPacketPrompt(packet)
	env, err := toolEnvironment(ctx, t.ConfigHome)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(buildTeamTaskCommand(executable, prompt), cwd, background.RunOptions{
		Kind: "task_packet",
		Env:  env,
	})
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"task_id":     task.ID,
		"status":      task.Status,
		"prompt":      prompt,
		"description": packet.Objective,
		"task_packet": packet,
		"created_at":  task.StartedAt,
		"task":        task,
	}), nil
}

func validateTaskPacket(packet taskPacketInput) error {
	required := map[string]string{
		"objective":          packet.Objective,
		"scope":              packet.Scope,
		"repo":               packet.Repo,
		"branch_policy":      packet.BranchPolicy,
		"commit_policy":      packet.CommitPolicy,
		"reporting_contract": packet.ReportingContract,
		"escalation_policy":  packet.EscalationPolicy,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	if len(packet.AcceptanceTests) == 0 {
		return errors.New("acceptance_tests is required")
	}
	for index, test := range packet.AcceptanceTests {
		if strings.TrimSpace(test) == "" {
			return fmt.Errorf("acceptance_tests[%d] is empty", index)
		}
	}
	return nil
}

func renderTaskPacketPrompt(packet taskPacketInput) string {
	var builder strings.Builder
	builder.WriteString("Execute this structured task packet.\n\n")
	builder.WriteString("Objective:\n")
	builder.WriteString(strings.TrimSpace(packet.Objective))
	builder.WriteString("\n\nScope:\n")
	builder.WriteString(strings.TrimSpace(packet.Scope))
	builder.WriteString("\n\nRepository:\n")
	builder.WriteString(strings.TrimSpace(packet.Repo))
	builder.WriteString("\n\nBranch policy:\n")
	builder.WriteString(strings.TrimSpace(packet.BranchPolicy))
	builder.WriteString("\n\nAcceptance tests:\n")
	for _, test := range packet.AcceptanceTests {
		builder.WriteString("- ")
		builder.WriteString(strings.TrimSpace(test))
		builder.WriteString("\n")
	}
	builder.WriteString("\nCommit policy:\n")
	builder.WriteString(strings.TrimSpace(packet.CommitPolicy))
	builder.WriteString("\n\nReporting contract:\n")
	builder.WriteString(strings.TrimSpace(packet.ReportingContract))
	builder.WriteString("\n\nEscalation policy:\n")
	builder.WriteString(strings.TrimSpace(packet.EscalationPolicy))
	return builder.String()
}

type TaskListTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_list",
		Description: "List background tasks, optionally filtered by session or kind.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string"},
				"kind":       map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
}

func (TaskListTool) Permission() Permission { return PermissionReadOnly }

func (t TaskListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		SessionID string `json:"session_id"`
		Kind      string `json:"kind"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	tasks, err := taskStore(t.ConfigHome, t.Workspace).List()
	if err != nil {
		return "", err
	}
	tasks = background.FilterBySession(tasks, payload.SessionID)
	tasks = background.FilterByKind(tasks, payload.Kind)
	return pretty(map[string]any{"tasks": tasks, "total": len(tasks)}), nil
}

type TaskStatusTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskStatusTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_status",
		Description: "Get background task metadata by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (TaskStatusTool) Permission() Permission { return PermissionReadOnly }

func (t TaskStatusTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID     string `json:"id"`
		TaskID string `json:"task_id"`
		TaskId string `json:"taskId"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.ID, payload.TaskID, payload.TaskId)
	if id == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Status(id)
	if err != nil {
		return "", err
	}
	return pretty(task), nil
}

type TaskGetTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskGetTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_get",
		Description: "Get background task metadata and stored task messages by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"taskId":  map[string]any{"type": "string"},
				"id":      map[string]any{"type": "string"},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
	}
}

func (TaskGetTool) Permission() Permission { return PermissionReadOnly }

func (t TaskGetTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TaskID string `json:"task_id"`
		TaskId string `json:"taskId"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.TaskID, payload.TaskId, payload.ID)
	if strings.TrimSpace(id) == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Status(id)
	if err != nil {
		return "", err
	}
	return pretty(task), nil
}

type TaskUpdateTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskUpdateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_update",
		Description: "Append a message update to a background task registry entry.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"taskId":  map[string]any{"type": "string"},
				"id":      map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"},
			},
			"required":             []string{"task_id", "message"},
			"additionalProperties": false,
		},
	}
}

func (TaskUpdateTool) Permission() Permission { return PermissionDanger }

func (t TaskUpdateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TaskID  string `json:"task_id"`
		TaskId  string `json:"taskId"`
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.TaskID, payload.TaskId, payload.ID)
	if strings.TrimSpace(id) == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Update(id, payload.Message)
	if err != nil {
		return "", err
	}
	last := ""
	if len(task.Messages) > 0 {
		last = task.Messages[len(task.Messages)-1].Message
	}
	return pretty(map[string]any{
		"id":            task.ID,
		"status":        task.Status,
		"message_count": len(task.Messages),
		"last_message":  last,
	}), nil
}

type TaskStopTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskStopTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_stop",
		Description: "Stop a running background task by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":       map[string]any{"type": "string"},
				"task_id":  map[string]any{"type": "string"},
				"taskId":   map[string]any{"type": "string"},
				"shell_id": map[string]any{"type": "string"},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (TaskStopTool) Permission() Permission { return PermissionWorkspace }

func (t TaskStopTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID      string `json:"id"`
		TaskID  string `json:"task_id"`
		TaskId  string `json:"taskId"`
		ShellID string `json:"shell_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.ID, payload.TaskID, payload.TaskId, payload.ShellID)
	if id == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Stop(id)
	if err != nil {
		return "", err
	}
	return pretty(task), nil
}

type TaskOutputTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskOutputTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_output",
		Description: "Read recent background task log output by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "string"},
				"task_id":     map[string]any{"type": "string"},
				"taskId":      map[string]any{"type": "string"},
				"limit_bytes": map[string]any{"type": "integer", "minimum": 1},
				"block":       map[string]any{"type": "boolean"},
				"timeout":     map[string]any{"type": "integer", "minimum": 0},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (TaskOutputTool) Permission() Permission { return PermissionReadOnly }

func (t TaskOutputTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID         string `json:"id"`
		TaskID     string `json:"task_id"`
		TaskId     string `json:"taskId"`
		LimitBytes int64  `json:"limit_bytes"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.LimitBytes <= 0 {
		payload.LimitBytes = 64 * 1024
	}
	store := taskStore(t.ConfigHome, t.Workspace)
	id := firstNonEmpty(payload.ID, payload.TaskID, payload.TaskId)
	if id == "" {
		return "", errors.New("task_id is required")
	}
	task, err := store.Status(id)
	if err != nil {
		return "", err
	}
	output, err := store.Logs(id, payload.LimitBytes)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"id":        id,
		"task_id":   id,
		"status":    task.Status,
		"exit_code": task.ExitCode,
		"error":     task.Error,
		"output":    output,
	}), nil
}

type TaskSuperviseTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskSuperviseTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_supervise",
		Description: "Run one background task supervisor pass and restart eligible tasks with restart policies.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
		},
	}
}

func (TaskSuperviseTool) Permission() Permission { return PermissionDanger }

func (t TaskSuperviseTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	if len(input) != 0 && string(input) != "null" {
		var payload map[string]any
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
		if len(payload) != 0 {
			return "", errors.New("task_supervise does not accept input fields")
		}
	}
	result, err := taskStore(t.ConfigHome, t.Workspace).SuperviseOnce(time.Now().UTC())
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

func taskStore(configHome string, workspace string) background.Store {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		if workspace == "" {
			workspace = "."
		}
		configHome = filepath.Join(workspace, ".codog")
	}
	return background.NewStore(configHome)
}

type AskUserQuestionTool struct {
	In  io.Reader
	Out io.Writer
}

func (AskUserQuestionTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "ask_user_question",
		Description: "Ask the user a concise question and return their answer to the model.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
				"choices":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"default":  map[string]any{"type": "string"},
			},
			"required":             []string{"question"},
			"additionalProperties": false,
		},
	}
}

func (AskUserQuestionTool) Permission() Permission { return PermissionReadOnly }

func (t AskUserQuestionTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Question string   `json:"question"`
		Choices  []string `json:"choices"`
		Default  string   `json:"default"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Question = strings.TrimSpace(payload.Question)
	if payload.Question == "" {
		return "", errors.New("question is required")
	}
	in := t.In
	if in == nil {
		in = os.Stdin
	}
	out := t.Out
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintf(out, "\n%s\n", payload.Question)
	choices := normalizeQuestionChoices(payload.Choices)
	for index, choice := range choices {
		fmt.Fprintf(out, "  %d. %s\n", index+1, choice)
	}
	if strings.TrimSpace(payload.Default) != "" {
		fmt.Fprintf(out, "Default: %s\n", strings.TrimSpace(payload.Default))
	}
	fmt.Fprint(out, "Answer: ")

	answerCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			errCh <- err
			return
		}
		answerCh <- strings.TrimSpace(line)
	}()
	var answer string
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case answer = <-answerCh:
	}
	if answer == "" {
		answer = strings.TrimSpace(payload.Default)
	}
	answer = resolveQuestionChoice(answer, choices)
	return pretty(map[string]any{
		"question": payload.Question,
		"answer":   answer,
	}), nil
}

func normalizeQuestionChoices(choices []string) []string {
	out := make([]string, 0, len(choices))
	seen := map[string]struct{}{}
	for _, choice := range choices {
		choice = strings.TrimSpace(choice)
		if choice == "" {
			continue
		}
		key := strings.ToLower(choice)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, choice)
	}
	return out
}

func resolveQuestionChoice(answer string, choices []string) string {
	if answer == "" || len(choices) == 0 {
		return answer
	}
	if index, err := strconv.Atoi(answer); err == nil && index >= 1 && index <= len(choices) {
		return choices[index-1]
	}
	for _, choice := range choices {
		if strings.EqualFold(answer, choice) {
			return choice
		}
	}
	return answer
}

type BriefTool struct {
	Workspace      string
	AdditionalDirs []string
}

type briefAttachment struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsImage bool   `json:"is_image"`
}

func (BriefTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "brief",
		Description: "Return a user-facing brief message with optional workspace attachment metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"attachments": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"status": map[string]any{"type": "string", "enum": []string{"normal", "proactive"}},
			},
			"required":             []string{"message", "status"},
			"additionalProperties": false,
		},
	}
}

func (BriefTool) Permission() Permission { return PermissionReadOnly }

func (t BriefTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Message     string   `json:"message"`
		Attachments []string `json:"attachments"`
		Status      string   `json:"status"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" {
		return "", errors.New("message is required")
	}
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	switch status {
	case "normal", "proactive":
	default:
		return "", fmt.Errorf("unknown brief status %q", payload.Status)
	}
	attachments := make([]briefAttachment, 0, len(payload.Attachments))
	for _, attachment := range payload.Attachments {
		path, err := safePathInScope(t.Workspace, t.AdditionalDirs, attachment, false)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		attachments = append(attachments, briefAttachment{
			Path:    path,
			Size:    info.Size(),
			IsImage: isImageAttachment(path),
		})
	}
	return pretty(map[string]any{
		"message":     payload.Message,
		"status":      status,
		"attachments": attachments,
		"sent_at":     time.Now().UTC().Format(time.RFC3339),
	}), nil
}

type SendUserMessageTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (SendUserMessageTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "send_user_message",
		Description: "Send a user-facing message with optional workspace attachment metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"attachments": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"status": map[string]any{"type": "string", "enum": []string{"normal", "proactive"}},
			},
			"required":             []string{"message", "status"},
			"additionalProperties": false,
		},
	}
}

func (SendUserMessageTool) Permission() Permission { return PermissionReadOnly }

func (t SendUserMessageTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return BriefTool{Workspace: t.Workspace, AdditionalDirs: t.AdditionalDirs}.Execute(ctx, input)
}

func isImageAttachment(path string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp", "svg":
		return true
	default:
		return false
	}
}

type StructuredOutputTool struct{}

func (StructuredOutputTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "structured_output",
		Description: "Return the provided non-empty JSON object as structured output.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		},
	}
}

func (StructuredOutputTool) Permission() Permission { return PermissionReadOnly }

func (StructuredOutputTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if len(payload) == 0 {
		return "", errors.New("structured output payload must not be empty")
	}
	return pretty(map[string]any{
		"data":              "Structured output provided successfully",
		"structured_output": payload,
	}), nil
}

type SleepTool struct{}

func (SleepTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "sleep",
		Description: "Sleep for a bounded duration in milliseconds.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"duration_ms": map[string]any{"type": "integer", "minimum": 0},
			},
			"required":             []string{"duration_ms"},
			"additionalProperties": false,
		},
	}
}

func (SleepTool) Permission() Permission { return PermissionReadOnly }

func (SleepTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		DurationMS int `json:"duration_ms"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.DurationMS < 0 {
		return "", errors.New("duration_ms must be non-negative")
	}
	if payload.DurationMS > 300000 {
		return "", errors.New("duration_ms must be 300000 or less")
	}
	timer := time.NewTimer(time.Duration(payload.DurationMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
	}
	return pretty(map[string]any{
		"duration_ms": payload.DurationMS,
		"message":     fmt.Sprintf("Slept for %dms", payload.DurationMS),
	}), nil
}

type REPLTool struct {
	Workspace string
}

func (REPLTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "repl",
		Description: "Execute code in a REPL-like subprocess for shell, python, or node.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code":       map[string]any{"type": "string"},
				"language":   map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"code", "language"},
			"additionalProperties": false,
		},
	}
}

func (REPLTool) Permission() Permission { return PermissionDanger }

func (t REPLTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Code      string `json:"code"`
		Language  string `json:"language"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Code = strings.TrimSpace(payload.Code)
	if payload.Code == "" {
		return "", errors.New("code is required")
	}
	args, err := replCommand(payload.Language, payload.Code)
	if err != nil {
		return "", err
	}
	timeout := time.Duration(payload.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 5*time.Minute {
		return "", errors.New("timeout_ms must be 300000 or less")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = t.Workspace
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return pretty(map[string]any{
		"language":    strings.ToLower(strings.TrimSpace(payload.Language)),
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"exit_code":   exitCode,
		"duration_ms": time.Since(start).Milliseconds(),
		"timed_out":   ctx.Err() == context.DeadlineExceeded,
	}), nil
}

func replCommand(language string, code string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "sh", "shell", "bash":
		return []string{"sh", "-c", code}, nil
	case "python", "python3", "py":
		return []string{"python3", "-c", code}, nil
	case "javascript", "js", "node":
		return []string{"node", "-e", code}, nil
	default:
		return nil, fmt.Errorf("unsupported repl language %q", language)
	}
}

type SkillTool struct {
	Workspace  string
	ConfigHome string
}

func (SkillTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "skill",
		Description: "Load a local Codog or Claude-style skill definition and render its invocation text.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill": map[string]any{
					"type":        "string",
					"description": "Skill name, such as review or team:audit.",
				},
				"args": map[string]any{
					"type":        "string",
					"description": "Optional user request or arguments to render with the skill.",
				},
			},
			"required":             []string{"skill"},
			"additionalProperties": false,
		},
	}
}

func (SkillTool) Permission() Permission {
	return PermissionReadOnly
}

func (t SkillTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Skill string `json:"skill"`
		Args  string `json:"args"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Skill) == "" {
		return "", errors.New("skill is required")
	}
	skill, err := skills.Find(t.ConfigHome, t.Workspace, payload.Skill)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"kind":     "skill",
		"skill":    skill.Name,
		"source":   skill.Source,
		"path":     skill.Path,
		"args":     strings.TrimSpace(payload.Args),
		"prompt":   skill.Body,
		"rendered": skills.RenderInvocation(skill, payload.Args),
	}), nil
}

type ConfigTool struct {
	Workspace  string
	ConfigHome string
}

func (ConfigTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "config",
		Description: "Get or set a Codog user config setting in the user config file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"setting": map[string]any{
					"type":        "string",
					"description": "Dotted config key, such as model, max_tokens, permission_mode, or future.sandbox_strategy.",
				},
				"value": map[string]any{
					"description": "When present, sets the setting to this JSON value. When omitted, reads the current user config value.",
				},
			},
			"required":             []string{"setting"},
			"additionalProperties": false,
		},
	}
}

func (ConfigTool) Permission() Permission {
	return PermissionWorkspace
}

func (t ConfigTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return "", err
	}
	var setting string
	if data := raw["setting"]; len(data) != 0 {
		if err := json.Unmarshal(data, &setting); err != nil {
			return "", err
		}
	}
	setting = strings.TrimSpace(setting)
	if err := validateConfigToolSetting(setting); err != nil {
		return "", err
	}
	path := configToolPath(t.ConfigHome, t.Workspace)
	current, err := readConfigToolFile(path)
	if err != nil {
		return "", err
	}
	previous, _ := nestedConfigToolValue(current, setting)
	valueData, hasValue := raw["value"]
	if !hasValue {
		return pretty(map[string]any{
			"success":   true,
			"operation": "get",
			"setting":   setting,
			"value":     redactConfigToolValue(setting, previous),
			"path":      path,
		}), nil
	}
	var value any
	if err := json.Unmarshal(valueData, &value); err != nil {
		return "", err
	}
	report, err := config.SetFileValue(path, setting, value)
	if err != nil {
		return "", err
	}
	updated, err := readConfigToolFile(path)
	if err != nil {
		return "", err
	}
	newValue, _ := nestedConfigToolValue(updated, setting)
	return pretty(map[string]any{
		"success":        true,
		"operation":      report.Action,
		"setting":        setting,
		"previous_value": redactConfigToolValue(setting, previous),
		"new_value":      redactConfigToolValue(setting, newValue),
		"path":           report.Path,
	}), nil
}

func configToolPath(configHome string, workspace string) string {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		if workspace == "" {
			workspace = "."
		}
		configHome = filepath.Join(workspace, ".codog")
	}
	return filepath.Join(configHome, "config.json")
}

func validateConfigToolSetting(setting string) error {
	if setting == "" {
		return errors.New("setting is required")
	}
	if strings.ContainsAny(setting, `/\`) {
		return fmt.Errorf("invalid config setting %q", setting)
	}
	for _, part := range strings.Split(setting, ".") {
		if strings.TrimSpace(part) == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid config setting %q", setting)
		}
	}
	return nil
}

func readConfigToolFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

func nestedConfigToolValue(root map[string]any, setting string) (any, bool) {
	var current any = root
	for _, part := range strings.Split(setting, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func redactConfigToolValue(setting string, value any) any {
	key := strings.ToLower(setting)
	if !strings.Contains(key, "token") && !strings.Contains(key, "api_key") && !strings.Contains(key, "apikey") && !strings.Contains(key, "secret") {
		return value
	}
	if value == nil {
		return nil
	}
	return "[redacted]"
}

type ToolSearchTool struct {
	Registry *Registry
}

func (ToolSearchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "tool_search",
		Description: "Search the currently available Codog tools by name, description, or permission.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string"},
				"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			},
			"additionalProperties": false,
		},
	}
}

func (ToolSearchTool) Permission() Permission { return PermissionReadOnly }

func (t ToolSearchTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	if t.Registry == nil {
		return "", errors.New("tool registry is not available")
	}
	var payload struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	limit := payload.MaxResults
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	matches := searchToolInfos(t.Registry.Infos(), payload.Query, limit)
	return pretty(map[string]any{
		"query":   strings.TrimSpace(payload.Query),
		"matches": matches,
		"total":   len(matches),
	}), nil
}

func searchToolInfos(infos []ToolInfo, query string, limit int) []ToolInfo {
	query = strings.ToLower(strings.TrimSpace(query))
	terms := strings.Fields(query)
	type scored struct {
		info  ToolInfo
		score int
	}
	scoredMatches := make([]scored, 0, len(infos))
	for _, info := range infos {
		score := 1
		if query != "" {
			score = toolInfoScore(info, terms, query)
			if score == 0 {
				continue
			}
		}
		scoredMatches = append(scoredMatches, scored{info: info, score: score})
	}
	sort.Slice(scoredMatches, func(i, j int) bool {
		if scoredMatches[i].score != scoredMatches[j].score {
			return scoredMatches[i].score > scoredMatches[j].score
		}
		return scoredMatches[i].info.Name < scoredMatches[j].info.Name
	})
	if len(scoredMatches) > limit {
		scoredMatches = scoredMatches[:limit]
	}
	matches := make([]ToolInfo, 0, len(scoredMatches))
	for _, match := range scoredMatches {
		matches = append(matches, match.info)
	}
	return matches
}

func toolInfoScore(info ToolInfo, terms []string, query string) int {
	haystack := strings.ToLower(info.Name + " " + info.Description + " " + string(info.Permission))
	score := 0
	if strings.EqualFold(info.Name, query) {
		score += 20
	}
	if strings.Contains(strings.ToLower(info.Name), query) {
		score += 10
	}
	for _, term := range terms {
		if strings.Contains(strings.ToLower(info.Name), term) {
			score += 6
		}
		if strings.Contains(haystack, term) {
			score += 2
		} else {
			return 0
		}
	}
	return score
}

type TodoReadTool struct {
	Workspace string
}

func (TodoReadTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "todo_read",
		Description: "Read the workspace todo list for the current task.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}
}

func (TodoReadTool) Permission() Permission { return PermissionReadOnly }

func (t TodoReadTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	report, err := todos.List(t.Workspace)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type TodoWriteTool struct {
	Workspace string
}

func (TodoWriteTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "todo_write",
		Description: "Replace the workspace todo list. Use pending, in_progress, or completed status.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":         map[string]any{"type": "string"},
							"content":    map[string]any{"type": "string"},
							"activeForm": map[string]any{"type": "string"},
							"status":     map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
							"priority":   map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
						},
						"required":             []string{"content", "status", "activeForm"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"todos"},
			"additionalProperties": false,
		},
	}
}

func (TodoWriteTool) Permission() Permission { return PermissionWorkspace }

func (t TodoWriteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Todos []todos.Item `json:"todos"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	oldReport, err := todos.List(t.Workspace)
	if err != nil {
		return "", err
	}
	submitted := todos.NormalizeItems(payload.Todos)
	persisted := submitted
	allCompleted := todoItemsAllCompleted(submitted)
	if allCompleted {
		persisted = nil
	}
	report, err := todos.Replace(t.Workspace, persisted)
	if err != nil {
		return "", err
	}
	output := todoWriteOutput{
		Kind:                    report.Kind,
		Action:                  report.Action,
		Status:                  report.Status,
		Total:                   report.Total,
		Items:                   report.Items,
		OldTodos:                todoWriteListItems(oldReport.Items),
		NewTodos:                todoWriteListItems(submitted),
		VerificationNudgeNeeded: todoWriteVerificationNudgeNeeded(submitted, allCompleted),
	}
	return pretty(output), nil
}

func todoItemsAllCompleted(items []todos.Item) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if strings.TrimSpace(item.Status) != "completed" {
			return false
		}
	}
	return true
}

type todoWriteOutput struct {
	Kind                    string              `json:"kind"`
	Action                  string              `json:"action"`
	Status                  string              `json:"status"`
	Total                   int                 `json:"total"`
	Items                   []todos.Item        `json:"items"`
	OldTodos                []todoWriteListItem `json:"oldTodos"`
	NewTodos                []todoWriteListItem `json:"newTodos"`
	VerificationNudgeNeeded bool                `json:"verificationNudgeNeeded"`
}

type todoWriteListItem struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
}

func todoWriteListItems(items []todos.Item) []todoWriteListItem {
	out := make([]todoWriteListItem, 0, len(items))
	for _, item := range items {
		out = append(out, todoWriteListItem{
			Content:    item.Content,
			ActiveForm: item.ActiveForm,
			Status:     item.Status,
		})
	}
	return out
}

func todoWriteVerificationNudgeNeeded(items []todos.Item, allCompleted bool) bool {
	if !allCompleted || len(items) < 3 {
		return false
	}
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Content), "verif") {
			return false
		}
	}
	return true
}

func safePath(workspace, requested string, allowMissing bool) (string, error) {
	return safePathInScope(workspace, nil, requested, allowMissing)
}

func readFileLimited(path string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		maxBytes = maxFileToolBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return data[:maxBytes], true, nil
	}
	return data, false, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func imageMediaType(path string, data []byte) (string, bool) {
	detected := strings.ToLower(http.DetectContentType(data[:min(len(data), 512)]))
	if strings.HasPrefix(detected, "image/") {
		return detected, true
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "bmp":
		return "image/bmp", true
	case "gif":
		return "image/gif", true
	case "jpg", "jpeg":
		return "image/jpeg", true
	case "png":
		return "image/png", true
	case "svg":
		return "image/svg+xml", true
	case "webp":
		return "image/webp", true
	default:
		return "", false
	}
}

func imageReadResult(path string, data []byte, mediaType string) map[string]any {
	result := map[string]any{
		"kind":       "image",
		"path":       path,
		"bytes":      len(data),
		"media_type": mediaType,
		"encoding":   "base64",
		"base64":     base64.StdEncoding.EncodeToString(data),
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		result["width"] = cfg.Width
		result["height"] = cfg.Height
	}
	return result
}

func safePathInScope(workspace string, additionalDirs []string, requested string, allowMissing bool) (string, error) {
	if requested == "" {
		return "", errors.New("path is required")
	}
	if workspace == "" {
		workspace = "."
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		roots[0] = resolved
	} else {
		return "", err
	}
	for _, dir := range additionalDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		roots = append(roots, resolved)
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved := ""
	if allowMissing {
		resolved, err = resolveMissingCandidate(candidate)
		if err != nil {
			return "", err
		}
	} else {
		resolved, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", err
		}
	}
	for _, root := range roots {
		if pathWithin(root, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path escapes workspace scope: %s", requested)
}

func resolveMissingCandidate(candidate string) (string, error) {
	var missing []string
	cursor := candidate
	for {
		resolved, err := filepath.EvalSymlinks(cursor)
		if err == nil {
			parts := append([]string{resolved}, missing...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", err
		}
		missing = append([]string{filepath.Base(cursor)}, missing...)
		cursor = parent
	}
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func displayPath(workspace string, path string) string {
	root, err := filepath.Abs(workspace)
	if err == nil {
		if resolved, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
			root = resolved
		}
		displayCandidate := path
		if resolved, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
			displayCandidate = resolved
		}
		if rel, relErr := filepath.Rel(root, displayCandidate); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
			return rel
		}
	}
	return path
}

func ignoredDir(name string) bool {
	switch name {
	case ".git", "node_modules", "target", "dist", "coverage", ".next", ".cache":
		return true
	default:
		return false
	}
}

func pretty(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
