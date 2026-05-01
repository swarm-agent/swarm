package searchipc

import "swarm/packages/swarmd/internal/fff"

type Request struct {
	SearchRoot    string   `json:"search_root"`
	Queries       []string `json:"queries"`
	Include       string   `json:"include,omitempty"`
	MaxResults    int      `json:"max_results"`
	PageLimit     uint32   `json:"page_limit"`
	TimeoutMillis int64    `json:"timeout_ms"`
	AfterContext  uint32   `json:"after_context"`
}

type Response struct {
	Completed      bool                `json:"completed"`
	Content        GrepQueryResult     `json:"content,omitempty"`
	ContentResults []GrepQueryResult   `json:"content_results,omitempty"`
	FileResults    []SearchQueryResult `json:"file_results,omitempty"`
	HelperError    string              `json:"helper_error,omitempty"`
}

type GrepQueryResult struct {
	Query   string          `json:"query"`
	Mode    string          `json:"mode"`
	Matches []fff.GrepMatch `json:"matches,omitempty"`
	Metrics fff.GrepMetrics `json:"metrics,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type SearchQueryResult struct {
	Query   string            `json:"query"`
	Mode    string            `json:"mode"`
	Items   []fff.SearchItem  `json:"items,omitempty"`
	Metrics fff.SearchMetrics `json:"metrics,omitempty"`
	Error   string            `json:"error,omitempty"`
}
