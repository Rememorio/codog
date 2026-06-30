package shellstate

import (
	"os"
	"path/filepath"
	"strings"
)

func Dir(configHome string, sessionID string) string {
	configHome = strings.TrimSpace(configHome)
	sessionID = safeSessionID(sessionID)
	if configHome == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(configHome, "session-state", sessionID)
}

func CWDPath(configHome string, sessionID string) string {
	dir := Dir(configHome, sessionID)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "cwd")
}

func CurrentCWD(configHome string, sessionID string, fallback string) (string, error) {
	fallback, err := normalizeDir(fallback)
	if err != nil {
		return "", err
	}
	path := CWDPath(configHome, sessionID)
	if path == "" {
		return fallback, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fallback, nil
		}
		return "", err
	}
	cwd, err := normalizeDir(strings.TrimSpace(string(data)))
	if err != nil {
		return fallback, nil
	}
	return cwd, nil
}

func SaveCWD(configHome string, sessionID string, cwd string) (string, error) {
	path := CWDPath(configHome, sessionID)
	if path == "" {
		return "", nil
	}
	cwd, err := normalizeDir(cwd)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return cwd, os.WriteFile(path, []byte(cwd+"\n"), 0o600)
}

func normalizeDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", os.ErrInvalid
	}
	return abs, nil
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
