package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	pageSize   = 100
	idleTick   = 30 * time.Second
	maxBackoff = 5 * time.Minute
	// About two hours of retries with backoff: long enough to fix a setting
	// or wait out an outage, short enough that one broken job cannot hold
	// the log back for good.
	maxResultRetries = 30
	keyCursorPrefix  = "outbox_meta/cursor/"
)

// Record is one log record: a signal of the machine's feed, and for a
// finished job the result it produced.
type Record struct {
	V        int               `json:"v"`
	Machine  string            `json:"machine"`
	Seq      uint64            `json:"seq"`
	ID       string            `json:"id,omitempty"`
	At       int64             `json:"at"`
	Kind     string            `json:"kind"`
	Severity string            `json:"severity,omitempty"`
	Source   string            `json:"source,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Refs     map[string]string `json:"refs,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	JobID    string            `json:"job_id,omitempty"`
	Result   string            `json:"result,omitempty"`
}

// Blob is a local file to export under its content hash.
type Blob struct {
	Path   string
	SHA256 string
	Size   int64
	Type   string
}

// Export is what a finished job hands the outbox: its manifest (any JSON
// object; the builder fills in the fields of docs/checkpoints/outbox.md) and
// the files the manifest points to. The outbox removes the files afterwards.
type Export struct {
	JobID    string
	RunID    string
	Manifest map[string]any
	Blobs    []Blob
}

// ResultBuilder builds the export for a signal that ends a job, or returns
// nil when the signal has no result to export.
type ResultBuilder func(ctx context.Context, sig pebblestore.Signal, b *Builder) (*Export, error)

// Feed is the part of the signal store the outbox reads.
type Feed interface {
	ListAfter(after uint64, limit int, filter pebblestore.SignalFilter) (pebblestore.SignalPage, error)
	Appended() <-chan struct{}
}

