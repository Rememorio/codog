package githubcomments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	PR     string
	Repo   string
	GHPath string
}

type Report struct {
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	Repository     string          `json:"repository"`
	Number         int             `json:"number"`
	URL            string          `json:"url,omitempty"`
	Total          int             `json:"total"`
	IssueComments  []IssueComment  `json:"issue_comments,omitempty"`
	ReviewComments []ReviewComment `json:"review_comments,omitempty"`
}

type IssueComment struct {
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at,omitempty"`
	URL       string `json:"url,omitempty"`
}

type ReviewComment struct {
	ID           int64  `json:"id"`
	Author       string `json:"author"`
	Body         string `json:"body"`
	Path         string `json:"path,omitempty"`
	Line         int    `json:"line,omitempty"`
	OriginalLine int    `json:"original_line,omitempty"`
	DiffHunk     string `json:"diff_hunk,omitempty"`
	InReplyToID  int64  `json:"in_reply_to_id,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	URL          string `json:"url,omitempty"`
}

type prView struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	HeadRepository struct {
		NameWithOwner string `json:"nameWithOwner"`
		Name          string `json:"name"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"headRepository"`
}

type apiIssueComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type apiReviewComment struct {
	ID           int64  `json:"id"`
	Body         string `json:"body"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	OriginalLine int    `json:"original_line"`
	DiffHunk     string `json:"diff_hunk"`
	InReplyToID  int64  `json:"in_reply_to_id"`
	CreatedAt    string `json:"created_at"`
	HTMLURL      string `json:"html_url"`
	User         struct {
		Login string `json:"login"`
	} `json:"user"`
}

func Fetch(ctx context.Context, opts Options) (Report, error) {
	gh := strings.TrimSpace(opts.GHPath)
	if gh == "" {
		gh = "gh"
	}
	viewArgs := []string{"pr", "view"}
	if strings.TrimSpace(opts.PR) != "" {
		viewArgs = append(viewArgs, strings.TrimSpace(opts.PR))
	}
	if strings.TrimSpace(opts.Repo) != "" {
		viewArgs = append(viewArgs, "--repo", strings.TrimSpace(opts.Repo))
	}
	viewArgs = append(viewArgs, "--json", "number,url,headRepository")
	viewJSON, err := runGH(ctx, gh, viewArgs...)
	if err != nil {
		return Report{}, err
	}
	view, repo, err := parsePRView(viewJSON, opts.Repo)
	if err != nil {
		return Report{}, err
	}
	issueJSON, err := runGH(ctx, gh, "api", "--paginate", "--slurp", "repos/"+repo+"/issues/"+strconv.Itoa(view.Number)+"/comments")
	if err != nil {
		return Report{}, err
	}
	reviewJSON, err := runGH(ctx, gh, "api", "--paginate", "--slurp", "repos/"+repo+"/pulls/"+strconv.Itoa(view.Number)+"/comments")
	if err != nil {
		return Report{}, err
	}
	return BuildReport(viewJSON, issueJSON, reviewJSON, opts.Repo)
}

func BuildReport(viewJSON []byte, issueJSON []byte, reviewJSON []byte, repoOverride string) (Report, error) {
	view, repo, err := parsePRView(viewJSON, repoOverride)
	if err != nil {
		return Report{}, err
	}
	issueComments, err := parseIssueComments(issueJSON)
	if err != nil {
		return Report{}, err
	}
	reviewComments, err := parseReviewComments(reviewJSON)
	if err != nil {
		return Report{}, err
	}
	sort.Slice(issueComments, func(i, j int) bool {
		return compareComment(issueComments[i].CreatedAt, issueComments[i].ID, issueComments[j].CreatedAt, issueComments[j].ID)
	})
	sort.Slice(reviewComments, func(i, j int) bool {
		return compareComment(reviewComments[i].CreatedAt, reviewComments[i].ID, reviewComments[j].CreatedAt, reviewComments[j].ID)
	})
	return Report{
		Kind:           "pr_comments",
		Status:         "ok",
		Repository:     repo,
		Number:         view.Number,
		URL:            view.URL,
		Total:          len(issueComments) + len(reviewComments),
		IssueComments:  issueComments,
		ReviewComments: reviewComments,
	}, nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "PR Comments")
	fmt.Fprintf(w, "  Repository       %s\n", report.Repository)
	fmt.Fprintf(w, "  Pull request     #%d\n", report.Number)
	if report.URL != "" {
		fmt.Fprintf(w, "  URL              %s\n", report.URL)
	}
	fmt.Fprintf(w, "  Total            %d\n", report.Total)
	if report.Total == 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "No comments found.")
		return
	}
	if len(report.IssueComments) != 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Issue comments")
		for _, comment := range report.IssueComments {
			fmt.Fprintf(w, "- @%s\n", emptyAs(comment.Author, "unknown"))
			renderQuotedBody(w, comment.Body)
		}
	}
	if len(report.ReviewComments) != 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Review comments")
		for _, comment := range report.ReviewComments {
			location := comment.Path
			line := comment.Line
			if line == 0 {
				line = comment.OriginalLine
			}
			if line != 0 {
				location += ":" + strconv.Itoa(line)
			}
			if location == "" {
				location = "review"
			}
			fmt.Fprintf(w, "- @%s %s\n", emptyAs(comment.Author, "unknown"), location)
			if strings.TrimSpace(comment.DiffHunk) != "" {
				fmt.Fprintln(w, "  ```diff")
				for _, line := range strings.Split(strings.TrimRight(comment.DiffHunk, "\n"), "\n") {
					fmt.Fprintf(w, "  %s\n", line)
				}
				fmt.Fprintln(w, "  ```")
			}
			renderQuotedBody(w, comment.Body)
		}
	}
}

