package pebblestore

import (
	"errors"
	"strings"
)

const (
	ArtifactV3RevisionFocusedParts = "focused_parts"
	ArtifactV3RevisionWholeProject = "whole_project"
)

// ValidateArtifactV3RevisionIntent keeps omission conservative for older callers:
// an omitted intent never authorizes structural Part replacement.
func ValidateArtifactV3RevisionIntent(intent string, targets []string) error {
	if intent != "" && intent != ArtifactV3RevisionFocusedParts && intent != ArtifactV3RevisionWholeProject {
		return ErrArtifactV3Invalid
	}
	if len(targets) > 256 || (intent == ArtifactV3RevisionFocusedParts && len(targets) == 0) || (intent == ArtifactV3RevisionWholeProject && len(targets) != 0) {
		return ErrArtifactV3Invalid
	}
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		if target == "" || target != strings.TrimSpace(target) || len(target) > 512 || seen[target] {
			return ErrArtifactV3Invalid
		}
		seen[target] = true
	}
	return nil
}

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
	CaptureTimeMS *int64                   `json:"capture_time_ms,omitempty"`
	Temporal      *ArtifactV3TemporalScene `json:"temporal,omitempty"`
}

// Temporal scenes share the complete project's playhead and may share a selector.
type ArtifactV3TemporalScene struct {
	SceneID string `json:"scene_id"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
}

func (p ArtifactV3Part) PreviewTimeMS() *int64 {
	if p.CaptureTimeMS != nil {
		return p.CaptureTimeMS
	}
	if p.Temporal == nil {
		return nil
	}
	t := p.Temporal.StartMS + (p.Temporal.EndMS-p.Temporal.StartMS)/2
	return &t
}

type ArtifactV3Manifest struct {
	OutputRequirements *SessionArtifactOutputRequirements `json:"output_requirements,omitempty"`
	SchemaVersion      string                             `json:"schema_version"`
	Entrypoint         string                             `json:"entrypoint"`
	Parts              []ArtifactV3Part                   `json:"parts"`
	AnimationProfile   *SessionArtifactAnimationProfile   `json:"animation_profile,omitempty"`
	SceneContract      *ArtifactV3SceneContract           `json:"scene_contract,omitempty"`
}

// SceneContract is explicit authoring policy, not inferred from duration or prose.
type ArtifactV3SceneContract struct {
	DurationMS int64                     `json:"duration_ms"`
	Scenes     []ArtifactV3TemporalScene `json:"scenes"`
}

// ValidateArtifactV3Scenes validates ordered, complete chapters and required policy.
// Duration zero defers only the HTML duration comparison to the trusted preview gate.
func ValidateArtifactV3Scenes(m ArtifactV3Manifest, duration int64) error {
	var previous int64
	count := 0
	for _, p := range m.Parts {
		s := p.Temporal
		if s == nil {
			continue
		}
		if m.AnimationProfile == nil || s.SceneID != p.ID || !artifactV3IDPattern.MatchString(s.SceneID) || s.StartMS != previous || s.EndMS <= s.StartMS || (duration > 0 && s.EndMS > duration) || p.Locator.Kind != "selector" || p.Locator.Path != m.Entrypoint || strings.TrimSpace(p.Locator.Value) == "" {
			return ErrArtifactV3Invalid
		}
		if t := p.CaptureTimeMS; t != nil && (*t < s.StartMS || *t >= s.EndMS) {
			return ErrArtifactV3Invalid
		}
		if c := m.SceneContract; c != nil {
			if count >= len(c.Scenes) || c.Scenes[count] != *s {
				return ErrArtifactV3Invalid
			}
		}
		previous = s.EndMS
		count++
	}
	if count > 256 || (count > 0 && duration > 0 && previous != duration) {
		return ErrArtifactV3Invalid
	}
	if c := m.SceneContract; c != nil {
		if c.DurationMS <= 0 || len(c.Scenes) == 0 || count != len(c.Scenes) || previous != c.DurationMS || (duration > 0 && duration != c.DurationMS) {
			return ErrArtifactV3Invalid
		}
	}
	return nil
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
