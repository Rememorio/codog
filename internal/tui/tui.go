package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
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

// Entry is one transcript item rendered by the interactive TUI shell.
type Entry struct {
	Role       string
	Text       string
	Permission *PermissionRequest
	Question   *QuestionRequest
	Tool       *ToolActivity
}

// ToolActivity is one model-requested tool call as it moves from running to a
// terminal success or error state.
type ToolActivity struct {
	ID      string
	Name    string
	Input   string
	Output  string
	Status  string
	IsError bool
}

// PermissionRequest describes a tool confirmation shown by the interactive
// shell. Text remains available on Entry as a transcript fallback.
type PermissionRequest struct {
	Tool          string
	Required      string
	Input         string
	Message       string
	SuggestedRule string
	AllowAlways   bool
}

// PermissionResponse is the user's structured answer to a permission request.
type PermissionResponse struct {
	Decision string
	Feedback string
	Rule     string
}

// QuestionRequest describes a structured AskUserQuestionTool interaction.
type QuestionRequest struct {
	Question  string
	Choices   []string
	Default   string
	Questions []Question
}

// Question is one tab in a structured user-question interaction.
type Question struct {
	Question    string
	Header      string
	Options     []QuestionOption
	MultiSelect bool
}

// QuestionOption is one selectable answer and its supporting context.
type QuestionOption struct {
	Label       string
	Description string
	Preview     string
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
type RuntimeControlFunc func(context.Context) (RuntimeControlResult, error)
type ModelSelectFunc func(context.Context, string) (RuntimeControlResult, error)

// ThemeSelectFunc persists a theme selected from the live TUI picker.
type ThemeSelectFunc func(context.Context, string) (RuntimeControlResult, error)
type ConversationRestoreFunc func(context.Context, int) (RuntimeControlResult, error)
type ConversationForkFunc func(context.Context, int) (RuntimeControlResult, error)
type ConversationSummarizeFunc func(context.Context, int) (RuntimeControlResult, error)
type MessageCopyFunc func(context.Context, string) (RuntimeControlResult, error)

// TodoItem is the small display model used by the TUI todo panel.
type TodoItem struct {
	ID         string
	Content    string
	ActiveForm string
	Status     string
	Priority   string
}

// RuntimeControlResult describes a runtime TUI setting change.
type RuntimeControlResult struct {
	Title  string
	Status string
	Lines  []string
	Badges []string
}

// ShellOptions configures the interactive TUI shell.
type ShellOptions struct {
	Candidates                []string
	FileCandidates            []string
	Prefill                   string
	InitialPrompt             string
	InitialAttachments        []string
	History                   []string
	Entries                   []Entry
	Submit                    SubmitFunc
	SubmitStream              StreamSubmitFunc
	SubmitAttachments         SubmitWithAttachmentsFunc
	SubmitStreamAttachments   StreamSubmitWithAttachmentsFunc
	Slash                     SlashFunc
	PermissionAnswer          func(string)
	PermissionRespond         func(PermissionResponse)
	QuestionAnswer            func(string)
	ExternalEditor            ExternalEditorFunc
	Paste                     PasteFunc
	Background                BackgroundFunc
	TaskBoard                 TaskBoardFunc
	Todos                     TodoListFunc
	ModelOptions              []string
	CurrentModel              string
	SelectModel               ModelSelectFunc
	Theme                     string
	SelectTheme               ThemeSelectFunc
	ToggleFast                RuntimeControlFunc
	ToggleThinking            RuntimeControlFunc
	StopBackground            RuntimeControlFunc
	CompactSession            RuntimeControlFunc
	UndoLast                  RuntimeControlFunc
	ExportConversation        RuntimeControlFunc
	CopyConversation          RuntimeControlFunc
	RestoreConversation       ConversationRestoreFunc
	ForkConversation          ConversationForkFunc
	SummarizeConversation     ConversationSummarizeFunc
	SummarizeUpToConversation ConversationSummarizeFunc
	CopyMessage               MessageCopyFunc
	ModeLabel                 string
	RuntimeBadges             []string
	VimMode                   bool
	Keybindings               map[string][]string
	ContextKeybindings        map[string]map[string][]string
	CycleMode                 func() string
	// FullScreen opts into the alternate-screen renderer. The default inline
	// renderer keeps completed conversation turns in terminal scrollback.
	FullScreen bool
}

// Preview captures a deterministic TUI model state for tests and parity
// harnesses without taking over the terminal.
type Preview struct {
	View            string
	Value           string
	Matches         []string
	Submitted       bool
	Prompt          string
	Attachments     []string
	Mode            string
	HelpOpen        bool
	HasStash        bool
	Transcript      bool
	QuickOpen       bool
	GlobalSearch    bool
	TodosOpen       bool
	ModelPicker     bool
	ThemePicker     bool
	MessageMenu     bool
	AttachmentsOpen bool
	DiffDialog      bool
	CommandHint     string
	InlineHint      string
	Quit            bool
}

// DiffSource describes one diff source rendered by the TUI diff dialog.
type DiffSource struct {
	Name     string
	Subtitle string
	Files    []DiffFile
}

// DiffFile describes one changed file rendered by the TUI diff dialog.
type DiffFile struct {
	Path    string
	Status  string
	Summary string
	Diff    string
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
	ctx                       context.Context
	textarea                  textarea.Model
	viewport                  viewport.Model
	result                    Result
	width                     int
	height                    int
	matches                   []string
	selected                  int
	commandArgumentHint       string
	inlineGhostText           string
	candidates                []string
	fileCandidates            []string
	helpOpen                  bool
	transcriptMode            bool
	quickOpen                 bool
	busy                      bool
	status                    string
	transcript                []transcriptEntry
	submit                    SubmitFunc
	submitStream              StreamSubmitFunc
	submitAttachments         SubmitWithAttachmentsFunc
	submitStreamAttachments   StreamSubmitWithAttachmentsFunc
	slash                     SlashFunc
	permissionAnswer          func(string)
	permissionRespond         func(PermissionResponse)
	questionAnswer            func(string)
	externalEditor            ExternalEditorFunc
	paste                     PasteFunc
	background                BackgroundFunc
	taskBoard                 TaskBoardFunc
	todos                     TodoListFunc
	modeLabel                 string
	runtimeBadges             []string
	vimEnabled                bool
	vimNormal                 bool
	vimOperator               string
	keybindings               map[string]map[string]bool
	contextKeybindings        map[string]map[string]map[string]bool
	cycleMode                 func() string
	history                   []string
	historyPos                int
	draft                     string
	undoStack                 []string
	keyChordPrefix            string
	ctrlXChord                bool
	quickOpenDraft            string
	quickOpenMatches          []string
	quickOpenSelected         int
	quickOpenPreviewPath      string
	quickOpenPreviewLines     []string
	globalSearch              bool
	globalSearchDraft         string
	globalSearchMatches       []globalSearchMatch
	globalSearchSelected      int
	globalSearchPreviewPath   string
	globalSearchPreviewLine   int
	globalSearchPreviewLines  []string
	todosOpen                 bool
	todosLoading              bool
	todoItems                 []TodoItem
	todoErr                   string
	modelPicker               bool
	modelOptions              []string
	currentModel              string
	modelPickerSelected       int
	selectModel               ModelSelectFunc
	theme                     string
	themePicker               bool
	themePickerSelected       int
	themePickerOriginal       string
	selectTheme               ThemeSelectFunc
	toggleFast                RuntimeControlFunc
	toggleThinking            RuntimeControlFunc
	stopBackground            RuntimeControlFunc
	compactSession            RuntimeControlFunc
	undoLast                  RuntimeControlFunc
	exportConversation        RuntimeControlFunc
	copyConversation          RuntimeControlFunc
	restoreConversation       ConversationRestoreFunc
	forkConversation          ConversationForkFunc
	summarizeConversation     ConversationSummarizeFunc
	summarizeUpToConversation ConversationSummarizeFunc
	copyMessage               MessageCopyFunc
	messageActions            bool
	messageActionTarget       int
	messageActionSelected     int
	queuedPrompts             []string
	initialPrompt             string
	attachments               []string
	attachmentsOpen           bool
	attachmentSelected        int
	diffDialog                bool
	diffSources               []DiffSource
	diffSourceSelected        int
	diffFileSelected          int
	diffDetail                bool
	exitPending               bool
	exitKey                   string
	inline                    bool
	printedEntries            int
	initialPrint              string
	stashedPrompt             *composerStash
	searchOpen                bool
	searchHits                []string
	searchPos                 int
	turnCancel                context.CancelFunc
	backgrounding             bool
	backgroundCancel          context.CancelFunc
	turnMessages              <-chan tea.Msg
	streamingIndex            int
	awaitingPermission        bool
	awaitingQuestion          bool
	permissionRequest         *PermissionRequest
	permissionSelected        int
	permissionInput           bool
	permissionInputAnswer     string
	permissionAcceptFeedback  string
	permissionRejectFeedback  string
	permissionRule            string
	permissionComposerDraft   string
	permissionDraftCaptured   bool
	questionRequest           *QuestionRequest
	questionSelected          int
	questionCustom            bool
	questionIndex             int
	questionLegacy            bool
	questionCursors           []int
	questionSelections        [][]bool
	questionCustomValues      []string
}

type transcriptEntry struct {
	Role string
	Text string
	Tool *ToolActivity
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
	m.inline = false
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
	return previewWithCandidates(input, candidates, width, height, complete, submit, false)
}

// PreviewInlineWithCandidates renders the default inline shell presentation
// without starting a terminal program.
func PreviewInlineWithCandidates(input string, candidates []string, width int, height int, complete bool, submit bool) Preview {
	return previewWithCandidates(input, candidates, width, height, complete, submit, true)
}

func previewWithCandidates(input string, candidates []string, width int, height int, complete bool, submit bool, inline bool) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, candidates, nil)
	m.inline = inline
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
		CommandHint:  m.commandArgumentHint,
		InlineHint:   m.inlineGhostText,
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
		CommandHint:  m.commandArgumentHint,
		InlineHint:   m.inlineGhostText,
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

// PreviewWithEscape renders the TUI after pressing Escape the requested number
// of times. It is used to verify safe clear/exit behavior without owning a
// terminal.
func PreviewWithEscape(input string, presses int, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	quit := false
	for index := 0; index < presses; index++ {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if next, ok := updated.(model); ok {
			m = next
		}
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				quit = true
			}
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
		CommandHint: m.commandArgumentHint,
		InlineHint:  m.inlineGhostText,
		Quit:        quit,
	}
}

// PreviewWithBashMode renders the TUI after submitting a leading ! command.
// The command is routed through the same slash dispatcher used by /run.
func PreviewWithBashMode(input string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	captured := ""
	m.slash = func(_ context.Context, line string) (string, bool, error) {
		captured = line
		return "bash ok: " + line, true, nil
	}
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	m.refreshCompletionMenu()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
		Prompt:      captured,
		Attachments: append([]string(nil), m.attachments...),
		Mode:        m.mode(),
		HelpOpen:    m.helpOpen,
		HasStash:    m.stashedPrompt != nil,
		Transcript:  m.transcriptMode,
		QuickOpen:   m.quickOpen,
		TodosOpen:   m.todosOpen,
		CommandHint: m.commandArgumentHint,
		InlineHint:  m.inlineGhostText,
	}
}

// PreviewWithBashHistory renders bash history ghost completion for tests and
// parity harnesses.
func PreviewWithBashHistory(input string, history []string, files []string, width int, height int, complete bool) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.setHistory(history)
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
		CommandHint: m.commandArgumentHint,
		InlineHint:  m.inlineGhostText,
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
		modelEntries = append(modelEntries, transcriptEntry{Role: entry.Role, Text: entry.Text, Tool: cloneToolActivity(entry.Tool)})
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

// PreviewWithAttachmentRemoval renders the TUI after removing the most recent
// pending attachment through the keyboard chord.
func PreviewWithAttachmentRemoval(input string, attachments []string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = append([]string(nil), attachments...)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if next, ok := updated.(model); ok {
		m = next
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
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

// PreviewWithAttachmentNavigation renders the TUI attachment selector after
// opening it and applying the provided navigation keys.
func PreviewWithAttachmentNavigation(input string, attachments []string, keys []string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = append([]string(nil), attachments...)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	m.textarea.SetValue("/attachments")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if next, ok := updated.(model); ok {
		m = next
	}
	for _, key := range keys {
		updated, _ = m.Update(attachmentPreviewKey(key))
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:            m.View(),
		Value:           m.textarea.Value(),
		Matches:         append([]string(nil), m.matches...),
		Attachments:     append([]string(nil), m.attachments...),
		Mode:            m.mode(),
		HelpOpen:        m.helpOpen,
		HasStash:        m.stashedPrompt != nil,
		Transcript:      m.transcriptMode,
		QuickOpen:       m.quickOpen,
		GlobalSearch:    m.globalSearch,
		TodosOpen:       m.todosOpen,
		ModelPicker:     m.modelPicker,
		MessageMenu:     m.messageActions,
		AttachmentsOpen: m.attachmentsOpen,
	}
}

func attachmentPreviewKey(key string) tea.KeyMsg {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "delete":
		return tea.KeyMsg{Type: tea.KeyDelete}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "esc", "escape":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

// PreviewWithDiffDialog renders a deterministic TUI diff dialog after applying
// the provided navigation keys.
func PreviewWithDiffDialog(sources []DiffSource, keys []string, width int, height int) Preview {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	m.openDiffDialog(sources)
	for _, key := range keys {
		updated, _ := m.Update(diffPreviewKey(key))
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:            m.View(),
		Value:           m.textarea.Value(),
		Matches:         append([]string(nil), m.matches...),
		Attachments:     append([]string(nil), m.attachments...),
		Mode:            m.mode(),
		HelpOpen:        m.helpOpen,
		HasStash:        m.stashedPrompt != nil,
		Transcript:      m.transcriptMode,
		QuickOpen:       m.quickOpen,
		GlobalSearch:    m.globalSearch,
		TodosOpen:       m.todosOpen,
		ModelPicker:     m.modelPicker,
		MessageMenu:     m.messageActions,
		AttachmentsOpen: m.attachmentsOpen,
		DiffDialog:      m.diffDialog,
	}
}

func diffPreviewKey(key string) tea.KeyMsg {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc", "escape":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
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

// PreviewWithModelPicker renders a deterministic TUI state after opening the
// runtime model picker.
func PreviewWithModelPicker(input string, models []string, current string, width int, height int, accept bool) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.modelOptions = normalizeModelOptions(models)
	m.currentModel = current
	m.selectModel = func(_ context.Context, model string) (RuntimeControlResult, error) {
		return RuntimeControlResult{Title: "Model", Status: "model selected", Lines: []string{"Model: " + model}}, nil
	}
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}, Alt: true})
	if next, ok := updated.(model); ok {
		m = next
	}
	if accept {
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if next, ok := updated.(model); ok {
			m = next
		}
		if cmd != nil {
			updated, _ = m.Update(cmd())
			if next, ok := updated.(model); ok {
				m = next
			}
		}
	} else if cmd != nil {
		_ = cmd
	}
	return Preview{
		View:        m.View(),
		Value:       m.textarea.Value(),
		Matches:     append([]string(nil), m.modelOptions...),
		Attachments: append([]string(nil), m.attachments...),
		Mode:        m.mode(),
		HelpOpen:    m.helpOpen,
		HasStash:    m.stashedPrompt != nil,
		Transcript:  m.transcriptMode,
		QuickOpen:   m.quickOpen,
		TodosOpen:   m.todosOpen,
		ModelPicker: m.modelPicker,
	}
}

// PreviewWithRuntimeToggle renders a deterministic TUI state after invoking a
// runtime control shortcut such as Alt-O or Alt-T.
func PreviewWithRuntimeToggle(input string, key string, result RuntimeControlResult, width int, height int) Preview {
	return PreviewWithRuntimeControl(input, key, result, width, height)
}

// PreviewWithRuntimeControl renders a deterministic TUI state after invoking a
// runtime control shortcut.
func PreviewWithRuntimeControl(input string, key string, result RuntimeControlResult, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	control := func(context.Context) (RuntimeControlResult, error) {
		return result, nil
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "alt+o", "meta+o":
		m.toggleFast = control
	case "alt+t", "meta+t":
		m.toggleThinking = control
	case "ctrl+x ctrl+u":
		m.undoLast = control
	case "ctrl+x ctrl+s":
		m.exportConversation = control
	case "ctrl+x ctrl+y":
		m.copyConversation = control
	}
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, cmd := m.Update(runtimeControlPreviewKey(key, false))
	if runtimeControlPreviewChord(key) {
		if next, ok := updated.(model); ok {
			m = next
		}
		updated, cmd = m.Update(runtimeControlPreviewKey(key, true))
	}
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
		ModelPicker: m.modelPicker,
	}
}

// PreviewWithVimMode renders a deterministic TUI state after applying vim
// editor-mode keys.
func PreviewWithVimMode(input string, keys []string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.vimEnabled = true
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	for _, key := range keys {
		updated, _ := m.Update(vimPreviewKey(key))
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

// PreviewWithKeybindings renders a deterministic TUI state after applying a
// custom TUI keybinding or keybinding chord.
func PreviewWithKeybindings(input string, bindings map[string][]string, files []string, key string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = append([]string(nil), files...)
	m.keybindings = normalizeTUIKeybindings(bindings)
	m.externalEditor = func(_ context.Context, value string) (string, error) {
		return "edited: " + value, nil
	}
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	for _, keyPart := range strings.Fields(key) {
		updated, cmd := m.Update(previewKey(keyPart))
		if next, ok := updated.(model); ok {
			m = next
		}
		if cmd != nil {
			updated, _ = m.Update(cmd())
			if next, ok := updated.(model); ok {
				m = next
			}
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

// PreviewWithContextKeybindings renders a deterministic modal state after
// applying custom keybindings for TUI sub-contexts such as tui-modal,
// tui-attachments, and tui-diff.
func PreviewWithContextKeybindings(target string, bindings map[string]map[string][]string, keys []string, width int, height int) Preview {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, []transcriptEntry{
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "second prompt"},
		{Role: "assistant", Text: "second answer"},
	})
	m.contextKeybindings = normalizeTUIContextKeybindings(bindings)
	m.modelOptions = []string{"alpha", "beta", "gamma"}
	m.currentModel = "alpha"
	m.selectModel = func(_ context.Context, model string) (RuntimeControlResult, error) {
		return RuntimeControlResult{Title: "Model", Status: "model selected", Lines: []string{"Model: " + model}}, nil
	}
	m.attachments = []string{"one.txt", "two.txt", "three.txt"}
	m.diffSources = normalizeDiffSources([]DiffSource{
		{
			Name:     "Uncommitted changes",
			Subtitle: "git diff HEAD",
			Files: []DiffFile{
				{Path: "main.go", Status: "modified", Summary: "+2 -1", Diff: "@@ main.go\n-old\n+new"},
				{Path: "main_test.go", Status: "added", Summary: "+8", Diff: "+func TestMain() {}"},
			},
		},
		{
			Name:     "Turn 2",
			Subtitle: "tests",
			Files: []DiffFile{
				{Path: "agent.go", Status: "modified", Summary: "+4", Diff: "+updated"},
			},
		},
	})
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	switch normalizeTUIContextTarget(target) {
	case "model-picker":
		m.openModelPicker()
	case "message-actions":
		m.openMessageActions()
	case "attachments":
		m.openAttachmentsPanel()
	case "diff":
		m.openDiffDialog(m.diffSources)
	case "quick-open":
		m.fileCandidates = []string{"internal/tui/tui.go", "internal/agent/agent.go"}
		m.openQuickOpen()
	case "global-search":
		m.fileCandidates = []string{"main.go"}
		m.openGlobalSearch()
		m.globalSearchDraft = "main"
		m.updateGlobalSearch()
	default:
		m.openModelPicker()
	}
	for _, key := range keys {
		updated, cmd := m.Update(previewKey(key))
		if next, ok := updated.(model); ok {
			m = next
		}
		if cmd != nil {
			updated, _ = m.Update(cmd())
			if next, ok := updated.(model); ok {
				m = next
			}
		}
	}
	return Preview{
		View:            m.View(),
		Value:           m.textarea.Value(),
		Matches:         append([]string(nil), m.matches...),
		Attachments:     append([]string(nil), m.attachments...),
		Mode:            m.mode(),
		HelpOpen:        m.helpOpen,
		HasStash:        m.stashedPrompt != nil,
		Transcript:      m.transcriptMode,
		QuickOpen:       m.quickOpen,
		GlobalSearch:    m.globalSearch,
		TodosOpen:       m.todosOpen,
		ModelPicker:     m.modelPicker,
		MessageMenu:     m.messageActions,
		AttachmentsOpen: m.attachmentsOpen,
		DiffDialog:      m.diffDialog,
	}
}

func normalizeTUIContextTarget(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	target = strings.ReplaceAll(target, "_", "-")
	return strings.Join(strings.Fields(target), "-")
}

func vimPreviewKey(key string) tea.KeyMsg {
	return previewKey(key)
}

func previewKey(key string) tea.KeyMsg {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case "esc", "escape":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "delete":
		return tea.KeyMsg{Type: tea.KeyDelete}
	case "shift+up":
		return tea.KeyMsg{Type: tea.KeyShiftUp}
	case "shift+down":
		return tea.KeyMsg{Type: tea.KeyShiftDown}
	default:
		if key, ok := previewControlKey(normalized); ok {
			return key
		}
		if strings.HasPrefix(normalized, "alt+") || strings.HasPrefix(normalized, "meta+") {
			token := strings.TrimPrefix(strings.TrimPrefix(normalized, "alt+"), "meta+")
			runes := []rune(token)
			if len(runes) > 0 {
				return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runes[0]}, Alt: true}
			}
		}
		runes := []rune(key)
		if len(runes) == 0 {
			return tea.KeyMsg{}
		}
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runes[0]}}
	}
}

