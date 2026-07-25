package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/fileinventory"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/sessionname"
	"github.com/Rememorio/codog/internal/sessionsummary"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/slash"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/charmbracelet/x/term"
	"github.com/chzyer/readline"
)

func tuiSlashHasStructuredFlags(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(strings.TrimSpace(arg), "-") {
			return true
		}
	}
	return false
}

func (a *App) tuiPlanSlashResult(rawArgs string, modeState *tuiModeState) (tui.SlashResult, bool, error) {
	rawArgs = strings.TrimSpace(rawArgs)
	req, err := parsePlanArgs(strings.Fields(rawArgs))
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if req.Action == "exit" || req.Action == "clear" || req.Action == "set" || (req.Action == "show" && rawArgs != "") {
		return tui.SlashResult{}, false, nil
	}
	if req.Action == "open" && a.planModeActive() {
		return tui.SlashResult{}, false, nil
	}
	if !a.planModeActive() {
		if _, err := planmode.Enter(a.Workspace, ""); err != nil {
			return tui.SlashResult{Handled: true}, true, err
		}
		a.enterTUIPlanMode(modeState)
		result := tui.SlashResult{Handled: true, Output: "Enabled plan mode."}
		if rawArgs != "" && !strings.EqualFold(rawArgs, "open") && req.Action == "enter" {
			result.Query = req.Text
		}
		return result, true, nil
	}
	state, err := planmode.Load(a.Workspace)
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if strings.TrimSpace(state.Plan) == "" {
		return tui.SlashResult{Handled: true, Output: "Already in plan mode. No plan written yet."}, true, nil
	}
	lines := []string{planmode.Path(a.Workspace), ""}
	lines = append(lines, strings.Split(strings.TrimSpace(state.Plan), "\n")...)
	view := tui.InformationView{Title: "Current Plan", Lines: lines}
	return tui.SlashResult{Handled: true, Information: &view}, true, nil
}

func (a *App) syncTUIPlanModeAfterSlash(line string, modeState *tuiModeState) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	if command == "/exit-plan" {
		a.exitTUIPlanMode(modeState)
		return
	}
	if command != "/plan" && command != "/ultraplan" {
		return
	}
	req, err := parsePlanArgs(fields[1:])
	if err != nil {
		return
	}
	switch req.Action {
	case "exit", "clear":
		a.exitTUIPlanMode(modeState)
	case "enter", "set":
		a.enterTUIPlanMode(modeState)
	}
}

func tuiSideQuestionInformation(line string, output string) (tui.InformationView, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || tuiSlashRequestsJSON(fields[1:]) {
		return tui.InformationView{}, false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	if command != "/btw" {
		return tui.InformationView{}, false
	}
	question := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
	lines := []string{question, ""}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "btw session:") || strings.HasPrefix(trimmed, "source session:") {
			continue
		}
		lines = append(lines, line)
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return tui.InformationView{Title: "/btw", Lines: lines, DismissOnConfirm: true}, true
}

const sessionTagHelp = `Usage: /tag <tag-name>

Toggle a searchable tag on the current session.
Run the same command again to remove the tag.
Tags are shown in /resume and can be searched there.

Examples:
  /tag bugfix
  /tag feature-auth
  /tag wip`

func (a *App) tagTUISession(sess *session.Session, tag string) (tui.SlashResult, bool, error) {
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.SlashResult{Handled: true}, true, errors.New("session is required")
	}
	if a.Sessions == nil {
		return tui.SlashResult{Handled: true}, true, errors.New("session store is not configured")
	}
	tag = strings.TrimSpace(tag)
	if tag == "" || tag == "-h" || tag == "--help" || tag == "help" {
		return tui.SlashResult{Handled: true, Output: sessionTagHelp}, true, nil
	}
	tag = session.NormalizeSessionTag(tag)
	if tag == "" {
		return tui.SlashResult{Handled: true}, true, errors.New("tag name cannot be empty")
	}
	if tag == session.NormalizeSessionTag(sess.Identity.Tag) {
		view := tui.CommandView{
			Title: "Remove tag?",
			Tabs: []tui.CommandViewTab{{
				Title: "Confirm",
				Lines: []string{
					"Current tag: #" + tag,
					"This will remove the tag from the current session.",
				},
				Items: []tui.CommandViewItem{
					{Label: "Yes, remove tag", Command: "/conversation-tag remove"},
					{Label: "No, keep tag", Command: "/conversation-tag keep"},
				},
			}},
		}
		return tui.SlashResult{Handled: true, CommandView: &view}, true, nil
	}
	identity, err := a.Sessions.SetTag(sess.ID, tag)
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	sess.Identity.Tag = identity.Tag
	state := a.tuiSessionState(sess)
	return tui.SlashResult{
		Handled: true,
		Output:  "Tagged session with #" + identity.Tag,
		Session: &state,
	}, true, nil
}

func (a *App) completeTUISessionTag(sess *session.Session, args []string) (tui.SlashResult, bool, error) {
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.SlashResult{Handled: true}, true, errors.New("session is required")
	}
	if a.Sessions == nil {
		return tui.SlashResult{Handled: true}, true, errors.New("session store is not configured")
	}
	if len(args) != 1 {
		return tui.SlashResult{Handled: true}, true, errors.New("invalid session tag confirmation")
	}
	current := session.NormalizeSessionTag(sess.Identity.Tag)
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "keep":
		if current == "" {
			return tui.SlashResult{Handled: true, Output: "No session tag is set."}, true, nil
		}
		state := a.tuiSessionState(sess)
		return tui.SlashResult{Handled: true, Output: "Kept tag #" + current, Session: &state}, true, nil
	case "remove":
		if current == "" {
			return tui.SlashResult{Handled: true, Output: "No session tag is set."}, true, nil
		}
		identity, err := a.Sessions.SetTag(sess.ID, "")
		if err != nil {
			return tui.SlashResult{Handled: true}, true, err
		}
		sess.Identity.Tag = identity.Tag
		state := a.tuiSessionState(sess)
		return tui.SlashResult{Handled: true, Output: "Removed tag #" + current, Session: &state}, true, nil
	default:
		return tui.SlashResult{Handled: true}, true, errors.New("invalid session tag confirmation")
	}
}

func (a *App) renameTUISession(sess *session.Session, name string) (tui.SlashResult, bool, error) {
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.SlashResult{Handled: true}, true, errors.New("session is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = generatedTUISessionTitle(sess)
		if name == "" {
			return tui.SlashResult{Handled: true}, true, errors.New("could not generate a name: no conversation context yet; usage: /rename <name>")
		}
	}
	identity, err := a.Sessions.UpdateIdentity(sess.ID, session.SessionIdentity{Title: name})
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	sess.Identity = identity
	state := a.tuiSessionState(sess)
	return tui.SlashResult{
		Handled: true,
		Output:  "Session renamed to: " + name,
		Session: &state,
	}, true, nil
}

func (a *App) branchTUISession(ctx context.Context, sess *session.Session, name string) (tui.SlashResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.SlashResult{Handled: true}, true, errors.New("session is required")
	}
	current, err := a.Sessions.Open(sess.ID)
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	if len(current.Messages) == 0 {
		return tui.SlashResult{Handled: true}, true, errors.New("no conversation to branch")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		base := strings.TrimSpace(current.Identity.Title)
		if base == "" || base == current.ID {
			base = generatedTUISessionTitle(current)
		}
		if base == "" {
			base = "Branched conversation"
		}
		name, err = a.uniqueTUIBranchTitle(base)
		if err != nil {
			return tui.SlashResult{Handled: true}, true, err
		}
	}
	forked, err := a.Sessions.Fork(current.ID, name)
	if err != nil {
		return tui.SlashResult{Handled: true}, true, err
	}
	identity, err := a.Sessions.UpdateIdentity(forked.ID, session.SessionIdentity{Title: name})
	if err != nil {
		_ = a.Sessions.Delete(forked.ID)
		return tui.SlashResult{Handled: true}, true, err
	}
	forked.Identity = identity
	*sess = *forked
	state := a.tuiSessionState(sess)
	return tui.SlashResult{
		Handled: true,
		Output:  fmt.Sprintf("Conversation branched as %s (%s).", name, forked.ID),
		Session: &state,
	}, true, nil
}

func generatedTUISessionTitle(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	for _, message := range sess.Messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		if text := trimSingleLine(firstMessageText(message), 80); text != "" {
			return text
		}
	}
	return ""
}

func (a *App) uniqueTUIBranchTitle(base string) (string, error) {
	sessions, err := a.Sessions.List()
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(sessions))
	for _, candidate := range sessions {
		used[strings.ToLower(strings.TrimSpace(candidate.Identity.Title))] = true
	}
	name := strings.TrimSpace(base) + " (Branch)"
	if !used[strings.ToLower(name)] {
		return name, nil
	}
	for index := 2; ; index++ {
		name = fmt.Sprintf("%s (Branch %d)", strings.TrimSpace(base), index)
		if !used[strings.ToLower(name)] {
			return name, nil
		}
	}
}

func (a *App) tuiDoctorInformation() (*tui.InformationView, error) {
	var out bytes.Buffer
	previous := a.Out
	a.Out = &out
	err := a.Doctor(nil)
	a.Out = previous
	if err != nil && strings.TrimSpace(out.String()) == "" {
		return nil, err
	}
	return &tui.InformationView{Title: "Doctor", Lines: tuiReportLines(out.String(), "Doctor")}, nil
}

func (a *App) tuiIDECommandView() (*tui.CommandView, error) {
	var out bytes.Buffer
	previous := a.Out
	a.Out = &out
	err := a.IDE(nil)
	a.Out = previous
	if err != nil {
		return nil, err
	}
	server := bridge.Server{
		Sessions:   a.Sessions,
		Version:    version,
		Workspace:  a.Workspace,
		ConfigHome: a.Config.ConfigHome,
		TrustToken: a.Config.Future.EditorBridgeToken,
	}
	state, err := server.EditorState()
	if err != nil {
		return nil, err
	}
	tab := tui.CommandViewTab{
		Title:          "Connections",
		Lines:          tuiReportLines(out.String(), "IDE Bridge"),
		RefreshCommand: "/ide",
	}
	if state.Identity == nil {
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:       "No IDE connected",
			Value:       "disconnected",
			Description: "Start the bridge, then connect a trusted IDE client for this workspace",
			Command:     "/ide status",
		})
	} else {
		label := firstNonEmpty(state.Identity.Editor, "IDE")
		if state.Identity.Version != "" {
			label += " " + state.Identity.Version
		}
		status := "connected"
		if !state.Identity.Trusted {
			status = "untrusted"
		}
		tab.Items = append(tab.Items, tui.CommandViewItem{
			Label:            label,
			Value:            status,
			Description:      firstNonEmpty(state.Identity.Workspace, a.Workspace),
			Command:          "/ide status",
			SecondaryLabel:   "disconnect",
			SecondaryCommand: "/ide clear",
			SecondaryKey:     "x",
		})
	}
	tab.Items = append(tab.Items, tui.CommandViewItem{
		Label:       "Start IDE bridge",
		Value:       "background",
		Description: "Run the local editor bridge for IDE clients",
		Command:     "/bridge serve",
	})
	return &tui.CommandView{Title: "IDE", Tabs: []tui.CommandViewTab{tab}}, nil
}

func tuiMemoryRefresh(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 || tuiSlashRequestsJSON(fields[1:]) {
		return false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	if command != "/memory" {
		return false
	}
	switch normalizeMemoryAction(fields[1]) {
	case "add", "ensure", "edit", "reset":
		return true
	default:
		return false
	}
}

func tuiIDERefresh(line string) bool {
	fields := strings.Fields(line)
	if len(fields) != 2 || tuiSlashRequestsJSON(fields[1:]) {
		return false
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	return command == "/ide" && strings.EqualFold(strings.TrimSpace(fields[1]), "clear")
}

func (a *App) tuiDiffView(req diffRequest) (tui.DiffView, error) {
	if req.Staged || len(req.Paths) > 0 {
		name := "Unstaged changes"
		subtitle := "git diff"
		if req.Staged {
			name = "Staged changes"
			subtitle = "git diff --staged"
		}
		source, err := a.tuiDiffSource(name, subtitle, req)
		if err != nil {
			return tui.DiffView{}, err
		}
		return tui.DiffView{Sources: []tui.DiffSource{source}}, nil
	}

	unstaged, err := a.tuiDiffSource("Unstaged changes", "git diff", diffRequest{Format: "text"})
	if err != nil {
		return tui.DiffView{}, err
	}
	untracked, err := a.tuiUntrackedDiffFiles()
	if err != nil {
		return tui.DiffView{}, err
	}
	unstaged.Files = append(unstaged.Files, untracked...)
	staged, err := a.tuiDiffSource("Staged changes", "git diff --staged", diffRequest{Format: "text", Staged: true})
	if err != nil {
		return tui.DiffView{}, err
	}
	sources := []tui.DiffSource{}
	if len(unstaged.Files) > 0 {
		sources = append(sources, unstaged)
	}
	if len(staged.Files) > 0 {
		sources = append(sources, staged)
	}
	if len(sources) == 0 {
		sources = append(sources, tui.DiffSource{Name: "Uncommitted changes", Subtitle: "working tree clean"})
	}
	return tui.DiffView{Sources: sources}, nil
}

func (a *App) tuiDiffSource(name string, subtitle string, req diffRequest) (tui.DiffSource, error) {
	report, err := a.buildDiffReport(req)
	if err != nil {
		return tui.DiffSource{}, err
	}
	source := tui.DiffSource{Name: name, Subtitle: subtitle}
	for _, path := range report.ChangedFiles {
		patch, err := gitops.DiffWithOptions(a.Workspace, gitops.DiffOptions{Staged: req.Staged, Paths: []string{path}})
		if err != nil {
			return tui.DiffSource{}, err
		}
		source.Files = append(source.Files, tui.DiffFile{
			Path:    path,
			Status:  tuiDiffStatus(patch),
			Summary: tuiDiffSummary(patch),
			Diff:    patch,
		})
	}
	return source, nil
}

func tuiDiffStatus(patch string) string {
	switch {
	case strings.Contains(patch, "\nnew file mode "):
		return "added"
	case strings.Contains(patch, "\ndeleted file mode "):
		return "deleted"
	case strings.Contains(patch, "\nrename from "):
		return "renamed"
	default:
		return "modified"
	}
}

func tuiDiffSummary(patch string) string {
	added, removed := 0, 0
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			removed++
		}
	}
	return fmt.Sprintf("+%d -%d", added, removed)
}

