package frontmatter

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func Parse(text string) (string, map[string]any, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return text, nil, nil
	}
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(strings.TrimSuffix(lines[index], "\r")) != "---" {
			continue
		}
		source := strings.Join(lines[1:index], "\n")
		body := strings.Join(lines[index+1:], "\n")
		values := map[string]any{}
		if err := yaml.Unmarshal([]byte(source), &values); err != nil {
			return body, nil, err
		}
		return body, values, nil
	}
	return text, nil, nil
}

func String(values map[string]any, key string) string {
	return strings.TrimSpace(ScalarString(values[key]))
}

func FirstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		value := String(values, key)
		if value != "" {
			return value
		}
	}
	return ""
}

func ScalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func StringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		out := []string{}
		for _, item := range typed {
			out = append(out, splitCommaOrNewline(ScalarString(item))...)
		}
		return CompactStrings(out)
	case []string:
		out := []string{}
		for _, item := range typed {
			out = append(out, splitCommaOrNewline(item)...)
		}
		return CompactStrings(out)
	case string:
		return CompactStrings(splitCommaOrNewline(typed))
	default:
		return CompactStrings(splitCommaOrNewline(ScalarString(typed)))
	}
}

func ArgumentList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any, []string:
		return StringList(value)
	case string:
		return CompactStrings(strings.Fields(strings.NewReplacer(",", " ", "\n", " ").Replace(typed)))
	default:
		return CompactStrings(strings.Fields(ScalarString(typed)))
	}
}

func splitCommaOrNewline(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
}

func CompactStrings(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func NormalizePaths(values []string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
		value = strings.TrimSuffix(value, "/**")
		if value == "" || value == "**" {
			continue
		}
		out = append(out, value)
	}
	return CompactStrings(out)
}

func Bool(value any) (bool, bool) {
	switch typed := value.(type) {
	case nil:
		return false, false
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "yes", "y", "1", "on":
			return true, true
		case "false", "no", "n", "0", "off":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func DescriptionFromMarkdown(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