func previewControlKey(key string) (tea.KeyMsg, bool) {
	if !strings.HasPrefix(key, "ctrl+") {
		return tea.KeyMsg{}, false
	}
	switch strings.TrimPrefix(key, "ctrl+") {
	case "a":
		return tea.KeyMsg{Type: tea.KeyCtrlA}, true
	case "b":
		return tea.KeyMsg{Type: tea.KeyCtrlB}, true
	case "c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}, true
	case "d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}, true
	case "e":
		return tea.KeyMsg{Type: tea.KeyCtrlE}, true
	case "f":
		return tea.KeyMsg{Type: tea.KeyCtrlF}, true
	case "g":
		return tea.KeyMsg{Type: tea.KeyCtrlG}, true
	case "h":
		return tea.KeyMsg{Type: tea.KeyCtrlH}, true
	case "i":
		return tea.KeyMsg{Type: tea.KeyCtrlI}, true
	case "j":
		return tea.KeyMsg{Type: tea.KeyCtrlJ}, true
	case "k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}, true
	case "l":
		return tea.KeyMsg{Type: tea.KeyCtrlL}, true
	case "m":
		return tea.KeyMsg{Type: tea.KeyCtrlM}, true
	case "n":
		return tea.KeyMsg{Type: tea.KeyCtrlN}, true
	case "o":
		return tea.KeyMsg{Type: tea.KeyCtrlO}, true
	case "p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}, true
	case "q":
		return tea.KeyMsg{Type: tea.KeyCtrlQ}, true
	case "r":
		return tea.KeyMsg{Type: tea.KeyCtrlR}, true
	case "s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}, true
	case "t":
		return tea.KeyMsg{Type: tea.KeyCtrlT}, true
	case "u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}, true
	case "v":
		return tea.KeyMsg{Type: tea.KeyCtrlV}, true
	case "w":
		return tea.KeyMsg{Type: tea.KeyCtrlW}, true
	case "x":
		return tea.KeyMsg{Type: tea.KeyCtrlX}, true
	case "y":
		return tea.KeyMsg{Type: tea.KeyCtrlY}, true
	case "z":
		return tea.KeyMsg{Type: tea.KeyCtrlZ}, true
	default:
		return tea.KeyMsg{}, false
	}
}

func runtimeControlPreviewKey(key string, chordSecond bool) tea.KeyMsg {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "ctrl+x ctrl+u" {
		if chordSecond {
			return tea.KeyMsg{Type: tea.KeyCtrlU}
		}
		return tea.KeyMsg{Type: tea.KeyCtrlX}
	}
	if key == "ctrl+x ctrl+s" {
		if chordSecond {
			return tea.KeyMsg{Type: tea.KeyCtrlS}
		}
		return tea.KeyMsg{Type: tea.KeyCtrlX}
	}
	if key == "ctrl+x ctrl+y" {
		if chordSecond {
			return tea.KeyMsg{Type: tea.KeyCtrlY}
		}
		return tea.KeyMsg{Type: tea.KeyCtrlX}
	}
	return altRuneKey(key)
}

func runtimeControlPreviewChord(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "ctrl+x ctrl+u", "ctrl+x ctrl+s", "ctrl+x ctrl+y":
		return true
	default:
		return false
	}
}

// PreviewWithMessageActions renders a deterministic TUI state after opening
// the transcript message action menu.
func PreviewWithMessageActions(entries []Entry, width int, height int, action int) Preview {
	return previewWithMessageActions(entries, width, height, action, 0)
}

// PreviewWithMessageActionTarget renders message actions after moving the
// selected target message by targetDelta steps.
func PreviewWithMessageActionTarget(entries []Entry, width int, height int, action int, targetDelta int) Preview {
	return previewWithMessageActions(entries, width, height, action, targetDelta)
}

