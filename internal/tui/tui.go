package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Rememorio/codog/internal/slash"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Result struct {
	Submitted bool
	Prompt    string
}

// Entry is one transcript item rendered by the full-screen TUI shell.
type Entry struct {
	Role string
	Text string
}

// SubmitFunc runs one user prompt and returns assistant output to append to the
// transcript.
type SubmitFunc func(context.Context, string) (string, error)

// SlashFunc runs one slash command and returns local command output. handled is
// true when the command should not be sent to the model.
type SlashFunc func(context.Context, string) (output string, handled bool, err error)

// ShellOptions configures the full-screen TUI shell.
type ShellOptions struct {
	Candidates []string
	Prefill    string
	Entries    []Entry
	Submit     SubmitFunc
	Slash      SlashFunc
}

// Preview captures a deterministic TUI model state for tests and parity
// harnesses without taking over the terminal.
type Preview struct {
	View      string
	Value     string
	Matches   []string
	Submitted bool
	Prompt    string
	Mode      string
	HelpOpen  bool
}

type model struct {
	ctx        context.Context
	textarea   textarea.Model
	viewport   viewport.Model
	result     Result
	width      int
	height     int
	matches    []string
	candidates []string
	helpOpen   bool
	busy       bool
	status     string
	transcript []transcriptEntry
	submit     SubmitFunc
	slash      SlashFunc
}

type transcriptEntry struct {
	Role string
	Text string
}

func Prompt() (Result, error) {
	return PromptWithCandidates(nil)
}

func PromptWithCandidates(candidates []string) (Result, error) {
	return PromptWithCandidatesPrefill(candidates, "")
}

func PromptWithCandidatesPrefill(candidates []string, prefill string) (Result, error) {
	ta := newPromptTextarea(prefill)
	m := newModel(context.Background(), ta, candidates, nil)
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return Result{}, err
	}
	if done, ok := final.(model); ok {
		return done.result, nil
	}
	return Result{}, nil
}

// PreviewWithCandidates renders the Bubble Tea prompt model after applying
// optional input, window sizing, tab completion, and submission.
func PreviewWithCandidates(input string, candidates []string, width int, height int, complete bool, submit bool) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, candidates, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	if complete {
		m = m.completeSlashCommand()
	}
	if isLocalHelpInput(input) && !submit {
		m.helpOpen = true
		m.textarea.SetValue("")
		m.matches = nil
		m.status = "help"
		m.refreshViewport()
	}
	if submit {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:      m.View(),
		Value:     m.textarea.Value(),
		Matches:   append([]string(nil), m.matches...),
		Submitted: m.result.Submitted,
		Prompt:    m.result.Prompt,
		Mode:      m.mode(),
		HelpOpen:  m.helpOpen,
	}
}

// Shell starts the full-screen interactive TUI loop.
func Shell(ctx context.Context, options ShellOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ta := newPromptTextarea(options.Prefill)
	entries := make([]transcriptEntry, 0, len(options.Entries))
	for _, entry := range options.Entries {
		entries = append(entries, transcriptEntry{Role: entry.Role, Text: entry.Text})
	}
	m := newModel(ctx, ta, options.Candidates, entries)
	m.submit = options.Submit
	m.slash = options.Slash
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func newModel(ctx context.Context, ta textarea.Model, candidates []string, entries []transcriptEntry) model {
	vp := viewport.New(80, 12)
	if ctx == nil {
		ctx = context.Background()
	}
	if len(entries) == 0 {
		entries = []transcriptEntry{
			{Role: "system", Text: "Codog TUI is ready. Compose a prompt, open /help, or use Tab for slash commands."},
		}
	}
	m := model{
		ctx:        ctx,
		textarea:   ta,
		viewport:   vp,
		candidates: candidates,
		status:     "ready",
		transcript: entries,
	}
	m.refreshViewport()
	return m
}

func newPromptTextarea(input string) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask Codog to work on this repository..."
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(8)
	ta.CharLimit = 16000
	ta.SetValue(input)
	return ta
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case turnDoneMsg:
		m.busy = false
		if msg.Err != nil {
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.status = "error"
		} else if strings.TrimSpace(msg.Output) != "" {
			m.transcript = append(m.transcript, transcriptEntry{Role: msg.Role, Text: msg.Output})
			m.status = "ready"
		} else {
			m.status = "ready"
		}
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout(msg.Width, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.helpOpen {
				m.helpOpen = false
				m.status = "ready"
				m.refreshViewport()
				return m, nil
			}
			return m, tea.Quit
		case "alt+enter", "ctrl+j":
			m.textarea.InsertString("\n")
			return m, nil
		case "ctrl+s":
			return m.submitCurrentInput()
		case "tab":
			if m.busy {
				return m, nil
			}
			m = m.completeSlashCommand()
			return m, nil
		case "?":
			if strings.TrimSpace(m.textarea.Value()) == "" {
				m.helpOpen = !m.helpOpen
				m.status = m.mode()
				m.refreshViewport()
				return m, nil
			}
		case "enter":
			if m.busy {
				return m, nil
			}
			if isLocalHelpInput(m.textarea.Value()) {
				m.helpOpen = true
				m.textarea.SetValue("")
				m.matches = nil
				m.status = "help"
				m.refreshViewport()
				return m, nil
			}
			return m.submitCurrentInput()
		}
	}
	var cmd tea.Cmd
	var viewportCmd tea.Cmd
	m.viewport, viewportCmd = m.viewport.Update(msg)
	m.textarea, cmd = m.textarea.Update(msg)
	if strings.TrimSpace(m.textarea.Value()) == "" || !strings.HasPrefix(strings.TrimSpace(m.textarea.Value()), "/") {
		m.matches = nil
	}
	if isLocalHelpInput(m.textarea.Value()) {
		m.status = "help ready"
	} else if m.busy {
		m.status = "running"
	} else {
		m.status = m.mode()
	}
	return m, tea.Batch(cmd, viewportCmd)
}

