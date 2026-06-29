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

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/future"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/slash"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/usage"
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
		cfg.APIKey = redact(cfg.APIKey)
		cfg.AuthToken = redact(cfg.AuthToken)
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
	if command == "roadmap" || command == "capabilities" {
		if hasFlag(rest, "--json") {
			return future.RenderReportJSON(os.Stdout, future.NewReport(version))
		}
		future.RenderText(os.Stdout, future.Surfaces())
		return nil
	}

	cfg, err := config.Load(overrides)
	if err != nil {
		return err
	}
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	app := &App{
		Config:    cfg,
		Client:    anthropic.New(cfg.BaseURL, cfg.APIKey, cfg.AuthToken),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewStore(cfg.ConfigHome),
		Workspace: workspace,
		Out:       os.Stdout,
		Err:       os.Stderr,
		In:        os.Stdin,
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
		return app.ListSessions()
	case "skills":
		return app.ListSkills()
	case "mcp":
		return app.MCP(ctx, rest)
	case "cost":
		return app.ShowCost(overrides)
	case "background":
		return app.Background(rest)
	case "agents":
		return app.ListAgents()
	case "marketplace":
		return app.ListPlugins()
	case "oauth":
		return app.OAuth(rest)
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
		return app.FutureStatus(command, rest)
	default:
		if command != "" {
			return fmt.Errorf("unknown command %q", command)
		}
		return app.REPL(ctx, overrides)
	}
}

func (a *App) FutureStatus(command string, args []string) error {
	surface, ok := future.Find(command)
	if !ok {
		return fmt.Errorf("unknown capability %q", command)
	}
	if hasFlag(args, "--json") {
		return future.RenderJSON(a.Out, []future.Surface{surface})
	}
	future.RenderText(a.Out, []future.Surface{surface})
	return nil
}

func (a *App) Remote(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return a.FutureStatus("remote", args)
	}
	addr := "127.0.0.1:8791"
	if len(args) > 1 {
		addr = args[1]
	}
	fmt.Fprintf(a.Err, "codog remote control listening on http://%s\n", addr)
	return http.ListenAndServe(addr, control.Server{Sessions: a.Sessions}.Handler())
}

func (a *App) Bridge(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return a.FutureStatus("bridge", args)
	}
	return bridge.Server{Sessions: a.Sessions, Version: version}.Serve(a.In, a.Out)
}

func (a *App) Updater(ctx context.Context, args []string) error {
	if len(args) < 2 || args[0] != "check" {
		return a.FutureStatus("updater", args)
	}
	result, err := updater.Check(ctx, version, args[1])
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Background(args []string) error {
	store := background.NewStore(a.Config.ConfigHome)
	if len(args) == 0 || args[0] == "list" {
		tasks, err := store.List()
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(tasks, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if args[0] == "run" {
		command := strings.Join(args[1:], " ")
		task, err := store.Run(command, a.Workspace)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(task, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	return fmt.Errorf("unknown background command %q", args[0])
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

func (a *App) ListPlugins() error {
	manifests, err := plugins.Load(a.Workspace)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(manifests, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) OAuth(args []string) error {
	if len(args) == 0 || args[0] != "pkce" {
		return errors.New("usage: codog oauth pkce")
	}
	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(pkce, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) Sandbox() error {
	status := sandbox.Detect()
	data, _ := json.MarshalIndent(status, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
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

func (a *App) Prompt(ctx context.Context, input string, overrides config.FlagOverrides) error {
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt is empty")
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	runner := runloop.Runner{
		Config:    a.Config,
		Client:    a.Client,
		Tools:     a.Tools,
		Prompter:  a.prompter(),
		Workspace: a.Workspace,
		Out:       a.Out,
		System:    a.systemPrompt(),
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
			Prompter:  a.prompter(),
			Workspace: a.Workspace,
			Out:       a.Out,
			System:    a.systemPrompt(),
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
		fmt.Fprintf(a.Err, "session=%s messages=%d model=%s permission=%s\n", sess.ID, len(sess.Messages), a.Config.Model, a.Config.PermissionMode)
	case "/cost":
		_ = a.ShowCost(config.FlagOverrides{SessionID: sess.ID})
	case "/compact":
		before := len(sess.Messages)
		sess.Messages = runloop.CompactMessages(sess.Messages, a.Config.AutoCompactMessages)
		fmt.Fprintf(a.Err, "compacted request context from %d to %d messages\n", before, len(sess.Messages))
	case "/skills":
		_ = a.ListSkills()
	case "/mcp":
		_ = a.MCP(ctx, nil)
	default:
		if _, ok := slash.Lookup(fields[0]); !ok {
			fmt.Fprintf(a.Err, "unknown slash command: %s\n", fields[0])
		}
	}
	return true
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

func (a *App) prompter() *tools.Prompter {
	return &tools.Prompter{
		Mode:        tools.Permission(a.Config.PermissionMode),
		AllowRules:  append([]string(nil), a.Config.PermissionRules.Allow...),
		DenyRules:   append([]string(nil), a.Config.PermissionRules.Deny...),
		AskRules:    append([]string(nil), a.Config.PermissionRules.Ask...),
		DeniedTools: append([]string(nil), a.Config.PermissionRules.DeniedTools...),
		In:          a.In,
		Err:         a.Err,
	}
}

func (a *App) systemPrompt() string {
	base := "You are Codog, a Go-native coding agent CLI. Be concise, inspect before editing, and use tools when they materially help."
	if len(a.Config.EnabledSkills) == 0 {
		return base
	}
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

func hasFlag(values []string, target string) bool {
	for _, value := range values {
		if value == target {
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
  %s [flags] sessions
  %s [flags] skills
  %s [flags] mcp
  %s [flags] cost --resume latest
  %s mock-server :8089
  %s self-test
  %s roadmap [--json]
  %s capabilities [--json]
  %s background run "command" | background list
  %s agents | marketplace | oauth pkce | sandbox | code-intel symbols
  %s remote serve [addr] | bridge serve | updater check URL
  %s enterprise [--json]
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
`, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe, exe)
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
