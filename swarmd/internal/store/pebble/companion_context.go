package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

// The Companion context bank is a per-account-user index of the Companion's
// topics, decisions and work pointers. It is separate from explicit account
// memory (memory_store.go), which it never reads or writes, and it holds no
// authoritative copy of the conversation: every record points at its original
// source (a message of one of the user's sessions, or a work session) and is
// verified against that source when retrieved. Every read is bounded by fixed
// per-query limits, so retrieval cost does not grow with the Companion's
// history.
const (
	CompanionContextKindTopic    = "topic"
	CompanionContextKindDecision = "decision"
	CompanionContextKindWork     = "work"

	CompanionContextStateActive      = "active"
	CompanionContextStateInvalidated = "invalidated"

	CompanionContextSourceMessage = "message"
	CompanionContextSourceSession = "session"

	CompanionContextMaxKeywords      = 12
	CompanionContextMaxQueryKeywords = 8
	CompanionContextMaxPage          = 100
	CompanionContextMaxResults       = 16
	// CompanionContextIndexScanPerKeyword bounds the index entries read per
	// query keyword: the newest entries of each keyword are considered.
	CompanionContextIndexScanPerKeyword = 32

	companionContextMaxTitleBytes   = 200
	companionContextMaxSummaryBytes = 1024
	companionContextMaxIDBytes      = 128
	companionContextMaxSourceBytes  = 2048
)

var (
	ErrCompanionContextConflict = errors.New("companion context record changed; reload and retry")
	ErrCompanionContextInvalid  = errors.New("invalid companion context record")
	ErrCompanionContextSource   = errors.New("companion context source is unavailable to this user")
	ErrCompanionContextNotFound = errors.New("companion context record not found")
)

