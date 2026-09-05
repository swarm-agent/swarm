package pebblestore

import "errors"

const (
	ArtifactV3ManifestFilename = "swarm-artifact.json"
	ArtifactV3ManifestVersion  = "swarm.artifact/v3"
)

var (
	ErrArtifactV3Invalid      = errors.New("artifact v3 git: invalid input")
	ErrArtifactV3NotFound     = errors.New("artifact v3 git: not found")
	ErrArtifactV3Conflict     = errors.New("artifact v3 git: compare-and-swap conflict")
	ErrArtifactV3Unauthorized = errors.New("artifact v3 git: owner does not match")
	ErrArtifactV3Quota        = errors.New("artifact v3 git: quota exceeded")
	ErrArtifactV3Integrity    = errors.New("artifact v3 git: repository integrity failure")
	ErrArtifactV3TxReuse      = errors.New("artifact v3 git: transaction id reused")
)

type ArtifactV3Owner struct {
	AccountScopeID string `json:"account_scope_id"`
	UserID         string `json:"user_id"`
	SessionID      string `json:"session_id"`
}

type ArtifactV3Limits struct {
	MaxFileBytes int64
	MaxTreeBytes int64
	MaxFiles     int
	MaxPathBytes int
	MaxPathDepth int
	MaxRefs      int
	MaxParts     int
	MaxPageSize  int
}

func (l ArtifactV3Limits) normalized() ArtifactV3Limits {
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 64 << 20
	}
	if l.MaxTreeBytes <= 0 {
		l.MaxTreeBytes = 256 << 20
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = 4096
	}
	if l.MaxPathBytes <= 0 {
		l.MaxPathBytes = 512
	}
	if l.MaxPathDepth <= 0 {
		l.MaxPathDepth = 32
	}
	if l.MaxRefs <= 0 {
		l.MaxRefs = 16384
	}
	if l.MaxParts <= 0 {
		l.MaxParts = l.MaxFiles * 4
	}
	if l.MaxPageSize <= 0 {
		l.MaxPageSize = 500
	}
	return l
}

type ArtifactV3Locator struct {
	Kind  string   `json:"kind"`
	Path  string   `json:"path,omitempty"`
	Value string   `json:"value,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

type ArtifactV3Part struct {
	ID      string            `json:"id"`
	Label   string            `json:"label"`
	Locator ArtifactV3Locator `json:"locator"`
	// CaptureTimeMS selects a deterministic temporal preview; nil retains static visibility checks.
	CaptureTimeMS *int64 `json:"capture_time_ms,omitempty"`
}

type ArtifactV3Manifest struct {
	SchemaVersion    string                           `json:"schema_version"`
	Entrypoint       string                           `json:"entrypoint"`
	Parts            []ArtifactV3Part                 `json:"parts"`
	AnimationProfile *SessionArtifactAnimationProfile `json:"animation_profile,omitempty"`
}

// ArtifactV3Project is always a complete conventional project tree.
type ArtifactV3Project struct{ Files map[string][]byte }

type ArtifactV3GenesisRequest struct {
	TransactionID string
	Project       ArtifactV3Project
	Message       string
}
type ArtifactV3CandidateRequest struct {
	TurnID, CandidateID, TransactionID, BaseCommit string
	Project                                        ArtifactV3Project
	Message                                        string
}
type ArtifactV3SelectionRequest struct{ TurnID, CandidateID, TransactionID, ExpectedHead, Candidate string }

type ArtifactV3File struct {
	Path, OID, Mode string
	Size            int64
}
type ArtifactV3Revision struct {
	CommitOID, TreeOID, ManifestBlobOID string
	Parents                             []string
	Manifest                            ArtifactV3Manifest
	FileCount                           int
	TreeBytes                           int64
}
type ArtifactV3FilePage struct {
	Files      []ArtifactV3File
	NextCursor string
}
type ArtifactV3RevisionPage struct {
	Revisions  []ArtifactV3Revision
	NextCursor string
}
type ArtifactV3Ref struct{ Name, CommitOID string }
type ArtifactV3RefPage struct {
	Refs       []ArtifactV3Ref
	NextCursor string
}

type ArtifactV3TransactionState string

const (
	ArtifactV3TransactionRecorded ArtifactV3TransactionState = "recorded"
	ArtifactV3TransactionApplied  ArtifactV3TransactionState = "applied"
)

type ArtifactV3Transaction struct {
	ID, CommitOID, HeadOID string
	State                  ArtifactV3TransactionState
}

type ArtifactV3Repository struct {
	root, path, git, hooks, id string
	owner                      ArtifactV3Owner
	limits                     ArtifactV3Limits
}
