package agent

import (
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestParseAPIKeyArgsOptions(t *testing.T) {
	req, err := parseAPIKeyArgs([]string{"set", "secret", "--target=project", "--path", "config.json", "--json"})
	require.NoError(t, err)
	require.Equal(t, apiKeyRequest{Action: "set", Key: "secret", Format: "json", Target: "project", Path: "config.json"}, req)

	req, err = parseAPIKeyArgs([]string{"--key=inline"})
	require.NoError(t, err)
	require.Equal(t, "set", req.Action)
	require.Equal(t, "inline", req.Key)
}

func TestParseAPIKeyArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing key", args: []string{"set"}, want: "KEY is required"},
		{name: "missing target", args: []string{"--target", "--json"}, want: "--target requires a value"},
		{name: "key with status", args: []string{"status", "--key=secret"}, want: "unexpected argument"},
		{name: "extra set arg", args: []string{"set", "one", "two"}, want: "KEY is required"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for api-key"},
		{name: "bad format", args: []string{"--output-format=yaml"}, want: "unknown api-key output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseAPIKeyArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseTemperatureArgsOptions(t *testing.T) {
	req, err := parseTemperatureArgs([]string{"set", "0.7", "--target", "local", "--path=config.json", "--json"})
	require.NoError(t, err)
	require.Equal(t, "set", req.Action)
	require.InDelta(t, 0.7, req.Value, 0.0001)
	require.Equal(t, "local", req.Target)
	require.Equal(t, "config.json", req.Path)
	require.Equal(t, "json", req.Format)

	req, err = parseTemperatureArgs([]string{"default"})
	require.NoError(t, err)
	require.Equal(t, "clear", req.Action)
}

func TestParseTemperatureArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing value", args: []string{"set"}, want: "VALUE is required"},
		{name: "bad value", args: []string{"1.5"}, want: "must be between 0 and 1"},
		{name: "not a number", args: []string{"warm"}, want: "must be a number"},
		{name: "extra value", args: []string{"0.5", "0.6"}, want: "unexpected argument"},
		{name: "missing path", args: []string{"--path", "--json"}, want: "--path requires a value"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for temperature"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseTemperatureArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParsePermissionsArgsOptions(t *testing.T) {
	req, err := parsePermissionsArgs([]string{"set", "read-only", "--target=project", "--path", "config.json"})
	require.NoError(t, err)
	require.Equal(t, permissionsRequest{Action: "set", Mode: "read-only", Format: "text", Target: "project", Path: "config.json"}, req)

	req, err = parsePermissionsArgs(nil)
	require.NoError(t, err)
	require.Equal(t, "show", req.Action)
	require.Equal(t, "json", req.Format)
}

func TestParsePermissionsArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing mode", args: []string{"set"}, want: "MODE is required"},
		{name: "bad mode", args: []string{"administrator"}, want: "unknown permission mode"},
		{name: "extra show arg", args: []string{"show", "extra"}, want: "unexpected argument"},
		{name: "missing target", args: []string{"--target"}, want: "target is required"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown permissions flag"},
		{name: "bad format", args: []string{"--output-format=yaml"}, want: "unknown permissions output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parsePermissionsArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseThemeAndEffortArgsOptions(t *testing.T) {
	theme, err := parseThemeArgs([]string{"use", "dark", "--target=project", "--path", "config.json", "--json"})
	require.NoError(t, err)
	require.Equal(t, themeRequest{Action: "set", Name: "dark", Format: "json", Target: "project", Path: "config.json"}, theme)

	effort, err := parseEffortArgs([]string{"set", "high", "--target", "local", "--output-format=json"}, "effort")
	require.NoError(t, err)
	require.Equal(t, effortRequest{Action: "set", Level: "high", Format: "json", Target: "local"}, effort)
}

func TestParseThemeAndEffortArgsFailures(t *testing.T) {
	tests := []struct {
		name    string
		command string
		args    []string
		want    string
	}{
		{name: "theme missing name", command: "theme", args: []string{"set"}, want: "NAME is required"},
		{name: "theme extra", command: "theme", args: []string{"list", "extra"}, want: "unexpected argument"},
		{name: "effort missing level", command: "effort", args: []string{"set"}, want: "LEVEL is required"},
		{name: "effort extra", command: "effort", args: []string{"status", "extra"}, want: "unexpected argument"},
		{name: "reasoning missing target", command: "reasoning", args: []string{"--target", "--json"}, want: "--target requires a value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.command == "theme" {
				_, err := parseThemeArgs(test.args)
				require.ErrorContains(t, err, test.want)
				return
			}
			_, err := parseEffortArgs(test.args, test.command)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseAntTraceArgsOptions(t *testing.T) {
	req, err := parseAntTraceArgs([]string{
		"fallback", "message", "--no-request", "--write", "--timeout-ms=2500",
		"--model", "model-1", "--base-url=https://example.test", "--provider=openai",
		"--output", "trace.md", "--json",
	})
	require.NoError(t, err)
	require.Equal(t, "fallback message", req.Message)
	require.True(t, req.NoRequest)
	require.True(t, req.Write)
	require.Equal(t, 2500, req.TimeoutMS)
	require.Equal(t, "model-1", req.Model)
	require.Equal(t, "https://example.test", req.BaseURL)
	require.Equal(t, "openai", req.Provider)
	require.Equal(t, "trace.md", req.Output)
	require.Equal(t, "json", req.Format)
}

func TestParseAntTraceArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing model", args: []string{"--model", "--json"}, want: "--model requires a value"},
		{name: "bad timeout", args: []string{"--timeout-ms=0"}, want: "ant-trace timeout must be positive"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for ant-trace"},
		{name: "bad format", args: []string{"--output-format=yaml"}, want: "unknown ant-trace output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseAntTraceArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseMockLimitsArgsOptions(t *testing.T) {
	req, err := parseMockLimitsArgs([]string{"serve", "--addr=:9000", "--failures", "3", "--retry-after-ms=250", "--text", "ready", "--json"})
	require.NoError(t, err)
	require.Equal(t, mockLimitsRequest{Action: "serve", Format: "json", Addr: ":9000", Failures: 3, RetryAfterMS: 250, Text: "ready"}, req)

	req, err = parseMockLimitsArgs([]string{"localhost:8080"})
	require.NoError(t, err)
	require.Equal(t, "serve", req.Action)
	require.Equal(t, "localhost:8080", req.Addr)
}

func TestParseMockLimitsArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing failures", args: []string{"--failures", "--json"}, want: "--failures requires a value"},
		{name: "bad failures", args: []string{"--failures=bad"}, want: "failures must be an integer"},
		{name: "negative failures", args: []string{"--failures=-1"}, want: "failures must be non-negative"},
		{name: "bad retry", args: []string{"--retry-after-ms=bad"}, want: "retry-after must be an integer"},
		{name: "negative retry", args: []string{"--retry-after-ms=-1"}, want: "retry-after must be non-negative"},
		{name: "unexpected", args: []string{"unknown"}, want: "unexpected argument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseMockLimitsArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseMobileHandoffArgsOptions(t *testing.T) {
	req, err := parseMobileHandoffArgs([]string{"status", "ios", "--addr=:9000", "--resume", "session-2", "--json"}, config.FlagOverrides{SessionID: "default"})
	require.NoError(t, err)
	require.Equal(t, mobileHandoffRequest{Action: "status", Platform: "ios", Format: "json", Addr: ":9000", SessionID: "session-2"}, req)
}

func TestParseMobileHandoffArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing address", args: []string{"--addr", "--json"}, want: "--addr requires a value"},
		{name: "two actions", args: []string{"status", "clear"}, want: "unexpected argument"},
		{name: "two platforms", args: []string{"ios", "android"}, want: "unexpected argument"},
		{name: "unknown positional", args: []string{"desktop"}, want: "unexpected argument"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for mobile"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseMobileHandoffArgs(test.args, config.FlagOverrides{})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseCronArgsOptions(t *testing.T) {
	req, err := parseCronArgsWithDefault([]string{
		"create", "0 * * * *", "run", "checks", "--description=hourly",
		"--now", "2026-07-14T12:00:00Z", "--json",
	}, "text")
	require.NoError(t, err)
	require.Equal(t, "create", req.Action)
	require.Equal(t, "0 * * * *", req.Schedule)
	require.Equal(t, "run checks", req.Prompt)
	require.Equal(t, "hourly", req.Description)
	require.Equal(t, "json", req.Format)
	require.Equal(t, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), req.Now)

	req, err = parseCronArgsWithDefault([]string{"get", "cron-1"}, "json")
	require.NoError(t, err)
	require.Equal(t, "show", req.Action)
	require.Equal(t, "cron-1", req.ID)
	require.Equal(t, "json", req.Format)
}

func TestParseCronArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown action", args: []string{"inspect"}, want: "unknown cron command"},
		{name: "missing create prompt", args: []string{"create", "daily"}, want: "SCHEDULE PROMPT is required"},
		{name: "missing id", args: []string{"delete"}, want: "CRON_ID is required"},
		{name: "extra due arg", args: []string{"due", "extra"}, want: "unexpected argument"},
		{name: "bad now", args: []string{"list", "--now=tomorrow"}, want: "must be RFC3339"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for cron"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCronArgsWithDefault(test.args, "text")
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseBackgroundHeartbeatArgsOptions(t *testing.T) {
	id, heartbeat, err := parseBackgroundHeartbeatArgs([]string{
		"task-1", "--status=running", "--transport-alive=false",
		"--observed-at", "2026-07-14T12:00:00Z", "--source-kind=worker",
		"--environment", "prod", "--channel=remote", "--emitter", "agent", "--confidence=high",
	})
	require.NoError(t, err)
	require.Equal(t, "task-1", id)
	require.Equal(t, "running", heartbeat.Status)
	require.False(t, heartbeat.TransportAlive)
	require.Equal(t, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), heartbeat.ObservedAt)
	require.Equal(t, "worker", heartbeat.Provenance.SourceKind)
	require.Equal(t, "prod", heartbeat.Provenance.Environment)
	require.Equal(t, "remote", heartbeat.Provenance.Channel)
	require.Equal(t, "agent", heartbeat.Provenance.Emitter)
	require.Equal(t, "high", heartbeat.Provenance.Confidence)
}

func TestParseBackgroundHeartbeatArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing id", want: "usage: codog background heartbeat"},
		{name: "empty id", args: []string{" "}, want: "task id is required"},
		{name: "missing status", args: []string{"task", "--status"}, want: "missing value for --status"},
		{name: "bad alive", args: []string{"task", "--transport-alive=maybe"}, want: "invalid syntax"},
		{name: "bad timestamp", args: []string{"task", "--observed-at=tomorrow"}, want: "cannot parse"},
		{name: "unknown option", args: []string{"task", "--unknown"}, want: "unknown heartbeat argument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseBackgroundHeartbeatArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}
