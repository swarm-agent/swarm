package design

import (
	"context"
	"errors"
)

// ErrBudgetExceeded is returned by a Model when a spending limit refuses the
// call before anything is spent. The pipeline stops starting new designs.
var ErrBudgetExceeded = errors.New("design: spending limit reached")

// ErrStaleBase is returned by Files.ReplaceIfDigest when the file no longer
// has the digest the job read at the start.
var ErrStaleBase = errors.New("design: file changed since the job read it")

// ModelCall is one model request. The model gets no tools.
type ModelCall struct {
	Phase        string // generate, repair or edit
	Item         int
	Instructions string
	Input        string
}

// ModelReply is the model's text and its reported usage.
type ModelReply struct {
	Text  string
	Usage CallUsage
}

// Model makes one model call.
type Model interface {
	Info() ModelInfo
	Complete(ctx context.Context, call ModelCall) (ModelReply, error)
}

// Checker loads a document once in a browser and reports runtime problems.
// It returns CheckUnavailable when the browser cannot run; that is never a
// pass.
type Checker interface {
	Check(ctx context.Context, html []byte) CheckReport
}

// Files reads and writes workspace files under the job's scope. Paths are
// workspace-relative.
type Files interface {
	// Read returns the file and its hex sha256.
	Read(ctx context.Context, rel string) ([]byte, string, error)
	// CreateExclusive writes a new file and refuses an existing one.
	CreateExclusive(ctx context.Context, rel string, data []byte) error
	// WriteResults replaces the job's results file (create mode only).
	WriteResults(ctx context.Context, rel string, data []byte) error
	// ReplaceIfDigest replaces a file only while it still has wantSHA256;
	// otherwise it returns ErrStaleBase and changes nothing.
	ReplaceIfDigest(ctx context.Context, rel, wantSHA256 string, data []byte) error
}

// Progress receives each finished design and the running batch result.
type Progress interface {
	Item(ItemResult)
	Batch(BatchResult)
}