func (a *App) tuiUntrackedDiffFiles() ([]tui.DiffFile, error) {
	raw, err := gitops.Run(a.Workspace, "status", "--short", "--branch", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	status := buildGitStatusReport(raw)
	files := []tui.DiffFile{}
	for _, entry := range status.Entries {
		if entry.Code != "??" {
			continue
		}
		path := decodeGitStatusPath(entry.Path)
		if path == ".codog" || strings.HasPrefix(filepath.ToSlash(path), ".codog/") {
			continue
		}
		preview, lines := a.tuiUntrackedPreview(path)
		files = append(files, tui.DiffFile{
			Path:    path,
			Status:  "untracked",
			Summary: fmt.Sprintf("+%d", lines),
			Diff:    preview,
		})
	}
	return files, nil
}

func decodeGitStatusPath(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "\"") {
		if decoded, err := strconv.Unquote(path); err == nil {
			return decoded
		}
	}
	return path
}

func (a *App) tuiUntrackedPreview(path string) (string, int) {
	const maxPreviewBytes = 16 * 1024
	workspace, err := filepath.Abs(a.Workspace)
	if err != nil {
		return "(preview unavailable)", 0
	}
	target, err := filepath.Abs(filepath.Join(workspace, filepath.FromSlash(path)))
	if err != nil {
		return "(preview unavailable)", 0
	}
	relative, err := filepath.Rel(workspace, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "(preview unavailable)", 0
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "(preview unavailable)", 0
	}
	if info.Mode()&os.ModeSymlink != 0 {
		destination, err := os.Readlink(target)
		if err != nil {
			return "(symlink preview unavailable)", 0
		}
		return "symlink: " + path + " -> " + destination, 0
	}
	if !info.Mode().IsRegular() {
		return "(preview unavailable for non-regular file)", 0
	}
	file, err := os.Open(target)
	if err != nil {
		return "(preview unavailable)", 0
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxPreviewBytes+1))
	if err != nil || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "(binary or unreadable file)", 0
	}
	truncated := len(data) > maxPreviewBytes
	if truncated {
		data = data[:maxPreviewBytes]
	}
	content := strings.TrimSuffix(string(data), "\n")
	lineCount := 0
	if content != "" {
		lineCount = strings.Count(content, "\n") + 1
	}
	lines := []string{"new file: " + path}
	for _, line := range strings.Split(content, "\n") {
		lines = append(lines, "+"+line)
	}
	if truncated {
		lines = append(lines, "... preview truncated")
	}
	return strings.Join(lines, "\n"), lineCount
}

func (a *App) tuiModelOptions() []string {
	out := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range out {
			if strings.EqualFold(existing, value) {
				return
			}
		}
		out = append(out, value)
	}
	add(a.Config.Model)
	add(config.DefaultModel)
	for _, alias := range modelAliases() {
		add(alias.Name)
	}
	return out
}

func (a *App) selectTUIModel(ctx context.Context, selected string) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return tui.RuntimeControlResult{}, errors.New("model is required")
	}
	previous := strings.TrimSpace(a.Config.Model)
	var out bytes.Buffer
	oldOut, oldErr := a.Out, a.Err
	a.Out, a.Err = &out, &out
	err := a.Model([]string{selected})
	a.Out, a.Err = oldOut, oldErr
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	lines := []string{"Model: " + strings.TrimSpace(a.Config.Model)}
	if previous != "" && !strings.EqualFold(previous, a.Config.Model) {
		lines = append(lines, "Previous: "+previous)
	}
	return tui.RuntimeControlResult{Title: "Model", Status: "model selected", Lines: lines, Badges: []string{"model: " + strings.TrimSpace(a.Config.Model)}}, nil
}

func (a *App) selectTUITheme(ctx context.Context, selected string) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	rawSelected := selected
	selected, ok := tui.NormalizeThemeName(selected)
	if !ok {
		return tui.RuntimeControlResult{}, fmt.Errorf("unknown theme %q", rawSelected)
	}
	previous := effectiveTUITheme(a.Config.Theme)
	var out bytes.Buffer
	oldOut, oldErr := a.Out, a.Err
	a.Out, a.Err = &out, &out
	err := a.Theme([]string{"set", selected})
	a.Out, a.Err = oldOut, oldErr
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	return tui.RuntimeControlResult{
		Title:  "Theme",
		Status: "theme " + selected,
		Lines:  []string{"Theme: " + selected, "Previous: " + previous},
		Badges: []string{"theme: " + selected},
	}, nil
}

func (a *App) submitTUITextInput(ctx context.Context, action string, value string) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "add-dir":
		report, err := pathscope.Add(a.Workspace, []string{value})
		if err != nil {
			return tui.RuntimeControlResult{}, err
		}
		if err := a.refreshBuiltinToolScope(); err != nil {
			return tui.RuntimeControlResult{}, err
		}
		var out bytes.Buffer
		pathscope.RenderText(&out, report)
		return tui.RuntimeControlResult{
			Title:  "Working Directory Added",
			Status: "directory added",
			Lines:  tuiReportLines(out.String(), "Additional Directories"),
		}, nil
	default:
		return tui.RuntimeControlResult{}, fmt.Errorf("unknown input action %q", action)
	}
}

func (a *App) toggleTUIFast(ctx context.Context) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	previous := fastModeEnabled(a.Config.FastMode)
	var out bytes.Buffer
	oldOut, oldErr := a.Out, a.Err
	a.Out, a.Err = &out, &out
	err := a.Fast([]string{"toggle"})
	a.Out, a.Err = oldOut, oldErr
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	enabled := fastModeEnabled(a.Config.FastMode)
	lines := []string{fmt.Sprintf("Fast mode: %s", onOff(enabled))}
	if previous != enabled {
		lines = append(lines, fmt.Sprintf("Previous: %s", onOff(previous)))
	}
	return tui.RuntimeControlResult{Title: "Fast Mode", Status: "fast " + onOff(enabled), Lines: lines, Badges: []string{"fast: " + onOff(enabled)}, Setting: "fast", Value: onOff(enabled)}, nil
}

func (a *App) toggleTUIThinking(ctx context.Context) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	previous := effectiveEffort(a.Config.ReasoningEffort)
	next := nextTUIReasoningEffort(previous)
	var out bytes.Buffer
	oldOut, oldErr := a.Out, a.Err
	a.Out, a.Err = &out, &out
	err := a.Reasoning([]string{"set", next})
	a.Out, a.Err = oldOut, oldErr
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	current := effectiveEffort(a.Config.ReasoningEffort)
	return tui.RuntimeControlResult{
		Title:   "Thinking",
		Status:  "thinking " + current,
		Lines:   []string{"Reasoning: " + current, "Previous: " + previous},
		Badges:  []string{"thinking: " + current},
		Setting: "thinking",
		Value:   current,
	}, nil
}

func (a *App) toggleTUIVim(ctx context.Context) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	previous := editorModeIsVim(a.Config.EditorMode)
	var out bytes.Buffer
	oldOut, oldErr := a.Out, a.Err
	a.Out, a.Err = &out, &out
	err := a.Vim([]string{"toggle"})
	a.Out, a.Err = oldOut, oldErr
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	enabled := editorModeIsVim(a.Config.EditorMode)
	return tui.RuntimeControlResult{
		Title:      "Vim Mode",
		Status:     "vim " + onOff(enabled),
		Lines:      []string{"Vim mode: " + onOff(enabled), "Previous: " + onOff(previous)},
		Setting:    "vim",
		Value:      onOff(enabled),
		VimEnabled: &enabled,
	}, nil
}

func (a *App) tuiRuntimeBadges() []string {
	badges := []string{}
	if model := strings.TrimSpace(a.Config.Model); model != "" {
		badges = append(badges, "model: "+model)
	}
	badges = append(badges, "fast: "+onOff(fastModeEnabled(a.Config.FastMode)))
	badges = append(badges, "thinking: "+effectiveEffort(a.Config.ReasoningEffort))
	return badges
}

func (a *App) tuiKeybindings() map[string][]string {
	bindings, validationErrors := a.effectiveKeybindings("")
	if len(validationErrors) != 0 {
		return nil
	}
	out := map[string][]string{}
	for _, binding := range bindings {
		if binding.Context != "tui" || binding.Disabled || binding.Source != "user" {
			continue
		}
		action := strings.TrimSpace(binding.Entry.Action)
		key := strings.TrimSpace(binding.Entry.NormalizedKey)
		if action == "" || key == "" {
			continue
		}
		out[action] = append(out[action], key)
	}
	return out
}

func (a *App) tuiContextKeybindings() map[string]map[string][]string {
	bindings, validationErrors := a.effectiveKeybindings("")
	if len(validationErrors) != 0 {
		return nil
	}
	out := map[string]map[string][]string{}
	for _, binding := range bindings {
		if binding.Disabled || binding.Source != "user" || !strings.HasPrefix(binding.Context, "tui-") {
			continue
		}
		action := strings.TrimSpace(binding.Entry.Action)
		key := strings.TrimSpace(binding.Entry.NormalizedKey)
		if action == "" || key == "" {
			continue
		}
		if out[binding.Context] == nil {
			out[binding.Context] = map[string][]string{}
		}
		out[binding.Context][action] = append(out[binding.Context][action], key)
	}
	return out
}

func (a *App) stopTUIBackground(ctx context.Context) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	store := background.NewStore(a.Config.ConfigHome)
	tasks, err := store.List()
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	stopped := []background.Task{}
	for _, task := range tasks {
		if !strings.EqualFold(task.Status, "running") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return tui.RuntimeControlResult{}, err
		}
		stoppedTask, err := store.Stop(task.ID)
		if err != nil {
			return tui.RuntimeControlResult{}, err
		}
		stopped = append(stopped, stoppedTask)
		a.runTaskCompletedHook(context.Background(), stoppedTask, "manual")
		a.runNotificationHook(context.Background(), "background_task_stopped", "Background task stopped", fmt.Sprintf("Background task %s stopped: %s", stoppedTask.ID, stoppedTask.Command))
		if stoppedTask.Kind == "agent" {
			a.runSubagentStopHook(context.Background(), stoppedTask.ID, subagentTypeForTask(stoppedTask), stoppedTask.LogPath, lastBackgroundLogLine(store, stoppedTask), false)
		}
	}
	if len(stopped) == 0 {
		return tui.RuntimeControlResult{
			Title:  "Background Tasks",
			Status: "no background tasks",
			Lines:  []string{"No running background tasks or agents."},
		}, nil
	}
	lines := []string{fmt.Sprintf("Stopped: %d", len(stopped))}
	for index, task := range stopped {
		if index >= 5 {
			lines = append(lines, fmt.Sprintf("... %d more", len(stopped)-index))
			break
		}
		label := strings.TrimSpace(task.Kind)
		if label == "" {
			label = "task"
		}
		lines = append(lines, fmt.Sprintf("%s: %s", label, task.ID))
	}
	return tui.RuntimeControlResult{
		Title:  "Background Tasks",
		Status: fmt.Sprintf("stopped %d", len(stopped)),
		Lines:  lines,
	}, nil
}

func (a *App) compactTUISession(ctx context.Context, sess *session.Session) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.RuntimeControlResult{}, errors.New("session is required")
	}
	var out bytes.Buffer
	oldOut, oldErr := a.Out, a.Err
	a.Out, a.Err = &out, &out
	err := a.Compact([]string{"--json"}, config.FlagOverrides{SessionID: sess.ID})
	a.Out, a.Err = oldOut, oldErr
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	var result session.ReplaceResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if current, err := a.Sessions.Open(sess.ID); err == nil {
		*sess = *current
	}
	lines := []string{
		"Session: " + result.SessionID,
		fmt.Sprintf("Original: %d", result.OriginalMessages),
		fmt.Sprintf("Remaining: %d", result.RemainingMessages),
		fmt.Sprintf("Removed: %d", result.RemovedMessages),
	}
	status := "compacted"
	if result.RemovedMessages > 0 {
		status = fmt.Sprintf("compacted %d", result.RemovedMessages)
	}
	return tui.RuntimeControlResult{
		Title:  "Session Compacted",
		Status: status,
		Lines:  lines,
	}, nil
}

