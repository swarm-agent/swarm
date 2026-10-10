package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

// Signals are this machine's durable feed of high-level events that a monitor,
// an alerting service or an AI follows with a cursor: an agent waiting on a
// person, a failed run, a refused key, an inactive sandbox, or an event an
// outside source (a host security monitor, a script) reported. A signal holds
// a kind, a severity, short ids and a one-line summary; it never holds
// transcripts, command arguments, provider payloads or secret values.
//
// The feed is append-only and numbered. It keeps the newest MaxSignals entries;
// a reader whose cursor fell behind the oldest kept entry is told so (Gap)
// instead of silently skipping what was dropped.
const (
	KeySignalPrefix        = "signal/"
	keySignalSequence      = "signal_meta/seq"
	keySignalOldest        = "signal_meta/oldest"
	MaxSignals             = 50_000
	MaxSignalSummaryRunes  = 300
	MaxSignalRefs          = 12
	MaxSignalAttrs         = 16
	MaxSignalValueRunes    = 200
	MaxSignalListLimit     = 500
	maxSignalScanPerList   = 5_000
	SignalSourceSwarmd     = "swarmd"
	SignalSourceExternal   = "external"
	SignalKindExternalRoot = "external"
)

const (
	SignalSeverityInfo     = "info"
	SignalSeverityWarning  = "warning"
	SignalSeverityCritical = "critical"
)

var (
	signalKindPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$`)
	signalFieldPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	signalSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

	signalSeverityRank = map[string]int{SignalSeverityInfo: 0, SignalSeverityWarning: 1, SignalSeverityCritical: 2}
)

type Signal struct {
	Seq      uint64            `json:"seq"`
	ID       string            `json:"id"`
	At       int64             `json:"at"`
	Kind     string            `json:"kind"`
	Severity string            `json:"severity"`
	Source   string            `json:"source"`
	Account  string            `json:"account,omitempty"`
	Summary  string            `json:"summary"`
	DedupKey string            `json:"dedup_key,omitempty"`
	Refs     map[string]string `json:"refs,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
}

// SignalFilter selects signals for one reader. Account is the reader's
// account: it sees that account's signals and machine-wide ones (no account).
type SignalFilter struct {
	Account      string
	KindPrefixes []string
	MinSeverity  string
}

type SignalPage struct {
	Signals   []Signal `json:"signals"`
	NextAfter uint64   `json:"next_after"`
	OldestSeq uint64   `json:"oldest_seq"`
	LatestSeq uint64   `json:"latest_seq"`
	Gap       bool     `json:"gap"`
}

type SignalStore struct {
	store *Store
	now   func() time.Time
	keep  uint64
	mu    sync.Mutex
}

func NewSignalStore(store *Store) *SignalStore {
	return &SignalStore{store: store, now: time.Now, keep: MaxSignals}
}

func signalKey(seq uint64) string {
	return fmt.Sprintf("%s%020d", KeySignalPrefix, seq)
}

// ValidSignalSeverity reports whether severity is one of info, warning, critical.
func ValidSignalSeverity(severity string) bool {
	_, ok := signalSeverityRank[severity]
	return ok
}

// NormalizeSignal validates sig and bounds every free-text field. It does not
// assign Seq, ID or At.
func NormalizeSignal(sig Signal) (Signal, error) {
	sig.Kind = strings.TrimSpace(sig.Kind)
	if !signalKindPattern.MatchString(sig.Kind) {
		return Signal{}, errors.New("signal kind must be 2 to 4 dot-separated lowercase words, such as agent.blocked")
	}
	sig.Severity = strings.ToLower(strings.TrimSpace(sig.Severity))
	if sig.Severity == "" {
		sig.Severity = SignalSeverityInfo
	}
	if !ValidSignalSeverity(sig.Severity) {
		return Signal{}, errors.New("signal severity must be info, warning or critical")
	}
	sig.Source = strings.TrimSpace(sig.Source)
	if sig.Source == "" {
		sig.Source = SignalSourceSwarmd
	}
	sig.Account = strings.TrimSpace(sig.Account)
	sig.Summary = boundRunes(oneLine(sig.Summary), MaxSignalSummaryRunes)
	if sig.Summary == "" {
		return Signal{}, errors.New("signal summary is required")
	}
	sig.DedupKey = boundRunes(oneLine(sig.DedupKey), MaxSignalValueRunes)
	var err error
	if sig.Refs, err = normalizeSignalFields("ref", sig.Refs, MaxSignalRefs); err != nil {
		return Signal{}, err
	}
	if sig.Attrs, err = normalizeSignalFields("attribute", sig.Attrs, MaxSignalAttrs); err != nil {
		return Signal{}, err
	}
	return sig, nil
}

// ValidSignalSourceName reports whether name may label an outside source
// ("external:<name>").
func ValidSignalSourceName(name string) bool {
	return signalSourcePattern.MatchString(name)
}

