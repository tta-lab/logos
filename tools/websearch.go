package tools

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Search performs a web search via DuckDuckGo Lite and returns formatted results.
// maxResults defaults to 10 if ≤ 0, capped at 20.
// Creates its own HTTP client — the fantasy tool (NewSearchWebTool) keeps a
// separate long-lived client with connection pooling.
func Search(ctx context.Context, query string, maxResults int) (string, error) {
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 20 {
		maxResults = 20
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}

	if err := maybeDelaySearch(ctx); err != nil {
		return "", fmt.Errorf("search cancelled: %w", err)
	}

	results, err := searchDuckDuckGo(ctx, client, query, maxResults)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}

	return formatSearchResults(results), nil
}
