package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/commandrun"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/contextview"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/projectinit"
	"github.com/Rememorio/codog/internal/prompthistory"
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
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/usage"
	"github.com/Rememorio/codog/internal/versioninfo"
	"github.com/Rememorio/codog/internal/workerstate"
	"github.com/Rememorio/codog/internal/worktree"
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
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h":
			printHelp(os.Stdout)
			return nil
		case "--version", "-v":
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}
			return renderVersion(os.Stdout, workspace, args[1:])
		}
	}
	overrides, command, rest, err := parseFlags(args, baseOverrides)
	if err != nil {
		return err
	}
	if command == "help" || command == "--help" || command == "-h" {
		printHelp(os.Stdout)
		return nil
	}
	if command == "version" || command == "--version" || command == "-v" {
		workspace, err := os.Getwd()
		if err != nil {
			return err
		}
		return renderVersion(os.Stdout, workspace, rest)
	}
	if command == "config" {
		cfg, paths, err := config.LoadForInspection(overrides)
		if err != nil {
			return err
		}
		cfg = redactedConfig(cfg)
		data, _ := json.MarshalIndent(map[string]any{"config": cfg, "paths": paths}, "", "  ")
		fmt.Fprintln(os.Stdout, string(data))
		return nil
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
		return initProject(os.Stdout, workspace, rest)
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
		return renderMemoryReport(os.Stdout, workspace, rest)
	}
	if command == "enterprise" && len(rest) > 0 && rest[0] == "verify" {
		return enterpriseVerify(os.Stdout, rest)
	}

	cfg, err := config.Load(overrides)
	if err != nil {
		return err
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
		Tools:     tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{SandboxStrategy: cfg.Future.SandboxStrategy, AdditionalDirs: additionalDirs}),
		Sessions:  session.NewWorkspaceStore(cfg.ConfigHome, workspace),
		Workspace: workspace,
		Out:       os.Stdout,
		Err:       os.Stderr,
		In:        os.Stdin,
	}
	if err := app.RegisterPluginTools(); err != nil {
		return err
	}

	switch command {
	case "", "repl":
		return app.REPL(ctx, overrides)
	case "tui":
		result, err := tui.Prompt()
		if err != nil {
			return err
		}
		if !result.Submitted || result.Prompt == "" {
			return nil
		}
		return app.Prompt(ctx, result.Prompt, overrides)
	case "prompt":
		input := strings.Join(rest, " ")
		if strings.TrimSpace(input) == "" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			input = string(data)
		}
		return app.Prompt(ctx, input, overrides)
	case "sessions":
		return app.SessionsCommand(rest)
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
	case "skills":
		return app.ListSkills()
	case "templates":
		return app.Templates(rest)
	case "hooks":
		return app.Hooks(ctx, rest)
	case "mcp":
		return app.MCP(ctx, rest)
	case "cost":
		return app.ShowCost(overrides)
	case "usage":
		return app.Usage(rest, overrides)
	case "rate-limit-options":
		return app.RateLimitOptions(rest)
	case "export":
		return app.Export(rest)
	case "git":
		return app.Git(rest)
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
	case "review":
		return app.Review(rest)
	case "run":
		return app.RunCommand(ctx, rest)
	case "test":
		return app.ProjectCommand(ctx, "test", rest)
	case "build":
		return app.ProjectCommand(ctx, "build", rest)
	case "lint":
		return app.ProjectCommand(ctx, "lint", rest)
	case "background":
		return app.BackgroundWithOverrides(rest, overrides)
	case "agents":
		return app.AgentsWithOverrides(rest, overrides)
	case "marketplace":
		return app.Marketplace(rest)
	case "login":
		return app.Login(rest)
	case "logout":
		return app.Logout(rest)
	case "oauth":
		return app.OAuth(rest)
	case "status":
		return app.Status(rest, overrides)
	case "context":
		return app.Context(rest, overrides)
	case "search":
		return app.Search(ctx, rest)
	case "security-review":
		return app.SecurityReview(rest)
	case "init":
		return app.Init(rest)
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
	case "code-intel":
		return app.CodeIntel(rest)
	case "remote":
		return app.Remote(rest)
	case "bridge":
		return app.Bridge(rest)
	case "updater":
		return app.Updater(ctx, rest)
	case "enterprise":
		return app.Enterprise(rest)
	default:
		if command != "" {
			return fmt.Errorf("unknown command %q", command)
		}
		return app.REPL(ctx, overrides)
	}
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
	fmt.Fprintf(a.Err, "codog remote control listening on http://%s\n", addr)
	return http.ListenAndServe(addr, control.Server{
		Sessions:   a.Sessions,
		ConfigHome: a.Config.ConfigHome,
		Workspace:  a.Workspace,
		AuthToken:  a.Config.Future.RemoteAuthToken,
		LeaseTTL:   time.Duration(a.Config.Future.RemoteLeaseSeconds) * time.Second,
	}.Handler())
}

