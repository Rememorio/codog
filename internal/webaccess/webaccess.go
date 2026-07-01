package webaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	defaultMaxFetchBytes = 200_000
	maxFetchBytes        = 2_000_000
	defaultSearchBaseURL = "https://duckduckgo.com/html/"
)

type FetchInput struct {
	URL       string `json:"url"`
	Prompt    string `json:"prompt,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	MaxBytes  int64  `json:"max_bytes,omitempty"`
}

type FetchOutput struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	StatusCode  int    `json:"status_code"`
	Code        int    `json:"code"`
	CodeText    string `json:"codeText"`
	ContentType string `json:"content_type,omitempty"`
	Title       string `json:"title,omitempty"`
	Bytes       int64  `json:"bytes"`
	Truncated   bool   `json:"truncated"`
	Text        string `json:"text"`
	Summary     string `json:"summary,omitempty"`
	Result      string `json:"result,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
	DurationMs  int64  `json:"durationMs"`
}

type SearchInput struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	TimeoutMS      int      `json:"timeout_ms,omitempty"`
}

type SearchOutput struct {
	Query           string         `json:"query"`
	SourceURL       string         `json:"source_url"`
	Results         []SearchResult `json:"results"`
	DurationMS      int64          `json:"duration_ms"`
	DurationSeconds float64        `json:"durationSeconds"`
}

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

type searchResultBlock struct {
	ToolUseID string         `json:"tool_use_id"`
	Content   []SearchResult `json:"content"`
}

func (o SearchOutput) MarshalJSON() ([]byte, error) {
	type searchOutputJSON struct {
		Query           string         `json:"query"`
		SourceURL       string         `json:"source_url"`
		Results         []any          `json:"results"`
		Hits            []SearchResult `json:"hits"`
		DurationMS      int64          `json:"duration_ms"`
		DurationSeconds float64        `json:"durationSeconds"`
	}
	return json.Marshal(searchOutputJSON{
		Query:     o.Query,
		SourceURL: o.SourceURL,
		Results: []any{
			webSearchSummary(o.Query, o.Results),
			searchResultBlock{ToolUseID: "web_search_1", Content: o.Results},
		},
		Hits:            o.Results,
		DurationMS:      o.DurationMS,
		DurationSeconds: o.DurationSeconds,
	})
}

func Fetch(ctx context.Context, input FetchInput) (FetchOutput, error) {
	requestURL, err := validateURL(input.URL)
	if err != nil {
		return FetchOutput{}, err
	}
	limit := input.MaxBytes
	if limit <= 0 {
		limit = defaultMaxFetchBytes
	}
	if limit > maxFetchBytes {
		limit = maxFetchBytes
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return FetchOutput{}, err
	}
	req.Header.Set("User-Agent", "codog/0.1")
	client := httpClient(input.TimeoutMS)
	resp, err := client.Do(req)
	if err != nil {
		return FetchOutput{}, err
	}
	defer resp.Body.Close()

	reader := io.LimitReader(resp.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return FetchOutput{}, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	contentType := resp.Header.Get("Content-Type")
	title, text := normalizeContent(string(data), contentType)
	summary := summarize(input.Prompt, title, text)
	durationMS := time.Since(started).Milliseconds()
	return FetchOutput{
		URL:         requestURL.String(),
		FinalURL:    resp.Request.URL.String(),
		StatusCode:  resp.StatusCode,
		Code:        resp.StatusCode,
		CodeText:    http.StatusText(resp.StatusCode),
		ContentType: contentType,
		Title:       title,
		Bytes:       int64(len(data)),
		Truncated:   truncated,
		Text:        text,
		Summary:     summary,
		Result:      firstNonEmpty(summary, text),
		DurationMS:  durationMS,
		DurationMs:  durationMS,
	}, nil
}

func Search(ctx context.Context, input SearchInput) (SearchOutput, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return SearchOutput{}, errors.New("query is required")
	}
	if len([]rune(query)) < 2 {
		return SearchOutput{}, errors.New("query must be at least 2 characters")
	}
	if len(input.AllowedDomains) != 0 && len(input.BlockedDomains) != 0 {
		return SearchOutput{}, errors.New("cannot specify both allowed_domains and blocked_domains in the same request")
	}
	maxResults := input.MaxResults
	if maxResults <= 0 {
		maxResults = 8
	}
	if maxResults > 20 {
		maxResults = 20
	}
	searchURL, err := buildSearchURL(query)
	if err != nil {
		return SearchOutput{}, err
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return SearchOutput{}, err
	}
	req.Header.Set("User-Agent", "codog/0.1")
	req.Header.Set("Accept", "text/html, text/plain;q=0.9")
	resp, err := httpClient(input.TimeoutMS).Do(req)
	if err != nil {
		return SearchOutput{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, defaultMaxFetchBytes))
	if err != nil {
		return SearchOutput{}, err
	}
	results := extractSearchResults(string(data), resp.Request.URL)
	results = filterDomains(results, input.AllowedDomains, input.BlockedDomains)
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	duration := time.Since(started)
	return SearchOutput{
		Query:           query,
		SourceURL:       resp.Request.URL.String(),
		Results:         results,
		DurationMS:      duration.Milliseconds(),
		DurationSeconds: duration.Seconds(),
	}, nil
}

