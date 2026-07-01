package webaccess

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchHTMLPlainTextAndInvalidURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>Test Title</title><script>hidden()</script></head><body><h1>Test Page</h1><p>Hello <b>world</b>.</p></body></html>`)
		case "/plain":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "plain text response")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	htmlOut, err := Fetch(context.Background(), FetchInput{URL: server.URL + "/page", Prompt: "What is the page title?"})
	require.NoError(t, err)
	require.Equal(t, 200, htmlOut.StatusCode)
	require.Equal(t, "Test Title", htmlOut.Title)
	require.Contains(t, htmlOut.Text, "Test Page Hello world.")
	require.Equal(t, "Title: Test Title", htmlOut.Summary)

	textOut, err := Fetch(context.Background(), FetchInput{URL: server.URL + "/plain"})
	require.NoError(t, err)
	require.Contains(t, textOut.Text, "plain text response")

	_, err = Fetch(context.Background(), FetchInput{URL: "not a url"})
	require.Error(t, err)
}

func TestSearchExtractsFiltersAndDecodesRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "rust web search", r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `
			<html><body>
			  <a class="result__a" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fdocs.rs%2Freqwest&amp;rut=abc">Reqwest docs</a>
			  <a class="result__snippet" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fdocs.rs%2Freqwest">Fast Rust HTTP client docs.</a>
			  <a class="result__a" href="https://example.com/blocked">Blocked result</a>
			  <div class="result__snippet">Blocked search snippet.</div>
			  <a href="https://example.org/fallback">Fallback ignored</a>
			</body></html>
		`)
	}))
	defer server.Close()
	t.Setenv("CODOG_WEB_SEARCH_BASE_URL", server.URL+"/search")

	out, err := Search(context.Background(), SearchInput{
		Query:          "rust web search",
		AllowedDomains: []string{"docs.rs"},
	})
	require.NoError(t, err)
	require.Equal(t, "rust web search", out.Query)
	require.Len(t, out.Results, 1)
	require.Equal(t, "Reqwest docs", out.Results[0].Title)
	require.Equal(t, "https://docs.rs/reqwest", out.Results[0].URL)
	require.Equal(t, "Fast Rust HTTP client docs.", out.Results[0].Snippet)
	require.Contains(t, out.SourceURL, "/search?q=rust+web+search")

	out, err = Search(context.Background(), SearchInput{
		Query:          "rust web search",
		BlockedDomains: []string{"example.com"},
	})
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
	require.Equal(t, "https://docs.rs/reqwest", out.Results[0].URL)
}

func TestSearchRejectsAllowedAndBlockedDomainsTogether(t *testing.T) {
	_, err := Search(context.Background(), SearchInput{
		Query:          "rust web search",
		AllowedDomains: []string{"docs.rs"},
		BlockedDomains: []string{"example.com"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "allowed_domains")
	require.Contains(t, err.Error(), "blocked_domains")
}

func TestSearchRejectsTooShortQuery(t *testing.T) {
	_, err := Search(context.Background(), SearchInput{Query: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least 2 characters")
}

func TestSearchFallsBackToGenericLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `
			<html><body>
			  <a href="https://example.com/one">Example One</a>
			  <a href="https://example.com/one">Duplicate One</a>
			  <a href="https://docs.rs/tokio">Tokio Docs</a>
			</body></html>
		`)
	}))
	defer server.Close()
	t.Setenv("CODOG_WEB_SEARCH_BASE_URL", server.URL+"/fallback")

	out, err := Search(context.Background(), SearchInput{Query: "generic links"})
	require.NoError(t, err)
	require.Len(t, out.Results, 2)
	require.Equal(t, "https://example.com/one", out.Results[0].URL)
	require.Equal(t, "https://docs.rs/tokio", out.Results[1].URL)
}
