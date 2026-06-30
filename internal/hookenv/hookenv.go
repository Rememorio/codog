package hookenv

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func Dir(configHome string, sessionID string) string {
	configHome = strings.TrimSpace(configHome)
	sessionID = strings.TrimSpace(sessionID)
	if configHome == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(configHome, "session-env", safeSessionID(sessionID))
}

func Path(configHome string, sessionID string, event string, index int) (string, error) {
	if !EventSupportsFile(event) {
		return "", nil
	}
	dir := Dir(configHome, sessionID)
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if index < 0 {
		index = 0
	}
	return filepath.Join(dir, normalizeEvent(event)+"-hook-"+strconv.Itoa(index)+".sh"), nil
}

func Load(configHome string, sessionID string) ([]string, error) {
	var out []string
	if external := strings.TrimSpace(os.Getenv("CLAUDE_ENV_FILE")); external != "" {
		data, err := os.ReadFile(external)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		out = append(out, Parse(string(data))...)
	}

	dir := Dir(configHome, sessionID)
	if dir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if !isHookEnvFile(entry.Name()) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.SliceStable(names, func(i, j int) bool {
		return hookEnvSortKey(names[i]) < hookEnvSortKey(names[j])
	})
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, Parse(string(data))...)
	}
	return out, nil
}

func Parse(data string) []string {
	var out []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[len("export"):])
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			continue
		}
		out = append(out, key+"="+unquoteShellValue(strings.TrimSpace(value)))
	}
	return out
}

func Merge(base []string, overlay []string) []string {
	out := append([]string(nil), base...)
	indexes := map[string]int{}
	for index, item := range out {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			indexes[key] = index
		}
	}
	for _, item := range overlay {
		key, _, ok := strings.Cut(item, "=")
		if !ok || !validEnvKey(key) {
			continue
		}
		if index, found := indexes[key]; found {
			out[index] = item
			continue
		}
		indexes[key] = len(out)
		out = append(out, item)
	}
	return out
}

func EventSupportsFile(event string) bool {
	switch normalizeEvent(event) {
	case "setup", "sessionstart", "cwdchanged", "filechanged":
		return true
	default:
		return false
	}
}

func normalizeEvent(event string) string {
	event = strings.ToLower(strings.TrimSpace(event))
	event = strings.ReplaceAll(event, "_", "")
	event = strings.ReplaceAll(event, "-", "")
	if event == "" {
		return "hook"
	}
	return event
}

func hookEnvSortKey(name string) string {
	prefix := name
	if index := strings.Index(prefix, "-hook-"); index >= 0 {
		prefix = prefix[:index]
	}
	return hookPriority(prefix) + ":" + name
}

func hookPriority(prefix string) string {
	switch prefix {
	case "setup":
		return "00"
	case "sessionstart":
		return "01"
	case "cwdchanged":
		return "02"
	case "filechanged":
		return "03"
	default:
		return "99"
	}
}

func isHookEnvFile(name string) bool {
	if !strings.HasSuffix(name, ".sh") {
		return false
	}
	prefix, index, ok := strings.Cut(name, "-hook-")
	if !ok || hookPriority(prefix) == "99" {
		return false
	}
	index = strings.TrimSuffix(index, ".sh")
	_, err := strconv.Atoi(index)
	return err == nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for index, r := range key {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	first := key[0]
	return first == '_' || (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')
}

func unquoteShellValue(value string) string {
	if len(value) < 2 {
		return value
	}
	quote := value[0]
	if quote != '\'' && quote != '"' {
		return value
	}
	if value[len(value)-1] != quote {
		return value
	}
	value = value[1 : len(value)-1]
	if quote == '"' {
		value = strings.ReplaceAll(value, `\"`, `"`)
		value = strings.ReplaceAll(value, `\\`, `\`)
		value = strings.ReplaceAll(value, `\$`, `$`)
	}
	return value
}

func safeSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range sessionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "session"
	}
	return builder.String()
}
