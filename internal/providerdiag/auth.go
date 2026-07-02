package providerdiag

import (
	"os"
	"strings"

	"github.com/Rememorio/codog/internal/modelrouting"
)

// AuthOptions contains the redaction-safe inputs needed to diagnose provider auth.
type AuthOptions struct {
	Model                 string
	RuntimeProvider       string
	RuntimeProviderSource string
	BaseURL               string
	APIKey                string
	AuthToken             string
}

// AuthDiagnostic reports provider auth preflight state without exposing secrets.
type AuthDiagnostic struct {
	SelectedProvider                 string   `json:"selected_provider"`
	RuntimeProviderSource            string   `json:"runtime_provider_source,omitempty"`
	RequiredAPIKeyEnv                string   `json:"required_api_key_env,omitempty"`
	RequiredAuthEnvs                 []string `json:"required_auth_envs,omitempty"`
	SelectedProviderAPIKeyPresent    bool     `json:"selected_provider_api_key_present"`
	SelectedProviderAuthTokenPresent bool     `json:"selected_provider_auth_token_present"`
	SelectedProviderAuthPresent      bool     `json:"selected_provider_auth_present"`
	LocalBaseURL                     bool     `json:"local_base_url,omitempty"`
	AuthOptional                     bool     `json:"auth_optional,omitempty"`
	AnthropicAPIKeyPresent           bool     `json:"anthropic_api_key_present"`
	AnthropicAuthTokenPresent        bool     `json:"anthropic_auth_token_present"`
	OpenAIAPIKeyPresent              bool     `json:"openai_api_key_present"`
	XAIAPIKeyPresent                 bool     `json:"xai_api_key_present"`
	DashScopeAPIKeyPresent           bool     `json:"dashscope_api_key_present"`
	OllamaHostPresent                bool     `json:"ollama_host_present"`
	EffectiveAuthSource              string   `json:"effective_auth_source"`
	HeadersSent                      []string `json:"headers_sent,omitempty"`
	BothAnthropicAuthEnvVarsPresent  bool     `json:"both_anthropic_auth_env_vars_present,omitempty"`
	SelectedProviderBothAuthPresent  bool     `json:"selected_provider_both_auth_present,omitempty"`
	Warning                          string   `json:"auth_warning,omitempty"`
	Hint                             string   `json:"auth_hint,omitempty"`
}

// AnalyzeAuth returns the effective auth state for the selected runtime provider.
func AnalyzeAuth(opts AuthOptions) AuthDiagnostic {
	provider := strings.TrimSpace(opts.RuntimeProvider)
	if provider == "" {
		provider = modelrouting.ProviderForModel(opts.Model)
	}
	requiredKeyEnv, requiredAuthEnvs, authOptional := AuthRequirements(provider, opts.RuntimeProviderSource, opts.BaseURL)
	apiKeyConfigured := strings.TrimSpace(opts.APIKey) != ""
	authTokenConfigured := strings.TrimSpace(opts.AuthToken) != ""
	authSource := effectiveAuthSource(provider, opts.RuntimeProviderSource, opts.BaseURL, apiKeyConfigured, authTokenConfigured, authOptional)
	headers := authHeaders(provider, apiKeyConfigured, authTokenConfigured, authOptional)
	warning, hint := authWarning(provider, apiKeyConfigured, authTokenConfigured)
	return AuthDiagnostic{
		SelectedProvider:                 provider,
		RuntimeProviderSource:            strings.TrimSpace(opts.RuntimeProviderSource),
		RequiredAPIKeyEnv:                requiredKeyEnv,
		RequiredAuthEnvs:                 append([]string(nil), requiredAuthEnvs...),
		SelectedProviderAPIKeyPresent:    apiKeyConfigured,
		SelectedProviderAuthTokenPresent: authTokenConfigured,
		SelectedProviderAuthPresent:      apiKeyConfigured || authTokenConfigured || authOptional,
		LocalBaseURL:                     modelrouting.IsLocalBaseURL(opts.BaseURL),
		AuthOptional:                     authOptional,
		AnthropicAPIKeyPresent:           strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "",
		AnthropicAuthTokenPresent:        strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")) != "",
		OpenAIAPIKeyPresent:              strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != "",
		XAIAPIKeyPresent:                 strings.TrimSpace(os.Getenv("XAI_API_KEY")) != "",
		DashScopeAPIKeyPresent:           strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")) != "",
		OllamaHostPresent:                strings.TrimSpace(os.Getenv("OLLAMA_HOST")) != "",
		EffectiveAuthSource:              authSource,
		HeadersSent:                      headers,
		BothAnthropicAuthEnvVarsPresent:  strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" && strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")) != "",
		SelectedProviderBothAuthPresent:  provider == modelrouting.ProviderAnthropic && apiKeyConfigured && authTokenConfigured,
		Warning:                          warning,
		Hint:                             hint,
	}
}

