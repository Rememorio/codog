package promptrefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxFileBytes          = 32 * 1024
	maxDirectoryBytes     = 64 * 1024
	maxDirectoryFileCount = 32
)

type Reference struct {
	Token     string
	Path      string
	Resolved  string
	Bytes     int
	Truncated bool
	Directory bool
	Files     []DirectoryFile
	Skipped   []string
	Error     string
	Body      string
}

type DirectoryFile struct {
	Path      string
	Bytes     int
	Truncated bool
	Body      string
}

func Expand(input string, workspace string, additionalDirs []string) string {
	refs := References(input)
	if len(refs) == 0 {
		return input
	}
	roots := allowedRoots(workspace, additionalDirs)
	if len(roots) == 0 {
		return input
	}
	seen := map[string]bool{}
	var resolved []Reference
	for _, token := range refs {
		ref := readReference(token, roots)
		key := ref.Resolved
		if key == "" {
			key = ref.Token
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		resolved = append(resolved, ref)
	}
	if len(resolved) == 0 {
		return input
	}
	return appendReferences(input, resolved)
}

func References(input string) []string {
	var refs []string
	for i := 0; i < len(input); i++ {
		if input[i] != '@' || !isReferenceStart(input, i) {
			continue
		}
		start := i + 1
		end := start
		for end < len(input) && isReferenceChar(rune(input[end])) {
			end++
		}
		if end == start {
			continue
		}
		token := strings.TrimRight(input[start:end], ".,;:!?)")
		if token != "" {
			refs = append(refs, token)
		}
		i = end
	}
	return refs
}

func readReference(token string, roots []string) Reference {
	ref := Reference{Token: token, Path: token}
	path, err := resolvePath(token, roots)
	if err != nil {
		ref.Error = err.Error()
		return ref
	}
	ref.Resolved = path
	info, err := os.Stat(path)
	if err != nil {
		ref.Error = err.Error()
		return ref
	}
	if info.IsDir() {
		readDirectoryReference(&ref, path, roots)
		return ref
	}
	data, err := os.ReadFile(path)
	if err != nil {
		ref.Error = err.Error()
		return ref
	}
	ref.Bytes = len(data)
	if len(data) > maxFileBytes {
		data = data[:maxFileBytes]
		ref.Truncated = true
	}
	ref.Body = string(data)
	return ref
}

func readDirectoryReference(ref *Reference, path string, roots []string) {
	ref.Directory = true
	totalBytes := 0
	err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		rel, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") {
				ref.Skipped = append(ref.Skipped, rel+"/")
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			ref.Skipped = append(ref.Skipped, rel)
			return nil
		}
		if len(ref.Files) >= maxDirectoryFileCount {
			ref.Skipped = append(ref.Skipped, rel)
			return nil
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			ref.Skipped = append(ref.Skipped, rel)
			return nil
		}
		if !pathWithin(path, resolved) || !pathWithinAny(roots, resolved) {
			ref.Skipped = append(ref.Skipped, rel)
			return nil
		}
		info, err := os.Stat(resolved)
		if err != nil || info.IsDir() {
			ref.Skipped = append(ref.Skipped, rel)
			return nil
		}
		if info.Size() > maxFileBytes || totalBytes >= maxDirectoryBytes {
			ref.Skipped = append(ref.Skipped, rel)
			return nil
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			ref.Skipped = append(ref.Skipped, rel)
			return nil
		}
		if !utf8.Valid(data) {
			ref.Skipped = append(ref.Skipped, rel)
			return nil
		}
		truncated := false
		if totalBytes+len(data) > maxDirectoryBytes {
			available := maxDirectoryBytes - totalBytes
			if available <= 0 {
				ref.Skipped = append(ref.Skipped, rel)
				return nil
			}
			data = data[:available]
			truncated = true
		}
		totalBytes += len(data)
		ref.Bytes += len(data)
		ref.Truncated = ref.Truncated || truncated
		ref.Files = append(ref.Files, DirectoryFile{
			Path:      rel,
			Bytes:     int(info.Size()),
			Truncated: truncated,
			Body:      string(data),
		})
		return nil
	})
	if err != nil {
		ref.Error = err.Error()
		return
	}
	if len(ref.Files) == 0 {
		ref.Error = "directory has no supported text files"
	}
}

