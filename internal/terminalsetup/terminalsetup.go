package terminalsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Rememorio/codog/internal/toolnames"
)

const (
	startMarker       = "# >>> codog shell integration >>>"
	endMarker         = "# <<< codog shell integration <<<"
	targetStartMarker = "codog terminal keybinding"
)

var actionNames = []string{"status", "snippet", "print", "install", "uninstall", "remove"}
var shellNames = []string{"zsh", "bash", "fish", "powershell", "pwsh"}
var targetNames = []string{"shell", "vscode", "cursor", "windsurf", "zed", "alacritty", "apple-terminal"}

type Options struct {
	Action string
	Shell  string
	Target string
	Path   string
	Force  bool
}

type Report struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	Target    string `json:"target,omitempty"`
	Shell     string `json:"shell"`
	Path      string `json:"path,omitempty"`
	Installed bool   `json:"installed"`
	Changed   bool   `json:"changed,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
	Message   string `json:"message,omitempty"`
}

func Run(opts Options) (Report, error) {
	action := strings.ToLower(strings.TrimSpace(opts.Action))
	if action == "" {
		action = "status"
	}
	target, err := NormalizeTarget(opts.Target)
	if err != nil {
		return Report{}, err
	}
	if target != "shell" {
		return runTarget(opts, action, target)
	}
	rawShell := strings.TrimSpace(opts.Shell)
	shell := NormalizeShell(rawShell)
	if rawShell != "" && shell == "" {
		return Report{}, suggestedValueError("unsupported shell", rawShell, shellNames)
	}
	if shell == "" {
		shell = DetectShell(os.Getenv("SHELL"))
	}
	if shell == "" {
		shell = defaultShell()
	}
	snippet, err := Snippet(shell)
	if err != nil {
		return Report{}, err
	}
	path := strings.TrimSpace(opts.Path)
	if path == "" && action != "snippet" {
		path, err = DefaultPath(shell)
		if err != nil {
			return Report{}, err
		}
	}
	report := Report{
		Kind:    "terminal_setup",
		Action:  action,
		Status:  "ok",
		Target:  "shell",
		Shell:   shell,
		Path:    path,
		Snippet: snippet,
	}
	switch action {
	case "status":
		report.Installed = fileContainsIntegration(path)
		if report.Installed {
			report.Message = "Codog shell integration is installed."
		} else {
			report.Message = "Codog shell integration is not installed."
		}
	case "snippet", "print":
		report.Action = "snippet"
		report.Message = "Add this snippet to your shell profile, or run install."
	case "install":
		changed, installed, err := install(path, snippet, opts.Force)
		if err != nil {
			return Report{}, err
		}
		report.Changed = changed
		report.Installed = installed
		if changed {
			report.Message = "Codog shell integration installed."
		} else {
			report.Message = "Codog shell integration already installed."
		}
	case "uninstall", "remove":
		changed, err := uninstall(path)
		if err != nil {
			return Report{}, err
		}
		report.Action = "uninstall"
		report.Changed = changed
		report.Installed = false
		if changed {
			report.Message = "Codog shell integration removed."
		} else {
			report.Message = "Codog shell integration was not installed."
		}
	default:
		return Report{}, suggestedValueError("unknown terminal setup action", action, actionNames)
	}
	return report, nil
}

func runTarget(opts Options, action string, target string) (Report, error) {
	snippet, err := TargetSnippet(target)
	if err != nil {
		return Report{}, err
	}
	path := strings.TrimSpace(opts.Path)
	if path == "" && action != "snippet" && action != "print" {
		path, err = DefaultTargetPath(target)
		if err != nil {
			return Report{}, err
		}
	}
	report := Report{
		Kind:    "terminal_setup",
		Action:  action,
		Status:  "ok",
		Target:  target,
		Path:    path,
		Snippet: snippet,
	}
	switch action {
	case "status":
		report.Installed = fileContainsTargetIntegration(path, target)
		if report.Installed {
			report.Message = targetDisplayName(target) + " Shift+Enter integration is installed."
		} else {
			report.Message = targetDisplayName(target) + " Shift+Enter integration is not installed."
		}
	case "snippet", "print":
		report.Action = "snippet"
		report.Message = "Add this Shift+Enter snippet to your terminal or editor keybinding file, or run install."
	case "install":
		changed, installed, err := installTarget(path, target, snippet, opts.Force)
		if err != nil {
			return Report{}, err
		}
		report.Changed = changed
		report.Installed = installed
		if changed {
			report.Message = targetDisplayName(target) + " Shift+Enter integration installed."
		} else {
			report.Message = targetDisplayName(target) + " Shift+Enter integration already installed."
		}
	case "uninstall", "remove":
		changed, err := uninstallTarget(path, target)
		if err != nil {
			return Report{}, err
		}
		report.Action = "uninstall"
		report.Changed = changed
		report.Installed = false
		if changed {
			report.Message = targetDisplayName(target) + " Shift+Enter integration removed."
		} else {
			report.Message = targetDisplayName(target) + " Shift+Enter integration was not installed."
		}
	default:
		return Report{}, suggestedValueError("unknown terminal setup action", action, actionNames)
	}
	return report, nil
}

func NormalizeShell(shell string) string {
	shell = strings.ToLower(strings.TrimSpace(filepath.Base(shell)))
	switch shell {
	case "zsh":
		return "zsh"
	case "bash":
		return "bash"
	case "fish":
		return "fish"
	case "pwsh", "powershell", "powershell.exe", "pwsh.exe":
		return "powershell"
	default:
		return ""
	}
}

func NormalizeTarget(target string) (string, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	target = strings.ReplaceAll(target, "_", "-")
	target = strings.ReplaceAll(target, " ", "-")
	if target == "" {
		return "shell", nil
	}
	switch target {
	case "shell", "profile":
		return "shell", nil
	case "vscode", "vs-code", "code":
		return "vscode", nil
	case "cursor":
		return "cursor", nil
	case "windsurf":
		return "windsurf", nil
	case "zed":
		return "zed", nil
	case "alacritty":
		return "alacritty", nil
	case "apple-terminal", "appleterminal", "terminal.app", "terminal":
		return "apple-terminal", nil
	default:
		return "", suggestedValueError("unsupported terminal setup target", target, targetNames)
	}
}

func DetectShell(envShell string) string {
	return NormalizeShell(envShell)
}

func DefaultPath(shell string) (string, error) {
	rawShell := strings.TrimSpace(shell)
	shell = NormalizeShell(shell)
	if shell == "" {
		if rawShell != "" {
			return "", suggestedValueError("unsupported shell", rawShell, shellNames)
		}
		return "", errors.New("supported shell is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch shell {
	case "zsh":
		return filepath.Join(home, ".zshrc"), nil
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "fish":
		return filepath.Join(home, ".config", "fish", "conf.d", "codog.fish"), nil
	case "powershell":
		if runtime.GOOS == "windows" {
			return filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"), nil
		}
		return filepath.Join(home, ".config", "powershell", "Microsoft.PowerShell_profile.ps1"), nil
	default:
		return "", suggestedValueError("unsupported shell", rawShell, shellNames)
	}
}

func DefaultTargetPath(target string) (string, error) {
	normalized, err := NormalizeTarget(target)
	if err != nil {
		return "", err
	}
	if normalized == "shell" {
		return DefaultPath("")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch normalized {
	case "vscode", "cursor", "windsurf":
		dir := targetDisplayName(normalized)
		if normalized == "vscode" {
			dir = "Code"
		}
		switch runtime.GOOS {
		case "windows":
			return filepath.Join(home, "AppData", "Roaming", dir, "User", "keybindings.json"), nil
		case "darwin":
			return filepath.Join(home, "Library", "Application Support", dir, "User", "keybindings.json"), nil
		default:
			return filepath.Join(home, ".config", dir, "User", "keybindings.json"), nil
		}
	case "zed":
		return filepath.Join(home, ".config", "zed", "keymap.json"), nil
	case "alacritty":
		if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
			return filepath.Join(configHome, "alacritty", "alacritty.toml"), nil
		}
		return filepath.Join(home, ".config", "alacritty", "alacritty.toml"), nil
	case "apple-terminal":
		return filepath.Join(home, "Library", "Preferences", "com.apple.Terminal.plist"), nil
	default:
		return "", suggestedValueError("unsupported terminal setup target", target, targetNames)
	}
}

func Snippet(shell string) (string, error) {
	rawShell := strings.TrimSpace(shell)
	shell = NormalizeShell(shell)
	switch shell {
	case "zsh", "bash":
		return strings.Join([]string{
			startMarker,
			"export CODOG_SHELL_INTEGRATION=1",
			"alias cdg='codog'",
			"codog_statusline() {",
			"  codog statusline \"$@\" 2>/dev/null",
			"}",
			endMarker,
			"",
		}, "\n"), nil
	case "fish":
		return strings.Join([]string{
			startMarker,
			"set -gx CODOG_SHELL_INTEGRATION 1",
			"alias cdg codog",
			"function codog_statusline",
			"    codog statusline $argv 2>/dev/null",
			"end",
			endMarker,
			"",
		}, "\n"), nil
	case "powershell":
		return strings.Join([]string{
			startMarker,
			"$env:CODOG_SHELL_INTEGRATION = \"1\"",
			"Set-Alias cdg codog",
			"function codog_statusline { codog statusline @args 2>$null }",
			endMarker,
			"",
		}, "\n"), nil
	default:
		if rawShell == "" {
			return "", errors.New("supported shell is required")
		}
		return "", suggestedValueError("unsupported shell", rawShell, shellNames)
	}
}

func TargetSnippet(target string) (string, error) {
	normalized, err := NormalizeTarget(target)
	if err != nil {
		return "", err
	}
	switch normalized {
	case "vscode", "cursor", "windsurf":
		return strings.Join([]string{
			`// >>> codog terminal keybinding >>>`,
			`{`,
			`  "key": "shift+enter",`,
			`  "command": "workbench.action.terminal.sendSequence",`,
			`  "args": { "text": "\u001b\r" },`,
			`  "when": "terminalFocus"`,
			`}`,
			`// <<< codog terminal keybinding <<<`,
			"",
		}, "\n"), nil
	case "zed":
		return strings.Join([]string{
			`// >>> codog terminal keybinding >>>`,
			`{`,
			`  "context": "Terminal",`,
			`  "bindings": {`,
			`    "shift-enter": ["terminal::SendText", "\u001b\r"]`,
			`  }`,
			`}`,
			`// <<< codog terminal keybinding <<<`,
			"",
		}, "\n"), nil
	case "alacritty":
		return strings.Join([]string{
			"# >>> codog terminal keybinding >>>",
			"[[keyboard.bindings]]",
			`key = "Return"`,
			`mods = "Shift"`,
			`chars = "\u001B\r"`,
			"# <<< codog terminal keybinding <<<",
			"",
		}, "\n"), nil
	case "apple-terminal":
		return strings.Join([]string{
			"# Apple Terminal requires plist updates; run install on macOS to configure Option as Meta.",
			`/usr/libexec/PlistBuddy -c "Set :'Window Settings':'<profile>':useOptionAsMetaKey true" ~/Library/Preferences/com.apple.Terminal.plist`,
			`/usr/libexec/PlistBuddy -c "Set :'Window Settings':'<profile>':Bell false" ~/Library/Preferences/com.apple.Terminal.plist`,
			"",
		}, "\n"), nil
	case "shell":
		return Snippet(defaultShell())
	default:
		return "", suggestedValueError("unsupported terminal setup target", target, targetNames)
	}
}