// Status is what GET /v3/outbox reports.
type Status struct {
	Enabled      bool   `json:"enabled"`
	Destination  string `json:"destination,omitempty"`
	Machine      string `json:"machine,omitempty"`
	Cursor       uint64 `json:"cursor"`
	LatestSeq    uint64 `json:"latest_seq"`
	Lag          uint64 `json:"lag"`
	Exported     int    `json:"exported_records"`
	Results      int    `json:"exported_results"`
	LastExportAt int64  `json:"last_export_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	LastErrorAt  int64  `json:"last_error_at,omitempty"`
	Failing      bool   `json:"failing"`
}

// Exporter appends the feed to the destination, in order, at least once.
type Exporter struct {
	cfg     Config
	dest    Destination
	feed    Feed
	state   *pebblestore.Store
	account func() string
	build   ResultBuilder
	now     func() time.Time

	mu       sync.Mutex
	status   Status
	failures int
	attempts map[uint64]int
}

func NewExporter(cfg Config, dest Destination, feed Feed, state *pebblestore.Store, account func() string, build ResultBuilder) *Exporter {
	e := &Exporter{cfg: cfg, dest: dest, feed: feed, state: state, account: account, build: build, now: time.Now, attempts: map[uint64]int{}}
	e.status = Status{Enabled: true, Destination: cfg.Describe(), Machine: cfg.Machine}
	return e
}

// Status reports progress; a nil exporter is a machine without an outbox.
func (e *Exporter) Status() Status {
	if e == nil {
		return Status{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	status := e.status
	if cursor, err := e.cursor(); err == nil {
		status.Cursor = cursor
	}
	if status.LatestSeq > status.Cursor {
		status.Lag = status.LatestSeq - status.Cursor
	}
	return status
}

// Run exports until ctx ends: promptly after new signals, on a slow tick
// otherwise, and with growing backoff after a failure.
func (e *Exporter) Run(ctx context.Context) {
	for {
		wait := idleTick
		if err := e.ExportOnce(ctx); err != nil && ctx.Err() == nil {
			e.mu.Lock()
			e.failures++
			wait = min(time.Duration(1<<min(e.failures, 8))*5*time.Second, maxBackoff)
			waiting := errors.Is(err, ErrNotReady)
			if !waiting {
				e.status.LastError, e.status.LastErrorAt, e.status.Failing = err.Error(), e.now().UnixMilli(), true
			}
			e.mu.Unlock()
			if !waiting {
				log.Printf("swarmd outbox: %v", err)
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-e.feed.Appended():
			timer.Stop()
		}
	}
}

func (e *Exporter) cursorKey() string {
	sum := sha256.Sum256([]byte(e.cfg.Describe() + "\x00" + e.cfg.Machine))
	return keyCursorPrefix + hex.EncodeToString(sum[:8])
}

func (e *Exporter) cursor() (uint64, error) {
	var cursor uint64
	_, err := e.state.GetJSON(e.cursorKey(), &cursor)
	return cursor, err
}

// ExportOnce exports everything the feed has after the cursor.
func (e *Exporter) ExportOnce(ctx context.Context) error {
	for ctx.Err() == nil {
		cursor, err := e.cursor()
		if err != nil {
			return fmt.Errorf("read cursor: %w", err)
		}
		account := ""
		if e.account != nil {
			account = e.account()
		}
		page, err := e.feed.ListAfter(cursor, pageSize, pebblestore.SignalFilter{Account: account})
		if err != nil {
			return fmt.Errorf("read feed: %w", err)
		}
		e.mu.Lock()
		e.status.LatestSeq = page.LatestSeq
		e.mu.Unlock()
		if page.Gap && page.OldestSeq > cursor+1 {
			gap := Record{V: Version, Machine: e.cfg.Machine, Seq: page.OldestSeq - 1, At: e.now().UnixMilli(), Kind: "outbox.gap",
				Severity: pebblestore.SignalSeverityWarning, Source: "outbox", Summary: "The machine's feed dropped signals before they were exported",
				Attrs: map[string]string{"from": strconv.FormatUint(cursor+1, 10), "to": strconv.FormatUint(page.OldestSeq-1, 10)}}
			if err := e.writeRecord(ctx, gap); err != nil {
				return err
			}
			if err := e.advance(page.OldestSeq - 1); err != nil {
				return err
			}
		}
		for _, sig := range page.Signals {
			if err := e.exportSignal(ctx, sig); err != nil {
				return err
			}
			if err := e.advance(sig.Seq); err != nil {
				return err
			}
		}
		if page.NextAfter > cursor && len(page.Signals) == 0 {
			// Only signals of other accounts: move past them.
			if err := e.advance(page.NextAfter); err != nil {
				return err
			}
		}
		if len(page.Signals) < pageSize || page.NextAfter <= cursor {
			e.mu.Lock()
			e.failures, e.status.Failing = 0, false
			e.mu.Unlock()
			return nil
		}
	}
	return ctx.Err()
}

func (e *Exporter) advance(seq uint64) error {
	if err := e.state.PutJSON(e.cursorKey(), seq); err != nil {
		return fmt.Errorf("save cursor: %w", err)
	}
	return nil
}

func (e *Exporter) exportSignal(ctx context.Context, sig pebblestore.Signal) error {
	rec := Record{V: Version, Machine: e.cfg.Machine, Seq: sig.Seq, ID: sig.ID, At: sig.At, Kind: sig.Kind, Severity: sig.Severity,
		Source: sig.Source, Summary: sig.Summary, Refs: sig.Refs, Attrs: sig.Attrs}
	if e.build != nil {
		e.mu.Lock()
		last := e.attempts[sig.Seq] >= maxResultRetries-1
		e.mu.Unlock()
		export, err := e.build(ctx, sig, &Builder{MaxFileBytes: e.cfg.MaxFileBytes, Machine: e.cfg.Machine, LastAttempt: last, key: e.layoutKey})
		if err != nil {
			e.mu.Lock()
			e.attempts[sig.Seq]++
			tries := e.attempts[sig.Seq]
			e.mu.Unlock()
			if tries < maxResultRetries {
				return fmt.Errorf("build result for signal %d: %w", sig.Seq, err)
			}
			// A result that keeps failing must not stop the log behind it.
			rec.Attrs = withAttr(rec.Attrs, "result_error", truncate(err.Error(), 200))
		} else if export != nil {
			defer export.cleanup()
			key, err := e.writeResult(ctx, export)
			if err != nil {
				return err
			}
			rec.JobID, rec.Result = export.JobID, key
		}
	}
	if err := e.writeRecord(ctx, rec); err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.attempts, sig.Seq)
	e.status.Exported++
	if rec.Result != "" {
		e.status.Results++
	}
	e.status.LastExportAt = e.now().UnixMilli()
	e.mu.Unlock()
	return nil
}

func (e *Exporter) layoutKey(path string) string {
	return "machines/" + e.cfg.Machine + "/" + path
}

func (e *Exporter) writeResult(ctx context.Context, export *Export) (string, error) {
	for _, blob := range export.Blobs {
		if err := e.dest.PutFile(ctx, e.cfg.key(e.layoutKey("blobs/sha256/"+blob.SHA256)), blob.Path, blob.Size, blob.SHA256, blob.Type); err != nil {
			return "", err
		}
	}
	manifest := export.Manifest
	manifest["v"], manifest["machine"], manifest["exported_at"] = Version, e.cfg.Machine, e.now().UnixMilli()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	key := e.layoutKey("results/" + export.RunID + ".json")
	if err := e.dest.PutBytes(ctx, e.cfg.key(key), data, "application/json"); err != nil {
		return "", err
	}
	return key, nil
}

func (e *Exporter) writeRecord(ctx context.Context, rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return e.dest.PutBytes(ctx, e.cfg.key(e.layoutKey(fmt.Sprintf("log/%012d.json", rec.Seq))), data, "application/json")
}

func (x *Export) cleanup() {
	for _, blob := range x.Blobs {
		_ = os.Remove(blob.Path)
	}
}

// Builder gives result builders the outbox's limits and layout.
type Builder struct {
	MaxFileBytes int64
	Machine      string
	// LastAttempt is set on the final try: a builder waiting for a result to
	// settle exports what it has instead of asking to be retried.
	LastAttempt bool
	key         func(string) string
}

// BlobKey is the layout key a blob with this hash is stored under.
func (b *Builder) BlobKey(sha256Hex string) string {
	return b.key("blobs/sha256/" + sha256Hex)
}

func withAttr(attrs map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(attrs)+1)
	for k, v := range attrs {
		out[k] = v
	}
	out[key] = value
	return out
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

// NewBuilder is a Builder for machine outside an exporter (tests, tools).
func NewBuilder(machine string, maxFileBytes int64) *Builder {
	return &Builder{MaxFileBytes: maxFileBytes, Machine: machine, key: func(path string) string { return "machines/" + machine + "/" + path }}
}

// ErrNotReady is a result that is not settled yet: retried with backoff
// like a failure, but not reported as one.
var ErrNotReady = errors.New("the job's result is not settled yet")
