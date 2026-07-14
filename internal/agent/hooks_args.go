package agent

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/toolnames"
)

type hookArgOption struct {
	missing string
	set     func(string) error
}

type hooksArgParser struct {
	req         hooksRequest
	positionals []string
	toolSet     bool
	options     map[string]hookArgOption
}

func newHooksArgParser() *hooksArgParser {
	p := &hooksArgParser{
		req: hooksRequest{
			Format: "text", Action: "list", Event: "pre_tool_use",
			Tool: "bash", Input: `{}`, TimeoutMS: 30000,
		},
	}
	p.options = p.hookOptions()
	return p
}

func (p *hooksArgParser) hookOptions() map[string]hookArgOption {
	options := map[string]hookArgOption{}
	p.addStringOption(options, "--output-format", "hooks output format is required", &p.req.Format)
	options["-o"] = options["--output-format"]
	p.addStringOption(options, "--session", "hooks session is required", &p.req.SessionID)
	p.addStringOption(options, "--input", "hooks input is required", &p.req.Input)
	p.addStringOption(options, "--output", "hooks output is required", &p.req.Output)
	p.addStringOption(options, "--notification-type", "hooks notification type is required", &p.req.NotificationType)
	p.addStringOption(options, "--title", "hooks title is required", &p.req.Title)
	p.addStringOption(options, "--agent-id", "hooks agent id is required", &p.req.AgentID)
	p.addStringOption(options, "--agent-type", "hooks agent type is required", &p.req.AgentType)
	p.addStringOption(options, "--agent-transcript-path", "hooks agent transcript path is required", &p.req.TranscriptPath)
	p.addStringOption(options, "--last-assistant-message", "hooks last assistant message is required", &p.req.LastAssistant)
	p.addStringOption(options, "--worktree-id", "hooks worktree id is required", &p.req.WorktreeID)
	p.addStringOption(options, "--worktree-path", "hooks worktree path is required", &p.req.WorktreePath)
	p.addStringOption(options, "--ref", "hooks ref is required", &p.req.Ref)
	p.addStringOption(options, "--old-cwd", "hooks old cwd is required", &p.req.OldCWD)
	p.addStringOption(options, "--new-cwd", "hooks new cwd is required", &p.req.NewCWD)
	p.addStringOption(options, "--path", "hooks file path is required", &p.req.FilePath)
	options["--file-path"] = options["--path"]
	p.addStringOption(options, "--operation", "hooks operation is required", &p.req.Operation)
	p.addStringOption(options, "--memory-type", "hooks memory type is required", &p.req.MemoryType)
	p.addStringOption(options, "--load-reason", "hooks load reason is required", &p.req.LoadReason)
	p.addStringOption(options, "--trigger-file-path", "hooks trigger file path is required", &p.req.TriggerFilePath)
	p.addStringOption(options, "--parent-file-path", "hooks parent file path is required", &p.req.ParentFilePath)
	p.addStringOption(options, "--task-id", "hooks task id is required", &p.req.TaskID)
	p.addStringOption(options, "--task-kind", "hooks task kind is required", &p.req.TaskKind)
	p.addStringOption(options, "--task-status", "hooks task status is required", &p.req.TaskStatus)
	p.addStringOption(options, "--reason", "hooks reason is required", &p.req.Reason)
	options["--tool"] = hookArgOption{missing: "hooks tool is required", set: func(value string) error {
		p.req.Tool = value
		p.toolSet = true
		return nil
	}}
	options["--glob"] = hookArgOption{missing: "hooks glob is required", set: func(value string) error {
		p.req.Globs = append(p.req.Globs, value)
		return nil
	}}
	options["--timeout-ms"] = hookArgOption{missing: "hooks timeout is required", set: p.setTimeout}
	return options
}

func (p *hooksArgParser) addStringOption(options map[string]hookArgOption, name string, missing string, target *string) {
	options[name] = hookArgOption{missing: missing, set: func(value string) error {
		*target = value
		return nil
	}}
}

func (p *hooksArgParser) setTimeout(value string) error {
	timeout, err := strconv.Atoi(value)
	if err != nil || timeout < 0 {
		return errors.New("hooks timeout must be a non-negative integer")
	}
	p.req.TimeoutMS = timeout
	return nil
}

func (p *hooksArgParser) parse(args []string) (hooksRequest, error) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if p.consumeBooleanOption(arg) {
			continue
		}
		handled, err := p.consumeValueOption(args, &index)
		if err != nil {
			return p.req, err
		}
		if !handled {
			p.positionals = append(p.positionals, arg)
		}
	}
	return p.finish()
}

