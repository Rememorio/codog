package workspaceops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const MaxFileBytes int64 = 2_000_000

type Service struct {
	Workspace string
}

type InfoResult struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type FileEntry struct {
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
}

type FilesOptions struct {
	Path          string `json:"path"`
	Pattern       string `json:"pattern"`
	Limit         int    `json:"limit"`
	IncludeHidden bool   `json:"include_hidden"`
}

type FilesResult struct {
	Root      string      `json:"root"`
	Files     []FileEntry `json:"files"`
	Truncated bool        `json:"truncated"`
}

type SearchOptions struct {
	Query         string `json:"query"`
	Path          string `json:"path"`
	Glob          string `json:"glob"`
	Regex         bool   `json:"regex"`
	Limit         int    `json:"limit"`
	IncludeHidden bool   `json:"include_hidden"`
}

type SearchMatch struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Column int    `json:"column,omitempty"`
}

type SearchResult struct {
	Matches   []SearchMatch `json:"matches"`
	Truncated bool          `json:"truncated"`
}

type ReadOptions struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type ReadResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

type WriteOptions struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type WriteResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type EditOptions struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type EditResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

type DiffOptions struct {
	Path      string `json:"path"`
	Original  string `json:"original"`
	Updated   string `json:"updated"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type DiffResult struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

func (s Service) Info() (InfoResult, error) {
	workspace, err := s.WorkspacePath()
	if err != nil {
		return InfoResult{}, err
	}
	return InfoResult{Path: workspace, Name: filepath.Base(workspace)}, nil
}

func (s Service) Files(options FilesOptions) (FilesResult, error) {
	root, relRoot, err := s.ResolveWorkspacePath(options.Path)
	if err != nil {
		return FilesResult{}, err
	}
	pattern := options.Pattern
	if pattern == "" {
		pattern = "*"
	}
	limit := BoundedLimit(options.Limit, 500, 5000)
	entries := []FileEntry{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := s.Rel(path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if shouldSkipPath(rel, entry, options.IncludeHidden) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		ok, err := PatternMatch(pattern, rel, entry.Name())
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, FileEntry{Path: rel, IsDir: entry.IsDir(), Size: info.Size(), ModTime: info.ModTime().UTC()})
		if len(entries) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return FilesResult{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return FilesResult{Root: relRoot, Files: entries, Truncated: len(entries) >= limit}, nil
}

func (s Service) Search(options SearchOptions) (SearchResult, error) {
	if options.Query == "" {
		return SearchResult{}, errors.New("query is required")
	}
	root, _, err := s.ResolveWorkspacePath(options.Path)
	if err != nil {
		return SearchResult{}, err
	}
	limit := BoundedLimit(options.Limit, 100, 1000)
	var expr *regexp.Regexp
	if options.Regex {
		expr, err = regexp.Compile(options.Query)
		if err != nil {
			return SearchResult{}, err
		}
	}
	matches := []SearchMatch{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := s.Rel(path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if shouldSkipPath(rel, entry, options.IncludeHidden) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if options.Glob != "" {
			ok, err := PatternMatch(options.Glob, rel, entry.Name())
			if err != nil || !ok {
				return err
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) > 1024*1024 || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			column := 0
			found := false
			if expr != nil {
				loc := expr.FindStringIndex(line)
				if loc != nil {
					found = true
					column = loc[0] + 1
				}
			} else if idx := strings.Index(line, options.Query); idx >= 0 {
				found = true
				column = idx + 1
			}
			if !found {
				continue
			}
			matches = append(matches, SearchMatch{Path: rel, Line: i + 1, Text: line, Column: column})
			if len(matches) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Matches: matches, Truncated: len(matches) >= limit}, nil
}

func (s Service) Read(options ReadOptions) (ReadResult, error) {
	path, rel, err := s.Resolve(options.Path, false)
	if err != nil {
		return ReadResult{}, err
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 64 * 1024
	}
	data, totalBytes, truncated, err := readWindow(path, options.Offset, limit)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{
		Path:      rel,
		Content:   string(data),
		Bytes:     totalBytes,
		Truncated: truncated,
	}, nil
}

func (s Service) Write(options WriteOptions) (WriteResult, error) {
	path, rel, err := s.Resolve(options.Path, true)
	if err != nil {
		return WriteResult{}, err
	}
	if int64(len(options.Content)) > MaxFileBytes {
		return WriteResult{}, fmt.Errorf("content exceeds maximum workspace file size of %d bytes", MaxFileBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return WriteResult{}, err
	}
	if err := os.WriteFile(path, []byte(options.Content), 0o644); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Path: rel, Bytes: len(options.Content)}, nil
}

func (s Service) Edit(options EditOptions) (EditResult, error) {
	if options.OldString == "" {
		return EditResult{}, errors.New("old_string is required")
	}
	path, rel, err := s.Resolve(options.Path, false)
	if err != nil {
		return EditResult{}, err
	}
	data, err := readEditableFile(path)
	if err != nil {
		return EditResult{}, err
	}
	content := string(data)
	count := strings.Count(content, options.OldString)
	if count == 0 {
		return EditResult{}, errors.New("old_string was not found")
	}
	if count > 1 && !options.ReplaceAll {
		return EditResult{}, fmt.Errorf("old_string appears %d times; set replace_all to true", count)
	}
	limit := 1
	if options.ReplaceAll {
		limit = -1
	}
	updated := strings.Replace(content, options.OldString, options.NewString, limit)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return EditResult{}, err
	}
	replacements := 1
	if options.ReplaceAll {
		replacements = count
	}
	return EditResult{Path: rel, Replacements: replacements}, nil
}

func (s Service) Diff(options DiffOptions) (DiffResult, error) {
	path, rel, err := s.Resolve(options.Path, false)
	if err != nil {
		return DiffResult{}, err
	}
	original := options.Original
	if original == "" {
		data, err := readEditableFile(path)
		if err != nil {
			return DiffResult{}, err
		}
		original = string(data)
	}
	if int64(len(original)) > MaxFileBytes || int64(len(options.Updated)) > MaxFileBytes {
		return DiffResult{}, fmt.Errorf("diff content exceeds maximum workspace file size of %d bytes", MaxFileBytes)
	}
	updated := options.Updated
	if updated == "" {
		if options.OldString == "" {
			return DiffResult{}, errors.New("updated or old_string is required")
		}
		if !strings.Contains(original, options.OldString) {
			return DiffResult{}, errors.New("old_string was not found")
		}
		updated = strings.Replace(original, options.OldString, options.NewString, 1)
	}
	return DiffResult{Path: rel, Diff: UnifiedDiff(rel, original, updated)}, nil
}

func readWindow(path string, offset int, limit int) ([]byte, int, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	total := info.Size()
	if offset < 0 {
		offset = 0
	}
	if int64(offset) > total {
		offset = int(total)
	}
	if limit <= 0 {
		limit = 64 * 1024
	}
	if int64(limit) > MaxFileBytes {
		limit = int(MaxFileBytes)
	}
	if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
		return nil, 0, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, 0, false, err
	}
	truncated := int64(offset)+int64(len(data)) < total
	if len(data) > limit {
		data = data[:limit]
		truncated = true
	}
	return data, safeInt64(total), truncated, nil
}

func readEditableFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxFileBytes {
		return nil, fmt.Errorf("file exceeds maximum editable size of %d bytes", MaxFileBytes)
	}
	return data, nil
}

func safeInt64(value int64) int {
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return int(value)
}

func (s Service) WorkspacePath() (string, error) {
	if strings.TrimSpace(s.Workspace) != "" {
		return filepath.Abs(s.Workspace)
	}
	return os.Getwd()
}

func (s Service) Resolve(requested string, allowMissing bool) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", "", errors.New("path is required")
	}
	workspace, err := s.WorkspacePath()
	if err != nil {
		return "", "", err
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", "", err
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)
	resolved := candidate
	if allowMissing {
		parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
		if err != nil {
			return "", "", err
		}
		resolved = filepath.Join(parent, filepath.Base(candidate))
	} else {
		resolved, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", "", err
		}
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	return resolved, filepath.ToSlash(rel), nil
}

func (s Service) ResolveWorkspacePath(requested string) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		workspace, err := s.WorkspacePath()
		if err != nil {
			return "", "", err
		}
		root, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return "", "", err
		}
		return root, ".", nil
	}
	return s.Resolve(requested, false)
}

func (s Service) Rel(path string) (string, error) {
	workspace, err := s.WorkspacePath()
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func BoundedLimit(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func PatternMatch(pattern string, rel string, base string) (bool, error) {
	target := base
	if strings.Contains(pattern, "/") {
		target = filepath.ToSlash(rel)
		pattern = filepath.ToSlash(pattern)
	}
	return filepath.Match(pattern, target)
}

func UnifiedDiff(path string, original string, updated string) string {
	if original == updated {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("--- a/" + path + "\n")
	builder.WriteString("+++ b/" + path + "\n")
	builder.WriteString("@@\n")
	oldLines := splitDiffLines(original)
	newLines := splitDiffLines(updated)
	for _, line := range oldLines {
		builder.WriteString("-" + line + "\n")
	}
	for _, line := range newLines {
		builder.WriteString("+" + line + "\n")
	}
	return builder.String()
}

func shouldSkipPath(rel string, entry os.DirEntry, includeHidden bool) bool {
	base := entry.Name()
	if entry.IsDir() && (base == ".git" || base == ".codog" || base == "node_modules") {
		return true
	}
	return !includeHidden && strings.HasPrefix(base, ".")
}

func splitDiffLines(value string) []string {
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}
