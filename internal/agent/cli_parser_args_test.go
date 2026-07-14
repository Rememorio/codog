package agent

import (
	"testing"

	"github.com/Rememorio/codog/internal/greencontract"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/stretchr/testify/require"
)

func TestParseSSHArgsOptions(t *testing.T) {
	req, err := parseSSHArgs([]string{
		"host.example", "/srv/app", "-p=inspect", "--continue", "--resume", "latest",
		"--model=gpt", "--permission-mode", "workspace-write", "--plan-mode-required",
		"--skip-permissions", "--local", "--run", "--output-format=json",
	})
	require.NoError(t, err)
	require.Equal(t, "host.example", req.Host)
	require.Equal(t, "/srv/app", req.Directory)
	require.True(t, req.Print)
	require.Equal(t, "inspect", req.Prompt)
	require.Equal(t, []string{"--continue", "--resume", "latest", "--model", "gpt"}, req.ExtraArgs)
	require.Equal(t, "workspace-write", req.PermissionMode)
	require.True(t, req.PlanModeRequired)
	require.True(t, req.DangerouslySkipPermissions)
	require.True(t, req.Local)
	require.True(t, req.Execute)
	require.Equal(t, "json", req.Format)
}

func TestParseSSHArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing host", want: "host is required"},
		{name: "extra positional", args: []string{"host", "dir", "extra"}, want: "unexpected argument"},
		{name: "missing resume", args: []string{"host", "--resume="}, want: "--resume requires a value"},
		{name: "missing model", args: []string{"host", "--model", "--json"}, want: "--model requires a value"},
		{name: "invalid permission mode", args: []string{"host", "--permission-mode=admin"}, want: "invalid --permission-mode"},
		{name: "unknown option", args: []string{"host", "--unknown"}, want: "unknown option \"--unknown\" for ssh"},
		{name: "invalid format", args: []string{"host", "--output-format=yaml"}, want: "unknown ssh output format \"yaml\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseSSHArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseGreenContractArgsOptions(t *testing.T) {
	req, err := parseGreenContractArgs([]string{
		"verify", "--merge-ready", "--required=workspace", "--level=targeted-tests",
		"--test-command", "go test ./...", "--failed-test-command=go test ./broken",
		"--test-result=lint=2", "--base-fresh", "--recovery-attempt-context",
		"--known-flake=TestFlaky", "--blocking-flake", "TestBlocking", "--json",
	})
	require.NoError(t, err)
	require.Equal(t, "check", req.Action)
	require.Equal(t, "json", req.Format)
	require.Equal(t, greencontract.LevelWorkspace, req.RequiredLevel)
	require.Equal(t, greencontract.LevelTargetedTests, req.ObservedLevel)
	require.True(t, req.MergeReady)
	require.True(t, req.BaseBranchFresh)
	require.True(t, req.RecoveryAttemptContextRecorded)
	require.Len(t, req.TestCommands, 3)
	require.Equal(t, 2, req.TestCommands[2].ExitCode)
	require.Len(t, req.KnownFlakes, 2)
	require.True(t, req.KnownFlakes[1].BlocksGreen)
}

func TestParseGreenContractArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing level", args: []string{"--required"}, want: "required level is required"},
		{name: "bad result", args: []string{"--test-result=lint"}, want: "must use COMMAND=EXIT"},
		{name: "bad level", args: []string{"--observed=unknown"}, want: "unknown green level \"unknown\""},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown green-contract flag"},
		{name: "unknown action", args: []string{"inspect"}, want: "usage: codog green-contract"},
		{name: "invalid format", args: []string{"--output-format=yaml"}, want: "unknown green-contract output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseGreenContractArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseReportSchemaArgsOptions(t *testing.T) {
	req, err := parseReportSchemaArgs([]string{
		"projection", "--input", `{\"schema_version\":\"v1\"}`, "--view=compact",
		"--consumer", "mobile", "--report=summary", "--schema-version", "v2",
		"--field-family=claims", "--max-sensitivity=secret", "--json",
	})
	require.NoError(t, err)
	require.Equal(t, "project", req.Action)
	require.Equal(t, "json", req.Format)
	require.Equal(t, "compact", req.View)
	require.Equal(t, "mobile", req.Consumer)
	require.Equal(t, []string{"summary"}, req.ReportIDs)
	require.Equal(t, []string{"v2"}, req.SchemaVersions)
	require.True(t, req.SchemaFilter)
	require.Equal(t, []string{"claims"}, req.FieldFamilies)
	require.Equal(t, reportschema.SensitivitySecret, req.MaxSensitivity)
}

func TestParseReportSchemaArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing report", args: []string{"--report"}, want: "report id is required"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown report-schema flag"},
		{name: "unknown action", args: []string{"inspect"}, want: "unknown report-schema action"},
		{name: "extra action", args: []string{"registry", "extra"}, want: "usage: codog report-schema"},
		{name: "multiple input sources", args: []string{"canonicalize", "--input={}", "--file=data.json"}, want: "only one of --input or --file"},
		{name: "stdin with input", args: []string{"project", "--stdin", "--input={}"}, want: "--stdin only without --input or --file"},
		{name: "missing required input", args: []string{"conformance"}, want: "input is required"},
		{name: "invalid format", args: []string{"--output-format=yaml"}, want: "unknown report-schema output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseReportSchemaArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParsePerfIssueArgsOptions(t *testing.T) {
	req, err := parsePerfIssueArgs([]string{
		"--limit", "9", "--token-threshold=1000", "--tool-threshold", "20",
		"--output=reports", "--write", "--json",
	})
	require.NoError(t, err)
	require.Equal(t, perfIssueRequest{
		Format: "json", Limit: 9, Output: "reports", Write: true,
		TokenThreshold: 1000, ToolThreshold: 20,
	}, req)
}

func TestParsePerfIssueArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing limit", args: []string{"--limit", "--json"}, want: "--limit requires a value"},
		{name: "bad limit", args: []string{"--limit=0"}, want: "limit must be a positive integer"},
		{name: "bad token threshold", args: []string{"--token-threshold=-1"}, want: "token-threshold must be a positive integer"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for perf-issue"},
		{name: "extra argument", args: []string{"extra"}, want: "unexpected argument"},
		{name: "invalid format", args: []string{"--output-format=yaml"}, want: "unknown perf-issue output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parsePerfIssueArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}
