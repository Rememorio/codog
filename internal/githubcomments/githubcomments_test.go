package githubcomments

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildReportParsesIssueAndReviewComments(t *testing.T) {
	report, err := BuildReport(
		[]byte(`{"number":42,"url":"https://github.com/acme/widgets/pull/42","headRepository":{"nameWithOwner":"acme/widgets"}}`),
		[]byte(`[
			{"id":2,"body":"top level","created_at":"2026-01-02T00:00:00Z","html_url":"https://example.test/issue","user":{"login":"alice"}}
		]`),
		[]byte(`[
			{"id":1,"body":"inline note","path":"main.go","line":12,"original_line":10,"diff_hunk":"@@ -1 +1 @@\n-old\n+new","in_reply_to_id":0,"created_at":"2026-01-01T00:00:00Z","html_url":"https://example.test/review","user":{"login":"bob"}}
		]`),
		"",
	)
	require.NoError(t, err)
	require.Equal(t, "pr_comments", report.Kind)
	require.Equal(t, "acme/widgets", report.Repository)
	require.Equal(t, 42, report.Number)
	require.Equal(t, 2, report.Total)
	require.Len(t, report.IssueComments, 1)
	require.Equal(t, "alice", report.IssueComments[0].Author)
	require.Len(t, report.ReviewComments, 1)
	require.Equal(t, "main.go", report.ReviewComments[0].Path)
	require.Equal(t, 12, report.ReviewComments[0].Line)
}

func TestRenderTextShowsCommentsAndDiffContext(t *testing.T) {
	report := Report{
		Kind:       "pr_comments",
		Status:     "ok",
		Repository: "acme/widgets",
		Number:     42,
		Total:      2,
		IssueComments: []IssueComment{{
			Author: "alice",
			Body:   "top level",
		}},
		ReviewComments: []ReviewComment{{
			Author:   "bob",
			Path:     "main.go",
			Line:     12,
			DiffHunk: "@@ -1 +1 @@\n-old\n+new",
			Body:     "inline note",
		}},
	}
	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "PR Comments")
	require.Contains(t, out.String(), "Repository       acme/widgets")
	require.Contains(t, out.String(), "- @alice")
	require.Contains(t, out.String(), "> top level")
	require.Contains(t, out.String(), "- @bob main.go:12")
	require.Contains(t, out.String(), "```diff")
	require.Contains(t, out.String(), "+new")
}

func TestBuildReportParsesPaginatedGhSlurpOutput(t *testing.T) {
	report, err := BuildReport(
		[]byte(`{"number":42,"headRepository":{"nameWithOwner":"acme/widgets"}}`),
		[]byte(`[
			[{"id":1,"body":"first","created_at":"2026-01-01T00:00:00Z","user":{"login":"alice"}}],
			[{"id":2,"body":"second","created_at":"2026-01-02T00:00:00Z","user":{"login":"bob"}}]
		]`),
		[]byte(`[
			[{"id":3,"body":"review","path":"main.go","line":3,"created_at":"2026-01-03T00:00:00Z","user":{"login":"carol"}}]
		]`),
		"",
	)
	require.NoError(t, err)
	require.Equal(t, 3, report.Total)
	require.Len(t, report.IssueComments, 2)
	require.Len(t, report.ReviewComments, 1)
	require.Equal(t, "bob", report.IssueComments[1].Author)
}

func TestRenderTextNoComments(t *testing.T) {
	var out bytes.Buffer
	RenderText(&out, Report{Repository: "acme/widgets", Number: 42})
	require.Contains(t, out.String(), "Total            0")
	require.Contains(t, out.String(), "No comments found.")
}
