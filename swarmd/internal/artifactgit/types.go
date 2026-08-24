// Package artifactgit implements the authoritative, private Git object model for
// Swarm-managed artifacts. Repositories are bare and never share configuration,
// hooks, credentials, remotes, or working trees with a user's repository.
package artifactgit

import (
	"errors"
	"fmt"
)

const ManifestVersion = "swarm.artifact/v1"

var (
	ErrInvalidID      = errors.New("artifactgit: invalid identifier")
	ErrNotFound       = errors.New("artifactgit: not found")
	ErrConflict       = errors.New("artifactgit: compare-and-swap conflict")
	ErrLockedPart     = errors.New("artifactgit: locked part cannot be changed")
	ErrQuotaExceeded  = errors.New("artifactgit: quota exceeded")
	ErrIntegrity      = errors.New("artifactgit: integrity check failed")
	ErrTransactionReuse = errors.New("artifactgit: transaction id already names another commit")
)

type Limits struct {
	MaxBlobBytes       int64
	MaxCompositionBytes int64
	MaxParts           int
	MaxRefs            int
}

func (l Limits) normalized() Limits {
	if l.MaxBlobBytes <= 0 { l.MaxBlobBytes = 64 << 20 }
	if l.MaxCompositionBytes <= 0 { l.MaxCompositionBytes = 256 << 20 }
	if l.MaxParts <= 0 { l.MaxParts = 256 }
	if l.MaxRefs <= 0 { l.MaxRefs = 4096 }
	return l
}

type Part struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	Blob      string `json:"blob"`
	Size      int64  `json:"size"`
	Locked    bool   `json:"locked,omitempty"`
}

type Manifest struct {
	Version   string `json:"version"`
	MediaType string `json:"media_type"`
	Content   *Part  `json:"content,omitempty"`
	Parts     []Part `json:"parts,omitempty"`
}

type BlobInput struct {
	MediaType string
	Bytes     []byte
}

type Genesis struct {
	MediaType string
	Content   *BlobInput
	Parts     map[string]BlobInput
}

type PartChange struct {
	MediaType string
	Bytes     []byte
	Lock      *bool
}

type CandidateRequest struct {
	ID      string
	Base    string
	Content *BlobInput
	Parts   map[string]PartChange
	Message string
}

type Selection struct {
	Commit string
	PartID string
	Lock   *bool
}

type MergeRequest struct {
	ID         string
	Parents    []string
	Selections map[string]Selection
	Message    string
}

type Ref struct { Name, Commit string }

type Commit struct {
	ID       string
	Parents  []string
	Manifest Manifest
}

type Repository struct {
	root, path, git, hooks string
	id string
	limits Limits
}

func invalid(field string) error { return fmt.Errorf("%w: %s", ErrInvalidID, field) }
