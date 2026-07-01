package autofixpr

import (
	"testing"

	"github.com/Rememorio/codog/internal/githubcomments"
	"github.com/stretchr/testify/require"
)

func TestBuildAndRenderMarkdown(t *testing.T) {
	report := Build(githubcomments.Report{
		Repository: "acme/widgets",
		Number:     42,
		URL:        "https://github.com/acme/widgets/pull/42",
		Total:      2,
		ReviewComments: []githubcomments.ReviewComment{{
			Author:   "bob",
			Body:     "inline fix needed",
			Path:     "script.sh",
			Line:     2,
			DiffHunk: "@@ -1 +1 @@\n-old\n+new",
			URL:      "https://example.test/review",
		}},
		IssueComments: []githubcomments.IssueComment{{
			Author: "alice",
			Body:   "please update the summary",
			URL:    "https://example.test/issue",
		}},
	}, 10)

	require.Equal(t, "autofix_pr", report.Kind)
	require.Equal(t, "ready", report.Status)
	require.Equal(t, 2, report.Actionable)
	require.Len(t, report.Items, 2)
	require.Contains(t, report.Prompt, "Fix the GitHub pull request feedback")
	require.Contains(t, report.Prompt, "script.sh:2")

	markdown := RenderMarkdown(report)
	require.Contains(t, markdown, "# Autofix PR Task")
	require.Contains(t, markdown, "inline fix needed")
	require.Contains(t, markdown, "please update the summary")
	require.Contains(t, markdown, "```diff")
}
