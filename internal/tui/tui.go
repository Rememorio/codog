package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
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

// SessionState is a saved conversation loaded by a slash command. Applying it
// replaces the visible transcript and prompt history atomically.
type SessionState struct {
	ID         string
	Entries    []Entry
	History    []string
	Candidates []string
}

// DiffView is a file-oriented diff browser opened by a slash command.
type DiffView struct {
	Sources []DiffSource
}

// PermissionModeOption is one session permission mode shown in the interactive
// permissions panel.
type PermissionModeOption struct {
	Name        string
	Label       string
	Description string
	Current     bool
}

// PermissionSettings describes the current session modes and configured tool
// rules shown by the interactive permissions panel.
type PermissionSettings struct {
	Modes []PermissionModeOption
	Allow []string
	Ask   []string
	Deny  []string
}

// InformationView is a scrollable read-only panel opened by a slash command.
type InformationView struct {
	Title            string
	Lines            []string
	DismissOnConfirm bool
}

// ExportDialog describes the initial state of the conversation export dialog.
type ExportDialog struct {
	DefaultFilename string
}

// TextInputDialog describes a local workflow that needs one line of user input.
type TextInputDialog struct {
	Title        string
	Prompt       string
	InitialValue string
	Action       string
}

// CommandView is a tabbed local-command panel with optional actions.
type CommandView struct {
	Title        string
	Tabs         []CommandViewTab
	SelectedTab  int
	SelectedItem int
}

// CommandViewTab is one tab in a CommandView.
type CommandViewTab struct {
	Title          string
	Lines          []string
	Items          []CommandViewItem
	RefreshCommand string
}

// CommandViewItem is one selectable action in a CommandView tab.
type CommandViewItem struct {
	Label            string
	Value            string
	Description      string
	Action           string
	Command          string
	SecondaryLabel   string
	SecondaryAction  string
	SecondaryCommand string
	SecondaryKey     string
}

// SlashResult is the structured outcome of one local slash command.
type SlashResult struct {
	Output             string
	Query              string
	Handled            bool
	Session            *SessionState
	SessionChoices     []SessionChoice
	OpenModelPicker    bool
	OpenThemePicker    bool
	OpenTodos          bool
	OpenMessageActions bool
	RuntimeAction      string
	Diff               *DiffView
	PermissionSettings *PermissionSettings
	Information        *InformationView
	CommandView        *CommandView
	ExportDialog       *ExportDialog
	TextInputDialog    *TextInputDialog
}

// SlashFunc runs one local slash command. Structured result fields let the
// shell open interactive views without parsing display text.
type SlashFunc func(context.Context, string) (SlashResult, error)

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

// ConversationExportFunc saves the active conversation to a user-selected path.
type ConversationExportFunc func(context.Context, string) (RuntimeControlResult, error)
type ModelSelectFunc func(context.Context, string) (RuntimeControlResult, error)

// PermissionModeSelectFunc applies one permission mode to the current shell.
type PermissionModeSelectFunc func(context.Context, string) (RuntimeControlResult, error)

// ThemeSelectFunc persists a theme selected from the live TUI picker.
type ThemeSelectFunc func(context.Context, string) (RuntimeControlResult, error)
type TextInputSubmitFunc func(context.Context, string, string) (RuntimeControlResult, error)
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
	Title      string
	Status     string
	Lines      []string
	Badges     []string
	Setting    string
	Value      string
	VimEnabled *bool
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
	SubmitTextInput           TextInputSubmitFunc
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
	SelectPermissionMode      PermissionModeSelectFunc
	Theme                     string
	SelectTheme               ThemeSelectFunc
	ToggleFast                RuntimeControlFunc
	ToggleThinking            RuntimeControlFunc
	ToggleVim                 RuntimeControlFunc
	StopBackground            RuntimeControlFunc
	CompactSession            RuntimeControlFunc
	UndoLast                  RuntimeControlFunc
	ExportConversation        RuntimeControlFunc
	ExportConversationTo      ConversationExportFunc
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
	ReadModeLabel             func() string
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
	InformationView bool
	CommandView     bool
	ExportDialog    bool
	TextInputDialog bool
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

