package terminalsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunSnippetAndStatus(t *testing.T) {
	report, err := Run(Options{Action: "snippet", Shell: "zsh"})
	require.NoError(t, err)
	require.Equal(t, "terminal_setup", report.Kind)
	require.Equal(t, "snippet", report.Action)
	require.Equal(t, "zsh", report.Shell)
	require.Contains(t, report.Snippet, "codog_statusline")
	require.Contains(t, report.Snippet, "alias cdg='codog'")

	path := filepath.Join(t.TempDir(), ".zshrc")
	report, err = Run(Options{Action: "status", Shell: "zsh", Path: path})
	require.NoError(t, err)
	require.False(t, report.Installed)
}

func TestRunSuggestsUnknownActionAndShell(t *testing.T) {
	_, err := Run(Options{Action: "instal", Shell: "zsh", Path: filepath.Join(t.TempDir(), ".zshrc")})
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown terminal setup action "instal"`)
	require.Contains(t, err.Error(), `suggestions:`)
	require.Contains(t, err.Error(), `install`)

	_, err = Run(Options{Action: "status", Shell: "zs", Path: filepath.Join(t.TempDir(), ".zshrc")})
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported shell "zs"`)
	require.Contains(t, err.Error(), `did you mean "zsh"?`)
}

func TestShellHelpersSuggestUnsupportedShell(t *testing.T) {
	_, err := Snippet("pwershell")
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported shell "pwershell"`)
	require.Contains(t, err.Error(), `did you mean "powershell"?`)

	_, err = DefaultPath("baash")
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported shell "baash"`)
	require.Contains(t, err.Error(), `did you mean "bash"?`)
}

func TestRunInstallIsIdempotentAndUninstallRemovesBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zshrc")
	require.NoError(t, os.WriteFile(path, []byte("export EXISTING=1\n"), 0o644))

	report, err := Run(Options{Action: "install", Shell: "zsh", Path: path})
	require.NoError(t, err)
	require.True(t, report.Changed)
	require.True(t, report.Installed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "export EXISTING=1")
	require.Contains(t, string(data), startMarker)
	require.Contains(t, string(data), "codog_statusline")

	report, err = Run(Options{Action: "install", Shell: "zsh", Path: path})
	require.NoError(t, err)
	require.False(t, report.Changed)
	require.True(t, report.Installed)

	report, err = Run(Options{Action: "uninstall", Shell: "zsh", Path: path})
	require.NoError(t, err)
	require.True(t, report.Changed)
	require.False(t, report.Installed)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "export EXISTING=1")
	require.NotContains(t, string(data), startMarker)
}

func TestRunInstallForceReplacesExistingBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.fish")
	content := strings.Join([]string{
		"set -gx EXISTING 1",
		startMarker,
		"old",
		endMarker,
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	report, err := Run(Options{Action: "install", Shell: "fish", Path: path, Force: true})
	require.NoError(t, err)
	require.True(t, report.Changed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "set -gx EXISTING 1")
	require.Contains(t, string(data), "alias cdg codog")
	require.NotContains(t, string(data), "\nold\n")
}

func TestRunTargetVSCodeInstallIsIdempotentAndUninstallRemovesBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	require.NoError(t, os.WriteFile(path, []byte("[\n  { \"key\": \"ctrl+k\", \"command\": \"workbench.action.terminal.clear\" }\n]\n"), 0o644))

	report, err := Run(Options{Action: "install", Target: "vscode", Path: path})
	require.NoError(t, err)
	require.Equal(t, "vscode", report.Target)
	require.True(t, report.Changed)
	require.True(t, report.Installed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	require.Contains(t, content, `"key": "ctrl+k"`)
	require.Contains(t, content, `// >>> codog terminal keybinding >>>`)
	require.Contains(t, content, `"key": "shift+enter"`)
	require.Contains(t, content, `"command": "workbench.action.terminal.sendSequence"`)

	report, err = Run(Options{Action: "status", Target: "vscode", Path: path})
	require.NoError(t, err)
	require.True(t, report.Installed)

	report, err = Run(Options{Action: "install", Target: "vscode", Path: path})
	require.NoError(t, err)
	require.False(t, report.Changed)
	require.True(t, report.Installed)

	report, err = Run(Options{Action: "uninstall", Target: "vscode", Path: path})
	require.NoError(t, err)
	require.True(t, report.Changed)
	require.False(t, report.Installed)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"key": "ctrl+k"`)
	require.NotContains(t, string(data), `codog terminal keybinding`)
	require.NotContains(t, string(data), `"key": "shift+enter"`)
}

func TestRunTargetSnippetsAndAlacrittyInstall(t *testing.T) {
	zed, err := Run(Options{Action: "snippet", Target: "zed"})
	require.NoError(t, err)
	require.Equal(t, "zed", zed.Target)
	require.Equal(t, "snippet", zed.Action)
	require.Contains(t, zed.Snippet, `"shift-enter"`)
	require.Contains(t, zed.Snippet, "terminal::SendText")

	path := filepath.Join(t.TempDir(), "alacritty.toml")
	report, err := Run(Options{Action: "install", Target: "alacritty", Path: path})
	require.NoError(t, err)
	require.Equal(t, "alacritty", report.Target)
	require.True(t, report.Changed)
	require.True(t, report.Installed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "[[keyboard.bindings]]")
	require.Contains(t, string(data), `mods = "Shift"`)

	report, err = Run(Options{Action: "uninstall", Target: "alacritty", Path: path})
	require.NoError(t, err)
	require.True(t, report.Changed)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "codog terminal keybinding")
}

func TestRunTargetSuggestsUnsupportedTarget(t *testing.T) {
	_, err := Run(Options{Action: "status", Target: "vscod", Path: filepath.Join(t.TempDir(), "keybindings.json")})
	require.Error(t, err)
	require.Contains(t, err.Error(), `unsupported terminal setup target "vscod"`)
	require.Contains(t, err.Error(), `did you mean "vscode"?`)
}
