package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOAuthCommandHelperValidation(t *testing.T) {
	app := &App{Out: &bytes.Buffer{}, Config: config.Config{ConfigHome: t.TempDir()}}

	require.NoError(t, app.oauthPKCE())
	require.Error(t, app.oauthDiscover([]string{"discover"}))
	require.NoError(t, app.oauthStatus(""))
	require.NoError(t, app.oauthToken(nil))
	require.Error(t, app.oauthToken([]string{"unknown"}))
	require.Error(t, app.oauthTokenSave([]string{"save"}))
	require.Error(t, app.oauthTokenSave([]string{"save", "access", "refresh", "invalid-time"}))
	require.Error(t, app.oauthTokenShow())
	require.Error(t, app.oauthTokenRefresh("missing"))
	require.Error(t, app.oauthTokenRevokeCommand(nil))

	require.Error(t, app.oauthBrowserStart(nil))
	require.Error(t, app.oauthBrowserStart([]string{"profile"}))
	require.Error(t, app.oauthBrowserStart([]string{"missing", "http://localhost/callback"}))
	for count := 0; count < 4; count++ {
		args := []string{"profile", "code", "verifier", "http://localhost/callback"}
		require.Error(t, app.oauthBrowserExchange(args[:count]))
	}
	require.Error(t, app.oauthBrowserExchange([]string{"missing", "code", "verifier", "http://localhost/callback"}))
	require.Error(t, app.oauthBrowserLogin(nil))
	require.Error(t, app.oauthBrowserLogin([]string{"missing"}))

	require.Error(t, app.oauthDeviceStart(nil))
	require.Error(t, app.oauthDeviceStart([]string{"https://issuer.example"}))
	require.Error(t, app.oauthDeviceStart([]string{"missing"}))
	require.Error(t, app.oauthDevicePoll(nil))
	require.Error(t, app.oauthDevicePoll([]string{"https://issuer.example"}))
	require.Error(t, app.oauthDevicePoll([]string{"https://issuer.example", "client"}))
	require.Error(t, app.oauthDevicePoll([]string{"missing"}))
	require.Error(t, app.oauthDevicePoll([]string{"missing", "code"}))
	require.Error(t, app.oauthDeviceLogin(nil))
	require.Error(t, app.oauthDeviceLogin([]string{"https://issuer.example"}))
	require.Error(t, app.oauthDeviceLogin([]string{"missing"}))

	require.Empty(t, optionalArgument(nil, 0))
	require.Equal(t, "value", optionalArgument([]string{"value"}, 0))
	require.NoError(t, writeIndentedJSON(app.Out, map[string]string{"status": "ok"}))
}

func TestOAuthTokenStorageFailures(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(configHome, []byte("file"), 0o600))
	app := &App{Out: &bytes.Buffer{}, Config: config.Config{ConfigHome: configHome}}

	require.Error(t, app.oauthTokenSave([]string{"save", "access"}))
	require.Error(t, app.oauthTokenShow())
	require.Error(t, app.oauthTokenRefresh("missing"))
	require.Error(t, app.oauthLogout("missing"))
	_ = app.oauthTokenDelete()
}

func TestProfileAndProviderParserBoundaries(t *testing.T) {
	profile, err := parseProfileRequest([]string{"show"})
	require.NoError(t, err)
	require.Equal(t, "show", profile.Action)
	require.Empty(t, profile.Name)

	profile, err = parseProfileRequest([]string{"--json", "--target=project", "--path=config.json", "set", "work"})
	require.NoError(t, err)
	require.Equal(t, profileRequest{Action: "set", Format: "json", Target: "project", Path: "config.json", Name: "work"}, profile)

	profileCases := [][]string{
		{"--output-format"}, {"--target"}, {"--path"}, {"--unknown"},
		{"--output-format=yaml"}, {"list", "extra"}, {"set"}, {"clear", "extra"}, {"work", "extra"},
	}
	for _, args := range profileCases {
		_, err := parseProfileRequest(args)
		require.Error(t, err, args)
	}

	provider, err := parseProviderRequest([]string{
		"set", "custom", "--base-url=https://api.example", "--model=model-1",
		"--target=project", "--path=config.json", "--json",
	})
	require.NoError(t, err)
	require.Equal(t, "set", provider.Action)
	require.Equal(t, "custom", provider.Name)
	require.Equal(t, "https://api.example", provider.BaseURL)
	require.Equal(t, "model-1", provider.Model)
	require.Equal(t, "json", provider.Format)

	providerCases := [][]string{
		{"--output-format"}, {"--base-url"}, {"--model"}, {"--target"}, {"--path"},
		{"--output-format=yaml"}, {"status", "extra"}, {"list", "extra"}, {"show"},
		{"show", "openai", "extra"}, {"set"}, {"set", "custom", "url", "model", "extra"},
	}
	for _, args := range providerCases {
		_, err := parseProviderRequest(args)
		require.Error(t, err, args)
	}
}

func TestResolveProviderConfigVariants(t *testing.T) {
	tests := []struct {
		name     string
		req      providerCommandRequest
		provider string
		model    string
		wantErr  bool
	}{
		{name: "missing", wantErr: true},
		{name: "anthropic", req: providerCommandRequest{Name: "default"}, provider: "anthropic", model: config.DefaultModel},
		{name: "custom missing URL", req: providerCommandRequest{Name: "custom"}, wantErr: true},
		{name: "custom", req: providerCommandRequest{Name: "compatible", BaseURL: "https://api.example"}, provider: "custom"},
		{name: "openai", req: providerCommandRequest{Name: "openai-compatible"}, provider: "openai", model: "openai/gpt-4o-mini"},
		{name: "xai", req: providerCommandRequest{Name: "grok"}, provider: "xai", model: "grok"},
		{name: "qwen", req: providerCommandRequest{Name: "qwen"}, provider: "dashscope", model: "qwen-plus"},
		{name: "kimi", req: providerCommandRequest{Name: "kimi"}, provider: "dashscope", model: "kimi"},
		{name: "unknown", req: providerCommandRequest{Name: "unknown"}, wantErr: true},
		{name: "unknown custom URL", req: providerCommandRequest{Name: "unknown", BaseURL: "https://api.example"}, provider: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := resolveProviderConfig(test.req)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.provider, resolved.name)
			require.Equal(t, test.model, resolved.model)
		})
	}
}
