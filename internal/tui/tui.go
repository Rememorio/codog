package tui

import (
	"context"
	"errors"
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

// StreamSubmitFunc runs one user prompt and can emit assistant deltas while the
// turn is still running.
type StreamSubmitFunc func(context.Context, string, func(Entry)) (string, error)

// SlashFunc runs one slash command and returns local command output. handled is
// true when the command should not be sent to the model.
type SlashFunc func(context.Context, string) (output string, handled bool, err error)

// ExternalEditorFunc edits the current composer value outside the TUI and
// returns the replacement composer text.
type ExternalEditorFunc func(context.Context, string) (string, error)

// BackgroundFunc starts the current composer prompt in a detached background
// task and returns a user-facing status line.
type BackgroundFunc func(context.Context, string) (string, error)

// ShellOptions configures the full-screen TUI shell.
type ShellOptions struct {
	Candidates       []string
	Prefill          string
	History          []string
	Entries          []Entry
	Submit           SubmitFunc
	SubmitStream     StreamSubmitFunc
	Slash            SlashFunc
	PermissionAnswer func(string)
	QuestionAnswer   func(string)
	ExternalEditor   ExternalEditorFunc
	Background       BackgroundFunc
	ModeLabel        string
	CycleMode        func() string
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
	ctx                context.Context
	textarea           textarea.Model
	viewport           viewport.Model
	result             Result
	width              int
	height             int
	matches            []string
	selected           int
	candidates         []string
	helpOpen           bool
	busy               bool
	status             string
	transcript         []transcriptEntry
	submit             SubmitFunc
	submitStream       StreamSubmitFunc
	slash              SlashFunc
	permissionAnswer   func(string)
	questionAnswer     func(string)
	externalEditor     ExternalEditorFunc
	background         BackgroundFunc
	modeLabel          string
	cycleMode          func() string
	history            []string
	historyPos         int
	draft              string
	queuedPrompts      []string
	searchOpen         bool
	searchHits         []string
	searchPos          int
	turnCancel         context.CancelFunc
	turnMessages       <-chan tea.Msg
	streamingIndex     int
	awaitingPermission bool
	awaitingQuestion   bool
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
	m.refreshSlashMenu()
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
	m.submitStream = options.SubmitStream
	m.slash = options.Slash
	m.permissionAnswer = options.PermissionAnswer
	m.questionAnswer = options.QuestionAnswer
	m.externalEditor = options.ExternalEditor
	m.background = options.Background
	m.modeLabel = strings.TrimSpace(options.ModeLabel)
	m.cycleMode = options.CycleMode
	m.setHistory(options.History)
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
		ctx:            ctx,
		textarea:       ta,
		viewport:       vp,
		candidates:     candidates,
		status:         "ready",
		transcript:     entries,
		historyPos:     -1,
		streamingIndex: -1,
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
		m.turnMessages = nil
		if m.turnCancel != nil {
			m.turnCancel()
			m.turnCancel = nil
		}
		if msg.Interrupted || errors.Is(msg.Err, context.Canceled) {
			m.streamingIndex = -1
			m.queuedPrompts = nil
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Interrupted by user."})
			m.status = "interrupted"
		} else if msg.Err != nil {
			m.streamingIndex = -1
			m.queuedPrompts = nil
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.status = "error"
		} else if strings.TrimSpace(msg.Output) != "" {
			m.finishStreamingOutput(msg.Role, msg.Output)
			m.status = "ready"
		} else {
			m.status = "ready"
		}
		m.streamingIndex = -1
		m.refreshViewport()
		m.viewport.GotoBottom()
		if len(m.queuedPrompts) > 0 && msg.Err == nil && !msg.Interrupted {
			next := m.queuedPrompts[0]
			m.queuedPrompts = append([]string(nil), m.queuedPrompts[1:]...)
			return m.startInput(next)
		}
		return m, nil
	case externalEditorDoneMsg:
		if msg.Err != nil {
			m.status = "editor error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		}
		m.textarea.SetValue(msg.Text)
		m.textarea.CursorEnd()
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "editor updated"
		m.refreshSlashMenu()
		return m, nil
	case backgroundDoneMsg:
		m.busy = false
		if msg.Err != nil {
			m.status = "background error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		}
		m.textarea.SetValue("")
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "backgrounded"
		if strings.TrimSpace(msg.Output) == "" {
			msg.Output = "Background task started."
		}
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: msg.Output})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil
	case turnStreamMsg:
		m.appendStreamDelta(msg.Role, msg.Delta)
		if strings.EqualFold(msg.Role, "permission") {
			m.awaitingPermission = isPermissionRequestDelta(msg.Delta)
			if m.awaitingPermission {
				m.status = "permission"
			} else {
				m.status = "permission answered"
			}
		} else if strings.EqualFold(msg.Role, "question") {
			m.awaitingQuestion = true
			m.status = "question"
		} else {
			m.status = "streaming"
		}
		m.refreshViewport()
		m.viewport.GotoBottom()
		if m.turnMessages != nil {
			return m, waitTurnMessage(m.turnMessages)
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout(msg.Width, msg.Height)
	case tea.KeyMsg:
		if msg.Paste {
			return m.handlePastedInput(msg)
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.busy {
				m.interruptTurn()
				return m, nil
			}
			if len(m.matches) > 0 {
				m.matches = nil
				m.selected = 0
				m.status = m.mode()
				return m, nil
			}
			if m.searchOpen {
				m.closeHistorySearch(false)
				return m, nil
			}
			if m.helpOpen {
				m.helpOpen = false
				m.status = "ready"
				m.refreshViewport()
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+d":
			if !m.busy && !m.searchOpen && !m.helpOpen && strings.TrimSpace(m.textarea.Value()) == "" {
				return m, tea.Quit
			}
		case "ctrl+l":
			if m.busy {
				return m, nil
			}
			m.clearScreen()
			return m, nil
		case "ctrl+b":
			if m.busy || m.background == nil || m.searchOpen {
				return m, nil
			}
			value := strings.TrimSpace(m.textarea.Value())
			if value == "" {
				return m, nil
			}
			m.appendHistory(value)
			m.matches = nil
			m.selected = 0
			m.historyPos = -1
			m.busy = true
			m.status = "backgrounding"
			m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: value})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, runBackgroundCommand(m.ctx, m.background, value)
		case "shift+enter", "alt+enter", "ctrl+j":
			m.textarea.InsertString("\n")
			return m, nil
		case "ctrl+s":
			return m.submitCurrentInput()
		case "ctrl+g":
			if m.busy || m.externalEditor == nil {
				return m, nil
			}
			if m.helpOpen {
				m.helpOpen = false
				m.refreshViewport()
			}
			m.matches = nil
			m.selected = 0
			m.status = "editing"
			return m, runExternalEditorCommand(m.ctx, m.externalEditor, m.textarea.Value())
		case "pgup":
			m.viewport.LineUp(max(1, m.viewport.Height/2))
			return m, nil
		case "pgdown":
			m.viewport.LineDown(max(1, m.viewport.Height/2))
			return m, nil
		case "up":
			if m.searchOpen {
				m.moveHistorySearch(-1)
				return m, nil
			}
			if len(m.matches) > 0 {
				m.selected = (m.selected - 1 + len(m.matches)) % len(m.matches)
				return m, nil
			}
			if m.canNavigateHistory() {
				m.navigateHistory(-1)
				return m, nil
			}
		case "down":
			if m.searchOpen {
				m.moveHistorySearch(1)
				return m, nil
			}
			if len(m.matches) > 0 {
				m.selected = (m.selected + 1) % len(m.matches)
				return m, nil
			}
			if m.canNavigateHistory() {
				m.navigateHistory(1)
				return m, nil
			}
		case "tab":
			if m.busy {
				return m, nil
			}
			if m.searchOpen {
				m.moveHistorySearch(1)
				return m, nil
			}
			m = m.completeSlashCommand()
			return m, nil
		case "shift+tab":
			if m.busy || m.cycleMode == nil {
				return m, nil
			}
			if label := strings.TrimSpace(m.cycleMode()); label != "" {
				m.modeLabel = label
				m.status = m.mode()
				m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Mode: " + label})
				m.refreshViewport()
				m.viewport.GotoBottom()
			}
			return m, nil
		case "ctrl+r":
			if len(m.history) > 0 {
				m.openHistorySearch()
				return m, nil
			}
		case "y", "Y", "a", "A", "n", "N":
			if m.awaitingPermission {
				m.answerPermission(msg.String())
				return m, nil
			}
		case "?":
			if strings.TrimSpace(m.textarea.Value()) == "" {
				m.helpOpen = !m.helpOpen
				m.status = m.mode()
				m.refreshViewport()
				return m, nil
			}
		case "enter":
			if m.busy && m.awaitingQuestion {
				m.answerQuestion()
				return m, nil
			}
			if m.busy {
				m.queueCurrentInput()
				return m, nil
			}
			if m.searchOpen {
				m.closeHistorySearch(true)
				return m, nil
			}
			if m.convertTrailingBackslashToNewline() {
				return m, nil
			}
			if len(m.matches) > 0 {
				m = m.acceptSelectedCompletion()
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
	if m.searchOpen {
		m.updateHistorySearch()
		return m, tea.Batch(cmd, viewportCmd)
	}
	m.refreshSlashMenu()
	if isLocalHelpInput(m.textarea.Value()) {
		m.status = "help ready"
	} else if m.awaitingQuestion {
		m.status = "question"
	} else if m.busy {
		m.status = "running"
	} else {
		m.status = m.mode()
	}
	return m, tea.Batch(cmd, viewportCmd)
}

func (m model) handlePastedInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	text := string(msg.Runes)
	if text == "" {
		return m, nil
	}
	if m.helpOpen {
		m.helpOpen = false
	}
	m.textarea.InsertString(text)
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	if m.searchOpen {
		m.updateHistorySearch()
		return m, nil
	}
	m.refreshSlashMenu()
	if m.awaitingPermission {
		m.status = "permission"
	} else if m.awaitingQuestion {
		m.status = "question"
	} else if m.busy {
		m.status = "running"
	} else {
		lines := pastedLineCount(text)
		m.status = fmt.Sprintf("pasted %d %s", lines, plural("line", lines))
	}
	return m, nil
}

func (m *model) convertTrailingBackslashToNewline() bool {
	value := m.textarea.Value()
	trimmed := strings.TrimRight(value, " \t")
	if !endsWithOddBackslashes(trimmed) {
		return false
	}
	suffix := value[len(trimmed):]
	m.textarea.SetValue(trimmed[:len(trimmed)-1] + "\n" + suffix)
	m.textarea.CursorEnd()
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.status = "newline"
	return true
}

func endsWithOddBackslashes(value string) bool {
	count := 0
	for i := len(value) - 1; i >= 0 && value[i] == '\\'; i-- {
		count++
	}
	return count%2 == 1
}

func pastedLineCount(text string) int {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func plural(word string, count int) string {
	if count == 1 {
		return word
	}
	return word + "s"
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
		composer += "\n" + renderCompletions(m.matches, m.selected)
	}
	if m.searchOpen {
		composer += "\n" + renderHistorySearch(m.searchHits, m.searchPos, m.textarea.Value())
	}
	statusText := appendStatusMode(statusBarText(m.status, m.width), m.modeLabel, m.width)
	status := statusStyle().Width(max(40, m.width)).Render(statusText)
	return strings.Join([]string{title, body, composer, status}, "\n")
}

type turnDoneMsg struct {
	Role        string
	Output      string
	Err         error
	Interrupted bool
}

func (m model) submitCurrentInput() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.textarea.Value())
	return m.startInput(value)
}

