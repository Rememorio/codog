package oauth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSaveLoadDeleteToken(t *testing.T) {
	configHome := t.TempDir()
	expiresAt := time.Now().UTC().Add(time.Hour)
	token, err := SaveToken(configHome, Token{
		AccessToken:  "access-token-1234",
		RefreshToken: "refresh-token-1234",
		ExpiresAt:    expiresAt,
	})
	require.NoError(t, err)
	require.Equal(t, "Bearer", token.TokenType)

	loaded, err := LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "access-token-1234", loaded.AccessToken)
	require.False(t, loaded.Expired(time.Now().UTC()))
	view := loaded.View(time.Now().UTC())
	require.Equal(t, "acce...1234", view.AccessToken)
	require.Equal(t, "refr...1234", view.RefreshToken)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(configHome, "oauth", "token.json"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	require.NoError(t, DeleteToken(configHome))
	_, err = LoadToken(configHome)
	require.True(t, errors.Is(err, ErrNoToken))
}

func TestTokenExpiredUsesSkew(t *testing.T) {
	now := time.Now().UTC()
	require.True(t, Token{AccessToken: "token", ExpiresAt: now.Add(30 * time.Second)}.Expired(now))
	require.False(t, Token{AccessToken: "token", ExpiresAt: now.Add(5 * time.Minute)}.Expired(now))
	require.False(t, Token{AccessToken: "token"}.Expired(now))
}

func TestKeychainTokenStoreUsesSecurityWhenForced(t *testing.T) {
	t.Setenv("CODOG_OAUTH_STORAGE", "keychain")
	var stored string
	restore := fakeSecurity(t, func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(command, "add-generic-password"):
			for i, arg := range args {
				if arg == "-w" && i+1 < len(args) {
					stored = args[i+1]
					return nil, nil
				}
			}
			return nil, errors.New("missing password")
		case strings.HasPrefix(command, "find-generic-password"):
			if stored == "" {
				return nil, errors.New("missing")
			}
			return []byte(stored + "\n"), nil
		case strings.HasPrefix(command, "delete-generic-password"):
			stored = ""
			return nil, nil
		default:
			return nil, errors.New("unexpected security command")
		}
	})
	defer restore()

	configHome := t.TempDir()
	_, err := SaveToken(configHome, Token{AccessToken: "keychain-access", RefreshToken: "keychain-refresh"})
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(configHome, "oauth", "token.json"))

	loaded, err := LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "keychain-access", loaded.AccessToken)
	require.Equal(t, "keychain-refresh", loaded.RefreshToken)
	require.NoError(t, DeleteToken(configHome))
	_, err = LoadToken(configHome)
	require.True(t, errors.Is(err, ErrNoToken))
}

func TestKeychainTokenStoreFallsBackToFile(t *testing.T) {
	t.Setenv("CODOG_OAUTH_STORAGE", "keychain")
	restore := fakeSecurity(t, func(args ...string) ([]byte, error) {
		return nil, errors.New("security unavailable")
	})
	defer restore()

	configHome := t.TempDir()
	_, err := SaveToken(configHome, Token{AccessToken: "fallback-access"})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(configHome, "oauth", "token.json"))

	loaded, err := LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "fallback-access", loaded.AccessToken)
}

func fakeSecurity(t *testing.T, run func(args ...string) ([]byte, error)) func() {
	t.Helper()
	oldLookPath := securityLookPath
	oldRun := securityRun
	securityLookPath = func(file string) (string, error) {
		require.Equal(t, "security", file)
		return "/usr/bin/security", nil
	}
	securityRun = run
	return func() {
		securityLookPath = oldLookPath
		securityRun = oldRun
	}
}
