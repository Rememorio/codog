package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type EditorIdentity struct {
	Editor    string    `json:"editor"`
	Version   string    `json:"version,omitempty"`
	Workspace string    `json:"workspace"`
	Trusted   bool      `json:"trusted"`
	TrustedAt time.Time `json:"trusted_at,omitempty"`
}

type EditorOpenFile struct {
	Path     string    `json:"path"`
	OpenedAt time.Time `json:"opened_at,omitempty"`
}

type EditorSelection struct {
	Path        string    `json:"path"`
	StartLine   int       `json:"start_line"`
	StartColumn int       `json:"start_column,omitempty"`
	EndLine     int       `json:"end_line"`
	EndColumn   int       `json:"end_column,omitempty"`
	Text        string    `json:"text,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type EditorState struct {
	Identity  *EditorIdentity  `json:"identity,omitempty"`
	OpenFile  *EditorOpenFile  `json:"open_file,omitempty"`
	Selection *EditorSelection `json:"selection,omitempty"`
	UpdatedAt time.Time        `json:"updated_at,omitempty"`
}

func (s Server) editorIdentify(params json.RawMessage) (any, error) {
	var payload struct {
		Editor    string `json:"editor"`
		Version   string `json:"version"`
		Workspace string `json:"workspace"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Editor) == "" {
		return nil, errors.New("editor is required")
	}
	if s.TrustToken != "" && payload.Token != s.TrustToken {
		return nil, errors.New("editor bridge token is invalid")
	}
	workspace, err := s.workspace()
	if err != nil {
		return nil, err
	}
	if payload.Workspace != "" {
		if err := sameWorkspace(workspace, payload.Workspace); err != nil {
			return nil, err
		}
	}
	state, err := s.loadEditorState()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	state.Identity = &EditorIdentity{
		Editor:    payload.Editor,
		Version:   payload.Version,
		Workspace: workspace,
		Trusted:   true,
		TrustedAt: now,
	}
	state.UpdatedAt = now
	if err := s.saveEditorState(state); err != nil {
		return nil, err
	}
	return state.Identity, nil
}

func (s Server) editorState() (any, error) {
	return s.loadEditorState()
}

func (s Server) editorOpen(params json.RawMessage) (any, error) {
	if _, err := s.requireTrustedEditor(); err != nil {
		return nil, err
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	path, rel, err := s.resolve(payload.Path, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("path must be a file")
	}
	state, err := s.loadEditorState()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	state.OpenFile = &EditorOpenFile{Path: rel, OpenedAt: now}
	state.UpdatedAt = now
	if err := s.saveEditorState(state); err != nil {
		return nil, err
	}
	return state.OpenFile, nil
}

func (s Server) editorSelection(params json.RawMessage) (any, error) {
	if _, err := s.requireTrustedEditor(); err != nil {
		return nil, err
	}
	state, err := s.loadEditorState()
	if err != nil {
		return nil, err
	}
	var payload EditorSelection
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	if payload.Path == "" && state.OpenFile != nil {
		payload.Path = state.OpenFile.Path
	}
	path, rel, err := s.resolve(payload.Path, false)
	if err != nil {
		return nil, err
	}
	if payload.StartLine <= 0 {
		return nil, errors.New("start_line is required")
	}
	if payload.EndLine <= 0 {
		payload.EndLine = payload.StartLine
	}
	if payload.EndLine < payload.StartLine {
		return nil, errors.New("end_line must be after start_line")
	}
	payload.Path = rel
	if payload.Text == "" {
		text, err := selectionText(path, payload)
		if err != nil {
			return nil, err
		}
		payload.Text = text
	}
	payload.UpdatedAt = time.Now().UTC()
	state.Selection = &payload
	state.UpdatedAt = payload.UpdatedAt
	if err := s.saveEditorState(state); err != nil {
		return nil, err
	}
	return state.Selection, nil
}

func (s Server) requireTrustedEditor() (*EditorIdentity, error) {
	state, err := s.loadEditorState()
	if err != nil {
		return nil, err
	}
	if state.Identity == nil || !state.Identity.Trusted {
		return nil, errors.New("editor is not trusted; call editor/identify first")
	}
	workspace, err := s.workspace()
	if err != nil {
		return nil, err
	}
	if err := sameWorkspace(state.Identity.Workspace, workspace); err != nil {
		return nil, err
	}
	return state.Identity, nil
}

func (s Server) loadEditorState() (EditorState, error) {
	path, err := s.editorStatePath()
	if err != nil {
		return EditorState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EditorState{}, nil
		}
		return EditorState{}, err
	}
	var state EditorState
	if err := json.Unmarshal(data, &state); err != nil {
		return EditorState{}, err
	}
	return state, nil
}

func (s Server) saveEditorState(state EditorState) error {
	path, err := s.editorStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Server) editorStatePath() (string, error) {
	if strings.TrimSpace(s.ConfigHome) == "" {
		return "", errors.New("config home is required")
	}
	return filepath.Join(s.ConfigHome, "bridge", "editor-state.json"), nil
}

func sameWorkspace(expected, actual string) error {
	if strings.TrimSpace(expected) == "" || strings.TrimSpace(actual) == "" {
		return nil
	}
	left, err := filepath.Abs(expected)
	if err != nil {
		return err
	}
	right, err := filepath.Abs(actual)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(left); err == nil {
		left = resolved
	}
	if resolved, err := filepath.EvalSymlinks(right); err == nil {
		right = resolved
	}
	if left != right {
		return errors.New("editor workspace is not trusted")
	}
	return nil
}

func selectionText(path string, selection EditorSelection) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if selection.StartLine > len(lines) || selection.EndLine > len(lines) {
		return "", errors.New("selection line is outside file")
	}
	selected := append([]string(nil), lines[selection.StartLine-1:selection.EndLine]...)
	if len(selected) == 0 {
		return "", nil
	}
	if selection.StartLine == selection.EndLine {
		selected[0] = sliceColumns(selected[0], selection.StartColumn, selection.EndColumn)
		return selected[0], nil
	}
	if selection.StartColumn > 0 {
		selected[0] = sliceColumns(selected[0], selection.StartColumn, 0)
	}
	if selection.EndColumn > 0 {
		last := len(selected) - 1
		selected[last] = sliceColumns(selected[last], 1, selection.EndColumn)
	}
	return strings.Join(selected, "\n"), nil
}

func sliceColumns(line string, startColumn int, endColumn int) string {
	runes := []rune(line)
	start := 0
	if startColumn > 1 {
		start = startColumn - 1
	}
	if start > len(runes) {
		start = len(runes)
	}
	end := len(runes)
	if endColumn > 0 {
		end = endColumn - 1
	}
	if end < start {
		end = start
	}
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}