func previewWithMessageActions(entries []Entry, width int, height int, action int, targetDelta int) Preview {
	ta := newPromptTextarea("")
	modelEntries := make([]transcriptEntry, 0, len(entries))
	for _, entry := range entries {
		modelEntries = append(modelEntries, transcriptEntry{Role: entry.Role, Text: entry.Text, Tool: cloneToolActivity(entry.Tool)})
	}
	m := newModel(context.Background(), ta, nil, modelEntries)
	m.restoreConversation = func(_ context.Context, keepMessages int) (RuntimeControlResult, error) {
		return RuntimeControlResult{
			Title:  "Conversation Restored",
			Status: "restored",
			Lines:  []string{fmt.Sprintf("Remaining: %d", keepMessages)},
		}, nil
	}
	m.forkConversation = func(_ context.Context, keepMessages int) (RuntimeControlResult, error) {
		return RuntimeControlResult{
			Title:  "Conversation Forked",
			Status: "forked",
			Lines:  []string{fmt.Sprintf("Remaining: %d", keepMessages)},
		}, nil
	}
	m.summarizeConversation = func(_ context.Context, keepMessages int) (RuntimeControlResult, error) {
		return RuntimeControlResult{
			Title:  "Conversation Summarized",
			Status: "summarized",
			Lines:  []string{fmt.Sprintf("Before: %d", keepMessages)},
		}, nil
	}
	m.summarizeUpToConversation = func(_ context.Context, keepMessages int) (RuntimeControlResult, error) {
		return RuntimeControlResult{
			Title:  "Earlier Conversation Summarized",
			Status: "summarized earlier",
			Lines:  []string{fmt.Sprintf("Summarized: %d", keepMessages)},
		}, nil
	}
	m.copyMessage = func(_ context.Context, text string) (RuntimeControlResult, error) {
		return RuntimeControlResult{
			Title:  "Message Copied",
			Status: "message copied",
			Lines:  []string{fmt.Sprintf("Bytes: %d", len([]byte(text)))},
		}, nil
	}
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	if next, ok := updated.(model); ok {
		m = next
	}
	key := tea.KeyRight
	steps := targetDelta
	if steps < 0 {
		key = tea.KeyLeft
		steps = -steps
	}
	for index := 0; index < steps; index++ {
		updated, _ = m.Update(tea.KeyMsg{Type: key})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	for index := 0; index < action; index++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	if action >= 0 {
		var cmd tea.Cmd
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if next, ok := updated.(model); ok {
			m = next
		}
		if cmd != nil {
			updated, _ = m.Update(cmd())
			if next, ok := updated.(model); ok {
				m = next
			}
		}
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Matches:      append([]string(nil), messageActionLabels...),
		Attachments:  append([]string(nil), m.attachments...),
		Mode:         m.mode(),
		HelpOpen:     m.helpOpen,
		HasStash:     m.stashedPrompt != nil,
		Transcript:   m.transcriptMode,
		QuickOpen:    m.quickOpen,
		GlobalSearch: m.globalSearch,
		TodosOpen:    m.todosOpen,
		ModelPicker:  m.modelPicker,
		MessageMenu:  m.messageActions,
	}
}

func altRuneKey(key string) tea.KeyMsg {
	key = strings.ToLower(strings.TrimSpace(key))
	r := 'o'
	if strings.HasSuffix(key, "+t") {
		r = 't'
	} else if strings.HasSuffix(key, "+p") {
		r = 'p'
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

// PreviewWithUndo renders a deterministic TUI state after editing the composer
// and invoking the undo shortcut.
func PreviewWithUndo(input string, inserted string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	if inserted != "" {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(inserted)})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ctrl+_")})
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

// Shell starts the interactive TUI loop.
func Shell(ctx context.Context, options ShellOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ta := newPromptTextarea(options.Prefill)
	entries := make([]transcriptEntry, 0, len(options.Entries))
	for _, entry := range options.Entries {
		entries = append(entries, transcriptEntry{Role: entry.Role, Text: entry.Text, Tool: cloneToolActivity(entry.Tool)})
	}
	m := newModel(ctx, ta, options.Candidates, entries)
	m.fileCandidates = append([]string(nil), options.FileCandidates...)
	m.submit = options.Submit
	m.submitStream = options.SubmitStream
	m.submitAttachments = options.SubmitAttachments
	m.submitStreamAttachments = options.SubmitStreamAttachments
	m.slash = options.Slash
	m.permissionAnswer = options.PermissionAnswer
	m.permissionRespond = options.PermissionRespond
	m.questionAnswer = options.QuestionAnswer
	m.externalEditor = options.ExternalEditor
	m.paste = options.Paste
	m.background = options.Background
	m.taskBoard = options.TaskBoard
	m.todos = options.Todos
	m.modelOptions = normalizeModelOptions(options.ModelOptions)
	m.currentModel = strings.TrimSpace(options.CurrentModel)
	m.selectModel = options.SelectModel
	m.theme, _ = NormalizeThemeName(options.Theme)
	if m.theme == "" {
		m.theme = "auto"
	}
	m.selectTheme = options.SelectTheme
	m.applyTheme()
	m.toggleFast = options.ToggleFast
	m.toggleThinking = options.ToggleThinking
	m.stopBackground = options.StopBackground
	m.compactSession = options.CompactSession
	m.undoLast = options.UndoLast
	m.exportConversation = options.ExportConversation
	m.copyConversation = options.CopyConversation
	m.restoreConversation = options.RestoreConversation
	m.forkConversation = options.ForkConversation
	m.summarizeConversation = options.SummarizeConversation
	m.summarizeUpToConversation = options.SummarizeUpToConversation
	m.copyMessage = options.CopyMessage
	m.modeLabel = strings.TrimSpace(options.ModeLabel)
	m.runtimeBadges = normalizeRuntimeBadges(options.RuntimeBadges)
	m.vimEnabled = options.VimMode
	m.vimNormal = false
	m.keybindings = normalizeTUIKeybindings(options.Keybindings)
	m.contextKeybindings = normalizeTUIContextKeybindings(options.ContextKeybindings)
	m.cycleMode = options.CycleMode
	m.initialPrompt = strings.TrimSpace(options.InitialPrompt)
	m.attachments = append([]string(nil), options.InitialAttachments...)
	m.setHistory(options.History)
	if options.FullScreen {
		m.inline = false
		_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
		return err
	}
	m.inline = true
	m.prepareInlineTranscript()
	_, err := tea.NewProgram(m).Run()
	return err
}

func newModel(ctx context.Context, ta textarea.Model, candidates []string, entries []transcriptEntry) model {
	vp := viewport.New(80, 12)
	if ctx == nil {
		ctx = context.Background()
	}
	if len(entries) == 0 {
		entries = defaultTranscriptEntries()
	}
	m := model{
		ctx:            ctx,
		textarea:       ta,
		viewport:       vp,
		candidates:     candidates,
		status:         "ready",
		transcript:     entries,
		theme:          "auto",
		historyPos:     -1,
		streamingIndex: -1,
	}
	m.applyTheme()
	m.refreshViewport()
	return m
}

func newPromptTextarea(input string) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask codog..."
	ta.Prompt = "❯ "
	ta.ShowLineNumbers = false
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.CharLimit = 16000
	ta.SetValue(input)
	return ta
}

func defaultTranscriptEntries() []transcriptEntry {
	return []transcriptEntry{
		{
			Role: "system",
			Text: strings.Join([]string{
				"Interactive coding agent ready.",
				"Mention @files, run !shell commands, or type /help.",
			}, "\n"),
		},
	}
}

func (m model) Init() tea.Cmd {
	commands := []tea.Cmd{}
	if strings.TrimSpace(m.initialPrint) != "" {
		commands = append(commands, tea.Println(m.initialPrint))
	}
	if m.initialPrompt != "" {
		commands = append(commands, func() tea.Msg {
			return initialPromptMsg{Value: m.initialPrompt}
		})
	}
	return tea.Batch(textarea.Blink, sequenceCommands(commands...))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case initialPromptMsg:
		m.initialPrompt = ""
		return m.startInput(msg.Value)
	case turnDoneMsg:
		m.busy = false
		m.clearInteractionPrompts()
		m.turnMessages = nil
		if m.turnCancel != nil {
			m.turnCancel()
			m.turnCancel = nil
		}
		if msg.Interrupted || errors.Is(msg.Err, context.Canceled) {
			m.streamingIndex = -1
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Interrupted by user."})
			m.status = "interrupted"
			if restored := m.restoreQueuedPrompts("interrupted turn"); restored > 0 {
				m.status = fmt.Sprintf("interrupted · %d queued restored", restored)
			}
		} else if msg.Err != nil {
			m.streamingIndex = -1
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.status = "error"
			if restored := m.restoreQueuedPrompts("failed turn"); restored > 0 {
				m.status = fmt.Sprintf("error · %d queued restored", restored)
			}
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
		flushCmd := m.flushInlineTranscript()
		if len(m.queuedPrompts) > 0 && msg.Err == nil && !msg.Interrupted {
			next := m.queuedPrompts[0]
			m.queuedPrompts = append([]string(nil), m.queuedPrompts[1:]...)
			nextModel, nextCmd := m.startInput(next)
			return nextModel, sequenceCommands(flushCmd, nextCmd)
		}
		return m, flushCmd
	case externalEditorDoneMsg:
		if msg.Err != nil {
			m.status = "editor error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		m.pushComposerUndo()
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
			return m, m.flushInlineTranscript()
		}
		if msg.Content.Text == "" && msg.Content.AttachmentPath == "" {
			m.status = "paste empty"
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Clipboard is empty."})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
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
			return m, m.flushInlineTranscript()
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
		return m, m.flushInlineTranscript()
	case taskBoardDoneMsg:
		if msg.Err != nil {
			m.status = "tasks error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		output := strings.TrimSpace(msg.Output)
		if output == "" {
			output = "No background tasks."
		}
		m.status = "tasks"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: output})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, m.flushInlineTranscript()
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
	case runtimeControlDoneMsg:
		if msg.Err != nil {
			m.status = "control error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		m.applyRuntimeControlResult(msg.Result)
		return m, m.flushInlineTranscript()
	case themeSelectDoneMsg:
		if msg.Err != nil {
			m.theme = msg.Previous
			m.applyTheme()
			m.status = "theme error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		m.theme = msg.Selected
		m.applyTheme()
		m.applyRuntimeControlResult(msg.Result)
		return m, m.flushInlineTranscript()
	case turnStreamMsg:
		m.appendStreamEntry(msg)
		switch {
		case msg.Permission != nil:
			m.openPermissionRequest(*msg.Permission)
		case msg.Question != nil:
			m.openQuestionRequest(*msg.Question)
		case msg.Tool != nil:
			if strings.EqualFold(msg.Tool.Status, "running") {
				m.status = "running " + strings.ToLower(toolActivityDisplayName(msg.Tool.Name))
			} else {
				m.status = "streaming"
			}
		case strings.EqualFold(msg.Role, "permission"):
			m.awaitingPermission = isPermissionRequestDelta(msg.Delta)
			if m.awaitingPermission {
				m.openPermissionRequest(PermissionRequest{Input: msg.Delta})
				m.status = "permission"
			} else {
				m.closePermissionRequest()
				m.status = "permission answered"
			}
		case strings.EqualFold(msg.Role, "question"):
			m.openQuestionRequest(QuestionRequest{Question: msg.Delta})
		default:
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
		if m.awaitingPermission {
			return m.updatePermissionRequest(msg)
		}
		if m.awaitingQuestion {
			return m.updateQuestionRequest(msg)
		}
		if m.themePicker {
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeThemePicker(true)
				return m, nil
			case "up", "left", "ctrl+p", "k", "shift+tab":
				m.moveThemePicker(-1)
				return m, nil
			case "down", "right", "ctrl+n", "j", "tab":
				m.moveThemePicker(1)
				return m, nil
			case "home":
				m.setThemePickerIndex(0)
				return m, nil
			case "end":
				m.setThemePickerIndex(len(ThemeNames()) - 1)
				return m, nil
			case "enter":
				return m.acceptThemePicker()
			default:
				return m, nil
			}
		}
		if m.diffDialog {
			if next, handled, cmd := m.handleBoundDiffAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeDiffDialog()
				return m, nil
			case "left":
				m.previousDiffSourceOrBack()
				return m, nil
			case "right":
				m.nextDiffSource()
				return m, nil
			case "up":
				m.moveDiffFile(-1)
				return m, nil
			case "down":
				m.moveDiffFile(1)
				return m, nil
			case "enter":
				m.openDiffDetail()
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "shift+up", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			return m, nil
		}
		if m.attachmentsOpen {
			if next, handled, cmd := m.handleBoundAttachmentAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc", "down":
				m.closeAttachmentsPanel()
				return m, nil
			case "right":
				m.moveAttachmentSelection(1)
				return m, nil
			case "left":
				m.moveAttachmentSelection(-1)
				return m, nil
			case "backspace", "delete":
				m.removeSelectedAttachment()
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "shift+up", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			return m, nil
		}
		if m.modelPicker {
			if next, handled, cmd := m.handleBoundModelPickerAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc", "alt+p", "meta+p":
				m.closeModelPicker()
				return m, nil
			case "up", "ctrl+p", "k":
				m.moveModelPicker(-1)
				return m, nil
			case "down", "ctrl+n", "j":
				m.moveModelPicker(1)
				return m, nil
			case "home", "ctrl+up", "meta+up", "alt+up", "K", "shift+k":
				m.setModelPickerIndex(0)
				return m, nil
			case "end", "ctrl+down", "meta+down", "alt+down", "J", "shift+j":
				m.setModelPickerIndex(len(m.modelOptions) - 1)
				return m, nil
			case "enter", "tab":
				return m.acceptModelPicker()
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "alt+m", "meta+m", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			return m, nil
		}
		if m.messageActions {
			if next, handled, cmd := m.handleBoundMessageActionMenuAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeMessageActions()
				return m, nil
			case "up", "ctrl+p", "k":
				m.moveMessageAction(-1)
				return m, nil
			case "down", "ctrl+n", "j":
				m.moveMessageAction(1)
				return m, nil
			case "home", "ctrl+up", "meta+up", "alt+up", "K", "shift+k":
				m.setMessageActionIndex(0)
				return m, nil
			case "end", "ctrl+down", "meta+down", "alt+down", "J", "shift+j":
				m.setMessageActionIndex(len(messageActionLabels) - 1)
				return m, nil
			case "shift+up":
				m.moveMessageActionUserTarget(-1)
				return m, nil
			case "shift+down":
				m.moveMessageActionUserTarget(1)
				return m, nil
			case "left":
				m.moveMessageActionTarget(-1)
				return m, nil
			case "right":
				m.moveMessageActionTarget(1)
				return m, nil
			case "enter", "tab":
				return m.applyMessageAction()
			case "c":
				m.messageActionSelected = 1
				return m.applyMessageAction()
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			return m, nil
		}
		if m.globalSearch {
			if next, handled, cmd := m.handleBoundGlobalSearchAction(msg); handled {
				return next, cmd
			}
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
			case "home", "ctrl+up", "meta+up", "alt+up":
				m.setGlobalSearchIndex(0)
				return m, nil
			case "end", "ctrl+down", "meta+down", "alt+down":
				m.setGlobalSearchIndex(len(m.globalSearchMatches) - 1)
				return m, nil
			case "enter", "tab":
				m.closeGlobalSearch(true, true)
				return m, nil
			case "shift+tab":
				m.closeGlobalSearch(true, false)
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "shift+up", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
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
			if next, handled, cmd := m.handleBoundQuickOpenAction(msg); handled {
				return next, cmd
			}
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
			case "home", "ctrl+up", "meta+up", "alt+up":
				m.setQuickOpenIndex(0)
				return m, nil
			case "end", "ctrl+down", "meta+down", "alt+down":
				m.setQuickOpenIndex(len(m.quickOpenMatches) - 1)
				return m, nil
			case "enter", "tab":
				m.closeQuickOpen(true, true)
				return m, nil
			case "shift+tab":
				m.closeQuickOpen(true, false)
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "shift+up", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			var cmd tea.Cmd
			var viewportCmd tea.Cmd
			m.viewport, viewportCmd = m.viewport.Update(msg)
			m.textarea, cmd = m.textarea.Update(msg)
			m.updateQuickOpen()
			return m, tea.Batch(cmd, viewportCmd)
		}
		if m.keyChordPrefix != "" {
			next, handled, cmd := m.handleBoundTUIChord(msg)
			if handled {
				return next, cmd
			}
		}
		if m.ctrlXChord {
			m.ctrlXChord = false
			return m.handleDefaultCtrlXChord(msg)
		}
		key := msg.String()
		if m.exitPending && key != m.exitKey {
			m.clearExitPending()
		}
		if next, handled, cmd := m.handleBoundTUIAction(msg); handled {
			return next, cmd
		}
		switch msg.String() {
		case "ctrl+c":
			if m.busy {
				m.interruptTurn()
				return m, nil
			}
			if m.backgrounding {
				m.interruptBackground()
				return m, nil
			}
			if m.exitPending && m.exitKey == "ctrl+c" {
				return m, tea.Quit
			}
			if strings.TrimSpace(m.textarea.Value()) != "" {
				m.pushComposerUndo()
				m.textarea.SetValue("")
				m.matches = nil
				m.selected = 0
				m.commandArgumentHint = ""
				m.inlineGhostText = ""
				m.historyPos = -1
				m.armExit("ctrl+c", "input cleared · press ctrl+c again to exit")
				return m, nil
			}
			m.armExit("ctrl+c", "press ctrl+c again to exit")
			return m, nil
		case "esc":
			if m.shouldEnterVimNormalMode() {
				m.vimNormal = true
				m.matches = nil
				m.selected = 0
				m.commandArgumentHint = ""
				m.inlineGhostText = ""
				m.clearExitPending()
				m.status = "vim normal"
				return m, nil
			}
			if m.vimEnabled && m.vimNormal && m.vimKeybindingsAvailable() && strings.TrimSpace(m.textarea.Value()) != "" {
				m.status = "vim normal"
				return m, nil
			}
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
				m.commandArgumentHint = ""
				m.inlineGhostText = ""
				m.clearExitPending()
				m.status = m.mode()
				return m, nil
			}
			if m.searchOpen {
				m.clearExitPending()
				m.closeHistorySearch(false)
				return m, nil
			}
			if m.todosOpen {
				m.clearExitPending()
				m.closeTodos()
				return m, nil
			}
			if m.helpOpen {
				m.clearExitPending()
				m.helpOpen = false
				m.status = "ready"
				m.refreshViewport()
				return m, nil
			}
			if strings.TrimSpace(m.textarea.Value()) != "" {
				m.pushComposerUndo()
				m.textarea.SetValue("")
				m.matches = nil
				m.selected = 0
				m.commandArgumentHint = ""
				m.inlineGhostText = ""
				m.historyPos = -1
				m.armExit("esc", "input cleared · press esc again to exit")
				return m, nil
			}
			if m.exitPending && m.exitKey == "esc" {
				return m, tea.Quit
			}
			m.armExit("esc", "press esc again to exit")
			return m, nil
		case "ctrl+d":
			if !m.busy && !m.searchOpen && !m.quickOpen && !m.globalSearch && !m.todosOpen && !m.modelPicker && !m.messageActions && !m.helpOpen && strings.TrimSpace(m.textarea.Value()) == "" {
				return m, tea.Quit
			}
		case "ctrl+l":
			if m.busy {
				return m, nil
			}
			m.clearScreen()
			if m.inline {
				return m, tea.ClearScreen
			}
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
		case "ctrl+_", "ctrl+shift+-":
			if m.busy || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.undoComposer()
			return m, nil
		case "ctrl+u":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.deleteComposerBeforeCursor()
			return m, nil
		case "ctrl+k":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.deleteComposerAfterCursor()
			return m, nil
		case "home", "ctrl+a":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.moveComposerLineStart()
			return m, nil
		case "end":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.moveComposerLineEnd()
			return m, nil
		case "ctrl+x":
			if m.busy || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.ctrlXChord = true
			m.status = "ctrl+x"
			return m, nil
		case "shift+up":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.openMessageActions()
			return m, nil
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
		case "alt+p", "meta+p":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.openModelPicker()
			return m, nil
		case "alt+o", "meta+o":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleFast == nil {
				return m, nil
			}
			m.status = "fast mode"
			return m, runRuntimeControlCommand(m.ctx, m.toggleFast)
		case "alt+t", "meta+t":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleThinking == nil {
				return m, nil
			}
			m.status = "thinking"
			return m, runRuntimeControlCommand(m.ctx, m.toggleThinking)
		case "shift+enter", "alt+enter", "ctrl+j":
			m.pushComposerUndo()
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
			return m.openExternalEditor()
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
			m.pushComposerUndo()
			m = m.completeSlashCommand()
			return m, nil
		case "shift+tab", "alt+m", "meta+m":
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
				if cmd := m.quitFromBusyInput(); cmd != nil {
					return m, cmd
				}
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
			if len(m.matches) > 0 && shouldAcceptCompletionOnEnter(m.textarea.Value()) {
				m.pushComposerUndo()
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
		if next, handled, cmd := m.handleVimNormalKey(msg); handled {
			return next, cmd
		}
	}
	var cmd tea.Cmd
	var viewportCmd tea.Cmd
	m.viewport, viewportCmd = m.viewport.Update(msg)
	if !m.searchOpen && !m.quickOpen && !m.globalSearch && !m.todosOpen {
		m.pushComposerUndo()
	}
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
	if m.awaitingPermission {
		if !m.permissionInput {
			m.status = "permission"
			return m, nil
		}
		m.textarea.InsertString(text)
		m.status = "permission"
		return m, nil
	}
	if m.awaitingQuestion && !m.questionCustom {
		m.beginQuestionCustomInput()
	}
	if m.helpOpen {
		m.helpOpen = false
	}
	if !m.searchOpen && !m.quickOpen && !m.globalSearch {
		m.pushComposerUndo()
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
	m.pushComposerUndoValue(value)
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
	styles := stylesForTheme(m.theme)
	barWidth := max(3, m.width)
	barContentWidth := barWidth - 2
	title := styles.header().Width(barWidth).Render(truncateFooterLine(m.headerText(), barContentWidth))
	if m.inline {
		title = styles.inlineHeader().Render(truncateFooterLine(m.headerText(), barContentWidth))
	}
	body := m.viewport.View()
	if m.inline {
		body = compactViewportView(body)
	}
	composerTextarea := m.textarea
	if m.inline {
		composerTextarea.SetHeight(m.inlineComposerHeight())
	}
	composer := composerTextarea.View()
	if !m.inline {
		composer = styles.panelTitle().Render(" composer ") + "\n" + composer
	}
	if m.commandArgumentHint != "" || m.inlineGhostText != "" {
		composer += "\n" + renderCommandAssist(m.commandArgumentHint, m.inlineGhostText, styles)
	}
	if len(m.matches) > 0 {
		composer += "\n" + renderCompletions(m.matches, m.selected, styles)
	}
	if m.searchOpen {
		composer += "\n" + renderHistorySearch(m.searchHits, m.searchPos, m.textarea.Value(), styles)
	}
	if m.quickOpen {
		composer += "\n" + renderQuickOpen(m.quickOpenMatches, m.quickOpenSelected, m.textarea.Value(), m.width, m.quickOpenPreviewPath, m.quickOpenPreviewLines, styles)
	}
	if m.globalSearch {
		composer += "\n" + renderGlobalSearch(m.globalSearchMatches, m.globalSearchSelected, m.textarea.Value(), m.width, m.globalSearchPreviewPath, m.globalSearchPreviewLine, m.globalSearchPreviewLines, styles)
	}
	if m.todosOpen {
		composer += "\n" + renderTodosPanel(m.todoItems, m.todosLoading, m.todoErr, m.width, styles)
	}
	if m.modelPicker {
		composer += "\n" + renderModelPicker(m.modelOptions, m.currentModel, m.modelPickerSelected, m.width, styles)
	}
	if m.themePicker {
		composer += "\n" + renderThemePicker(m.theme, m.themePickerSelected, m.width, styles)
	}
	if m.messageActions {
		targetPos, targetCount := m.messageActionTargetPosition()
		composer += "\n" + renderMessageActions(m.messageActionEntry(), m.messageActionSelected, m.width, targetPos, targetCount, styles)
	}
	if m.diffDialog {
		composer += "\n" + renderDiffDialog(m.diffSources, m.diffSourceSelected, m.diffFileSelected, m.diffDetail, m.width, styles)
	}
	if len(m.queuedPrompts) > 0 {
		composer += "\n" + renderQueuedPrompts(m.queuedPrompts, styles)
	}
	if m.attachmentsOpen {
		composer += "\n" + renderAttachmentPanel(m.attachments, m.attachmentSelected, m.width, styles)
	} else if len(m.attachments) > 0 {
		composer += "\n" + renderPendingAttachments(m.attachments, styles)
	}
	if m.stashedPrompt != nil {
		composer += "\n" + renderStashNotice(m.stashedPrompt, styles)
	}
	if m.awaitingPermission && m.permissionRequest != nil {
		composer = renderPermissionRequest(*m.permissionRequest, m.permissionSelected, m.permissionInput, m.permissionInputAnswer, m.width, styles)
		if m.permissionInput {
			composer += "\n" + composerTextarea.View()
		}
	} else if m.awaitingQuestion && m.questionRequest != nil {
		composer = renderQuestionRequest(*m.questionRequest, m.questionIndex, m.questionSelected, m.questionCustom, m.questionSelections, m.questionCustomValues, m.width, styles)
		if m.questionCustom {
			composer += "\n" + composerTextarea.View()
		}
	}
	statusText := fitFooterText(m.promptFooterText(barWidth), barContentWidth)
	status := styles.status().Width(barWidth).Render(statusText)
	if m.inline {
		statusText = fitFooterText(m.inlineFooterText(barWidth), barContentWidth)
		status = styles.inlineStatus().Render(statusText)
	}
	parts := []string{title}
	if body != "" {
		parts = append(parts, body)
	}
	parts = append(parts, composer, status)
	return strings.Join(parts, "\n")
}

func (m model) headerText() string {
	prefix := "Codog TUI"
	if m.inline {
		prefix = "codog"
	}
	badges := m.runtimeStatusBadges()
	if len(badges) == 0 {
		return prefix
	}
	title := prefix + " · " + strings.Join(badges, " · ")
	width := m.width
	if width <= 0 || len([]rune(title)) <= width {
		return title
	}
	runes := []rune(title)
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func (m model) inlineComposerHeight() int {
	value := m.textarea.Value()
	if value == "" {
		return 1
	}
	width := max(1, m.textarea.Width()-lipgloss.Width(m.textarea.Prompt))
	height := 0
	for _, line := range strings.Split(value, "\n") {
		height += max(1, (lipgloss.Width(line)+width-1)/width)
	}
	return min(max(height, 1), 6)
}

func compactViewportView(view string) string {
	return strings.TrimRight(view, " \n\r\t")
}

func (m model) inlineFooterText(width int) string {
	status := strings.TrimSpace(m.status)
	hints := m.promptFooterHints(width)
	if m.exitPending {
		return strings.Join(hints, " · ")
	}
	if status == "" || strings.EqualFold(status, "ready") {
		if len(hints) == 0 {
			return "ready"
		}
		return strings.Join(hints, " · ")
	}
	if mode := strings.TrimSpace(m.modeLabel); mode != "" {
		status += " · " + mode
	}
	if len(hints) > 0 {
		status += " · " + strings.Join(hints, " · ")
	}
	return status
}

type turnDoneMsg struct {
	Role        string
	Output      string
	Err         error
	Interrupted bool
}

type initialPromptMsg struct {
	Value string
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
		m.matches = nil
		m.selected = 0
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
	if isThemePickerInput(value) && m.selectTheme != nil {
		m.appendHistory(value)
		m.textarea.SetValue("")
		m.historyPos = -1
		m.openThemePicker()
		return m, nil
	}
	if m.handleAttachmentInput(value) {
		return m, nil
	}
	if isBashModeInput(value) {
		return m.startBashInput(value)
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
		m.undoStack = nil
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
		m.vimNormal = false
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
		m.vimNormal = false
		m.result = Result{Submitted: true, Prompt: value, Attachments: attachments}
		return m, tea.Quit
	}
	m.appendHistory(value)
	ctx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.vimNormal = false
	m.textarea.SetValue("")
	m.undoStack = nil
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.attachments = nil
	m.closeAttachmentsPanel()
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

func isThemePickerInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/theme", "/color":
		return true
	default:
		return false
	}
}

func (m model) startBashInput(value string) (tea.Model, tea.Cmd) {
	command := bashModeCommand(value)
	if command == "" {
		m.status = "bash"
		return m, nil
	}
	if m.slash == nil {
		m.vimNormal = false
		m.result = Result{Submitted: true, Prompt: value, Attachments: append([]string(nil), m.attachments...)}
		return m, tea.Quit
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.appendHistory(value)
	m.vimNormal = false
	m.textarea.SetValue("")
	m.undoStack = nil
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.historyPos = -1
	m.busy = true
	m.status = "running bash"
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: "!" + command})
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m, runSlashCommand(ctx, m.slash, "/run "+command)
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
	m.undoStack = nil
	m.matches = nil
	m.selected = 0
	m.status = "queued"
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Queued %s %d: %s", queuedPromptKind(value), len(m.queuedPrompts), truncateForComposer(queuedPromptDisplay(value), 120))})
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) quitFromBusyInput() tea.Cmd {
	if !isREPLExitInput(m.textarea.Value()) {
		return nil
	}
	if m.turnCancel != nil {
		m.turnCancel()
		m.turnCancel = nil
	}
	if m.backgroundCancel != nil {
		m.backgroundCancel()
		m.backgroundCancel = nil
	}
	m.queuedPrompts = nil
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.status = "exiting"
	return tea.Quit
}

func (m *model) restoreQueuedPrompts(reason string) int {
	count := len(m.queuedPrompts)
	if count == 0 {
		return 0
	}
	parts := append([]string(nil), m.queuedPrompts...)
	if current := strings.TrimSpace(m.textarea.Value()); current != "" {
		parts = append(parts, current)
	}
	m.queuedPrompts = nil
	m.textarea.SetValue(strings.Join(parts, "\n\n"))
	m.textarea.CursorEnd()
	m.undoStack = nil
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.transcript = append(m.transcript, transcriptEntry{
		Role: "system",
		Text: fmt.Sprintf("Restored %d queued %s to the composer after the %s.", count, plural("prompt", count), reason),
	})
	m.refreshCompletionMenu()
	return count
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
		m.normalizeAttachmentSelection()
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
	m.closeAttachmentsPanel()
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.status = "prompt stashed"
}

func (m model) shouldEnterVimNormalMode() bool {
	if !m.vimEnabled || m.vimNormal {
		return false
	}
	return m.vimKeybindingsAvailable() && strings.TrimSpace(m.textarea.Value()) != ""
}

func (m model) vimKeybindingsAvailable() bool {
	return !m.busy &&
		!m.backgrounding &&
		!m.searchOpen &&
		!m.quickOpen &&
		!m.globalSearch &&
		!m.todosOpen &&
		!m.modelPicker &&
		!m.messageActions &&
		!m.attachmentsOpen &&
		!m.diffDialog &&
		!m.awaitingPermission &&
		!m.awaitingQuestion &&
		!m.helpOpen
}

func (m model) handleVimNormalKey(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	if !m.vimEnabled || !m.vimNormal || !m.vimKeybindingsAvailable() {
		return m, false, nil
	}
	key := msg.String()
	if m.vimOperator != "" {
		return m.handleVimOperatorKey(key)
	}
	switch key {
	case "i":
		m.vimOperator = ""
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "a":
		m.vimOperator = ""
		m.textarea, _ = m.textarea.Update(tea.KeyMsg{Type: tea.KeyRight})
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "I":
		m.vimOperator = ""
		m.textarea.CursorStart()
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "A":
		m.vimOperator = ""
		m.textarea.CursorEnd()
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "h":
		m.textarea, _ = m.textarea.Update(tea.KeyMsg{Type: tea.KeyLeft})
		m.status = "vim normal"
		return m, true, nil
	case "l":
		m.textarea, _ = m.textarea.Update(tea.KeyMsg{Type: tea.KeyRight})
		m.status = "vim normal"
		return m, true, nil
	case "w":
		m.moveVimWordForward()
		m.status = "vim normal"
		return m, true, nil
	case "b":
		m.moveVimWordBackward()
		m.status = "vim normal"
		return m, true, nil
	case "0":
		m.textarea.CursorStart()
		m.status = "vim normal"
		return m, true, nil
	case "$":
		m.textarea.CursorEnd()
		m.status = "vim normal"
		return m, true, nil
	case "x":
		m.pushComposerUndo()
		m.textarea, _ = m.textarea.Update(tea.KeyMsg{Type: tea.KeyDelete})
		m.matches = nil
		m.selected = 0
		m.refreshCompletionMenu()
		m.status = "vim normal"
		return m, true, nil
	case "D":
		m.deleteVimToLineEnd()
		m.status = "vim normal"
		return m, true, nil
	case "C":
		m.deleteVimToLineEnd()
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "d", "c":
		m.vimOperator = key
		m.status = "vim " + key
		return m, true, nil
	case "u":
		m.undoComposer()
		m.vimNormal = true
		m.status = "vim normal"
		return m, true, nil
	default:
		return m, false, nil
	}
}

func (m model) handleBoundTUIAction(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	key := normalizeTUIKey(msg.String())
	if key == "" || len(m.keybindings) == 0 {
		return m, false, nil
	}
	if next, handled, cmd := m.handleBoundTUIActionKey(key); handled {
		return next, true, cmd
	}
	if m.isBoundTUIChordPrefix(key) {
		m.keyChordPrefix = key
		m.status = key
		return m, true, nil
	}
	return m, false, nil
}

func (m model) handleBoundTUIChord(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	prefix := m.keyChordPrefix
	key := normalizeTUIKey(msg.String())
	m.keyChordPrefix = ""
	if prefix == "" {
		return m, false, nil
	}
	if key == "esc" || key == "" {
		m.status = m.mode()
		return m, true, nil
	}
	sequence := strings.TrimSpace(prefix + " " + key)
	if next, handled, cmd := m.handleBoundTUIActionKey(sequence); handled {
		return next, true, cmd
	}
	if prefix == "ctrl+x" {
		next, cmd := m.handleDefaultCtrlXChord(msg)
		return next, true, cmd
	}
	m.status = "compose"
	return m, true, nil
}

func (m model) handleBoundTUIActionKey(key string) (model, bool, tea.Cmd) {
	if key == "" {
		return m, false, nil
	}
	switch {
	case m.isBoundTUIAction("submit prompt", key):
		if m.busy && m.awaitingQuestion {
			m.answerQuestion()
			return m, true, nil
		}
		if m.busy {
			if cmd := m.quitFromBusyInput(); cmd != nil {
				return m, true, cmd
			}
			m.queueCurrentInput()
			return m, true, nil
		}
		if m.searchOpen {
			m.closeHistorySearch(true)
			return m, true, nil
		}
		if m.convertTrailingBackslashToNewline() {
			return m, true, nil
		}
		if len(m.matches) > 0 {
			m.pushComposerUndo()
			m = m.acceptSelectedCompletion()
			return m, true, nil
		}
		if isLocalHelpInput(m.textarea.Value()) {
			m.helpOpen = true
			m.textarea.SetValue("")
			m.matches = nil
			m.status = "help"
			m.refreshViewport()
			return m, true, nil
		}
		next, cmd := m.submitCurrentInput()
		if modelNext, ok := next.(model); ok {
			return modelNext, true, cmd
		}
		return m, true, cmd
	case m.isBoundTUIAction("insert newline", key) || m.isBoundTUIAction("insert newline fallback", key):
		if m.busy {
			return m, true, nil
		}
		m.pushComposerUndo()
		m.textarea.InsertString("\n")
		return m, true, nil
	case m.isBoundTUIAction("stash or restore composer", key):
		m.togglePromptStash()
		return m, true, nil
	case m.isBoundTUIAction("edit composer in $EDITOR", key):
		next, cmd := m.openExternalEditor()
		if modelNext, ok := next.(model); ok {
			return modelNext, true, cmd
		}
		return m, true, cmd
	case m.isBoundTUIAction("stop running background tasks and agents", key):
		if m.stopBackground == nil {
			m.status = "no background stop"
			return m, true, nil
		}
		m.status = "stopping background"
		return m, true, runRuntimeControlCommand(m.ctx, m.stopBackground)
	case m.isBoundTUIAction("compact current session", key):
		if m.compactSession == nil {
			m.status = "no compact"
			return m, true, nil
		}
		m.status = "compacting"
		return m, true, runRuntimeControlCommand(m.ctx, m.compactSession)
	case m.isBoundTUIAction("undo last file change", key):
		if m.undoLast == nil {
			m.status = "no undo"
			return m, true, nil
		}
		m.status = "undoing"
		return m, true, runRuntimeControlCommand(m.ctx, m.undoLast)
	case m.isBoundTUIAction("export current conversation", key):
		if m.exportConversation == nil {
			m.status = "no export"
			return m, true, nil
		}
		m.status = "exporting"
		return m, true, runRuntimeControlCommand(m.ctx, m.exportConversation)
	case m.isBoundTUIAction("copy current conversation", key):
		if m.copyConversation == nil {
			m.status = "no copy"
			return m, true, nil
		}
		m.status = "copying"
		return m, true, runRuntimeControlCommand(m.ctx, m.copyConversation)
	case m.isBoundTUIAction("remove last attachment", key):
		m.removeLastAttachment()
		return m, true, nil
	case m.isBoundTUIAction("undo composer edit", key):
		if m.busy || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.undoComposer()
		return m, true, nil
	case m.isBoundTUIAction("delete before cursor", key):
		if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.deleteComposerBeforeCursor()
		return m, true, nil
	case m.isBoundTUIAction("delete after cursor", key):
		if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.deleteComposerAfterCursor()
		return m, true, nil
	case m.isBoundTUIAction("move to line start", key):
		if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.moveComposerLineStart()
		return m, true, nil
	case m.isBoundTUIAction("move to line end", key):
		if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.moveComposerLineEnd()
		return m, true, nil
	case m.isBoundTUIAction("quick open files", key) || m.isBoundTUIAction("quick open fallback", key):
		if m.busy || m.backgrounding || m.searchOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion || len(m.fileCandidates) == 0 {
			return m, true, nil
		}
		m.openQuickOpen()
		return m, true, nil
	case m.isBoundTUIAction("search workspace", key) || m.isBoundTUIAction("search workspace fallback", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.todosOpen || m.awaitingPermission || m.awaitingQuestion || len(m.fileCandidates) == 0 {
			return m, true, nil
		}
		m.openGlobalSearch()
		return m, true, nil
	case m.isBoundTUIAction("cycle permission mode fallback", key):
		if m.busy || m.cycleMode == nil {
			return m, true, nil
		}
		if label := strings.TrimSpace(m.cycleMode()); label != "" {
			m.modeLabel = label
			m.status = m.mode()
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Mode: " + label})
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
		return m, true, nil
	case m.isBoundTUIAction("open model picker", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.openModelPicker()
		return m, true, nil
	case m.isBoundTUIAction("toggle fast mode", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleFast == nil {
			return m, true, nil
		}
		m.status = "fast mode"
		return m, true, runRuntimeControlCommand(m.ctx, m.toggleFast)
	case m.isBoundTUIAction("cycle thinking effort", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleThinking == nil {
			return m, true, nil
		}
		m.status = "thinking"
		return m, true, runRuntimeControlCommand(m.ctx, m.toggleThinking)
	case m.isBoundTUIAction("toggle expanded transcript", key):
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
		return m, true, nil
	case m.isBoundTUIAction("clear screen", key):
		if m.busy {
			return m, true, nil
		}
		m.clearScreen()
		return m, true, nil
	case m.isBoundTUIAction("paste clipboard text or image", key):
		if m.paste == nil || m.busy || m.backgrounding || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		if m.helpOpen {
			m.helpOpen = false
			m.refreshViewport()
		}
		m.matches = nil
		m.selected = 0
		m.status = "pasting"
		return m, true, runPasteCommand(m.ctx, m.paste)
	default:
		return m, false, nil
	}
}

func (m model) handleDefaultCtrlXChord(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+e":
		next, cmd := m.openExternalEditor()
		if modelNext, ok := next.(model); ok {
			return modelNext, cmd
		}
		return m, cmd
	case "ctrl+k":
		if m.stopBackground == nil {
			m.status = "no background stop"
			return m, nil
		}
		m.status = "stopping background"
		return m, runRuntimeControlCommand(m.ctx, m.stopBackground)
	case "ctrl+c":
		if m.compactSession == nil {
			m.status = "no compact"
			return m, nil
		}
		m.status = "compacting"
		return m, runRuntimeControlCommand(m.ctx, m.compactSession)
	case "ctrl+u":
		if m.undoLast == nil {
			m.status = "no undo"
			return m, nil
		}
		m.status = "undoing"
		return m, runRuntimeControlCommand(m.ctx, m.undoLast)
	case "ctrl+s":
		if m.exportConversation == nil {
			m.status = "no export"
			return m, nil
		}
		m.status = "exporting"
		return m, runRuntimeControlCommand(m.ctx, m.exportConversation)
	case "ctrl+y":
		if m.copyConversation == nil {
			m.status = "no copy"
			return m, nil
		}
		m.status = "copying"
		return m, runRuntimeControlCommand(m.ctx, m.copyConversation)
	case "backspace", "delete":
		m.removeLastAttachment()
		return m, nil
	case "esc":
		m.status = m.mode()
		return m, nil
	default:
		m.status = "compose"
		return m, nil
	}
}

func (m model) handleBoundModelPickerAction(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	key := normalizeTUIKey(msg.String())
	switch {
	case m.isBoundTUIContextAction("tui-modal", "move modal selection down", key):
		m.moveModelPicker(1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "move modal selection up", key):
		m.moveModelPicker(-1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "jump modal selection to top", key):
		m.setModelPickerIndex(0)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "jump modal selection to bottom", key):
		m.setModelPickerIndex(len(m.modelOptions) - 1)
		return m, true, nil
	default:
		return m, false, nil
	}
}

func (m model) handleBoundMessageActionMenuAction(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	key := normalizeTUIKey(msg.String())
	switch {
	case m.isBoundTUIContextAction("tui-modal", "move modal selection down", key):
		m.moveMessageAction(1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "move modal selection up", key):
		m.moveMessageAction(-1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "jump modal selection to top", key):
		m.setMessageActionIndex(0)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "jump modal selection to bottom", key):
		m.setMessageActionIndex(len(messageActionLabels) - 1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "move message target backward", key):
		m.moveMessageActionTarget(-1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "move message target forward", key):
		m.moveMessageActionTarget(1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "move to previous user message", key):
		m.moveMessageActionUserTarget(-1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "move to next user message", key):
		m.moveMessageActionUserTarget(1)
		return m, true, nil
	default:
		return m, false, nil
	}
}

func (m model) handleBoundGlobalSearchAction(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	key := normalizeTUIKey(msg.String())
	switch {
	case m.isBoundTUIContextAction("tui-modal", "move modal selection down", key):
		m.moveGlobalSearch(1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "move modal selection up", key):
		m.moveGlobalSearch(-1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "jump modal selection to top", key):
		m.setGlobalSearchIndex(0)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "jump modal selection to bottom", key):
		m.setGlobalSearchIndex(len(m.globalSearchMatches) - 1)
		return m, true, nil
	default:
		return m, false, nil
	}
}

func (m model) handleBoundQuickOpenAction(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	key := normalizeTUIKey(msg.String())
	switch {
	case m.isBoundTUIContextAction("tui-modal", "move modal selection down", key):
		m.moveQuickOpen(1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "move modal selection up", key):
		m.moveQuickOpen(-1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "jump modal selection to top", key):
		m.setQuickOpenIndex(0)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-modal", "jump modal selection to bottom", key):
		m.setQuickOpenIndex(len(m.quickOpenMatches) - 1)
		return m, true, nil
	default:
		return m, false, nil
	}
}

func (m model) handleBoundAttachmentAction(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	key := normalizeTUIKey(msg.String())
	switch {
	case m.isBoundTUIContextAction("tui-attachments", "select next attachment", key):
		m.moveAttachmentSelection(1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-attachments", "select previous attachment", key):
		m.moveAttachmentSelection(-1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-attachments", "remove selected attachment", key):
		m.removeSelectedAttachment()
		return m, true, nil
	case m.isBoundTUIContextAction("tui-attachments", "close attachment selector", key):
		m.closeAttachmentsPanel()
		return m, true, nil
	default:
		return m, false, nil
	}
}

func (m model) handleBoundDiffAction(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	key := normalizeTUIKey(msg.String())
	switch {
	case m.isBoundTUIContextAction("tui-diff", "close diff dialog", key):
		m.closeDiffDialog()
		return m, true, nil
	case m.isBoundTUIContextAction("tui-diff", "previous diff source or back from detail", key):
		m.previousDiffSourceOrBack()
		return m, true, nil
	case m.isBoundTUIContextAction("tui-diff", "next diff source", key):
		m.nextDiffSource()
		return m, true, nil
	case m.isBoundTUIContextAction("tui-diff", "select previous changed file", key):
		m.moveDiffFile(-1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-diff", "select next changed file", key):
		m.moveDiffFile(1)
		return m, true, nil
	case m.isBoundTUIContextAction("tui-diff", "view selected file diff", key):
		m.openDiffDetail()
		return m, true, nil
	default:
		return m, false, nil
	}
}

func (m model) isBoundTUIAction(action string, key string) bool {
	keys := m.keybindings[normalizeTUIAction(action)]
	return keys != nil && keys[key]
}

func (m model) isBoundTUIChordPrefix(key string) bool {
	if key == "" || len(m.keybindings) == 0 {
		return false
	}
	prefix := key + " "
	for _, keys := range m.keybindings {
		for sequence := range keys {
			if strings.HasPrefix(sequence, prefix) {
				return true
			}
		}
	}
	return false
}

func (m model) isBoundTUIContextAction(contextName string, action string, key string) bool {
	if key == "" || len(m.contextKeybindings) == 0 {
		return false
	}
	actions := m.contextKeybindings[normalizeTUIContext(contextName)]
	if len(actions) == 0 {
		return false
	}
	keys := actions[normalizeTUIAction(action)]
	return keys != nil && keys[key]
}

func (m model) handleVimOperatorKey(key string) (model, bool, tea.Cmd) {
	operator := m.vimOperator
	m.vimOperator = ""
	switch {
	case operator == "d" && key == "d":
		m.clearVimComposer(false)
		return m, true, nil
	case operator == "c" && key == "c":
		m.clearVimComposer(true)
		return m, true, nil
	case operator == "d" && key == "$":
		m.deleteVimToLineEnd()
		m.status = "vim normal"
		return m, true, nil
	case operator == "c" && key == "$":
		m.deleteVimToLineEnd()
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	default:
		m.status = "vim normal"
		return m, true, nil
	}
}

func (m *model) moveVimWordForward() {
	value := m.textarea.Value()
	runes := []rune(value)
	if len(runes) == 0 {
		return
	}
	col := clampIndex(m.vimCursorColumn(), len(runes)+1)
	if col >= len(runes) {
		m.textarea.CursorEnd()
		return
	}
	for col < len(runes) && !isVimWordRune(runes[col]) {
		col++
	}
	for col < len(runes) && isVimWordRune(runes[col]) {
		col++
	}
	for col < len(runes) && !isVimWordRune(runes[col]) {
		col++
	}
	m.textarea.SetCursor(min(col, len(runes)))
}

func (m *model) moveVimWordBackward() {
	value := m.textarea.Value()
	runes := []rune(value)
	if len(runes) == 0 {
		return
	}
	col := clampIndex(m.vimCursorColumn(), len(runes)+1)
	if col <= 0 {
		m.textarea.CursorStart()
		return
	}
	col--
	for col > 0 && !isVimWordRune(runes[col]) {
		col--
	}
	for col > 0 && isVimWordRune(runes[col-1]) {
		col--
	}
	m.textarea.SetCursor(col)
}

func (m *model) deleteVimToLineEnd() {
	value := m.textarea.Value()
	runes := []rune(value)
	if len(runes) == 0 {
		return
	}
	col := clampIndex(m.vimCursorColumn(), len(runes)+1)
	m.pushComposerUndoValue(value)
	m.textarea.SetValue(string(runes[:min(col, len(runes))]))
	m.textarea.SetCursor(min(col, len([]rune(m.textarea.Value()))))
	m.matches = nil
	m.selected = 0
	m.refreshCompletionMenu()
}

func (m *model) clearVimComposer(insert bool) {
	m.pushComposerUndo()
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.historyPos = -1
	m.vimNormal = !insert
	if insert {
		m.status = "vim insert"
	} else {
		m.status = "vim normal"
	}
}

func (m model) vimCursorColumn() int {
	info := m.textarea.LineInfo()
	return info.StartColumn + info.ColumnOffset
}

func isVimWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func normalizeTUIKeybindings(bindings map[string][]string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for action, keys := range bindings {
		normalizedAction := normalizeTUIAction(action)
		if normalizedAction == "" {
			continue
		}
		for _, key := range keys {
			normalizedKey := normalizeTUIKey(key)
			if normalizedKey == "" {
				continue
			}
			if out[normalizedAction] == nil {
				out[normalizedAction] = map[string]bool{}
			}
			out[normalizedAction][normalizedKey] = true
		}
	}
	return out
}

func normalizeTUIContextKeybindings(contexts map[string]map[string][]string) map[string]map[string]map[string]bool {
	out := map[string]map[string]map[string]bool{}
	for contextName, bindings := range contexts {
		normalizedContext := normalizeTUIContext(contextName)
		if normalizedContext == "" {
			continue
		}
		normalizedBindings := normalizeTUIKeybindings(bindings)
		if len(normalizedBindings) == 0 {
			continue
		}
		out[normalizedContext] = normalizedBindings
	}
	return out
}

func normalizeTUIContext(contextName string) string {
	contextName = strings.ToLower(strings.TrimSpace(contextName))
	contextName = strings.ReplaceAll(contextName, "_", "-")
	return strings.Join(strings.Fields(contextName), "-")
}

func normalizeTUIAction(action string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(action)), " "))
}

func normalizeTUIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	fields := strings.Fields(key)
	if len(fields) > 1 {
		normalized := make([]string, 0, len(fields))
		for _, field := range fields {
			part := normalizeTUIKey(field)
			if part == "" {
				return ""
			}
			normalized = append(normalized, part)
		}
		return strings.Join(normalized, " ")
	}
	lower := strings.ToLower(key)
	lower = strings.ReplaceAll(lower, " ", "")
	lower = strings.ReplaceAll(lower, "-", "+")
	parts := strings.Split(lower, "+")
	if len(parts) == 0 {
		return ""
	}
	modSeen := map[string]bool{}
	keyPart := ""
	for _, part := range parts {
		token := normalizeTUIKeyToken(part)
		if token == "" {
			continue
		}
		if isTUIKeyModifier(token) {
			modSeen[token] = true
			continue
		}
		keyPart = token
	}
	if keyPart == "" {
		return ""
	}
	normalized := []string{}
	for _, modifier := range []string{"ctrl", "alt", "shift", "meta"} {
		if modSeen[modifier] {
			normalized = append(normalized, modifier)
		}
	}
	normalized = append(normalized, keyPart)
	return strings.Join(normalized, "+")
}

func normalizeTUIKeyToken(token string) string {
	switch strings.TrimSpace(token) {
	case "control", "ctl":
		return "ctrl"
	case "cmd", "command", "super":
		return "meta"
	case "option":
		return "alt"
	case "escape":
		return "esc"
	case "return":
		return "enter"
	case "spacebar":
		return "space"
	default:
		return token
	}
}

func isTUIKeyModifier(token string) bool {
	switch token {
	case "ctrl", "alt", "shift", "meta":
		return true
	default:
		return false
	}
}

func (m model) openExternalEditor() (tea.Model, tea.Cmd) {
	if m.busy || m.todosOpen || m.externalEditor == nil {
		return m, nil
	}
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.matches = nil
	m.selected = 0
	m.keyChordPrefix = ""
	m.ctrlXChord = false
	m.status = "editing"
	return m, runExternalEditorCommand(m.ctx, m.externalEditor, m.textarea.Value())
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
		if len(m.attachments) > 0 {
			m.openAttachmentsPanel()
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: renderAttachmentSummary(m.attachments)})
		} else {
			m.closeAttachmentsPanel()
			m.status = "attachments"
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: renderAttachmentSummary(m.attachments)})
		}
		m.refreshViewport()
		m.viewport.GotoBottom()
		return true
	}
	switch strings.ToLower(fields[1]) {
	case "clear":
		count := len(m.attachments)
		m.attachments = nil
		m.closeAttachmentsPanel()
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
		m.normalizeAttachmentSelection()
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
	m.normalizeAttachmentSelection()
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Added %d %s for the next prompt.\n%s", added, plural("attachment", added), renderAttachmentSummary(m.attachments))})
	m.refreshViewport()
	m.viewport.GotoBottom()
	return true
}

func isLocalPasteInput(value string) bool {
	fields := strings.Fields(value)
	return len(fields) == 1 && strings.EqualFold(fields[0], "/paste")
}

func isBashModeInput(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "!")
}

