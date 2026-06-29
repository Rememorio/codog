package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Result struct {
	Submitted bool
	Prompt    string
}

type model struct {
	textarea textarea.Model
	result   Result
	width    int
	height   int
}

func Prompt() (Result, error) {
	ta := textarea.New()
	ta.Placeholder = "Ask Codog to work on this repository..."
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(8)
	ta.CharLimit = 16000
	m := model{textarea: ta}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return Result{}, err
	}
	if done, ok := final.(model); ok {
		return done.result, nil
	}
	return Result{}, nil
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textarea.SetWidth(max(40, msg.Width-4))
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "ctrl+s":
			m.result = Result{Submitted: true, Prompt: strings.TrimSpace(m.textarea.Value())}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m model) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("Codog TUI")
	help := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Ctrl+S submit · Esc quit")
	body := m.textarea.View()
	if strings.TrimSpace(body) == "" {
		body = "\n"
	}
	return fmt.Sprintf("%s\n\n%s\n\n%s\n", title, body, help)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
