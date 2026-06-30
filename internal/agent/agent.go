package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/bughunt"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/commandrun"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/contextview"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/fileinventory"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/githubcomments"
	"github.com/Rememorio/codog/internal/githubsetup"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/insights"
	"github.com/Rememorio/codog/internal/manifests"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/mcpserver"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/projectinit"
	"github.com/Rememorio/codog/internal/prompthistory"
	"github.com/Rememorio/codog/internal/promptrefs"
	"github.com/Rememorio/codog/internal/prworkflow"
	"github.com/Rememorio/codog/internal/releasenotes"
	localreview "github.com/Rememorio/codog/internal/review"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/securityreview"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/sessionsummary"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/slash"
	localstatus "github.com/Rememorio/codog/internal/status"
	prompttemplates "github.com/Rememorio/codog/internal/templates"
	"github.com/Rememorio/codog/internal/terminalsetup"
	"github.com/Rememorio/codog/internal/thinkback"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/usage"
	"github.com/Rememorio/codog/internal/verifiers"
	"github.com/Rememorio/codog/internal/versioninfo"
	"github.com/Rememorio/codog/internal/workerstate"
	"github.com/Rememorio/codog/internal/workspaceops"
	"github.com/Rememorio/codog/internal/worktree"
	"github.com/chzyer/readline"
)

const version = "0.1.0"

type App struct {
	Config    config.Config
	Client    *anthropic.Client
	Tools     *tools.Registry
	Sessions  *session.Store
	Workspace string
	Out       io.Writer
	Err       io.Writer
	In        io.Reader

	mcpToolsLoaded bool
}

func RunCLI(ctx context.Context, args []string, baseOverrides config.FlagOverrides) error {
	originalArgs := append([]string(nil), args...)
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h":
			return renderHelpCommand(os.Stdout, args[1:])
		case "--version", "-v":
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			return renderVersion(os.Stdout, workspace, args[1:])
		case "--acp", "-acp":
			if !acpServeRequested(args[1:]) {
				return renderACPStatus(os.Stdout, args[1:])
			}
			args = append([]string{"acp"}, args[1:]...)
		}
	}
	if handled, err := renderGlobalResumeHelp(os.Stdout, args); handled {
		return err
	}
	if acpArgs, ok, err := parseACPGlobalInvocation(args); ok || err != nil {
		if err != nil {
			return err
		}
		if acpServeRequested(acpArgs) {
			args = append([]string{"acp"}, acpArgs...)
		} else {
			return renderACPStatus(os.Stdout, acpArgs)
		}
	}
	overrides, command, rest, err := parseFlags(args, baseOverrides)
	if err != nil {
		return renderCLIError(os.Stdout, err, requestedOutputFormat(originalArgs))
	}
	if command == "help" || command == "--help" || command == "-h" {
		return renderHelpCommand(os.Stdout, rest)
	}
	if command == "version" || command == "--version" || command == "-v" {
		workspace, err := os.Getwd()
		if err != nil {
			return err
		}
		return renderVersion(os.Stdout, workspace, rest)
	}
	if command == "acp" && !acpServeRequested(rest) {
		return renderACPStatus(os.Stdout, rest)
	}
	if command == "config" {
		cfg, paths, err := config.LoadForInspection(overrides)
		if err != nil {
			return renderCLIError(os.Stdout, err, requestedOutputFormat(originalArgs))
		}
		cfg = redactedConfig(cfg)
		return renderConfigInspection(os.Stdout, cfg, paths, rest)
	}
	if command == "providers" {
		cfg, paths, err := config.LoadForInspection(overrides)
		if err != nil {
			return renderCLIError(os.Stdout, err, requestedOutputFormat(originalArgs))
		}
		applyStoredOAuthToken(&cfg, time.Now().UTC())
		return renderProvidersCommand(os.Stdout, cfg, paths, rest)
	}
	if handled, err := renderCommandHelpRequest(os.Stdout, command, rest, requestedOutputFormat(originalArgs)); handled {
		return err
	}
	if handled, err := renderLocalRouteGuard(os.Stdout, command, rest, requestedOutputFormat(originalArgs)); handled {
		return err
	}
	if command == "mock-server" {
		addr := ":8089"
		if len(rest) > 0 {
			addr = rest[0]
		}
		fmt.Fprintf(os.Stderr, "mock Anthropic-compatible server listening on %s\n", addr)
		return http.ListenAndServe(addr, mockanthropic.Server{Text: "mock response from codog"}.Handler())
	}
	if command == "self-test" {
		report, err := harness.Run(ctx)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	}
	if command == "init" {
		workspace, err := os.Getwd()
		if err != nil {
			return err
		}
		return initProject(os.Stdout, workspace, rest, func(report projectinit.Report) error {
			cfg, _, err := config.LoadForInspection(overrides)
			if err != nil {
				return nil
			}
			return runSetupHookPayload(ctx, hooks.Runner{Config: cfg.Hooks, Workspace: workspace, ConfigHome: cfg.ConfigHome}, workspace, "init", report.Status)
		})
	}
	if command == "state" {
		workspace, err := os.Getwd()
		if err != nil {
			return err
		}
		return renderWorkerState(os.Stdout, workspace, rest)
	}
	if command == "memory" {
		workspace, err := os.Getwd()
		if err != nil {
			return err
		}
		return renderMemoryCommand(os.Stdout, workspace, rest)
	}
	if command == "enterprise" && len(rest) > 0 && rest[0] == "verify" {
		return enterpriseVerify(os.Stdout, rest)
	}

	cfg, err := config.Load(overrides)
	if err != nil {
		return renderCLIError(os.Stdout, err, requestedOutputFormat(originalArgs))
	}
	applyStoredOAuthToken(&cfg, time.Now().UTC())
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	additionalDirs, err := pathscope.EffectiveDirs(workspace, cfg.AdditionalDirs)
	if err != nil {
		return err
	}
	app := &App{
		Config:    cfg,
		Client:    anthropic.NewWithRateLimit(cfg.BaseURL, cfg.APIKey, cfg.AuthToken, anthropicRateLimitOptions(cfg.RateLimit)),
		Tools:     tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{SandboxStrategy: cfg.Future.SandboxStrategy, AdditionalDirs: additionalDirs, ConfigHome: cfg.ConfigHome, MCPServers: cfg.MCPServers, QuestionIn: os.Stdin, QuestionOut: os.Stderr}),
		Sessions:  session.NewWorkspaceStore(cfg.ConfigHome, workspace),
		Workspace: workspace,
		Out:       os.Stdout,
		Err:       os.Stderr,
		In:        os.Stdin,
	}
	if err := app.RegisterPluginTools(); err != nil {
		return err
	}
	if err := app.validateGlobalToolRules(overrides, requestedOutputFormat(originalArgs)); err != nil {
		return err
	}
	if strings.HasPrefix(command, "/") {
		mappedCommand, mappedRest, err := normalizeDirectSlashInvocation(os.Stdout, command, rest, requestedOutputFormat(originalArgs))
		if err != nil {
			return err
		}
		command, rest = mappedCommand, mappedRest
	}

	switch command {
	case "help":
		return renderHelpCommand(app.Out, rest)
	case "version":
		return renderVersion(app.Out, app.Workspace, rest)
	case "", "repl":
		return app.REPL(ctx, overrides)
	case "tui":
		result, err := tui.PromptWithCandidates(app.slashCompletionCandidates(""))
		if err != nil {
			return err
		}
		if !result.Submitted || result.Prompt == "" {
			return nil
		}
		return app.Prompt(ctx, result.Prompt, overrides)
	case "prompt":
		req, err := parsePromptArgs(rest)
		if err != nil {
			return renderCLIError(app.Out, err, requestedOutputFormat(originalArgs))
		}
		input := req.Prompt
		if !req.PromptProvided {
			data, err := readPromptInput(app.In)
			if err != nil {
				return err
			}
			input = string(data)
		}
		if strings.TrimSpace(input) == "" {
			return renderMissingPrompt(app.Out, req.Format)
		}
		return app.PromptWithOutput(ctx, input, overrides, req.Format)
	case "acp":
		return app.ACP(ctx, rest)
	case "btw":
		return app.BTW(ctx, rest, overrides, nil)
	case "sessions":
		return app.SessionsCommand(rest)
	case "backfill-sessions":
		return app.BackfillSessions(rest)
	case "rename":
		return app.Rename(rest, overrides)
	case "history", "prompt-history":
		return app.History(rest, overrides)
	case "summary":
		return app.Summary(rest, overrides)
	case "rewind":
		return app.Rewind(rest, overrides)
	case "todos":
		return app.Todos(rest)
	case "focus":
		return app.Focus(rest)
	case "unfocus":
		return app.Unfocus(rest)
	case "add-dir":
		return app.AddDir(rest)
	case "output-style":
		return app.OutputStyle(rest)
	case "model":
		return app.Model(rest)
	case "advisor":
		return app.Advisor(rest)
	case "max-tokens":
		return app.MaxTokens(rest)
	case "max-turns":
		return app.MaxTurns(rest)
	case "permissions":
		return app.Permissions(rest)
	case "allowed-tools":
		return app.AllowedTools(rest)
	case "theme":
		return app.Theme(rest)
	case "color":
		return app.Theme(rest)
	case "vim":
		return app.Vim(rest)
	case "effort":
		return app.Effort(rest)
	case "fast":
		return app.Fast(rest)
	case "voice":
		return app.Voice(rest)
	case "chrome":
		return app.Chrome(rest)
	case "privacy-settings":
		return app.PrivacySettings(rest)
	case "keybindings":
		return app.Keybindings(rest)
	case "skills":
		return app.Skills(rest)
	case "commands":
		return app.Commands(rest)
	case "templates":
		return app.Templates(rest)
	case "hooks":
		return app.Hooks(ctx, rest)
	case "mcp":
		return app.MCP(ctx, rest)
	case "capabilities":
		return app.Capabilities(rest)
	case "cost":
		return app.ShowCost(overrides)
	case "usage":
		return app.Usage(rest, overrides)
	case "stats":
		return app.Usage(rest, overrides)
	case "insights":
		return app.Insights(rest)
	case "think-back", "thinkback", "thinkback-play":
		return app.ThinkBack(rest)
	case "compact":
		return app.Compact(rest, overrides)
	case "undo":
		return app.Undo(rest)
	case "extra-usage":
		return app.ExtraUsage(rest)
	case "rate-limit-options":
		return app.RateLimitOptions(rest)
	case "reset-limits":
		return app.ResetLimits(rest)
	case "plan":
		return app.Plan(rest)
	case "ultraplan":
		return app.Plan(rest)
	case "exit-plan":
		return app.Plan(append([]string{"exit"}, rest...))
	case "export":
		return app.Export(rest)
	case "share":
		return app.Share(rest, overrides)
	case "copy":
		return app.Copy(ctx, rest, overrides)
	case "git":
		return app.Git(rest)
	case "diff", "log", "blame", "commit":
		return app.Git(append([]string{command}, rest...))
	case "branch":
		return app.Branch(rest)
	case "tag":
		return app.Tag(rest)
	case "stash":
		return app.Stash(rest)
	case "changelog":
		return app.Changelog(rest)
	case "release-notes":
		return app.ReleaseNotes(rest)
	case "review", "ultrareview":
		return app.Review(rest)
	case "feedback":
		return app.Feedback(rest, overrides)
	case "pr":
		return app.PullRequestDraft(rest, overrides)
	case "commit-push-pr":
		return app.CommitPushPR(ctx, rest)
	case "pr-comments", "pr_comments":
		return app.PRComments(ctx, rest)
	case "install-github-app":
		return app.InstallGitHubApp(rest)
	case "install-slack-app":
		return app.InstallSlackApp(rest)
	case "stickers":
		return app.Stickers(rest)
	case "passes":
		return app.Passes(rest)
	case "issue":
		return app.IssueDraft(rest, overrides)
	case "run":
		return app.RunCommand(ctx, rest)
	case "node", "python":
		return app.LanguageCommand(ctx, command, rest)
	case "test":
		return app.ProjectCommand(ctx, "test", rest)
	case "build":
		return app.ProjectCommand(ctx, "build", rest)
	case "lint":
		return app.ProjectCommand(ctx, "lint", rest)
	case "background":
		return app.BackgroundWithOverrides(rest, overrides)
	case "tasks", "bashes":
		return app.BackgroundWithOverrides(rest, overrides)
	case "agents":
		return app.AgentsWithOverrides(rest, overrides)
	case "reload-plugins":
		return app.ReloadPlugins(rest)
	case "plugin", "plugins", "marketplace":
		return app.Marketplace(rest)
	case "login":
		return app.Login(rest)
	case "logout":
		return app.Logout(rest)
	case "oauth":
		return app.OAuth(rest)
	case "oauth-refresh":
		return app.OAuthRefresh(rest)
	case "providers":
		return app.Providers(rest)
	case "brief":
		return app.Brief(rest)
	case "status":
		return app.Status(rest, overrides)
	case "statusline":
		return app.Statusline(rest, overrides)
	case "terminal-setup", "terminalSetup":
		return app.TerminalSetup(rest)
	case "context":
		return app.Context(rest, overrides)
	case "ctx_viz":
		return app.ContextViz(rest, overrides)
	case "files":
		return app.Files(rest)
	case "search":
		return app.Search(ctx, rest)
	case "security-review":
		return app.SecurityReview(rest)
	case "bughunter":
		return app.Bughunter(rest)
	case "init":
		return app.Init(rest)
	case "init-verifiers":
		return app.InitVerifiers(rest)
	case "state":
		return app.State(rest)
	case "memory":
		return app.Memory(rest)
	case "project":
		return app.Project(rest)
	case "env":
		return app.Env(rest)
	case "doctor":
		return app.Doctor(rest)
	case "sandbox":
		return app.Sandbox()
	case "sandbox-toggle":
		return app.SandboxToggle(rest)
	case "heapdump":
		return app.HeapDump(rest)
	case "symbols":
		return app.Symbols(rest)
	case "diagnostics":
		return app.Diagnostics(ctx, rest)
	case "map":
		return app.Map(rest)
	case "references":
		return app.References(rest)
	case "definition":
		return app.Definition(rest)
	case "hover":
		return app.Hover(rest)
	case "teleport":
		return app.Teleport(rest)
	case "completion":
		return app.Completion(rest)
	case "format":
		return app.Format(rest)
	case "code-intel":
		return app.CodeIntel(rest)
	case "remote":
		return app.Remote(rest)
	case "remote-env":
		return app.RemoteEnv(rest)
	case "remote-setup", "web-setup":
		return app.RemoteSetup(rest, overrides)
	case "bridge", "remote-control":
		return app.Bridge(rest)
	case "bridge-kick":
		return app.BridgeKick(rest)
	case "desktop", "app":
		return app.Desktop(rest, overrides)
	case "mobile":
		return app.Mobile(rest, overrides)
	case "ios", "android":
		return app.Mobile(append([]string{command}, rest...), overrides)
	case "ide":
		return app.IDE(rest)
	case "debug-tool-call":
		return app.DebugToolCall(ctx, rest, overrides)
	case "updater":
		return app.Updater(ctx, rest)
	case "upgrade":
		return app.Upgrade(ctx, rest)
	case "install":
		return app.Install(ctx, rest)
	case "enterprise":
		return app.Enterprise(rest)
	case "dump-manifests":
		return app.DumpManifests(rest)
	case "system-prompt":
		return app.SystemPromptCommand(rest)
	default:
		if command != "" {
			return renderCommandNotFound(os.Stdout, command, rest, requestedOutputFormat(originalArgs))
		}
		return app.REPL(ctx, overrides)
	}
}

type ExitError struct {
	Code   int
	Err    error
	Silent bool
}

func (e *ExitError) Error() string {
	if e == nil || e.Err == nil {
		return "exit"
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func applyStoredOAuthToken(cfg *config.Config, now time.Time) {
	if cfg.AuthToken != "" {
		return
	}
	token, err := oauth.LoadToken(cfg.ConfigHome)
	if err != nil {
		return
	}
	if token.Expired(now) {
		if token.RefreshToken == "" {
			return
		}
		refreshed, err := oauth.RefreshStoredToken(context.Background(), cfg.ConfigHome, "")
		if err != nil || refreshed.Expired(now) {
			return
		}
		token = refreshed
	}
	cfg.AuthToken = token.AccessToken
}

func anthropicRateLimitOptions(cfg config.RateLimitConfig) anthropic.RateLimitOptions {
	cfg = config.NormalizeRateLimitConfig(cfg)
	return anthropic.RateLimitOptions{
		MaxRetries:     cfg.MaxRetries,
		InitialBackoff: time.Duration(cfg.InitialBackoffMS) * time.Millisecond,
		MaxBackoff:     time.Duration(cfg.MaxBackoffMS) * time.Millisecond,
	}
}

func (a *App) Remote(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: codog remote serve [addr]")
	}
	addr := "127.0.0.1:8791"
	if len(args) > 1 {
		addr = args[1]
	}
	executable, _ := os.Executable()
	fmt.Fprintf(a.Err, "codog remote control listening on http://%s\n", addr)
	return http.ListenAndServe(addr, control.Server{
		Sessions:    a.Sessions,
		ConfigHome:  a.Config.ConfigHome,
		Workspace:   a.Workspace,
		AuthToken:   a.Config.Future.RemoteAuthToken,
		LeaseTTL:    time.Duration(a.Config.Future.RemoteLeaseSeconds) * time.Second,
		Executable:  executable,
		EditorToken: a.Config.Future.EditorBridgeToken,
	}.Handler())
}

type remoteEnvRequest struct {
	Action       string
	Format       string
	Target       string
	Path         string
	SetEnabled   bool
	Enabled      bool
	AuthToken    string
	ClearToken   bool
	SetLease     bool
	LeaseSeconds int
}

type remoteEnvReport struct {
	Kind                string `json:"kind"`
	Action              string `json:"action"`
	Status              string `json:"status"`
	Enabled             bool   `json:"enabled"`
	AuthTokenConfigured bool   `json:"auth_token_configured"`
	LeaseSeconds        int    `json:"lease_seconds"`
	Path                string `json:"path,omitempty"`
}

type remoteSetupRequest struct {
	Action       string
	Format       string
	Addr         string
	Target       string
	Path         string
	AuthToken    string
	ClearToken   bool
	SetLease     bool
	LeaseSeconds int
	SessionID    string
}

type remoteSetupReport struct {
	Kind                string   `json:"kind"`
	Action              string   `json:"action"`
	Status              string   `json:"status"`
	Workspace           string   `json:"workspace,omitempty"`
	SessionID           string   `json:"session_id,omitempty"`
	Enabled             bool     `json:"enabled"`
	Ready               bool     `json:"ready"`
	AuthTokenConfigured bool     `json:"auth_token_configured"`
	LeaseSeconds        int      `json:"lease_seconds"`
	RemoteCommand       string   `json:"remote_command"`
	RemoteAddr          string   `json:"remote_addr"`
	RemoteURL           string   `json:"remote_url"`
	HealthURL           string   `json:"health_url"`
	StateURL            string   `json:"state_url"`
	Path                string   `json:"path,omitempty"`
	Messages            []string `json:"messages,omitempty"`
}

func (a *App) RemoteEnv(args []string) error {
	req, err := parseRemoteEnvArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "show":
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if !req.SetEnabled && req.AuthToken == "" && !req.ClearToken && !req.SetLease {
			return errors.New("remote-env set requires --enabled, --auth-token, --clear-auth-token, or --lease-seconds")
		}
		if req.SetEnabled {
			if _, err := config.SetFileValue(path, "future.remote_enabled", req.Enabled); err != nil {
				return err
			}
			a.Config.Future.RemoteEnabled = req.Enabled
		}
		if req.AuthToken != "" {
			if _, err := config.SetFileValue(path, "future.remote_auth_token", req.AuthToken); err != nil {
				return err
			}
			a.Config.Future.RemoteAuthToken = req.AuthToken
		}
		if req.ClearToken {
			if _, err := config.UnsetFileValue(path, "future.remote_auth_token"); err != nil {
				return err
			}
			a.Config.Future.RemoteAuthToken = ""
		}
		if req.SetLease {
			if _, err := config.SetFileValue(path, "future.remote_lease_seconds", req.LeaseSeconds); err != nil {
				return err
			}
			a.Config.Future.RemoteLeaseSeconds = req.LeaseSeconds
		}
		req.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		for _, key := range []string{"future.remote_enabled", "future.remote_auth_token", "future.remote_lease_seconds"} {
			if _, err := config.UnsetFileValue(path, key); err != nil {
				return err
			}
		}
		a.Config.Future.RemoteEnabled = false
		a.Config.Future.RemoteAuthToken = ""
		a.Config.Future.RemoteLeaseSeconds = 0
		req.Path = path
	default:
		return fmt.Errorf("unknown remote-env command %q", req.Action)
	}
	report := remoteEnvReport{
		Kind:                "remote_env",
		Action:              req.Action,
		Status:              "ok",
		Enabled:             a.Config.Future.RemoteEnabled,
		AuthTokenConfigured: strings.TrimSpace(a.Config.Future.RemoteAuthToken) != "",
		LeaseSeconds:        a.Config.Future.RemoteLeaseSeconds,
		Path:                req.Path,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderRemoteEnvReport(a.Out, report)
	return nil
}

func (a *App) RemoteSetup(args []string, overrides config.FlagOverrides) error {
	req, err := parseRemoteSetupArgs(args, overrides)
	if err != nil {
		return err
	}
	sessionID, err := resolveHandoffSessionID(a.Sessions, req.SessionID)
	if err != nil {
		return err
	}
	addr, remoteURL, err := normalizeRemoteHandoffAddr(req.Addr)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "enable":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "future.remote_enabled", true); err != nil {
			return err
		}
		a.Config.Future.RemoteEnabled = true
		if req.AuthToken != "" {
			if _, err := config.SetFileValue(path, "future.remote_auth_token", req.AuthToken); err != nil {
				return err
			}
			a.Config.Future.RemoteAuthToken = req.AuthToken
		}
		if req.ClearToken {
			if _, err := config.UnsetFileValue(path, "future.remote_auth_token"); err != nil {
				return err
			}
			a.Config.Future.RemoteAuthToken = ""
		}
		if req.SetLease {
			if _, err := config.SetFileValue(path, "future.remote_lease_seconds", req.LeaseSeconds); err != nil {
				return err
			}
			a.Config.Future.RemoteLeaseSeconds = req.LeaseSeconds
		}
		req.Path = path
	case "disable":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "future.remote_enabled", false); err != nil {
			return err
		}
		a.Config.Future.RemoteEnabled = false
		if req.ClearToken {
			if _, err := config.UnsetFileValue(path, "future.remote_auth_token"); err != nil {
				return err
			}
			a.Config.Future.RemoteAuthToken = ""
		}
		req.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		for _, key := range []string{"future.remote_enabled", "future.remote_auth_token", "future.remote_lease_seconds"} {
			if _, err := config.UnsetFileValue(path, key); err != nil {
				return err
			}
		}
		a.Config.Future.RemoteEnabled = false
		a.Config.Future.RemoteAuthToken = ""
		a.Config.Future.RemoteLeaseSeconds = 0
		req.Path = path
	default:
		return fmt.Errorf("unknown remote-setup command %q", req.Action)
	}
	report := a.buildRemoteSetupReport(req, sessionID, addr, remoteURL)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderRemoteSetupReport(a.Out, report)
	return nil
}

func parseRemoteEnvArgs(args []string) (remoteEnvRequest, error) {
	req := remoteEnvRequest{Action: "show", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("remote-env output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("remote-env target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("remote-env path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--enabled":
			index++
			if index >= len(args) {
				return req, errors.New("remote-env enabled value is required")
			}
			enabled, err := parseOnOffBool(args[index])
			if err != nil {
				return req, err
			}
			req.SetEnabled = true
			req.Enabled = enabled
		case strings.HasPrefix(arg, "--enabled="):
			enabled, err := parseOnOffBool(strings.TrimPrefix(arg, "--enabled="))
			if err != nil {
				return req, err
			}
			req.SetEnabled = true
			req.Enabled = enabled
		case arg == "--auth-token":
			index++
			if index >= len(args) {
				return req, errors.New("remote-env auth token is required")
			}
			req.AuthToken = args[index]
		case strings.HasPrefix(arg, "--auth-token="):
			req.AuthToken = strings.TrimPrefix(arg, "--auth-token=")
		case arg == "--clear-auth-token":
			req.ClearToken = true
		case arg == "--lease-seconds":
			index++
			if index >= len(args) {
				return req, errors.New("remote-env lease seconds is required")
			}
			seconds, err := strconv.Atoi(args[index])
			if err != nil || seconds < 0 {
				return req, errors.New("remote-env lease seconds must be a non-negative integer")
			}
			req.SetLease = true
			req.LeaseSeconds = seconds
		case strings.HasPrefix(arg, "--lease-seconds="):
			seconds, err := strconv.Atoi(strings.TrimPrefix(arg, "--lease-seconds="))
			if err != nil || seconds < 0 {
				return req, errors.New("remote-env lease seconds must be a non-negative integer")
			}
			req.SetLease = true
			req.LeaseSeconds = seconds
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "remote-env"); err != nil {
		return req, err
	}
	if len(rest) > 1 {
		return req, fmt.Errorf("unexpected remote-env argument %q", rest[1])
	}
	if len(rest) == 1 {
		switch strings.ToLower(rest[0]) {
		case "show", "status":
			req.Action = "show"
		case "set":
			req.Action = "set"
		case "clear", "reset", "unset":
			req.Action = "clear"
		default:
			return req, fmt.Errorf("unknown remote-env command %q", rest[0])
		}
	}
	return req, nil
}

func parseRemoteSetupArgs(args []string, overrides config.FlagOverrides) (remoteSetupRequest, error) {
	req := remoteSetupRequest{
		Action:    "status",
		Format:    "text",
		Addr:      "127.0.0.1:8791",
		Target:    "user",
		SessionID: firstNonEmpty(overrides.Resume, overrides.SessionID),
	}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("remote-setup output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--addr":
			index++
			if index >= len(args) {
				return req, errors.New("remote-setup addr is required")
			}
			req.Addr = args[index]
		case strings.HasPrefix(arg, "--addr="):
			req.Addr = strings.TrimPrefix(arg, "--addr=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("remote-setup target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("remote-setup path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--auth-token":
			index++
			if index >= len(args) {
				return req, errors.New("remote-setup auth token is required")
			}
			req.AuthToken = args[index]
		case strings.HasPrefix(arg, "--auth-token="):
			req.AuthToken = strings.TrimPrefix(arg, "--auth-token=")
		case arg == "--clear-auth-token":
			req.ClearToken = true
		case arg == "--lease-seconds":
			index++
			if index >= len(args) {
				return req, errors.New("remote-setup lease seconds is required")
			}
			seconds, err := strconv.Atoi(args[index])
			if err != nil || seconds < 0 {
				return req, errors.New("remote-setup lease seconds must be a non-negative integer")
			}
			req.SetLease = true
			req.LeaseSeconds = seconds
		case strings.HasPrefix(arg, "--lease-seconds="):
			seconds, err := strconv.Atoi(strings.TrimPrefix(arg, "--lease-seconds="))
			if err != nil || seconds < 0 {
				return req, errors.New("remote-setup lease seconds must be a non-negative integer")
			}
			req.SetLease = true
			req.LeaseSeconds = seconds
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("remote-setup session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("remote-setup resume session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--enable":
			req.Action = "enable"
			actionSet = true
		case arg == "--disable":
			req.Action = "disable"
			actionSet = true
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown remote-setup flag %q", arg)
		default:
			if actionSet {
				return req, fmt.Errorf("unexpected remote-setup argument %q", arg)
			}
			switch strings.ToLower(arg) {
			case "status", "show", "check":
				req.Action = "status"
			case "enable", "on", "setup":
				req.Action = "enable"
			case "disable", "off":
				req.Action = "disable"
			case "clear", "reset", "unset":
				req.Action = "clear"
			default:
				return req, fmt.Errorf("unknown remote-setup command %q", arg)
			}
			actionSet = true
		}
	}
	if err := validateTextOrJSON(req.Format, "remote-setup"); err != nil {
		return req, err
	}
	if req.AuthToken != "" && req.ClearToken {
		return req, errors.New("remote-setup cannot set and clear auth token in the same command")
	}
	if req.Action == "status" && (req.AuthToken != "" || req.ClearToken || req.SetLease) {
		req.Action = "enable"
	}
	if req.Action == "disable" && (req.AuthToken != "" || req.SetLease) {
		return req, errors.New("remote-setup disable only accepts --clear-auth-token as a write option")
	}
	return req, nil
}

func (a *App) buildRemoteSetupReport(req remoteSetupRequest, sessionID, addr, remoteURL string) remoteSetupReport {
	enabled := a.Config.Future.RemoteEnabled
	authConfigured := strings.TrimSpace(a.Config.Future.RemoteAuthToken) != ""
	status := "disabled"
	switch {
	case enabled && authConfigured:
		status = "ready"
	case enabled:
		status = "enabled_without_auth"
	}
	report := remoteSetupReport{
		Kind:                "remote_setup",
		Action:              req.Action,
		Status:              status,
		Workspace:           a.Workspace,
		SessionID:           sessionID,
		Enabled:             enabled,
		Ready:               enabled,
		AuthTokenConfigured: authConfigured,
		LeaseSeconds:        a.Config.Future.RemoteLeaseSeconds,
		RemoteCommand:       "codog remote serve " + addr,
		RemoteAddr:          addr,
		RemoteURL:           remoteURL,
		HealthURL:           strings.TrimRight(remoteURL, "/") + "/health",
		StateURL:            strings.TrimRight(remoteURL, "/") + "/state",
		Path:                req.Path,
	}
	switch {
	case !enabled:
		report.Messages = append(report.Messages, "Enable remote control with `codog remote-setup enable` before connecting a remote client.")
	case !authConfigured:
		report.Messages = append(report.Messages, "No auth token is configured; keep the listener on localhost or set `--auth-token` before exposing it.")
	default:
		report.Messages = append(report.Messages, "Start the remote command shown above, then connect a desktop, mobile, or remote client to the URL.")
	}
	if sessionID != "" {
		report.Messages = append(report.Messages, "Use the reported session id when the client should attach to the current conversation.")
	}
	return report
}

func renderRemoteEnvReport(out io.Writer, report remoteEnvReport) {
	fmt.Fprintln(out, "Remote Environment")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Auth token       %t\n", report.AuthTokenConfigured)
	fmt.Fprintf(out, "  Lease seconds    %d\n", report.LeaseSeconds)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func renderRemoteSetupReport(out io.Writer, report remoteSetupReport) {
	fmt.Fprintln(out, "Remote Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Ready            %t\n", report.Ready)
	fmt.Fprintf(out, "  Auth token       %t\n", report.AuthTokenConfigured)
	fmt.Fprintf(out, "  Lease seconds    %d\n", report.LeaseSeconds)
	fmt.Fprintf(out, "  Remote command   %s\n", report.RemoteCommand)
	fmt.Fprintf(out, "  Remote URL       %s\n", report.RemoteURL)
	fmt.Fprintf(out, "  Health URL       %s\n", report.HealthURL)
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Note             %s\n", message)
	}
}

func parseOnOffBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes", "enabled", "enable":
		return true, nil
	case "0", "false", "off", "no", "disabled", "disable":
		return false, nil
	default:
		return false, fmt.Errorf("unknown boolean value %q", value)
	}
}

func (a *App) Bridge(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: codog bridge serve")
	}
	executable, _ := os.Executable()
	return bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
		Executable: executable,
	}.Serve(a.In, a.Out)
}

type ideRequest struct {
	Action string
	Format string
}

type ideReport struct {
	Kind      string             `json:"kind"`
	Action    string             `json:"action"`
	Workspace string             `json:"workspace"`
	Bridge    ideBridgeReport    `json:"bridge"`
	StatePath string             `json:"state_path,omitempty"`
	State     bridge.EditorState `json:"state"`
	Cleared   bool               `json:"cleared,omitempty"`
}

type ideBridgeReport struct {
	Command         string `json:"command"`
	Socket          string `json:"socket,omitempty"`
	TokenConfigured bool   `json:"token_configured"`
}

func (a *App) IDE(args []string) error {
	req, err := parseIDEArgs(args)
	if err != nil {
		return err
	}
	server := bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}
	if req.Action == "clear" {
		if err := server.ClearEditorState(); err != nil {
			return err
		}
	}
	state, err := server.EditorState()
	if err != nil {
		return err
	}
	statePath, err := server.EditorStatePath()
	if err != nil {
		return err
	}
	report := ideReport{
		Kind:      "ide",
		Action:    req.Action,
		Workspace: a.Workspace,
		Bridge: ideBridgeReport{
			Command:         "codog bridge serve",
			Socket:          a.Config.Future.EditorBridgeSocket,
			TokenConfigured: a.Config.Future.EditorBridgeToken != "",
		},
		StatePath: statePath,
		State:     state,
		Cleared:   req.Action == "clear",
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderIDEReport(a.Out, report)
	return nil
}

type bridgeKickRequest struct {
	Action string
	Format string
	Args   []string
}

type bridgeKickReport struct {
	Kind     string              `json:"kind"`
	Action   string              `json:"action"`
	Status   string              `json:"status"`
	Message  string              `json:"message,omitempty"`
	Bridge   ideBridgeReport     `json:"bridge"`
	State    bridge.EditorState  `json:"state"`
	Faults   []bridge.FaultEvent `json:"faults,omitempty"`
	Recorded *bridge.FaultEvent  `json:"recorded,omitempty"`
	Cleared  bool                `json:"cleared,omitempty"`
}

func (a *App) BridgeKick(args []string) error {
	req, err := parseBridgeKickArgs(args)
	if err != nil {
		return err
	}
	server := bridge.Server{
		ConfigHome: a.Config.ConfigHome,
		Workspace:  a.Workspace,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}
	report := bridgeKickReport{
		Kind:   "bridge_kick",
		Action: req.Action,
		Status: "ok",
		Bridge: ideBridgeReport{
			Command:         "codog bridge serve",
			Socket:          a.Config.Future.EditorBridgeSocket,
			TokenConfigured: a.Config.Future.EditorBridgeToken != "",
		},
	}
	switch req.Action {
	case "status":
		report.State, _ = server.EditorState()
		report.Faults, _ = server.BridgeFaults()
		report.Message = "Local bridge diagnostics are available."
	case "clear":
		if err := server.ClearEditorState(); err != nil {
			return err
		}
		if err := server.ClearBridgeFaults(); err != nil {
			return err
		}
		report.State, _ = server.EditorState()
		report.Cleared = true
		report.Message = "Cleared local trusted editor bridge state and bridge fault events."
	default:
		event, err := server.RecordBridgeFault(req.Action, req.Args)
		if err != nil {
			return err
		}
		report.Recorded = &event
		report.Faults, _ = server.BridgeFaults()
		report.State, _ = server.EditorState()
		report.Message = fmt.Sprintf("Recorded local bridge fault event for bridge-kick %s.", strings.Join(append([]string{req.Action}, req.Args...), " "))
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBridgeKickReport(a.Out, report)
	return nil
}

func parseIDEArgs(args []string) (ideRequest, error) {
	req := ideRequest{Action: "status", Format: "text"}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("ide output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown ide flag %q", arg)
		default:
			if actionSet {
				return req, fmt.Errorf("unexpected ide argument %q", arg)
			}
			action := strings.ToLower(arg)
			switch action {
			case "status", "state":
				req.Action = "status"
			case "clear", "reset", "disconnect":
				req.Action = "clear"
			default:
				return req, fmt.Errorf("unknown ide action %q", arg)
			}
			actionSet = true
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return req, fmt.Errorf("unknown ide output format %q", req.Format)
	}
}

func parseBridgeKickArgs(args []string) (bridgeKickRequest, error) {
	req := bridgeKickRequest{Action: "status", Format: "text"}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("bridge-kick output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown bridge-kick flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "bridge-kick"); err != nil {
		return req, err
	}
	if len(positionals) != 0 {
		req.Action = strings.ToLower(positionals[0])
		req.Args = positionals[1:]
	}
	return req, nil
}

func renderIDEReport(out io.Writer, report ideReport) {
	fmt.Fprintln(out, "IDE Bridge")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Bridge command   %s\n", report.Bridge.Command)
	fmt.Fprintf(out, "  Socket           %s\n", emptyAsNone(report.Bridge.Socket))
	fmt.Fprintf(out, "  Token configured %t\n", report.Bridge.TokenConfigured)
	if report.Cleared {
		fmt.Fprintln(out, "  State cleared    true")
	}
	if report.State.Identity == nil {
		fmt.Fprintln(out, "  Trusted editor   none")
	} else {
		identity := report.State.Identity.Editor
		if report.State.Identity.Version != "" {
			identity += " " + report.State.Identity.Version
		}
		fmt.Fprintf(out, "  Trusted editor   %s\n", identity)
		fmt.Fprintf(out, "  Trusted          %t\n", report.State.Identity.Trusted)
	}
	if report.State.OpenFile == nil {
		fmt.Fprintln(out, "  Open file        none")
	} else {
		fmt.Fprintf(out, "  Open file        %s\n", report.State.OpenFile.Path)
	}
	if report.State.Selection == nil {
		fmt.Fprintln(out, "  Selection        none")
	} else {
		selection := report.State.Selection
		fmt.Fprintf(out, "  Selection        %s:%d", selection.Path, selection.StartLine)
		if selection.EndLine > 0 && selection.EndLine != selection.StartLine {
			fmt.Fprintf(out, "-%d", selection.EndLine)
		}
		fmt.Fprintln(out)
	}
}

func renderBridgeKickReport(out io.Writer, report bridgeKickReport) {
	fmt.Fprintln(out, "Bridge Kick")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Bridge command   %s\n", report.Bridge.Command)
	fmt.Fprintf(out, "  Socket           %s\n", emptyAsNone(report.Bridge.Socket))
	fmt.Fprintf(out, "  Token configured %t\n", report.Bridge.TokenConfigured)
	fmt.Fprintf(out, "  Fault events     %d\n", len(report.Faults))
	if report.Cleared {
		fmt.Fprintln(out, "  Cleared          true")
	}
	if report.Recorded != nil {
		fmt.Fprintf(out, "  Recorded         %s %s\n", report.Recorded.Action, strings.Join(report.Recorded.Args, " "))
		fmt.Fprintf(out, "  Fault message    %s\n", report.Recorded.Message)
	} else if len(report.Faults) > 0 {
		last := report.Faults[len(report.Faults)-1]
		fmt.Fprintf(out, "  Last fault       %s %s\n", last.Action, strings.Join(last.Args, " "))
		fmt.Fprintf(out, "  Fault message    %s\n", last.Message)
	}
	if report.State.Identity == nil {
		fmt.Fprintln(out, "  Trusted editor   none")
	} else {
		fmt.Fprintf(out, "  Trusted editor   %s\n", report.State.Identity.Editor)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type desktopHandoffRequest struct {
	Action    string
	Format    string
	SessionID string
}

type desktopHandoffReport struct {
	Kind      string             `json:"kind"`
	Action    string             `json:"action"`
	Surface   string             `json:"surface"`
	Workspace string             `json:"workspace"`
	SessionID string             `json:"session_id,omitempty"`
	Supported bool               `json:"supported"`
	Platform  string             `json:"platform"`
	Bridge    ideBridgeReport    `json:"bridge"`
	StatePath string             `json:"state_path,omitempty"`
	State     bridge.EditorState `json:"state"`
	Messages  []string           `json:"messages,omitempty"`
}

type mobileHandoffRequest struct {
	Platform  string
	Format    string
	Addr      string
	SessionID string
}

type mobileHandoffReport struct {
	Kind                string   `json:"kind"`
	Action              string   `json:"action"`
	Surface             string   `json:"surface"`
	Workspace           string   `json:"workspace"`
	SessionID           string   `json:"session_id,omitempty"`
	Platform            string   `json:"platform"`
	RemoteCommand       string   `json:"remote_command"`
	RemoteAddr          string   `json:"remote_addr"`
	RemoteURL           string   `json:"remote_url"`
	RemoteEnabled       bool     `json:"remote_enabled"`
	AuthTokenConfigured bool     `json:"auth_token_configured"`
	LeaseSeconds        int      `json:"lease_seconds"`
	Messages            []string `json:"messages,omitempty"`
}

func (a *App) Desktop(args []string, overrides config.FlagOverrides) error {
	req, err := parseDesktopHandoffArgs(args, overrides)
	if err != nil {
		return err
	}
	sessionID, err := resolveHandoffSessionID(a.Sessions, req.SessionID)
	if err != nil {
		return err
	}
	server := bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}
	state, err := server.EditorState()
	if err != nil {
		return err
	}
	statePath, err := server.EditorStatePath()
	if err != nil {
		return err
	}
	report := desktopHandoffReport{
		Kind:      "desktop_handoff",
		Action:    req.Action,
		Surface:   "desktop",
		Workspace: a.Workspace,
		SessionID: sessionID,
		Supported: desktopHandoffSupported(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		Bridge: ideBridgeReport{
			Command:         "codog bridge serve",
			Socket:          a.Config.Future.EditorBridgeSocket,
			TokenConfigured: a.Config.Future.EditorBridgeToken != "",
		},
		StatePath: statePath,
		State:     state,
		Messages: []string{
			"Start the bridge command, then connect a trusted desktop or editor client to the stdio bridge.",
			"Use `codog ide status` to inspect the currently trusted client.",
		},
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDesktopHandoffReport(a.Out, report)
	return nil
}

func parseDesktopHandoffArgs(args []string, overrides config.FlagOverrides) (desktopHandoffRequest, error) {
	req := desktopHandoffRequest{Action: "handoff", Format: "text"}
	req.SessionID = firstNonEmpty(overrides.Resume, overrides.SessionID)
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("desktop output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("desktop session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("desktop resume session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown desktop flag %q", arg)
		default:
			if actionSet {
				return req, fmt.Errorf("unexpected desktop argument %q", arg)
			}
			switch strings.ToLower(arg) {
			case "handoff", "show", "status":
				req.Action = "handoff"
			default:
				return req, fmt.Errorf("unknown desktop action %q", arg)
			}
			actionSet = true
		}
	}
	if err := validateTextOrJSON(req.Format, "desktop"); err != nil {
		return req, err
	}
	return req, nil
}

func renderDesktopHandoffReport(out io.Writer, report desktopHandoffReport) {
	fmt.Fprintln(out, "Desktop Handoff")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Platform         %s\n", report.Platform)
	fmt.Fprintf(out, "  Supported        %t\n", report.Supported)
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	fmt.Fprintf(out, "  Bridge command   %s\n", report.Bridge.Command)
	fmt.Fprintf(out, "  Socket           %s\n", emptyAsNone(report.Bridge.Socket))
	fmt.Fprintf(out, "  Token configured %t\n", report.Bridge.TokenConfigured)
	if report.State.Identity == nil {
		fmt.Fprintln(out, "  Trusted client   none")
	} else {
		identity := report.State.Identity.Editor
		if report.State.Identity.Version != "" {
			identity += " " + report.State.Identity.Version
		}
		fmt.Fprintf(out, "  Trusted client   %s\n", identity)
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Note             %s\n", message)
	}
}

func (a *App) Mobile(args []string, overrides config.FlagOverrides) error {
	req, err := parseMobileHandoffArgs(args, overrides)
	if err != nil {
		return err
	}
	sessionID, err := resolveHandoffSessionID(a.Sessions, req.SessionID)
	if err != nil {
		return err
	}
	addr, remoteURL, err := normalizeRemoteHandoffAddr(req.Addr)
	if err != nil {
		return err
	}
	report := mobileHandoffReport{
		Kind:                "mobile_handoff",
		Action:              "handoff",
		Surface:             "mobile",
		Workspace:           a.Workspace,
		SessionID:           sessionID,
		Platform:            req.Platform,
		RemoteCommand:       "codog remote serve " + addr,
		RemoteAddr:          addr,
		RemoteURL:           remoteURL,
		RemoteEnabled:       a.Config.Future.RemoteEnabled,
		AuthTokenConfigured: strings.TrimSpace(a.Config.Future.RemoteAuthToken) != "",
		LeaseSeconds:        a.Config.Future.RemoteLeaseSeconds,
		Messages: []string{
			"Start the remote command, then connect a mobile or remote client to the local control API.",
			"Use `codog remote-env set --auth-token TOKEN` when the endpoint is reachable outside localhost.",
		},
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderMobileHandoffReport(a.Out, report)
	return nil
}

func parseMobileHandoffArgs(args []string, overrides config.FlagOverrides) (mobileHandoffRequest, error) {
	req := mobileHandoffRequest{Platform: "all", Format: "text", Addr: "127.0.0.1:8791"}
	req.SessionID = firstNonEmpty(overrides.Resume, overrides.SessionID)
	platformSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("mobile output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--addr":
			index++
			if index >= len(args) {
				return req, errors.New("mobile remote address is required")
			}
			req.Addr = args[index]
		case strings.HasPrefix(arg, "--addr="):
			req.Addr = strings.TrimPrefix(arg, "--addr=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("mobile session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("mobile resume session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown mobile flag %q", arg)
		default:
			if platformSet {
				return req, fmt.Errorf("unexpected mobile argument %q", arg)
			}
			switch strings.ToLower(arg) {
			case "show", "status", "all":
				req.Platform = "all"
			case "ios":
				req.Platform = "ios"
			case "android":
				req.Platform = "android"
			default:
				return req, fmt.Errorf("unknown mobile platform %q", arg)
			}
			platformSet = true
		}
	}
	if err := validateTextOrJSON(req.Format, "mobile"); err != nil {
		return req, err
	}
	return req, nil
}

func renderMobileHandoffReport(out io.Writer, report mobileHandoffReport) {
	fmt.Fprintln(out, "Mobile Handoff")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Platform         %s\n", report.Platform)
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	fmt.Fprintf(out, "  Remote command   %s\n", report.RemoteCommand)
	fmt.Fprintf(out, "  Remote URL       %s\n", report.RemoteURL)
	fmt.Fprintf(out, "  Remote enabled   %t\n", report.RemoteEnabled)
	fmt.Fprintf(out, "  Token configured %t\n", report.AuthTokenConfigured)
	if report.LeaseSeconds > 0 {
		fmt.Fprintf(out, "  Lease seconds    %d\n", report.LeaseSeconds)
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Note             %s\n", message)
	}
}

func desktopHandoffSupported() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return false
	}
}

func resolveHandoffSessionID(store *session.Store, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	if id != "latest" {
		return id, nil
	}
	if store == nil {
		return "", errors.New("session store is unavailable")
	}
	return store.LatestID()
}

func normalizeRemoteHandoffAddr(value string) (string, string, error) {
	addr := strings.TrimSpace(value)
	if addr == "" {
		addr = "127.0.0.1:8791"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		parsed, err := url.Parse(addr)
		if err != nil || parsed.Host == "" {
			return "", "", fmt.Errorf("invalid mobile remote URL %q", value)
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		return parsed.Host, parsed.String(), nil
	}
	if strings.Contains(addr, "://") {
		return "", "", fmt.Errorf("mobile remote address must use http or https: %q", value)
	}
	displayHost := addr
	switch {
	case strings.HasPrefix(addr, ":"):
		displayHost = "127.0.0.1" + addr
	case strings.HasPrefix(addr, "0.0.0.0:"):
		displayHost = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	case strings.HasPrefix(addr, "[::]:"):
		displayHost = "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	return addr, "http://" + displayHost, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type briefRequest struct {
	Message     string
	Status      string
	Attachments []string
	Format      string
}

type briefReport struct {
	Message     string                  `json:"message"`
	Status      string                  `json:"status"`
	Attachments []briefAttachmentReport `json:"attachments"`
	SentAt      string                  `json:"sent_at"`
}

type briefAttachmentReport struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsImage bool   `json:"is_image"`
}

func (a *App) Brief(args []string) error {
	req, err := parseBriefArgs(args)
	if err != nil {
		return err
	}
	input, err := json.Marshal(map[string]any{
		"message":     req.Message,
		"status":      req.Status,
		"attachments": req.Attachments,
	})
	if err != nil {
		return err
	}
	result, err := (tools.BriefTool{
		Workspace:      a.Workspace,
		AdditionalDirs: a.Config.AdditionalDirs,
	}).Execute(context.Background(), input)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		fmt.Fprintln(a.Out, result)
		return nil
	}
	var report briefReport
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		return err
	}
	renderBriefReport(a.Out, report)
	return nil
}

func parseBriefArgs(args []string) (briefRequest, error) {
	req := briefRequest{Status: "normal", Format: "text"}
	var message []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("brief output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--status":
			index++
			if index >= len(args) {
				return req, errors.New("brief status is required")
			}
			req.Status = args[index]
		case strings.HasPrefix(arg, "--status="):
			req.Status = strings.TrimPrefix(arg, "--status=")
		case arg == "--attach" || arg == "--attachment" || arg == "--file":
			index++
			if index >= len(args) {
				return req, errors.New("brief attachment path is required")
			}
			req.Attachments = append(req.Attachments, args[index])
		case strings.HasPrefix(arg, "--attach="):
			req.Attachments = append(req.Attachments, strings.TrimPrefix(arg, "--attach="))
		case strings.HasPrefix(arg, "--attachment="):
			req.Attachments = append(req.Attachments, strings.TrimPrefix(arg, "--attachment="))
		case strings.HasPrefix(arg, "--file="):
			req.Attachments = append(req.Attachments, strings.TrimPrefix(arg, "--file="))
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown brief flag %q", arg)
		default:
			message = append(message, arg)
		}
	}
	req.Message = strings.TrimSpace(strings.Join(message, " "))
	if req.Message == "" {
		return req, errors.New("usage: codog brief MESSAGE [--status normal|proactive] [--attach PATH] [--json]")
	}
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	switch req.Status {
	case "normal", "proactive":
	default:
		return req, fmt.Errorf("unknown brief status %q", req.Status)
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown brief output format %q", req.Format)
	}
	return req, nil
}

func renderBriefReport(out io.Writer, report briefReport) {
	fmt.Fprintln(out, report.Message)
	fmt.Fprintf(out, "status: %s\n", report.Status)
	if len(report.Attachments) > 0 {
		fmt.Fprintln(out, "attachments:")
		for _, attachment := range report.Attachments {
			image := ""
			if attachment.IsImage {
				image = " image"
			}
			fmt.Fprintf(out, "- %s (%d bytes%s)\n", attachment.Path, attachment.Size, image)
		}
	}
}

func (a *App) Updater(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: codog updater check|verify|download|install|rollback")
	}
	var payload any
	switch args[0] {
	case "check":
		if len(args) < 2 {
			return errors.New("usage: codog updater check URL [PUBLIC_KEY]")
		}
		var result updater.CheckResult
		var err error
		if len(args) > 2 {
			result, err = updater.CheckSigned(ctx, version, args[1], args[2])
		} else {
			result, err = updater.Check(ctx, version, args[1])
		}
		if err != nil {
			return err
		}
		payload = result
	case "verify":
		if len(args) < 3 {
			return errors.New("usage: codog updater verify URL PUBLIC_KEY")
		}
		result, err := updater.CheckSigned(ctx, version, args[1], args[2])
		if err != nil {
			return err
		}
		payload = result
	case "download":
		if len(args) < 2 {
			return errors.New("usage: codog updater download URL [PLATFORM] [DEST] [PUBLIC_KEY]")
		}
		platform := ""
		if len(args) > 2 {
			platform = args[2]
		}
		dest := filepath.Join(a.Config.ConfigHome, "updater")
		if len(args) > 3 {
			dest = args[3]
		}
		var result updater.DownloadResult
		var err error
		if len(args) > 4 {
			result, err = updater.DownloadSigned(ctx, args[1], platform, dest, args[4])
		} else {
			result, err = updater.Download(ctx, args[1], platform, dest)
		}
		if err != nil {
			return err
		}
		payload = result
	case "install":
		if len(args) < 2 {
			return errors.New("usage: codog updater install ARTIFACT [TARGET]")
		}
		target := ""
		if len(args) > 2 {
			target = args[2]
		} else {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			target = exe
		}
		result, err := updater.Install(args[1], target)
		if err != nil {
			return err
		}
		payload = result
	case "rollback":
		target := ""
		if len(args) > 1 {
			target = args[1]
		} else {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			target = exe
		}
		result, err := updater.Rollback(target)
		if err != nil {
			return err
		}
		payload = result
	default:
		return fmt.Errorf("unknown updater command %q", args[0])
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Enterprise(args []string) error {
	if len(args) == 0 || (len(args) == 1 && args[0] == "--json") {
		return errors.New("usage: codog enterprise audit [limit] | enterprise verify POLICY PUBLIC_KEY")
	}
	var payload any
	switch args[0] {
	case "audit":
		limit := audit.DefaultLimit
		if len(args) > 1 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			limit = parsed
		}
		events, err := audit.NewStore(a.Config.ConfigHome).List(limit)
		if err != nil {
			return err
		}
		payload = events
	case "verify":
		return enterpriseVerify(a.Out, args)
	default:
		return fmt.Errorf("unknown enterprise command %q", args[0])
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func enterpriseVerify(out io.Writer, args []string) error {
	if len(args) < 3 {
		return errors.New("usage: codog enterprise verify POLICY PUBLIC_KEY")
	}
	policy, err := config.VerifyManagedPolicyFile(args[1], args[2])
	if err != nil {
		return err
	}
	policy.Signature = ""
	payload := map[string]any{
		"path":            args[1],
		"signature_valid": true,
		"policy":          policy,
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(out, string(data))
	return nil
}

type dumpManifestsRequest struct {
	Format       string
	ManifestsDir string
}

func (a *App) DumpManifests(args []string) error {
	req, err := parseDumpManifestsArgs(args)
	if err != nil {
		return err
	}
	workspace := a.Workspace
	registry := a.Tools
	if req.ManifestsDir != "" {
		workspace, err = resolveManifestDiscoveryRoot(req.ManifestsDir)
		if err != nil {
			return err
		}
		registry = tools.NewRegistry(workspace)
	}
	report, err := manifests.Build(workspace, a.Config.ConfigHome, registry)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderManifestDump(a.Out, report)
	return nil
}

func parseDumpManifestsArgs(args []string) (dumpManifestsRequest, error) {
	req := dumpManifestsRequest{Format: "text"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("dump-manifests output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--manifests-dir":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return req, errors.New("missing_flag_value: --manifests-dir requires a path")
			}
			req.ManifestsDir = args[index]
		case strings.HasPrefix(arg, "--manifests-dir="):
			value := strings.TrimPrefix(arg, "--manifests-dir=")
			if strings.TrimSpace(value) == "" {
				return req, errors.New("missing_flag_value: --manifests-dir requires a path")
			}
			req.ManifestsDir = value
		default:
			return req, fmt.Errorf("unknown dump-manifests option %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return req, fmt.Errorf("unknown dump-manifests output format %q", req.Format)
	}
}

func resolveManifestDiscoveryRoot(path string) (string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("missing_manifests: manifest discovery directory does not exist: %s", root)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("missing_manifests: manifest discovery path is not a directory: %s", root)
	}
	return root, nil
}

func renderManifestDump(out io.Writer, report manifests.Report) {
	fmt.Fprintln(out, "Manifest Dump")
	fmt.Fprintf(out, "  Source           %s\n", report.Source)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  Commands         %d\n", report.Commands)
	fmt.Fprintf(out, "  Tools            %d\n", report.Tools)
	fmt.Fprintf(out, "  Agents           %d\n", report.Agents)
	fmt.Fprintf(out, "  Skills           %d\n", report.Skills)
}

func (a *App) SystemPromptCommand(args []string) error {
	format, err := parseSimpleOutputFormat("system-prompt", args)
	if err != nil {
		return err
	}
	prompt := a.systemPrompt()
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{
			"kind":          "system-prompt",
			"status":        "ok",
			"system_prompt": prompt,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, prompt)
	return nil
}

func (a *App) Upgrade(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: codog upgrade [check|verify|download|install|rollback] ARGS...")
	}
	switch args[0] {
	case "check", "verify", "download", "install", "rollback":
		return a.Updater(ctx, args)
	default:
		return a.Updater(ctx, append([]string{"check"}, args...))
	}
}

func (a *App) Install(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: codog install ARTIFACT [TARGET]")
	}
	return a.Updater(ctx, append([]string{"install"}, args...))
}

func (a *App) Background(args []string) error {
	return a.BackgroundWithOverrides(args, config.FlagOverrides{})
}

func (a *App) BackgroundWithOverrides(args []string, overrides config.FlagOverrides) error {
	store := background.NewStore(a.Config.ConfigHome)
	if len(args) == 0 || args[0] == "list" {
		tasks, err := store.List()
		if err != nil {
			return err
		}
		sessionID, err := a.sessionIDFromOverrides(overrides)
		if err != nil {
			return err
		}
		if len(args) > 1 {
			sessionID = args[1]
		}
		tasks = background.FilterBySession(tasks, sessionID)
		data, _ := json.MarshalIndent(tasks, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "run" {
		command, options, err := parseBackgroundRunArgs(args[1:])
		if err != nil {
			return err
		}
		sessionID, err := a.sessionIDFromOverrides(overrides)
		if err != nil {
			return err
		}
		options.SessionID = sessionID
		task, err := store.RunWithOptions(command, a.Workspace, options)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(task, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		a.runTaskCreatedHook(context.Background(), task)
		a.runNotificationHook(context.Background(), "background_task_started", "Background task started", fmt.Sprintf("Background task %s started: %s", task.ID, task.Command))
		return nil
	}
	if len(args) < 2 && args[0] != "prune" && args[0] != "supervise" {
		return errors.New("usage: codog background list [session-id] | run [--restart[=on-failure|always]] [--restart-limit N] [--restart-delay SECONDS] COMMAND | status ID | stop ID | restart ID | logs ID [bytes] | watch ID [offset] | prune [days] [keep] | supervise")
	}
	switch args[0] {
	case "status":
		task, err := store.Status(args[1])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(task, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "stop":
		task, err := store.Stop(args[1])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(task, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		a.runTaskCompletedHook(context.Background(), task, "manual")
		a.runNotificationHook(context.Background(), "background_task_stopped", "Background task stopped", fmt.Sprintf("Background task %s stopped: %s", task.ID, task.Command))
		if task.Kind == "agent" {
			a.runSubagentStopHook(context.Background(), task.ID, subagentTypeForTask(task), task.LogPath, lastBackgroundLogLine(store, task), false)
		}
		return nil
	case "restart":
		task, err := store.Restart(args[1], a.Workspace)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(task, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		a.runTaskCreatedHook(context.Background(), task)
		a.runNotificationHook(context.Background(), "background_task_restarted", "Background task restarted", fmt.Sprintf("Background task %s restarted: %s", task.ID, task.Command))
		return nil
	case "logs":
		limit := int64(64 * 1024)
		if len(args) > 2 {
			parsed, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return err
			}
			limit = parsed
		}
		logs, err := store.Logs(args[1], limit)
		if err != nil {
			return err
		}
		fmt.Fprint(a.Out, logs)
		return nil
	case "watch":
		offset := int64(0)
		if len(args) > 2 {
			parsed, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return err
			}
			offset = parsed
		}
		encoder := json.NewEncoder(a.Out)
		return store.Watch(context.Background(), args[1], background.WatchOptions{Offset: offset}, func(event background.WatchEvent) error {
			return encoder.Encode(event)
		})
	case "prune":
		options, err := parseBackgroundPruneArgs(args[1:])
		if err != nil {
			return err
		}
		result, err := store.Prune(options)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "supervise":
		result, err := store.SuperviseOnce(time.Now().UTC())
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		for _, task := range result.Restarted {
			a.runTaskCreatedHook(context.Background(), task)
			a.runNotificationHook(context.Background(), "background_task_restarted", "Background task restarted", fmt.Sprintf("Background task %s restarted: %s", task.ID, task.Command))
		}
		return nil
	default:
		return fmt.Errorf("unknown background command %q", args[0])
	}
}

func parseBackgroundRunArgs(args []string) (string, background.RunOptions, error) {
	var options background.RunOptions
	var policy *background.RestartPolicy
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			args = args[1:]
			break
		}
		if arg == "--restart" {
			policy = ensureRestartPolicy(policy)
			args = args[1:]
			continue
		}
		if strings.HasPrefix(arg, "--restart=") {
			policy = ensureRestartPolicy(policy)
			mode := strings.TrimPrefix(arg, "--restart=")
			if mode != "on-failure" && mode != "always" {
				return "", options, errors.New("restart mode must be on-failure or always")
			}
			policy.Mode = mode
			args = args[1:]
			continue
		}
		if arg == "--restart-limit" {
			if len(args) < 2 {
				return "", options, errors.New("missing value for --restart-limit")
			}
			limit, err := strconv.Atoi(args[1])
			if err != nil {
				return "", options, err
			}
			if limit < 0 {
				return "", options, errors.New("restart limit must be non-negative")
			}
			policy = ensureRestartPolicy(policy)
			policy.MaxAttempts = limit
			args = args[2:]
			continue
		}
		if arg == "--restart-delay" {
			if len(args) < 2 {
				return "", options, errors.New("missing value for --restart-delay")
			}
			delay, err := strconv.Atoi(args[1])
			if err != nil {
				return "", options, err
			}
			if delay < 0 {
				return "", options, errors.New("restart delay must be non-negative")
			}
			policy = ensureRestartPolicy(policy)
			policy.DelaySeconds = delay
			args = args[2:]
			continue
		}
		break
	}
	command := strings.Join(args, " ")
	if strings.TrimSpace(command) == "" {
		return "", options, errors.New("background command is required")
	}
	options.RestartPolicy = policy
	return command, options, nil
}

func ensureRestartPolicy(policy *background.RestartPolicy) *background.RestartPolicy {
	if policy != nil {
		return policy
	}
	return &background.RestartPolicy{Enabled: true, Mode: "on-failure"}
}

func parseBackgroundPruneArgs(args []string) (background.PruneOptions, error) {
	options := background.DefaultPruneOptions()
	if len(args) > 0 {
		days, err := strconv.Atoi(args[0])
		if err != nil {
			return options, err
		}
		if days < 0 {
			return options, errors.New("prune days must be non-negative")
		}
		options.OlderThan = time.Duration(days) * 24 * time.Hour
	}
	if len(args) > 1 {
		keep, err := strconv.Atoi(args[1])
		if err != nil {
			return options, err
		}
		if keep < 0 {
			return options, errors.New("prune keep must be non-negative")
		}
		options.Keep = keep
	}
	if len(args) > 2 {
		return options, errors.New("usage: codog background prune [days] [keep]")
	}
	return options, nil
}

func (a *App) RegisterPluginTools() error {
	manifests, err := plugins.Load(a.Workspace)
	if err != nil {
		return err
	}
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		for _, tool := range manifest.Tools {
			if tool.Command == "" {
				continue
			}
			name := tool.Name
			if name == "" {
				continue
			}
			if !plugins.ValidToolPermission(tool.Permission) {
				return fmt.Errorf("plugin tool %q declares unsupported permission %q", name, tool.Permission)
			}
			if a.Tools.Has(name) {
				return fmt.Errorf("plugin tool %q conflicts with an existing tool", name)
			}
			a.Tools.Register(tools.CommandTool{
				Name:        name,
				Description: tool.Description,
				Schema:      tool.InputSchema,
				Required:    tools.Permission(tool.Permission),
				Command:     tool.Command,
				Args:        tool.Args,
				Workspace:   manifest.Root,
			})
		}
	}
	return nil
}

type reloadPluginsRequest struct {
	Format string
}

type reloadPluginsReport struct {
	Kind             string   `json:"kind"`
	Action           string   `json:"action"`
	Status           string   `json:"status"`
	Workspace        string   `json:"workspace"`
	Plugins          int      `json:"plugins"`
	EnabledPlugins   int      `json:"enabled_plugins"`
	PluginTools      int      `json:"plugin_tools"`
	ToolCountBefore  int      `json:"tool_count_before"`
	ToolCountAfter   int      `json:"tool_count_after"`
	MCPToolsReloaded bool     `json:"mcp_tools_reloaded"`
	PluginIDs        []string `json:"plugin_ids,omitempty"`
	EnabledPluginIDs []string `json:"enabled_plugin_ids,omitempty"`
	Reloaded         bool     `json:"reloaded"`
}

func (a *App) ReloadPlugins(args []string) error {
	req, err := parseReloadPluginsArgs(args)
	if err != nil {
		return err
	}
	manifests, err := plugins.Load(a.Workspace)
	if err != nil {
		return err
	}
	before := 0
	if a.Tools != nil {
		before = len(a.Tools.Infos())
	}
	oldRegistry := a.Tools
	oldMCPLoaded := a.mcpToolsLoaded
	nextRegistry, err := a.newToolRegistry()
	if err != nil {
		return err
	}
	a.Tools = nextRegistry
	a.mcpToolsLoaded = false
	if err := a.RegisterPluginTools(); err != nil {
		a.Tools = oldRegistry
		a.mcpToolsLoaded = oldMCPLoaded
		return err
	}
	report := buildReloadPluginsReport(a.Workspace, manifests, before, len(a.Tools.Infos()), oldMCPLoaded)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderReloadPluginsReport(a.Out, report)
	return nil
}

func (a *App) newToolRegistry() (*tools.Registry, error) {
	additionalDirs, err := pathscope.EffectiveDirs(a.Workspace, a.Config.AdditionalDirs)
	if err != nil {
		return nil, err
	}
	questionIn := a.In
	if questionIn == nil {
		questionIn = os.Stdin
	}
	questionOut := a.Err
	if questionOut == nil {
		questionOut = io.Discard
	}
	return tools.NewRegistryWithOptions(a.Workspace, tools.RegistryOptions{
		SandboxStrategy: a.Config.Future.SandboxStrategy,
		AdditionalDirs:  additionalDirs,
		ConfigHome:      a.Config.ConfigHome,
		MCPServers:      a.Config.MCPServers,
		QuestionIn:      questionIn,
		QuestionOut:     questionOut,
	}), nil
}

func parseReloadPluginsArgs(args []string) (reloadPluginsRequest, error) {
	req := reloadPluginsRequest{Format: "text"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("reload-plugins output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "reload" || arg == "refresh":
		default:
			return req, fmt.Errorf("unexpected reload-plugins argument %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "reload-plugins"); err != nil {
		return req, err
	}
	return req, nil
}

func buildReloadPluginsReport(workspace string, manifests []plugins.Manifest, before, after int, oldMCPLoaded bool) reloadPluginsReport {
	report := reloadPluginsReport{
		Kind:             "reload_plugins",
		Action:           "reload",
		Status:           "ok",
		Workspace:        workspace,
		Plugins:          len(manifests),
		ToolCountBefore:  before,
		ToolCountAfter:   after,
		MCPToolsReloaded: oldMCPLoaded,
		Reloaded:         true,
	}
	for _, manifest := range manifests {
		report.PluginIDs = append(report.PluginIDs, manifest.ID)
		if !manifest.Enabled {
			continue
		}
		report.EnabledPlugins++
		report.EnabledPluginIDs = append(report.EnabledPluginIDs, manifest.ID)
		for _, tool := range manifest.Tools {
			if strings.TrimSpace(tool.Name) != "" && strings.TrimSpace(tool.Command) != "" {
				report.PluginTools++
			}
		}
	}
	sort.Strings(report.PluginIDs)
	sort.Strings(report.EnabledPluginIDs)
	return report
}

func renderReloadPluginsReport(out io.Writer, report reloadPluginsReport) {
	fmt.Fprintln(out, "Plugins Reloaded")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Plugins          %d\n", report.Plugins)
	fmt.Fprintf(out, "  Enabled          %d\n", report.EnabledPlugins)
	fmt.Fprintf(out, "  Plugin tools     %d\n", report.PluginTools)
	fmt.Fprintf(out, "  Tools before     %d\n", report.ToolCountBefore)
	fmt.Fprintf(out, "  Tools after      %d\n", report.ToolCountAfter)
	if len(report.EnabledPluginIDs) > 0 {
		fmt.Fprintf(out, "  Enabled IDs      %s\n", strings.Join(report.EnabledPluginIDs, ", "))
	}
	if report.MCPToolsReloaded {
		fmt.Fprintln(out, "  MCP tools        will reload on next provider turn")
	}
}

func (a *App) RegisterMCPTools(ctx context.Context) error {
	if a.mcpToolsLoaded {
		return nil
	}
	failures := []string{}
	registered := 0
	for _, serverName := range sortedMCPServerNames(a.Config.MCPServers) {
		server := a.Config.MCPServers[serverName]
		result := mcp.ListTools(ctx, serverName, server)
		if result.Error != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", serverName, result.Error))
			continue
		}
		for _, remoteTool := range result.Tools {
			name := tools.NewMCPToolName(serverName, remoteTool.Name)
			if a.Tools.Has(name) {
				return fmt.Errorf("mcp tool %q conflicts with an existing tool", name)
			}
			a.Tools.Register(tools.MCPTool{
				Name:        name,
				Description: remoteTool.Description,
				Schema:      remoteTool.InputSchema,
				Required:    tools.PermissionWorkspace,
				ServerName:  serverName,
				Server:      server,
				RemoteName:  remoteTool.Name,
			})
			registered++
		}
	}
	if len(failures) != 0 {
		if a.Err != nil {
			for _, failure := range failures {
				fmt.Fprintf(a.Err, "MCP server unavailable: %s\n", failure)
			}
		}
		if registered == 0 {
			return fmt.Errorf("no MCP tools registered; %s", strings.Join(failures, "; "))
		}
	}
	a.mcpToolsLoaded = true
	return nil
}

func sortedMCPServerNames(servers map[string]config.MCPServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a *App) ListAgents() error {
	return a.listAgents("text", "")
}

type agentsListReport struct {
	Kind   string                 `json:"kind"`
	Action string                 `json:"action"`
	Status string                 `json:"status"`
	Count  int                    `json:"count"`
	Agents []agentdefs.Definition `json:"agents"`
}

type agentShowReport struct {
	Kind   string               `json:"kind"`
	Action string               `json:"action"`
	Status string               `json:"status"`
	Agent  agentdefs.Definition `json:"agent"`
}

func (a *App) listAgents(format string, filter string) error {
	defs, err := agentdefs.Load(a.Workspace)
	if err != nil {
		return err
	}
	if strings.TrimSpace(filter) != "" {
		defs = filterAgentDefinitions(defs, filter)
	}
	if format == "json" {
		data, _ := json.MarshalIndent(agentsListReport{
			Kind:   "agents",
			Action: "list",
			Status: "ok",
			Count:  len(defs),
			Agents: defs,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	data, _ := json.MarshalIndent(defs, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) showAgent(name string, format string) error {
	defs, err := agentdefs.Load(a.Workspace)
	if err != nil {
		return err
	}
	for _, def := range defs {
		if strings.EqualFold(def.Name, name) {
			if format == "json" {
				data, _ := json.MarshalIndent(agentShowReport{
					Kind:   "agents",
					Action: "show",
					Status: "ok",
					Agent:  def,
				}, "", "  ")
				fmt.Fprintln(a.Out, string(data))
				return nil
			}
			data, _ := json.MarshalIndent(def, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
	}
	return fmt.Errorf("unknown agent %q", name)
}

func filterAgentDefinitions(defs []agentdefs.Definition, filter string) []agentdefs.Definition {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return defs
	}
	out := make([]agentdefs.Definition, 0, len(defs))
	for _, def := range defs {
		if strings.Contains(strings.ToLower(def.Name), filter) || strings.Contains(strings.ToLower(def.Description), filter) {
			out = append(out, def)
		}
	}
	return out
}

func (a *App) Agents(args []string) error {
	return a.AgentsWithOverrides(args, config.FlagOverrides{})
}

func (a *App) AgentsWithOverrides(args []string, overrides config.FlagOverrides) error {
	var err error
	var format string
	args, format, err = stripJSONOnlyOutputFormat("agents", args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return a.listAgents(format, "")
	}
	if args[0] == "list" {
		filter, err := parseListFilterArgs("agents list", args[1:], "codog agents list [FILTER] [--json|--output-format text|json]", "unknown_option")
		if err != nil {
			return renderCLIError(a.Out, err, format)
		}
		return a.listAgents(format, filter)
	}
	if args[0] == "show" {
		if len(args) < 2 {
			return errors.New("usage: codog agents show NAME")
		}
		if len(args) > 2 {
			return renderCLIError(a.Out, unexpectedExtraArgsError{
				Command: "agents show",
				Args:    append([]string(nil), args[2:]...),
				Usage:   "codog agents show NAME [--json|--output-format text|json]",
			}, format)
		}
		return a.showAgent(args[1], format)
	}
	if args[0] == "worktrees" {
		allocations, err := worktree.List(a.Workspace)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(allocations, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "worktree-remove" {
		if len(args) < 2 {
			return errors.New("usage: codog agents worktree-remove ID")
		}
		allocation, err := worktree.Load(a.Workspace, args[1])
		if err != nil {
			return err
		}
		if err := worktree.Remove(a.Workspace, args[1]); err != nil {
			return err
		}
		if err := a.runWorktreeRemoveHook(context.Background(), allocation, "manual"); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]any{"removed": true, "id": args[1]}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] != "run" {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    args[0],
			Status:    "error",
			ErrorKind: "unknown_agents_subcommand",
			Message:   fmt.Sprintf("unknown agents command %q", args[0]),
			Hint:      "Use `codog agents list`, `codog agents show NAME`, `codog agents run NAME PROMPT`, or `codog agents worktrees`.",
		}, format)
	}
	req, err := parseAgentRunArgs(args[1:])
	if err != nil {
		return err
	}
	defs, err := agentdefs.Load(a.Workspace)
	if err != nil {
		return err
	}
	var selected *agentdefs.Definition
	for i := range defs {
		if strings.EqualFold(defs[i].Name, req.Name) {
			selected = &defs[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("unknown agent %q", req.Name)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	runWorkspace := a.Workspace
	var allocation *worktree.Allocation
	if req.Worktree {
		next, err := worktree.Allocate(a.Workspace, selected.Name)
		if err != nil {
			return err
		}
		allocation = &next
		runWorkspace = next.Path
		if err := a.runWorktreeCreateHook(context.Background(), next, "agent"); err != nil {
			_ = a.removeAllocatedWorktree(context.Background(), next, "create_hook_failed")
			return err
		}
	}
	command := buildAgentCommand(exe, *selected, req.Prompt)
	sessionID, err := a.sessionIDFromOverrides(overrides)
	if err != nil {
		if allocation != nil {
			_ = a.removeAllocatedWorktree(context.Background(), *allocation, "run_failed")
		}
		return err
	}
	task, err := background.NewStore(a.Config.ConfigHome).RunWithOptions(command, runWorkspace, background.RunOptions{Kind: "agent", AgentType: selected.Name, SessionID: sessionID})
	if err != nil {
		if allocation != nil {
			_ = a.removeAllocatedWorktree(context.Background(), *allocation, "run_failed")
		}
		return err
	}
	response := map[string]any{"agent": selected.Name, "task": task}
	if allocation != nil {
		response["worktree"] = allocation
	}
	data, _ := json.MarshalIndent(response, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	a.runTaskCreatedHook(context.Background(), task)
	a.runSubagentStartHook(context.Background(), task.ID, selected.Name)
	return nil
}

type agentRunRequest struct {
	Name     string
	Prompt   string
	Worktree bool
}

func parseAgentRunArgs(args []string) (agentRunRequest, error) {
	var req agentRunRequest
	if len(args) > 0 && args[0] == "--worktree" {
		req.Worktree = true
		args = args[1:]
	}
	if len(args) < 2 {
		return agentRunRequest{}, errors.New("usage: codog agents run [--worktree] NAME PROMPT")
	}
	req.Name = args[0]
	req.Prompt = strings.Join(args[1:], " ")
	return req, nil
}

func buildAgentCommand(exe string, def agentdefs.Definition, prompt string) string {
	combined := strings.TrimSpace(strings.Join([]string{def.Prompt, prompt}, "\n\n"))
	args := []string{shellQuote(exe)}
	if def.Model != "" {
		args = append(args, "--model", shellQuote(def.Model))
	}
	args = append(args, "prompt", shellQuote(combined))
	return strings.Join(args, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (a *App) ListPlugins() error {
	return a.listPlugins("text")
}

type pluginsListSummary struct {
	Total    int `json:"total"`
	Enabled  int `json:"enabled"`
	Disabled int `json:"disabled"`
}

type pluginsListReport struct {
	Kind            string              `json:"kind"`
	Action          string              `json:"action"`
	Status          string              `json:"status"`
	Summary         pluginsListSummary  `json:"summary"`
	Plugins         []plugins.Manifest  `json:"plugins"`
	ConfigLoadError *string             `json:"config_load_error"`
	LoadFailures    []map[string]string `json:"load_failures"`
}

func (a *App) listPlugins(format string) error {
	manifests, err := plugins.Load(a.Workspace)
	if err != nil {
		return err
	}
	if format == "json" {
		summary := pluginsListSummary{Total: len(manifests)}
		for _, manifest := range manifests {
			if manifest.Enabled {
				summary.Enabled++
			} else {
				summary.Disabled++
			}
		}
		data, _ := json.MarshalIndent(pluginsListReport{
			Kind:            "plugin",
			Action:          "list",
			Status:          "ok",
			Summary:         summary,
			Plugins:         manifests,
			ConfigLoadError: nil,
			LoadFailures:    []map[string]string{},
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	data, _ := json.MarshalIndent(manifests, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Marketplace(args []string) error {
	var err error
	var format string
	args, format, err = stripJSONOnlyOutputFormat("marketplace", args)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			if option := firstFlagShapedArg(args[1:]); option != "" {
				return renderCLIError(a.Out, unknownOptionError{
					Kind:    "cli_parse",
					Command: "plugins list",
					Option:  option,
					Usage:   "codog plugins list [--json|--output-format text|json]",
				}, format)
			}
			return renderCLIError(a.Out, unexpectedExtraArgsError{
				Command: "plugins list",
				Args:    append([]string(nil), args[1:]...),
				Usage:   "codog plugins list [--json|--output-format text|json]",
			}, format)
		}
		return a.listPlugins(format)
	}
	var payload any
	switch args[0] {
	case "remote":
		indexes, err := a.marketplaceRemote(args[1:])
		if err != nil {
			return err
		}
		payload = indexes
	case "updates":
		updates, err := a.marketplaceUpdates(args[1:])
		if err != nil {
			return err
		}
		payload = updates
	case "install":
		if len(args) < 2 {
			return errors.New("usage: codog marketplace install PATH")
		}
		manifest, err := plugins.Install(a.Workspace, args[1])
		if err != nil {
			if os.IsNotExist(err) {
				return renderPluginSourceNotFound(a.Out, args[0], args[1], format)
			}
			return err
		}
		payload = manifest
	case "validate":
		if len(args) < 2 {
			return renderActionError(a.Out, actionErrorReport{
				Kind:      "plugins",
				Action:    args[0],
				Status:    "error",
				ErrorKind: "plugin_source_required",
				Message:   "plugin validation requires a source path",
				Hint:      "Usage: codog plugins validate PATH [--json|--output-format text|json]",
			}, format)
		}
		if len(args) > 2 {
			return renderCLIError(a.Out, unexpectedExtraArgsError{
				Command: "plugins validate",
				Args:    append([]string(nil), args[2:]...),
				Usage:   "codog plugins validate PATH [--json|--output-format text|json]",
			}, format)
		}
		result, err := plugins.Validate(args[1])
		if err != nil {
			if os.IsNotExist(err) {
				return renderPluginSourceNotFound(a.Out, args[0], args[1], format)
			}
			return err
		}
		return renderPluginValidation(a.Out, args[1], result, format)
	case "install-remote":
		result, err := a.marketplaceInstallRemote(args[1:])
		if err != nil {
			return err
		}
		payload = result
	case "update":
		result, err := a.marketplaceUpdate(args[1:])
		if err != nil {
			return err
		}
		payload = result
	case "enable":
		if len(args) < 2 {
			return errors.New("usage: codog marketplace enable ID")
		}
		manifest, err := plugins.Enable(a.Workspace, args[1])
		if err != nil {
			if os.IsNotExist(err) {
				return renderPluginNotFound(a.Out, args[0], args[1], format)
			}
			return err
		}
		payload = manifest
	case "disable":
		if len(args) < 2 {
			return errors.New("usage: codog marketplace disable ID")
		}
		manifest, err := plugins.Disable(a.Workspace, args[1])
		if err != nil {
			if os.IsNotExist(err) {
				return renderPluginNotFound(a.Out, args[0], args[1], format)
			}
			return err
		}
		payload = manifest
	case "remove", "uninstall":
		if len(args) < 2 {
			return errors.New("usage: codog marketplace remove ID")
		}
		if err := plugins.Remove(a.Workspace, args[1]); err != nil {
			if os.IsNotExist(err) {
				return renderPluginNotFound(a.Out, args[0], args[1], format)
			}
			return err
		}
		payload = map[string]any{"removed": true, "id": args[1]}
	case "show":
		if len(args) < 2 {
			return errors.New("usage: codog plugins show ID")
		}
		if len(args) > 2 {
			return renderCLIError(a.Out, unexpectedExtraArgsError{
				Command: "plugins show",
				Args:    append([]string(nil), args[2:]...),
				Usage:   "codog plugins show ID [--json|--output-format text|json]",
			}, format)
		}
		manifest, err := a.findPlugin(args[1])
		if err != nil {
			if errors.Is(err, errPluginNotFound) {
				return renderPluginNotFound(a.Out, args[0], args[1], format)
			}
			return err
		}
		payload = map[string]any{"kind": "plugin", "action": "show", "status": "ok", "plugin": manifest}
	default:
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "plugins",
			Action:    args[0],
			Status:    "error",
			ErrorKind: "unknown_plugins_action",
			Message:   fmt.Sprintf("unknown plugins action %q", args[0]),
			Hint:      "Use `codog plugins list`, `show`, `validate`, `remote`, `updates`, `install`, `enable`, `disable`, or `remove`.",
		}, format)
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

var errPluginNotFound = errors.New("plugin not found")

func (a *App) findPlugin(id string) (plugins.Manifest, error) {
	manifests, err := plugins.Load(a.Workspace)
	if err != nil {
		return plugins.Manifest{}, err
	}
	for _, manifest := range manifests {
		if strings.EqualFold(manifest.ID, id) || strings.EqualFold(manifest.Name, id) {
			return manifest, nil
		}
	}
	return plugins.Manifest{}, errPluginNotFound
}

func renderPluginNotFound(out io.Writer, action string, id string, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      "plugins",
		Action:    action,
		Status:    "error",
		ErrorKind: "plugin_not_found",
		Message:   fmt.Sprintf("plugin %q was not found", id),
		Hint:      "Run `codog plugins list` to see installed plugins, then retry with one of those IDs.",
	}, format)
}

func renderPluginSourceNotFound(out io.Writer, action string, source string, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      "plugins",
		Action:    action,
		Status:    "error",
		ErrorKind: "plugin_source_not_found",
		Message:   fmt.Sprintf("plugin source %q was not found", source),
		Hint:      "Pass a directory containing plugin.json or the path to a plugin.json file.",
	}, format)
}

type pluginValidationReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Source string `json:"source"`
	plugins.ValidationResult
}

func renderPluginValidation(out io.Writer, source string, result plugins.ValidationResult, format string) error {
	status := "ok"
	if !result.Success {
		status = "error"
	}
	report := pluginValidationReport{
		Kind:             "plugin",
		Action:           "validate",
		Status:           status,
		Source:           source,
		ValidationResult: result,
	}
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		if !result.Success {
			return &ExitError{Code: 1, Err: errors.New("plugin validation failed"), Silent: true}
		}
		return nil
	}
	fmt.Fprintf(out, "Plugin Validation\n\n")
	fmt.Fprintf(out, "  Source   %s\n", source)
	fmt.Fprintf(out, "  File     %s\n", result.FilePath)
	fmt.Fprintf(out, "  Status   %s\n", status)
	if len(result.Errors) > 0 {
		fmt.Fprintln(out, "\nErrors:")
		for _, item := range result.Errors {
			fmt.Fprintf(out, "  - %s: %s\n", item.Path, item.Message)
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Fprintln(out, "\nWarnings:")
		for _, item := range result.Warnings {
			fmt.Fprintf(out, "  - %s: %s\n", item.Path, item.Message)
		}
	}
	if !result.Success {
		return &ExitError{Code: 1, Err: errors.New("plugin validation failed"), Silent: true}
	}
	return nil
}

func (a *App) marketplaceRemote(args []string) ([]plugins.MarketplaceIndex, error) {
	sources := a.marketplaceSources()
	if len(args) > 0 {
		source := plugins.MarketplaceSource{URL: args[0], PublicKey: a.marketplacePublicKey(args[0])}
		if len(args) > 1 {
			source.PublicKey = args[1]
		}
		sources = []plugins.MarketplaceSource{source}
	}
	if len(sources) == 0 {
		return nil, errors.New("usage: codog marketplace remote [URL] [PUBLIC_KEY]")
	}
	indexes := make([]plugins.MarketplaceIndex, 0, len(sources))
	for _, source := range sources {
		index, err := plugins.FetchMarketplace(context.Background(), source.URL, source.PublicKey)
		if err != nil {
			return nil, err
		}
		indexes = append(indexes, index)
	}
	return indexes, nil
}

func (a *App) marketplaceUpdates(args []string) ([]plugins.MarketplaceUpdate, error) {
	sources := a.marketplaceSources()
	if len(args) > 0 {
		source := plugins.MarketplaceSource{URL: args[0], PublicKey: a.marketplacePublicKey(args[0])}
		if len(args) > 1 {
			source.PublicKey = args[1]
		}
		sources = []plugins.MarketplaceSource{source}
	}
	if len(sources) == 0 {
		return nil, errors.New("usage: codog marketplace updates [URL] [PUBLIC_KEY]")
	}
	return plugins.CheckUpdates(context.Background(), a.Workspace, sources)
}

func (a *App) marketplaceInstallRemote(args []string) (plugins.RemoteInstallResult, error) {
	if len(args) < 1 {
		return plugins.RemoteInstallResult{}, errors.New("usage: codog marketplace install-remote ID [URL] [PUBLIC_KEY]")
	}
	id := args[0]
	if len(args) > 1 {
		source := plugins.MarketplaceSource{URL: args[1], PublicKey: a.marketplacePublicKey(args[1])}
		if len(args) > 2 {
			source.PublicKey = args[2]
		}
		return plugins.InstallRemote(context.Background(), a.Workspace, source.URL, id, source.PublicKey)
	}
	sources := a.marketplaceSources()
	if len(sources) == 0 {
		return plugins.RemoteInstallResult{}, errors.New("usage: codog marketplace install-remote ID [URL] [PUBLIC_KEY]")
	}
	for _, source := range sources {
		index, err := plugins.FetchMarketplace(context.Background(), source.URL, source.PublicKey)
		if err != nil {
			return plugins.RemoteInstallResult{}, err
		}
		if _, ok := index.Find(id); ok {
			return plugins.InstallRemoteFromIndex(context.Background(), a.Workspace, index, id)
		}
	}
	return plugins.RemoteInstallResult{}, fmt.Errorf("plugin %q not found in configured marketplaces", id)
}

func (a *App) marketplaceUpdate(args []string) (plugins.RemoteUpdateResult, error) {
	if len(args) < 1 {
		return plugins.RemoteUpdateResult{}, errors.New("usage: codog marketplace update ID [URL] [PUBLIC_KEY]")
	}
	id := args[0]
	if len(args) > 1 {
		source := plugins.MarketplaceSource{URL: args[1], PublicKey: a.marketplacePublicKey(args[1])}
		if len(args) > 2 {
			source.PublicKey = args[2]
		}
		return plugins.UpdateRemote(context.Background(), a.Workspace, []plugins.MarketplaceSource{source}, id)
	}
	sources := a.marketplaceSources()
	if len(sources) == 0 {
		return plugins.RemoteUpdateResult{}, errors.New("usage: codog marketplace update ID [URL] [PUBLIC_KEY]")
	}
	return plugins.UpdateRemote(context.Background(), a.Workspace, sources, id)
}

func (a *App) marketplaceSources() []plugins.MarketplaceSource {
	sources := make([]plugins.MarketplaceSource, 0, len(a.Config.Future.PluginMarketplaces))
	for _, marketplaceURL := range a.Config.Future.PluginMarketplaces {
		marketplaceURL = strings.TrimSpace(marketplaceURL)
		if marketplaceURL == "" {
			continue
		}
		sources = append(sources, plugins.MarketplaceSource{
			URL:       marketplaceURL,
			PublicKey: a.marketplacePublicKey(marketplaceURL),
		})
	}
	return sources
}

func (a *App) marketplacePublicKey(marketplaceURL string) string {
	if a.Config.Future.PluginMarketplaceKeys == nil {
		return ""
	}
	return a.Config.Future.PluginMarketplaceKeys[marketplaceURL]
}

func (a *App) Login(args []string) error {
	flow := "browser"
	rest := args
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "browser":
			flow = "browser"
			rest = args[1:]
		case "device":
			flow = "device"
			rest = args[1:]
		}
	}
	return a.OAuth(append([]string{flow, "login"}, rest...))
}

func (a *App) Logout(args []string) error {
	return a.OAuth(append([]string{"logout"}, args...))
}

func (a *App) OAuthRefresh(args []string) error {
	profile, err := parseOAuthRefreshArgs(args)
	if err != nil {
		return err
	}
	token, err := oauth.RefreshStoredToken(context.Background(), a.Config.ConfigHome, profile)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(token.View(time.Now().UTC()), "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func parseOAuthRefreshArgs(args []string) (string, error) {
	profile := ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return "", errors.New("oauth-refresh output format is required")
			}
			if args[index] != "json" {
				return "", fmt.Errorf("unknown oauth-refresh output format %q", args[index])
			}
		case strings.HasPrefix(arg, "--output-format="):
			format := strings.TrimPrefix(arg, "--output-format=")
			if format != "json" {
				return "", fmt.Errorf("unknown oauth-refresh output format %q", format)
			}
		case strings.HasPrefix(arg, "-"):
			return "", fmt.Errorf("unknown oauth-refresh flag %q", arg)
		default:
			if profile != "" {
				return "", fmt.Errorf("unexpected oauth-refresh argument %q", arg)
			}
			profile = arg
		}
	}
	return profile, nil
}

func (a *App) OAuth(args []string) error {
	if len(args) == 0 || args[0] == "pkce" {
		pkce, err := oauth.GeneratePKCE()
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(pkce, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "discover" {
		if len(args) < 2 {
			return errors.New("usage: codog oauth discover ISSUER_URL")
		}
		metadata, err := oauth.DiscoverProvider(context.Background(), args[1])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(metadata, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "device" {
		return a.oauthDevice(args[1:])
	}
	if args[0] == "browser" {
		return a.oauthBrowser(args[1:])
	}
	if args[0] == "provider" {
		return a.oauthProvider(args[1:])
	}
	if args[0] == "status" {
		profile := ""
		if len(args) > 1 {
			profile = args[1]
		}
		status := oauth.InspectStatus(a.Config.ConfigHome, profile, time.Now().UTC())
		data, _ := json.MarshalIndent(status, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "logout" {
		profile := ""
		if len(args) > 1 {
			profile = args[1]
		}
		result, err := oauth.Logout(context.Background(), a.Config.ConfigHome, profile)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] != "token" {
		return errors.New("usage: codog oauth pkce | oauth discover ISSUER_URL | oauth provider save|list|show|delete | oauth device start|poll|login | oauth browser start|exchange|login | oauth status [PROFILE] | oauth logout [PROFILE] | oauth token save|show|refresh|revoke|delete")
	}
	if len(args) < 2 {
		return errors.New("usage: codog oauth token save ACCESS_TOKEN [REFRESH_TOKEN] [EXPIRES_AT] | show | refresh [PROFILE] | delete")
	}
	switch args[1] {
	case "save":
		if len(args) < 3 {
			return errors.New("usage: codog oauth token save ACCESS_TOKEN [REFRESH_TOKEN] [EXPIRES_AT]")
		}
		token := oauth.Token{AccessToken: args[2]}
		if len(args) > 3 {
			token.RefreshToken = args[3]
		}
		if len(args) > 4 {
			expiresAt, err := time.Parse(time.RFC3339, args[4])
			if err != nil {
				return err
			}
			token.ExpiresAt = expiresAt
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(saved.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "show":
		token, err := oauth.LoadToken(a.Config.ConfigHome)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(token.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "refresh":
		profile := ""
		if len(args) > 2 {
			profile = args[2]
		}
		token, err := oauth.RefreshStoredToken(context.Background(), a.Config.ConfigHome, profile)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(token.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "revoke":
		result, err := a.oauthTokenRevoke(args[2:])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "delete":
		if err := oauth.DeleteToken(a.Config.ConfigHome); err != nil {
			return err
		}
		fmt.Fprintln(a.Out, `{"deleted":true}`)
		return nil
	default:
		return fmt.Errorf("unknown oauth token command %q", args[1])
	}
}

func (a *App) oauthTokenRevoke(args []string) (map[string]any, error) {
	profileName := ""
	tokenKind := "access"
	if len(args) > 0 {
		profileName = args[0]
	}
	if len(args) > 1 {
		tokenKind = args[1]
	}
	profile, err := oauth.ResolveProviderProfile(a.Config.ConfigHome, profileName)
	if err != nil {
		return nil, err
	}
	token, err := oauth.LoadToken(a.Config.ConfigHome)
	if err != nil {
		return nil, err
	}
	tokenValue := token.AccessToken
	hint := "access_token"
	if tokenKind == "refresh" {
		tokenValue = token.RefreshToken
		hint = "refresh_token"
	} else if tokenKind != "access" {
		return nil, errors.New("token kind must be access or refresh")
	}
	if err := oauth.RevokeToken(context.Background(), profile.Metadata, profile.ClientID, tokenValue, hint); err != nil {
		return nil, err
	}
	return map[string]any{"revoked": true, "profile": profile.Name, "token": tokenKind}, nil
}

func (a *App) oauthProvider(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: codog oauth provider save NAME ISSUER_URL CLIENT_ID [SCOPE...] | list | show NAME | delete NAME")
	}
	var payload any
	switch args[0] {
	case "save":
		if len(args) < 4 {
			return errors.New("usage: codog oauth provider save NAME ISSUER_URL CLIENT_ID [SCOPE...]")
		}
		profile, err := oauth.SaveProviderProfile(context.Background(), a.Config.ConfigHome, args[1], args[2], args[3], args[4:])
		if err != nil {
			return err
		}
		payload = profile
	case "list":
		profiles, err := oauth.ListProviderProfiles(a.Config.ConfigHome)
		if err != nil {
			return err
		}
		payload = profiles
	case "show":
		if len(args) < 2 {
			return errors.New("usage: codog oauth provider show NAME")
		}
		profile, err := oauth.LoadProviderProfile(a.Config.ConfigHome, args[1])
		if err != nil {
			return err
		}
		payload = profile
	case "delete":
		if len(args) < 2 {
			return errors.New("usage: codog oauth provider delete NAME")
		}
		if err := oauth.DeleteProviderProfile(a.Config.ConfigHome, args[1]); err != nil {
			return err
		}
		payload = map[string]any{"deleted": true, "name": args[1]}
	default:
		return fmt.Errorf("unknown oauth provider command %q", args[0])
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

type providerPreset struct {
	Name         string   `json:"name"`
	Protocol     string   `json:"protocol"`
	BaseURL      string   `json:"base_url,omitempty"`
	DefaultModel string   `json:"default_model,omitempty"`
	AuthEnv      []string `json:"auth_env,omitempty"`
	Description  string   `json:"description,omitempty"`
}

type providerAuthReport struct {
	Configured     bool     `json:"configured"`
	Sources        []string `json:"sources"`
	APIKey         bool     `json:"api_key"`
	AuthToken      bool     `json:"auth_token"`
	StoredOAuth    bool     `json:"stored_oauth"`
	PreferredToken string   `json:"preferred_token,omitempty"`
}

type activeProviderReport struct {
	Name      string             `json:"name"`
	Protocol  string             `json:"protocol"`
	BaseURL   string             `json:"base_url"`
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	MaxTurns  int                `json:"max_turns"`
	Auth      providerAuthReport `json:"auth"`
}

type oauthProviderSummary struct {
	Name     string   `json:"name"`
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes,omitempty"`
}

type providersReport struct {
	Kind          string                 `json:"kind"`
	Action        string                 `json:"action"`
	Active        activeProviderReport   `json:"active"`
	Presets       []providerPreset       `json:"presets,omitempty"`
	OAuthProfiles []oauthProviderSummary `json:"oauth_profiles,omitempty"`
}

type providerSetReport struct {
	Kind     string                  `json:"kind"`
	Action   string                  `json:"action"`
	Status   string                  `json:"status"`
	Provider string                  `json:"provider"`
	BaseURL  string                  `json:"base_url,omitempty"`
	Model    string                  `json:"model,omitempty"`
	Target   string                  `json:"target,omitempty"`
	Path     string                  `json:"path,omitempty"`
	Changes  []config.MutationReport `json:"changes"`
}

type providerCommandRequest struct {
	Action  string
	Format  string
	Name    string
	BaseURL string
	Model   string
	Path    string
	Target  string
}

func (a *App) Providers(args []string) error {
	paths := []string{
		filepath.Join(a.Config.ConfigHome, "config.json"),
		".codog.json",
		".codog.local.json",
	}
	return renderProvidersCommand(a.Out, a.Config, paths, args)
}

func renderProvidersCommand(out io.Writer, cfg config.Config, paths []string, args []string) error {
	req, err := parseProviderCommandArgs(args)
	if err != nil {
		return err
	}
	if req.Action == "set" {
		report, err := setProviderConfig(paths, req)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		renderProviderSetText(out, report)
		return nil
	}
	report, err := buildProvidersReport(cfg, req.Action)
	if err != nil {
		return err
	}
	if req.Action == "show" {
		payload, err := providerShowPayload(report, req.Name)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		renderProviderShowText(out, payload)
		return nil
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	renderProvidersText(out, report)
	return nil
}

func parseProviderCommandArgs(args []string) (providerCommandRequest, error) {
	req := providerCommandRequest{Action: "status", Format: "text"}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("providers output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--base-url":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return req, errors.New("provider base URL is required")
			}
			req.BaseURL = args[i]
		case strings.HasPrefix(arg, "--base-url="):
			req.BaseURL = strings.TrimPrefix(arg, "--base-url=")
		case arg == "--model":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return req, errors.New("provider model is required")
			}
			req.Model = args[i]
		case strings.HasPrefix(arg, "--model="):
			req.Model = strings.TrimPrefix(arg, "--model=")
		case arg == "--target":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return req, errors.New("provider config target is required")
			}
			req.Target = args[i]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" {
				return req, errors.New("provider config path is required")
			}
			req.Path = args[i]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 0 {
		switch strings.ToLower(positionals[0]) {
		case "status", "list", "show", "set":
			req.Action = strings.ToLower(positionals[0])
			positionals = positionals[1:]
		default:
			req.Name = positionals[0]
			req.Action = "show"
			positionals = positionals[1:]
		}
	}
	switch req.Action {
	case "status", "list":
		if len(positionals) > 0 {
			return req, fmt.Errorf("unexpected providers argument %q", positionals[0])
		}
	case "show":
		if req.Name == "" {
			if len(positionals) == 0 {
				return req, errors.New("usage: codog providers show NAME")
			}
			req.Name = positionals[0]
			positionals = positionals[1:]
		}
		if len(positionals) > 0 {
			return req, fmt.Errorf("unexpected providers argument %q", positionals[0])
		}
	case "set":
		if len(positionals) == 0 {
			return req, errors.New("usage: codog providers set anthropic|openai|custom [BASE_URL] [MODEL] [--target user|project|local|--path PATH]")
		}
		req.Name = positionals[0]
		if len(positionals) > 1 && req.BaseURL == "" {
			req.BaseURL = positionals[1]
		}
		if len(positionals) > 2 && req.Model == "" {
			req.Model = positionals[2]
		}
		if len(positionals) > 3 {
			return req, fmt.Errorf("unexpected providers argument %q", positionals[3])
		}
	default:
		return req, fmt.Errorf("unknown providers command %q", req.Action)
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return req, fmt.Errorf("unknown providers output format %q", req.Format)
	}
}

func buildProvidersReport(cfg config.Config, action string) (providersReport, error) {
	profiles, err := oauth.ListProviderProfiles(cfg.ConfigHome)
	if err != nil {
		return providersReport{}, err
	}
	oauthProfiles := make([]oauthProviderSummary, 0, len(profiles))
	for _, profile := range profiles {
		oauthProfiles = append(oauthProfiles, oauthProviderSummary{
			Name:     profile.Name,
			Issuer:   profile.Issuer,
			ClientID: profile.ClientID,
			Scopes:   append([]string(nil), profile.Scopes...),
		})
	}
	return providersReport{
		Kind:          "providers",
		Action:        action,
		Active:        activeProvider(cfg),
		Presets:       providerPresets(),
		OAuthProfiles: oauthProfiles,
	}, nil
}

func activeProvider(cfg config.Config) activeProviderReport {
	name := "custom"
	protocol := "anthropic-compatible"
	if sameProviderURL(cfg.BaseURL, config.DefaultBaseURL) {
		name = "anthropic"
	}
	if strings.HasPrefix(strings.TrimSpace(cfg.Model), "openai/") {
		name = "openai"
		protocol = "openai-compatible"
	}
	return activeProviderReport{
		Name:      name,
		Protocol:  protocol,
		BaseURL:   cfg.BaseURL,
		Model:     cfg.Model,
		MaxTokens: cfg.MaxTokens,
		MaxTurns:  cfg.MaxTurns,
		Auth:      providerAuthStatus(cfg),
	}
}

func providerAuthStatus(cfg config.Config) providerAuthReport {
	storedOAuth := false
	if token, err := oauth.LoadToken(cfg.ConfigHome); err == nil && token.AccessToken != "" && token.AccessToken == cfg.AuthToken {
		storedOAuth = true
	}
	sources := []string{}
	if cfg.APIKey != "" {
		sources = append(sources, "api_key")
	}
	if cfg.AuthToken != "" {
		if storedOAuth {
			sources = append(sources, "stored_oauth")
		} else {
			sources = append(sources, "auth_token")
		}
	}
	preferred := ""
	if cfg.AuthToken != "" {
		preferred = "auth_token"
		if storedOAuth {
			preferred = "stored_oauth"
		}
	} else if cfg.APIKey != "" {
		preferred = "api_key"
	}
	return providerAuthReport{
		Configured:     len(sources) != 0,
		Sources:        sources,
		APIKey:         cfg.APIKey != "",
		AuthToken:      cfg.AuthToken != "",
		StoredOAuth:    storedOAuth,
		PreferredToken: preferred,
	}
}

func providerPresets() []providerPreset {
	return []providerPreset{
		{
			Name:         "anthropic",
			Protocol:     "anthropic-compatible",
			BaseURL:      config.DefaultBaseURL,
			DefaultModel: config.DefaultModel,
			AuthEnv:      []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
			Description:  "Anthropic Messages API.",
		},
		{
			Name:         "openai",
			Protocol:     "openai-compatible",
			BaseURL:      "https://api.openai.com/v1",
			DefaultModel: "openai/gpt-4o-mini",
			AuthEnv:      []string{"CODOG_API_KEY", "CODOG_AUTH_TOKEN", "OPENAI_API_KEY"},
			Description:  "OpenAI-compatible Chat Completions API selected by the openai/ model prefix.",
		},
		{
			Name:        "custom",
			Protocol:    "anthropic-compatible",
			AuthEnv:     []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
			Description: "Any endpoint that implements the Anthropic Messages API.",
		},
	}
}

func providerShowPayload(report providersReport, name string) (any, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "current", "active", "":
		return report.Active, nil
	case "oauth":
		return report.OAuthProfiles, nil
	}
	for _, preset := range report.Presets {
		if strings.EqualFold(preset.Name, name) {
			return preset, nil
		}
	}
	for _, profile := range report.OAuthProfiles {
		if strings.EqualFold(profile.Name, name) {
			return profile, nil
		}
	}
	return nil, fmt.Errorf("unknown provider %q", name)
}

func setProviderConfig(paths []string, req providerCommandRequest) (providerSetReport, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		return providerSetReport{}, errors.New("provider name is required")
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	model := strings.TrimSpace(req.Model)
	switch name {
	case "anthropic", "default":
		name = "anthropic"
		if baseURL == "" {
			baseURL = config.DefaultBaseURL
		}
		if model == "" {
			model = config.DefaultModel
		}
	case "custom", "compatible", "anthropic-compatible":
		name = "custom"
		if baseURL == "" {
			return providerSetReport{}, errors.New("custom provider requires --base-url or a BASE_URL positional argument")
		}
	case "openai", "openai-compatible":
		name = "openai"
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		if model == "" {
			model = "openai/gpt-4o-mini"
		}
	default:
		if baseURL == "" {
			return providerSetReport{}, fmt.Errorf("unknown provider %q; use anthropic, openai, or custom --base-url URL", req.Name)
		}
	}
	if err := validateProviderBaseURL(baseURL); err != nil {
		return providerSetReport{}, err
	}
	mutationReq := configMutationRequest{Target: req.Target, Path: req.Path}
	path, err := configMutationPath(mutationReq, paths)
	if err != nil {
		return providerSetReport{}, err
	}
	changes := []config.MutationReport{}
	baseReport, err := config.SetFileValue(path, "base_url", baseURL)
	if err != nil {
		return providerSetReport{}, err
	}
	changes = append(changes, baseReport)
	if model != "" {
		modelReport, err := config.SetFileValue(path, "model", model)
		if err != nil {
			return providerSetReport{}, err
		}
		changes = append(changes, modelReport)
	}
	return providerSetReport{
		Kind:     "provider",
		Action:   "set",
		Status:   "ok",
		Provider: name,
		BaseURL:  baseURL,
		Model:    model,
		Target:   req.Target,
		Path:     path,
		Changes:  changes,
	}, nil
}

func validateProviderBaseURL(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("provider base URL is required")
	}
	if strings.Contains(value, "://") {
		if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
			return nil
		}
		return errors.New("provider base URL must use http or https")
	}
	return errors.New("provider base URL must include a scheme")
}

func sameProviderURL(left, right string) bool {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(left)), "/") == strings.TrimRight(strings.ToLower(strings.TrimSpace(right)), "/")
}

func renderProvidersText(out io.Writer, report providersReport) {
	active := report.Active
	fmt.Fprintf(out, "Provider: %s (%s)\n", active.Name, active.Protocol)
	fmt.Fprintf(out, "Model: %s\n", active.Model)
	fmt.Fprintf(out, "Base URL: %s\n", active.BaseURL)
	auth := "not configured"
	if active.Auth.Configured {
		auth = strings.Join(active.Auth.Sources, ", ")
	}
	fmt.Fprintf(out, "Auth: %s\n", auth)
	if len(report.Presets) != 0 {
		fmt.Fprintln(out, "\nPresets:")
		for _, preset := range report.Presets {
			baseURL := preset.BaseURL
			if baseURL == "" {
				baseURL = "<custom>"
			}
			fmt.Fprintf(out, "  %s: %s (%s)\n", preset.Name, baseURL, preset.Protocol)
		}
	}
	if len(report.OAuthProfiles) != 0 {
		fmt.Fprintln(out, "\nOAuth profiles:")
		for _, profile := range report.OAuthProfiles {
			fmt.Fprintf(out, "  %s: %s\n", profile.Name, profile.Issuer)
		}
	}
}

func renderProviderShowText(out io.Writer, payload any) {
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(out, string(data))
}

func renderProviderSetText(out io.Writer, report providerSetReport) {
	fmt.Fprintf(out, "Provider set: %s\n", report.Provider)
	fmt.Fprintf(out, "Base URL: %s\n", report.BaseURL)
	if report.Model != "" {
		fmt.Fprintf(out, "Model: %s\n", report.Model)
	}
	fmt.Fprintf(out, "Config: %s\n", report.Path)
}

func (a *App) oauthBrowser(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: codog oauth browser start PROFILE REDIRECT_URI [SCOPE...] | exchange PROFILE CODE CODE_VERIFIER REDIRECT_URI | login PROFILE [ADDR]")
	}
	switch args[0] {
	case "start":
		if len(args) < 3 {
			return errors.New("usage: codog oauth browser start PROFILE REDIRECT_URI [SCOPE...]")
		}
		source, err := a.oauthProfileSource(args[1], args[3:])
		if err != nil {
			return err
		}
		auth, err := oauth.BuildBrowserAuthorization(source.Metadata, source.ClientID, args[2], source.Scopes, "", oauth.PKCE{})
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(auth, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "exchange":
		if len(args) < 5 {
			return errors.New("usage: codog oauth browser exchange PROFILE CODE CODE_VERIFIER REDIRECT_URI")
		}
		source, err := a.oauthProfileSource(args[1], nil)
		if err != nil {
			return err
		}
		token, err := oauth.ExchangeAuthorizationCode(context.Background(), source.Metadata, source.ClientID, args[2], args[3], args[4])
		if err != nil {
			return err
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(saved.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "login":
		if len(args) < 2 {
			return errors.New("usage: codog oauth browser login PROFILE [ADDR]")
		}
		source, err := a.oauthProfileSource(args[1], nil)
		if err != nil {
			return err
		}
		addr := "127.0.0.1:0"
		if len(args) > 2 {
			addr = args[2]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		pkce, err := oauth.GeneratePKCE()
		if err != nil {
			return err
		}
		state, err := oauth.GenerateState()
		if err != nil {
			return err
		}
		callback, err := oauth.StartBrowserCallbackServer(ctx, addr, "/oauth/callback", state)
		if err != nil {
			return err
		}
		defer callback.Close()
		auth, err := oauth.BuildBrowserAuthorization(source.Metadata, source.ClientID, callback.RedirectURI, source.Scopes, state, pkce)
		if err != nil {
			return err
		}
		if a.Err != nil {
			fmt.Fprintf(a.Err, "Open %s\n", auth.AuthorizationURL)
		}
		result := <-callback.Results
		if result.Err != nil {
			return result.Err
		}
		token, err := oauth.ExchangeAuthorizationCode(ctx, source.Metadata, source.ClientID, result.Callback.Code, auth.CodeVerifier, callback.RedirectURI)
		if err != nil {
			return err
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]any{"redirect_uri": callback.RedirectURI, "token": saved.View(time.Now().UTC())}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	default:
		return fmt.Errorf("unknown oauth browser command %q", args[0])
	}
}

func (a *App) oauthDevice(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: codog oauth device start ISSUER_URL CLIENT_ID [SCOPE...] | start PROFILE [SCOPE...] | poll ISSUER_URL CLIENT_ID DEVICE_CODE | poll PROFILE DEVICE_CODE | login ISSUER_URL CLIENT_ID [SCOPE...] | login PROFILE [SCOPE...]")
	}
	switch args[0] {
	case "start":
		source, err := a.oauthDeviceSource(args[1:], true)
		if err != nil {
			return err
		}
		auth, err := oauth.StartDeviceAuthorization(context.Background(), source.Metadata, source.ClientID, source.Scopes)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(auth, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "poll":
		source, deviceCode, err := a.oauthDevicePollSource(args[1:])
		if err != nil {
			return err
		}
		token, err := oauth.PollDeviceToken(context.Background(), source.Metadata, deviceCode, oauth.DevicePollOptions{ClientID: source.ClientID})
		if err != nil {
			return err
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(saved.View(time.Now().UTC()), "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "login":
		source, err := a.oauthDeviceSource(args[1:], true)
		if err != nil {
			return err
		}
		auth, err := oauth.StartDeviceAuthorization(context.Background(), source.Metadata, source.ClientID, source.Scopes)
		if err != nil {
			return err
		}
		if a.Err != nil {
			target := auth.VerificationURI
			if auth.VerificationURIComplete != "" {
				target = auth.VerificationURIComplete
			}
			fmt.Fprintf(a.Err, "Open %s and enter code %s\n", target, auth.UserCode)
		}
		token, err := oauth.PollDeviceToken(context.Background(), source.Metadata, auth.DeviceCode, oauth.DevicePollOptions{
			ClientID:  source.ClientID,
			Interval:  time.Duration(auth.Interval) * time.Second,
			ExpiresAt: auth.ExpiresAt,
		})
		if err != nil {
			return err
		}
		saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]any{"device": auth, "token": saved.View(time.Now().UTC())}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	default:
		return fmt.Errorf("unknown oauth device command %q", args[0])
	}
}

type oauthDeviceSource struct {
	Metadata oauth.ProviderMetadata
	ClientID string
	Scopes   []string
}

func (a *App) oauthProfileSource(name string, overrideScopes []string) (oauthDeviceSource, error) {
	profile, err := oauth.ResolveProviderProfile(a.Config.ConfigHome, name)
	if err != nil {
		return oauthDeviceSource{}, err
	}
	scopes := append([]string(nil), profile.Scopes...)
	if len(overrideScopes) != 0 {
		scopes = append([]string(nil), overrideScopes...)
	}
	return oauthDeviceSource{Metadata: profile.Metadata, ClientID: profile.ClientID, Scopes: scopes}, nil
}

func (a *App) oauthDeviceSource(args []string, allowScopes bool) (oauthDeviceSource, error) {
	if len(args) == 0 {
		return oauthDeviceSource{}, errors.New("oauth device provider is required")
	}
	if isURLish(args[0]) {
		if len(args) < 2 {
			return oauthDeviceSource{}, errors.New("oauth device client id is required")
		}
		metadata, err := oauth.DiscoverProvider(context.Background(), args[0])
		if err != nil {
			return oauthDeviceSource{}, err
		}
		source := oauthDeviceSource{Metadata: metadata, ClientID: args[1]}
		if allowScopes {
			source.Scopes = append([]string(nil), args[2:]...)
		}
		return source, nil
	}
	profile, err := oauth.ResolveProviderProfile(a.Config.ConfigHome, args[0])
	if err != nil {
		return oauthDeviceSource{}, err
	}
	scopes := append([]string(nil), profile.Scopes...)
	if allowScopes && len(args) > 1 {
		scopes = append([]string(nil), args[1:]...)
	}
	return oauthDeviceSource{Metadata: profile.Metadata, ClientID: profile.ClientID, Scopes: scopes}, nil
}

func (a *App) oauthDevicePollSource(args []string) (oauthDeviceSource, string, error) {
	if len(args) == 0 {
		return oauthDeviceSource{}, "", errors.New("usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE | poll PROFILE DEVICE_CODE")
	}
	if isURLish(args[0]) {
		if len(args) < 3 {
			return oauthDeviceSource{}, "", errors.New("usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE")
		}
		source, err := a.oauthDeviceSource(args[:2], false)
		return source, args[2], err
	}
	if len(args) < 2 {
		return oauthDeviceSource{}, "", errors.New("usage: codog oauth device poll PROFILE DEVICE_CODE")
	}
	source, err := a.oauthDeviceSource(args[:1], false)
	return source, args[1], err
}

func isURLish(value string) bool {
	return strings.Contains(value, "://")
}

func (a *App) Sandbox() error {
	status := sandbox.Detect()
	data, _ := json.MarshalIndent(sandboxReport{
		Kind:       "sandbox",
		Action:     "show",
		Status:     sandboxReportStatus(status.Available),
		OS:         status.OS,
		Strategies: append([]string(nil), status.Strategies...),
		Default:    status.Default,
		Available:  status.Available,
	}, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

type sandboxReport struct {
	Kind       string   `json:"kind"`
	Action     string   `json:"action"`
	Status     string   `json:"status"`
	OS         string   `json:"os"`
	Strategies []string `json:"strategies"`
	Default    string   `json:"default"`
	Available  bool     `json:"available"`
}

func sandboxReportStatus(available bool) string {
	if available {
		return "ok"
	}
	return "warn"
}

type sandboxToggleRequest struct {
	Action   string
	Format   string
	Strategy string
	Target   string
	Path     string
}

type sandboxToggleReport struct {
	Kind               string   `json:"kind"`
	Action             string   `json:"action"`
	Status             string   `json:"status"`
	OS                 string   `json:"os"`
	ConfiguredStrategy string   `json:"configured_strategy"`
	EffectiveStrategy  string   `json:"effective_strategy,omitempty"`
	Enabled            bool     `json:"enabled"`
	Available          bool     `json:"available"`
	DefaultStrategy    string   `json:"default_strategy,omitempty"`
	Strategies         []string `json:"strategies,omitempty"`
	Path               string   `json:"path,omitempty"`
	Error              string   `json:"error,omitempty"`
}

func (a *App) SandboxToggle(args []string) error {
	req, err := parseSandboxToggleArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "future.sandbox_strategy", req.Strategy); err != nil {
			return err
		}
		a.Config.Future.SandboxStrategy = req.Strategy
		req.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "future.sandbox_strategy"); err != nil {
			return err
		}
		a.Config.Future.SandboxStrategy = ""
		req.Path = path
	default:
		return fmt.Errorf("unknown sandbox-toggle command %q", req.Action)
	}
	report := buildSandboxToggleReport(req, a.Config.Future.SandboxStrategy)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSandboxToggleReport(a.Out, report)
	return nil
}

func parseSandboxToggleArgs(args []string) (sandboxToggleRequest, error) {
	req := sandboxToggleRequest{Action: "status", Format: "text", Target: "user"}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("sandbox-toggle output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("sandbox-toggle target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("sandbox-toggle path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown sandbox-toggle flag %q", arg)
		default:
			if actionSet {
				return req, fmt.Errorf("unexpected sandbox-toggle argument %q", arg)
			}
			action, strategy, err := normalizeSandboxToggleAction(arg)
			if err != nil {
				return req, err
			}
			req.Action = action
			req.Strategy = strategy
			actionSet = true
		}
	}
	if err := validateTextOrJSON(req.Format, "sandbox-toggle"); err != nil {
		return req, err
	}
	return req, nil
}

func normalizeSandboxToggleAction(value string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "status", "show", "list":
		return "status", "", nil
	case "clear", "reset", "unset":
		return "clear", "", nil
	case "on", "enable", "enabled", "auto", "detect":
		return "set", "detect", nil
	case "off", "disable", "disabled", "none":
		return "set", "off", nil
	case "sandbox-exec", "bwrap", "unshare":
		return "set", strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", "", fmt.Errorf("unknown sandbox-toggle strategy %q", value)
	}
}

func buildSandboxToggleReport(req sandboxToggleRequest, configured string) sandboxToggleReport {
	status := sandbox.Detect()
	configured = strings.TrimSpace(configured)
	effective, err := sandbox.ResolveStrategy(configured)
	report := sandboxToggleReport{
		Kind:               "sandbox_toggle",
		Action:             req.Action,
		OS:                 status.OS,
		ConfiguredStrategy: configured,
		EffectiveStrategy:  effective,
		Enabled:            effective != "",
		Available:          status.Available,
		DefaultStrategy:    status.Default,
		Strategies:         status.Strategies,
		Path:               req.Path,
	}
	switch {
	case err != nil:
		report.Status = "unavailable"
		report.Error = err.Error()
	case effective != "":
		report.Status = "enabled"
	default:
		report.Status = "disabled"
	}
	return report
}

func renderSandboxToggleReport(out io.Writer, report sandboxToggleReport) {
	fmt.Fprintln(out, "Sandbox Toggle")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  OS               %s\n", report.OS)
	fmt.Fprintf(out, "  Configured       %s\n", emptyAsNone(report.ConfiguredStrategy))
	fmt.Fprintf(out, "  Effective        %s\n", emptyAsNone(report.EffectiveStrategy))
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Available        %t\n", report.Available)
	if report.DefaultStrategy != "" {
		fmt.Fprintf(out, "  Default          %s\n", report.DefaultStrategy)
	}
	if len(report.Strategies) > 0 {
		fmt.Fprintf(out, "  Strategies       %s\n", strings.Join(report.Strategies, ", "))
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Error != "" {
		fmt.Fprintf(out, "  Error            %s\n", report.Error)
	}
}

type heapDumpRequest struct {
	Path   string
	Format string
	GC     bool
}

type heapDumpReport struct {
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	GC        bool   `json:"gc"`
	WrittenAt string `json:"written_at"`
}

func (a *App) HeapDump(args []string) error {
	req, err := parseHeapDumpArgs(args)
	if err != nil {
		return err
	}
	path := req.Path
	if strings.TrimSpace(path) == "" {
		path = a.defaultHeapDumpPath(time.Now().UTC())
	} else {
		path = a.resolveOutputPath(path)
	}
	if req.GC {
		runtime.GC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writeErr := pprof.WriteHeapProfile(file)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	report := heapDumpReport{
		Kind:      "heapdump",
		Status:    "ok",
		Path:      path,
		Bytes:     stat.Size(),
		GC:        req.GC,
		WrittenAt: time.Now().UTC().Format(time.RFC3339),
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderHeapDumpReport(a.Out, report)
	return nil
}

func parseHeapDumpArgs(args []string) (heapDumpRequest, error) {
	req := heapDumpRequest{Format: "text", GC: true}
	pathSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("heapdump output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--gc":
			req.GC = true
		case arg == "--no-gc":
			req.GC = false
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown heapdump flag %q", arg)
		default:
			if pathSet {
				return req, fmt.Errorf("unexpected heapdump argument %q", arg)
			}
			req.Path = arg
			pathSet = true
		}
	}
	if err := validateTextOrJSON(req.Format, "heapdump"); err != nil {
		return req, err
	}
	return req, nil
}

func (a *App) defaultHeapDumpPath(now time.Time) string {
	name := "heap-" + now.Format("20060102-150405") + ".pprof"
	return a.resolveOutputPath(filepath.Join(".codog", "heap", name))
}

func renderHeapDumpReport(out io.Writer, report heapDumpReport) {
	fmt.Fprintln(out, "Heap Dump")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	fmt.Fprintf(out, "  GC               %t\n", report.GC)
	fmt.Fprintf(out, "  Written at       %s\n", report.WrittenAt)
}

func (a *App) Init(args []string) error {
	return initProject(a.Out, a.Workspace, args, func(report projectinit.Report) error {
		return a.runSetupHook(context.Background(), "init", report.Status)
	})
}

type initVerifiersRequest struct {
	Format    string
	Target    string
	Workspace string
	Force     bool
	DryRun    bool
}

func (a *App) InitVerifiers(args []string) error {
	req, err := parseInitVerifiersArgs(args)
	if err != nil {
		return err
	}
	workspace := a.Workspace
	if req.Workspace != "" {
		workspace = a.resolveOutputPath(req.Workspace)
	}
	report, err := verifiers.Initialize(verifiers.Options{
		Workspace: workspace,
		Target:    req.Target,
		Force:     req.Force,
		DryRun:    req.DryRun,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, verifiers.RenderText(report))
	return nil
}

func parseInitVerifiersArgs(args []string) (initVerifiersRequest, error) {
	req := initVerifiersRequest{Format: "text", Target: "claude"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("init-verifiers output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("init-verifiers target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--workspace":
			index++
			if index >= len(args) {
				return req, errors.New("init-verifiers workspace is required")
			}
			req.Workspace = args[index]
		case strings.HasPrefix(arg, "--workspace="):
			req.Workspace = strings.TrimPrefix(arg, "--workspace=")
		case arg == "--force":
			req.Force = true
		case arg == "--dry-run":
			req.DryRun = true
		default:
			return req, fmt.Errorf("unknown init-verifiers option %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "init-verifiers"); err != nil {
		return req, err
	}
	return req, nil
}

func (a *App) State(args []string) error {
	return renderWorkerState(a.Out, a.Workspace, args)
}

func (a *App) Memory(args []string) error {
	return renderMemoryCommand(a.Out, a.Workspace, args)
}

type projectReport struct {
	Kind        string           `json:"kind"`
	Workspace   string           `json:"workspace"`
	Name        string           `json:"name"`
	Git         projectGitReport `json:"git"`
	GoModule    string           `json:"go_module,omitempty"`
	CodogDir    string           `json:"codog_dir,omitempty"`
	MemoryFiles []memory.Summary `json:"memory_files,omitempty"`
}

type projectGitReport struct {
	Available bool   `json:"available"`
	Root      string `json:"root,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Head      string `json:"head,omitempty"`
	Error     string `json:"error,omitempty"`
}

type envReport struct {
	Kind      string     `json:"kind"`
	Total     int        `json:"total"`
	Redacted  int        `json:"redacted"`
	Variables []envValue `json:"variables"`
}

type envValue struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Redacted bool   `json:"redacted,omitempty"`
}

func (a *App) Project(args []string) error {
	format, err := parseSimpleOutputFormat("project", args)
	if err != nil {
		return err
	}
	report := a.buildProjectReport()
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderProjectReport(a.Out, report)
	return nil
}

func (a *App) buildProjectReport() projectReport {
	workspace := a.Workspace
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	workspace = filepath.Clean(workspace)
	report := projectReport{
		Kind:      "project",
		Workspace: workspace,
		Name:      filepath.Base(workspace),
	}
	if root, err := gitops.Root(workspace); err == nil {
		report.Git.Available = true
		report.Git.Root = root
		if branch, err := gitops.Branch(workspace); err == nil {
			report.Git.Branch = branch
		}
		if head, err := gitops.Head(workspace); err == nil {
			report.Git.Head = head
		}
	} else {
		report.Git.Error = err.Error()
	}
	if path := findUp(workspace, "go.mod"); path != "" {
		report.GoModule = path
	}
	if path := filepath.Join(workspace, ".codog"); dirExists(path) {
		report.CodogDir = path
	}
	if files, err := memory.Discover(workspace); err == nil {
		report.MemoryFiles = memory.Summaries(files)
	}
	return report
}

func renderProjectReport(out io.Writer, report projectReport) {
	fmt.Fprintln(out, "Project")
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  Name             %s\n", report.Name)
	if report.Git.Available {
		fmt.Fprintf(out, "  Git root         %s\n", report.Git.Root)
		fmt.Fprintf(out, "  Git branch       %s\n", emptyAsNone(report.Git.Branch))
		fmt.Fprintf(out, "  Git head         %s\n", emptyAsNone(report.Git.Head))
	} else {
		fmt.Fprintf(out, "  Git              unavailable: %s\n", report.Git.Error)
	}
	fmt.Fprintf(out, "  Go module        %s\n", emptyAsNone(report.GoModule))
	fmt.Fprintf(out, "  Codog dir        %s\n", emptyAsNone(report.CodogDir))
	fmt.Fprintf(out, "  Memory files     %d\n", len(report.MemoryFiles))
	for index, file := range report.MemoryFiles {
		fmt.Fprintf(out, "  %d. %s\n", index+1, file.Path)
	}
}

func (a *App) Env(args []string) error {
	format, err := parseSimpleOutputFormat("env", args)
	if err != nil {
		return err
	}
	report := buildEnvReport(os.Environ())
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderEnvReport(a.Out, report)
	return nil
}

func buildEnvReport(environ []string) envReport {
	var variables []envValue
	redacted := 0
	for _, item := range environ {
		name, value, ok := strings.Cut(item, "=")
		if !ok || name == "" {
			continue
		}
		entry := envValue{Name: name, Value: value}
		if isSensitiveEnvName(name) {
			entry.Value = "[redacted]"
			entry.Redacted = true
			redacted++
		}
		variables = append(variables, entry)
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	return envReport{
		Kind:      "env",
		Total:     len(variables),
		Redacted:  redacted,
		Variables: variables,
	}
}

func renderEnvReport(out io.Writer, report envReport) {
	fmt.Fprintln(out, "Environment")
	fmt.Fprintf(out, "  Variables        %d\n", report.Total)
	fmt.Fprintf(out, "  Redacted         %d\n", report.Redacted)
	fmt.Fprintln(out)
	for _, variable := range report.Variables {
		fmt.Fprintf(out, "%s=%s\n", variable.Name, variable.Value)
	}
}

func isSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	sensitive := []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "AUTH"}
	for _, marker := range sensitive {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

type hooksRequest struct {
	Format           string
	Action           string
	Event            string
	Tool             string
	Input            string
	Output           string
	IsError          bool
	TimeoutMS        int
	NotificationType string
	Title            string
	AgentID          string
	AgentType        string
	TranscriptPath   string
	LastAssistant    string
	WorktreeID       string
	WorktreePath     string
	Ref              string
	OldCWD           string
	NewCWD           string
	TaskID           string
	TaskKind         string
	TaskStatus       string
	FilePath         string
	Operation        string
	MemoryType       string
	LoadReason       string
	Globs            []string
	TriggerFilePath  string
	ParentFilePath   string
	StopHookActive   bool
	Reason           string
}

type hooksListReport struct {
	Kind                       string               `json:"kind"`
	Action                     string               `json:"action"`
	Status                     string               `json:"status"`
	PreToolUse                 []string             `json:"pre_tool_use"`
	PostToolUse                []string             `json:"post_tool_use"`
	PostToolUseFailure         []string             `json:"post_tool_use_failure"`
	PermissionRequest          []string             `json:"permission_request"`
	PermissionDenied           []string             `json:"permission_denied"`
	UserPromptSubmit           []string             `json:"user_prompt_submit"`
	SessionStart               []string             `json:"session_start"`
	SessionEnd                 []string             `json:"session_end"`
	Setup                      []string             `json:"setup"`
	Stop                       []string             `json:"stop"`
	StopFailure                []string             `json:"stop_failure"`
	PreCompact                 []string             `json:"pre_compact"`
	PostCompact                []string             `json:"post_compact"`
	Notification               []string             `json:"notification"`
	SubagentStart              []string             `json:"subagent_start"`
	SubagentStop               []string             `json:"subagent_stop"`
	WorktreeCreate             []string             `json:"worktree_create"`
	WorktreeRemove             []string             `json:"worktree_remove"`
	CwdChanged                 []string             `json:"cwd_changed"`
	TaskCreated                []string             `json:"task_created"`
	TaskCompleted              []string             `json:"task_completed"`
	InstructionsLoaded         []string             `json:"instructions_loaded"`
	FileChanged                []string             `json:"file_changed"`
	PreToolUseCommands         []hookCommandSummary `json:"pre_tool_use_commands,omitempty"`
	PostToolUseCommands        []hookCommandSummary `json:"post_tool_use_commands,omitempty"`
	PostToolUseFailureCommands []hookCommandSummary `json:"post_tool_use_failure_commands,omitempty"`
	PermissionRequestCommands  []hookCommandSummary `json:"permission_request_commands,omitempty"`
	PermissionDeniedCommands   []hookCommandSummary `json:"permission_denied_commands,omitempty"`
	UserPromptSubmitCommands   []hookCommandSummary `json:"user_prompt_submit_commands,omitempty"`
	SessionStartCommands       []hookCommandSummary `json:"session_start_commands,omitempty"`
	SessionEndCommands         []hookCommandSummary `json:"session_end_commands,omitempty"`
	SetupCommands              []hookCommandSummary `json:"setup_commands,omitempty"`
	StopCommands               []hookCommandSummary `json:"stop_commands,omitempty"`
	StopFailureCommands        []hookCommandSummary `json:"stop_failure_commands,omitempty"`
	PreCompactCommands         []hookCommandSummary `json:"pre_compact_commands,omitempty"`
	PostCompactCommands        []hookCommandSummary `json:"post_compact_commands,omitempty"`
	NotificationCommands       []hookCommandSummary `json:"notification_commands,omitempty"`
	SubagentStartCommands      []hookCommandSummary `json:"subagent_start_commands,omitempty"`
	SubagentStopCommands       []hookCommandSummary `json:"subagent_stop_commands,omitempty"`
	WorktreeCreateCommands     []hookCommandSummary `json:"worktree_create_commands,omitempty"`
	WorktreeRemoveCommands     []hookCommandSummary `json:"worktree_remove_commands,omitempty"`
	CwdChangedCommands         []hookCommandSummary `json:"cwd_changed_commands,omitempty"`
	TaskCreatedCommands        []hookCommandSummary `json:"task_created_commands,omitempty"`
	TaskCompletedCommands      []hookCommandSummary `json:"task_completed_commands,omitempty"`
	InstructionsLoadedCommands []hookCommandSummary `json:"instructions_loaded_commands,omitempty"`
	FileChangedCommands        []hookCommandSummary `json:"file_changed_commands,omitempty"`
}

type hookCommandSummary struct {
	Matcher string `json:"matcher,omitempty"`
	Type    string `json:"type,omitempty"`
	If      string `json:"if,omitempty"`
	Command string `json:"command"`
}

func (a *App) Hooks(ctx context.Context, args []string) error {
	req, err := parseHooksArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "list":
		report := hooksListReport{
			Kind:                       "hooks",
			Action:                     "list",
			Status:                     "ok",
			PreToolUse:                 append([]string(nil), a.Config.Hooks.PreToolUse...),
			PostToolUse:                append([]string(nil), a.Config.Hooks.PostToolUse...),
			PostToolUseFailure:         append([]string(nil), a.Config.Hooks.PostToolUseFailure...),
			PermissionRequest:          append([]string(nil), a.Config.Hooks.PermissionRequest...),
			PermissionDenied:           append([]string(nil), a.Config.Hooks.PermissionDenied...),
			UserPromptSubmit:           append([]string(nil), a.Config.Hooks.UserPromptSubmit...),
			SessionStart:               append([]string(nil), a.Config.Hooks.SessionStart...),
			SessionEnd:                 append([]string(nil), a.Config.Hooks.SessionEnd...),
			Setup:                      append([]string(nil), a.Config.Hooks.Setup...),
			Stop:                       append([]string(nil), a.Config.Hooks.Stop...),
			StopFailure:                append([]string(nil), a.Config.Hooks.StopFailure...),
			PreCompact:                 append([]string(nil), a.Config.Hooks.PreCompact...),
			PostCompact:                append([]string(nil), a.Config.Hooks.PostCompact...),
			Notification:               append([]string(nil), a.Config.Hooks.Notification...),
			SubagentStart:              append([]string(nil), a.Config.Hooks.SubagentStart...),
			SubagentStop:               append([]string(nil), a.Config.Hooks.SubagentStop...),
			WorktreeCreate:             append([]string(nil), a.Config.Hooks.WorktreeCreate...),
			WorktreeRemove:             append([]string(nil), a.Config.Hooks.WorktreeRemove...),
			CwdChanged:                 append([]string(nil), a.Config.Hooks.CwdChanged...),
			TaskCreated:                append([]string(nil), a.Config.Hooks.TaskCreated...),
			TaskCompleted:              append([]string(nil), a.Config.Hooks.TaskCompleted...),
			InstructionsLoaded:         append([]string(nil), a.Config.Hooks.InstructionsLoaded...),
			FileChanged:                append([]string(nil), a.Config.Hooks.FileChanged...),
			PreToolUseCommands:         hookCommandsForList(a.Config.Hooks.PreToolUseCommands, a.Config.Hooks.PreToolUse),
			PostToolUseCommands:        hookCommandsForList(a.Config.Hooks.PostToolUseCommands, a.Config.Hooks.PostToolUse),
			PostToolUseFailureCommands: hookCommandsForList(a.Config.Hooks.PostToolUseFailureCommands, a.Config.Hooks.PostToolUseFailure),
			PermissionRequestCommands:  hookCommandsForList(a.Config.Hooks.PermissionRequestCommands, a.Config.Hooks.PermissionRequest),
			PermissionDeniedCommands:   hookCommandsForList(a.Config.Hooks.PermissionDeniedCommands, a.Config.Hooks.PermissionDenied),
			UserPromptSubmitCommands:   hookCommandsForList(a.Config.Hooks.UserPromptSubmitCommands, a.Config.Hooks.UserPromptSubmit),
			SessionStartCommands:       hookCommandsForList(a.Config.Hooks.SessionStartCommands, a.Config.Hooks.SessionStart),
			SessionEndCommands:         hookCommandsForList(a.Config.Hooks.SessionEndCommands, a.Config.Hooks.SessionEnd),
			SetupCommands:              hookCommandsForList(a.Config.Hooks.SetupCommands, a.Config.Hooks.Setup),
			StopCommands:               hookCommandsForList(a.Config.Hooks.StopCommands, a.Config.Hooks.Stop),
			StopFailureCommands:        hookCommandsForList(a.Config.Hooks.StopFailureCommands, a.Config.Hooks.StopFailure),
			PreCompactCommands:         hookCommandsForList(a.Config.Hooks.PreCompactCommands, a.Config.Hooks.PreCompact),
			PostCompactCommands:        hookCommandsForList(a.Config.Hooks.PostCompactCommands, a.Config.Hooks.PostCompact),
			NotificationCommands:       hookCommandsForList(a.Config.Hooks.NotificationCommands, a.Config.Hooks.Notification),
			SubagentStartCommands:      hookCommandsForList(a.Config.Hooks.SubagentStartCommands, a.Config.Hooks.SubagentStart),
			SubagentStopCommands:       hookCommandsForList(a.Config.Hooks.SubagentStopCommands, a.Config.Hooks.SubagentStop),
			WorktreeCreateCommands:     hookCommandsForList(a.Config.Hooks.WorktreeCreateCommands, a.Config.Hooks.WorktreeCreate),
			WorktreeRemoveCommands:     hookCommandsForList(a.Config.Hooks.WorktreeRemoveCommands, a.Config.Hooks.WorktreeRemove),
			CwdChangedCommands:         hookCommandsForList(a.Config.Hooks.CwdChangedCommands, a.Config.Hooks.CwdChanged),
			TaskCreatedCommands:        hookCommandsForList(a.Config.Hooks.TaskCreatedCommands, a.Config.Hooks.TaskCreated),
			TaskCompletedCommands:      hookCommandsForList(a.Config.Hooks.TaskCompletedCommands, a.Config.Hooks.TaskCompleted),
			InstructionsLoadedCommands: hookCommandsForList(a.Config.Hooks.InstructionsLoadedCommands, a.Config.Hooks.InstructionsLoaded),
			FileChangedCommands:        hookCommandsForList(a.Config.Hooks.FileChangedCommands, a.Config.Hooks.FileChanged),
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderHooksList(a.Out, report)
		return nil
	case "run":
		payload := hooks.Payload{
			Event:            req.Event,
			Tool:             req.Tool,
			ToolName:         req.Tool,
			ToolInput:        json.RawMessage(req.Input),
			Input:            req.Input,
			Output:           req.Output,
			IsError:          req.IsError,
			Reason:           req.Reason,
			Message:          req.Input,
			Title:            req.Title,
			NotificationType: req.NotificationType,
			AgentID:          req.AgentID,
			AgentType:        req.AgentType,
			TranscriptPath:   req.TranscriptPath,
			LastAssistant:    req.LastAssistant,
			WorktreeID:       req.WorktreeID,
			WorktreePath:     req.WorktreePath,
			Ref:              req.Ref,
			OldCWD:           req.OldCWD,
			NewCWD:           req.NewCWD,
			TaskID:           req.TaskID,
			TaskKind:         req.TaskKind,
			TaskStatus:       req.TaskStatus,
			FilePath:         req.FilePath,
			Operation:        req.Operation,
			MemoryType:       req.MemoryType,
			LoadReason:       req.LoadReason,
			Globs:            append([]string(nil), req.Globs...),
			TriggerFilePath:  req.TriggerFilePath,
			ParentFilePath:   req.ParentFilePath,
			StopHookActive:   req.StopHookActive,
		}
		if req.Event == "notification" {
			payload.NotificationType = firstNonEmpty(req.NotificationType, req.Tool, "generic")
			payload.Tool = payload.NotificationType
			payload.Message = req.Input
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.Reason = ""
			payload.ToolName = ""
			payload.ToolInput = nil
		} else if req.Event == "subagent_start" || req.Event == "subagent_stop" {
			payload.AgentType = firstNonEmpty(req.AgentType, req.Tool, "general")
			payload.Tool = payload.AgentType
			payload.Input = req.Input
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.Reason = ""
			payload.ToolName = ""
			payload.ToolInput = nil
		} else if req.Event == "permission_request" || req.Event == "permission_denied" {
			payload.ToolName = req.Tool
			payload.Tool = req.Tool
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
		} else if req.Event == "session_end" || req.Event == "setup" {
			payload.Tool = ""
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.ToolName = ""
			payload.ToolInput = nil
			if req.Event == "setup" {
				payload.Reason = ""
			}
		} else if req.Event == "stop_failure" {
			payload.Tool = ""
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.ToolName = ""
			payload.ToolInput = nil
			payload.IsError = true
		} else if req.Event == "worktree_create" || req.Event == "worktree_remove" {
			payload.WorktreeID = firstNonEmpty(req.WorktreeID, req.Tool)
			payload.Tool = payload.WorktreeID
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.ToolName = ""
			payload.ToolInput = nil
			if req.Event == "worktree_create" {
				payload.Reason = ""
			}
		} else if req.Event == "cwd_changed" {
			payload.OldCWD = req.OldCWD
			payload.NewCWD = firstNonEmpty(req.NewCWD, req.Tool)
			payload.Tool = payload.NewCWD
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.ToolName = ""
			payload.ToolInput = nil
			payload.Reason = ""
		} else if req.Event == "task_created" || req.Event == "task_completed" {
			payload.TaskID = firstNonEmpty(req.TaskID, req.Tool)
			payload.TaskKind = firstNonEmpty(req.TaskKind, req.AgentType, "background")
			payload.Tool = firstNonEmpty(payload.TaskKind, payload.TaskID)
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.ToolName = ""
			payload.ToolInput = nil
			if req.Event == "task_created" {
				payload.Reason = ""
			}
		} else if req.Event == "file_changed" {
			payload.Operation = firstNonEmpty(req.Operation, req.Tool, "write_file")
			payload.Tool = payload.Operation
			payload.ToolName = payload.Operation
			payload.FilePath = req.FilePath
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.Reason = ""
		} else if req.Event == "instructions_loaded" {
			payload.LoadReason = firstNonEmpty(req.LoadReason, req.Tool, "session_start")
			payload.Tool = payload.LoadReason
			payload.FilePath = req.FilePath
			payload.MemoryType = firstNonEmpty(req.MemoryType, "Project")
			payload.Globs = append([]string(nil), req.Globs...)
			payload.TriggerFilePath = req.TriggerFilePath
			payload.ParentFilePath = req.ParentFilePath
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.Reason = ""
			payload.ToolName = ""
			payload.ToolInput = nil
		} else {
			payload.Message = ""
			payload.Title = ""
			payload.NotificationType = ""
			payload.AgentID = ""
			payload.AgentType = ""
			payload.TranscriptPath = ""
			payload.LastAssistant = ""
			payload.StopHookActive = false
			payload.Reason = ""
			payload.ToolName = ""
			payload.ToolInput = nil
			payload.WorktreeID = ""
			payload.WorktreePath = ""
			payload.Ref = ""
			payload.OldCWD = ""
			payload.NewCWD = ""
			payload.TaskID = ""
			payload.TaskKind = ""
			payload.TaskStatus = ""
			payload.FilePath = ""
			payload.Operation = ""
			payload.MemoryType = ""
			payload.LoadReason = ""
			payload.Globs = nil
			payload.TriggerFilePath = ""
			payload.ParentFilePath = ""
		}
		if req.Event != "worktree_create" && req.Event != "worktree_remove" {
			payload.WorktreeID = ""
			payload.WorktreePath = ""
			payload.Ref = ""
		}
		if req.Event != "cwd_changed" {
			payload.OldCWD = ""
			payload.NewCWD = ""
		}
		if req.Event != "task_created" && req.Event != "task_completed" {
			payload.TaskID = ""
			payload.TaskKind = ""
			payload.TaskStatus = ""
		}
		if req.Event != "file_changed" && req.Event != "instructions_loaded" {
			payload.FilePath = ""
		}
		if req.Event != "file_changed" {
			payload.Operation = ""
		}
		if req.Event != "instructions_loaded" {
			payload.MemoryType = ""
			payload.LoadReason = ""
			payload.Globs = nil
			payload.TriggerFilePath = ""
			payload.ParentFilePath = ""
		}
		hookList := hooks.HooksForPayload(a.Config.Hooks, payload)
		timeout := time.Duration(req.TimeoutMS) * time.Millisecond
		runner := hooks.Runner{
			Config:       a.Config.Hooks,
			Workspace:    a.Workspace,
			ConfigHome:   a.Config.ConfigHome,
			Timeout:      timeout,
			PromptRunner: a.hookPromptRunner(a.effectiveConfig()),
		}
		report, runErr := runner.RunHooks(ctx, hookList, payload)
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
		} else {
			renderHooksRun(a.Out, report)
		}
		return runErr
	default:
		return fmt.Errorf("unknown hooks action %q", req.Action)
	}
}

func parseHooksArgs(args []string) (hooksRequest, error) {
	req := hooksRequest{Format: "text", Action: "list", Event: "pre_tool_use", Tool: "bash", Input: `{}`, TimeoutMS: 30000}
	var positionals []string
	toolSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("hooks output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--tool":
			i++
			if i >= len(args) {
				return req, errors.New("hooks tool is required")
			}
			req.Tool = args[i]
			toolSet = true
		case strings.HasPrefix(arg, "--tool="):
			req.Tool = strings.TrimPrefix(arg, "--tool=")
			toolSet = true
		case arg == "--input":
			i++
			if i >= len(args) {
				return req, errors.New("hooks input is required")
			}
			req.Input = args[i]
		case strings.HasPrefix(arg, "--input="):
			req.Input = strings.TrimPrefix(arg, "--input=")
		case arg == "--output":
			i++
			if i >= len(args) {
				return req, errors.New("hooks output is required")
			}
			req.Output = args[i]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--notification-type":
			i++
			if i >= len(args) {
				return req, errors.New("hooks notification type is required")
			}
			req.NotificationType = args[i]
		case strings.HasPrefix(arg, "--notification-type="):
			req.NotificationType = strings.TrimPrefix(arg, "--notification-type=")
		case arg == "--title":
			i++
			if i >= len(args) {
				return req, errors.New("hooks title is required")
			}
			req.Title = args[i]
		case strings.HasPrefix(arg, "--title="):
			req.Title = strings.TrimPrefix(arg, "--title=")
		case arg == "--agent-id":
			i++
			if i >= len(args) {
				return req, errors.New("hooks agent id is required")
			}
			req.AgentID = args[i]
		case strings.HasPrefix(arg, "--agent-id="):
			req.AgentID = strings.TrimPrefix(arg, "--agent-id=")
		case arg == "--agent-type":
			i++
			if i >= len(args) {
				return req, errors.New("hooks agent type is required")
			}
			req.AgentType = args[i]
		case strings.HasPrefix(arg, "--agent-type="):
			req.AgentType = strings.TrimPrefix(arg, "--agent-type=")
		case arg == "--agent-transcript-path":
			i++
			if i >= len(args) {
				return req, errors.New("hooks agent transcript path is required")
			}
			req.TranscriptPath = args[i]
		case strings.HasPrefix(arg, "--agent-transcript-path="):
			req.TranscriptPath = strings.TrimPrefix(arg, "--agent-transcript-path=")
		case arg == "--last-assistant-message":
			i++
			if i >= len(args) {
				return req, errors.New("hooks last assistant message is required")
			}
			req.LastAssistant = args[i]
		case strings.HasPrefix(arg, "--last-assistant-message="):
			req.LastAssistant = strings.TrimPrefix(arg, "--last-assistant-message=")
		case arg == "--worktree-id":
			i++
			if i >= len(args) {
				return req, errors.New("hooks worktree id is required")
			}
			req.WorktreeID = args[i]
		case strings.HasPrefix(arg, "--worktree-id="):
			req.WorktreeID = strings.TrimPrefix(arg, "--worktree-id=")
		case arg == "--worktree-path":
			i++
			if i >= len(args) {
				return req, errors.New("hooks worktree path is required")
			}
			req.WorktreePath = args[i]
		case strings.HasPrefix(arg, "--worktree-path="):
			req.WorktreePath = strings.TrimPrefix(arg, "--worktree-path=")
		case arg == "--ref":
			i++
			if i >= len(args) {
				return req, errors.New("hooks ref is required")
			}
			req.Ref = args[i]
		case strings.HasPrefix(arg, "--ref="):
			req.Ref = strings.TrimPrefix(arg, "--ref=")
		case arg == "--old-cwd":
			i++
			if i >= len(args) {
				return req, errors.New("hooks old cwd is required")
			}
			req.OldCWD = args[i]
		case strings.HasPrefix(arg, "--old-cwd="):
			req.OldCWD = strings.TrimPrefix(arg, "--old-cwd=")
		case arg == "--new-cwd":
			i++
			if i >= len(args) {
				return req, errors.New("hooks new cwd is required")
			}
			req.NewCWD = args[i]
		case strings.HasPrefix(arg, "--new-cwd="):
			req.NewCWD = strings.TrimPrefix(arg, "--new-cwd=")
		case arg == "--path" || arg == "--file-path":
			i++
			if i >= len(args) {
				return req, errors.New("hooks file path is required")
			}
			req.FilePath = args[i]
		case strings.HasPrefix(arg, "--path="):
			req.FilePath = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "--file-path="):
			req.FilePath = strings.TrimPrefix(arg, "--file-path=")
		case arg == "--operation":
			i++
			if i >= len(args) {
				return req, errors.New("hooks operation is required")
			}
			req.Operation = args[i]
		case strings.HasPrefix(arg, "--operation="):
			req.Operation = strings.TrimPrefix(arg, "--operation=")
		case arg == "--memory-type":
			i++
			if i >= len(args) {
				return req, errors.New("hooks memory type is required")
			}
			req.MemoryType = args[i]
		case strings.HasPrefix(arg, "--memory-type="):
			req.MemoryType = strings.TrimPrefix(arg, "--memory-type=")
		case arg == "--load-reason":
			i++
			if i >= len(args) {
				return req, errors.New("hooks load reason is required")
			}
			req.LoadReason = args[i]
		case strings.HasPrefix(arg, "--load-reason="):
			req.LoadReason = strings.TrimPrefix(arg, "--load-reason=")
		case arg == "--glob":
			i++
			if i >= len(args) {
				return req, errors.New("hooks glob is required")
			}
			req.Globs = append(req.Globs, args[i])
		case strings.HasPrefix(arg, "--glob="):
			req.Globs = append(req.Globs, strings.TrimPrefix(arg, "--glob="))
		case arg == "--trigger-file-path":
			i++
			if i >= len(args) {
				return req, errors.New("hooks trigger file path is required")
			}
			req.TriggerFilePath = args[i]
		case strings.HasPrefix(arg, "--trigger-file-path="):
			req.TriggerFilePath = strings.TrimPrefix(arg, "--trigger-file-path=")
		case arg == "--parent-file-path":
			i++
			if i >= len(args) {
				return req, errors.New("hooks parent file path is required")
			}
			req.ParentFilePath = args[i]
		case strings.HasPrefix(arg, "--parent-file-path="):
			req.ParentFilePath = strings.TrimPrefix(arg, "--parent-file-path=")
		case arg == "--task-id":
			i++
			if i >= len(args) {
				return req, errors.New("hooks task id is required")
			}
			req.TaskID = args[i]
		case strings.HasPrefix(arg, "--task-id="):
			req.TaskID = strings.TrimPrefix(arg, "--task-id=")
		case arg == "--task-kind":
			i++
			if i >= len(args) {
				return req, errors.New("hooks task kind is required")
			}
			req.TaskKind = args[i]
		case strings.HasPrefix(arg, "--task-kind="):
			req.TaskKind = strings.TrimPrefix(arg, "--task-kind=")
		case arg == "--task-status":
			i++
			if i >= len(args) {
				return req, errors.New("hooks task status is required")
			}
			req.TaskStatus = args[i]
		case strings.HasPrefix(arg, "--task-status="):
			req.TaskStatus = strings.TrimPrefix(arg, "--task-status=")
		case arg == "--stop-hook-active":
			req.StopHookActive = true
		case arg == "--reason":
			i++
			if i >= len(args) {
				return req, errors.New("hooks reason is required")
			}
			req.Reason = args[i]
		case strings.HasPrefix(arg, "--reason="):
			req.Reason = strings.TrimPrefix(arg, "--reason=")
		case arg == "--error":
			req.IsError = true
		case arg == "--timeout-ms":
			i++
			if i >= len(args) {
				return req, errors.New("hooks timeout is required")
			}
			timeout, err := strconv.Atoi(args[i])
			if err != nil || timeout < 0 {
				return req, errors.New("hooks timeout must be a non-negative integer")
			}
			req.TimeoutMS = timeout
		case strings.HasPrefix(arg, "--timeout-ms="):
			timeout, err := strconv.Atoi(strings.TrimPrefix(arg, "--timeout-ms="))
			if err != nil || timeout < 0 {
				return req, errors.New("hooks timeout must be a non-negative integer")
			}
			req.TimeoutMS = timeout
		default:
			positionals = append(positionals, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown hooks output format %q", req.Format)
	}
	if len(positionals) == 0 {
		return req, nil
	}
	action := strings.ToLower(positionals[0])
	switch action {
	case "list", "show":
		req.Action = "list"
	case "run", "test":
		req.Action = "run"
		if len(positionals) > 1 {
			event, err := normalizeHookEvent(positionals[1])
			if err != nil {
				return req, err
			}
			req.Event = event
		}
	default:
		return req, fmt.Errorf("unknown hooks action %q", positionals[0])
	}
	if !toolSet && (req.Event == "user_prompt_submit" || req.Event == "session_start" || req.Event == "stop" || req.Event == "pre_compact" || req.Event == "post_compact" || req.Event == "notification" || req.Event == "subagent_start" || req.Event == "subagent_stop" || req.Event == "file_changed" || req.Event == "instructions_loaded") {
		req.Tool = ""
	}
	if req.Event == "notification" && strings.TrimSpace(req.NotificationType) == "" && strings.TrimSpace(req.Tool) != "" {
		req.NotificationType = req.Tool
	}
	if (req.Event == "subagent_start" || req.Event == "subagent_stop") && strings.TrimSpace(req.AgentType) == "" && strings.TrimSpace(req.Tool) != "" {
		req.AgentType = req.Tool
	}
	if req.Event == "file_changed" && strings.TrimSpace(req.Operation) == "" && strings.TrimSpace(req.Tool) != "" {
		req.Operation = req.Tool
	}
	if req.Event == "instructions_loaded" && strings.TrimSpace(req.LoadReason) == "" && strings.TrimSpace(req.Tool) != "" {
		req.LoadReason = req.Tool
	}
	return req, nil
}

func normalizeHookEvent(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pre", "pre_tool_use", "pre-tool-use":
		return "pre_tool_use", nil
	case "post", "post_tool_use", "post-tool-use":
		return "post_tool_use", nil
	case "post-failure", "postfailure", "post_tool_use_failure", "post-tool-use-failure":
		return "post_tool_use_failure", nil
	case "permission-request", "permissionrequest", "permission_request":
		return "permission_request", nil
	case "permission-denied", "permissiondenied", "permission_denied":
		return "permission_denied", nil
	case "prompt", "userpromptsubmit", "user_prompt_submit", "user-prompt-submit":
		return "user_prompt_submit", nil
	case "session", "sessionstart", "session_start", "session-start":
		return "session_start", nil
	case "session-end", "sessionend", "session_end":
		return "session_end", nil
	case "setup":
		return "setup", nil
	case "stop":
		return "stop", nil
	case "stop-failure", "stopfailure", "stop_failure":
		return "stop_failure", nil
	case "compact", "precompact", "pre_compact", "pre-compact":
		return "pre_compact", nil
	case "postcompact", "post_compact", "post-compact":
		return "post_compact", nil
	case "notification", "notify":
		return "notification", nil
	case "subagent-start", "subagentstart", "subagent_start":
		return "subagent_start", nil
	case "subagent-stop", "subagentstop", "subagent_stop":
		return "subagent_stop", nil
	case "worktree-create", "worktreecreate", "worktree_create":
		return "worktree_create", nil
	case "worktree-remove", "worktreeremove", "worktree_remove":
		return "worktree_remove", nil
	case "cwd-changed", "cwdchanged", "cwd_changed":
		return "cwd_changed", nil
	case "task-created", "taskcreated", "task_created":
		return "task_created", nil
	case "task-completed", "taskcompleted", "task_completed":
		return "task_completed", nil
	case "instructions-loaded", "instructionsloaded", "instructions_loaded":
		return "instructions_loaded", nil
	case "file-changed", "filechanged", "file_changed":
		return "file_changed", nil
	default:
		return "", fmt.Errorf("unknown hook event %q", value)
	}
}

func summarizeHookCommands(commands []config.HookCommand) []hookCommandSummary {
	out := make([]hookCommandSummary, 0, len(commands))
	for _, command := range commands {
		display := config.HookCommandDisplay(command)
		if display == "" {
			continue
		}
		out = append(out, hookCommandSummary{
			Matcher: strings.TrimSpace(command.Matcher),
			Type:    strings.TrimSpace(command.Type),
			If:      strings.TrimSpace(command.If),
			Command: display,
		})
	}
	return out
}

func hookCommandsForList(commands []config.HookCommand, legacy []string) []hookCommandSummary {
	summaries := summarizeHookCommands(commands)
	if len(summaries) != 0 || len(legacy) == 0 {
		return summaries
	}
	out := make([]hookCommandSummary, 0, len(legacy))
	for _, command := range legacy {
		command = strings.TrimSpace(command)
		if command != "" {
			out = append(out, hookCommandSummary{Command: command})
		}
	}
	return out
}

func renderHooksList(out io.Writer, report hooksListReport) {
	fmt.Fprintln(out, "Hooks")
	fmt.Fprintf(out, "  Pre tool use     %d\n", len(report.PreToolUse))
	for _, command := range report.PreToolUseCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Post tool use    %d\n", len(report.PostToolUse))
	for _, command := range report.PostToolUseCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Post tool failure %d\n", len(report.PostToolUseFailure))
	for _, command := range report.PostToolUseFailureCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Permission request %d\n", len(report.PermissionRequest))
	for _, command := range report.PermissionRequestCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Permission denied %d\n", len(report.PermissionDenied))
	for _, command := range report.PermissionDeniedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  User prompt submit %d\n", len(report.UserPromptSubmit))
	for _, command := range report.UserPromptSubmitCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Session start   %d\n", len(report.SessionStart))
	for _, command := range report.SessionStartCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Session end     %d\n", len(report.SessionEnd))
	for _, command := range report.SessionEndCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Setup           %d\n", len(report.Setup))
	for _, command := range report.SetupCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Stop             %d\n", len(report.Stop))
	for _, command := range report.StopCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Stop failure     %d\n", len(report.StopFailure))
	for _, command := range report.StopFailureCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Pre compact      %d\n", len(report.PreCompact))
	for _, command := range report.PreCompactCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Post compact     %d\n", len(report.PostCompact))
	for _, command := range report.PostCompactCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Notification     %d\n", len(report.Notification))
	for _, command := range report.NotificationCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Subagent start   %d\n", len(report.SubagentStart))
	for _, command := range report.SubagentStartCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Subagent stop    %d\n", len(report.SubagentStop))
	for _, command := range report.SubagentStopCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Worktree create  %d\n", len(report.WorktreeCreate))
	for _, command := range report.WorktreeCreateCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Worktree remove  %d\n", len(report.WorktreeRemove))
	for _, command := range report.WorktreeRemoveCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Cwd changed      %d\n", len(report.CwdChanged))
	for _, command := range report.CwdChangedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Task created     %d\n", len(report.TaskCreated))
	for _, command := range report.TaskCreatedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Task completed   %d\n", len(report.TaskCompleted))
	for _, command := range report.TaskCompletedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  Instructions loaded %d\n", len(report.InstructionsLoaded))
	for _, command := range report.InstructionsLoadedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
	fmt.Fprintf(out, "  File changed     %d\n", len(report.FileChanged))
	for _, command := range report.FileChangedCommands {
		fmt.Fprintf(out, "    %s\n", renderHookCommandSummary(command))
	}
}

func renderHookCommandSummary(command hookCommandSummary) string {
	var labels []string
	if strings.TrimSpace(command.Matcher) != "" {
		labels = append(labels, strings.TrimSpace(command.Matcher))
	}
	if strings.TrimSpace(command.If) != "" {
		labels = append(labels, "if "+strings.TrimSpace(command.If))
	}
	if len(labels) == 0 {
		return command.Command
	}
	return fmt.Sprintf("[%s] %s", strings.Join(labels, ", "), command.Command)
}

func renderHooksRun(out io.Writer, report hooks.RunReport) {
	fmt.Fprintln(out, "Hook Run")
	fmt.Fprintf(out, "  Event            %s\n", report.Event)
	fmt.Fprintf(out, "  Tool             %s\n", report.Tool)
	fmt.Fprintf(out, "  Commands         %d\n", report.Count)
	for _, result := range report.Results {
		name := result.Command
		if result.Type == "http" && result.URL != "" {
			name = result.URL
		}
		fmt.Fprintf(out, "  %s success=%t duration_ms=%d\n", name, result.Success, result.DurationMS)
		if result.StatusCode != 0 {
			fmt.Fprintf(out, "    status: %d\n", result.StatusCode)
		}
		if strings.TrimSpace(result.Stdout) != "" {
			fmt.Fprintf(out, "    stdout: %s\n", strings.ReplaceAll(strings.TrimSpace(result.Stdout), "\n", "\n            "))
		}
		if strings.TrimSpace(result.Stderr) != "" {
			fmt.Fprintf(out, "    stderr: %s\n", strings.ReplaceAll(strings.TrimSpace(result.Stderr), "\n", "\n            "))
		}
		if result.Error != "" {
			fmt.Fprintf(out, "    error: %s\n", result.Error)
		}
	}
}

func findUp(start string, name string) string {
	cursor := filepath.Clean(start)
	for {
		path := filepath.Join(cursor, name)
		if fileExists(path) {
			return path
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return ""
		}
		cursor = parent
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func emptyAsNone(value string) string {
	return emptyAs(value, "none")
}

func emptyAs(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (a *App) Focus(args []string) error {
	format, paths, err := parseFocusArgs("focus", args)
	if err != nil {
		return err
	}
	var report focus.Report
	if len(paths) == 0 {
		report, err = focus.BuildReport(a.Workspace)
	} else {
		report, err = focus.Add(a.Workspace, paths)
	}
	if err != nil {
		return err
	}
	return a.renderFocusReport(format, report)
}

func (a *App) AddDir(args []string) error {
	req, err := parseAddDirArgs(args)
	if err != nil {
		return err
	}
	var report pathscope.Report
	switch req.Action {
	case "list":
		report, err = pathscope.BuildReport(a.Workspace, a.Config.AdditionalDirs, "list")
	case "add":
		if _, err = pathscope.Add(a.Workspace, req.Paths); err == nil {
			err = a.refreshBuiltinToolScope()
		}
		if err == nil {
			report, err = pathscope.BuildReport(a.Workspace, a.Config.AdditionalDirs, "add")
		}
	case "remove":
		if _, err = pathscope.Remove(a.Workspace, req.Paths); err == nil {
			err = a.refreshBuiltinToolScope()
		}
		if err == nil {
			report, err = pathscope.BuildReport(a.Workspace, a.Config.AdditionalDirs, "remove")
		}
	case "clear":
		if report, err = pathscope.Clear(a.Workspace); err == nil {
			err = a.refreshBuiltinToolScope()
		}
		if err == nil {
			report, err = pathscope.BuildReport(a.Workspace, a.Config.AdditionalDirs, "clear")
		}
	default:
		err = fmt.Errorf("unknown add-dir action %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	pathscope.RenderText(a.Out, report)
	return nil
}

type addDirRequest struct {
	Format string
	Action string
	Paths  []string
}

func parseAddDirArgs(args []string) (addDirRequest, error) {
	req := addDirRequest{Format: "text", Action: "list"}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("add-dir output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--clear":
			req.Action = "clear"
		case arg == "--remove":
			req.Action = "remove"
		default:
			positionals = append(positionals, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown add-dir output format %q", req.Format)
	}
	if len(positionals) == 0 {
		if req.Action == "remove" {
			return req, errors.New("add-dir remove requires at least one path")
		}
		return req, nil
	}
	switch strings.ToLower(positionals[0]) {
	case "list", "show":
		req.Action = "list"
		req.Paths = nil
	case "add":
		req.Action = "add"
		req.Paths = positionals[1:]
	case "remove", "rm", "delete":
		req.Action = "remove"
		req.Paths = positionals[1:]
	case "clear", "reset":
		req.Action = "clear"
		req.Paths = nil
	default:
		if req.Action == "remove" {
			req.Paths = positionals
		} else {
			req.Action = "add"
			req.Paths = positionals
		}
	}
	if (req.Action == "add" || req.Action == "remove") && len(req.Paths) == 0 {
		return req, fmt.Errorf("add-dir %s requires at least one path", req.Action)
	}
	return req, nil
}

func (a *App) refreshBuiltinToolScope() error {
	if a.Tools == nil {
		return nil
	}
	additionalDirs, err := pathscope.EffectiveDirs(a.Workspace, a.Config.AdditionalDirs)
	if err != nil {
		return err
	}
	a.Tools.UpdateBuiltinScope(a.Workspace, tools.RegistryOptions{
		SandboxStrategy: a.Config.Future.SandboxStrategy,
		AdditionalDirs:  additionalDirs,
		MCPServers:      a.Config.MCPServers,
	})
	return nil
}

func (a *App) Unfocus(args []string) error {
	format, paths, err := parseFocusArgs("unfocus", args)
	if err != nil {
		return err
	}
	var report focus.Report
	if len(paths) == 0 || containsFold(paths, "--all") || containsFold(paths, "all") {
		report, err = focus.Clear(a.Workspace)
	} else {
		report, err = focus.Remove(a.Workspace, paths)
	}
	if err != nil {
		return err
	}
	return a.renderFocusReport(format, report)
}

func (a *App) renderFocusReport(format string, report focus.Report) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	focus.RenderText(a.Out, report)
	return nil
}

func parseFocusArgs(command string, args []string) (string, []string, error) {
	format := "text"
	var paths []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", nil, fmt.Errorf("%s output format is required", command)
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			paths = append(paths, arg)
		}
	}
	switch format {
	case "text", "json":
		return format, paths, nil
	default:
		return "", nil, fmt.Errorf("unknown %s output format %q", command, format)
	}
}

type outputStyleRequest struct {
	Action string
	Name   string
	Format string
}

func (a *App) OutputStyle(args []string) error {
	req, err := parseOutputStyleArgs(args)
	if err != nil {
		return err
	}
	var report outputstyle.Report
	switch req.Action {
	case "list":
		report, err = outputstyle.List(a.Config.ConfigHome, a.Workspace)
	case "show":
		report, err = outputstyle.Show(a.Config.ConfigHome, a.Workspace, req.Name)
	case "set":
		report, err = outputstyle.Set(a.Config.ConfigHome, a.Workspace, req.Name)
	case "clear":
		report, err = outputstyle.Clear(a.Workspace)
	default:
		err = fmt.Errorf("unknown output-style command %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	outputstyle.RenderText(a.Out, report)
	return nil
}

func parseOutputStyleArgs(args []string) (outputStyleRequest, error) {
	req := outputStyleRequest{Action: "list", Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return outputStyleRequest{}, errors.New("output-style output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		default:
			rest = append(rest, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return outputStyleRequest{}, fmt.Errorf("unknown output-style output format %q", req.Format)
	}
	if len(rest) == 0 {
		return req, nil
	}
	switch rest[0] {
	case "list":
		req.Action = "list"
	case "show", "set":
		if len(rest) < 2 {
			return outputStyleRequest{}, fmt.Errorf("output-style %s requires a name", rest[0])
		}
		req.Action = rest[0]
		req.Name = rest[1]
	case "clear", "reset":
		req.Action = "clear"
	default:
		req.Action = "set"
		req.Name = rest[0]
	}
	return req, nil
}

var availableThemes = []string{"default", "dark", "light", "ansi", "no-color"}

type themeRequest struct {
	Action string
	Name   string
	Format string
	Target string
	Path   string
}

type themeReport struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Theme     string   `json:"theme"`
	Previous  string   `json:"previous,omitempty"`
	Path      string   `json:"path,omitempty"`
	Available []string `json:"available"`
}

func (a *App) Theme(args []string) error {
	req, err := parseThemeArgs(args)
	if err != nil {
		return err
	}
	report := themeReport{
		Kind:      "theme",
		Action:    req.Action,
		Status:    "ok",
		Theme:     effectiveTheme(a.Config.Theme),
		Available: append([]string(nil), availableThemes...),
	}
	switch req.Action {
	case "status", "list":
	case "set":
		if err := validateThemeName(req.Name); err != nil {
			return err
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := effectiveTheme(a.Config.Theme)
		if _, err := config.SetFileValue(path, "theme", req.Name); err != nil {
			return err
		}
		a.Config.Theme = req.Name
		report.Action = "set"
		report.Theme = effectiveTheme(a.Config.Theme)
		report.Previous = previous
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := effectiveTheme(a.Config.Theme)
		if _, err := config.UnsetFileValue(path, "theme"); err != nil {
			return err
		}
		a.Config.Theme = ""
		report.Action = "clear"
		report.Theme = effectiveTheme(a.Config.Theme)
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown theme command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderThemeReport(a.Out, report)
	return nil
}

func parseThemeArgs(args []string) (themeRequest, error) {
	req := themeRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("theme output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("theme target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("theme config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "theme"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "list":
		req.Action = "list"
	case "set":
		if len(rest) < 2 {
			return req, errors.New("theme name is required")
		}
		req.Action = "set"
		req.Name = rest[1]
	case "clear", "reset":
		req.Action = "clear"
	default:
		if len(rest) > 1 {
			return req, fmt.Errorf("unexpected theme argument %q", rest[1])
		}
		req.Action = "set"
		req.Name = rest[0]
	}
	return req, nil
}

func renderThemeReport(out io.Writer, report themeReport) {
	fmt.Fprintln(out, "Theme")
	fmt.Fprintf(out, "  Active           %s\n", report.Theme)
	if report.Previous != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.Previous)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	fmt.Fprintf(out, "  Available        %s\n", strings.Join(report.Available, ", "))
}

func effectiveTheme(theme string) string {
	theme = strings.TrimSpace(theme)
	if theme == "" {
		return "default"
	}
	return theme
}

func validateThemeName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("theme name is required")
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("invalid theme name %q", name)
	}
	return nil
}

var availableEfforts = []string{"auto", "low", "medium", "high"}

type effortRequest struct {
	Action string
	Level  string
	Format string
	Target string
	Path   string
}

type effortReport struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Effort    string   `json:"effort"`
	Previous  string   `json:"previous,omitempty"`
	Path      string   `json:"path,omitempty"`
	Available []string `json:"available"`
}

func (a *App) Effort(args []string) error {
	req, err := parseEffortArgs(args)
	if err != nil {
		return err
	}
	report := effortReport{
		Kind:      "effort",
		Action:    req.Action,
		Status:    "ok",
		Effort:    effectiveEffort(a.Config.ReasoningEffort),
		Available: append([]string(nil), availableEfforts...),
	}
	switch req.Action {
	case "status", "list":
	case "set":
		if err := validateEffort(req.Level); err != nil {
			return err
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := effectiveEffort(a.Config.ReasoningEffort)
		if _, err := config.SetFileValue(path, "reasoning_effort", req.Level); err != nil {
			return err
		}
		a.Config.ReasoningEffort = req.Level
		report.Action = "set"
		report.Effort = effectiveEffort(a.Config.ReasoningEffort)
		report.Previous = previous
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := effectiveEffort(a.Config.ReasoningEffort)
		if _, err := config.UnsetFileValue(path, "reasoning_effort"); err != nil {
			return err
		}
		a.Config.ReasoningEffort = ""
		report.Action = "clear"
		report.Effort = effectiveEffort(a.Config.ReasoningEffort)
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown effort command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderEffortReport(a.Out, report)
	return nil
}

func parseEffortArgs(args []string) (effortRequest, error) {
	req := effortRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("effort output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("effort target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("effort config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "effort"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "list":
		req.Action = "list"
	case "set":
		if len(rest) < 2 {
			return req, errors.New("effort level is required")
		}
		req.Action = "set"
		req.Level = strings.ToLower(rest[1])
	case "clear", "reset":
		req.Action = "clear"
	default:
		if len(rest) > 1 {
			return req, fmt.Errorf("unexpected effort argument %q", rest[1])
		}
		req.Action = "set"
		req.Level = strings.ToLower(rest[0])
	}
	return req, nil
}

func renderEffortReport(out io.Writer, report effortReport) {
	fmt.Fprintln(out, "Effort")
	fmt.Fprintf(out, "  Active           %s\n", report.Effort)
	if report.Previous != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.Previous)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	fmt.Fprintf(out, "  Available        %s\n", strings.Join(report.Available, ", "))
}

func effectiveEffort(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return "auto"
	}
	return level
}

func validateEffort(level string) error {
	level = effectiveEffort(level)
	for _, allowed := range availableEfforts {
		if level == allowed {
			return nil
		}
	}
	return fmt.Errorf("unknown effort level %q", level)
}

type fastRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type fastReport struct {
	Kind     string `json:"kind"`
	Action   string `json:"action"`
	Status   string `json:"status"`
	Enabled  bool   `json:"enabled"`
	Previous bool   `json:"previous,omitempty"`
	Path     string `json:"path,omitempty"`
}

func (a *App) Fast(args []string) error {
	req, err := parseFastArgs(args)
	if err != nil {
		return err
	}
	previous := fastModeEnabled(a.Config.FastMode)
	report := fastReport{
		Kind:    "fast",
		Action:  req.Action,
		Status:  "ok",
		Enabled: previous,
	}
	switch req.Action {
	case "status":
	case "on", "off", "toggle":
		next := req.Action == "on"
		if req.Action == "toggle" {
			next = !previous
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "fast_mode", next); err != nil {
			return err
		}
		a.Config.FastMode = &next
		report.Action = "set"
		report.Enabled = next
		report.Previous = previous
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "fast_mode"); err != nil {
			return err
		}
		a.Config.FastMode = nil
		report.Action = "clear"
		report.Enabled = false
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown fast command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderFastReport(a.Out, report)
	return nil
}

func parseFastArgs(args []string) (fastRequest, error) {
	req := fastRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("fast output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("fast target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("fast config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "fast"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	if len(rest) > 1 {
		return req, fmt.Errorf("unexpected fast argument %q", rest[1])
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "true":
		req.Action = "on"
	case "off", "disable", "disabled", "false":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "clear", "reset", "unset":
		req.Action = "clear"
	default:
		return req, fmt.Errorf("unknown fast command %q", rest[0])
	}
	return req, nil
}

func renderFastReport(out io.Writer, report fastReport) {
	fmt.Fprintln(out, "Fast Mode")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func fastModeEnabled(value *bool) bool {
	return value != nil && *value
}

type voiceRequest struct {
	Action  string
	Format  string
	Target  string
	Path    string
	Command string
}

type voiceReport struct {
	Kind              string `json:"kind"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	Enabled           bool   `json:"enabled"`
	CommandConfigured bool   `json:"command_configured"`
	CommandAvailable  bool   `json:"command_available"`
	Command           string `json:"command,omitempty"`
	Path              string `json:"path,omitempty"`
	Message           string `json:"message,omitempty"`
}

func (a *App) Voice(args []string) error {
	req, err := parseVoiceArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "on", "off", "toggle":
		next := req.Action == "on"
		if req.Action == "toggle" {
			next = !boolPtrEnabled(a.Config.VoiceEnabled)
		}
		if next && !voiceCommandAvailable(a.Config.VoiceCommand) {
			return errors.New("voice mode requires a configured executable command; run `codog voice set-command COMMAND`")
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "voice_enabled", next); err != nil {
			return err
		}
		a.Config.VoiceEnabled = &next
		req.Path = path
	case "set-command":
		command := strings.TrimSpace(req.Command)
		if command == "" {
			return errors.New("voice command is required")
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "voice_command", command); err != nil {
			return err
		}
		a.Config.VoiceCommand = command
		req.Path = path
	case "clear-command":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "voice_command"); err != nil {
			return err
		}
		disabled := false
		if _, err := config.SetFileValue(path, "voice_enabled", disabled); err != nil {
			return err
		}
		a.Config.VoiceCommand = ""
		a.Config.VoiceEnabled = &disabled
		req.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		for _, key := range []string{"voice_enabled", "voice_command"} {
			if _, err := config.UnsetFileValue(path, key); err != nil {
				return err
			}
		}
		a.Config.VoiceEnabled = nil
		a.Config.VoiceCommand = ""
		req.Path = path
	default:
		return fmt.Errorf("unknown voice command %q", req.Action)
	}
	report := voiceReport{
		Kind:              "voice",
		Action:            req.Action,
		Status:            "ok",
		Enabled:           boolPtrEnabled(a.Config.VoiceEnabled),
		CommandConfigured: strings.TrimSpace(a.Config.VoiceCommand) != "",
		CommandAvailable:  voiceCommandAvailable(a.Config.VoiceCommand),
		Command:           strings.TrimSpace(a.Config.VoiceCommand),
		Path:              req.Path,
	}
	if !report.CommandConfigured {
		report.Message = "Voice mode needs an external STT command before it can be enabled."
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderVoiceReport(a.Out, report)
	return nil
}

func parseVoiceArgs(args []string) (voiceRequest, error) {
	req := voiceRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("voice output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("voice target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("voice config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--command":
			index++
			if index >= len(args) {
				return req, errors.New("voice command is required")
			}
			req.Command = args[index]
		case strings.HasPrefix(arg, "--command="):
			req.Command = strings.TrimPrefix(arg, "--command=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "voice"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "true":
		req.Action = "on"
	case "off", "disable", "disabled", "false":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "set-command", "command":
		req.Action = "set-command"
		if req.Command == "" && len(rest) > 1 {
			req.Command = strings.Join(rest[1:], " ")
		}
	case "clear-command":
		req.Action = "clear-command"
	case "clear", "reset", "unset":
		req.Action = "clear"
	default:
		return req, fmt.Errorf("unknown voice command %q", rest[0])
	}
	if req.Action != "set-command" && len(rest) > 1 {
		return req, fmt.Errorf("unexpected voice argument %q", rest[1])
	}
	return req, nil
}

func renderVoiceReport(out io.Writer, report voiceReport) {
	fmt.Fprintln(out, "Voice")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Command          %t\n", report.CommandConfigured)
	fmt.Fprintf(out, "  Available        %t\n", report.CommandAvailable)
	if report.Command != "" {
		fmt.Fprintf(out, "  Command value    %s\n", report.Command)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

const (
	chromeExtensionURL   = "https://claude.ai/chrome"
	chromePermissionsURL = "https://clau.de/chrome/permissions"
	chromeReconnectURL   = "https://clau.de/chrome/reconnect"
	chromeMCPServerName  = "claude-in-chrome"
)

type chromeRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type chromeReport struct {
	Kind           string `json:"kind"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Enabled        bool   `json:"enabled"`
	Previous       bool   `json:"previous,omitempty"`
	Configured     bool   `json:"configured"`
	MCPServer      string `json:"mcp_server"`
	InstallURL     string `json:"install_url"`
	PermissionsURL string `json:"permissions_url"`
	ReconnectURL   string `json:"reconnect_url"`
	RecommendedURL string `json:"recommended_url,omitempty"`
	Path           string `json:"path,omitempty"`
	Message        string `json:"message,omitempty"`
}

func (a *App) Chrome(args []string) error {
	req, err := parseChromeArgs(args)
	if err != nil {
		return err
	}
	previous := boolPtrEnabled(a.Config.Future.ChromeDefaultEnabled)
	report := chromeReport{
		Kind:           "chrome",
		Action:         req.Action,
		Status:         "ok",
		Enabled:        previous,
		Configured:     a.Config.Future.ChromeDefaultEnabled != nil,
		MCPServer:      chromeMCPServerName,
		InstallURL:     chromeExtensionURL,
		PermissionsURL: chromePermissionsURL,
		ReconnectURL:   chromeReconnectURL,
		RecommendedURL: chromeRecommendedURL(req.Action),
		Message:        chromeActionMessage(req.Action),
	}
	switch req.Action {
	case "status", "install", "permissions", "reconnect":
	case "on", "off", "toggle":
		next := req.Action == "on"
		if req.Action == "toggle" {
			next = !previous
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "future.chrome_default_enabled", next); err != nil {
			return err
		}
		a.Config.Future.ChromeDefaultEnabled = &next
		report.Action = "set"
		report.Enabled = next
		report.Previous = previous
		report.Configured = true
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "future.chrome_default_enabled"); err != nil {
			return err
		}
		a.Config.Future.ChromeDefaultEnabled = nil
		report.Enabled = false
		report.Previous = previous
		report.Configured = false
		report.Path = path
	default:
		return fmt.Errorf("unknown chrome command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderChromeReport(a.Out, report)
	return nil
}

func parseChromeArgs(args []string) (chromeRequest, error) {
	req := chromeRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("chrome output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("chrome target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("chrome config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown chrome flag %q", arg)
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "chrome"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "true":
		req.Action = "on"
	case "off", "disable", "disabled", "false":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "clear", "reset", "unset":
		req.Action = "clear"
	case "install", "extension":
		req.Action = "install"
	case "permissions", "manage-permissions":
		req.Action = "permissions"
	case "reconnect", "connect":
		req.Action = "reconnect"
	default:
		return req, fmt.Errorf("unknown chrome command %q", rest[0])
	}
	if len(rest) > 1 {
		return req, fmt.Errorf("unexpected chrome argument %q", rest[1])
	}
	return req, nil
}

func chromeRecommendedURL(action string) string {
	switch action {
	case "install":
		return chromeExtensionURL
	case "permissions":
		return chromePermissionsURL
	case "reconnect":
		return chromeReconnectURL
	default:
		return ""
	}
}

func chromeActionMessage(action string) string {
	switch action {
	case "install":
		return "Open the install URL in Chrome, then reconnect the extension."
	case "permissions":
		return "Open the permissions URL to manage site-level browser access."
	case "reconnect":
		return "Open the reconnect URL after installing or updating the Chrome extension."
	default:
		return "Chrome integration uses the claude-in-chrome MCP server when the extension is connected."
	}
}

func renderChromeReport(out io.Writer, report chromeReport) {
	fmt.Fprintln(out, "Chrome")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Configured       %t\n", report.Configured)
	fmt.Fprintf(out, "  MCP server       %s\n", report.MCPServer)
	fmt.Fprintf(out, "  Install URL      %s\n", report.InstallURL)
	fmt.Fprintf(out, "  Permissions URL  %s\n", report.PermissionsURL)
	fmt.Fprintf(out, "  Reconnect URL    %s\n", report.ReconnectURL)
	if report.RecommendedURL != "" {
		fmt.Fprintf(out, "  Recommended URL  %s\n", report.RecommendedURL)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func boolPtrEnabled(value *bool) bool {
	return value != nil && *value
}

func voiceCommandAvailable(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	name := fields[0]
	if filepath.IsAbs(name) {
		info, err := os.Stat(name)
		return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
	}
	_, err := exec.LookPath(name)
	return err == nil
}

type vimRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type vimReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	EditorMode string `json:"editor_mode"`
	Previous   string `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
}

func (a *App) Vim(args []string) error {
	req, err := parseVimArgs(args)
	if err != nil {
		return err
	}
	previous := effectiveEditorMode(a.Config.EditorMode)
	report := vimReport{
		Kind:       "vim",
		Action:     req.Action,
		Status:     "ok",
		Enabled:    editorModeIsVim(previous),
		EditorMode: previous,
	}
	switch req.Action {
	case "status":
	case "on", "off", "toggle":
		nextEnabled := req.Action == "on"
		if req.Action == "toggle" {
			nextEnabled = !editorModeIsVim(previous)
		}
		nextMode := "default"
		if nextEnabled {
			nextMode = "vim"
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "editorMode", nextMode); err != nil {
			return err
		}
		a.Config.EditorMode = nextMode
		report.Action = "set"
		report.Enabled = nextEnabled
		report.EditorMode = nextMode
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown vim command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderVimReport(a.Out, report)
	return nil
}

func parseVimArgs(args []string) (vimRequest, error) {
	req := vimRequest{Action: "toggle", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("vim output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("vim target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("vim config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "vim"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	if len(rest) > 1 && strings.ToLower(rest[0]) != "set" {
		return req, fmt.Errorf("unexpected vim argument %q", rest[1])
	}
	action := strings.ToLower(rest[0])
	if action == "set" {
		if len(rest) < 2 {
			return req, errors.New("vim mode is required")
		}
		action = strings.ToLower(rest[1])
	}
	switch action {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "vim":
		req.Action = "on"
	case "off", "disable", "disabled", "default", "emacs":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	default:
		return req, fmt.Errorf("unknown vim mode %q", action)
	}
	return req, nil
}

func renderVimReport(out io.Writer, report vimReport) {
	fmt.Fprintln(out, "Vim")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Editor mode      %s\n", report.EditorMode)
	if report.Previous != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.Previous)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func effectiveEditorMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "default"
	}
	return mode
}

func editorModeIsVim(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), "vim")
}

type privacyRequest struct {
	Action string
	Key    string
	Value  bool
	Format string
	Target string
	Path   string
}

type privacyReport struct {
	Kind     string          `json:"kind"`
	Action   string          `json:"action"`
	Status   string          `json:"status"`
	Settings map[string]bool `json:"settings"`
	Key      string          `json:"key,omitempty"`
	Value    *bool           `json:"value,omitempty"`
	Path     string          `json:"path,omitempty"`
}

func (a *App) PrivacySettings(args []string) error {
	req, err := parsePrivacyArgs(args)
	if err != nil {
		return err
	}
	report := privacyReport{
		Kind:     "privacy_settings",
		Action:   req.Action,
		Status:   "ok",
		Settings: effectivePrivacySettings(a.Config.Privacy),
	}
	switch req.Action {
	case "show":
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "privacy_settings."+req.Key, req.Value); err != nil {
			return err
		}
		a.setPrivacyValue(req.Key, &req.Value)
		report.Settings = effectivePrivacySettings(a.Config.Privacy)
		report.Key = req.Key
		report.Value = &req.Value
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "privacy_settings."+req.Key); err != nil {
			return err
		}
		a.setPrivacyValue(req.Key, nil)
		report.Settings = effectivePrivacySettings(a.Config.Privacy)
		report.Key = req.Key
		report.Path = path
	default:
		return fmt.Errorf("unknown privacy-settings command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderPrivacyReport(a.Out, report)
	return nil
}

func parsePrivacyArgs(args []string) (privacyRequest, error) {
	req := privacyRequest{Action: "show", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("privacy-settings output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("privacy-settings target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("privacy-settings config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "privacy-settings"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	action := strings.ToLower(rest[0])
	switch action {
	case "show", "status", "list":
		req.Action = "show"
		return req, nil
	case "set":
		if len(rest) < 3 {
			return req, errors.New("usage: privacy-settings set KEY on|off")
		}
		key, err := canonicalPrivacyKey(rest[1])
		if err != nil {
			return req, err
		}
		value, err := parseOnOff(rest[2])
		if err != nil {
			return req, err
		}
		req.Action = "set"
		req.Key = key
		req.Value = value
		return req, nil
	case "enable", "enabled", "on":
		if len(rest) < 2 {
			return req, errors.New("privacy setting key is required")
		}
		key, err := canonicalPrivacyKey(rest[1])
		if err != nil {
			return req, err
		}
		req.Action = "set"
		req.Key = key
		req.Value = true
		return req, nil
	case "disable", "disabled", "off":
		if len(rest) < 2 {
			return req, errors.New("privacy setting key is required")
		}
		key, err := canonicalPrivacyKey(rest[1])
		if err != nil {
			return req, err
		}
		req.Action = "set"
		req.Key = key
		req.Value = false
		return req, nil
	case "clear", "reset", "unset":
		if len(rest) < 2 {
			return req, errors.New("privacy setting key is required")
		}
		key, err := canonicalPrivacyKey(rest[1])
		if err != nil {
			return req, err
		}
		req.Action = "clear"
		req.Key = key
		return req, nil
	default:
		if len(rest) != 2 {
			return req, fmt.Errorf("unknown privacy-settings command %q", rest[0])
		}
		key, err := canonicalPrivacyKey(rest[0])
		if err != nil {
			return req, err
		}
		value, err := parseOnOff(rest[1])
		if err != nil {
			return req, err
		}
		req.Action = "set"
		req.Key = key
		req.Value = value
		return req, nil
	}
}

func renderPrivacyReport(out io.Writer, report privacyReport) {
	fmt.Fprintln(out, "Privacy Settings")
	for _, key := range []string{"telemetry_enabled", "crash_reports_enabled", "prompt_history_enabled"} {
		label := privacyLabel(key)
		state := "disabled"
		if report.Settings[key] {
			state = "enabled"
		}
		fmt.Fprintf(out, "  %-16s %s\n", label, state)
	}
	if report.Key != "" {
		fmt.Fprintf(out, "  Updated          %s\n", report.Key)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func effectivePrivacySettings(cfg config.PrivacyConfig) map[string]bool {
	return map[string]bool{
		"telemetry_enabled":      privacyBool(cfg.TelemetryEnabled, false),
		"crash_reports_enabled":  privacyBool(cfg.CrashReportsEnabled, false),
		"prompt_history_enabled": privacyBool(cfg.PromptHistoryEnabled, true),
	}
}

func privacyBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func (a *App) promptHistoryEnabled() bool {
	return privacyBool(a.Config.Privacy.PromptHistoryEnabled, true)
}

func (a *App) setPrivacyValue(key string, value *bool) {
	switch key {
	case "telemetry_enabled":
		a.Config.Privacy.TelemetryEnabled = value
	case "crash_reports_enabled":
		a.Config.Privacy.CrashReportsEnabled = value
	case "prompt_history_enabled":
		a.Config.Privacy.PromptHistoryEnabled = value
	}
}

func canonicalPrivacyKey(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.ReplaceAll(key, "_", "-")
	switch key {
	case "telemetry", "analytics", "telemetry-enabled":
		return "telemetry_enabled", nil
	case "crash", "crash-report", "crash-reports", "crash-reports-enabled":
		return "crash_reports_enabled", nil
	case "history", "prompt-history", "prompt-history-enabled":
		return "prompt_history_enabled", nil
	default:
		return "", fmt.Errorf("unknown privacy setting %q", raw)
	}
}

func privacyLabel(key string) string {
	switch key {
	case "telemetry_enabled":
		return "Telemetry"
	case "crash_reports_enabled":
		return "Crash reports"
	case "prompt_history_enabled":
		return "Prompt history"
	default:
		return key
	}
}

func parseOnOff(raw string) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "on", "enable", "enabled", "yes", "y":
		return true, nil
	case "off", "disable", "disabled", "no", "n":
		return false, nil
	default:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("expected on or off, got %q", raw)
		}
		return parsed, nil
	}
}

func validateTextOrJSON(format, command string) error {
	switch format {
	case "text", "json":
		return nil
	default:
		return fmt.Errorf("unknown %s output format %q", command, format)
	}
}

func (a *App) preferenceConfigPath(target, path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return a.resolveOutputPath(path), nil
	}
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "user", "global":
		if strings.TrimSpace(a.Config.ConfigHome) == "" {
			return "", errors.New("config home is unavailable")
		}
		return filepath.Join(a.Config.ConfigHome, "config.json"), nil
	case "project", "workspace":
		return a.resolveOutputPath(".codog.json"), nil
	case "local":
		return a.resolveOutputPath(".codog.local.json"), nil
	default:
		return "", fmt.Errorf("unknown config target %q", target)
	}
}

type keybindingEntry struct {
	Key         string `json:"key"`
	Action      string `json:"action"`
	Mode        string `json:"mode,omitempty"`
	Description string `json:"description,omitempty"`
}

type keybindingSection struct {
	Name     string            `json:"name"`
	Entries  []keybindingEntry `json:"entries"`
	Disabled bool              `json:"disabled,omitempty"`
}

type keybindingsRequest struct {
	Action string
	Format string
	Force  bool
}

type keybindingReport struct {
	Kind              string              `json:"kind"`
	Action            string              `json:"action"`
	Status            string              `json:"status"`
	EditorMode        string              `json:"editor_mode"`
	VimMode           bool                `json:"vim_mode"`
	KeybindingsPath   string              `json:"keybindings_path,omitempty"`
	KeybindingsExists bool                `json:"keybindings_exists"`
	Sections          []keybindingSection `json:"sections,omitempty"`
}

type keybindingsFileReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Exists  bool   `json:"exists"`
}

func (a *App) Keybindings(args []string) error {
	req, err := parseKeybindingsArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "show":
		report := a.keybindingReport()
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderKeybindings(a.Out, report)
		return nil
	case "path":
		path, err := a.keybindingsPath()
		if err != nil {
			return err
		}
		report := keybindingsFileReport{
			Kind:   "keybindings",
			Action: "path",
			Status: "ok",
			Path:   path,
			Exists: fileExists(path),
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprintln(a.Out, path)
		return nil
	case "init":
		report, err := a.initKeybindings(req.Force)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderKeybindingsFileReport(a.Out, report)
		return nil
	default:
		return fmt.Errorf("unknown keybindings command %q", req.Action)
	}
}

func parseKeybindingsArgs(args []string) (keybindingsRequest, error) {
	req := keybindingsRequest{Action: "show", Format: "text"}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("keybindings output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--force":
			req.Force = true
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown keybindings flag %q", arg)
		default:
			if actionSet {
				return req, fmt.Errorf("unexpected keybindings argument %q", arg)
			}
			switch strings.ToLower(arg) {
			case "show", "list", "report":
				req.Action = "show"
			case "path", "where":
				req.Action = "path"
			case "init", "create", "template":
				req.Action = "init"
			default:
				return req, fmt.Errorf("unknown keybindings command %q", arg)
			}
			actionSet = true
		}
	}
	if err := validateTextOrJSON(req.Format, "keybindings"); err != nil {
		return req, err
	}
	return req, nil
}

func (a *App) initKeybindings(force bool) (keybindingsFileReport, error) {
	path, err := a.keybindingsPath()
	if err != nil {
		return keybindingsFileReport{}, err
	}
	alreadyExists := fileExists(path)
	report := keybindingsFileReport{
		Kind:   "keybindings",
		Action: "init",
		Status: "exists",
		Path:   path,
		Exists: alreadyExists,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return report, err
	}
	data := defaultKeybindingsTemplate()
	if force {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return report, err
		}
		report.Status = "written"
		report.Created = !alreadyExists
		report.Exists = true
		return report, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return report, nil
		}
		return report, err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return report, err
	}
	report.Status = "created"
	report.Created = true
	report.Exists = true
	return report, nil
}

func (a *App) keybindingsPath() (string, error) {
	if strings.TrimSpace(a.Config.ConfigHome) == "" {
		return "", errors.New("config home is unavailable")
	}
	return filepath.Join(a.Config.ConfigHome, "keybindings.json"), nil
}

func defaultKeybindingsTemplate() []byte {
	type bindingBlock struct {
		Context  string            `json:"context"`
		Bindings map[string]string `json:"bindings"`
	}
	template := struct {
		Bindings []bindingBlock `json:"bindings"`
	}{
		Bindings: []bindingBlock{
			{
				Context: "repl",
				Bindings: map[string]string{
					"enter":  "submit prompt",
					"tab":    "complete slash command, skill, model, or session",
					"ctrl+r": "reverse search prompt history",
					"ctrl+c": "exit current REPL read",
				},
			},
			{
				Context: "repl-vim",
				Bindings: map[string]string{
					"esc": "enter normal mode",
					"i":   "enter insert mode",
					"h":   "move left",
					"j":   "previous history item",
					"k":   "next history item",
					"l":   "move right",
				},
			},
			{
				Context: "tui",
				Bindings: map[string]string{
					"ctrl+s": "submit prompt",
					"tab":    "complete slash command",
					"esc":    "quit without submitting",
					"ctrl+c": "quit",
				},
			},
			{
				Context: "slash",
				Bindings: map[string]string{
					"/help":             "show command help",
					"/keybindings":      "show keybinding report",
					"/keybindings init": "create keybindings.json template",
					"/vim":              "toggle vim keybinding preference",
				},
			},
		},
	}
	data, _ := json.MarshalIndent(template, "", "  ")
	return append(data, '\n')
}

func (a *App) keybindingReport() keybindingReport {
	editorMode := effectiveEditorMode(a.Config.EditorMode)
	path, _ := a.keybindingsPath()
	return keybindingReport{
		Kind:              "keybindings",
		Action:            "show",
		Status:            "ok",
		EditorMode:        editorMode,
		VimMode:           editorModeIsVim(editorMode),
		KeybindingsPath:   path,
		KeybindingsExists: path != "" && fileExists(path),
		Sections: []keybindingSection{
			{
				Name: "REPL",
				Entries: []keybindingEntry{
					{Key: "Enter", Action: "submit prompt"},
					{Key: "Tab", Action: "complete slash command, skill, model, or session"},
					{Key: "Ctrl-R", Action: "reverse search prompt history", Description: "available when prompt history is enabled"},
					{Key: "Ctrl-C", Action: "exit current REPL read"},
					{Key: "/exit", Action: "quit the REPL"},
				},
			},
			{
				Name:     "REPL vim",
				Disabled: !editorModeIsVim(editorMode),
				Entries: []keybindingEntry{
					{Key: "Esc", Action: "enter normal mode", Mode: "insert"},
					{Key: "i", Action: "enter insert mode", Mode: "normal"},
					{Key: "h/j/k/l", Action: "move cursor/history", Mode: "normal"},
					{Key: "0/$", Action: "jump to line start/end", Mode: "normal"},
				},
			},
			{
				Name: "TUI",
				Entries: []keybindingEntry{
					{Key: "Ctrl-S", Action: "submit prompt"},
					{Key: "Tab", Action: "complete slash command"},
					{Key: "Esc", Action: "quit without submitting"},
					{Key: "Ctrl-C", Action: "quit"},
				},
			},
			{
				Name: "Slash",
				Entries: []keybindingEntry{
					{Key: "/help", Action: "show command help"},
					{Key: "/keybindings", Action: "show this report"},
					{Key: "/keybindings init", Action: "create keybindings.json template"},
					{Key: "/vim", Action: "toggle vim keybinding preference"},
					{Key: "/privacy-settings", Action: "change local privacy preferences"},
				},
			},
		},
	}
}

func renderKeybindingsFileReport(out io.Writer, report keybindingsFileReport) {
	switch report.Status {
	case "created":
		fmt.Fprintf(out, "Created keybindings template: %s\n", report.Path)
	case "written":
		fmt.Fprintf(out, "Wrote keybindings template: %s\n", report.Path)
	default:
		fmt.Fprintf(out, "Keybindings file already exists: %s\n", report.Path)
	}
}

func renderKeybindings(out io.Writer, report keybindingReport) {
	fmt.Fprintln(out, "Keybindings")
	fmt.Fprintf(out, "  Editor mode      %s\n", report.EditorMode)
	fmt.Fprintf(out, "  Vim mode         %t\n", report.VimMode)
	if report.KeybindingsPath != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.KeybindingsPath)
		fmt.Fprintf(out, "  Config exists    %t\n", report.KeybindingsExists)
	}
	for _, section := range report.Sections {
		fmt.Fprintln(out)
		name := section.Name
		if section.Disabled {
			name += " (disabled)"
		}
		fmt.Fprintf(out, "%s\n", name)
		for _, entry := range section.Entries {
			action := entry.Action
			if entry.Mode != "" {
				action += " [" + entry.Mode + "]"
			}
			if entry.Description != "" {
				action += " - " + entry.Description
			}
			fmt.Fprintf(out, "  %-14s %s\n", entry.Key, action)
		}
	}
}

type todosRequest struct {
	Action   string
	ID       string
	Content  string
	Priority string
	Format   string
}

func (a *App) Todos(args []string) error {
	req, err := parseTodosArgs(args)
	if err != nil {
		return err
	}
	var report todos.Report
	switch req.Action {
	case "list":
		report, err = todos.List(a.Workspace)
	case "add":
		report, err = todos.Add(a.Workspace, req.Content, req.Priority)
	case "start":
		report, err = todos.UpdateStatus(a.Workspace, req.ID, "in_progress")
	case "done":
		report, err = todos.UpdateStatus(a.Workspace, req.ID, "completed")
	case "pending":
		report, err = todos.UpdateStatus(a.Workspace, req.ID, "pending")
	case "clear":
		report, err = todos.Clear(a.Workspace)
	default:
		err = fmt.Errorf("unknown todos command %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	todos.RenderText(a.Out, report)
	return nil
}

func parseTodosArgs(args []string) (todosRequest, error) {
	req := todosRequest{Action: "list", Format: "text", Priority: "medium"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return todosRequest{}, errors.New("todos output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--priority":
			index++
			if index >= len(args) {
				return todosRequest{}, errors.New("todo priority is required")
			}
			req.Priority = args[index]
		case strings.HasPrefix(arg, "--priority="):
			req.Priority = strings.TrimPrefix(arg, "--priority=")
		default:
			rest = append(rest, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return todosRequest{}, fmt.Errorf("unknown todos output format %q", req.Format)
	}
	if len(rest) == 0 || rest[0] == "list" {
		return req, nil
	}
	req.Action = rest[0]
	switch req.Action {
	case "add":
		if len(rest) < 2 {
			return todosRequest{}, errors.New("todo content is required")
		}
		req.Content = strings.Join(rest[1:], " ")
	case "start", "done", "pending":
		if len(rest) < 2 {
			return todosRequest{}, fmt.Errorf("todo id is required for %s", req.Action)
		}
		req.ID = rest[1]
	case "clear":
	default:
		return todosRequest{}, fmt.Errorf("unknown todos command %q", req.Action)
	}
	return req, nil
}

type commandRequest struct {
	Format    string
	TimeoutMS int
	Command   []string
}

func (a *App) RunCommand(ctx context.Context, args []string) error {
	req, err := parseCommandRequest(args, nil)
	if err != nil {
		return err
	}
	return a.runCommandRequest(ctx, "run", req)
}

func (a *App) LanguageCommand(ctx context.Context, language string, args []string) error {
	req, err := parseCommandRequest(args, nil)
	if err != nil {
		return err
	}
	command, err := languageCommand(a.Workspace, language, req.Command)
	if err != nil {
		return err
	}
	req.Command = command
	return a.runCommandRequest(ctx, language, req)
}

func (a *App) ProjectCommand(ctx context.Context, kind string, args []string) error {
	req, err := parseCommandRequest(args, defaultProjectCommand(kind))
	if err != nil {
		return err
	}
	return a.runCommandRequest(ctx, kind, req)
}

func (a *App) runCommandRequest(ctx context.Context, kind string, req commandRequest) error {
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	result, err := commandrun.Run(ctx, commandrun.Options{
		Workspace: a.Workspace,
		Command:   req.Command,
		Timeout:   timeout,
		Kind:      kind,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	commandrun.RenderText(a.Out, result)
	return nil
}

func parseCommandRequest(args []string, defaultCommand []string) (commandRequest, error) {
	req := commandRequest{Format: "text", TimeoutMS: 10 * 60 * 1000}
	commandArgs := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			commandArgs = append(commandArgs, args[index+1:]...)
			index = len(args)
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("command output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--timeout-ms":
			index++
			if index >= len(args) {
				return req, errors.New("command timeout is required")
			}
			timeout, err := parseCommandTimeout(args[index])
			if err != nil {
				return req, err
			}
			req.TimeoutMS = timeout
		case strings.HasPrefix(arg, "--timeout-ms="):
			timeout, err := parseCommandTimeout(strings.TrimPrefix(arg, "--timeout-ms="))
			if err != nil {
				return req, err
			}
			req.TimeoutMS = timeout
		default:
			commandArgs = append(commandArgs, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown command output format %q", req.Format)
	}
	if len(defaultCommand) == 0 {
		req.Command = commandArgs
	} else {
		req.Command = append([]string(nil), defaultCommand...)
		req.Command = append(req.Command, commandArgs...)
	}
	if len(req.Command) == 0 {
		return req, errors.New("usage: codog run COMMAND [ARG...]")
	}
	return req, nil
}

func languageCommand(workspace, language string, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: codog %s CODE|FILE [ARG...]", language)
	}
	var executable string
	var inlineFlag string
	switch strings.ToLower(language) {
	case "node", "javascript", "js":
		executable = "node"
		inlineFlag = "-e"
	case "python", "python3", "py":
		executable = pythonExecutable()
		inlineFlag = "-c"
	default:
		return nil, fmt.Errorf("unknown language command %q", language)
	}
	if path, ok := existingLanguageScript(workspace, args[0]); ok {
		return append([]string{executable, path}, args[1:]...), nil
	}
	return []string{executable, inlineFlag, strings.Join(args, " ")}, nil
}

func existingLanguageScript(workspace, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	path := value
	if !filepath.IsAbs(path) && strings.TrimSpace(workspace) != "" {
		path = filepath.Join(workspace, path)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

func pythonExecutable() string {
	if _, err := exec.LookPath("python3"); err == nil {
		return "python3"
	}
	return "python"
}

func parseCommandTimeout(value string) (int, error) {
	timeout, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if timeout <= 0 {
		return 0, errors.New("command timeout must be positive")
	}
	return timeout, nil
}

func defaultProjectCommand(kind string) []string {
	switch kind {
	case "test":
		return []string{"go", "test", "./..."}
	case "build":
		return []string{"go", "build", "./..."}
	case "lint":
		return []string{"go", "vet", "./..."}
	default:
		return nil
	}
}

type searchRequest struct {
	Query      string
	Path       string
	Glob       string
	IgnoreCase bool
	Limit      int
	Format     string
}

type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type searchReport struct {
	Kind       string        `json:"kind"`
	Query      string        `json:"query"`
	Path       string        `json:"path,omitempty"`
	Glob       string        `json:"glob,omitempty"`
	IgnoreCase bool          `json:"ignore_case,omitempty"`
	Limit      int           `json:"limit"`
	Total      int           `json:"total"`
	Truncated  bool          `json:"truncated"`
	Matches    []searchMatch `json:"matches"`
}

type filesRequest struct {
	Format        string
	Path          string
	Glob          string
	Limit         int
	IncludeHidden bool
}

func (a *App) Files(args []string) error {
	req, err := parseFilesArgs(args)
	if err != nil {
		return err
	}
	report, err := fileinventory.Build(a.Workspace, fileinventory.Options{
		Path:          req.Path,
		Glob:          req.Glob,
		Limit:         req.Limit,
		IncludeHidden: req.IncludeHidden,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fileinventory.RenderText(a.Out, report)
	return nil
}

func (a *App) Search(ctx context.Context, args []string) error {
	req, err := parseSearchArgs(args)
	if err != nil {
		return err
	}
	report, err := a.searchReport(ctx, req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSearchReport(a.Out, report)
	return nil
}

func (a *App) searchReport(ctx context.Context, req searchRequest) (searchReport, error) {
	payload, _ := json.Marshal(map[string]any{
		"pattern":     req.Query,
		"path":        req.Path,
		"glob":        req.Glob,
		"ignore_case": req.IgnoreCase,
		"limit":       req.Limit,
	})
	raw, err := tools.GrepTool{Workspace: a.Workspace}.Execute(ctx, payload)
	if err != nil {
		return searchReport{}, err
	}
	var result struct {
		Matches   []searchMatch `json:"matches"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return searchReport{}, err
	}
	return searchReport{
		Kind:       "search",
		Query:      req.Query,
		Path:       req.Path,
		Glob:       req.Glob,
		IgnoreCase: req.IgnoreCase,
		Limit:      req.Limit,
		Total:      len(result.Matches),
		Truncated:  result.Truncated,
		Matches:    result.Matches,
	}, nil
}

func renderSearchReport(out io.Writer, report searchReport) {
	fmt.Fprintln(out, "Search")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Matches          %d\n", report.Total)
	if report.Truncated {
		fmt.Fprintf(out, "  Truncated        true\n")
	}
	if report.Total == 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "No matches.")
		return
	}
	fmt.Fprintln(out)
	for _, match := range report.Matches {
		fmt.Fprintf(out, "%s:%d:%s\n", match.Path, match.Line, match.Text)
	}
}

type securityReviewRequest struct {
	Format string
	Limit  int
}

type bughunterRequest struct {
	Format string
	Scope  string
	Limit  int
}

func (a *App) Bughunter(args []string) error {
	req, err := parseBughunterArgs(args)
	if err != nil {
		return err
	}
	report, err := bughunt.Scan(a.Workspace, bughunt.Options{Scope: req.Scope, Limit: req.Limit})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	bughunt.RenderText(a.Out, report)
	return nil
}

func parseBughunterArgs(args []string) (bughunterRequest, error) {
	req := bughunterRequest{Format: "text", Limit: 200}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return bughunterRequest{}, errors.New("bughunter output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return bughunterRequest{}, errors.New("bughunter limit is required")
			}
			limit, err := strconv.Atoi(args[index])
			if err != nil {
				return bughunterRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return bughunterRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "-"):
			return bughunterRequest{}, fmt.Errorf("unknown bughunter argument %q", arg)
		default:
			if req.Scope != "" {
				return bughunterRequest{}, fmt.Errorf("unexpected bughunter argument %q", arg)
			}
			req.Scope = arg
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return bughunterRequest{}, fmt.Errorf("unknown bughunter output format %q", req.Format)
	}
}

func (a *App) SecurityReview(args []string) error {
	req, err := parseSecurityReviewArgs(args)
	if err != nil {
		return err
	}
	report, err := securityreview.Review(a.Workspace, req.Limit)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	securityreview.RenderText(a.Out, report)
	return nil
}

func parseSecurityReviewArgs(args []string) (securityReviewRequest, error) {
	req := securityReviewRequest{Format: "text", Limit: 200}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return securityReviewRequest{}, errors.New("security-review output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return securityReviewRequest{}, errors.New("security-review limit is required")
			}
			limit, err := strconv.Atoi(args[index])
			if err != nil {
				return securityReviewRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return securityReviewRequest{}, err
			}
			req.Limit = limit
		default:
			return securityReviewRequest{}, fmt.Errorf("unknown security-review argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return securityReviewRequest{}, fmt.Errorf("unknown security-review output format %q", req.Format)
	}
}

type reviewRequest struct {
	Format string
	Base   string
	Staged bool
	Limit  int
}

func (a *App) Review(args []string) error {
	req, err := parseReviewArgs(args)
	if err != nil {
		return err
	}
	report, err := localreview.Run(a.Workspace, localreview.Options{
		Base:   req.Base,
		Staged: req.Staged,
		Limit:  req.Limit,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	localreview.RenderText(a.Out, report)
	return nil
}

func parseReviewArgs(args []string) (reviewRequest, error) {
	req := reviewRequest{Format: "text", Limit: 200}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--staged":
			req.Staged = true
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return reviewRequest{}, errors.New("review output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--base":
			index++
			if index >= len(args) {
				return reviewRequest{}, errors.New("review base ref is required")
			}
			req.Base = args[index]
		case strings.HasPrefix(arg, "--base="):
			req.Base = strings.TrimPrefix(arg, "--base=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return reviewRequest{}, errors.New("review limit is required")
			}
			limit, err := strconv.Atoi(args[index])
			if err != nil {
				return reviewRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return reviewRequest{}, err
			}
			req.Limit = limit
		default:
			return reviewRequest{}, fmt.Errorf("unknown review argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return reviewRequest{}, fmt.Errorf("unknown review output format %q", req.Format)
	}
}

type feedbackRequest struct {
	Format    string
	Output    string
	Message   string
	SessionID string
}

type feedbackReport struct {
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	File            string `json:"file"`
	Bytes           int    `json:"bytes"`
	Message         string `json:"message,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	SessionMessages int    `json:"session_messages,omitempty"`
	Model           string `json:"model"`
	PermissionMode  string `json:"permission_mode"`
	GitBranch       string `json:"git_branch,omitempty"`
	GitClean        bool   `json:"git_clean"`
}

type feedbackBundle struct {
	CreatedAt time.Time            `json:"created_at"`
	Message   string               `json:"message"`
	Version   versioninfo.Report   `json:"version"`
	Status    localstatus.Snapshot `json:"status"`
}

func (a *App) Feedback(args []string, overrides config.FlagOverrides) error {
	req, err := parseFeedbackArgs(args, overrides)
	if err != nil {
		return err
	}
	active, err := a.feedbackSession(req.SessionID)
	if err != nil {
		return err
	}
	snapshot := a.statusSnapshot(active)
	bundle := feedbackBundle{
		CreatedAt: time.Now().UTC(),
		Message:   strings.TrimSpace(req.Message),
		Version:   versioninfo.Build(version, a.Workspace),
		Status:    snapshot,
	}
	path := a.feedbackOutputPath(req.Output, bundle.CreatedAt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := session.ValidateExportOutputPath(path); err != nil {
		return err
	}
	data := []byte(renderFeedbackMarkdown(bundle))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	report := feedbackReport{
		Kind:            "feedback",
		Action:          "write",
		Status:          "ok",
		File:            path,
		Bytes:           len(data),
		Message:         bundle.Message,
		SessionID:       snapshot.Session.ID,
		SessionMessages: snapshot.Session.MessageCount,
		Model:           snapshot.Config.Model,
		PermissionMode:  snapshot.Config.PermissionMode,
		GitBranch:       snapshot.Git.Branch,
		GitClean:        snapshot.Git.Clean,
	}
	if req.Format == "json" {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	renderFeedbackReport(a.Out, report)
	return nil
}

func parseFeedbackArgs(args []string, overrides config.FlagOverrides) (feedbackRequest, error) {
	req := feedbackRequest{Format: "text"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	var message []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("feedback output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--output":
			index++
			if index >= len(args) {
				return req, errors.New("feedback output path is required")
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("feedback session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("feedback resume session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--message":
			index++
			if index >= len(args) {
				return req, errors.New("feedback message is required")
			}
			message = append(message, args[index])
		case strings.HasPrefix(arg, "--message="):
			message = append(message, strings.TrimPrefix(arg, "--message="))
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown feedback flag %q", arg)
		default:
			message = append(message, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "feedback"); err != nil {
		return req, err
	}
	req.Message = strings.TrimSpace(strings.Join(message, " "))
	return req, nil
}

func (a *App) feedbackSession(sessionID string) (*session.Session, error) {
	if strings.TrimSpace(sessionID) == "" || a.Sessions == nil {
		return nil, nil
	}
	active, err := a.Sessions.Open(sessionID)
	if errors.Is(err, session.ErrNoSessions) {
		return nil, nil
	}
	return active, err
}

func (a *App) feedbackOutputPath(output string, createdAt time.Time) string {
	filename := fmt.Sprintf("feedback-%s-%d.md", createdAt.Format("20060102T150405Z"), createdAt.UnixNano())
	if strings.TrimSpace(output) == "" {
		return filepath.Join(a.Workspace, ".codog", "feedback", filename)
	}
	path := a.resolveOutputPath(output)
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path
	}
	return filepath.Join(path, filename)
}

func renderFeedbackReport(out io.Writer, report feedbackReport) {
	fmt.Fprintln(out, "Feedback")
	fmt.Fprintf(out, "  File             %s\n", report.File)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", prompthistory.Preview(report.Message, 80))
	}
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s (%d messages)\n", report.SessionID, report.SessionMessages)
	}
	fmt.Fprintf(out, "  Model            %s\n", report.Model)
	fmt.Fprintf(out, "  Permission       %s\n", report.PermissionMode)
	if report.GitBranch != "" {
		fmt.Fprintf(out, "  Git              branch=%s clean=%t\n", report.GitBranch, report.GitClean)
	}
}

func renderFeedbackMarkdown(bundle feedbackBundle) string {
	var builder strings.Builder
	builder.WriteString("# Codog Feedback\n\n")
	builder.WriteString(fmt.Sprintf("- Created: %s\n", bundle.CreatedAt.Format(time.RFC3339)))
	builder.WriteString(fmt.Sprintf("- Version: %s\n", bundle.Version.Version))
	builder.WriteString(fmt.Sprintf("- Target: %s\n", bundle.Version.BuildTarget))
	builder.WriteString(fmt.Sprintf("- Workspace: %s\n", bundle.Status.Workspace.Path))
	builder.WriteString(fmt.Sprintf("- Model: %s\n", bundle.Status.Config.Model))
	builder.WriteString(fmt.Sprintf("- Permission mode: %s\n", bundle.Status.Config.PermissionMode))
	if bundle.Status.Session.Active {
		builder.WriteString(fmt.Sprintf("- Session: %s (%d messages)\n", bundle.Status.Session.ID, bundle.Status.Session.MessageCount))
	}
	if bundle.Status.Git.Available {
		builder.WriteString(fmt.Sprintf("- Git: branch=%s clean=%t staged=%d unstaged=%d untracked=%d conflicts=%d\n",
			bundle.Status.Git.Branch,
			bundle.Status.Git.Clean,
			bundle.Status.Git.Staged,
			bundle.Status.Git.Unstaged,
			bundle.Status.Git.Untracked,
			bundle.Status.Git.Conflicts,
		))
	}
	builder.WriteString("\n## Message\n\n")
	if strings.TrimSpace(bundle.Message) == "" {
		builder.WriteString("No description provided.\n")
	} else {
		builder.WriteString(bundle.Message)
		builder.WriteString("\n")
	}
	builder.WriteString("\n## Diagnostics\n\n```json\n")
	diagnostics, _ := json.MarshalIndent(map[string]any{
		"version": bundle.Version,
		"status":  bundle.Status,
	}, "", "  ")
	builder.Write(diagnostics)
	builder.WriteString("\n```\n")
	return builder.String()
}

type draftRequest struct {
	Format    string
	Output    string
	Context   string
	SessionID string
}

type draftReport struct {
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	File            string `json:"file"`
	Bytes           int    `json:"bytes"`
	Title           string `json:"title"`
	Context         string `json:"context,omitempty"`
	Branch          string `json:"branch,omitempty"`
	GitClean        bool   `json:"git_clean"`
	SessionID       string `json:"session_id,omitempty"`
	SessionMessages int    `json:"session_messages,omitempty"`
}

type draftBundle struct {
	Kind       string
	CreatedAt  time.Time
	Context    string
	Title      string
	Status     localstatus.Snapshot
	GitStatus  string
	DiffStat   string
	StagedStat string
	RecentLog  string
	Remote     string
}

func (a *App) PullRequestDraft(args []string, overrides config.FlagOverrides) error {
	return a.writeDraft("pr", args, overrides)
}

type commitPushPRRequest struct {
	Format  string
	Message string
	Title   string
	Body    string
	Branch  string
	Base    string
	Remote  string
	All     bool
	Draft   bool
	NoPR    bool
	DryRun  bool
}

func (a *App) CommitPushPR(ctx context.Context, args []string) error {
	req, err := parseCommitPushPRArgs(args)
	if err != nil {
		return err
	}
	report, err := prworkflow.Run(ctx, prworkflow.Options{
		Workspace: a.Workspace,
		Message:   req.Message,
		Title:     req.Title,
		Body:      req.Body,
		Branch:    req.Branch,
		Base:      req.Base,
		Remote:    req.Remote,
		All:       req.All,
		Draft:     req.Draft,
		NoPR:      req.NoPR,
		DryRun:    req.DryRun,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCommitPushPRReport(a.Out, report)
	return nil
}

func parseCommitPushPRArgs(args []string) (commitPushPRRequest, error) {
	req := commitPushPRRequest{Format: "text", Remote: "origin", All: true}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--message" || arg == "-m":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr message is required")
			}
			req.Message = args[index]
		case strings.HasPrefix(arg, "--message="):
			req.Message = strings.TrimPrefix(arg, "--message=")
		case arg == "--title":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr title is required")
			}
			req.Title = args[index]
		case strings.HasPrefix(arg, "--title="):
			req.Title = strings.TrimPrefix(arg, "--title=")
		case arg == "--body":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr body is required")
			}
			req.Body = args[index]
		case strings.HasPrefix(arg, "--body="):
			req.Body = strings.TrimPrefix(arg, "--body=")
		case arg == "--branch" || arg == "-b":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr branch is required")
			}
			req.Branch = args[index]
		case strings.HasPrefix(arg, "--branch="):
			req.Branch = strings.TrimPrefix(arg, "--branch=")
		case arg == "--base":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr base branch is required")
			}
			req.Base = args[index]
		case strings.HasPrefix(arg, "--base="):
			req.Base = strings.TrimPrefix(arg, "--base=")
		case arg == "--remote":
			index++
			if index >= len(args) {
				return req, errors.New("commit-push-pr remote is required")
			}
			req.Remote = args[index]
		case strings.HasPrefix(arg, "--remote="):
			req.Remote = strings.TrimPrefix(arg, "--remote=")
		case arg == "--draft":
			req.Draft = true
		case arg == "--no-pr":
			req.NoPR = true
		case arg == "--dry-run":
			req.DryRun = true
		case arg == "--all":
			req.All = true
		case arg == "--staged":
			req.All = false
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown commit-push-pr flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "commit-push-pr"); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.Message) == "" {
		req.Message = strings.Join(positionals, " ")
	}
	if strings.TrimSpace(req.Message) == "" && strings.TrimSpace(req.Title) != "" {
		req.Message = req.Title
	}
	if strings.TrimSpace(req.Message) == "" {
		return req, errors.New("commit-push-pr requires a commit message")
	}
	return req, nil
}

func renderCommitPushPRReport(out io.Writer, report prworkflow.Report) {
	fmt.Fprintln(out, "Commit Push PR")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Dry run          %t\n", report.DryRun)
	fmt.Fprintf(out, "  Branch           %s\n", report.Branch)
	if report.Base != "" {
		fmt.Fprintf(out, "  Base             %s\n", report.Base)
	}
	fmt.Fprintf(out, "  Remote           %s\n", report.Remote)
	fmt.Fprintf(out, "  Title            %s\n", report.Title)
	if report.Commit != "" {
		fmt.Fprintf(out, "  Commit           %s\n", report.Commit)
	}
	if report.PRURL != "" {
		fmt.Fprintf(out, "  PR URL           %s\n", report.PRURL)
	}
	for _, step := range report.Steps {
		fmt.Fprintf(out, "  Step             %-12s %s", step.Name, step.Status)
		if len(step.Command) > 0 {
			fmt.Fprintf(out, "  %s", strings.Join(step.Command, " "))
		}
		fmt.Fprintln(out)
		if step.Output != "" {
			fmt.Fprintf(out, "    %s\n", prompthistory.Preview(step.Output, 180))
		}
	}
}

type prCommentsRequest struct {
	PR     string
	Repo   string
	Format string
}

type installGitHubAppRequest struct {
	Format     string
	SecretName string
	Workflows  []string
	Force      bool
	DryRun     bool
}

type installSlackAppRequest struct {
	Format string
	Target string
	Path   string
	Open   bool
}

type installSlackAppReport struct {
	Kind         string `json:"kind"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	URL          string `json:"url"`
	Opened       bool   `json:"opened"`
	Opener       string `json:"opener,omitempty"`
	InstallCount int    `json:"install_count"`
	Path         string `json:"path,omitempty"`
	Message      string `json:"message,omitempty"`
}

type stickersRequest struct {
	Format string
	Target string
	Path   string
	Open   bool
}

type stickersReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	URL        string `json:"url"`
	Opened     bool   `json:"opened"`
	Opener     string `json:"opener,omitempty"`
	OrderCount int    `json:"order_count"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type extraUsageRequest struct {
	Format string
	Target string
	Path   string
	Open   bool
	Mode   string
}

type extraUsageReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Mode       string `json:"mode"`
	URL        string `json:"url"`
	Opened     bool   `json:"opened"`
	Opener     string `json:"opener,omitempty"`
	VisitCount int    `json:"visit_count"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type passesRequest struct {
	Action      string
	Format      string
	Target      string
	Path        string
	Open        bool
	Docs        bool
	ReferralURL string
}

type passesReport struct {
	Kind        string `json:"kind"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	URL         string `json:"url"`
	DocsURL     string `json:"docs_url"`
	ReferralURL string `json:"referral_url,omitempty"`
	Opened      bool   `json:"opened"`
	Opener      string `json:"opener,omitempty"`
	VisitCount  int    `json:"visit_count"`
	Path        string `json:"path,omitempty"`
	Message     string `json:"message,omitempty"`
}

const slackAppURL = "https://slack.com/marketplace/A08SF47R6P4-claude"
const stickerOrderURL = "https://www.stickermule.com/claudecode"
const extraUsagePersonalURL = "https://claude.ai/settings/usage"
const extraUsageAdminURL = "https://claude.ai/admin-settings/usage"
const guestPassDocsURL = "https://support.claude.com/en/articles/12875061-claude-code-guest-passes"

var openExternalURL = openSystemURL

func (a *App) PRComments(ctx context.Context, args []string) error {
	req, err := parsePRCommentsArgs(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	report, err := githubcomments.Fetch(ctx, githubcomments.Options{
		PR:   req.PR,
		Repo: req.Repo,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	githubcomments.RenderText(a.Out, report)
	return nil
}

func parsePRCommentsArgs(args []string) (prCommentsRequest, error) {
	req := prCommentsRequest{Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("pr-comments output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--repo":
			index++
			if index >= len(args) {
				return req, errors.New("pr-comments repository is required")
			}
			req.Repo = args[index]
		case strings.HasPrefix(arg, "--repo="):
			req.Repo = strings.TrimPrefix(arg, "--repo=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "pr-comments"); err != nil {
		return req, err
	}
	if len(rest) > 1 {
		return req, fmt.Errorf("unexpected pr-comments argument %q", rest[1])
	}
	if len(rest) == 1 {
		req.PR = rest[0]
	}
	return req, nil
}

func (a *App) InstallGitHubApp(args []string) error {
	req, err := parseInstallGitHubAppArgs(args)
	if err != nil {
		return err
	}
	report, err := githubsetup.Setup(githubsetup.Options{
		Workspace:  a.Workspace,
		SecretName: req.SecretName,
		Workflows:  req.Workflows,
		Force:      req.Force,
		DryRun:     req.DryRun,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderInstallGitHubAppReport(a.Out, report)
	return nil
}

func parseInstallGitHubAppArgs(args []string) (installGitHubAppRequest, error) {
	req := installGitHubAppRequest{Format: "text"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("install-github-app output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--secret-name" || arg == "--secret":
			index++
			if index >= len(args) {
				return req, errors.New("GitHub secret name is required")
			}
			req.SecretName = args[index]
		case strings.HasPrefix(arg, "--secret-name="):
			req.SecretName = strings.TrimPrefix(arg, "--secret-name=")
		case strings.HasPrefix(arg, "--secret="):
			req.SecretName = strings.TrimPrefix(arg, "--secret=")
		case arg == "--workflow" || arg == "--workflows":
			index++
			if index >= len(args) {
				return req, errors.New("GitHub workflow name is required")
			}
			req.Workflows = append(req.Workflows, args[index])
		case strings.HasPrefix(arg, "--workflow="):
			req.Workflows = append(req.Workflows, strings.TrimPrefix(arg, "--workflow="))
		case strings.HasPrefix(arg, "--workflows="):
			req.Workflows = append(req.Workflows, strings.TrimPrefix(arg, "--workflows="))
		case arg == "--force":
			req.Force = true
		case arg == "--dry-run":
			req.DryRun = true
		default:
			return req, fmt.Errorf("unknown install-github-app option %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "install-github-app"); err != nil {
		return req, err
	}
	return req, nil
}

func renderInstallGitHubAppReport(out io.Writer, report githubsetup.Report) {
	fmt.Fprintln(out, "GitHub App Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	if report.Repo != "" {
		fmt.Fprintf(out, "  Repository       %s\n", report.Repo)
	}
	fmt.Fprintf(out, "  Secret           %s\n", report.SecretName)
	fmt.Fprintf(out, "  Dry run          %t\n", report.DryRun)
	fmt.Fprintf(out, "  Docs             %s\n", report.DocsURL)
	for _, workflow := range report.Workflows {
		state := "ready"
		switch {
		case workflow.Created:
			state = "created"
		case workflow.Overwritten:
			state = "overwritten"
		case workflow.Exists:
			state = "exists"
		}
		fmt.Fprintf(out, "  Workflow         %s %s %s\n", workflow.Name, state, workflow.Path)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "  Warning          %s\n", warning)
	}
	for _, instruction := range report.Instructions {
		fmt.Fprintf(out, "  Next             %s\n", instruction)
	}
}

func (a *App) InstallSlackApp(args []string) error {
	req, err := parseInstallSlackAppArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	count := a.Config.Future.SlackAppInstallCount + 1
	if _, err := config.SetFileValue(path, "future.slack_app_install_count", count); err != nil {
		return err
	}
	a.Config.Future.SlackAppInstallCount = count
	report := installSlackAppReport{
		Kind:         "install_slack_app",
		Action:       "open",
		Status:       "ok",
		URL:          slackAppURL,
		InstallCount: count,
		Path:         path,
		Message:      "Visit the Slack Marketplace URL to install the Claude app.",
	}
	if req.Open {
		opener, err := openExternalURL(slackAppURL)
		if err != nil {
			report.Status = "open_failed"
			report.Message = "Could not open a browser automatically. Visit the URL manually."
		} else {
			report.Opened = true
			report.Opener = opener
			report.Message = "Opening Slack app installation page in browser."
		}
	} else {
		report.Action = "show"
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderInstallSlackAppReport(a.Out, report)
	return nil
}

func parseInstallSlackAppArgs(args []string) (installSlackAppRequest, error) {
	req := installSlackAppRequest{Format: "text", Target: "user", Open: true}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("install-slack-app output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("install-slack-app target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("install-slack-app config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--open":
			req.Open = true
		case arg == "--no-open":
			req.Open = false
		default:
			return req, fmt.Errorf("unknown install-slack-app option %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "install-slack-app"); err != nil {
		return req, err
	}
	return req, nil
}

func renderInstallSlackAppReport(out io.Writer, report installSlackAppReport) {
	fmt.Fprintln(out, "Slack App Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  URL              %s\n", report.URL)
	fmt.Fprintf(out, "  Opened           %t\n", report.Opened)
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opener           %s\n", report.Opener)
	}
	fmt.Fprintf(out, "  Install count    %d\n", report.InstallCount)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) Stickers(args []string) error {
	req, err := parseStickersArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	count := a.Config.Future.StickerOrderCount + 1
	if _, err := config.SetFileValue(path, "future.sticker_order_count", count); err != nil {
		return err
	}
	a.Config.Future.StickerOrderCount = count
	report := stickersReport{
		Kind:       "stickers",
		Action:     "open",
		Status:     "ok",
		URL:        stickerOrderURL,
		OrderCount: count,
		Path:       path,
		Message:    "Visit the sticker page to order Claude Code stickers.",
	}
	if req.Open {
		opener, err := openExternalURL(stickerOrderURL)
		if err != nil {
			report.Status = "open_failed"
			report.Message = "Could not open a browser automatically. Visit the URL manually."
		} else {
			report.Opened = true
			report.Opener = opener
			report.Message = "Opening sticker order page in browser."
		}
	} else {
		report.Action = "show"
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderStickersReport(a.Out, report)
	return nil
}

func parseStickersArgs(args []string) (stickersRequest, error) {
	req := stickersRequest{Format: "text", Target: "user", Open: true}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("stickers output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("stickers target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("stickers config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--open":
			req.Open = true
		case arg == "--no-open":
			req.Open = false
		default:
			return req, fmt.Errorf("unknown stickers option %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "stickers"); err != nil {
		return req, err
	}
	return req, nil
}

func renderStickersReport(out io.Writer, report stickersReport) {
	fmt.Fprintln(out, "Sticker Order")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  URL              %s\n", report.URL)
	fmt.Fprintf(out, "  Opened           %t\n", report.Opened)
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opener           %s\n", report.Opener)
	}
	fmt.Fprintf(out, "  Order count      %d\n", report.OrderCount)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) ExtraUsage(args []string) error {
	req, err := parseExtraUsageArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	count := a.Config.Future.ExtraUsageVisitCount + 1
	if _, err := config.SetFileValue(path, "future.extra_usage_visit_count", count); err != nil {
		return err
	}
	a.Config.Future.ExtraUsageVisitCount = count

	url := extraUsageURL(req.Mode)
	report := extraUsageReport{
		Kind:       "extra_usage",
		Action:     "open",
		Status:     "ok",
		Mode:       req.Mode,
		URL:        url,
		VisitCount: count,
		Path:       path,
		Message:    extraUsageMessage(req.Mode),
	}
	if req.Open {
		opener, err := openExternalURL(url)
		if err != nil {
			report.Status = "open_failed"
			report.Message = "Could not open a browser automatically. Visit the URL manually."
		} else {
			report.Opened = true
			report.Opener = opener
			report.Message = "Opening Claude usage settings in browser."
		}
	} else {
		report.Action = "show"
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderExtraUsageReport(a.Out, report)
	return nil
}

func parseExtraUsageArgs(args []string) (extraUsageRequest, error) {
	req := extraUsageRequest{Format: "text", Target: "user", Open: true, Mode: "personal"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("extra-usage output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("extra-usage target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("extra-usage config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--open":
			req.Open = true
		case arg == "--no-open":
			req.Open = false
		case arg == "--admin":
			req.Mode = "admin"
		case arg == "--personal":
			req.Mode = "personal"
		case arg == "admin" || arg == "team" || arg == "enterprise" || arg == "org" || arg == "organization":
			req.Mode = "admin"
		case arg == "personal" || arg == "user" || arg == "individual":
			req.Mode = "personal"
		default:
			return req, fmt.Errorf("unknown extra-usage option %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "extra-usage"); err != nil {
		return req, err
	}
	return req, nil
}

func extraUsageURL(mode string) string {
	if mode == "admin" {
		return extraUsageAdminURL
	}
	return extraUsagePersonalURL
}

func extraUsageMessage(mode string) string {
	if mode == "admin" {
		return "Visit Claude admin usage settings to manage organization extra usage."
	}
	return "Visit Claude usage settings to manage extra usage."
}

func renderExtraUsageReport(out io.Writer, report extraUsageReport) {
	fmt.Fprintln(out, "Extra Usage")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Mode             %s\n", report.Mode)
	fmt.Fprintf(out, "  URL              %s\n", report.URL)
	fmt.Fprintf(out, "  Opened           %t\n", report.Opened)
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opener           %s\n", report.Opener)
	}
	fmt.Fprintf(out, "  Visit count      %d\n", report.VisitCount)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) Passes(args []string) error {
	req, err := parsePassesArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	report := passesReport{
		Kind:        "passes",
		Action:      req.Action,
		Status:      "ok",
		DocsURL:     guestPassDocsURL,
		ReferralURL: firstNonEmpty(req.ReferralURL, a.Config.Future.GuestPassReferralURL),
		Path:        path,
	}
	switch req.Action {
	case "set-url":
		if err := validateHTTPURL(req.ReferralURL, "guest pass referral URL"); err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "future.guest_pass_referral_url", req.ReferralURL); err != nil {
			return err
		}
		a.Config.Future.GuestPassReferralURL = req.ReferralURL
		report.ReferralURL = req.ReferralURL
		report.URL = req.ReferralURL
		report.Message = "Guest pass referral URL saved."
	case "clear-url":
		if _, err := config.UnsetFileValue(path, "future.guest_pass_referral_url"); err != nil {
			return err
		}
		a.Config.Future.GuestPassReferralURL = ""
		report.ReferralURL = ""
		report.URL = guestPassDocsURL
		report.Message = "Guest pass referral URL cleared."
	case "show", "open":
		count := a.Config.Future.GuestPassVisitCount + 1
		if _, err := config.SetFileValue(path, "future.guest_pass_visit_count", count); err != nil {
			return err
		}
		a.Config.Future.GuestPassVisitCount = count
		report.VisitCount = count
		report.URL = passesURL(report.ReferralURL, req.Docs)
		if report.ReferralURL == "" || req.Docs {
			report.Message = "No guest pass referral URL is configured. Showing Claude Code guest pass documentation."
		} else {
			report.Message = "Showing configured guest pass referral URL."
		}
		if req.Action == "show" || !req.Open {
			report.Action = "show"
			break
		}
		opener, err := openExternalURL(report.URL)
		if err != nil {
			report.Status = "open_failed"
			report.Message = "Could not open a browser automatically. Visit the URL manually."
		} else {
			report.Opened = true
			report.Opener = opener
			report.Message = "Opening guest pass page in browser."
		}
	default:
		return fmt.Errorf("unknown passes command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderPassesReport(a.Out, report)
	return nil
}

func parsePassesArgs(args []string) (passesRequest, error) {
	req := passesRequest{Action: "open", Format: "text", Target: "user", Open: true}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("passes output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("passes target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("passes config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--open":
			req.Open = true
		case arg == "--no-open":
			req.Open = false
		case arg == "--docs":
			req.Docs = true
		case arg == "--referral-url":
			index++
			if index >= len(args) {
				return req, errors.New("passes referral URL is required")
			}
			req.ReferralURL = args[index]
		case strings.HasPrefix(arg, "--referral-url="):
			req.ReferralURL = strings.TrimPrefix(arg, "--referral-url=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "passes"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "show", "status":
		req.Action = "show"
		req.Open = false
	case "open":
		req.Action = "open"
	case "set-url", "set", "url":
		req.Action = "set-url"
		if req.ReferralURL == "" && len(rest) > 1 {
			req.ReferralURL = rest[1]
		}
		if req.ReferralURL == "" {
			return req, errors.New("passes referral URL is required")
		}
	case "clear-url", "clear", "unset":
		req.Action = "clear-url"
	default:
		return req, fmt.Errorf("unknown passes command %q", rest[0])
	}
	if (req.Action == "show" || req.Action == "open" || req.Action == "clear-url") && len(rest) > 1 {
		return req, fmt.Errorf("unexpected passes argument %q", rest[1])
	}
	if req.Action == "set-url" && len(rest) > 2 {
		return req, fmt.Errorf("unexpected passes argument %q", rest[2])
	}
	return req, nil
}

func passesURL(referralURL string, docs bool) string {
	if docs || strings.TrimSpace(referralURL) == "" {
		return guestPassDocsURL
	}
	return referralURL
}

func validateHTTPURL(raw string, label string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be a valid URL", label)
	}
	switch parsed.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("%s must use http or https", label)
	}
}

func renderPassesReport(out io.Writer, report passesReport) {
	fmt.Fprintln(out, "Guest Passes")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  URL              %s\n", report.URL)
	fmt.Fprintf(out, "  Docs             %s\n", report.DocsURL)
	if report.ReferralURL != "" {
		fmt.Fprintf(out, "  Referral URL     %s\n", report.ReferralURL)
	}
	fmt.Fprintf(out, "  Opened           %t\n", report.Opened)
	if report.Opener != "" {
		fmt.Fprintf(out, "  Opener           %s\n", report.Opener)
	}
	if report.VisitCount != 0 {
		fmt.Fprintf(out, "  Visit count      %d\n", report.VisitCount)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func openSystemURL(url string) (string, error) {
	var command []string
	switch runtime.GOOS {
	case "darwin":
		command = []string{"open", url}
	case "windows":
		command = []string{"rundll32", "url.dll,FileProtocolHandler", url}
	default:
		command = []string{"xdg-open", url}
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		return "", err
	}
	cmd := exec.Command(command[0], command[1:]...)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	return command[0], nil
}

func (a *App) IssueDraft(args []string, overrides config.FlagOverrides) error {
	return a.writeDraft("issue", args, overrides)
}

func (a *App) writeDraft(kind string, args []string, overrides config.FlagOverrides) error {
	req, err := parseDraftArgs(kind, args, overrides)
	if err != nil {
		return err
	}
	active, err := a.feedbackSession(req.SessionID)
	if err != nil {
		return err
	}
	createdAt := time.Now().UTC()
	bundle := draftBundle{
		Kind:       kind,
		CreatedAt:  createdAt,
		Context:    strings.TrimSpace(req.Context),
		Status:     a.statusSnapshot(active),
		GitStatus:  boundedGitOutput(a.Workspace, 12000, "status", "--short", "--branch"),
		DiffStat:   boundedGitOutput(a.Workspace, 12000, "diff", "--stat"),
		StagedStat: boundedGitOutput(a.Workspace, 12000, "diff", "--cached", "--stat"),
		RecentLog:  boundedGitOutput(a.Workspace, 12000, "log", "--oneline", "--decorate", "--max-count=12"),
		Remote:     boundedGitOutput(a.Workspace, 2000, "remote", "get-url", "origin"),
	}
	bundle.Title = draftTitle(kind, bundle)
	path := a.draftOutputPath(kind, req.Output, createdAt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := session.ValidateExportOutputPath(path); err != nil {
		return err
	}
	data := []byte(renderDraftMarkdown(bundle))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	report := draftReport{
		Kind:            kind,
		Action:          "draft",
		Status:          "ok",
		File:            path,
		Bytes:           len(data),
		Title:           bundle.Title,
		Context:         bundle.Context,
		Branch:          bundle.Status.Git.Branch,
		GitClean:        bundle.Status.Git.Clean,
		SessionID:       bundle.Status.Session.ID,
		SessionMessages: bundle.Status.Session.MessageCount,
	}
	if req.Format == "json" {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	renderDraftReport(a.Out, report)
	return nil
}

func parseDraftArgs(kind string, args []string, overrides config.FlagOverrides) (draftRequest, error) {
	req := draftRequest{Format: "text"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	var contextParts []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output format is required", kind)
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--output":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output path is required", kind)
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s session id is required", kind)
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s resume session id is required", kind)
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--context" || arg == "--message":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s context is required", kind)
			}
			contextParts = append(contextParts, args[index])
		case strings.HasPrefix(arg, "--context="):
			contextParts = append(contextParts, strings.TrimPrefix(arg, "--context="))
		case strings.HasPrefix(arg, "--message="):
			contextParts = append(contextParts, strings.TrimPrefix(arg, "--message="))
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown %s flag %q", kind, arg)
		default:
			contextParts = append(contextParts, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, kind); err != nil {
		return req, err
	}
	req.Context = strings.TrimSpace(strings.Join(contextParts, " "))
	return req, nil
}

func (a *App) draftOutputPath(kind, output string, createdAt time.Time) string {
	filename := fmt.Sprintf("%s-%s-%d.md", kind, createdAt.Format("20060102T150405Z"), createdAt.UnixNano())
	if strings.TrimSpace(output) == "" {
		return filepath.Join(a.Workspace, ".codog", "drafts", filename)
	}
	path := a.resolveOutputPath(output)
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path
	}
	return filepath.Join(path, filename)
}

func draftTitle(kind string, bundle draftBundle) string {
	context := prompthistory.Preview(bundle.Context, 72)
	if context != "" {
		if kind == "issue" {
			return "Issue: " + context
		}
		return "PR: " + context
	}
	if kind == "issue" {
		return "Issue: " + emptyAs(bundle.Status.Workspace.Name, "workspace follow-up")
	}
	branch := emptyAs(bundle.Status.Git.Branch, "workspace changes")
	return "PR: " + branch
}

func renderDraftReport(out io.Writer, report draftReport) {
	label := "Pull Request Draft"
	if report.Kind == "issue" {
		label = "Issue Draft"
	}
	fmt.Fprintln(out, label)
	fmt.Fprintf(out, "  File             %s\n", report.File)
	fmt.Fprintf(out, "  Title            %s\n", report.Title)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	if report.Branch != "" {
		fmt.Fprintf(out, "  Branch           %s\n", report.Branch)
	}
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s (%d messages)\n", report.SessionID, report.SessionMessages)
	}
}

func renderDraftMarkdown(bundle draftBundle) string {
	label := "Pull Request Draft"
	if bundle.Kind == "issue" {
		label = "Issue Draft"
	}
	var builder strings.Builder
	builder.WriteString("# " + label + "\n\n")
	builder.WriteString("## Title\n\n")
	builder.WriteString(bundle.Title + "\n\n")
	builder.WriteString("## Context\n\n")
	if bundle.Context == "" {
		builder.WriteString("No additional context provided.\n\n")
	} else {
		builder.WriteString(bundle.Context + "\n\n")
	}
	builder.WriteString("## Workspace\n\n")
	builder.WriteString(fmt.Sprintf("- Created: %s\n", bundle.CreatedAt.Format(time.RFC3339)))
	builder.WriteString(fmt.Sprintf("- Workspace: %s\n", bundle.Status.Workspace.Path))
	builder.WriteString(fmt.Sprintf("- Branch: %s\n", emptyAs(bundle.Status.Git.Branch, "unknown")))
	builder.WriteString(fmt.Sprintf("- Git clean: %t\n", bundle.Status.Git.Clean))
	if bundle.Remote != "" {
		builder.WriteString(fmt.Sprintf("- Origin: %s\n", bundle.Remote))
	}
	if bundle.Status.Session.Active {
		builder.WriteString(fmt.Sprintf("- Session: %s (%d messages)\n", bundle.Status.Session.ID, bundle.Status.Session.MessageCount))
	}
	builder.WriteString("\n## Current Git Status\n\n")
	writeDraftCodeBlock(&builder, bundle.GitStatus)
	builder.WriteString("\n## Unstaged Diff Stat\n\n")
	writeDraftCodeBlock(&builder, emptyAs(bundle.DiffStat, "No unstaged changes."))
	builder.WriteString("\n## Staged Diff Stat\n\n")
	writeDraftCodeBlock(&builder, emptyAs(bundle.StagedStat, "No staged changes."))
	builder.WriteString("\n## Recent Commits\n\n")
	writeDraftCodeBlock(&builder, bundle.RecentLog)
	if bundle.Kind == "pr" {
		builder.WriteString("\n## Checklist\n\n")
		builder.WriteString("- [ ] Tests pass\n")
		builder.WriteString("- [ ] Documentation updated if needed\n")
		builder.WriteString("- [ ] Review risk noted\n")
	} else {
		builder.WriteString("\n## Expected Follow-Up\n\n")
		builder.WriteString("- [ ] Reproduce or confirm the issue\n")
		builder.WriteString("- [ ] Identify affected versions or environments\n")
		builder.WriteString("- [ ] Attach logs, screenshots, or session details if useful\n")
	}
	return builder.String()
}

func writeDraftCodeBlock(builder *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "No data."
	}
	builder.WriteString("```text\n")
	builder.WriteString(text)
	builder.WriteString("\n```\n")
}

func boundedGitOutput(workspace string, limit int, args ...string) string {
	out, err := gitops.Run(workspace, args...)
	if err != nil {
		return err.Error()
	}
	if limit <= 0 {
		limit = 12000
	}
	runes := []rune(strings.TrimSpace(out))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "\n[truncated]"
}

func renderVersion(out io.Writer, workspace string, args []string) error {
	format, err := parseSimpleOutputFormat("version", args)
	if err != nil {
		return err
	}
	report := versioninfo.Build(version, workspace)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	versioninfo.RenderText(out, report)
	fmt.Fprintln(out)
	return nil
}

type acpStatusReport struct {
	SchemaVersion string       `json:"schema_version"`
	Kind          string       `json:"kind"`
	Action        string       `json:"action"`
	Status        string       `json:"status"`
	Supported     bool         `json:"supported"`
	Message       string       `json:"message"`
	LaunchCommand *string      `json:"launch_command"`
	Protocol      acpProtocol  `json:"protocol"`
	Contracts     acpContracts `json:"contracts"`
	Aliases       []string     `json:"aliases"`
}

type acpProtocol struct {
	Name              string  `json:"name"`
	JSONRPC           bool    `json:"json_rpc"`
	Daemon            bool    `json:"daemon"`
	Endpoint          *string `json:"endpoint"`
	ServeStartsDaemon bool    `json:"serve_starts_daemon"`
}

type acpContracts struct {
	BlockingGates             []string `json:"blocking_gates"`
	StableStatusSurface       string   `json:"stable_status_surface"`
	UnsupportedInvocationKind string   `json:"unsupported_invocation_kind"`
}

type acpUnsupportedReport struct {
	SchemaVersion string   `json:"schema_version"`
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	Supported     bool     `json:"supported"`
	Message       string   `json:"message"`
	Invocation    []string `json:"invocation"`
	Hint          string   `json:"hint"`
}

type acpRequest struct {
	Format      string
	Serve       bool
	Unsupported []string
}

func renderACPStatus(out io.Writer, args []string) error {
	req, err := parseACPRequest(args)
	if err != nil {
		return err
	}
	if len(req.Unsupported) > 0 {
		report := acpUnsupportedReport{
			SchemaVersion: "1.0",
			Kind:          "unsupported_acp_invocation",
			Action:        "status",
			Status:        "error",
			Supported:     false,
			Message:       "unsupported ACP invocation. Use `codog acp` for status or `codog acp serve` for stdio JSON-RPC.",
			Invocation:    append([]string(nil), args...),
			Hint:          "Start the editor bridge with `codog acp serve`, then send line-delimited JSON-RPC requests on stdin.",
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
		}
		return fmt.Errorf("unsupported_acp_invocation: unsupported ACP invocation %q", strings.Join(req.Unsupported, " "))
	}
	report := buildACPStatusReport()
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "ACP / Zed")
	fmt.Fprintln(out, "  Status           ok")
	fmt.Fprintln(out, "  Supported        true")
	fmt.Fprintln(out, "  Serve            codog acp serve")
	fmt.Fprintln(out, "  Protocol         stdio JSON-RPC")
	fmt.Fprintln(out, "  Surface          initialize, status, session/new, session/list, session/get, session/history, session/rename, session/delete, prompt, shutdown")
	fmt.Fprintln(out, "  Message          "+report.Message)
	return nil
}

func buildACPStatusReport() acpStatusReport {
	return acpStatusReport{
		SchemaVersion: "1.0",
		Kind:          "acp",
		Action:        "status",
		Status:        "ok",
		Supported:     true,
		Message:       "ACP/Zed editor integration is available over stdio JSON-RPC. Start it with `codog acp serve` and use initialize, status, session/new, session/list, session/get, session/history, session/rename, session/delete, prompt, and shutdown requests.",
		LaunchCommand: stringPtr("codog acp serve"),
		Protocol: acpProtocol{
			Name:              "ACP/Zed",
			JSONRPC:           true,
			Daemon:            false,
			Endpoint:          stringPtr("stdio"),
			ServeStartsDaemon: true,
		},
		Contracts: acpContracts{
			BlockingGates: []string{
				"initialize",
				"session/new",
				"prompt",
				"shutdown",
			},
			StableStatusSurface:       "codog acp --output-format json",
			UnsupportedInvocationKind: "unsupported_acp_invocation",
		},
		Aliases: []string{"acp", "--acp", "-acp"},
	}
}

func parseACPGlobalInvocation(args []string) ([]string, bool, error) {
	if len(args) == 0 {
		return nil, false, nil
	}
	switch {
	case args[0] == "--json":
		if len(args) >= 2 && args[1] == "acp" {
			acpArgs := append([]string{"--json"}, args[2:]...)
			return acpArgs, true, nil
		}
	case args[0] == "--output-format" || args[0] == "-o":
		if len(args) < 2 {
			return nil, false, nil
		}
		if len(args) >= 3 && args[2] == "acp" {
			acpArgs := append([]string{args[0], args[1]}, args[3:]...)
			return acpArgs, true, nil
		}
	case strings.HasPrefix(args[0], "--output-format="):
		if len(args) >= 2 && args[1] == "acp" {
			acpArgs := append([]string{args[0]}, args[2:]...)
			return acpArgs, true, nil
		}
	}
	return nil, false, nil
}

func parseACPArgs(args []string) (string, []string, error) {
	req, err := parseACPRequest(args)
	if err != nil {
		return "", nil, err
	}
	return req.Format, req.Unsupported, nil
}

func parseACPRequest(args []string) (acpRequest, error) {
	req := acpRequest{Format: "text"}
	serveSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "serve":
			if serveSeen {
				req.Unsupported = append(req.Unsupported, arg)
			}
			serveSeen = true
			req.Serve = true
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return acpRequest{}, errors.New("acp output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		default:
			req.Unsupported = append(req.Unsupported, arg)
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return acpRequest{}, fmt.Errorf("unknown acp output format %q", req.Format)
	}
}

func acpServeRequested(args []string) bool {
	for _, arg := range args {
		if arg == "serve" {
			return true
		}
	}
	return false
}

func stringPtr(value string) *string {
	return &value
}

func (a *App) ACP(ctx context.Context, args []string) error {
	req, err := parseACPRequest(args)
	if err != nil {
		return err
	}
	if len(req.Unsupported) != 0 || !req.Serve {
		return renderACPStatus(a.Out, args)
	}
	return a.serveACP(ctx)
}

func (a *App) serveACP(ctx context.Context) error {
	in := a.In
	if in == nil {
		in = os.Stdin
	}
	out := a.Out
	if out == nil {
		out = os.Stdout
	}
	return acpserver.Serve(ctx, in, out, acpserver.Handlers{
		NewSession: func(context.Context) (acpserver.SessionInfo, error) {
			if a.Sessions == nil {
				return acpserver.SessionInfo{}, errors.New("session store is unavailable")
			}
			sess, err := a.Sessions.Open("")
			if err != nil {
				return acpserver.SessionInfo{}, err
			}
			return acpserver.SessionInfo{SessionID: sess.ID, Workspace: a.Workspace}, nil
		},
		ListSessions: func(context.Context) (acpserver.SessionList, error) {
			if a.Sessions == nil {
				return acpserver.SessionList{}, errors.New("session store is unavailable")
			}
			sessions, err := a.Sessions.List()
			if err != nil {
				return acpserver.SessionList{}, err
			}
			summaries := make([]acpserver.SessionSummary, 0, len(sessions))
			for _, sess := range sessions {
				summaries = append(summaries, acpserver.SessionSummary{
					SessionID:    sess.ID,
					Workspace:    a.Workspace,
					Path:         sess.Path,
					MessageCount: len(sess.Messages),
				})
			}
			return acpserver.SessionList{
				Kind:      "session_list",
				Count:     len(summaries),
				Sessions:  summaries,
				Workspace: a.Workspace,
			}, nil
		},
		GetSession: func(_ context.Context, req acpserver.SessionLookupRequest) (acpserver.SessionDetail, error) {
			if a.Sessions == nil {
				return acpserver.SessionDetail{}, errors.New("session store is unavailable")
			}
			sess, err := a.Sessions.Open(req.SessionID)
			if err != nil {
				return acpserver.SessionDetail{}, err
			}
			return acpserver.SessionDetail{
				SessionID:    sess.ID,
				Workspace:    a.Workspace,
				Path:         sess.Path,
				MessageCount: len(sess.Messages),
				Messages:     sess.Messages,
			}, nil
		},
		History: func(_ context.Context, req acpserver.SessionHistoryRequest) (acpserver.SessionHistory, error) {
			if a.Sessions == nil {
				return acpserver.SessionHistory{}, errors.New("session store is unavailable")
			}
			sessionID := strings.TrimSpace(req.SessionID)
			if sessionID == "" || sessionID == "latest" {
				latest, err := a.Sessions.LatestID()
				if err != nil {
					return acpserver.SessionHistory{}, err
				}
				sessionID = latest
			}
			entries, err := a.Sessions.PromptHistory(sessionID)
			if err != nil {
				return acpserver.SessionHistory{}, err
			}
			entries = limitACPHistory(entries, req.Limit)
			return acpserver.SessionHistory{
				Kind:      "session_history",
				SessionID: sessionID,
				Count:     len(entries),
				Entries:   entries,
			}, nil
		},
		RenameSession: func(_ context.Context, req acpserver.SessionRenameRequest) (acpserver.SessionMutationResult, error) {
			if a.Sessions == nil {
				return acpserver.SessionMutationResult{}, errors.New("session store is unavailable")
			}
			result, err := a.Sessions.Rename(req.SessionID, req.NewSessionID)
			if err != nil {
				return acpserver.SessionMutationResult{}, err
			}
			return acpserver.SessionMutationResult{
				Kind:         "session_mutation",
				Action:       "rename",
				Status:       "ok",
				SessionID:    result.OldID,
				NewSessionID: result.NewID,
				Path:         result.NewPath,
				MessageCount: result.MessageCount,
			}, nil
		},
		DeleteSession: func(_ context.Context, req acpserver.SessionLookupRequest) (acpserver.SessionMutationResult, error) {
			if a.Sessions == nil {
				return acpserver.SessionMutationResult{}, errors.New("session store is unavailable")
			}
			sessionID := strings.TrimSpace(req.SessionID)
			if sessionID == "" || sessionID == "latest" {
				latest, err := a.Sessions.LatestID()
				if err != nil {
					return acpserver.SessionMutationResult{}, err
				}
				sessionID = latest
			}
			sess, err := a.Sessions.Open(sessionID)
			if err != nil {
				return acpserver.SessionMutationResult{}, err
			}
			if err := a.Sessions.Delete(sessionID); err != nil {
				return acpserver.SessionMutationResult{}, err
			}
			return acpserver.SessionMutationResult{
				Kind:         "session_mutation",
				Action:       "delete",
				Status:       "ok",
				SessionID:    sess.ID,
				Path:         sess.Path,
				MessageCount: len(sess.Messages),
			}, nil
		},
		Prompt: func(ctx context.Context, req acpserver.PromptRequest) (acpserver.PromptResult, error) {
			if a.Sessions == nil {
				return acpserver.PromptResult{}, errors.New("session store is unavailable")
			}
			if a.Tools == nil {
				return acpserver.PromptResult{}, errors.New("tool registry is not initialized")
			}
			if err := a.RegisterMCPTools(ctx); err != nil {
				return acpserver.PromptResult{}, err
			}
			sess, err := a.Sessions.Open(req.SessionID)
			if err != nil {
				return acpserver.PromptResult{}, err
			}
			if err := a.runSessionStartHook(ctx, sess, "acp"); err != nil {
				return acpserver.PromptResult{}, err
			}
			var streamed bytes.Buffer
			previousOut := a.Out
			a.Out = &streamed
			defer func() {
				a.Out = previousOut
			}()
			if err := a.runSessionTurn(ctx, "acp", sess, req.Prompt, "completed"); err != nil {
				return acpserver.PromptResult{}, err
			}
			output := strings.TrimSpace(streamed.String())
			if output == "" {
				output = acpLastAssistantText(sess.Messages)
			}
			return acpserver.PromptResult{SessionID: sess.ID, Output: output}, nil
		},
		Status: func(context.Context) (any, error) {
			return buildACPStatusReport(), nil
		},
	}, acpserver.Options{Version: version, Workspace: a.Workspace})
}

func acpLastAssistantText(messages []anthropic.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		var out strings.Builder
		for _, block := range messages[i].Content {
			if block.Type == "text" {
				out.WriteString(block.Text)
			}
		}
		return strings.TrimSpace(out.String())
	}
	return ""
}

func limitACPHistory(entries []session.PromptEntry, limit int) []session.PromptEntry {
	if limit <= 0 || len(entries) <= limit {
		return entries
	}
	return entries[len(entries)-limit:]
}

func initProject(out io.Writer, workspace string, args []string, setupHook func(projectinit.Report) error) error {
	format, err := parseSimpleOutputFormat("init", args)
	if err != nil {
		return err
	}
	report, err := projectinit.Initialize(workspace)
	if err != nil {
		return err
	}
	if setupHook != nil {
		if err := setupHook(report); err != nil {
			return err
		}
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, projectinit.RenderText(report))
	return nil
}

type memoryRequest struct {
	Action string
	Format string
	Editor string
	NoOpen bool
	Rest   []string
}

func renderMemoryCommand(out io.Writer, workspace string, args []string) error {
	req, err := parseMemoryArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "list":
		report, err := memory.BuildReport(workspace)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		memory.RenderReport(out, report)
	case "show":
		report, err := memory.Show(workspace, strings.Join(req.Rest, " "))
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		memory.RenderShowReport(out, report)
	case "add":
		if len(req.Rest) == 0 {
			return errors.New("usage: codog memory add TEXT [--json]")
		}
		report, err := memory.Append(workspace, strings.Join(req.Rest, " "))
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		memory.RenderAppendReport(out, report)
	case "path":
		report, err := memory.Path(workspace, strings.Join(req.Rest, " "))
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		memory.RenderFileReport(out, report)
	case "ensure":
		report, err := memory.Ensure(workspace, strings.Join(req.Rest, " "))
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		memory.RenderFileReport(out, report)
	case "edit":
		report, err := memory.Edit(workspace, strings.Join(req.Rest, " "), req.Editor, !req.NoOpen)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
			return nil
		}
		memory.RenderFileReport(out, report)
	default:
		return fmt.Errorf("unknown memory action %q", req.Action)
	}
	return nil
}

func parseMemoryArgs(args []string) (memoryRequest, error) {
	req := memoryRequest{Action: "list", Format: "text"}
	actionSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--no-open":
			req.NoOpen = true
		case arg == "--editor":
			if i+1 >= len(args) {
				return req, errors.New("memory editor is required")
			}
			i++
			req.Editor = args[i]
		case strings.HasPrefix(arg, "--editor="):
			req.Editor = strings.TrimPrefix(arg, "--editor=")
		case arg == "--output-format":
			if i+1 >= len(args) {
				return req, errors.New("memory output format is required")
			}
			i++
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown memory flag %q", arg)
		default:
			if !actionSet {
				action := strings.ToLower(arg)
				switch action {
				case "list", "show", "add", "path", "ensure", "edit":
					req.Action = action
					actionSet = true
					continue
				default:
					return req, fmt.Errorf("unknown memory action %q", arg)
				}
			}
			req.Rest = append(req.Rest, arg)
		}
	}
	if req.Format != "text" && req.Format != "json" {
		return req, fmt.Errorf("unknown memory output format %q", req.Format)
	}
	return req, nil
}

func renderWorkerState(out io.Writer, workspace string, args []string) error {
	format, err := parseSimpleOutputFormat("state", args)
	if err != nil {
		return err
	}
	state, err := workerstate.Load(workspace)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(state, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	workerstate.RenderText(out, state)
	return nil
}

func (a *App) Status(args []string, overrides config.FlagOverrides) error {
	format, err := parseSimpleOutputFormat("status", args)
	if err != nil {
		return err
	}
	var active *session.Session
	sessionRef := overrides.Resume
	if sessionRef == "" {
		sessionRef = overrides.SessionID
	}
	if sessionRef != "" && a.Sessions != nil {
		active, err = a.Sessions.Open(sessionRef)
		if err != nil {
			return err
		}
	}
	a.renderStatus(format, active)
	return nil
}

type statuslineReport struct {
	Kind            string `json:"kind"`
	Line            string `json:"line"`
	Status          string `json:"status"`
	Workspace       string `json:"workspace"`
	Model           string `json:"model"`
	FastMode        bool   `json:"fast_mode"`
	PermissionMode  string `json:"permission_mode"`
	SessionActive   bool   `json:"session_active"`
	SessionID       string `json:"session_id,omitempty"`
	SessionMessages int    `json:"session_messages,omitempty"`
	SessionCount    int    `json:"session_count"`
	GitAvailable    bool   `json:"git_available"`
	GitBranch       string `json:"git_branch,omitempty"`
	GitClean        bool   `json:"git_clean"`
	GitDirty        bool   `json:"git_dirty"`
	GitConflicts    int    `json:"git_conflicts,omitempty"`
	PlanActive      bool   `json:"plan_active"`
}

func (a *App) Statusline(args []string, overrides config.FlagOverrides) error {
	format, err := parseSimpleOutputFormat("statusline", args)
	if err != nil {
		return err
	}
	active, err := a.contextSession(overrides)
	if err != nil {
		return err
	}
	report := buildStatuslineReport(a.statusSnapshot(active))
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, report.Line)
	return nil
}

type terminalSetupRequest struct {
	Action string
	Format string
	Shell  string
	Path   string
	Force  bool
}

func (a *App) TerminalSetup(args []string) error {
	req, err := parseTerminalSetupArgs(args)
	if err != nil {
		return err
	}
	report, err := terminalsetup.Run(terminalsetup.Options{
		Action: req.Action,
		Shell:  req.Shell,
		Path:   req.Path,
		Force:  req.Force,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTerminalSetupReport(a.Out, report)
	return nil
}

func parseTerminalSetupArgs(args []string) (terminalSetupRequest, error) {
	req := terminalSetupRequest{Action: "status", Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("terminal-setup output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--shell":
			index++
			if index >= len(args) {
				return req, errors.New("terminal-setup shell is required")
			}
			req.Shell = args[index]
		case strings.HasPrefix(arg, "--shell="):
			req.Shell = strings.TrimPrefix(arg, "--shell=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("terminal-setup path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--force":
			req.Force = true
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "terminal-setup"); err != nil {
		return req, err
	}
	if len(rest) > 1 {
		return req, fmt.Errorf("unexpected terminal-setup argument %q", rest[1])
	}
	if len(rest) == 1 {
		req.Action = strings.ToLower(rest[0])
	}
	return req, nil
}

func renderTerminalSetupReport(out io.Writer, report terminalsetup.Report) {
	fmt.Fprintln(out, "Terminal Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Shell            %s\n", report.Shell)
	if report.Path != "" {
		fmt.Fprintf(out, "  Path             %s\n", report.Path)
	}
	fmt.Fprintf(out, "  Installed        %t\n", report.Installed)
	if report.Action == "install" || report.Action == "uninstall" {
		fmt.Fprintf(out, "  Changed          %t\n", report.Changed)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
	if report.Action == "snippet" && report.Snippet != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, report.Snippet)
	}
}

func buildStatuslineReport(snapshot localstatus.Snapshot) statuslineReport {
	workspace := snapshot.Workspace.Name
	if strings.TrimSpace(workspace) == "" {
		workspace = filepath.Base(snapshot.Workspace.Path)
	}
	if workspace == "." || workspace == string(filepath.Separator) {
		workspace = "workspace"
	}
	gitLabel := "no-git"
	gitDirty := false
	if snapshot.Git.Available {
		gitLabel = emptyAs(snapshot.Git.Branch, "detached")
		switch {
		case snapshot.Git.Conflicts > 0:
			gitLabel += "!"
			gitDirty = true
		case !snapshot.Git.Clean:
			gitLabel += "*"
			gitDirty = true
		}
	}
	sessionLabel := fmt.Sprintf("sessions=%d", snapshot.Session.SavedCount)
	if snapshot.Session.Active {
		sessionLabel = fmt.Sprintf("session=%s(%d)", snapshot.Session.ID, snapshot.Session.MessageCount)
	}
	planLabel := "plan=off"
	if snapshot.Plan.Active {
		planLabel = "plan=on"
	}
	fastLabel := "fast=off"
	if snapshot.Config.FastMode {
		fastLabel = "fast=on"
	}
	line := strings.Join([]string{
		"codog",
		workspace,
		gitLabel,
		emptyAs(snapshot.Config.Model, "model=unset"),
		fastLabel,
		emptyAs(snapshot.Config.PermissionMode, "permission=unset"),
		sessionLabel,
		planLabel,
	}, " ")
	return statuslineReport{
		Kind:            "statusline",
		Line:            line,
		Status:          snapshot.Status,
		Workspace:       workspace,
		Model:           snapshot.Config.Model,
		FastMode:        snapshot.Config.FastMode,
		PermissionMode:  snapshot.Config.PermissionMode,
		SessionActive:   snapshot.Session.Active,
		SessionID:       snapshot.Session.ID,
		SessionMessages: snapshot.Session.MessageCount,
		SessionCount:    snapshot.Session.SavedCount,
		GitAvailable:    snapshot.Git.Available,
		GitBranch:       snapshot.Git.Branch,
		GitClean:        snapshot.Git.Clean,
		GitDirty:        gitDirty,
		GitConflicts:    snapshot.Git.Conflicts,
		PlanActive:      snapshot.Plan.Active,
	}
}

func (a *App) Context(args []string, overrides config.FlagOverrides) error {
	format, err := parseSimpleOutputFormat("context", args)
	if err != nil {
		return err
	}
	active, err := a.contextSession(overrides)
	if err != nil {
		return err
	}
	report := a.buildContextReport(active)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	contextview.RenderText(a.Out, report)
	return nil
}

type contextVizRequest struct {
	Format string
	Output string
}

type contextVizReport struct {
	Kind    string             `json:"kind"`
	Action  string             `json:"action"`
	Status  string             `json:"status"`
	File    string             `json:"file"`
	Bytes   int                `json:"bytes"`
	Context contextview.Report `json:"context"`
}

func (a *App) ContextViz(args []string, overrides config.FlagOverrides) error {
	req, err := parseContextVizArgs(args)
	if err != nil {
		return err
	}
	active, err := a.contextSession(overrides)
	if err != nil {
		return err
	}
	contextReport := a.buildContextReport(active)
	html := []byte(contextview.RenderHTML(contextReport))
	output := req.Output
	if output == "" {
		output = filepath.Join(".codog", "context-viz.html")
	}
	path := a.resolveOutputPath(output)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, html, 0o644); err != nil {
		return err
	}
	report := contextVizReport{
		Kind:    "ctx_viz",
		Action:  "write",
		Status:  "ok",
		File:    path,
		Bytes:   len(html),
		Context: contextReport,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderContextVizReport(a.Out, report)
	return nil
}

func parseContextVizArgs(args []string) (contextVizRequest, error) {
	req := contextVizRequest{Format: "text"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("ctx_viz output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--output" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("ctx_viz output path is required")
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown ctx_viz flag %q", arg)
		default:
			if req.Output != "" {
				return req, fmt.Errorf("unexpected ctx_viz argument %q", arg)
			}
			req.Output = arg
		}
	}
	if err := validateTextOrJSON(req.Format, "ctx_viz"); err != nil {
		return req, err
	}
	return req, nil
}

func renderContextVizReport(out io.Writer, report contextVizReport) {
	fmt.Fprintln(out, "Context Viz")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  File             %s\n", report.File)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	fmt.Fprintf(out, "  Context status   %s\n", report.Context.Status)
}

func (a *App) contextSession(overrides config.FlagOverrides) (*session.Session, error) {
	sessionRef := overrides.Resume
	if sessionRef == "" {
		sessionRef = overrides.SessionID
	}
	if sessionRef == "" || a.Sessions == nil {
		return nil, nil
	}
	return a.Sessions.Open(sessionRef)
}

func (a *App) buildContextReport(active *session.Session) contextview.Report {
	var warnings []string
	memoryReport, err := memory.BuildReport(a.Workspace)
	if err != nil {
		warnings = append(warnings, "memory: "+err.Error())
	}
	focusReport, err := focus.BuildReport(a.Workspace)
	if err != nil {
		warnings = append(warnings, "focus: "+err.Error())
	}
	var tokenEstimate usage.Summary
	if active != nil {
		tokenEstimate = usage.Estimate(active.Messages, a.Config.Model)
	}
	return contextview.Build(contextview.Options{
		Status:       a.statusSnapshot(active),
		Memory:       memoryReport,
		Focus:        focusReport,
		TokenUsage:   tokenEstimate,
		SystemPrompt: a.systemPrompt(),
		Warnings:     warnings,
	})
}

type planRequest struct {
	Action string
	Format string
	Text   string
}

func (a *App) Plan(args []string) error {
	req, err := parsePlanArgs(args)
	if err != nil {
		return err
	}
	var report planmode.Report
	switch req.Action {
	case "show":
		report, err = planmode.Show(a.Workspace)
	case "enter":
		report, err = planmode.Enter(a.Workspace, req.Text)
	case "set":
		report, err = planmode.Set(a.Workspace, req.Text)
	case "exit":
		report, err = planmode.Exit(a.Workspace)
	case "clear":
		report, err = planmode.Clear(a.Workspace)
	default:
		return fmt.Errorf("unknown plan action %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	planmode.RenderText(a.Out, report)
	return nil
}

type historyRequest struct {
	SessionID string
	Format    string
	Limit     int
}

type summaryRequest struct {
	SessionID string
	Format    string
}

func (a *App) History(args []string, overrides config.FlagOverrides) error {
	req, err := parseHistoryArgs(args, overrides)
	if err != nil {
		return err
	}
	sessionID := req.SessionID
	if sessionID == "latest" {
		latest, err := a.Sessions.LatestID()
		if errors.Is(err, session.ErrNoSessions) {
			return a.renderPromptHistory(req.Format, "", nil, req.Limit)
		}
		if err != nil {
			return err
		}
		sessionID = latest
	}
	entries, err := a.Sessions.PromptHistory(sessionID)
	if err != nil {
		return err
	}
	return a.renderPromptHistory(req.Format, sessionID, entries, req.Limit)
}

func (a *App) Summary(args []string, overrides config.FlagOverrides) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseSummaryArgs(args, overrides)
	if err != nil {
		return err
	}
	sess, err := a.Sessions.Open(req.SessionID)
	if err != nil {
		return err
	}
	report := sessionsummary.Build(sess.ID, sess.Path, a.Config.Model, sess.Messages)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	sessionsummary.RenderText(a.Out, report)
	return nil
}

type rewindRequest struct {
	SessionID string
	Format    string
	Messages  int
}

type rewindReport struct {
	Kind   string               `json:"kind"`
	Action string               `json:"action"`
	Status string               `json:"status"`
	Result session.RewindResult `json:"result"`
}

func (a *App) Rewind(args []string, overrides config.FlagOverrides) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseRewindArgs(args, overrides, "")
	if err != nil {
		return err
	}
	if req.SessionID == "" {
		req.SessionID = "latest"
	}
	result, err := a.Sessions.Rewind(req.SessionID, req.Messages)
	if err != nil {
		return err
	}
	a.renderRewindReport(req.Format, result)
	return nil
}

func (a *App) renderRewindReport(format string, result session.RewindResult) {
	report := rewindReport{
		Kind:   "rewind",
		Action: "rewind",
		Status: "ok",
		Result: result,
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	fmt.Fprintln(a.Out, "Rewind")
	fmt.Fprintf(a.Out, "  Session          %s\n", result.SessionID)
	fmt.Fprintf(a.Out, "  Removed          %d\n", result.RemovedMessages)
	fmt.Fprintf(a.Out, "  Remaining        %d\n", result.RemainingMessages)
	fmt.Fprintf(a.Out, "  Path             %s\n", result.Path)
}

func (a *App) Undo(args []string) error {
	format, err := parseSimpleOutputFormat("undo", args)
	if err != nil {
		return err
	}
	report, err := undo.RestoreLast(a.Workspace)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Undo")
	fmt.Fprintf(a.Out, "  Tool             %s\n", emptyAsNone(report.Tool))
	fmt.Fprintf(a.Out, "  Path             %s\n", report.Path)
	if report.Restored {
		fmt.Fprintf(a.Out, "  Restored         true\n")
		fmt.Fprintf(a.Out, "  Bytes            %d\n", report.Bytes)
	}
	if report.Removed {
		fmt.Fprintf(a.Out, "  Removed          true\n")
	}
	fmt.Fprintf(a.Out, "  Remaining        %d\n", report.Remaining)
	return nil
}

func (a *App) renderPromptHistory(format string, sessionID string, entries []session.PromptEntry, limit int) error {
	report := prompthistory.Build(sessionID, entries, limit)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	prompthistory.RenderText(a.Out, report)
	return nil
}

func (a *App) renderStatus(format string, active *session.Session) {
	snapshot := a.statusSnapshot(active)
	if format == "json" {
		data, _ := json.MarshalIndent(snapshot, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	localstatus.RenderText(a.Out, snapshot)
}

func (a *App) statusSnapshot(active *session.Session) localstatus.Snapshot {
	sessionCount := -1
	if a.Sessions != nil {
		sessions, err := a.Sessions.List()
		if err == nil {
			sessionCount = len(sessions)
		}
	}
	var sessionID, sessionPath string
	var sessionMessages int
	if active != nil {
		sessionID = active.ID
		sessionPath = active.Path
		sessionMessages = len(active.Messages)
	}
	var toolNames []string
	if a.Tools != nil {
		for _, def := range a.Tools.Definitions() {
			toolNames = append(toolNames, def.Name)
		}
	}
	memoryFiles, _ := memory.Discover(a.Workspace)
	memoryStatuses := make([]localstatus.MemoryFileStatus, 0, len(memoryFiles))
	for _, file := range memoryFiles {
		memoryStatuses = append(memoryStatuses, localstatus.MemoryFileStatus{
			Path:      file.Path,
			Name:      file.Name,
			Scope:     file.Scope,
			Chars:     file.Chars,
			Truncated: file.Truncated,
		})
	}
	gitRaw, gitErr := gitops.Status(a.Workspace)
	gitError := ""
	if gitErr != nil {
		gitError = gitErr.Error()
	}
	var gitFreshness *gitops.BranchFreshness
	if gitErr == nil {
		if freshness, err := gitops.CheckBranchFreshness(a.Workspace, "", "main"); err == nil {
			gitFreshness = &freshness
		}
	}
	sandboxStatus := sandbox.Detect()
	executable := ""
	if path, err := os.Executable(); err == nil {
		executable = path
	}
	planState, _ := planmode.Load(a.Workspace)
	return localstatus.Build(localstatus.Options{
		Version:                     version,
		Workspace:                   a.Workspace,
		ConfigHome:                  a.Config.ConfigHome,
		Model:                       a.Config.Model,
		FastMode:                    fastModeEnabled(a.Config.FastMode),
		BaseURL:                     a.Config.BaseURL,
		PermissionMode:              a.Config.PermissionMode,
		MaxTokens:                   a.Config.MaxTokens,
		MaxTurns:                    a.Config.MaxTurns,
		AutoCompactMessages:         a.Config.AutoCompactMessages,
		AuthConfigured:              a.Config.APIKey != "" || a.Config.AuthToken != "",
		MCPServerCount:              len(a.Config.MCPServers),
		UserPromptSubmitHookCount:   len(a.Config.Hooks.UserPromptSubmit),
		SessionStartHookCount:       len(a.Config.Hooks.SessionStart),
		SessionEndHookCount:         len(a.Config.Hooks.SessionEnd),
		SetupHookCount:              len(a.Config.Hooks.Setup),
		PreHookCount:                len(a.Config.Hooks.PreToolUse),
		PostHookCount:               len(a.Config.Hooks.PostToolUse),
		PostFailureHookCount:        len(a.Config.Hooks.PostToolUseFailure),
		PermissionRequestHookCount:  len(a.Config.Hooks.PermissionRequest),
		PermissionDeniedHookCount:   len(a.Config.Hooks.PermissionDenied),
		StopHookCount:               len(a.Config.Hooks.Stop),
		StopFailureHookCount:        len(a.Config.Hooks.StopFailure),
		PreCompactHookCount:         len(a.Config.Hooks.PreCompact),
		PostCompactHookCount:        len(a.Config.Hooks.PostCompact),
		NotificationHookCount:       len(a.Config.Hooks.Notification),
		SubagentStartHookCount:      len(a.Config.Hooks.SubagentStart),
		SubagentStopHookCount:       len(a.Config.Hooks.SubagentStop),
		WorktreeCreateHookCount:     len(a.Config.Hooks.WorktreeCreate),
		WorktreeRemoveHookCount:     len(a.Config.Hooks.WorktreeRemove),
		CwdChangedHookCount:         len(a.Config.Hooks.CwdChanged),
		TaskCreatedHookCount:        len(a.Config.Hooks.TaskCreated),
		TaskCompletedHookCount:      len(a.Config.Hooks.TaskCompleted),
		InstructionsLoadedHookCount: len(a.Config.Hooks.InstructionsLoaded),
		FileChangedHookCount:        len(a.Config.Hooks.FileChanged),
		EnabledSkillCount:           len(a.Config.EnabledSkills),
		PlanActive:                  planState.Active,
		PlanText:                    planState.Plan,
		PlanUpdatedAt:               planState.UpdatedAt,
		MemoryFiles:                 memoryStatuses,
		ToolNames:                   toolNames,
		SessionID:                   sessionID,
		SessionPath:                 sessionPath,
		SessionMessages:             sessionMessages,
		SessionCount:                sessionCount,
		GitStatus:                   gitRaw,
		GitError:                    gitError,
		GitFreshness:                gitFreshness,
		SandboxOS:                   sandboxStatus.OS,
		SandboxDefault:              sandboxStatus.Default,
		SandboxStrategies:           sandboxStatus.Strategies,
		SandboxAvailable:            sandboxStatus.Available,
		Executable:                  executable,
	})
}

type capabilitiesReport struct {
	Kind              string            `json:"kind"`
	Action            string            `json:"action"`
	Status            string            `json:"status"`
	Version           string            `json:"version"`
	Workspace         string            `json:"workspace"`
	Model             string            `json:"model"`
	PermissionMode    string            `json:"permission_mode"`
	CommandCount      int               `json:"command_count"`
	Commands          []string          `json:"commands"`
	SlashCommandCount int               `json:"slash_command_count"`
	SlashCommands     []capabilitySlash `json:"slash_commands"`
	ToolCount         int               `json:"tool_count"`
	Tools             []capabilityTool  `json:"tools"`
	MCP               capabilityMCP     `json:"mcp"`
	Features          []string          `json:"features"`
	Protocols         []string          `json:"protocols"`
	OutputFormats     []string          `json:"output_formats"`
}

type capabilitySlash struct {
	Name        string `json:"name"`
	Usage       string `json:"usage"`
	Description string `json:"description"`
	Hidden      bool   `json:"hidden,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}

type capabilityTool struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Permission     string         `json:"permission"`
	ExposedOverMCP bool           `json:"exposed_over_mcp"`
	InputSchema    map[string]any `json:"input_schema,omitempty"`
}

type capabilityMCP struct {
	ConfiguredServerCount  int              `json:"configured_server_count"`
	ConfiguredServers      []string         `json:"configured_servers"`
	LocalResourceCount     int              `json:"local_resource_count"`
	LocalResources         []map[string]any `json:"local_resources"`
	LocalTemplateCount     int              `json:"local_resource_template_count"`
	LocalResourceTemplates []map[string]any `json:"local_resource_templates"`
	LocalPromptCount       int              `json:"local_prompt_count"`
	LocalPrompts           []map[string]any `json:"local_prompts"`
	ExposedToolCount       int              `json:"exposed_tool_count"`
}

func (a *App) Capabilities(args []string) error {
	format, err := parseSimpleOutputFormat("capabilities", args)
	if err != nil {
		return err
	}
	report := a.capabilitiesReport()
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCapabilitiesText(a.Out, report)
	return nil
}

func (a *App) capabilitiesReport() capabilitiesReport {
	commands := builtInCommandNames()
	slashCommands := slashCapabilities()
	toolInfos := []tools.ToolInfo{}
	if a.Tools != nil {
		toolInfos = a.Tools.Infos()
	}
	exposed := mcpserver.ExposedTools(a.Tools)
	exposedNames := map[string]bool{}
	for _, tool := range exposed {
		if name, ok := tool["name"].(string); ok && name != "" {
			exposedNames[name] = true
		}
	}
	capTools := make([]capabilityTool, 0, len(toolInfos))
	for _, info := range toolInfos {
		capTools = append(capTools, capabilityTool{
			Name:           info.Name,
			Description:    info.Description,
			Permission:     string(info.Permission),
			ExposedOverMCP: exposedNames[info.Name],
			InputSchema:    info.InputSchema,
		})
	}
	localResources := mcpserver.LocalResources(a.mcpServerOptions())
	localTemplates := mcpserver.LocalResourceTemplates()
	localPrompts := mcpserver.LocalPrompts()
	return capabilitiesReport{
		Kind:              "capabilities",
		Action:            "show",
		Status:            "ok",
		Version:           version,
		Workspace:         a.Workspace,
		Model:             a.Config.Model,
		PermissionMode:    a.Config.PermissionMode,
		CommandCount:      len(commands),
		Commands:          commands,
		SlashCommandCount: len(slashCommands),
		SlashCommands:     slashCommands,
		ToolCount:         len(capTools),
		Tools:             capTools,
		MCP: capabilityMCP{
			ConfiguredServerCount:  len(a.Config.MCPServers),
			ConfiguredServers:      sortedMCPServerNames(a.Config.MCPServers),
			LocalResourceCount:     len(localResources),
			LocalResources:         localResources,
			LocalTemplateCount:     len(localTemplates),
			LocalResourceTemplates: localTemplates,
			LocalPromptCount:       len(localPrompts),
			LocalPrompts:           localPrompts,
			ExposedToolCount:       len(exposed),
		},
		Features:      codogCapabilityFeatures(),
		Protocols:     codogCapabilityProtocols(),
		OutputFormats: []string{"text", "json", "stream-json"},
	}
}

func renderCapabilitiesText(out io.Writer, report capabilitiesReport) {
	fmt.Fprintln(out, "Codog Capabilities")
	fmt.Fprintf(out, "  Version           %s\n", report.Version)
	fmt.Fprintf(out, "  Commands          %d\n", report.CommandCount)
	fmt.Fprintf(out, "  Slash commands    %d\n", report.SlashCommandCount)
	fmt.Fprintf(out, "  Tools             %d\n", report.ToolCount)
	fmt.Fprintf(out, "  MCP servers       %d configured\n", report.MCP.ConfiguredServerCount)
	fmt.Fprintf(out, "  MCP local data    %d resources, %d templates, %d prompts\n", report.MCP.LocalResourceCount, report.MCP.LocalTemplateCount, report.MCP.LocalPromptCount)
	fmt.Fprintln(out, "  Features")
	for _, feature := range report.Features {
		fmt.Fprintf(out, "    - %s\n", feature)
	}
}

func slashCapabilities() []capabilitySlash {
	specs := slash.Specs()
	out := make([]capabilitySlash, 0, len(specs))
	for _, spec := range specs {
		out = append(out, capabilitySlash{
			Name:        spec.Name,
			Usage:       spec.Usage,
			Description: spec.Description,
			Hidden:      spec.Hidden,
			Disabled:    spec.Disabled,
		})
	}
	return out
}

func codogCapabilityFeatures() []string {
	return sortedUniqueStrings([]string{
		"acp_bridge",
		"anthropic_streaming",
		"auto_compaction",
		"background_tasks",
		"bubble_tea_tui",
		"config_layers",
		"cost_token_tracking",
		"editor_bridge",
		"git_workflows",
		"hooks",
		"ide_bridge",
		"jsonl_sessions",
		"lsp",
		"mcp_client",
		"mcp_server",
		"mock_parity_harness",
		"multi_agent",
		"notebooks",
		"oauth",
		"one_shot_prompt",
		"openai_compatible_streaming",
		"permission_confirmation",
		"plugin_marketplace",
		"project_memory",
		"remote_control",
		"repl",
		"sandbox",
		"session_resume",
		"skills",
		"slash_commands",
		"updater",
		"workspace_tools",
	})
}

func codogCapabilityProtocols() []string {
	return sortedUniqueStrings([]string{
		"acp_json_rpc_stdio",
		"anthropic_messages",
		"editor_bridge_http",
		"mcp_stdio_client",
		"mcp_stdio_server",
		"openai_chat_completions",
		"remote_control_http",
	})
}

func builtInCommandNames() []string {
	return sortedUniqueStrings([]string{
		"acp",
		"add-dir",
		"advisor",
		"agents",
		"allowed-tools",
		"app",
		"backfill-sessions",
		"background",
		"bashes",
		"blame",
		"branch",
		"brief",
		"btw",
		"bughunter",
		"build",
		"capabilities",
		"changelog",
		"chrome",
		"code-intel",
		"color",
		"commands",
		"commit",
		"commit-push-pr",
		"compact",
		"completion",
		"config",
		"context",
		"copy",
		"cost",
		"ctx_viz",
		"debug-tool-call",
		"definition",
		"desktop",
		"diagnostics",
		"diff",
		"doctor",
		"dump-manifests",
		"effort",
		"enterprise",
		"env",
		"exit-plan",
		"export",
		"extra-usage",
		"fast",
		"feedback",
		"files",
		"focus",
		"format",
		"git",
		"heapdump",
		"help",
		"history",
		"hooks",
		"hover",
		"ide",
		"init",
		"init-verifiers",
		"insights",
		"install",
		"install-github-app",
		"install-slack-app",
		"issue",
		"keybindings",
		"lint",
		"log",
		"login",
		"logout",
		"map",
		"marketplace",
		"max-tokens",
		"max-turns",
		"mcp",
		"memory",
		"mobile",
		"mock-server",
		"model",
		"node",
		"oauth",
		"oauth-refresh",
		"output-style",
		"passes",
		"plugin",
		"plugins",
		"pr",
		"pr-comments",
		"privacy-settings",
		"project",
		"prompt",
		"prompt-history",
		"providers",
		"python",
		"rate-limit-options",
		"references",
		"release-notes",
		"reload-plugins",
		"remote",
		"remote-control",
		"remote-env",
		"remote-setup",
		"rename",
		"repl",
		"reset-limits",
		"review",
		"rewind",
		"run",
		"sandbox",
		"sandbox-toggle",
		"search",
		"security-review",
		"self-test",
		"sessions",
		"share",
		"skills",
		"stash",
		"state",
		"stats",
		"status",
		"statusline",
		"stickers",
		"summary",
		"symbols",
		"system-prompt",
		"tag",
		"tasks",
		"templates",
		"terminal-setup",
		"terminalSetup",
		"test",
		"theme",
		"think-back",
		"thinkback",
		"thinkback-play",
		"todos",
		"tui",
		"ultraplan",
		"ultrareview",
		"undo",
		"unfocus",
		"updater",
		"upgrade",
		"usage",
		"version",
		"vim",
		"voice",
		"web-setup",
	})
}

func sortedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

type commandNotFoundReport struct {
	Kind      string   `json:"kind"`
	ErrorKind string   `json:"error_kind"`
	Status    string   `json:"status"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Message   string   `json:"message"`
	Hint      string   `json:"hint"`
}

type slashErrorReport struct {
	Kind      string `json:"kind"`
	ErrorKind string `json:"error_kind"`
	Status    string `json:"status"`
	Command   string `json:"command"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
}

type cliErrorReport struct {
	Kind        string            `json:"kind"`
	ErrorKind   string            `json:"error_kind"`
	Status      string            `json:"status"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Option      string            `json:"option,omitempty"`
	Message     string            `json:"message"`
	Hint        string            `json:"hint"`
	Value       string            `json:"value,omitempty"`
	Expected    []string          `json:"expected,omitempty"`
	Argument    string            `json:"argument,omitempty"`
	ToolName    string            `json:"tool_name,omitempty"`
	Available   []string          `json:"available,omitempty"`
	ToolAliases map[string]string `json:"tool_aliases,omitempty"`
}

type actionErrorReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	ErrorKind string `json:"error_kind"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
}

type outputFormatError struct {
	Command  string
	Value    string
	Expected []string
}

func (e outputFormatError) Error() string {
	command := strings.TrimSpace(e.Command)
	if command == "" {
		command = "command"
	}
	return fmt.Sprintf("invalid_output_format: unknown %s output format %q", command, e.Value)
}

type missingArgumentError struct {
	Argument string
	Example  string
}

func (e missingArgumentError) Error() string {
	return fmt.Sprintf("missing_argument: %s requires a value", e.Argument)
}

type toolNameError struct {
	Argument  string
	ToolName  string
	Available []string
	Aliases   map[string]string
}

func (e toolNameError) Error() string {
	return fmt.Sprintf("invalid_tool_name: unknown tool name %q for %s", e.ToolName, e.Argument)
}

type unknownOptionError struct {
	Kind    string
	Command string
	Option  string
	Usage   string
}

func (e unknownOptionError) Error() string {
	return fmt.Sprintf("unknown_option: unknown option %q for %s", e.Option, e.Command)
}

type unexpectedExtraArgsError struct {
	Command string
	Args    []string
	Usage   string
}

func (e unexpectedExtraArgsError) Error() string {
	return fmt.Sprintf("unexpected_extra_args: %s got unexpected arguments: %s", e.Command, strings.Join(e.Args, " "))
}

type promptErrorReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	ErrorKind string `json:"error_kind"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
}

func renderMissingPrompt(out io.Writer, format string) error {
	report := promptErrorReport{
		Kind:      "prompt",
		Action:    "abort",
		ErrorKind: "missing_prompt",
		Status:    "error",
		Message:   "prompt is empty",
		Hint:      "Provide a prompt with `codog prompt \"...\"`, `codog -p \"...\"`, or pipe text into `codog prompt`.",
	}
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "stream-json":
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	default:
		return &ExitError{Code: 1, Err: err}
	}
}

func renderActionError(out io.Writer, report actionErrorReport, format string) error {
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func renderCLIError(out io.Writer, err error, format string) error {
	report := buildCLIErrorReport(err)
	exitErr := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	var formatErr outputFormatError
	forceJSON := errors.As(err, &formatErr)
	if strings.EqualFold(format, "json") || forceJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: exitErr, Silent: true}
	}
	return &ExitError{Code: 1, Err: exitErr}
}

func buildCLIErrorReport(err error) cliErrorReport {
	message := strings.TrimSpace(err.Error())
	kind := "config_load_failed"
	hint := "Check `codog config paths` and fix the active configuration."
	var formatErr outputFormatError
	if errors.As(err, &formatErr) {
		expected := append([]string(nil), formatErr.Expected...)
		if len(expected) == 0 {
			expected = []string{"text", "json"}
		}
		return cliErrorReport{
			Kind:      "invalid_output_format",
			ErrorKind: "invalid_output_format",
			Status:    "error",
			Message:   fmt.Sprintf("unknown output format %q", formatErr.Value),
			Hint:      "Use `--output-format json` or `--output-format text`.",
			Value:     formatErr.Value,
			Expected:  expected,
		}
	}
	var missingErr missingArgumentError
	if errors.As(err, &missingErr) {
		argument := strings.TrimSpace(missingErr.Argument)
		if argument == "" {
			argument = "argument"
		}
		example := strings.TrimSpace(missingErr.Example)
		if example == "" {
			example = argument + " read_file,grep"
		}
		return cliErrorReport{
			Kind:      "missing_argument",
			ErrorKind: "missing_argument",
			Status:    "error",
			Message:   fmt.Sprintf("%s requires a value", argument),
			Hint:      fmt.Sprintf("Provide a comma-separated tool list, for example `%s`.", example),
			Argument:  argument,
		}
	}
	var toolErr toolNameError
	if errors.As(err, &toolErr) {
		argument := strings.TrimSpace(toolErr.Argument)
		if argument == "" {
			argument = "--allowed-tools"
		}
		toolName := strings.TrimSpace(toolErr.ToolName)
		return cliErrorReport{
			Kind:        "invalid_tool_name",
			ErrorKind:   "invalid_tool_name",
			Status:      "error",
			Message:     fmt.Sprintf("unknown tool name %q for %s", toolName, argument),
			Hint:        "Use canonical snake_case tool names or supported aliases; MCP tools may use mcp__server__tool or mcp__server__*.",
			Argument:    argument,
			ToolName:    toolName,
			Available:   append([]string(nil), toolErr.Available...),
			ToolAliases: copyStringMap(toolErr.Aliases),
		}
	}
	var optionErr unknownOptionError
	if errors.As(err, &optionErr) {
		kind := strings.TrimSpace(optionErr.Kind)
		if kind == "" {
			kind = "unknown_option"
		}
		command := strings.TrimSpace(optionErr.Command)
		option := strings.TrimSpace(optionErr.Option)
		usage := strings.TrimSpace(optionErr.Usage)
		hint := fmt.Sprintf("Remove %s or use a supported option.", option)
		if usage != "" {
			hint = "Usage: " + usage
		}
		return cliErrorReport{
			Kind:      kind,
			ErrorKind: kind,
			Status:    "error",
			Command:   command,
			Option:    option,
			Message:   fmt.Sprintf("unknown option %q for %s", option, command),
			Hint:      hint,
		}
	}
	var extraArgsErr unexpectedExtraArgsError
	if errors.As(err, &extraArgsErr) {
		command := strings.TrimSpace(extraArgsErr.Command)
		args := append([]string(nil), extraArgsErr.Args...)
		usage := strings.TrimSpace(extraArgsErr.Usage)
		if usage == "" {
			usage = "codog " + command
		}
		return cliErrorReport{
			Kind:      "unexpected_extra_args",
			ErrorKind: "unexpected_extra_args",
			Status:    "error",
			Command:   command,
			Args:      args,
			Message:   fmt.Sprintf("%s does not accept extra arguments: %s", command, strings.Join(args, " ")),
			Hint:      "Usage: " + usage,
		}
	}
	if rest, ok := strings.CutPrefix(message, "invalid_permission_mode:"); ok {
		kind = "invalid_permission_mode"
		message = strings.TrimSpace(rest)
		hint = "Use one of: read-only, workspace-write, danger-full-access, prompt, allow."
	}
	return cliErrorReport{
		Kind:      kind,
		ErrorKind: kind,
		Status:    "error",
		Message:   message,
		Hint:      hint,
	}
}

func normalizeDirectSlashInvocation(out io.Writer, command string, args []string, format string) (string, []string, error) {
	name := strings.TrimSpace(command)
	if !strings.HasPrefix(name, "/") {
		return command, args, nil
	}
	if directSlashInteractiveOnly(name) {
		return "", nil, renderInteractiveOnlySlash(out, name, format)
	}
	mapped := directSlashCommandName(name)
	if mapped == "" {
		return "", nil, renderUnknownSlashCommand(out, name, format)
	}
	return mapped, injectGlobalOutputFormat(mapped, args, format), nil
}

func directSlashCommandName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "/exit_plan_mode":
		return "exit-plan"
	case "/tokens":
		return "cost"
	case "/session":
		return "sessions"
	}
	if _, ok := slash.Lookup(name); !ok {
		return ""
	}
	return strings.TrimPrefix(name, "/")
}

func directSlashInteractiveOnly(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "/approve", "/yes", "/deny", "/no", "/clear", "/resume", "/exit", "/compact", "/commit", "/pr", "/issue", "/bughunter", "/ultraplan":
		return true
	default:
		return false
	}
}

func directSlashResumeSafe(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "/compact", "/resume":
		return true
	default:
		return false
	}
}

func renderUnknownSlashCommand(out io.Writer, command string, format string) error {
	report := slashErrorReport{
		Kind:      "unknown_slash_command",
		ErrorKind: "unknown_slash_command",
		Status:    "error",
		Command:   command,
		Message:   fmt.Sprintf("unknown slash command %q", command),
		Hint:      "Run `codog repl` and use `/help` to list interactive slash commands.",
	}
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func renderInteractiveOnly(out io.Writer, command string, format string) error {
	return renderInteractiveOnlyWithHint(out, command, fmt.Sprintf("%s is only available in an interactive REPL session", command), "Run `codog repl` and use the command there.", format)
}

func renderInteractiveOnlySlash(out io.Writer, command string, format string) error {
	hint := "Run `codog repl` and use the command there."
	if directSlashResumeSafe(command) {
		hint = fmt.Sprintf("Run `codog --resume latest %s` to target a saved session, or run `codog repl` and use the command there.", command)
	}
	return renderInteractiveOnlyWithHint(out, command, fmt.Sprintf("%s is only available in an interactive REPL session", command), hint, format)
}

func renderInteractiveOnlyWithHint(out io.Writer, command string, message string, hint string, format string) error {
	report := slashErrorReport{
		Kind:      "interactive_only",
		ErrorKind: "interactive_only",
		Status:    "error",
		Command:   command,
		Message:   message,
		Hint:      hint,
	}
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func renderLocalRouteGuard(out io.Writer, command string, args []string, format string) (bool, error) {
	meaningful := routeMeaningfulArgs(args)
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "model" && len(meaningful) > 1 {
		err := unexpectedExtraArgsError{Command: "model", Args: meaningful[1:], Usage: "codog model [MODEL]"}
		return true, renderCLIError(out, err, format)
	}
	interactive := false
	slashName := "/" + lower
	hint := fmt.Sprintf("Run `codog repl` and use `%s` there.", slashName)
	switch lower {
	case "session":
		interactive = len(meaningful) > 0
		hint = "Run `codog repl` and use `/session`, or use `codog sessions ...` for saved session management."
	case "clear", "fork":
		interactive = len(meaningful) > 0
	case "cost", "usage", "stats":
		interactive = len(meaningful) > 0
	case "memory":
		interactive = len(meaningful) > 0 && strings.EqualFold(meaningful[0], "reset")
	case "ultraplan":
		interactive = len(meaningful) > 0 && !isPlanAction(meaningful[0])
	}
	if !interactive {
		return false, nil
	}
	invocation := strings.TrimSpace(strings.Join(append([]string{command}, meaningful...), " "))
	if invocation == "" {
		invocation = command
	}
	message := fmt.Sprintf("%s is only available in an interactive REPL session", invocation)
	return true, renderInteractiveOnlyWithHint(out, invocation, message, hint, format)
}

func routeMeaningfulArgs(args []string) []string {
	out := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			continue
		case arg == "--output-format" || arg == "-o":
			index++
			continue
		case strings.HasPrefix(arg, "--output-format="):
			continue
		default:
			out = append(out, arg)
		}
	}
	return out
}

func renderCommandNotFound(out io.Writer, command string, args []string, format string) error {
	report := buildCommandNotFoundReport(command, args)
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func buildCommandNotFoundReport(command string, args []string) commandNotFoundReport {
	command = strings.TrimSpace(command)
	cleanArgs := append([]string(nil), args...)
	message := fmt.Sprintf("unknown command %q", command)
	suggestions := commandSuggestions(command, 4)
	hint := "Run `codog --help` to list commands."
	if len(cleanArgs) > 0 {
		prompt := strings.TrimSpace(strings.Join(append([]string{command}, cleanArgs...), " "))
		hint = fmt.Sprintf("Use `codog prompt %q` to send this as a prompt, or run `codog --help` to list commands.", prompt)
	} else if len(suggestions) > 0 {
		hint = fmt.Sprintf("Did you mean: %s? Run `codog --help` to list commands.", strings.Join(suggestions, ", "))
	}
	return commandNotFoundReport{
		Kind:      "command_not_found",
		ErrorKind: "command_not_found",
		Status:    "error",
		Command:   command,
		Args:      cleanArgs,
		Message:   message,
		Hint:      hint,
	}
}

func requestedOutputFormat(args []string) string {
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "--json":
			return "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index < len(args) {
				return strings.ToLower(strings.TrimSpace(args[index]))
			}
			return ""
		case strings.HasPrefix(arg, "--output-format="):
			return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(arg, "--output-format=")))
		}
	}
	return strings.ToLower(strings.TrimSpace(os.Getenv("CODOG_OUTPUT_FORMAT")))
}

func commandSuggestions(command string, limit int) []string {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" || limit <= 0 {
		return nil
	}
	type candidate struct {
		name  string
		score int
	}
	candidates := []candidate{}
	for _, name := range builtInCommandNames() {
		lower := strings.ToLower(name)
		score := levenshteinDistance(command, lower)
		if strings.HasPrefix(lower, command) || strings.HasPrefix(command, lower) {
			score--
		}
		if score <= 3 || strings.Contains(lower, command) {
			candidates = append(candidates, candidate{name: name, score: score})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].score < candidates[j].score
	})
	out := make([]string, 0, limit)
	for _, candidate := range candidates {
		if candidate.name == command {
			continue
		}
		out = append(out, candidate.name)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len([]rune(b))
	}
	if b == "" {
		return len([]rune(a))
	}
	ar := []rune(a)
	br := []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ra := range ar {
		curr[0] = i + 1
		for j, rb := range br {
			cost := 0
			if ra != rb {
				cost = 1
			}
			curr[j+1] = minInt(curr[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func minInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	min := values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
	}
	return min
}

func parseSimpleOutputFormat(command string, args []string) (string, error) {
	format := "text"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s output format is required", command)
			}
			format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			return "", fmt.Errorf("unknown %s flag %q", command, arg)
		}
	}
	switch format {
	case "text", "json":
		return format, nil
	default:
		return "", fmt.Errorf("unknown %s output format %q", command, format)
	}
}

func stripJSONOnlyOutputFormat(command string, args []string) ([]string, string, error) {
	format := "text"
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return nil, "", fmt.Errorf("%s output format is required", command)
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			remaining = append(remaining, arg)
		}
	}
	if err := validateTextOrJSON(format, command); err != nil {
		return nil, "", err
	}
	return remaining, format, nil
}

func parsePlanArgs(args []string) (planRequest, error) {
	req := planRequest{Action: "show", Format: "text"}
	textParts := []string{}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("plan output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown plan flag %q", arg)
		case !actionSet && isPlanAction(arg):
			req.Action = normalizePlanAction(arg)
			actionSet = true
		default:
			textParts = append(textParts, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown plan output format %q", req.Format)
	}
	req.Text = strings.TrimSpace(strings.Join(textParts, " "))
	if req.Text != "" && req.Action == "show" {
		req.Action = "enter"
	}
	if (req.Action == "set") && req.Text == "" {
		return req, errors.New("plan text is required")
	}
	return req, nil
}

func isPlanAction(value string) bool {
	switch normalizePlanAction(value) {
	case "show", "enter", "set", "exit", "clear":
		return true
	default:
		return false
	}
}

func normalizePlanAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "show", "status", "list":
		return "show"
	case "enter", "start", "on":
		return "enter"
	case "set", "update":
		return "set"
	case "exit", "stop", "off", "done", "accept":
		return "exit"
	case "clear", "reset", "delete":
		return "clear"
	default:
		return value
	}
}

func parseHistoryArgs(args []string, overrides config.FlagOverrides) (historyRequest, error) {
	req := historyRequest{Format: "text", Limit: prompthistory.DefaultLimit}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
		if req.SessionID == "true" {
			req.SessionID = "latest"
		}
	}
	if req.SessionID == "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("history output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit" || arg == "-n":
			index++
			if index >= len(args) {
				return req, errors.New("history limit is required")
			}
			limit, err := parseHistoryLimit(args[index])
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parseHistoryLimit(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("history session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown history flag %q", arg)
		default:
			limit, err := strconv.Atoi(arg)
			if err == nil {
				if limit <= 0 {
					return req, errors.New("history limit must be positive")
				}
				req.Limit = limit
				continue
			}
			if req.SessionID == "" || req.SessionID == "latest" {
				req.SessionID = arg
				continue
			}
			return req, fmt.Errorf("unexpected history argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown history output format %q", req.Format)
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return req, nil
}

func parseHistoryLimit(value string) (int, error) {
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, errors.New("history limit must be positive")
	}
	return limit, nil
}

func parseSummaryArgs(args []string, overrides config.FlagOverrides) (summaryRequest, error) {
	req := summaryRequest{Format: "text"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
		if req.SessionID == "true" {
			req.SessionID = "latest"
		}
	}
	if req.SessionID == "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("summary output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("summary session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("summary resume id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown summary flag %q", arg)
		default:
			if req.SessionID == "" || req.SessionID == "latest" {
				req.SessionID = arg
				continue
			}
			return req, fmt.Errorf("unexpected summary argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown summary output format %q", req.Format)
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return req, nil
}

func parseRewindArgs(args []string, overrides config.FlagOverrides, defaultSession string) (rewindRequest, error) {
	req := rewindRequest{Format: "text", Messages: 2, SessionID: defaultSession}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
		if req.SessionID == "true" {
			req.SessionID = "latest"
		}
	}
	if req.SessionID == "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("rewind output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("rewind session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("rewind resume id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--messages" || arg == "-n":
			index++
			if index >= len(args) {
				return req, errors.New("rewind message count is required")
			}
			count, err := parseRewindCount(args[index])
			if err != nil {
				return req, err
			}
			req.Messages = count
		case strings.HasPrefix(arg, "--messages="):
			count, err := parseRewindCount(strings.TrimPrefix(arg, "--messages="))
			if err != nil {
				return req, err
			}
			req.Messages = count
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown rewind flag %q", arg)
		default:
			count, err := strconv.Atoi(arg)
			if err == nil {
				if count <= 0 {
					return req, errors.New("rewind message count must be positive")
				}
				req.Messages = count
				continue
			}
			if req.SessionID == "" || req.SessionID == "latest" {
				req.SessionID = arg
				continue
			}
			return req, fmt.Errorf("unexpected rewind argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown rewind output format %q", req.Format)
	}
	return req, nil
}

func parseRewindCount(value string) (int, error) {
	count, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if count <= 0 {
		return 0, errors.New("rewind message count must be positive")
	}
	return count, nil
}

func parseFilesArgs(args []string) (filesRequest, error) {
	req := filesRequest{Format: "text", Limit: 200}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("files output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("files path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--glob":
			index++
			if index >= len(args) {
				return req, errors.New("files glob is required")
			}
			req.Glob = args[index]
		case strings.HasPrefix(arg, "--glob="):
			req.Glob = strings.TrimPrefix(arg, "--glob=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return req, errors.New("files limit is required")
			}
			limit, err := parseFilesLimit(args[index])
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parseFilesLimit(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case arg == "--hidden" || arg == "--include-hidden":
			req.IncludeHidden = true
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown files flag %q", arg)
		default:
			if req.Path == "" {
				req.Path = arg
				continue
			}
			return req, fmt.Errorf("unexpected files argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown files output format %q", req.Format)
	}
	return req, nil
}

func parseFilesLimit(value string) (int, error) {
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, errors.New("files limit must be positive")
	}
	return limit, nil
}

func parseSearchArgs(args []string) (searchRequest, error) {
	req := searchRequest{Format: "text", Limit: 100}
	queryParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("search output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("search path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--glob":
			index++
			if index >= len(args) {
				return req, errors.New("search glob is required")
			}
			req.Glob = args[index]
		case strings.HasPrefix(arg, "--glob="):
			req.Glob = strings.TrimPrefix(arg, "--glob=")
		case arg == "--ignore-case" || arg == "-i":
			req.IgnoreCase = true
		case arg == "--limit" || arg == "-n":
			index++
			if index >= len(args) {
				return req, errors.New("search limit is required")
			}
			limit, err := parseSearchLimit(args[index])
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parseSearchLimit(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown search flag %q", arg)
		default:
			queryParts = append(queryParts, arg)
		}
	}
	req.Query = strings.TrimSpace(strings.Join(queryParts, " "))
	if req.Query == "" {
		return req, errors.New("usage: codog search PATTERN [--path PATH] [--glob GLOB] [--ignore-case] [--limit N] [--json]")
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown search output format %q", req.Format)
	}
	return req, nil
}

func parseSearchLimit(value string) (int, error) {
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, errors.New("search limit must be positive")
	}
	return limit, nil
}

func (a *App) Doctor(args []string) error {
	format, err := parseSimpleOutputFormat("doctor", args)
	if err != nil {
		return err
	}
	toolCount := 0
	if a.Tools != nil {
		toolCount = len(a.Tools.Definitions())
	}
	sessionCount := -1
	if a.Sessions != nil {
		sessions, err := a.Sessions.List()
		if err == nil {
			sessionCount = len(sessions)
		}
	}
	memoryFiles, _ := memory.Discover(a.Workspace)
	memoryPaths := make([]string, 0, len(memoryFiles))
	for _, file := range memoryFiles {
		memoryPaths = append(memoryPaths, file.Path)
	}
	mcpStatuses := mcp.PreflightAll(context.Background(), a.Config.MCPServers)
	sandboxStatus := sandbox.Detect()
	report := doctor.Run(doctor.Options{
		Workspace:          a.Workspace,
		ConfigHome:         a.Config.ConfigHome,
		Model:              a.Config.Model,
		BaseURL:            a.Config.BaseURL,
		APIKey:             a.Config.APIKey,
		AuthToken:          a.Config.AuthToken,
		PermissionMode:     a.Config.PermissionMode,
		ToolCount:          toolCount,
		MCPServerStatuses:  mcpStatuses,
		SessionCount:       sessionCount,
		MemoryFiles:        memoryPaths,
		UserPromptSubmit:   a.Config.Hooks.UserPromptSubmit,
		SessionStart:       a.Config.Hooks.SessionStart,
		PreToolUse:         a.Config.Hooks.PreToolUse,
		PostToolUse:        a.Config.Hooks.PostToolUse,
		PostToolUseFailure: a.Config.Hooks.PostToolUseFailure,
		PermissionRequest:  a.Config.Hooks.PermissionRequest,
		PermissionDenied:   a.Config.Hooks.PermissionDenied,
		SessionEnd:         a.Config.Hooks.SessionEnd,
		Setup:              a.Config.Hooks.Setup,
		Stop:               a.Config.Hooks.Stop,
		StopFailure:        a.Config.Hooks.StopFailure,
		PreCompact:         a.Config.Hooks.PreCompact,
		PostCompact:        a.Config.Hooks.PostCompact,
		Notification:       a.Config.Hooks.Notification,
		SubagentStart:      a.Config.Hooks.SubagentStart,
		SubagentStop:       a.Config.Hooks.SubagentStop,
		WorktreeCreate:     a.Config.Hooks.WorktreeCreate,
		WorktreeRemove:     a.Config.Hooks.WorktreeRemove,
		CwdChanged:         a.Config.Hooks.CwdChanged,
		TaskCreated:        a.Config.Hooks.TaskCreated,
		TaskCompleted:      a.Config.Hooks.TaskCompleted,
		InstructionsLoaded: a.Config.Hooks.InstructionsLoaded,
		FileChanged:        a.Config.Hooks.FileChanged,
		SandboxDefault:     sandboxStatus.Default,
		SandboxOK:          sandboxStatus.Available,
	})
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	} else {
		doctor.RenderText(a.Out, report)
	}
	if report.HasFailures {
		return errors.New("doctor found failing checks")
	}
	return nil
}

type symbolsReport struct {
	Kind    string             `json:"kind"`
	Total   int                `json:"total"`
	Symbols []codeintel.Symbol `json:"symbols"`
}

type diagnosticsReport struct {
	Kind        string                 `json:"kind"`
	Total       int                    `json:"total"`
	Diagnostics []codeintel.Diagnostic `json:"diagnostics"`
}

type mapReport struct {
	Kind    string               `json:"kind"`
	Total   int                  `json:"total"`
	Depth   int                  `json:"depth"`
	Entries []codeintel.MapEntry `json:"entries"`
}

type referencesReport struct {
	Kind       string                `json:"kind"`
	Symbol     string                `json:"symbol"`
	Total      int                   `json:"total"`
	References []codeintel.Reference `json:"references"`
}

type definitionReport struct {
	Kind       string           `json:"kind"`
	Symbol     string           `json:"symbol"`
	Found      bool             `json:"found"`
	Definition codeintel.Symbol `json:"definition,omitempty"`
}

type completionReport struct {
	Kind        string                 `json:"kind"`
	Query       string                 `json:"query"`
	Total       int                    `json:"total"`
	Completions []codeintel.Completion `json:"completions"`
}

type formatReport struct {
	Kind   string                 `json:"kind"`
	Write  bool                   `json:"write"`
	Result codeintel.FormatResult `json:"result"`
}

type teleportReport struct {
	Kind       string             `json:"kind"`
	Query      string             `json:"query"`
	Mode       string             `json:"mode"`
	Found      bool               `json:"found"`
	Path       string             `json:"path,omitempty"`
	Content    string             `json:"content,omitempty"`
	Bytes      int                `json:"bytes,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
	Hover      codeintel.Hover    `json:"hover,omitempty"`
	Candidates []codeintel.Symbol `json:"candidates,omitempty"`
}

func (a *App) Symbols(args []string) error {
	format, rest, err := parseCodeIntelOutputArgs("symbols", args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("usage: codog symbols [--json]")
	}
	symbols, err := codeintel.GoSymbols(a.Workspace)
	if err != nil {
		return err
	}
	report := symbolsReport{Kind: "symbols", Total: len(symbols), Symbols: symbols}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSymbols(a.Out, report)
	return nil
}

func (a *App) Diagnostics(ctx context.Context, args []string) error {
	format, rest, err := parseCodeIntelOutputArgs("diagnostics", args)
	if err != nil {
		return err
	}
	diagnostics, err := codeintel.GoDiagnostics(ctx, a.Workspace, rest)
	if err != nil {
		return err
	}
	report := diagnosticsReport{Kind: "diagnostics", Total: len(diagnostics), Diagnostics: diagnostics}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDiagnostics(a.Out, report)
	return nil
}

func (a *App) Map(args []string) error {
	format, rest, depth, limit, err := parseMapArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("unexpected map argument %q", rest[0])
	}
	entries, err := codeintel.CodeMap(a.Workspace, depth, limit)
	if err != nil {
		return err
	}
	report := mapReport{Kind: "map", Total: len(entries), Depth: depth, Entries: entries}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderMap(a.Out, report)
	return nil
}

func (a *App) References(args []string) error {
	format, rest, limit, err := parseSymbolLimitArgs("references", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog references SYMBOL [--limit N] [--json]")
	}
	symbol := rest[0]
	refs, err := codeintel.References(a.Workspace, symbol, limit)
	if err != nil {
		return err
	}
	report := referencesReport{Kind: "references", Symbol: symbol, Total: len(refs), References: refs}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderReferences(a.Out, report)
	return nil
}

func (a *App) Definition(args []string) error {
	format, rest, err := parseCodeIntelOutputArgs("definition", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog definition SYMBOL [--json]")
	}
	symbol := rest[0]
	definition, found, err := codeintel.Definition(a.Workspace, symbol)
	if err != nil {
		return err
	}
	report := definitionReport{Kind: "definition", Symbol: symbol, Found: found, Definition: definition}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDefinition(a.Out, report)
	return nil
}

func (a *App) Hover(args []string) error {
	format, rest, contextLines, err := parseHoverArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog hover SYMBOL [--context N] [--json]")
	}
	hover, err := codeintel.HoverInfo(a.Workspace, rest[0], contextLines)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{"kind": "hover", "hover": hover}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderHover(a.Out, hover)
	return nil
}

func (a *App) Completion(args []string) error {
	format, rest, limit, err := parseSymbolLimitArgs("completion", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog completion PREFIX [--limit N] [--json]")
	}
	query := rest[0]
	completions, err := codeintel.Completions(a.Workspace, query, limit)
	if err != nil {
		return err
	}
	report := completionReport{Kind: "completion", Query: query, Total: len(completions), Completions: completions}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCompletion(a.Out, report)
	return nil
}

func (a *App) Format(args []string) error {
	format, rest, write, err := parseFormatArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog format PATH [--write] [--json]")
	}
	result, err := codeintel.FormatGoFile(a.Workspace, rest[0], write)
	if err != nil {
		return err
	}
	report := formatReport{Kind: "format", Write: write, Result: result}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderFormat(a.Out, report)
	return nil
}

func (a *App) Teleport(args []string) error {
	format, rest, limit, err := parseSymbolLimitArgs("teleport", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog teleport TARGET [--limit N] [--json]")
	}
	report, err := a.teleportReport(rest[0], limit)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTeleport(a.Out, report)
	return nil
}

func (a *App) teleportReport(target string, limit int) (teleportReport, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return teleportReport{}, errors.New("teleport target is required")
	}
	if limit <= 0 {
		limit = 20
	}
	if file, err := (workspaceops.Service{Workspace: a.Workspace}).Read(workspaceops.ReadOptions{Path: target, Limit: 128 * 1024}); err == nil {
		return teleportReport{
			Kind:      "teleport",
			Query:     target,
			Mode:      "file",
			Found:     true,
			Path:      file.Path,
			Content:   file.Content,
			Bytes:     file.Bytes,
			Truncated: file.Truncated,
		}, nil
	}
	symbols, err := codeintel.GoSymbols(a.Workspace)
	if err != nil {
		return teleportReport{}, err
	}
	exact := []codeintel.Symbol{}
	partial := []codeintel.Symbol{}
	lowerTarget := strings.ToLower(target)
	for _, symbol := range symbols {
		switch {
		case symbol.Name == target:
			exact = append(exact, symbol)
		case strings.Contains(strings.ToLower(symbol.Name), lowerTarget):
			partial = append(partial, symbol)
		}
	}
	candidates := exact
	if len(candidates) == 0 {
		candidates = partial
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Path == candidates[j].Path {
			if candidates[i].Line == candidates[j].Line {
				return candidates[i].Name < candidates[j].Name
			}
			return candidates[i].Line < candidates[j].Line
		}
		return candidates[i].Path < candidates[j].Path
	})
	if len(candidates) == 0 {
		return teleportReport{Kind: "teleport", Query: target, Mode: "symbol", Found: false}, nil
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if len(candidates) == 1 {
		hover, err := codeintel.HoverInfo(a.Workspace, candidates[0].Name, 2)
		if err != nil {
			return teleportReport{}, err
		}
		return teleportReport{
			Kind:       "teleport",
			Query:      target,
			Mode:       "symbol",
			Found:      hover.Found,
			Path:       hover.Path,
			Hover:      hover,
			Candidates: candidates,
		}, nil
	}
	return teleportReport{Kind: "teleport", Query: target, Mode: "candidates", Found: true, Candidates: candidates}, nil
}

func renderTeleport(out io.Writer, report teleportReport) {
	fmt.Fprintln(out, "Teleport")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Mode             %s\n", report.Mode)
	if !report.Found {
		fmt.Fprintln(out, "  Found            false")
		return
	}
	fmt.Fprintln(out, "  Found            true")
	switch report.Mode {
	case "file":
		fmt.Fprintf(out, "  Path             %s\n", report.Path)
		fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
		if report.Truncated {
			fmt.Fprintln(out, "  Truncated        true")
		}
		fmt.Fprintln(out)
		fmt.Fprint(out, report.Content)
		if !strings.HasSuffix(report.Content, "\n") {
			fmt.Fprintln(out)
		}
	case "symbol":
		if report.Hover.Path != "" {
			fmt.Fprintf(out, "  Location         %s:%d\n", report.Hover.Path, report.Hover.Line)
			fmt.Fprintf(out, "  Kind             %s\n", report.Hover.Kind)
			fmt.Fprintln(out)
			for _, line := range report.Hover.Snippet {
				fmt.Fprintln(out, line)
			}
		}
	default:
		fmt.Fprintf(out, "  Candidates       %d\n", len(report.Candidates))
		fmt.Fprintln(out)
		for _, candidate := range report.Candidates {
			fmt.Fprintf(out, "%s:%d:%s %s\n", candidate.Path, candidate.Line, candidate.Kind, candidate.Name)
		}
	}
}

func renderSymbols(out io.Writer, report symbolsReport) {
	fmt.Fprintln(out, "Symbols")
	fmt.Fprintf(out, "  Total            %d\n", report.Total)
	for _, symbol := range report.Symbols {
		fmt.Fprintf(out, "%s:%d:%s %s\n", symbol.Path, symbol.Line, symbol.Kind, symbol.Name)
	}
}

func renderDiagnostics(out io.Writer, report diagnosticsReport) {
	fmt.Fprintln(out, "Diagnostics")
	fmt.Fprintf(out, "  Total            %d\n", report.Total)
	if report.Total == 0 {
		fmt.Fprintln(out, "No diagnostics.")
		return
	}
	for _, diagnostic := range report.Diagnostics {
		location := diagnostic.Package
		if diagnostic.Path != "" {
			location = fmt.Sprintf("%s:%d", diagnostic.Path, diagnostic.Line)
			if diagnostic.Column > 0 {
				location = fmt.Sprintf("%s:%d:%d", diagnostic.Path, diagnostic.Line, diagnostic.Column)
			}
		}
		fmt.Fprintf(out, "%s %s\n", location, diagnostic.Message)
	}
}

func renderMap(out io.Writer, report mapReport) {
	fmt.Fprintln(out, "Map")
	fmt.Fprintf(out, "  Entries          %d\n", report.Total)
	for _, entry := range report.Entries {
		fmt.Fprintf(out, "%s\t%s\n", entry.Type, entry.Path)
	}
}

func renderReferences(out io.Writer, report referencesReport) {
	fmt.Fprintln(out, "References")
	fmt.Fprintf(out, "  Symbol           %s\n", report.Symbol)
	fmt.Fprintf(out, "  Total            %d\n", report.Total)
	for _, ref := range report.References {
		fmt.Fprintf(out, "%s:%d:%s\n", ref.Path, ref.Line, ref.Text)
	}
}

func renderDefinition(out io.Writer, report definitionReport) {
	fmt.Fprintln(out, "Definition")
	fmt.Fprintf(out, "  Symbol           %s\n", report.Symbol)
	if !report.Found {
		fmt.Fprintln(out, "  Found            false")
		return
	}
	fmt.Fprintln(out, "  Found            true")
	fmt.Fprintf(out, "  Location         %s:%d\n", report.Definition.Path, report.Definition.Line)
	fmt.Fprintf(out, "  Kind             %s\n", report.Definition.Kind)
}

func renderHover(out io.Writer, hover codeintel.Hover) {
	fmt.Fprintln(out, "Hover")
	fmt.Fprintf(out, "  Symbol           %s\n", hover.Symbol)
	if !hover.Found {
		fmt.Fprintln(out, "  Found            false")
		return
	}
	fmt.Fprintln(out, "  Found            true")
	fmt.Fprintf(out, "  Location         %s:%d\n", hover.Path, hover.Line)
	fmt.Fprintf(out, "  Kind             %s\n", hover.Kind)
	fmt.Fprintln(out)
	for _, line := range hover.Snippet {
		fmt.Fprintln(out, line)
	}
}

func renderCompletion(out io.Writer, report completionReport) {
	fmt.Fprintln(out, "Completion")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Total            %d\n", report.Total)
	for _, completion := range report.Completions {
		if completion.Path != "" {
			fmt.Fprintf(out, "%s:%d:%s %s\n", completion.Path, completion.Line, completion.Kind, completion.Label)
			continue
		}
		if completion.Detail != "" {
			fmt.Fprintf(out, "%s %s %s\n", completion.Kind, completion.Label, completion.Detail)
			continue
		}
		fmt.Fprintf(out, "%s %s\n", completion.Kind, completion.Label)
	}
}

func renderFormat(out io.Writer, report formatReport) {
	fmt.Fprintln(out, "Format")
	fmt.Fprintf(out, "  Path             %s\n", report.Result.Path)
	fmt.Fprintf(out, "  Changed          %t\n", report.Result.Changed)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Result.Bytes)
	fmt.Fprintf(out, "  Written          %t\n", report.Write)
	if !report.Write && report.Result.Content != "" {
		fmt.Fprintln(out)
		fmt.Fprint(out, report.Result.Content)
		if !strings.HasSuffix(report.Result.Content, "\n") {
			fmt.Fprintln(out)
		}
	}
}

func parseCodeIntelOutputArgs(command string, args []string) (string, []string, error) {
	format := "text"
	rest := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", nil, fmt.Errorf("%s output format is required", command)
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			rest = append(rest, arg)
		}
	}
	switch format {
	case "text", "json":
		return format, rest, nil
	default:
		return "", nil, fmt.Errorf("unknown %s output format %q", command, format)
	}
}

func parseMapArgs(args []string) (string, []string, int, int, error) {
	format, rest, err := parseCodeIntelOutputArgs("map", args)
	if err != nil {
		return "", nil, 0, 0, err
	}
	depth := 3
	limit := 200
	filtered := []string{}
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		switch {
		case arg == "--depth":
			index++
			if index >= len(rest) {
				return "", nil, 0, 0, errors.New("map depth is required")
			}
			parsed, err := parsePositiveInt(rest[index], "map depth")
			if err != nil {
				return "", nil, 0, 0, err
			}
			depth = parsed
		case strings.HasPrefix(arg, "--depth="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(arg, "--depth="), "map depth")
			if err != nil {
				return "", nil, 0, 0, err
			}
			depth = parsed
		case arg == "--limit":
			index++
			if index >= len(rest) {
				return "", nil, 0, 0, errors.New("map limit is required")
			}
			parsed, err := parsePositiveInt(rest[index], "map limit")
			if err != nil {
				return "", nil, 0, 0, err
			}
			limit = parsed
		case strings.HasPrefix(arg, "--limit="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(arg, "--limit="), "map limit")
			if err != nil {
				return "", nil, 0, 0, err
			}
			limit = parsed
		default:
			filtered = append(filtered, arg)
		}
	}
	return format, filtered, depth, limit, nil
}

func parseSymbolLimitArgs(command string, args []string) (string, []string, int, error) {
	format, rest, err := parseCodeIntelOutputArgs(command, args)
	if err != nil {
		return "", nil, 0, err
	}
	limit := 100
	filtered := []string{}
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		switch {
		case arg == "--limit":
			index++
			if index >= len(rest) {
				return "", nil, 0, fmt.Errorf("%s limit is required", command)
			}
			parsed, err := parsePositiveInt(rest[index], command+" limit")
			if err != nil {
				return "", nil, 0, err
			}
			limit = parsed
		case strings.HasPrefix(arg, "--limit="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(arg, "--limit="), command+" limit")
			if err != nil {
				return "", nil, 0, err
			}
			limit = parsed
		default:
			filtered = append(filtered, arg)
		}
	}
	return format, filtered, limit, nil
}

func parseHoverArgs(args []string) (string, []string, int, error) {
	format, rest, err := parseCodeIntelOutputArgs("hover", args)
	if err != nil {
		return "", nil, 0, err
	}
	contextLines := 2
	filtered := []string{}
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		switch {
		case arg == "--context":
			index++
			if index >= len(rest) {
				return "", nil, 0, errors.New("hover context is required")
			}
			parsed, err := parsePositiveInt(rest[index], "hover context")
			if err != nil {
				return "", nil, 0, err
			}
			contextLines = parsed
		case strings.HasPrefix(arg, "--context="):
			parsed, err := parsePositiveInt(strings.TrimPrefix(arg, "--context="), "hover context")
			if err != nil {
				return "", nil, 0, err
			}
			contextLines = parsed
		default:
			filtered = append(filtered, arg)
		}
	}
	return format, filtered, contextLines, nil
}

func parseFormatArgs(args []string) (string, []string, bool, error) {
	format, rest, err := parseCodeIntelOutputArgs("format", args)
	if err != nil {
		return "", nil, false, err
	}
	write := false
	filtered := []string{}
	for _, arg := range rest {
		switch {
		case arg == "--write":
			write = true
		case strings.HasPrefix(arg, "--write="):
			parsed, err := strconv.ParseBool(strings.TrimPrefix(arg, "--write="))
			if err != nil {
				return "", nil, false, fmt.Errorf("format write must be a boolean")
			}
			write = parsed
		default:
			filtered = append(filtered, arg)
		}
	}
	return format, filtered, write, nil
}

func parsePositiveInt(value string, label string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return parsed, nil
}

func (a *App) CodeIntel(args []string) error {
	if len(args) == 0 || args[0] == "symbols" {
		symbols, err := codeintel.GoSymbols(a.Workspace)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(symbols, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "diagnostics" {
		diagnostics, err := codeintel.GoDiagnostics(context.Background(), a.Workspace, args[1:])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(diagnostics, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "completion" || args[0] == "completions" {
		return a.Completion(args[1:])
	}
	if args[0] == "format" || args[0] == "formatting" {
		return a.Format(args[1:])
	}
	if args[0] == "lsp" {
		return a.CodeIntelLSP(args[1:])
	}
	if args[0] == "notebook-edit" {
		if len(args) < 5 {
			return errors.New("usage: codog code-intel notebook-edit NOTEBOOK INDEX CELL_TYPE SOURCE")
		}
		index, err := strconv.Atoi(args[2])
		if err != nil {
			return err
		}
		return codeintel.EditNotebookCell(args[1], index, args[3], strings.Join(args[4:], " "))
	}
	return fmt.Errorf("unknown code-intel command %q", args[0])
}

func (a *App) CodeIntelLSP(args []string) error {
	store := codeintel.NewLSPStore(a.Config.ConfigHome, a.Workspace)
	if len(args) == 0 || args[0] == "list" {
		statuses, err := store.List()
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(statuses, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	var payload any
	switch args[0] {
	case "discover":
		payload = codeintel.DefaultLSPCandidates()
	case "start":
		if len(args) < 2 {
			return errors.New("usage: codog code-intel lsp start LANGUAGE [COMMAND...]")
		}
		status, err := store.Start(args[1], args[2:])
		if err != nil {
			return err
		}
		payload = status
	case "status":
		if len(args) < 2 {
			return errors.New("usage: codog code-intel lsp status LANGUAGE")
		}
		status, err := store.Status(args[1])
		if err != nil {
			return err
		}
		payload = status
	case "query", "request":
		if len(args) < 4 {
			return errors.New("usage: codog code-intel lsp query LANGUAGE ACTION PATH [LINE CHARACTER]")
		}
		line := 0
		character := 0
		if len(args) > 4 {
			var err error
			line, err = strconv.Atoi(args[4])
			if err != nil {
				return fmt.Errorf("lsp query line must be an integer")
			}
		}
		if len(args) > 5 {
			var err error
			character, err = strconv.Atoi(args[5])
			if err != nil {
				return fmt.Errorf("lsp query character must be an integer")
			}
		}
		result, err := store.Query(context.Background(), args[1], codeintel.LSPQueryRequest{
			Action:    args[2],
			Path:      args[3],
			Line:      line,
			Character: character,
		})
		if err != nil {
			return err
		}
		payload = result
	case "stop":
		if len(args) < 2 {
			return errors.New("usage: codog code-intel lsp stop LANGUAGE")
		}
		status, err := store.Stop(args[1])
		if err != nil {
			return err
		}
		payload = status
	default:
		return fmt.Errorf("unknown code-intel lsp command %q", args[0])
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Prompt(ctx context.Context, input string, overrides config.FlagOverrides) error {
	return a.PromptWithOutput(ctx, input, overrides, "text")
}

func (a *App) PromptWithOutput(ctx context.Context, input string, overrides config.FlagOverrides, format string) error {
	format = strings.TrimSpace(strings.ToLower(format))
	if format == "" {
		format = "text"
	}
	switch format {
	case "text", "json", "stream-json":
	default:
		return fmt.Errorf("unknown prompt output format %q", format)
	}
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt is empty")
	}
	if err := a.RegisterMCPTools(ctx); err != nil {
		return err
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	if err := a.runSessionStartHook(ctx, sess, sessionStartSource(overrides)); err != nil {
		return err
	}
	var streamCapture bytes.Buffer
	var turnOut io.Writer = a.Out
	if format == "json" {
		turnOut = &streamCapture
	} else if format == "stream-json" {
		writer := promptStreamJSONWriter{Out: a.Out}
		if err := writer.Event("start", map[string]any{
			"session_id": sess.ID,
			"mode":       "prompt",
		}); err != nil {
			return err
		}
		turnOut = writer
	}
	runErr := a.runSessionTurnWithOptions(ctx, "prompt", sess, input, "completed", turnOptions{Out: turnOut})
	endReason := "completed"
	if runErr != nil {
		endReason = "error"
	}
	if endErr := a.runSessionEndHook(ctx, sess, endReason); endErr != nil {
		if runErr != nil {
			if a.Err != nil {
				fmt.Fprintf(a.Err, "session end hook error: %v\n", endErr)
			}
			return runErr
		}
		return endErr
	}
	if runErr != nil {
		return runErr
	}
	if format == "json" || format == "stream-json" {
		current, err := a.Sessions.Open(sess.ID)
		if err != nil {
			return err
		}
		report := promptOutputReport(current, streamCapture.String(), endReason)
		if format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		writer := promptStreamJSONWriter{Out: a.Out}
		return writer.Event("result", report)
	}
	fmt.Fprintf(a.Err, "\n\nsession: %s\n", sess.ID)
	return nil
}

type promptReport struct {
	Kind         string `json:"kind"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	SessionID    string `json:"session_id"`
	MessageCount int    `json:"message_count"`
	Response     string `json:"response"`
}

func promptOutputReport(sess *session.Session, streamed string, status string) promptReport {
	response := strings.TrimSpace(streamed)
	if response == "" && sess != nil {
		response = strings.TrimSpace(lastAssistantText(sess.Messages))
	}
	messageCount := 0
	sessionID := ""
	if sess != nil {
		messageCount = len(sess.Messages)
		sessionID = sess.ID
	}
	return promptReport{
		Kind:         "prompt",
		Action:       "run",
		Status:       status,
		SessionID:    sessionID,
		MessageCount: messageCount,
		Response:     response,
	}
}

func lastAssistantText(messages []anthropic.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "assistant" {
			continue
		}
		var builder strings.Builder
		for _, block := range messages[index].Content {
			if block.Type == "text" {
				builder.WriteString(block.Text)
			}
		}
		return builder.String()
	}
	return ""
}

type promptStreamJSONWriter struct {
	Out io.Writer
}

func (w promptStreamJSONWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.Event("assistant_delta", map[string]any{"delta": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w promptStreamJSONWriter) Event(event string, payload any) error {
	if w.Out == nil {
		return nil
	}
	data, err := json.Marshal(map[string]any{
		"type":    event,
		"payload": payload,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w.Out, string(data))
	return err
}

type btwRequest struct {
	Question  string
	SessionID string
}

func (a *App) BTW(ctx context.Context, args []string, overrides config.FlagOverrides, active *session.Session) error {
	req, err := parseBTWArgs(args, overrides)
	if err != nil {
		return err
	}
	if a.Sessions == nil {
		return errors.New("session store is unavailable")
	}
	if a.Tools == nil {
		return errors.New("tool registry is not initialized")
	}
	if err := a.RegisterMCPTools(ctx); err != nil {
		return err
	}
	source, err := a.btwSourceSession(req.SessionID, active)
	if err != nil {
		return err
	}
	side, err := a.btwSideSession(source)
	if err != nil {
		return err
	}
	if err := a.runSessionTurn(ctx, "btw", side, req.Question, "completed"); err != nil {
		return err
	}
	fmt.Fprintf(a.Err, "\n\nbtw session: %s\n", side.ID)
	if source != nil && strings.TrimSpace(source.ID) != "" {
		fmt.Fprintf(a.Err, "source session: %s\n", source.ID)
	}
	return nil
}

func parseBTWArgs(args []string, overrides config.FlagOverrides) (btwRequest, error) {
	req := btwRequest{SessionID: firstNonEmpty(overrides.Resume, overrides.SessionID)}
	questionParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			questionParts = append(questionParts, args[index+1:]...)
			index = len(args)
		case len(questionParts) == 0 && arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("btw session id is required")
			}
			req.SessionID = args[index]
		case len(questionParts) == 0 && strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case len(questionParts) == 0 && arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("btw resume session id is required")
			}
			req.SessionID = args[index]
		case len(questionParts) == 0 && strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case len(questionParts) == 0 && strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown btw flag %q", arg)
		default:
			questionParts = append(questionParts, arg)
		}
	}
	req.Question = strings.TrimSpace(strings.Join(questionParts, " "))
	if req.Question == "" {
		return req, errors.New("usage: codog btw QUESTION [--session ID|--resume ID]")
	}
	if req.SessionID == "true" {
		req.SessionID = "latest"
	}
	return req, nil
}

func (a *App) btwSourceSession(sessionID string, active *session.Session) (*session.Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" && active != nil && strings.TrimSpace(active.ID) != "" {
		return active, nil
	}
	if sessionID == "" {
		return nil, nil
	}
	if sessionID == "latest" {
		latest, err := a.Sessions.LatestID()
		if err != nil {
			return nil, err
		}
		sessionID = latest
	}
	if active != nil && active.ID == sessionID {
		return active, nil
	}
	return a.Sessions.Open(sessionID)
}

func (a *App) btwSideSession(source *session.Session) (*session.Session, error) {
	if source != nil && strings.TrimSpace(source.ID) != "" {
		exists, err := a.Sessions.Exists(source.ID)
		if err != nil {
			return nil, err
		}
		if exists {
			forked, err := a.Sessions.Fork(source.ID, "btw")
			if err != nil {
				return nil, err
			}
			forked.Messages = append([]anthropic.Message(nil), source.Messages...)
			return forked, nil
		}
	}
	side, err := a.Sessions.Open("")
	if err != nil {
		return nil, err
	}
	if source != nil {
		side.Messages = append([]anthropic.Message(nil), source.Messages...)
	}
	return side, nil
}

type turnOptions struct {
	Skill        *skills.Skill
	AllowedTools []string
	Out          io.Writer
}

type promptCLIRequest struct {
	Prompt         string
	Format         string
	PromptProvided bool
}

func parsePromptArgs(args []string) (promptCLIRequest, error) {
	req := promptCLIRequest{Format: "text"}
	parts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			if len(args[index+1:]) > 0 {
				req.PromptProvided = true
			}
			parts = append(parts, args[index+1:]...)
			index = len(args)
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("prompt output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		default:
			req.PromptProvided = true
			parts = append(parts, arg)
		}
	}
	req.Prompt = strings.TrimSpace(strings.Join(parts, " "))
	normalized, err := normalizeOutputFormat("prompt", req.Format, []string{"text", "json", "stream-json"})
	if err != nil {
		return req, err
	}
	req.Format = normalized
	return req, nil
}

func readPromptInput(in io.Reader) (string, error) {
	if in == nil {
		return "", nil
	}
	if file, ok := in.(*os.File); ok {
		info, err := file.Stat()
		if err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return "", nil
		}
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a *App) runSessionTurn(ctx context.Context, mode string, sess *session.Session, input string, successStatus string) error {
	return a.runSessionTurnWithOptions(ctx, mode, sess, input, successStatus, turnOptions{})
}

func (a *App) runSessionTurnWithOptions(ctx context.Context, mode string, sess *session.Session, input string, successStatus string, opts turnOptions) error {
	if a.promptHistoryEnabled() {
		if err := a.Sessions.AppendInput(sess.ID, input); err != nil {
			return err
		}
	} else {
		if err := a.Sessions.AppendPromptHistoryDisabled(sess.ID); err != nil {
			return err
		}
	}
	modelInput := input
	activeSkill := opts.Skill
	if activeSkill == nil {
		modelInput, activeSkill = a.expandSkillInvocationWithSkill(input)
	}
	allowedTools := append([]string(nil), opts.AllowedTools...)
	if activeSkill != nil {
		allowedTools = append(allowedTools, activeSkill.AllowedTools...)
	}
	modelInput = a.expandPromptReferences(modelInput)
	a.writeWorkerState(mode, "running", sess, "")
	if err := a.runInstructionsLoadedHooks(ctx, sess.ID, "session_start"); err != nil {
		a.writeWorkerState(mode, "error", sess, err.Error())
		return err
	}
	effectiveConfig := a.effectiveConfig()
	runner := runloop.Runner{
		Config:           effectiveConfig,
		Client:           a.Client,
		Tools:            a.Tools,
		Prompter:         a.prompterWithAllowedTools(sess.ID, allowedTools),
		HookPromptRunner: a.hookPromptRunner(effectiveConfig),
		Workspace:        a.Workspace,
		SessionID:        sess.ID,
		Out:              firstWriter(opts.Out, a.Out),
		System:           a.systemPromptForInput(input),
		OnToolUse:        a.auditToolUse(sess.ID),
	}
	result, err := runner.Run(ctx, sess.Messages, modelInput)
	if err != nil {
		a.writeWorkerState(mode, "error", sess, err.Error())
		return err
	}
	messageUsages := usageByMessageIndex(result.MessageUsages)
	for index, msg := range result.Messages[len(sess.Messages):] {
		messageIndex := len(sess.Messages) + index
		if providerUsage, ok := messageUsages[messageIndex]; ok {
			if err := a.Sessions.AppendWithUsage(sess.ID, msg, &providerUsage); err != nil {
				return err
			}
			continue
		}
		if err := a.Sessions.Append(sess.ID, msg); err != nil {
			return err
		}
	}
	sess.Messages = result.Messages
	a.writeWorkerState(mode, successStatus, sess, "")
	return nil
}

func (a *App) hookPromptRunner(cfg config.Config) hooks.PromptRunner {
	return func(ctx context.Context, req hooks.PromptRequest) (string, error) {
		if a.Client == nil {
			return "", errors.New("missing model client")
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return "", errors.New("hook prompt is empty")
		}
		model := firstNonEmpty(strings.TrimSpace(req.Model), cfg.Model, config.DefaultModel)
		maxTokens := cfg.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 1024
		}
		var streamed strings.Builder
		assistant, err := a.Client.Stream(ctx, anthropic.Request{
			Model:     model,
			MaxTokens: maxTokens,
			System:    "You are executing a Codog hook. Evaluate the hook prompt and return a concise result for the calling process.",
			Messages:  []anthropic.Message{anthropic.TextMessage("user", prompt)},
		}, func(delta string) {
			streamed.WriteString(delta)
		})
		if err != nil {
			return "", err
		}
		output := strings.TrimSpace(streamed.String())
		if output != "" {
			return output, nil
		}
		var builder strings.Builder
		for _, block := range assistant.Blocks {
			if block.Type == "text" {
				builder.WriteString(block.Text)
			}
		}
		return strings.TrimSpace(builder.String()), nil
	}
}

func usageByMessageIndex(usages []runloop.MessageUsage) map[int]anthropic.Usage {
	out := make(map[int]anthropic.Usage, len(usages))
	for _, usage := range usages {
		out[usage.MessageIndex] = usage.Usage
	}
	return out
}

func (a *App) expandPromptReferences(input string) string {
	additionalDirs, err := pathscope.EffectiveDirs(a.Workspace, a.Config.AdditionalDirs)
	if err != nil {
		additionalDirs = nil
	}
	return promptrefs.Expand(input, a.Workspace, additionalDirs)
}

func (a *App) expandSkillInvocation(input string) string {
	rendered, _ := a.expandSkillInvocationWithSkill(input)
	return rendered
}

func (a *App) expandSkillInvocationWithSkill(input string) (string, *skills.Skill) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return input, nil
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return input, nil
	}
	skill, err := skills.Find(a.Config.ConfigHome, a.Workspace, fields[0])
	if err != nil {
		return input, nil
	}
	if !skill.UserInvocable {
		return input, nil
	}
	args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
	rendered := skills.RenderInvocation(skill, args)
	return rendered, &skill
}

func (a *App) REPL(ctx context.Context, overrides config.FlagOverrides) error {
	if err := a.RegisterMCPTools(ctx); err != nil {
		return err
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	if err := a.runSessionStartHook(ctx, sess, sessionStartSource(overrides)); err != nil {
		return err
	}
	a.writeWorkerState("repl", "idle", sess, "")
	fmt.Fprintf(a.Err, "Codog %s (%s). Type /help for commands, Tab for completions, /exit to quit.\n", version, sess.ID)
	if rl, ok, err := a.newLineReader(sess.ID); err != nil {
		return err
	} else if ok {
		defer rl.Close()
		return a.finishREPL(ctx, sess, a.replReadline(ctx, sess, rl))
	}
	return a.finishREPL(ctx, sess, a.replScanner(ctx, sess))
}

func (a *App) finishREPL(ctx context.Context, sess *session.Session, loopErr error) error {
	reason := "exit"
	if loopErr != nil {
		reason = "error"
	}
	if err := a.runSessionEndHook(ctx, sess, reason); err != nil {
		if loopErr != nil {
			if a.Err != nil {
				fmt.Fprintf(a.Err, "session end hook error: %v\n", err)
			}
			return loopErr
		}
		return err
	}
	return loopErr
}

func (a *App) replScanner(ctx context.Context, sess *session.Session) error {
	scanner := bufio.NewScanner(a.In)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for {
		fmt.Fprint(a.Err, "codog> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			return nil
		}
		if line == "/help" {
			slash.RenderHelp(a.Err)
			continue
		}
		if a.handleSlash(ctx, line, sess) {
			continue
		}
		if err := a.runSessionTurn(ctx, "repl", sess, line, "idle"); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			continue
		}
	}
	return scanner.Err()
}

func (a *App) replReadline(ctx context.Context, sess *session.Session, rl *readline.Instance) error {
	for {
		line, err := rl.Readline()
		if errors.Is(err, readline.ErrInterrupt) {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if a.promptHistoryEnabled() {
			_ = rl.SaveHistory(line)
		}
		if line == "/exit" || line == "/quit" {
			return nil
		}
		if line == "/help" {
			slash.RenderHelp(a.Err)
			continue
		}
		if a.handleSlash(ctx, line, sess) {
			if strings.HasPrefix(line, "/vim") {
				rl.SetVimMode(a.readlineVimMode())
			}
			continue
		}
		if err := a.runSessionTurn(ctx, "repl", sess, line, "idle"); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			continue
		}
	}
}

func (a *App) newLineReader(activeSessionID string) (*readline.Instance, bool, error) {
	input, ok := terminalInput(a.In)
	if !ok {
		return nil, false, nil
	}
	cfg := &readline.Config{
		Prompt:                 "codog> ",
		Stdin:                  input,
		Stdout:                 a.Err,
		Stderr:                 a.Err,
		VimMode:                a.readlineVimMode(),
		AutoComplete:           slashCompleter{candidates: a.slashCompletionCandidates(activeSessionID)},
		HistorySearchFold:      true,
		DisableAutoSaveHistory: true,
		FuncIsTerminal:         func() bool { return true },
	}
	if historyFile := a.replHistoryFile(); historyFile != "" {
		cfg.HistoryFile = historyFile
	}
	rl, err := readline.NewEx(cfg)
	if err != nil {
		return nil, false, err
	}
	return rl, true, nil
}

func (a *App) readlineVimMode() bool {
	return editorModeIsVim(a.Config.EditorMode)
}

func (a *App) replHistoryFile() string {
	if !a.promptHistoryEnabled() {
		return ""
	}
	if strings.TrimSpace(a.Config.ConfigHome) == "" {
		return ""
	}
	dir := filepath.Join(a.Config.ConfigHome, "history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(dir, "repl.txt")
}

func terminalInput(input io.Reader) (*os.File, bool) {
	file, ok := input.(*os.File)
	if !ok {
		return nil, false
	}
	info, err := file.Stat()
	if err != nil {
		return nil, false
	}
	return file, info.Mode()&os.ModeCharDevice != 0
}

type slashCompleter struct {
	candidates []string
}

func (c slashCompleter) Do(line []rune, pos int) ([][]rune, int) {
	if pos < 0 || pos > len(line) {
		return nil, 0
	}
	prefix := strings.Trim(string(line[:pos]), "\r\n\t")
	if !strings.HasPrefix(prefix, "/") {
		return nil, 0
	}
	matches := slash.FilterCandidates(prefix, c.candidates)
	out := make([][]rune, 0, len(matches))
	for _, candidate := range matches {
		suffix := strings.TrimPrefix(candidate, prefix)
		out = append(out, []rune(suffix))
	}
	return out, len([]rune(prefix))
}

func (a *App) handleSlash(ctx context.Context, line string, sess *session.Session) bool {
	if !strings.HasPrefix(line, "/") {
		return false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return true
	}
	switch fields[0] {
	case "/status":
		a.renderStatus("text", sess)
	case "/statusline":
		if err := a.Statusline(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/terminal-setup", "/terminalSetup":
		if err := a.TerminalSetup(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/remote-env":
		if err := a.RemoteEnv(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/remote-setup", "/web-setup":
		if err := a.RemoteSetup(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/bridge", "/remote-control":
		if err := a.Bridge(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/bridge-kick":
		if err := a.BridgeKick(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/desktop", "/app":
		if err := a.Desktop(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/mobile":
		if err := a.Mobile(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/ios", "/android":
		platform := strings.TrimPrefix(fields[0], "/")
		args := append([]string{platform}, fields[1:]...)
		if err := a.Mobile(args, config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/context":
		if err := a.Context(nil, config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/ctx_viz":
		if err := a.ContextViz(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/sandbox":
		if err := a.Sandbox(); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/sandbox-toggle":
		if err := a.SandboxToggle(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/heapdump":
		if err := a.HeapDump(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/version":
		if err := renderVersion(a.Out, a.Workspace, nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/init":
		if err := a.Init(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/init-verifiers":
		if err := a.InitVerifiers(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/state":
		if err := a.State(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/memory":
		if err := a.Memory(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/project":
		if err := a.Project(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/env":
		if err := a.Env(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/files":
		if err := a.Files(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/search":
		if err := a.Search(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/security-review":
		if err := a.SecurityReview(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/bughunter":
		if err := a.Bughunter(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/review", "/ultrareview":
		if err := a.Review(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/feedback":
		if err := a.Feedback(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/pr":
		if err := a.PullRequestDraft(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/commit-push-pr":
		if err := a.CommitPushPR(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/pr-comments", "/pr_comments":
		if err := a.PRComments(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/install-github-app":
		if err := a.InstallGitHubApp(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/install-slack-app":
		if err := a.InstallSlackApp(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/stickers":
		if err := a.Stickers(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/passes":
		if err := a.Passes(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/issue":
		if err := a.IssueDraft(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/focus":
		if err := a.Focus(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/unfocus":
		if err := a.Unfocus(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/add-dir":
		if err := a.AddDir(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/output-style":
		if err := a.OutputStyle(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/theme":
		if err := a.Theme(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/color":
		if err := a.Theme(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/vim":
		if err := a.Vim(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/effort":
		if err := a.Effort(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/fast":
		if err := a.Fast(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/voice":
		if err := a.Voice(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/chrome":
		if err := a.Chrome(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/privacy-settings":
		if err := a.PrivacySettings(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/keybindings":
		if err := a.Keybindings(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/cost":
		_ = a.ShowCost(config.FlagOverrides{SessionID: sess.ID})
	case "/tokens":
		_ = a.ShowCost(config.FlagOverrides{SessionID: sess.ID})
	case "/usage":
		if err := a.Usage(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/stats":
		if err := a.Usage(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/insights":
		if err := a.Insights(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/think-back", "/thinkback", "/thinkback-play":
		if err := a.ThinkBack(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/extra-usage":
		if err := a.ExtraUsage(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/rate-limit-options":
		if err := a.RateLimitOptions(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/reset-limits":
		if err := a.ResetLimits(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/plan", "/ultraplan":
		if err := a.Plan(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/exit-plan", "/exit_plan_mode":
		if err := a.Plan(append([]string{"exit"}, fields[1:]...)); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/config":
		a.handleConfigSlash(fields[1:])
	case "/model":
		a.handleModelSlash(fields[1:])
	case "/advisor":
		if err := a.Advisor(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/max-tokens":
		a.handleMaxTokensSlash(fields[1:])
	case "/max-turns":
		a.handleMaxTurnsSlash(fields[1:])
	case "/system-prompt":
		fmt.Fprintln(a.Out, a.systemPrompt())
	case "/tool-details":
		a.handleToolDetailsSlash(fields[1:])
	case "/debug-tool-call":
		if err := a.DebugToolCall(ctx, fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/permissions":
		a.handlePermissionsSlash(fields[1:])
	case "/allowed-tools":
		a.handleAllowedToolsSlash(fields[1:])
	case "/doctor":
		if err := a.Doctor(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/compact":
		if err := a.Compact(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		} else if current, err := a.Sessions.Open(sess.ID); err == nil {
			*sess = *current
		}
	case "/undo":
		if err := a.Undo(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/diff":
		a.handleDiffSlash(fields[1:])
	case "/commit":
		a.handleCommitSlash(fields[1:])
	case "/branch":
		if err := a.Branch(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/tag":
		if err := a.Tag(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/log":
		a.handleLogSlash(fields[1:])
	case "/changelog":
		a.handleChangelogSlash(fields[1:])
	case "/release-notes":
		if err := a.ReleaseNotes(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/blame":
		a.handleBlameSlash(fields[1:])
	case "/stash":
		if err := a.Stash(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/git":
		if err := a.Git(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/run":
		if err := a.RunCommand(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/node", "/python":
		language := strings.TrimPrefix(fields[0], "/")
		if err := a.LanguageCommand(ctx, language, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/test":
		if err := a.ProjectCommand(ctx, "test", fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/build":
		if err := a.ProjectCommand(ctx, "build", fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/lint":
		if err := a.ProjectCommand(ctx, "lint", fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/symbols":
		if err := a.Symbols(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/diagnostics":
		if err := a.Diagnostics(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/map":
		if err := a.Map(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/references":
		if err := a.References(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/definition":
		if err := a.Definition(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/hover":
		if err := a.Hover(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/teleport":
		if err := a.Teleport(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/completion":
		if err := a.Completion(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/format":
		if err := a.Format(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/export":
		a.handleExportSlash(fields[1:], sess)
	case "/share":
		if err := a.Share(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/copy":
		if err := a.Copy(ctx, fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/history", "/prompt-history":
		a.handleHistorySlash(fields[1:], sess)
	case "/summary":
		if err := a.Summary(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/rename":
		if len(fields) < 2 {
			fmt.Fprintln(a.Err, "usage: /rename NEW_ID")
			return true
		}
		result, err := a.Sessions.Rename(sess.ID, fields[1])
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return true
		}
		next, err := a.Sessions.Open(result.NewID)
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return true
		}
		*sess = *next
		fmt.Fprintf(a.Err, "session renamed: %s -> %s\n", result.OldID, result.NewID)
	case "/todos":
		if err := a.Todos(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/skills":
		if err := a.Skills(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/commands":
		if err := a.Commands(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/templates":
		if err := a.Templates(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/hooks":
		if err := a.Hooks(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/mcp":
		if err := a.MCP(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/capabilities":
		if err := a.Capabilities(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/acp":
		if err := a.ACP(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/brief":
		if err := a.Brief(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/btw":
		if err := a.BTW(ctx, fields[1:], config.FlagOverrides{}, sess); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/ide":
		if err := a.IDE(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/upgrade":
		if err := a.Upgrade(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/install":
		if err := a.Install(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/agents":
		if err := a.AgentsWithOverrides(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/background":
		if err := a.BackgroundWithOverrides(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/tasks", "/bashes":
		if err := a.BackgroundWithOverrides(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/plugin", "/plugins", "/marketplace":
		if err := a.Marketplace(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/reload-plugins":
		if err := a.ReloadPlugins(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/providers":
		args := fields[1:]
		if len(args) == 0 {
			args = []string{"status"}
		}
		if err := a.Providers(args); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/login":
		if err := a.Login(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/oauth-refresh":
		if err := a.OAuthRefresh(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/logout":
		if err := a.Logout(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/session":
		a.handleSessionSlash(fields[1:], sess)
	case "/backfill-sessions":
		if err := a.BackfillSessions(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/clear":
		a.handleClearSlash(ctx, fields[1:], sess)
	case "/resume":
		a.handleResumeSlash(ctx, fields[1:], sess)
	case "/rewind":
		a.handleRewindSlash(fields[1:], sess)
	default:
		if a.handleCustomSlash(ctx, line, sess) {
			return true
		}
		if a.handleSkillSlash(ctx, line, sess) {
			return true
		}
		if _, ok := slash.Lookup(fields[0]); !ok {
			fmt.Fprintf(a.Err, "unknown slash command: %s\n", fields[0])
		}
	}
	return true
}

func (a *App) handleSkillSlash(ctx context.Context, line string, sess *session.Session) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	name := strings.TrimPrefix(fields[0], "/")
	name = strings.ReplaceAll(name, "/", ":")
	skill, err := skills.Find(a.Config.ConfigHome, a.Workspace, name)
	if err != nil {
		if errors.Is(err, skills.ErrNotFound) {
			return false
		}
		fmt.Fprintln(a.Err, "error:", err)
		return true
	}
	if !skill.UserInvocable {
		return false
	}
	args := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	rendered := skills.RenderInvocation(skill, args)
	if err := a.runSessionTurnWithOptions(ctx, "repl", sess, rendered, "idle", turnOptions{Skill: &skill}); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
	return true
}

func (a *App) handleCustomSlash(ctx context.Context, line string, sess *session.Session) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	command, err := customcommands.Find(a.Config.ConfigHome, a.Workspace, fields[0])
	if err != nil {
		if errors.Is(err, customcommands.ErrNotFound) {
			return false
		}
		fmt.Fprintln(a.Err, "error:", err)
		return true
	}
	args := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	rendered := customcommands.Render(command, args)
	if strings.TrimSpace(rendered.Rendered) == "" {
		fmt.Fprintf(a.Err, "custom command %s rendered an empty prompt\n", fields[0])
		return true
	}
	if err := a.runSessionTurnWithOptions(ctx, "repl", sess, rendered.Rendered, "idle", turnOptions{AllowedTools: command.AllowedTools}); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
	return true
}

func (a *App) handleClearSlash(ctx context.Context, args []string, sess *session.Session) {
	for _, arg := range args {
		if arg != "--confirm" {
			fmt.Fprintln(a.Err, "usage: /clear [--confirm]")
			return
		}
	}
	next, err := a.Sessions.Open("")
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.runSessionEndHook(ctx, sess, "clear"); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.runSessionStartHook(ctx, next, "clear"); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	*sess = *next
	a.writeWorkerState("repl", "idle", sess, "")
	fmt.Fprintf(a.Err, "session cleared: %s\n", sess.ID)
}

func (a *App) handleResumeSlash(ctx context.Context, args []string, sess *session.Session) {
	id := "latest"
	if len(args) > 0 {
		id = args[0]
	}
	if len(args) > 1 {
		fmt.Fprintln(a.Err, "usage: /resume [session-id|latest]")
		return
	}
	next, err := a.Sessions.Open(id)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.restoreTodosFromSession(next); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.runSessionEndHook(ctx, sess, "resume"); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.runSessionStartHook(ctx, next, "resume"); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	*sess = *next
	a.writeWorkerState("repl", "idle", sess, "")
	fmt.Fprintf(a.Err, "session resumed: %s\n", sess.ID)
}

func (a *App) handleRewindSlash(args []string, sess *session.Session) {
	if a.Sessions == nil {
		fmt.Fprintln(a.Err, "error: session store is not configured")
		return
	}
	defaultSession := ""
	if sess != nil {
		defaultSession = sess.ID
	}
	req, err := parseRewindArgs(args, config.FlagOverrides{}, defaultSession)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	result, err := a.Sessions.Rewind(req.SessionID, req.Messages)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	next, err := a.Sessions.Open(result.SessionID)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if sess != nil {
		*sess = *next
		a.writeWorkerState("repl", "idle", sess, "")
	}
	a.renderRewindReport(req.Format, result)
}

func (a *App) handleHistorySlash(args []string, sess *session.Session) {
	overrides := config.FlagOverrides{}
	if sess != nil {
		overrides.SessionID = sess.ID
	}
	if err := a.History(args, overrides); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleConfigSlash(args []string) {
	payload, err := a.runtimeConfigPayload(args)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
}

type advisorRequest struct {
	Action string
	Model  string
	Format string
	Target string
	Path   string
}

type advisorReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	Model     string `json:"model,omitempty"`
	MainModel string `json:"main_model,omitempty"`
	Path      string `json:"path,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (a *App) Model(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(a.Out, "model=%s\n", a.Config.Model)
		return nil
	}
	model := strings.TrimSpace(strings.Join(args, " "))
	if model == "" {
		return errors.New("usage: codog model [name]")
	}
	a.Config.Model = model
	fmt.Fprintf(a.Out, "model=%s\n", a.Config.Model)
	return nil
}

func (a *App) Advisor(args []string) error {
	req, err := parseAdvisorArgs(args)
	if err != nil {
		return err
	}
	report := advisorReport{
		Kind:      "advisor",
		Action:    req.Action,
		Status:    "ok",
		Model:     a.Config.AdvisorModel,
		MainModel: a.Config.Model,
	}
	switch req.Action {
	case "show":
		if report.Model == "" {
			report.Message = "Advisor is not set. Use advisor MODEL to enable it."
		} else {
			report.Message = "Advisor model preference is set."
		}
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "advisor_model", req.Model); err != nil {
			return err
		}
		a.Config.AdvisorModel = req.Model
		report.Model = req.Model
		report.Path = path
		report.Message = "Advisor model preference saved."
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "advisor_model"); err != nil {
			return err
		}
		previous := a.Config.AdvisorModel
		a.Config.AdvisorModel = ""
		report.Model = ""
		report.Path = path
		if previous == "" {
			report.Message = "Advisor was already unset."
		} else {
			report.Message = "Advisor model preference cleared."
		}
	default:
		return fmt.Errorf("unknown advisor command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderAdvisorReport(a.Out, report)
	return nil
}

func parseAdvisorArgs(args []string) (advisorRequest, error) {
	req := advisorRequest{Action: "show", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("advisor output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("advisor target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("advisor config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			rest = append(rest, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "advisor"); err != nil {
		return req, err
	}
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "show", "status":
		req.Action = "show"
		if len(rest) > 1 {
			return req, fmt.Errorf("unexpected advisor argument %q", rest[1])
		}
	case "unset", "off", "disable", "disabled", "clear", "reset":
		req.Action = "clear"
		if len(rest) > 1 {
			return req, fmt.Errorf("unexpected advisor argument %q", rest[1])
		}
	case "set":
		req.Action = "set"
		if len(rest) < 2 {
			return req, errors.New("advisor model is required")
		}
		req.Model = strings.TrimSpace(strings.Join(rest[1:], " "))
	default:
		req.Action = "set"
		req.Model = strings.TrimSpace(strings.Join(rest, " "))
	}
	if req.Action == "set" && req.Model == "" {
		return req, errors.New("advisor model is required")
	}
	return req, nil
}

func renderAdvisorReport(out io.Writer, report advisorReport) {
	fmt.Fprintln(out, "Advisor")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.MainModel != "" {
		fmt.Fprintf(out, "  Main model       %s\n", report.MainModel)
	}
	if report.Model != "" {
		fmt.Fprintf(out, "  Advisor model    %s\n", report.Model)
	} else {
		fmt.Fprintln(out, "  Advisor model    unset")
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) handleModelSlash(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(a.Err, "model=%s\n", a.Config.Model)
		return
	}
	model := strings.TrimSpace(strings.Join(args, " "))
	if model == "" {
		fmt.Fprintln(a.Err, "usage: /model [name]")
		return
	}
	a.Config.Model = model
	fmt.Fprintf(a.Err, "model=%s\n", a.Config.Model)
}

func (a *App) MaxTokens(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(a.Out, "max_tokens=%d\n", a.Config.MaxTokens)
		return nil
	}
	if len(args) != 1 {
		return errors.New("usage: codog max-tokens [count]")
	}
	value, err := strconv.Atoi(args[0])
	if err != nil || value <= 0 {
		return errors.New("max_tokens must be a positive integer")
	}
	a.Config.MaxTokens = value
	fmt.Fprintf(a.Out, "max_tokens=%d\n", a.Config.MaxTokens)
	return nil
}

func (a *App) handleMaxTokensSlash(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(a.Err, "max_tokens=%d\n", a.Config.MaxTokens)
		return
	}
	if len(args) != 1 {
		fmt.Fprintln(a.Err, "usage: /max-tokens [count]")
		return
	}
	value, err := strconv.Atoi(args[0])
	if err != nil || value <= 0 {
		fmt.Fprintln(a.Err, "max_tokens must be a positive integer")
		return
	}
	a.Config.MaxTokens = value
	fmt.Fprintf(a.Err, "max_tokens=%d\n", a.Config.MaxTokens)
}

func (a *App) MaxTurns(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(a.Out, "max_turns=%d\n", a.Config.MaxTurns)
		return nil
	}
	if len(args) != 1 {
		return errors.New("usage: codog max-turns [count]")
	}
	value, err := strconv.Atoi(args[0])
	if err != nil || value <= 0 {
		return errors.New("max_turns must be a positive integer")
	}
	a.Config.MaxTurns = value
	fmt.Fprintf(a.Out, "max_turns=%d\n", a.Config.MaxTurns)
	return nil
}

func (a *App) handleMaxTurnsSlash(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(a.Err, "max_turns=%d\n", a.Config.MaxTurns)
		return
	}
	if len(args) != 1 {
		fmt.Fprintln(a.Err, "usage: /max-turns [count]")
		return
	}
	value, err := strconv.Atoi(args[0])
	if err != nil || value <= 0 {
		fmt.Fprintln(a.Err, "max_turns must be a positive integer")
		return
	}
	a.Config.MaxTurns = value
	fmt.Fprintf(a.Err, "max_turns=%d\n", a.Config.MaxTurns)
}

func (a *App) handleToolDetailsSlash(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(a.Err, "usage: /tool-details TOOL")
		return
	}
	if a.Tools == nil {
		fmt.Fprintln(a.Err, "error: tool registry is not initialized")
		return
	}
	info, ok := a.Tools.Info(args[0])
	if !ok {
		fmt.Fprintf(a.Err, "unknown tool: %s\n", args[0])
		return
	}
	renderToolInfo(a.Out, info)
}

func renderToolInfo(out io.Writer, info tools.ToolInfo) {
	fmt.Fprintln(out, "Tool")
	fmt.Fprintf(out, "  Name             %s\n", info.Name)
	fmt.Fprintf(out, "  Permission       %s\n", info.Permission)
	fmt.Fprintf(out, "  Description      %s\n", info.Description)
	data, _ := json.MarshalIndent(info.InputSchema, "  ", "  ")
	fmt.Fprintln(out, "  Input schema")
	fmt.Fprintln(out, string(data))
}

type debugToolCallRequest struct {
	Tool      string
	Input     json.RawMessage
	Format    string
	SessionID string
}

type debugToolCallReport struct {
	Kind       string           `json:"kind"`
	Tool       string           `json:"tool"`
	Permission tools.Permission `json:"permission"`
	Success    bool             `json:"success"`
	DurationMS int64            `json:"duration_ms"`
	Output     string           `json:"output,omitempty"`
	Error      string           `json:"error,omitempty"`
}

func (a *App) DebugToolCall(ctx context.Context, args []string, overrides config.FlagOverrides) error {
	req, err := parseDebugToolCallArgs(args, overrides)
	if err != nil {
		return err
	}
	if a.Tools == nil {
		return errors.New("tool registry is not initialized")
	}
	info, ok := a.Tools.Info(req.Tool)
	if !ok && !a.mcpToolsLoaded && len(a.Config.MCPServers) > 0 {
		if err := a.RegisterMCPTools(ctx); err != nil {
			return err
		}
		info, ok = a.Tools.Info(req.Tool)
	}
	if !ok {
		return fmt.Errorf("unknown tool %q", req.Tool)
	}
	start := time.Now()
	output, execErr := a.Tools.Execute(tools.ContextWithSessionID(ctx, req.SessionID), req.Tool, req.Input, a.prompter(req.SessionID))
	report := debugToolCallReport{
		Kind:       "debug_tool_call",
		Tool:       info.Name,
		Permission: info.Permission,
		Success:    execErr == nil,
		DurationMS: time.Since(start).Milliseconds(),
		Output:     output,
	}
	if execErr != nil {
		report.Error = execErr.Error()
	}
	a.auditToolUse(req.SessionID)(runloop.ToolCall{
		Name:    info.Name,
		Input:   string(req.Input),
		Output:  output,
		IsError: execErr != nil,
	})
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDebugToolCallReport(a.Out, report)
	return nil
}

func parseDebugToolCallArgs(args []string, overrides config.FlagOverrides) (debugToolCallRequest, error) {
	req := debugToolCallRequest{Format: "text", SessionID: firstNonEmpty(overrides.Resume, overrides.SessionID)}
	inputParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("debug-tool-call output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("debug-tool-call session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("debug-tool-call resume session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-") && req.Tool == "":
			return req, fmt.Errorf("unknown debug-tool-call flag %q", arg)
		default:
			if req.Tool == "" {
				req.Tool = arg
				continue
			}
			inputParts = append(inputParts, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "debug-tool-call"); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.Tool) == "" {
		return req, errors.New("usage: codog debug-tool-call TOOL JSON")
	}
	input := strings.TrimSpace(strings.Join(inputParts, " "))
	if input == "" {
		return req, errors.New("usage: codog debug-tool-call TOOL JSON")
	}
	if !json.Valid([]byte(input)) {
		return req, errors.New("debug-tool-call input must be valid JSON")
	}
	req.Input = json.RawMessage(input)
	return req, nil
}

func renderDebugToolCallReport(out io.Writer, report debugToolCallReport) {
	fmt.Fprintln(out, "Tool Call")
	fmt.Fprintf(out, "  Tool             %s\n", report.Tool)
	fmt.Fprintf(out, "  Permission       %s\n", report.Permission)
	fmt.Fprintf(out, "  Success          %t\n", report.Success)
	fmt.Fprintf(out, "  Duration         %dms\n", report.DurationMS)
	if report.Error != "" {
		fmt.Fprintf(out, "  Error            %s\n", report.Error)
	}
	if report.Output != "" {
		fmt.Fprintln(out, "Output:")
		fmt.Fprintln(out, report.Output)
	}
}

func (a *App) Permissions(args []string) error {
	if len(args) == 0 || args[0] == "show" {
		data, _ := json.MarshalIndent(map[string]any{
			"permission_mode":  a.Config.PermissionMode,
			"permission_rules": a.Config.PermissionRules,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	mode := args[0]
	if args[0] == "mode" || args[0] == "set" {
		if len(args) < 2 {
			return errors.New("usage: codog permissions [read-only|workspace-write|danger-full-access|prompt|allow]")
		}
		mode = args[1]
	}
	if !validPermissionMode(mode) {
		return fmt.Errorf("unknown permission mode: %s", mode)
	}
	a.Config.PermissionMode = mode
	fmt.Fprintf(a.Out, "permission_mode=%s\n", a.Config.PermissionMode)
	return nil
}

func (a *App) handlePermissionsSlash(args []string) {
	if len(args) == 0 || args[0] == "show" {
		data, _ := json.MarshalIndent(map[string]any{
			"permission_mode":  a.Config.PermissionMode,
			"permission_rules": a.Config.PermissionRules,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	mode := args[0]
	if args[0] == "mode" || args[0] == "set" {
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /permissions [read-only|workspace-write|danger-full-access|prompt|allow]")
			return
		}
		mode = args[1]
	}
	if !validPermissionMode(mode) {
		fmt.Fprintf(a.Err, "unknown permission mode: %s\n", mode)
		return
	}
	a.Config.PermissionMode = mode
	fmt.Fprintf(a.Err, "permission_mode=%s\n", a.Config.PermissionMode)
}

func (a *App) AllowedTools(args []string) error {
	action := "list"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "list", "show":
	case "add":
		if len(args) < 2 {
			return errors.New("usage: codog allowed-tools add TOOL [TOOL...]")
		}
		if err := a.validateToolRuleValues("allowed-tools add", args[1:]); err != nil {
			return err
		}
		a.Config.PermissionRules.Allow = addRuleValues(a.Config.PermissionRules.Allow, args[1:])
	case "remove", "rm", "delete":
		if len(args) < 2 {
			return errors.New("usage: codog allowed-tools remove TOOL [TOOL...]")
		}
		a.Config.PermissionRules.Allow = removeRuleValues(a.Config.PermissionRules.Allow, args[1:])
	case "clear":
		a.Config.PermissionRules.Allow = nil
	default:
		return fmt.Errorf("unknown allowed-tools action: %s", args[0])
	}
	renderAllowedTools(a.Out, a.Config.PermissionRules.Allow)
	return nil
}

func (a *App) handleAllowedToolsSlash(args []string) {
	action := "list"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "list", "show":
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /allowed-tools add TOOL [TOOL...]")
			return
		}
		if err := a.validateToolRuleValues("/allowed-tools add", args[1:]); err != nil {
			fmt.Fprintln(a.Err, err)
			return
		}
		a.Config.PermissionRules.Allow = addRuleValues(a.Config.PermissionRules.Allow, args[1:])
	case "remove", "rm", "delete":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /allowed-tools remove TOOL [TOOL...]")
			return
		}
		a.Config.PermissionRules.Allow = removeRuleValues(a.Config.PermissionRules.Allow, args[1:])
	case "clear":
		a.Config.PermissionRules.Allow = nil
	default:
		fmt.Fprintf(a.Err, "unknown /allowed-tools action: %s\n", args[0])
		return
	}
	renderAllowedTools(a.Out, a.Config.PermissionRules.Allow)
}

func renderAllowedTools(out io.Writer, rules []string) {
	fmt.Fprintln(out, "Allowed tools")
	if len(rules) == 0 {
		fmt.Fprintln(out, "  Result           no allow rules configured")
		return
	}
	fmt.Fprintf(out, "  Count            %d\n", len(rules))
	fmt.Fprintln(out)
	for index, rule := range rules {
		fmt.Fprintf(out, "  %d. %s\n", index+1, rule)
	}
}

func addRuleValues(current []string, values []string) []string {
	next := append([]string(nil), current...)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || containsFold(next, value) {
			continue
		}
		next = append(next, value)
	}
	return next
}

func removeRuleValues(current []string, values []string) []string {
	remove := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			remove[value] = struct{}{}
		}
	}
	next := make([]string, 0, len(current))
	for _, value := range current {
		if _, ok := remove[strings.ToLower(value)]; ok {
			continue
		}
		next = append(next, value)
	}
	return next
}

func (a *App) runtimeConfigPayload(args []string) (any, error) {
	cfg := redactedConfig(a.Config)
	return configSectionPayload(cfg, args)
}

func renderConfigInspection(out io.Writer, cfg config.Config, paths []string, args []string) error {
	req, err := parseConfigInspectionArgs(args)
	if err != nil {
		return err
	}
	args = req.Args
	if len(args) == 0 {
		return renderConfigInspectionPayload(out, req.Format, map[string]any{"config": cfg, "paths": paths})
	}
	if strings.EqualFold(args[0], "set") || strings.EqualFold(args[0], "unset") {
		report, err := mutateConfigFile(args, paths)
		if err != nil {
			return err
		}
		return renderConfigInspectionPayload(out, req.Format, report)
	}
	if strings.EqualFold(args[0], "paths") {
		return renderConfigInspectionPayload(out, req.Format, map[string]any{"paths": paths})
	}
	if strings.EqualFold(args[0], "show") {
		if len(args) > 1 {
			return renderCLIError(out, unexpectedExtraArgsError{
				Command: "config show",
				Args:    append([]string(nil), args[1:]...),
				Usage:   "codog config show [--json|--output-format text|json]",
			}, req.Format)
		}
		return renderConfigInspectionPayload(out, req.Format, map[string]any{"config": cfg, "paths": paths})
	}
	if strings.EqualFold(args[0], "get") {
		if len(args) < 2 {
			return errors.New("usage: codog config get SECTION")
		}
		args = args[1:]
	}
	payload, err := configSectionPayload(cfg, args)
	if err != nil {
		return err
	}
	return renderConfigInspectionPayload(out, req.Format, payload)
}

type configInspectionRequest struct {
	Format string
	Args   []string
}

func parseConfigInspectionArgs(args []string) (configInspectionRequest, error) {
	req := configInspectionRequest{Format: "json"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("config output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		default:
			req.Args = append(req.Args, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "config")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	return req, nil
}

func renderConfigInspectionPayload(out io.Writer, format string, payload any) error {
	if format == "text" {
		renderConfigInspectionText(out, payload)
		return nil
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(out, string(data))
	return nil
}

func renderConfigInspectionText(out io.Writer, payload any) {
	fmt.Fprintln(out, "Config")
	switch value := payload.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(out, "  %-16s %s\n", key, configTextValue(value[key]))
		}
	case config.MutationReport:
		fmt.Fprintf(out, "  Status           %s\n", value.Status)
		fmt.Fprintf(out, "  Action           %s\n", value.Action)
		fmt.Fprintf(out, "  Path             %s\n", value.Path)
		fmt.Fprintf(out, "  Key              %s\n", value.Key)
	default:
		data, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintf(out, "  %s\n", strings.ReplaceAll(string(data), "\n", "\n  "))
	}
}

func configTextValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []string:
		return strings.Join(typed, ", ")
	default:
		data, _ := json.Marshal(typed)
		return string(data)
	}
}

type configMutationRequest struct {
	Action string
	Key    string
	Value  any
	Path   string
	Target string
}

func mutateConfigFile(args []string, paths []string) (config.MutationReport, error) {
	req, err := parseConfigMutationArgs(args)
	if err != nil {
		return config.MutationReport{}, err
	}
	path, err := configMutationPath(req, paths)
	if err != nil {
		return config.MutationReport{}, err
	}
	switch req.Action {
	case "set":
		return config.SetFileValue(path, req.Key, req.Value)
	case "unset":
		return config.UnsetFileValue(path, req.Key)
	default:
		return config.MutationReport{}, fmt.Errorf("unknown config action %q", req.Action)
	}
}

func parseConfigMutationArgs(args []string) (configMutationRequest, error) {
	if len(args) == 0 {
		return configMutationRequest{}, errors.New("config action is required")
	}
	req := configMutationRequest{Action: strings.ToLower(args[0])}
	var positionals []string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--target":
			if i+1 >= len(args) {
				return req, errors.New("config target is required")
			}
			i++
			req.Target = args[i]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			if i+1 >= len(args) {
				return req, errors.New("config path is required")
			}
			i++
			req.Path = args[i]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			positionals = append(positionals, arg)
		}
	}
	switch req.Action {
	case "set":
		if len(positionals) < 2 {
			return req, errors.New("usage: codog config set KEY VALUE [--target user|project|local|--path PATH]")
		}
		req.Key = positionals[0]
		req.Value = config.ParseConfigValue(strings.Join(positionals[1:], " "))
	case "unset":
		if len(positionals) != 1 {
			return req, errors.New("usage: codog config unset KEY [--target user|project|local|--path PATH]")
		}
		req.Key = positionals[0]
	default:
		return req, fmt.Errorf("unknown config action %q", req.Action)
	}
	return req, nil
}

func configMutationPath(req configMutationRequest, paths []string) (string, error) {
	if req.Path != "" {
		return req.Path, nil
	}
	target := strings.ToLower(strings.TrimSpace(req.Target))
	switch target {
	case "", "user":
		if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
			return "", errors.New("user config path is unavailable")
		}
		return paths[0], nil
	case "project":
		return ".codog.json", nil
	case "local":
		return ".codog.local.json", nil
	default:
		return "", fmt.Errorf("unknown config target %q", req.Target)
	}
}

func configSectionPayload(cfg config.Config, args []string) (any, error) {
	if len(args) == 0 {
		return cfg, nil
	}
	switch strings.ToLower(args[0]) {
	case "model":
		return map[string]any{"model": cfg.Model, "advisor_model": cfg.AdvisorModel, "max_tokens": cfg.MaxTokens, "max_turns": cfg.MaxTurns, "reasoning_effort": cfg.ReasoningEffort, "fast_mode": fastModeEnabled(cfg.FastMode)}, nil
	case "interface", "ui":
		return map[string]any{"theme": cfg.Theme, "editorMode": cfg.EditorMode}, nil
	case "privacy", "privacy-settings":
		return map[string]any{"privacy_settings": cfg.Privacy}, nil
	case "permissions", "permission":
		return map[string]any{"permission_mode": cfg.PermissionMode, "permission_rules": cfg.PermissionRules}, nil
	case "mcp":
		return cfg.MCPServers, nil
	case "hooks":
		return cfg.Hooks, nil
	case "skills":
		return map[string]any{"enabled_skills": cfg.EnabledSkills}, nil
	case "auth":
		return map[string]any{"api_key": cfg.APIKey, "auth_token": cfg.AuthToken, "base_url": cfg.BaseURL}, nil
	default:
		return nil, fmt.Errorf("unknown config section %q", args[0])
	}
}

func redactedConfig(cfg config.Config) config.Config {
	cfg.APIKey = redact(cfg.APIKey)
	cfg.AuthToken = redact(cfg.AuthToken)
	cfg.Future.RemoteAuthToken = redact(cfg.Future.RemoteAuthToken)
	cfg.Future.EditorBridgeToken = redact(cfg.Future.EditorBridgeToken)
	return cfg
}

func validPermissionMode(mode string) bool {
	switch tools.Permission(mode) {
	case tools.PermissionReadOnly, tools.PermissionWorkspace, tools.PermissionDanger, tools.PermissionPrompt, tools.PermissionAllow:
		return true
	default:
		return false
	}
}

func (a *App) Git(args []string) error {
	var err error
	args, err = rewriteLeadingGitOutputFormat(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: codog git status [--json|--output-format text|json] | git diff [--staged] [PATH...] [--json|--output-format text|json] | git log [count] [--json|--output-format text|json] | git changelog [count] [--json|--output-format text|json] | git blame FILE [line] [--json|--output-format text|json] | git branch [ARGS...] | git tag [ARGS...] | git stash [list|push|apply|pop] | git commit [--all] MESSAGE [--json|--output-format text|json]")
	}
	switch args[0] {
	case "status":
		return a.GitStatus(args[1:])
	case "diff":
		return a.Diff(args[1:])
	case "log":
		return a.GitLog(args[1:])
	case "changelog":
		return a.Changelog(args[1:])
	case "blame":
		return a.GitBlame(args[1:])
	case "stash":
		return a.Stash(args[1:])
	case "commit":
		return a.GitCommit(args[1:], "json")
	case "branch":
		return a.Branch(args[1:])
	case "tag":
		return a.Tag(args[1:])
	default:
		return fmt.Errorf("unknown git command %q", args[0])
	}
	return nil
}

func rewriteLeadingGitOutputFormat(args []string) ([]string, error) {
	format := ""
	rest := args
	for len(rest) > 0 {
		arg := rest[0]
		switch {
		case arg == "--json":
			format = "json"
			rest = rest[1:]
		case arg == "--output-format" || arg == "-o":
			if len(rest) < 2 {
				return nil, errors.New("git output format is required")
			}
			format = rest[1]
			rest = rest[2:]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
			rest = rest[1:]
		default:
			if format == "" {
				return args, nil
			}
			normalized, err := normalizeTextOrJSON(format, "git")
			if err != nil {
				return nil, err
			}
			out := append([]string(nil), rest...)
			if gitSubcommandAcceptsOutputFormat(out[0]) && !argsHaveOutputFormat(out[1:]) {
				out = append(out, "--output-format", normalized)
			}
			return out, nil
		}
	}
	return rest, nil
}

func gitSubcommandAcceptsOutputFormat(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "status", "diff", "log", "changelog", "blame", "branch", "tag", "stash", "commit":
		return true
	default:
		return false
	}
}

func normalizeTextOrJSON(format, command string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if err := validateTextOrJSON(normalized, command); err != nil {
		return "", err
	}
	return normalized, nil
}

type gitStatusRequest struct {
	Format string
}

type gitStatusEntry struct {
	Code         string `json:"code"`
	Index        string `json:"index"`
	Worktree     string `json:"worktree"`
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
}

type gitStatusReport struct {
	Kind       string           `json:"kind"`
	Action     string           `json:"action"`
	Status     string           `json:"status"`
	Clean      bool             `json:"clean"`
	BranchLine string           `json:"branch_line,omitempty"`
	Branch     string           `json:"branch,omitempty"`
	Entries    []gitStatusEntry `json:"entries"`
	Raw        string           `json:"raw"`
}

func (a *App) GitStatus(args []string) error {
	req, err := parseGitStatusArgs(args)
	if err != nil {
		return err
	}
	raw, err := gitops.Status(a.Workspace)
	if err != nil {
		return err
	}
	report := buildGitStatusReport(raw)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, raw)
	return nil
}

func parseGitStatusArgs(args []string) (gitStatusRequest, error) {
	req := gitStatusRequest{Format: "text"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("git status output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--short" || arg == "-s" || arg == "--branch" || arg == "-b" || arg == "-sb" || arg == "-bs" || arg == "--porcelain":
		case strings.HasPrefix(arg, "--porcelain="):
		default:
			return req, fmt.Errorf("unknown git status flag %q", arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "git status")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	return req, nil
}

func buildGitStatusReport(raw string) gitStatusReport {
	report := gitStatusReport{
		Kind:    "git_status",
		Action:  "show",
		Status:  "ok",
		Entries: []gitStatusEntry{},
		Raw:     raw,
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			report.BranchLine = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			report.Branch = parseGitStatusBranch(report.BranchLine)
			continue
		}
		if entry, ok := parseGitStatusEntry(line); ok {
			report.Entries = append(report.Entries, entry)
		}
	}
	report.Clean = len(report.Entries) == 0
	return report
}

func parseGitStatusBranch(line string) string {
	branch := strings.TrimSpace(line)
	if index := strings.Index(branch, "..."); index >= 0 {
		branch = branch[:index]
	}
	if index := strings.Index(branch, " ["); index >= 0 {
		branch = branch[:index]
	}
	return strings.TrimSpace(branch)
}

func parseGitStatusEntry(line string) (gitStatusEntry, bool) {
	if len(line) < 3 {
		return gitStatusEntry{}, false
	}
	code := line[:2]
	path := strings.TrimSpace(line[3:])
	if path == "" {
		return gitStatusEntry{}, false
	}
	entry := gitStatusEntry{
		Code:     code,
		Index:    strings.TrimSpace(string(code[0])),
		Worktree: strings.TrimSpace(string(code[1])),
		Path:     path,
	}
	if before, after, ok := strings.Cut(path, " -> "); ok {
		entry.OriginalPath = strings.TrimSpace(before)
		entry.Path = strings.TrimSpace(after)
	}
	return entry, true
}

type diffRequest struct {
	Format string
	Staged bool
	Paths  []string
}

type diffReport struct {
	Kind   string   `json:"kind"`
	Action string   `json:"action"`
	Status string   `json:"status"`
	Staged bool     `json:"staged"`
	Empty  bool     `json:"empty"`
	Bytes  int      `json:"bytes"`
	Paths  []string `json:"paths,omitempty"`
	Diff   string   `json:"diff"`
}

func (a *App) Diff(args []string) error {
	req, err := parseDiffArgs(args)
	if err != nil {
		return err
	}
	report, err := a.buildDiffReport(req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, report.Diff)
	return nil
}

func (a *App) buildDiffReport(req diffRequest) (diffReport, error) {
	diff, err := gitops.DiffWithOptions(a.Workspace, gitops.DiffOptions{Staged: req.Staged, Paths: req.Paths})
	if err != nil {
		return diffReport{}, err
	}
	return diffReport{
		Kind:   "diff",
		Action: "show",
		Status: "ok",
		Staged: req.Staged,
		Empty:  diff == "",
		Bytes:  len(diff),
		Paths:  append([]string(nil), req.Paths...),
		Diff:   diff,
	}, nil
}

func parseDiffArgs(args []string) (diffRequest, error) {
	req := diffRequest{Format: "text"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("diff output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--staged" || arg == "--cached":
			req.Staged = true
		case arg == "--":
			req.Paths = append(req.Paths, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown diff flag %q", arg)
		default:
			req.Paths = append(req.Paths, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "diff")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	return req, nil
}

type gitLogRequest struct {
	Format string
	Limit  int
}

type gitLogReport struct {
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Status  string            `json:"status"`
	Limit   int               `json:"limit"`
	Count   int               `json:"count"`
	Entries []gitops.LogEntry `json:"entries"`
	Raw     string            `json:"raw"`
}

func (a *App) GitLog(args []string) error {
	req, err := parseGitLogArgs(args)
	if err != nil {
		return err
	}
	raw, err := gitops.Log(a.Workspace, req.Limit)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		entries, err := gitops.LogEntries(a.Workspace, req.Limit)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(gitLogReport{
			Kind:    "git_log",
			Action:  "show",
			Status:  "ok",
			Limit:   req.Limit,
			Count:   len(entries),
			Entries: entries,
			Raw:     raw,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, raw)
	return nil
}

func parseGitLogArgs(args []string) (gitLogRequest, error) {
	req := gitLogRequest{Format: "text", Limit: 20}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("git log output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown git log flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "git log")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 {
		return req, nil
	}
	if len(positionals) > 1 {
		return req, errors.New("usage: codog git log [count] [--json|--output-format text|json]")
	}
	limit, err := strconv.Atoi(positionals[0])
	if err != nil || limit <= 0 {
		return req, errors.New("git log count must be a positive integer")
	}
	req.Limit = limit
	return req, nil
}

type branchRequest struct {
	Format     string
	Action     string
	Name       string
	NewName    string
	Base       string
	StartPoint string
	Switch     bool
	Force      bool
}

type branchReport struct {
	Kind      string                  `json:"kind"`
	Action    string                  `json:"action"`
	Status    string                  `json:"status"`
	Current   string                  `json:"current"`
	Branches  []gitops.BranchInfo     `json:"branches,omitempty"`
	Freshness *gitops.BranchFreshness `json:"freshness,omitempty"`
	Output    string                  `json:"output,omitempty"`
}

func (a *App) Branch(args []string) error {
	req, err := parseBranchArgs(args)
	if err != nil {
		return err
	}
	report := branchReport{Kind: "branch", Action: req.Action, Status: "ok"}
	switch req.Action {
	case "list":
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "current":
		current, err := gitops.Branch(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = current
	case "create":
		output, err := gitops.CreateBranch(a.Workspace, req.Name, req.StartPoint, req.Switch)
		if err != nil {
			return err
		}
		report.Output = output
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "switch":
		output, err := gitops.SwitchBranch(a.Workspace, req.Name)
		if err != nil {
			return err
		}
		report.Output = output
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "delete":
		output, err := gitops.DeleteBranch(a.Workspace, req.Name, req.Force)
		if err != nil {
			return err
		}
		report.Output = output
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "rename":
		output, err := gitops.RenameBranch(a.Workspace, req.Name, req.NewName)
		if err != nil {
			return err
		}
		report.Output = output
		list, err := gitops.ListBranches(a.Workspace)
		if err != nil {
			return err
		}
		report.Current = list.Current
		report.Branches = list.Branches
	case "freshness":
		freshness, err := gitops.CheckBranchFreshness(a.Workspace, req.Name, req.Base)
		if err != nil {
			return err
		}
		report.Current = freshness.Branch
		report.Freshness = &freshness
	default:
		return fmt.Errorf("unknown branch action %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBranchReport(a.Out, report)
	return nil
}

func parseBranchArgs(args []string) (branchRequest, error) {
	req := branchRequest{Format: "text", Action: "list"}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("branch output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--switch" || arg == "--checkout":
			req.Switch = true
		case arg == "--force" || arg == "-f":
			req.Force = true
		case arg == "--base":
			i++
			if i >= len(args) {
				return req, errors.New("branch base is required")
			}
			req.Base = args[i]
		case strings.HasPrefix(arg, "--base="):
			req.Base = strings.TrimPrefix(arg, "--base=")
		default:
			positionals = append(positionals, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown branch output format %q", req.Format)
	}
	if len(positionals) == 0 {
		return req, nil
	}
	req.Action = strings.ToLower(positionals[0])
	rest := positionals[1:]
	switch req.Action {
	case "list", "show":
		req.Action = "list"
	case "current":
	case "create", "new":
		req.Action = "create"
		if len(rest) == 0 {
			return req, errors.New("branch create requires a name")
		}
		req.Name = rest[0]
		if len(rest) > 1 {
			req.StartPoint = rest[1]
		}
	case "switch", "checkout":
		req.Action = "switch"
		if len(rest) == 0 {
			return req, errors.New("branch switch requires a name")
		}
		req.Name = rest[0]
	case "delete", "del", "remove", "rm":
		req.Action = "delete"
		if len(rest) == 0 {
			return req, errors.New("branch delete requires a name")
		}
		req.Name = rest[0]
	case "rename", "mv":
		req.Action = "rename"
		switch len(rest) {
		case 0:
			return req, errors.New("branch rename requires a new name")
		case 1:
			req.NewName = rest[0]
		default:
			req.Name = rest[0]
			req.NewName = rest[1]
		}
	case "freshness", "fresh", "stale":
		req.Action = "freshness"
		if len(rest) > 0 {
			req.Name = rest[0]
		}
		if len(rest) > 1 {
			req.Base = rest[1]
		}
	default:
		return req, fmt.Errorf("unknown branch action %q", positionals[0])
	}
	return req, nil
}

func renderBranchReport(out io.Writer, report branchReport) {
	fmt.Fprintln(out, "Branches")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Current          %s\n", report.Current)
	if strings.TrimSpace(report.Output) != "" {
		fmt.Fprintf(out, "  Output           %s\n", strings.ReplaceAll(strings.TrimSpace(report.Output), "\n", "\n                   "))
	}
	if report.Freshness != nil {
		freshness := report.Freshness
		fmt.Fprintf(out, "  Base             %s\n", freshness.Base)
		fmt.Fprintf(out, "  Freshness        %s\n", freshness.Status)
		fmt.Fprintf(out, "  Ahead            %d\n", freshness.Ahead)
		fmt.Fprintf(out, "  Behind           %d\n", freshness.Behind)
		if len(freshness.MissingFixes) > 0 {
			fmt.Fprintln(out, "  Missing commits")
			for _, subject := range freshness.MissingFixes {
				fmt.Fprintf(out, "    - %s\n", subject)
			}
		}
	}
	if len(report.Branches) == 0 {
		return
	}
	fmt.Fprintf(out, "  Count            %d\n", len(report.Branches))
	fmt.Fprintln(out)
	for _, branch := range report.Branches {
		marker := " "
		if branch.Current {
			marker = "*"
		}
		detail := branch.Commit
		if branch.Subject != "" {
			detail = strings.TrimSpace(detail + " " + branch.Subject)
		}
		if branch.Upstream != "" {
			detail = strings.TrimSpace(detail + " upstream=" + branch.Upstream)
		}
		fmt.Fprintf(out, "  %s %s", marker, branch.Name)
		if detail != "" {
			fmt.Fprintf(out, "  %s", detail)
		}
		fmt.Fprintln(out)
	}
}

type tagRequest struct {
	Format  string
	Action  string
	Name    string
	Ref     string
	Message string
	Pattern string
	Limit   int
}

type tagReport struct {
	Kind    string           `json:"kind"`
	Action  string           `json:"action"`
	Status  string           `json:"status"`
	Pattern string           `json:"pattern,omitempty"`
	Tags    []gitops.TagInfo `json:"tags,omitempty"`
	Output  string           `json:"output,omitempty"`
}

func (a *App) Tag(args []string) error {
	req, err := parseTagArgs(args)
	if err != nil {
		return err
	}
	report := tagReport{Kind: "tag", Action: req.Action, Status: "ok", Pattern: req.Pattern}
	switch req.Action {
	case "list":
		tags, err := gitops.ListTags(a.Workspace, req.Pattern, req.Limit)
		if err != nil {
			return err
		}
		report.Tags = tags
	case "create":
		output, err := gitops.CreateTag(a.Workspace, req.Name, req.Ref, req.Message)
		if err != nil {
			return err
		}
		report.Output = output
		report.Tags, err = gitops.ListTags(a.Workspace, req.Name, 1)
		if err != nil {
			return err
		}
	case "show":
		output, err := gitops.ShowTag(a.Workspace, req.Name)
		if err != nil {
			return err
		}
		report.Output = output
	case "delete":
		output, err := gitops.DeleteTag(a.Workspace, req.Name)
		if err != nil {
			return err
		}
		report.Output = output
	default:
		return fmt.Errorf("unknown tag action %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTagReport(a.Out, report)
	return nil
}

func parseTagArgs(args []string) (tagRequest, error) {
	req := tagRequest{Format: "text", Action: "list", Limit: 50}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("tag output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			i++
			if i >= len(args) {
				return req, errors.New("tag limit is required")
			}
			limit, err := strconv.Atoi(args[i])
			if err != nil || limit < 0 {
				return req, errors.New("tag limit must be a non-negative integer")
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil || limit < 0 {
				return req, errors.New("tag limit must be a non-negative integer")
			}
			req.Limit = limit
		case arg == "--message" || arg == "-m":
			i++
			if i >= len(args) {
				return req, errors.New("tag message is required")
			}
			req.Message = args[i]
		case strings.HasPrefix(arg, "--message="):
			req.Message = strings.TrimPrefix(arg, "--message=")
		default:
			positionals = append(positionals, arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown tag output format %q", req.Format)
	}
	if len(positionals) == 0 {
		return req, nil
	}
	req.Action = strings.ToLower(positionals[0])
	rest := positionals[1:]
	switch req.Action {
	case "list", "ls":
		req.Action = "list"
		if len(rest) > 0 {
			req.Pattern = rest[0]
		}
	case "create", "add":
		req.Action = "create"
		if len(rest) == 0 {
			return req, errors.New("tag create requires a name")
		}
		req.Name = rest[0]
		if len(rest) > 1 {
			req.Ref = rest[1]
		}
	case "show":
		if len(rest) == 0 {
			return req, errors.New("tag show requires a name")
		}
		req.Name = rest[0]
	case "delete", "del", "remove", "rm":
		req.Action = "delete"
		if len(rest) == 0 {
			return req, errors.New("tag delete requires a name")
		}
		req.Name = rest[0]
	default:
		return req, fmt.Errorf("unknown tag action %q", positionals[0])
	}
	return req, nil
}

func renderTagReport(out io.Writer, report tagReport) {
	fmt.Fprintln(out, "Tags")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	if report.Pattern != "" {
		fmt.Fprintf(out, "  Pattern          %s\n", report.Pattern)
	}
	if strings.TrimSpace(report.Output) != "" {
		fmt.Fprintf(out, "  Output           %s\n", strings.ReplaceAll(strings.TrimSpace(report.Output), "\n", "\n                   "))
	}
	if len(report.Tags) == 0 {
		return
	}
	fmt.Fprintf(out, "  Count            %d\n", len(report.Tags))
	fmt.Fprintln(out)
	for _, tag := range report.Tags {
		detail := tag.Commit
		if tag.Subject != "" {
			detail = strings.TrimSpace(detail + " " + tag.Subject)
		}
		fmt.Fprintf(out, "  %s", tag.Name)
		if detail != "" {
			fmt.Fprintf(out, "  %s", detail)
		}
		fmt.Fprintln(out)
	}
}

func (a *App) Changelog(args []string) error {
	req, err := parseChangelogArgs(args)
	if err != nil {
		return err
	}
	raw, err := gitops.Changelog(a.Workspace, req.Limit)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		entries, err := gitops.LogEntries(a.Workspace, req.Limit)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(changelogReport{
			Kind:    "changelog",
			Action:  "show",
			Status:  "ok",
			Limit:   req.Limit,
			Count:   len(entries),
			Entries: entries,
			Raw:     raw,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, raw)
	return nil
}

type changelogRequest struct {
	Format string
	Limit  int
}

type changelogReport struct {
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Status  string            `json:"status"`
	Limit   int               `json:"limit"`
	Count   int               `json:"count"`
	Entries []gitops.LogEntry `json:"entries"`
	Raw     string            `json:"raw"`
}

func parseChangelogArgs(args []string) (changelogRequest, error) {
	req := changelogRequest{Format: "text", Limit: 10}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("changelog output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown changelog flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "changelog")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 {
		return req, nil
	}
	if len(positionals) > 1 {
		return req, errors.New("usage: codog changelog [count] [--json|--output-format text|json]")
	}
	limit, err := strconv.Atoi(positionals[0])
	if err != nil || limit <= 0 {
		return req, errors.New("changelog count must be a positive integer")
	}
	req.Limit = limit
	return req, nil
}

type releaseNotesRequest struct {
	Format string
	From   string
	To     string
	Limit  int
}

func (a *App) ReleaseNotes(args []string) error {
	req, err := parseReleaseNotesArgs(args)
	if err != nil {
		return err
	}
	report, err := releasenotes.Generate(a.Workspace, releasenotes.Options{
		From:  req.From,
		To:    req.To,
		Limit: req.Limit,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	releasenotes.RenderMarkdown(a.Out, report)
	return nil
}

func parseReleaseNotesArgs(args []string) (releaseNotesRequest, error) {
	req := releaseNotesRequest{Format: "markdown", Limit: 50}
	var positional []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--format" || arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return releaseNotesRequest{}, errors.New("release-notes format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--from":
			index++
			if index >= len(args) {
				return releaseNotesRequest{}, errors.New("release-notes from ref is required")
			}
			req.From = args[index]
		case strings.HasPrefix(arg, "--from="):
			req.From = strings.TrimPrefix(arg, "--from=")
		case arg == "--to":
			index++
			if index >= len(args) {
				return releaseNotesRequest{}, errors.New("release-notes to ref is required")
			}
			req.To = args[index]
		case strings.HasPrefix(arg, "--to="):
			req.To = strings.TrimPrefix(arg, "--to=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return releaseNotesRequest{}, errors.New("release-notes limit is required")
			}
			limit, err := strconv.Atoi(args[index])
			if err != nil {
				return releaseNotesRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return releaseNotesRequest{}, err
			}
			req.Limit = limit
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) > 2 {
		return releaseNotesRequest{}, errors.New("usage: codog release-notes [FROM [TO]] [--from REF] [--to REF] [--limit N] [--format markdown|json]")
	}
	if len(positional) > 0 && req.From == "" {
		req.From = positional[0]
	}
	if len(positional) > 1 && req.To == "" {
		req.To = positional[1]
	}
	switch req.Format {
	case "markdown", "text":
		req.Format = "markdown"
	case "json":
	default:
		return releaseNotesRequest{}, fmt.Errorf("unknown release-notes format %q", req.Format)
	}
	return req, nil
}

func (a *App) Stash(args []string) error {
	req, err := parseStashArgs(args)
	if err != nil {
		return err
	}
	var output string
	switch req.Action {
	case "list", "show":
		output, err = gitops.StashList(a.Workspace)
	case "push", "save":
		output, err = gitops.StashPush(a.Workspace, req.Push)
	case "apply":
		output, err = gitops.StashApply(a.Workspace, req.Ref)
	case "pop":
		output, err = gitops.StashPop(a.Workspace, req.Ref)
	default:
		return fmt.Errorf("unknown stash action %q", req.Action)
	}
	if err != nil {
		return err
	}
	if output == "" {
		output = "No output."
	}
	if req.Format == "json" {
		stashes, err := gitops.ListStashes(a.Workspace)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(stashReport{
			Kind:    "stash",
			Action:  req.Action,
			Status:  "ok",
			Ref:     req.Ref,
			Output:  output,
			Count:   len(stashes),
			Stashes: stashes,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, output)
	return nil
}

type stashRequest struct {
	Format string
	Action string
	Ref    string
	Push   gitops.StashPushOptions
}

type stashReport struct {
	Kind    string             `json:"kind"`
	Action  string             `json:"action"`
	Status  string             `json:"status"`
	Ref     string             `json:"ref,omitempty"`
	Output  string             `json:"output"`
	Count   int                `json:"count"`
	Stashes []gitops.StashInfo `json:"stashes"`
}

func parseStashArgs(args []string) (stashRequest, error) {
	req := stashRequest{Format: "text", Action: "list"}
	var positionals []string
	var pushArgs []string
	collectPushArgs := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("stash output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case collectPushArgs:
			pushArgs = append(pushArgs, arg)
		default:
			positionals = append(positionals, arg)
			if len(positionals) == 1 {
				action := strings.ToLower(strings.TrimSpace(positionals[0]))
				collectPushArgs = action == "push" || action == "save"
			}
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "stash")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 {
		return req, nil
	}
	req.Action = strings.ToLower(strings.TrimSpace(positionals[0]))
	switch req.Action {
	case "list", "show":
		if len(positionals) > 1 || len(pushArgs) > 0 {
			return req, errors.New("usage: codog stash list [--json|--output-format text|json]")
		}
	case "push", "save":
		req.Push = parseStashPushArgs(pushArgs)
	case "apply", "pop":
		rest := positionals[1:]
		if len(pushArgs) > 0 {
			rest = append(rest, pushArgs...)
		}
		if len(rest) > 1 {
			return req, fmt.Errorf("usage: codog stash %s [stash-ref] [--json|--output-format text|json]", req.Action)
		}
		if len(rest) == 1 {
			req.Ref = rest[0]
		}
	default:
		return req, fmt.Errorf("unknown stash action %q", req.Action)
	}
	return req, nil
}

func parseStashPushArgs(args []string) gitops.StashPushOptions {
	options := gitops.StashPushOptions{}
	message := []string{}
	for _, arg := range args {
		switch arg {
		case "--include-untracked", "-u":
			options.IncludeUntracked = true
		default:
			message = append(message, arg)
		}
	}
	options.Message = strings.Join(message, " ")
	return options
}

func parseOptionalPositiveInt(args []string, defaultValue int, label string) (int, error) {
	if len(args) == 0 {
		return defaultValue, nil
	}
	if len(args) != 1 {
		return 0, fmt.Errorf("usage: %s", label)
	}
	value, err := strconv.Atoi(args[0])
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return value, nil
}

type gitBlameRequest struct {
	Format string
	Path   string
	Line   int
}

type gitBlameReport struct {
	Kind    string              `json:"kind"`
	Action  string              `json:"action"`
	Status  string              `json:"status"`
	Path    string              `json:"path"`
	Line    int                 `json:"line,omitempty"`
	Count   int                 `json:"count"`
	Entries []gitops.BlameEntry `json:"entries"`
	Raw     string              `json:"raw"`
}

func (a *App) GitBlame(args []string) error {
	req, err := parseGitBlameArgs(args)
	if err != nil {
		return err
	}
	raw, err := gitops.Blame(a.Workspace, req.Path, req.Line)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		entries, err := gitops.BlameEntries(a.Workspace, req.Path, req.Line)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(gitBlameReport{
			Kind:    "git_blame",
			Action:  "show",
			Status:  "ok",
			Path:    req.Path,
			Line:    req.Line,
			Count:   len(entries),
			Entries: entries,
			Raw:     raw,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, raw)
	return nil
}

func parseGitBlameArgs(args []string) (gitBlameRequest, error) {
	req := gitBlameRequest{Format: "text"}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("git blame output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown git blame flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "git blame")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 || len(positionals) > 2 {
		return req, errors.New("usage: codog git blame FILE [line] [--json|--output-format text|json]")
	}
	req.Path = positionals[0]
	if len(positionals) == 2 {
		parsed, err := strconv.Atoi(positionals[1])
		if err != nil || parsed <= 0 {
			return req, errors.New("blame line must be a positive integer")
		}
		req.Line = parsed
	}
	return req, nil
}

func (a *App) handleDiffSlash(args []string) {
	req, err := parseDiffArgs(args)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	report, err := a.buildDiffReport(req)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	if report.Empty {
		fmt.Fprintln(a.Out, "No diff.")
		return
	}
	fmt.Fprintln(a.Out, report.Diff)
}

func (a *App) handleLogSlash(args []string) {
	if err := a.GitLog(args); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleChangelogSlash(args []string) {
	if err := a.Changelog(args); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleBlameSlash(args []string) {
	if err := a.GitBlame(args); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleCommitSlash(args []string) {
	req, err := parseGitCommitArgs(args, "text")
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	report, err := a.buildCommitReport(req)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	fmt.Fprintf(a.Err, "commit %s\n", report.Commit)
	if report.Summary != "" {
		fmt.Fprintln(a.Out, report.Summary)
	}
}

type gitCommitRequest struct {
	Format  string
	Options gitops.CommitOptions
}

type commitReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	All     bool   `json:"all"`
	Commit  string `json:"commit"`
	Summary string `json:"summary"`
	Output  string `json:"output,omitempty"`
}

func (a *App) GitCommit(args []string, defaultFormat string) error {
	req, err := parseGitCommitArgs(args, defaultFormat)
	if err != nil {
		return err
	}
	report, err := a.buildCommitReport(req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCommitReport(a.Out, report)
	return nil
}

func (a *App) buildCommitReport(req gitCommitRequest) (commitReport, error) {
	result, err := gitops.Commit(a.Workspace, req.Options)
	if err != nil {
		return commitReport{}, err
	}
	return commitReport{
		Kind:    "commit",
		Action:  "create",
		Status:  "ok",
		All:     req.Options.All,
		Commit:  result.Commit,
		Summary: result.Summary,
		Output:  result.Output,
	}, nil
}

func renderCommitReport(out io.Writer, report commitReport) {
	fmt.Fprintln(out, "Commit")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Commit           %s\n", report.Commit)
	fmt.Fprintf(out, "  All              %t\n", report.All)
	if strings.TrimSpace(report.Summary) != "" {
		fmt.Fprintf(out, "  Summary          %s\n", report.Summary)
	}
	if strings.TrimSpace(report.Output) != "" {
		fmt.Fprintf(out, "  Output           %s\n", strings.ReplaceAll(strings.TrimSpace(report.Output), "\n", "\n                   "))
	}
}

func parseGitCommitArgs(args []string, defaultFormat string) (gitCommitRequest, error) {
	req := gitCommitRequest{Format: defaultFormat}
	normalized, err := normalizeTextOrJSON(req.Format, "git commit")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	message := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--all" || arg == "-a":
			req.Options.All = true
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("git commit output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--":
			message = append(message, args[i+1:]...)
			i = len(args)
		default:
			message = append(message, arg)
		}
	}
	normalized, err = normalizeTextOrJSON(req.Format, "git commit")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	req.Options.Message = strings.Join(message, " ")
	return req, nil
}

type exportRequest struct {
	SessionID string
	Output    string
	Format    string
}

type renameRequest struct {
	SessionID string
	NewID     string
	Format    string
}

type shareRequest struct {
	SessionID string
	OutputDir string
	Format    string
	JSON      bool
}

type shareReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	SessionID string `json:"session_id"`
	File      string `json:"file"`
	Format    string `json:"format"`
	Messages  int    `json:"messages"`
	Bytes     int    `json:"bytes"`
}

type copyRequest struct {
	SessionID string
	Scope     string
	Nth       int
	Format    string
	JSON      bool
}

type copyReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	SessionID string `json:"session_id"`
	Scope     string `json:"scope"`
	Nth       int    `json:"nth,omitempty"`
	Format    string `json:"format"`
	Bytes     int    `json:"bytes"`
	Clipboard string `json:"clipboard"`
}

var writeClipboard = writeSystemClipboard

func (a *App) Rename(args []string, overrides config.FlagOverrides) error {
	req, err := parseRenameArgs(args, overrides)
	if err != nil {
		return err
	}
	result, err := a.Sessions.Rename(req.SessionID, req.NewID)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintf(a.Out, "Renamed session %s to %s (%d messages).\n", result.OldID, result.NewID, result.MessageCount)
	return nil
}

func parseRenameArgs(args []string, overrides config.FlagOverrides) (renameRequest, error) {
	req := renameRequest{SessionID: "latest", Format: "text"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("rename output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("rename session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown rename flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog rename NEW_ID [--session ID] [--json]")
	}
	req.NewID = positionals[0]
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return req, fmt.Errorf("unknown rename output format %q", req.Format)
	}
}

func (a *App) Export(args []string) error {
	req, err := parseExportArgs(args, "latest")
	if err != nil {
		return err
	}
	data, sess, err := a.Sessions.Export(req.SessionID, req.Format)
	if err != nil {
		return err
	}
	if req.Output == "" {
		_, err = a.Out.Write(data)
		return err
	}
	path := a.resolveOutputPath(req.Output)
	if err := session.ValidateExportOutputPath(path); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	format, _ := session.NormalizeExportFormat(req.Format)
	report := map[string]any{
		"session_id": sess.ID,
		"file":       path,
		"format":     format,
		"messages":   len(sess.Messages),
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(a.Out, string(encoded))
	return nil
}

func (a *App) Share(args []string, overrides config.FlagOverrides) error {
	req, err := parseShareArgs(args, overrides)
	if err != nil {
		return err
	}
	data, sess, err := a.Sessions.Export(req.SessionID, req.Format)
	if err != nil {
		return err
	}
	format, _ := session.NormalizeExportFormat(req.Format)
	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = filepath.Join(a.Workspace, ".codog", "share")
	} else {
		outputDir = a.resolveOutputPath(outputDir)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(outputDir, shareFileName(sess.ID, format))
	if err := session.ValidateExportOutputPath(path); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	report := shareReport{
		Kind:      "share",
		Action:    "create",
		Status:    "ok",
		SessionID: sess.ID,
		File:      path,
		Format:    format,
		Messages:  len(sess.Messages),
		Bytes:     len(data),
	}
	if req.JSON {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	fmt.Fprintf(a.Out, "Shared session %s to %s (%d bytes).\n", report.SessionID, report.File, report.Bytes)
	return nil
}

func parseShareArgs(args []string, overrides config.FlagOverrides) (shareRequest, error) {
	req := shareRequest{SessionID: "latest", Format: session.ExportMarkdown}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.JSON = true
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("share session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--format":
			index++
			if index >= len(args) {
				return req, errors.New("share format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case arg == "--output" || arg == "--output-dir":
			index++
			if index >= len(args) {
				return req, errors.New("share output directory is required")
			}
			req.OutputDir = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.OutputDir = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "--output-dir="):
			req.OutputDir = strings.TrimPrefix(arg, "--output-dir=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown share option %q", arg)
		default:
			if req.OutputDir != "" {
				return req, fmt.Errorf("unexpected share argument %q", arg)
			}
			req.OutputDir = arg
		}
	}
	if _, err := session.NormalizeExportFormat(req.Format); err != nil {
		return req, err
	}
	return req, nil
}

func shareFileName(sessionID string, format string) string {
	ext := "md"
	switch format {
	case session.ExportJSON:
		ext = "json"
	case session.ExportJSONL:
		ext = "jsonl"
	case session.ExportHTML:
		ext = "html"
	}
	return shareSafeSessionID(sessionID) + "." + ext
}

func shareSafeSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "session"
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(sessionID) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if ok {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(builder.String(), "-.")
	if out == "" {
		return "session"
	}
	return out
}

func (a *App) Copy(ctx context.Context, args []string, overrides config.FlagOverrides) error {
	req, err := parseCopyArgs(args, overrides)
	if err != nil {
		return err
	}
	data, sess, format, err := a.copyPayload(req)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("nothing to copy")
	}
	clipboard, err := writeClipboard(ctx, data)
	if err != nil {
		return err
	}
	nth := 0
	if req.Scope == "nth" {
		nth = req.Nth
	}
	report := copyReport{
		Kind:      "copy",
		Action:    "copy",
		Status:    "ok",
		SessionID: sess.ID,
		Scope:     req.Scope,
		Nth:       nth,
		Format:    format,
		Bytes:     len(data),
		Clipboard: clipboard,
	}
	if req.JSON {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	fmt.Fprintf(a.Out, "Copied %s from session %s to clipboard (%d bytes).\n", copyScopeLabel(req), sess.ID, len(data))
	return nil
}

func (a *App) copyPayload(req copyRequest) ([]byte, *session.Session, string, error) {
	if req.Scope == "all" {
		format := req.Format
		if strings.TrimSpace(format) == "" {
			format = session.ExportMarkdown
		}
		data, sess, err := a.Sessions.Export(req.SessionID, format)
		if err != nil {
			return nil, nil, "", err
		}
		normalized, _ := session.NormalizeExportFormat(format)
		return data, sess, normalized, nil
	}
	sess, err := a.Sessions.Open(req.SessionID)
	if err != nil {
		return nil, nil, "", err
	}
	text := renderNthAssistantMessage(sess, req.Nth)
	if strings.TrimSpace(text) == "" && req.Nth == 1 {
		text = renderLastSessionMessage(sess)
	}
	if strings.TrimSpace(text) == "" && req.Nth > 1 {
		return nil, nil, "", fmt.Errorf("assistant response %d not found", req.Nth)
	}
	data := []byte(text)
	return data, sess, "text", nil
}

func parseCopyArgs(args []string, overrides config.FlagOverrides) (copyRequest, error) {
	req := copyRequest{Scope: "last", Nth: 1, SessionID: "latest"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.JSON = true
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("usage: codog copy [last|N|all] [--session ID] [--format markdown|json|jsonl|html] [--json]")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("usage: codog copy [last|N|all] [--resume ID|latest] [--format markdown|json|jsonl|html] [--json]")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--format" || arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("copy format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "last" || arg == "latest":
			req.Scope = "last"
			req.Nth = 1
		case arg == "all" || arg == "session":
			req.Scope = "all"
			req.Nth = 0
		default:
			nth, err := strconv.Atoi(arg)
			if err != nil {
				return req, fmt.Errorf("unknown copy argument %q", arg)
			}
			if nth < 1 {
				return req, errors.New("copy response index must be greater than zero")
			}
			req.Scope = "nth"
			req.Nth = nth
		}
	}
	if req.Scope != "all" && strings.TrimSpace(req.Format) != "" && req.Format != "text" {
		return req, errors.New("copy response only supports text format")
	}
	if req.Scope == "all" {
		if _, err := session.NormalizeExportFormat(req.Format); err != nil {
			return req, err
		}
	}
	return req, nil
}

func copyScopeLabel(req copyRequest) string {
	if req.Scope == "nth" {
		return fmt.Sprintf("response %d", req.Nth)
	}
	return req.Scope
}

func renderNthAssistantMessage(sess *session.Session, nth int) string {
	if nth < 1 {
		return ""
	}
	count := 0
	for index := len(sess.Messages) - 1; index >= 0; index-- {
		msg := sess.Messages[index]
		if msg.Role != "assistant" {
			continue
		}
		text := renderMessagePlainText(msg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		count++
		if count == nth {
			return text
		}
	}
	return ""
}

func renderLastSessionMessage(sess *session.Session) string {
	for index := len(sess.Messages) - 1; index >= 0; index-- {
		msg := sess.Messages[index]
		if msg.Role == "assistant" {
			return renderMessagePlainText(msg)
		}
	}
	if len(sess.Messages) == 0 {
		return ""
	}
	return renderMessagePlainText(sess.Messages[len(sess.Messages)-1])
}

func renderMessagePlainText(msg anthropic.Message) string {
	var builder strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				builder.WriteString(strings.TrimSpace(block.Text))
				builder.WriteString("\n")
			}
		case "tool_result":
			if strings.TrimSpace(block.Content) != "" {
				builder.WriteString(strings.TrimSpace(block.Content))
				builder.WriteString("\n")
			}
		}
	}
	text := strings.TrimSpace(builder.String())
	if text == "" {
		return ""
	}
	return text + "\n"
}

func writeSystemClipboard(ctx context.Context, data []byte) (string, error) {
	candidates := clipboardCommands()
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, candidate[0], candidate[1:]...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return "", err
		}
		if err := cmd.Start(); err != nil {
			return "", err
		}
		if _, err := stdin.Write(data); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			return "", err
		}
		if err := stdin.Close(); err != nil {
			_ = cmd.Wait()
			return "", err
		}
		if err := cmd.Wait(); err != nil {
			return "", err
		}
		return candidate[0], nil
	}
	return "", errors.New("no clipboard command found")
}

func clipboardCommands() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip"}}
	default:
		return [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
}

func (a *App) handleExportSlash(args []string, sess *session.Session) {
	req, err := parseExportArgs(args, sess.ID)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.Output == "" {
		req.Output = session.DefaultExportFilenameForFormat(sess, req.Format)
	}
	data, exported, err := a.Sessions.Export(req.SessionID, req.Format)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	path := a.resolveOutputPath(req.Output)
	if err := session.ValidateExportOutputPath(path); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	fmt.Fprintf(a.Err, "exported session %s to %s (%d messages)\n", exported.ID, path, len(exported.Messages))
}

func parseExportArgs(args []string, defaultSession string) (exportRequest, error) {
	req := exportRequest{SessionID: defaultSession, Format: session.ExportMarkdown}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("usage: codog export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl|html]")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--output" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("usage: codog export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl|html]")
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--format" || arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("usage: codog export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl|html]")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown export option %q", arg)
		default:
			if req.Output != "" {
				return req, fmt.Errorf("unexpected export argument %q", arg)
			}
			req.Output = arg
		}
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	if _, err := session.NormalizeExportFormat(req.Format); err != nil {
		return req, err
	}
	return req, nil
}

func (a *App) resolveOutputPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	base := a.Workspace
	if base == "" {
		base = "."
	}
	return filepath.Join(base, path)
}

func (a *App) handleSessionSlash(args []string, sess *session.Session) {
	if len(args) == 0 || args[0] == "list" {
		if err := a.ListSessions(); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
		return
	}
	switch args[0] {
	case "exists":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /session exists ID")
			return
		}
		ok, err := a.Sessions.Exists(args[1])
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
		fmt.Fprintf(a.Err, "%t\n", ok)
	case "switch":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /session switch ID")
			return
		}
		next, err := a.Sessions.Open(args[1])
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
		*sess = *next
		fmt.Fprintf(a.Err, "session switched: %s\n", sess.ID)
	case "fork":
		branch := strings.Join(args[1:], " ")
		next, err := a.Sessions.Fork(sess.ID, branch)
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
		*sess = *next
		fmt.Fprintf(a.Err, "session forked: %s\n", sess.ID)
	case "rename":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /session rename NEW_ID")
			return
		}
		result, err := a.Sessions.Rename(sess.ID, args[1])
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
		next, err := a.Sessions.Open(result.NewID)
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
		*sess = *next
		fmt.Fprintf(a.Err, "session renamed: %s -> %s\n", result.OldID, result.NewID)
	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /session delete ID")
			return
		}
		if args[1] == sess.ID && !containsFold(args[2:], "--force") {
			fmt.Fprintln(a.Err, "refusing to delete active session without --force")
			return
		}
		if err := a.Sessions.Delete(args[1]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
		fmt.Fprintf(a.Err, "session deleted: %s\n", args[1])
	default:
		fmt.Fprintf(a.Err, "unknown /session action: %s\n", args[0])
	}
}

func (a *App) SessionsCommand(args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return a.ListSessions()
	}
	switch args[0] {
	case "show":
		if len(args) < 2 {
			return errors.New("usage: codog sessions show ID")
		}
		sess, err := a.Sessions.Open(args[1])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(sess, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	case "exists":
		if len(args) < 2 {
			return errors.New("usage: codog sessions exists ID")
		}
		ok, err := a.Sessions.Exists(args[1])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]any{"id": args[1], "exists": ok}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	case "fork":
		if len(args) < 2 {
			return errors.New("usage: codog sessions fork ID [branch-name]")
		}
		forked, err := a.Sessions.Fork(args[1], strings.Join(args[2:], " "))
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(forked, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	case "rename":
		if len(args) < 3 {
			return errors.New("usage: codog sessions rename OLD_ID NEW_ID")
		}
		result, err := a.Sessions.Rename(args[1], args[2])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	case "delete":
		if len(args) < 2 {
			return errors.New("usage: codog sessions delete ID")
		}
		if err := a.Sessions.Delete(args[1]); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]any{"deleted": true, "id": args[1]}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	default:
		return fmt.Errorf("unknown sessions command %q", args[0])
	}
	return nil
}

func (a *App) BackfillSessions(args []string) error {
	format, err := parseSimpleOutputFormat("backfill-sessions", args)
	if err != nil {
		return err
	}
	report, err := a.Sessions.BackfillPromptHistory()
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBackfillSessions(a.Out, report)
	return nil
}

func renderBackfillSessions(out io.Writer, report session.BackfillReport) {
	fmt.Fprintln(out, "Backfill Sessions")
	fmt.Fprintf(out, "  Sessions scanned %d\n", report.SessionsScanned)
	fmt.Fprintf(out, "  Sessions updated %d\n", report.SessionsUpdated)
	fmt.Fprintf(out, "  Inputs added      %d\n", report.InputsAdded)
	fmt.Fprintf(out, "  Skipped existing  %d\n", report.SkippedWithInputs)
	fmt.Fprintf(out, "  Skipped disabled  %d\n", report.SkippedDisabled)
}

func (a *App) ListSessions() error {
	sessions, err := a.Sessions.List()
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		fmt.Fprintf(a.Out, "%s\t%d messages\t%s\n", sess.ID, len(sess.Messages), sess.Path)
	}
	return nil
}

func (a *App) ListSkills() error {
	return a.Skills(nil)
}

func (a *App) Skills(args []string) error {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = strings.ToLower(args[0])
		rest = args[1:]
	}
	switch action {
	case "list":
		return a.listSkills(rest)
	case "show":
		format, remaining, err := parseTemplateOutputArgs("skills show", rest)
		if err != nil {
			return err
		}
		if len(remaining) < 1 {
			return errors.New("usage: codog skills show NAME [--json]")
		}
		if len(remaining) > 1 {
			return renderCLIError(a.Out, unexpectedExtraArgsError{
				Command: "skills show",
				Args:    append([]string(nil), remaining[1:]...),
				Usage:   "codog skills show NAME [--json|--output-format text|json]",
			}, format)
		}
		skill, err := skills.Find(a.Config.ConfigHome, a.Workspace, remaining[0])
		if err != nil {
			return err
		}
		if format == "json" {
			data, _ := json.MarshalIndent(skill, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprint(a.Out, skill.Body)
		if !strings.HasSuffix(skill.Body, "\n") {
			fmt.Fprintln(a.Out)
		}
	case "invoke", "run":
		format, remaining, err := parseTemplateOutputArgs("skills invoke", rest)
		if err != nil {
			return err
		}
		if len(remaining) < 1 {
			return errors.New("usage: codog skills invoke NAME [ARGS...] [--json]")
		}
		skill, err := skills.Find(a.Config.ConfigHome, a.Workspace, remaining[0])
		if err != nil {
			return err
		}
		rendered := skills.RenderInvocation(skill, strings.Join(remaining[1:], " "))
		if format == "json" {
			data, _ := json.MarshalIndent(map[string]any{
				"kind":     "skill_invocation",
				"name":     skill.Name,
				"source":   skill.Source,
				"path":     skill.Path,
				"rendered": rendered,
			}, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprintln(a.Out, rendered)
	case "install":
		req, err := parseSkillInstallArgs(rest)
		if err != nil {
			return err
		}
		targetRoot, targetLabel, err := a.skillTargetRoot(req.Target)
		if err != nil {
			return err
		}
		report, err := skills.Install(req.Source, targetRoot, req.Name, targetLabel)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprintln(a.Out, "Skill Installed")
		fmt.Fprintf(a.Out, "  Name             %s\n", report.Name)
		fmt.Fprintf(a.Out, "  Target           %s\n", report.Target)
		fmt.Fprintf(a.Out, "  Path             %s\n", report.Path)
	case "uninstall", "remove", "delete":
		req, err := parseSkillUninstallArgs(rest)
		if err != nil {
			return err
		}
		roots := a.skillUninstallRoots(req.Target)
		report, err := skills.Uninstall(req.Name, roots)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprintln(a.Out, "Skill Uninstalled")
		fmt.Fprintf(a.Out, "  Name             %s\n", report.Name)
		fmt.Fprintf(a.Out, "  Path             %s\n", report.Path)
	default:
		return fmt.Errorf("unknown skills action %q", action)
	}
	return nil
}

type skillInstallRequest struct {
	Format string
	Target string
	Name   string
	Source string
}

type skillUninstallRequest struct {
	Format string
	Target string
	Name   string
}

func parseSkillInstallArgs(args []string) (skillInstallRequest, error) {
	req := skillInstallRequest{Format: "text", Target: "user"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("skills install output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--project":
			req.Target = "project"
		case arg == "--user":
			req.Target = "user"
		case arg == "--claude":
			req.Target = "claude"
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("skills install target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--name":
			index++
			if index >= len(args) {
				return req, errors.New("skills install name is required")
			}
			req.Name = args[index]
		case strings.HasPrefix(arg, "--name="):
			req.Name = strings.TrimPrefix(arg, "--name=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if req.Format != "text" && req.Format != "json" {
		return req, fmt.Errorf("unknown skills install output format %q", req.Format)
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog skills install [--project|--user|--claude] [--name NAME] SOURCE [--json]")
	}
	req.Source = positionals[0]
	return req, nil
}

func parseSkillUninstallArgs(args []string) (skillUninstallRequest, error) {
	req := skillUninstallRequest{Format: "text"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("skills uninstall output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--project":
			req.Target = "project"
		case arg == "--user":
			req.Target = "user"
		case arg == "--claude":
			req.Target = "claude"
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("skills uninstall target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if req.Format != "text" && req.Format != "json" {
		return req, fmt.Errorf("unknown skills uninstall output format %q", req.Format)
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog skills uninstall NAME [--project|--user|--claude] [--json]")
	}
	req.Name = positionals[0]
	return req, nil
}

func (a *App) skillTargetRoot(target string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "user":
		return filepath.Join(a.Config.ConfigHome, "skills"), "user", nil
	case "project", "workspace":
		return filepath.Join(a.Workspace, ".codog", "skills"), "workspace", nil
	case "claude":
		return filepath.Join(a.Workspace, ".claude", "skills"), "claude", nil
	default:
		return "", "", fmt.Errorf("unknown skills target %q", target)
	}
}

func (a *App) skillUninstallRoots(target string) []string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "user":
		return []string{filepath.Join(a.Config.ConfigHome, "skills")}
	case "project", "workspace":
		return []string{filepath.Join(a.Workspace, ".codog", "skills")}
	case "claude":
		return []string{filepath.Join(a.Workspace, ".claude", "skills")}
	default:
		return []string{
			filepath.Join(a.Config.ConfigHome, "skills"),
			filepath.Join(a.Workspace, ".codog", "skills"),
			filepath.Join(a.Workspace, ".claude", "skills"),
		}
	}
}

func (a *App) listSkills(args []string) error {
	remaining, format, err := stripJSONOnlyOutputFormat("skills", args)
	if err != nil {
		return err
	}
	filter, err := parseListFilterArgs("skills list", remaining, "codog skills list [FILTER] [--json|--output-format text|json]", "unknown_option")
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	all, err := skills.Load(a.Config.ConfigHome, a.Workspace)
	if err != nil {
		return err
	}
	if filter != "" {
		all = filterSkills(all, filter)
	}
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{"kind": "skills", "action": "list", "skills": all}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if len(all) == 0 {
		fmt.Fprintln(a.Out, "No skills found.")
		return nil
	}
	for _, skill := range all {
		enabled := ""
		if containsFold(a.Config.EnabledSkills, skill.Name) {
			enabled = "enabled"
		}
		fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", skill.Name, skill.Source, enabled, skill.Path)
	}
	return nil
}

func filterSkills(all []skills.Skill, filter string) []skills.Skill {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return all
	}
	out := make([]skills.Skill, 0, len(all))
	for _, skill := range all {
		if strings.Contains(strings.ToLower(skill.Name), filter) ||
			strings.Contains(strings.ToLower(skill.DisplayName), filter) ||
			strings.Contains(strings.ToLower(skill.Description), filter) {
			out = append(out, skill)
		}
	}
	return out
}

func parseListFilterArgs(command string, args []string, usage string, errorKind string) (string, error) {
	if option := firstFlagShapedArg(args); option != "" {
		return "", unknownOptionError{Kind: errorKind, Command: command, Option: option, Usage: usage}
	}
	return strings.TrimSpace(strings.Join(args, " ")), nil
}

func firstFlagShapedArg(args []string) string {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

func (a *App) Commands(args []string) error {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = strings.ToLower(args[0])
		rest = args[1:]
	}
	switch action {
	case "list":
		format, err := parseSimpleOutputFormat("commands", rest)
		if err != nil {
			return err
		}
		all, err := customcommands.Load(a.Config.ConfigHome, a.Workspace)
		if err != nil {
			return err
		}
		if format == "json" {
			summaries := make([]customcommands.Command, len(all))
			copy(summaries, all)
			for i := range summaries {
				summaries[i].Body = ""
			}
			data, _ := json.MarshalIndent(map[string]any{"kind": "commands", "commands": summaries}, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		if len(all) == 0 {
			fmt.Fprintln(a.Out, "No custom commands found.")
			return nil
		}
		for _, command := range all {
			fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", command.Name, command.Source, command.Preview, command.Path)
		}
	case "show":
		format, remaining, err := parseTemplateOutputArgs("commands show", rest)
		if err != nil {
			return err
		}
		if len(remaining) != 1 {
			return errors.New("usage: codog commands show NAME [--json]")
		}
		command, err := customcommands.Find(a.Config.ConfigHome, a.Workspace, remaining[0])
		if err != nil {
			return err
		}
		if format == "json" {
			data, _ := json.MarshalIndent(command, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprint(a.Out, command.Body)
		if !strings.HasSuffix(command.Body, "\n") {
			fmt.Fprintln(a.Out)
		}
	case "run", "render":
		format, remaining, err := parseTemplateOutputArgs("commands run", rest)
		if err != nil {
			return err
		}
		if len(remaining) < 1 {
			return errors.New("usage: codog commands run NAME [ARGS...] [--json]")
		}
		command, err := customcommands.Find(a.Config.ConfigHome, a.Workspace, remaining[0])
		if err != nil {
			return err
		}
		rendered := customcommands.Render(command, strings.Join(remaining[1:], " "))
		if format == "json" {
			data, _ := json.MarshalIndent(map[string]any{"kind": "command_run", "command": rendered}, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprint(a.Out, rendered.Rendered)
		if !strings.HasSuffix(rendered.Rendered, "\n") {
			fmt.Fprintln(a.Out)
		}
	default:
		return fmt.Errorf("unknown commands action %q", action)
	}
	return nil
}

func (a *App) Templates(args []string) error {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = strings.ToLower(args[0])
		rest = args[1:]
	}
	switch action {
	case "list":
		format, err := parseSimpleOutputFormat("templates", rest)
		if err != nil {
			return err
		}
		all, err := prompttemplates.Load(a.Config.ConfigHome, a.Workspace)
		if err != nil {
			return err
		}
		if format == "json" {
			summaries := make([]prompttemplates.Template, len(all))
			copy(summaries, all)
			for i := range summaries {
				summaries[i].Body = ""
			}
			data, _ := json.MarshalIndent(map[string]any{"kind": "templates", "templates": summaries}, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		if len(all) == 0 {
			fmt.Fprintln(a.Out, "No templates found.")
			return nil
		}
		for _, tmpl := range all {
			fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", tmpl.Name, tmpl.Source, tmpl.Preview, tmpl.Path)
		}
	case "show":
		format, remaining, err := parseTemplateOutputArgs("templates show", rest)
		if err != nil {
			return err
		}
		if len(remaining) != 1 {
			return errors.New("usage: codog templates show NAME [--json]")
		}
		tmpl, err := prompttemplates.Find(a.Config.ConfigHome, a.Workspace, remaining[0])
		if err != nil {
			return err
		}
		if format == "json" {
			data, _ := json.MarshalIndent(tmpl, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprint(a.Out, tmpl.Body)
		if !strings.HasSuffix(tmpl.Body, "\n") {
			fmt.Fprintln(a.Out)
		}
	case "apply":
		req, err := parseTemplateApplyArgs(rest)
		if err != nil {
			return err
		}
		tmpl, err := prompttemplates.Find(a.Config.ConfigHome, a.Workspace, req.Name)
		if err != nil {
			return err
		}
		rendered, err := prompttemplates.Render(tmpl, req.Vars)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(map[string]any{"kind": "template_apply", "template": rendered}, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprint(a.Out, rendered.Rendered)
		if !strings.HasSuffix(rendered.Rendered, "\n") {
			fmt.Fprintln(a.Out)
		}
	default:
		return fmt.Errorf("unknown templates action %q", action)
	}
	return nil
}

type templateApplyRequest struct {
	Name   string
	Vars   map[string]string
	Format string
}

func parseTemplateOutputArgs(command string, args []string) (string, []string, error) {
	format := "text"
	remaining := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", nil, fmt.Errorf("%s output format is required", command)
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			remaining = append(remaining, arg)
		}
	}
	switch format {
	case "text", "json":
		return format, remaining, nil
	default:
		return "", nil, fmt.Errorf("unknown %s output format %q", command, format)
	}
}

func parseTemplateApplyArgs(args []string) (templateApplyRequest, error) {
	req := templateApplyRequest{Format: "text", Vars: map[string]string{}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("templates apply output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--var" || arg == "-v":
			index++
			if index >= len(args) {
				return req, errors.New("template variable is required")
			}
			if err := addTemplateVar(req.Vars, args[index]); err != nil {
				return req, err
			}
		case strings.HasPrefix(arg, "--var="):
			if err := addTemplateVar(req.Vars, strings.TrimPrefix(arg, "--var=")); err != nil {
				return req, err
			}
		default:
			if req.Name == "" {
				req.Name = arg
				continue
			}
			if strings.Contains(arg, "=") {
				if err := addTemplateVar(req.Vars, arg); err != nil {
					return req, err
				}
				continue
			}
			return req, fmt.Errorf("unexpected template apply argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown templates apply output format %q", req.Format)
	}
	if strings.TrimSpace(req.Name) == "" {
		return req, errors.New("usage: codog templates apply NAME [--var key=value] [--json]")
	}
	return req, nil
}

func addTemplateVar(vars map[string]string, value string) error {
	key, val, ok := strings.Cut(value, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return fmt.Errorf("template variable must use key=value: %s", value)
	}
	vars[key] = val
	return nil
}

func (a *App) MCP(ctx context.Context, args []string) error {
	cleanArgs, format, err := stripJSONOnlyOutputFormat("mcp", args)
	if err != nil {
		return err
	}
	args = cleanArgs
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return errors.New("usage: codog mcp list")
		}
		if len(a.Config.MCPServers) == 0 {
			if format == "json" {
				renderMCPList(a.Out, nil)
				return nil
			}
			fmt.Fprintln(a.Out, "No MCP servers configured.")
			return nil
		}
		statuses := mcp.InspectAll(ctx, a.Config.MCPServers)
		if format == "json" {
			renderMCPList(a.Out, statuses)
			return nil
		}
		data, _ := json.MarshalIndent(statuses, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	switch args[0] {
	case "serve":
		return a.mcpServe(ctx, args[1:])
	case "self", "self-test":
		return a.mcpSelf(ctx, args[1:], format)
	case "show":
		return a.mcpShow(args[1:], format)
	case "add":
		return a.mcpAdd(args[1:])
	case "remove", "delete", "rm":
		return a.mcpRemove(args[1:])
	}
	if !mcpRemoteAction(args[0]) {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "mcp",
			Action:    args[0],
			Status:    "error",
			ErrorKind: "unsupported_action",
			Message:   fmt.Sprintf("unsupported mcp action %q", args[0]),
			Hint:      mcpUsage,
		}, format)
	}
	if len(a.Config.MCPServers) == 0 {
		fmt.Fprintln(a.Out, "No MCP servers configured.")
		return nil
	}
	if len(args) < 2 {
		return errors.New(mcpUsage)
	}
	serverName := args[1]
	server, ok := a.Config.MCPServers[serverName]
	if !ok {
		return fmt.Errorf("unknown MCP server %q", serverName)
	}
	var payload any
	switch args[0] {
	case "tools", "list-tools":
		payload = mcp.ListTools(ctx, serverName, server)
	case "auth":
		payload = mcp.InspectAuth(ctx, serverName, server)
	case "call":
		if len(args) < 4 {
			return errors.New("usage: codog mcp call SERVER TOOL JSON")
		}
		payload = mcp.CallTool(ctx, serverName, server, args[2], json.RawMessage(args[3]))
	case "resources":
		payload = mcp.ListResources(ctx, serverName, server)
	case "resource-templates", "resources-templates":
		payload = mcp.ListResourceTemplates(ctx, serverName, server)
	case "read", "read-resource":
		if len(args) < 3 {
			return errors.New("usage: codog mcp read SERVER URI")
		}
		payload = mcp.ReadResource(ctx, serverName, server, args[2])
	case "prompts":
		payload = mcp.ListPrompts(ctx, serverName, server)
	case "prompt", "get-prompt":
		if len(args) < 3 {
			return errors.New("usage: codog mcp prompt SERVER NAME [JSON]")
		}
		var arguments json.RawMessage
		if len(args) > 3 {
			arguments = json.RawMessage(args[3])
		}
		payload = mcp.GetPrompt(ctx, serverName, server, args[2], arguments)
	default:
		return fmt.Errorf("unknown mcp command %q", args[0])
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func mcpRemoteAction(action string) bool {
	switch action {
	case "tools", "list-tools", "auth", "call", "resources", "resource-templates", "resources-templates", "read", "read-resource", "prompts", "prompt", "get-prompt":
		return true
	default:
		return false
	}
}

type mcpListReport struct {
	Kind        string             `json:"kind"`
	Action      string             `json:"action"`
	Status      string             `json:"status"`
	ServerCount int                `json:"server_count"`
	Servers     []mcp.ServerStatus `json:"servers"`
}

func renderMCPList(out io.Writer, statuses []mcp.ServerStatus) {
	if statuses == nil {
		statuses = []mcp.ServerStatus{}
	}
	data, _ := json.MarshalIndent(mcpListReport{
		Kind:        "mcp",
		Action:      "list",
		Status:      "ok",
		ServerCount: len(statuses),
		Servers:     statuses,
	}, "", "  ")
	fmt.Fprintln(out, string(data))
}

const mcpUsage = "usage: codog mcp list | serve | self | show SERVER | add NAME COMMAND [ARG...] [--env KEY=VALUE] | remove SERVER | tools SERVER | auth SERVER | call SERVER TOOL JSON | resources SERVER | resource-templates SERVER | read SERVER URI | prompts SERVER | prompt SERVER NAME [JSON]"

type mcpSelfReport struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	ToolCount     int      `json:"tool_count"`
	ResourceCount int      `json:"resource_count"`
	PromptCount   int      `json:"prompt_count"`
	Tools         []string `json:"tools"`
	Resources     []string `json:"resources"`
	Prompts       []string `json:"prompts"`
}

func (a *App) mcpSelf(ctx context.Context, args []string, format string) error {
	if len(args) != 0 {
		return errors.New("usage: codog mcp self [--json|--output-format text|json]")
	}
	registry := a.mcpRegistry()
	input := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"prompts/list","params":{}}`,
		"",
	}, "\n"))
	var output bytes.Buffer
	if err := mcpserver.Serve(ctx, input, &output, registry, a.mcpServerOptions()); err != nil {
		return err
	}
	responses, err := decodeMCPResponseLines(output.String())
	if err != nil {
		return err
	}
	report := mcpSelfReport{Kind: "mcp", Action: "self", Status: "ok"}
	for _, response := range responses {
		result, _ := response["result"].(map[string]any)
		if result == nil {
			continue
		}
		if values, ok := result["tools"].([]any); ok {
			report.Tools = mcpValueStrings(values, "name")
			report.ToolCount = len(report.Tools)
		}
		if values, ok := result["resources"].([]any); ok {
			report.Resources = mcpValueStrings(values, "uri")
			report.ResourceCount = len(report.Resources)
		}
		if values, ok := result["prompts"].([]any); ok {
			report.Prompts = mcpValueStrings(values, "name")
			report.PromptCount = len(report.Prompts)
		}
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "MCP Self")
	fmt.Fprintf(a.Out, "  Status           %s\n", report.Status)
	fmt.Fprintf(a.Out, "  Tools            %d\n", report.ToolCount)
	fmt.Fprintf(a.Out, "  Resources        %d\n", report.ResourceCount)
	fmt.Fprintf(a.Out, "  Prompts          %d\n", report.PromptCount)
	if len(report.Resources) > 0 {
		fmt.Fprintf(a.Out, "  Resource URIs    %s\n", strings.Join(report.Resources, ", "))
	}
	return nil
}

func decodeMCPResponseLines(output string) ([]map[string]any, error) {
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			return nil, err
		}
		responses = append(responses, response)
	}
	return responses, nil
}

func mcpValueStrings(values []any, key string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		item, _ := value.(map[string]any)
		text, _ := item[key].(string)
		if strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

func (a *App) mcpServe(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: codog mcp serve")
	}
	return mcpserver.Serve(ctx, mcpReader(a.In, os.Stdin), mcpWriter(a.Out, os.Stdout), a.mcpRegistry(), a.mcpServerOptions())
}

func (a *App) mcpRegistry() *tools.Registry {
	registry := a.Tools
	if registry == nil {
		registry = tools.NewRegistryWithOptions(a.Workspace, tools.RegistryOptions{
			SandboxStrategy: a.Config.Future.SandboxStrategy,
			AdditionalDirs:  a.Config.AdditionalDirs,
			ConfigHome:      a.Config.ConfigHome,
			MCPServers:      a.Config.MCPServers,
		})
	}
	return registry
}

func (a *App) mcpServerOptions() mcpserver.Options {
	return mcpserver.Options{
		Version:         version,
		Workspace:       a.Workspace,
		PermissionMode:  a.Config.PermissionMode,
		PermissionRules: a.Config.PermissionRules,
	}
}

func mcpReader(value io.Reader, fallback io.Reader) io.Reader {
	if value != nil {
		return value
	}
	return fallback
}

func mcpWriter(value io.Writer, fallback io.Writer) io.Writer {
	if value != nil {
		return value
	}
	return fallback
}

func (a *App) mcpShow(args []string, format string) error {
	if len(args) != 1 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "mcp",
			Action:    "show",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "mcp show requires a server name",
			Hint:      "Usage: codog mcp show <server>.",
		}, format)
	}
	name := args[0]
	server, ok := a.Config.MCPServers[name]
	if !ok {
		return fmt.Errorf("unknown MCP server %q", name)
	}
	data, _ := json.MarshalIndent(map[string]any{
		"kind":   "mcp",
		"action": "show",
		"status": "ok",
		"name":   name,
		"server": server,
	}, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) mcpAdd(args []string) error {
	req, err := parseMCPAddArgs(args)
	if err != nil {
		return err
	}
	path := filepath.Join(a.Config.ConfigHome, "config.json")
	server := config.MCPServerConfig{Command: req.Command, Args: req.Args, Env: req.Env}
	report, err := config.SetFileValue(path, "mcp_servers."+req.Name, server)
	if err != nil {
		return err
	}
	if a.Config.MCPServers == nil {
		a.Config.MCPServers = map[string]config.MCPServerConfig{}
	}
	a.Config.MCPServers[req.Name] = server
	if err := a.refreshBuiltinToolScope(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(map[string]any{
		"kind":   "mcp",
		"action": "add",
		"status": "ok",
		"name":   req.Name,
		"path":   report.Path,
		"server": server,
	}, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) mcpRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: codog mcp remove SERVER")
	}
	name := args[0]
	if err := validateMCPServerName(name); err != nil {
		return err
	}
	path := filepath.Join(a.Config.ConfigHome, "config.json")
	report, err := config.UnsetFileValue(path, "mcp_servers."+name)
	if err != nil {
		return err
	}
	delete(a.Config.MCPServers, name)
	if err := a.refreshBuiltinToolScope(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(map[string]any{
		"kind":    "mcp",
		"action":  "remove",
		"status":  "ok",
		"name":    name,
		"path":    report.Path,
		"removed": true,
	}, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

type mcpAddRequest struct {
	Name    string
	Command string
	Args    []string
	Env     []string
}

func parseMCPAddArgs(args []string) (mcpAddRequest, error) {
	var req mcpAddRequest
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			positionals = append(positionals, args[index+1:]...)
			index = len(args)
		case arg == "--env" || arg == "-e":
			index++
			if index >= len(args) {
				return req, errors.New("mcp add env value is required")
			}
			req.Env = append(req.Env, args[index])
		case strings.HasPrefix(arg, "--env="):
			req.Env = append(req.Env, strings.TrimPrefix(arg, "--env="))
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) < 2 {
		return req, errors.New("usage: codog mcp add NAME COMMAND [ARG...] [--env KEY=VALUE]")
	}
	req.Name = positionals[0]
	if err := validateMCPServerName(req.Name); err != nil {
		return req, err
	}
	req.Command = positionals[1]
	req.Args = append([]string(nil), positionals[2:]...)
	req.Env = compactMCPEnv(req.Env)
	for _, value := range req.Env {
		if key, _, ok := strings.Cut(value, "="); !ok || strings.TrimSpace(key) == "" {
			return req, fmt.Errorf("mcp env value must use KEY=VALUE: %s", value)
		}
	}
	return req, nil
}

func compactMCPEnv(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func validateMCPServerName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("mcp server name is required")
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("invalid MCP server name %q", name)
	}
	return nil
}

func (a *App) ShowCost(overrides config.FlagOverrides) error {
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	summary := usage.Estimate(sess.Messages, a.Config.Model)
	if actual, err := a.sessionUsageValues(sess.ID); err == nil {
		if actualSummary, ok := usage.ActualSummary(actual, a.Config.Model); ok {
			summary = actualSummary
		}
	}
	data, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Usage(args []string, overrides config.FlagOverrides) error {
	format, err := parseSimpleOutputFormat("usage", args)
	if err != nil {
		return err
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	actual, _ := a.sessionUsageValues(sess.ID)
	report := usage.BuildReportWithUsage(sess.ID, a.Config.Model, sess.Messages, actual)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	usage.RenderText(a.Out, report)
	return nil
}

type insightsRequest struct {
	Format string
	Limit  int
}

func (a *App) Insights(args []string) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseInsightsArgs(args)
	if err != nil {
		return err
	}
	report, err := insights.Build(a.Sessions, insights.Options{Limit: req.Limit})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return insights.RenderJSON(a.Out, report)
	}
	insights.RenderText(a.Out, report)
	return nil
}

func parseInsightsArgs(args []string) (insightsRequest, error) {
	req := insightsRequest{Format: "text", Limit: 5}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("insights output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return req, errors.New("insights limit is required")
			}
			limit, err := parsePositiveInt(args[index], "insights limit")
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parsePositiveInt(strings.TrimPrefix(arg, "--limit="), "insights limit")
			if err != nil {
				return req, err
			}
			req.Limit = limit
		default:
			return req, fmt.Errorf("unknown insights argument %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "insights"); err != nil {
		return req, err
	}
	return req, nil
}

type thinkBackRequest struct {
	Format string
	Year   int
	Limit  int
	Output string
}

func (a *App) ThinkBack(args []string) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseThinkBackArgs(args)
	if err != nil {
		return err
	}
	report, err := thinkback.Write(a.Sessions, thinkback.Options{
		Workspace: a.Workspace,
		Year:      req.Year,
		Limit:     req.Limit,
		Output:    req.Output,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return thinkback.RenderJSON(a.Out, report)
	}
	thinkback.RenderText(a.Out, report)
	return nil
}

func parseThinkBackArgs(args []string) (thinkBackRequest, error) {
	req := thinkBackRequest{Format: "text", Limit: 8}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("think-back output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--year":
			index++
			if index >= len(args) {
				return req, errors.New("think-back year is required")
			}
			year, err := parseThinkBackYear(args[index])
			if err != nil {
				return req, err
			}
			req.Year = year
		case strings.HasPrefix(arg, "--year="):
			year, err := parseThinkBackYear(strings.TrimPrefix(arg, "--year="))
			if err != nil {
				return req, err
			}
			req.Year = year
		case arg == "--limit":
			index++
			if index >= len(args) {
				return req, errors.New("think-back limit is required")
			}
			limit, err := parsePositiveInt(args[index], "think-back limit")
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parsePositiveInt(strings.TrimPrefix(arg, "--limit="), "think-back limit")
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case arg == "--output":
			index++
			if index >= len(args) {
				return req, errors.New("think-back output path is required")
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		default:
			return req, fmt.Errorf("unknown think-back argument %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "think-back"); err != nil {
		return req, err
	}
	return req, nil
}

func parseThinkBackYear(value string) (int, error) {
	year, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || year < 2000 || year > 9999 {
		return 0, errors.New("think-back year must be a four digit year")
	}
	return year, nil
}

func (a *App) sessionUsageValues(sessionID string) ([]anthropic.Usage, error) {
	entries, err := a.Sessions.Usage(sessionID)
	if err != nil {
		return nil, err
	}
	values := make([]anthropic.Usage, 0, len(entries))
	for _, entry := range entries {
		values = append(values, entry.Usage)
	}
	return values, nil
}

type compactRequest struct {
	Format  string
	Session string
	Keep    int
}

func (a *App) Compact(args []string, overrides config.FlagOverrides) error {
	req, err := parseCompactArgs(args, overrides, a.Config.AutoCompactMessages)
	if err != nil {
		return err
	}
	sess, err := a.Sessions.Open(req.Session)
	if err != nil {
		return err
	}
	compactPayload := runloop.CompactHookPayload("manual", sess.ID, len(sess.Messages), req.Keep)
	if err := a.lifecycleHookRunner().PreCompact(context.Background(), compactPayload); err != nil {
		return err
	}
	compacted := runloop.CompactMessages(sess.Messages, req.Keep)
	var result session.ReplaceResult
	if len(compacted) == len(sess.Messages) {
		result = session.ReplaceResult{
			SessionID:         sess.ID,
			Path:              sess.Path,
			OriginalMessages:  len(sess.Messages),
			RemainingMessages: len(sess.Messages),
			RemovedMessages:   0,
		}
	} else {
		result, err = a.Sessions.ReplaceMessages(sess, compacted)
		if err != nil {
			return err
		}
	}
	if err := a.lifecycleHookRunner().PostCompact(context.Background(), compactPayload); err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Session Compacted")
	fmt.Fprintf(a.Out, "  Session          %s\n", result.SessionID)
	fmt.Fprintf(a.Out, "  Original         %d\n", result.OriginalMessages)
	fmt.Fprintf(a.Out, "  Remaining        %d\n", result.RemainingMessages)
	fmt.Fprintf(a.Out, "  Removed          %d\n", result.RemovedMessages)
	return nil
}

func parseCompactArgs(args []string, overrides config.FlagOverrides, defaultKeep int) (compactRequest, error) {
	req := compactRequest{Format: "text", Keep: defaultKeep}
	req.Session = overrides.Resume
	if req.Session == "" {
		req.Session = overrides.SessionID
	}
	if req.Session == "" {
		req.Session = "latest"
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			if i+1 >= len(args) {
				return req, errors.New("compact output format is required")
			}
			i++
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--keep":
			if i+1 >= len(args) {
				return req, errors.New("compact keep count is required")
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 {
				return req, errors.New("compact keep count must be a positive integer")
			}
			req.Keep = value
		case strings.HasPrefix(arg, "--keep="):
			value, err := strconv.Atoi(strings.TrimPrefix(arg, "--keep="))
			if err != nil || value <= 0 {
				return req, errors.New("compact keep count must be a positive integer")
			}
			req.Keep = value
		case arg == "--session":
			if i+1 >= len(args) {
				return req, errors.New("compact session id is required")
			}
			i++
			req.Session = args[i]
		case strings.HasPrefix(arg, "--session="):
			req.Session = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			if i+1 >= len(args) {
				return req, errors.New("compact resume id is required")
			}
			i++
			req.Session = args[i]
		case strings.HasPrefix(arg, "--resume="):
			req.Session = strings.TrimPrefix(arg, "--resume=")
		default:
			return req, fmt.Errorf("unknown compact argument %q", arg)
		}
	}
	if req.Format != "text" && req.Format != "json" {
		return req, fmt.Errorf("unknown compact output format %q", req.Format)
	}
	if req.Keep <= 0 {
		req.Keep = 40
	}
	return req, nil
}

type rateLimitOptionsReport struct {
	Kind              string `json:"kind"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	MaxRetries        int    `json:"max_retries"`
	InitialBackoffMS  int    `json:"initial_backoff_ms"`
	MaxBackoffMS      int    `json:"max_backoff_ms"`
	RetryableStatuses []int  `json:"retryable_statuses"`
}

type resetLimitsRequest struct {
	Format string
	Target string
	Path   string
}

type resetLimitsReport struct {
	Kind     string                 `json:"kind"`
	Action   string                 `json:"action"`
	Status   string                 `json:"status"`
	Path     string                 `json:"path"`
	Target   string                 `json:"target,omitempty"`
	Previous rateLimitOptionsReport `json:"previous"`
	Current  rateLimitOptionsReport `json:"current"`
}

func (a *App) RateLimitOptions(args []string) error {
	format, err := parseSimpleOutputFormat("rate-limit-options", args)
	if err != nil {
		return err
	}
	report := buildRateLimitOptionsReport(a.Config.RateLimit)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderRateLimitOptionsReport(a.Out, report)
	return nil
}

func (a *App) ResetLimits(args []string) error {
	req, err := parseResetLimitsArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if _, err := config.UnsetFileValue(path, "rate_limit"); err != nil {
		return err
	}
	previous := buildRateLimitOptionsReport(a.Config.RateLimit)
	a.Config.RateLimit = config.RateLimitConfig{}
	report := resetLimitsReport{
		Kind:     "reset_limits",
		Action:   "reset",
		Status:   "ok",
		Path:     path,
		Target:   req.Target,
		Previous: previous,
		Current:  buildRateLimitOptionsReport(a.Config.RateLimit),
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderResetLimitsReport(a.Out, report)
	return nil
}

func parseResetLimitsArgs(args []string) (resetLimitsRequest, error) {
	req := resetLimitsRequest{Format: "text", Target: "user"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("reset-limits output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("reset-limits target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("reset-limits config path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			return req, fmt.Errorf("unknown reset-limits argument %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "reset-limits"); err != nil {
		return req, err
	}
	return req, nil
}

func renderResetLimitsReport(out io.Writer, report resetLimitsReport) {
	fmt.Fprintln(out, "Reset Limits")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	fmt.Fprintf(out, "  Previous retries %d\n", report.Previous.MaxRetries)
	fmt.Fprintf(out, "  Current retries  %d\n", report.Current.MaxRetries)
}

func buildRateLimitOptionsReport(cfg config.RateLimitConfig) rateLimitOptionsReport {
	snapshot := anthropicRateLimitOptions(cfg).Report()
	return rateLimitOptionsReport{
		Kind:              "rate_limit_options",
		Action:            "show",
		Status:            "ok",
		MaxRetries:        snapshot.MaxRetries,
		InitialBackoffMS:  snapshot.InitialBackoffMS,
		MaxBackoffMS:      snapshot.MaxBackoffMS,
		RetryableStatuses: append([]int(nil), snapshot.RetryableStatuses...),
	}
}

func renderRateLimitOptionsReport(out io.Writer, report rateLimitOptionsReport) {
	fmt.Fprintln(out, "Rate Limit Options")
	fmt.Fprintf(out, "  Max retries      %d\n", report.MaxRetries)
	fmt.Fprintf(out, "  Initial backoff  %dms\n", report.InitialBackoffMS)
	fmt.Fprintf(out, "  Max backoff      %dms\n", report.MaxBackoffMS)
	fmt.Fprintf(out, "  Retry statuses   %s\n", joinInts(report.RetryableStatuses))
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func (a *App) openSession(overrides config.FlagOverrides) (*session.Session, error) {
	id := overrides.SessionID
	if overrides.Resume != "" {
		id = overrides.Resume
		if id == "true" {
			id = "latest"
		}
	}
	sess, err := a.Sessions.Open(id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(id) != "" {
		if err := a.restoreTodosFromSession(sess); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func (a *App) restoreTodosFromSession(sess *session.Session) error {
	items, ok := lastTodoWriteItems(sess.Messages)
	if !ok {
		return nil
	}
	if todoItemsAllCompletedForRestore(items) {
		items = nil
	}
	_, err := todos.Replace(a.Workspace, items)
	return err
}

func lastTodoWriteItems(messages []anthropic.Message) ([]todos.Item, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !strings.EqualFold(msg.Role, "assistant") {
			continue
		}
		for j := len(msg.Content) - 1; j >= 0; j-- {
			block := msg.Content[j]
			if block.Type != "tool_use" || tools.CanonicalToolName(block.Name) != "todo_write" {
				continue
			}
			var payload struct {
				Todos []todos.Item `json:"todos"`
			}
			if err := json.Unmarshal(block.Input, &payload); err != nil {
				return nil, false
			}
			return payload.Todos, true
		}
	}
	return nil, false
}

func todoItemsAllCompletedForRestore(items []todos.Item) bool {
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

func sessionStartSource(overrides config.FlagOverrides) string {
	if strings.TrimSpace(overrides.Resume) != "" || strings.TrimSpace(overrides.SessionID) != "" {
		return "resume"
	}
	return "startup"
}

func (a *App) lifecycleHookRunner() hooks.Runner {
	cfg := a.effectiveConfig()
	return hooks.Runner{
		Config:       cfg.Hooks,
		Workspace:    a.Workspace,
		ConfigHome:   cfg.ConfigHome,
		PromptRunner: a.hookPromptRunner(cfg),
	}
}

func (a *App) runNotificationHook(ctx context.Context, notificationType string, title string, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if err := a.lifecycleHookRunner().Notification(ctx, notificationType, title, message); err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "notification hook error: %v\n", err)
	}
}

func (a *App) runSubagentStartHook(ctx context.Context, agentID string, agentType string) {
	if strings.TrimSpace(agentID) == "" {
		return
	}
	if err := a.lifecycleHookRunner().SubagentStart(ctx, agentID, firstNonEmpty(agentType, "agent")); err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "subagent start hook error: %v\n", err)
	}
}

func (a *App) runWorktreeCreateHook(ctx context.Context, allocation worktree.Allocation, source string) error {
	input, err := worktreeHookInput(allocation, source, "")
	if err != nil {
		return err
	}
	return a.lifecycleHookRunner().WorktreeCreate(ctx, allocation.ID, allocation.Path, allocation.Ref, input)
}

func (a *App) runWorktreeRemoveHook(ctx context.Context, allocation worktree.Allocation, reason string) error {
	input, err := worktreeHookInput(allocation, "", reason)
	if err != nil {
		return err
	}
	return a.lifecycleHookRunner().WorktreeRemove(ctx, allocation.ID, allocation.Path, allocation.Ref, reason, input)
}

func (a *App) removeAllocatedWorktree(ctx context.Context, allocation worktree.Allocation, reason string) error {
	removeErr := worktree.Remove(a.Workspace, allocation.ID)
	hookErr := a.runWorktreeRemoveHook(ctx, allocation, reason)
	if removeErr != nil {
		return removeErr
	}
	return hookErr
}

func worktreeHookInput(allocation worktree.Allocation, source string, reason string) (string, error) {
	payload := map[string]string{
		"worktree_id":   allocation.ID,
		"worktree_path": allocation.Path,
		"ref":           allocation.Ref,
	}
	if strings.TrimSpace(source) != "" {
		payload["source"] = strings.TrimSpace(source)
	}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = strings.TrimSpace(reason)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a *App) runTaskCreatedHook(ctx context.Context, task background.Task) {
	input, err := taskHookInput(task)
	if err != nil {
		if a.Err != nil {
			fmt.Fprintf(a.Err, "task created hook payload error: %v\n", err)
		}
		return
	}
	if err := a.lifecycleHookRunner().TaskCreated(ctx, task.ID, taskKindForHook(task), task.Status, input); err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "task created hook error: %v\n", err)
	}
}

func (a *App) runTaskCompletedHook(ctx context.Context, task background.Task, reason string) {
	input, err := taskHookInput(task)
	if err != nil {
		if a.Err != nil {
			fmt.Fprintf(a.Err, "task completed hook payload error: %v\n", err)
		}
		return
	}
	if err := a.lifecycleHookRunner().TaskCompleted(ctx, task.ID, taskKindForHook(task), task.Status, reason, input); err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "task completed hook error: %v\n", err)
	}
}

func taskHookInput(task background.Task) (string, error) {
	data, err := json.Marshal(task)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func taskKindForHook(task background.Task) string {
	return firstNonEmpty(task.Kind, "background")
}

func (a *App) runSubagentStopHook(ctx context.Context, agentID string, agentType string, transcriptPath string, lastAssistant string, stopHookActive bool) {
	if strings.TrimSpace(agentID) == "" {
		return
	}
	if err := a.lifecycleHookRunner().SubagentStop(ctx, agentID, firstNonEmpty(agentType, "agent"), transcriptPath, lastAssistant, stopHookActive); err != nil && a.Err != nil {
		fmt.Fprintf(a.Err, "subagent stop hook error: %v\n", err)
	}
}

func subagentTypeForTask(task background.Task) string {
	if strings.TrimSpace(task.AgentType) != "" {
		return strings.TrimSpace(task.AgentType)
	}
	if strings.TrimSpace(task.Kind) != "" {
		return strings.TrimSpace(task.Kind)
	}
	return "agent"
}

func lastBackgroundLogLine(store background.Store, task background.Task) string {
	logs, err := store.Logs(task.ID, 64*1024)
	if err != nil {
		return ""
	}
	lines := strings.Split(logs, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func (a *App) runSessionStartHook(ctx context.Context, sess *session.Session, source string) error {
	if sess == nil {
		return nil
	}
	data, err := json.Marshal(map[string]string{
		"source":     source,
		"session_id": sess.ID,
	})
	if err != nil {
		return err
	}
	runner := a.lifecycleHookRunner()
	runner.SessionID = sess.ID
	return runner.SessionStart(ctx, string(data))
}

func (a *App) runSessionEndHook(ctx context.Context, sess *session.Session, reason string) error {
	if sess == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "exit"
	}
	data, err := json.Marshal(map[string]string{
		"reason":     reason,
		"session_id": sess.ID,
	})
	if err != nil {
		return err
	}
	runner := a.lifecycleHookRunner()
	runner.SessionID = sess.ID
	return runner.SessionEnd(ctx, string(data), reason)
}

func (a *App) runInstructionsLoadedHooks(ctx context.Context, sessionID string, loadReason string) error {
	runner := a.lifecycleHookRunner()
	runner.SessionID = strings.TrimSpace(sessionID)
	if len(runner.Config.InstructionsLoaded) == 0 && len(runner.Config.InstructionsLoadedCommands) == 0 {
		return nil
	}
	files, err := memory.Discover(a.Workspace)
	if err != nil {
		return err
	}
	loadReason = firstNonEmpty(loadReason, "session_start")
	for _, file := range files {
		if err := runner.InstructionsLoaded(ctx, file.Path, instructionsMemoryType(file), loadReason, nil, "", ""); err != nil {
			return err
		}
	}
	return nil
}

func instructionsMemoryType(file memory.File) string {
	return "Project"
}

func (a *App) runSetupHook(ctx context.Context, source string, status string) error {
	return runSetupHookPayload(ctx, a.lifecycleHookRunner(), a.Workspace, source, status)
}

func runSetupHookPayload(ctx context.Context, runner hooks.Runner, workspace string, source string, status string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "ok"
	}
	if runner.Workspace == "" {
		runner.Workspace = workspace
	}
	data, err := json.Marshal(map[string]string{
		"source":    source,
		"status":    status,
		"workspace": workspace,
	})
	if err != nil {
		return err
	}
	return runner.Setup(ctx, string(data))
}

func (a *App) writeWorkerState(mode string, status string, sess *session.Session, lastError string) {
	if sess == nil {
		return
	}
	cfg := a.effectiveConfig()
	state := workerstate.New(workerstate.Options{
		Version:        version,
		Mode:           mode,
		Status:         status,
		Workspace:      a.Workspace,
		SessionID:      sess.ID,
		SessionPath:    sess.Path,
		Model:          cfg.Model,
		PermissionMode: cfg.PermissionMode,
		LastError:      lastError,
	})
	if err := workerstate.Save(a.Workspace, state); err != nil && a.Err != nil {
		fmt.Fprintln(a.Err, "state:", err)
	}
}

func (a *App) sessionIDFromOverrides(overrides config.FlagOverrides) (string, error) {
	id := overrides.SessionID
	if overrides.Resume != "" {
		id = overrides.Resume
		if id == "true" {
			id = "latest"
		}
	}
	if id == "latest" {
		return a.Sessions.LatestID()
	}
	return id, nil
}

func (a *App) prompter(sessionID string) *tools.Prompter {
	return a.prompterWithAllowedTools(sessionID, nil)
}

func (a *App) prompterWithSkill(sessionID string, activeSkill *skills.Skill) *tools.Prompter {
	if activeSkill == nil {
		return a.prompterWithAllowedTools(sessionID, nil)
	}
	return a.prompterWithAllowedTools(sessionID, activeSkill.AllowedTools)
}

func (a *App) prompterWithAllowedTools(sessionID string, allowedTools []string) *tools.Prompter {
	cfg := a.effectiveConfig()
	allowRules := append([]string(nil), cfg.PermissionRules.Allow...)
	if len(allowedTools) > 0 && !a.planModeActive() {
		allowRules = addRuleValues(allowRules, skillAllowedToolRules(allowedTools))
	}
	return &tools.Prompter{
		Mode:        tools.Permission(cfg.PermissionMode),
		AllowRules:  allowRules,
		DenyRules:   append([]string(nil), cfg.PermissionRules.Deny...),
		AskRules:    append([]string(nil), cfg.PermissionRules.Ask...),
		DeniedTools: append([]string(nil), cfg.PermissionRules.DeniedTools...),
		Workspace:   a.Workspace,
		In:          a.In,
		Err:         a.Err,
		OnRequest:   a.permissionRequestHook(sessionID),
		OnDecision:  a.permissionDecisionHandler(sessionID),
	}
}

func (a *App) permissionRequestHook(sessionID string) func(tools.PermissionDecision) {
	return func(decision tools.PermissionDecision) {
		if err := a.lifecycleHookRunner().PermissionRequest(context.Background(), decision.ToolName, []byte(decision.Input)); err != nil && a.Err != nil {
			fmt.Fprintf(a.Err, "permission request hook error: %v\n", err)
		}
	}
}

func (a *App) permissionDecisionHandler(sessionID string) func(tools.PermissionDecision) {
	audit := a.auditPermissionDecision(sessionID)
	return func(decision tools.PermissionDecision) {
		if audit != nil {
			audit(decision)
		}
		if decision.Allowed {
			return
		}
		if err := a.lifecycleHookRunner().PermissionDenied(context.Background(), decision.ToolName, []byte(decision.Input), decision.Reason); err != nil && a.Err != nil {
			fmt.Fprintf(a.Err, "permission denied hook error: %v\n", err)
		}
	}
}

func (a *App) validateGlobalToolRules(overrides config.FlagOverrides, format string) error {
	if err := a.validateGlobalToolRuleList("--allowed-tools", overrides.AllowedTools, format); err != nil {
		return err
	}
	if err := a.validateGlobalToolRuleList("--disallowed-tools", overrides.DisallowedTools, format); err != nil {
		return err
	}
	return nil
}

func (a *App) validateGlobalToolRuleList(argument string, rules []string, format string) error {
	if err := a.validateToolRuleValues(argument, rules); err != nil {
		return renderCLIError(a.Out, err, format)
	}
	return nil
}

func (a *App) validateToolRuleValues(argument string, rules []string) error {
	for _, rule := range rules {
		toolName := toolRuleName(rule)
		if a.validToolRuleName(toolName) {
			continue
		}
		return toolNameError{
			Argument:  argument,
			ToolName:  toolName,
			Available: a.availableToolNames(),
			Aliases:   tools.ClaudeToolAliases(),
		}
	}
	return nil
}

func (a *App) validToolRuleName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "*" {
		return true
	}
	if isMCPToolPattern(name) {
		return true
	}
	if strings.Contains(name, "*") {
		return a.toolWildcardMatchesRegisteredName(name)
	}
	registry := a.activeToolRegistry()
	if registry == nil {
		return tools.CanonicalToolName(name) != name
	}
	if _, ok := registry.Info(name); ok {
		return true
	}
	if canonical := tools.CanonicalToolName(name); canonical != name {
		_, ok := registry.Info(canonical)
		return ok
	}
	return false
}

func (a *App) toolWildcardMatchesRegisteredName(pattern string) bool {
	registry := a.activeToolRegistry()
	if registry == nil {
		return false
	}
	for _, info := range registry.Infos() {
		if permissionNamePatternMatches(pattern, info.Name) {
			return true
		}
	}
	return false
}

func (a *App) availableToolNames() []string {
	registry := a.activeToolRegistry()
	if registry == nil {
		return nil
	}
	infos := registry.Infos()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	sort.Strings(names)
	return names
}

func (a *App) activeToolRegistry() *tools.Registry {
	if a != nil && a.Tools != nil {
		return a.Tools
	}
	workspace := ""
	if a != nil {
		workspace = a.Workspace
	}
	return tools.NewRegistry(workspace)
}

func toolRuleName(rule string) string {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return ""
	}
	if open := strings.Index(rule, "("); open > 0 && strings.HasSuffix(rule, ")") {
		return strings.TrimSpace(rule[:open])
	}
	if toolName, _, ok := strings.Cut(rule, ":"); ok {
		return strings.TrimSpace(toolName)
	}
	return rule
}

func isMCPToolPattern(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(name, "mcp__") {
		return false
	}
	parts := strings.Split(name, "__")
	return len(parts) >= 3 && strings.TrimSpace(parts[1]) != "" && strings.TrimSpace(parts[2]) != ""
}

func permissionNamePatternMatches(pattern string, value string) bool {
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
		return strings.HasSuffix(value, last)
	}
	return true
}

func skillAllowedToolRules(values []string) []string {
	rules := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if value == "*" {
			rules = append(rules, value)
			continue
		}
		toolName := value
		inputNeedle := ""
		if open := strings.Index(value, "("); open > 0 && strings.HasSuffix(value, ")") {
			toolName = strings.TrimSpace(value[:open])
			inputNeedle = strings.TrimSpace(strings.TrimSuffix(value[open+1:], ")"))
			inputNeedle = strings.TrimSpace(strings.TrimSuffix(inputNeedle, "*"))
			inputNeedle = strings.TrimSpace(strings.TrimSuffix(inputNeedle, ":"))
		}
		canonical := strings.TrimSpace(tools.CanonicalToolName(toolName))
		if canonical == "" {
			continue
		}
		if inputNeedle != "" {
			rules = append(rules, canonical+":"+inputNeedle)
			continue
		}
		rules = append(rules, canonical)
	}
	return addRuleValues(nil, rules)
}

func firstWriter(values ...io.Writer) io.Writer {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (a *App) effectiveConfig() config.Config {
	cfg := a.Config
	if a.planModeActive() {
		cfg.PlanMode = true
		cfg.PermissionMode = string(tools.PermissionReadOnly)
		cfg.PermissionRules.Allow = nil
	}
	return cfg
}

func (a *App) planModeActive() bool {
	state, err := planmode.Load(a.Workspace)
	return err == nil && state.Active
}

func (a *App) auditToolUse(sessionID string) func(runloop.ToolCall) {
	store := audit.NewStore(a.Config.ConfigHome)
	return func(call runloop.ToolCall) {
		if err := store.Append(audit.Event{
			Type:      "tool_use",
			SessionID: sessionID,
			Workspace: a.Workspace,
			ToolName:  call.Name,
			Input:     audit.Clip(call.Input, 16*1024),
			Output:    audit.Clip(call.Output, 16*1024),
			IsError:   call.IsError,
		}); err != nil && a.Err != nil {
			fmt.Fprintln(a.Err, "audit:", err)
		}
	}
}

func (a *App) auditPermissionDecision(sessionID string) func(tools.PermissionDecision) {
	store := audit.NewStore(a.Config.ConfigHome)
	return func(decision tools.PermissionDecision) {
		if err := store.Append(audit.Event{
			Type:               "permission",
			SessionID:          sessionID,
			Workspace:          a.Workspace,
			ToolName:           decision.ToolName,
			Input:              audit.Clip(decision.Input, 16*1024),
			PermissionMode:     string(decision.Mode),
			RequiredPermission: string(decision.Required),
			Allowed:            audit.Bool(decision.Allowed),
			Reason:             decision.Reason,
		}); err != nil && a.Err != nil {
			fmt.Fprintln(a.Err, "audit:", err)
		}
	}
}

func (a *App) systemPrompt() string {
	return a.systemPromptForInput("")
}

func (a *App) systemPromptForInput(input string) string {
	base := "You are Codog, a Go-native coding agent CLI. Be concise, inspect before editing, and use tools when they materially help."
	if strings.TrimSpace(a.Config.SystemPrompt) != "" {
		base = strings.TrimSpace(a.Config.SystemPrompt)
	}
	var builder strings.Builder
	builder.WriteString(base)
	if strings.TrimSpace(a.Config.AppendSystemPrompt) != "" {
		builder.WriteString("\n\n")
		builder.WriteString(strings.TrimSpace(a.Config.AppendSystemPrompt))
	}
	if effort := strings.TrimSpace(a.Config.ReasoningEffort); effort != "" {
		builder.WriteString("\n\n<codog_reasoning_effort>")
		builder.WriteString(effectiveEffort(effort))
		builder.WriteString("</codog_reasoning_effort>")
	}
	if fastModeEnabled(a.Config.FastMode) {
		builder.WriteString("\n\n<codog_fast_mode>enabled</codog_fast_mode>")
	}
	includedSkills := map[string]bool{}
	for _, name := range a.Config.EnabledSkills {
		skill, err := skills.Find(a.Config.ConfigHome, a.Workspace, name)
		if err != nil {
			continue
		}
		if skill.DisableModelInvocation {
			continue
		}
		includedSkills[strings.ToLower(skill.Name)] = true
		builder.WriteString("\n\n")
		builder.WriteString(skills.RenderPromptBlock(skill))
	}
	for _, skill := range a.pathMatchedSkills(input) {
		key := strings.ToLower(skill.Name)
		if includedSkills[key] {
			continue
		}
		includedSkills[key] = true
		builder.WriteString("\n\n")
		builder.WriteString(skills.RenderPromptBlock(skill))
	}
	if rendered := outputstyle.RenderPrompt(a.Config.ConfigHome, a.Workspace); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	if state, err := planmode.Load(a.Workspace); err == nil {
		if rendered := planmode.RenderPrompt(state); rendered != "" {
			builder.WriteString("\n\n")
			builder.WriteString(rendered)
		}
	}
	if files, err := memory.Discover(a.Workspace); err == nil {
		if rendered := memory.Render(files); rendered != "" {
			builder.WriteString("\n\n")
			builder.WriteString(rendered)
		}
	}
	if rendered := focus.RenderPrompt(a.Workspace); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	if rendered := pathscope.RenderPrompt(a.Workspace, a.Config.AdditionalDirs); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
	}
	return builder.String()
}

func (a *App) pathMatchedSkills(input string) []skills.Skill {
	paths := a.skillContextPaths(input)
	if len(paths) == 0 {
		return nil
	}
	all, err := skills.Load(a.Config.ConfigHome, a.Workspace)
	if err != nil {
		return nil
	}
	out := []skills.Skill{}
	for _, skill := range all {
		if skill.DisableModelInvocation || len(skill.Paths) == 0 {
			continue
		}
		if skills.MatchesAnyPath(skill, paths) {
			out = append(out, skill)
		}
	}
	return out
}

func (a *App) skillContextPaths(input string) []string {
	paths := []string{}
	paths = append(paths, promptrefs.References(input)...)
	if state, err := focus.Load(a.Workspace); err == nil {
		for _, entry := range state.Entries {
			paths = append(paths, entry.Path)
		}
	}
	return addRuleValues(nil, paths)
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func (a *App) slashCompletionCandidates(activeSessionID string) []string {
	recent := []string{}
	if a.Sessions != nil {
		if sessions, err := a.Sessions.List(); err == nil {
			for _, sess := range sessions {
				recent = append(recent, sess.ID)
				if len(recent) >= 10 {
					break
				}
			}
		}
	}
	extra := a.customSlashCompletionCandidates()
	return slash.AllCandidates(slash.CandidateOptions{
		Model:            a.Config.Model,
		ActiveSessionID:  activeSessionID,
		RecentSessionIDs: recent,
		Extra:            extra,
	})
}

func (a *App) customSlashCompletionCandidates() []string {
	candidates := []string{}
	if commands, err := customcommands.Load(a.Config.ConfigHome, a.Workspace); err == nil {
		for _, command := range commands {
			candidates = append(candidates, "/"+strings.ReplaceAll(command.Name, ":", "/")+" ")
		}
	}
	if loadedSkills, err := skills.Load(a.Config.ConfigHome, a.Workspace); err == nil {
		for _, skill := range loadedSkills {
			if !skill.UserInvocable {
				continue
			}
			candidates = append(candidates, "/"+strings.ReplaceAll(skill.Name, ":", "/")+" ")
		}
	}
	return candidates
}

type stringListFlag []string

func (v *stringListFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*v = append(*v, part)
		}
	}
	return nil
}

func (v *stringListFlag) String() string {
	if v == nil {
		return ""
	}
	return strings.Join(*v, ",")
}

func parseFlags(args []string, base config.FlagOverrides) (config.FlagOverrides, string, []string, error) {
	if missing, ok := missingToolFlagArgument(args); ok {
		return base, "", nil, missing
	}
	flags := flag.NewFlagSet("codog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	printMode := false
	jsonOutput := false
	outputFormat := ""
	allowedTools := stringListFlag(base.AllowedTools)
	disallowedTools := stringListFlag(base.DisallowedTools)
	flags.StringVar(&base.ConfigPath, "config", base.ConfigPath, "config path")
	flags.StringVar(&base.Model, "model", base.Model, "model name")
	flags.StringVar(&base.BaseURL, "base-url", base.BaseURL, "Anthropic-compatible base URL")
	flags.StringVar(&base.SystemPrompt, "system-prompt", base.SystemPrompt, "override the base system prompt")
	flags.StringVar(&base.AppendPrompt, "append-system-prompt", base.AppendPrompt, "append text to the system prompt")
	flags.StringVar(&base.SessionID, "session", base.SessionID, "session id")
	flags.StringVar(&base.Resume, "resume", base.Resume, "resume session id or latest")
	flags.BoolVar(&printMode, "p", false, "run a one-shot prompt")
	flags.BoolVar(&printMode, "print", false, "run a one-shot prompt")
	flags.BoolVar(&jsonOutput, "json", false, "alias for --output-format json for local commands")
	flags.StringVar(&outputFormat, "output-format", "", "text or json output for local commands")
	flags.StringVar(&outputFormat, "o", "", "text or json output for local commands")
	flags.StringVar(&base.PermissionMode, "permission-mode", base.PermissionMode, "read-only, workspace-write, danger-full-access, prompt, allow")
	flags.BoolVar(&base.SkipPermissions, "dangerously-skip-permissions", base.SkipPermissions, "alias for --permission-mode allow")
	flags.BoolVar(&base.SkipPermissions, "skip-permissions", base.SkipPermissions, "alias for --permission-mode allow")
	flags.Var(&allowedTools, "allowed-tools", "allow a tool or tool rule; repeat or comma-separate")
	flags.Var(&allowedTools, "allowedTools", "allow a tool or tool rule; repeat or comma-separate")
	flags.Var(&disallowedTools, "disallowed-tools", "deny a tool; repeat or comma-separate")
	flags.Var(&disallowedTools, "disallowedTools", "deny a tool; repeat or comma-separate")
	flags.IntVar(&base.MaxTurns, "max-turns", base.MaxTurns, "max model/tool loop iterations")
	flags.IntVar(&base.MaxTokens, "max-tokens", base.MaxTokens, "maximum output tokens")
	if err := flags.Parse(args); err != nil {
		return base, "", nil, err
	}
	base.AllowedTools = []string(allowedTools)
	base.DisallowedTools = []string(disallowedTools)
	rest := flags.Args()
	if printMode {
		if len(rest) > 0 && rest[0] == "prompt" {
			rest = rest[1:]
		}
		outputFormat = resolveGlobalOutputFormat(outputFormat, jsonOutput)
		normalized, err := normalizeOutputFormat("prompt", outputFormat, []string{"text", "json", "stream-json"})
		if err != nil {
			return base, "", nil, err
		}
		outputFormat = normalized
		rest = injectGlobalOutputFormat("prompt", rest, outputFormat)
		return base, "prompt", rest, nil
	}
	if len(rest) == 0 {
		return base, "", nil, nil
	}
	command, rest := rest[0], rest[1:]
	outputFormat = resolveGlobalOutputFormat(outputFormat, jsonOutput)
	if outputFormat != "" && commandAcceptsGlobalOutputFormat(command) && !argsHaveOutputFormat(rest) {
		expected := []string{"text", "json"}
		if strings.EqualFold(command, "prompt") {
			expected = []string{"text", "json", "stream-json"}
		}
		normalized, err := normalizeOutputFormat(command, outputFormat, expected)
		if err != nil {
			return base, "", nil, err
		}
		outputFormat = normalized
	}
	rest = injectGlobalOutputFormat(command, rest, outputFormat)
	return base, command, rest, nil
}

func missingToolFlagArgument(args []string) (missingArgumentError, bool) {
	for index := 0; index < len(args); index++ {
		argument, inlineValue, inline, ok := parseToolRuleFlag(args[index])
		if !ok {
			continue
		}
		if inline {
			if strings.TrimSpace(inlineValue) == "" {
				return missingArgumentError{Argument: argument, Example: toolRuleFlagExample(argument)}, true
			}
			continue
		}
		nextIndex := index + 1
		if nextIndex >= len(args) {
			return missingArgumentError{Argument: argument, Example: toolRuleFlagExample(argument)}, true
		}
		next := strings.TrimSpace(args[nextIndex])
		if next == "" || strings.HasPrefix(next, "-") || looksLikeCommandName(next) {
			return missingArgumentError{Argument: argument, Example: toolRuleFlagExample(argument)}, true
		}
		index = nextIndex
	}
	return missingArgumentError{}, false
}

func parseToolRuleFlag(arg string) (argument string, value string, inline bool, ok bool) {
	for _, candidate := range []string{"--allowed-tools", "--allowedTools", "--disallowed-tools", "--disallowedTools"} {
		if arg == candidate {
			return candidate, "", false, true
		}
		prefix := candidate + "="
		if strings.HasPrefix(arg, prefix) {
			return candidate, strings.TrimPrefix(arg, prefix), true, true
		}
	}
	return "", "", false, false
}

func looksLikeCommandName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") {
		return true
	}
	for _, command := range builtInCommandNames() {
		if strings.EqualFold(value, command) {
			return true
		}
	}
	return false
}

func toolRuleFlagExample(argument string) string {
	switch argument {
	case "--allowedTools", "--disallowedTools":
		return argument + " read,glob"
	default:
		return argument + " read_file,grep"
	}
}

func normalizeOutputFormat(command, value string, expected []string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	lower := strings.ToLower(value)
	for _, candidate := range expected {
		if lower == candidate {
			return lower, nil
		}
	}
	return "", outputFormatError{Command: command, Value: value, Expected: expected}
}

func resolveGlobalOutputFormat(outputFormat string, jsonOutput bool) string {
	if strings.TrimSpace(outputFormat) != "" {
		return outputFormat
	}
	if jsonOutput {
		return "json"
	}
	return strings.TrimSpace(os.Getenv("CODOG_OUTPUT_FORMAT"))
}

func injectGlobalOutputFormat(command string, rest []string, format string) []string {
	format = strings.TrimSpace(format)
	if format == "" || !commandAcceptsGlobalOutputFormat(command) || argsHaveOutputFormat(rest) {
		return rest
	}
	out := append([]string(nil), rest...)
	out = append(out, "--output-format", format)
	return out
}

func commandAcceptsGlobalOutputFormat(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "add-dir", "advisor", "agents", "background", "blame", "brief", "bughunter", "capabilities", "changelog", "chrome",
		"color", "commands", "commit", "commit-push-pr", "compact", "config", "context", "ctx_viz",
		"debug-tool-call", "desktop", "diff", "doctor", "dump-manifests", "effort", "env",
		"extra-usage", "fast", "feedback", "files", "focus", "heapdump", "hooks",
		"help", "init", "init-verifiers", "insights", "issue", "keybindings", "log", "marketplace",
		"mcp", "memory", "mobile", "output-style", "passes", "plugin", "plugins", "pr",
		"pr-comments", "prompt", "privacy-settings", "project", "rate-limit-options", "reload-plugins",
		"remote-env", "remote-setup", "reset-limits", "review", "sandbox-toggle",
		"search", "security-review", "skills", "state", "status", "statusline",
		"stash", "stickers", "stats", "system-prompt", "templates", "terminal-setup", "theme",
		"think-back", "thinkback", "thinkback-play", "todos", "undo", "unfocus",
		"ultrareview", "usage", "version", "vim", "voice", "web-setup":
		return true
	default:
		return false
	}
}

func argsHaveOutputFormat(args []string) bool {
	for _, arg := range args {
		if arg == "--json" || arg == "--output-format" || arg == "-o" || strings.HasPrefix(arg, "--output-format=") {
			return true
		}
	}
	return false
}

type helpReport struct {
	Kind                    string   `json:"kind"`
	Action                  string   `json:"action"`
	Status                  string   `json:"status"`
	Topic                   string   `json:"topic,omitempty"`
	Command                 string   `json:"command,omitempty"`
	Usage                   string   `json:"usage"`
	Help                    string   `json:"help"`
	LocalOnly               *bool    `json:"local_only,omitempty"`
	RequiresCredentials     *bool    `json:"requires_credentials,omitempty"`
	RequiresProviderRequest *bool    `json:"requires_provider_request,omitempty"`
	RequiresSessionResume   *bool    `json:"requires_session_resume,omitempty"`
	MutatesWorkspace        *bool    `json:"mutates_workspace,omitempty"`
	OutputFields            []string `json:"output_fields,omitempty"`
	StatusValues            []string `json:"status_values,omitempty"`
	CheckNames              []string `json:"check_names,omitempty"`
}

func renderHelpCommand(out io.Writer, args []string) error {
	format, topic, err := parseHelpArgs(args)
	if err != nil {
		return err
	}
	if topic != "" {
		if spec, ok := commandHelpSpecFor(topic); ok {
			return renderCommandHelpSpec(out, spec, format)
		}
	}
	help := helpText(filepath.Base(os.Args[0]))
	if format == "json" {
		usage := "codog [flags] COMMAND [ARGS...]"
		if topic != "" {
			usage = "codog " + topic + " [ARGS...]"
		}
		data, _ := json.MarshalIndent(helpReport{
			Kind:   "help",
			Action: "show",
			Status: "ok",
			Topic:  topic,
			Usage:  usage,
			Help:   help,
		}, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprint(out, help)
	return nil
}

func parseHelpArgs(args []string) (string, string, error) {
	format := "text"
	topicParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", "", errors.New("help output format is required")
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return "", "", fmt.Errorf("unknown help flag %q", arg)
		default:
			topicParts = append(topicParts, arg)
		}
	}
	if err := validateTextOrJSON(format, "help"); err != nil {
		return "", "", err
	}
	return format, strings.TrimSpace(strings.Join(topicParts, " ")), nil
}

type commandHelpSpec struct {
	Topic                   string
	Command                 string
	Usage                   string
	Text                    string
	LocalOnly               bool
	RequiresCredentials     bool
	RequiresProviderRequest bool
	RequiresSessionResume   bool
	MutatesWorkspace        bool
	OutputFields            []string
	StatusValues            []string
	CheckNames              []string
}

func renderGlobalResumeHelp(out io.Writer, args []string) (bool, error) {
	if len(args) < 2 || args[0] != "--resume" || !isHelpFlag(args[1]) {
		return false, nil
	}
	return true, renderCommandHelpTopic(out, "resume", args[2:], requestedOutputFormat(args))
}

func renderCommandHelpRequest(out io.Writer, command string, args []string, fallbackFormat string) (bool, error) {
	if _, ok := commandHelpSpecFor(command); !ok {
		return false, nil
	}
	helpArgs := make([]string, 0, len(args))
	helpRequested := false
	for _, arg := range args {
		if isHelpFlag(arg) {
			helpRequested = true
			continue
		}
		helpArgs = append(helpArgs, arg)
	}
	if !helpRequested {
		return false, nil
	}
	return true, renderCommandHelpTopic(out, command, helpArgs, fallbackFormat)
}

func renderCommandHelpTopic(out io.Writer, topic string, args []string, fallbackFormat string) error {
	spec, ok := commandHelpSpecFor(topic)
	if !ok {
		return fmt.Errorf("unknown help topic %q", topic)
	}
	format, err := parseCommandHelpFormat(spec.Command, args, fallbackFormat)
	if err != nil {
		return renderCLIError(out, err, fallbackFormat)
	}
	return renderCommandHelpSpec(out, spec, format)
}

func parseCommandHelpFormat(command string, args []string, fallbackFormat string) (string, error) {
	format := strings.TrimSpace(fallbackFormat)
	if format == "" {
		format = "text"
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", missingArgumentError{Argument: "--output-format", Example: "--output-format json"}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return "", fmt.Errorf("unknown %s help flag %q", command, arg)
		default:
			return "", fmt.Errorf("unknown %s help argument %q", command, arg)
		}
	}
	normalized, err := normalizeOutputFormat(command+" help", format, []string{"text", "json"})
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func renderCommandHelpSpec(out io.Writer, spec commandHelpSpec, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(helpReport{
			Kind:                    "help",
			Action:                  "help",
			Status:                  "ok",
			Topic:                   spec.Topic,
			Command:                 spec.Command,
			Usage:                   spec.Usage,
			Help:                    spec.Text,
			LocalOnly:               boolPtr(spec.LocalOnly),
			RequiresCredentials:     boolPtr(spec.RequiresCredentials),
			RequiresProviderRequest: boolPtr(spec.RequiresProviderRequest),
			RequiresSessionResume:   boolPtr(spec.RequiresSessionResume),
			MutatesWorkspace:        boolPtr(spec.MutatesWorkspace),
			OutputFields:            append([]string(nil), spec.OutputFields...),
			StatusValues:            append([]string(nil), spec.StatusValues...),
			CheckNames:              append([]string(nil), spec.CheckNames...),
		}, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprint(out, spec.Text)
	return nil
}

func commandHelpSpecFor(topic string) (commandHelpSpec, bool) {
	switch strings.ToLower(strings.TrimSpace(topic)) {
	case "doctor":
		return commandHelpSpec{
			Topic:                   "doctor",
			Command:                 "doctor",
			Usage:                   "codog doctor [--output-format text|json]",
			Text:                    "Doctor\n\nUsage:\n  codog doctor [--output-format text|json]\n\nRuns local diagnostics for auth, config, memory, MCP, hooks, git, sandbox, and runtime state; no provider request or session resume required.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"checks", "has_failures", "summary"},
			StatusValues:            []string{"ok", "warn", "fail"},
			CheckNames:              []string{"Auth", "Base URL", "Config home", "Workspace", "Memory", "Model", "Permissions", "Tools", "MCP", "Sessions", "Hooks", "Git", "Sandbox", "Go toolchain", "Runtime"},
		}, true
	case "compact":
		return commandHelpSpec{
			Topic:                   "compact",
			Command:                 "compact",
			Usage:                   "codog compact [--session ID|--resume ID|latest] [--keep N] [--output-format text|json]",
			Text:                    "Compact\n\nUsage:\n  codog compact [--session ID|--resume ID|latest] [--keep N] [--output-format text|json]\n\nCompacts a saved session by keeping the most recent messages. Help is local and does not resume a session.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"session_id", "path", "original_messages", "remaining_messages", "removed_messages"},
			StatusValues:            []string{"ok", "error"},
		}, true
	case "session":
		return commandHelpSpec{
			Topic:                   "session",
			Command:                 "session",
			Usage:                   "codog sessions [list|show|exists|fork|rename|delete] [ARGS...]",
			Text:                    "Session\n\nUsage:\n  codog sessions [list|show|exists|fork|rename|delete] [ARGS...]\n\nInspects and mutates saved session metadata. Help is local and does not open a session.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"id", "messages", "created_at", "updated_at"},
			StatusValues:            []string{"ok", "error"},
		}, true
	case "sessions":
		spec, _ := commandHelpSpecFor("session")
		spec.Topic = "sessions"
		spec.Command = "sessions"
		return spec, true
	case "resume":
		return commandHelpSpec{
			Topic:                   "resume",
			Command:                 "resume",
			Usage:                   "codog --resume ID|latest [prompt TEXT|repl]",
			Text:                    "Resume\n\nUsage:\n  codog --resume ID|latest [prompt TEXT|repl]\n\nSelects an existing session before running prompt or REPL. Help is local and does not open a session.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"session_id", "message_count"},
			StatusValues:            []string{"ok", "error"},
		}, true
	default:
		return commandHelpSpec{}, false
	}
}

func isHelpFlag(arg string) bool {
	return arg == "--help" || arg == "-h"
}

func boolPtr(value bool) *bool {
	return &value
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, helpText(filepath.Base(os.Args[0])))
}

func helpText(exe string) string {
	help := `%s is a Go-native coding agent CLI.

Usage:
  %s [flags] prompt "explain this repo" [--json|--output-format text|json|stream-json] | -p "explain this repo"
  %s [flags] btw "quick side question" [--session ID|--resume ID]
  %s version [--json|--output-format text|json]
  %s config [get SECTION|paths|set KEY VALUE|unset KEY] [--json|--output-format text|json]
  %s [flags] repl
  %s [flags] tui
  %s [flags] sessions [list|show|exists|fork|rename|delete]
  %s [flags] backfill-sessions [--json|--output-format text|json]
  %s [flags] rename NEW_ID [--session ID] [--json|--output-format text|json]
  %s [flags] history [--session ID] [--limit N] [--json|--output-format text|json]
  %s [flags] summary [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] rewind [N] [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] todos [list|add|start|done|pending|clear] [ARGS...] [--json|--output-format text|json]
  %s [flags] export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl|html] | share [DIR] [--session ID] [--format markdown|json|jsonl|html] | copy [last|N|all] [--session ID]
  %s [flags] skills [list|show|invoke|install|uninstall]
  %s [flags] commands [list|show|run]
  %s [flags] templates [list|show|apply]
  %s [flags] hooks [list|run pre|post|post-failure|permission-request|permission-denied|user-prompt-submit|session-start|session-end|setup|stop|stop-failure|pre-compact|post-compact|notification|subagent-start|subagent-stop|worktree-create|worktree-remove|cwd-changed|task-created|task-completed|instructions-loaded|file-changed] [--tool NAME] [--input JSON] [--output TEXT] [--reason TEXT] [--notification-type TYPE] [--title TEXT] [--agent-id ID] [--agent-type TYPE] [--worktree-id ID] [--worktree-path PATH] [--ref REF] [--old-cwd PATH] [--new-cwd PATH] [--task-id ID] [--task-kind KIND] [--task-status STATUS] [--path PATH] [--operation NAME] [--memory-type TYPE] [--load-reason REASON] [--json|--output-format text|json]
  %s [flags] output-style [list|show|set|clear] [NAME] [--json|--output-format text|json]
  %s [flags] model [NAME]
  %s [flags] advisor [MODEL|off] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] max-tokens [N]
  %s [flags] max-turns [N]
  %s [flags] permissions [show|read-only|workspace-write|danger-full-access|prompt|allow]
  %s [flags] allowed-tools [list|add|remove|clear] [TOOL...]
  %s [flags] brief MESSAGE [--status normal|proactive] [--attach PATH] [--json|--output-format text|json]
  %s [flags] mcp [list|serve|self|show|add|remove|tools|call|resources|resource-templates|read|prompts|prompt]
  %s [flags] capabilities [--json|--output-format text|json]
  %s acp [serve] [--json|--output-format text|json]
  %s [flags] status [--json|--output-format text|json]
  %s [flags] statusline [--json|--output-format text|json]
  %s [flags] terminal-setup [status|snippet|install|uninstall] [--shell zsh|bash|fish|powershell] [--path PATH] [--force] [--json|--output-format text|json]
  %s [flags] context [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] ctx_viz [--session ID|--resume ID|latest] [--output PATH] [--json|--output-format text|json]
  %s [flags] init [--json|--output-format text|json]
  %s [flags] init-verifiers [--target claude|codog] [--dry-run] [--force] [--json|--output-format text|json]
  %s [flags] state [--json|--output-format text|json]
  %s [flags] memory [list|show|add|path|ensure|edit] [ARGS...] [--editor COMMAND] [--no-open] [--json|--output-format text|json]
  %s [flags] project [--json|--output-format text|json]
  %s [flags] env [--json|--output-format text|json]
  %s [flags] files [PATH] [--glob GLOB] [--limit N] [--hidden] [--json|--output-format text|json]
  %s [flags] search PATTERN [--path PATH] [--glob GLOB] [--ignore-case] [--limit N] [--json|--output-format text|json]
  %s [flags] security-review [--limit N] [--json|--output-format text|json]
  %s [flags] bughunter [PATH] [--limit N] [--json|--output-format text|json]
  %s [flags] review|ultrareview [--staged] [--base REF] [--limit N] [--json|--output-format text|json]
  %s [flags] feedback [MESSAGE...] [--session ID] [--output PATH] [--json|--output-format text|json]
  %s [flags] pr [CONTEXT...] [--session ID] [--output PATH] [--json|--output-format text|json]
  %s [flags] commit-push-pr MESSAGE [--title TITLE] [--body BODY] [--branch NAME] [--base REF] [--remote NAME] [--staged] [--draft] [--no-pr] [--dry-run] [--json|--output-format text|json]
  %s [flags] pr-comments [PR|URL|NUMBER] [--repo OWNER/REPO] [--json|--output-format text|json]
  %s [flags] install-github-app [--workflow claude|review|all] [--secret-name NAME] [--dry-run] [--force] [--json|--output-format text|json]
  %s [flags] install-slack-app [--no-open] [--json|--output-format text|json]
  %s [flags] stickers [--no-open] [--json|--output-format text|json]
  %s [flags] passes [show|set-url URL|clear-url] [--no-open] [--json|--output-format text|json]
  %s [flags] issue [CONTEXT...] [--session ID] [--output PATH] [--json|--output-format text|json]
  %s [flags] focus [PATH...] [--json|--output-format text|json]
  %s [flags] unfocus [PATH...|--all] [--json|--output-format text|json]
  %s [flags] add-dir [PATH...|list|remove PATH|clear] [--json|--output-format text|json]
  %s [flags] theme [list|NAME|clear] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] color [list|NAME|clear] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] vim [on|off|toggle|status] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] effort [auto|low|medium|high|clear] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] fast [on|off|toggle|status|clear] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] voice [status|set-command|on|off|toggle|clear] [--command COMMAND] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] chrome [status|on|off|toggle|clear|install|permissions|reconnect] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] privacy-settings [show|set KEY on|off|clear KEY] [--target user|project|local] [--json|--output-format text|json]
  %s [flags] keybindings [show|path|init] [--force] [--json|--output-format text|json]
  %s [flags] cost --resume latest
  %s [flags] usage [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] stats [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] insights [--limit N] [--json|--output-format text|json]
  %s [flags] think-back|thinkback-play [--year YYYY] [--limit N] [--output PATH] [--json|--output-format text|json]
  %s [flags] extra-usage [--admin|--personal] [--no-open] [--json|--output-format text|json]
  %s [flags] compact [--session ID|--resume ID|latest] [--keep N] [--json|--output-format text|json]
  %s [flags] undo [--json|--output-format text|json]
  %s [flags] rate-limit-options [--json|--output-format text|json]
  %s [flags] reset-limits [--target user|project|local] [--path PATH] [--json|--output-format text|json]
  %s [flags] plan|ultraplan [show|enter|set|exit|clear] [TEXT] [--json|--output-format text|json]
  %s [flags] doctor [--json|--output-format text|json]
  %s [flags] branch [list|current|freshness [BRANCH] [BASE]|create NAME [START] [--switch]|switch NAME|delete NAME [--force]|rename [OLD] NEW] [--base REF] [--json|--output-format text|json]
  %s [flags] tag [list [PATTERN]|create NAME [REF] [-m MESSAGE]|show NAME|delete NAME] [--json|--output-format text|json]
  %s [flags] diff [--staged] [PATH...] [--json|--output-format text|json] | log [count] [--json|--output-format text|json] | blame FILE [line] [--json|--output-format text|json] | commit [--all] MESSAGE [--json|--output-format text|json]
  %s [flags] git status [--json|--output-format text|json] | git diff [--staged] [PATH...] [--json|--output-format text|json] | git branch [ARGS...] | git tag [ARGS...] | git log [count] [--json|--output-format text|json] | git changelog [count] [--json|--output-format text|json] | git blame FILE [line] [--json|--output-format text|json] | git stash [list|push|apply|pop] [ARGS...] [--json|--output-format text|json] | git commit [--all] MESSAGE [--json|--output-format text|json]
  %s [flags] stash [list|push|apply|pop] [ARGS...] [--json|--output-format text|json]
  %s [flags] changelog [count] [--json|--output-format text|json]
  %s [flags] release-notes [FROM [TO]] [--limit N] [--format markdown|json]
  %s [flags] run [--timeout-ms N] COMMAND [ARG...]
  %s [flags] node|python [--timeout-ms N] CODE|FILE [ARG...]
  %s [flags] test|build|lint [--timeout-ms N] [ARGS...]
  %s [flags] symbols|diagnostics|map|references|definition|hover|teleport|completion|format [ARGS...] [--json]
  %s mock-server :8089
  %s self-test
  %s dump-manifests [--manifests-dir PATH] [--json|--output-format text|json]
  %s system-prompt [--json|--output-format text|json]
  %s debug-tool-call TOOL JSON [--json|--output-format text|json]
  %s background run "command" | background list [session-id] | background status|stop|restart|logs|watch ID | background prune [days] [keep]
  %s tasks|bashes list|status|stop|restart|logs|watch ID
  %s agents list [FILTER] | agents show NAME | agents run [--worktree] NAME PROMPT | agents worktrees | agents worktree-remove ID [--json|--output-format text|json]
  %s reload-plugins [--json|--output-format text|json]
  %s plugin|plugins|marketplace list|show|validate|remote|updates|install|install-remote|update|enable|disable|remove | providers status|list|show|set
  %s login [browser|device] PROFILE [ARGS...] | oauth-refresh [PROFILE] | logout [PROFILE]
  %s oauth pkce | oauth discover ISSUER_URL | oauth provider save|list|show|delete | oauth device start|poll|login | oauth browser start|exchange|login | oauth status [PROFILE] | oauth logout [PROFILE] | oauth token save|show|refresh|revoke|delete
  %s sandbox | code-intel symbols|diagnostics|completion|format|lsp
  %s heapdump [PATH] [--no-gc] [--json|--output-format text|json]
  %s code-intel lsp query LANGUAGE ACTION PATH [LINE CHARACTER]
  %s remote serve [addr] | bridge|remote-control serve | bridge-kick [status|clear] | ide [status|clear] | updater check|verify|download|install|rollback
  %s sandbox-toggle [status|on|off|detect|sandbox-exec|bwrap|unshare|clear] [--target user|project|local] [--json|--output-format text|json]
  %s upgrade [check|verify|download|install|rollback] ARGS...
  %s install ARTIFACT [TARGET]
  %s remote-env [show|set|clear] [--enabled on|off] [--auth-token TOKEN|--clear-auth-token] [--lease-seconds N] [--target user|project|local] [--json|--output-format text|json]
  %s remote-setup|web-setup [status|enable|disable|clear] [--addr HOST:PORT] [--auth-token TOKEN|--clear-auth-token] [--lease-seconds N] [--target user|project|local] [--json|--output-format text|json]
  %s desktop|app [status] [--session ID|--resume latest] [--json|--output-format text|json]
  %s mobile|ios|android [all|ios|android] [--addr HOST:PORT] [--session ID|--resume latest] [--json|--output-format text|json]
  %s --acp|-acp [serve] [--json|--output-format text|json]
  %s enterprise [--json] | enterprise audit [limit] | enterprise verify POLICY PUBLIC_KEY
  %s config [get SECTION|paths|set KEY VALUE|unset KEY] [--json|--output-format text|json]

Flags:
  --model NAME
  --base-url URL
  --system-prompt TEXT
  --append-system-prompt TEXT
  --session ID
  --resume ID|latest
  --permission-mode read-only|workspace-write|danger-full-access|prompt|allow
  --dangerously-skip-permissions
  --skip-permissions
  --allowed-tools TOOL[,TOOL]
  --disallowed-tools TOOL[,TOOL]
  --max-turns N
  --max-tokens N
  --json
  --output-format text|json (prompt also accepts stream-json; CODOG_OUTPUT_FORMAT sets the default)
  --config PATH

Environment:
  ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_BASE_URL, CODOG_BASE_URL, CODOG_MODEL, CODOG_ADVISOR_MODEL, CODOG_SYSTEM_PROMPT, CODOG_APPEND_SYSTEM_PROMPT, CODOG_THEME, CODOG_EDITOR_MODE, CODOG_REASONING_EFFORT, CODOG_FAST_MODE, CODOG_VOICE_ENABLED, CODOG_VOICE_COMMAND, CODOG_CHROME_DEFAULT_ENABLED, CODOG_PRIVACY_PROMPT_HISTORY_ENABLED
`
	return strings.ReplaceAll(help, "%s", exe)
}

func redact(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "[redacted]"
	}
	return value[:4] + "..." + value[len(value)-4:]
}
