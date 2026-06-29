package promptrefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const maxFileBytes = 32 * 1024

type Reference struct {
	Token     string
	Path      string
	Resolved  string
	Bytes     int
	Truncated bool
	Error     string
	Body      string
}

func Expand(input string, workspace string, additionalDirs []string) string {
	refs := references(input)
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

func references(input string) []string {
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
		ref.Error = "path is a directory"
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

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
