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
	ID                 string    `json:"id"`
	Path               string    `json:"path"`
	Ref                string    `json:"ref"`
	CreatedAt          time.Time `json:"created_at"`
	SymlinkDirectories []string  `json:"symlink_directories,omitempty"`
	SparsePaths        []string  `json:"sparse_paths,omitempty"`
}

type Options struct {
	SymlinkDirectories []string
	SparsePaths        []string
}

func Allocate(workspace, name string) (Allocation, error) {
	return AllocateWithOptions(workspace, name, Options{})
}

func AllocateWithOptions(workspace, name string, options Options) (Allocation, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Allocation{}, err
	}
	normalized, err := normalizeOptions(options)
	if err != nil {
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
	if err := addGitWorktree(workspace, path, ref, normalized.SparsePaths); err != nil {
		return Allocation{}, err
	}
	if err := linkDirectories(workspace, path, normalized.SymlinkDirectories); err != nil {
		_, _ = gitOutput(workspace, "worktree", "remove", "--force", path)
		_ = os.RemoveAll(path)
		return Allocation{}, err
	}
	allocation := Allocation{
		ID:                 id,
		Path:               path,
		Ref:                ref,
		CreatedAt:          time.Now().UTC(),
		SymlinkDirectories: append([]string(nil), normalized.SymlinkDirectories...),
		SparsePaths:        append([]string(nil), normalized.SparsePaths...),
	}
	if err := save(workspace, allocation); err != nil {
		_ = Remove(workspace, id)
		return Allocation{}, err
	}
	return allocation, nil
}

func addGitWorktree(workspace, path, ref string, sparsePaths []string) error {
	if len(sparsePaths) == 0 {
		_, err := gitOutput(workspace, "worktree", "add", "--detach", path, ref)
		return err
	}
	if _, err := gitOutput(workspace, "worktree", "add", "--detach", "--no-checkout", path, ref); err != nil {
		return err
	}
	if _, err := gitOutput(path, "sparse-checkout", "init", "--cone"); err != nil {
		return err
	}
	args := append([]string{"sparse-checkout", "set"}, sparsePaths...)
	if _, err := gitOutput(path, args...); err != nil {
		return err
	}
	_, err := gitOutput(path, "checkout", "--detach", ref)
	return err
}

func linkDirectories(workspace, checkout string, directories []string) error {
	for _, dir := range directories {
		source := filepath.Join(workspace, dir)
		if _, err := os.Stat(source); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		target := filepath.Join(checkout, dir)
		if existing, err := os.Lstat(target); err == nil {
			if existing.Mode()&os.ModeSymlink != 0 {
				if link, readErr := os.Readlink(target); readErr == nil {
					if samePath(source, filepath.Join(filepath.Dir(target), link)) {
						continue
					}
				}
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(source, target); err != nil {
			return err
		}
	}
	return nil
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

func Load(workspace, id string) (Allocation, error) {
	return load(workspace, id)
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

func normalizeOptions(options Options) (Options, error) {
	symlinks, err := normalizeRelativePaths(options.SymlinkDirectories, "worktree symlink directory")
	if err != nil {
		return Options{}, err
	}
	sparse, err := normalizeRelativePaths(options.SparsePaths, "worktree sparse path")
	if err != nil {
		return Options{}, err
	}
	return Options{
		SymlinkDirectories: symlinks,
		SparsePaths:        sparse,
	}, nil
}

func normalizeRelativePaths(paths []string, label string) ([]string, error) {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range paths {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if filepath.IsAbs(value) {
			return nil, fmt.Errorf("%s must be relative: %s", label, value)
		}
		cleaned := filepath.Clean(value)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s must stay inside the workspace: %s", label, value)
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out, nil
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil {
		a = absA
	}
	if errB == nil {
		b = absB
	}
	return filepath.Clean(a) == filepath.Clean(b)
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
