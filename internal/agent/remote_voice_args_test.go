package agent

import (
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestParseVoiceArgsOptionsAndActions(t *testing.T) {
	req, err := parseVoiceArgs([]string{
		"set-command", "speech", "to", "text",
		"--target", "project", "--path=project.json",
		"--input", "sample", "--timeout-ms=250", "-o", "json",
	})
	require.NoError(t, err)
	require.Equal(t, voiceRequest{
		Action: "set-command", Format: "json", Target: "project", Path: "project.json",
		Command: "speech to text", Input: "sample", TimeoutMS: 250,
	}, req)

	req, err = parseVoiceArgs([]string{"listen", "--stdin=audio", "--command=listener", "--json"})
	require.NoError(t, err)
	require.Equal(t, "listen", req.Action)
	require.Equal(t, "audio", req.Input)
	require.Equal(t, "listener", req.Command)
	require.Equal(t, "json", req.Format)
}

func TestParseVoiceArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing target", args: []string{"--target", "--json"}, want: "missing_flag_value: --target requires a value"},
		{name: "missing command", args: []string{"set-command"}, want: "missing_argument: COMMAND is required"},
		{name: "negative timeout", args: []string{"--timeout-ms=-1"}, want: "timeout-ms must be a non-negative integer"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for voice"},
		{name: "extra action", args: []string{"status", "extra"}, want: "unexpected argument"},
		{name: "invalid format", args: []string{"--output-format=yaml"}, want: "unknown voice output format \"yaml\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseVoiceArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseRemoteEnvArgsOptionsAndAliases(t *testing.T) {
	req, err := parseRemoteEnvArgs([]string{
		"reset", "--target=project", "--path", "config.json",
		"--enabled=off", "--auth-token", "secret", "--clear-auth-token",
		"--lease-seconds", "90", "--output-format=json",
	})
	require.NoError(t, err)
	require.Equal(t, remoteEnvRequest{
		Action: "clear", Format: "json", Target: "project", Path: "config.json",
		SetEnabled: true, Enabled: false, AuthToken: "secret", ClearToken: true,
		SetLease: true, LeaseSeconds: 90,
	}, req)

	req, err = parseRemoteEnvArgs([]string{"status"})
	require.NoError(t, err)
	require.Equal(t, remoteEnvRequest{Action: "show", Format: "text", Target: "user"}, req)
}

func TestParseRemoteEnvArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing path", args: []string{"--path", ""}, want: "missing_flag_value: --path requires a value"},
		{name: "missing token before format", args: []string{"--auth-token", "--json"}, want: "missing_flag_value: --auth-token requires a value"},
		{name: "invalid enabled", args: []string{"--enabled=maybe"}, want: "unknown boolean value \"maybe\""},
		{name: "negative lease", args: []string{"--lease-seconds", "-1"}, want: "lease-seconds must be a non-negative integer"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for remote-env"},
		{name: "extra action", args: []string{"show", "set"}, want: "unexpected argument"},
		{name: "unknown action", args: []string{"inspect"}, want: "unknown remote-env command \"inspect\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseRemoteEnvArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseRemoteSetupArgsOptionsAndDefaults(t *testing.T) {
	req, err := parseRemoteSetupArgs([]string{
		"setup", "--addr=:9000", "--target", "local", "--path=config.json",
		"--auth-token", "secret", "--lease-seconds=120", "--resume", "resumed", "--json",
	}, config.FlagOverrides{SessionID: "default"})
	require.NoError(t, err)
	require.Equal(t, remoteSetupRequest{
		Action: "enable", Format: "json", Addr: ":9000", Target: "local", Path: "config.json",
		AuthToken: "secret", SetLease: true, LeaseSeconds: 120, SessionID: "resumed",
	}, req)

	req, err = parseRemoteSetupArgs(nil, config.FlagOverrides{Resume: "resume-default", SessionID: "session-default"})
	require.NoError(t, err)
	require.Equal(t, remoteSetupRequest{
		Action: "status", Format: "text", Addr: "127.0.0.1:8791", Target: "user", SessionID: "resume-default",
	}, req)

	req, err = parseRemoteSetupArgs([]string{"--clear-auth-token"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "enable", req.Action)
}

func TestParseRemoteSetupArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing address", args: []string{"--addr", ""}, want: "missing_flag_value: --addr requires a value"},
		{name: "missing session before format", args: []string{"--session", "--json"}, want: "missing_flag_value: --session requires a value"},
		{name: "negative lease", args: []string{"--lease-seconds=-1"}, want: "lease-seconds must be a non-negative integer"},
		{name: "set and clear token", args: []string{"--auth-token=secret", "--clear-auth-token"}, want: "cannot set and clear auth token"},
		{name: "disable write", args: []string{"disable", "--lease-seconds=1"}, want: "disable only accepts --clear-auth-token"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for remote-setup"},
		{name: "extra action", args: []string{"status", "enable"}, want: "unexpected argument"},
		{name: "unknown action", args: []string{"inspect"}, want: "unknown remote-setup command \"inspect\""},
		{name: "invalid format", args: []string{"--output-format=yaml"}, want: "unknown remote-setup output format \"yaml\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseRemoteSetupArgs(test.args, config.FlagOverrides{})
			require.ErrorContains(t, err, test.want)
		})
	}
}
