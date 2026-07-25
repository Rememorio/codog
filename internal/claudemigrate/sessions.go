package claudemigrate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/session"
)

func discoverSessions(opts Options) ([]candidate, []SessionResult, error) {
	root := filepath.Join(opts.SourceHome, "projects")
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, fmt.Errorf("inspect Claude Code projects: %w", err)
	}
	now := time.Now()
	candidates := []candidate{}
	failures := []SessionResult{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if opts.MaxAge > 0 && now.Sub(info.ModTime()) > opts.MaxAge {
			return nil
		}
		item, relevant, err := parseSession(path, opts.Workspace, info.ModTime())
		if err != nil {
			failures = append(failures, SessionResult{
				Source:     path,
				ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
				Status:     "failed",
				Reason:     err.Error(),
			})
			return nil
		}
		if relevant {
			candidates = append(candidates, item)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("discover Claude Code sessions: %w", err)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].modifiedAt.Equal(candidates[j].modifiedAt) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].modifiedAt.After(candidates[j].modifiedAt)
	})
	if opts.MaxSessions > 0 && len(candidates) > opts.MaxSessions {
		candidates = candidates[:opts.MaxSessions]
	}
	return candidates, failures, nil
}

func parseSession(path, workspace string, modifiedAt time.Time) (candidate, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return candidate{}, false, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(bufio.NewReader(file))
	item := candidate{path: path, modifiedAt: modifiedAt}
	seen := map[string]bool{}
	relevant := false
	for {
		var record rawRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return candidate{}, false, fmt.Errorf("decode transcript: %w", err)
		}
		if record.IsSidechain || (record.Type != "user" && record.Type != "assistant") {
			continue
		}
		recordWorkspace := canonicalPath(strings.TrimSpace(record.CWD))
		if recordWorkspace == "" || !sameWorkspace(recordWorkspace, workspace) {
			continue
		}
		relevant = true
		if item.workspace == "" {
			item.workspace = recordWorkspace
		}
		if item.id == "" {
			item.id = strings.TrimSpace(record.SessionID)
		}
		if record.UUID != "" && seen[record.UUID] {
			continue
		}
		seen[record.UUID] = true
		message, ok, err := convertMessage(record.Message)
		if err != nil {
			return candidate{}, false, err
		}
		if ok {
			item.messages = append(item.messages, message)
		}
	}
	if !relevant {
		return candidate{}, false, nil
	}
	if item.id == "" {
		item.id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if len(item.messages) == 0 {
		return candidate{}, false, errors.New("transcript contains no importable messages")
	}
	return item, true, nil
}

func convertMessage(data json.RawMessage) (importMessage, bool, error) {
	var raw rawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return importMessage{}, false, fmt.Errorf("decode message: %w", err)
	}
	role := strings.ToLower(strings.TrimSpace(raw.Role))
	if role != "user" && role != "assistant" {
		return importMessage{}, false, nil
	}
	blocks, err := convertContent(raw.Content)
	if err != nil {
		return importMessage{}, false, err
	}
	if len(blocks) == 0 {
		return importMessage{}, false, nil
	}
	message := anthropic.Message{ID: strings.TrimSpace(raw.ID), Role: role, Content: blocks}
	imported := importMessage{message: message}
	if role == "user" {
		imported.input = messageText(message)
	}
	if raw.Usage != (anthropic.Usage{}) {
		usage := raw.Usage
		imported.usage = &usage
	}
	return imported, true, nil
}

func convertContent(data json.RawMessage) ([]anthropic.ContentBlock, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []anthropic.ContentBlock{{Type: "text", Text: text}}, nil
	}
	var rawBlocks []rawBlock
	if err := json.Unmarshal(data, &rawBlocks); err != nil {
		return nil, fmt.Errorf("decode message content: %w", err)
	}
	blocks := make([]anthropic.ContentBlock, 0, len(rawBlocks))
	for _, raw := range rawBlocks {
		block := anthropic.ContentBlock{
			Type:      strings.TrimSpace(raw.Type),
			Text:      raw.Text,
			Thinking:  raw.Thinking,
			Signature: raw.Signature,
			ID:        raw.ID,
			Name:      raw.Name,
			Input:     raw.Input,
			ToolUseID: raw.ToolUseID,
			IsError:   raw.IsError,
		}
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				blocks = append(blocks, block)
			}
		case "thinking", "redacted_thinking":
			if strings.TrimSpace(block.Thinking) != "" || block.Type == "redacted_thinking" {
				blocks = append(blocks, block)
			}
		case "tool_use":
			if block.ID != "" && block.Name != "" {
				blocks = append(blocks, block)
			}
		case "tool_result":
			block.Content = contentText(raw.Content)
			if block.ToolUseID != "" {
				blocks = append(blocks, block)
			}
		case "image", "document":
			var source anthropic.ContentSource
			if err := json.Unmarshal(raw.Source, &source); err == nil && source.Type != "" {
				block.Source = &source
				blocks = append(blocks, block)
			}
		}
	}
	return blocks, nil
}

func contentText(data json.RawMessage) string {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(data, &text) == nil {
		return text
	}
	var blocks []rawBlock
	if json.Unmarshal(data, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return string(data)
}

func importCandidate(store *session.Store, workspace string, item candidate) (target string, err error) {
	identity := session.SessionIdentity{
		Title:     sessionTitle(item),
		Workspace: workspace,
		Purpose:   "Imported from Claude Code",
		Tag:       "claude-import",
	}
	created, err := store.CreateWithIdentity(item.id, identity)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(created.Path)
		}
	}()
	for _, imported := range item.messages {
		if imported.input != "" {
			if err := store.AppendInput(created.ID, imported.input); err != nil {
				return "", err
			}
		}
		if err := store.AppendWithUsage(created.ID, imported.message, imported.usage); err != nil {
			return "", err
		}
	}
	complete = true
	return created.Path, nil
}

func sessionTitle(item candidate) string {
	for _, imported := range item.messages {
		if imported.message.Role != "user" {
			continue
		}
		text := strings.TrimSpace(messageText(imported.message))
		if text == "" {
			continue
		}
		line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
		runes := []rune(line)
		if len(runes) > 72 {
			line = string(runes[:72])
		}
		if line != "" {
			return line
		}
	}
	return "Imported Claude Code session " + item.id
}

func messageText(message anthropic.Message) string {
	parts := []string{}
	for _, block := range message.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
