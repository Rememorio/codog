package mocklimits

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/mockanthropic"
)

type Options struct {
	Addr         string
	Failures     int
	RetryAfterMS int
	Text         string
}

type Report struct {
	Kind         string   `json:"kind"`
	Action       string   `json:"action"`
	Status       string   `json:"status"`
	Addr         string   `json:"addr"`
	BaseURL      string   `json:"base_url"`
	Failures     int      `json:"failures"`
	RetryAfterMS int      `json:"retry_after_ms"`
	Text         string   `json:"text"`
	Endpoint     string   `json:"endpoint"`
	Messages     []string `json:"messages,omitempty"`
}

func Normalize(options Options) Options {
	if strings.TrimSpace(options.Addr) == "" {
		options.Addr = ":8089"
	}
	if options.Failures < 0 {
		options.Failures = 0
	}
	if options.RetryAfterMS < 0 {
		options.RetryAfterMS = 0
	}
	if strings.TrimSpace(options.Text) == "" {
		options.Text = "mock response after rate limits"
	}
	return options
}

func BuildReport(action string, options Options) Report {
	options = Normalize(options)
	baseURL := BaseURL(options.Addr)
	status := "ready"
	if action == "serve" {
		status = "serving"
	}
	return Report{
		Kind:         "mock_limits",
		Action:       action,
		Status:       status,
		Addr:         options.Addr,
		BaseURL:      baseURL,
		Failures:     options.Failures,
		RetryAfterMS: options.RetryAfterMS,
		Text:         options.Text,
		Endpoint:     strings.TrimRight(baseURL, "/") + "/v1/messages",
		Messages: []string{
			"Point Codog at base_url=" + baseURL + " with a mock model.",
			fmt.Sprintf("The first %d request(s) return HTTP 429, then the server streams a normal Anthropic-compatible response.", options.Failures),
		},
	}
}

func Handler(options Options) http.Handler {
	options = Normalize(options)
	retryAfter := ""
	if options.RetryAfterMS > 0 {
		seconds := (options.RetryAfterMS + 999) / 1000
		if seconds <= 0 {
			seconds = 1
		}
		retryAfter = strconv.Itoa(seconds)
	}
	return mockanthropic.Server{
		Text:              options.Text,
		RateLimitFailures: options.Failures,
		RetryAfter:        retryAfter,
	}.Handler()
}

func BaseURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = ":8089"
	}
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return strings.TrimRight(host, "/")
}

func RenderText(out io.Writer, report Report) {
	fmt.Fprintln(out, "Mock Limits")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Address          %s\n", report.Addr)
	fmt.Fprintf(out, "  Base URL         %s\n", report.BaseURL)
	fmt.Fprintf(out, "  Endpoint         %s\n", report.Endpoint)
	fmt.Fprintf(out, "  Failures         %d\n", report.Failures)
	fmt.Fprintf(out, "  Retry after      %dms\n", report.RetryAfterMS)
	fmt.Fprintf(out, "  Text             %s\n", report.Text)
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Message          %s\n", message)
	}
}