func bashModeCommand(value string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "!"))
}

func (m *model) removeAttachment(indexText string) bool {
	var index int
	if _, err := fmt.Sscanf(strings.TrimSpace(indexText), "%d", &index); err != nil || index < 1 || index > len(m.attachments) {
		return false
	}
	m.attachments = append(append([]string(nil), m.attachments[:index-1]...), m.attachments[index:]...)
	m.normalizeAttachmentSelection()
	return true
}

func (m *model) removeLastAttachment() {
	if len(m.attachments) == 0 {
		m.status = "no attachments"
		return
	}
	removed := m.attachments[len(m.attachments)-1]
	m.attachments = append([]string(nil), m.attachments[:len(m.attachments)-1]...)
	m.normalizeAttachmentSelection()
	m.status = "attachment removed"
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Removed attachment: %s\n%s", removed, renderAttachmentSummary(m.attachments))})
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) openAttachmentsPanel() {
	if len(m.attachments) == 0 {
		m.closeAttachmentsPanel()
		m.status = "no attachments"
		return
	}
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.matches = nil
	m.selected = 0
	m.searchOpen = false
	m.attachmentsOpen = true
	m.normalizeAttachmentSelection()
	m.status = "attachments"
}

func (m *model) closeAttachmentsPanel() {
	m.attachmentsOpen = false
	m.attachmentSelected = 0
	if !m.busy && !m.backgrounding {
		m.status = m.mode()
	}
}

func (m *model) normalizeAttachmentSelection() {
	if len(m.attachments) == 0 {
		m.attachmentsOpen = false
		m.attachmentSelected = 0
		return
	}
	m.attachmentSelected = clampIndex(m.attachmentSelected, len(m.attachments))
}

func (m *model) moveAttachmentSelection(delta int) {
	if len(m.attachments) == 0 {
		m.closeAttachmentsPanel()
		return
	}
	m.attachmentSelected = (m.attachmentSelected + delta + len(m.attachments)) % len(m.attachments)
	m.status = "attachments"
}

func (m *model) removeSelectedAttachment() {
	if len(m.attachments) == 0 {
		m.closeAttachmentsPanel()
		m.status = "no attachments"
		return
	}
	m.normalizeAttachmentSelection()
	removed := m.attachments[m.attachmentSelected]
	m.attachments = append(append([]string(nil), m.attachments[:m.attachmentSelected]...), m.attachments[m.attachmentSelected+1:]...)
	m.normalizeAttachmentSelection()
	m.status = "attachment removed"
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Removed attachment: %s\n%s", removed, renderAttachmentSummary(m.attachments))})
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) openDiffDialog(sources []DiffSource) {
	m.diffSources = normalizeDiffSources(sources)
	if len(m.diffSources) == 0 {
		m.status = "no diff"
		return
	}
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.matches = nil
	m.selected = 0
	m.searchOpen = false
	m.quickOpen = false
	m.globalSearch = false
	m.todosOpen = false
	m.modelPicker = false
	m.messageActions = false
	m.attachmentsOpen = false
	m.diffDialog = true
	m.diffDetail = false
	m.diffSourceSelected = clampIndex(m.diffSourceSelected, len(m.diffSources))
	m.diffFileSelected = clampIndex(m.diffFileSelected, len(m.currentDiffSource().Files))
	m.status = "diff"
}

func (m *model) closeDiffDialog() {
	m.diffDialog = false
	m.diffDetail = false
	m.diffSources = nil
	m.diffSourceSelected = 0
	m.diffFileSelected = 0
	if !m.busy && !m.backgrounding {
		m.status = m.mode()
	}
}

func (m model) currentDiffSource() DiffSource {
	if len(m.diffSources) == 0 {
		return DiffSource{}
	}
	return m.diffSources[clampIndex(m.diffSourceSelected, len(m.diffSources))]
}

func (m model) currentDiffFile() DiffFile {
	source := m.currentDiffSource()
	if len(source.Files) == 0 {
		return DiffFile{}
	}
	return source.Files[clampIndex(m.diffFileSelected, len(source.Files))]
}

func (m *model) previousDiffSourceOrBack() {
	if m.diffDetail {
		m.diffDetail = false
		m.status = "diff"
		return
	}
	m.moveDiffSource(-1)
}

func (m *model) nextDiffSource() {
	if m.diffDetail {
		return
	}
	m.moveDiffSource(1)
}

func (m *model) moveDiffSource(delta int) {
	if len(m.diffSources) <= 1 {
		return
	}
	m.diffSourceSelected = (m.diffSourceSelected + delta + len(m.diffSources)) % len(m.diffSources)
	m.diffFileSelected = 0
	m.diffDetail = false
	m.status = "diff"
}

func (m *model) moveDiffFile(delta int) {
	if m.diffDetail {
		return
	}
	source := m.currentDiffSource()
	if len(source.Files) == 0 {
		return
	}
	m.diffFileSelected = (m.diffFileSelected + delta + len(source.Files)) % len(source.Files)
	m.status = "diff"
}

func (m *model) openDiffDetail() {
	if len(m.currentDiffSource().Files) == 0 {
		return
	}
	m.diffDetail = true
	m.status = "diff detail"
}