func resolvePath(requested string, roots []string) (string, error) {
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		if pathWithin(root, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path escapes allowed scope: %s", requested)
}

func allowedRoots(workspace string, additionalDirs []string) []string {
	roots := []string{}
	for _, root := range append([]string{workspace}, additionalDirs...) {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			continue
		}
		roots = append(roots, filepath.Clean(resolved))
	}
	return roots
}

func appendReferences(input string, refs []Reference) string {
	var builder strings.Builder
	builder.WriteString(input)
	builder.WriteString("\n\n<codog_file_references>\n")
	for _, ref := range refs {
		if ref.Directory && ref.Error == "" {
			appendDirectoryReference(&builder, ref)
			continue
		}
		builder.WriteString("<file path=\"")
		builder.WriteString(escapeAttr(ref.Path))
		builder.WriteString("\"")
		if ref.Bytes != 0 {
			builder.WriteString(fmt.Sprintf(" bytes=\"%d\"", ref.Bytes))
		}
		if ref.Truncated {
			builder.WriteString(` truncated="true"`)
		}
		if ref.Error != "" {
			builder.WriteString(" unavailable=\"")
			builder.WriteString(escapeAttr(ref.Error))
			builder.WriteString("\" />\n")
			continue
		}
		builder.WriteString(">\n")
		builder.WriteString(strings.TrimRight(ref.Body, "\n"))
		if ref.Truncated {
			builder.WriteString("\n[truncated]")
		}
		builder.WriteString("\n</file>\n")
	}
	builder.WriteString("</codog_file_references>")
	return builder.String()
}

func appendDirectoryReference(builder *strings.Builder, ref Reference) {
	builder.WriteString("<directory path=\"")
	builder.WriteString(escapeAttr(ref.Path))
	builder.WriteString("\" files=\"")
	builder.WriteString(strconv.Itoa(len(ref.Files)))
	builder.WriteString("\"")
	if ref.Bytes != 0 {
		builder.WriteString(fmt.Sprintf(" bytes=\"%d\"", ref.Bytes))
	}
	if ref.Truncated {
		builder.WriteString(` truncated="true"`)
	}
	if len(ref.Skipped) != 0 {
		builder.WriteString(fmt.Sprintf(" skipped=\"%d\"", len(ref.Skipped)))
	}
	builder.WriteString(">\n")
	for _, file := range ref.Files {
		builder.WriteString("<file path=\"")
		builder.WriteString(escapeAttr(file.Path))
		builder.WriteString("\"")
		if file.Bytes != 0 {
			builder.WriteString(fmt.Sprintf(" bytes=\"%d\"", file.Bytes))
		}
		if file.Truncated {
			builder.WriteString(` truncated="true"`)
		}
		builder.WriteString(">\n")
		builder.WriteString(strings.TrimRight(file.Body, "\n"))
		if file.Truncated {
			builder.WriteString("\n[truncated]")
		}
		builder.WriteString("\n</file>\n")
	}
	if len(ref.Skipped) != 0 {
		builder.WriteString("<skipped>\n")
		for _, skipped := range ref.Skipped {
			builder.WriteString(escapeAttr(skipped))
			builder.WriteByte('\n')
		}
		builder.WriteString("</skipped>\n")
	}
	builder.WriteString("</directory>\n")
}

func isReferenceStart(input string, index int) bool {
	if index == 0 {
		return true
	}
	prev := rune(input[index-1])
	return unicode.IsSpace(prev) || strings.ContainsRune("([{<", prev)
}

func isReferenceChar(r rune) bool {
	return !unicode.IsSpace(r) && !strings.ContainsRune("\"'`<>", r)
}

func pathWithin(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func pathWithinAny(roots []string, path string) bool {
	for _, root := range roots {
		if pathWithin(root, path) {
			return true
		}
	}
	return false
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
