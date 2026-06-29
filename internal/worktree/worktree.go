package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Allocation struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Ref       string    `json:"ref"`
	CreatedAt time.Time `json:"created_at"`
}

func Allocate(workspace, name string) (Allocation, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Allocation{}, err
	}
	ref, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		return Allocation{}, fmt.Errorf("cannot allocate worktree outside a committed git repository: %w", err)
	}
	id := safeID(name, time.Now().UTC())
	path := filepath.Join(root(workspace), "checkouts", id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Allocation{}, err
	}
	if _, err := gitOutput(workspace, "worktree", "add", "--detach", path, ref); err != nil {
		return Allocation{}, err
	}
	allocation := Allocation{
		ID:        id,
		Path:      path,
		Ref:       ref,
		CreatedAt: time.Now().UTC(),
	}
	if err := save(workspace, allocation); err != nil {
		_ = Remove(workspace, id)
		return Allocation{}, err
	}
	return allocation, nil
}

func List(workspace string) ([]Allocation, error) {
	dir := metadataRoot(workspace)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Allocation{}, nil
		}
		return nil, err
	}
	allocations := []Allocation{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var allocation Allocation
		if err := json.Unmarshal(data, &allocation); err != nil {
			return nil, err
		}
		allocations = append(allocations, allocation)
	}
	sort.Slice(allocations, func(i, j int) bool {
		return allocations[i].CreatedAt.After(allocations[j].CreatedAt)
	})
	return allocations, nil
}

func Remove(workspace, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	allocation, err := load(workspace, id)
	if err != nil {
		return err
	}
	if _, err := gitOutput(workspace, "worktree", "remove", "--force", allocation.Path); err != nil {
		_ = os.RemoveAll(allocation.Path)
	}
	if err := os.Remove(filepath.Join(metadataRoot(workspace), id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func save(workspace string, allocation Allocation) error {
	if err := os.MkdirAll(metadataRoot(workspace), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(allocation, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(metadataRoot(workspace), allocation.ID+".json"), append(data, '\n'), 0o644)
}

func load(workspace, id string) (Allocation, error) {
	if err := validateID(id); err != nil {
		return Allocation{}, err
	}
	data, err := os.ReadFile(filepath.Join(metadataRoot(workspace), id+".json"))
	if err != nil {
		return Allocation{}, err
	}
	var allocation Allocation
	if err := json.Unmarshal(data, &allocation); err != nil {
		return Allocation{}, err
	}
	return allocation, nil
}

func root(workspace string) string {
	return filepath.Join(workspace, ".codog", "worktrees")
}

func metadataRoot(workspace string) string {
	return filepath.Join(root(workspace), "metadata")
}

func gitOutput(workspace string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(data)))
	}
	return strings.TrimSpace(string(data)), nil
}

func safeID(name string, now time.Time) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	base := strings.Trim(builder.String(), "-_")
	if base == "" {
		base = "agent"
	}
	return fmt.Sprintf("%s-%s", base, now.Format("20060102T150405.000000000Z"))
}

func validateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("worktree id is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return errors.New("worktree id must be a single path component")
	}
	return nil
}
