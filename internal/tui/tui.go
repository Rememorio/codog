package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Rememorio/codog/internal/slash"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Result struct {
	Submitted   bool
	Prompt      string
	Attachments []string
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

// SubmitWithAttachmentsFunc runs one user prompt with staged local attachments.
type SubmitWithAttachmentsFunc func(context.Context, string, []string) (string, error)

// StreamSubmitWithAttachmentsFunc runs one user prompt with staged local
// attachments and can emit assistant deltas while the turn is still running.
type StreamSubmitWithAttachmentsFunc func(context.Context, string, []string, func(Entry)) (string, error)

// SlashFunc runs one slash command and returns local command output. handled is
// true when the command should not be sent to the model.
type SlashFunc func(context.Context, string) (output string, handled bool, err error)

// ExternalEditorFunc edits the current composer value outside the TUI and
// returns the replacement composer text.
type ExternalEditorFunc func(context.Context, string) (string, error)

// PasteContent is clipboard content ready for the TUI composer. Text is
// inserted into the composer; AttachmentPath is staged for the next prompt.
type PasteContent struct {
	Text           string
	AttachmentPath string
	MediaType      string
}

// PasteFunc returns the current system clipboard content for insertion into
// the composer or staging as an attachment.
type PasteFunc func(context.Context) (PasteContent, error)

// BackgroundFunc starts the current composer prompt in a detached background
// task and returns a user-facing status line.
type BackgroundFunc func(context.Context, string) (string, error)

// TaskBoardFunc returns a user-facing snapshot of detached background tasks.
type TaskBoardFunc func(context.Context) (string, error)

// TodoListFunc returns the current workspace todo list for the TUI task panel.
type TodoListFunc func(context.Context) ([]TodoItem, error)

// TodoItem is the small display model used by the TUI todo panel.
type TodoItem struct {
	ID         string
	Content    string
	ActiveForm string
	Status     string
	Priority   string
}

// ShellOptions configures the full-screen TUI shell.
type ShellOptions struct {
	Candidates              []string
	FileCandidates          []string
	Prefill                 string
	History                 []string
	Entries                 []Entry
	Submit                  SubmitFunc
	SubmitStream            StreamSubmitFunc
	SubmitAttachments       SubmitWithAttachmentsFunc
	SubmitStreamAttachments StreamSubmitWithAttachmentsFunc
	Slash                   SlashFunc
	PermissionAnswer        func(string)
	QuestionAnswer          func(string)
	ExternalEditor          ExternalEditorFunc
	Paste                   PasteFunc
	Background              BackgroundFunc
	TaskBoard               TaskBoardFunc
	Todos                   TodoListFunc
	ModeLabel               string
	CycleMode               func() string
}

// Preview captures a deterministic TUI model state for tests and parity
// harnesses without taking over the terminal.
type Preview struct {
	View         string
	Value        string
	Matches      []string
	Submitted    bool
	Prompt       string
	Attachments  []string
	Mode         string
	HelpOpen     bool
	HasStash     bool
	Transcript   bool
	QuickOpen    bool
	GlobalSearch bool
	TodosOpen    bool
}

type composerStash struct {
	Text        string
	Attachments []string
}

type globalSearchMatch struct {
	File string
	Line int
	Text string
}

type model struct {
	ctx                      context.Context
	textarea                 textarea.Model
	viewport                 viewport.Model
	result                   Result
	width                    int
	height                   int
	matches                  []string
	selected                 int
	candidates               []string
	fileCandidates           []string
	helpOpen                 bool
	transcriptMode           bool
	quickOpen                bool
	busy                     bool
	status                   string
	transcript               []transcriptEntry
	submit                   SubmitFunc
	submitStream             StreamSubmitFunc
	submitAttachments        SubmitWithAttachmentsFunc
	submitStreamAttachments  StreamSubmitWithAttachmentsFunc
	slash                    SlashFunc
	permissionAnswer         func(string)
	questionAnswer           func(string)
	externalEditor           ExternalEditorFunc
	paste                    PasteFunc
	background               BackgroundFunc
	taskBoard                TaskBoardFunc
	todos                    TodoListFunc
	modeLabel                string
	cycleMode                func() string
	history                  []string
	historyPos               int
	draft                    string
	quickOpenDraft           string
	quickOpenMatches         []string
	quickOpenSelected        int
	quickOpenPreviewPath     string
	quickOpenPreviewLines    []string
	globalSearch             bool
	globalSearchDraft        string
	globalSearchMatches      []globalSearchMatch
	globalSearchSelected     int
	globalSearchPreviewPath  string
	globalSearchPreviewLine  int
	globalSearchPreviewLines []string
	todosOpen                bool
	todosLoading             bool
	todoItems                []TodoItem
	todoErr                  string
	queuedPrompts            []string
	attachments              []string
	stashedPrompt            *composerStash
	searchOpen               bool
	searchHits               []string
	searchPos                int
	turnCancel               context.CancelFunc
	backgrounding            bool
	backgroundCancel         context.CancelFunc
	turnMessages             <-chan tea.Msg
	streamingIndex           int
	awaitingPermission       bool
	awaitingQuestion         bool
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
	m.refreshCompletionMenu()
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
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      append([]string(nil), m.matches...),
		Submitted:    m.result.Submitted,
		Prompt:       m.result.Prompt,
		Attachments:  append([]string(nil), m.result.Attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
	}
}

// PreviewWithFileCandidates renders a deterministic TUI state with @file
// reference suggestions.
func PreviewWithFileCandidates(input string, files []string, width int, height int, complete bool) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = append([]string(nil), files...)
	m.refreshCompletionMenu()
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	if complete {
		m = m.completeSlashCommand()
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      append([]string(nil), m.matches...),
		Attachments:  append([]string(nil), m.attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
	}
}

// PreviewWithQueued renders a deterministic busy TUI state with queued prompts
// for tests and parity harnesses.
func PreviewWithQueued(input string, queued []string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.busy = true
	m.status = "running"
	m.queuedPrompts = append([]string(nil), queued...)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      append([]string(nil), m.matches...),
		Attachments:  append([]string(nil), m.attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
	}
}

// PreviewWithStash renders a deterministic TUI state after stashing the current
// composer draft.
func PreviewWithStash(input string, attachments []string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = append([]string(nil), attachments...)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if next, ok := updated.(model); ok {
		m = next
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      append([]string(nil), m.matches...),
		Attachments:  append([]string(nil), m.attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
	}
}

// PreviewWithTranscript renders a deterministic TUI state after switching the
// viewport into expanded transcript mode.
func PreviewWithTranscript(entries []Entry, width int, height int) Preview {
	ta := newPromptTextarea("")
	modelEntries := make([]transcriptEntry, 0, len(entries))
	for _, entry := range entries {
		modelEntries = append(modelEntries, transcriptEntry{Role: entry.Role, Text: entry.Text})
	}
	m := newModel(context.Background(), ta, nil, modelEntries)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if next, ok := updated.(model); ok {
		m = next
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      append([]string(nil), m.matches...),
		Attachments:  append([]string(nil), m.attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
	}
}

// PreviewWithQuickOpen renders a deterministic TUI state after opening the file
// picker, typing a query, and optionally accepting the selected file.
func PreviewWithQuickOpen(input string, files []string, query string, width int, height int, accept bool) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = append([]string(nil), files...)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if next, ok := updated.(model); ok {
		m = next
	}
	if query != "" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(query)})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	if accept {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      append([]string(nil), m.quickOpenMatches...),
		Attachments:  append([]string(nil), m.attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
	}
}

