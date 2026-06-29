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
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/slash"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/usage"
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
	overrides, command, rest, err := parseFlags(args, baseOverrides)
	if err != nil {
		return err
	}
	if command == "help" || command == "--help" || command == "-h" {
		printHelp(os.Stdout)
		return nil
	}
	if command == "version" || command == "--version" || command == "-v" {
		fmt.Fprintln(os.Stdout, version)
		return nil
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
	app := &App{
		Config:    cfg,
		Client:    anthropic.New(cfg.BaseURL, cfg.APIKey, cfg.AuthToken),
		Tools:     tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{SandboxStrategy: cfg.Future.SandboxStrategy}),
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
	case "skills":
		return app.ListSkills()
	case "mcp":
		return app.MCP(ctx, rest)
	case "cost":
		return app.ShowCost(overrides)
	case "export":
		return app.Export(rest)
	case "git":
		return app.Git(rest)
	case "background":
		return app.BackgroundWithOverrides(rest, overrides)
	case "agents":
		return app.AgentsWithOverrides(rest, overrides)
	case "marketplace":
		return app.Marketplace(rest)
	case "oauth":
		return app.OAuth(rest)
	case "status":
		return app.Status(rest, overrides)
	case "doctor":
		return app.Doctor(rest)
	case "sandbox":
		return app.Sandbox()
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

func (a *App) Status(args []string, overrides config.FlagOverrides) error {
	format, err := parseStatusOutputFormat(args)
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

func parseStatusOutputFormat(args []string) (string, error) {
	format := "text"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return "", errors.New("status output format is required")
			}
			format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			return "", fmt.Errorf("unknown status flag %q", arg)
		}
	}
	switch format {
	case "text", "json":
		return format, nil
	default:
		return "", fmt.Errorf("unknown status output format %q", format)
	}
}

func (a *App) Doctor(args []string) error {
	format, err := parseDoctorOutputFormat(args)
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

func parseDoctorOutputFormat(args []string) (string, error) {
	format := "text"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return "", errors.New("doctor output format is required")
			}
			format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			return "", fmt.Errorf("unknown doctor flag %q", arg)
		}
	}
	switch format {
	case "text", "json":
		return format, nil
	default:
		return "", fmt.Errorf("unknown doctor output format %q", format)
	}
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
		return err
	}
	for _, msg := range result.Messages[len(sess.Messages):] {
		if err := a.Sessions.Append(sess.ID, msg); err != nil {
			return err
		}
	}
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
			fmt.Fprintln(a.Err, "error:", err)
			continue
		}
		for _, msg := range result.Messages[len(sess.Messages):] {
			if err := a.Sessions.Append(sess.ID, msg); err != nil {
				return err
			}
		}
		sess.Messages = result.Messages
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
	case "/cost":
		_ = a.ShowCost(config.FlagOverrides{SessionID: sess.ID})
	case "/config":
		a.handleConfigSlash(fields[1:])
	case "/model":
		a.handleModelSlash(fields[1:])
	case "/permissions":
		a.handlePermissionsSlash(fields[1:])
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
	case "/export":
		a.handleExportSlash(fields[1:], sess)
	case "/skills":
		_ = a.ListSkills()
	case "/mcp":
		_ = a.MCP(ctx, nil)
	case "/session":
		a.handleSessionSlash(fields[1:], sess)
	default:
		if _, ok := slash.Lookup(fields[0]); !ok {
			fmt.Fprintf(a.Err, "unknown slash command: %s\n", fields[0])
		}
	}
	return true
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
		return errors.New("usage: codog git status | git diff [--staged] | git commit [--all] MESSAGE")
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
	case "commit":
		options := parseGitCommitArgs(args[1:])
		result, err := gitops.Commit(a.Workspace, options)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	default:
		return fmt.Errorf("unknown git command %q", args[0])
	}
	return nil
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
	if files, err := memory.Discover(a.Workspace); err == nil {
		if rendered := memory.Render(files); rendered != "" {
			builder.WriteString("\n\n")
			builder.WriteString(rendered)
		}
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
  %s [flags] repl
  %s [flags] tui
  %s [flags] sessions [list|show|exists|fork|delete]
  %s [flags] export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl]
  %s [flags] skills
  %s [flags] mcp
  %s [flags] status [--json|--output-format text|json]
  %s [flags] cost --resume latest
  %s [flags] doctor [--json|--output-format text|json]
  %s [flags] git status | git diff [--staged] | git commit [--all] MESSAGE
  %s mock-server :8089
  %s self-test
  %s background run "command" | background list [session-id] | background status|stop|restart|logs|watch ID | background prune [days] [keep]
  %s agents list | agents run [--worktree] NAME PROMPT | agents worktrees | agents worktree-remove ID
  %s marketplace list|remote|updates|install|install-remote|update|enable|disable|remove
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
`, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe)
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
