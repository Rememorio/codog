package autofixpr

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/githubcomments"
	"github.com/Rememorio/codog/internal/prompthistory"
)

type Item struct {
	Index        int    `json:"index"`
	Source       string `json:"source"`
	Author       string `json:"author"`
	Body         string `json:"body"`
	Path         string `json:"path,omitempty"`
	Line         int    `json:"line,omitempty"`
	DiffHunk     string `json:"diff_hunk,omitempty"`
	URL          string `json:"url,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	Instruction  string `json:"instruction"`
	Actionable   bool   `json:"actionable"`
	OriginalLine int    `json:"original_line,omitempty"`
}

type Report struct {
	Kind        string `json:"kind"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	Repository  string `json:"repository"`
	PullRequest int    `json:"pull_request"`
	URL         string `json:"url,omitempty"`
	Total       int    `json:"total_comments"`
	Actionable  int    `json:"actionable_comments"`
	Limit       int    `json:"limit,omitempty"`
	File        string `json:"file,omitempty"`
	Bytes       int    `json:"bytes,omitempty"`
	Items       []Item `json:"items,omitempty"`
	Prompt      string `json:"prompt"`
}

func Build(comments githubcomments.Report, limit int) Report {
	items := itemsFromComments(comments)
	if limit <= 0 {
		limit = len(items)
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	actionable := 0
	for _, item := range items {
		if item.Actionable {
			actionable++
		}
	}
	status := "ready"
	if actionable == 0 {
		status = "no_comments"
	}
	report := Report{
		Kind:        "autofix_pr",
		Action:      "prepare",
		Status:      status,
		Repository:  comments.Repository,
		PullRequest: comments.Number,
		URL:         comments.URL,
		Total:       comments.Total,
		Actionable:  actionable,
		Limit:       limit,
		Items:       items,
	}
	report.Prompt = RenderPrompt(report)
	return report
}

func RenderText(out io.Writer, report Report) {
	fmt.Fprintln(out, "Autofix PR")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Repository       %s\n", report.Repository)
	fmt.Fprintf(out, "  Pull request     #%d\n", report.PullRequest)
	if report.URL != "" {
		fmt.Fprintf(out, "  URL              %s\n", report.URL)
	}
	fmt.Fprintf(out, "  Comments         %d\n", report.Total)
	fmt.Fprintf(out, "  Actionable       %d\n", report.Actionable)
	if report.File != "" {
		fmt.Fprintf(out, "  File             %s\n", report.File)
		fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	}
	if len(report.Items) == 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "No PR comments found.")
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Fix items")
	for _, item := range report.Items {
		location := itemLocation(item)
		if location == "" {
			location = "pull request"
		}
		fmt.Fprintf(out, "  %d. @%s %s\n", item.Index, item.Author, location)
		fmt.Fprintf(out, "     %s\n", prompthistory.Preview(item.Body, 120))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Prompt")
	fmt.Fprintln(out, indentText(report.Prompt, "  "))
}

func RenderMarkdown(report Report) string {
	var builder strings.Builder
	builder.WriteString("# Autofix PR Task\n\n")
	builder.WriteString(fmt.Sprintf("- Repository: %s\n", report.Repository))
	builder.WriteString(fmt.Sprintf("- Pull request: #%d\n", report.PullRequest))
	if report.URL != "" {
		builder.WriteString(fmt.Sprintf("- URL: %s\n", report.URL))
	}
	builder.WriteString(fmt.Sprintf("- Comments: %d\n", report.Total))
	builder.WriteString(fmt.Sprintf("- Actionable comments: %d\n", report.Actionable))
	builder.WriteString("\n## Prompt\n\n")
	builder.WriteString(report.Prompt)
	builder.WriteString("\n\n## Comments\n\n")
	for _, item := range report.Items {
		builder.WriteString(fmt.Sprintf("### %d. @%s %s\n\n", item.Index, item.Author, item.Source))
		if location := itemLocation(item); location != "" {
			builder.WriteString(fmt.Sprintf("- Location: `%s`\n", location))
		}
		if item.URL != "" {
			builder.WriteString(fmt.Sprintf("- URL: %s\n", item.URL))
		}
		if item.CreatedAt != "" {
			builder.WriteString(fmt.Sprintf("- Created: %s\n", item.CreatedAt))
		}
		builder.WriteString("\n")
		builder.WriteString(item.Body)
		builder.WriteString("\n")
		if item.DiffHunk != "" {
			builder.WriteString("\n```diff\n")
			builder.WriteString(item.DiffHunk)
			builder.WriteString("\n```\n")
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

func RenderPrompt(report Report) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Fix the GitHub pull request feedback for %s#%d.\n\n", report.Repository, report.PullRequest))
	builder.WriteString("Work method:\n")
	builder.WriteString("- Inspect the referenced files before editing.\n")
	builder.WriteString("- Address every actionable comment below, or leave a concise note in the final response when a comment is not applicable.\n")
	builder.WriteString("- Keep the patch focused on the requested PR feedback.\n")
	builder.WriteString("- Run targeted tests or explain why they could not be run.\n\n")
	if report.URL != "" {
		builder.WriteString(fmt.Sprintf("Pull request URL: %s\n\n", report.URL))
	}
	if len(report.Items) == 0 {
		builder.WriteString("No PR comments were found. Re-check the PR before making changes.\n")
		return builder.String()
	}
	builder.WriteString("Comments to address:\n")
	for _, item := range report.Items {
		location := itemLocation(item)
		if location == "" {
			location = "pull request"
		}
		builder.WriteString(fmt.Sprintf("\n%d. %s by @%s at %s\n", item.Index, strings.ReplaceAll(item.Source, "_", " "), item.Author, location))
		if item.URL != "" {
			builder.WriteString(fmt.Sprintf("   URL: %s\n", item.URL))
		}
		builder.WriteString("   Body:\n")
		for _, line := range strings.Split(strings.TrimSpace(item.Body), "\n") {
			builder.WriteString("   > ")
			builder.WriteString(line)
			builder.WriteString("\n")
		}
		if item.DiffHunk != "" {
			builder.WriteString("   Diff hunk:\n")
			for _, line := range strings.Split(strings.TrimRight(item.DiffHunk, "\n"), "\n") {
				builder.WriteString("   ")
				builder.WriteString(line)
				builder.WriteString("\n")
			}
		}
	}
	return strings.TrimSpace(builder.String()) + "\n"
}

func itemsFromComments(comments githubcomments.Report) []Item {
	items := make([]Item, 0, comments.Total)
	for _, comment := range comments.ReviewComments {
		line := comment.Line
		if line == 0 {
			line = comment.OriginalLine
		}
		item := Item{
			Source:       "review_comment",
			Author:       emptyAs(comment.Author, "unknown"),
			Body:         strings.TrimSpace(comment.Body),
			Path:         comment.Path,
			Line:         line,
			OriginalLine: comment.OriginalLine,
			DiffHunk:     strings.TrimSpace(comment.DiffHunk),
			URL:          comment.URL,
			CreatedAt:    comment.CreatedAt,
		}
		item.Actionable = strings.TrimSpace(item.Body) != ""
		item.Instruction = instruction(item)
		items = append(items, item)
	}
	for _, comment := range comments.IssueComments {
		item := Item{
			Source:    "issue_comment",
			Author:    emptyAs(comment.Author, "unknown"),
			Body:      strings.TrimSpace(comment.Body),
			URL:       comment.URL,
			CreatedAt: comment.CreatedAt,
		}
		item.Actionable = strings.TrimSpace(item.Body) != ""
		item.Instruction = instruction(item)
		items = append(items, item)
	}
	for index := range items {
		items[index].Index = index + 1
	}
	return items
}

func instruction(item Item) string {
	target := itemLocation(item)
	if target == "" {
		target = "pull request"
	}
	body := prompthistory.Preview(item.Body, 120)
	if body == "" {
		body = "empty comment"
	}
	return fmt.Sprintf("Address @%s's %s feedback on %s: %s", item.Author, strings.ReplaceAll(item.Source, "_", " "), target, body)
}

func itemLocation(item Item) string {
	if item.Path == "" {
		return ""
	}
	location := item.Path
	if item.Line != 0 {
		location += ":" + strconv.Itoa(item.Line)
	}
	return location
}

func indentText(text string, prefix string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return prefix
	}
	var builder strings.Builder
	for index, line := range strings.Split(text, "\n") {
		if index > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(prefix)
		builder.WriteString(line)
	}
	return builder.String()
}

func emptyAs(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
