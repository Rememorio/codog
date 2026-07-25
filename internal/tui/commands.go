package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

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

func appendUniqueAttachments(attachments []string, paths []string) []string {
	for _, path := range paths {
		addUniqueAttachment(&attachments, path)
	}
	return attachments
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
		result, err := slash(ctx, line)
		if !result.Handled && err == nil {
			err = fmt.Errorf("unknown slash command: %s", line)
		}
		if result.Handled && err == nil && strings.TrimSpace(result.Output) == "" && !slashResultHasInteractiveView(result) {
			result.Output = "Done."
		}
		return turnDoneMsg{
			Role:               "system",
			Output:             result.Output,
			Query:              result.Query,
			Err:                err,
			Interrupted:        errors.Is(err, context.Canceled),
			Session:            result.Session,
			SessionChoices:     append([]SessionChoice(nil), result.SessionChoices...),
			OpenModelPicker:    result.OpenModelPicker,
			OpenThemePicker:    result.OpenThemePicker,
			OpenTodos:          result.OpenTodos,
			OpenMessageActions: result.OpenMessageActions,
			RuntimeAction:      result.RuntimeAction,
			Diff:               result.Diff,
			PermissionSettings: result.PermissionSettings,
			Information:        result.Information,
			CommandView:        result.CommandView,
			ExportDialog:       result.ExportDialog,
			TextInputDialog:    result.TextInputDialog,
		}
	}
}

func slashResultHasInteractiveView(result SlashResult) bool {
	return strings.TrimSpace(result.Query) != "" || result.Session != nil || len(result.SessionChoices) > 0 || result.OpenModelPicker || result.OpenThemePicker || result.OpenTodos || result.OpenMessageActions || strings.TrimSpace(result.RuntimeAction) != "" || result.Diff != nil || result.PermissionSettings != nil || result.Information != nil || result.CommandView != nil || result.ExportDialog != nil || result.TextInputDialog != nil
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

type permissionModeSelectDoneMsg struct {
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

func runTextInputSubmitCommand(ctx context.Context, submit TextInputSubmitFunc, action string, value string) tea.Cmd {
	return func() tea.Msg {
		result, err := submit(ctx, action, value)
		return runtimeControlDoneMsg{Result: result, Err: err}
	}
}

func runConversationExportCommand(ctx context.Context, export ConversationExportFunc, filename string) tea.Cmd {
	return func() tea.Msg {
		result, err := export(ctx, filename)
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

func runPermissionModeSelectCommand(ctx context.Context, selectMode PermissionModeSelectFunc, mode string) tea.Cmd {
	return func() tea.Msg {
		result, err := selectMode(ctx, mode)
		return permissionModeSelectDoneMsg{Result: result, Err: err}
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
	if !m.questionDraftCaptured {
		m.questionComposerDraft = m.textarea.Value()
		m.questionDraftCaptured = true
	}
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
		m.textarea.SetValue("")
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
	if m.questionDraftCaptured {
		m.textarea.SetValue(m.questionComposerDraft)
		m.textarea.CursorEnd()
	}
	m.awaitingQuestion = false
	m.questionRequest = nil
	m.questionSelected = 0
	m.questionCustom = false
	m.questionIndex = 0
	m.questionLegacy = false
	m.questionCursors = nil
	m.questionSelections = nil
	m.questionCustomValues = nil
	m.questionComposerDraft = ""
	m.questionDraftCaptured = false
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

const exitConfirmationWindow = 800 * time.Millisecond

func (m *model) armExit(key string, status string) tea.Cmd {
	m.exitPending = true
	m.exitKey = key
	m.exitPendingGeneration++
	m.status = status
	generation := m.exitPendingGeneration
	return tea.Tick(exitConfirmationWindow, func(time.Time) tea.Msg {
		return exitPendingExpiredMsg{Key: key, Generation: generation}
	})
}

func (m *model) clearExitPending() {
	m.exitPending = false
	m.exitKey = ""
}

func (m *model) clearComposerInput(saveHistory bool) {
	value := m.textarea.Value()
	if saveHistory {
		m.appendHistory(value)
	}
	if value != "" {
		m.pushComposerUndo()
	}
	m.textarea.SetValue("")
	m.attachments = nil
	m.closeAttachmentsPanel()
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.historyPos = -1
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
	m.sessionPicker = nil
	m.permissionSettings = nil
	m.permissionModeSelected = 0
	m.information = nil
	m.informationOffset = 0
	m.commandView = nil
	m.commandViewTab = 0
	m.commandViewItem = 0
	m.commandViewOffset = 0
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
	if msg.Permission != nil {
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
	if strings.Contains(current, "::codog-inline-vis{") && !strings.Contains(output, "::codog-inline-vis{") {
		m.transcript[m.streamingIndex].Text = output
		return
	}
	if current == output || strings.Contains(current, output) {
		return
	}
	m.transcript[m.streamingIndex].Text = strings.TrimSpace(current + "\n" + output)
}