func (a *App) Bridge(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: codog bridge serve")
	}
	return bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}.Serve(a.In, a.Out)
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
		return nil
	case "restart":
		task, err := store.Restart(args[1], a.Workspace)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(task, "", "  ")
		fmt.Fprintln(a.Out, string(data))
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

func (a *App) RegisterMCPTools(ctx context.Context) error {
	if a.mcpToolsLoaded {
		return nil
	}
	for serverName, server := range a.Config.MCPServers {
		result := mcp.ListTools(ctx, serverName, server)
		if result.Error != "" {
			return fmt.Errorf("mcp server %q: %s", serverName, result.Error)
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
		}
	}
	a.mcpToolsLoaded = true
	return nil
}

func (a *App) ListAgents() error {
	defs, err := agentdefs.Load(a.Workspace)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(defs, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Agents(args []string) error {
	return a.AgentsWithOverrides(args, config.FlagOverrides{})
}

func (a *App) AgentsWithOverrides(args []string, overrides config.FlagOverrides) error {
	if len(args) == 0 || args[0] == "list" {
		return a.ListAgents()
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
		if err := worktree.Remove(a.Workspace, args[1]); err != nil {
			return err
		}
		data, _ := json.MarshalIndent(map[string]any{"removed": true, "id": args[1]}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] != "run" {
		return fmt.Errorf("unknown agents command %q", args[0])
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
	}
	command := buildAgentCommand(exe, *selected, req.Prompt)
	sessionID, err := a.sessionIDFromOverrides(overrides)
	if err != nil {
		if allocation != nil {
			_ = worktree.Remove(a.Workspace, allocation.ID)
		}
		return err
	}
	task, err := background.NewStore(a.Config.ConfigHome).RunWithOptions(command, runWorkspace, background.RunOptions{SessionID: sessionID})
	if err != nil {
		if allocation != nil {
			_ = worktree.Remove(a.Workspace, allocation.ID)
		}
		return err
	}
	response := map[string]any{"agent": selected.Name, "task": task}
	if allocation != nil {
		response["worktree"] = allocation
	}
	data, _ := json.MarshalIndent(response, "", "  ")
	fmt.Fprintln(a.Out, string(data))
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
	manifests, err := plugins.Load(a.Workspace)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(manifests, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Marketplace(args []string) error {
	if len(args) == 0 || args[0] == "list" {
		return a.ListPlugins()
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
			return err
		}
		payload = manifest
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
			return err
		}
		payload = manifest
	case "disable":
		if len(args) < 2 {
			return errors.New("usage: codog marketplace disable ID")
		}
		manifest, err := plugins.Disable(a.Workspace, args[1])
		if err != nil {
			return err
		}
		payload = manifest
	case "remove":
		if len(args) < 2 {
			return errors.New("usage: codog marketplace remove ID")
		}
		if err := plugins.Remove(a.Workspace, args[1]); err != nil {
			return err
		}
		payload = map[string]any{"removed": true, "id": args[1]}
	default:
		return fmt.Errorf("unknown marketplace command %q", args[0])
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
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
	data, _ := json.MarshalIndent(status, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Init(args []string) error {
	return initProject(a.Out, a.Workspace, args)
}

func (a *App) State(args []string) error {
	return renderWorkerState(a.Out, a.Workspace, args)
}

func (a *App) Memory(args []string) error {
	return renderMemoryReport(a.Out, a.Workspace, args)
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
	Format    string
	Action    string
	Event     string
	Tool      string
	Input     string
	Output    string
	IsError   bool
	TimeoutMS int
}

type hooksListReport struct {
	Kind        string   `json:"kind"`
	Action      string   `json:"action"`
	Status      string   `json:"status"`
	PreToolUse  []string `json:"pre_tool_use"`
	PostToolUse []string `json:"post_tool_use"`
}

func (a *App) Hooks(ctx context.Context, args []string) error {
	req, err := parseHooksArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "list":
		report := hooksListReport{
			Kind:        "hooks",
			Action:      "list",
			Status:      "ok",
			PreToolUse:  append([]string(nil), a.Config.Hooks.PreToolUse...),
			PostToolUse: append([]string(nil), a.Config.Hooks.PostToolUse...),
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
			Event:   req.Event,
			Tool:    req.Tool,
			Input:   req.Input,
			Output:  req.Output,
			IsError: req.IsError,
		}
		commands := a.Config.Hooks.PreToolUse
		if req.Event == "post_tool_use" {
			commands = a.Config.Hooks.PostToolUse
		}
		timeout := time.Duration(req.TimeoutMS) * time.Millisecond
		report, runErr := hooks.Runner{Config: a.Config.Hooks, Workspace: a.Workspace, Timeout: timeout}.RunPayload(ctx, commands, payload)
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
		case strings.HasPrefix(arg, "--tool="):
			req.Tool = strings.TrimPrefix(arg, "--tool=")
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
	return req, nil
}

func normalizeHookEvent(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pre", "pre_tool_use", "pre-tool-use":
		return "pre_tool_use", nil
	case "post", "post_tool_use", "post-tool-use":
		return "post_tool_use", nil
	default:
		return "", fmt.Errorf("unknown hook event %q", value)
	}
}

func renderHooksList(out io.Writer, report hooksListReport) {
	fmt.Fprintln(out, "Hooks")
	fmt.Fprintf(out, "  Pre tool use     %d\n", len(report.PreToolUse))
	for _, command := range report.PreToolUse {
		fmt.Fprintf(out, "    %s\n", command)
	}
	fmt.Fprintf(out, "  Post tool use    %d\n", len(report.PostToolUse))
	for _, command := range report.PostToolUse {
		fmt.Fprintf(out, "    %s\n", command)
	}
}

func renderHooksRun(out io.Writer, report hooks.RunReport) {
	fmt.Fprintln(out, "Hook Run")
	fmt.Fprintf(out, "  Event            %s\n", report.Event)
	fmt.Fprintf(out, "  Tool             %s\n", report.Tool)
	fmt.Fprintf(out, "  Commands         %d\n", report.Count)
	for _, result := range report.Results {
		fmt.Fprintf(out, "  %s success=%t duration_ms=%d\n", result.Command, result.Success, result.DurationMS)
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
	if strings.TrimSpace(value) == "" {
		return "none"
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

func initProject(out io.Writer, workspace string, args []string) error {
	format, err := parseSimpleOutputFormat("init", args)
	if err != nil {
		return err
	}
	report, err := projectinit.Initialize(workspace)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, projectinit.RenderText(report))
	return nil
}

func renderMemoryReport(out io.Writer, workspace string, args []string) error {
	format, err := parseSimpleOutputFormat("memory", args)
	if err != nil {
		return err
	}
	report, err := memory.BuildReport(workspace)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	memory.RenderReport(out, report)
	return nil
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
	sandboxStatus := sandbox.Detect()
	executable := ""
	if path, err := os.Executable(); err == nil {
		executable = path
	}
	return localstatus.Build(localstatus.Options{
		Version:             version,
		Workspace:           a.Workspace,
		ConfigHome:          a.Config.ConfigHome,
		Model:               a.Config.Model,
		BaseURL:             a.Config.BaseURL,
		PermissionMode:      a.Config.PermissionMode,
		MaxTokens:           a.Config.MaxTokens,
		MaxTurns:            a.Config.MaxTurns,
		AutoCompactMessages: a.Config.AutoCompactMessages,
		AuthConfigured:      a.Config.APIKey != "" || a.Config.AuthToken != "",
		MCPServerCount:      len(a.Config.MCPServers),
		PreHookCount:        len(a.Config.Hooks.PreToolUse),
		PostHookCount:       len(a.Config.Hooks.PostToolUse),
		EnabledSkillCount:   len(a.Config.EnabledSkills),
		MemoryFiles:         memoryStatuses,
		ToolNames:           toolNames,
		SessionID:           sessionID,
		SessionPath:         sessionPath,
		SessionMessages:     sessionMessages,
		SessionCount:        sessionCount,
		GitStatus:           gitRaw,
		GitError:            gitError,
		SandboxOS:           sandboxStatus.OS,
		SandboxDefault:      sandboxStatus.Default,
		SandboxStrategies:   sandboxStatus.Strategies,
		SandboxAvailable:    sandboxStatus.Available,
		Executable:          executable,
	})
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
	sandboxStatus := sandbox.Detect()
	report := doctor.Run(doctor.Options{
		Workspace:      a.Workspace,
		ConfigHome:     a.Config.ConfigHome,
		Model:          a.Config.Model,
		BaseURL:        a.Config.BaseURL,
		APIKey:         a.Config.APIKey,
		AuthToken:      a.Config.AuthToken,
		PermissionMode: a.Config.PermissionMode,
		ToolCount:      toolCount,
		SessionCount:   sessionCount,
		MemoryFiles:    memoryPaths,
		SandboxDefault: sandboxStatus.Default,
		SandboxOK:      sandboxStatus.Available,
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
	if err := a.Sessions.AppendInput(sess.ID, input); err != nil {
		return err
	}
	a.writeWorkerState("prompt", "running", sess, "")
	runner := runloop.Runner{
		Config:    a.Config,
		Client:    a.Client,
		Tools:     a.Tools,
		Prompter:  a.prompter(sess.ID),
		Workspace: a.Workspace,
		Out:       a.Out,
		System:    a.systemPrompt(),
		OnToolUse: a.auditToolUse(sess.ID),
	}
	result, err := runner.Run(ctx, sess.Messages, input)
	if err != nil {
		a.writeWorkerState("prompt", "error", sess, err.Error())
		return err
	}
	for _, msg := range result.Messages[len(sess.Messages):] {
		if err := a.Sessions.Append(sess.ID, msg); err != nil {
			return err
		}
	}
	sess.Messages = result.Messages
	a.writeWorkerState("prompt", "completed", sess, "")
	fmt.Fprintf(a.Err, "\n\nsession: %s\n", sess.ID)
	return nil
}

func (a *App) REPL(ctx context.Context, overrides config.FlagOverrides) error {
	if err := a.RegisterMCPTools(ctx); err != nil {
		return err
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	a.writeWorkerState("repl", "idle", sess, "")
	fmt.Fprintf(a.Err, "Codog %s (%s). Type /exit to quit.\n", version, sess.ID)
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
		if err := a.Sessions.AppendInput(sess.ID, line); err != nil {
			return err
		}
		a.writeWorkerState("repl", "running", sess, "")
		runner := runloop.Runner{
			Config:    a.Config,
			Client:    a.Client,
			Tools:     a.Tools,
			Prompter:  a.prompter(sess.ID),
			Workspace: a.Workspace,
			Out:       a.Out,
			System:    a.systemPrompt(),
			OnToolUse: a.auditToolUse(sess.ID),
		}
		result, err := runner.Run(ctx, sess.Messages, line)
		if err != nil {
			a.writeWorkerState("repl", "error", sess, err.Error())
			fmt.Fprintln(a.Err, "error:", err)
			continue
		}
		for _, msg := range result.Messages[len(sess.Messages):] {
			if err := a.Sessions.Append(sess.ID, msg); err != nil {
				return err
			}
		}
		sess.Messages = result.Messages
		a.writeWorkerState("repl", "idle", sess, "")
	}
	return scanner.Err()
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
	case "/context":
		if err := a.Context(nil, config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/sandbox":
		if err := a.Sandbox(); err != nil {
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
	case "/state":
		if err := a.State(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/memory":
		if err := a.Memory(nil); err != nil {
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
	case "/search":
		if err := a.Search(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/security-review":
		if err := a.SecurityReview(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/review":
		if err := a.Review(fields[1:]); err != nil {
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
	case "/cost":
		_ = a.ShowCost(config.FlagOverrides{SessionID: sess.ID})
	case "/tokens":
		_ = a.ShowCost(config.FlagOverrides{SessionID: sess.ID})
	case "/usage":
		if err := a.Usage(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/rate-limit-options":
		if err := a.RateLimitOptions(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/config":
		a.handleConfigSlash(fields[1:])
	case "/model":
		a.handleModelSlash(fields[1:])
	case "/max-tokens":
		a.handleMaxTokensSlash(fields[1:])
	case "/max-turns":
		a.handleMaxTurnsSlash(fields[1:])
	case "/system-prompt":
		fmt.Fprintln(a.Out, a.systemPrompt())
	case "/tool-details":
		a.handleToolDetailsSlash(fields[1:])
	case "/permissions":
		a.handlePermissionsSlash(fields[1:])
	case "/allowed-tools":
		a.handleAllowedToolsSlash(fields[1:])
	case "/doctor":
		if err := a.Doctor(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/compact":
		before := len(sess.Messages)
		sess.Messages = runloop.CompactMessages(sess.Messages, a.Config.AutoCompactMessages)
		fmt.Fprintf(a.Err, "compacted request context from %d to %d messages\n", before, len(sess.Messages))
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
	case "/export":
		a.handleExportSlash(fields[1:], sess)
	case "/history", "/prompt-history":
		a.handleHistorySlash(fields[1:], sess)
	case "/summary":
		if err := a.Summary(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/todos":
		if err := a.Todos(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/skills":
		_ = a.ListSkills()
	case "/templates":
		if err := a.Templates(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/hooks":
		if err := a.Hooks(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/mcp":
		_ = a.MCP(ctx, nil)
	case "/session":
		a.handleSessionSlash(fields[1:], sess)
	case "/clear":
		a.handleClearSlash(fields[1:], sess)
	case "/resume":
		a.handleResumeSlash(fields[1:], sess)
	case "/rewind":
		a.handleRewindSlash(fields[1:], sess)
	default:
		if _, ok := slash.Lookup(fields[0]); !ok {
			fmt.Fprintf(a.Err, "unknown slash command: %s\n", fields[0])
		}
	}
	return true
}

func (a *App) handleClearSlash(args []string, sess *session.Session) {
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
	*sess = *next
	a.writeWorkerState("repl", "idle", sess, "")
	fmt.Fprintf(a.Err, "session cleared: %s\n", sess.ID)
}

func (a *App) handleResumeSlash(args []string, sess *session.Session) {
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
	if len(args) == 0 {
		return cfg, nil
	}
	switch strings.ToLower(args[0]) {
	case "model":
		return map[string]any{"model": cfg.Model, "max_tokens": cfg.MaxTokens, "max_turns": cfg.MaxTurns}, nil
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
	if len(args) == 0 {
		return errors.New("usage: codog git status | git diff [--staged] | git log [count] | git changelog [count] | git blame FILE [line] | git branch [ARGS...] | git tag [ARGS...] | git stash [list|push|apply|pop] | git commit [--all] MESSAGE")
	}
	switch args[0] {
	case "status":
		status, err := gitops.Status(a.Workspace)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, status)
	case "diff":
		diff, err := gitops.Diff(a.Workspace, containsFold(args[1:], "--staged") || containsFold(args[1:], "--cached"))
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, diff)
	case "log":
		limit, err := parseOptionalPositiveInt(args[1:], 20, "git log count")
		if err != nil {
			return err
		}
		log, err := gitops.Log(a.Workspace, limit)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, log)
	case "changelog":
		return a.Changelog(args[1:])
	case "blame":
		path, line, err := parseGitBlameArgs(args[1:])
		if err != nil {
			return err
		}
		blame, err := gitops.Blame(a.Workspace, path, line)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, blame)
	case "stash":
		return a.Stash(args[1:])
	case "commit":
		options := parseGitCommitArgs(args[1:])
		result, err := gitops.Commit(a.Workspace, options)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	case "branch":
		return a.Branch(args[1:])
	case "tag":
		return a.Tag(args[1:])
	default:
		return fmt.Errorf("unknown git command %q", args[0])
	}
	return nil
}

type branchRequest struct {
	Format     string
	Action     string
	Name       string
	NewName    string
	StartPoint string
	Switch     bool
	Force      bool
}

type branchReport struct {
	Kind     string              `json:"kind"`
	Action   string              `json:"action"`
	Status   string              `json:"status"`
	Current  string              `json:"current"`
	Branches []gitops.BranchInfo `json:"branches,omitempty"`
	Output   string              `json:"output,omitempty"`
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
	limit, err := parseOptionalPositiveInt(args, 10, "changelog count")
	if err != nil {
		return err
	}
	log, err := gitops.Changelog(a.Workspace, limit)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, log)
	return nil
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
	action := "list"
	rest := args
	if len(args) > 0 {
		action = strings.ToLower(args[0])
		rest = args[1:]
	}
	var output string
	var err error
	switch action {
	case "list", "show":
		if len(rest) != 0 {
			return errors.New("usage: codog stash list")
		}
		output, err = gitops.StashList(a.Workspace)
	case "push", "save":
		options := parseStashPushArgs(rest)
		output, err = gitops.StashPush(a.Workspace, options)
	case "apply":
		if len(rest) > 1 {
			return errors.New("usage: codog stash apply [stash-ref]")
		}
		ref := ""
		if len(rest) == 1 {
			ref = rest[0]
		}
		output, err = gitops.StashApply(a.Workspace, ref)
	case "pop":
		if len(rest) > 1 {
			return errors.New("usage: codog stash pop [stash-ref]")
		}
		ref := ""
		if len(rest) == 1 {
			ref = rest[0]
		}
		output, err = gitops.StashPop(a.Workspace, ref)
	default:
		return fmt.Errorf("unknown stash action %q", action)
	}
	if err != nil {
		return err
	}
	if output == "" {
		output = "No output."
	}
	fmt.Fprintln(a.Out, output)
	return nil
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

func parseGitBlameArgs(args []string) (string, int, error) {
	if len(args) == 0 || len(args) > 2 {
		return "", 0, errors.New("usage: codog git blame FILE [line]")
	}
	path := args[0]
	line := 0
	if len(args) == 2 {
		parsed, err := strconv.Atoi(args[1])
		if err != nil || parsed <= 0 {
			return "", 0, errors.New("blame line must be a positive integer")
		}
		line = parsed
	}
	return path, line, nil
}

func (a *App) handleDiffSlash(args []string) {
	diff, err := gitops.Diff(a.Workspace, containsFold(args, "--staged") || containsFold(args, "--cached"))
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if diff == "" {
		fmt.Fprintln(a.Out, "No diff.")
		return
	}
	fmt.Fprintln(a.Out, diff)
}

func (a *App) handleLogSlash(args []string) {
	limit, err := parseOptionalPositiveInt(args, 20, "/log count")
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	log, err := gitops.Log(a.Workspace, limit)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	fmt.Fprintln(a.Out, log)
}

func (a *App) handleChangelogSlash(args []string) {
	if err := a.Changelog(args); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleBlameSlash(args []string) {
	path, line, err := parseGitBlameArgs(args)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	blame, err := gitops.Blame(a.Workspace, path, line)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	fmt.Fprintln(a.Out, blame)
}

func (a *App) handleCommitSlash(args []string) {
	options := parseGitCommitArgs(args)
	result, err := gitops.Commit(a.Workspace, options)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	fmt.Fprintf(a.Err, "commit %s\n", result.Commit)
	if result.Summary != "" {
		fmt.Fprintln(a.Out, result.Summary)
	}
}

func parseGitCommitArgs(args []string) gitops.CommitOptions {
	options := gitops.CommitOptions{}
	message := []string{}
	for _, arg := range args {
		switch arg {
		case "--all", "-a":
			options.All = true
		default:
			message = append(message, arg)
		}
	}
	options.Message = strings.Join(message, " ")
	return options
}

type exportRequest struct {
	SessionID string
	Output    string
	Format    string
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

func (a *App) handleExportSlash(args []string, sess *session.Session) {
	req, err := parseExportArgs(args, sess.ID)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.Output == "" {
		req.Output = session.DefaultExportFilename(sess)
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
				return req, errors.New("usage: codog export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl]")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--output" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("usage: codog export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl]")
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--format" || arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("usage: codog export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl]")
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
	all, err := skills.Load(a.Config.ConfigHome, a.Workspace)
	if err != nil {
		return err
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
	if len(a.Config.MCPServers) == 0 {
		fmt.Fprintln(a.Out, "No MCP servers configured.")
		return nil
	}
	if len(args) == 0 || args[0] == "list" {
		statuses := mcp.InspectAll(ctx, a.Config.MCPServers)
		data, _ := json.MarshalIndent(statuses, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if len(args) < 2 {
		return errors.New("usage: codog mcp list | call SERVER TOOL JSON | resources SERVER | read SERVER URI")
	}
	serverName := args[1]
	server, ok := a.Config.MCPServers[serverName]
	if !ok {
		return fmt.Errorf("unknown MCP server %q", serverName)
	}
	var payload any
	switch args[0] {
	case "call":
		if len(args) < 4 {
			return errors.New("usage: codog mcp call SERVER TOOL JSON")
		}
		payload = mcp.CallTool(ctx, serverName, server, args[2], json.RawMessage(args[3]))
	case "resources":
		payload = mcp.ListResources(ctx, serverName, server)
	case "read":
		if len(args) < 3 {
			return errors.New("usage: codog mcp read SERVER URI")
		}
		payload = mcp.ReadResource(ctx, serverName, server, args[2])
	default:
		return fmt.Errorf("unknown mcp command %q", args[0])
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) ShowCost(overrides config.FlagOverrides) error {
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	summary := usage.Estimate(sess.Messages, a.Config.Model)
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
	report := usage.BuildReport(sess.ID, a.Config.Model, sess.Messages)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	usage.RenderText(a.Out, report)
	return nil
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
	return a.Sessions.Open(id)
}

func (a *App) writeWorkerState(mode string, status string, sess *session.Session, lastError string) {
	if sess == nil {
		return
	}
	state := workerstate.New(workerstate.Options{
		Version:        version,
		Mode:           mode,
		Status:         status,
		Workspace:      a.Workspace,
		SessionID:      sess.ID,
		SessionPath:    sess.Path,
		Model:          a.Config.Model,
		PermissionMode: a.Config.PermissionMode,
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
	return &tools.Prompter{
		Mode:        tools.Permission(a.Config.PermissionMode),
		AllowRules:  append([]string(nil), a.Config.PermissionRules.Allow...),
		DenyRules:   append([]string(nil), a.Config.PermissionRules.Deny...),
		AskRules:    append([]string(nil), a.Config.PermissionRules.Ask...),
		DeniedTools: append([]string(nil), a.Config.PermissionRules.DeniedTools...),
		In:          a.In,
		Err:         a.Err,
		OnDecision:  a.auditPermissionDecision(sessionID),
	}
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
	base := "You are Codog, a Go-native coding agent CLI. Be concise, inspect before editing, and use tools when they materially help."
	var builder strings.Builder
	builder.WriteString(base)
	for _, name := range a.Config.EnabledSkills {
		skill, err := skills.Find(a.Config.ConfigHome, a.Workspace, name)
		if err != nil {
			continue
		}
		builder.WriteString("\n\n<skill name=\"")
		builder.WriteString(skill.Name)
		builder.WriteString("\">\n")
		builder.WriteString(skill.Body)
		builder.WriteString("\n</skill>")
	}
	if rendered := outputstyle.RenderPrompt(a.Config.ConfigHome, a.Workspace); rendered != "" {
		builder.WriteString("\n\n")
		builder.WriteString(rendered)
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

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func parseFlags(args []string, base config.FlagOverrides) (config.FlagOverrides, string, []string, error) {
	flags := flag.NewFlagSet("codog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&base.ConfigPath, "config", base.ConfigPath, "config path")
	flags.StringVar(&base.Model, "model", base.Model, "model name")
	flags.StringVar(&base.BaseURL, "base-url", base.BaseURL, "Anthropic-compatible base URL")
	flags.StringVar(&base.SessionID, "session", base.SessionID, "session id")
	flags.StringVar(&base.Resume, "resume", base.Resume, "resume session id or latest")
	flags.StringVar(&base.PermissionMode, "permission-mode", base.PermissionMode, "read-only, workspace-write, danger-full-access, prompt, allow")
	flags.IntVar(&base.MaxTurns, "max-turns", base.MaxTurns, "max model/tool loop iterations")
	flags.IntVar(&base.MaxTokens, "max-tokens", base.MaxTokens, "maximum output tokens")
	if err := flags.Parse(args); err != nil {
		return base, "", nil, err
	}
	rest := flags.Args()
	if len(rest) == 0 {
		return base, "", nil, nil
	}
	return base, rest[0], rest[1:], nil
}

func printHelp(out io.Writer) {
	exe := filepath.Base(os.Args[0])
	fmt.Fprintf(out, `%s is a Go-native coding agent CLI.

Usage:
  %s [flags] prompt "explain this repo"
  %s version [--json|--output-format text|json]
  %s [flags] repl
  %s [flags] tui
  %s [flags] sessions [list|show|exists|fork|delete]
  %s [flags] history [--session ID] [--limit N] [--json|--output-format text|json]
  %s [flags] summary [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] rewind [N] [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] todos [list|add|start|done|pending|clear] [ARGS...] [--json|--output-format text|json]
  %s [flags] export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl]
  %s [flags] skills
  %s [flags] templates [list|show|apply]
  %s [flags] hooks [list|run pre|post] [--tool NAME] [--input JSON] [--output TEXT] [--json|--output-format text|json]
  %s [flags] output-style [list|show|set|clear] [NAME] [--json|--output-format text|json]
  %s [flags] mcp
  %s [flags] status [--json|--output-format text|json]
  %s [flags] context [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] init [--json|--output-format text|json]
  %s [flags] state [--json|--output-format text|json]
  %s [flags] memory [--json|--output-format text|json]
  %s [flags] project [--json|--output-format text|json]
  %s [flags] env [--json|--output-format text|json]
  %s [flags] search PATTERN [--path PATH] [--glob GLOB] [--ignore-case] [--limit N] [--json|--output-format text|json]
  %s [flags] security-review [--limit N] [--json|--output-format text|json]
  %s [flags] review [--staged] [--base REF] [--limit N] [--json|--output-format text|json]
  %s [flags] focus [PATH...] [--json|--output-format text|json]
  %s [flags] unfocus [PATH...|--all] [--json|--output-format text|json]
  %s [flags] add-dir [PATH...|list|remove PATH|clear] [--json|--output-format text|json]
  %s [flags] cost --resume latest
  %s [flags] usage [--session ID|--resume ID|latest] [--json|--output-format text|json]
  %s [flags] rate-limit-options [--json|--output-format text|json]
  %s [flags] doctor [--json|--output-format text|json]
  %s [flags] branch [list|current|create NAME [START] [--switch]|switch NAME|delete NAME [--force]|rename [OLD] NEW] [--json|--output-format text|json]
  %s [flags] tag [list [PATTERN]|create NAME [REF] [-m MESSAGE]|show NAME|delete NAME] [--json|--output-format text|json]
  %s [flags] git status | git diff [--staged] | git branch [ARGS...] | git tag [ARGS...] | git log|changelog [count] | git blame FILE [line] | git stash [list|push|apply|pop] | git commit [--all] MESSAGE
  %s [flags] stash [list|push|apply|pop] [ARGS...]
  %s [flags] changelog [count]
  %s [flags] release-notes [FROM [TO]] [--limit N] [--format markdown|json]
  %s [flags] run [--timeout-ms N] COMMAND [ARG...]
  %s [flags] test|build|lint [--timeout-ms N] [ARGS...]
  %s [flags] symbols|diagnostics|map|references|definition|hover [ARGS...] [--json]
  %s mock-server :8089
  %s self-test
  %s background run "command" | background list [session-id] | background status|stop|restart|logs|watch ID | background prune [days] [keep]
  %s agents list | agents run [--worktree] NAME PROMPT | agents worktrees | agents worktree-remove ID
  %s marketplace list|remote|updates|install|install-remote|update|enable|disable|remove
  %s login [browser|device] PROFILE [ARGS...] | logout [PROFILE]
  %s oauth pkce | oauth discover ISSUER_URL | oauth provider save|list|show|delete | oauth device start|poll|login | oauth browser start|exchange|login | oauth status [PROFILE] | oauth logout [PROFILE] | oauth token save|show|refresh|revoke|delete
  %s sandbox | code-intel symbols|diagnostics|lsp
  %s remote serve [addr] | bridge serve | updater check|verify|download|install|rollback
  %s enterprise [--json] | enterprise audit [limit] | enterprise verify POLICY PUBLIC_KEY
  %s config

Flags:
  --model NAME
  --base-url URL
  --session ID
  --resume ID|latest
  --permission-mode read-only|workspace-write|danger-full-access|prompt|allow
  --max-turns N
  --max-tokens N
  --config PATH

Environment:
  ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_BASE_URL, CODOG_MODEL
`, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe)
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