func (m model) startInput(value string) (tea.Model, tea.Cmd) {
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
		m.selected = 0
		m.status = "help"
		m.refreshViewport()
		return m, nil
	}
	if strings.HasPrefix(value, "/") && m.slash != nil {
		ctx, cancel := context.WithCancel(m.ctx)
		m.turnCancel = cancel
		m.appendHistory(value)
		m.textarea.SetValue("")
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.busy = true
		m.status = "running slash"
		m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: value})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, runSlashCommand(ctx, m.slash, value)
	}
	if m.submit == nil && m.submitStream == nil {
		m.result = Result{Submitted: true, Prompt: value}
		return m, tea.Quit
	}
	m.appendHistory(value)
	ctx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.busy = true
	m.status = "running"
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: value})
	m.refreshViewport()
	m.viewport.GotoBottom()
	if m.submitStream != nil {
		messages := make(chan tea.Msg, 32)
		m.turnMessages = messages
		return m, runStreamSubmitCommand(ctx, m.submitStream, value, messages)
	}
	return m, runSubmitCommand(ctx, m.submit, value)
}

func (m *model) queueCurrentInput() {
	value := strings.TrimSpace(m.textarea.Value())
	if value == "" || m.awaitingPermission || m.awaitingQuestion {
		return
	}
	m.queuedPrompts = append(m.queuedPrompts, value)
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.status = "queued"
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Queued prompt %d: %s", len(m.queuedPrompts), truncateForComposer(value, 120))})
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func runSubmitCommand(ctx context.Context, submit SubmitFunc, prompt string) tea.Cmd {
	return func() tea.Msg {
		output, err := submit(ctx, prompt)
		return turnDoneMsg{Role: "assistant", Output: output, Err: err, Interrupted: errors.Is(err, context.Canceled)}
	}
}

func runStreamSubmitCommand(ctx context.Context, submit StreamSubmitFunc, prompt string, messages chan tea.Msg) tea.Cmd {
	go func() {
		output, err := submit(ctx, prompt, func(entry Entry) {
			if strings.TrimSpace(entry.Role) == "" {
				entry.Role = "assistant"
			}
			if entry.Text == "" {
				return
			}
			select {
			case messages <- turnStreamMsg{Role: entry.Role, Delta: entry.Text}:
			case <-ctx.Done():
			}
		})
		messages <- turnDoneMsg{Role: "assistant", Output: output, Err: err, Interrupted: errors.Is(err, context.Canceled)}
	}()
	return waitTurnMessage(messages)
}

func waitTurnMessage(messages <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-messages
	}
}

