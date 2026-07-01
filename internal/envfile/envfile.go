package envfile

import (
	"os"
	"path/filepath"
	"strings"
)

func Parse(content string) map[string]string {
	values := map[string]string{}
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rawKey, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(rawKey)
		if strings.HasPrefix(key, "export ") {
			key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		}
		if key == "" {
			continue
		}
		value := strings.TrimSpace(rawValue)
		if len(value) >= 2 {
			first := value[0]
			last := value[len(value)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values
}

func Load(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return Parse(string(data)), nil
}

func Current() map[string]string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	values, err := Load(filepath.Join(cwd, ".env"))
	if err != nil {
		return nil
	}
	return values
}

func Lookup(key string, values map[string]string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), true
	}
	if value, ok := values[key]; ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), true
	}
	return "", false
}

func LookupCurrent(key string) (string, bool) {
	return Lookup(key, Current())
}
