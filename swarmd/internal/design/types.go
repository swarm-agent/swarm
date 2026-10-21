// Package design makes standalone HTML designs: one model call per design,
// one browser check, and at most one repair call that returns exact edits.
// It owns no storage, model resolution or browser; callers supply those
// through the interfaces in ports.go.
package design

// Limits shared by the pipeline and its callers.
const (
	MaxCount          = 50
	MaxBriefRunes     = 20000
	MaxStyleRunes     = 2000
	MaxVariations     = 50
	MaxVariationRunes = 500
	MaxHTMLBytes      = 4 << 20
	DefaultFolder     = "designs"
	ResultsFile       = "results.json"
	SchemaVersion     = "swarm.design-results/v1"
	maxDiagnostics    = 20
	maxDiagnosticLen  = 512
)

// Mode says whether a request makes new designs or edits one existing file.
type Mode string

const (
	ModeCreate Mode = "create"
	ModeEdit   Mode = "edit"
)

// Status is one design's outcome.
type Status string

const (
	StatusPassed           Status = "passed"            // first check found no errors
	StatusRepaired         Status = "repaired"          // the one repair fixed every error
	StatusFailed           Status = "failed"            // errors remain after the one repair (or the repair was refused)
	StatusCheckUnavailable Status = "check_unavailable" // the browser check could not run; never a pass
	StatusGenerationFailed Status = "generation_failed" // the model call failed or returned no document
	StatusSkippedBudget    Status = "skipped_budget"    // the spending limit stopped the batch before this design
	StatusCancelled        Status = "cancelled"
	StatusStaleBase        Status = "stale_base"   // edit mode: the file changed while the job ran
	StatusEditRefused      Status = "edit_refused" // edit mode: the requested edits did not apply exactly
)

// Request is one design job.
type Request struct {
	Brief      string
	Count      int
	Style      string
	Variations []string
	// Folder is the workspace-relative directory the designs are written to
	// (create mode). The caller makes it unique per job.
	Folder string
	// EditPath is the workspace-relative HTML file to edit (edit mode).
	EditPath string
}

// Mode reports the request's mode.
func (r Request) Mode() Mode {
	if r.EditPath != "" {
		return ModeEdit
	}
	return ModeCreate
}

// Diagnostic is one problem a check found.
type Diagnostic struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	URL     string `json:"url,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// CheckStatus is a check's verdict.
type CheckStatus string

const (
	CheckOK          CheckStatus = "ok"
	CheckErrors      CheckStatus = "errors"
	CheckUnavailable CheckStatus = "unavailable"
)

// CheckReport is one check of one document.
type CheckReport struct {
	Status      CheckStatus  `json:"status"`
	Code        string       `json:"code,omitempty"` // why the check was unavailable
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	DurationMS  int64        `json:"duration_ms"`
}

// CallUsage is what one model call reported. Tokens and cost come only from
// the provider's response; Reported is false when it reported nothing.
type CallUsage struct {
	Phase       string   `json:"phase"`
	Provider    string   `json:"provider,omitempty"`
	Model       string   `json:"model,omitempty"`
	Input       int64    `json:"input_tokens"`
	Output      int64    `json:"output_tokens"`
	Thinking    int64    `json:"thinking_tokens,omitempty"`
	CacheRead   int64    `json:"cache_read_tokens,omitempty"`
	CacheWrite  int64    `json:"cache_write_tokens,omitempty"`
	Total       int64    `json:"total_tokens"`
	CostUSD     *float64 `json:"cost_usd,omitempty"`
	PriceStatus string   `json:"price_status,omitempty"`
	Reported    bool     `json:"reported"`
	Attempts    int      `json:"attempts,omitempty"` // 2 when an attempt that produced nothing was sent again
	DurationMS  int64    `json:"duration_ms"`
}

// RepairOutcome records the one repair call.
type RepairOutcome struct {
	Edits   int    `json:"edits"`
	Refused string `json:"refused,omitempty"` // why the edits were not applied
}

// ItemResult is one design's result.
type ItemResult struct {
	Index       int            `json:"n"`
	Path        string         `json:"path,omitempty"`
	SHA256      string         `json:"sha256,omitempty"`
	Status      Status         `json:"status"`
	Error       string         `json:"error,omitempty"`
	Variation   string         `json:"variation,omitempty"`
	CheckBefore *CheckReport   `json:"check,omitempty"`
	Repair      *RepairOutcome `json:"repair,omitempty"`
	CheckAfter  *CheckReport   `json:"check_after_repair,omitempty"`
	Calls       []CallUsage    `json:"calls,omitempty"`
	DurationMS  int64          `json:"duration_ms"`
}

// Totals sums the reported usage of every call in a job.
type Totals struct {
	Calls        int     `json:"calls"`
	Tokens       int64   `json:"tokens"`
	CostUSD      float64 `json:"cost_usd"`
	CostComplete bool    `json:"cost_complete"` // false when any call reported no usage or no price
}

// BatchResult is the whole job's result, written as results.json in create mode.
type BatchResult struct {
	SchemaVersion   string         `json:"schema_version"`
	Mode            Mode           `json:"mode"`
	Brief           string         `json:"brief"`
	Folder          string         `json:"folder,omitempty"`
	EditPath        string         `json:"edit_path,omitempty"`
	Model           ModelInfo      `json:"model"`
	Items           []ItemResult   `json:"items"`
	Counts          map[Status]int `json:"counts"`
	Totals          Totals         `json:"totals"`
	EditBaseSHA256  string         `json:"edit_base_sha256,omitempty"`
	EditFinalSHA256 string         `json:"edit_final_sha256,omitempty"`
	ResultsError    string         `json:"results_error,omitempty"` // writing results.json failed
	StartedAt       int64          `json:"started_at"`
	FinishedAt      int64          `json:"finished_at,omitempty"`
}

// ModelInfo names the model a job used.
type ModelInfo struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}
