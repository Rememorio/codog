package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
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
