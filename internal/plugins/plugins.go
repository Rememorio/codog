package plugins

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DisabledMarker = ".disabled"

type Manifest struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Version     string         `json:"version,omitempty"`
	Description string         `json:"description,omitempty"`
	Tools       []ToolManifest `json:"tools,omitempty"`
	Commands    []string       `json:"commands,omitempty"`
	Hooks       []string       `json:"hooks,omitempty"`
	Path        string         `json:"path,omitempty"`
	Root        string         `json:"root,omitempty"`
	Enabled     bool           `json:"enabled"`
}

type ToolManifest struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Command     string         `json:"command,omitempty"`
	Args        []string       `json:"args,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Permission  string         `json:"permission,omitempty"`
}

type rawManifest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	Commands    []string          `json:"commands,omitempty"`
	Hooks       []string          `json:"hooks,omitempty"`
}

func Load(workspace string) ([]Manifest, error) {
	root := Root(workspace)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []Manifest{}, nil
		}
		return nil, err
	}
	manifests := []Manifest{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := LoadManifest(filepath.Join(root, entry.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if manifest.ID == "" {
			manifest.ID = entry.Name()
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	return manifests, nil
}

func Root(workspace string) string {
	return filepath.Join(workspace, ".codog", "plugins")
}

func LoadManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var raw rawManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		ID:          raw.ID,
		Name:        raw.Name,
		Version:     raw.Version,
		Description: raw.Description,
		Commands:    raw.Commands,
		Hooks:       raw.Hooks,
		Path:        path,
		Root:        dir,
		Enabled:     !Disabled(dir),
	}
	for _, rawTool := range raw.Tools {
		var name string
		if err := json.Unmarshal(rawTool, &name); err == nil {
			manifest.Tools = append(manifest.Tools, ToolManifest{Name: name})
			continue
		}
		var tool ToolManifest
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			return Manifest{}, err
		}
		manifest.Tools = append(manifest.Tools, tool)
	}
	if manifest.ID == "" {
		manifest.ID = filepath.Base(dir)
	}
	if manifest.Name == "" {
		manifest.Name = manifest.ID
	}
	return manifest, nil
}

func Disabled(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, DisabledMarker))
	return err == nil
}

func Install(workspace, source string) (Manifest, error) {
	sourceDir, err := sourceDir(source)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err := LoadManifest(sourceDir)
	if err != nil {
		return Manifest{}, err
	}
	if err := validateID(manifest.ID); err != nil {
		return Manifest{}, err
	}
	target := filepath.Join(Root(workspace), manifest.ID)
	if _, err := os.Stat(target); err == nil {
		return Manifest{}, errors.New("plugin is already installed")
	} else if err != nil && !os.IsNotExist(err) {
		return Manifest{}, err
	}
	if err := copyDir(sourceDir, target); err != nil {
		return Manifest{}, err
	}
	return LoadManifest(target)
}

func Enable(workspace, id string) (Manifest, error) {
	dir, err := pluginDir(workspace, id)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Remove(filepath.Join(dir, DisabledMarker)); err != nil && !os.IsNotExist(err) {
		return Manifest{}, err
	}
	return LoadManifest(dir)
}

func Disable(workspace, id string) (Manifest, error) {
	dir, err := pluginDir(workspace, id)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, DisabledMarker), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return Manifest{}, err
	}
	return LoadManifest(dir)
}

func Remove(workspace, id string) error {
	dir, err := pluginDir(workspace, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func pluginDir(workspace, id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	dir := filepath.Join(Root(workspace), id)
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		return "", err
	}
	return dir, nil
}

func validateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("plugin id is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return errors.New("plugin id must be a single path component")
	}
	return nil
}

func sourceDir(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("plugin source is required")
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return source, nil
	}
	if filepath.Base(source) != "plugin.json" {
		return "", errors.New("plugin source must be a directory or plugin.json")
	}
	return filepath.Dir(source), nil
}

func copyDir(source, target string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(target, 0o755)
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(target, rel), 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return copyFile(path, filepath.Join(target, rel))
	})
}

func copyFile(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
