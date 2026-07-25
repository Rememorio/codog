package claudemigrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func discoverAssets(sourceHome, workspace string) ([]Asset, error) {
	specs := []struct {
		kind  string
		mode  string
		paths []string
		match func(string, fs.DirEntry) bool
	}{
		{kind: "config", mode: "compatible", paths: []string{
			filepath.Join(sourceHome, "settings.json"),
			filepath.Join(workspace, ".claude", "settings.json"),
			filepath.Join(workspace, ".claude", "settings.local.json"),
		}},
		{kind: "instructions", mode: "compatible", paths: []string{
			filepath.Join(workspace, "CLAUDE.md"),
			filepath.Join(workspace, "CLAUDE.local.md"),
			filepath.Join(workspace, ".claude", "CLAUDE.md"),
		}},
		{kind: "skills", mode: "compatible", paths: []string{
			filepath.Join(sourceHome, "skills"),
			filepath.Join(workspace, ".claude", "skills"),
		}, match: namedFile("SKILL.md")},
		{kind: "commands", mode: "compatible", paths: []string{
			filepath.Join(sourceHome, "commands"),
			filepath.Join(workspace, ".claude", "commands"),
		}, match: extensionFile(".md")},
		{kind: "agents", mode: "compatible", paths: []string{
			filepath.Join(sourceHome, "agents"),
			filepath.Join(workspace, ".claude", "agents"),
		}, match: extensionFile(".md")},
		{kind: "mcp", mode: "compatible", paths: []string{
			filepath.Join(workspace, ".mcp.json"),
		}},
	}
	assets := make([]Asset, 0, len(specs)+1)
	for _, spec := range specs {
		asset, err := inspectAsset(spec.kind, spec.mode, spec.paths, spec.match)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	hooks, err := inspectHooks([]string{
		filepath.Join(sourceHome, "settings.json"),
		filepath.Join(workspace, ".claude", "settings.json"),
		filepath.Join(workspace, ".claude", "settings.local.json"),
	})
	if err != nil {
		return nil, err
	}
	assets = append(assets, hooks)
	sort.Slice(assets, func(i, j int) bool { return assets[i].Kind < assets[j].Kind })
	return assets, nil
}

func inspectAsset(kind, mode string, paths []string, match func(string, fs.DirEntry) bool) (Asset, error) {
	asset := Asset{Kind: kind, Mode: mode, Sources: []string{}}
	for _, path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Asset{}, fmt.Errorf("inspect %s asset %s: %w", kind, path, err)
		}
		if !info.IsDir() {
			asset.Count++
			asset.Sources = append(asset.Sources, path)
			continue
		}
		count := 0
		err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if match == nil || match(current, entry) {
				count++
			}
			return nil
		})
		if err != nil {
			return Asset{}, fmt.Errorf("inspect %s asset %s: %w", kind, path, err)
		}
		if count > 0 {
			asset.Count += count
			asset.Sources = append(asset.Sources, path)
		}
	}
	return asset, nil
}

func namedFile(name string) func(string, fs.DirEntry) bool {
	return func(_ string, entry fs.DirEntry) bool {
		return strings.EqualFold(entry.Name(), name)
	}
}

func extensionFile(extension string) func(string, fs.DirEntry) bool {
	return func(_ string, entry fs.DirEntry) bool {
		return strings.EqualFold(filepath.Ext(entry.Name()), extension)
	}
}

func inspectHooks(paths []string) (Asset, error) {
	asset := Asset{Kind: "hooks", Mode: "compatible", Sources: []string{}}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Asset{}, fmt.Errorf("read hooks source %s: %w", path, err)
		}
		var root map[string]json.RawMessage
		if err := json.Unmarshal(data, &root); err != nil {
			return Asset{}, fmt.Errorf("decode hooks source %s: %w", path, err)
		}
		raw, ok := root["hooks"]
		if !ok {
			continue
		}
		var hooks map[string]json.RawMessage
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return Asset{}, fmt.Errorf("decode hooks in %s: %w", path, err)
		}
		asset.Count += len(hooks)
		asset.Sources = append(asset.Sources, path)
	}
	return asset, nil
}