func (a *App) undoTUIChange(ctx context.Context) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	report, err := undo.RestoreLast(a.Workspace)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	lines := []string{
		"Tool: " + emptyAsNone(report.Tool),
		"Path: " + report.Path,
	}
	if report.Restored {
		lines = append(lines, "Restored: true", fmt.Sprintf("Bytes: %d", report.Bytes))
	}
	if report.Removed {
		lines = append(lines, "Removed: true")
	}
	lines = append(lines, fmt.Sprintf("Remaining: %d", report.Remaining))
	status := "undo"
	if report.Restored {
		status = "restored"
	} else if report.Removed {
		status = "removed"
	}
	return tui.RuntimeControlResult{
		Title:  "Undo",
		Status: status,
		Lines:  lines,
	}, nil
}

func (a *App) exportTUIConversation(ctx context.Context, sess *session.Session) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.RuntimeControlResult{}, errors.New("session is required")
	}
	outputDir := filepath.Join(a.Workspace, ".codog", "exports")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	output := filepath.Join(".codog", "exports", safeTUIExportName(sess.ID)+".md")
	return a.exportTUIConversationTo(ctx, sess, output)
}

func (a *App) exportTUIConversationTo(ctx context.Context, sess *session.Session, output string) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.RuntimeControlResult{}, errors.New("session is required")
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return tui.RuntimeControlResult{}, errors.New("export filename is required")
	}
	var out bytes.Buffer
	oldOut, oldErr := a.Out, a.Err
	a.Out, a.Err = &out, &out
	err := a.ExportWithOverrides([]string{"--format", "markdown", "--output", output}, config.FlagOverrides{SessionID: sess.ID})
	a.Out, a.Err = oldOut, oldErr
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	var report struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
		Format    string `json:"format"`
		Messages  int    `json:"messages"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	displayPath := report.File
	if rel, err := filepath.Rel(a.Workspace, report.File); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		displayPath = filepath.ToSlash(rel)
	}
	lines := []string{
		"Session: " + report.SessionID,
		"File: " + displayPath,
		"Format: " + report.Format,
		fmt.Sprintf("Messages: %d", report.Messages),
	}
	return tui.RuntimeControlResult{
		Title:  "Conversation Exported",
		Status: "exported",
		Lines:  lines,
	}, nil
}

func (a *App) copyTUIConversation(ctx context.Context, sess *session.Session) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.RuntimeControlResult{}, errors.New("session is required")
	}
	req := copyRequest{SessionID: sess.ID, Scope: "all", Format: session.ExportMarkdown}
	data, copiedSession, format, err := a.copyPayload(req)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return tui.RuntimeControlResult{}, errors.New("nothing to copy")
	}
	clipboard, err := writeClipboard(ctx, data)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	messageCount := 0
	sessionID := sess.ID
	if copiedSession != nil {
		sessionID = copiedSession.ID
		messageCount = len(copiedSession.Messages)
	}
	lines := []string{
		"Session: " + sessionID,
		"Clipboard: " + clipboard,
		"Format: " + format,
		fmt.Sprintf("Messages: %d", messageCount),
		fmt.Sprintf("Bytes: %d", len(data)),
	}
	return tui.RuntimeControlResult{
		Title:  "Conversation Copied",
		Status: "copied",
		Lines:  lines,
	}, nil
}

func (a *App) copyTUIMessage(ctx context.Context, text string) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return tui.RuntimeControlResult{}, errors.New("message is empty")
	}
	data := []byte(text + "\n")
	clipboard, err := writeClipboard(ctx, data)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	lines := []string{
		"Clipboard: " + clipboard,
		fmt.Sprintf("Lines: %d", countTextLines(text)),
		fmt.Sprintf("Bytes: %d", len(data)),
	}
	return tui.RuntimeControlResult{
		Title:  "Message Copied",
		Status: "message copied",
		Lines:  lines,
	}, nil
}

func safeTUIExportName(value string) string {
	name := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(strings.TrimSpace(value), "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		return "session"
	}
	return name
}

func (a *App) restoreTUIConversation(ctx context.Context, sess *session.Session, keepMessages int) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.RuntimeControlResult{}, errors.New("session is required")
	}
	if keepMessages < 0 {
		return tui.RuntimeControlResult{}, errors.New("restore point is invalid")
	}
	current, err := a.Sessions.Open(sess.ID)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if keepMessages > len(current.Messages) {
		keepMessages = len(current.Messages)
	}
	retained := append([]anthropic.Message(nil), current.Messages[:keepMessages]...)
	result, err := a.Sessions.ReplaceMessages(current, retained)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	refreshed, err := a.Sessions.Open(sess.ID)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	*sess = *refreshed
	lines := []string{
		"Session: " + result.SessionID,
		fmt.Sprintf("Remaining: %d", result.RemainingMessages),
		fmt.Sprintf("Removed: %d", result.RemovedMessages),
	}
	status := "conversation restored"
	if result.RemovedMessages > 0 {
		status = fmt.Sprintf("restored %d", result.RemovedMessages)
	}
	return tui.RuntimeControlResult{
		Title:  "Conversation Restored",
		Status: status,
		Lines:  lines,
	}, nil
}

func (a *App) forkTUIConversation(ctx context.Context, sess *session.Session, keepMessages int) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.RuntimeControlResult{}, errors.New("session is required")
	}
	if keepMessages < 0 {
		return tui.RuntimeControlResult{}, errors.New("fork point is invalid")
	}
	current, err := a.Sessions.Open(sess.ID)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if keepMessages > len(current.Messages) {
		keepMessages = len(current.Messages)
	}
	retained := append([]anthropic.Message(nil), current.Messages[:keepMessages]...)
	forked, err := a.Sessions.Fork(current.ID, "rewind")
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	result, err := a.Sessions.ReplaceMessages(forked, retained)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	refreshed, err := a.Sessions.Open(forked.ID)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	*sess = *refreshed
	lines := []string{
		"Session: " + result.SessionID,
		"Parent: " + current.ID,
		fmt.Sprintf("Remaining: %d", result.RemainingMessages),
		fmt.Sprintf("Removed: %d", result.RemovedMessages),
	}
	status := "conversation forked"
	if result.RemovedMessages > 0 {
		status = fmt.Sprintf("forked %d", result.RemovedMessages)
	}
	return tui.RuntimeControlResult{
		Title:  "Conversation Forked",
		Status: status,
		Lines:  lines,
	}, nil
}

func (a *App) summarizeTUIConversation(ctx context.Context, sess *session.Session, keepMessages int) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.RuntimeControlResult{}, errors.New("session is required")
	}
	if keepMessages < 0 {
		return tui.RuntimeControlResult{}, errors.New("summarize point is invalid")
	}
	current, err := a.Sessions.Open(sess.ID)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if keepMessages > len(current.Messages) {
		keepMessages = len(current.Messages)
	}
	omitted := append([]anthropic.Message(nil), current.Messages[keepMessages:]...)
	if len(omitted) == 0 {
		return tui.RuntimeControlResult{
			Title:  "Conversation Summarized",
			Status: "nothing to summarize",
			Lines: []string{
				"Session: " + current.ID,
				fmt.Sprintf("Before: %d", keepMessages),
				"Summarized: 0",
			},
		}, nil
	}
	compactPayload := runloop.CompactHookPayload("tui_summarize", current.ID, len(current.Messages), keepMessages)
	if err := a.lifecycleHookRunner().PreCompact(context.Background(), compactPayload); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	summaryText := sessionsummary.BuildCompactionSummary(omitted, 0).Summary
	summary := anthropic.TextMessage("user", summaryText)
	next := make([]anthropic.Message, 0, keepMessages+1)
	next = append(next, current.Messages[:keepMessages]...)
	next = append(next, summary)
	result, err := a.Sessions.ReplaceMessages(current, next)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if err := a.lifecycleHookRunner().PostCompact(context.Background(), compactPayload); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	refreshed, err := a.Sessions.Open(current.ID)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	*sess = *refreshed
	lines := []string{
		"Session: " + result.SessionID,
		fmt.Sprintf("Before: %d", keepMessages),
		fmt.Sprintf("Summarized: %d", len(omitted)),
		fmt.Sprintf("Remaining: %d", result.RemainingMessages),
		fmt.Sprintf("Removed: %d", result.RemovedMessages),
	}
	status := "conversation summarized"
	if len(omitted) > 0 {
		status = fmt.Sprintf("summarized %d", len(omitted))
	}
	return tui.RuntimeControlResult{
		Title:  "Conversation Summarized",
		Status: status,
		Lines:  lines,
	}, nil
}

func (a *App) summarizeUpToTUIConversation(ctx context.Context, sess *session.Session, summarizeMessages int) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return tui.RuntimeControlResult{}, errors.New("session is required")
	}
	if summarizeMessages < 0 {
		return tui.RuntimeControlResult{}, errors.New("summarize point is invalid")
	}
	current, err := a.Sessions.Open(sess.ID)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if summarizeMessages > len(current.Messages) {
		summarizeMessages = len(current.Messages)
	}
	omitted := append([]anthropic.Message(nil), current.Messages[:summarizeMessages]...)
	if len(omitted) == 0 {
		return tui.RuntimeControlResult{
			Title:  "Earlier Conversation Summarized",
			Status: "nothing to summarize",
			Lines: []string{
				"Session: " + current.ID,
				"Summarized: 0",
				fmt.Sprintf("After: %d", len(current.Messages)),
			},
		}, nil
	}
	retained := append([]anthropic.Message(nil), current.Messages[summarizeMessages:]...)
	compactPayload := runloop.CompactHookPayload("tui_summarize_up_to", current.ID, len(current.Messages), len(retained))
	if err := a.lifecycleHookRunner().PreCompact(context.Background(), compactPayload); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	summaryText := sessionsummary.BuildCompactionSummary(omitted, len(retained)).Summary
	summary := anthropic.TextMessage("user", summaryText)
	next := make([]anthropic.Message, 0, len(retained)+1)
	next = append(next, summary)
	next = append(next, retained...)
	result, err := a.Sessions.ReplaceMessages(current, next)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if err := a.lifecycleHookRunner().PostCompact(context.Background(), compactPayload); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	refreshed, err := a.Sessions.Open(current.ID)
	if err != nil {
		return tui.RuntimeControlResult{}, err
	}
	*sess = *refreshed
	lines := []string{
		"Session: " + result.SessionID,
		fmt.Sprintf("Summarized: %d", len(omitted)),
		fmt.Sprintf("After: %d", len(retained)),
		fmt.Sprintf("Remaining: %d", result.RemainingMessages),
		fmt.Sprintf("Removed: %d", result.RemovedMessages),
	}
	status := "earlier conversation summarized"
	if len(omitted) > 0 {
		status = fmt.Sprintf("summarized earlier %d", len(omitted))
	}
	return tui.RuntimeControlResult{
		Title:  "Earlier Conversation Summarized",
		Status: status,
		Lines:  lines,
	}, nil
}

func nextTUIReasoningEffort(current string) string {
	current = strings.ToLower(strings.TrimSpace(current))
	if current == "" {
		current = "auto"
	}
	for index, level := range availableEfforts {
		if level == current {
			return availableEfforts[(index+1)%len(availableEfforts)]
		}
	}
	return "auto"
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func (a *App) readTUITodos(ctx context.Context) ([]tui.TodoItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := todos.Load(a.Workspace)
	if err != nil {
		return nil, err
	}
	items := make([]tui.TodoItem, 0, len(state.Items))
	for _, item := range state.Items {
		items = append(items, tui.TodoItem{
			ID:         item.ID,
			Content:    item.Content,
			ActiveForm: item.ActiveForm,
			Status:     item.Status,
			Priority:   item.Priority,
		})
	}
	return items, nil
}

func (a *App) startTUIBackgroundPrompt(ctx context.Context, sessionID string, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("background prompt is empty")
	}
	executable, err := a.executablePath()
	if err != nil {
		return "", err
	}
	command := buildDetachedPromptCommand(a.Config.ConfigHome, executable, prompt)
	task, err := background.NewStore(a.Config.ConfigHome).RunWithOptions(command, a.Workspace, background.RunOptions{
		Kind:        "prompt",
		SessionID:   sessionID,
		Prompt:      prompt,
		Description: "TUI background prompt",
	})
	if err != nil {
		return "", err
	}
	a.runTaskCreatedHook(context.Background(), task)
	a.runNotificationHook(context.Background(), "background_task_started", "Background prompt started", fmt.Sprintf("Background task %s started from TUI", task.ID))
	return fmt.Sprintf("Background task %s started. Use /background logs %s to inspect output.", task.ID, task.ID), nil
}

func (a *App) renderTUITaskBoard(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	board, err := background.NewStore(a.Config.ConfigHome).LaneBoard(30 * time.Second)
	if err != nil {
		return "", err
	}
	return renderTUILaneBoard(board), nil
}

func (a *App) readTUIClipboard(ctx context.Context) (tui.PasteContent, error) {
	if image, err := readClipboardImage(ctx); err == nil {
		path, err := a.storeTUIClipboardImage(image)
		if err != nil {
			return tui.PasteContent{}, err
		}
		return tui.PasteContent{
			AttachmentPath: path,
			MediaType:      image.MediaType,
		}, nil
	} else if !errors.Is(err, errNoClipboardImage) {
		return tui.PasteContent{}, err
	}
	data, _, err := readClipboard(ctx)
	if err != nil {
		return tui.PasteContent{}, err
	}
	return tui.PasteContent{Text: string(data)}, nil
}

func (a *App) tuiFileReferenceCandidates() []string {
	report, err := fileinventory.Build(a.Workspace, fileinventory.Options{
		Limit:            1500,
		RespectGitignore: a.Config.EffectiveRespectGitignore(),
	})
	if err != nil {
		return nil
	}
	candidates := make([]string, 0, len(report.Files))
	for _, file := range report.Files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			continue
		}
		candidates = append(candidates, filepath.ToSlash(path))
	}
	return candidates
}

func (a *App) storeTUIClipboardImage(image clipboardImage) (string, error) {
	if len(image.Data) == 0 {
		return "", errNoClipboardImage
	}
	workspace := strings.TrimSpace(a.Workspace)
	if workspace == "" {
		workspace = "."
	}
	extension := strings.ToLower(strings.TrimSpace(image.Extension))
	if extension == "" {
		extension = extensionForClipboardImageMediaType(image.MediaType)
	}
	if extension == "" {
		extension = ".png"
	}
	if !strings.HasPrefix(extension, ".") {
		extension = "." + extension
	}
	dir := filepath.Join(workspace, ".codog", "attachments", "clipboard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "clipboard-" + time.Now().UTC().Format("20060102T150405.000000000Z0700") + extension
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, image.Data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func extensionForClipboardImageMediaType(mediaType string) string {
	switch cleanMediaType(mediaType) {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/png":
		return ".png"
	default:
		return ""
	}
}

func renderTUILaneBoard(board background.LaneBoard) string {
	var b strings.Builder
	total := len(board.Active) + len(board.Blocked) + len(board.Finished)
	fmt.Fprintf(&b, "Background tasks\n")
	fmt.Fprintf(&b, "  Active   %d\n", len(board.Active))
	fmt.Fprintf(&b, "  Blocked  %d\n", len(board.Blocked))
	fmt.Fprintf(&b, "  Finished %d", len(board.Finished))
	if total == 0 {
		b.WriteString("\n\nNo background tasks.")
		return b.String()
	}
	renderTUILaneSection(&b, "Active", board.Active)
	renderTUILaneSection(&b, "Blocked", board.Blocked)
	renderTUILaneSection(&b, "Finished", board.Finished)
	return b.String()
}

func renderTUILaneSection(b *strings.Builder, title string, entries []background.LaneBoardEntry) {
	if len(entries) == 0 {
		return
	}
	fmt.Fprintf(b, "\n\n%s\n", title)
	for _, entry := range entries {
		kind := strings.TrimSpace(entry.Kind)
		if kind == "" {
			kind = "task"
		}
		status := strings.TrimSpace(entry.Status)
		if status == "" {
			status = entry.Lifecycle.Status
		}
		if status == "" {
			status = "unknown"
		}
		summary := tuiLaneSummary(entry)
		fmt.Fprintf(b, "  %s  %s/%s  %s  %s", entry.TaskID, status, entry.Freshness, kind, summary)
		if sessionID := strings.TrimSpace(entry.SessionID); sessionID != "" {
			fmt.Fprintf(b, "  session=%s", truncateTUILaneText(sessionID, 24))
		}
		if entry.TerminalOutcome != nil && entry.TerminalOutcome.Actionable {
			fmt.Fprintf(b, "  actionable")
		}
		b.WriteByte('\n')
	}
}

func tuiLaneSummary(entry background.LaneBoardEntry) string {
	for _, value := range []string{entry.Prompt, entry.Command, entry.Kind} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return truncateTUILaneText(trimmed, 96)
		}
	}
	return "(task)"
}

func truncateTUILaneText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
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

func (a *App) editTUIComposer(ctx context.Context, value string) (string, error) {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return "", errors.New("no editor configured; set VISUAL or EDITOR")
	}
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return "", errors.New("no editor configured; set VISUAL or EDITOR")
	}
	file, err := os.CreateTemp("", "codog-tui-*.md")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := file.WriteString(value); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, fields[0], append(fields[1:], path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type tuiModeOption struct {
	Label          string
	PermissionMode string
	PlanMode       bool
}

type tuiModeState struct {
	options                []tuiModeOption
	index                  int
	previousPermissionMode string
}

func newTUIModeState(cfg config.Config) *tuiModeState {
	options := []tuiModeOption{
		{Label: "read-only", PermissionMode: "read-only"},
		{Label: "default", PermissionMode: "prompt"},
		{Label: "accept edits", PermissionMode: "workspace-write"},
		{Label: "plan", PermissionMode: "read-only", PlanMode: true},
	}
	state := &tuiModeState{options: options, index: 1}
	mode := strings.TrimSpace(cfg.PermissionMode)
	switch mode {
	case "allow":
		state.options = append(state.options, tuiModeOption{Label: "bypass permissions", PermissionMode: "allow"})
	case "danger-full-access":
		state.options = append(state.options, tuiModeOption{Label: "danger full access", PermissionMode: "danger-full-access"})
	}
	for index, option := range state.options {
		if option.PermissionMode == mode && option.PlanMode == cfg.PlanMode {
			state.index = index
			return state
		}
	}
	return state
}

func (s *tuiModeState) Label() string {
	if s == nil || len(s.options) == 0 {
		return ""
	}
	option := s.options[s.index]
	return option.Label
}

func (s *tuiModeState) Cycle() string {
	if s == nil || len(s.options) == 0 {
		return ""
	}
	s.index = (s.index + 1) % len(s.options)
	return s.Label()
}

func (s *tuiModeState) Select(label string) bool {
	if s == nil {
		return false
	}
	for index, option := range s.options {
		if strings.EqualFold(option.Label, strings.TrimSpace(label)) {
			s.index = index
			return true
		}
	}
	return false
}

func (s *tuiModeState) Sync(cfg config.Config) {
	if s == nil {
		return
	}
	next := newTUIModeState(cfg)
	s.options = next.options
	s.index = next.index
}

func (s *tuiModeState) Apply(cfg *config.Config) {
	if s == nil || cfg == nil || len(s.options) == 0 {
		return
	}
	option := s.options[s.index]
	cfg.PermissionMode = option.PermissionMode
	cfg.PlanMode = option.PlanMode
}

func (a *App) enterTUIPlanMode(state *tuiModeState) {
	if state == nil {
		return
	}
	if !a.Config.PlanMode {
		state.previousPermissionMode = strings.TrimSpace(a.Config.PermissionMode)
	}
	a.Config.PermissionMode = "read-only"
	a.Config.PlanMode = true
	state.Sync(a.Config)
}

func (a *App) exitTUIPlanMode(state *tuiModeState) {
	a.Config.PlanMode = false
	mode := "prompt"
	if state != nil && strings.TrimSpace(state.previousPermissionMode) != "" {
		mode = strings.TrimSpace(state.previousPermissionMode)
	}
	a.Config.PermissionMode = mode
	if state != nil {
		state.previousPermissionMode = ""
		state.Sync(a.Config)
	}
}

func (a *App) selectTUIPermissionMode(ctx context.Context, selected string, modeState *tuiModeState) (tui.RuntimeControlResult, error) {
	if err := ctx.Err(); err != nil {
		return tui.RuntimeControlResult{}, err
	}
	if modeState == nil || !modeState.Select(selected) {
		return tui.RuntimeControlResult{}, fmt.Errorf("unknown interactive permission mode %q", selected)
	}
	previousMode := a.Config.PermissionMode
	previousPlan := a.Config.PlanMode
	modeState.Apply(&a.Config)
	lines := []string{"Mode: " + modeState.Label()}
	if previousMode != a.Config.PermissionMode || previousPlan != a.Config.PlanMode {
		previous := previousMode
		if previousPlan {
			previous = "plan"
		}
		lines = append(lines, "Previous: "+previous)
	}
	return tui.RuntimeControlResult{
		Title:  "Permissions",
		Status: "permissions updated",
		Lines:  lines,
	}, nil
}

type lineAnswerReader struct {
	answers <-chan string
	done    <-chan struct{}
	buffer  string
}

func encodeTUIPermissionResponse(response tui.PermissionResponse) string {
	payload, _ := json.Marshal(tools.PermissionResponse{
		Decision: response.Decision,
		Feedback: response.Feedback,
		Rule:     response.Rule,
	})
	return string(payload) + "\n"
}

func (r *lineAnswerReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	for r.buffer == "" {
		select {
		case answer, ok := <-r.answers:
			if !ok {
				return 0, io.EOF
			}
			r.buffer = answer
		case <-r.done:
			return 0, io.EOF
		}
	}
	n := copy(data, r.buffer)
	r.buffer = r.buffer[n:]
	return n, nil
}

func wrapTUIPermissionEvents(prompter *tools.Prompter, emit func(tui.Entry)) {
	if prompter == nil || emit == nil {
		return
	}
	baseRequest := prompter.OnRequest
	baseDecision := prompter.OnDecision
	pendingRequest := false
	prompter.OnRequest = func(decision tools.PermissionDecision) {
		if baseRequest != nil {
			baseRequest(decision)
		}
		pendingRequest = true
		suggestedRule := tools.SuggestedPermissionRule(decision.ToolName, decision.Input)
		emit(tui.Entry{
			Role: "permission",
			Text: renderTUIPermissionRequest(decision),
			Permission: &tui.PermissionRequest{
				Tool:          decision.ToolName,
				Required:      string(decision.Required),
				Input:         decision.Input,
				Message:       decision.Message,
				SuggestedRule: suggestedRule,
				AllowAlways:   strings.Contains(suggestedRule, "("),
			},
		})
	}
	prompter.OnDecision = func(decision tools.PermissionDecision) {
		if baseDecision != nil {
			baseDecision(decision)
		}
		if pendingRequest {
			pendingRequest = false
			emit(tui.Entry{Role: "permission"})
		}
	}
}

type tuiStreamWriter struct {
	buffer  *bytes.Buffer
	emit    func(tui.Entry)
	emitted bool
}

func (w *tuiStreamWriter) Write(data []byte) (int, error) {
	if w.buffer != nil {
		_, _ = w.buffer.Write(data)
	}
	text := string(data)
	if text != "" && w.emit != nil {
		w.emitted = true
		w.emit(tui.Entry{Role: "assistant", Text: text})
	}
	return len(data), nil
}

func (w *tuiStreamWriter) Emitted() bool {
	return w != nil && w.emitted
}

func (a *App) tuiPromptHistory(sessionID string) []string {
	if !a.promptHistoryEnabled() || a.Sessions == nil {
		return nil
	}
	entries, err := a.Sessions.PromptHistory(sessionID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		text := strings.TrimSpace(entry.Text)
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func renderTUIToolSummary(calls []runloop.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	lines := []string{"Tools"}
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			name = "tool"
		}
		status := "ok"
		if call.IsError {
			status = "error"
		}
		line := fmt.Sprintf("- %s %s", name, status)
		if detail := toolSummaryDetail(call.Output); detail != "" {
			line += ": " + detail
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderTUIToolStart(call runloop.ToolCall) string {
	name := strings.TrimSpace(call.Name)
	if name == "" {
		name = "tool"
	}
	return "Tools\n- " + name + " running"
}

func tuiToolActivity(call runloop.ToolCall, status string) *tui.ToolActivity {
	return &tui.ToolActivity{
		ID:      call.ID,
		Name:    call.Name,
		Input:   call.Input,
		Output:  call.Output,
		Status:  status,
		IsError: call.IsError,
	}
}

func renderTUIPermissionRequest(decision tools.PermissionDecision) string {
	name := strings.TrimSpace(decision.ToolName)
	if name == "" {
		name = "tool"
	}
	required := strings.TrimSpace(string(decision.Required))
	if required == "" {
		required = "permission"
	}
	line := fmt.Sprintf("Permission\n- %s requires %s", name, required)
	if message := strings.TrimSpace(decision.Message); message != "" {
		line += ": " + truncateForReport(message, 180)
	}
	return line
}

func renderTUIQuestionRequest(request tools.UserQuestionRequest) string {
	if len(request.Questions) > 0 {
		lines := []string{"Questions"}
		for questionIndex, question := range request.Questions {
			lines = append(lines, fmt.Sprintf("%d. %s", questionIndex+1, strings.TrimSpace(question.Question)))
			for optionIndex, option := range question.Options {
				lines = append(lines, fmt.Sprintf("  %d. %s - %s", optionIndex+1, option.Label, option.Description))
			}
		}
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	lines := []string{strings.TrimSpace(request.Question)}
	for index, choice := range request.Choices {
		lines = append(lines, fmt.Sprintf("  %d. %s", index+1, choice))
	}
	if defaultAnswer := strings.TrimSpace(request.Default); defaultAnswer != "" {
		lines = append(lines, "Default: "+defaultAnswer)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func tuiQuestions(questions []tools.UserQuestion) []tui.Question {
	out := make([]tui.Question, 0, len(questions))
	for _, question := range questions {
		options := make([]tui.QuestionOption, 0, len(question.Options))
		for _, option := range question.Options {
			options = append(options, tui.QuestionOption{
				Label:       option.Label,
				Description: option.Description,
				Preview:     option.Preview,
			})
		}
		out = append(out, tui.Question{
			Question:    question.Question,
			Header:      question.Header,
			Options:     options,
			MultiSelect: question.MultiSelect,
		})
	}
	return out
}

func toolSummaryDetail(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	if detail := structuredToolSummaryDetail(output); detail != "" {
		return detail
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return truncateForReport(line, 180)
		}
	}
	return ""
}

func structuredToolSummaryDetail(output string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil || len(payload) == 0 {
		return ""
	}
	for _, key := range []string{"stdout", "stderr", "output", "message", "error"} {
		if value := cleanToolSummaryValue(payload[key]); value != "" {
			return value
		}
	}
	kind := cleanToolSummaryValue(payload["kind"])
	if kind == "" {
		kind = cleanToolSummaryValue(payload["type"])
	}
	if bytesValue, ok := payload["bytes"]; ok {
		if kind != "" {
			return fmt.Sprintf("%s %v bytes", kind, bytesValue)
		}
		return fmt.Sprintf("%v bytes", bytesValue)
	}
	return ""
}

func cleanToolSummaryValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return truncateForReport(line, 180)
		}
	}
	return ""
}

func (a *App) finishREPL(ctx context.Context, sess *session.Session, loopErr error) error {
	reason := "exit"
	if loopErr != nil {
		reason = "error"
	}
	if err := a.runSessionEndHook(ctx, sess, reason); err != nil {
		if loopErr != nil {
			if a.Err != nil {
				fmt.Fprintf(a.Err, "session end hook error: %v\n", err)
			}
			return loopErr
		}
		return err
	}
	return loopErr
}

func (a *App) renderDeepLinkBanner(overrides config.FlagOverrides) {
	banner := buildDeepLinkBanner(a.Workspace, overrides, time.Now())
	if banner == "" || a.Err == nil {
		return
	}
	fmt.Fprintln(a.Err, banner)
}

func buildDeepLinkBanner(workspace string, overrides config.FlagOverrides, now time.Time) string {
	if !overrides.DeepLinkOrigin && strings.TrimSpace(overrides.Prefill) == "" {
		return ""
	}
	if !overrides.DeepLinkOrigin {
		return "Warning: launched with a pre-filled prompt - review it before pressing Enter."
	}
	lines := []string{"Warning: this session was opened by an external deep link in " + displayDeepLinkPath(workspace)}
	if repo := strings.TrimSpace(overrides.DeepLinkRepo); repo != "" {
		age := "never"
		stale := true
		if overrides.DeepLinkLastFetchMS > 0 {
			fetched := time.UnixMilli(overrides.DeepLinkLastFetchMS)
			age = relativeTimeAgo(fetched, now)
			stale = now.Sub(fetched) > 7*24*time.Hour
		}
		line := "Resolved " + repo + " from local clones; last fetched " + age
		if stale {
			line += " - project instructions may be stale"
		}
		lines = append(lines, line)
	}
	if length := len(overrides.Prefill); length > 0 {
		if length > 1000 {
			lines = append(lines, fmt.Sprintf("The prompt below (%d chars) was supplied by the link - scroll to review the entire prompt before pressing Enter.", length))
		} else {
			lines = append(lines, "The prompt below was supplied by the link - review carefully before pressing Enter.")
		}
	}
	return strings.Join(lines, "\n")
}

func displayDeepLinkPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "."
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func relativeTimeAgo(then time.Time, now time.Time) string {
	if then.IsZero() {
		return "never"
	}
	if now.IsZero() {
		now = time.Now()
	}
	if then.After(now) {
		return "just now"
	}
	delta := now.Sub(then)
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm ago", int(delta/time.Minute))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(delta/(24*time.Hour)))
	}
}

func (a *App) replScanner(ctx context.Context, sess *session.Session) error {
	scanner := bufio.NewScanner(a.In)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for {
		fmt.Fprint(a.Err, "codog> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if isREPLExitCommand(line) {
			return nil
		}
		if isREPLHelpCommand(line) {
			a.renderSlashHelp(a.Err)
			continue
		}
		if a.handleSlash(ctx, line, sess) {
			continue
		}
		if err := a.runSessionTurn(ctx, "repl", sess, line, "idle"); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			continue
		}
	}
	return scanner.Err()
}

func (a *App) replReadline(ctx context.Context, sess *session.Session, rl *readline.Instance, prefill string) error {
	for {
		var line string
		var err error
		if strings.TrimSpace(prefill) != "" {
			line, err = rl.ReadlineWithDefault(prefill)
			prefill = ""
		} else {
			line, err = rl.Readline()
		}
		if errors.Is(err, readline.ErrInterrupt) {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if a.promptHistoryEnabled() {
			_ = rl.SaveHistory(line)
		}
		if isREPLExitCommand(line) {
			return nil
		}
		if isREPLHelpCommand(line) {
			a.renderSlashHelp(a.Err)
			continue
		}
		if a.handleSlash(ctx, line, sess) {
			if strings.HasPrefix(line, "/vim") {
				rl.SetVimMode(a.readlineVimMode())
			}
			continue
		}
		if err := a.runSessionTurn(ctx, "repl", sess, line, "idle"); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			continue
		}
	}
}

func isREPLExitCommand(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "/exit", "/quit", "exit", "quit":
		return true
	default:
		return false
	}
}

func isREPLHelpCommand(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "/help", "help", "?":
		return true
	default:
		return false
	}
}

func (a *App) newLineReader(activeSessionID string) (*readline.Instance, bool, error) {
	input, ok := terminalInput(a.In)
	if !ok {
		return nil, false, nil
	}
	cfg := &readline.Config{
		Prompt:                 "codog> ",
		Stdin:                  input,
		Stdout:                 a.Err,
		Stderr:                 a.Err,
		VimMode:                a.readlineVimMode(),
		AutoComplete:           slashCompleter{candidates: a.slashCompletionCandidates(activeSessionID)},
		HistorySearchFold:      true,
		DisableAutoSaveHistory: true,
		FuncIsTerminal:         func() bool { return true },
	}
	if historyFile := a.replHistoryFile(); historyFile != "" {
		cfg.HistoryFile = historyFile
	}
	rl, err := readline.NewEx(cfg)
	if err != nil {
		return nil, false, err
	}
	return rl, true, nil
}

func (a *App) readlineVimMode() bool {
	return editorModeIsVim(a.Config.EditorMode)
}

func (a *App) replHistoryFile() string {
	if !a.promptHistoryEnabled() {
		return ""
	}
	if strings.TrimSpace(a.Config.ConfigHome) == "" {
		return ""
	}
	dir := filepath.Join(a.Config.ConfigHome, "history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(dir, "repl.txt")
}

func terminalInput(input io.Reader) (*os.File, bool) {
	file, ok := input.(*os.File)
	if !ok {
		return nil, false
	}
	return file, term.IsTerminal(file.Fd())
}

type slashCompleter struct {
	candidates []string
}

func (c slashCompleter) Do(line []rune, pos int) ([][]rune, int) {
	if pos < 0 || pos > len(line) {
		return nil, 0
	}
	prefix := strings.Trim(string(line[:pos]), "\r\n\t")
	if !strings.HasPrefix(prefix, "/") {
		return nil, 0
	}
	matches := slash.FilterCandidates(prefix, c.candidates)
	out := make([][]rune, 0, len(matches))
	for _, candidate := range matches {
		suffix := strings.TrimPrefix(candidate, prefix)
		out = append(out, []rune(suffix))
	}
	return out, len([]rune(prefix))
}

func (a *App) handleSlash(ctx context.Context, line string, sess *session.Session) bool {
	if !strings.HasPrefix(line, "/") {
		return false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return true
	}
	command := fields[0]
	if mapped := slashCommandName(command); mapped != "" {
		command = slashSwitchName(mapped)
	}
	handlers := []interactiveSlashHandler{
		a.handleSystemSlash,
		a.handleRemoteSystemSlash,
		a.handleWorkspaceSlash,
		a.handleReviewSlash,
		a.handleWorkspaceStateSlash,
		a.handlePreferencesSlash,
		a.handleInterfacePreferenceSlash,
		a.handleUsageSlash,
		a.handleLimitSlash,
		a.handlePlanConfigSlash,
		a.handleRuntimeConfigSlash,
		a.handleEditingSlash,
		a.handleDevelopmentSlash,
		a.handleBuildSlash,
		a.handleCodeIntelSlash,
		a.handleSharingSlash,
		a.handleExtensionSlash,
		a.handleAgentExtensionSlash,
		a.handleIntegrationSlash,
		a.handlePluginSlash,
		a.handleAuthSessionsSlash,
	}
	for _, handle := range handlers {
		if handle(ctx, command, fields, sess) {
			return true
		}
	}
	if a.handleCustomSlash(ctx, line, sess) {
		return true
	}
	if a.handleSkillSlash(ctx, line, sess) {
		return true
	}
	if _, ok := slash.Lookup(fields[0]); !ok {
		writeUnknownSlashCommand(a.Err, fields[0], a.customSlashCompletionCandidates())
	}
	return true
}

type interactiveSlashHandler func(context.Context, string, []string, *session.Session) bool

func (a *App) handleSystemSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/help":
		a.renderSlashHelp(a.Err)
	case "/status":
		if err := a.Status(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/statusline":
		if err := a.Statusline(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/setup":
		if err := a.Setup(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/onboarding":
		if err := a.Onboarding(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/terminal-setup":
		if err := a.TerminalSetup(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/remote-env":
		if err := a.RemoteEnv(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/api":
		if err := a.API(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/remote":
		if len(fields) > 1 && strings.EqualFold(fields[1], "serve") {
			if err := a.Remote(fields[1:]); err != nil {
				fmt.Fprintln(a.Err, "error:", err)
			}
		} else if err := a.RemoteSetup(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/remote-setup":
		if err := a.RemoteSetup(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleRemoteSystemSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/bridge":
		if err := a.Bridge(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/bridge-kick":
		if err := a.BridgeKick(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/desktop":
		if err := a.Desktop(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/mobile":
		if err := a.Mobile(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/ios", "/android":
		platform := strings.TrimPrefix(command, "/")
		args := append([]string{platform}, fields[1:]...)
		if err := a.Mobile(args, config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/context":
		if err := a.Context(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/ctx_viz":
		if err := a.ContextViz(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/sandbox":
		if err := a.Sandbox(); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/sandbox-toggle":
		if err := a.SandboxToggle(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/heapdump":
		if err := a.HeapDump(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleWorkspaceSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/version":
		if err := renderVersion(a.Out, a.Workspace, nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/init":
		if err := a.Init(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/init-verifiers":
		if err := a.InitVerifiers(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/state":
		if err := a.State(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/memory":
		if err := a.Memory(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/project":
		if err := a.Project(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/env":
		if err := a.Env(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/files":
		if err := a.Files(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/scope":
		if err := a.Scope(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/search":
		if err := a.Search(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/security-review":
		if err := a.SecurityReview(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/bughunter":
		if err := a.Bughunter(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleReviewSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/review", "/ultrareview":
		if err := a.Review(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/reviewremote":
		if err := a.ReviewRemote(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/feedback":
		if err := a.Feedback(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/pr":
		if err := a.PullRequestDraft(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/commit-push-pr":
		if err := a.CommitPushPR(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/autofix-pr":
		if err := a.AutofixPR(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/pr-comments":
		if err := a.PRComments(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/install-github-app":
		if err := a.InstallGitHubApp(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/install-slack-app":
		if err := a.InstallSlackApp(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/stickers":
		if err := a.Stickers(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/passes":
		if err := a.Passes(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/issue":
		if err := a.IssueDraft(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleWorkspaceStateSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/focus":
		if err := a.Focus(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/unfocus":
		if err := a.Unfocus(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/add-dir":
		if err := a.AddDir(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/validation":
		if err := a.Validation(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/workspace":
		if err := a.WorkspaceCommand(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handlePreferencesSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/output-style":
		if err := a.OutputStyle(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/reset":
		if err := a.Reset(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/language":
		if err := a.Language(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/theme":
		if err := a.Theme(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/vim":
		if err := a.Vim(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/effort":
		if err := a.Effort(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/reasoning":
		if err := a.Reasoning(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/fast":
		if err := a.Fast(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleInterfacePreferenceSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/voice":
		if err := a.Voice(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/listen":
		if err := a.Voice(append([]string{"listen"}, fields[1:]...)); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/speak":
		if err := a.Speak(ctx, fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/chrome":
		if err := a.Chrome(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/privacy-settings":
		if err := a.PrivacySettings(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/profile":
		if err := a.Profile(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/telemetry":
		if err := a.Telemetry(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/keybindings":
		if err := a.Keybindings(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/notifications":
		if err := a.Notifications(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleUsageSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/cost", "/tokens", "/stats":
		_ = a.UsageOverview(strings.TrimPrefix(command, "/"), fields[1:], config.FlagOverrides{SessionID: sess.ID})
	case "/cache":
		if err := a.Cache(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/break-cache":
		if err := a.BreakCache(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			break
		}
		next, err := a.Sessions.Open(sess.ID)
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			break
		}
		*sess = *next
	case "/usage":
		if err := a.Usage(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/metrics":
		if err := a.Metrics(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/insights":
		if err := a.Insights(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/perf-issue":
		if err := a.PerfIssue(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleLimitSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/think-back":
		if err := a.ThinkBack(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/extra-usage":
		if err := a.ExtraUsage(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/rate-limit":
		if err := a.RateLimit(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/rate-limit-options":
		if err := a.RateLimitOptions(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/ant-trace":
		if err := a.AntTrace(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/mock-limits":
		if err := a.MockLimits(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/mock-parity", "/self-test":
		defaultFormat := "text"
		if command == "/self-test" {
			defaultFormat = "json"
		}
		if err := runMockParityCommand(ctx, a.Out, fields[1:], "", defaultFormat); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/reset-limits":
		if err := a.ResetLimits(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handlePlanConfigSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/plan", "/ultraplan":
		if err := a.Plan(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/exit-plan":
		if err := a.Plan(append([]string{"exit"}, fields[1:]...)); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/config":
		a.handleConfigSlash(fields[1:])
	case "/api-key":
		if err := a.APIKey(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/model":
		a.handleModelSlash(fields[1:])
	case "/models":
		if err := a.Models(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/advisor":
		if err := a.Advisor(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/budget":
		if err := a.Budget(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleRuntimeConfigSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/max-tokens":
		a.handleMaxTokensSlash(fields[1:])
	case "/temperature":
		if err := a.Temperature(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/max-turns":
		a.handleMaxTurnsSlash(fields[1:])
	case "/system-prompt":
		fmt.Fprintln(a.Out, a.systemPrompt())
	case "/tool-details":
		a.handleToolDetailsSlash(fields[1:])
	case "/debug-tool-call":
		if err := a.DebugToolCall(ctx, fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/permissions":
		a.handlePermissionsSlash(fields[1:])
	case "/allowed-tools":
		a.handleAllowedToolsSlash(fields[1:])
	case "/doctor":
		if err := a.Doctor(nil); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleEditingSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/compact":
		if err := a.Compact(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		} else if current, err := a.Sessions.Open(sess.ID); err == nil {
			*sess = *current
		}
	case "/undo":
		if err := a.Undo(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/diff":
		a.handleDiffSlash(fields[1:])
	case "/commit":
		a.handleCommitSlash(fields[1:])
	case "/branch":
		if err := a.Branch(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/branch-lock":
		if err := a.BranchLock(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/stale-base":
		if err := a.StaleBase(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/green-contract":
		if err := a.GreenContract(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/g004-conformance":
		if err := a.G004Conformance(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/report-schema":
		if err := a.ReportSchema(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleDevelopmentSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/trust":
		if err := a.Trust(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/tag":
		if err := a.sessionTagCommand(fields[1:], config.FlagOverrides{Resume: sess.ID}, sess); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/log":
		a.handleLogSlash(fields[1:])
	case "/changelog":
		a.handleChangelogSlash(fields[1:])
	case "/release-notes":
		if err := a.ReleaseNotes(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/blame":
		a.handleBlameSlash(fields[1:])
	default:
		return false
	}
	return true
}

func (a *App) handleBuildSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/stash":
		if err := a.Stash(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/git":
		if err := a.Git(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/run":
		if err := a.RunCommand(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/node", "/python":
		language := strings.TrimPrefix(command, "/")
		if err := a.LanguageCommand(ctx, language, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/test":
		if err := a.ProjectCommand(ctx, "test", fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/build":
		if err := a.ProjectCommand(ctx, "build", fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/lint":
		if err := a.ProjectCommand(ctx, "lint", fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleCodeIntelSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/symbols":
		if err := a.Symbols(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/diagnostics":
		if err := a.Diagnostics(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/map":
		if err := a.Map(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/references":
		if err := a.References(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/definition":
		if err := a.Definition(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/hover":
		if err := a.Hover(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/teleport":
		if err := a.Teleport(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/completion":
		if err := a.Completion(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/format":
		if err := a.Format(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/code-intel":
		if err := a.CodeIntel(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/notebook-read":
		if err := a.CodeIntel(append([]string{"notebook-read"}, fields[1:]...)); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/notebook-edit":
		if err := a.CodeIntel(append([]string{"notebook-edit"}, fields[1:]...)); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleSharingSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/export":
		a.handleExportSlash(fields[1:], sess)
	case "/share":
		if err := a.Share(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/copy":
		if err := a.Copy(ctx, fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/paste":
		a.handlePasteSlash(ctx, fields[1:], sess)
	case "/pin":
		if err := a.Pin(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/unpin":
		if err := a.Unpin(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/history":
		a.handleHistorySlash(fields[1:], sess)
	case "/summary":
		if err := a.Summary(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleExtensionSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/generatesessionname":
		report, format, err := a.generateSessionNameReport(fields[1:], config.FlagOverrides{SessionID: sess.ID})
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return true
		}
		if format == "json" {
			if err := sessionname.RenderJSON(a.Out, report); err != nil {
				fmt.Fprintln(a.Err, "error:", err)
			}
		} else {
			sessionname.RenderText(a.Out, report)
		}
		if report.Renamed && report.NewID != "" {
			next, err := a.Sessions.Open(report.NewID)
			if err != nil {
				fmt.Fprintln(a.Err, "error:", err)
				return true
			}
			*sess = *next
		}
	case "/rename":
		if len(fields) < 2 {
			fmt.Fprintln(a.Err, "usage: /rename NEW_ID")
			return true
		}
		result, err := a.Sessions.Rename(sess.ID, fields[1])
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return true
		}
		next, err := a.Sessions.Open(result.NewID)
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return true
		}
		*sess = *next
		fmt.Fprintf(a.Err, "session renamed: %s -> %s\n", result.OldID, result.NewID)
	case "/todos":
		if err := a.Todos(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/skill", "/skills":
		if err := a.Skills(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleAgentExtensionSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/commands":
		if err := a.Commands(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/templates":
		if err := a.Templates(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/hooks":
		if err := a.Hooks(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/mcp":
		if err := a.MCP(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/capabilities":
		if err := a.Capabilities(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleIntegrationSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/prefetch":
		if err := a.Prefetch(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/acp":
		if err := a.ACP(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/attach":
		prompt, attachments, err := parseAttachSlashArgs(fields[1:])
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			break
		}
		if err := a.runSessionTurnWithOptions(ctx, "repl", sess, prompt, "idle", turnOptions{Attachments: attachments}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/brief":
		if err := a.Brief(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/btw":
		if err := a.BTW(ctx, fields[1:], config.FlagOverrides{}, sess); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/ide":
		if err := a.IDE(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/upgrade":
		if err := a.Upgrade(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/install":
		if err := a.Install(ctx, fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handlePluginSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/agents":
		if err := a.AgentsWithOverrides(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/subagent":
		if err := a.Subagent(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/background":
		if err := a.BackgroundWithOverrides(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/tasks", "/bashes":
		if err := a.BackgroundWithOverrides(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/cron":
		if err := a.Cron(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/team":
		if err := a.Team(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/plugin", "/plugins", "/marketplace":
		if err := a.Marketplace(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/reload-plugins":
		if err := a.ReloadPlugins(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	default:
		return false
	}
	return true
}

func (a *App) handleAuthSessionsSlash(ctx context.Context, command string, fields []string, sess *session.Session) bool {
	switch command {
	case "/providers":
		args := fields[1:]
		if len(args) == 0 {
			args = []string{"status"}
		}
		if err := a.Providers(args); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/oauth":
		if err := a.OAuth(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/login":
		if err := a.Login(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/oauth-refresh":
		if err := a.OAuthRefresh(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/logout":
		if err := a.Logout(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/sessions":
		a.handleSessionSlash(fields[1:], sess)
	case "/bookmarks":
		if err := a.Bookmarks(fields[1:], config.FlagOverrides{SessionID: sess.ID}); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/backfill-sessions":
		if err := a.BackfillSessions(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/import":
		if err := a.ClaudeImport(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/visualize":
		if err := a.Visualize(fields[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "/clear":
		a.handleClearSlash(ctx, fields[1:], sess)
	case "/conversation":
		a.handleConversationSlash(ctx, fields[1:], sess)
	case "/resume":
		a.handleResumeSlash(ctx, fields[1:], sess)
	case "/rewind":
		a.handleRewindSlash(fields[1:], sess)
	default:
		return false
	}
	return true
}

func (a *App) handleSkillSlash(ctx context.Context, line string, sess *session.Session) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	name := strings.TrimPrefix(fields[0], "/")
	name = strings.ReplaceAll(name, "/", ":")
	skill, err := a.findRuntimeSkill(name)
	if err != nil {
		if errors.Is(err, skills.ErrNotFound) {
			return false
		}
		fmt.Fprintln(a.Err, "error:", err)
		return true
	}
	if !skill.UserInvocable {
		return false
	}
	args := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	rendered := skills.RenderInvocationWithSession(skill, args, sess.ID)
	if err := a.runSessionTurnWithOptions(ctx, "repl", sess, rendered, "idle", turnOptions{Skill: &skill}); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
	return true
}

func (a *App) handleCustomSlash(ctx context.Context, line string, sess *session.Session) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	command, err := a.findRuntimeCustomCommand(fields[0])
	if err != nil {
		if errors.Is(err, customcommands.ErrNotFound) {
			return false
		}
		fmt.Fprintln(a.Err, "error:", err)
		return true
	}
	args := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	rendered := customcommands.RenderWithSession(command, args, sess.ID)
	if strings.TrimSpace(rendered.Rendered) == "" {
		fmt.Fprintf(a.Err, "custom command %s rendered an empty prompt\n", fields[0])
		return true
	}
	if err := a.runSessionTurnWithOptions(ctx, "repl", sess, rendered.Rendered, "idle", turnOptions{AllowedTools: command.AllowedTools}); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
	return true
}

func (a *App) handleClearSlash(ctx context.Context, args []string, sess *session.Session) {
	for _, arg := range args {
		if arg != "--confirm" {
			fmt.Fprintln(a.Err, "usage: /clear [--confirm]")
			return
		}
	}
	next, err := a.Sessions.Open("")
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.ensureSessionIdentity(next, "repl", "", ""); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.runSessionEndHook(ctx, sess, "clear"); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.runSessionStartHook(ctx, next, "clear"); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	*sess = *next
	a.dynamicSkillPaths = nil
	a.writeWorkerState("repl", "idle", sess, "")
	fmt.Fprintf(a.Err, "session cleared: %s\n", sess.ID)
}

func (a *App) handleConversationSlash(ctx context.Context, args []string, sess *session.Session) {
	overrides := config.FlagOverrides{SessionID: sess.ID}
	req, err := parseConversationArgs(args, overrides)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.Action == "clear" && req.Confirm {
		a.handleClearSlash(ctx, []string{"--confirm"}, sess)
		return
	}
	if err := a.Conversation(args, overrides); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleResumeSlash(ctx context.Context, args []string, sess *session.Session) {
	id := "latest"
	if len(args) > 0 {
		id = strings.TrimSpace(strings.Join(args, " "))
	}
	var next *session.Session
	var err error
	if session.IsSessionReferenceAlias(id) && sess != nil {
		next, err = a.Sessions.LatestSessionExcluding(sess.ID)
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
	} else {
		next, err = a.openExistingResumeSession(id)
	}
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.restoreTodosFromSession(next); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.runSessionEndHook(ctx, sess, "resume"); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := a.runSessionStartHook(ctx, next, "resume"); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	*sess = *next
	a.dynamicSkillPaths = nil
	a.writeWorkerState("repl", "idle", sess, "")
	fmt.Fprintf(a.Err, "session resumed: %s\n", sess.ID)
}

func (a *App) openExistingResumeSession(reference string) (*session.Session, error) {
	if a.Sessions == nil {
		return nil, errors.New("session store is not configured")
	}
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, errors.New("session id is required")
	}
	opened, err := a.Sessions.OpenExisting(reference)
	if err == nil || !errors.Is(err, session.ErrSessionNotFound) {
		return opened, err
	}
	sessions, listErr := a.Sessions.List()
	if listErr != nil {
		return nil, listErr
	}
	matches := make([]session.Session, 0, 1)
	for _, candidate := range sessions {
		if strings.EqualFold(strings.TrimSpace(candidate.Identity.Title), reference) {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 0:
		return nil, err
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("found %d sessions matching %q; use /resume to pick a specific session", len(matches), reference)
	}
}

func (a *App) handleRewindSlash(args []string, sess *session.Session) {
	if a.Sessions == nil {
		fmt.Fprintln(a.Err, "error: session store is not configured")
		return
	}
	defaultSession := ""
	if sess != nil {
		defaultSession = sess.ID
	}
	req, err := parseRewindArgs(args, config.FlagOverrides{}, defaultSession)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	result, err := a.Sessions.Rewind(req.SessionID, req.Messages)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	next, err := a.Sessions.Open(result.SessionID)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if sess != nil {
		*sess = *next
		a.writeWorkerState("repl", "idle", sess, "")
	}
	a.renderRewindReport(req.Format, result)
}

func (a *App) handleHistorySlash(args []string, sess *session.Session) {
	overrides := config.FlagOverrides{}
	if sess != nil {
		overrides.SessionID = sess.ID
	}
	if err := a.History(args, overrides); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleConfigSlash(args []string) {
	payload, err := a.runtimeConfigPayload(args)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
}

type advisorRequest struct {
	Action string
	Model  string
	Format string
	Target string
	Path   string
}

type advisorReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	Model     string `json:"model,omitempty"`
	MainModel string `json:"main_model,omitempty"`
	Path      string `json:"path,omitempty"`
	Message   string `json:"message,omitempty"`
}

type modelRequest struct {
	Format string
	Action string
	Model  string
	Target string
	Path   string
}

type modelsRequest struct {
	Format string
	Action string
	Model  string
	Target string
	Path   string
}

type modelReport struct {
	Kind           string `json:"kind"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Model          string `json:"model"`
	Previous       string `json:"previous,omitempty"`
	RequestedModel string `json:"requested_model,omitempty"`
	Path           string `json:"path,omitempty"`
	Cleared        bool   `json:"cleared,omitempty"`
}