// CompanionContextSource is a record's provenance: the original message
// (session and sequence, with the content digest at index time) or the work
// session it points to.
type CompanionContextSource struct {
	Kind          string `json:"kind"`
	SessionID     string `json:"session_id"`
	MessageSeq    uint64 `json:"message_seq,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
}

type CompanionContextRecord struct {
	ID             string                 `json:"id"`
	AccountScopeID string                 `json:"account_scope_id"`
	UserID         string                 `json:"user_id"`
	Kind           string                 `json:"kind"`
	State          string                 `json:"state"`
	InvalidReason  string                 `json:"invalid_reason,omitempty"`
	Title          string                 `json:"title"`
	Summary        string                 `json:"summary,omitempty"`
	Keywords       []string               `json:"keywords"`
	WorkspacePath  string                 `json:"workspace_path,omitempty"`
	Source         CompanionContextSource `json:"source"`
	Revision       int64                  `json:"revision"`
	CreatedAt      int64                  `json:"created_at"`
	UpdatedAt      int64                  `json:"updated_at"`
}

// CompanionFocus is the current conversational focus: the latest substantive
// topic, so short follow-ups ("yes, do that") keep retrieving its context.
type CompanionFocus struct {
	RecordID   string   `json:"record_id"`
	Keywords   []string `json:"keywords"`
	MessageSeq uint64   `json:"message_seq,omitempty"`
	UpdatedAt  int64    `json:"updated_at"`
}

// CompanionContextReadStats counts the Pebble keys one query read, so callers
// and tests can prove a bounded cost independent of history size.
type CompanionContextReadStats struct {
	IndexKeys int `json:"index_keys"`
	Records   int `json:"records"`
	Sources   int `json:"sources"`
}

func (s *CompanionContextReadStats) add(o CompanionContextReadStats) {
	s.IndexKeys += o.IndexKeys
	s.Records += o.Records
	s.Sources += o.Sources
}

// CompanionContextQuery selects records for one Companion turn or API search.
type CompanionContextQuery struct {
	Keywords   []string
	Kind       string // optional: only this kind
	MaxResults int
	// Sources from ExcludeSessionID at or after ExcludeFromSeq are already in
	// the caller's recent-message tail and are not repeated.
	ExcludeSessionID string
	ExcludeFromSeq   uint64
	// AllowWorkspace is the caller's current permission scope. A record bound
	// to a workspace is returned only when it is allowed; nil allows none.
	AllowWorkspace func(path string) bool
	Now            int64
}

// CompanionContextHit is one verified record. SourceText is a bounded excerpt
// of the original source; it is untrusted user/model content, never
// instructions.
type CompanionContextHit struct {
	Record     CompanionContextRecord `json:"record"`
	Score      int                    `json:"score"`
	SourceText string                 `json:"source_text,omitempty"`
	// SourceSession is the live work session (kind work only), read at
	// retrieval time so status is never a stored copy.
	SourceSession *SessionSnapshot `json:"-"`
}

var companionContextStopwords = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields("the and for are but not you your yours with this that these those from have has had was were will would could should can just then than them they their there here what when where which who whom why how into onto about over under again also only some any all each very more most such own same too out off our ours its it's i'm i've don't doesn't didn't won't can't let let's please thanks thank okay yes yeah sure now get got make made like want need does did done been being one two use using") {
		companionContextStopwords[w] = struct{}{}
	}
}

// CompanionContextKeywords is the deterministic keyword extraction shared by
// indexing and retrieval: lowercase letter/digit tokens of 3 to 32 runes,
// without common stopwords, deduplicated in first-seen order, at most
// CompanionContextMaxKeywords.
func CompanionContextKeywords(text string) []string {
	out := make([]string, 0, CompanionContextMaxKeywords)
	seen := map[string]struct{}{}
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		n := utf8.RuneCountInString(field)
		if n < 3 || n > 32 {
			continue
		}
		if _, stop := companionContextStopwords[field]; stop {
			continue
		}
		if _, dup := seen[field]; dup {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
		if len(out) == CompanionContextMaxKeywords {
			break
		}
	}
	return out
}

func normalizeCompanionKeywords(in []string, max int) []string {
	return CompanionContextKeywordsFromList(in, max)
}

// CompanionContextKeywordsFromList normalizes an explicit keyword list with
// the same rules as CompanionContextKeywords.
func CompanionContextKeywordsFromList(in []string, max int) []string {
	out := CompanionContextKeywords(strings.Join(in, " "))
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// CompanionMessageContextID is the deterministic record ID for a topic
// indexed from one message, so re-indexing the same message is idempotent.
func CompanionMessageContextID(sessionID string, seq uint64) string {
	b, _ := json.Marshal([]any{"companion-message", strings.TrimSpace(sessionID), seq})
	sum := sha256.Sum256(b)
	return "msg-" + hex.EncodeToString(sum[:12])
}

func companionContextPrefix(account, user string) string {
	b, _ := json.Marshal([]string{account, user})
	h := sha256.Sum256(b)
	return "companion_ctx/" + hex.EncodeToString(h[:16]) + "/"
}

func companionInvertedTime(ts int64) string {
	if ts < 0 {
		ts = 0
	}
	return fmt.Sprintf("%020d", math.MaxInt64-ts)
}

func companionRecordKey(prefix, id string) []byte { return []byte(prefix + "r/" + id) }
func companionRecentKey(prefix string, rec CompanionContextRecord) []byte {
	return []byte(prefix + "t/" + companionInvertedTime(rec.UpdatedAt) + "/" + rec.ID)
}
func companionKindKey(prefix string, rec CompanionContextRecord) []byte {
	return []byte(prefix + "n/" + rec.Kind + "/" + companionInvertedTime(rec.UpdatedAt) + "/" + rec.ID)
}
func companionKeywordKey(prefix, keyword string, rec CompanionContextRecord) []byte {
	return []byte(prefix + "k/" + keyword + "/" + companionInvertedTime(rec.UpdatedAt) + "/" + rec.ID)
}
func companionFocusKey(prefix string) []byte { return []byte(prefix + "f") }

func validCompanionContextID(id string) bool {
	if id == "" || len(id) > companionContextMaxIDBytes {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

func companionTruncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}

func companionSourceDigest(message MessageSnapshot) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(message.Role) + "\n" + message.Content))
	return hex.EncodeToString(sum[:])
}

// verifyCompanionContextSource checks a record's source against its
// original: the session must exist, belong to this account user and not be
// deleted; a message source must still exist with the same content digest.
// It returns the current digest, a bounded excerpt and the live session.
func (s *SessionStore) verifyCompanionContextSource(account, user string, src CompanionContextSource) (string, string, *SessionSnapshot, error) {
	sessionID := strings.TrimSpace(src.SessionID)
	if sessionID == "" {
		return "", "", nil, fmt.Errorf("%w: source session_id required", ErrCompanionContextInvalid)
	}
	session, found, err := s.GetSession(sessionID)
	if err != nil {
		return "", "", nil, err
	}
	if !found || session.AccountScopeID != account || session.UserID != user {
		return "", "", nil, fmt.Errorf("%w: source session not found", ErrCompanionContextSource)
	}
	if tomb, ok, err := s.GetV3SessionTombstone(sessionID); err != nil {
		return "", "", nil, err
	} else if ok && tomb.Deleted {
		return "", "", nil, fmt.Errorf("%w: source session deleted", ErrCompanionContextSource)
	}
	switch src.Kind {
	case CompanionContextSourceSession:
		return "", "", &session, nil
	case CompanionContextSourceMessage:
		if src.MessageSeq == 0 {
			return "", "", nil, fmt.Errorf("%w: source message_seq required", ErrCompanionContextInvalid)
		}
		messages, err := listV3SessionMessagesRangeFromReader(s.store.db, sessionID, src.MessageSeq, src.MessageSeq, 1)
		if err != nil {
			return "", "", nil, err
		}
		if len(messages) != 1 || messages[0].GlobalSeq != src.MessageSeq {
			return "", "", nil, fmt.Errorf("%w: source message deleted", ErrCompanionContextSource)
		}
		digest := companionSourceDigest(messages[0])
		if src.ContentSHA256 != "" && src.ContentSHA256 != digest {
			return "", "", nil, fmt.Errorf("%w: source message changed", ErrCompanionContextSource)
		}
		return digest, companionTruncate(messages[0].Content, companionContextMaxSourceBytes), &session, nil
	default:
		return "", "", nil, fmt.Errorf("%w: source kind must be message or session", ErrCompanionContextInvalid)
	}
}

func (s *SessionStore) getCompanionContextRecord(prefix, id string) (CompanionContextRecord, bool, error) {
	var rec CompanionContextRecord
	ok, err := getJSONFromReader(s.store.db, string(companionRecordKey(prefix, id)), &rec)
	return rec, ok, err
}

// GetCompanionContext reads one record of the account user's context bank.
func (s *SessionStore) GetCompanionContext(account, user, id string) (CompanionContextRecord, bool, error) {
	account, user, id = strings.TrimSpace(account), strings.TrimSpace(user), strings.TrimSpace(id)
	if !validCompanionPrincipal(account, user) || !validCompanionContextID(id) {
		return CompanionContextRecord{}, false, ErrCompanionContextInvalid
	}
	return s.getCompanionContextRecord(companionContextPrefix(account, user), id)
}

func sameCompanionContextContent(a, b CompanionContextRecord) bool {
	return a.Kind == b.Kind && a.State == b.State && a.Title == b.Title && a.Summary == b.Summary &&
		strings.Join(a.Keywords, " ") == strings.Join(b.Keywords, " ") && a.WorkspacePath == b.WorkspacePath && a.Source == b.Source
}

func writeCompanionContextIndexes(batch *pebble.Batch, prefix string, rec CompanionContextRecord) error {
	if err := batch.Set(companionRecentKey(prefix, rec), []byte(rec.ID), nil); err != nil {
		return err
	}
	if rec.State != CompanionContextStateActive {
		return nil
	}
	if err := batch.Set(companionKindKey(prefix, rec), []byte(rec.ID), nil); err != nil {
		return err
	}
	for _, kw := range rec.Keywords {
		if err := batch.Set(companionKeywordKey(prefix, kw, rec), []byte(rec.ID), nil); err != nil {
			return err
		}
	}
	return nil
}

func deleteCompanionContextIndexes(batch *pebble.Batch, prefix string, rec CompanionContextRecord) error {
	if err := batch.Delete(companionRecentKey(prefix, rec), nil); err != nil {
		return err
	}
	if err := batch.Delete(companionKindKey(prefix, rec), nil); err != nil {
		return err
	}
	for _, kw := range rec.Keywords {
		if err := batch.Delete(companionKeywordKey(prefix, kw, rec), nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) commitCompanionContext(prefix string, previous *CompanionContextRecord, next *CompanionContextRecord) error {
	batch := s.store.db.NewBatch()
	defer batch.Close()
	if previous != nil {
		if err := deleteCompanionContextIndexes(batch, prefix, *previous); err != nil {
			return err
		}
	}
	if next != nil {
		payload, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := batch.Set(companionRecordKey(prefix, next.ID), payload, nil); err != nil {
			return err
		}
		if err := writeCompanionContextIndexes(batch, prefix, *next); err != nil {
			return err
		}
	} else if previous != nil {
		if err := batch.Delete(companionRecordKey(prefix, previous.ID), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

// PutCompanionContext creates (expectedRevision 0) or replaces (the current
// revision) one record after verifying its source belongs to this account
// user. Repeating an identical write is an idempotent no-op. It returns the
// stored record and whether anything changed.
func (s *SessionStore) PutCompanionContext(rec CompanionContextRecord, expectedRevision int64, now int64) (CompanionContextRecord, bool, error) {
	account, user := strings.TrimSpace(rec.AccountScopeID), strings.TrimSpace(rec.UserID)
	rec.ID = strings.TrimSpace(rec.ID)
	if !validCompanionPrincipal(account, user) || !validCompanionContextID(rec.ID) {
		return CompanionContextRecord{}, false, fmt.Errorf("%w: principal and id ([A-Za-z0-9._:-], at most %d bytes) required", ErrCompanionContextInvalid, companionContextMaxIDBytes)
	}
	switch rec.Kind {
	case CompanionContextKindTopic, CompanionContextKindDecision:
		if rec.Source.Kind != CompanionContextSourceMessage {
			return CompanionContextRecord{}, false, fmt.Errorf("%w: %s records need a message source", ErrCompanionContextInvalid, rec.Kind)
		}
	case CompanionContextKindWork:
		if rec.Source.Kind != CompanionContextSourceSession {
			return CompanionContextRecord{}, false, fmt.Errorf("%w: work records need a session source", ErrCompanionContextInvalid)
		}
	default:
		return CompanionContextRecord{}, false, fmt.Errorf("%w: kind must be topic, decision or work", ErrCompanionContextInvalid)
	}
	rec.AccountScopeID, rec.UserID = account, user
	rec.State, rec.InvalidReason = CompanionContextStateActive, ""
	rec.Title = companionTruncate(rec.Title, companionContextMaxTitleBytes)
	rec.Summary = companionTruncate(rec.Summary, companionContextMaxSummaryBytes)
	rec.WorkspacePath = strings.TrimSpace(rec.WorkspacePath)
	rec.Source.SessionID = strings.TrimSpace(rec.Source.SessionID)
	if rec.Title == "" {
		return CompanionContextRecord{}, false, fmt.Errorf("%w: title required", ErrCompanionContextInvalid)
	}
	if len(rec.Keywords) > 0 {
		rec.Keywords = normalizeCompanionKeywords(rec.Keywords, CompanionContextMaxKeywords)
	} else {
		rec.Keywords = CompanionContextKeywords(rec.Title + " " + rec.Summary)
	}
	digest, _, _, err := s.verifyCompanionContextSource(account, user, rec.Source)
	if err != nil {
		return CompanionContextRecord{}, false, err
	}
	rec.Source.ContentSHA256 = digest

	s.store.companionContextMu.Lock()
	defer s.store.companionContextMu.Unlock()
	prefix := companionContextPrefix(account, user)
	current, found, err := s.getCompanionContextRecord(prefix, rec.ID)
	if err != nil {
		return CompanionContextRecord{}, false, err
	}
	if found {
		rec.Revision, rec.CreatedAt, rec.UpdatedAt = current.Revision, current.CreatedAt, current.UpdatedAt
		if sameCompanionContextContent(current, rec) {
			return current, false, nil
		}
		if expectedRevision != current.Revision {
			return CompanionContextRecord{}, false, ErrCompanionContextConflict
		}
		rec.Revision = current.Revision + 1
		rec.UpdatedAt = max(now, current.UpdatedAt)
		if err := s.commitCompanionContext(prefix, &current, &rec); err != nil {
			return CompanionContextRecord{}, false, err
		}
		return rec, true, nil
	}
	if expectedRevision != 0 {
		return CompanionContextRecord{}, false, ErrCompanionContextConflict
	}
	rec.Revision, rec.CreatedAt, rec.UpdatedAt = 1, now, now
	if err := s.commitCompanionContext(prefix, nil, &rec); err != nil {
		return CompanionContextRecord{}, false, err
	}
	return rec, true, nil
}

// DeleteCompanionContext removes one record and its index entries at the
// expected revision; the user's explicit forgetting of a context item.
func (s *SessionStore) DeleteCompanionContext(account, user, id string, expectedRevision int64) error {
	account, user, id = strings.TrimSpace(account), strings.TrimSpace(user), strings.TrimSpace(id)
	if !validCompanionPrincipal(account, user) || !validCompanionContextID(id) {
		return ErrCompanionContextInvalid
	}
	s.store.companionContextMu.Lock()
	defer s.store.companionContextMu.Unlock()
	prefix := companionContextPrefix(account, user)
	current, found, err := s.getCompanionContextRecord(prefix, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrCompanionContextNotFound
	}
	if current.Revision != expectedRevision {
		return ErrCompanionContextConflict
	}
	if err := s.commitCompanionContext(prefix, &current, nil); err != nil {
		return err
	}
	focus, ok, err := s.GetCompanionFocus(account, user)
	if err == nil && ok && focus.RecordID == id {
		return s.store.db.Delete(companionFocusKey(prefix), pebble.Sync)
	}
	return err
}

// invalidateCompanionContext marks a record whose source was deleted, changed
// or became foreign. It leaves the record listable with its reason but removes
// it from keyword and kind retrieval.
func (s *SessionStore) invalidateCompanionContext(prefix string, rec CompanionContextRecord, reason string, now int64) error {
	s.store.companionContextMu.Lock()
	defer s.store.companionContextMu.Unlock()
	current, found, err := s.getCompanionContextRecord(prefix, rec.ID)
	if err != nil || !found || current.Revision != rec.Revision || current.State != CompanionContextStateActive {
		return err
	}
	next := current
	next.State = CompanionContextStateInvalidated
	next.InvalidReason = companionTruncate(reason, 200)
	next.Revision++
	next.UpdatedAt = max(now, current.UpdatedAt)
	return s.commitCompanionContext(prefix, &current, &next)
}

// PurgeCompanionContext removes the account user's whole context bank. It is
// part of the explicit Companion deletion; it never touches account memory.
func (s *SessionStore) PurgeCompanionContext(account, user string) error {
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return ErrCompanionContextInvalid
	}
	s.store.companionContextMu.Lock()
	defer s.store.companionContextMu.Unlock()
	prefix := companionContextPrefix(account, user)
	return s.store.db.DeleteRange([]byte(prefix), []byte(prefix+"\xff"), pebble.Sync)
}

// SetCompanionFocus records the current conversational focus.
func (s *SessionStore) SetCompanionFocus(account, user string, focus CompanionFocus) error {
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return ErrCompanionContextInvalid
	}
	focus.Keywords = normalizeCompanionKeywords(focus.Keywords, CompanionContextMaxKeywords)
	payload, err := json.Marshal(focus)
	if err != nil {
		return err
	}
	return s.store.db.Set(companionFocusKey(companionContextPrefix(account, user)), payload, pebble.Sync)
}

// CompanionIndexMark is how far the Companion session's own messages have been
// considered for the context bank. Indexing only moves forward from it, so a
// message is indexed at most once and a topic the person explicitly forgot is
// never indexed again from its source message.
type CompanionIndexMark struct {
	SessionID  string `json:"session_id"`
	MessageSeq uint64 `json:"message_seq"`
}

func companionIndexMarkKey(prefix string) []byte { return []byte(prefix + "w") }

// GetCompanionIndexMark reads the index mark (zero when nothing was indexed).
func (s *SessionStore) GetCompanionIndexMark(account, user string) (CompanionIndexMark, error) {
	var mark CompanionIndexMark
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return mark, ErrCompanionContextInvalid
	}
	_, err := getJSONFromReader(s.store.db, string(companionIndexMarkKey(companionContextPrefix(account, user))), &mark)
	return mark, err
}

// AdvanceCompanionIndexMark moves the mark of sessionID forward to seq. It
// never moves backward, and a mark of another session (a deleted Companion's)
// is replaced. It reports whether the mark moved.
func (s *SessionStore) AdvanceCompanionIndexMark(account, user, sessionID string, seq uint64) (bool, error) {
	account, user, sessionID = strings.TrimSpace(account), strings.TrimSpace(user), strings.TrimSpace(sessionID)
	if !validCompanionPrincipal(account, user) || sessionID == "" {
		return false, ErrCompanionContextInvalid
	}
	s.store.companionContextMu.Lock()
	defer s.store.companionContextMu.Unlock()
	key := companionIndexMarkKey(companionContextPrefix(account, user))
	var current CompanionIndexMark
	if _, err := getJSONFromReader(s.store.db, string(key), &current); err != nil {
		return false, err
	}
	if current.SessionID == sessionID && current.MessageSeq >= seq {
		return false, nil
	}
	payload, err := json.Marshal(CompanionIndexMark{SessionID: sessionID, MessageSeq: seq})
	if err != nil {
		return false, err
	}
	return true, s.store.db.Set(key, payload, pebble.Sync)
}

func (s *SessionStore) GetCompanionFocus(account, user string) (CompanionFocus, bool, error) {
	var focus CompanionFocus
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return focus, false, ErrCompanionContextInvalid
	}
	ok, err := getJSONFromReader(s.store.db, string(companionFocusKey(companionContextPrefix(account, user))), &focus)
	return focus, ok, err
}

// ListCompanionContext pages the account user's records newest first. The
// cursor is opaque; pass the returned next cursor to continue. Each page reads
// at most limit+1 index keys and limit records.
func (s *SessionStore) ListCompanionContext(account, user, cursor string, limit int) ([]CompanionContextRecord, string, CompanionContextReadStats, error) {
	var stats CompanionContextReadStats
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return nil, "", stats, ErrCompanionContextInvalid
	}
	if limit <= 0 || limit > CompanionContextMaxPage {
		limit = CompanionContextMaxPage
	}
	prefix := companionContextPrefix(account, user)
	recentPrefix := prefix + "t/"
	start := ""
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		recency, id, ok := strings.Cut(cursor, "/")
		if !ok || len(recency) != 20 || strings.Trim(recency, "0123456789") != "" || !validCompanionContextID(id) {
			return nil, "", stats, fmt.Errorf("%w: cursor", ErrCompanionContextInvalid)
		}
		start = recentPrefix + cursor + "\x00"
	}
	ids, keys := make([]string, 0, limit+1), make([]string, 0, limit+1)
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: recentPrefix, StartKey: start, Limit: limit + 1}, func(key string, value []byte) (bool, error) {
		stats.IndexKeys++
		ids = append(ids, string(value))
		keys = append(keys, strings.TrimPrefix(key, recentPrefix))
		return len(ids) <= limit, nil
	})
	if err != nil {
		return nil, "", stats, err
	}
	next := ""
	if len(ids) > limit {
		ids, keys = ids[:limit], keys[:limit]
		next = keys[len(keys)-1]
	}
	out := make([]CompanionContextRecord, 0, len(ids))
	for _, id := range ids {
		stats.Records++
		rec, ok, err := s.getCompanionContextRecord(prefix, id)
		if err != nil {
			return nil, "", stats, err
		}
		if ok {
			out = append(out, rec)
		}
	}
	return out, next, stats, nil
}

type companionCandidate struct {
	id      string
	score   int
	recency string
}

// RetrieveCompanionContext returns verified, scope-allowed records for a
// query: keyword matches (scored by matched keyword count, then recency) or,
// with Kind and no keywords, the newest records of that kind. Reads are
// bounded by CompanionContextMaxQueryKeywords*CompanionContextIndexScanPerKeyword
// index keys plus 2*MaxResults records and sources, whatever the history size.
// A record whose source was deleted, changed or became foreign is invalidated.
func (s *SessionStore) RetrieveCompanionContext(account, user string, q CompanionContextQuery) ([]CompanionContextHit, CompanionContextReadStats, error) {
	var stats CompanionContextReadStats
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return nil, stats, ErrCompanionContextInvalid
	}
	maxResults := q.MaxResults
	if maxResults <= 0 || maxResults > CompanionContextMaxResults {
		maxResults = CompanionContextMaxResults
	}
	prefix := companionContextPrefix(account, user)
	candidates := map[string]*companionCandidate{}
	scan := func(indexPrefix string, limit int) error {
		return scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: indexPrefix, Limit: limit}, func(key string, value []byte) (bool, error) {
			stats.IndexKeys++
			id := string(value)
			rest := strings.TrimPrefix(key, indexPrefix)
			recency, _, _ := strings.Cut(rest, "/")
			if c, ok := candidates[id]; ok {
				c.score++
				if recency < c.recency {
					c.recency = recency
				}
			} else {
				candidates[id] = &companionCandidate{id: id, score: 1, recency: recency}
			}
			return true, nil
		})
	}
	keywords := normalizeCompanionKeywords(q.Keywords, CompanionContextMaxQueryKeywords)
	if len(keywords) > 0 {
		for _, kw := range keywords {
			if err := scan(prefix+"k/"+kw+"/", CompanionContextIndexScanPerKeyword); err != nil {
				return nil, stats, err
			}
		}
	} else if q.Kind != "" {
		if err := scan(prefix+"n/"+q.Kind+"/", maxResults*2); err != nil {
			return nil, stats, err
		}
	} else {
		return nil, stats, nil
	}
	ordered := make([]companionCandidate, 0, len(candidates))
	for _, c := range candidates {
		ordered = append(ordered, *c)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		if ordered[i].recency != ordered[j].recency {
			return ordered[i].recency < ordered[j].recency
		}
		return ordered[i].id < ordered[j].id
	})
	if len(ordered) > maxResults*2 {
		ordered = ordered[:maxResults*2]
	}
	hits := make([]CompanionContextHit, 0, maxResults)
	for _, c := range ordered {
		if len(hits) == maxResults {
			break
		}
		stats.Records++
		rec, ok, err := s.getCompanionContextRecord(prefix, c.id)
		if err != nil {
			return nil, stats, err
		}
		if !ok || rec.State != CompanionContextStateActive || (q.Kind != "" && rec.Kind != q.Kind) {
			continue
		}
		if rec.WorkspacePath != "" && (q.AllowWorkspace == nil || !q.AllowWorkspace(rec.WorkspacePath)) {
			continue
		}
		if q.ExcludeSessionID != "" && rec.Source.SessionID == q.ExcludeSessionID && q.ExcludeFromSeq > 0 && rec.Source.MessageSeq >= q.ExcludeFromSeq {
			continue
		}
		stats.Sources++
		_, text, session, err := s.verifyCompanionContextSource(account, user, rec.Source)
		if err != nil {
			if errors.Is(err, ErrCompanionContextSource) {
				if invErr := s.invalidateCompanionContext(prefix, rec, err.Error(), q.Now); invErr != nil {
					return nil, stats, invErr
				}
				continue
			}
			return nil, stats, err
		}
		hit := CompanionContextHit{Record: rec, Score: c.score, SourceText: text}
		if rec.Kind == CompanionContextKindWork {
			hit.SourceSession = session
		}
		hits = append(hits, hit)
	}
	return hits, stats, nil
}

// CompanionWorkspaceScope is the one permission rule for workspace-bound
// context records: a record is retrievable only inside one of the caller's
// currently authorized absolute workspace roots. Removing a workspace from the
// scope therefore revokes its records. No roots allows none.
func CompanionWorkspaceScope(roots []string) func(string) bool {
	clean := make([]string, 0, len(roots))
	for _, root := range roots {
		if root = strings.TrimSpace(root); root != "" && filepath.IsAbs(root) {
			clean = append(clean, filepath.Clean(root))
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return func(path string) bool {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			return false
		}
		path = filepath.Clean(path)
		for _, root := range clean {
			rel, err := filepath.Rel(root, path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return true
			}
		}
		return false
	}
}

// AddCompanionContextReadStats sums read statistics across queries.
func AddCompanionContextReadStats(a, b CompanionContextReadStats) CompanionContextReadStats {
	a.add(b)
	return a
}