func normalizeDiffSources(sources []DiffSource) []DiffSource {
	out := make([]DiffSource, 0, len(sources))
	for _, source := range sources {
		name := strings.TrimSpace(source.Name)
		if name == "" {
			name = "Diff"
		}
		files := make([]DiffFile, 0, len(source.Files))
		for _, file := range source.Files {
			path := strings.TrimSpace(filepathToSlash(file.Path))
			if path == "" {
				continue
			}
			status := strings.TrimSpace(file.Status)
			if status == "" {
				status = "modified"
			}
			files = append(files, DiffFile{
				Path:    path,
				Status:  status,
				Summary: strings.TrimSpace(file.Summary),
				Diff:    strings.TrimSpace(file.Diff),
			})
		}
		out = append(out, DiffSource{
			Name:     name,
			Subtitle: strings.TrimSpace(source.Subtitle),
			Files:    files,
		})
	}
	return out
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
			if entry.Text == "" && entry.Permission == nil && entry.Question == nil && entry.Tool == nil {
				return
			}
			select {
			case messages <- turnStreamMsg{Role: entry.Role, Delta: entry.Text, Permission: entry.Permission, Question: entry.Question, Tool: entry.Tool}:
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
			if entry.Text == "" && entry.Permission == nil && entry.Question == nil && entry.Tool == nil {
				return
			}
			select {
			case messages <- turnStreamMsg{Role: entry.Role, Delta: entry.Text, Permission: entry.Permission, Question: entry.Question, Tool: entry.Tool}:
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
	Role       string
	Delta      string
	Permission *PermissionRequest
	Question   *QuestionRequest
	Tool       *ToolActivity
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

type runtimeControlDoneMsg struct {
	Result RuntimeControlResult
	Err    error
}

type themeSelectDoneMsg struct {
	Result   RuntimeControlResult
	Selected string
	Previous string
	Err      error
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

func runRuntimeControlCommand(ctx context.Context, control RuntimeControlFunc) tea.Cmd {
	return func() tea.Msg {
		result, err := control(ctx)
		return runtimeControlDoneMsg{Result: result, Err: err}
	}
}

func runMessageCopyCommand(ctx context.Context, copyMessage MessageCopyFunc, text string) tea.Cmd {
	return func() tea.Msg {
		result, err := copyMessage(ctx, text)
		return runtimeControlDoneMsg{Result: result, Err: err}
	}
}

func runModelSelectCommand(ctx context.Context, selectModel ModelSelectFunc, model string) tea.Cmd {
	return func() tea.Msg {
		result, err := selectModel(ctx, model)
		return runtimeControlDoneMsg{Result: result, Err: err}
	}
}

func runThemeSelectCommand(ctx context.Context, selectTheme ThemeSelectFunc, theme string, previous string) tea.Cmd {
	return func() tea.Msg {
		result, err := selectTheme(ctx, theme)
		return themeSelectDoneMsg{Result: result, Selected: theme, Previous: previous, Err: err}
	}
}

func runConversationRestoreCommand(ctx context.Context, restore ConversationRestoreFunc, keepMessages int) tea.Cmd {
	return func() tea.Msg {
		result, err := restore(ctx, keepMessages)
		return runtimeControlDoneMsg{Result: result, Err: err}
	}
}

func runConversationForkCommand(ctx context.Context, fork ConversationForkFunc, keepMessages int) tea.Cmd {
	return func() tea.Msg {
		result, err := fork(ctx, keepMessages)
		return runtimeControlDoneMsg{Result: result, Err: err}
	}
}

func runConversationSummarizeCommand(ctx context.Context, summarize ConversationSummarizeFunc, keepMessages int) tea.Cmd {
	return func() tea.Msg {
		result, err := summarize(ctx, keepMessages)
		return runtimeControlDoneMsg{Result: result, Err: err}
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
	if answer == "" || (m.permissionRespond == nil && m.permissionAnswer == nil) {
		return
	}
	switch answer {
	case "y", "yes":
		answer = "y"
	case "a", "always":
		answer = "a"
	case "n", "no":
		answer = "n"
	default:
		return
	}
	if m.permissionInput {
		m.savePermissionInputValue()
	}
	response := PermissionResponse{}
	switch answer {
	case "y":
		response.Decision = "allow_once"
		response.Feedback = strings.TrimSpace(m.permissionAcceptFeedback)
	case "a":
		response.Decision = "allow_always"
		response.Feedback = strings.TrimSpace(m.permissionAcceptFeedback)
		response.Rule = strings.TrimSpace(m.permissionRule)
	case "n":
		response.Decision = "deny"
		response.Feedback = strings.TrimSpace(m.permissionRejectFeedback)
	}
	if m.permissionRespond != nil {
		m.permissionRespond(response)
	} else {
		m.permissionAnswer(answer)
	}
	m.closePermissionRequest()
	m.status = "permission answered"
}

func (m *model) answerQuestion() {
	answer := strings.TrimSpace(m.textarea.Value())
	if !m.questionLegacy {
		if answer == "" {
			m.status = "answer required"
			return
		}
		m.setQuestionCustomAnswer(answer)
		m.textarea.SetValue("")
		m.questionCustom = false
		m.advanceQuestion()
		return
	}
	m.answerQuestionValue(answer)
}

func (m *model) answerQuestionValue(answer string) {
	if m.questionAnswer == nil {
		return
	}
	answer = strings.TrimSpace(answer)
	m.questionAnswer(answer)
	displayAnswer := answer
	if displayAnswer == "" && m.questionRequest != nil {
		displayAnswer = strings.TrimSpace(m.questionRequest.Default)
	}
	if displayAnswer == "" {
		displayAnswer = "(no response)"
	}
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: displayAnswer})
	m.textarea.SetValue("")
	m.textarea.Placeholder = "Ask codog..."
	m.matches = nil
	m.selected = 0
	m.closeQuestionRequest()
	m.status = "question answered"
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) openPermissionRequest(request PermissionRequest) {
	request.Tool = strings.TrimSpace(request.Tool)
	request.Required = strings.TrimSpace(request.Required)
	request.Input = strings.TrimSpace(request.Input)
	request.Message = strings.TrimSpace(request.Message)
	request.SuggestedRule = strings.TrimSpace(request.SuggestedRule)
	m.permissionRequest = &request
	m.permissionSelected = 0
	m.permissionInput = false
	m.permissionInputAnswer = ""
	m.permissionAcceptFeedback = ""
	m.permissionRejectFeedback = ""
	m.permissionRule = request.SuggestedRule
	m.permissionComposerDraft = ""
	m.permissionDraftCaptured = false
	m.awaitingPermission = true
	m.status = "permission"
}

func (m *model) closePermissionRequest() {
	if m.permissionInput {
		m.savePermissionInputValue()
	}
	m.restorePermissionComposer()
	m.awaitingPermission = false
	m.permissionRequest = nil
	m.permissionSelected = 0
	m.permissionInput = false
	m.permissionInputAnswer = ""
	m.permissionAcceptFeedback = ""
	m.permissionRejectFeedback = ""
	m.permissionRule = ""
	m.permissionComposerDraft = ""
	m.permissionDraftCaptured = false
}

func (m *model) updatePermissionRequest(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.permissionInput {
		switch msg.String() {
		case "tab", "esc":
			m.collapsePermissionInput()
			return *m, nil
		case "ctrl+c":
			m.answerPermission("n")
			return *m, nil
		case "enter":
			if strings.TrimSpace(m.textarea.Value()) == "" {
				m.collapsePermissionInput()
				return *m, nil
			}
			m.answerPermission(m.permissionInputAnswer)
			return *m, nil
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return *m, cmd
	}
	switch msg.String() {
	case "up", "left", "ctrl+p", "k", "shift+tab":
		m.movePermissionSelection(-1)
	case "down", "right", "ctrl+n", "j":
		m.movePermissionSelection(1)
	case "tab":
		answers := m.permissionAnswers()
		if len(answers) > 0 {
			m.beginPermissionInput(answers[clampIndex(m.permissionSelected, len(answers))])
		}
	case "home":
		m.permissionSelected = 0
	case "end":
		m.permissionSelected = len(m.permissionAnswers()) - 1
	case "enter":
		answers := m.permissionAnswers()
		if len(answers) > 0 {
			m.answerPermission(answers[clampIndex(m.permissionSelected, len(answers))])
		}
	case "y", "Y":
		m.answerPermission("y")
	case "a", "A":
		if m.permissionRequest != nil && m.permissionRequest.AllowAlways {
			m.answerPermission("a")
		}
	case "n", "N", "esc", "ctrl+c":
		m.answerPermission("n")
	}
	return *m, nil
}

func (m *model) beginPermissionInput(answer string) {
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "a" && answer != "n" {
		return
	}
	if answer == "a" && (m.permissionRequest == nil || !m.permissionRequest.AllowAlways) {
		return
	}
	if !m.permissionDraftCaptured {
		m.permissionComposerDraft = m.textarea.Value()
		m.permissionDraftCaptured = true
	}
	m.permissionInput = true
	m.permissionInputAnswer = answer
	value := ""
	switch answer {
	case "y":
		value = m.permissionAcceptFeedback
		m.textarea.Placeholder = "Tell codog what to do next..."
	case "a":
		value = m.permissionRule
		m.textarea.Placeholder = "Command, path, or rule to allow this session..."
	case "n":
		value = m.permissionRejectFeedback
		m.textarea.Placeholder = "Tell codog what to do differently..."
	}
	m.textarea.SetValue(value)
	m.textarea.CursorEnd()
	m.status = "permission"
}

func (m *model) savePermissionInputValue() {
	value := strings.TrimSpace(m.textarea.Value())
	switch m.permissionInputAnswer {
	case "y":
		m.permissionAcceptFeedback = value
	case "a":
		m.permissionRule = value
		if m.permissionRequest != nil {
			m.permissionRequest.SuggestedRule = value
		}
	case "n":
		m.permissionRejectFeedback = value
	}
}

func (m *model) collapsePermissionInput() {
	if !m.permissionInput {
		return
	}
	m.savePermissionInputValue()
	m.permissionInput = false
	m.permissionInputAnswer = ""
	m.restorePermissionComposer()
	m.status = "permission"
}

func (m *model) restorePermissionComposer() {
	if m.permissionDraftCaptured {
		m.textarea.SetValue(m.permissionComposerDraft)
		m.textarea.CursorEnd()
	}
	m.textarea.Placeholder = "Ask codog..."
}

func (m *model) permissionAnswers() []string {
	answers := []string{"y"}
	if m.permissionRequest != nil && m.permissionRequest.AllowAlways {
		answers = append(answers, "a")
	}
	return append(answers, "n")
}

func (m *model) movePermissionSelection(delta int) {
	count := len(m.permissionAnswers())
	if count == 0 {
		return
	}
	m.permissionSelected = (m.permissionSelected + delta + count) % count
	m.status = "permission"
}

func (m *model) openQuestionRequest(request QuestionRequest) {
	request.Question = strings.TrimSpace(request.Question)
	request.Default = strings.TrimSpace(request.Default)
	request.Choices = normalizeQuestionRequestChoices(request.Choices)
	m.questionLegacy = len(request.Questions) == 0
	if m.questionLegacy {
		options := make([]QuestionOption, 0, len(request.Choices))
		for _, choice := range request.Choices {
			options = append(options, QuestionOption{Label: choice})
		}
		request.Questions = []Question{{Question: request.Question, Header: "Question", Options: options}}
	} else {
		request.Questions = normalizeTUIQuestions(request.Questions)
	}
	m.questionRequest = &request
	m.questionIndex = 0
	m.questionCursors = make([]int, len(request.Questions))
	m.questionSelections = make([][]bool, len(request.Questions))
	m.questionCustomValues = make([]string, len(request.Questions))
	for questionIndex, question := range request.Questions {
		m.questionSelections[questionIndex] = make([]bool, len(question.Options))
		if m.questionLegacy {
			for optionIndex, option := range question.Options {
				if strings.EqualFold(option.Label, request.Default) {
					m.questionCursors[questionIndex] = optionIndex
					break
				}
			}
		}
	}
	m.questionSelected = m.questionCursors[0]
	m.questionCustom = len(request.Questions[0].Options) == 0
	m.awaitingQuestion = true
	if m.questionCustom {
		m.textarea.Placeholder = "Type your answer..."
	}
	m.status = "question"
}

func normalizeTUIQuestions(questions []Question) []Question {
	out := make([]Question, 0, len(questions))
	for index, question := range questions {
		question.Question = strings.TrimSpace(question.Question)
		question.Header = strings.TrimSpace(question.Header)
		if question.Header == "" {
			question.Header = fmt.Sprintf("Q%d", index+1)
		}
		options := make([]QuestionOption, 0, len(question.Options))
		seen := map[string]struct{}{}
		for _, option := range question.Options {
			option.Label = strings.TrimSpace(option.Label)
			option.Description = strings.TrimSpace(option.Description)
			option.Preview = strings.TrimSpace(option.Preview)
			key := strings.ToLower(option.Label)
			if option.Label == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			options = append(options, option)
		}
		question.Options = options
		out = append(out, question)
	}
	if len(out) == 0 {
		out = append(out, Question{Question: "Choose an answer", Header: "Question"})
	}
	return out
}

func normalizeQuestionRequestChoices(choices []string) []string {
	out := make([]string, 0, len(choices))
	seen := map[string]struct{}{}
	for _, choice := range choices {
		choice = strings.TrimSpace(choice)
		key := strings.ToLower(choice)
		if choice == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, choice)
	}
	return out
}

func (m *model) closeQuestionRequest() {
	m.awaitingQuestion = false
	m.questionRequest = nil
	m.questionSelected = 0
	m.questionCustom = false
	m.questionIndex = 0
	m.questionLegacy = false
	m.questionCursors = nil
	m.questionSelections = nil
	m.questionCustomValues = nil
	m.textarea.Placeholder = "Ask codog..."
}

func (m *model) updateQuestionRequest(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.questionCustom {
		switch msg.String() {
		case "esc", "ctrl+c":
			m.closeQuestionRequest()
			m.interruptTurn()
			return *m, nil
		case "enter":
			m.answerQuestion()
			return *m, nil
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return *m, cmd
	}

	if m.questionRequest == nil || len(m.questionRequest.Questions) == 0 {
		return *m, nil
	}
	if m.questionIndex >= len(m.questionRequest.Questions) {
		return m.updateQuestionReview(msg)
	}
	question := m.questionRequest.Questions[m.questionIndex]
	choiceCount := len(question.Options)
	optionCount := choiceCount + 1
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeQuestionRequest()
		m.interruptTurn()
	case "up", "ctrl+p", "k":
		m.questionSelected = (m.questionSelected - 1 + optionCount) % optionCount
		m.saveQuestionCursor()
	case "down", "ctrl+n", "j":
		m.questionSelected = (m.questionSelected + 1) % optionCount
		m.saveQuestionCursor()
	case "left", "shift+tab":
		if m.questionLegacy {
			m.questionSelected = (m.questionSelected - 1 + optionCount) % optionCount
			m.saveQuestionCursor()
		} else {
			m.moveQuestionTab(-1)
		}
	case "right", "tab":
		if m.questionLegacy {
			m.questionSelected = (m.questionSelected + 1) % optionCount
			m.saveQuestionCursor()
		} else {
			m.moveQuestionTab(1)
		}
	case "home":
		m.questionSelected = 0
		m.saveQuestionCursor()
	case "end":
		m.questionSelected = optionCount - 1
		m.saveQuestionCursor()
	case " ", "space":
		if !m.questionLegacy && question.MultiSelect && m.questionSelected < choiceCount {
			m.toggleQuestionSelection(m.questionSelected)
		}
	case "enter":
		if m.questionSelected >= choiceCount {
			m.beginQuestionCustomInput()
		} else if m.questionLegacy {
			m.answerQuestionValue(question.Options[m.questionSelected].Label)
		} else if question.MultiSelect {
			if !m.questionAnswered(m.questionIndex) {
				m.toggleQuestionSelection(m.questionSelected)
			}
			m.advanceQuestion()
		} else {
			m.selectSingleQuestionOption(m.questionSelected)
			m.advanceQuestion()
		}
	default:
		if index, ok := questionNumberShortcut(msg, choiceCount); ok {
			m.questionSelected = index
			m.saveQuestionCursor()
			return *m, nil
		}
		if len(msg.Runes) > 0 || msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete {
			m.beginQuestionCustomInput()
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			return *m, cmd
		}
	}
	return *m, nil
}

func (m *model) beginQuestionCustomInput() {
	if question := m.currentQuestion(); question != nil {
		m.questionSelected = len(question.Options)
		m.saveQuestionCursor()
		if m.questionIndex < len(m.questionCustomValues) {
			m.textarea.SetValue(m.questionCustomValues[m.questionIndex])
			m.textarea.CursorEnd()
		}
	}
	m.questionCustom = true
	m.textarea.Placeholder = "Type your answer..."
	m.status = "question"
}

func (m *model) currentQuestion() *Question {
	if m.questionRequest == nil || m.questionIndex < 0 || m.questionIndex >= len(m.questionRequest.Questions) {
		return nil
	}
	return &m.questionRequest.Questions[m.questionIndex]
}

func (m *model) saveQuestionCursor() {
	if m.questionIndex >= 0 && m.questionIndex < len(m.questionCursors) {
		m.questionCursors[m.questionIndex] = m.questionSelected
	}
}

func (m *model) moveQuestionTab(delta int) {
	if m.questionRequest == nil {
		return
	}
	count := len(m.questionRequest.Questions) + 1
	m.saveQuestionCursor()
	m.questionIndex = (m.questionIndex + delta + count) % count
	m.questionCustom = false
	m.textarea.SetValue("")
	m.textarea.Placeholder = "Ask codog..."
	if m.questionIndex < len(m.questionCursors) {
		m.questionSelected = m.questionCursors[m.questionIndex]
	}
	m.status = "question"
}

func (m *model) updateQuestionReview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeQuestionRequest()
		m.interruptTurn()
	case "left", "shift+tab", "up", "k":
		m.moveQuestionTab(-1)
	case "home":
		m.questionIndex = 0
		m.questionSelected = m.questionCursors[0]
	case "enter":
		if index := m.firstUnansweredQuestion(); index >= 0 {
			m.questionIndex = index
			m.questionSelected = m.questionCursors[index]
			m.status = "answer required"
			return *m, nil
		}
		m.submitModernQuestionAnswers()
	}
	return *m, nil
}

func (m *model) selectSingleQuestionOption(optionIndex int) {
	if m.questionIndex < 0 || m.questionIndex >= len(m.questionSelections) {
		return
	}
	for index := range m.questionSelections[m.questionIndex] {
		m.questionSelections[m.questionIndex][index] = index == optionIndex
	}
	m.questionCustomValues[m.questionIndex] = ""
}

func (m *model) toggleQuestionSelection(optionIndex int) {
	if m.questionIndex < 0 || m.questionIndex >= len(m.questionSelections) || optionIndex < 0 || optionIndex >= len(m.questionSelections[m.questionIndex]) {
		return
	}
	m.questionSelections[m.questionIndex][optionIndex] = !m.questionSelections[m.questionIndex][optionIndex]
}

func (m *model) setQuestionCustomAnswer(answer string) {
	if m.questionIndex < 0 || m.questionIndex >= len(m.questionCustomValues) {
		return
	}
	question := m.currentQuestion()
	if question == nil {
		return
	}
	if !question.MultiSelect {
		for index := range m.questionSelections[m.questionIndex] {
			m.questionSelections[m.questionIndex][index] = false
		}
	}
	m.questionCustomValues[m.questionIndex] = strings.TrimSpace(answer)
}

func (m *model) questionAnswered(index int) bool {
	if index < 0 || index >= len(m.questionSelections) {
		return false
	}
	if index < len(m.questionCustomValues) && strings.TrimSpace(m.questionCustomValues[index]) != "" {
		return true
	}
	for _, selected := range m.questionSelections[index] {
		if selected {
			return true
		}
	}
	return false
}

func (m *model) firstUnansweredQuestion() int {
	if m.questionRequest == nil {
		return -1
	}
	for index := range m.questionRequest.Questions {
		if !m.questionAnswered(index) {
			return index
		}
	}
	return -1
}

func (m *model) advanceQuestion() {
	if m.questionLegacy {
		return
	}
	if m.questionRequest == nil {
		return
	}
	if len(m.questionRequest.Questions) == 1 {
		m.submitModernQuestionAnswers()
		return
	}
	m.saveQuestionCursor()
	m.questionIndex = min(m.questionIndex+1, len(m.questionRequest.Questions))
	m.questionCustom = false
	m.textarea.SetValue("")
	m.textarea.Placeholder = "Ask codog..."
	if m.questionIndex < len(m.questionCursors) {
		m.questionSelected = m.questionCursors[m.questionIndex]
	}
	m.status = "question"
}

func (m *model) submitModernQuestionAnswers() {
	if m.questionAnswer == nil || m.questionRequest == nil {
		return
	}
	answers := make(map[string]string, len(m.questionRequest.Questions))
	display := make([]string, 0, len(m.questionRequest.Questions))
	for questionIndex, question := range m.questionRequest.Questions {
		parts := []string{}
		for optionIndex, option := range question.Options {
			if questionIndex < len(m.questionSelections) && optionIndex < len(m.questionSelections[questionIndex]) && m.questionSelections[questionIndex][optionIndex] {
				parts = append(parts, option.Label)
			}
		}
		if questionIndex < len(m.questionCustomValues) {
			if custom := strings.TrimSpace(m.questionCustomValues[questionIndex]); custom != "" {
				parts = append(parts, custom)
			}
		}
		answer := strings.Join(parts, ", ")
		answers[question.Question] = answer
		display = append(display, question.Header+": "+answer)
	}
	payload, err := json.Marshal(answers)
	if err != nil {
		m.status = "question error"
		return
	}
	m.questionAnswer(string(payload))
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: strings.Join(display, "\n")})
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.closeQuestionRequest()
	m.status = "question answered"
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func questionNumberShortcut(msg tea.KeyMsg, choiceCount int) (int, bool) {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 || msg.Runes[0] < '1' || msg.Runes[0] > '9' {
		return 0, false
	}
	index := int(msg.Runes[0] - '1')
	return index, index < choiceCount
}

func (m *model) clearInteractionPrompts() {
	m.closePermissionRequest()
	m.closeQuestionRequest()
}

func (m *model) armExit(key string, status string) {
	m.exitPending = true
	m.exitKey = key
	m.status = status
}

func (m *model) clearExitPending() {
	m.exitPending = false
	m.exitKey = ""
}