// PreviewWithAttachments renders a deterministic TUI state with pending
// attachments staged for the next user prompt.
func PreviewWithAttachments(input string, attachments []string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = append([]string(nil), attachments...)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      append([]string(nil), m.matches...),
		Attachments:  append([]string(nil), m.attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
	}
}

// PreviewWithGlobalSearch renders a deterministic TUI state after opening
// workspace search, typing a query, and optionally accepting the selected match.
func PreviewWithGlobalSearch(input string, files []string, query string, width int, height int, accept bool) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = append([]string(nil), files...)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	if next, ok := updated.(model); ok {
		m = next
	}
	if query != "" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(query)})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	if accept {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      globalSearchMatchLabels(m.globalSearchMatches),
		Attachments:  append([]string(nil), m.attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
	}
}

// PreviewWithPaste renders a deterministic TUI state after inserting clipboard
// text into the composer.
func PreviewWithPaste(input string, clipboardText string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(pasteDoneMsg{Content: PasteContent{Text: clipboardText}})
	if next, ok := updated.(model); ok {
		m = next
	}
	return Preview{
		View:        m.View(),
		Value:       m.textarea.Value(),
		Matches:     append([]string(nil), m.matches...),
		Attachments: append([]string(nil), m.attachments...),
		Mode:        m.mode(),
		HelpOpen:    m.helpOpen,
		HasStash:    m.stashedPrompt != nil,
		Transcript:  m.transcriptMode,
		QuickOpen:   m.quickOpen,
		TodosOpen:   m.todosOpen,
	}
}

// PreviewWithPasteAttachment renders a deterministic TUI state after staging a
// clipboard attachment for the next prompt.
func PreviewWithPasteAttachment(input string, attachmentPath string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(pasteDoneMsg{Content: PasteContent{AttachmentPath: attachmentPath, MediaType: "image/png"}})
	if next, ok := updated.(model); ok {
		m = next
	}
	return Preview{
		View:        m.View(),
		Value:       m.textarea.Value(),
		Matches:     append([]string(nil), m.matches...),
		Attachments: append([]string(nil), m.attachments...),
		Mode:        m.mode(),
		HelpOpen:    m.helpOpen,
		HasStash:    m.stashedPrompt != nil,
		Transcript:  m.transcriptMode,
		QuickOpen:   m.quickOpen,
		TodosOpen:   m.todosOpen,
	}
}

