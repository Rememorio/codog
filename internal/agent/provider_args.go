package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/modelrouting"
)

func parseProfileRequest(args []string) (profileRequest, error) {
	req, rest, err := parseProfileOptions(args)
	if err != nil {
		return req, err
	}
	req.Format, err = normalizeOutputFormat("profile", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	return applyProfilePositionals(req, rest)
}

func parseProfileOptions(args []string) (profileRequest, []string, error) {
	req := profileRequest{Action: "show", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			value, next, err := profileOptionValue(args, index, arg, false)
			if err != nil {
				return req, nil, err
			}
			req.Format, index = value, next
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			value, next, err := profileOptionValue(args, index, arg, true)
			if err != nil {
				return req, nil, err
			}
			req.Target, index = value, next
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			value, next, err := profileOptionValue(args, index, arg, true)
			if err != nil {
				return req, nil, err
			}
			req.Path, index = value, next
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, nil, unknownOptionError{Command: "profile", Option: arg, Usage: profileUsage}
		default:
			rest = append(rest, arg)
		}
	}
	return req, rest, nil
}

func profileOptionValue(args []string, index int, flag string, rejectFormatFlag bool) (string, int, error) {
	next := index + 1
	if next >= len(args) || rejectFormatFlag && isOutputFormatFlag(args[next]) {
		return "", index, missingFlagValueError{Command: "profile", Flag: flag, Usage: profileUsage}
	}
	return args[next], next, nil
}

func applyProfilePositionals(req profileRequest, rest []string) (profileRequest, error) {
	if len(rest) == 0 {
		return req, nil
	}
	action := strings.ToLower(strings.TrimSpace(rest[0]))
	switch action {
	case "status", "show", "current":
		return applyProfileShow(req, action, rest[1:])
	case "list":
		req.Action = "list"
		return req, rejectProfileExtras("profile list", rest[1:])
	case "set", "switch", "use":
		return applyProfileSet(req, action, rest[1:])
	case "clear", "reset", "unset":
		req.Action = "clear"
		return req, rejectProfileExtras("profile "+action, rest[1:])
	default:
		req.Action = "set"
		req.Name = rest[0]
		return req, rejectProfileExtras("profile", rest[1:])
	}
}

func applyProfileShow(req profileRequest, action string, rest []string) (profileRequest, error) {
	req.Action = "show"
	if len(rest) > 0 {
		req.Name = rest[0]
		rest = rest[1:]
	}
	return req, rejectProfileExtras("profile "+action, rest)
}

func applyProfileSet(req profileRequest, action string, rest []string) (profileRequest, error) {
	req.Action = "set"
	if len(rest) == 0 {
		return req, requiredArgumentError{Command: "profile set", Argument: "NAME", Usage: profileUsage}
	}
	req.Name = rest[0]
	return req, rejectProfileExtras("profile "+action, rest[1:])
}

func rejectProfileExtras(command string, extras []string) error {
	if len(extras) == 0 {
		return nil
	}
	return unexpectedExtraArgsError{Command: command, Args: extras, Usage: profileUsage}
}

func parseProviderRequest(args []string) (providerCommandRequest, error) {
	req, positionals, err := parseProviderOptions(args)
	if err != nil {
		return req, err
	}
	req, positionals = applyProviderAction(req, positionals)
	if err := validateProviderPositionals(&req, positionals); err != nil {
		return req, err
	}
	if req.Format != "text" && req.Format != "json" {
		return req, outputFormatError{Command: "providers", Value: req.Format, Expected: []string{"text", "json"}}
	}
	return req, nil
}

func parseProviderOptions(args []string) (providerCommandRequest, []string, error) {
	req := providerCommandRequest{Action: "status", Format: "text"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			value, next, err := providerOptionValue(args, index, "providers output format is required", false)
			if err != nil {
				return req, nil, err
			}
			req.Format, index = value, next
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--base-url":
			value, next, err := providerOptionValue(args, index, "provider base URL is required", true)
			if err != nil {
				return req, nil, err
			}
			req.BaseURL, index = value, next
		case strings.HasPrefix(arg, "--base-url="):
			req.BaseURL = strings.TrimPrefix(arg, "--base-url=")
		case arg == "--model":
			value, next, err := providerOptionValue(args, index, "provider model is required", true)
			if err != nil {
				return req, nil, err
			}
			req.Model, index = value, next
		case strings.HasPrefix(arg, "--model="):
			req.Model = strings.TrimPrefix(arg, "--model=")
		case arg == "--target":
			value, next, err := providerOptionValue(args, index, "provider config target is required", true)
			if err != nil {
				return req, nil, err
			}
			req.Target, index = value, next
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			value, next, err := providerOptionValue(args, index, "provider config path is required", true)
			if err != nil {
				return req, nil, err
			}
			req.Path, index = value, next
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			positionals = append(positionals, arg)
		}
	}
	return req, positionals, nil
}

func providerOptionValue(args []string, index int, missing string, requireNonEmpty bool) (string, int, error) {
	next := index + 1
	if next >= len(args) || requireNonEmpty && strings.TrimSpace(args[next]) == "" {
		return "", index, errors.New(missing)
	}
	return args[next], next, nil
}