func runSlashCommand(ctx context.Context, slash SlashFunc, line string) tea.Cmd {
	return func() tea.Msg {
		output, handled, err := slash(ctx, line)
		if !handled && err == nil {
			err = fmt.Errorf("unknown slash command: %s", line)
		}
		if handled && err == nil && strings.TrimSpace(output) == "" {
			output = "Done."
		}
		return turnDoneMsg{Role: "system", Output: output, Err: err, Interrupted: errors.Is(err, context.Canceled)}
	}
}

type turnStreamMsg struct {
	Role  string
	Delta string
}

type externalEditorDoneMsg struct {
	Text string
	Err  error
}

type backgroundDoneMsg struct {
	Output string
	Err    error
}

func runExternalEditorCommand(ctx context.Context, editor ExternalEditorFunc, value string) tea.Cmd {
	return func() tea.Msg {
		text, err := editor(ctx, value)
		return externalEditorDoneMsg{Text: text, Err: err}
	}
}

func runBackgroundCommand(ctx context.Context, background BackgroundFunc, prompt string) tea.Cmd {
	return func() tea.Msg {
		output, err := background(ctx, prompt)
		return backgroundDoneMsg{Output: output, Err: err}
	}
}

func (m *model) interruptTurn() {
	if !m.busy {
		return
	}
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.status = "interrupting"
}