// PreviewWithTodos renders a deterministic TUI state after toggling the todo
// panel with the provided items.
func PreviewWithTodos(input string, items []TodoItem, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.todos = func(context.Context) ([]TodoItem, error) {
		return append([]TodoItem(nil), items...), nil
	}
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if next, ok := updated.(model); ok {
		m = next
	}
	if cmd != nil {
		updated, _ = m.Update(cmd())
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:        m.View(),
		Value:       m.textarea.Value(),
		Matches:     append([]string(nil), m.matches...),
		Attachments: append([]string(nil), m.attachments...),
		Mode:        m.mode(),
		HelpOpen:    m.helpOpen,
		HasStash:    m.stashedPrompt != nil,
		Transcript:  m.transcriptMode,
		QuickOpen:   m.quickOpen,
		TodosOpen:   m.todosOpen,
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
	m.fileCandidates = append([]string(nil), options.FileCandidates...)
	m.submit = options.Submit
	m.submitStream = options.SubmitStream
	m.submitAttachments = options.SubmitAttachments
	m.submitStreamAttachments = options.SubmitStreamAttachments
	m.slash = options.Slash
	m.permissionAnswer = options.PermissionAnswer
	m.questionAnswer = options.QuestionAnswer
	m.externalEditor = options.ExternalEditor
	m.paste = options.Paste
	m.background = options.Background
	m.taskBoard = options.TaskBoard
	m.todos = options.Todos
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
		if m.backgrounding {
			m.status = "backgrounding"
		}
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
		m.refreshCompletionMenu()
		return m, nil
	case pasteDoneMsg:
		if msg.Err != nil {
			m.status = "paste error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		}
		if msg.Content.Text == "" && msg.Content.AttachmentPath == "" {
			m.status = "paste empty"
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Clipboard is empty."})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		}
		return m.insertPasteContent(msg.Content)
	case backgroundDoneMsg:
		m.backgrounding = false
		if m.backgroundCancel != nil {
			m.backgroundCancel()
			m.backgroundCancel = nil
		}
		if msg.Err != nil {
			if errors.Is(msg.Err, context.Canceled) {
				m.status = "background canceled"
				m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Background prompt canceled."})
			} else {
				m.status = "background error"
				m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			}
			if m.busy {
				m.status = "running"
			}
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		}
		m.textarea.SetValue("")
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "backgrounded"
		if m.busy {
			m.status = "running"
		}
		if strings.TrimSpace(msg.Output) == "" {
			msg.Output = "Background task started."
		}
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: msg.Output})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil
	case taskBoardDoneMsg:
		if msg.Err != nil {
			m.status = "tasks error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		}
		output := strings.TrimSpace(msg.Output)
		if output == "" {
			output = "No background tasks."
		}
		m.status = "tasks"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: output})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil
	case todoListDoneMsg:
		m.todosLoading = false
		if msg.Err != nil {
			m.todoErr = msg.Err.Error()
			m.status = "todos error"
			return m, nil
		}
		m.todoErr = ""
		m.todoItems = normalizeTUITodoItems(msg.Items)
		m.status = fmt.Sprintf("todos %d", len(m.todoItems))
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
		if m.globalSearch {
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeGlobalSearch(false, false)
				return m, nil
			case "up", "ctrl+p":
				m.moveGlobalSearch(-1)
				return m, nil
			case "down", "ctrl+n":
				m.moveGlobalSearch(1)
				return m, nil
			case "enter", "tab":
				m.closeGlobalSearch(true, true)
				return m, nil
			case "shift+tab":
				m.closeGlobalSearch(true, false)
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d":
				return m, nil
			}
			var cmd tea.Cmd
			var viewportCmd tea.Cmd
			m.viewport, viewportCmd = m.viewport.Update(msg)
			m.textarea, cmd = m.textarea.Update(msg)
			m.updateGlobalSearch()
			return m, tea.Batch(cmd, viewportCmd)
		}
		if m.quickOpen {
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeQuickOpen(false, false)
				return m, nil
			case "up", "ctrl+p":
				m.moveQuickOpen(-1)
				return m, nil
			case "down", "ctrl+n":
				m.moveQuickOpen(1)
				return m, nil
			case "enter", "tab":
				m.closeQuickOpen(true, true)
				return m, nil
			case "shift+tab":
				m.closeQuickOpen(true, false)
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d":
				return m, nil
			}
			var cmd tea.Cmd
			var viewportCmd tea.Cmd
			m.viewport, viewportCmd = m.viewport.Update(msg)
			m.textarea, cmd = m.textarea.Update(msg)
			m.updateQuickOpen()
			return m, tea.Batch(cmd, viewportCmd)
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.busy {
				m.interruptTurn()
				return m, nil
			}
			if m.backgrounding {
				m.interruptBackground()
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
			if m.todosOpen {
				m.closeTodos()
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
			if !m.busy && !m.searchOpen && !m.quickOpen && !m.globalSearch && !m.todosOpen && !m.helpOpen && strings.TrimSpace(m.textarea.Value()) == "" {
				return m, tea.Quit
			}
		case "ctrl+l":
			if m.busy {
				return m, nil
			}
			m.clearScreen()
			return m, nil
		case "ctrl+v":
			if m.paste == nil || m.busy || m.backgrounding || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			if m.helpOpen {
				m.helpOpen = false
				m.refreshViewport()
			}
			m.matches = nil
			m.selected = 0
			m.status = "pasting"
			return m, runPasteCommand(m.ctx, m.paste)
		case "ctrl+b":
			if m.backgrounding || m.background == nil || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
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
			ctx, cancel := context.WithCancel(m.ctx)
			m.backgrounding = true
			m.backgroundCancel = cancel
			m.status = "backgrounding"
			m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: value})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, runBackgroundCommand(ctx, m.background, value)
		case "ctrl+t":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			if m.todos != nil {
				return m.toggleTodos()
			}
			if m.taskBoard == nil {
				return m, nil
			}
			return m.openTaskBoard()
		case "ctrl+shift+t":
			if m.taskBoard == nil || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			return m.openTaskBoard()
		case "ctrl+shift+p", "ctrl+p":
			if m.busy || m.backgrounding || m.searchOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion || len(m.fileCandidates) == 0 {
				return m, nil
			}
			m.openQuickOpen()
			return m, nil
		case "ctrl+shift+f", "ctrl+f":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.todosOpen || m.awaitingPermission || m.awaitingQuestion || len(m.fileCandidates) == 0 {
				return m, nil
			}
			m.openGlobalSearch()
			return m, nil
		case "shift+enter", "alt+enter", "ctrl+j":
			m.textarea.InsertString("\n")
			return m, nil
		case "ctrl+s":
			m.togglePromptStash()
			return m, nil
		case "ctrl+o":
			if m.helpOpen {
				m.helpOpen = false
			}
			if m.todosOpen {
				m.closeTodos()
			}
			m.transcriptMode = !m.transcriptMode
			if m.transcriptMode {
				m.status = "transcript"
			} else {
				m.status = "ready"
			}
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		case "ctrl+g":
			if m.busy || m.todosOpen || m.externalEditor == nil {
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
			if m.canEditQueuedPrompts() {
				m.editQueuedPrompts()
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
			if !m.todosOpen && len(m.history) > 0 {
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
	if m.quickOpen {
		m.updateQuickOpen()
		return m, tea.Batch(cmd, viewportCmd)
	}
	if m.globalSearch {
		m.updateGlobalSearch()
		return m, tea.Batch(cmd, viewportCmd)
	}
	m.refreshCompletionMenu()
	if isLocalHelpInput(m.textarea.Value()) {
		m.status = "help ready"
	} else if m.awaitingQuestion {
		m.status = "question"
	} else if m.backgrounding {
		m.status = "backgrounding"
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
	return m.insertPastedText(text)
}

func (m model) insertPasteContent(content PasteContent) (tea.Model, tea.Cmd) {
	if content.AttachmentPath != "" {
		return m.stagePastedAttachment(content)
	}
	return m.insertPastedText(content.Text)
}

func (m model) insertPastedText(text string) (tea.Model, tea.Cmd) {
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
	if m.quickOpen {
		m.updateQuickOpen()
		return m, nil
	}
	if m.globalSearch {
		m.updateGlobalSearch()
		return m, nil
	}
	m.refreshCompletionMenu()
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

func (m model) stagePastedAttachment(content PasteContent) (tea.Model, tea.Cmd) {
	if m.helpOpen {
		m.helpOpen = false
	}
	added := addUniqueAttachment(&m.attachments, content.AttachmentPath)
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	label := "clipboard attachment"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(content.MediaType)), "image/") {
		label = "clipboard image"
	}
	if added {
		m.status = label + " attached"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Added %s for the next prompt.\n%s", label, renderAttachmentSummary(m.attachments))})
	} else {
		m.status = "attachment already staged"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: renderAttachmentSummary(m.attachments)})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
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
	if m.quickOpen {
		composer += "\n" + renderQuickOpen(m.quickOpenMatches, m.quickOpenSelected, m.textarea.Value(), m.width, m.quickOpenPreviewPath, m.quickOpenPreviewLines)
	}
	if m.globalSearch {
		composer += "\n" + renderGlobalSearch(m.globalSearchMatches, m.globalSearchSelected, m.textarea.Value(), m.width, m.globalSearchPreviewPath, m.globalSearchPreviewLine, m.globalSearchPreviewLines)
	}
	if m.todosOpen {
		composer += "\n" + renderTodosPanel(m.todoItems, m.todosLoading, m.todoErr, m.width)
	}
	if len(m.queuedPrompts) > 0 {
		composer += "\n" + renderQueuedPrompts(m.queuedPrompts)
	}
	if len(m.attachments) > 0 {
		composer += "\n" + renderPendingAttachments(m.attachments)
	}
	if m.stashedPrompt != nil {
		composer += "\n" + renderStashNotice(m.stashedPrompt)
	}
	statusText := appendStatusMode(statusBarText(m.visibleStatus(), m.width), m.modeLabel, m.width)
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
	if m.handleAttachmentInput(value) {
		return m, nil
	}
	if isLocalPasteInput(value) && m.paste != nil {
		if m.busy || m.backgrounding || m.awaitingPermission || m.awaitingQuestion {
			m.status = "paste unavailable"
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Finish the current turn before pasting clipboard content."})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		}
		m.appendHistory(value)
		m.textarea.SetValue("")
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "pasting"
		return m, runPasteCommand(m.ctx, m.paste)
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
	attachments := append([]string(nil), m.attachments...)
	if m.submit == nil && m.submitStream == nil && m.submitAttachments == nil && m.submitStreamAttachments == nil {
		m.result = Result{Submitted: true, Prompt: value, Attachments: attachments}
		return m, tea.Quit
	}
	m.appendHistory(value)
	ctx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.attachments = nil
	m.busy = true
	m.status = "running"
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: renderSubmittedInput(value, attachments)})
	m.refreshViewport()
	m.viewport.GotoBottom()
	if m.submitStreamAttachments != nil {
		messages := make(chan tea.Msg, 32)
		m.turnMessages = messages
		return m, runStreamSubmitAttachmentsCommand(ctx, m.submitStreamAttachments, value, attachments, messages)
	}
	if m.submitStream != nil {
		messages := make(chan tea.Msg, 32)
		m.turnMessages = messages
		return m, runStreamSubmitCommand(ctx, m.submitStream, value, messages)
	}
	if m.submitAttachments != nil {
		return m, runSubmitAttachmentsCommand(ctx, m.submitAttachments, value, attachments)
	}
	return m, runSubmitCommand(ctx, m.submit, value)
}

func (m *model) queueCurrentInput() {
	value := strings.TrimSpace(m.textarea.Value())
	if value == "" || m.awaitingPermission || m.awaitingQuestion {
		return
	}
	if len(m.attachments) > 0 {
		m.status = "attachments pending"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Send or clear pending attachments before queueing another prompt."})
		m.refreshViewport()
		m.viewport.GotoBottom()
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

func (m model) canEditQueuedPrompts() bool {
	return m.busy &&
		!m.awaitingPermission &&
		!m.awaitingQuestion &&
		!m.searchOpen &&
		len(m.matches) == 0 &&
		len(m.queuedPrompts) > 0
}

func (m *model) editQueuedPrompts() {
	if len(m.queuedPrompts) == 0 {
		return
	}
	count := len(m.queuedPrompts)
	parts := append([]string(nil), m.queuedPrompts...)
	if current := strings.TrimSpace(m.textarea.Value()); current != "" {
		parts = append(parts, current)
	}
	value := strings.Join(parts, "\n")
	m.queuedPrompts = nil
	m.textarea.SetValue(value)
	m.textarea.CursorEnd()
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.status = "editing queued prompts"
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Editing %d queued %s.", count, plural("prompt", count))})
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) togglePromptStash() {
	if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
		return
	}
	if m.helpOpen {
		m.helpOpen = false
	}
	value := m.textarea.Value()
	hasDraft := strings.TrimSpace(value) != "" || len(m.attachments) > 0
	if !hasDraft {
		if m.stashedPrompt == nil {
			m.status = "nothing to stash"
			return
		}
		m.textarea.SetValue(m.stashedPrompt.Text)
		m.textarea.CursorEnd()
		m.attachments = append([]string(nil), m.stashedPrompt.Attachments...)
		m.stashedPrompt = nil
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "stash restored"
		m.refreshCompletionMenu()
		return
	}
	m.stashedPrompt = &composerStash{
		Text:        value,
		Attachments: append([]string(nil), m.attachments...),
	}
	m.textarea.SetValue("")
	m.attachments = nil
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.status = "prompt stashed"
}

func (m *model) handleAttachmentInput(value string) bool {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return false
	}
	command := strings.ToLower(fields[0])
	switch command {
	case "/attach", "/attachments":
	default:
		return false
	}
	if m.busy || m.backgrounding || m.awaitingPermission || m.awaitingQuestion {
		m.status = "attachments unavailable"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Finish the current turn before changing pending attachments."})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return true
	}
	m.appendHistory(value)
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	if len(fields) == 1 || strings.EqualFold(fields[1], "list") {
		m.status = "attachments"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: renderAttachmentSummary(m.attachments)})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return true
	}
	switch strings.ToLower(fields[1]) {
	case "clear":
		count := len(m.attachments)
		m.attachments = nil
		m.status = "attachments cleared"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Cleared %d pending %s.", count, plural("attachment", count))})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return true
	case "remove", "rm", "delete":
		if len(fields) < 3 {
			m.status = "attachment error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: "usage: /attach remove INDEX"})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return true
		}
		if !m.removeAttachment(fields[2]) {
			m.status = "attachment error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: "attachment index is out of range"})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return true
		}
		m.status = "attachment removed"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: renderAttachmentSummary(m.attachments)})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return true
	}
	added := 0
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "--") {
			m.status = "attachment error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: fmt.Sprintf("unknown /attach option %q", field)})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return true
		}
		if addUniqueAttachment(&m.attachments, field) {
			added++
		}
	}
	m.status = fmt.Sprintf("%d attached", len(m.attachments))
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Added %d %s for the next prompt.\n%s", added, plural("attachment", added), renderAttachmentSummary(m.attachments))})
	m.refreshViewport()
	m.viewport.GotoBottom()
	return true
}