func suggestedValueError(prefix string, value string, candidates []string) error {
	suggestions := toolnames.Suggestions(value, candidates, 4)
	switch len(suggestions) {
	case 0:
		return fmt.Errorf("%s %q", prefix, value)
	case 1:
		return fmt.Errorf("%s %q; did you mean %q?", prefix, value, suggestions[0])
	default:
		return fmt.Errorf("%s %q; suggestions: %s", prefix, value, strings.Join(suggestions, ", "))
	}
}

func defaultShell() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "zsh"
}

func fileContainsIntegration(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), startMarker) && strings.Contains(string(data), endMarker)
}

func fileContainsTargetIntegration(path string, target string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	if strings.Contains(content, targetBlockStart(target)) && strings.Contains(content, targetBlockEnd(target)) {
		return true
	}
	switch target {
	case "vscode", "cursor", "windsurf":
		return strings.Contains(content, `"key": "shift+enter"`) &&
			strings.Contains(content, `"command": "workbench.action.terminal.sendSequence"`) &&
			strings.Contains(content, `"when": "terminalFocus"`)
	case "zed":
		return strings.Contains(content, `"shift-enter"`) && strings.Contains(content, "terminal::SendText")
	case "alacritty":
		return strings.Contains(content, `key = "Return"`) && strings.Contains(content, `mods = "Shift"`)
	default:
		return false
	}
}

