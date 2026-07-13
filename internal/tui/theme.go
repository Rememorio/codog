package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ThemeNames returns the themes offered by the interactive theme picker.
func ThemeNames() []string {
	return []string{
		"auto",
		"dark",
		"light",
		"dark-daltonized",
		"light-daltonized",
		"dark-ansi",
		"light-ansi",
		"no-color",
	}
}

// NormalizeThemeName canonicalizes supported theme names and legacy aliases.
// The second return value is false for unknown names.
func NormalizeThemeName(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "default", "auto", "system":
		return "auto", true
	case "dark":
		return "dark", true
	case "light":
		return "light", true
	case "dark-daltonized", "dark-colorblind", "dark-colorblind-friendly":
		return "dark-daltonized", true
	case "light-daltonized", "light-colorblind", "light-colorblind-friendly":
		return "light-daltonized", true
	case "ansi", "dark-ansi":
		return "dark-ansi", true
	case "light-ansi":
		return "light-ansi", true
	case "no-color", "none", "plain":
		return "no-color", true
	default:
		return "", false
	}
}

// SelectTheme opens an inline theme picker. Moving the selection previews the
// palette; Enter accepts it and Escape cancels without selecting a theme.
func SelectTheme(ctx context.Context, current string, intro bool) (string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	model := newThemePickerModel(current, intro)
	final, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	if err != nil {
		return "", false, err
	}
	selection, ok := final.(themePickerModel)
	return selection.selectedTheme(), ok && selection.accepted, nil
}

type themePalette struct {
	headerForeground    string
	headerBackground    string
	statusForeground    string
	statusBackground    string
	accent              string
	muted               string
	subtle              string
	selectionForeground string
	selectionBackground string
	assistant           string
	tool                string
	permission          string
	question            string
	user                string
	success             string
	error               string
	fallback            string
	noColor             bool
}

type themeStyles struct {
	palette themePalette
}

func stylesForTheme(name string) themeStyles {
	name, ok := NormalizeThemeName(name)
	if !ok {
		name = "auto"
	}
	resolved := name
	if name == "auto" {
		resolved = "dark"
		if !lipgloss.HasDarkBackground() {
			resolved = "light"
		}
	}
	var palette themePalette
	switch resolved {
	case "light":
		palette = themePalette{
			headerForeground: "0", headerBackground: "153", statusForeground: "238", statusBackground: "254", accent: "25", muted: "242", subtle: "240",
			selectionForeground: "15", selectionBackground: "25", assistant: "25", tool: "130",
			permission: "136", question: "30", user: "127", success: "28", error: "160", fallback: "242",
		}
	case "dark-daltonized":
		palette = themePalette{
			headerForeground: "15", headerBackground: "24", statusForeground: "250", statusBackground: "238", accent: "45", muted: "244", subtle: "247",
			selectionForeground: "0", selectionBackground: "220", assistant: "45", tool: "214",
			permission: "220", question: "81", user: "208", success: "45", error: "208", fallback: "250",
		}
	case "light-daltonized":
		palette = themePalette{
			headerForeground: "15", headerBackground: "24", statusForeground: "238", statusBackground: "254", accent: "24", muted: "242", subtle: "240",
			selectionForeground: "0", selectionBackground: "220", assistant: "24", tool: "166",
			permission: "136", question: "31", user: "166", success: "24", error: "166", fallback: "242",
		}
	case "dark-ansi":
		palette = themePalette{
			headerForeground: "15", headerBackground: "4", statusForeground: "7", statusBackground: "8", accent: "6", muted: "8", subtle: "7",
			selectionForeground: "15", selectionBackground: "4", assistant: "6", tool: "3",
			permission: "3", question: "6", user: "5", success: "2", error: "1", fallback: "8",
		}
	case "light-ansi":
		palette = themePalette{
			headerForeground: "15", headerBackground: "4", statusForeground: "0", statusBackground: "7", accent: "4", muted: "8", subtle: "8",
			selectionForeground: "15", selectionBackground: "4", assistant: "4", tool: "3",
			permission: "3", question: "6", user: "5", success: "2", error: "1", fallback: "8",
		}
	case "no-color":
		palette = themePalette{noColor: true}
	default:
		palette = themePalette{
			headerForeground: "15", headerBackground: "62", statusForeground: "250", statusBackground: "238", accent: "39", muted: "244", subtle: "247",
			selectionForeground: "15", selectionBackground: "31", assistant: "39", tool: "214",
			permission: "220", question: "45", user: "205", success: "42", error: "203", fallback: "241",
		}
	}
	return themeStyles{palette: palette}
}

func (s themeStyles) header() lipgloss.Style {
	style := lipgloss.NewStyle().Padding(0, 1)
	if s.palette.noColor {
		return style
	}
	return style.Bold(true).Foreground(lipgloss.Color(s.palette.headerForeground)).Background(lipgloss.Color(s.palette.headerBackground))
}

func (s themeStyles) inlineHeader() lipgloss.Style {
	style := lipgloss.NewStyle().PaddingLeft(2)
	if s.palette.noColor {
		return style
	}
	return style.Bold(true).Foreground(lipgloss.Color(s.palette.accent))
}