func normalizeSignalFields(label string, in map[string]string, max int) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > max {
		return nil, fmt.Errorf("a signal has at most %d %ss", max, label)
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if !signalFieldPattern.MatchString(k) {
			return nil, fmt.Errorf("signal %s name %q must be lowercase letters, digits and _", label, k)
		}
		v = boundRunes(oneLine(v), MaxSignalValueRunes)
		if v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func boundRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

// Append validates sig, numbers it and stores it durably, dropping the oldest
// entries beyond MaxSignals in the same batch.
func (s *SignalStore) Append(sig Signal) (Signal, error) {
	if s == nil || s.store == nil {
		return Signal{}, errors.New("signal store is not configured")
	}
	sig, err := NormalizeSignal(sig)
	if err != nil {
		return Signal{}, err
	}
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return Signal{}, fmt.Errorf("signal id: %w", err)
	}
	sig.ID = "sig_" + hex.EncodeToString(idBytes[:])
	sig.At = s.now().UnixMilli()

	s.mu.Lock()
	defer s.mu.Unlock()
	latest, oldest, err := s.boundsLocked()
	if err != nil {
		return Signal{}, err
	}
	sig.Seq = latest + 1
	payload, err := json.Marshal(sig)
	if err != nil {
		return Signal{}, fmt.Errorf("marshal signal: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(signalKey(sig.Seq)), payload, nil); err != nil {
		return Signal{}, err
	}
	if err := batch.Set([]byte(keySignalSequence), uint64ToBytes(sig.Seq), nil); err != nil {
		return Signal{}, err
	}
	if oldest == 0 {
		oldest = sig.Seq
	}
	if sig.Seq-oldest+1 > s.keep {
		newOldest := sig.Seq - s.keep + 1
		if err := batch.DeleteRange([]byte(signalKey(oldest)), []byte(signalKey(newOldest)), nil); err != nil {
			return Signal{}, err
		}
		oldest = newOldest
	}
	if err := batch.Set([]byte(keySignalOldest), uint64ToBytes(oldest), nil); err != nil {
		return Signal{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return Signal{}, fmt.Errorf("commit signal: %w", err)
	}
	return sig, nil
}

func (s *SignalStore) boundsLocked() (latest, oldest uint64, err error) {
	for _, item := range []struct {
		key string
		out *uint64
	}{{keySignalSequence, &latest}, {keySignalOldest, &oldest}} {
		raw, ok, err := s.store.GetBytes(item.key)
		if err != nil {
			return 0, 0, fmt.Errorf("read signal bounds: %w", err)
		}
		if ok {
			if *item.out, err = bytesToUint64(raw); err != nil {
				return 0, 0, fmt.Errorf("decode signal bounds: %w", err)
			}
		}
	}
	return latest, oldest, nil
}

// ListAfter returns up to limit signals numbered after the cursor that match
// filter, oldest first. NextAfter is the last position examined (pass it back
// to continue), so filtered-out entries are not re-read. Gap reports that
// entries after the cursor were already dropped by retention.
func (s *SignalStore) ListAfter(after uint64, limit int, filter SignalFilter) (SignalPage, error) {
	if s == nil || s.store == nil {
		return SignalPage{}, errors.New("signal store is not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > MaxSignalListLimit {
		limit = MaxSignalListLimit
	}
	minRank := 0
	if filter.MinSeverity != "" {
		rank, ok := signalSeverityRank[filter.MinSeverity]
		if !ok {
			return SignalPage{}, errors.New("min_severity must be info, warning or critical")
		}
		minRank = rank
	}
	s.mu.Lock()
	latest, oldest, err := s.boundsLocked()
	s.mu.Unlock()
	if err != nil {
		return SignalPage{}, err
	}
	page := SignalPage{Signals: []Signal{}, NextAfter: after, OldestSeq: oldest, LatestSeq: latest}
	if after > latest {
		// A cursor from the future (another machine, or a reset store) never
		// silently resumes: report where the feed actually is.
		page.NextAfter = latest
		page.Gap = true
		return page, nil
	}
	if oldest > 0 && after+1 < oldest {
		page.Gap = true
	}
	scanned := 0
	err = scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: KeySignalPrefix, StartKey: signalKey(after + 1)}, func(_ string, value []byte) (bool, error) {
		var sig Signal
		if err := json.Unmarshal(value, &sig); err != nil {
			return false, fmt.Errorf("decode signal: %w", err)
		}
		scanned++
		page.NextAfter = sig.Seq
		if signalVisible(sig, filter, minRank) {
			page.Signals = append(page.Signals, sig)
		}
		return len(page.Signals) < limit && scanned < maxSignalScanPerList, nil
	})
	if err != nil {
		return SignalPage{}, err
	}
	return page, nil
}

func signalVisible(sig Signal, filter SignalFilter, minRank int) bool {
	if sig.Account != "" && sig.Account != filter.Account {
		return false
	}
	if signalSeverityRank[sig.Severity] < minRank {
		return false
	}
	if len(filter.KindPrefixes) == 0 {
		return true
	}
	for _, prefix := range filter.KindPrefixes {
		if sig.Kind == prefix || strings.HasPrefix(sig.Kind, prefix+".") {
			return true
		}
	}
	return false
}