func install(path string, snippet string, force bool) (bool, bool, error) {
	contentBytes, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	content := string(contentBytes)
	if strings.Contains(content, startMarker) && strings.Contains(content, endMarker) {
		if !force {
			return false, true, nil
		}
		content = replaceBlock(content, snippet)
	} else {
		if strings.TrimSpace(content) != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if strings.TrimSpace(content) != "" {
			content += "\n"
		}
		content += snippet
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, false, err
	}
	return true, true, nil
}

func installTarget(path string, target string, snippet string, force bool) (bool, bool, error) {
	contentBytes, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	content := string(contentBytes)
	if strings.Contains(content, targetBlockStart(target)) && strings.Contains(content, targetBlockEnd(target)) {
		if !force {
			return false, true, nil
		}
		content = replaceTargetBlock(content, target, snippet)
	} else {
		switch target {
		case "vscode", "cursor", "windsurf", "zed":
			content = insertJSONCArrayBlock(content, snippet)
		case "alacritty":
			if strings.TrimSpace(content) != "" && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			if strings.TrimSpace(content) != "" {
				content += "\n"
			}
			content += snippet
		case "apple-terminal":
			return false, false, errors.New("apple-terminal setup requires macOS plist commands; use snippet for the manual plan")
		default:
			return false, false, suggestedValueError("unsupported terminal setup target", target, targetNames)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, false, err
	}
	return true, true, nil
}

func uninstall(path string) (bool, error) {
	contentBytes, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	content := string(contentBytes)
	next, changed := removeBlock(content)
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func uninstallTarget(path string, target string) (bool, error) {
	contentBytes, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	next, changed := removeTargetBlock(string(contentBytes), target)
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func replaceBlock(content string, snippet string) string {
	next, changed := removeBlock(content)
	if !changed {
		return content
	}
	if strings.TrimSpace(next) != "" && !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	if strings.TrimSpace(next) != "" {
		next += "\n"
	}
	return next + snippet
}

func replaceTargetBlock(content string, target string, snippet string) string {
	next, changed := removeTargetBlock(content, target)
	if !changed {
		return content
	}
	switch target {
	case "vscode", "cursor", "windsurf", "zed":
		return insertJSONCArrayBlock(next, snippet)
	default:
		if strings.TrimSpace(next) != "" && !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
		if strings.TrimSpace(next) != "" {
			next += "\n"
		}
		return next + snippet
	}
}

func removeBlock(content string) (string, bool) {
	start := strings.Index(content, startMarker)
	end := strings.Index(content, endMarker)
	if start < 0 || end < start {
		return content, false
	}
	end += len(endMarker)
	for end < len(content) && (content[end] == '\n' || content[end] == '\r') {
		end++
	}
	next := content[:start] + content[end:]
	return strings.TrimRight(next, "\n") + "\n", true
}

func insertJSONCArrayBlock(content string, snippet string) string {
	if strings.TrimSpace(content) == "" {
		content = "[]\n"
	}
	index := strings.LastIndex(content, "]")
	if index < 0 {
		content = "[]\n"
		index = strings.LastIndex(content, "]")
	}
	prefix := strings.TrimRight(content[:index], " \t\r\n")
	inner := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(prefix), "["))
	if inner != "" && !strings.HasSuffix(prefix, ",") {
		prefix += ","
	}
	block := indentBlock(strings.TrimRight(snippet, "\r\n"), "  ")
	return prefix + "\n" + block + "\n]\n"
}

func removeTargetBlock(content string, target string) (string, bool) {
	start := strings.Index(content, targetBlockStart(target))
	end := strings.Index(content, targetBlockEnd(target))
	if start < 0 || end < start {
		return content, false
	}
	end += len(targetBlockEnd(target))
	for end < len(content) && (content[end] == '\n' || content[end] == '\r') {
		end++
	}
	next := content[:start] + content[end:]
	next = strings.ReplaceAll(next, ",\n]", "\n]")
	next = strings.ReplaceAll(next, ",\r\n]", "\r\n]")
	return strings.TrimRight(next, "\n") + "\n", true
}

func indentBlock(value string, indent string) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines[index] = indent + line
	}
	return strings.Join(lines, "\n")
}

func targetBlockStart(target string) string {
	switch target {
	case "alacritty":
		return "# >>> " + targetStartMarker + " >>>"
	default:
		return "// >>> " + targetStartMarker + " >>>"
	}
}

func targetBlockEnd(target string) string {
	switch target {
	case "alacritty":
		return "# <<< " + targetStartMarker + " <<<"
	default:
		return "// <<< " + targetStartMarker + " <<<"
	}
}

func targetDisplayName(target string) string {
	switch target {
	case "vscode":
		return "VSCode"
	case "cursor":
		return "Cursor"
	case "windsurf":
		return "Windsurf"
	case "zed":
		return "Zed"
	case "alacritty":
		return "Alacritty"
	case "apple-terminal":
		return "Apple Terminal"
	default:
		return target
	}
}