func (m *model) clearScreen() {
	m.helpOpen = false
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.clearExitPending()
	m.searchOpen = false
	m.searchHits = nil
	m.searchPos = 0
	m.undoStack = nil
	m.keyChordPrefix = ""
	m.ctrlXChord = false
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
	m.modelPicker = false
	m.modelPickerSelected = 0
	m.messageActions = false
	m.messageActionTarget = 0
	m.messageActionSelected = 0
	if m.inline {
		m.printedEntries = 0
		m.initialPrint = ""
	}
	m.transcript = []transcriptEntry{{Role: "system", Text: "Screen cleared."}}
	m.status = "cleared"
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func isPermissionRequestDelta(delta string) bool {
	normalized := strings.ToLower(delta)
	return strings.Contains(normalized, " requires ")
}

func cloneToolActivity(activity *ToolActivity) *ToolActivity {
	if activity == nil {
		return nil
	}
	cloned := *activity
	return &cloned
}

func (m *model) appendStreamEntry(msg turnStreamMsg) {
	if msg.Tool != nil {
		m.upsertToolActivity(msg.Role, msg.Delta, *msg.Tool)
		return
	}
	m.appendStreamDelta(msg.Role, msg.Delta)
}

func (m *model) upsertToolActivity(role string, text string, activity ToolActivity) {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "tool"
	}
	activity.ID = strings.TrimSpace(activity.ID)
	activity.Name = strings.TrimSpace(activity.Name)
	activity.Input = strings.TrimSpace(activity.Input)
	activity.Output = strings.TrimSpace(activity.Output)
	activity.Status = strings.ToLower(strings.TrimSpace(activity.Status))
	if activity.IsError {
		activity.Status = "error"
	}
	switch activity.Status {
	case "running", "success", "error":
	default:
		activity.Status = "running"
	}
	entry := transcriptEntry{Role: role, Text: text, Tool: cloneToolActivity(&activity)}
	if activity.ID != "" {
		for index := len(m.transcript) - 1; index >= 0; index-- {
			existing := m.transcript[index].Tool
			if existing == nil || existing.ID != activity.ID {
				continue
			}
			m.transcript[index] = entry
			m.streamingIndex = index
			return
		}
	}
	m.transcript = append(m.transcript, entry)
	m.streamingIndex = len(m.transcript) - 1
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
	if len(candidates) == 0 && isBashModeInput(value) {
		if completion, ok := m.bashHistoryCompletion(value); ok {
			m.textarea.SetValue(completeValue(completion))
			m.textarea.CursorEnd()
			m.matches = nil
			m.selected = 0
			m.commandArgumentHint = ""
			m.inlineGhostText = ""
			return m
		}
	}
	if !strings.HasPrefix(value, "/") && !isBashModeInput(value) {
		if completion, ok := m.midInputSlashCompletion(value); ok {
			m.textarea.SetValue(value[:completion.start] + completeValue(completion.candidate))
			m.textarea.CursorEnd()
			m.matches = nil
			m.selected = 0
			m.commandArgumentHint = slashCommandArgumentHint(m.textarea.Value())
			m.inlineGhostText = ""
			return m
		}
	}
	switch len(candidates) {
	case 0:
		m.matches = nil
		m.selected = 0
	case 1:
		m.textarea.SetValue(m.completeValue(value, candidates[0]))
		m.matches = nil
		m.selected = 0
		m.commandArgumentHint = slashCommandArgumentHint(m.textarea.Value())
		m.inlineGhostText = ""
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
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	if value == "" || m.busy || m.searchOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.messageActions || m.attachmentsOpen || m.diffDialog {
		m.matches = nil
		m.selected = 0
		return
	}
	m.commandArgumentHint = slashCommandArgumentHint(value)
	candidates := m.filteredCompletionCandidates(value)
	if len(candidates) == 0 && isBashModeInput(value) {
		if completion, ok := m.bashHistoryCompletion(value); ok {
			m.inlineGhostText = completion
		}
		m.matches = nil
		m.selected = 0
		return
	}
	if len(candidates) == 0 && !strings.HasPrefix(value, "/") {
		if completion, ok := m.midInputSlashCompletion(value); ok {
			m.inlineGhostText = completion.display()
		}
		m.matches = nil
		m.selected = 0
		return
	}
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
	if isBashModeInput(value) {
		if prefix, ok := activeBashPathPrefix(value); ok {
			return filterBashPathCandidates(prefix, m.fileCandidates)
		}
		return nil
	}
	if strings.HasPrefix(value, "/") {
		return slash.FilterCandidates(value, m.completionCandidates())
	}
	if prefix, ok := activeFileReferencePrefix(value); ok {
		return filterFileReferenceCandidates(prefix, m.fileCandidates)
	}
	return nil
}

func (m model) bashHistoryCompletion(value string) (string, bool) {
	value = strings.TrimRight(value, "\r\n\t")
	if !isBashModeInput(value) {
		return "", false
	}
	command := strings.TrimSpace(bashModeCommand(value))
	if command == "" {
		return "", false
	}
	normalized := strings.TrimSpace(value)
	for index := len(m.history) - 1; index >= 0; index-- {
		entry := strings.TrimSpace(m.history[index])
		if entry == "" || entry == normalized || !isBashModeInput(entry) {
			continue
		}
		if strings.HasPrefix(entry, normalized) {
			return entry, true
		}
	}
	return "", false
}

type midInputSlashCompletion struct {
	start     int
	token     string
	candidate string
	suffix    string
}

func (m model) midInputSlashCompletion(value string) (midInputSlashCompletion, bool) {
	token, start, ok := trailingMidInputSlashToken(value)
	if !ok {
		return midInputSlashCompletion{}, false
	}
	candidates := slash.FilterCandidates(token, m.completionCandidates())
	if len(candidates) == 0 {
		candidates = slash.SuggestWithCandidates(token, 1, m.completionCandidates())
	}
	if len(candidates) == 0 {
		return midInputSlashCompletion{}, false
	}
	candidate := firstSlashCommandToken(candidates[0])
	if candidate == "" || !strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(token)) {
		return midInputSlashCompletion{}, false
	}
	suffix := candidate[len(token):]
	if suffix == "" {
		return midInputSlashCompletion{}, false
	}
	return midInputSlashCompletion{start: start, token: token, candidate: candidate, suffix: suffix}, true
}

func (completion midInputSlashCompletion) display() string {
	if completion.token == "" || completion.suffix == "" {
		return ""
	}
	return completion.token + completion.suffix
}

func trailingMidInputSlashToken(value string) (string, int, bool) {
	if strings.HasPrefix(value, "/") {
		return "", 0, false
	}
	trimmed := strings.TrimRight(value, " \t\r\n")
	if trimmed == "" || len(trimmed) != len(value) {
		return "", 0, false
	}
	start := strings.LastIndexAny(trimmed, " \t\r\n")
	if start < 0 {
		return "", 0, false
	}
	tokenStart := start + 1
	token := trimmed[tokenStart:]
	if len(token) <= 1 || !strings.HasPrefix(token, "/") {
		return "", 0, false
	}
	if strings.ContainsAny(strings.TrimPrefix(token, "/"), `/\ "'`) {
		return "", 0, false
	}
	return token, tokenStart, true
}

func firstSlashCommandToken(candidate string) string {
	fields := strings.Fields(strings.TrimSpace(candidate))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	return fields[0]
}

func slashCommandArgumentHint(value string) string {
	if !strings.HasPrefix(strings.TrimSpace(value), "/") {
		return ""
	}
	trimmedRight := strings.TrimRight(value, "\r\n\t")
	fields := strings.Fields(trimmedRight)
	if len(fields) == 0 {
		return ""
	}
	if !strings.ContainsAny(trimmedRight, " \t") {
		return ""
	}
	spec, ok := slash.Lookup(fields[0])
	if !ok {
		return ""
	}
	usage := strings.TrimSpace(spec.Usage)
	if usage == "" {
		usage = spec.Name
	}
	args := strings.TrimSpace(strings.TrimPrefix(usage, spec.Name))
	if args == "" {
		return "usage: " + usage
	}
	return "arguments: " + args + "  ·  " + spec.Description
}

func isExactSlashCommandInput(value string) bool {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 1 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	_, ok := slash.Lookup(fields[0])
	return ok
}

func shouldAcceptCompletionOnEnter(value string) bool {
	return !isExactSlashCommandInput(value) && !isREPLExitInput(value) && !isLocalHelpInput(value)
}

func automaticCompletionCandidates(value string, candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(candidates))
	normalizedValue := strings.TrimSpace(value)
	exactCommand := ""
	if isExactSlashCommandInput(normalizedValue) {
		exactCommand = firstSlashCommandToken(normalizedValue)
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == normalizedValue && !strings.HasSuffix(candidate, " ") {
			continue
		}
		if exactCommand != "" && firstSlashCommandToken(candidate) != exactCommand {
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
	m.commandArgumentHint = slashCommandArgumentHint(m.textarea.Value())
	m.inlineGhostText = ""
	return m
}

func (m model) completeValue(value string, candidate string) string {
	if isBashModeInput(value) {
		return completeBashPathValue(value, candidate)
	}
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

func activeBashPathPrefix(value string) (string, bool) {
	value = strings.TrimRight(value, "\r\n\t")
	if !isBashModeInput(value) {
		return "", false
	}
	body := strings.TrimPrefix(strings.TrimSpace(value), "!")
	if strings.TrimSpace(body) == "" || strings.HasSuffix(body, " ") || strings.HasSuffix(body, "\t") {
		return "", false
	}
	index := strings.LastIndexAny(body, " \t\n")
	if index >= 0 {
		body = body[index+1:]
	}
	body = strings.Trim(body, `"'`)
	if body == "" || strings.HasPrefix(body, "-") || strings.HasPrefix(body, "$") {
		return "", false
	}
	return filepathToSlash(body), true
}

func filterBashPathCandidates(prefix string, files []string) []string {
	query := strings.ToLower(strings.TrimSpace(filepathToSlash(prefix)))
	if query == "" {
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
		if !strings.HasPrefix(lower, query) && !strings.Contains(lower, "/"+query) {
			continue
		}
		seen[file] = true
		out = append(out, file)
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

func completeBashPathValue(value string, candidate string) string {
	candidate = strings.TrimSpace(filepathToSlash(candidate))
	if candidate == "" {
		return value
	}
	trimmedRight := strings.TrimRight(value, "\r\n\t")
	searchStart := strings.LastIndexAny(trimmedRight, " \t\n")
	if searchStart < 0 {
		searchStart = 0
	} else {
		searchStart++
	}
	if searchStart < len(trimmedRight) && (trimmedRight[searchStart] == '"' || trimmedRight[searchStart] == '\'') {
		searchStart++
	}
	return trimmedRight[:searchStart] + completeValue(candidate)
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

func (m *model) pushComposerUndo() {
	m.pushComposerUndoValue(m.textarea.Value())
}

func (m *model) pushComposerUndoValue(value string) {
	const maxComposerUndo = 100
	if len(m.undoStack) > 0 && m.undoStack[len(m.undoStack)-1] == value {
		return
	}
	m.undoStack = append(m.undoStack, value)
	if len(m.undoStack) > maxComposerUndo {
		m.undoStack = append([]string(nil), m.undoStack[len(m.undoStack)-maxComposerUndo:]...)
	}
}

func (m *model) undoComposer() {
	current := m.textarea.Value()
	for len(m.undoStack) > 0 {
		last := m.undoStack[len(m.undoStack)-1]
		m.undoStack = m.undoStack[:len(m.undoStack)-1]
		if last == current {
			continue
		}
		m.textarea.SetValue(last)
		m.textarea.CursorEnd()
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.keyChordPrefix = ""
		m.ctrlXChord = false
		m.status = "undo"
		m.refreshCompletionMenu()
		return
	}
	m.status = "nothing to undo"
}

func (m *model) deleteComposerBeforeCursor() {
	m.deleteComposerWithTextareaKey(tea.KeyMsg{Type: tea.KeyCtrlU}, "deleted before cursor")
}

func (m *model) deleteComposerAfterCursor() {
	m.deleteComposerWithTextareaKey(tea.KeyMsg{Type: tea.KeyCtrlK}, "deleted after cursor")
}

func (m *model) deleteComposerWithTextareaKey(key tea.KeyMsg, status string) {
	before := m.textarea.Value()
	m.textarea, _ = m.textarea.Update(key)
	after := m.textarea.Value()
	if after == before {
		m.status = "nothing to delete"
		return
	}
	m.pushComposerUndoValue(before)
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.historyPos = -1
	m.status = status
	m.refreshCompletionMenu()
}

func (m *model) moveComposerLineStart() {
	m.textarea.CursorStart()
	m.status = "line start"
}

func (m *model) moveComposerLineEnd() {
	m.textarea.CursorEnd()
	m.status = "line end"
}

func filepathToSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func renderCompletions(matches []string, selected int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(matches) == 0 {
		return ""
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	lines := []string{styles.completionTitle().Render(" suggestions ")}
	for index, match := range matches {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+completionDisplayLine(match)))
	}
	lines = append(lines, styles.completion().Render("  Enter accept · Tab complete · Esc close"))
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
		if !strings.HasPrefix(candidate, "/") && (strings.Contains(candidate, "/") || strings.Contains(candidate, ".")) {
			return truncateForComposer(candidate+"  -  file path", 120)
		}
		return candidate
	}
	return truncateForComposer(candidate+"  -  "+spec.Description, 120)
}

func renderCommandAssist(argumentHint string, inlineHint string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	lines := []string{}
	if argumentHint != "" {
		lines = append(lines, styles.completionTitle().Render(" command args "))
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(argumentHint, 120)))
	}
	if inlineHint != "" {
		if len(lines) == 0 {
			lines = append(lines, styles.completionTitle().Render(" command hint "))
		}
		lines = append(lines, styles.completion().Render("  "+truncateForComposer("ghost: "+inlineHint+"  ·  Tab accept", 120)))
	}
	return strings.Join(lines, "\n")
}

func renderHistorySearch(matches []string, selected int, query string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	query = strings.TrimSpace(query)
	title := " history "
	if query != "" {
		title = fmt.Sprintf(" history: %s ", query)
	}
	lines := []string{styles.completionTitle().Render(title)}
	if len(matches) == 0 {
		return strings.Join(append(lines, styles.completion().Render("  no matches")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	for index, match := range matches {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+truncateForComposer(match, 100)))
	}
	return strings.Join(lines, "\n")
}

func renderQueuedPrompts(queued []string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(queued) == 0 {
		return ""
	}
	lines := []string{styles.completionTitle().Render(fmt.Sprintf(" queued prompts: %d ", len(queued)))}
	start := 0
	if len(queued) > 3 {
		start = len(queued) - 3
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  ... %d earlier", start)))
	}
	for index := start; index < len(queued); index++ {
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  %d. %s", index+1, truncateForComposer(queuedPromptDisplay(queued[index]), 100))))
	}
	return strings.Join(lines, "\n")
}

func queuedPromptKind(prompt string) string {
	if isBashModeInput(prompt) {
		return "bash"
	}
	return "prompt"
}

func queuedPromptDisplay(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if isBashModeInput(prompt) {
		command := bashModeCommand(prompt)
		if command == "" {
			return "bash:"
		}
		return "bash: " + command
	}
	return prompt
}

func (m *model) openModelPicker() {
	if len(m.modelOptions) == 0 || m.selectModel == nil {
		m.status = "no model picker"
		return
	}
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.matches = nil
	m.selected = 0
	m.modelPicker = true
	m.modelPickerSelected = indexOfModelOption(m.modelOptions, m.currentModel)
	m.status = "model picker"
}

func (m *model) openThemePicker() {
	if m.selectTheme == nil {
		m.status = "no theme picker"
		return
	}
	if m.helpOpen {
		m.helpOpen = false
	}
	m.matches = nil
	m.selected = 0
	m.themePicker = true
	m.themePickerOriginal = m.theme
	m.themePickerSelected = indexOfTheme(ThemeNames(), m.theme)
	m.status = "theme picker"
	m.applyTheme()
}

func (m *model) closeThemePicker(restore bool) {
	if restore && m.themePickerOriginal != "" {
		m.theme = m.themePickerOriginal
	}
	m.themePicker = false
	m.themePickerSelected = 0
	m.themePickerOriginal = ""
	m.status = m.mode()
	m.applyTheme()
}

func (m *model) moveThemePicker(delta int) {
	options := ThemeNames()
	if len(options) == 0 {
		return
	}
	m.themePickerSelected = (m.themePickerSelected + delta + len(options)) % len(options)
	m.theme = options[m.themePickerSelected]
	m.status = "theme preview"
	m.applyTheme()
}

func (m *model) setThemePickerIndex(index int) {
	options := ThemeNames()
	if len(options) == 0 {
		return
	}
	m.themePickerSelected = clampIndex(index, len(options))
	m.theme = options[m.themePickerSelected]
	m.status = "theme preview"
	m.applyTheme()
}

func (m model) acceptThemePicker() (tea.Model, tea.Cmd) {
	options := ThemeNames()
	if len(options) == 0 || m.selectTheme == nil {
		m.closeThemePicker(true)
		return m, nil
	}
	selected := options[clampIndex(m.themePickerSelected, len(options))]
	previous := m.themePickerOriginal
	m.theme = selected
	m.themePicker = false
	m.themePickerOriginal = ""
	m.status = "saving theme"
	m.applyTheme()
	return m, runThemeSelectCommand(m.ctx, m.selectTheme, selected, previous)
}

func (m *model) applyTheme() {
	name, ok := NormalizeThemeName(m.theme)
	if !ok {
		name = "auto"
	}
	m.theme = name
	stylesForTheme(name).applyTextarea(&m.textarea)
	m.refreshViewport()
}

func (m *model) closeModelPicker() {
	m.modelPicker = false
	m.modelPickerSelected = 0
	m.status = m.mode()
}

func (m *model) moveModelPicker(delta int) {
	if len(m.modelOptions) == 0 {
		return
	}
	m.modelPickerSelected = (m.modelPickerSelected + delta + len(m.modelOptions)) % len(m.modelOptions)
	m.status = "model picker"
}

func (m *model) setModelPickerIndex(index int) {
	if len(m.modelOptions) == 0 {
		return
	}
	m.modelPickerSelected = clampIndex(index, len(m.modelOptions))
	m.status = "model picker"
}

func (m model) acceptModelPicker() (tea.Model, tea.Cmd) {
	if len(m.modelOptions) == 0 || m.selectModel == nil {
		m.closeModelPicker()
		return m, nil
	}
	if m.modelPickerSelected < 0 || m.modelPickerSelected >= len(m.modelOptions) {
		m.modelPickerSelected = 0
	}
	selected := m.modelOptions[m.modelPickerSelected]
	m.modelPicker = false
	m.currentModel = selected
	m.status = "selecting model"
	return m, runModelSelectCommand(m.ctx, m.selectModel, selected)
}

func (m *model) applyRuntimeControlResult(result RuntimeControlResult) {
	title := strings.TrimSpace(result.Title)
	if title == "" {
		title = "Runtime Control"
	}
	status := strings.TrimSpace(result.Status)
	if status == "" {
		status = strings.ToLower(title)
	}
	lines := []string{title}
	for _, line := range result.Lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 1 {
		lines = append(lines, status)
	}
	for _, line := range result.Lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && strings.EqualFold(strings.TrimRight(fields[0], ":"), "model") {
			m.currentModel = fields[1]
			break
		}
	}
	m.runtimeBadges = mergeRuntimeBadges(m.runtimeBadges, runtimeBadgesFromResult(result))
	m.status = status
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: strings.Join(lines, "\n")})
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m model) runtimeStatusBadges() []string {
	badges := []string{}
	if current := strings.TrimSpace(m.currentModel); current != "" {
		badges = append(badges, "model: "+current)
	}
	badges = append(badges, m.runtimeBadges...)
	if m.vimEnabled {
		mode := "insert"
		if m.vimNormal {
			mode = "normal"
		}
		badges = append(badges, "vim: "+mode)
	}
	return normalizeRuntimeBadges(badges)
}

func runtimeBadgesFromResult(result RuntimeControlResult) []string {
	badges := normalizeRuntimeBadges(result.Badges)
	if len(badges) > 0 {
		return badges
	}
	out := []string{}
	for _, line := range result.Lines {
		key, value, ok := splitRuntimeStatusLine(line)
		if !ok {
			continue
		}
		switch key {
		case "fast mode":
			out = append(out, "fast: "+value)
		case "reasoning":
			out = append(out, "thinking: "+value)
		case "model":
			out = append(out, "model: "+value)
		}
	}
	return normalizeRuntimeBadges(out)
}

func splitRuntimeStatusLine(line string) (string, string, bool) {
	before, after, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(before))
	value := strings.TrimSpace(after)
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func mergeRuntimeBadges(existing []string, updates []string) []string {
	out := normalizeRuntimeBadges(existing)
	for _, update := range normalizeRuntimeBadges(updates) {
		key := runtimeBadgeKey(update)
		replaced := false
		for index, badge := range out {
			if runtimeBadgeKey(badge) == key {
				out[index] = update
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, update)
		}
	}
	return out
}

func normalizeRuntimeBadges(badges []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, badge := range badges {
		badge = strings.Join(strings.Fields(strings.TrimSpace(badge)), " ")
		if badge == "" {
			continue
		}
		key := runtimeBadgeKey(badge)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, badge)
	}
	return out
}

func runtimeBadgeKey(badge string) string {
	badge = strings.TrimSpace(badge)
	before, _, ok := strings.Cut(badge, ":")
	if ok && strings.TrimSpace(before) != "" {
		return strings.ToLower(strings.TrimSpace(before))
	}
	return strings.ToLower(badge)
}

func normalizeModelOptions(options []string) []string {
	out := make([]string, 0, len(options))
	seen := map[string]bool{}
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}
		key := strings.ToLower(option)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, option)
	}
	return out
}

func indexOfModelOption(options []string, current string) int {
	current = strings.TrimSpace(current)
	if current == "" {
		return 0
	}
	lower := strings.ToLower(current)
	for index, option := range options {
		if strings.EqualFold(option, current) || strings.EqualFold(option, lower) {
			return index
		}
	}
	return 0
}

func renderModelPicker(options []string, current string, selected int, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{styles.completionTitle().Render(" model picker ")}
	if len(options) == 0 {
		return strings.Join(append(lines, styles.completion().Render("  no models configured")), "\n")
	}
	if selected < 0 || selected >= len(options) {
		selected = 0
	}
	for index, option := range options {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		suffix := ""
		if strings.EqualFold(option, current) {
			suffix = "  current"
		}
		lines = append(lines, style.Render(prefix+truncateForComposer(option+suffix, limit)))
	}
	lines = append(lines, styles.completion().Render("  Enter select · Up/Down move · Esc cancel"))
	return strings.Join(lines, "\n")
}

func renderThemePicker(current string, selected int, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	options := ThemeNames()
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{styles.completionTitle().Render(" theme ")}
	for index, option := range options {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "❯ "
			style = styles.selectedCompletion()
		}
		suffix := ""
		if option == current {
			suffix = "  preview"
		}
		labelLimit := max(1, limit-lipgloss.Width(prefix))
		lines = append(lines, style.Render(prefix+truncateForComposer(themeLabel(option)+suffix, labelLimit)))
	}
	lines = append(lines, styles.completion().Render("  Enter save · Up/Down preview · Esc restore"))
	if width > 0 {
		for index := range lines {
			lines[index] = truncateFooterLine(lines[index], max(12, width))
		}
	}
	return strings.Join(lines, "\n")
}

var messageActionLabels = []string{
	"copy to composer",
	"copy to clipboard",
	"quote in composer",
	"stash message",
	"restore before turn",
	"fork before turn",
	"summarize from turn",
	"summarize up to turn",
}

func (m *model) openMessageActions() {
	target := m.lastTranscriptIndex()
	if target < 0 {
		m.status = "no messages"
		return
	}
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.matches = nil
	m.selected = 0
	m.messageActions = true
	m.messageActionTarget = target
	m.messageActionSelected = 0
	m.status = "message actions"
}

func (m *model) closeMessageActions() {
	m.messageActions = false
	m.messageActionTarget = 0
	m.messageActionSelected = 0
	m.status = m.mode()
}

func (m *model) moveMessageAction(delta int) {
	if len(messageActionLabels) == 0 {
		return
	}
	m.messageActionSelected = (m.messageActionSelected + delta + len(messageActionLabels)) % len(messageActionLabels)
	m.status = "message actions"
}

func (m *model) setMessageActionIndex(index int) {
	if len(messageActionLabels) == 0 {
		return
	}
	m.messageActionSelected = clampIndex(index, len(messageActionLabels))
	m.status = "message actions"
}

