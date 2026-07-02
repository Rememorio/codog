// Package remote models remote-session environment and upstream proxy bootstrap
// state for Codog remote clients.
package remote

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// DefaultBaseURL is the Anthropic-compatible remote endpoint used when no
	// environment override is present.
	DefaultBaseURL = "https://api.anthropic.com"

	// DefaultTokenPath is the container path used by Claude Code remote sessions.
	DefaultTokenPath = "/run/ccr/session_token"

	// DefaultSystemCAPath is the system CA bundle path used when bootstrapping a
	// remote upstream proxy.
	DefaultSystemCAPath = "/etc/ssl/certs/ca-certificates.crt"

	defaultProxyEndpoint = "/v1/code/upstreamproxy/ws"
)

var upstreamProxyEnvKeys = []string{
	"HTTPS_PROXY",
	"https_proxy",
	"NO_PROXY",
	"no_proxy",
	"SSL_CERT_FILE",
	"NODE_EXTRA_CA_CERTS",
	"REQUESTS_CA_BUNDLE",
	"CURL_CA_BUNDLE",
}

var noProxyHosts = []string{
	"localhost",
	"127.0.0.1",
	"::1",
	"169.254.0.0/16",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"anthropic.com",
	".anthropic.com",
	"*.anthropic.com",
	"github.com",
	"api.github.com",
	"*.github.com",
	"*.githubusercontent.com",
	"registry.npmjs.org",
	"index.crates.io",
	"pypi.org",
	"files.pythonhosted.org",
	"proxy.golang.org",
}

// SessionContext describes whether the current process is running inside a
// remote Codog/Claude Code session.
type SessionContext struct {
	Enabled   bool   `json:"enabled"`
	SessionID string `json:"session_id,omitempty"`
	BaseURL   string `json:"base_url"`
}

// UpstreamProxyBootstrap captures the environment and token facts needed to
// start an upstream proxy for remote sessions.
type UpstreamProxyBootstrap struct {
	Remote               SessionContext `json:"remote"`
	UpstreamProxyEnabled bool           `json:"upstream_proxy_enabled"`
	TokenPath            string         `json:"token_path"`
	CABundlePath         string         `json:"ca_bundle_path"`
	SystemCAPath         string         `json:"system_ca_path"`
	Token                string         `json:"-"`
	TokenConfigured      bool           `json:"token_configured"`
	Missing              []string       `json:"missing,omitempty"`
	WebSocketURL         string         `json:"websocket_url"`
}