func webSearchSummary(query string, results []SearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No web search results matched the query %q.", query)
	}
	rendered := make([]string, 0, len(results))
	for _, hit := range results {
		rendered = append(rendered, fmt.Sprintf("- [%s](%s)", hit.Title, hit.URL))
	}
	return fmt.Sprintf("Search results for %q. Include a Sources section in the final answer.\n%s", query, strings.Join(rendered, "\n"))
}

func httpClient(timeoutMS int) *http.Client {
	timeout := 20 * time.Second
	if timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	return &http.Client{Timeout: timeout}
}

func validateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("url must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("url host is required")
	}
	return parsed, nil
}

func buildSearchURL(query string) (*url.URL, error) {
	base := strings.TrimSpace(os.Getenv("CODOG_WEB_SEARCH_BASE_URL"))
	if base == "" {
		base = defaultSearchBaseURL
	}
	parsed, err := validateURL(base)
	if err != nil {
		return nil, err
	}
	values := parsed.Query()
	values.Set("q", query)
	parsed.RawQuery = values.Encode()
	return parsed, nil
}

func normalizeContent(body string, contentType string) (string, string) {
	if strings.Contains(strings.ToLower(contentType), "html") || strings.Contains(strings.ToLower(body[:min(len(body), 512)]), "<html") {
		return htmlTitle(body), htmlText(body)
	}
	return "", collapseWhitespace(body)
}

func htmlTitle(body string) string {
	re := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	match := re.FindStringSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	return collapseWhitespace(stripTags(match[1]))
}

func htmlText(body string) string {
	replacements := []struct {
		re   *regexp.Regexp
		with string
	}{
		{regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`), " "},
		{regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`), " "},
		{regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`), " "},
		{regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>|</h[1-6]>`), "\n"},
	}
	for _, replacement := range replacements {
		body = replacement.re.ReplaceAllString(body, replacement.with)
	}
	return collapseWhitespace(stripTags(body))
}

func stripTags(value string) string {
	value = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(value, " ")
	return html.UnescapeString(value)
}

func collapseWhitespace(value string) string {
	collapsed := strings.Join(strings.Fields(html.UnescapeString(value)), " ")
	return regexp.MustCompile(`\s+([.,;:!?])`).ReplaceAllString(collapsed, "$1")
}

func summarize(prompt, title, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(prompt), "title") && title != "" {
		return "Title: " + title
	}
	limit := 1200
	if len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "..."
}

func extractSearchResults(body string, base *url.URL) []SearchResult {
	anchors := extractAnchors(body, base)
	preferred := make([]SearchResult, 0, len(anchors))
	for _, anchor := range anchors {
		if anchor.Preferred {
			preferred = append(preferred, SearchResult{Title: anchor.Title, URL: anchor.URL, Snippet: anchor.Snippet})
		}
	}
	if len(preferred) != 0 {
		return dedupeResults(preferred)
	}
	results := make([]SearchResult, 0, len(anchors))
	for _, anchor := range anchors {
		results = append(results, SearchResult{Title: anchor.Title, URL: anchor.URL, Snippet: anchor.Snippet})
	}
	return dedupeResults(results)
}

