package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadPluginManifest(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"Demo","version":"0.1.0","commands":["demo"],"skills":["./skills/review"],"agents":["./agents/helper.json"],"mcp_servers":{"local":{"command":"demo-mcp","args":["--stdio"]}},"tools":[{"name":"demo_tool","command":"cat","permission":"read-only"}]}`), 0o644))

	manifests, err := Load(workspace)
	require.NoError(t, err)
	require.Len(t, manifests, 1)
	require.Equal(t, "demo", manifests[0].ID)
	require.Equal(t, "Demo", manifests[0].Name)
	require.Equal(t, []string{"demo"}, manifests[0].Commands)
	require.Equal(t, []string{"./skills/review"}, manifests[0].Skills)
	require.Equal(t, []string{"./agents/helper.json"}, manifests[0].Agents)
	require.Equal(t, "demo-mcp", manifests[0].MCPServers["local"].Command)
	require.Equal(t, []string{"--stdio"}, manifests[0].MCPServers["local"].Args)
	require.Len(t, manifests[0].Tools, 1)
	require.Equal(t, "demo_tool", manifests[0].Tools[0].Name)
	require.Equal(t, "cat", manifests[0].Tools[0].Command)
	require.True(t, manifests[0].Enabled)
	require.Equal(t, filepath.Join(workspace, ".codog", "plugins"), Root(workspace))
	require.Equal(t, filepath.Join(workspace, ".codog", "plugin-data"), DataRoot(workspace))
	require.Equal(t, filepath.Join(workspace, ".codog", "plugin-data", "demo"), DataDir(workspace, "demo"))
	require.Equal(t, filepath.Join(workspace, ".codog", "plugin-data", "demo"), DataDirForManifest(manifests[0]))
}

func TestLoadMCPServersNamespacesEnabledPluginServers(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{
		"id":"demo",
		"name":"demo",
		"mcp_servers":{"local":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/mcp","args":["--data","${CLAUDE_PLUGIN_DATA}"],"env":["CONFIG=${CLAUDE_PLUGIN_ROOT}/config.json"]}},
		"mcpServers":{"camel":{"command":"cat"}}
	}`), 0o644))

	servers, err := LoadMCPServers(workspace)
	require.NoError(t, err)
	require.Len(t, servers, 2)
	pluginRoot := filepath.ToSlash(root)
	pluginData := filepath.ToSlash(filepath.Join(workspace, ".codog", "plugin-data", "demo"))
	require.Equal(t, pluginRoot+"/bin/mcp", servers["plugin:demo:local"].Command)
	require.Equal(t, []string{"--data", pluginData}, servers["plugin:demo:local"].Args)
	require.Equal(t, []string{
		"CLAUDE_PLUGIN_ROOT=" + pluginRoot,
		"CLAUDE_PLUGIN_DATA=" + pluginData,
		"CONFIG=" + pluginRoot + "/config.json",
	}, servers["plugin:demo:local"].Env)
	require.Equal(t, "cat", servers["plugin:demo:camel"].Command)
	require.Contains(t, servers["plugin:demo:camel"].Env, "CLAUDE_PLUGIN_ROOT="+pluginRoot)
	require.Contains(t, servers["plugin:demo:camel"].Env, "CLAUDE_PLUGIN_DATA="+pluginData)
}

func TestInstallDisableEnableRemovePlugin(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"demo","name":"Demo"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "tool.sh"), []byte("echo ok\n"), 0o755))

	installed, err := Install(workspace, source)
	require.NoError(t, err)
	require.Equal(t, "demo", installed.ID)
	require.True(t, installed.Enabled)
	require.FileExists(t, filepath.Join(workspace, ".codog", "plugins", "demo", "tool.sh"))

	disabled, err := Disable(workspace, "demo")
	require.NoError(t, err)
	require.False(t, disabled.Enabled)
	require.FileExists(t, filepath.Join(disabled.Root, DisabledMarker))

	enabled, err := Enable(workspace, "demo")
	require.NoError(t, err)
	require.True(t, enabled.Enabled)
	require.NoFileExists(t, filepath.Join(enabled.Root, DisabledMarker))

	require.NoError(t, Remove(workspace, "demo"))
	require.NoDirExists(t, filepath.Join(workspace, ".codog", "plugins", "demo"))
}

func TestInstallRejectsUnsafePluginID(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"../bad","name":"Bad"}`), 0o644))

	_, err := Install(workspace, source)
	require.Error(t, err)
	require.Contains(t, err.Error(), "single path component")
}

func TestInstallRejectsInvalidPluginManifest(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat","permission":"root"}]}`), 0o644))

	_, err := Install(workspace, source)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plugin validation failed")
	require.Contains(t, err.Error(), "permission")
	require.NoDirExists(t, filepath.Join(workspace, ".codog", "plugins", "demo"))
}