func applyProviderAction(req providerCommandRequest, positionals []string) (providerCommandRequest, []string) {
	if len(positionals) == 0 {
		return req, positionals
	}
	action := strings.ToLower(positionals[0])
	switch action {
	case "status", "list", "show", "set":
		req.Action = action
	default:
		req.Name = positionals[0]
		req.Action = "show"
	}
	return req, positionals[1:]
}

func validateProviderPositionals(req *providerCommandRequest, positionals []string) error {
	switch req.Action {
	case "status", "list":
		return rejectProviderExtras("providers "+req.Action, positionals)
	case "show":
		return applyProviderShowPositionals(req, positionals)
	case "set":
		return applyProviderSetPositionals(req, positionals)
	default:
		return unexpectedExtraArgsError{Command: "providers", Args: []string{req.Action}, Usage: providersUsage}
	}
}

const providersUsage = "codog providers [status|list|show NAME|set NAME [BASE_URL] [MODEL]] [--json|--output-format text|json]"

func applyProviderShowPositionals(req *providerCommandRequest, positionals []string) error {
	if req.Name == "" {
		if len(positionals) == 0 {
			return requiredArgumentError{Command: "providers show", Argument: "NAME", Usage: "codog providers show NAME [--json|--output-format text|json]"}
		}
		req.Name, positionals = positionals[0], positionals[1:]
	}
	return rejectProviderExtras("providers show", positionals)
}

func applyProviderSetPositionals(req *providerCommandRequest, positionals []string) error {
	if len(positionals) == 0 {
		return requiredArgumentError{Command: "providers set", Argument: "provider", Usage: "codog providers set anthropic|openai|xai|dashscope|custom [BASE_URL] [MODEL] [--target user|project|local|--path PATH]"}
	}
	req.Name = positionals[0]
	if len(positionals) > 1 && req.BaseURL == "" {
		req.BaseURL = positionals[1]
	}
	if len(positionals) > 2 && req.Model == "" {
		req.Model = positionals[2]
	}
	if len(positionals) > 3 {
		return unexpectedExtraArgsError{Command: "providers set", Args: append([]string(nil), positionals[3:]...), Usage: "codog providers set anthropic|openai|xai|dashscope|custom [BASE_URL] [MODEL] [--target user|project|local|--path PATH]"}
	}
	return nil
}

func rejectProviderExtras(command string, positionals []string) error {
	if len(positionals) == 0 {
		return nil
	}
	return unexpectedExtraArgsError{Command: command, Args: append([]string(nil), positionals...), Usage: providersUsage}
}

type resolvedProviderConfig struct {
	name    string
	baseURL string
	model   string
}

func resolveProviderConfig(req providerCommandRequest) (resolvedProviderConfig, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		return resolvedProviderConfig{}, errors.New("provider name is required")
	}
	resolved := resolvedProviderConfig{name: name, baseURL: strings.TrimSpace(req.BaseURL), model: strings.TrimSpace(req.Model)}
	switch name {
	case "anthropic", "default":
		resolved.name = "anthropic"
		resolved.baseURL = firstNonEmpty(resolved.baseURL, config.DefaultBaseURL)
		resolved.model = firstNonEmpty(resolved.model, config.DefaultModel)
	case "custom", "compatible", "anthropic-compatible":
		resolved.name = "custom"
		if resolved.baseURL == "" {
			return resolvedProviderConfig{}, errors.New("custom provider requires --base-url or a BASE_URL positional argument")
		}
	case "openai", "openai-compatible":
		resolved = withProviderDefaults(resolved, "openai", modelrouting.DefaultOpenAIBaseURL, "openai/gpt-4o-mini")
	case "xai", "grok":
		resolved = withProviderDefaults(resolved, "xai", modelrouting.DefaultXAIBaseURL, "grok")
	case "dashscope", "qwen", "kimi":
		model := "qwen-plus"
		if name == "kimi" {
			model = "kimi"
		}
		resolved = withProviderDefaults(resolved, "dashscope", modelrouting.DefaultDashScopeBaseURL, model)
	default:
		if resolved.baseURL == "" {
			return resolvedProviderConfig{}, unknownProviderSetError(req.Name)
		}
	}
	return resolved, nil
}

func withProviderDefaults(resolved resolvedProviderConfig, name string, baseURL string, model string) resolvedProviderConfig {
	resolved.name = name
	resolved.baseURL = firstNonEmpty(resolved.baseURL, baseURL)
	resolved.model = firstNonEmpty(resolved.model, model)
	return resolved
}

func unknownProviderSetError(name string) error {
	return invalidFlagValueError{
		Flag: "provider", Value: name,
		Message: fmt.Sprintf("unknown provider %q; use anthropic, openai, xai, dashscope, or custom --base-url URL", name),
		Hint:    unknownProviderNameHint(name, "set"),
		Usage:   "codog providers set anthropic|openai|xai|dashscope|custom [BASE_URL] [MODEL] [--target user|project|local|--path PATH]",
	}
}