type modelAliasReport struct {
	Name                string `json:"name"`
	Model               string `json:"model"`
	Provider            string `json:"provider,omitempty"`
	MaxOutputTokens     int    `json:"max_output_tokens,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
}

type modelRouteReport struct {
	Prefix         string `json:"prefix"`
	Provider       string `json:"provider"`
	WireProtocol   string `json:"wire_protocol"`
	AuthEnv        string `json:"auth_env"`
	BaseURLEnv     string `json:"base_url_env"`
	DefaultBaseURL string `json:"default_base_url"`
	Description    string `json:"description"`
}

type modelsReport struct {
	Kind                    string             `json:"kind"`
	Action                  string             `json:"action"`
	Status                  string             `json:"status"`
	DefaultModel            string             `json:"default_model"`
	Aliases                 []modelAliasReport `json:"aliases"`
	Routes                  []modelRouteReport `json:"routes"`
	ConfiguredModel         string             `json:"configured_model"`
	ResolvedConfiguredModel string             `json:"resolved_configured_model"`
	ModelCommand            string             `json:"model_command"`
	ConfigCommand           string             `json:"config_command"`
	LocalOnly               bool               `json:"local_only"`
	RequiresCredentials     bool               `json:"requires_credentials"`
	RequiresProviderRequest bool               `json:"requires_provider_request"`
	Message                 string             `json:"message"`
}

type modelAliasesInventoryReport struct {
	Kind                    string             `json:"kind"`
	Action                  string             `json:"action"`
	Status                  string             `json:"status"`
	Count                   int                `json:"count"`
	Aliases                 []modelAliasReport `json:"aliases"`
	LocalOnly               bool               `json:"local_only"`
	RequiresProviderRequest bool               `json:"requires_provider_request"`
	Message                 string             `json:"message"`
}

type modelRoutesInventoryReport struct {
	Kind                    string             `json:"kind"`
	Action                  string             `json:"action"`
	Status                  string             `json:"status"`
	Count                   int                `json:"count"`
	Routes                  []modelRouteReport `json:"routes"`
	LocalOnly               bool               `json:"local_only"`
	RequiresProviderRequest bool               `json:"requires_provider_request"`
	Message                 string             `json:"message"`
}

type modelSearchReport struct {
	Kind                    string              `json:"kind"`
	Action                  string              `json:"action"`
	Status                  string              `json:"status"`
	Query                   string              `json:"query"`
	MatchCount              int                 `json:"match_count"`
	Aliases                 []modelAliasReport  `json:"aliases,omitempty"`
	Routes                  []modelRouteReport  `json:"routes,omitempty"`
	Models                  []modelDetailReport `json:"models,omitempty"`
	LocalOnly               bool                `json:"local_only"`
	RequiresProviderRequest bool                `json:"requires_provider_request"`
	Message                 string              `json:"message"`
}

type modelDetailReport struct {
	Kind                                  string                     `json:"kind"`
	Action                                string                     `json:"action"`
	Status                                string                     `json:"status"`
	RequestedModel                        string                     `json:"requested_model"`
	ResolvedModel                         string                     `json:"resolved_model"`
	Alias                                 string                     `json:"alias,omitempty"`
	Provider                              string                     `json:"provider"`
	WireProtocol                          string                     `json:"wire_protocol"`
	BaseURL                               string                     `json:"base_url"`
	WireModel                             string                     `json:"wire_model"`
	AuthEnv                               string                     `json:"auth_env"`
	BaseURLEnv                            string                     `json:"base_url_env"`
	MaxOutputTokens                       int                        `json:"max_output_tokens,omitempty"`
	ContextWindowTokens                   int                        `json:"context_window_tokens,omitempty"`
	OpenAICompatible                      bool                       `json:"openai_compatible"`
	ReasoningModel                        bool                       `json:"reasoning_model"`
	UsesMaxCompletionTokens               bool                       `json:"uses_max_completion_tokens"`
	StripsTuningParams                    bool                       `json:"strips_tuning_params"`
	SupportsStreamUsage                   bool                       `json:"supports_stream_usage"`
	HonorsProxyEnv                        bool                       `json:"honors_proxy_env"`
	SupportsExtraBodyParams               bool                       `json:"supports_extra_body_params"`
	ExtraBodyConfigured                   bool                       `json:"extra_body_configured"`
	ExtraBodyKeys                         []string                   `json:"extra_body_keys,omitempty"`
	ExtraBodyForwardedKeys                []string                   `json:"extra_body_forwarded_keys,omitempty"`
	ExtraBodyIgnoredKeys                  []string                   `json:"extra_body_ignored_keys,omitempty"`
	PreservesSlashModelIDsOnCustomBaseURL bool                       `json:"preserves_slash_model_ids_on_custom_base_url,omitempty"`
	ProtectedExtraBodyKeys                []string                   `json:"protected_extra_body_keys,omitempty"`
	Diagnostics                           []providerDiagnosticReport `json:"diagnostics,omitempty"`
	RejectsToolResultIsErrorField         bool                       `json:"rejects_tool_result_is_error_field"`
	RequiresReasoningContentHistory       bool                       `json:"requires_reasoning_content_history"`
	LocalOnly                             bool                       `json:"local_only"`
	RequiresProviderRequest               bool                       `json:"requires_provider_request"`
	Message                               string                     `json:"message"`
}

func (a *App) Model(args []string) error {
	if modelHelpRequested(args) {
		return renderCommandHelpTopic(a.Out, "models", modelHelpArgsWithoutHelp(args), "text")
	}
	req, err := parseModelArgs(args)
	if err != nil {
		return err
	}
	if req.Action == "clear" {
		report, err := a.clearModelRequest(req)
		if err != nil {
			return err
		}
		return renderModelReport(a.Out, report, req.Format)
	}
	report, err := a.applyModelRequest(req)
	if err != nil {
		return err
	}
	return renderModelReport(a.Out, report, req.Format)
}

func (a *App) Models(args []string) error {
	req, err := parseModelsArgs(args)
	if err != nil {
		return err
	}
	if req.Action == "help" {
		return renderCommandHelpTopic(a.Out, "models", modelHelpArgsWithoutHelp(args), req.Format)
	}
	switch req.Action {
	case "list":
		return renderModelsReport(a.Out, a.buildModelsReport(), req.Format)
	case "aliases":
		return renderModelAliasesInventoryReport(a.Out, modelAliasesInventoryReport{
			Kind:                    "models",
			Action:                  "aliases",
			Status:                  "ok",
			Aliases:                 modelAliases(),
			LocalOnly:               true,
			RequiresProviderRequest: false,
			Message:                 "Built-in aliases are resolved locally before provider routing.",
		}, req.Format)
	case "routes":
		return renderModelRoutesInventoryReport(a.Out, modelRoutesInventoryReport{
			Kind:                    "models",
			Action:                  "routes",
			Status:                  "ok",
			Routes:                  modelRoutes(),
			LocalOnly:               true,
			RequiresProviderRequest: false,
			Message:                 "Routes are selected from the resolved model name without making a provider request.",
		}, req.Format)
	case "search":
		if strings.TrimSpace(req.Model) == "" {
			return renderMissingActionArgument(a.Out, "models", "search", "query", "models search requires a query", "Usage: codog models search QUERY [--output-format text|json].", req.Format)
		}
		return renderModelSearchReport(a.Out, a.buildModelSearchReport(req.Model), req.Format)
	case "show":
		return renderModelDetailReport(a.Out, a.buildModelDetailReport(req.Model), req.Format)
	case "set":
		if strings.TrimSpace(req.Model) == "" {
			return renderMissingActionArgument(a.Out, "models", "set", "model", "models set requires a model", "Usage: codog models set MODEL [--target user|project|local] [--path PATH] [--output-format text|json].", req.Format)
		}
		report, err := a.applyModelRequest(modelRequest{Format: req.Format, Model: req.Model, Target: req.Target, Path: req.Path})
		if err != nil {
			return err
		}
		return renderModelReport(a.Out, report, req.Format)
	case "clear":
		report, err := a.clearModelRequest(modelRequest{Format: req.Format, Target: req.Target, Path: req.Path})
		if err != nil {
			return err
		}
		return renderModelReport(a.Out, report, req.Format)
	default:
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "models",
			Action:    req.Action,
			Status:    "error",
			ErrorKind: "unsupported_models_action",
			Message:   fmt.Sprintf("unsupported models action %q", req.Action),
			Hint:      unknownModelsActionHint(req.Action),
		}, req.Format)
	}
}

func (a *App) applyModelRequest(req modelRequest) (modelReport, error) {
	previous := a.Config.Model
	action := "show"
	path := ""
	if strings.TrimSpace(req.Model) != "" {
		var err error
		path, err = a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return modelReport{}, err
		}
		if _, err := config.SetFileValue(path, "model", req.Model); err != nil {
			return modelReport{}, err
		}
		action = "set"
		a.Config.Model = req.Model
	}
	report := modelReport{
		Kind:     "model",
		Action:   action,
		Status:   "ok",
		Model:    a.Config.Model,
		Previous: previous,
		Path:     path,
	}
	if action == "show" {
		report.Previous = ""
	}
	return report, nil
}

func (a *App) clearModelRequest(req modelRequest) (modelReport, error) {
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return modelReport{}, err
	}
	if _, err := config.UnsetFileValue(path, "model"); err != nil {
		return modelReport{}, err
	}
	previous := a.Config.Model
	a.Config.Model = config.DefaultModel
	return modelReport{
		Kind:     "model",
		Action:   "clear",
		Status:   "ok",
		Model:    a.Config.Model,
		Previous: previous,
		Path:     path,
		Cleared:  true,
	}, nil
}

func (a *App) buildModelsReport() modelsReport {
	report := modelsReport{
		Kind:                    "models",
		Action:                  "list",
		Status:                  "ok",
		DefaultModel:            config.DefaultModel,
		Aliases:                 modelAliases(),
		Routes:                  modelRoutes(),
		ConfiguredModel:         a.Config.Model,
		ResolvedConfiguredModel: resolveModelAlias(a.Config.Model),
		ModelCommand:            "codog --model MODEL prompt \"...\"",
		ConfigCommand:           "codog model MODEL",
		LocalOnly:               true,
		RequiresCredentials:     false,
		RequiresProviderRequest: false,
		Message:                 "Use --model MODEL for one run or `codog model MODEL` to change the current configured model.",
	}
	if strings.TrimSpace(report.ConfiguredModel) == "" {
		report.ConfiguredModel = config.DefaultModel
		report.ResolvedConfiguredModel = config.DefaultModel
	}
	return report
}

func (a *App) buildModelSearchReport(query string) modelSearchReport {
	query = strings.TrimSpace(query)
	aliases := matchingModelAliases(query, modelAliases())
	routes := matchingModelRoutes(query, modelRoutes())
	models := matchingModelDetails(a, query, aliases)
	return modelSearchReport{
		Kind:                    "models",
		Action:                  "search",
		Status:                  "ok",
		Query:                   query,
		MatchCount:              len(aliases) + len(routes) + len(models),
		Aliases:                 aliases,
		Routes:                  routes,
		Models:                  models,
		LocalOnly:               true,
		RequiresProviderRequest: false,
		Message:                 "Model search is resolved locally across aliases, providers, routes, and compatibility metadata.",
	}
}

func matchingModelAliases(query string, aliases []modelAliasReport) []modelAliasReport {
	out := []modelAliasReport{}
	for _, alias := range aliases {
		if modelSearchMatches(query, alias.Name, alias.Model, alias.Provider) {
			out = append(out, alias)
		}
	}
	return out
}

func matchingModelRoutes(query string, routes []modelRouteReport) []modelRouteReport {
	out := []modelRouteReport{}
	for _, route := range routes {
		if modelSearchMatches(query, route.Prefix, route.Provider, route.WireProtocol, route.AuthEnv, route.BaseURLEnv, route.DefaultBaseURL, route.Description) {
			out = append(out, route)
		}
	}
	return out
}

func matchingModelDetails(a *App, query string, aliases []modelAliasReport) []modelDetailReport {
	seen := map[string]bool{}
	out := []modelDetailReport{}
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		key := strings.ToLower(resolveModelAlias(model))
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, a.buildModelDetailReport(model))
	}
	for _, alias := range aliases {
		add(alias.Name)
	}
	if modelSearchLooksLikeModel(query) {
		add(query)
	}
	return out
}

func modelSearchMatches(query string, values ...string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return false
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(strings.TrimSpace(value)), query) {
			return true
		}
	}
	return false
}

func modelSearchLooksLikeModel(query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return false
	}
	if modelAliasName(query) != "" {
		return true
	}
	lower := strings.ToLower(query)
	return strings.ContainsAny(query, "/-.") ||
		strings.HasPrefix(lower, "gpt") ||
		strings.HasPrefix(lower, "glm") ||
		strings.HasPrefix(lower, "claude")
}

func (a *App) ResumedModel(args []string) error {
	req, err := parseModelArgs(args)
	if err != nil {
		return err
	}
	report := modelReport{
		Kind:           "model",
		Action:         "show",
		Status:         "ok",
		Model:          a.Config.Model,
		RequestedModel: req.Model,
	}
	return renderModelReport(a.Out, report, req.Format)
}

const (
	modelUsage  = "codog model [MODEL|clear|reset|unset] [--target user|project|local] [--path PATH] [--output-format text|json]"
	modelsUsage = "codog models [list|ls|aliases|shortcuts|routes|routing|search|find QUERY|show|view|inspect [MODEL]|current|set MODEL|clear|reset|help] [--target user|project|local] [--path PATH] [--output-format text|json]"
)

func parseModelArgs(args []string) (modelRequest, error) {
	req := modelRequest{Format: "text", Target: "user"}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "model", Flag: arg, Usage: modelUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "model", Flag: arg, Usage: modelUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "model", Flag: arg, Usage: modelUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "model", Option: arg, Usage: modelUsage}
		default:
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("model", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) == 1 && isModelClearAction(positionals[0]) {
		req.Action = "clear"
		return req, nil
	}
	req.Model = strings.TrimSpace(strings.Join(positionals, " "))
	return req, nil
}

func isModelClearAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "clear", "reset", "unset", "default":
		return true
	default:
		return false
	}
}

func parseModelsArgs(args []string) (modelsRequest, error) {
	req := modelsRequest{Format: "text", Action: "list", Target: "user"}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "models", Flag: arg, Usage: modelsUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "models", Flag: arg, Usage: modelsUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "models", Flag: arg, Usage: modelsUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "models", Option: arg, Usage: modelsUsage}
		default:
			positionals = append(positionals, strings.TrimSpace(arg))
		}
	}
	normalizedFormat, err := normalizeOutputFormat("models", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) == 0 {
		return req, nil
	}
	rawAction := strings.ToLower(strings.TrimSpace(positionals[0]))
	req.Action = normalizeModelsAction(rawAction)
	if req.Action == "show" || req.Action == "search" || req.Action == "set" {
		if rawAction == "current" {
			if len(positionals) > 1 {
				return req, unexpectedExtraArgsError{
					Command: "models current",
					Args:    append([]string(nil), positionals[1:]...),
					Usage:   modelsUsage,
				}
			}
			return req, nil
		}
		req.Model = strings.TrimSpace(strings.Join(positionals[1:], " "))
		return req, nil
	}
	if req.Action == "clear" {
		if len(positionals) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "models " + req.Action,
				Args:    append([]string(nil), positionals[1:]...),
				Usage:   modelsUsage,
			}
		}
		return req, nil
	}
	if !supportedModelsAction(req.Action) {
		return req, nil
	}
	if len(positionals) > 1 {
		return req, unexpectedExtraArgsError{
			Command: "models " + req.Action,
			Args:    append([]string(nil), positionals[1:]...),
			Usage:   modelsUsage,
		}
	}
	return req, nil
}

func supportedModelsAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "list", "aliases", "routes", "search", "show", "set", "clear", "help":
		return true
	default:
		return false
	}
}

func normalizeModelsAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls", "catalog", "inventory", "available":
		return "list"
	case "alias", "aliases", "shortcut", "shortcuts":
		return "aliases"
	case "route", "routes", "routing", "map", "maps", "mapping", "mappings":
		return "routes"
	case "show", "info", "describe", "resolve", "current", "get", "view", "inspect":
		return "show"
	case "search", "find", "lookup", "query":
		return "search"
	case "set", "select", "use":
		return "set"
	case "clear", "reset", "unset", "default":
		return "clear"
	case "help":
		return "help"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

var modelsActionCandidates = []string{
	"list", "ls", "catalog", "inventory", "available", "alias", "aliases", "shortcut",
	"shortcuts", "route", "routes", "routing", "map", "maps", "mapping", "mappings",
	"show", "info", "describe", "resolve", "current", "get", "view", "inspect",
	"search", "find", "lookup", "query", "set", "select", "use", "clear", "reset",
	"unset", "default", "help",
}

func unknownModelsActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, modelsActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog models %s`? Use `codog models help` to list supported actions.", suggestions[0])
	case 0:
		return modelsUsage
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog models help` to list supported actions.", strings.Join(suggestions, ", "))
	}
}

func modelHelpRequested(args []string) bool {
	meaningful := routeMeaningfulArgs(args)
	return len(meaningful) == 1 && strings.EqualFold(strings.TrimSpace(meaningful[0]), "help")
}

func modelHelpArgsWithoutHelp(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), "help") || isHelpFlag(arg) {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func modelAliases() []modelAliasReport {
	aliases := modelrouting.BuiltInAliases()
	out := make([]modelAliasReport, 0, len(aliases))
	for _, alias := range aliases {
		report := modelAliasReport{
			Name:     alias.Name,
			Model:    alias.Model,
			Provider: modelrouting.ProviderForModel(alias.Model),
		}
		if limit, ok := modelrouting.TokenLimitForModel(alias.Model); ok {
			report.MaxOutputTokens = limit.MaxOutputTokens
			report.ContextWindowTokens = limit.ContextWindowTokens
		}
		out = append(out, report)
	}
	return out
}

func modelRoutes() []modelRouteReport {
	return []modelRouteReport{
		{
			Prefix:         "openai/",
			Provider:       modelrouting.ProviderOpenAI,
			WireProtocol:   "openai_chat_completions",
			AuthEnv:        "OPENAI_API_KEY",
			BaseURLEnv:     "OPENAI_BASE_URL",
			DefaultBaseURL: modelrouting.DefaultOpenAIBaseURL,
			Description:    "Routes explicit OpenAI-compatible model names; the prefix is stripped for default OpenAI and local endpoints.",
		},
		{
			Prefix:         "gpt-",
			Provider:       modelrouting.ProviderOpenAI,
			WireProtocol:   "openai_chat_completions",
			AuthEnv:        "OPENAI_API_KEY",
			BaseURLEnv:     "OPENAI_BASE_URL",
			DefaultBaseURL: modelrouting.DefaultOpenAIBaseURL,
			Description:    "Routes bare GPT model names to the OpenAI-compatible backend.",
		},
		{
			Prefix:         "local/",
			Provider:       modelrouting.ProviderOpenAI,
			WireProtocol:   "openai_chat_completions",
			AuthEnv:        "OPENAI_API_KEY",
			BaseURLEnv:     "OPENAI_BASE_URL or OLLAMA_HOST",
			DefaultBaseURL: modelrouting.DefaultOpenAIBaseURL,
			Description:    "Forces OpenAI-compatible local routing while preserving slash-containing model IDs after the prefix.",
		},
		{
			Prefix:         "glm or glm/",
			Provider:       modelrouting.ProviderOpenAI,
			WireProtocol:   "openai_chat_completions",
			AuthEnv:        "OPENAI_API_KEY",
			BaseURLEnv:     "OPENAI_BASE_URL",
			DefaultBaseURL: modelrouting.DefaultOpenAIBaseURL,
			Description:    "Routes GLM model names to the OpenAI-compatible backend, including local GLM gateways.",
		},
		{
			Prefix:         "OLLAMA_HOST",
			Provider:       modelrouting.ProviderOpenAI,
			WireProtocol:   "openai_chat_completions",
			AuthEnv:        "none",
			BaseURLEnv:     "OLLAMA_HOST",
			DefaultBaseURL: "http://127.0.0.1:11434/v1",
			Description:    "When set, routes the configured model through Ollama's OpenAI-compatible endpoint regardless of model prefix.",
		},
		{
			Prefix:         "grok or xai/",
			Provider:       modelrouting.ProviderXAI,
			WireProtocol:   "openai_chat_completions",
			AuthEnv:        "XAI_API_KEY",
			BaseURLEnv:     "XAI_BASE_URL",
			DefaultBaseURL: modelrouting.DefaultXAIBaseURL,
			Description:    "Routes Grok aliases and explicit xAI model names to the xAI OpenAI-compatible backend.",
		},
		{
			Prefix:         "qwen/ or qwen-",
			Provider:       modelrouting.ProviderDashScope,
			WireProtocol:   "openai_chat_completions",
			AuthEnv:        "DASHSCOPE_API_KEY",
			BaseURLEnv:     "DASHSCOPE_BASE_URL",
			DefaultBaseURL: modelrouting.DefaultDashScopeBaseURL,
			Description:    "Routes Qwen models to Alibaba DashScope compatible mode.",
		},
		{
			Prefix:         "kimi/ or kimi-",
			Provider:       modelrouting.ProviderDashScope,
			WireProtocol:   "openai_chat_completions",
			AuthEnv:        "DASHSCOPE_API_KEY",
			BaseURLEnv:     "DASHSCOPE_BASE_URL",
			DefaultBaseURL: modelrouting.DefaultDashScopeBaseURL,
			Description:    "Routes Kimi models to Alibaba DashScope compatible mode.",
		},
	}
}

func (a *App) buildModelDetailReport(model string) modelDetailReport {
	requested := strings.TrimSpace(model)
	if requested == "" {
		requested = strings.TrimSpace(a.Config.Model)
	}
	if requested == "" {
		requested = config.DefaultModel
	}
	resolved := resolveModelAlias(requested)
	provider := modelrouting.ProviderForModel(resolved)
	if requestedMatchesConfiguredModel(requested, a.Config.Model) && strings.TrimSpace(a.Config.RuntimeProvider) != "" {
		provider = a.Config.RuntimeProvider
	}
	baseURL := a.modelDiagnosticsBaseURL(provider)
	protocol := modelWireProtocol(provider)
	authEnv, baseURLEnv := modelProviderEnv(provider)
	if requestedMatchesConfiguredModel(requested, a.Config.Model) && strings.EqualFold(a.Config.RuntimeProviderSource, "OLLAMA_HOST") {
		authEnv = "none"
		baseURLEnv = "OLLAMA_HOST"
	}
	wireModel := resolved
	openAICompatible := provider == modelrouting.ProviderOpenAI || modelrouting.IsOpenAICompatibleModel(resolved)
	if openAICompatible {
		wireModel = modelrouting.WireModelForBaseURL(resolved, baseURL)
	}
	reasoningModel := openAICompatible && modelrouting.IsReasoningModel(wireModel)
	requiresReasoningContentHistory := openAICompatible && modelrouting.RequiresReasoningContentHistory(wireModel)
	extraBodyKeys, extraBodyForwardedKeys, extraBodyIgnoredKeys := providerExtraBodyKeyDiagnostics(a.Config.ExtraBody, openAICompatible)
	diagnosticConfig := a.Config
	diagnosticConfig.Model = requested
	report := modelDetailReport{
		Kind:                                  "models",
		Action:                                "show",
		Status:                                "ok",
		RequestedModel:                        requested,
		ResolvedModel:                         resolved,
		Alias:                                 modelAliasName(requested),
		Provider:                              provider,
		WireProtocol:                          protocol,
		BaseURL:                               baseURL,
		WireModel:                             wireModel,
		AuthEnv:                               authEnv,
		BaseURLEnv:                            baseURLEnv,
		OpenAICompatible:                      openAICompatible,
		ReasoningModel:                        reasoningModel,
		UsesMaxCompletionTokens:               modelrouting.UsesMaxCompletionTokens(wireModel),
		StripsTuningParams:                    reasoningModel,
		SupportsStreamUsage:                   providerSupportsStreamUsage(provider, openAICompatible),
		HonorsProxyEnv:                        true,
		SupportsExtraBodyParams:               openAICompatible,
		ExtraBodyConfigured:                   len(a.Config.ExtraBody) != 0,
		ExtraBodyKeys:                         extraBodyKeys,
		ExtraBodyForwardedKeys:                extraBodyForwardedKeys,
		ExtraBodyIgnoredKeys:                  extraBodyIgnoredKeys,
		PreservesSlashModelIDsOnCustomBaseURL: provider == modelrouting.ProviderOpenAI,
		ProtectedExtraBodyKeys:                providerProtectedExtraBodyKeys(openAICompatible),
		Diagnostics:                           providerDiagnosticsForActiveConfig(diagnosticConfig, provider, wireModel, reasoningModel, requiresReasoningContentHistory, reasoningModel, extraBodyIgnoredKeys),
		RejectsToolResultIsErrorField:         modelrouting.ModelRejectsIsErrorField(resolved),
		RequiresReasoningContentHistory:       requiresReasoningContentHistory,
		LocalOnly:                             true,
		RequiresProviderRequest:               false,
		Message:                               "Model details are resolved locally; no provider request was made.",
	}
	if limit, ok := modelrouting.TokenLimitForModel(resolved); ok {
		report.MaxOutputTokens = limit.MaxOutputTokens
		report.ContextWindowTokens = limit.ContextWindowTokens
	}
	return report
}
