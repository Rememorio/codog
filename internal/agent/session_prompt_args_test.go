package agent

import (
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestParsePromptArgsOptionsAndTerminator(t *testing.T) {
	req, err := parsePromptArgs([]string{
		"--input-format=stream-json", "--output-format", "stream-json",
		"--replay-user-messages", "--include-partial-messages", "--verbose",
		"--json-schema", `{\"type\":\"object\"}`, "--max-budget-usd=1.25",
		"--attach", "one.txt", "--file=two.txt", "--stdin", "hello",
	})
	require.NoError(t, err)
	require.Equal(t, "hello", req.Prompt)
	require.True(t, req.PromptProvided)
	require.Equal(t, "stream-json", req.Format)
	require.Equal(t, "stream-json", req.InputFormat)
	require.True(t, req.ReplayUserMessages)
	require.True(t, req.IncludePartialMessages)
	require.True(t, req.Verbose)
	require.True(t, req.UseStdin)
	require.Equal(t, []string{"one.txt", "two.txt"}, req.Attachments)
	require.NotNil(t, req.MaxBudgetUSD)
	require.Equal(t, 1.25, *req.MaxBudgetUSD)

	req, err = parsePromptArgs([]string{"--", "--json", "literal"})
	require.NoError(t, err)
	require.Equal(t, "--json literal", req.Prompt)
	require.Equal(t, "text", req.Format)
}

func TestParsePromptArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing output", args: []string{"--output-format"}, want: "prompt output format is required"},
		{name: "missing input format", args: []string{"--input-format"}, want: "--input-format requires a value"},
		{name: "missing attachment", args: []string{"--attach"}, want: "attachment path is required"},
		{name: "invalid budget", args: []string{"--max-budget-usd=0"}, want: "must be greater than 0"},
		{name: "input mismatch", args: []string{"--input-format=stream-json"}, want: "requires --output-format=stream-json"},
		{name: "replay mismatch", args: []string{"--replay-user-messages"}, want: "requires --input-format=stream-json"},
		{name: "partial mismatch", args: []string{"--include-partial-messages"}, want: "requires --output-format=stream-json"},
		{name: "compact schema", args: []string{"--compact", "--json-schema={}"}, want: "cannot be used with --compact"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parsePromptArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseConversationArgsOptions(t *testing.T) {
	req, err := parseConversationArgs([]string{
		"export", "report.md", "--resume=session-1", "--format=html", "--json",
	}, config.FlagOverrides{Resume: "resume-default", SessionID: "session-default"})
	require.NoError(t, err)
	require.Equal(t, "export", req.Action)
	require.Equal(t, "session-1", req.SessionID)
	require.Equal(t, "report.md", req.Output)
	require.Equal(t, "html", req.ExportFormat)
	require.Equal(t, "json", req.Format)

	req, err = parseConversationArgs([]string{"--confirm"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "clear", req.Action)
	require.Equal(t, "latest", req.SessionID)
}

func TestParseConversationArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing session", args: []string{"--session"}, want: "session id is required"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for conversation"},
		{name: "bad output format", args: []string{"--output-format=yaml"}, want: "unknown conversation output format"},
		{name: "bad export format", args: []string{"--format=yaml"}, want: "unsupported export format"},
		{name: "extra status arg", args: []string{"status", "one", "two"}, want: "unexpected argument"},
		{name: "clear positional", args: []string{"clear", "session"}, want: "unexpected argument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseConversationArgs(test.args, config.FlagOverrides{})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseHistoryArgsOptions(t *testing.T) {
	req, err := parseHistoryArgs([]string{"session-2", "--limit=25", "--offset", "5", "--json"}, config.FlagOverrides{Resume: "latest"})
	require.NoError(t, err)
	require.Equal(t, "session-2", req.SessionID)
	require.Equal(t, 25, req.Limit)
	require.Equal(t, 5, req.Offset)
	require.True(t, req.UseOffset)
	require.Equal(t, "json", req.Format)

	req, err = parseHistoryArgs([]string{"12"}, config.FlagOverrides{Resume: "true"})
	require.NoError(t, err)
	require.Equal(t, "latest", req.SessionID)
	require.Equal(t, 12, req.Limit)
}

func TestParseHistoryArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing offset", args: []string{"--offset", "--json"}, want: "--offset requires a value"},
		{name: "bad limit", args: []string{"0"}, want: "history limit must be positive"},
		{name: "bad offset", args: []string{"--offset=-1"}, want: "offset must be a non-negative integer"},
		{name: "extra session", args: []string{"first", "second"}, want: "unexpected argument"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for history"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseHistoryArgs(test.args, config.FlagOverrides{})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseRewindArgsOptions(t *testing.T) {
	req, err := parseRewindArgs([]string{"session-2", "--messages=6", "--json"}, config.FlagOverrides{Resume: "latest"}, "default")
	require.NoError(t, err)
	require.Equal(t, "session-2", req.SessionID)
	require.Equal(t, 6, req.Messages)
	require.Equal(t, "json", req.Format)

	req, err = parseRewindArgs([]string{"4"}, config.FlagOverrides{Resume: "true"}, "default")
	require.NoError(t, err)
	require.Equal(t, "latest", req.SessionID)
	require.Equal(t, 4, req.Messages)
}

func TestParseRewindArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing messages", args: []string{"--messages", "--json"}, want: "--messages requires a value"},
		{name: "bad count", args: []string{"0"}, want: "message count must be positive"},
		{name: "extra session", args: []string{"first", "second"}, want: "unexpected argument"},
		{name: "unknown option", args: []string{"--unknown"}, want: "unknown option \"--unknown\" for rewind"},
		{name: "invalid format", args: []string{"--output-format=yaml"}, want: "unknown rewind output format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseRewindArgs(test.args, config.FlagOverrides{}, "")
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseCodeIntelNotebookReadArgsOptions(t *testing.T) {
	req, err := parseCodeIntelNotebookReadArgs([]string{
		"notebook.ipynb", "--index=2", "--limit", "15", "--outputs=true", "--json",
	})
	require.NoError(t, err)
	require.Equal(t, "notebook.ipynb", req.NotebookPath)
	require.NotNil(t, req.CellIndex)
	require.Equal(t, 2, *req.CellIndex)
	require.Equal(t, 15, req.Limit)
	require.True(t, req.IncludeOutputs)
	require.Equal(t, "json", req.Format)
}

func TestParseCodeIntelNotebookReadArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing notebook", args: []string{"--limit=2"}, want: "usage: codog code-intel notebook-read"},
		{name: "extra notebook", args: []string{"one.ipynb", "two.ipynb"}, want: "usage: codog code-intel notebook-read"},
		{name: "missing index", args: []string{"one.ipynb", "--index"}, want: "cell index is required"},
		{name: "bad index", args: []string{"one.ipynb", "--index=-1"}, want: "must be a non-negative integer"},
		{name: "bad limit", args: []string{"one.ipynb", "--limit=0"}, want: "must be a positive integer"},
		{name: "bad outputs", args: []string{"one.ipynb", "--outputs=maybe"}, want: "outputs must be a boolean"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCodeIntelNotebookReadArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseCodeIntelNotebookEditArgsOptions(t *testing.T) {
	req, err := parseCodeIntelNotebookEditArgs([]string{
		"notebook.ipynb", "--edit-mode=insert", "--cell-id", "cell-1",
		"--type=markdown", "--new_source=hello", "--json",
	})
	require.NoError(t, err)
	require.Equal(t, "notebook.ipynb", req.NotebookPath)
	require.Equal(t, "insert", req.Mode)
	require.Equal(t, "cell-1", req.CellID)
	require.Equal(t, "markdown", req.CellType)
	require.Equal(t, "hello", req.Source)
	require.True(t, req.SourceSet)
	require.Equal(t, "json", req.Format)

	req, err = parseCodeIntelNotebookEditArgs([]string{"legacy.ipynb", "1", "code", "print(1)"})
	require.NoError(t, err)
	require.NotNil(t, req.CellIndex)
	require.Equal(t, 1, *req.CellIndex)
	require.Equal(t, "print(1)", req.Source)
}

func TestParseCodeIntelNotebookEditArgsFailures(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing notebook", args: []string{"--source=hello"}, want: "usage: codog code-intel notebook-edit"},
		{name: "missing mode", args: []string{"one.ipynb", "--mode"}, want: "mode is required"},
		{name: "bad index", args: []string{"one.ipynb", "--index=-1", "--source=x"}, want: "must be a non-negative integer"},
		{name: "two selectors", args: []string{"one.ipynb", "--index=1", "--cell-id=x", "--source=x"}, want: "either cell_index or cell_id"},
		{name: "missing source", args: []string{"one.ipynb", "--mode=replace"}, want: "new_source is required"},
		{name: "bad mode", args: []string{"one.ipynb", "--mode=unknown", "--source=x"}, want: "unknown notebook edit mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCodeIntelNotebookEditArgs(test.args)
			require.ErrorContains(t, err, test.want)
		})
	}
}
