package oauth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
