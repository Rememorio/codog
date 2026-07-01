package todos

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const FileName = "todos.json"

type Item struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	ActiveForm string    `json:"activeForm,omitempty"`
	Status     string    `json:"status"`
	Priority   string    `json:"priority"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type State struct {
	Kind      string    `json:"kind"`
	UpdatedAt time.Time `json:"updated_at"`
	Items     []Item    `json:"items"`
}

type Report struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Total  int    `json:"total"`
	Items  []Item `json:"items"`
}

func Path(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, ".codog", FileName)
}

func Load(workspace string) (State, error) {
	data, err := os.ReadFile(Path(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return State{Kind: "todos", Items: []Item{}}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Kind != "todos" {
		return State{}, errors.New("todo state kind is invalid")
	}
	state.Items = NormalizeItems(state.Items)
	return state, nil
}

func Save(workspace string, state State) error {
	state.Kind = "todos"
	state.UpdatedAt = time.Now().UTC()
	state.Items = NormalizeItems(state.Items)
	path := Path(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".todos-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func List(workspace string) (Report, error) {
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	return report("list", state.Items), nil
}

func Replace(workspace string, items []Item) (Report, error) {
	normalized := NormalizeItems(items)
	for _, item := range normalized {
		if strings.TrimSpace(item.Content) == "" {
			return Report{}, errors.New("todo content is required")
		}
		if !validStatus(item.Status) {
			return Report{}, fmt.Errorf("invalid todo status %q", item.Status)
		}
		if !validPriority(item.Priority) {
			return Report{}, fmt.Errorf("invalid todo priority %q", item.Priority)
		}
	}
	state := State{Kind: "todos", Items: normalized}
	if err := Save(workspace, state); err != nil {
		return Report{}, err
	}
	return report("replace", normalized), nil
}

func Add(workspace string, content string, priority string) (Report, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Report{}, errors.New("todo content is required")
	}
	if priority == "" {
		priority = "medium"
	}
	if !validPriority(priority) {
		return Report{}, fmt.Errorf("invalid todo priority %q", priority)
	}
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	nextID := nextID(state.Items)
	state.Items = append(state.Items, Item{
		ID:        nextID,
		Content:   content,
		Status:    "pending",
		Priority:  priority,
		UpdatedAt: time.Now().UTC(),
	})
	if err := Save(workspace, state); err != nil {
		return Report{}, err
	}
	return report("add", state.Items), nil
}

func UpdateStatus(workspace string, id string, status string) (Report, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Report{}, errors.New("todo id is required")
	}
	if !validStatus(status) {
		return Report{}, fmt.Errorf("invalid todo status %q", status)
	}
	state, err := Load(workspace)
	if err != nil {
		return Report{}, err
	}
	found := false
	for index := range state.Items {
		if state.Items[index].ID == id {
			state.Items[index].Status = status
			state.Items[index].UpdatedAt = time.Now().UTC()
			found = true
			break
		}
	}
	if !found {
		return Report{}, fmt.Errorf("todo %q not found", id)
	}
	if err := Save(workspace, state); err != nil {
		return Report{}, err
	}
	return report(status, state.Items), nil
}

func Clear(workspace string) (Report, error) {
	if err := os.Remove(Path(workspace)); err != nil && !os.IsNotExist(err) {
		return Report{}, err
	}
	return report("clear", []Item{}), nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Todos")
	fmt.Fprintf(w, "  Items            %d\n", report.Total)
	if report.Total == 0 {
		fmt.Fprintln(w, "  Result           no todos")
		return
	}
	for _, item := range report.Items {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", item.ID, item.Status, item.Priority, item.Content)
	}
}

// NormalizeItems applies stable defaults to todo items before validation,
// storage, or tool output.
func NormalizeItems(items []Item) []Item {
	now := time.Now().UTC()
	out := make([]Item, 0, len(items))
	seen := map[string]int{}
	for index, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = fmt.Sprintf("todo-%d", index+1)
		}
		if seen[item.ID] > 0 {
			seen[item.ID]++
			item.ID = fmt.Sprintf("%s-%d", item.ID, seen[item.ID])
		} else {
			seen[item.ID] = 1
		}
		item.Content = strings.TrimSpace(item.Content)
		item.ActiveForm = strings.TrimSpace(item.ActiveForm)
		if item.ActiveForm == "" {
			item.ActiveForm = item.Content
		}
		item.Status = strings.TrimSpace(item.Status)
		if item.Status == "" {
			item.Status = "pending"
		}
		item.Priority = strings.TrimSpace(item.Priority)
		if item.Priority == "" {
			item.Priority = "medium"
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = now
		}
		out = append(out, item)
	}
	return out
}

func report(action string, items []Item) Report {
	return Report{Kind: "todos", Action: action, Status: "ok", Total: len(items), Items: append([]Item(nil), items...)}
}

func nextID(items []Item) string {
	used := map[string]struct{}{}
	for _, item := range items {
		used[item.ID] = struct{}{}
	}
	for index := 1; ; index++ {
		id := fmt.Sprintf("todo-%d", index)
		if _, ok := used[id]; !ok {
			return id
		}
	}
}

func validStatus(status string) bool {
	switch status {
	case "pending", "in_progress", "completed":
		return true
	default:
		return false
	}
}

func validPriority(priority string) bool {
	switch priority {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}
