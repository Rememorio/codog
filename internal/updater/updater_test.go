package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.2.0","downloads":{"darwin":"https://example.invalid/codog"}}`))
	}))
	defer server.Close()

	result, err := Check(context.Background(), "0.1.0", server.URL)
	require.NoError(t, err)
	require.True(t, result.UpdateAvailable)
	require.Equal(t, "0.2.0", result.LatestVersion)
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	payload := []byte("codog binary")
	sum := sha256.Sum256(payload)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = fmt.Fprintf(w, `{"version":"0.2.0","downloads":{"test":"%s/binary"},"checksums":{"test":"sha256:%s"}}`, serverURL, hex.EncodeToString(sum[:]))
		case "/binary":
			_, _ = w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	result, err := Download(context.Background(), server.URL+"/manifest.json", "test", t.TempDir())
	require.NoError(t, err)
	require.True(t, result.Verified)
	require.Equal(t, "test", result.Platform)
	require.FileExists(t, result.Path)
	require.Equal(t, hex.EncodeToString(sum[:]), result.SHA256)
}

func TestDownloadRejectsChecksumMismatch(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = fmt.Fprintf(w, `{"version":"0.2.0","downloads":{"test":"%s/binary"},"checksums":{"test":"sha256:deadbeef"}}`, serverURL)
		case "/binary":
			_, _ = w.Write([]byte("codog binary"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	_, err := Download(context.Background(), server.URL+"/manifest.json", "test", t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "checksum mismatch")
}

func TestVerifyManifest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	manifest := Manifest{
		Version:   "0.2.0",
		Downloads: map[string]string{"test": "https://example.invalid/codog"},
	}
	payload, err := canonicalManifest(manifest)
	require.NoError(t, err)
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))

	require.NoError(t, VerifyManifest(manifest, base64.StdEncoding.EncodeToString(publicKey)))

	manifest.Version = "0.3.0"
	err = VerifyManifest(manifest, base64.StdEncoding.EncodeToString(publicKey))
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestDownloadSignedVerifiesManifest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	payload := []byte("codog binary")
	sum := sha256.Sum256(payload)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			manifest := Manifest{
				Version:   "0.2.0",
				Downloads: map[string]string{"test": serverURL + "/binary"},
				Checksums: map[string]string{"test": hex.EncodeToString(sum[:])},
			}
			data, err := canonicalManifest(manifest)
			require.NoError(t, err)
			manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data))
			require.NoError(t, json.NewEncoder(w).Encode(manifest))
		case "/binary":
			_, _ = w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	result, err := DownloadSigned(context.Background(), server.URL+"/manifest.json", "test", t.TempDir(), base64.StdEncoding.EncodeToString(publicKey))
	require.NoError(t, err)
	require.True(t, result.Verified)
	require.FileExists(t, result.Path)
}

func TestInstallAndRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "codog")
	artifact := filepath.Join(dir, "codog-new")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(artifact, []byte("new"), 0o644))

	result, err := Install(artifact, target)
	require.NoError(t, err)
	require.True(t, result.Installed)
	require.Equal(t, target+".bak", result.BackupPath)
	require.FileExists(t, target+".bak")
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "new", string(data))
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	}

	rollback, err := Rollback(target)
	require.NoError(t, err)
	require.True(t, rollback.RolledBack)
	data, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
}

func TestInstallNewTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bin", "codog")
	artifact := filepath.Join(dir, "codog-new")
	require.NoError(t, os.WriteFile(artifact, []byte("new"), 0o755))

	result, err := Install(artifact, target)
	require.NoError(t, err)
	require.True(t, result.Installed)
	require.Empty(t, result.BackupPath)
	require.FileExists(t, target)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "new", string(data))
}
