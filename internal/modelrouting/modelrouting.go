package modelrouting

import (
	"net"
	"strings"
)

const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderDashScope = "dashscope"

	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	DefaultOpenAIBaseURL    = "https://api.openai.com/v1"
	DefaultDashScopeBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
)

func ProviderForModel(model string) string {
	canonical := strings.ToLower(strings.TrimSpace(model))
	switch {
	case canonical == "":
		return ProviderAnthropic
	case strings.HasPrefix(canonical, "qwen/"), strings.HasPrefix(canonical, "qwen-"):
		return ProviderDashScope
	case strings.HasPrefix(canonical, "kimi/"), strings.HasPrefix(canonical, "kimi-"):
		return ProviderDashScope
	case strings.HasPrefix(canonical, "openai/"), strings.HasPrefix(canonical, "local/"), strings.HasPrefix(canonical, "gpt-"):
		return ProviderOpenAI
	default:
		return ProviderAnthropic
	}
}

func IsOpenAICompatibleModel(model string) bool {
	provider := ProviderForModel(model)
	return provider == ProviderOpenAI || provider == ProviderDashScope
}

func WireModelForBaseURL(model string, baseURL string) string {
	trimmed := strings.TrimSpace(model)
	pos := strings.Index(trimmed, "/")
	if pos < 0 {
		return trimmed
	}

	prefix := strings.ToLower(trimmed[:pos])
	switch prefix {
	case "openai":
		if shouldStripOpenAIPrefix(baseURL) {
			return trimmed[pos+1:]
		}
		return trimmed
	case "local", "qwen", "kimi":
		return trimmed[pos+1:]
	default:
		return trimmed
	}
}

func shouldStripOpenAIPrefix(baseURL string) bool {
	normalized := normalizeBaseURL(baseURL)
	if normalized == "" || strings.EqualFold(normalized, normalizeBaseURL(DefaultOpenAIBaseURL)) {
		return true
	}
	return isLocalBaseURL(baseURL)
}

func normalizeBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	trimmed = strings.TrimRight(strings.TrimSuffix(trimmed, "/chat/completions"), "/")
	return trimmed
}

func isLocalBaseURL(baseURL string) bool {
	host := urlHost(baseURL)
	if strings.EqualFold(host, "localhost") || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	return false
}

func urlHost(raw string) string {
	afterScheme := raw
	if parts := strings.SplitN(afterScheme, "://", 2); len(parts) == 2 {
		afterScheme = parts[1]
	}
	authority := strings.FieldsFunc(afterScheme, func(r rune) bool {
		return r == '/' || r == '?' || r == '#'
	})
	if len(authority) == 0 {
		return ""
	}
	hostPort := authority[0]
	if parts := strings.Split(hostPort, "@"); len(parts) > 1 {
		hostPort = parts[len(parts)-1]
	}
	if strings.HasPrefix(hostPort, "[") {
		end := strings.Index(hostPort, "]")
		if end >= 0 {
			return strings.TrimPrefix(hostPort[:end], "[")
		}
	}
	if host, _, err := net.SplitHostPort(hostPort); err == nil {
		return host
	}
	if colon := strings.Index(hostPort, ":"); colon >= 0 {
		return hostPort[:colon]
	}
	return hostPort
}