func (m *model) answerPermission(answer string) {
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" || m.permissionAnswer == nil {
		return
	}
	switch answer {
	case "y", "yes", "a", "always", "n", "no":
	default:
		return
	}
	m.permissionAnswer(answer)
	m.awaitingPermission = false
	m.status = "permission answered"
}

func (m *model) answerQuestion() {
	if m.questionAnswer == nil {
		return
	}
	answer := strings.TrimSpace(m.textarea.Value())
	m.questionAnswer(answer)
	if answer == "" {
		answer = "(default)"
	}
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: answer})
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.awaitingQuestion = false
	m.status = "question answered"
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) clearScreen() {
	m.helpOpen = false
	m.matches = nil
	m.selected = 0
	m.searchOpen = false
	m.searchHits = nil
	m.searchPos = 0
	m.transcript = []transcriptEntry{{Role: "system", Text: "Screen cleared."}}
	m.status = "cleared"
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func isPermissionRequestDelta(delta string) bool {
	normalized := strings.ToLower(delta)
	return strings.Contains(normalized, " requires ")
}

func (m *model) appendStreamDelta(role string, delta string) {
	if strings.TrimSpace(role) == "" {
		role = "assistant"
	}
	if delta == "" {
		return
	}
	if !strings.EqualFold(role, "assistant") {
		m.transcript = append(m.transcript, transcriptEntry{Role: role, Text: delta})
		m.streamingIndex = len(m.transcript) - 1
		return
	}
	if m.streamingIndex < 0 || m.streamingIndex >= len(m.transcript) || !strings.EqualFold(m.transcript[m.streamingIndex].Role, role) {
		m.transcript = append(m.transcript, transcriptEntry{Role: role, Text: delta})
		m.streamingIndex = len(m.transcript) - 1
		return
	}
	m.transcript[m.streamingIndex].Text += delta
}

func (m *model) finishStreamingOutput(role string, output string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	if m.streamingIndex < 0 || m.streamingIndex >= len(m.transcript) || !strings.EqualFold(m.transcript[m.streamingIndex].Role, role) {
		m.transcript = append(m.transcript, transcriptEntry{Role: role, Text: output})
		return
	}
	current := strings.TrimSpace(m.transcript[m.streamingIndex].Text)
	if current == "" {
		m.transcript[m.streamingIndex].Text = output
		return
	}
	if current == output || strings.Contains(current, output) {
		return
	}
	m.transcript[m.streamingIndex].Text = strings.TrimSpace(current + "\n" + output)
}

func (m model) completeSlashCommand() model {
	value := strings.Trim(m.textarea.Value(), "\r\n\t")
	candidates := slash.FilterCandidates(value, m.completionCandidates())
	switch len(candidates) {
	case 0:
		m.matches = nil
		m.selected = 0
	case 1:
		m.textarea.SetValue(completeValue(candidates[0]))
		m.matches = nil
		m.selected = 0
	default:
		if len(candidates) > 8 {
			candidates = candidates[:8]
		}
		m.matches = candidates
		if m.selected < 0 || m.selected >= len(m.matches) {
			m.selected = 0
		}
	}
	return m
}

func (m *model) refreshSlashMenu() {
	value := strings.Trim(m.textarea.Value(), "\r\n\t")
	if value == "" || !strings.HasPrefix(value, "/") || m.busy || m.searchOpen {
		m.matches = nil
		m.selected = 0
		return
	}
	candidates := slash.FilterCandidates(value, m.completionCandidates())
	candidates = automaticCompletionCandidates(value, candidates)
	if len(candidates) > 8 {
		candidates = candidates[:8]
	}
	m.matches = candidates
	if len(m.matches) == 0 {
		m.selected = 0
		return
	}
	if m.selected < 0 || m.selected >= len(m.matches) {
		m.selected = 0
	}
}

func automaticCompletionCandidates(value string, candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(candidates))
	normalizedValue := strings.TrimSpace(value)
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == normalizedValue && !strings.HasSuffix(candidate, " ") {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func (m model) acceptSelectedCompletion() model {
	if len(m.matches) == 0 {
		return m
	}
	if m.selected < 0 || m.selected >= len(m.matches) {
		m.selected = 0
	}
	m.textarea.SetValue(completeValue(m.matches[m.selected]))
	m.matches = nil
	m.selected = 0
	return m
}

func completeValue(candidate string) string {
	if strings.HasSuffix(candidate, " ") {
		return candidate
	}
	return candidate + " "
}

func renderCompletions(matches []string, selected int) string {
	if len(matches) == 0 {
		return ""
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	lines := []string{completionTitleStyle().Render(" suggestions ")}
	for index, match := range matches {
		prefix := "  "
		style := completionStyle()
		if index == selected {
			prefix = "> "
			style = selectedCompletionStyle()
		}
		lines = append(lines, style.Render(prefix+completionDisplayLine(match)))
	}
	return strings.Join(lines, "\n")
}

func completionDisplayLine(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}
	spec, ok := slash.DescribeCandidate(candidate)
	if !ok || strings.TrimSpace(spec.Description) == "" {
		return candidate
	}
	return truncateForComposer(candidate+"  -  "+spec.Description, 120)
}

func renderHistorySearch(matches []string, selected int, query string) string {
	query = strings.TrimSpace(query)
	title := " history "
	if query != "" {
		title = fmt.Sprintf(" history: %s ", query)
	}
	lines := []string{completionTitleStyle().Render(title)}
	if len(matches) == 0 {
		return strings.Join(append(lines, completionStyle().Render("  no matches")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	for index, match := range matches {
		prefix := "  "
		style := completionStyle()
		if index == selected {
			prefix = "> "
			style = selectedCompletionStyle()
		}
		lines = append(lines, style.Render(prefix+truncateForComposer(match, 100)))
	}
	return strings.Join(lines, "\n")
}

func statusBarText(status string, width int) string {
	status = strings.TrimSpace(status)
	if status == "" {
		status = "ready"
	}
	if strings.EqualFold(status, "permission") {
		switch {
		case width > 0 && width < 70:
			return "permission · y yes · n no · a always"
		default:
			return "permission · y approve · n deny · a always for session"
		}
	}
	if strings.EqualFold(status, "question") {
		switch {
		case width > 0 && width < 70:
			return "question · Enter reply · Esc cancel"
		default:
			return "question · type answer · Enter reply · Esc/Ctrl-C cancel current turn"
		}
	}
	if isBusyStatus(status) {
		switch {
		case width > 0 && width < 70:
			return fmt.Sprintf("%s · Esc cancel", status)
		case width > 0 && width < 90:
			return fmt.Sprintf("%s · Esc/Ctrl-C cancel current turn", status)
		default:
			return fmt.Sprintf("%s · Esc/Ctrl-C cancel current turn · wait for tools to stop", status)
		}
	}
	switch {
	case width > 0 && width < 70:
		return fmt.Sprintf("%s · Enter · Tab · Ctrl-R · Esc", status)
	case width > 0 && width < 90:
		return fmt.Sprintf("%s · Enter send · Shift+Enter newline · Tab · Ctrl-R · Ctrl-D · Esc", status)
	default:
		return fmt.Sprintf("%s · Enter send · Shift+Enter or \\+Enter newline · Tab complete · Ctrl-R history · Ctrl-L clear · Ctrl-D exit", status)
	}
}

func appendStatusMode(status string, mode string, width int) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return status
	}
	suffix := " · " + mode + " · Shift+Tab mode"
	if width > 0 && len([]rune(status+suffix)) > width {
		suffix = " · " + mode
	}
	out := status + suffix
	if width <= 0 || len([]rune(out)) <= width {
		return out
	}
	runes := []rune(out)
	return string(runes[:width])
}

func isBusyStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "running slash", "interrupting", "backgrounding":
		return true
	default:
		return false
	}
}

func (m *model) setHistory(history []string) {
	m.history = normalizeHistory(history)
	m.historyPos = -1
}

func normalizeHistory(history []string) []string {
	seen := map[string]struct{}{}
	reversed := make([]string, 0, len(history))
	for index := len(history) - 1; index >= 0; index-- {
		text := strings.TrimSpace(history[index])
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		reversed = append(reversed, text)
	}
	out := make([]string, 0, len(reversed))
	for index := len(reversed) - 1; index >= 0; index-- {
		out = append(out, reversed[index])
	}
	return out
}

func (m model) canNavigateHistory() bool {
	if len(m.history) == 0 || m.busy || m.helpOpen || m.searchOpen {
		return false
	}
	if strings.Contains(m.textarea.Value(), "\n") {
		return false
	}
	return m.historyPos >= 0 || strings.TrimSpace(m.textarea.Value()) == ""
}

func (m *model) navigateHistory(delta int) {
	if len(m.history) == 0 {
		return
	}
	if m.historyPos < 0 {
		m.draft = m.textarea.Value()
		if delta < 0 {
			m.historyPos = len(m.history) - 1
		} else {
			return
		}
	} else {
		m.historyPos += delta
	}
	if m.historyPos < 0 {
		m.historyPos = -1
		m.textarea.SetValue(m.draft)
		m.status = "compose"
		return
	}
	if m.historyPos >= len(m.history) {
		m.historyPos = -1
		m.textarea.SetValue(m.draft)
		m.status = "compose"
		return
	}
	m.textarea.SetValue(m.history[m.historyPos])
	m.status = fmt.Sprintf("history %d/%d", m.historyPos+1, len(m.history))
}

func (m *model) openHistorySearch() {
	if m.searchOpen {
		return
	}
	m.searchOpen = true
	m.draft = m.textarea.Value()
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.updateHistorySearch()
}

func (m *model) updateHistorySearch() {
	m.searchHits = filterHistory(m.history, m.textarea.Value(), 8)
	if m.searchPos < 0 || m.searchPos >= len(m.searchHits) {
		m.searchPos = 0
	}
	m.status = fmt.Sprintf("history search %d/%d", min(len(m.searchHits), m.searchPos+1), len(m.searchHits))
	if len(m.searchHits) == 0 {
		m.status = "history search"
	}
}

func (m *model) moveHistorySearch(delta int) {
	if len(m.searchHits) == 0 {
		return
	}
	m.searchPos = (m.searchPos + delta + len(m.searchHits)) % len(m.searchHits)
	m.status = fmt.Sprintf("history search %d/%d", m.searchPos+1, len(m.searchHits))
}

func (m *model) closeHistorySearch(accept bool) {
	if accept && len(m.searchHits) > 0 {
		if m.searchPos < 0 || m.searchPos >= len(m.searchHits) {
			m.searchPos = 0
		}
		m.textarea.SetValue(m.searchHits[m.searchPos])
		m.status = "history selected"
	} else {
		m.textarea.SetValue(m.draft)
		m.status = m.mode()
	}
	m.searchOpen = false
	m.searchHits = nil
	m.searchPos = 0
}

func filterHistory(history []string, query string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	query = strings.ToLower(strings.TrimSpace(query))
	out := []string{}
	seen := map[string]struct{}{}
	for index := len(history) - 1; index >= 0 && len(out) < limit; index-- {
		text := strings.TrimSpace(history[index])
		if text == "" {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(text), query) {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func (m *model) appendHistory(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	m.history = append(m.history, value)
	m.history = normalizeHistory(m.history)
}

func truncateForComposer(text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if limit <= 0 {
		limit = 80
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
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
		lines = append(lines, renderTranscriptEntry(entry, max(40, m.viewport.Width-2)))
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

func renderTranscriptEntry(entry transcriptEntry, width int) string {
	role := strings.TrimSpace(entry.Role)
	if role == "" {
		role = "message"
	}
	text := strings.TrimSpace(entry.Text)
	if text == "" {
		text = "(empty)"
	}
	return roleStyle(role).Render(role) + "\n" + wrapTranscriptText(text, width)
}

func wrapTranscriptText(text string, width int) string {
	if width <= 0 {
		return text
	}
	sourceLines := strings.Split(text, "\n")
	out := make([]string, 0, len(sourceLines))
	for _, line := range sourceLines {
		wrapped := wrapTranscriptLine(line, width)
		out = append(out, wrapped...)
	}
	return strings.Join(out, "\n")
}

func wrapTranscriptLine(line string, width int) []string {
	if len([]rune(line)) <= width {
		return []string{line}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}
	out := []string{}
	current := ""
	for _, word := range words {
		for len([]rune(word)) > width {
			if current != "" {
				out = append(out, current)
				current = ""
			}
			runes := []rune(word)
			out = append(out, string(runes[:width]))
			word = string(runes[width:])
		}
		if current == "" {
			current = word
			continue
		}
		if len([]rune(current))+1+len([]rune(word)) <= width {
			current += " " + word
			continue
		}
		out = append(out, current)
		current = word
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

func helpPanel(candidates []string, width int) string {
	if len(candidates) > 12 {
		candidates = candidates[:12]
	}
	sections := []string{
		panelTitleStyle().Render(" help "),
		"Type a prompt and press Enter to submit. Slash commands run locally inside the session.",
		"",
		"Common commands",
		"  /status   inspect workspace and runtime",
		"  /context  inspect prompt context",
		"  /diff     view git changes",
		"  /review   review current diff",
		"  /exit     quit",
		"",
		"Keys",
		"  Enter       submit composer",
		"  Shift+Enter insert newline",
		"  Alt+Enter   insert newline fallback",
		"  \\+Enter     replace trailing backslash with newline",
		"  Ctrl+G      edit composer in $EDITOR",
		"  Ctrl+L      clear screen",
		"  Ctrl+U      delete before cursor",
		"  Ctrl+K      delete after cursor",
		"  Ctrl+D      exit when composer is empty",
		"  Ctrl+R      search prompt history",
		"  Tab         complete slash command",
		"  Up/Down     choose a shown completion",
		"  Up/Down     recall prompt history when composer is empty",
		"  Ctrl+J      insert newline",
		"  PgUp/PgDn   scroll transcript",
		"  Ctrl+B      run composer prompt in background",
		"  ?           toggle this help panel",
		"  Esc         cancel a running turn, close help, or quit",
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

func completionTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
}

func selectedCompletionStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("31"))
}

func roleStyle(role string) lipgloss.Style {
	switch strings.ToLower(role) {
	case "assistant":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	case "tool":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	case "permission":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	case "question":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	case "user":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	default:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241"))
	}
}
