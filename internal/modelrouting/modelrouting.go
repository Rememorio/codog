package modelrouting

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderXAI       = "xai"
	ProviderDashScope = "dashscope"

	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	DefaultOpenAIBaseURL    = "https://api.openai.com/v1"
	DefaultXAIBaseURL       = "https://api.x.ai/v1"
	DefaultDashScopeBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
)

type ModelAlias struct {
	Name  string
	Model string
}

type TokenLimit struct {
	MaxOutputTokens     int
	ContextWindowTokens int
}

// BaseURLDiagnostic is a redaction-safe provider endpoint validation result.
type BaseURLDiagnostic struct {
	Provider  string `json:"provider,omitempty"`
	Env       string `json:"base_url_env,omitempty"`
	Source    string `json:"base_url_source,omitempty"`
	URL       string `json:"base_url,omitempty"`
	Valid     bool   `json:"base_url_valid"`
	Scheme    string `json:"base_url_scheme,omitempty"`
	Host      string `json:"base_url_host,omitempty"`
	Local     bool   `json:"local_base_url,omitempty"`
	ErrorKind string `json:"base_url_error_kind,omitempty"`
	Error     string `json:"base_url_error,omitempty"`
}

var builtInAliases = []ModelAlias{
	{Name: "opus", Model: "claude-opus-4-7"},
	{Name: "sonnet", Model: "claude-sonnet-4-6"},
	{Name: "haiku", Model: "claude-haiku-4-5-20251213"},
	{Name: "grok", Model: "grok-3"},
	{Name: "grok-3", Model: "grok-3"},
	{Name: "grok-mini", Model: "grok-3-mini"},
	{Name: "grok-3-mini", Model: "grok-3-mini"},
	{Name: "grok-2", Model: "grok-2"},
	{Name: "kimi", Model: "kimi-k2.5"},
}

func BuiltInAliases() []ModelAlias {
	out := make([]ModelAlias, len(builtInAliases))
	copy(out, builtInAliases)
	return out
}

func ResolveAlias(model string) string {
	trimmed := strings.TrimSpace(model)
	for _, alias := range builtInAliases {
		if strings.EqualFold(trimmed, alias.Name) {
			return alias.Model
		}
	}
	return trimmed
}

func ProviderForModel(model string) string {
	canonical := strings.ToLower(ResolveAlias(model))
	switch {
	case canonical == "":
		return ProviderAnthropic
	case strings.HasPrefix(canonical, "xai/"), strings.HasPrefix(canonical, "grok/"), strings.HasPrefix(canonical, "grok"):
		return ProviderXAI
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
	return provider == ProviderOpenAI || provider == ProviderXAI || provider == ProviderDashScope
}

func LooksLikeLocalOpenAICompatibleModel(model string) bool {
	canonical := strings.ToLower(ResolveAlias(model))
	if canonical == "" || IsOpenAICompatibleModel(canonical) {
		return false
	}
	return strings.Contains(canonical, ":") || strings.Contains(canonical, ".")
}

func WireModelForBaseURL(model string, baseURL string) string {
	trimmed := ResolveAlias(model)
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
	case "local", "xai", "grok", "qwen", "kimi":
		return trimmed[pos+1:]
	default:
		return trimmed
	}
}

func TokenLimitForModel(model string) (TokenLimit, bool) {
	canonical := ResolveAlias(model)
	base := canonical
	if slash := strings.LastIndex(base, "/"); slash >= 0 {
		base = base[slash+1:]
	}
	switch base {
	case "claude-opus-4-7", "claude-opus-4-6":
		return TokenLimit{MaxOutputTokens: 32000, ContextWindowTokens: 200000}, true
	case "claude-sonnet-4-6", "claude-sonnet-4-5", "claude-haiku-4-5-20251213":
		return TokenLimit{MaxOutputTokens: 64000, ContextWindowTokens: 200000}, true
	case "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano":
		return TokenLimit{MaxOutputTokens: 32768, ContextWindowTokens: 1047576}, true
	case "gpt-5.4":
		return TokenLimit{MaxOutputTokens: 128000, ContextWindowTokens: 1000000}, true
	case "gpt-5.4-mini", "gpt-5.4-nano":
		return TokenLimit{MaxOutputTokens: 128000, ContextWindowTokens: 400000}, true
	case "grok-3", "grok-3-mini":
		return TokenLimit{MaxOutputTokens: 64000, ContextWindowTokens: 131072}, true
	case "kimi-k2.5", "kimi-k1.5":
		return TokenLimit{MaxOutputTokens: 16384, ContextWindowTokens: 256000}, true
	case "qwen-max", "qwen-plus":
		return TokenLimit{MaxOutputTokens: 8192, ContextWindowTokens: 131072}, true
	default:
		return TokenLimit{}, false
	}
}