// UpstreamProxyState is the child-process proxy configuration derived from a
// ready bootstrap.
type UpstreamProxyState struct {
	Enabled      bool   `json:"enabled"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	CABundlePath string `json:"ca_bundle_path,omitempty"`
	NoProxy      string `json:"no_proxy"`
}

// RuntimeReport is a redacted, JSON-safe view of remote runtime readiness.
type RuntimeReport struct {
	Remote                SessionContext         `json:"remote"`
	UpstreamProxy         UpstreamProxyReport    `json:"upstream_proxy"`
	InheritedProxyEnvKeys []string               `json:"inherited_proxy_env_keys,omitempty"`
	SubprocessEnv         map[string]string      `json:"subprocess_env,omitempty"`
	Bootstrap             UpstreamProxyBootstrap `json:"-"`
	State                 UpstreamProxyState     `json:"-"`
	InheritedProxyEnv     map[string]string      `json:"-"`
}

// UpstreamProxyReport is the redacted proxy-readiness summary shown in command
// and API reports.
type UpstreamProxyReport struct {
	Enabled              bool     `json:"enabled"`
	Ready                bool     `json:"ready"`
	SessionIDConfigured  bool     `json:"session_id_configured"`
	TokenConfigured      bool     `json:"token_configured"`
	TokenPath            string   `json:"token_path"`
	CABundlePath         string   `json:"ca_bundle_path"`
	SystemCAPath         string   `json:"system_ca_path"`
	WebSocketURL         string   `json:"websocket_url"`
	ProxyURL             string   `json:"proxy_url,omitempty"`
	NoProxy              string   `json:"no_proxy,omitempty"`
	Missing              []string `json:"missing,omitempty"`
	SubprocessEnvKeyList []string `json:"subprocess_env_keys,omitempty"`
}

// Env reads the current process environment into a map.
func Env() map[string]string {
	env := map[string]string{}
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env
}

// InspectEnv builds a redacted remote runtime report from an environment map.
func InspectEnv(env map[string]string, proxyPort uint16) RuntimeReport {
	bootstrap := BootstrapFromEnv(env)
	state := bootstrap.StateForPort(proxyPort)
	subprocessEnv := state.SubprocessEnv()
	inherited := InheritedProxyEnv(env)
	return RuntimeReport{
		Remote:                bootstrap.Remote,
		UpstreamProxy:         bootstrap.Report(state, subprocessEnv),
		InheritedProxyEnvKeys: sortedKeys(inherited),
		SubprocessEnv:         subprocessEnv,
		Bootstrap:             bootstrap,
		State:                 state,
		InheritedProxyEnv:     inherited,
	}
}

// SessionContextFromEnv reads remote-session identity from an environment map.
func SessionContextFromEnv(env map[string]string) SessionContext {
	baseURL := strings.TrimSpace(env["ANTHROPIC_BASE_URL"])
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return SessionContext{
		Enabled:   envTruthy(env["CLAUDE_CODE_REMOTE"]),
		SessionID: strings.TrimSpace(env["CLAUDE_CODE_REMOTE_SESSION_ID"]),
		BaseURL:   baseURL,
	}
}

// BootstrapFromEnv reads the upstream proxy bootstrap contract from an
// environment map.
func BootstrapFromEnv(env map[string]string) UpstreamProxyBootstrap {
	tokenPath := strings.TrimSpace(env["CCR_SESSION_TOKEN_PATH"])
	if tokenPath == "" {
		tokenPath = DefaultTokenPath
	}
	systemCAPath := strings.TrimSpace(env["CCR_SYSTEM_CA_BUNDLE"])
	if systemCAPath == "" {
		systemCAPath = DefaultSystemCAPath
	}
	caBundlePath := strings.TrimSpace(env["CCR_CA_BUNDLE_PATH"])
	if caBundlePath == "" {
		caBundlePath = defaultCABundlePath()
	}
	token, _ := ReadToken(tokenPath)
	bootstrap := UpstreamProxyBootstrap{
		Remote:               SessionContextFromEnv(env),
		UpstreamProxyEnabled: envTruthy(env["CCR_UPSTREAM_PROXY_ENABLED"]),
		TokenPath:            tokenPath,
		CABundlePath:         caBundlePath,
		SystemCAPath:         systemCAPath,
		Token:                token,
		TokenConfigured:      token != "",
	}
	bootstrap.WebSocketURL = UpstreamProxyWebSocketURL(bootstrap.Remote.BaseURL)
	bootstrap.Missing = bootstrap.missing()
	return bootstrap
}

// ShouldEnable reports whether the upstream proxy has every required runtime
// input.
func (b UpstreamProxyBootstrap) ShouldEnable() bool {
	return len(b.missing()) == 0
}

// StateForPort derives subprocess proxy settings for a local proxy listener.
func (b UpstreamProxyBootstrap) StateForPort(port uint16) UpstreamProxyState {
	state := UpstreamProxyState{NoProxy: NoProxyList()}
	if !b.ShouldEnable() {
		return state
	}
	state.Enabled = true
	state.ProxyURL = "http://127.0.0.1:" + strconv.Itoa(int(port))
	state.CABundlePath = b.CABundlePath
	return state
}

// Report returns a redacted JSON-safe proxy readiness summary.
func (b UpstreamProxyBootstrap) Report(state UpstreamProxyState, subprocessEnv map[string]string) UpstreamProxyReport {
	return UpstreamProxyReport{
		Enabled:              b.UpstreamProxyEnabled,
		Ready:                b.ShouldEnable(),
		SessionIDConfigured:  strings.TrimSpace(b.Remote.SessionID) != "",
		TokenConfigured:      b.TokenConfigured,
		TokenPath:            b.TokenPath,
		CABundlePath:         b.CABundlePath,
		SystemCAPath:         b.SystemCAPath,
		WebSocketURL:         b.WebSocketURL,
		ProxyURL:             state.ProxyURL,
		NoProxy:              state.NoProxy,
		Missing:              b.missing(),
		SubprocessEnvKeyList: sortedKeys(subprocessEnv),
	}
}

// SubprocessEnv returns the environment values needed by child processes that
// should use the local upstream proxy.
func (s UpstreamProxyState) SubprocessEnv() map[string]string {
	if !s.Enabled || strings.TrimSpace(s.ProxyURL) == "" || strings.TrimSpace(s.CABundlePath) == "" {
		return map[string]string{}
	}
	return map[string]string{
		"HTTPS_PROXY":         s.ProxyURL,
		"https_proxy":         s.ProxyURL,
		"NO_PROXY":            s.NoProxy,
		"no_proxy":            s.NoProxy,
		"SSL_CERT_FILE":       s.CABundlePath,
		"NODE_EXTRA_CA_CERTS": s.CABundlePath,
		"REQUESTS_CA_BUNDLE":  s.CABundlePath,
		"CURL_CA_BUNDLE":      s.CABundlePath,
	}
}

// ReadToken reads and trims a remote session token. Missing or blank files
// return an empty token.
func ReadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// UpstreamProxyWebSocketURL derives the upstream proxy websocket endpoint from
// an Anthropic-compatible base URL.
func UpstreamProxyWebSocketURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	case base == "":
		base = "wss://" + strings.TrimPrefix(DefaultBaseURL, "https://")
	default:
		base = "wss://" + base
	}
	return base + defaultProxyEndpoint
}

// NoProxyList returns the default host list excluded from upstream proxying.
func NoProxyList() string {
	return strings.Join(noProxyHosts, ",")
}

// InheritedProxyEnv returns existing proxy environment values when both a proxy
// and CA bundle are already configured.
func InheritedProxyEnv(env map[string]string) map[string]string {
	if strings.TrimSpace(env["HTTPS_PROXY"]) == "" || strings.TrimSpace(env["SSL_CERT_FILE"]) == "" {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, key := range upstreamProxyEnvKeys {
		if value := strings.TrimSpace(env[key]); value != "" {
			out[key] = value
		}
	}
	return out
}

// MergeEnv overlays environment map values onto a base env list while
// preserving unmodified entries and key order.
func MergeEnv(base []string, overlay map[string]string) []string {
	out := append([]string(nil), base...)
	indexes := map[string]int{}
	for index, item := range out {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			indexes[key] = index
		}
	}
	keys := sortedKeys(overlay)
	for _, key := range keys {
		item := key + "=" + overlay[key]
		if index, ok := indexes[key]; ok {
			out[index] = item
			continue
		}
		indexes[key] = len(out)
		out = append(out, item)
	}
	return out
}

func (b UpstreamProxyBootstrap) missing() []string {
	missing := []string{}
	if !b.Remote.Enabled {
		missing = append(missing, "CLAUDE_CODE_REMOTE")
	}
	if !b.UpstreamProxyEnabled {
		missing = append(missing, "CCR_UPSTREAM_PROXY_ENABLED")
	}
	if strings.TrimSpace(b.Remote.SessionID) == "" {
		missing = append(missing, "CLAUDE_CODE_REMOTE_SESSION_ID")
	}
	if !b.TokenConfigured {
		missing = append(missing, "session_token")
	}
	return missing
}

func defaultCABundlePath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(".", ".ccr", "ca-bundle.crt")
	}
	return filepath.Join(home, ".ccr", "ca-bundle.crt")
}

func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
