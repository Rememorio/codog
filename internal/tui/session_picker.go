package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// SessionChoice is one saved conversation shown by the resume picker.
type SessionChoice struct {
	ID           string
	Title        string
	Workspace    string
	MessageCount int
	UpdatedAt    time.Time
}

// SessionPickerPreview captures deterministic resume-picker state for tests
// and capability reporting without owning a terminal.
type SessionPickerPreview struct {
	View       string
	Query      string
	MatchCount int
	SelectedID string
}

// PreviewSessionPicker filters and optionally selects a saved session.
func PreviewSessionPicker(choices []SessionChoice, query string, width int, height int, selectMatch bool) SessionPickerPreview {
	model := newSessionPickerModel(choices)
	model.query = query
	model.applyFilter()
	if width > 0 {
		model.width = width
	}
	if height > 0 {
		model.height = height
	}
	view := model.View()
	if selectMatch {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(sessionPickerModel)
	}
	return SessionPickerPreview{
		View:       view,
		Query:      model.query,
		MatchCount: len(model.filtered),
		SelectedID: model.selectedID,
	}
}

// SelectSession opens an inline, searchable saved-session picker. An empty
// result means the user canceled the picker.
func SelectSession(ctx context.Context, choices []SessionChoice) (string, error) {
	return SelectSessionWithTheme(ctx, choices, "auto")
}

// SelectSessionWithTheme opens the saved-session picker with the configured
// terminal theme. An empty result means the user canceled the picker.
func SelectSessionWithTheme(ctx context.Context, choices []SessionChoice, theme string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	model := newSessionPickerModelWithTheme(choices, theme)
	final, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	if err != nil {
		return "", err
	}
	selected, ok := final.(sessionPickerModel)
	if !ok || selected.canceled {
		return "", nil
	}
	return selected.selectedID, nil
}

type sessionPickerModel struct {
	choices    []SessionChoice
	filtered   []int
	query      string
	selected   int
	selectedID string
	canceled   bool
	width      int
	height     int
	theme      string
}

func newSessionPickerModel(choices []SessionChoice) sessionPickerModel {
	return newSessionPickerModelWithTheme(choices, "auto")
}

func newSessionPickerModelWithTheme(choices []SessionChoice, theme string) sessionPickerModel {
	theme, ok := NormalizeThemeName(theme)
	if !ok {
		theme = "auto"
	}
	model := sessionPickerModel{
		choices: append([]SessionChoice(nil), choices...),
		width:   80,
		height:  24,
		theme:   theme,
	}
	model.applyFilter()
	return model
}

func (m sessionPickerModel) Init() tea.Cmd {
	return nil
}

func (m sessionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = max(12, msg.Width)
		}
		if msg.Height > 0 {
			m.height = max(6, msg.Height)
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.canceled = true
			return m, tea.Quit
		case "esc":
			if m.query != "" {
				m.query = ""
				m.applyFilter()
				return m, nil
			}
			m.canceled = true
			return m, tea.Quit
		case "up", "ctrl+p":
			m.move(-1)
			return m, nil
		case "down", "ctrl+n":
			m.move(1)
			return m, nil
		case "home", "ctrl+up":
			m.selected = 0
			return m, nil
		case "end", "ctrl+down":
			m.selected = max(0, len(m.filtered)-1)
			return m, nil
		case "pgup":
			m.move(-m.visibleCount())
			return m, nil
		case "pgdown":
			m.move(m.visibleCount())
			return m, nil
		case "backspace", "ctrl+h":
			if m.query != "" {
				runes := []rune(m.query)
				m.query = string(runes[:len(runes)-1])
				m.applyFilter()
			}
			return m, nil
		case "ctrl+u":
			m.query = ""
			m.applyFilter()
			return m, nil
		case "enter":
			if len(m.filtered) == 0 {
				return m, nil
			}
			m.selectedID = m.choices[m.filtered[m.selected]].ID
			return m, tea.Quit
		}
		if msg.Type == tea.KeyRunes {
			m.query += string(msg.Runes)
			m.applyFilter()
		}
	}
	return m, nil
}

func (m sessionPickerModel) View() string {
	if m.canceled || m.selectedID != "" {
		return ""
	}
	width := max(12, m.width)
	contentWidth := max(1, width-2)
	styles := stylesForTheme(m.theme)
	lines := []string{styles.inlineHeader().Render(truncateFooterLine("Resume a session", contentWidth))}
	if len(m.filtered) == 0 {
		lines = append(lines, "  "+styles.completion().Render(truncateFooterLine("No matching sessions", max(1, width-2))))
	} else {
		start, end := m.visibleRange()
		for position := start; position < end; position++ {
			choice := m.choices[m.filtered[position]]
			line := renderSessionChoice(choice, position == m.selected, width, styles)
			lines = append(lines, line)
		}
	}
	query := "Type to filter"
	if m.query != "" {
		query = "Filter: " + m.query
	}
	footer := truncateFooterLine(query+" · ↑/↓ select · Enter resume · Esc cancel", contentWidth)
	lines = append(lines, styles.inlineStatus().Render(footer))
	return strings.Join(lines, "\n")
}

func (m *sessionPickerModel) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(m.query))
	m.filtered = m.filtered[:0]
	for index, choice := range m.choices {
		haystack := strings.ToLower(strings.Join([]string{choice.ID, choice.Title, choice.Workspace}, " "))
		if query == "" || strings.Contains(haystack, query) {
			m.filtered = append(m.filtered, index)
		}
	}
	m.selected = min(max(m.selected, 0), max(0, len(m.filtered)-1))
}

func (m *sessionPickerModel) move(delta int) {
	if len(m.filtered) == 0 {
		m.selected = 0
		return
	}
	m.selected = min(max(m.selected+delta, 0), len(m.filtered)-1)
}

func (m sessionPickerModel) visibleCount() int {
	return min(10, max(3, m.height-4))
}

func (m sessionPickerModel) visibleRange() (int, int) {
	count := m.visibleCount()
	start := max(0, m.selected-count/2)
	end := min(len(m.filtered), start+count)
	start = max(0, end-count)
	return start, end
}

func renderSessionChoice(choice SessionChoice, selected bool, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	marker := "  "
	if selected {
		marker = "❯ "
	}
	title := strings.TrimSpace(choice.Title)
	if title == "" {
		title = choice.ID
	}
	meta := fmt.Sprintf("%d %s", choice.MessageCount, plural("message", choice.MessageCount))
	if !choice.UpdatedAt.IsZero() {
		meta += " · " + relativeSessionTime(choice.UpdatedAt, time.Now())
	}
	if id := abbreviatedSessionID(choice.ID); id != "" && id != title {
		meta += " · " + id
	}
	line := truncateFooterLine(marker+title+"  "+meta, max(1, width))
	if selected {
		return styles.selectedCompletion().Render(line)
	}
	return line
}

func abbreviatedSessionID(id string) string {
	id = strings.TrimSpace(id)
	runes := []rune(id)
	if len(runes) <= 12 {
		return id
	}
	return string(runes[:12])
}

func relativeSessionTime(updated time.Time, now time.Time) string {
	if updated.IsZero() {
		return ""
	}
	delta := now.Sub(updated)
	if delta < 0 {
		delta = 0
	}
	switch {
	case delta < time.Minute:
		return "now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm", int(delta/time.Minute))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh", int(delta/time.Hour))
	case delta < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(delta/(24*time.Hour)))
	default:
		return updated.Format("2006-01-02")
	}
}
