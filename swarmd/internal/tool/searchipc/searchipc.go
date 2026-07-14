package searchipc

import "swarm/packages/swarmd/internal/fff"

const ProtocolVersion = 1

type Request struct {
	ProtocolVersion   int      `json:"protocol_version,omitempty"`
	RequestID         string   `json:"request_id,omitempty"`
	IndexRoot         string   `json:"index_root,omitempty"`
	TargetPath        string   `json:"target_path,omitempty"`
	SearchRoot        string   `json:"search_root,omitempty"` // Legacy alias for both index_root and target_path.
	Operation         string   `json:"operation,omitempty"`
	Queries           []string `json:"queries"`
	Include           string   `json:"include,omitempty"`
	MaxResults        int      `json:"max_results"`
	PageLimit         uint32   `json:"page_limit"`
	PageIndex         uint32   `json:"page_index,omitempty"`
	TimeoutMillis     int64    `json:"timeout_ms"`
	ContentMode       string   `json:"content_mode,omitempty"`
	FileOffset        uint32   `json:"file_offset,omitempty"`
	MaxMatchesPerFile uint32   `json:"max_matches_per_file,omitempty"`
	BeforeContext     uint32   `json:"before_context,omitempty"`
	AfterContext      uint32   `json:"after_context,omitempty"`
}

type Diagnostics struct {
	ColdStartCount        uint64 `json:"cold_start_count,omitempty"`
	InitialScanMillis     int64  `json:"initial_scan_ms,omitempty"`
	WatcherWaitMillis     int64  `json:"watcher_wait_ms,omitempty"`
	WatcherReady          bool   `json:"watcher_ready"`
	IndexAgeMillis        int64  `json:"index_age_ms,omitempty"`
	RequestDurationMillis int64  `json:"request_duration_ms,omitempty"`
	ProtocolFailureCount  uint64 `json:"protocol_failure_count,omitempty"`
	RootFailureCount      uint64 `json:"root_failure_count,omitempty"`
}

type Response struct {
	ProtocolVersion  int                    `json:"protocol_version,omitempty"`
	RequestID        string                 `json:"request_id,omitempty"`
	Completed        bool                   `json:"completed"`
	Content          GrepQueryResult        `json:"content,omitempty"`
	ContentResults   []GrepQueryResult      `json:"content_results,omitempty"`
	FileResults      []SearchQueryResult    `json:"file_results,omitempty"`
	DirectoryResults []DirectoryQueryResult `json:"directory_results,omitempty"`
	MixedResults     []MixedQueryResult     `json:"mixed_results,omitempty"`
	HelperError      string                 `json:"helper_error,omitempty"`
	ErrorCode        string                 `json:"error_code,omitempty"`
	Diagnostics      Diagnostics            `json:"diagnostics,omitempty"`
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

type DirectoryQueryResult struct {
	Query   string              `json:"query"`
	Mode    string              `json:"mode"`
	Items   []fff.DirectoryItem `json:"items,omitempty"`
	Metrics fff.SearchMetrics   `json:"metrics,omitempty"`
	Error   string              `json:"error,omitempty"`
}

type MixedQueryResult struct {
	Query   string            `json:"query"`
	Mode    string            `json:"mode"`
	Items   []fff.MixedItem   `json:"items,omitempty"`
	Metrics fff.SearchMetrics `json:"metrics,omitempty"`
	Error   string            `json:"error,omitempty"`
}
