package agent

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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/perfissue"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/stretchr/testify/require"
)

func TestInstallStatusDefaults(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "codog")
	require.NoError(t, os.WriteFile(target, []byte("current"), 0o755))

	var out bytes.Buffer
	app := &App{
		Config:     config.Config{ConfigHome: dir},
		Executable: target,
		Out:        &out,
	}
	require.NoError(t, app.Install(context.Background(), []string{"--json"}))
	var report struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Status string `json:"status"`
		Result struct {
			Usage            string `json:"usage"`
			RequiresArtifact bool   `json:"requires_artifact"`
			NextCommand      string `json:"next_command"`
			Updater          struct {
				CurrentVersion string   `json:"current_version"`
				Platform       string   `json:"platform"`
				Executable     string   `json:"executable"`
				DefaultTarget  string   `json:"default_target"`
				TargetPresent  bool     `json:"target_present"`
				Commands       []string `json:"commands"`
			} `json:"updater"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "install", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "codog install ARTIFACT [TARGET]", report.Result.Usage)
	require.True(t, report.Result.RequiresArtifact)
	require.Equal(t, "codog install ARTIFACT [TARGET]", report.Result.NextCommand)
	require.Equal(t, version, report.Result.Updater.CurrentVersion)
	require.Equal(t, updater.PlatformKey(), report.Result.Updater.Platform)
	require.Equal(t, target, report.Result.Updater.Executable)
	require.Equal(t, target, report.Result.Updater.DefaultTarget)
	require.True(t, report.Result.Updater.TargetPresent)
	require.Contains(t, report.Result.Updater.Commands, "install")
}

func TestUpdaterCheckUsesConfiguredManifestURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(updater.Manifest{Version: "0.2.0"}))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{
		Config: config.Config{Future: config.FutureConfig{UpdaterManifestURL: server.URL}},
		Out:    &out,
	}
	require.NoError(t, app.Updater(context.Background(), []string{"check"}))
	var report struct {
		Kind   string              `json:"kind"`
		Action string              `json:"action"`
		Status string              `json:"status"`
		Result updater.CheckResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "updater", report.Kind)
	require.Equal(t, "check", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "0.2.0", report.Result.LatestVersion)
	require.True(t, report.Result.UpdateAvailable)
}

func TestUpdaterVerifyCommand(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		manifest := updater.Manifest{Version: "0.2.0"}
		data, err := json.Marshal(manifest)
		require.NoError(t, err)
		manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data))
		require.NoError(t, json.NewEncoder(w).Encode(manifest))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.Updater(context.Background(), []string{"verify", server.URL, base64.StdEncoding.EncodeToString(publicKey)}))
	require.Contains(t, out.String(), `"signature_valid": true`)
}

func TestUpdaterErrorsHonorGlobalJSONFormat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "check missing URL",
			args:      []string{"updater", "check"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "updater check"`, `"argument": "URL"`},
		},
		{
			name:      "verify missing URL and public key",
			args:      []string{"updater", "verify"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "updater verify"`, `"argument": "URL PUBLIC_KEY"`},
		},
		{
			name:      "download missing URL",
			args:      []string{"updater", "download"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "updater download"`, `"argument": "URL"`},
		},
		{
			name:      "install missing artifact",
			args:      []string{"updater", "install"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "updater install"`, `"argument": "ARTIFACT"`},
		},
		{
			name:      "unknown updater action",
			args:      []string{"updater", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "updater"`, `"bogus"`},
		},
		{
			name:      "upgrade verify missing URL and public key",
			args:      []string{"upgrade", "verify"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "updater verify"`, `"argument": "URL PUBLIC_KEY"`},
		},
		{
			name:      "upgrade install missing artifact",
			args:      []string{"upgrade", "install"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "updater install"`, `"argument": "ARTIFACT"`},
		},
		{
			name:      "install invalid output format",
			args:      []string{"install", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`, `"json"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestUpdaterDownloadCommandReportsStructuredResult(t *testing.T) {
	payload := []byte("codog cli updater binary")
	sum := sha256.Sum256(payload)
	platform := updater.PlatformKey()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = fmt.Fprintf(w, `{"version":"0.4.0","downloads":{"test":"bin/codog","%s":"bin/codog"},"checksums":{"test":"sha256:%s","%s":"sha256:%s"}}`, platform, hex.EncodeToString(sum[:]), platform, hex.EncodeToString(sum[:]))
		case "/bin/codog":
			_, _ = w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Out: &out}
	require.NoError(t, app.Updater(context.Background(), []string{"download", server.URL + "/manifest.json", "test"}))
	var report struct {
		Kind   string                 `json:"kind"`
		Action string                 `json:"action"`
		Status string                 `json:"status"`
		Result updater.DownloadResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "updater", report.Kind)
	require.Equal(t, "download", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "0.4.0", report.Result.Version)
	require.Equal(t, server.URL+"/bin/codog", report.Result.URL)
	require.Equal(t, hex.EncodeToString(sum[:]), report.Result.SHA256)
	require.FileExists(t, report.Result.Path)

	out.Reset()
	app.Config.Future.UpdaterManifestURL = server.URL + "/manifest.json"
	require.NoError(t, app.Updater(context.Background(), []string{"download"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "download", report.Action)
	require.Equal(t, platform, report.Result.Platform)
	require.Equal(t, server.URL+"/bin/codog", report.Result.URL)
	require.FileExists(t, report.Result.Path)
}

func makeAgentPluginZip(t *testing.T, files map[string]string) []byte {
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

func perfSignalKinds(signals []perfissue.Signal) []string {
	kinds := make([]string, 0, len(signals))
	for _, signal := range signals {
		kinds = append(kinds, signal.Kind)
	}
	return kinds
}

func TestVoiceCommandHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_TEST_SPEAK_HELPER") == "1" {
		data, _ := io.ReadAll(os.Stdin)
		fmt.Printf("speak:%s", strings.TrimSpace(string(data)))
		os.Exit(0)
	}
	if os.Getenv("CODOG_TEST_VOICE_HELPER") != "1" {
		return
	}
	data, _ := io.ReadAll(os.Stdin)
	fmt.Printf("voice:%s", strings.TrimSpace(string(data)))
	os.Exit(0)
}

func failedMockParityScenarioSummaries(report harness.Report) string {
	var builder strings.Builder
	for _, scenario := range report.Scenarios {
		if scenario.OK {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("; ")
		}
		builder.WriteString(scenario.Name)
		if scenario.Error != "" {
			builder.WriteString(": ")
			builder.WriteString(scenario.Error)
		}
		if scenario.Output != "" {
			builder.WriteString(" output=")
			builder.WriteString(scenario.Output)
		}
	}
	if builder.Len() == 0 {
		return "no failed scenario details"
	}
	return builder.String()
}
