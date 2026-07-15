// Package tools defines the model-callable workspace tools and their execution
// registry.
package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	// Register image decoders for read_image and notebook image outputs.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/bashvalidation"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/mcpauthdiag"
	"github.com/Rememorio/codog/internal/powershellvalidation"
	"github.com/Rememorio/codog/internal/toolnames"
)

// Permission is the minimum host access level required to run a tool.
type Permission string

const (
	// PermissionReadOnly allows inspection-only tools.
	PermissionReadOnly Permission = "read-only"
	// PermissionWorkspace allows writes inside the configured workspace.
	PermissionWorkspace Permission = "workspace-write"
	// PermissionDanger allows unrestricted host actions.
	PermissionDanger Permission = "danger-full-access"
	// PermissionPrompt requires a user approval decision before execution.
	PermissionPrompt Permission = "prompt"
	// PermissionAllow marks tools that are explicitly allow-listed.
	PermissionAllow    Permission = "allow"
	maxFileToolBytes   int64      = 2_000_000
	maxRichReadBytes   int64      = 5 * 1024 * 1024
	maxRemoteBodyBytes int64      = 2_000_000
	maxRAGBodyBytes    int64      = 2_000_000
	maxRAGQueryChars              = 12_000
)

// Tool is the runtime contract implemented by every model-callable tool.
type Tool interface {
	Definition() anthropic.ToolDefinition
	Permission() Permission
	Execute(context.Context, json.RawMessage) (string, error)
}

// CommandTool adapts a local executable into a model-callable tool.
// Tools without an explicit permission default to danger-full-access because
// they run arbitrary host executables.
type CommandTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Required    Permission
	Command     string
	Args        []string
	Workspace   string
	ConfigEnv   map[string]string
}

// MCPTool adapts a remote MCP tool into Codog's local tool contract.
type MCPTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Required    Permission
	ServerName  string
	Server      config.MCPServerConfig
	RemoteName  string
	Options     func() mcp.ClientOptions
	Clients     *mcp.ClientPool
}

// Registry holds all tools available for a model turn.
type Registry struct {
	tools        map[string]Tool
	workspace    string
	configHome   string
	mcpServers   map[string]config.MCPServerConfig
	mcpClients   *mcp.ClientPool
	lspClients   *codeintel.LSPClientPool
	mcpOptionsMu sync.RWMutex
	mcpOptions   mcp.ClientOptions
}

// ToolInfo is the JSON-safe metadata view of one registered tool.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Permission  Permission     `json:"permission"`
	InputSchema map[string]any `json:"input_schema"`
}

// RegistryOptions controls optional tool integrations and execution defaults.
type RegistryOptions struct {
	SandboxStrategy  string
	Sandbox          config.SandboxConfig
	AdditionalDirs   []string
	ConfigHome       string
	ConfigEnv        map[string]string
	Executable       string
	DefaultShell     string
	TrustedRoots     []string
	RespectGitignore bool
	OAuthProfile     string
	MCPServers       map[string]config.MCPServerConfig
	PowerShell       string
	RAGBaseURL       string
	RAGTimeout       time.Duration
	RAGTopKMax       int
	QuestionIn       io.Reader
	QuestionOut      io.Writer
	AgentDefinitions []agentdefs.Definition
	PluginDirs       []string
}

// The initial model surface stays intentionally small. Less common tools are
// loaded after tool_search returns their schemas, while product-mode output
// tools remain available only through an explicit --tools selection.
var eagerModelTools = map[string]struct{}{
	"agent":       {},
	"apply_patch": {},
	"bash":        {},
	"bash_output": {},
	"edit_file":   {},
	"glob":        {},
	"grep":        {},
	"kill_bash":   {},
	"ls":          {},
	"multi_edit":  {},
	"read_file":   {},
	"skill":       {},
	"todo_read":   {},
	"tool_search": {},
	"write_file":  {},
}

var explicitModelTools = map[string]struct{}{
	"brief":             {},
	"send_user_message": {},
	"structured_output": {},
}

