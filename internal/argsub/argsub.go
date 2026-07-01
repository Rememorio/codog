package argsub

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	indexedArgumentsPattern = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`)
	numericArgumentPattern  = regexp.MustCompile(`\$(\d+)([^A-Za-z0-9_]|$)`)
)

func Parse(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, ok := parseShellLike(raw)
	if !ok {
		return strings.Fields(raw)
	}
	return parsed
}

func Substitute(content string, rawArgs string, appendIfNoPlaceholder bool, argumentNames []string) string {
	parsed := Parse(rawArgs)
	original := content
	for index, name := range argumentNames {
		name = strings.TrimSpace(name)
		if name == "" || numericName(name) {
			continue
		}
		value := ""
		if index < len(parsed) {
			value = parsed[index]
		}
		content = replaceNamedArgument(content, name, value)
	}
	content = indexedArgumentsPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := indexedArgumentsPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return ""
		}
		index, err := strconv.Atoi(parts[1])
		if err != nil || index < 0 || index >= len(parsed) {
			return ""
		}
		return parsed[index]
	})
	content = numericArgumentPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := numericArgumentPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		index, err := strconv.Atoi(parts[1])
		value := ""
		if err == nil && index >= 0 && index < len(parsed) {
			value = parsed[index]
		}
		return value + parts[2]
	})
	content = strings.ReplaceAll(content, "$ARGUMENTS", rawArgs)
	for _, marker := range []string{"{{args}}", "{{ args }}", "{{ARGUMENTS}}", "{{ ARGUMENTS }}"} {
		content = strings.ReplaceAll(content, marker, rawArgs)
	}
	if content == original && appendIfNoPlaceholder && strings.TrimSpace(rawArgs) != "" {
		content += "\n\nARGUMENTS: " + rawArgs
	}
	return content
}

func parseShellLike(raw string) ([]string, bool) {
	args := []string{}
	var current strings.Builder
	var quote rune
	escaped := false
	hasToken := false
	for _, r := range raw {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
			hasToken = true
		case quote != '\'' && r == '\\':
			escaped = true
			hasToken = true
		case quote != 0:
			if r == quote {
				quote = 0
				hasToken = true
			} else {
				current.WriteRune(r)
				hasToken = true
			}
		case r == '\'' || r == '"':
			quote = r
			hasToken = true
		case unicode.IsSpace(r):
			if hasToken {
				args = append(args, current.String())
				current.Reset()
				hasToken = false
			}
		default:
			current.WriteRune(r)
			hasToken = true
		}
	}
	if escaped || quote != 0 {
		return nil, false
	}
	if hasToken {
		args = append(args, current.String())
	}
	return args, true
}

func replaceNamedArgument(content string, name string, value string) string {
	var builder strings.Builder
	prefix := "$" + name
	for {
		index := strings.Index(content, prefix)
		if index < 0 {
			builder.WriteString(content)
			return builder.String()
		}
		builder.WriteString(content[:index])
		after := content[index+len(prefix):]
		if after == "" || !isNameContinuation(rune(after[0])) {
			builder.WriteString(value)
		} else {
			builder.WriteString(prefix)
		}
		content = after
	}
}

func isNameContinuation(r rune) bool {
	return r == '[' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func numericName(name string) bool {
	for _, r := range name {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return name != ""
}
