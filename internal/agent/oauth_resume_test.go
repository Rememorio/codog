package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResumedOAuthRouteArgumentCounts(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		label     string
		supported bool
	}{
		{name: "discover", args: []string{"discover", "https://issuer.example"}, label: "/oauth discover", supported: true},
		{name: "discover missing issuer", args: []string{"discover"}, label: "/oauth discover"},
		{name: "pkce", args: []string{"pkce"}, label: "/oauth pkce", supported: true},
		{name: "pkce extras", args: []string{"pkce", "extra"}, label: "/oauth pkce"},
		{name: "status profile", args: []string{"status", "work"}, label: "/oauth status", supported: true},
		{name: "provider save", args: []string{"provider", "save", "work", "https://issuer.example", "client"}, label: "/oauth provider save", supported: true},
		{name: "provider save missing client", args: []string{"provider", "save", "work", "https://issuer.example"}, label: "/oauth provider save"},
		{name: "token save", args: []string{"token", "save", "access", "refresh", "2030-01-01T00:00:00Z"}, label: "/oauth token save", supported: true},
		{name: "token save extras", args: []string{"token", "save", "access", "refresh", "2030-01-01T00:00:00Z", "extra"}, label: "/oauth token save"},
		{name: "token revoke defaults", args: []string{"token", "revoke"}, label: "/oauth token revoke", supported: true},
		{name: "device poll", args: []string{"device", "poll", "work", "device-code"}, label: "/oauth device poll", supported: true},
		{name: "device poll missing code", args: []string{"device", "poll", "work"}, label: "/oauth device poll"},
		{name: "browser login", args: []string{"browser", "login", "work"}, label: "/oauth browser login", supported: true},
		{name: "browser login extras", args: []string{"browser", "login", "work", "127.0.0.1:0", "extra"}, label: "/oauth browser login"},
		{name: "unknown", args: []string{"unknown"}, label: "/oauth unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			label, supported := resumedOAuthRoute(test.args)
			require.Equal(t, test.label, label)
			require.Equal(t, test.supported, supported)
		})
	}
}

func TestResumedOAuthTokenRoutePreservesUnsupportedLabel(t *testing.T) {
	label, supported := resumedOAuthRoute([]string{"token", "UnknownAction"})
	require.False(t, supported)
	require.Equal(t, "/oauth token UnknownAction", label)
}