func (m *model) moveMessageActionTarget(delta int) {
	targets := m.messageActionTargets()
	if len(targets) == 0 {
		m.status = "no messages"
		return
	}
	position := 0
	for index, target := range targets {
		if target == m.messageActionTarget {
			position = index
			break
		}
	}
	next := (position + delta + len(targets)) % len(targets)
	m.messageActionTarget = targets[next]
	m.status = "message actions"
}

func (m *model) moveMessageActionUserTarget(delta int) {
	targets := m.messageActionRoleTargets("user")
	if len(targets) == 0 {
		m.status = "no user messages"
		return
	}
	current := m.messageActionTarget
	next := targets[0]
	if delta < 0 {
		next = targets[len(targets)-1]
		for index := len(targets) - 1; index >= 0; index-- {
			if targets[index] < current {
				next = targets[index]
				break
			}
		}
	} else {
		for _, target := range targets {
			if target > current {
				next = target
				break
			}
		}
	}
	m.messageActionTarget = next
	m.status = "message actions"
}

func (m model) applyMessageAction() (tea.Model, tea.Cmd) {
	entry := m.messageActionEntry()
	text := strings.TrimSpace(entry.Text)
	if text == "" {
		m.closeMessageActions()
		return m, nil
	}
	switch m.messageActionSelected {
	case 1:
		if m.copyMessage == nil {
			m.status = "copy unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "copying message"
		return m, runMessageCopyCommand(m.ctx, m.copyMessage, text)
	case 2:
		m.pushComposerUndo()
		m.textarea.SetValue(insertWithComposerSpacing(m.textarea.Value(), quoteMessageText(text)))
		m.textarea.CursorEnd()
		m.status = "message quoted"
	case 3:
		m.stashedPrompt = &composerStash{Text: text}
		m.status = "message stashed"
	case 4:
		if m.restoreConversation == nil {
			m.status = "restore unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		keepMessages := m.restoreMessageKeepCount()
		if keepMessages < 0 {
			m.status = "restore unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "restoring"
		return m, runConversationRestoreCommand(m.ctx, m.restoreConversation, keepMessages)
	case 5:
		if m.forkConversation == nil {
			m.status = "fork unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		keepMessages := m.restoreMessageKeepCount()
		if keepMessages < 0 {
			m.status = "fork unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "forking"
		return m, runConversationForkCommand(m.ctx, m.forkConversation, keepMessages)
	case 6:
		if m.summarizeConversation == nil {
			m.status = "summarize unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		keepMessages := m.restoreMessageKeepCount()
		if keepMessages < 0 {
			m.status = "summarize unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "summarizing"
		return m, runConversationSummarizeCommand(m.ctx, m.summarizeConversation, keepMessages)
	case 7:
		if m.summarizeUpToConversation == nil {
			m.status = "summarize unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		keepMessages := m.restoreMessageKeepCount()
		if keepMessages < 0 {
			m.status = "summarize unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "summarizing"
		return m, runConversationSummarizeCommand(m.ctx, m.summarizeUpToConversation, keepMessages)
	default:
		m.pushComposerUndo()
		m.textarea.SetValue(text)
		m.textarea.CursorEnd()
		m.status = "message copied"
	}
	m.messageActions = false
	m.messageActionSelected = 0
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.refreshCompletionMenu()
	return m, nil
}

func (m model) lastTranscriptIndex() int {
	for index := len(m.transcript) - 1; index >= 0; index-- {
		if strings.TrimSpace(m.transcript[index].Text) != "" {
			return index
		}
	}
	return -1
}

func (m model) messageActionEntry() transcriptEntry {
	if m.messageActionTarget >= 0 && m.messageActionTarget < len(m.transcript) {
		return m.transcript[m.messageActionTarget]
	}
	return transcriptEntry{}
}

func (m model) messageActionTargetPosition() (int, int) {
	targets := m.messageActionTargets()
	for index, target := range targets {
		if target == m.messageActionTarget {
			return index + 1, len(targets)
		}
	}
	if len(targets) == 0 {
		return 0, 0
	}
	return 1, len(targets)
}

func (m model) messageActionTargets() []int {
	targets := []int{}
	for index, entry := range m.transcript {
		if strings.TrimSpace(entry.Text) != "" {
			targets = append(targets, index)
		}
	}
	return targets
}

func (m model) messageActionRoleTargets(role string) []int {
	role = strings.TrimSpace(role)
	targets := []int{}
	for index, entry := range m.transcript {
		if strings.TrimSpace(entry.Text) != "" && strings.EqualFold(entry.Role, role) {
			targets = append(targets, index)
		}
	}
	return targets
}

func (m model) restoreMessageKeepCount() int {
	if m.messageActionTarget < 0 || m.messageActionTarget >= len(m.transcript) {
		return -1
	}
	restoreTarget := m.messageActionTarget
	if strings.EqualFold(m.transcript[restoreTarget].Role, "assistant") {
		for index := restoreTarget - 1; index >= 0; index-- {
			if strings.EqualFold(m.transcript[index].Role, "user") {
				restoreTarget = index
				break
			}
		}
	}
	keep := 0
	for index := 0; index < restoreTarget && index < len(m.transcript); index++ {
		if transcriptEntryCountsAsSessionMessage(m.transcript[index]) {
			keep++
		}
	}
	return keep
}

func transcriptEntryCountsAsSessionMessage(entry transcriptEntry) bool {
	return strings.EqualFold(entry.Role, "user") || strings.EqualFold(entry.Role, "assistant")
}

func quoteMessageText(text string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	for index, line := range lines {
		lines[index] = "> " + strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func renderMessageActions(entry transcriptEntry, selected int, width int, targetPos int, targetCount int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	role := strings.TrimSpace(entry.Role)
	if role == "" {
		role = "message"
	}
	title := " message actions "
	if targetCount > 1 && targetPos > 0 {
		title = fmt.Sprintf(" message actions %d/%d ", targetPos, targetCount)
	}
	lines := []string{styles.completionTitle().Render(title)}
	summary := strings.Join(strings.Fields(entry.Text), " ")
	if summary == "" {
		summary = "(empty message)"
	}
	lines = append(lines, styles.completion().Render("  "+truncateForComposer(role+": "+summary, limit)))
	if selected < 0 || selected >= len(messageActionLabels) {
		selected = 0
	}
	for index, action := range messageActionLabels {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+action))
	}
	hint := "  Enter apply · c copy · Up/Down choose · Esc cancel"
	if targetCount > 1 {
		hint = "  Enter apply · c copy · Up/Down choose · Left/Right message · Esc cancel"
	}
	lines = append(lines, styles.completion().Render(hint))
	return strings.Join(lines, "\n")
}

func renderQuickOpen(matches []string, selected int, query string, width int, previewPath string, previewLines []string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	query = strings.TrimSpace(query)
	title := " quick open "
	if query != "" {
		title = fmt.Sprintf(" quick open: %s ", query)
	}
	lines := []string{styles.completionTitle().Render(title)}
	if query == "" {
		return strings.Join(append(lines, styles.completion().Render("  start typing to search files")), "\n")
	}
	if len(matches) == 0 {
		return strings.Join(append(lines, styles.completion().Render("  no matching files")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	for index, match := range matches {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+truncateForComposer(match, limit)))
	}
	if previewPath != "" {
		lines = append(lines, styles.completionTitle().Render(" preview "))
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(previewPath, limit)))
		if len(previewLines) == 0 {
			lines = append(lines, styles.completion().Render("  (empty file)"))
		}
		for _, line := range previewLines {
			lines = append(lines, styles.completion().Render("  "+truncateForComposer(line, limit)))
		}
	}
	lines = append(lines, styles.completion().Render("  Enter/Tab insert @file · Shift+Tab insert path · Esc cancel"))
	return strings.Join(lines, "\n")
}

func renderTodosPanel(items []TodoItem, loading bool, errText string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	title := " tasks "
	if len(items) > 0 {
		completed, inProgress, pending := todoCounts(items)
		title = fmt.Sprintf(" tasks: %d total, %d done, %d active, %d open ", len(items), completed, inProgress, pending)
	}
	lines := []string{styles.completionTitle().Render(title)}
	switch {
	case loading:
		lines = append(lines, styles.completion().Render("  loading tasks..."))
	case strings.TrimSpace(errText) != "":
		lines = append(lines, styles.completion().Render("  error: "+truncateForComposer(errText, limit)))
	case len(items) == 0:
		lines = append(lines, styles.completion().Render("  no tasks"))
	default:
		visible := items
		if len(visible) > 10 {
			visible = visible[:10]
		}
		for _, item := range visible {
			lines = append(lines, styles.completion().Render("  "+truncateForComposer(renderTodoLine(item), limit)))
		}
		if hidden := len(items) - len(visible); hidden > 0 {
			lines = append(lines, styles.completion().Render(fmt.Sprintf("  ... %d more", hidden)))
		}
	}
	lines = append(lines, styles.completion().Render("  Ctrl+T close · /todos manage tasks · Ctrl+Shift+T background tasks"))
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

func renderGlobalSearch(matches []globalSearchMatch, selected int, query string, width int, previewPath string, previewLine int, previewLines []string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	query = strings.TrimSpace(query)
	title := " global search "
	if query != "" {
		title = fmt.Sprintf(" global search: %s ", query)
	}
	lines := []string{styles.completionTitle().Render(title)}
	if query == "" {
		return strings.Join(append(lines, styles.completion().Render("  type to search workspace")), "\n")
	}
	if len(matches) == 0 {
		return strings.Join(append(lines, styles.completion().Render("  no matches")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	for index, match := range matches {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		label := fmt.Sprintf("%s:%d  %s", match.File, match.Line, strings.TrimSpace(match.Text))
		lines = append(lines, style.Render(prefix+truncateForComposer(label, limit)))
	}
	if previewPath != "" {
		lines = append(lines, styles.completionTitle().Render(" preview "))
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(fmt.Sprintf("%s:%d", previewPath, previewLine), limit)))
		if len(previewLines) == 0 {
			lines = append(lines, styles.completion().Render("  (empty file)"))
		}
		for _, line := range previewLines {
			lines = append(lines, styles.completion().Render("  "+truncateForComposer(line, limit)))
		}
	}
	lines = append(lines, styles.completion().Render("  Enter/Tab insert @file#Lline · Shift+Tab insert path:line · Esc cancel"))
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

func renderPendingAttachments(attachments []string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(attachments) == 0 {
		return ""
	}
	lines := []string{styles.completionTitle().Render(fmt.Sprintf(" attachments: %d ", len(attachments)))}
	start := 0
	if len(attachments) > 4 {
		start = len(attachments) - 4
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  ... %d earlier", start)))
	}
	for index := start; index < len(attachments); index++ {
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  %d. %s", index+1, truncateForComposer(attachments[index], 100))))
	}
	return strings.Join(lines, "\n")
}

func renderAttachmentPanel(attachments []string, selected int, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(attachments) == 0 {
		return ""
	}
	selected = clampIndex(selected, len(attachments))
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{styles.completionTitle().Render(fmt.Sprintf(" attachments %d/%d ", selected+1, len(attachments)))}
	for index, attachment := range attachments {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%d. %s", prefix, index+1, truncateForComposer(attachment, limit))))
	}
	lines = append(lines, styles.completion().Render("  Left/Right select · Backspace/Delete remove · Down/Esc close"))
	return strings.Join(lines, "\n")
}

func renderPermissionRequest(request PermissionRequest, selected int, inputMode bool, inputAnswer string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	tool := strings.TrimSpace(request.Tool)
	if tool == "" {
		tool = "tool"
	}
	required := strings.TrimSpace(request.Required)
	if required == "" {
		required = "additional permission"
	}
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{
		styles.role("permission").Render("Permission request"),
		truncateForComposer(fmt.Sprintf("Allow %s to use %s?", tool, required), limit),
	}
	if message := strings.TrimSpace(request.Message); message != "" {
		lines = append(lines, styles.role("permission").Render("Warning: ")+truncateForComposer(strings.Join(strings.Fields(message), " "), max(12, limit-9)))
	}
	if input := strings.TrimSpace(request.Input); input != "" {
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(strings.Join(strings.Fields(input), " "), max(12, limit-2))))
	}
	answers := []string{"Yes"}
	if request.AllowAlways {
		label := "Yes, and don't ask again this session"
		if rule := strings.TrimSpace(request.SuggestedRule); rule != "" {
			label = "Yes, and don't ask again for: " + rule
		}
		answers = append(answers, label)
	}
	answers = append(answers, "No")
	answerValues := []string{"y"}
	if request.AllowAlways {
		answerValues = append(answerValues, "a")
	}
	answerValues = append(answerValues, "n")
	selected = clampIndex(selected, len(answers))
	for index, label := range answers {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		if inputMode && index < len(answerValues) && answerValues[index] == inputAnswer {
			label += ":"
		}
		lines = append(lines, style.Render(truncateForComposer(prefix+label, limit)))
	}
	hint := "  Enter select · Up/Down navigate · Tab amend · Esc deny"
	if inputMode {
		switch inputAnswer {
		case "a":
			hint = "  Edit the session rule · Enter allow · Tab/Esc collapse"
		case "n":
			hint = "  Add guidance for a safer approach · Enter deny · Tab/Esc collapse"
		default:
			hint = "  Add next-step guidance · Enter allow · Tab/Esc collapse"
		}
	}
	lines = append(lines, styles.completion().Render(truncateForComposer(hint, limit)))
	return strings.Join(lines, "\n")
}

func renderQuestionRequest(request QuestionRequest, questionIndex int, selected int, custom bool, selections [][]bool, customValues []string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	questions := request.Questions
	if len(questions) == 0 {
		options := make([]QuestionOption, 0, len(request.Choices))
		for _, choice := range request.Choices {
			options = append(options, QuestionOption{Label: choice})
		}
		questions = []Question{{Question: request.Question, Header: "Question", Options: options}}
	}
	questionIndex = min(max(questionIndex, 0), len(questions))
	lines := []string{styles.role("question").Render("Questions")}
	if len(questions) > 1 {
		tabs := make([]string, 0, len(questions)+1)
		for index, question := range questions {
			marker := "[ ]"
			if renderedQuestionAnswered(index, selections, customValues) {
				marker = "[x]"
			}
			prefix := ""
			if index == questionIndex {
				prefix = ">"
			}
			tabs = append(tabs, fmt.Sprintf("%s%s %s", prefix, marker, question.Header))
		}
		submitPrefix := ""
		if questionIndex == len(questions) {
			submitPrefix = ">"
		}
		tabs = append(tabs, submitPrefix+"Submit")
		lines = append(lines, styles.completionTitle().Render(truncateForComposer(strings.Join(tabs, "  "), limit)))
	}
	if questionIndex == len(questions) {
		lines = append(lines, styles.panelTitle().Render("Review answers"))
		for index, question := range questions {
			answer := renderedQuestionAnswer(index, question, selections, customValues)
			if answer == "" {
				answer = "(not answered)"
			}
			lines = append(lines, styles.completion().Render(truncateForComposer(question.Header+": "+answer, limit)))
		}
		lines = append(lines, styles.completion().Render(truncateForComposer("  Enter to submit · Left to go back · Esc to cancel", limit)))
		return strings.Join(lines, "\n")
	}

	question := questions[questionIndex]
	questionText := strings.TrimSpace(question.Question)
	if questionText == "" {
		questionText = "Choose an answer"
	}
	lines = append(lines, truncateForComposer(questionText, limit))
	if question.MultiSelect {
		lines = append(lines, styles.completionTitle().Render("Select one or more"))
	}
	if len(question.Options) == 0 {
		lines = append(lines, styles.selectedCompletion().Render("> Type something"))
	} else {
		selected = clampIndex(selected, len(question.Options)+1)
		for index, option := range question.Options {
			prefix := "  "
			style := styles.completion()
			if index == selected && !custom {
				prefix = "> "
				style = styles.selectedCompletion()
			}
			marker := ""
			if question.MultiSelect {
				marker = "[ ] "
				if questionIndex < len(selections) && index < len(selections[questionIndex]) && selections[questionIndex][index] {
					marker = "[x] "
				}
			}
			label := fmt.Sprintf("%d. %s%s", index+1, marker, option.Label)
			if strings.EqualFold(option.Label, request.Default) {
				label += " (default)"
			}
			lines = append(lines, style.Render(truncateForComposer(prefix+label, limit)))
		}
		prefix := "  "
		style := styles.completion()
		if selected == len(question.Options) || custom {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+"Type something"))
		if selected < len(question.Options) {
			option := question.Options[selected]
			if option.Description != "" {
				lines = append(lines, styles.completion().Render(truncateForComposer("  "+option.Description, limit)))
			}
			if option.Preview != "" {
				lines = append(lines, styles.completionTitle().Render("Preview"))
				for _, previewLine := range firstLines(option.Preview, 5) {
					lines = append(lines, styles.completion().Render(truncateForComposer("  "+previewLine, limit)))
				}
			}
		}
	}
	if custom {
		lines = append(lines, styles.completion().Render(truncateForComposer("  Type your response below, then press Enter", limit)))
	} else if question.MultiSelect {
		lines = append(lines, styles.completion().Render(truncateForComposer("  Space to toggle · Enter next · Up/Down navigate · Esc cancel", limit)))
	} else {
		lines = append(lines, styles.completion().Render(truncateForComposer("  Enter to select · Up/Down to navigate · Esc to cancel", limit)))
	}
	return strings.Join(lines, "\n")
}

func renderedQuestionAnswered(index int, selections [][]bool, customValues []string) bool {
	if index >= 0 && index < len(customValues) && strings.TrimSpace(customValues[index]) != "" {
		return true
	}
	if index >= 0 && index < len(selections) {
		for _, selected := range selections[index] {
			if selected {
				return true
			}
		}
	}
	return false
}

func renderedQuestionAnswer(index int, question Question, selections [][]bool, customValues []string) string {
	parts := []string{}
	if index >= 0 && index < len(selections) {
		for optionIndex, selected := range selections[index] {
			if selected && optionIndex < len(question.Options) {
				parts = append(parts, question.Options[optionIndex].Label)
			}
		}
	}
	if index >= 0 && index < len(customValues) {
		if custom := strings.TrimSpace(customValues[index]); custom != "" {
			parts = append(parts, custom)
		}
	}
	return strings.Join(parts, ", ")
}

func renderDiffDialog(sources []DiffSource, sourceIndex int, fileIndex int, detail bool, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	sources = normalizeDiffSources(sources)
	if len(sources) == 0 {
		return styles.completionTitle().Render(" diff ") + "\n" + styles.completion().Render("  no changes")
	}
	sourceIndex = clampIndex(sourceIndex, len(sources))
	source := sources[sourceIndex]
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	title := fmt.Sprintf(" diff %d/%d: %s ", sourceIndex+1, len(sources), source.Name)
	lines := []string{styles.completionTitle().Render(title)}
	if source.Subtitle != "" {
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(source.Subtitle, limit)))
	}
	if len(source.Files) == 0 {
		lines = append(lines, styles.completion().Render("  no changed files"))
		lines = append(lines, styles.completion().Render("  Left/Right source · Esc close"))
		return strings.Join(lines, "\n")
	}
	fileIndex = clampIndex(fileIndex, len(source.Files))
	selected := source.Files[fileIndex]
	if detail {
		header := fmt.Sprintf("  %s %s", strings.ToUpper(selected.Status), selected.Path)
		lines = append(lines, styles.selectedCompletion().Render(truncateForComposer(header, limit)))
		diff := strings.TrimSpace(selected.Diff)
		if diff == "" {
			diff = selected.Summary
		}
		if diff == "" {
			diff = "(no diff preview)"
		}
		for _, line := range firstLines(diff, 12) {
			lines = append(lines, styles.completion().Render("  "+truncateForComposer(line, limit)))
		}
		lines = append(lines, styles.completion().Render("  Left back · Esc close"))
		return strings.Join(lines, "\n")
	}
	stats := fmt.Sprintf("%d changed %s", len(source.Files), plural("file", len(source.Files)))
	lines = append(lines, styles.completion().Render("  "+stats))
	for index, file := range source.Files {
		prefix := "  "
		style := styles.completion()
		if index == fileIndex {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		summary := strings.TrimSpace(file.Summary)
		if summary != "" {
			summary = " · " + summary
		}
		lines = append(lines, style.Render(truncateForComposer(fmt.Sprintf("%s%s %s%s", prefix, strings.ToUpper(file.Status), file.Path, summary), limit)))
	}
	lines = append(lines, styles.completion().Render("  Up/Down file · Left/Right source · Enter detail · Esc close"))
	return strings.Join(lines, "\n")
}

func firstLines(text string, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines
}

func renderStashNotice(stash *composerStash, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if stash == nil {
		return ""
	}
	summary := truncateForComposer(strings.Join(strings.Fields(stash.Text), " "), 80)
	if summary == "" {
		summary = fmt.Sprintf("%d pending %s", len(stash.Attachments), plural("attachment", len(stash.Attachments)))
	}
	lines := []string{styles.completionTitle().Render(" stashed prompt ")}
	lines = append(lines, styles.completion().Render("  Ctrl+S restore: "+summary))
	if len(stash.Attachments) > 0 {
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  attachments: %d", len(stash.Attachments))))
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
			return "permission · Up/Down · Enter · Tab amend · Esc deny"
		default:
			return "permission · Up/Down choose · Enter select · Tab amend · y/n/a shortcuts · Esc deny"
		}
	}
	if strings.EqualFold(status, "question") {
		switch {
		case width > 0 && width < 70:
			return "question · Up/Down · Enter · Esc"
		default:
			return "question · Up/Down choose · Enter select · type for custom response · Esc cancel"
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
	if strings.EqualFold(status, "model picker") {
		switch {
		case width > 0 && width < 80:
			return "model picker · Enter select · Esc"
		default:
			return "model picker · Up/Down choose · Enter select · Esc cancel"
		}
	}
	if strings.EqualFold(status, "message actions") {
		switch {
		case width > 0 && width < 80:
			return "message actions · Enter apply · Esc"
		default:
			return "message actions · Up/Down choose · Enter apply · Esc cancel"
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
	if strings.EqualFold(status, "ctrl+x") {
		return "Ctrl+X · Ctrl+E edit in $EDITOR · Ctrl+K stop background · Ctrl+C compact · Esc cancel"
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

func (m model) promptFooterText(width int) string {
	limit := width
	if limit <= 0 {
		limit = 120
	}
	baseStatus := strings.TrimSpace(m.status)
	if baseStatus == "" {
		baseStatus = m.mode()
	}
	status := statusBarText(baseStatus, width)
	if m.transcriptMode && !strings.EqualFold(baseStatus, "transcript") {
		status = appendStatusMode(status, "transcript", width)
	}
	status = appendStatusMode(status, m.modeLabel, width)
	status = truncateFooterLine(status, limit)
	hints := m.promptFooterHints(width)
	if len(hints) == 0 {
		return status
	}
	byline := truncateFooterLine(strings.Join(hints, " · "), limit)
	if strings.TrimSpace(byline) == "" {
		return status
	}
	return status + "\n" + byline
}

func (m model) promptFooterHints(width int) []string {
	status := strings.ToLower(strings.TrimSpace(m.status))
	hints := []string{}
	add := func(hint string) {
		hint = strings.TrimSpace(hint)
		if hint == "" {
			return
		}
		for _, existing := range hints {
			if strings.EqualFold(existing, hint) {
				return
			}
		}
		hints = append(hints, hint)
	}
	if m.awaitingPermission {
		if m.permissionInput {
			add("Enter submit")
			add("Tab/Esc collapse")
		} else {
			add("Up/Down choose")
			add("Enter select")
			add("Tab amend")
			add("y/n/a shortcuts")
		}
		return trimFooterHints(hints, width)
	}
	if m.awaitingQuestion {
		add("Up/Down choose")
		add("Enter select")
		add("type custom response")
		return trimFooterHints(hints, width)
	}
	if m.busy {
		add("Esc interrupt")
		if len(m.queuedPrompts) > 0 {
			add(fmt.Sprintf("%d queued", len(m.queuedPrompts)))
			add("Up edit queue")
		} else {
			add("type next prompt to queue")
		}
		if m.background != nil {
			add("Ctrl+B background")
		}
		return trimFooterHints(hints, width)
	}
	if m.backgrounding {
		add("background starting")
		if m.stopBackground != nil {
			add("Ctrl+X Ctrl+K stop")
		}
		return trimFooterHints(hints, width)
	}
	if m.helpOpen {
		add("Esc close help")
		add("/ for commands")
		add("@ for files")
		return trimFooterHints(hints, width)
	}
	if m.exitPending {
		exitLabel := "Esc"
		if m.exitKey == "ctrl+c" {
			exitLabel = "Ctrl+C"
		}
		add(exitLabel + " again to exit")
		add("type to continue")
		add("Ctrl+_ undo")
		return trimFooterHints(hints, width)
	}
	if status == "bash" || isBashModeInput(m.textarea.Value()) {
		add("! for bash mode")
		add("Enter run local command")
		add("Esc clear")
		return trimFooterHints(hints, width)
	}
	if m.vimEnabled && m.vimNormal {
		add("vim NORMAL")
		add("i/a insert")
		add("h/l/w/b move")
		add("x/D/dd delete")
		add("C/cc change")
		add("Enter send")
		return trimFooterHints(hints, width)
	}
	if m.searchOpen {
		add("Enter restore")
		add("Esc close")
		return trimFooterHints(hints, width)
	}
	if m.quickOpen {
		add("Enter insert @file")
		add("Shift+Tab path only")
		add("Esc close")
		return trimFooterHints(hints, width)
	}
	if m.globalSearch {
		add("Enter insert @line")
		add("Shift+Tab path:line")
		add("Esc close")
		return trimFooterHints(hints, width)
	}
	if m.todosOpen {
		add("Ctrl+T close tasks")
		add("/todos manage")
		if m.taskBoard != nil {
			add("Ctrl+Shift+T background tasks")
		}
		return trimFooterHints(hints, width)
	}
	if m.modelPicker {
		add("Enter select model")
		add("Esc close")
		return trimFooterHints(hints, width)
	}
	if m.messageActions {
		add("Enter apply")
		add("Left/Right target")
		add("Esc close")
		return trimFooterHints(hints, width)
	}
	if m.attachmentsOpen {
		add("Left/Right select")
		add("Backspace remove")
		add("Esc close")
		return trimFooterHints(hints, width)
	}
	if m.diffDialog {
		add("Enter details")
		add("Left/Right sources")
		add("Esc close")
		return trimFooterHints(hints, width)
	}
	if status == "ctrl+x" {
		add("Ctrl+E editor")
		add("Ctrl+C compact")
		add("Ctrl+U undo")
		add("Esc cancel")
		return trimFooterHints(hints, width)
	}
	add("? for shortcuts")
	add("/ commands")
	add("@ files")
	for _, badge := range m.runtimeStatusBadges() {
		add(badge)
	}
	if len(m.attachments) > 0 {
		add(fmt.Sprintf("%d attached", len(m.attachments)))
	}
	if len(m.queuedPrompts) > 0 {
		add(fmt.Sprintf("%d queued", len(m.queuedPrompts)))
	}
	add("Ctrl+R history")
	add("Ctrl+T tasks")
	if m.transcriptMode {
		add("Ctrl+O compact transcript")
	} else {
		add("Ctrl+O transcript")
	}
	if strings.TrimSpace(m.modeLabel) != "" {
		add("mode: " + m.modeLabel)
	}
	if m.vimEnabled {
		if m.vimNormal {
			add("vim: normal")
		} else {
			add("vim: insert")
		}
	}
	if m.cycleMode != nil || strings.TrimSpace(m.modeLabel) != "" {
		add("Shift+Tab mode")
	}
	if m.stashedPrompt != nil {
		add("Ctrl+S restore stash")
	} else {
		add("Ctrl+S stash")
	}
	if len(m.modelOptions) > 0 {
		add("Alt+P model")
	}
	if m.background != nil {
		add("Ctrl+B background")
	}
	return trimFooterHints(hints, width)
}

func trimFooterHints(hints []string, width int) []string {
	if width <= 0 || len(hints) <= 2 {
		return hints
	}
	limit := 8
	switch {
	case width < 70:
		limit = 3
	case width < 95:
		limit = 4
	case width < 120:
		limit = 5
	case width >= 150:
		limit = 14
	}
	if len(hints) > limit {
		return hints[:limit]
	}
	return hints
}

func truncateFooterLine(line string, width int) string {
	if width <= 0 || lipgloss.Width(line) <= width {
		return line
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	var builder strings.Builder
	used := 0
	for _, r := range line {
		runeWidth := lipgloss.Width(string(r))
		if used+runeWidth > width-3 {
			break
		}
		builder.WriteRune(r)
		used += runeWidth
	}
	return builder.String() + "..."
}

func fitFooterText(text string, width int) string {
	lines := strings.Split(text, "\n")
	for index := range lines {
		lines[index] = truncateFooterLine(lines[index], width)
	}
	return strings.Join(lines, "\n")
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
	return truncateFooterLine(out, width)
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
		m.pushComposerUndoValue(m.draft)
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

func (m *model) setQuickOpenIndex(index int) {
	if len(m.quickOpenMatches) == 0 {
		return
	}
	m.quickOpenSelected = clampIndex(index, len(m.quickOpenMatches))
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
		m.pushComposerUndoValue(m.quickOpenDraft)
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

func (m *model) setGlobalSearchIndex(index int) {
	if len(m.globalSearchMatches) == 0 {
		return
	}
	m.globalSearchSelected = clampIndex(index, len(m.globalSearchMatches))
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
		m.pushComposerUndoValue(m.globalSearchDraft)
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

func clampIndex(index int, length int) int {
	if length <= 0 {
		return 0
	}
	return min(max(index, 0), length-1)
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
	minimumWidth := 40
	if m.inline {
		minimumWidth = 8
	}
	m.textarea.SetWidth(max(minimumWidth, width-4))
	composerHeight := 4
	reservedHeight := 9
	if m.inline {
		composerHeight = 1
		reservedHeight = 5
	}
	m.textarea.SetHeight(composerHeight)
	viewportHeight := height - reservedHeight
	if viewportHeight < 6 {
		viewportHeight = 6
	}
	m.viewport.Width = max(minimumWidth, width)
	m.viewport.Height = viewportHeight
	m.refreshViewport()
}

func (m *model) refreshViewport() {
	if m.helpOpen {
		m.viewport.SetContent(helpPanel(m.completionCandidates(), m.viewport.Width, stylesForTheme(m.theme)))
		return
	}
	lines := []string{}
	start := 0
	if m.inline && !m.transcriptMode {
		start = min(max(m.printedEntries, 0), len(m.transcript))
	}
	for index, entry := range m.transcript[start:] {
		index += start
		lines = append(lines, renderTranscriptEntry(entry, max(8, m.viewport.Width-2), index, len(m.transcript), m.transcriptMode, stylesForTheme(m.theme)))
	}
	m.viewport.SetContent(strings.Join(lines, "\n\n"))
}

func (m *model) prepareInlineTranscript() {
	if !m.inline || len(m.transcript) == 0 {
		return
	}
	m.initialPrint = m.renderTranscriptRange(0, len(m.transcript))
	m.printedEntries = len(m.transcript)
	m.refreshViewport()
}

func (m *model) flushInlineTranscript() tea.Cmd {
	if !m.inline || m.printedEntries >= len(m.transcript) {
		return nil
	}
	content := m.renderTranscriptRange(m.printedEntries, len(m.transcript))
	m.printedEntries = len(m.transcript)
	m.refreshViewport()
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return tea.Println(content)
}

func (m model) renderTranscriptRange(start int, end int) string {
	start = min(max(start, 0), len(m.transcript))
	end = min(max(end, start), len(m.transcript))
	width := max(8, m.viewport.Width-2)
	entries := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		entries = append(entries, renderTranscriptEntry(m.transcript[index], width, index, len(m.transcript), m.transcriptMode, stylesForTheme(m.theme)))
	}
	return strings.Join(entries, "\n\n")
}

func sequenceCommands(commands ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(commands))
	for _, command := range commands {
		if command != nil {
			filtered = append(filtered, command)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Sequence(filtered...)
	}
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
	if m.modelPicker {
		return "model picker"
	}
	if m.themePicker {
		return "theme picker"
	}
	if m.messageActions {
		return "message actions"
	}
	if m.attachmentsOpen {
		return "attachments"
	}
	if m.diffDialog {
		if m.diffDetail {
			return "diff detail"
		}
		return "diff"
	}
	value := strings.TrimSpace(m.textarea.Value())
	if isBashModeInput(value) {
		return "bash"
	}
	if m.vimEnabled && m.vimNormal {
		return "vim normal"
	}
	if len(m.matches) > 0 {
		return fmt.Sprintf("%d completions", len(m.matches))
	}
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

func renderTranscriptEntry(entry transcriptEntry, width int, index int, total int, transcriptMode bool, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	role := strings.TrimSpace(entry.Role)
	if role == "" {
		role = "message"
	}
	if entry.Tool != nil {
		if !transcriptMode {
			return renderToolActivity(*entry.Tool, width, false, styles)
		}
		text := toolActivityTranscriptText(*entry.Tool)
		header := fmt.Sprintf("%03d/%03d tool · %d %s · %d %s", index+1, max(1, total), transcriptLineCount(text), plural("line", transcriptLineCount(text)), len([]rune(text)), plural("char", len([]rune(text))))
		return styles.role("tool").Render(header) + "\n" + renderToolActivity(*entry.Tool, width, true, styles)
	}
	if !transcriptMode {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			text = "(empty)"
		}
		marker := transcriptRoleMarker(role)
		prefix := styles.role(role).Render(marker)
		contentWidth := max(4, width-lipgloss.Width(marker)-1)
		content := wrapTranscriptText(text, contentWidth)
		if strings.EqualFold(role, "assistant") {
			content = renderAssistantMarkdown(text, contentWidth, styles)
		}
		wrapped := strings.ReplaceAll(content, "\n", "\n  ")
		return prefix + " " + wrapped
	}
	text := entry.Text
	if text == "" {
		text = "(empty)"
	}
	header := fmt.Sprintf("%03d/%03d %s · %d %s · %d %s", index+1, max(1, total), role, transcriptLineCount(text), plural("line", transcriptLineCount(text)), len([]rune(text)), plural("char", len([]rune(text))))
	content := wrapTranscriptText(text, width)
	if strings.EqualFold(role, "assistant") {
		content = renderAssistantMarkdown(text, width, styles)
	}
	return styles.role(role).Render(header) + "\n" + content
}

func renderToolActivity(activity ToolActivity, width int, expanded bool, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	width = max(12, width)
	status := strings.ToLower(strings.TrimSpace(activity.Status))
	if activity.IsError {
		status = "error"
	}
	marker := "●"
	markerStyle := styles.role("tool")
	suffix := ""
	switch status {
	case "running":
		marker = "◐"
		suffix = " running"
	case "success":
		markerStyle = styles.role("success")
	case "error":
		marker = "!"
		markerStyle = styles.role("error")
		suffix = " failed"
	}
	name := toolActivityDisplayName(activity.Name)
	if summary := toolActivityInputSummary(activity.Name, activity.Input); summary != "" {
		name += "(" + summary + ")"
	}
	headerLimit := max(1, width-lipgloss.Width(marker)-1-lipgloss.Width(suffix))
	header := markerStyle.Render(marker) + " " + styles.panelTitle().Render(truncateForComposer(name, headerLimit))
	if suffix != "" {
		header += styles.completion().Render(suffix)
	}
	lines := []string{header}
	outputLines := toolActivityOutputLines(activity, expanded)
	if len(outputLines) == 0 {
		switch status {
		case "running":
			outputLines = []string{"Running..."}
		case "success":
			outputLines = []string{"Done"}
		}
	}
	contentWidth := max(8, width-2)
	for _, line := range outputLines {
		wrapped := wrapTranscriptText(line, contentWidth)
		for _, part := range strings.Split(wrapped, "\n") {
			lines = append(lines, styles.completion().Render("  "+part))
		}
	}
	return strings.Join(lines, "\n")
}

func toolActivityDisplayName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "bashtool":
		return "Bash"
	case "powershell", "powershelltool":
		return "PowerShell"
	case "read", "read_file", "readfiletool":
		return "Read"
	case "write", "write_file", "writefiletool":
		return "Write"
	case "edit", "edit_file", "multiedit", "multi_edit", "apply_patch", "editfiletool":
		return "Edit"
	case "grep", "greptool":
		return "Grep"
	case "glob", "globtool":
		return "Glob"
	case "web_search", "websearchtool":
		return "Web Search"
	case "web_fetch", "webfetchtool":
		return "Web Fetch"
	case "ask_user_question", "askuserquestiontool":
		return "Ask User"
	}
	name = strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSpace(name))
	words := strings.Fields(name)
	for index, word := range words {
		runes := []rune(strings.ToLower(word))
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		words[index] = string(runes)
	}
	if len(words) == 0 {
		return "Tool"
	}
	return strings.Join(words, " ")
}

func toolActivityInputSummary(name string, input string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(input)), &payload) != nil {
		return truncateForComposer(strings.Join(strings.Fields(input), " "), 100)
	}
	value := func(keys ...string) string {
		for _, key := range keys {
			if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		return ""
	}
	canonical := strings.ToLower(strings.TrimSpace(name))
	summary := ""
	switch canonical {
	case "bash", "bashtool", "powershell", "powershelltool":
		summary = value("command", "code")
	case "read", "read_file", "readfiletool", "write", "write_file", "writefiletool", "edit", "edit_file", "editfiletool":
		summary = value("path", "file_path")
	case "multi_edit", "multiedit":
		summary = value("path", "file_path")
		if summary == "" {
			if edits, ok := payload["edits"].([]any); ok && len(edits) > 0 {
				if edit, editOK := edits[0].(map[string]any); editOK {
					summary, _ = edit["path"].(string)
					if summary == "" {
						summary, _ = edit["file_path"].(string)
					}
				}
			}
		}
	case "apply_patch":
		summary = "patch"
	case "notebook_edit", "notebookedittool":
		summary = value("notebook_path")
	case "grep", "greptool":
		summary = value("pattern", "query")
		if path := value("path"); path != "" {
			summary += " in " + path
		}
	case "glob", "globtool":
		summary = value("pattern")
		if path := value("path"); path != "" {
			summary += " in " + path
		}
	case "web_search", "websearchtool":
		summary = value("query")
	case "web_fetch", "webfetchtool":
		summary = value("url")
	case "ask_user_question", "askuserquestiontool":
		if questions, ok := payload["questions"].([]any); ok {
			summary = fmt.Sprintf("%d %s", len(questions), plural("question", len(questions)))
		} else {
			summary = value("question")
		}
	default:
		summary = value("prompt", "query", "name", "id", "path")
	}
	return truncateForComposer(strings.Join(strings.Fields(summary), " "), 100)
}

func toolActivityOutputLines(activity ToolActivity, expanded bool) []string {
	output := strings.TrimSpace(activity.Output)
	if output == "" {
		return nil
	}
	if expanded {
		return strings.Split(output, "\n")
	}
	lines := []string{}
	var payload map[string]any
	if json.Unmarshal([]byte(output), &payload) == nil {
		appendField := func(label string, keys ...string) {
			for _, key := range keys {
				value, ok := payload[key].(string)
				value = strings.TrimSpace(value)
				if !ok || value == "" {
					continue
				}
				if label != "" {
					value = label + ": " + value
				}
				lines = append(lines, strings.Split(value, "\n")...)
				return
			}
		}
		appendField("", "stdout")
		appendField("stderr", "stderr")
		appendField("error", "error")
		appendField("", "output", "message", "content")
		if len(lines) == 0 {
			path, _ := payload["path"].(string)
			if path == "" {
				path, _ = payload["file_path"].(string)
			}
			if path != "" {
				line := path
				if count, ok := payload["bytes"].(float64); ok {
					line += fmt.Sprintf(" · %.0f bytes", count)
				}
				lines = append(lines, line)
			}
			if exitCode, ok := payload["exit_code"].(float64); ok {
				line := fmt.Sprintf("Exit code %.0f", exitCode)
				if duration, durationOK := payload["duration_ms"].(float64); durationOK {
					line += fmt.Sprintf(" · %.0f ms", duration)
				}
				lines = append(lines, line)
			}
		}
	}
	if len(lines) == 0 {
		lines = strings.Split(output, "\n")
	}
	if !expanded && len(lines) > 4 {
		hidden := len(lines) - 4
		lines = append(append([]string(nil), lines[:4]...), fmt.Sprintf("... %d more %s", hidden, plural("line", hidden)))
	}
	return lines
}

func toolActivityTranscriptText(activity ToolActivity) string {
	parts := []string{toolActivityDisplayName(activity.Name)}
	if input := strings.TrimSpace(activity.Input); input != "" {
		parts = append(parts, input)
	}
	if output := strings.TrimSpace(activity.Output); output != "" {
		parts = append(parts, output)
	}
	return strings.Join(parts, "\n")
}

func transcriptRoleMarker(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		return "●"
	case "user":
		return "❯"
	case "tool":
		return "●"
	case "permission", "question":
		return "◆"
	case "error":
		return "!"
	default:
		return "·"
	}
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

func helpPanel(candidates []string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(candidates) > 12 {
		candidates = candidates[:12]
	}
	sections := []string{
		styles.panelTitle().Render(" help "),
		"Codog is an interactive coding agent. Type a task, reference @files, run !shell commands, or use slash commands.",
		"Enter sends the composer. Shift+Enter, Alt+Enter, Ctrl+J, or a trailing backslash inserts a newline.",
		"",
		"Common workflows",
		"  ask normally       describe the code change, investigation, or test you want",
		"  @path              attach a file reference to the next prompt",
		"  !command           run a local shell command through the permission flow",
		"  /attach PATH       stage files or images for the next prompt",
		"  /paste             insert clipboard text or stage clipboard images",
		"",
		"Core commands",
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
		"  Ctrl+X Ctrl+E edit composer in $EDITOR",
		"  Ctrl+X Ctrl+K stop background tasks",
		"  Ctrl+X Ctrl+C compact session",
		"  Ctrl+X Ctrl+U undo last file change",
		"  Ctrl+X Ctrl+S export conversation",
		"  Ctrl+X Ctrl+Y copy conversation",
		"  Ctrl+X Backspace remove last attachment",
		"  Ctrl+_      undo composer edit",
		"  Ctrl+Shift+- undo composer edit",
		"  Ctrl+V      paste clipboard text or image",
		"  Ctrl+Shift+P quick open files",
		"  Ctrl+P      quick open fallback",
		"  Ctrl+Shift+F search workspace",
		"  Ctrl+F      search workspace fallback",
		"  Alt+P       open model picker",
		"  Alt/Meta+M  cycle permission mode fallback",
		"  Alt+O       toggle fast mode",
		"  Alt+T       cycle thinking effort",
		"  Shift+Up    open message actions",
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
		"  Esc         clear input, close panels, or press twice to exit",
		"  Ctrl+C      interrupt running work or exit immediately",
	}
	if len(candidates) > 0 {
		sections = append(sections, "", "Completions")
		for _, candidate := range candidates {
			sections = append(sections, "  "+candidate)
		}
	}
	return lipgloss.NewStyle().Width(max(10, width-2)).Render(strings.Join(sections, "\n"))
}

func isREPLExitInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/exit", "/quit", "exit", "quit":
		return true
	default:
		return false
	}
}

func resolveThemeStyles(themed []themeStyles) themeStyles {
	if len(themed) > 0 {
		return themed[0]
	}
	return stylesForTheme("auto")
}