type anchor struct {
	Title     string
	URL       string
	Snippet   string
	Preferred bool
}

func extractAnchors(body string, base *url.URL) []anchor {
	re := regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	hrefRe := regexp.MustCompile(`(?is)\bhref\s*=\s*["']([^"']+)["']`)
	var anchors []anchor
	for _, match := range re.FindAllStringSubmatchIndex(body, -1) {
		if len(match) < 6 || match[2] < 0 || match[4] < 0 {
			continue
		}
		attrs := body[match[2]:match[3]]
		hrefMatch := hrefRe.FindStringSubmatch(attrs)
		if len(hrefMatch) < 2 {
			continue
		}
		title := collapseWhitespace(stripTags(body[match[4]:match[5]]))
		if title == "" {
			continue
		}
		cleanURL := normalizeResultURL(html.UnescapeString(hrefMatch[1]), base)
		if cleanURL == "" {
			continue
		}
		preferred := strings.Contains(strings.ToLower(attrs), "result__a")
		anchors = append(anchors, anchor{
			Title:     title,
			URL:       cleanURL,
			Snippet:   extractSnippetAfter(body, match[1]),
			Preferred: preferred,
		})
	}
	return anchors
}

func extractSnippetAfter(body string, offset int) string {
	if offset < 0 || offset >= len(body) {
		return ""
	}
	window := body[offset:min(len(body), offset+4000)]
	nextResultRe := regexp.MustCompile(`(?is)<a\b[^>]*class\s*=\s*["'][^"']*result__a[^"']*["']`)
	if next := nextResultRe.FindStringIndex(window); next != nil && next[0] > 0 {
		window = window[:next[0]]
	}
	snippetRe := regexp.MustCompile(`(?is)<(?:a|div|span|p)\b[^>]*class\s*=\s*["'][^"']*(?:result__snippet|web-result__snippet|search-result-snippet|result-snippet|snippet|result__body)[^"']*["'][^>]*>(.*?)</(?:a|div|span|p)>`)
	for _, match := range snippetRe.FindAllStringSubmatch(window, -1) {
		if len(match) < 2 {
			continue
		}
		snippet := collapseWhitespace(stripTags(match[1]))
		if snippet != "" {
			return snippet
		}
	}
	return ""
}

func normalizeResultURL(raw string, base *url.URL) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if !parsed.IsAbs() && base != nil {
		parsed = base.ResolveReference(parsed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if strings.Contains(parsed.Host, "duckduckgo.com") && strings.HasPrefix(parsed.Path, "/l/") {
		if target := parsed.Query().Get("uddg"); target != "" {
			if decoded, err := url.QueryUnescape(target); err == nil {
				return normalizeResultURL(decoded, nil)
			}
		}
	}
	parsed.Fragment = ""
	return parsed.String()
}

func dedupeResults(results []SearchResult) []SearchResult {
	seen := map[string]int{}
	out := make([]SearchResult, 0, len(results))
	for _, result := range results {
		if index, ok := seen[result.URL]; ok {
			if out[index].Snippet == "" && result.Snippet != "" {
				out[index].Snippet = result.Snippet
			}
			continue
		}
		seen[result.URL] = len(out)
		out = append(out, result)
	}
	return out
}

func filterDomains(results []SearchResult, allowed []string, blocked []string) []SearchResult {
	out := make([]SearchResult, 0, len(results))
	for _, result := range results {
		if len(allowed) != 0 && !hostInList(result.URL, allowed) {
			continue
		}
		if len(blocked) != 0 && hostInList(result.URL, blocked) {
			continue
		}
		out = append(out, result)
	}
	return out
}

func hostInList(raw string, domains []string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, domain := range domains {
		normalized := strings.ToLower(strings.TrimSpace(domain))
		if normalized == "" {
			continue
		}
		if parsedDomain, err := url.Parse(normalized); err == nil && parsedDomain.Hostname() != "" {
			normalized = parsedDomain.Hostname()
		}
		normalized = strings.TrimPrefix(normalized, ".")
		if host == normalized || strings.HasSuffix(host, "."+normalized) {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