func (s themeStyles) status() lipgloss.Style {
	style := lipgloss.NewStyle().Padding(0, 1)
	if s.palette.noColor {
		return style
	}
	return style.Foreground(lipgloss.Color(s.palette.statusForeground)).Background(lipgloss.Color(s.palette.statusBackground))
}

func (s themeStyles) inlineStatus() lipgloss.Style {
	style := lipgloss.NewStyle().PaddingLeft(2)
	if s.palette.noColor {
		return style
	}
	return style.Foreground(lipgloss.Color(s.palette.muted))
}

func (s themeStyles) panelTitle() lipgloss.Style {
	if s.palette.noColor {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(s.palette.accent))
}

func (s themeStyles) completion() lipgloss.Style {
	if s.palette.noColor {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(s.palette.muted))
}

func (s themeStyles) completionTitle() lipgloss.Style {
	if s.palette.noColor {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(s.palette.subtle))
}

func (s themeStyles) selectedCompletion() lipgloss.Style {
	if s.palette.noColor {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(s.palette.selectionForeground)).Background(lipgloss.Color(s.palette.selectionBackground))
}

func (s themeStyles) role(role string) lipgloss.Style {
	if s.palette.noColor {
		return lipgloss.NewStyle()
	}
	color := s.palette.fallback
	switch strings.ToLower(role) {
	case "assistant":
		color = s.palette.assistant
	case "tool":
		color = s.palette.tool
	case "permission":
		color = s.palette.permission
	case "question":
		color = s.palette.question
	case "user":
		color = s.palette.user
	case "success":
		color = s.palette.success
	case "error":
		color = s.palette.error
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(color))
}

func (s themeStyles) applyTextarea(ta *textarea.Model) {
	if ta == nil {
		return
	}
	base := lipgloss.NewStyle()
	prompt := s.panelTitle()
	placeholder := s.completion()
	text := lipgloss.NewStyle()
	if !s.palette.noColor {
		text = text.Foreground(lipgloss.Color(s.palette.subtle))
	}
	ta.FocusedStyle.Base = base
	ta.FocusedStyle.Prompt = prompt
	ta.FocusedStyle.Placeholder = placeholder
	ta.FocusedStyle.Text = text
	ta.BlurredStyle = ta.FocusedStyle
	ta.Cursor.Style = s.panelTitle()
	ta.Focus()
}

type themePickerModel struct {
	options  []string
	selected int
	accepted bool
	intro    bool
	width    int
}

func newThemePickerModel(current string, intro bool) themePickerModel {
	options := ThemeNames()
	current, ok := NormalizeThemeName(current)
	if !ok {
		current = "auto"
	}
	selected := indexOfTheme(options, current)
	return themePickerModel{options: options, selected: selected, intro: intro, width: 80}
}

func (m themePickerModel) Init() tea.Cmd { return nil }

func (m themePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = max(12, msg.Width)
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "left", "shift+tab", "k", "ctrl+p":
			m.selected = (m.selected - 1 + len(m.options)) % len(m.options)
		case "down", "right", "tab", "j", "ctrl+n":
			m.selected = (m.selected + 1) % len(m.options)
		case "home":
			m.selected = 0
		case "end":
			m.selected = len(m.options) - 1
		case "enter":
			m.accepted = true
			return m, tea.Quit
		case "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m themePickerModel) View() string {
	if m.accepted {
		return ""
	}
	styles := stylesForTheme(m.selectedTheme())
	width := max(12, m.width)
	lines := []string{}
	if m.intro {
		lines = append(lines, styles.inlineHeader().Render("Let's get started."))
	}
	lines = append(lines,
		styles.panelTitle().Render("Theme"),
		"Choose the text style that looks best with your terminal",
	)
	for index, option := range m.options {
		marker := "  "
		style := styles.completion()
		if index == m.selected {
			marker = "❯ "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(marker+themeLabel(option)))
	}
	lines = append(lines, "", styles.inlineStatus().Render("↑/↓ preview · Enter select · Esc cancel"))
	for index := range lines {
		lines[index] = truncateFooterLine(lines[index], width)
	}
	return strings.Join(lines, "\n")
}

func (m themePickerModel) selectedTheme() string {
	if m.selected < 0 || m.selected >= len(m.options) {
		return "auto"
	}
	return m.options[m.selected]
}

func indexOfTheme(options []string, current string) int {
	for index, option := range options {
		if option == current {
			return index
		}
	}
	return 0
}

func themeLabel(name string) string {
	switch name {
	case "auto":
		return "Auto (match terminal)"
	case "dark":
		return "Dark mode"
	case "light":
		return "Light mode"
	case "dark-daltonized":
		return "Dark mode (colorblind-friendly)"
	case "light-daltonized":
		return "Light mode (colorblind-friendly)"
	case "dark-ansi":
		return "Dark mode (ANSI colors only)"
	case "light-ansi":
		return "Light mode (ANSI colors only)"
	case "no-color":
		return "No color"
	default:
		return name
	}
}