var claudeToolAliases = map[string]string{
	"agenttool":                    "agent",
	"applypatch":                   "apply_patch",
	"applypatchtool":               "apply_patch",
	"approvaltoken":                "approval_token",
	"approvaltokentool":            "approval_token",
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
	"nudge":                        "nudge",
	"nudgetool":                    "nudge",
	"provisionalstatus":            "provisional_status",
	"provisionalstatustool":        "provisional_status",
	"roadmappinpoint":              "roadmap_pinpoint",
	"roadmappinpointtool":          "roadmap_pinpoint",
	"reportbackpressure":           "report_backpressure",
	"reportbackpressuretool":       "report_backpressure",
	"reportschema":                 "report_schema",
	"reportschematool":             "report_schema",
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
	"branchfreshness":              "branch_freshness",
	"branchfreshnesstool":          "branch_freshness",
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
	"permissioncheck":              "permission_check",
	"permissionchecktool":          "permission_check",
	"powershell":                   "powershell",
	"powershelltool":               "powershell",
	"policyevaluate":               "policy_evaluate",
	"policyevaluatetool":           "policy_evaluate",
	"read":                         "read_file",
	"readfile":                     "read_file",
	"readtool":                     "read_file",
	"readmcpresource":              "read_mcp_resource",
	"readmcpresourcetool":          "read_mcp_resource",
	"retrievecontext":              "retrieve_context",
	"retrievecontexttool":          "retrieve_context",
	"recoveryattempt":              "recovery_attempt",
	"recoveryattempttool":          "recovery_attempt",
	"recoveryrecipe":               "recovery_recipe",
	"recoveryrecipetool":           "recovery_recipe",
	"recoverystatus":               "recovery_status",
	"recoverystatustool":           "recovery_status",
	"repl":                         "repl",
	"repltool":                     "repl",
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
	"taskheartbeat":                "task_heartbeat",
	"taskheartbeattool":            "task_heartbeat",
	"tasklaneboard":                "task_lane_board",
	"tasklaneboardtool":            "task_lane_board",
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
	"testingpermission":            "permission_check",
	"testingpermissiontool":        "permission_check",
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
	"workerstartuptimeout":         "worker_startup_timeout",
	"workerstartuptimeouttool":     "worker_startup_timeout",
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
	"ApplyPatch":                   "apply_patch",
	"ApplyPatchTool":               "apply_patch",
	"ApprovalToken":                "approval_token",
	"ApprovalTokenTool":            "approval_token",
	"AskUserQuestion":              "ask_user_question",
	"AskUserQuestionTool":          "ask_user_question",
	"Bash":                         "bash",
	"BashOutput":                   "bash_output",
	"BashOutputTool":               "bash_output",
	"BashTool":                     "bash",
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
	"Edit":                         "edit_file",
	"EditFile":                     "edit_file",
	"EditTool":                     "edit_file",
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
	"FileEdit":                     "edit_file",
	"FileEditTool":                 "edit_file",
	"FileRead":                     "read_file",
	"FileReadTool":                 "read_file",
	"FileWrite":                    "write_file",
	"FileWriteTool":                "write_file",
	"GetMcpPrompt":                 "get_mcp_prompt",
	"GetMcpPromptTool":             "get_mcp_prompt",
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
	"MCP":                          "mcp",
	"MCPTool":                      "mcp",
	"McpAuth":                      "mcp_auth",
	"McpAuthTool":                  "mcp_auth",
	"MultiEdit":                    "multi_edit",
	"MultiEditFile":                "multi_edit",
	"MultiEditTool":                "multi_edit",
	"NotebookEdit":                 "notebook_edit",
	"NotebookEditTool":             "notebook_edit",
	"NotebookRead":                 "notebook_read",
	"NotebookReadTool":             "notebook_read",
	"PermissionCheck":              "permission_check",
	"PermissionCheckTool":          "permission_check",
	"Nudge":                        "nudge",
	"NudgeTool":                    "nudge",
	"ProvisionalStatus":            "provisional_status",
	"ProvisionalStatusTool":        "provisional_status",
	"PowerShell":                   "powershell",
	"PowerShellTool":               "powershell",
	"PolicyEvaluate":               "policy_evaluate",
	"PolicyEvaluateTool":           "policy_evaluate",
	"Read":                         "read_file",
	"ReadFile":                     "read_file",
	"ReadMcpResource":              "read_mcp_resource",
	"ReadMcpResourceTool":          "read_mcp_resource",
	"ReadTool":                     "read_file",
	"RetrieveContext":              "retrieve_context",
	"RetrieveContextTool":          "retrieve_context",
	"RecoveryAttempt":              "recovery_attempt",
	"RecoveryAttemptTool":          "recovery_attempt",
	"RecoveryRecipe":               "recovery_recipe",
	"RecoveryRecipeTool":           "recovery_recipe",
	"RecoveryStatus":               "recovery_status",
	"RecoveryStatusTool":           "recovery_status",
	"RoadmapPinpoint":              "roadmap_pinpoint",
	"RoadmapPinpointTool":          "roadmap_pinpoint",
	"ReportBackpressure":           "report_backpressure",
	"ReportBackpressureTool":       "report_backpressure",
	"ReportSchema":                 "report_schema",
	"ReportSchemaTool":             "report_schema",
	"RemoteTrigger":                "remote_trigger",
	"RemoteTriggerTool":            "remote_trigger",
	"RunTaskPacket":                "run_task_packet",
	"RunTaskPacketTool":            "run_task_packet",
	"SendMessage":                  "send_user_message",
	"SendMessageTool":              "send_user_message",
	"SendUserMessage":              "send_user_message",
	"SendUserMessageTool":          "send_user_message",
	"Skill":                        "skill",
	"SkillTool":                    "skill",
	"REPL":                         "repl",
	"REPLTool":                     "repl",
	"Sleep":                        "sleep",
	"SleepTool":                    "sleep",
	"StructuredOutput":             "structured_output",
	"StructuredOutputTool":         "structured_output",
	"SyntheticOutputTool":          "structured_output",
	"Task":                         "agent",
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
	"TestingPermission":            "permission_check",
	"TestingPermissionTool":        "permission_check",
	"TodoRead":                     "todo_read",
	"TodoReadTool":                 "todo_read",
	"TodoWrite":                    "todo_write",
	"TodoWriteTool":                "todo_write",
	"ToolSearch":                   "tool_search",
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
	"WorkerStartupTimeout":         "worker_startup_timeout",
	"WorkerStartupTimeoutTool":     "worker_startup_timeout",
	"WorkerTerminate":              "worker_terminate",
	"WorkerTerminateTool":          "worker_terminate",
	"Write":                        "write_file",
	"WriteFile":                    "write_file",
	"WriteTool":                    "write_file",
}

// ClaudeToolAliases returns the Claude-style display aliases accepted by the
// tool registry, keyed by alias and mapped to Codog's canonical tool names.
func ClaudeToolAliases() map[string]string {
	aliases := make(map[string]string, len(claudeToolAliasDisplay))
	for alias, canonical := range claudeToolAliasDisplay {
		aliases[alias] = canonical
	}
	return aliases
}

// CanonicalToolName normalizes a model-supplied tool name or known alias into
// the canonical registry name used for permission and execution checks.
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

// Prompter decides whether a tool invocation may run under the active
// permission mode and records the decision for callers that need audit data.
type Prompter struct {
	Mode           Permission
	AllowRules     []string
	DenyRules      []string
	AskRules       []string
	DeniedTools    []string
	Workspace      string
	AdditionalDirs []string
	DefaultShell   string
	In             io.Reader
	Err            io.Writer
	OnRequest      func(PermissionDecision)
	OnDecision     func(PermissionDecision)
}