func TestValidatePluginManifestAcceptsDirectoryAndFile(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	manifestPath := filepath.Join(source, "plugin.json")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"id":"demo","name":"demo","version":"1.0.0","description":"Demo","tools":[{"name":"demo_tool","command":"echo","permission":"read-only"}]}`), 0o644))

	result, err := Validate(source)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Empty(t, result.Errors)
	require.Empty(t, result.Warnings)
	require.Equal(t, manifestPath, result.FilePath)
	require.NotNil(t, result.Manifest)
	require.Equal(t, "demo", result.Manifest.ID)

	result, err = Validate(manifestPath)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, manifestPath, result.FilePath)
}

func TestValidatePluginManifestReportsWarningsAndErrors(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{
		"id":"demo",
		"name":"Demo Plugin",
		"source":"./plugins/demo",
		"tools":[
			{"name":"demo_tool","command":"echo","permission":"root"},
			{"name":"demo_tool","command":"echo"}
		],
		"commands":["../bad.md"],
		"hooks":["hooks/hook.json"]
	}`), 0o644))

	result, err := Validate(source)
	require.NoError(t, err)
	require.False(t, result.Success)
	requireValidationCode(t, result.Errors, "invalid_tool_permission")
	requireValidationCode(t, result.Errors, "duplicate_tool_name")
	requireValidationCode(t, result.Errors, "path_traversal")
	requireValidationCode(t, result.Warnings, "missing_version")
	requireValidationCode(t, result.Warnings, "missing_description")
	requireValidationCode(t, result.Warnings, "marketplace_only_field")
	requireValidationCode(t, result.Warnings, "non_kebab_name")
}

func TestValidatePluginManifestInvalidJSON(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":`), 0o644))

	result, err := Validate(source)
	require.NoError(t, err)
	require.False(t, result.Success)
	requireValidationCode(t, result.Errors, "invalid_json")
}

func TestValidatePluginManifestRecognizesStandardContentDirs(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "skills", "review"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"demo","name":"demo"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "skills", "review", "SKILL.md"), []byte("Review skill"), 0o644))

	result, err := Validate(source)
	require.NoError(t, err)
	require.True(t, result.Success)
	requireNoValidationCode(t, result.Warnings, "empty_plugin")
}

func TestResolveContentPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin")
	path, err := ResolveContentPath(root, "./commands/deploy.md")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "commands", "deploy.md"), path)

	_, err = ResolveContentPath(root, "../outside.md")
	require.Error(t, err)
	_, err = ResolveContentPath(root, "/tmp/outside.md")
	require.Error(t, err)
	_, err = ResolveContentPath(root, `commands\deploy.md`)
	require.Error(t, err)
}

