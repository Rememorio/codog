package undo

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrNoUndo = errors.New("no undo records available")

type Record struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	Tool    string    `json:"tool"`
	Path    string    `json:"path"`
	Existed bool      `json:"existed"`
	Content string    `json:"content_base64,omitempty"`
}

type RestoreReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	ID        string `json:"id"`
	Tool      string `json:"tool"`
	Path      string `json:"path"`
	Restored  bool   `json:"restored"`
	Removed   bool   `json:"removed"`
	Bytes     int    `json:"bytes"`
	Remaining int    `json:"remaining"`
}

func Push(workspace, tool, targetPath string, existed bool, content []byte) (Record, error) {
	if strings.TrimSpace(workspace) == "" {
		return Record{}, errors.New("workspace is required")
	}
	path, err := storedPath(workspace, targetPath)
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	record := Record{
		ID:      now.Format("20060102T150405.000000000Z"),
		Time:    now,
		Tool:    strings.TrimSpace(tool),
		Path:    path,
		Existed: existed,
	}
	if existed {
		record.Content = base64.StdEncoding.EncodeToString(content)
	}
	if err := os.MkdirAll(filepath.Dir(journalPath(workspace)), 0o755); err != nil {
		return Record{}, err
	}
	file, err := os.OpenFile(journalPath(workspace), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	data, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return Record{}, err
	}
	return record, nil
}

func RestoreLast(workspace string) (RestoreReport, error) {
	records, err := readRecords(workspace)
	if err != nil {
		return RestoreReport{}, err
	}
	if len(records) == 0 {
		return RestoreReport{}, ErrNoUndo
	}
	last := records[len(records)-1]
	target, err := resolvePath(workspace, last.Path)
	if err != nil {
		return RestoreReport{}, err
	}
	report := RestoreReport{
		Kind:      "undo",
		Action:    "restore",
		Status:    "ok",
		ID:        last.ID,
		Tool:      last.Tool,
		Path:      last.Path,
		Remaining: len(records) - 1,
	}
	if last.Existed {
		data, err := base64.StdEncoding.DecodeString(last.Content)
		if err != nil {
			return RestoreReport{}, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return RestoreReport{}, err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return RestoreReport{}, err
		}
		report.Restored = true
		report.Bytes = len(data)
	} else {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return RestoreReport{}, err
		}
		report.Removed = true
	}
	if err := writeRecords(workspace, records[:len(records)-1]); err != nil {
		return RestoreReport{}, err
	}
	return report, nil
}

func journalPath(workspace string) string {
	return filepath.Join(workspace, ".codog", "undo.jsonl")
}

func storedPath(workspace, targetPath string) (string, error) {
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(workspaceAbs, targetAbs); err == nil && scopedRelativePath(rel) {
		return filepath.ToSlash(rel), nil
	}
	return targetAbs, nil
}

func resolvePath(workspace, stored string) (string, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return "", errors.New("undo record path is empty")
	}
	if filepath.IsAbs(stored) {
		return stored, nil
	}
	clean := filepath.Clean(filepath.FromSlash(stored))
	if !scopedRelativePath(clean) {
		return "", fmt.Errorf("undo record path escapes workspace: %s", stored)
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	return filepath.Join(workspaceAbs, clean), nil
}

func scopedRelativePath(path string) bool {
	if path == "." || path == "" || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func readRecords(workspace string) ([]Record, error) {
	file, err := os.Open(journalPath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	records := []Record{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

func writeRecords(workspace string, records []Record) error {
	path := journalPath(workspace)
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			_ = file.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
