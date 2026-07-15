package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const welcomeWideWidth = 72

var welcomeLogo = []string{
	"   ____          __",
	"  / ___|___   __| | ___   __ _",
	" | |   / _ \\ / _` |/ _ \\ / _` |",
	" | |__| (_) | (_| | (_) | (_| |",
	"  \\____\\___/ \\__,_|\\___/ \\__, |",
	"                         |___/",
}

type welcomeInfo struct {
	Version    string
	Model      string
	Permission string
	Workspace  string
	GitBranch  string
}

func renderWelcome(info welcomeInfo, width int, styles themeStyles) string {
	width = max(8, width)
	if width < 40 {
		return renderCompactWelcome(info, width, styles)
	}

	logo := make([]string, len(welcomeLogo))
	logoWidth := 0
	for index, line := range welcomeLogo {
		logo[index] = styles.panelTitle().Render(line)
		logoWidth = max(logoWidth, lipgloss.Width(line))
	}
	if width < welcomeWideWidth {
		return strings.Join(append(append(logo, ""), renderWelcomeMetadata(info, width, styles)...), "\n")
	}

	metadataWidth := max(8, width-logoWidth-4)
	metadata := renderWelcomeMetadata(info, metadataWidth, styles)
	lines := make([]string, 0, len(logo))
	for index, line := range logo {
		right := ""
		if index < len(metadata) {
			right = metadata[index]
		}
		lines = append(lines, line+strings.Repeat(" ", logoWidth-lipgloss.Width(line)+4)+right)
	}
	return strings.Join(lines, "\n")
}

func renderCompactWelcome(info welcomeInfo, width int, styles themeStyles) string {
	lines := []string{welcomeName(info)}
	if model := strings.TrimSpace(info.Model); model != "" {
		lines = append(lines, "model · "+model)
	}
	if permission := strings.TrimSpace(info.Permission); permission != "" {
		lines = append(lines, "permission · "+permission)
	}
	if workspace := welcomeWorkspace(info); workspace != "" {
		lines = append(lines, workspace)
	}
	for index, line := range lines {
		style := styles.completion()
		if index == 0 {
			style = styles.panelTitle()
		}
		lines[index] = style.Render(truncateFooterLine(line, width))
	}
	return strings.Join(lines, "\n")
}

func renderWelcomeMetadata(info welcomeInfo, width int, styles themeStyles) []string {
	lines := []string{styles.panelTitle().Render(truncateFooterLine(welcomeName(info), width))}
	rows := []struct {
		label string
		value string
	}{
		{label: "model", value: info.Model},
		{label: "permission", value: info.Permission},
		{label: "workspace", value: welcomeWorkspace(info)},
	}
	for _, row := range rows {
		if value := strings.TrimSpace(row.value); value != "" {
			line := row.label + strings.Repeat(" ", max(1, 12-len(row.label))) + value
			lines = append(lines, styles.completion().Render(truncateFooterLine(line, width)))
		}
	}
	return lines
}

func welcomeName(info welcomeInfo) string {
	name := "Codog"
	if version := strings.TrimSpace(info.Version); version != "" {
		name += " " + version
	}
	return name
}

func welcomeWorkspace(info welcomeInfo) string {
	workspace := displayWelcomePath(info.Workspace)
	branch := strings.TrimSpace(info.GitBranch)
	if workspace == "" {
		return branch
	}
	if branch != "" {
		workspace += " · " + branch
	}
	return workspace
}

func displayWelcomePath(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if relative, relErr := filepath.Rel(home, path); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			path = filepath.Join("~", relative)
		}
	}
	return filepath.ToSlash(path)
}
