package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/tools"
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
	default:
		if command != "" {
			return fmt.Errorf("unknown command %q", command)
		}
		return app.REPL(ctx, overrides)
	}
}

func (a *App) Prompt(ctx context.Context, input string, overrides config.FlagOverrides) error {
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt is empty")
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	runner := Runner{
		Config:   a.Config,
		Client:   a.Client,
		Tools:    a.Tools,
		Prompter: &tools.Prompter{Mode: tools.Permission(a.Config.PermissionMode), In: a.In, Err: a.Err},
		Out:      a.Out,
	}
	messages, err := runner.Run(ctx, sess.Messages, input)
	if err != nil {
		return err
	}
	for _, msg := range messages[len(sess.Messages):] {
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
			fmt.Fprintln(a.Err, "Commands: /help, /exit. Ask normally to run an agent turn.")
			continue
		}
		runner := Runner{
			Config:   a.Config,
			Client:   a.Client,
			Tools:    a.Tools,
			Prompter: &tools.Prompter{Mode: tools.Permission(a.Config.PermissionMode), In: a.In, Err: a.Err},
			Out:      a.Out,
		}
		next, err := runner.Run(ctx, sess.Messages, line)
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			continue
		}
		for _, msg := range next[len(sess.Messages):] {
			if err := a.Sessions.Append(sess.ID, msg); err != nil {
				return err
			}
		}
		sess.Messages = next
	}
	return scanner.Err()
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

type Runner struct {
	Config   config.Config
	Client   *anthropic.Client
	Tools    *tools.Registry
	Prompter *tools.Prompter
	Out      io.Writer
}

func (r Runner) Run(ctx context.Context, previous []anthropic.Message, input string) ([]anthropic.Message, error) {
	messages := append([]anthropic.Message(nil), previous...)
	messages = append(messages, anthropic.TextMessage("user", input))
	system := "You are Codog, a Go-native coding agent CLI. Be concise, inspect before editing, and use tools when they materially help."

	for turn := 0; turn < r.Config.MaxTurns; turn++ {
		req := anthropic.Request{
			Model:     r.Config.Model,
			MaxTokens: r.Config.MaxTokens,
			System:    system,
			Messages:  messages,
			Tools:     r.Tools.Definitions(),
		}
		assistant, err := r.Client.Stream(ctx, req, func(delta string) {
			if r.Out != nil {
				fmt.Fprint(r.Out, delta)
			}
		})
		if err != nil {
			return nil, err
		}
		assistantMsg := anthropic.Message{Role: "assistant", Content: assistant.Blocks}
		messages = append(messages, assistantMsg)

		toolUses := toolUseBlocks(assistant.Blocks)
		if len(toolUses) == 0 {
			return messages, nil
		}
		for _, block := range toolUses {
			output, err := r.Tools.Execute(ctx, block.Name, block.Input, r.Prompter)
			isErr := false
			if err != nil {
				output = err.Error()
				isErr = true
			}
			messages = append(messages, anthropic.ToolResultMessage(block.ID, output, isErr))
		}
	}
	return messages, errors.New("conversation exceeded max turns")
}

func toolUseBlocks(blocks []anthropic.ContentBlock) []anthropic.ContentBlock {
	var result []anthropic.ContentBlock
	for _, block := range blocks {
		if block.Type == "tool_use" {
			result = append(result, block)
		}
	}
	return result
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
  %s [flags] sessions
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
`, exe, exe, exe, exe, exe)
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