func (p *hooksArgParser) consumeBooleanOption(arg string) bool {
	switch arg {
	case "--json":
		p.req.Format = "json"
	case "--stop-hook-active":
		p.req.StopHookActive = true
	case "--error":
		p.req.IsError = true
	default:
		return false
	}
	return true
}

func (p *hooksArgParser) consumeValueOption(args []string, index *int) (bool, error) {
	arg := args[*index]
	if option, ok := p.options[arg]; ok {
		(*index)++
		if *index >= len(args) {
			return true, errors.New(option.missing)
		}
		return true, option.set(args[*index])
	}
	name, value, inline := strings.Cut(arg, "=")
	option, ok := p.options[name]
	if !inline || !ok || !strings.HasPrefix(name, "--") {
		return false, nil
	}
	return true, option.set(value)
}

func (p *hooksArgParser) finish() (hooksRequest, error) {
	if p.req.Format != "text" && p.req.Format != "json" {
		return p.req, fmt.Errorf("unknown hooks output format %q", p.req.Format)
	}
	if len(p.positionals) == 0 {
		return p.req, nil
	}
	if err := p.applyAction(); err != nil {
		return p.req, err
	}
	p.applyEventDefaults()
	return p.req, nil
}

func (p *hooksArgParser) applyAction() error {
	switch strings.ToLower(p.positionals[0]) {
	case "list", "show":
		p.req.Action = "list"
	case "health", "status", "match", "matches", "diagnose":
		p.req.Action = "health"
		return p.applyEventPositional()
	case "run", "test":
		p.req.Action = "run"
		return p.applyEventPositional()
	case "watch-paths", "watchpaths", "watch":
		return p.applyWatchPathAction()
	default:
		return unknownActionError{
			Command: "hooks", Action: p.positionals[0],
			Expected:    append([]string(nil), hooksActionCandidates...),
			Suggestions: toolnames.Suggestions(p.positionals[0], hooksActionCandidates, 4),
			Usage:       hooksUsage,
		}
	}
	return nil
}

func (p *hooksArgParser) applyEventPositional() error {
	if len(p.positionals) < 2 {
		return nil
	}
	event, err := normalizeHookEvent(p.positionals[1])
	if err != nil {
		return err
	}
	p.req.Event = event
	return nil
}

func (p *hooksArgParser) applyWatchPathAction() error {
	p.req.Action = "watch-paths"
	p.req.WatchAction = "list"
	if len(p.positionals) > 1 {
		switch strings.ToLower(strings.TrimSpace(p.positionals[1])) {
		case "", "list", "show":
		case "check", "scan":
			p.req.WatchAction = "check"
		default:
			return unknownActionError{
				Command: "hooks watch-paths", Action: p.positionals[1],
				Expected:    append([]string(nil), hooksWatchPathsActionCandidates...),
				Suggestions: toolnames.Suggestions(p.positionals[1], hooksWatchPathsActionCandidates, 4),
				Usage:       "codog hooks watch-paths [list|show|check|scan] [SESSION_ID] [--json|--output-format text|json]",
			}
		}
	}
	if len(p.positionals) > 2 {
		p.req.SessionID = p.positionals[2]
	}
	return nil
}

func (p *hooksArgParser) applyEventDefaults() {
	if !p.toolSet && hookEventHasNoDefaultTool(p.req.Event) {
		p.req.Tool = ""
	}
	if p.req.Event == "notification" && strings.TrimSpace(p.req.NotificationType) == "" && strings.TrimSpace(p.req.Tool) != "" {
		p.req.NotificationType = p.req.Tool
	}
	if (p.req.Event == "subagent_start" || p.req.Event == "subagent_stop") && strings.TrimSpace(p.req.AgentType) == "" && strings.TrimSpace(p.req.Tool) != "" {
		p.req.AgentType = p.req.Tool
	}
	if p.req.Event == "file_changed" && strings.TrimSpace(p.req.Operation) == "" && strings.TrimSpace(p.req.Tool) != "" {
		p.req.Operation = p.req.Tool
	}
	if p.req.Event == "instructions_loaded" && strings.TrimSpace(p.req.LoadReason) == "" && strings.TrimSpace(p.req.Tool) != "" {
		p.req.LoadReason = p.req.Tool
	}
}

func hookEventHasNoDefaultTool(event string) bool {
	switch event {
	case "user_prompt_submit", "session_start", "stop", "pre_compact", "post_compact",
		"notification", "subagent_start", "subagent_stop", "file_changed", "instructions_loaded":
		return true
	default:
		return false
	}
}