func parsePRView(data []byte, repoOverride string) (prView, string, error) {
	var view prView
	if err := json.Unmarshal(data, &view); err != nil {
		return view, "", err
	}
	if view.Number == 0 {
		return view, "", errors.New("pull request number not found")
	}
	repo := strings.TrimSpace(repoOverride)
	if repo == "" {
		repo = strings.TrimSpace(view.HeadRepository.NameWithOwner)
	}
	if repo == "" && view.HeadRepository.Owner.Login != "" && view.HeadRepository.Name != "" {
		repo = view.HeadRepository.Owner.Login + "/" + view.HeadRepository.Name
	}
	if repo == "" {
		return view, "", errors.New("pull request repository not found")
	}
	return view, repo, nil
}

func parseIssueComments(data []byte) ([]IssueComment, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var raw []apiIssueComment
	if err := json.Unmarshal(data, &raw); err != nil {
		raw = nil
		var pages [][]apiIssueComment
		if pageErr := json.Unmarshal(data, &pages); pageErr != nil {
			return nil, err
		}
		for _, page := range pages {
			raw = append(raw, page...)
		}
	}
	comments := make([]IssueComment, 0, len(raw))
	for _, comment := range raw {
		comments = append(comments, IssueComment{
			ID:        comment.ID,
			Author:    comment.User.Login,
			Body:      strings.TrimSpace(comment.Body),
			CreatedAt: comment.CreatedAt,
			URL:       comment.HTMLURL,
		})
	}
	return comments, nil
}

func parseReviewComments(data []byte) ([]ReviewComment, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var raw []apiReviewComment
	if err := json.Unmarshal(data, &raw); err != nil {
		raw = nil
		var pages [][]apiReviewComment
		if pageErr := json.Unmarshal(data, &pages); pageErr != nil {
			return nil, err
		}
		for _, page := range pages {
			raw = append(raw, page...)
		}
	}
	comments := make([]ReviewComment, 0, len(raw))
	for _, comment := range raw {
		comments = append(comments, ReviewComment{
			ID:           comment.ID,
			Author:       comment.User.Login,
			Body:         strings.TrimSpace(comment.Body),
			Path:         comment.Path,
			Line:         comment.Line,
			OriginalLine: comment.OriginalLine,
			DiffHunk:     strings.TrimSpace(comment.DiffHunk),
			InReplyToID:  comment.InReplyToID,
			CreatedAt:    comment.CreatedAt,
			URL:          comment.HTMLURL,
		})
	}
	return comments, nil
}

func runGH(ctx context.Context, gh string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, gh, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func compareComment(leftTime string, leftID int64, rightTime string, rightID int64) bool {
	leftParsed, leftErr := time.Parse(time.RFC3339, leftTime)
	rightParsed, rightErr := time.Parse(time.RFC3339, rightTime)
	if leftErr == nil && rightErr == nil && !leftParsed.Equal(rightParsed) {
		return leftParsed.Before(rightParsed)
	}
	return leftID < rightID
}

func renderQuotedBody(w io.Writer, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		fmt.Fprintln(w, "  >")
		return
	}
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintf(w, "  > %s\n", line)
	}
}

func emptyAs(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
