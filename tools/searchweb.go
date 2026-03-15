package tools

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"charm.land/fantasy"
)

// SearchWebParams are the input parameters for the search_web tool.
type SearchWebParams struct {
	Query      string `json:"query" description:"The search query"`
	MaxResults int    `json:"max_results,omitempty" description:"Maximum number of results (default 10, max 20)"`
}

// newSearchHTTPClient creates an HTTP client tuned for repeated search requests.
// Uses a cloned transport with connection pooling settings.
func newSearchHTTPClient() *http.Client {
	var transport *http.Transport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = t.Clone()
	} else {
		transport = &http.Transport{}
	}
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}

// normalizeMaxResults clamps maxResults to the [1, 20] range, defaulting to 10.
func normalizeMaxResults(n int) int {
	if n <= 0 {
		return 10
	}
	if n > 20 {
		return 20
	}
	return n
}

// NewSearchWebTool creates a web search tool using DuckDuckGo Lite.
func NewSearchWebTool(client *http.Client) fantasy.AgentTool {
	if client == nil {
		client = newSearchHTTPClient()
	}

	return fantasy.NewParallelAgentTool(
		"search_web",
		schemaDescription(searchWebDescription),
		func(ctx context.Context, params SearchWebParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}
			maxResults := normalizeMaxResults(params.MaxResults)

			if err := maybeDelaySearch(ctx); err != nil {
				return fantasy.NewTextErrorResponse("search cancelled: " + err.Error()), nil
			}
			results, err := searchDuckDuckGo(ctx, client, params.Query, maxResults)
			if err != nil {
				slog.Warn("Web search failed", "query", params.Query, "err", err)
				return fantasy.NewTextErrorResponse("Failed to search: " + err.Error()), nil
			}
			slog.Debug("Web search completed", "query", params.Query, "results", len(results))
			return fantasy.NewTextResponse(formatSearchResults(results)), nil
		})
}