func isLocalPasteInput(value string) bool {
	fields := strings.Fields(value)
	return len(fields) == 1 && strings.EqualFold(fields[0], "/paste")
}

func (m *model) removeAttachment(indexText string) bool {
	var index int
	if _, err := fmt.Sscanf(strings.TrimSpace(indexText), "%d", &index); err != nil || index < 1 || index > len(m.attachments) {
		return false
	}
	m.attachments = append(append([]string(nil), m.attachments[:index-1]...), m.attachments[index:]...)
	return true
}

func addUniqueAttachment(attachments *[]string, path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	for _, existing := range *attachments {
		if existing == path {
			return false
		}
	}
	*attachments = append(*attachments, path)
	return true
}

func runSubmitCommand(ctx context.Context, submit SubmitFunc, prompt string) tea.Cmd {
	return func() tea.Msg {
		output, err := submit(ctx, prompt)
		return turnDoneMsg{Role: "assistant", Output: output, Err: err, Interrupted: errors.Is(err, context.Canceled)}
	}
}

func runSubmitAttachmentsCommand(ctx context.Context, submit SubmitWithAttachmentsFunc, prompt string, attachments []string) tea.Cmd {
	return func() tea.Msg {
		output, err := submit(ctx, prompt, append([]string(nil), attachments...))
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

func runStreamSubmitAttachmentsCommand(ctx context.Context, submit StreamSubmitWithAttachmentsFunc, prompt string, attachments []string, messages chan tea.Msg) tea.Cmd {
	go func() {
		output, err := submit(ctx, prompt, append([]string(nil), attachments...), func(entry Entry) {
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

type pasteDoneMsg struct {
	Content PasteContent
	Err     error
}

type backgroundDoneMsg struct {
	Output string
	Err    error
}

type taskBoardDoneMsg struct {
	Output string
	Err    error
}

type todoListDoneMsg struct {
	Items []TodoItem
	Err   error
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

func runTaskBoardCommand(ctx context.Context, taskBoard TaskBoardFunc) tea.Cmd {
	return func() tea.Msg {
		output, err := taskBoard(ctx)
		return taskBoardDoneMsg{Output: output, Err: err}
	}
}

func runTodoListCommand(ctx context.Context, todos TodoListFunc) tea.Cmd {
	return func() tea.Msg {
		items, err := todos(ctx)
		return todoListDoneMsg{Items: items, Err: err}
	}
}

func runPasteCommand(ctx context.Context, paste PasteFunc) tea.Cmd {
	return func() tea.Msg {
		content, err := paste(ctx)
		return pasteDoneMsg{Content: content, Err: err}
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

func (m *model) interruptBackground() {
	if !m.backgrounding {
		return
	}
	if m.backgroundCancel != nil {
		m.backgroundCancel()
	}
	m.status = "canceling background"
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
	m.quickOpen = false
	m.quickOpenMatches = nil
	m.quickOpenSelected = 0
	m.quickOpenDraft = ""
	m.quickOpenPreviewPath = ""
	m.quickOpenPreviewLines = nil
	m.globalSearch = false
	m.globalSearchMatches = nil
	m.globalSearchSelected = 0
	m.globalSearchDraft = ""
	m.globalSearchPreviewPath = ""
	m.globalSearchPreviewLine = 0
	m.globalSearchPreviewLines = nil
	m.todosOpen = false
	m.todosLoading = false
	m.todoItems = nil
	m.todoErr = ""
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
	candidates := m.filteredCompletionCandidates(value)
	switch len(candidates) {
	case 0:
		m.matches = nil
		m.selected = 0
	case 1:
		m.textarea.SetValue(m.completeValue(value, candidates[0]))
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

func (m *model) refreshCompletionMenu() {
	value := strings.Trim(m.textarea.Value(), "\r\n\t")
	if value == "" || m.busy || m.searchOpen || m.globalSearch || m.todosOpen {
		m.matches = nil
		m.selected = 0
		return
	}
	candidates := m.filteredCompletionCandidates(value)
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

func (m model) filteredCompletionCandidates(value string) []string {
	if strings.HasPrefix(value, "/") {
		return slash.FilterCandidates(value, m.completionCandidates())
	}
	if prefix, ok := activeFileReferencePrefix(value); ok {
		return filterFileReferenceCandidates(prefix, m.fileCandidates)
	}
	return nil
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
	m.textarea.SetValue(m.completeValue(m.textarea.Value(), m.matches[m.selected]))
	m.matches = nil
	m.selected = 0
	return m
}

func (m model) completeValue(value string, candidate string) string {
	if strings.HasPrefix(candidate, "@") {
		return completeFileReferenceValue(value, candidate)
	}
	return completeValue(candidate)
}

func completeValue(candidate string) string {
	if strings.HasSuffix(candidate, " ") {
		return candidate
	}
	return candidate + " "
}

func activeFileReferencePrefix(value string) (string, bool) {
	index := strings.LastIndex(value, "@")
	if index < 0 {
		return "", false
	}
	if index > 0 {
		previous := value[index-1]
		if previous != ' ' && previous != '\t' && previous != '\n' && previous != '(' && previous != '[' && previous != '{' {
			return "", false
		}
	}
	token := value[index:]
	if token == "" || strings.ContainsAny(token, " \t\r\n\"'") {
		return "", false
	}
	return token, true
}

func filterFileReferenceCandidates(prefix string, files []string) []string {
	query := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(prefix), "@"))
	out := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		file = strings.TrimSpace(filepathToSlash(file))
		if file == "" {
			continue
		}
		lower := strings.ToLower(file)
		if query != "" && !strings.HasPrefix(lower, query) && !strings.Contains(lower, "/"+query) {
			continue
		}
		candidate := "@" + file
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func filterQuickOpenFileCandidates(query string, files []string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(tokens) == 0 {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		file = strings.TrimSpace(filepathToSlash(file))
		if file == "" || seen[file] {
			continue
		}
		lower := strings.ToLower(file)
		matched := true
		for _, token := range tokens {
			if !strings.Contains(lower, token) && !fuzzySubsequence(token, lower) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		seen[file] = true
		out = append(out, file)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func fuzzySubsequence(query string, candidate string) bool {
	if query == "" {
		return true
	}
	queryRunes := []rune(query)
	pos := 0
	for _, r := range candidate {
		if pos >= len(queryRunes) {
			break
		}
		if r == queryRunes[pos] {
			pos++
		}
	}
	return pos == len(queryRunes)
}

func completeFileReferenceValue(value string, candidate string) string {
	index := strings.LastIndex(value, "@")
	if index < 0 {
		return completeValue(candidate)
	}
	return value[:index] + completeValue(candidate)
}

func insertWithComposerSpacing(base string, insert string) string {
	if strings.TrimSpace(base) == "" {
		return insert
	}
	if strings.HasSuffix(base, " ") || strings.HasSuffix(base, "\t") || strings.HasSuffix(base, "\n") {
		return base + insert
	}
	return base + " " + insert
}

func filepathToSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
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
		if strings.HasPrefix(candidate, "@") {
			return truncateForComposer(candidate+"  -  file reference", 120)
		}
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

func renderQueuedPrompts(queued []string) string {
	if len(queued) == 0 {
		return ""
	}
	lines := []string{completionTitleStyle().Render(fmt.Sprintf(" queued prompts: %d ", len(queued)))}
	start := 0
	if len(queued) > 3 {
		start = len(queued) - 3
		lines = append(lines, completionStyle().Render(fmt.Sprintf("  ... %d earlier", start)))
	}
	for index := start; index < len(queued); index++ {
		lines = append(lines, completionStyle().Render(fmt.Sprintf("  %d. %s", index+1, truncateForComposer(queued[index], 100))))
	}
	return strings.Join(lines, "\n")
}

func renderQuickOpen(matches []string, selected int, query string, width int, previewPath string, previewLines []string) string {
	query = strings.TrimSpace(query)
	title := " quick open "
	if query != "" {
		title = fmt.Sprintf(" quick open: %s ", query)
	}
	lines := []string{completionTitleStyle().Render(title)}
	if query == "" {
		return strings.Join(append(lines, completionStyle().Render("  start typing to search files")), "\n")
	}
	if len(matches) == 0 {
		return strings.Join(append(lines, completionStyle().Render("  no matching files")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	limit := 120
	if width > 0 {
		limit = max(40, width-8)
	}
	for index, match := range matches {
		prefix := "  "
		style := completionStyle()
		if index == selected {
			prefix = "> "
			style = selectedCompletionStyle()
		}
		lines = append(lines, style.Render(prefix+truncateForComposer(match, limit)))
	}
	if previewPath != "" {
		lines = append(lines, completionTitleStyle().Render(" preview "))
		lines = append(lines, completionStyle().Render("  "+truncateForComposer(previewPath, limit)))
		if len(previewLines) == 0 {
			lines = append(lines, completionStyle().Render("  (empty file)"))
		}
		for _, line := range previewLines {
			lines = append(lines, completionStyle().Render("  "+truncateForComposer(line, limit)))
		}
	}
	lines = append(lines, completionStyle().Render("  Enter/Tab insert @file · Shift+Tab insert path · Esc cancel"))
	return strings.Join(lines, "\n")
}

func renderTodosPanel(items []TodoItem, loading bool, errText string, width int) string {
	limit := 120
	if width > 0 {
		limit = max(40, width-8)
	}
	title := " tasks "
	if len(items) > 0 {
		completed, inProgress, pending := todoCounts(items)
		title = fmt.Sprintf(" tasks: %d total, %d done, %d active, %d open ", len(items), completed, inProgress, pending)
	}
	lines := []string{completionTitleStyle().Render(title)}
	switch {
	case loading:
		lines = append(lines, completionStyle().Render("  loading tasks..."))
	case strings.TrimSpace(errText) != "":
		lines = append(lines, completionStyle().Render("  error: "+truncateForComposer(errText, limit)))
	case len(items) == 0:
		lines = append(lines, completionStyle().Render("  no tasks"))
	default:
		visible := items
		if len(visible) > 10 {
			visible = visible[:10]
		}
		for _, item := range visible {
			lines = append(lines, completionStyle().Render("  "+truncateForComposer(renderTodoLine(item), limit)))
		}
		if hidden := len(items) - len(visible); hidden > 0 {
			lines = append(lines, completionStyle().Render(fmt.Sprintf("  ... %d more", hidden)))
		}
	}
	lines = append(lines, completionStyle().Render("  Ctrl+T close · /todos manage tasks · Ctrl+Shift+T background tasks"))
	return strings.Join(lines, "\n")
}

func renderTodoLine(item TodoItem) string {
	status := strings.ToLower(strings.TrimSpace(item.Status))
	marker := "[ ]"
	switch status {
	case "completed":
		marker = "[x]"
	case "in_progress":
		marker = "[~]"
	}
	content := strings.TrimSpace(item.ActiveForm)
	if content == "" {
		content = strings.TrimSpace(item.Content)
	}
	if content == "" {
		content = "(empty task)"
	}
	priority := strings.TrimSpace(item.Priority)
	if priority != "" {
		priority = " " + priority
	}
	id := strings.TrimSpace(item.ID)
	if id != "" {
		id += " "
	}
	return fmt.Sprintf("%s %s%s%s", marker, id, content, priority)
}

func todoCounts(items []TodoItem) (completed int, inProgress int, pending int) {
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "completed":
			completed++
		case "in_progress":
			inProgress++
		default:
			pending++
		}
	}
	return completed, inProgress, pending
}

func normalizeTUITodoItems(items []TodoItem) []TodoItem {
	out := make([]TodoItem, 0, len(items))
	for index, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = fmt.Sprintf("todo-%d", index+1)
		}
		item.Content = strings.TrimSpace(item.Content)
		item.ActiveForm = strings.TrimSpace(item.ActiveForm)
		item.Status = strings.TrimSpace(item.Status)
		if item.Status == "" {
			item.Status = "pending"
		}
		item.Priority = strings.TrimSpace(item.Priority)
		if item.Priority == "" {
			item.Priority = "medium"
		}
		out = append(out, item)
	}
	return out
}

func renderGlobalSearch(matches []globalSearchMatch, selected int, query string, width int, previewPath string, previewLine int, previewLines []string) string {
	query = strings.TrimSpace(query)
	title := " global search "
	if query != "" {
		title = fmt.Sprintf(" global search: %s ", query)
	}
	lines := []string{completionTitleStyle().Render(title)}
	if query == "" {
		return strings.Join(append(lines, completionStyle().Render("  type to search workspace")), "\n")
	}
	if len(matches) == 0 {
		return strings.Join(append(lines, completionStyle().Render("  no matches")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	limit := 120
	if width > 0 {
		limit = max(40, width-8)
	}
	for index, match := range matches {
		prefix := "  "
		style := completionStyle()
		if index == selected {
			prefix = "> "
			style = selectedCompletionStyle()
		}
		label := fmt.Sprintf("%s:%d  %s", match.File, match.Line, strings.TrimSpace(match.Text))
		lines = append(lines, style.Render(prefix+truncateForComposer(label, limit)))
	}
	if previewPath != "" {
		lines = append(lines, completionTitleStyle().Render(" preview "))
		lines = append(lines, completionStyle().Render("  "+truncateForComposer(fmt.Sprintf("%s:%d", previewPath, previewLine), limit)))
		if len(previewLines) == 0 {
			lines = append(lines, completionStyle().Render("  (empty file)"))
		}
		for _, line := range previewLines {
			lines = append(lines, completionStyle().Render("  "+truncateForComposer(line, limit)))
		}
	}
	lines = append(lines, completionStyle().Render("  Enter/Tab insert @file#Lline · Shift+Tab insert path:line · Esc cancel"))
	return strings.Join(lines, "\n")
}

func readQuickOpenPreview(path string, maxLines int, maxBytes int64) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if maxLines <= 0 {
		maxLines = 8
	}
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	info, err := os.Stat(path)
	if err != nil {
		return []string{"(preview unavailable)"}
	}
	if info.IsDir() {
		return []string{"(directory)"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{"(preview unavailable)"}
	}
	truncated := false
	if int64(len(data)) > maxBytes {
		data = data[:maxBytes]
		truncated = true
	}
	if bytes.Contains(data, []byte{0}) || !utf8.Valid(data) {
		return []string{"(binary file)"}
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	parts := strings.Split(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	lines := make([]string, 0, min(len(parts), maxLines)+1)
	for index, line := range parts {
		if index >= maxLines {
			truncated = true
			break
		}
		lines = append(lines, line)
	}
	if truncated {
		lines = append(lines, "(preview truncated)")
	}
	return lines
}

func searchWorkspaceFiles(query string, files []string, limit int, maxMatchesPerFile int, maxBytes int64) []globalSearchMatch {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	if maxMatchesPerFile <= 0 {
		maxMatchesPerFile = 5
	}
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	out := []globalSearchMatch{}
	seen := map[string]bool{}
	for _, file := range files {
		file = strings.TrimSpace(filepathToSlash(file))
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		info, err := os.Stat(file)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if int64(len(data)) > maxBytes {
			data = data[:maxBytes]
		}
		if bytes.Contains(data, []byte{0}) || !utf8.Valid(data) {
			continue
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		perFile := 0
		for index, line := range strings.Split(text, "\n") {
			if !strings.Contains(strings.ToLower(line), query) {
				continue
			}
			out = append(out, globalSearchMatch{File: file, Line: index + 1, Text: line})
			perFile++
			if len(out) >= limit {
				return out
			}
			if perFile >= maxMatchesPerFile {
				break
			}
		}
	}
	return out
}

func readGlobalSearchPreview(path string, line int, contextLines int, maxBytes int64) []string {
	path = strings.TrimSpace(path)
	if path == "" || line <= 0 {
		return nil
	}
	if contextLines < 0 {
		contextLines = 2
	}
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return []string{"(preview unavailable)"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{"(preview unavailable)"}
	}
	truncated := false
	if int64(len(data)) > maxBytes {
		data = data[:maxBytes]
		truncated = true
	}
	if bytes.Contains(data, []byte{0}) || !utf8.Valid(data) {
		return []string{"(binary file)"}
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	parts := strings.Split(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return nil
	}
	index := line - 1
	if index < 0 {
		index = 0
	}
	if index >= len(parts) {
		index = len(parts) - 1
	}
	start := max(0, index-contextLines)
	end := min(len(parts), index+contextLines+1)
	out := make([]string, 0, end-start+1)
	for current := start; current < end; current++ {
		marker := " "
		if current == index {
			marker = ">"
		}
		out = append(out, fmt.Sprintf("%s%4d: %s", marker, current+1, parts[current]))
	}
	if truncated {
		out = append(out, "(preview truncated)")
	}
	return out
}

func globalSearchMatchLabels(matches []globalSearchMatch) []string {
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, fmt.Sprintf("%s:%d", match.File, match.Line))
	}
	return out
}

func globalSearchReference(match globalSearchMatch, mention bool) string {
	if mention {
		return fmt.Sprintf("@%s#L%d ", match.File, match.Line)
	}
	return fmt.Sprintf("%s:%d ", match.File, match.Line)
}

func renderPendingAttachments(attachments []string) string {
	if len(attachments) == 0 {
		return ""
	}
	lines := []string{completionTitleStyle().Render(fmt.Sprintf(" attachments: %d ", len(attachments)))}
	start := 0
	if len(attachments) > 4 {
		start = len(attachments) - 4
		lines = append(lines, completionStyle().Render(fmt.Sprintf("  ... %d earlier", start)))
	}
	for index := start; index < len(attachments); index++ {
		lines = append(lines, completionStyle().Render(fmt.Sprintf("  %d. %s", index+1, truncateForComposer(attachments[index], 100))))
	}
	return strings.Join(lines, "\n")
}

func renderStashNotice(stash *composerStash) string {
	if stash == nil {
		return ""
	}
	summary := truncateForComposer(strings.Join(strings.Fields(stash.Text), " "), 80)
	if summary == "" {
		summary = fmt.Sprintf("%d pending %s", len(stash.Attachments), plural("attachment", len(stash.Attachments)))
	}
	lines := []string{completionTitleStyle().Render(" stashed prompt ")}
	lines = append(lines, completionStyle().Render("  Ctrl+S restore: "+summary))
	if len(stash.Attachments) > 0 {
		lines = append(lines, completionStyle().Render(fmt.Sprintf("  attachments: %d", len(stash.Attachments))))
	}
	return strings.Join(lines, "\n")
}

func renderAttachmentSummary(attachments []string) string {
	if len(attachments) == 0 {
		return "No pending attachments."
	}
	lines := []string{fmt.Sprintf("Pending attachments: %d", len(attachments))}
	for index, attachment := range attachments {
		lines = append(lines, fmt.Sprintf("  %d. %s", index+1, attachment))
	}
	return strings.Join(lines, "\n")
}

func renderSubmittedInput(prompt string, attachments []string) string {
	prompt = strings.TrimSpace(prompt)
	if len(attachments) == 0 {
		return prompt
	}
	lines := []string{prompt, "", "Attachments:"}
	for _, attachment := range attachments {
		lines = append(lines, "- "+attachment)
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
	if strings.HasPrefix(strings.ToLower(status), "quick open") {
		switch {
		case width > 0 && width < 80:
			return "quick open · type · Enter · Esc"
		default:
			return "quick open · type to search · Enter/Tab insert @file · Shift+Tab path · Esc cancel"
		}
	}
	if strings.HasPrefix(strings.ToLower(status), "global search") {
		switch {
		case width > 0 && width < 80:
			return "global search · type · Enter · Esc"
		default:
			return "global search · type to search · Enter/Tab insert @line · Shift+Tab path:line · Esc cancel"
		}
	}
	if strings.HasPrefix(strings.ToLower(status), "todos") || strings.EqualFold(status, "loading todos") {
		switch {
		case width > 0 && width < 80:
			return "tasks · Ctrl+T close"
		default:
			return "tasks · Ctrl+T close · /todos manage tasks · Ctrl+Shift+T background tasks"
		}
	}
	switch {
	case width > 0 && width < 70:
		return fmt.Sprintf("%s · Enter · Tab · Ctrl-R · Esc", status)
	case width > 0 && width < 90:
		return fmt.Sprintf("%s · Enter send · Shift+Enter newline · Tab · Ctrl-R · Ctrl-D · Esc", status)
	case width > 0 && width < 110:
		return fmt.Sprintf("%s · Enter send · Shift+Enter newline · Tab complete · Ctrl-R history · Ctrl-L clear · Ctrl-D exit", status)
	default:
		return fmt.Sprintf("%s · Enter send · Shift+Enter or \\+Enter newline · Tab complete · Ctrl-R history · Ctrl+Shift+P files · Ctrl+Shift+F search · Ctrl+T tasks · Ctrl-O transcript · Ctrl-L clear · Ctrl-D exit", status)
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
	case "running", "running slash", "interrupting", "backgrounding", "canceling background":
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
	if len(m.history) == 0 || m.busy || m.helpOpen || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen {
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

func (m model) openTaskBoard() (tea.Model, tea.Cmd) {
	if m.helpOpen {
		m.helpOpen = false
	}
	m.matches = nil
	m.selected = 0
	m.status = "loading tasks"
	m.refreshViewport()
	return m, runTaskBoardCommand(m.ctx, m.taskBoard)
}

func (m model) toggleTodos() (tea.Model, tea.Cmd) {
	if m.todosOpen {
		m.closeTodos()
		return m, nil
	}
	m.todosOpen = true
	m.todosLoading = true
	m.todoErr = ""
	m.todoItems = nil
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.status = "loading todos"
	return m, runTodoListCommand(m.ctx, m.todos)
}

func (m *model) closeTodos() {
	m.todosOpen = false
	m.todosLoading = false
	m.todoErr = ""
	m.todoItems = nil
	m.status = m.mode()
}

func (m *model) openQuickOpen() {
	if m.quickOpen {
		return
	}
	m.quickOpen = true
	m.quickOpenDraft = m.textarea.Value()
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.searchOpen = false
	m.searchHits = nil
	m.searchPos = 0
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.updateQuickOpen()
}

func (m *model) updateQuickOpen() {
	m.quickOpenMatches = filterQuickOpenFileCandidates(m.textarea.Value(), m.fileCandidates, 8)
	if m.quickOpenSelected < 0 || m.quickOpenSelected >= len(m.quickOpenMatches) {
		m.quickOpenSelected = 0
	}
	if len(m.quickOpenMatches) == 0 {
		m.quickOpenPreviewPath = ""
		m.quickOpenPreviewLines = nil
		m.status = "quick open"
		return
	}
	m.refreshQuickOpenPreview()
	m.status = fmt.Sprintf("quick open %d/%d", m.quickOpenSelected+1, len(m.quickOpenMatches))
}

func (m *model) moveQuickOpen(delta int) {
	if len(m.quickOpenMatches) == 0 {
		return
	}
	m.quickOpenSelected = (m.quickOpenSelected + delta + len(m.quickOpenMatches)) % len(m.quickOpenMatches)
	m.refreshQuickOpenPreview()
	m.status = fmt.Sprintf("quick open %d/%d", m.quickOpenSelected+1, len(m.quickOpenMatches))
}

func (m *model) refreshQuickOpenPreview() {
	if len(m.quickOpenMatches) == 0 {
		m.quickOpenPreviewPath = ""
		m.quickOpenPreviewLines = nil
		return
	}
	if m.quickOpenSelected < 0 || m.quickOpenSelected >= len(m.quickOpenMatches) {
		m.quickOpenSelected = 0
	}
	path := m.quickOpenMatches[m.quickOpenSelected]
	if path == m.quickOpenPreviewPath && len(m.quickOpenPreviewLines) > 0 {
		return
	}
	m.quickOpenPreviewPath = path
	m.quickOpenPreviewLines = readQuickOpenPreview(path, 8, 32*1024)
}

func (m *model) closeQuickOpen(accept bool, mention bool) {
	if accept && len(m.quickOpenMatches) > 0 {
		if m.quickOpenSelected < 0 || m.quickOpenSelected >= len(m.quickOpenMatches) {
			m.quickOpenSelected = 0
		}
		selected := m.quickOpenMatches[m.quickOpenSelected]
		insert := selected + " "
		if mention {
			insert = "@" + insert
		}
		m.textarea.SetValue(insertWithComposerSpacing(m.quickOpenDraft, insert))
		m.textarea.CursorEnd()
		if mention {
			m.status = "file referenced"
		} else {
			m.status = "path inserted"
		}
	} else {
		m.textarea.SetValue(m.quickOpenDraft)
		m.textarea.CursorEnd()
		m.status = m.mode()
	}
	m.quickOpen = false
	m.quickOpenDraft = ""
	m.quickOpenMatches = nil
	m.quickOpenSelected = 0
	m.quickOpenPreviewPath = ""
	m.quickOpenPreviewLines = nil
	m.matches = nil
	m.selected = 0
	m.refreshCompletionMenu()
}

func (m *model) openGlobalSearch() {
	if m.globalSearch {
		return
	}
	m.globalSearch = true
	m.globalSearchDraft = m.textarea.Value()
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.searchOpen = false
	m.searchHits = nil
	m.searchPos = 0
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.updateGlobalSearch()
}

func (m *model) updateGlobalSearch() {
	m.globalSearchMatches = searchWorkspaceFiles(m.textarea.Value(), m.fileCandidates, 50, 5, 256*1024)
	if m.globalSearchSelected < 0 || m.globalSearchSelected >= len(m.globalSearchMatches) {
		m.globalSearchSelected = 0
	}
	if len(m.globalSearchMatches) == 0 {
		m.globalSearchPreviewPath = ""
		m.globalSearchPreviewLine = 0
		m.globalSearchPreviewLines = nil
		m.status = "global search"
		return
	}
	m.refreshGlobalSearchPreview()
	m.status = fmt.Sprintf("global search %d/%d", m.globalSearchSelected+1, len(m.globalSearchMatches))
}

func (m *model) moveGlobalSearch(delta int) {
	if len(m.globalSearchMatches) == 0 {
		return
	}
	m.globalSearchSelected = (m.globalSearchSelected + delta + len(m.globalSearchMatches)) % len(m.globalSearchMatches)
	m.refreshGlobalSearchPreview()
	m.status = fmt.Sprintf("global search %d/%d", m.globalSearchSelected+1, len(m.globalSearchMatches))
}

func (m *model) refreshGlobalSearchPreview() {
	if len(m.globalSearchMatches) == 0 {
		m.globalSearchPreviewPath = ""
		m.globalSearchPreviewLine = 0
		m.globalSearchPreviewLines = nil
		return
	}
	if m.globalSearchSelected < 0 || m.globalSearchSelected >= len(m.globalSearchMatches) {
		m.globalSearchSelected = 0
	}
	selected := m.globalSearchMatches[m.globalSearchSelected]
	if selected.File == m.globalSearchPreviewPath && selected.Line == m.globalSearchPreviewLine && len(m.globalSearchPreviewLines) > 0 {
		return
	}
	m.globalSearchPreviewPath = selected.File
	m.globalSearchPreviewLine = selected.Line
	m.globalSearchPreviewLines = readGlobalSearchPreview(selected.File, selected.Line, 2, 64*1024)
}

func (m *model) closeGlobalSearch(accept bool, mention bool) {
	if accept && len(m.globalSearchMatches) > 0 {
		if m.globalSearchSelected < 0 || m.globalSearchSelected >= len(m.globalSearchMatches) {
			m.globalSearchSelected = 0
		}
		insert := globalSearchReference(m.globalSearchMatches[m.globalSearchSelected], mention)
		m.textarea.SetValue(insertWithComposerSpacing(m.globalSearchDraft, insert))
		m.textarea.CursorEnd()
		if mention {
			m.status = "line referenced"
		} else {
			m.status = "location inserted"
		}
	} else {
		m.textarea.SetValue(m.globalSearchDraft)
		m.textarea.CursorEnd()
		m.status = m.mode()
	}
	m.globalSearch = false
	m.globalSearchDraft = ""
	m.globalSearchMatches = nil
	m.globalSearchSelected = 0
	m.globalSearchPreviewPath = ""
	m.globalSearchPreviewLine = 0
	m.globalSearchPreviewLines = nil
	m.matches = nil
	m.selected = 0
	m.refreshCompletionMenu()
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
	for index, entry := range m.transcript {
		lines = append(lines, renderTranscriptEntry(entry, max(40, m.viewport.Width-2), index, len(m.transcript), m.transcriptMode))
	}
	m.viewport.SetContent(strings.Join(lines, "\n\n"))
}

func (m model) mode() string {
	if m.helpOpen {
		return "help"
	}
	if m.quickOpen {
		return "quick open"
	}
	if m.globalSearch {
		return "global search"
	}
	if m.todosOpen {
		return "todos"
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

func (m model) visibleStatus() string {
	status := strings.TrimSpace(m.status)
	if status == "" {
		status = m.mode()
	}
	if m.transcriptMode && !strings.EqualFold(status, "transcript") {
		status += " · transcript"
	}
	if len(m.queuedPrompts) == 0 {
		if len(m.attachments) == 0 {
			return status
		}
		return fmt.Sprintf("%s · %d attached", status, len(m.attachments))
	}
	if len(m.attachments) == 0 {
		return fmt.Sprintf("%s · %d queued", status, len(m.queuedPrompts))
	}
	return fmt.Sprintf("%s · %d queued · %d attached", status, len(m.queuedPrompts), len(m.attachments))
}

func isLocalHelpInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/help", "help", "?":
		return true
	default:
		return false
	}
}

func renderTranscriptEntry(entry transcriptEntry, width int, index int, total int, transcriptMode bool) string {
	role := strings.TrimSpace(entry.Role)
	if role == "" {
		role = "message"
	}
	if !transcriptMode {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			text = "(empty)"
		}
		return roleStyle(role).Render(role) + "\n" + wrapTranscriptText(text, width)
	}
	text := entry.Text
	if text == "" {
		text = "(empty)"
	}
	header := fmt.Sprintf("%03d/%03d %s · %d %s · %d %s", index+1, max(1, total), role, transcriptLineCount(text), plural("line", transcriptLineCount(text)), len([]rune(text)), plural("char", len([]rune(text))))
	return roleStyle(role).Render(header) + "\n" + wrapTranscriptText(text, width)
}

func transcriptLineCount(text string) int {
	if text == "" {
		return 0
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Count(text, "\n") + 1
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
		"Type a prompt, @path file reference, or slash command. Enter submits.",
		"",
		"Common commands",
		"  /status   inspect workspace and runtime",
		"  /context  inspect context; /attach files; /paste clipboard",
		"  /diff     view git changes",
		"  /review   review current diff",
		"  /exit     quit",
		"",
		"Keys",
		"  Enter       submit composer",
		"  Shift+Enter insert newline",
		"  Alt+Enter   insert newline fallback",
		"  \\+Enter     replace trailing backslash with newline",
		"  Ctrl+S      stash or restore composer",
		"  Ctrl+G      edit composer in $EDITOR",
		"  Ctrl+V      paste clipboard text or image",
		"  Ctrl+Shift+P quick open files",
		"  Ctrl+P      quick open fallback",
		"  Ctrl+Shift+F search workspace",
		"  Ctrl+F      search workspace fallback",
		"  Ctrl+T      toggle tasks",
		"  Ctrl+O      toggle expanded transcript",
		"  Ctrl+L      clear screen",
		"  Ctrl+U      delete before cursor",
		"  Ctrl+K      delete after cursor",
		"  Ctrl+D      exit when composer is empty",
		"  Ctrl+R      search prompt history",
		"  Tab         complete slash command or @file reference",
		"  Up/Down     choose a shown completion",
		"  Up          edit queued prompts while a turn is running",
		"  Up/Down     recall prompt history when composer is empty",
		"  Ctrl+J      insert newline",
		"  PgUp/PgDn   scroll transcript",
		"  Ctrl+B      run composer prompt in background",
		"  Ctrl+Shift+T show background task board",
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
