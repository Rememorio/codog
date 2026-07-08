package githubcomments

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchUsesGhCLIAndBuildsReport(t *testing.T) {
	gh := fakeGH(t, map[string]string{
		"pr view 99 --repo acme/widgets --json number,url,headRepository": `{"number":99,"url":"https://github.com/acme/widgets/pull/99","headRepository":{"nameWithOwner":"acme/widgets"}}`,
		"api --paginate --slurp repos/acme/widgets/issues/99/comments":    `[[{"id":2,"body":"issue note","created_at":"2026-01-02T00:00:00Z","html_url":"https://example.test/issue","user":{"login":"alice"}}]]`,
		"api --paginate --slurp repos/acme/widgets/pulls/99/comments":     `[[{"id":1,"body":"review note","path":"main.go","line":12,"created_at":"2026-01-01T00:00:00Z","html_url":"https://example.test/review","user":{"login":"bob"}}]]`,
	})

	report, err := Fetch(context.Background(), Options{PR: "99", Repo: "acme/widgets", GHPath: gh})
	require.NoError(t, err)
	require.Equal(t, "acme/widgets", report.Repository)
	require.Equal(t, 99, report.Number)
	require.Equal(t, "https://github.com/acme/widgets/pull/99", report.URL)
	require.Equal(t, 2, report.Total)
	require.Equal(t, "alice", report.IssueComments[0].Author)
	require.Equal(t, "bob", report.ReviewComments[0].Author)
}

func TestFetchReportsGhFailures(t *testing.T) {
	gh := fakeGH(t, map[string]string{
		"pr view 99 --repo acme/widgets --json number,url,headRepository": `__FAIL__:no auth token`,
	})

	_, err := Fetch(context.Background(), Options{PR: "99", Repo: "acme/widgets", GHPath: gh})
	require.Error(t, err)
	require.Contains(t, err.Error(), "gh pr view 99 --repo acme/widgets --json number,url,headRepository failed")
	require.Contains(t, err.Error(), "no auth token")
}

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

func TestBuildReportUsesRepoOverrideAndOwnerFallback(t *testing.T) {
	report, err := BuildReport(
		[]byte(`{"number":7,"headRepository":{"name":"widgets","owner":{"login":"acme"}}}`),
		nil,
		nil,
		"override/repo",
	)
	require.NoError(t, err)
	require.Equal(t, "override/repo", report.Repository)
	require.Equal(t, 7, report.Number)

	report, err = BuildReport(
		[]byte(`{"number":8,"headRepository":{"name":"widgets","owner":{"login":"acme"}}}`),
		nil,
		nil,
		"",
	)
	require.NoError(t, err)
	require.Equal(t, "acme/widgets", report.Repository)
	require.Equal(t, 8, report.Number)
}

func TestBuildReportRejectsInvalidInputs(t *testing.T) {
	_, err := BuildReport([]byte(`{"headRepository":{"nameWithOwner":"acme/widgets"}}`), nil, nil, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pull request number not found")

	_, err = BuildReport([]byte(`{"number":9,"headRepository":{}}`), nil, nil, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pull request repository not found")

	_, err = BuildReport([]byte(`{"number":9,"headRepository":{"nameWithOwner":"acme/widgets"}}`), []byte(`{bad`), nil, "")
	require.Error(t, err)

	_, err = BuildReport([]byte(`{"number":9,"headRepository":{"nameWithOwner":"acme/widgets"}}`), nil, []byte(`{bad`), "")
	require.Error(t, err)
}

func TestRenderTextNoComments(t *testing.T) {
	var out bytes.Buffer
	RenderText(&out, Report{Repository: "acme/widgets", Number: 42})
	require.Contains(t, out.String(), "Total            0")
	require.Contains(t, out.String(), "No comments found.")
}

func TestRenderTextFallsBackForEmptyAuthorsAndReviewLocation(t *testing.T) {
	report := Report{
		Repository: "acme/widgets",
		Number:     42,
		Total:      2,
		IssueComments: []IssueComment{{
			Body: "",
		}},
		ReviewComments: []ReviewComment{{
			OriginalLine: 5,
			Body:         "original line only",
		}},
	}
	var out bytes.Buffer
	RenderText(&out, report)
	text := out.String()
	require.Contains(t, text, "- @unknown")
	require.Contains(t, text, "  >\n")
	require.Contains(t, text, "- @unknown review:5")
	require.Contains(t, text, "> original line only")
}

func fakeGH(t *testing.T, responses map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if runtime.GOOS == "windows" {
		path += ".cmd"
	}
	data, err := os.CreateTemp(dir, "responses-*.json")
	require.NoError(t, err)
	require.NoError(t, data.Close())
	encoded := "{\n"
	first := true
	for key, value := range responses {
		if !first {
			encoded += ",\n"
		}
		first = false
		encoded += "\t" + strconvQuote(key) + ": " + strconvQuote(value)
	}
	encoded += "\n}\n"
	require.NoError(t, os.WriteFile(data.Name(), []byte(encoded), 0o644))
	if runtime.GOOS == "windows" {
		script := "@echo off\r\ngo run " + strconvQuote(filepath.Join(dir, "fake-gh.go")) + " " + strconvQuote(data.Name()) + " %*\r\n"
		require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	} else {
		script := "#!/bin/sh\nexec go run " + strconvQuote(filepath.Join(dir, "fake-gh.go")) + " " + strconvQuote(data.Name()) + " \"$@\"\n"
		require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	}
	program := `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	responses := map[string]string{}
	if err := json.Unmarshal(data, &responses); err != nil {
		panic(err)
	}
	key := strings.Join(os.Args[2:], " ")
	value, ok := responses[key]
	if !ok {
		fmt.Fprintln(os.Stderr, "unexpected gh args: "+key)
		os.Exit(3)
	}
	if strings.HasPrefix(value, "__FAIL__:") {
		fmt.Fprintln(os.Stderr, strings.TrimPrefix(value, "__FAIL__:"))
		os.Exit(4)
	}
	fmt.Print(value)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fake-gh.go"), []byte(program), 0o644))
	return path
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