// PermissionResponse is an interactive answer to a permission request.
// Decision accepts allow_once, allow_always, or deny. Feedback is passed back
// to the model, and Rule narrows an allow_always answer to the current tool.
type PermissionResponse struct {
	Decision string `json:"decision"`
	Feedback string `json:"feedback,omitempty"`
	Rule     string `json:"rule,omitempty"`
}

// PermissionDecision captures the resolved permission outcome for one proposed
// tool invocation.
type PermissionDecision struct {
	ToolName    string
	Required    Permission
	Mode        Permission
	Input       string
	Allowed     bool
	WouldPrompt bool
	Reason      string
	Message     string
	Feedback    string
	Rule        string
}

// NewRegistry constructs the default tool registry for a workspace.
func NewRegistry(workspace string) *Registry {
	return NewRegistryWithOptions(workspace, RegistryOptions{})
}

// NewRegistryWithOptions constructs the default tool registry and wires optional
// integrations such as MCP servers, sandbox defaults, config state, and plugin
// execution settings.
func NewRegistryWithOptions(workspace string, opts RegistryOptions) *Registry {
	reg := &Registry{
		tools:      map[string]Tool{},
		workspace:  workspace,
		configHome: opts.ConfigHome,
		mcpServers: cloneMCPServers(opts.MCPServers),
		mcpClients: mcp.NewClientPool(),
		lspClients: codeintel.NewLSPClientPool(),
	}
	reg.registerBuiltinTools(workspace, opts)
	return reg
}

// Close releases persistent protocol sessions owned by the registry.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	var joined error
	if r.mcpClients != nil {
		joined = errors.Join(joined, r.mcpClients.Close())
	}
	if r.lspClients != nil {
		joined = errors.Join(joined, r.lspClients.Close())
	}
	return joined
}

// LSPClientPool returns the registry-owned language-server session pool.
func (r *Registry) LSPClientPool() *codeintel.LSPClientPool {
	if r == nil {
		return nil
	}
	return r.lspClients
}

// Register adds or replaces a tool by the name declared in its definition.
func (r *Registry) Register(tool Tool) {
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	r.tools[tool.Definition().Name] = tool
}

// RemoveMCPTools removes dynamically discovered MCP tool adapters while
// preserving the built-in MCP management tools.
func (r *Registry) RemoveMCPTools() {
	for name, tool := range r.tools {
		if _, ok := tool.(MCPTool); ok {
			delete(r.tools, name)
		}
	}
}

// UpdateBuiltinScope re-registers built-in tools with a new workspace or
// execution configuration while preserving the registry object.
func (r *Registry) UpdateBuiltinScope(workspace string, opts RegistryOptions) {
	r.registerBuiltinTools(workspace, opts)
}