// AuthRequirements returns the auth env contract for a selected provider.
func AuthRequirements(provider string, source string, baseURL string) (string, []string, bool) {
	if provider == modelrouting.ProviderOpenAI {
		if strings.EqualFold(strings.TrimSpace(source), "OLLAMA_HOST") || modelrouting.IsLocalBaseURL(baseURL) {
			return "", nil, true
		}
	}
	switch provider {
	case modelrouting.ProviderOpenAI:
		return "OPENAI_API_KEY", []string{"OPENAI_API_KEY"}, false
	case modelrouting.ProviderXAI:
		return "XAI_API_KEY", []string{"XAI_API_KEY"}, false
	case modelrouting.ProviderDashScope:
		return "DASHSCOPE_API_KEY", []string{"DASHSCOPE_API_KEY"}, false
	default:
		return "ANTHROPIC_API_KEY", []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}, false
	}
}

// Data returns the legacy doctor data map plus newer auth-source fields.
func (d AuthDiagnostic) Data() map[string]any {
	return map[string]any{
		"selected_provider":                    d.SelectedProvider,
		"runtime_provider_source":              d.RuntimeProviderSource,
		"required_api_key_env":                 d.RequiredAPIKeyEnv,
		"required_auth_envs":                   append([]string(nil), d.RequiredAuthEnvs...),
		"selected_provider_api_key_present":    d.SelectedProviderAPIKeyPresent,
		"selected_provider_auth_token_present": d.SelectedProviderAuthTokenPresent,
		"selected_provider_auth_present":       d.SelectedProviderAuthPresent,
		"local_base_url":                       d.LocalBaseURL,
		"auth_optional":                        d.AuthOptional,
		"anthropic_api_key_present":            d.AnthropicAPIKeyPresent,
		"anthropic_auth_token_present":         d.AnthropicAuthTokenPresent,
		"openai_api_key_present":               d.OpenAIAPIKeyPresent,
		"xai_api_key_present":                  d.XAIAPIKeyPresent,
		"dashscope_api_key_present":            d.DashScopeAPIKeyPresent,
		"ollama_host_present":                  d.OllamaHostPresent,
		"effective_auth_source":                d.EffectiveAuthSource,
		"headers_sent":                         append([]string(nil), d.HeadersSent...),
		"both_anthropic_auth_env_vars_present": d.BothAnthropicAuthEnvVarsPresent,
		"selected_provider_both_auth_present":  d.SelectedProviderBothAuthPresent,
		"auth_warning":                         d.Warning,
		"auth_hint":                            d.Hint,
	}
}

func effectiveAuthSource(provider string, source string, baseURL string, apiKeyConfigured bool, authTokenConfigured bool, authOptional bool) string {
	if authOptional {
		source = strings.TrimSpace(source)
		if source != "" {
			return source
		}
		if modelrouting.IsLocalBaseURL(baseURL) {
			return "local_base_url"
		}
		return "provider_does_not_require_auth"
	}
	if provider == modelrouting.ProviderAnthropic && apiKeyConfigured && authTokenConfigured {
		return "api_key_and_bearer"
	}
	if apiKeyConfigured {
		return "api_key"
	}
	if authTokenConfigured {
		if provider == modelrouting.ProviderAnthropic {
			return "bearer_token"
		}
		return "auth_token"
	}
	return "none"
}

func authHeaders(provider string, apiKeyConfigured bool, authTokenConfigured bool, authOptional bool) []string {
	if authOptional {
		return nil
	}
	if provider == modelrouting.ProviderAnthropic {
		headers := []string{}
		if apiKeyConfigured {
			headers = append(headers, "x-api-key")
		}
		if authTokenConfigured {
			headers = append(headers, "authorization_bearer")
		}
		return headers
	}
	if apiKeyConfigured || authTokenConfigured {
		return []string{"authorization_bearer"}
	}
	return nil
}

func authWarning(provider string, apiKeyConfigured bool, authTokenConfigured bool) (string, string) {
	if provider != modelrouting.ProviderAnthropic || !apiKeyConfigured || !authTokenConfigured {
		return "", ""
	}
	return "both ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN are configured; Anthropic requests will send both x-api-key and bearer headers",
		"Unset the stale or incorrect Anthropic credential. Put sk-ant-* API keys in ANTHROPIC_API_KEY; use ANTHROPIC_AUTH_TOKEN only for bearer/OAuth-style tokens."
}
