package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStartDeviceAuthorization(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/device", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "client-1", r.Form.Get("client_id"))
		require.Equal(t, "profile email", r.Form.Get("scope"))
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"` + server.URL + `/verify","verification_uri_complete":"` + server.URL + `/verify?user_code=ABCD-EFGH","expires_in":600,"interval":1}`))
	}))
	defer server.Close()

	auth, err := StartDeviceAuthorization(context.Background(), ProviderMetadata{DeviceAuthorizationEndpoint: server.URL + "/device"}, "client-1", []string{"profile", "email"})
	require.NoError(t, err)
	require.Equal(t, "device-1", auth.DeviceCode)
	require.Equal(t, "ABCD-EFGH", auth.UserCode)
	require.Equal(t, 1, auth.Interval)
	require.False(t, auth.ExpiresAt.IsZero())
}

func TestPollDeviceTokenRetriesPendingThenSavesTokenShape(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/token", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, DeviceCodeGrantType, r.Form.Get("grant_type"))
		require.Equal(t, "device-1", r.Form.Get("device_code"))
		require.Equal(t, "client-1", r.Form.Get("client_id"))
		attempts++
		w.Header().Set("content-type", "application/json")
		if attempts == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()

	var sleeps []time.Duration
	token, err := PollDeviceToken(context.Background(), ProviderMetadata{TokenEndpoint: server.URL + "/token"}, "device-1", DevicePollOptions{
		ClientID:  "client-1",
		Interval:  time.Second,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, "access-1", token.AccessToken)
	require.Equal(t, "refresh-1", token.RefreshToken)
	require.False(t, token.ExpiresAt.IsZero())
	require.Equal(t, []time.Duration{time.Second}, sleeps)
}

func TestPollDeviceTokenHandlesSlowDown(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("content-type", "application/json")
		if attempts == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"access-1"}`))
	}))
	defer server.Close()

	var sleeps []time.Duration
	_, err := PollDeviceToken(context.Background(), ProviderMetadata{TokenEndpoint: server.URL}, "device-1", DevicePollOptions{
		ClientID: "client-1",
		Interval: time.Second,
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, []time.Duration{6 * time.Second}, sleeps)
}