type queuedPrompt struct {
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
	readModeLabel             func() string
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
	selectPermissionMode      PermissionModeSelectFunc
	permissionSettings        *PermissionSettings
	permissionModeSelected    int
	information               *InformationView
	informationOffset         int
	commandView               *CommandView
	commandViewTab            int
	commandViewItem           int
	commandViewOffset         int
	exportDialog              *ExportDialog
	exportDialogSelected      int
	exportFilenameInput       bool
	exportComposerDraft       string
	textInputDialog           *TextInputDialog
	textInputComposerDraft    string
	submitTextInput           TextInputSubmitFunc
	theme                     string
	themePicker               bool
	themePickerSelected       int
	themePickerOriginal       string
	selectTheme               ThemeSelectFunc
	toggleFast                RuntimeControlFunc
	toggleThinking            RuntimeControlFunc
	toggleVim                 RuntimeControlFunc
	stopBackground            RuntimeControlFunc
	compactSession            RuntimeControlFunc
	undoLast                  RuntimeControlFunc
	exportConversation        RuntimeControlFunc
	exportConversationTo      ConversationExportFunc
	copyConversation          RuntimeControlFunc
	restoreConversation       ConversationRestoreFunc
	forkConversation          ConversationForkFunc
	summarizeConversation     ConversationSummarizeFunc
	summarizeUpToConversation ConversationSummarizeFunc
	copyMessage               MessageCopyFunc
	messageActions            bool
	messageActionTarget       int
	messageActionSelected     int
	sessionPicker             *sessionPickerModel
	queuedPrompts             []queuedPrompt
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
	exitPendingGeneration     uint64
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
	questionComposerDraft     string
	questionDraftCaptured     bool
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
	m.queuedPrompts = make([]queuedPrompt, 0, len(queued))
	for _, prompt := range queued {
		m.queuedPrompts = append(m.queuedPrompts, queuedPrompt{Text: prompt})
	}
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
// of times. It is used to verify safe clear and message-action behavior without
// owning a terminal.
func PreviewWithEscape(input string, presses int, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	for index := 0; index < presses; index++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
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
		CommandHint: m.commandArgumentHint,
		InlineHint:  m.inlineGhostText,
		Quit:        false,
	}
}

// PreviewWithBashMode renders the TUI after submitting a leading ! command.
// The command is routed through the same slash dispatcher used by /run.
func PreviewWithBashMode(input string, width int, height int) Preview {
	ta := newPromptTextarea(input)
	m := newModel(context.Background(), ta, nil, nil)
	captured := ""
	m.slash = func(_ context.Context, line string) (SlashResult, error) {
		captured = line
		return SlashResult{Output: "bash ok: " + line, Handled: true}, nil
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
		CommandView:     m.commandView != nil,
	}
}

// PreviewWithCommandView renders a deterministic tabbed command panel after
// applying the provided navigation keys.
func PreviewWithCommandView(view CommandView, keys []string, width int, height int) Preview {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	m.openCommandView(view)
	for _, key := range keys {
		updated, _ := m.Update(diffPreviewKey(key))
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:        m.View(),
		Value:       m.textarea.Value(),
		Mode:        m.mode(),
		CommandView: m.commandView != nil,
	}
}

// PreviewWithInformation renders a deterministic read-only information panel.
func PreviewWithInformation(view InformationView, keys []string, width int, height int) Preview {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	m.openInformation(view)
	for _, key := range keys {
		updated, _ := m.Update(diffPreviewKey(key))
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:            m.View(),
		Value:           m.textarea.Value(),
		Mode:            m.mode(),
		InformationView: m.information != nil,
	}
}

// PreviewWithExportDialog renders a deterministic conversation export dialog
// after applying the provided navigation keys.
func PreviewWithExportDialog(dialog ExportDialog, keys []string, width int, height int) Preview {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	m.openExportDialog(dialog)
	for _, key := range keys {
		updated, _ := m.Update(diffPreviewKey(key))
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:         m.View(),
		Value:        m.textarea.Value(),
		Mode:         m.mode(),
		ExportDialog: m.exportDialog != nil,
	}
}

// PreviewWithTextInputDialog renders a deterministic local text-input workflow.
func PreviewWithTextInputDialog(dialog TextInputDialog, keys []string, width int, height int) Preview {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	if width > 0 || height > 0 {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	m.openTextInputDialog(dialog)
	for _, key := range keys {
		updated, _ := m.Update(diffPreviewKey(key))
		if next, ok := updated.(model); ok {
			m = next
		}
	}
	return Preview{
		View:            m.View(),
		Value:           m.textarea.Value(),
		Mode:            m.mode(),
		TextInputDialog: m.textInputDialog != nil,
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
		CommandView:     m.commandView != nil,
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
	entries := transcriptEntries(options.Entries)
	m := newModel(ctx, ta, options.Candidates, entries)
	m.fileCandidates = append([]string(nil), options.FileCandidates...)
	m.submit = options.Submit
	m.submitStream = options.SubmitStream
	m.submitAttachments = options.SubmitAttachments
	m.submitStreamAttachments = options.SubmitStreamAttachments
	m.slash = options.Slash
	m.submitTextInput = options.SubmitTextInput
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
	m.selectPermissionMode = options.SelectPermissionMode
	m.theme, _ = NormalizeThemeName(options.Theme)
	if m.theme == "" {
		m.theme = "auto"
	}
	m.selectTheme = options.SelectTheme
	m.applyTheme()
	m.toggleFast = options.ToggleFast
	m.toggleThinking = options.ToggleThinking
	m.toggleVim = options.ToggleVim
	m.stopBackground = options.StopBackground
	m.compactSession = options.CompactSession
	m.undoLast = options.UndoLast
	m.exportConversation = options.ExportConversation
	m.exportConversationTo = options.ExportConversationTo
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
	m.readModeLabel = options.ReadModeLabel
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

func transcriptEntries(entries []Entry) []transcriptEntry {
	out := make([]transcriptEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, transcriptEntry{Role: entry.Role, Text: entry.Text, Tool: cloneToolActivity(entry.Tool)})
	}
	return out
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
