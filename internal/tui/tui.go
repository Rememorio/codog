package tui

import (
	"fmt"
	"strings"

	"github.com/Rememorio/codog/internal/slash"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Result struct {
	Submitted bool
	Prompt    string
}

type model struct {
	textarea   textarea.Model
	result     Result
	width      int
	height     int
	matches    []string
	candidates []string
}

func Prompt() (Result, error) {
	return PromptWithCandidates(nil)
}

func PromptWithCandidates(candidates []string) (Result, error) {
	ta := textarea.New()
	ta.Placeholder = "Ask Codog to work on this repository..."
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(8)
	ta.CharLimit = 16000
	m := model{textarea: ta, candidates: candidates}
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
		case "tab":
			m = m.completeSlashCommand()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	if strings.TrimSpace(m.textarea.Value()) == "" || !strings.HasPrefix(strings.TrimSpace(m.textarea.Value()), "/") {
		m.matches = nil
	}
	return m, cmd
}

func (m model) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("Codog TUI")
	help := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Ctrl+S submit · Tab complete · Esc quit")
	body := m.textarea.View()
	if strings.TrimSpace(body) == "" {
		body = "\n"
	}
	if len(m.matches) > 0 {
		body += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(strings.Join(m.matches, "  "))
	}
	return fmt.Sprintf("%s\n\n%s\n\n%s\n", title, body, help)
}

func (m model) completeSlashCommand() model {
	value := strings.Trim(m.textarea.Value(), "\r\n\t")
	candidates := slash.FilterCandidates(value, m.completionCandidates())
	switch len(candidates) {
	case 0:
		m.matches = nil
	case 1:
		m.textarea.SetValue(completeValue(candidates[0]))
		m.matches = nil
	default:
		if len(candidates) > 8 {
			candidates = candidates[:8]
		}
		m.matches = candidates
	}
	return m
}

func completeValue(candidate string) string {
	if strings.HasSuffix(candidate, " ") {
		return candidate
	}
	return candidate + " "
}

func (m model) completionCandidates() []string {
	if len(m.candidates) > 0 {
		return m.candidates
	}
	return slash.AllCandidates(slash.CandidateOptions{})
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
