package terminalsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	startMarker = "# >>> codog shell integration >>>"
	endMarker   = "# <<< codog shell integration <<<"
)

type Options struct {
	Action string
	Shell  string
	Path   string
	Force  bool
}

type Report struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
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
	shell := NormalizeShell(opts.Shell)
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
		return Report{}, fmt.Errorf("unknown terminal setup action %q", action)
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

func DetectShell(envShell string) string {
	return NormalizeShell(envShell)
}

func DefaultPath(shell string) (string, error) {
	shell = NormalizeShell(shell)
	if shell == "" {
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
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

func Snippet(shell string) (string, error) {
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
		return "", fmt.Errorf("unsupported shell %q", shell)
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