func TestLoadHookConfigsLoadsStandardAndManifestPaths(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hooks"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "extra"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{"id":"demo","name":"demo","hooks":["./extra/hooks.json"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hooks", "hooks.json"), []byte(`{
		"pre_tool_use":["${CLAUDE_PLUGIN_ROOT}/hooks/pre.sh"],
		"notification":[{"type":"http","url":"https://example.test/hook","headers":{"X-Plugin":"${CLAUDE_PLUGIN_ROOT}"},"allowedEnvVars":["${CLAUDE_PLUGIN_DATA}"]}]
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "extra", "hooks.json"), []byte(`{"session_start":[{"command":"${CLAUDE_PLUGIN_DATA}/state.sh"}]}`), 0o644))

	files, err := LoadHookConfigs(workspace)
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.Equal(t, "demo", files[0].PluginID)
	pluginRoot := filepath.ToSlash(root)
	pluginData := filepath.ToSlash(filepath.Join(workspace, ".codog", "plugin-data", "demo"))
	require.Equal(t, []string{pluginRoot + "/hooks/pre.sh"}, files[0].Config.PreToolUse)
	require.Equal(t, pluginRoot+"/hooks/pre.sh", files[0].Config.PreToolUseCommands[0].Command)
	require.Equal(t, pluginRoot, files[0].Config.NotificationCommands[0].Headers["X-Plugin"])
	require.Equal(t, []string{pluginData}, files[0].Config.NotificationCommands[0].AllowedEnvVars)
	require.Equal(t, []string{pluginData + "/state.sh"}, files[1].Config.SessionStart)
	require.Equal(t, pluginData+"/state.sh", files[1].Config.SessionStartCommands[0].Command)
}

func requireValidationCode(t *testing.T, messages []ValidationMessage, code string) {
	t.Helper()
	for _, message := range messages {
		if message.Code == code {
			return
		}
	}
	require.Failf(t, "missing validation message", "code %q not found in %#v", code, messages)
}

func requireNoValidationCode(t *testing.T, messages []ValidationMessage, code string) {
	t.Helper()
	for _, message := range messages {
		require.NotEqual(t, code, message.Code)
	}
}

func TestFetchMarketplaceVerifiesSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	index := MarketplaceIndex{
		Name: "Default",
		Plugins: []RemotePlugin{
			{ID: "demo", URL: "demo.zip", SHA256: "sha256:abc123"},
		},
	}
	payload, err := canonicalMarketplace(index)
	require.NoError(t, err)
	index.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(index))
	}))
	defer server.Close()

	fetched, err := FetchMarketplace(context.Background(), server.URL, base64.StdEncoding.EncodeToString(publicKey))
	require.NoError(t, err)
	require.True(t, fetched.SignatureValid)
	require.Equal(t, server.URL, fetched.Source)
	require.Len(t, fetched.Plugins, 1)

	index.Name = "Tampered"
	payload, err = json.Marshal(index)
	require.NoError(t, err)
	tamperedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer tamperedServer.Close()
	_, err = FetchMarketplace(context.Background(), tamperedServer.URL, base64.StdEncoding.EncodeToString(publicKey))
	require.Error(t, err)
	require.Contains(t, err.Error(), "marketplace signature verification failed")
}

func TestInstallRemoteVerifiesChecksumAndInstallsZip(t *testing.T) {
	workspace := t.TempDir()
	archive := makePluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo","version":"0.1.0"}`,
		"demo/tool.sh":     "echo ok\n",
	})
	sum := sha256.Sum256(archive)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = fmt.Fprintf(w, `{"plugins":[{"id":"demo","name":"Demo","version":"0.1.0","url":"demo.zip","sha256":"sha256:%s"}]}`, hex.EncodeToString(sum[:]))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	result, err := InstallRemote(context.Background(), workspace, serverURL+"/index.json", "demo", "")
	require.NoError(t, err)
	require.True(t, result.ChecksumValid)
	require.Equal(t, "demo", result.ID)
	require.Equal(t, "0.1.0", result.Version)
	require.Equal(t, hex.EncodeToString(sum[:]), result.SHA256)
	require.FileExists(t, filepath.Join(workspace, ".codog", "plugins", "demo", "tool.sh"))
}

func TestInstallRemoteRejectsChecksumMismatch(t *testing.T) {
	archive := makePluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo"}`,
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write([]byte(`{"plugins":[{"id":"demo","url":"demo.zip","sha256":"sha256:deadbeef"}]}`))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	_, err := InstallRemote(context.Background(), t.TempDir(), server.URL+"/index.json", "demo", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "checksum mismatch")
}

func TestCheckUpdatesFindsNewerRemoteVersion(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","name":"Demo","version":"0.1.0"}`), 0o644))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"plugins":[{"id":"demo","version":"0.2.0","url":"demo.zip","sha256":"abc"}]}`))
	}))
	defer server.Close()

	updates, err := CheckUpdates(context.Background(), workspace, []MarketplaceSource{{URL: server.URL}})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	require.Equal(t, "demo", updates[0].ID)
	require.Equal(t, "0.1.0", updates[0].CurrentVersion)
	require.Equal(t, "0.2.0", updates[0].LatestVersion)
	require.True(t, updates[0].UpdateAvailable)
}

func TestUpdateRemoteReplacesInstalledPluginAndBacksUpOldVersion(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","name":"Demo","version":"0.1.0"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tool.sh"), []byte("echo old\n"), 0o755))
	archive := makePluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo","version":"0.2.0"}`,
		"demo/tool.sh":     "echo new\n",
	})
	sum := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = fmt.Fprintf(w, `{"plugins":[{"id":"demo","name":"Demo","version":"0.2.0","url":"demo.zip","sha256":"%s"}]}`, hex.EncodeToString(sum[:]))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	result, err := UpdateRemote(context.Background(), workspace, []MarketplaceSource{{URL: server.URL + "/index.json"}}, "demo")
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.Equal(t, "0.1.0", result.PreviousVersion)
	require.Equal(t, "0.2.0", result.Version)
	require.DirExists(t, result.BackupPath)
	require.FileExists(t, filepath.Join(result.BackupPath, "plugin.json"))
	data, err := os.ReadFile(filepath.Join(dir, "tool.sh"))
	require.NoError(t, err)
	require.Equal(t, "echo new\n", string(data))
}

func TestVersionNewerComparesNumericSegments(t *testing.T) {
	require.True(t, versionNewer("v1.10.0", "1.2.0"))
	require.False(t, versionNewer("1.2.0", "1.2"))
	require.False(t, versionNewer("1.0.0", "1.0.1"))
}

func makePluginZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, body := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		file, err := writer.CreateHeader(header)
		require.NoError(t, err)
		_, err = file.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buf.Bytes()
}