func (m model) View() string {
	if m.width == 0 {
		m.layout(80, 24)
	}
	title := headerStyle().Width(max(40, m.width)).Render("Codog TUI")
	body := m.viewport.View()
	composerTitle := panelTitleStyle().Render(" composer ")
	composer := composerTitle + "\n" + m.textarea.View()
	if len(m.matches) > 0 {
		composer += "\n" + completionStyle().Render(strings.Join(m.matches, "  "))
	}
	status := statusStyle().Width(max(40, m.width)).Render(fmt.Sprintf("%s · Enter send · Alt+Enter newline · Tab complete · ? help · Esc quit", m.status))
	return strings.Join([]string{title, body, composer, status}, "\n")
}

type turnDoneMsg struct {
	Role   string
	Output string
	Err    error
}

func (m model) submitCurrentInput() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.textarea.Value())
	if value == "" {
		return m, nil
	}
	if isREPLExitInput(value) {
		return m, tea.Quit
	}
	if isLocalHelpInput(value) {
		m.helpOpen = true
		m.textarea.SetValue("")
		m.matches = nil
		m.status = "help"
		m.refreshViewport()
		return m, nil
	}
	if strings.HasPrefix(value, "/") && m.slash != nil {
		m.textarea.SetValue("")
		m.matches = nil
		m.busy = true
		m.status = "running slash"
		m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: value})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, runSlashCommand(m.ctx, m.slash, value)
	}
	if m.submit == nil {
		m.result = Result{Submitted: true, Prompt: value}
		return m, tea.Quit
	}
	m.textarea.SetValue("")
	m.matches = nil
	m.busy = true
	m.status = "running"
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: value})
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m, runSubmitCommand(m.ctx, m.submit, value)
}

func runSubmitCommand(ctx context.Context, submit SubmitFunc, prompt string) tea.Cmd {
	return func() tea.Msg {
		output, err := submit(ctx, prompt)
		return turnDoneMsg{Role: "assistant", Output: output, Err: err}
	}
}

func runSlashCommand(ctx context.Context, slash SlashFunc, line string) tea.Cmd {
	return func() tea.Msg {
		output, handled, err := slash(ctx, line)
		if !handled && err == nil {
			err = fmt.Errorf("unknown slash command: %s", line)
		}
		return turnDoneMsg{Role: "system", Output: output, Err: err}
	}
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

func (m *model) layout(width int, height int) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	m.width = width
	m.height = height
	m.textarea.SetWidth(max(40, width-4))
	m.textarea.SetHeight(4)
	viewportHeight := height - 9
	if viewportHeight < 6 {
		viewportHeight = 6
	}
	m.viewport.Width = max(40, width)
	m.viewport.Height = viewportHeight
	m.refreshViewport()
}

func (m *model) refreshViewport() {
	if m.helpOpen {
		m.viewport.SetContent(helpPanel(m.completionCandidates(), m.viewport.Width))
		return
	}
	lines := []string{}
	for _, entry := range m.transcript {
		lines = append(lines, renderTranscriptEntry(entry))
	}
	m.viewport.SetContent(strings.Join(lines, "\n\n"))
}

func (m model) mode() string {
	if m.helpOpen {
		return "help"
	}
	if len(m.matches) > 0 {
		return fmt.Sprintf("%d completions", len(m.matches))
	}
	value := strings.TrimSpace(m.textarea.Value())
	if strings.HasPrefix(value, "/") {
		return "slash"
	}
	if value == "" {
		return "ready"
	}
	return "compose"
}

func isLocalHelpInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/help", "help", "?":
		return true
	default:
		return false
	}
}

func renderTranscriptEntry(entry transcriptEntry) string {
	role := strings.TrimSpace(entry.Role)
	if role == "" {
		role = "message"
	}
	text := strings.TrimSpace(entry.Text)
	if text == "" {
		text = "(empty)"
	}
	return roleStyle(role).Render(role) + "\n" + text
}

func helpPanel(candidates []string, width int) string {
	if len(candidates) > 12 {
		candidates = candidates[:12]
	}
	sections := []string{
		panelTitleStyle().Render(" help "),
		"Type a prompt and press Enter to submit. Slash commands run locally inside the session.",
		"",
		"Keys",
		"  Enter       submit composer",
		"  Alt+Enter   insert newline",
		"  Ctrl+J      insert newline",
		"  Tab         complete slash command",
		"  ?           toggle this help panel",
		"  Esc         close help or quit",
		"",
		"Common commands",
		"  /status   inspect workspace and runtime",
		"  /context  inspect prompt context",
		"  /diff     view git changes",
		"  /review   review current diff",
		"  /exit     quit",
	}
	if len(candidates) > 0 {
		sections = append(sections, "", "Completions")
		for _, candidate := range candidates {
			sections = append(sections, "  "+candidate)
		}
	}
	return lipgloss.NewStyle().Width(max(40, width-2)).Render(strings.Join(sections, "\n"))
}

func isREPLExitInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/exit", "/quit", "exit", "quit":
		return true
	default:
		return false
	}
}

func headerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
}

func statusStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(lipgloss.Color("238")).Padding(0, 1)
}

func panelTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
}

func completionStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
}

func roleStyle(role string) lipgloss.Style {
	switch strings.ToLower(role) {
	case "assistant":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	case "user":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	default:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241"))
	}
}
