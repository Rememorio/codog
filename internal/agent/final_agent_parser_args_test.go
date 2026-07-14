package agent

import (
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestParseMarketplaceSourcesArgsOptions(t *testing.T) {
	req, err := parseMarketplaceSourcesArgs([]string{"set", "https://example.test/index.json", "key-data", "--target=project", "--path", "sources.json"})
	require.NoError(t, err)
	require.Equal(t, marketplaceSourcesRequest{
		Action: "add", Target: "project", Path: "sources.json",
		URL: "https://example.test/index.json", PublicKey: "key-data",
	}, req)

	req, err = parseMarketplaceSourcesArgs([]string{"rm", "https://example.test/index.json"})
	require.NoError(t, err)
	require.Equal(t, "remove", req.Action)
}

func TestParseMarketplaceSourcesArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing target", args: []string{"--target"}, want: "target is required"},
		{name: "bad target", args: []string{"--target=team"}, want: "unknown marketplace sources target"},
		{name: "missing add URL", args: []string{"add"}, want: "sources add URL"},
		{name: "duplicate key", args: []string{"add", "https://example.test", "positional", "--key=inline"}, want: "unexpected marketplace sources argument"},
		{name: "remove extras", args: []string{"remove", "one", "two"}, want: "sources remove URL"},
		{name: "unknown flag", args: []string{"--unknown"}, want: "unknown marketplace sources flag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseMarketplaceSourcesArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseThinkBackArgsOptions(t *testing.T) {
	req, err := parseThinkBackArgs([]string{"--year=2025", "--limit", "12", "--output=report.md", "--json"})
	require.NoError(t, err)
	require.Equal(t, thinkBackRequest{Format: "json", Year: 2025, Limit: 12, Output: "report.md"}, req)
}

func TestParseThinkBackArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing year", args: []string{"--year", "--json"}, want: "--year requires a value"},
		{name: "bad year", args: []string{"--year=1999"}, want: "four digit year"},
		{name: "bad limit", args: []string{"--limit=0"}, want: "limit must be a positive integer"},
		{name: "extra", args: []string{"2025"}, want: "unexpected argument"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for think-back"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseThinkBackArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseCompactArgsOptions(t *testing.T) {
	req, err := parseCompactArgs([]string{"--session=session-2", "--keep", "25", "--json"}, config.FlagOverrides{Resume: "resume-default"}, 10)
	require.NoError(t, err)
	require.Equal(t, compactRequest{Format: "json", Session: "session-2", Keep: 25}, req)

	req, err = parseCompactArgs(nil, config.FlagOverrides{}, 0)
	require.NoError(t, err)
	require.Equal(t, "latest", req.Session)
	require.Equal(t, 40, req.Keep)
}

func TestParseCompactArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing keep", args: []string{"--keep", "--json"}, want: "--keep requires a value"},
		{name: "bad keep", args: []string{"--keep=bad"}, want: "compact keep count must be"},
		{name: "missing session", args: []string{"--session", "--json"}, want: "--session requires a value"},
		{name: "extra", args: []string{"session"}, want: "unexpected argument"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for compact"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCompactArgs(test.args, config.FlagOverrides{}, 10)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseBranchLockArgsOptions(t *testing.T) {
	req, err := parseBranchLockArgs([]string{"detect", `[{\"branch\":\"main\"}]`, "--json"})
	require.NoError(t, err)
	require.Equal(t, "check", req.Action)
	require.Equal(t, `[{\"branch\":\"main\"}]`, req.Input)
	require.Equal(t, "json", req.Format)

	req, err = parseBranchLockArgs([]string{"collisions", "intents.json"})
	require.NoError(t, err)
	require.Equal(t, "intents.json", req.File)
}

func TestParseBranchLockArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing file", args: []string{"--file"}, want: "file is required"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown branch-lock flag"},
		{name: "too many inputs", args: []string{"check", "one", "two"}, want: "usage: codog branch-lock"},
		{name: "mixed input", args: []string{"check", "file.json", "--input=[]"}, want: "only one of --input or --file"},
		{name: "stdin input", args: []string{"check", "--stdin", "[]"}, want: "--stdin only without"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseBranchLockArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseStaleBaseArgsOptions(t *testing.T) {
	req, err := parseStaleBaseArgs([]string{"status", "abc1234", "--json"})
	require.NoError(t, err)
	require.Equal(t, staleBaseRequest{Format: "json", Action: "check", BaseCommit: "abc1234"}, req)
}

func TestParseStaleBaseArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing base", args: []string{"--base"}, want: "base commit is required"},
		{name: "duplicate base", args: []string{"check", "abc1234", "--base=def5678"}, want: "only one base commit"},
		{name: "too many", args: []string{"check", "abc", "def"}, want: "usage: codog stale-base"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown stale-base flag"},
		{name: "bad format", args: []string{"--output-format=yaml"}, want: "unknown stale-base output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseStaleBaseArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseG004ConformanceArgsOptions(t *testing.T) {
	req, err := parseG004ConformanceArgs([]string{"verify", `{\"schema\":\"g004\"}`, "--json"})
	require.NoError(t, err)
	require.Equal(t, "validate", req.Action)
	require.Equal(t, `{\"schema\":\"g004\"}`, req.Input)
	require.Equal(t, "json", req.Format)
}

func TestParseG004ConformanceArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing input", want: "input is required"},
		{name: "missing file", args: []string{"--file"}, want: "file is required"},
		{name: "mixed input", args: []string{"validate", "file.json", "--input={}"}, want: "only one of --input or --file"},
		{name: "stdin input", args: []string{"--stdin", "{}"}, want: "--stdin only without"},
		{name: "too many", args: []string{"validate", "one", "two"}, want: "usage: codog g004-conformance"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown g004-conformance flag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseG004ConformanceArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseTagArgsOptions(t *testing.T) {
	req, err := parseTagArgs([]string{"add", "v1.2.3", "main", "--message", "release", "--limit=10", "--json"})
	require.NoError(t, err)
	require.Equal(t, tagRequest{Format: "json", Action: "create", Name: "v1.2.3", Ref: "main", Message: "release", Limit: 10}, req)

	req, err = parseTagArgs([]string{"ls", "v1.*"})
	require.NoError(t, err)
	require.Equal(t, "list", req.Action)
	require.Equal(t, "v1.*", req.Pattern)
}

func TestParseTagArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing create name", args: []string{"create"}, want: "NAME is required"},
		{name: "missing show name", args: []string{"show"}, want: "NAME is required"},
		{name: "bad limit", args: []string{"--limit=-1"}, want: "non-negative integer"},
		{name: "unknown action", args: []string{"inspect"}, want: "unexpected argument"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for tag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseTagArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseGenerateSessionNameArgsOptions(t *testing.T) {
	req, err := parseGenerateSessionNameArgs([]string{
		"--resume=session-2", "--source=latest", "--prefix", "work",
		"--max-words=5", "--text", "Fix parser", "--apply", "--json",
	}, config.FlagOverrides{SessionID: "default"})
	require.NoError(t, err)
	require.Equal(t, generateSessionNameRequest{
		SessionID: "session-2", Source: "latest", Format: "json", Prefix: "work",
		MaxWords: 5, Text: "Fix parser", Rename: true,
	}, req)
}

func TestParseGenerateSessionNameArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing source", args: []string{"--source"}, want: "source is required"},
		{name: "bad source", args: []string{"--source=middle"}, want: "unknown generateSessionName source"},
		{name: "bad max words", args: []string{"--max-words=0"}, want: "must be a positive integer"},
		{name: "unknown argument", args: []string{"extra"}, want: "unknown generateSessionName argument"},
		{name: "bad format", args: []string{"--output-format=yaml"}, want: "unknown generateSessionName output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseGenerateSessionNameArgs(test.args, config.FlagOverrides{})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseCopyArgsOptions(t *testing.T) {
	req, err := parseCopyArgs([]string{"all", "--resume=session-2", "--format=html", "--json"}, config.FlagOverrides{SessionID: "default"})
	require.NoError(t, err)
	require.Equal(t, copyRequest{Scope: "all", Nth: 0, SessionID: "session-2", Format: "html", JSON: true}, req)

	req, err = parseCopyArgs([]string{"3"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "nth", req.Scope)
	require.Equal(t, 3, req.Nth)
}

func TestParseCopyArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing session", args: []string{"--session"}, want: "--session requires a value"},
		{name: "bad index", args: []string{"0"}, want: "greater than zero"},
		{name: "bad scope", args: []string{"middle"}, want: "unexpected argument"},
		{name: "response format", args: []string{"last", "--format=json"}, want: "only supports text format"},
		{name: "session format", args: []string{"all", "--format=yaml"}, want: "unsupported export format"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for copy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCopyArgs(test.args, config.FlagOverrides{})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseCommitPushPRArgsOptions(t *testing.T) {
	req, err := parseCommitPushPRArgs([]string{
		"--message=ship", "--title", "Ship change", "--body=body", "--branch", "work_branch",
		"--base=main", "--remote", "upstream", "--draft", "--no-pr", "--dry-run", "--staged", "--json",
	})
	require.NoError(t, err)
	require.Equal(t, commitPushPRRequest{
		Format: "json", Message: "ship", Title: "Ship change", Body: "body", Branch: "work_branch",
		Base: "main", Remote: "upstream", All: false, Draft: true, NoPR: true, DryRun: true,
	}, req)

	req, err = parseCommitPushPRArgs([]string{"commit", "from", "positionals"})
	require.NoError(t, err)
	require.Equal(t, "commit from positionals", req.Message)
}

func TestParseCommitPushPRArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing message", want: "requires a commit message"},
		{name: "missing branch", args: []string{"--branch"}, want: "branch is required"},
		{name: "unknown option", args: []string{"message", "--unknown"}, want: "unknown commit-push-pr flag"},
		{name: "bad format", args: []string{"message", "--output-format=yaml"}, want: "unknown commit-push-pr output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCommitPushPRArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}