func ModelRejectsIsErrorField(model string) bool {
	canonical := strings.ToLower(ResolveAlias(model))
	if slash := strings.LastIndex(canonical, "/"); slash >= 0 {
		canonical = canonical[slash+1:]
	}
	return canonical == "kimi" || strings.HasPrefix(canonical, "kimi-")
}

func IsReasoningModel(model string) bool {
	canonical := strings.ToLower(ResolveAlias(model))
	if slash := strings.LastIndex(canonical, "/"); slash >= 0 {
		canonical = canonical[slash+1:]
	}
	return strings.HasPrefix(canonical, "o1") ||
		strings.HasPrefix(canonical, "o3") ||
		strings.HasPrefix(canonical, "o4") ||
		canonical == "grok-3-mini" ||
		strings.HasPrefix(canonical, "qwen-qwq") ||
		strings.HasPrefix(canonical, "qwq") ||
		strings.Contains(canonical, "thinking")
}

func RequiresReasoningContentHistory(model string) bool {
	canonical := strings.ToLower(ResolveAlias(model))
	if slash := strings.LastIndex(canonical, "/"); slash >= 0 {
		canonical = canonical[slash+1:]
	}
	return strings.HasPrefix(canonical, "deepseek-v4")
}

func UsesMaxCompletionTokens(model string) bool {
	canonical := strings.ToLower(ResolveAlias(model))
	if slash := strings.LastIndex(canonical, "/"); slash >= 0 {
		canonical = canonical[slash+1:]
	}
	return strings.HasPrefix(canonical, "gpt-5")
}

func shouldStripOpenAIPrefix(baseURL string) bool {
	normalized := normalizeBaseURL(baseURL)
	if normalized == "" || strings.EqualFold(normalized, normalizeBaseURL(DefaultOpenAIBaseURL)) {
		return true
	}
	return IsLocalBaseURL(baseURL)
}

func normalizeBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	trimmed = strings.TrimRight(strings.TrimSuffix(trimmed, "/chat/completions"), "/")
	return trimmed
}

// DiagnoseBaseURL validates a provider base URL without exposing credentials.
func DiagnoseBaseURL(provider string, envName string, source string, raw string) BaseURLDiagnostic {
	trimmed := strings.TrimSpace(raw)
	out := BaseURLDiagnostic{
		Provider: strings.TrimSpace(provider),
		Env:      strings.TrimSpace(envName),
		Source:   strings.TrimSpace(source),
		URL:      RedactURL(trimmed),
	}
	parsed, err := url.Parse(trimmed)
	if trimmed == "" {
		out.ErrorKind = "empty_base_url"
		out.Error = "base URL is empty"
		return out
	}
	if err != nil {
		out.ErrorKind = "invalid_base_url"
		out.Error = err.Error()
		return out
	}
	out.Scheme = strings.ToLower(parsed.Scheme)
	out.Host = parsed.Hostname()
	out.Local = IsLocalBaseURL(trimmed)
	switch {
	case out.Scheme == "":
		out.ErrorKind = "missing_scheme"
		out.Error = "base URL must include http or https scheme"
	case out.Scheme != "http" && out.Scheme != "https":
		out.ErrorKind = "unsupported_scheme"
		out.Error = "base URL must use http or https"
	case out.Host == "":
		out.ErrorKind = "missing_host"
		out.Error = "base URL must include a host"
	case !validURLPort(parsed.Port()):
		out.ErrorKind = "invalid_port"
		out.Error = "base URL port must be between 1 and 65535"
	default:
		out.Valid = true
	}
	return out
}

func validURLPort(port string) bool {
	if port == "" {
		return true
	}
	value, err := strconv.Atoi(port)
	return err == nil && value > 0 && value <= 65535
}

// RedactURL returns raw with URL userinfo replaced when it can be parsed.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = url.UserPassword("redacted", "redacted")
	return parsed.String()
}

// IsLocalBaseURL reports whether baseURL points at loopback or private-network host.
func IsLocalBaseURL(baseURL string) bool {
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
