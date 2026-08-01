package api

import sessionruntime "swarm/packages/swarmd/internal/session"

// sessionV3StreamFrame preserves only the JSON decoding shape referenced by
// retired per-session stream tests. The runtime stream contract remains removed.
type sessionV3StreamFrame struct {
	OK               bool                         `json:"ok"`
	Type             string                       `json:"type"`
	SessionID        string                       `json:"session_id,omitempty"`
	ParentSessionID  string                       `json:"parent_session_id,omitempty"`
	Relation         string                       `json:"relation,omitempty"`
	LineageKind      string                       `json:"lineage_kind,omitempty"`
	AfterSeq         uint64                       `json:"after_seq,omitempty"`
	HighWatermarkSeq uint64                       `json:"high_watermark_seq,omitempty"`
	LastSeq          uint64                       `json:"last_seq,omitempty"`
	NextSeq          uint64                       `json:"next_seq,omitempty"`
	Event            *sessionruntime.SessionEvent `json:"event,omitempty"`
	Error            string                       `json:"error,omitempty"`
}
