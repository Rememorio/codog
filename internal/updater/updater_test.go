package updater

import (
	"context"
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