func (r *Registry) registerBuiltinTools(workspace string, opts RegistryOptions) {
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	r.mcpServers = cloneMCPServers(opts.MCPServers)
	r.SetMCPClientOptions(mcpClientOptions(workspace, opts.AdditionalDirs))
	r.Register(BashTool{Workspace: workspace, ConfigHome: opts.ConfigHome, ConfigEnv: opts.ConfigEnv, DefaultShell: opts.DefaultShell, PowerShell: opts.PowerShell, SandboxStrategy: opts.SandboxStrategy, Sandbox: opts.Sandbox})
	r.Register(PowerShellTool{Workspace: workspace, ConfigHome: opts.ConfigHome, ConfigEnv: opts.ConfigEnv, Executable: opts.PowerShell})
	r.Register(BashOutputTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(KillBashTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(ReadFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(WriteFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(EditFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(MultiEditTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(ApplyPatchTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(GrepTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs, RespectGitignore: opts.RespectGitignore})
	r.Register(GlobTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs, RespectGitignore: opts.RespectGitignore})
	r.Register(LSTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(WebFetchTool{})
	r.Register(WebSearchTool{})
	if ragBaseURL := configuredRAGBaseURL(opts.RAGBaseURL); ragBaseURL != "" {
		r.Register(RetrieveContextTool{
			BaseURL: ragBaseURL,
			Timeout: opts.RAGTimeout,
			TopKMax: opts.RAGTopKMax,
		})
	}
	r.Register(RemoteTriggerTool{})
	r.Register(PermissionCheckTool{})
	r.Register(NotebookReadTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(NotebookEditTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(LSPTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs, ConfigHome: opts.ConfigHome, Clients: r.lspClients})
	r.Register(EnterWorktreeTool{Workspace: workspace})
	r.Register(ExitWorktreeTool{Workspace: workspace})
	r.Register(EnterPlanModeTool{Workspace: workspace})
	r.Register(ExitPlanModeTool{Workspace: workspace})
	r.Register(AgentTool{Workspace: workspace, ConfigHome: opts.ConfigHome, ConfigEnv: opts.ConfigEnv, Executable: opts.Executable, Definitions: opts.AgentDefinitions, PluginDirs: opts.PluginDirs})
	r.Register(CronCreateTool{ConfigHome: opts.ConfigHome})
	r.Register(CronDeleteTool{ConfigHome: opts.ConfigHome})
	r.Register(CronListTool{ConfigHome: opts.ConfigHome})
	r.Register(PolicyEvaluateTool{})
	r.Register(ApprovalTokenTool{ConfigHome: opts.ConfigHome})
	r.Register(TeamCreateTool{Workspace: workspace, ConfigHome: opts.ConfigHome, ConfigEnv: opts.ConfigEnv, Executable: opts.Executable})
	r.Register(TeamListTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TeamGetTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TeamDeleteTool{ConfigHome: opts.ConfigHome})
	r.Register(WorkerCreateTool{Workspace: workspace, ConfigHome: opts.ConfigHome, TrustedRoots: opts.TrustedRoots})
	r.Register(WorkerListTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerGetTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerObserveTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerResolveTrustTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerAwaitReadyTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerSendPromptTool{Workspace: workspace, ConfigHome: opts.ConfigHome, ConfigEnv: opts.ConfigEnv, Executable: opts.Executable})
	r.Register(WorkerRestartTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerTerminateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerObserveCompletionTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(WorkerStartupTimeoutTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(RecoveryRecipeTool{ConfigHome: opts.ConfigHome})
	r.Register(RecoveryAttemptTool{ConfigHome: opts.ConfigHome})
	r.Register(RecoveryStatusTool{ConfigHome: opts.ConfigHome})
	r.Register(RoadmapPinpointTool{ConfigHome: opts.ConfigHome})
	r.Register(ReportBackpressureTool{ConfigHome: opts.ConfigHome})
	r.Register(ReportSchemaTool{})
	r.Register(NudgeTool{ConfigHome: opts.ConfigHome})
	r.Register(ProvisionalStatusTool{ConfigHome: opts.ConfigHome})
	r.Register(TaskCreateTool{Workspace: workspace, ConfigHome: opts.ConfigHome, ConfigEnv: opts.ConfigEnv, Executable: opts.Executable})
	r.Register(RunTaskPacketTool{Workspace: workspace, ConfigHome: opts.ConfigHome, ConfigEnv: opts.ConfigEnv, Executable: opts.Executable})
	r.Register(TaskListTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskStatusTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskGetTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskUpdateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskHeartbeatTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskLaneBoardTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskStopTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskOutputTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TaskSuperviseTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(TodoReadTool{Workspace: workspace})
	r.Register(TodoWriteTool{Workspace: workspace})
	r.Register(BriefTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(SendUserMessageTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(StructuredOutputTool{})
	r.Register(SleepTool{})
	r.Register(REPLTool{Workspace: workspace, ConfigEnv: opts.ConfigEnv})
	r.Register(SkillTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(ConfigTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	r.Register(MCPDispatchTool{Servers: opts.MCPServers, Options: r.MCPClientOptions, Clients: r.mcpClients})
	r.Register(MCPAuthTool{Servers: opts.MCPServers, ConfigHome: opts.ConfigHome, OAuthProfile: opts.OAuthProfile})
	r.Register(ListMCPResourcesTool{Servers: opts.MCPServers, Options: r.MCPClientOptions, Clients: r.mcpClients})
	r.Register(ReadMCPResourceTool{Servers: opts.MCPServers, Options: r.MCPClientOptions, Clients: r.mcpClients})
	r.Register(ListMCPResourceTemplatesTool{Servers: opts.MCPServers, Options: r.MCPClientOptions, Clients: r.mcpClients})
	r.Register(ListMCPPromptsTool{Servers: opts.MCPServers, Options: r.MCPClientOptions, Clients: r.mcpClients})
	r.Register(GetMCPPromptTool{Servers: opts.MCPServers, Options: r.MCPClientOptions, Clients: r.mcpClients})
	r.Register(GitStatusTool{Workspace: workspace})
	r.Register(BranchFreshnessTool{Workspace: workspace})
	r.Register(GitDiffTool{Workspace: workspace})
	r.Register(GitLogTool{Workspace: workspace})
	r.Register(GitShowTool{Workspace: workspace})
	r.Register(GitBlameTool{Workspace: workspace})
	r.Register(AskUserQuestionTool{In: opts.QuestionIn, Out: opts.QuestionOut})
	r.Register(ToolSearchTool{Registry: r})
}

func cloneMCPServers(servers map[string]config.MCPServerConfig) map[string]config.MCPServerConfig {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]config.MCPServerConfig, len(servers))
	for name, server := range servers {
		out[name] = server
	}
	return out
}

// SetMCPClientOptions updates roots and interactive handlers used by MCP tool
// calls. It is safe to call between or during interactive turns.
func (r *Registry) SetMCPClientOptions(options mcp.ClientOptions) {
	r.mcpOptionsMu.Lock()
	defer r.mcpOptionsMu.Unlock()
	r.mcpOptions = options
}

// MCPClientOptions returns a snapshot of the current MCP interaction options.
func (r *Registry) MCPClientOptions() mcp.ClientOptions {
	r.mcpOptionsMu.RLock()
	defer r.mcpOptionsMu.RUnlock()
	options := r.mcpOptions
	options.Roots = append([]mcp.Root(nil), options.Roots...)
	return options
}

// ListMCPTools discovers tools through the registry's persistent MCP pool.
func (r *Registry) ListMCPTools(ctx context.Context, serverName string, server config.MCPServerConfig) mcp.ToolListResult {
	return r.mcpClients.ListTools(ctx, serverName, server, r.MCPClientOptions())
}

// RegisterMCPTool installs a dynamically discovered MCP tool adapter.
func (r *Registry) RegisterMCPTool(serverName string, server config.MCPServerConfig, remote mcp.ToolInfo) {
	r.Register(MCPTool{
		Name:        NewMCPToolName(serverName, remote.Name),
		Description: remote.Description,
		Schema:      remote.InputSchema,
		Required:    PermissionWorkspace,
		ServerName:  serverName,
		Server:      server,
		RemoteName:  remote.Name,
		Options:     r.MCPClientOptions,
		Clients:     r.mcpClients,
	})
}

func configuredRAGBaseURL(explicit string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("RAG_BASE_URL"))
}

// Has reports whether a tool name or supported alias resolves to a registered
// tool.
func (r *Registry) Has(name string) bool {
	_, _, ok := r.resolve(name)
	return ok
}

// Definitions returns sorted model-facing definitions for every registered
// tool.
func (r *Registry) Definitions() []anthropic.ToolDefinition {
	defs := make([]anthropic.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// DefinitionsForModel returns the eager tool surface plus deferred tools that
// tool_search loaded during the current model turn.
func (r *Registry) DefinitionsForModel(loaded []string) []anthropic.ToolDefinition {
	return r.modelDefinitions(false, loaded)
}

// DefinitionsForPlanModeWithLoaded applies model deferral and plan-mode access
// rules to the advertised tool surface.
func (r *Registry) DefinitionsForPlanModeWithLoaded(loaded []string) []anthropic.ToolDefinition {
	return r.modelDefinitions(true, loaded)
}

func (r *Registry) modelDefinitions(planMode bool, loaded []string) []anthropic.ToolDefinition {
	loadedSet := make(map[string]struct{}, len(loaded))
	for _, name := range loaded {
		loadedSet[CanonicalToolName(name)] = struct{}{}
	}
	defs := make([]anthropic.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		def := tool.Definition()
		name := CanonicalToolName(def.Name)
		if !defaultModelToolAvailable(name) {
			continue
		}
		if _, eager := eagerModelTools[name]; !eager {
			if _, ok := loadedSet[name]; !ok {
				continue
			}
		}
		if planMode && !ToolVisibleInPlanMode(name, tool.Permission()) {
			continue
		}
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// DeferredInfos returns searchable model tools whose schemas are not present
// in the initial request.
func (r *Registry) DeferredInfos() []ToolInfo {
	infos := make([]ToolInfo, 0, len(r.tools))
	for _, tool := range r.tools {
		def := tool.Definition()
		name := CanonicalToolName(def.Name)
		if !defaultModelToolAvailable(name) {
			continue
		}
		if _, eager := eagerModelTools[name]; eager {
			continue
		}
		infos = append(infos, toolInfo(tool))
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

func defaultModelToolAvailable(name string) bool {
	_, explicit := explicitModelTools[CanonicalToolName(name)]
	return !explicit
}

// DefinitionsForPlanMode returns the subset of tool definitions visible while
// the model is planning rather than executing workspace changes.
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

// ToolVisibleInPlanMode reports whether a tool should be advertised while plan
// mode is active.
func ToolVisibleInPlanMode(name string, permission Permission) bool {
	if permission == PermissionReadOnly {
		return true
	}
	return CanonicalToolName(name) == "bash"
}

// ToolAllowedInPlanMode reports whether a tool may execute while plan mode is
// active.
func ToolAllowedInPlanMode(name string, permission Permission) bool {
	return ToolVisibleInPlanMode(name, permission)
}

// ReadOnlyPrompter derives a prompter that only allows read-only tools.
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

func effectiveDefaultShell(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "powershell":
		return "powershell"
	default:
		return "bash"
	}
}

func (r *Registry) Infos() []ToolInfo {
	infos := make([]ToolInfo, 0, len(r.tools))
	for _, tool := range r.tools {
		infos = append(infos, toolInfo(tool))
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

func (r *Registry) Info(name string) (ToolInfo, bool) {
	_, tool, ok := r.resolve(name)
	if !ok {
		return ToolInfo{}, false
	}
	return toolInfo(tool), true
}

func (r *Registry) modelInfo(name string) (ToolInfo, bool) {
	info, ok := r.Info(name)
	if !ok || !defaultModelToolAvailable(info.Name) {
		return ToolInfo{}, false
	}
	return info, true
}

func toolInfo(tool Tool) ToolInfo {
	def := tool.Definition()
	return ToolInfo{
		Name:        def.Name,
		Description: def.Description,
		Permission:  tool.Permission(),
		InputSchema: def.InputSchema,
	}
}

func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage, prompter *Prompter) (string, error) {
	canonical, _, ok := r.resolve(name)
	if !ok {
		return "", r.unknownToolError(name)
	}
	if strings.EqualFold(canonical, "permission_check") {
		return r.executePermissionCheck(input, prompter)
	}
	execution, err := r.AuthorizeExecution(canonical, input, prompter)
	if err != nil {
		return "", err
	}
	return execution.Execute(ctx, input)
}

func (r *Registry) unknownToolError(name string) error {
	suggestions := toolnames.Suggestions(name, r.toolNameSuggestionCandidates(), 4)
	switch len(suggestions) {
	case 0:
		return fmt.Errorf("unknown tool %q", name)
	case 1:
		return fmt.Errorf("unknown tool %q; did you mean %q?", name, suggestions[0])
	default:
		return fmt.Errorf("unknown tool %q; suggestions: %s", name, strings.Join(suggestions, ", "))
	}
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
	_, err := p.AuthorizeDecision(name, required, input)
	return err
}

// AuthorizeDecision authorizes one invocation and returns the resolved
// decision so callers can preserve user feedback in the model-visible result.
func (p *Prompter) AuthorizeDecision(name string, required Permission, input json.RawMessage) (PermissionDecision, error) {
	decision := p.Decide(name, required, input)
	if decision.Allowed {
		p.emitDecision(decision)
		return decision, nil
	}
	if !decision.WouldPrompt {
		p.emitDecision(decision)
		return decision, permissionDecisionError(decision)
	}
	if p.In == nil {
		p.In = os.Stdin
	}
	if p.Err == nil {
		p.Err = os.Stderr
	}
	if decision.Message != "" {
		fmt.Fprintf(p.Err, "\nTool validation warning: %s\n", decision.Message)
	}
	p.emitRequest(decision)
	fmt.Fprintf(p.Err, "\nTool %s requires %s permission.\nInput: %s\nAllow? [y/N/a=always for session] ", name, required, string(input))
	reader := bufio.NewReader(p.In)
	answer, _ := reader.ReadString('\n')
	response := parsePermissionResponse(answer)
	feedback := strings.TrimSpace(response.Feedback)
	if response.Decision == "allow_once" {
		resolved := PermissionDecision{ToolName: name, Required: required, Mode: decision.Mode, Input: decision.Input, Allowed: true, Reason: "user_approved", Feedback: feedback}
		p.emitDecision(resolved)
		return resolved, nil
	}
	if response.Decision == "allow_always" {
		rule := sessionAllowRule(name, decision.Input, response.Rule)
		if !permissionRulesContain(p.AllowRules, rule) {
			p.AllowRules = append(p.AllowRules, rule)
		}
		resolved := PermissionDecision{ToolName: name, Required: required, Mode: decision.Mode, Input: decision.Input, Allowed: true, Reason: "user_approved_always", Feedback: feedback, Rule: rule}
		p.emitDecision(resolved)
		return resolved, nil
	}
	resolved := PermissionDecision{ToolName: name, Required: required, Mode: decision.Mode, Input: decision.Input, Allowed: false, Reason: "user_denied", Feedback: feedback}
	p.emitDecision(resolved)
	return resolved, permissionDecisionError(resolved)
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
	validation := p.validateInvocation(name, mode, input, inputText)
	if validation.terminal {
		decision.Allowed = validation.allowed
		decision.Reason = validation.reason
		decision.Message = validation.message
		return decision
	}
	validationWarning := validation.warning
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

type invocationValidation struct {
	terminal bool
	allowed  bool
	reason   string
	message  string
	warning  string
}

func (p *Prompter) validateInvocation(name string, mode Permission, input json.RawMessage, inputText string) invocationValidation {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash":
		if effectiveDefaultShell(p.DefaultShell) == "powershell" {
			return p.validatePowerShellInvocation(name, mode, input, inputText)
		}
		return p.validateBashInvocation(name, mode, bashvalidation.CommandFromInput(input), inputText, "bash_validation", true)
	case "powershell":
		return p.validatePowerShellInvocation(name, mode, input, inputText)
	case "task_create":
		return p.validateBashInvocation(name, mode, bashvalidation.CommandFromInput(input), inputText, "task_create_validation", false)
	case "repl":
		language, code := replPayloadCommand(input)
		if isShellREPLLanguage(language) {
			return p.validateBashInvocation(name, mode, code, inputText, "repl_validation", true)
		}
	}
	return invocationValidation{}
}

func (p *Prompter) validatePowerShellInvocation(name string, mode Permission, input json.RawMessage, inputText string) invocationValidation {
	result := powershellvalidation.Validate(powershellvalidation.CommandFromInput(input), string(mode), p.Workspace, p.AdditionalDirs)
	switch result.Severity {
	case powershellvalidation.SeverityBlock:
		return invocationValidation{terminal: true, reason: "powershell_validation", message: result.Reason}
	case powershellvalidation.SeverityConfirm:
		return invocationValidation{warning: result.Reason}
	case powershellvalidation.SeverityAllow:
		if mode == PermissionReadOnly && result.Intent == powershellvalidation.IntentReadOnly && !ruleMatches(p.AskRules, name, inputText) {
			return invocationValidation{terminal: true, allowed: true, reason: "powershell_validation_read_only"}
		}
	}
	return invocationValidation{}
}

func (p *Prompter) validateBashInvocation(name string, mode Permission, command string, inputText string, reason string, allowReadOnly bool) invocationValidation {
	if command == "" {
		return invocationValidation{}
	}
	result := bashvalidation.ValidateWithAdditionalDirs(command, string(mode), p.Workspace, p.AdditionalDirs)
	switch result.Severity {
	case bashvalidation.SeverityBlock:
		return invocationValidation{terminal: true, reason: reason, message: result.Reason}
	case bashvalidation.SeverityConfirm:
		return invocationValidation{warning: result.Reason}
	case bashvalidation.SeverityAllow:
		if allowReadOnly && mode == PermissionReadOnly && result.Intent == bashvalidation.IntentReadOnly && !ruleMatches(p.AskRules, name, inputText) {
			return invocationValidation{terminal: true, allowed: true, reason: reason + "_read_only"}
		}
	}
	return invocationValidation{}
}

func parsePermissionResponse(answer string) PermissionResponse {
	answer = strings.TrimSpace(answer)
	response := PermissionResponse{}
	if strings.HasPrefix(answer, "{") && json.Unmarshal([]byte(answer), &response) == nil {
		response.Decision = normalizePermissionResponseDecision(response.Decision)
		response.Feedback = strings.TrimSpace(response.Feedback)
		response.Rule = strings.TrimSpace(response.Rule)
		return response
	}
	response.Decision = normalizePermissionResponseDecision(answer)
	return response
}

func normalizePermissionResponseDecision(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "y", "yes", "allow", "allow_once":
		return "allow_once"
	case "a", "always", "allow_always":
		return "allow_always"
	default:
		return "deny"
	}
}

// SuggestedPermissionRule returns a session-scoped allow rule narrowed to the
// current invocation when its input exposes a stable command, path, or query.
func SuggestedPermissionRule(name string, input string) string {
	name = strings.TrimSpace(CanonicalToolName(name))
	if name == "" {
		name = "tool"
	}
	needle := permissionRuleInputNeedle(name, input)
	if needle == "" {
		return name
	}
	return fmt.Sprintf("%s(%s)", name, needle)
}

func permissionRuleInputNeedle(name string, input string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(input)), &payload) != nil {
		return truncatePermissionRuleNeedle(input)
	}
	value := func(keys ...string) string {
		for _, key := range keys {
			if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		return ""
	}
	var needle string
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "powershell":
		needle = shellPermissionPrefix(value("command", "code"))
	case "read_file", "write_file", "edit_file", "notebook_edit":
		needle = value("path", "file_path", "notebook_path")
	case "multi_edit":
		needle = value("path", "file_path")
		if needle == "" {
			if edits, ok := payload["edits"].([]any); ok && len(edits) > 0 {
				if edit, editOK := edits[0].(map[string]any); editOK {
					if path, pathOK := edit["path"].(string); pathOK {
						needle = path
					} else if path, pathOK := edit["file_path"].(string); pathOK {
						needle = path
					}
				}
			}
		}
	case "grep", "glob":
		needle = value("pattern", "query")
	case "web_fetch":
		needle = value("url")
	case "web_search":
		needle = value("query")
	default:
		needle = value("path", "file_path", "command", "query", "url", "name", "id")
	}
	return truncatePermissionRuleNeedle(needle)
}

func shellPermissionPrefix(command string) string {
	fields := strings.Fields(command)
	if len(fields) > 2 {
		fields = fields[:2]
	}
	for index, field := range fields {
		if strings.ContainsAny(field, "|;&<>") {
			fields = fields[:index]
			break
		}
	}
	return strings.Join(fields, " ")
}

func truncatePermissionRuleNeedle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 240 {
		value = string(runes[:240])
	}
	return strings.TrimSpace(value)
}

func sessionAllowRule(name string, input string, requested string) string {
	fallback := SuggestedPermissionRule(name, input)
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return fallback
	}
	if open := strings.Index(requested, "("); open > 0 && strings.HasSuffix(requested, ")") {
		tool, _ := parsePermissionRule(requested)
		if tool == "*" || !permissionToolMatches(tool, name) {
			return fallback
		}
		return requested
	}
	return fmt.Sprintf("%s(%s)", strings.TrimSpace(CanonicalToolName(name)), truncatePermissionRuleNeedle(requested))
}

func permissionRulesContain(rules []string, target string) bool {
	for _, rule := range rules {
		if strings.EqualFold(strings.TrimSpace(rule), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func permissionDecisionError(decision PermissionDecision) error {
	var err error
	switch decision.Reason {
	case "denied_tools":
		err = fmt.Errorf("permission denied for tool %s by denied_tools", decision.ToolName)
	case "deny_rule":
		err = fmt.Errorf("permission denied for tool %s by deny rule", decision.ToolName)
	case "bash_validation", "powershell_validation", "task_create_validation", "repl_validation":
		if decision.Message != "" {
			err = fmt.Errorf("permission denied for tool %s by tool validation: %s", decision.ToolName, decision.Message)
			break
		}
		err = fmt.Errorf("permission denied for tool %s by tool validation", decision.ToolName)
	default:
		err = fmt.Errorf("permission denied for tool %s", decision.ToolName)
	}
	if feedback := strings.TrimSpace(decision.Feedback); feedback != "" {
		return fmt.Errorf("%w; user feedback: %s", err, feedback)
	}
	return err
}

func appendPermissionFeedback(output string, feedback string) string {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return output
	}
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &payload) == nil && payload != nil {
		payload["permission_feedback"] = feedback
		if encoded, err := json.Marshal(payload); err == nil {
			return string(encoded)
		}
	}
	if strings.TrimSpace(output) == "" {
		encoded, _ := json.Marshal(map[string]string{"permission_feedback": feedback})
		return string(encoded)
	}
	return strings.TrimSpace(output) + "\n\nPermission feedback: " + feedback
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
		return PermissionDanger
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
	cmd.Env = toolEnvironmentFromConfig(t.ConfigEnv, nil)
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

// NewMCPToolName returns the canonical registry name for a tool exposed by an
// MCP server.
func NewMCPToolName(serverName, toolName string) string {
	return mcp.ToolName(serverName, toolName)
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
	var result mcp.ToolCallResult
	if t.Clients != nil {
		result = t.Clients.CallTool(ctx, t.ServerName, t.Server, t.RemoteName, input, currentMCPOptions(t.Options))
	} else {
		result = mcp.CallToolWithOptions(ctx, t.ServerName, t.Server, t.RemoteName, input, currentMCPOptions(t.Options))
	}
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	if len(result.Result) == 0 {
		return "{}", nil
	}
	return string(result.Result), nil
}

// MCPDispatchTool calls a named tool on one configured MCP server.
type MCPDispatchTool struct {
	Servers map[string]config.MCPServerConfig
	Options func() mcp.ClientOptions
	Clients *mcp.ClientPool
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
		return "", unknownMCPServerError(payload.Server, t.Servers)
	}
	if len(payload.Arguments) == 0 {
		payload.Arguments = json.RawMessage(`{}`)
	}
	var result mcp.ToolCallResult
	if t.Clients != nil {
		result = t.Clients.CallTool(ctx, payload.Server, server, payload.Tool, payload.Arguments, currentMCPOptions(t.Options))
	} else {
		result = mcp.CallToolWithOptions(ctx, payload.Server, server, payload.Tool, payload.Arguments, currentMCPOptions(t.Options))
	}
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	if len(result.Result) == 0 {
		return "{}", nil
	}
	return string(result.Result), nil
}

// MCPAuthTool reports, refreshes, or clears authentication readiness for a
// configured MCP server.
type MCPAuthTool struct {
	Servers      map[string]config.MCPServerConfig
	ConfigHome   string
	OAuthProfile string
}

type mcpAuthInput struct {
	Server string `json:"server"`
	Action string `json:"action,omitempty"`
}

var mcpAuthActionNames = []string{
	"status",
	"refresh", "login", "log-in", "signin", "sign-in", "authenticate", "auth", "reauth", "reauthenticate",
	"clear", "logout", "signout", "sign-out", "disconnect", "revoke", "reset",
}

func (MCPAuthTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "mcp_auth",
		Description: "Inspect or refresh authentication and readiness status for a configured MCP server.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Configured MCP server name.",
				},
				"action": map[string]any{
					"type":        "string",
					"enum":        append([]string(nil), mcpAuthActionNames...),
					"description": "status inspects readiness; refresh/login/signin/authenticate refresh a saved OAuth token when possible; clear/logout/signout/disconnect/revoke revoke when possible and delete the saved token.",
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
	action := normalizeMCPAuthAction(payload.Action)
	if action != "status" && action != "refresh" && action != "clear" && action != "logout" {
		return "", unknownMCPAuthActionError(payload.Action)
	}
	now := time.Now().UTC()
	server, ok := t.Servers[payload.Server]
	if !ok {
		report := mcpauthdiag.Build(mcp.AuthStatusResult{
			Server: payload.Server,
			Status: "unknown",
			Error:  "server is not configured",
		}, t.ConfigHome, t.OAuthProfile, now)
		if action == "refresh" {
			report = mcpauthdiag.Refresh(ctx, report.AuthStatusResult, t.ConfigHome, t.OAuthProfile, now)
		} else if action == "clear" || action == "logout" {
			report = mcpauthdiag.Clear(ctx, report.AuthStatusResult, t.ConfigHome, t.OAuthProfile, now)
		}
		return pretty(report), nil
	}
	result := mcp.InspectAuth(ctx, payload.Server, server)
	if action == "refresh" {
		return pretty(mcpauthdiag.Refresh(ctx, result, t.ConfigHome, t.OAuthProfile, now)), nil
	}
	if action == "clear" || action == "logout" {
		return pretty(mcpauthdiag.Clear(ctx, result, t.ConfigHome, t.OAuthProfile, now)), nil
	}
	return pretty(mcpauthdiag.Build(result, t.ConfigHome, t.OAuthProfile, now)), nil
}

func unknownMCPAuthActionError(action string) error {
	suggestions := toolnames.Suggestions(action, mcpAuthActionNames, 4)
	switch len(suggestions) {
	case 0:
		return fmt.Errorf("unsupported mcp_auth action %q", action)
	case 1:
		return fmt.Errorf("unsupported mcp_auth action %q; did you mean %q?", action, suggestions[0])
	default:
		return fmt.Errorf("unsupported mcp_auth action %q; suggestions: %s", action, strings.Join(suggestions, ", "))
	}
}

func normalizeMCPAuthAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "":
		return "status"
	case "login", "log-in", "signin", "sign-in", "authenticate", "auth", "reauth", "reauthenticate":
		return "refresh"
	case "signout", "sign-out", "disconnect", "revoke", "reset":
		return "clear"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

// ListMCPResourcesTool lists resources exposed by configured MCP servers.
type ListMCPResourcesTool struct {
	Servers map[string]config.MCPServerConfig
	Options func() mcp.ClientOptions
	Clients *mcp.ClientPool
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
			return "", unknownMCPServerError(payload.Server, t.Servers)
		}
		result := t.listResources(ctx, payload.Server, server)
		if result.Error != "" {
			return "", errors.New(result.Error)
		}
		return pretty(result), nil
	}

	names := sortedMCPServerNames(t.Servers)
	results := make([]mcp.ResourceListResult, 0, len(names))
	for _, name := range names {
		results = append(results, t.listResources(ctx, name, t.Servers[name]))
	}
	return pretty(map[string]any{
		"kind":    "mcp_resources",
		"servers": results,
		"total":   len(results),
	}), nil
}

func (t ListMCPResourcesTool) listResources(ctx context.Context, name string, server config.MCPServerConfig) mcp.ResourceListResult {
	if t.Clients != nil {
		return t.Clients.ListResources(ctx, name, server, currentMCPOptions(t.Options))
	}
	return mcp.ListResourcesWithOptions(ctx, name, server, currentMCPOptions(t.Options))
}

// ReadMCPResourceTool reads one resource URI from a configured MCP server.
type ReadMCPResourceTool struct {
	Servers map[string]config.MCPServerConfig
	Options func() mcp.ClientOptions
	Clients *mcp.ClientPool
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
		return "", unknownMCPServerError(payload.Server, t.Servers)
	}
	var result mcp.ResourceReadResult
	if t.Clients != nil {
		result = t.Clients.ReadResource(ctx, payload.Server, server, payload.URI, currentMCPOptions(t.Options))
	} else {
		result = mcp.ReadResourceWithOptions(ctx, payload.Server, server, payload.URI, currentMCPOptions(t.Options))
	}
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	return pretty(result), nil
}

// ListMCPResourceTemplatesTool lists resource templates exposed by configured
// MCP servers.
type ListMCPResourceTemplatesTool struct {
	Servers map[string]config.MCPServerConfig
	Options func() mcp.ClientOptions
	Clients *mcp.ClientPool
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
			return "", unknownMCPServerError(payload.Server, t.Servers)
		}
		result := t.listResourceTemplates(ctx, payload.Server, server)
		if result.Error != "" {
			return "", errors.New(result.Error)
		}
		return pretty(result), nil
	}
	names := sortedMCPServerNames(t.Servers)
	results := make([]mcp.ResourceTemplateListResult, 0, len(names))
	for _, name := range names {
		results = append(results, t.listResourceTemplates(ctx, name, t.Servers[name]))
	}
	return pretty(map[string]any{"kind": "mcp_resource_templates", "servers": results, "total": len(results)}), nil
}

func (t ListMCPResourceTemplatesTool) listResourceTemplates(ctx context.Context, name string, server config.MCPServerConfig) mcp.ResourceTemplateListResult {
	if t.Clients != nil {
		return t.Clients.ListResourceTemplates(ctx, name, server, currentMCPOptions(t.Options))
	}
	return mcp.ListResourceTemplatesWithOptions(ctx, name, server, currentMCPOptions(t.Options))
}

// ListMCPPromptsTool lists prompts exposed by configured MCP servers.
type ListMCPPromptsTool struct {
	Servers map[string]config.MCPServerConfig
	Options func() mcp.ClientOptions
	Clients *mcp.ClientPool
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
			return "", unknownMCPServerError(payload.Server, t.Servers)
		}
		result := t.listPrompts(ctx, payload.Server, server)
		if result.Error != "" {
			return "", errors.New(result.Error)
		}
		return pretty(result), nil
	}
	names := sortedMCPServerNames(t.Servers)
	results := make([]mcp.PromptListResult, 0, len(names))
	for _, name := range names {
		results = append(results, t.listPrompts(ctx, name, t.Servers[name]))
	}
	return pretty(map[string]any{"kind": "mcp_prompts", "servers": results, "total": len(results)}), nil
}

func (t ListMCPPromptsTool) listPrompts(ctx context.Context, name string, server config.MCPServerConfig) mcp.PromptListResult {
	if t.Clients != nil {
		return t.Clients.ListPrompts(ctx, name, server, currentMCPOptions(t.Options))
	}
	return mcp.ListPromptsWithOptions(ctx, name, server, currentMCPOptions(t.Options))
}
