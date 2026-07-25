package companion

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const (
	// BuiltinID identifies Codog's bundled terminal companion.
	BuiltinID = "codog"
	// DisabledID is the persisted value used to disable terminal companions.
	DisabledID = "off"

	maxManifestBytes = 32 << 10
	maxFrameRows     = 4
	maxFrameColumns  = 18
)

var (
	// ErrInvalidID reports an unsafe or unsupported companion identifier.
	ErrInvalidID = errors.New("invalid companion id")
	// ErrInvalidManifest reports a malformed or unsafe companion manifest.
	ErrInvalidManifest = errors.New("invalid companion manifest")
)

// Frames contains the companion artwork for each runtime state. Missing state
// frames fall back to Ready.
type Frames struct {
	Ready   []string `json:"ready"`
	Running []string `json:"running,omitempty"`
	Waiting []string `json:"waiting,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

// Manifest describes one terminal companion.
type Manifest struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Frames Frames `json:"frames"`
}

// CatalogEntry is the public summary used by companion pickers and reports.
type CatalogEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
}

// Frame returns artwork for a normalized runtime state.
func (m Manifest) Frame(state string) []string {
	var selected []string
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running":
		selected = m.Frames.Running
	case "waiting":
		selected = m.Frames.Waiting
	case "failed":
		selected = m.Frames.Failed
	default:
		selected = m.Frames.Ready
	}
	if len(selected) == 0 {
		selected = m.Frames.Ready
	}
	return append([]string(nil), selected...)
}

// Load resolves a built-in or local custom companion. Empty and "off" values
// return nil without error.
func Load(configHome string, id string) (*Manifest, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" || id == DisabledID {
		return nil, nil
	}
	if id == BuiltinID {
		manifest := builtin()
		return &manifest, nil
	}
	if !validID(id) {
		return nil, ErrInvalidID
	}
	path := filepath.Join(configHome, "pets", id, "pet.json")
	data, err := readManifest(configHome, path)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoderHasTrailingValue(decoder) {
		return nil, ErrInvalidManifest
	}
	manifest.ID = strings.ToLower(strings.TrimSpace(manifest.ID))
	manifest.Name = strings.TrimSpace(manifest.Name)
	if manifest.ID != id {
		return nil, fmt.Errorf("%w: id does not match directory", ErrInvalidManifest)
	}
	if manifest.Name == "" {
		manifest.Name = manifest.ID
	}
	if err := validateFrames(manifest.Frames); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func readManifest(configHome string, path string) ([]byte, error) {
	parentInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidManifest
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxManifestBytes {
		return nil, ErrInvalidManifest
	}
	if !resolvedWithin(configHome, path) {
		return nil, ErrInvalidManifest
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestBytes {
		return nil, ErrInvalidManifest
	}
	return data, nil
}

func resolvedWithin(root string, path string) bool {
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedPath, pathErr := filepath.EvalSymlinks(path)
	if rootErr != nil || pathErr != nil {
		return false
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func decoderHasTrailingValue(decoder *json.Decoder) bool {
	var extra any
	return decoder.Decode(&extra) != io.EOF
}

func validID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 48 {
		return false
	}
	for _, char := range id {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validateFrames(frames Frames) error {
	if len(frames.Ready) == 0 {
		return fmt.Errorf("%w: ready frame is required", ErrInvalidManifest)
	}
	for _, frame := range [][]string{frames.Ready, frames.Running, frames.Waiting, frames.Failed} {
		if err := validateFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

func validateFrame(lines []string) error {
	if len(lines) > maxFrameRows {
		return fmt.Errorf("%w: frame exceeds %d rows", ErrInvalidManifest, maxFrameRows)
	}
	for _, line := range lines {
		if !utf8.ValidString(line) || ansi.StringWidth(line) > maxFrameColumns || containsTerminalControl(line) {
			return fmt.Errorf("%w: unsafe frame content", ErrInvalidManifest)
		}
	}
	return nil
}

func containsTerminalControl(value string) bool {
	for _, char := range value {
		if char == '\x1b' || char == '\x7f' || char < ' ' {
			return true
		}
	}
	return false
}

// List returns the built-in companion and all valid local manifests.
func List(configHome string) []CatalogEntry {
	entries := []CatalogEntry{{ID: BuiltinID, Name: "Codog", Builtin: true}}
	root := filepath.Join(configHome, "pets")
	directories, err := os.ReadDir(root)
	if err != nil {
		return entries
	}
	for _, directory := range directories {
		if !directory.IsDir() || !validID(directory.Name()) || directory.Name() == BuiltinID {
			continue
		}
		manifest, loadErr := Load(configHome, directory.Name())
		if loadErr != nil || manifest == nil {
			continue
		}
		entries = append(entries, CatalogEntry{ID: manifest.ID, Name: manifest.Name})
	}
	sort.Slice(entries[1:], func(i, j int) bool {
		return entries[i+1].ID < entries[j+1].ID
	})
	return entries
}

func builtin() Manifest {
	return Manifest{
		ID:   BuiltinID,
		Name: "Codog",
		Frames: Frames{
			Ready:   []string{` / \__`, `(    @\___`, ` /         O`},
			Running: []string{` / \__  >>`, `(    @\___`, ` /         O`},
			Waiting: []string{` / \__  ?`, `(    @\___`, ` /         O`},
			Failed:  []string{` / \__  !`, `(    x\___`, ` /         O`},
		},
	}
}

// WriteExample writes a documented custom companion manifest to w.
func WriteExample(w io.Writer) error {
	manifest := Manifest{
		ID:   "helper",
		Name: "Helper",
		Frames: Frames{
			Ready:   []string{"[o_o]", " /|\\"},
			Running: []string{"[o_o] >", " /|\\"},
			Waiting: []string{"[o_o] ?", " /|\\"},
			Failed:  []string{"[x_x] !", " /|\\"},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(w)
	if _, err := writer.Write(append(data, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}
