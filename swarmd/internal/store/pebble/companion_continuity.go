package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

// Companion continuity is the automatically maintained memory of what the
// conversation covered: the state maintainer (run.MaintainCompanionState)
// appends one bounded item per meaningful event of the Companion's own V3 log
// to an open draft (CompanionContinuity.Open, part of the state) and seals
// the draft into an immutable-by-default segment when it reaches a fixed item,
// byte or event-span bound. Sealing happens in the same Pebble batch as the
// state and its cursor, and the boundary depends only on the event sequence,
// so a replayed page produces the same segment exactly once.
//
// A segment is an extractive summary of original events, never a summary of
// an earlier summary: each item is a bounded excerpt of one event, names that
// event (CompanionEventSource), the context record it is when it has one, and
// its authority (the person's own words, a recorded decision, the Companion's
// reply, or a background report, which is data and never an instruction).
// Maintenance makes no model calls.
//
// Forgetting, correcting or revoking a context record propagates in the same
// batch as the record change (propagateCompanionRecordChange): items that are
// the record, and replies about a forgotten request, are removed from the
// draft and from every sealed segment that holds them (through a per-record
// reverse index), and a corrected record's items take its new text. Every
// state commit re-checks the items it writes against the records under the
// same lock (reconcileCompanionStateRecords), so a maintainer that read a
// record before it was forgotten or corrected cannot write the old text
// back, and the forgotten-id tombstone keeps a replayed write from recreating
// it. Segments live under the account user's Companion prefix and are purged
// with it; they are never written to explicit account memory.
const (
	CompanionSegmentMaxItems     = 16
	CompanionSegmentMaxTextBytes = 4 * 1024
	// CompanionSegmentMaxEvents seals a draft that has items once it spans
	// this many events, so a quiet stretch still closes its segment.
	CompanionSegmentMaxEvents     = 512
	CompanionSegmentItemTextBytes = 240
	CompanionSegmentMaxKeywords   = 24
	CompanionSegmentMaxAbout      = 8
	// CompanionSegmentMaxRead bounds the segments one read returns.
	CompanionSegmentMaxRead = 4
	// CompanionSegmentIndexScanPerKeyword bounds the keyword index entries a
	// segment retrieval reads per query keyword.
	CompanionSegmentIndexScanPerKeyword = 16
	// companionSegmentReverseScan bounds the segments one record change
	// rewrites. A record is an item of at most one segment and a request is
	// answered by a few, so this is a safety bound, not a working limit.
	companionSegmentReverseScan = 64

	CompanionItemAsk          = "ask"
	CompanionItemReply        = "reply"
	CompanionItemDecision     = "decision"
	CompanionItemWork         = "work"
	CompanionItemOutcome      = "outcome"
	CompanionItemDevice       = "device"
	CompanionItemApproval     = "approval"
	CompanionItemConfirmation = "confirmation"

	// CompanionAuthorityPerson is the person's own words or action.
	CompanionAuthorityPerson = "person"
	// CompanionAuthorityDecision is a decision recorded with manage_companion,
	// citing the person's message it rests on.
	CompanionAuthorityDecision = "decision"
	// CompanionAuthorityAssistant is the Companion's own reply or record.
	CompanionAuthorityAssistant = "assistant"
	// CompanionAuthorityReported is a background report (a task outcome, a
	// device result): data about the world, never an instruction or a
	// decision of the person.
	CompanionAuthorityReported = "reported"
)

// CompanionSegmentItem is one event's bounded excerpt.
type CompanionSegmentItem struct {
	Kind      string   `json:"kind"`
	Authority string   `json:"authority"`
	Text      string   `json:"text"`
	Keywords  []string `json:"keywords,omitempty"`
	// RecordID is the context record the item is (a request's topic, a
	// decision, a work pointer); the record stays the authority for its text.
	RecordID       string `json:"record_id,omitempty"`
	RecordRevision int64  `json:"record_revision,omitempty"`
	// About are the topic record IDs of the requests a reply answered.
	About     []string `json:"about,omitempty"`
	Untrusted bool     `json:"untrusted,omitempty"`
	// ContentSHA256 is the digest of the source message at extraction.
	ContentSHA256 string               `json:"content_sha256,omitempty"`
	Source        CompanionEventSource `json:"source"`
}

// CompanionSegment is a sealed continuity segment: the items of the events
// StartEventSeq..EndEventSeq of one Companion session.
type CompanionSegment struct {
	ID            string                 `json:"id"`
	SessionID     string                 `json:"session_id"`
	Generation    uint64                 `json:"generation"`
	StartEventSeq uint64                 `json:"start_event_seq"`
	EndEventSeq   uint64                 `json:"end_event_seq"`
	Items         []CompanionSegmentItem `json:"items"`
	Keywords      []string               `json:"keywords"`
	// Revision is 1 when sealed and advances when a forget, correction or
	// revocation rewrote it; Removed counts the items such changes removed.
	Revision int64  `json:"revision"`
	Removed  uint64 `json:"removed,omitempty"`
	SealedAt int64  `json:"sealed_at"`
}

// CompanionContinuity is the continuity part of the state: the open draft
// and what was sealed.
type CompanionContinuity struct {
	OpenStartSeq  uint64                 `json:"open_start_seq,omitempty"`
	Open          []CompanionSegmentItem `json:"open"`
	OpenBytes     int                    `json:"open_bytes,omitempty"`
	Sealed        uint64                 `json:"sealed"`
	LastSegmentID string                 `json:"last_segment_id,omitempty"`
	LastSealedSeq uint64                 `json:"last_sealed_seq,omitempty"`
	// Removed counts items forgets, corrections and revocations removed
	// from the draft and from sealed segments.
	Removed uint64 `json:"removed,omitempty"`
}

// CompanionSegmentID is the deterministic ID of the segment that starts at
// startSeq of sessionID.
func CompanionSegmentID(sessionID string, startSeq uint64) string {
	raw, _ := json.Marshal([]any{"companion-segment", strings.TrimSpace(sessionID), startSeq})
	sum := sha256.Sum256(raw)
	return "seg-" + hex.EncodeToString(sum[:12])
}

// CompanionMessageDigest is the content digest a context record or segment
// item keeps of its source message.
func CompanionMessageDigest(message MessageSnapshot) string { return companionSourceDigest(message) }

// CompanionSegmentKeywords is a segment's keyword set: each item's keywords
// in order, deduplicated, at most CompanionSegmentMaxKeywords.
func CompanionSegmentKeywords(items []CompanionSegmentItem) []string {
	out := make([]string, 0, CompanionSegmentMaxKeywords)
	seen := map[string]struct{}{}
	for _, item := range items {
		for _, kw := range item.Keywords {
			if _, dup := seen[kw]; dup {
				continue
			}
			seen[kw] = struct{}{}
			out = append(out, kw)
			if len(out) == CompanionSegmentMaxKeywords {
				return out
			}
		}
	}
	return out
}

func companionSegmentKey(prefix, id string) []byte { return []byte(prefix + "g/" + id) }
func companionSegmentRecentKey(prefix string, seg CompanionSegment) []byte {
	return []byte(prefix + "gt/" + companionInvertedTime(int64(seg.EndEventSeq)) + "/" + seg.ID)
}
func companionSegmentKeywordKey(prefix, keyword string, seg CompanionSegment) []byte {
	return []byte(prefix + "gk/" + keyword + "/" + companionInvertedTime(int64(seg.EndEventSeq)) + "/" + seg.ID)
}
func companionSegmentReverseKey(prefix, recordID, segID string) []byte {
	return []byte(prefix + "gi/" + recordID + "/" + segID)
}

func companionItemRecordIDs(item CompanionSegmentItem) []string {
	ids := make([]string, 0, 1+len(item.About))
	if item.RecordID != "" {
		ids = append(ids, item.RecordID)
	}
	return append(ids, item.About...)
}

func (s *SessionStore) getCompanionSegment(prefix, id string) (CompanionSegment, bool, error) {
	var seg CompanionSegment
	ok, err := getJSONFromReader(s.store.db, string(companionSegmentKey(prefix, id)), &seg)
	return seg, ok, err
}

// writeCompanionSegment writes seg with its recency, keyword and reverse
// indexes, replacing previous (nil for a new segment). A segment left with no
// items is deleted with its indexes.
func writeCompanionSegment(batch *pebble.Batch, prefix string, previous *CompanionSegment, seg *CompanionSegment) error {
	if previous != nil {
		if err := batch.Delete(companionSegmentRecentKey(prefix, *previous), nil); err != nil {
			return err
		}
		for _, kw := range previous.Keywords {
			if err := batch.Delete(companionSegmentKeywordKey(prefix, kw, *previous), nil); err != nil {
				return err
			}
		}
		for _, item := range previous.Items {
			for _, id := range companionItemRecordIDs(item) {
				if err := batch.Delete(companionSegmentReverseKey(prefix, id, previous.ID), nil); err != nil {
					return err
				}
			}
		}
	}
	if seg == nil || len(seg.Items) == 0 {
		if previous != nil {
			return batch.Delete(companionSegmentKey(prefix, previous.ID), nil)
		}
		return nil
	}
	payload, err := json.Marshal(seg)
	if err != nil {
		return err
	}
	if err := batch.Set(companionSegmentKey(prefix, seg.ID), payload, nil); err != nil {
		return err
	}
	if err := batch.Set(companionSegmentRecentKey(prefix, *seg), []byte(seg.ID), nil); err != nil {
		return err
	}
	for _, kw := range seg.Keywords {
		if err := batch.Set(companionSegmentKeywordKey(prefix, kw, *seg), []byte(seg.ID), nil); err != nil {
			return err
		}
	}
	for _, item := range seg.Items {
		for _, id := range companionItemRecordIDs(item) {
			if err := batch.Set(companionSegmentReverseKey(prefix, id, seg.ID), []byte(seg.ID), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// companionRecordView is what reconciliation needs to know of one record.
type companionRecordView struct {
	found     bool
	active    bool
	forgotten bool
	record    CompanionContextRecord
}

func (s *SessionStore) companionRecordView(prefix, id string, cache map[string]companionRecordView) (companionRecordView, error) {
	if view, ok := cache[id]; ok {
		return view, nil
	}
	var view companionRecordView
	rec, found, err := s.getCompanionContextRecord(prefix, id)
	if err != nil {
		return view, err
	}
	if found {
		view = companionRecordView{found: true, active: rec.State == CompanionContextStateActive, record: rec}
	} else {
		forgotten, err := getJSONFromReader(s.store.db, string(companionForgottenKey(prefix, id)), &companionForgotten{})
		if err != nil {
			return view, err
		}
		view.forgotten = forgotten
	}
	cache[id] = view
	return view, nil
}

// reconcileCompanionItems drops items whose record is gone or inactive, or
// that answer a forgotten request, and refreshes a corrected record's text.
// It reports the items kept and how many it removed.
func (s *SessionStore) reconcileCompanionItems(prefix string, items []CompanionSegmentItem, cache map[string]companionRecordView) ([]CompanionSegmentItem, uint64, bool, error) {
	kept := make([]CompanionSegmentItem, 0, len(items))
	removed, changed := uint64(0), false
	for _, item := range items {
		drop := false
		if item.RecordID != "" {
			view, err := s.companionRecordView(prefix, item.RecordID, cache)
			if err != nil {
				return nil, 0, false, err
			}
			switch {
			case !view.found || !view.active:
				drop = true
			case view.record.Revision != item.RecordRevision:
				item = companionItemFromRecord(item, view.record)
				changed = true
			}
		}
		for _, about := range item.About {
			if drop {
				break
			}
			view, err := s.companionRecordView(prefix, about, cache)
			if err != nil {
				return nil, 0, false, err
			}
			drop = view.forgotten || (view.found && !view.active)
		}
		if drop {
			removed++
			changed = true
			continue
		}
		kept = append(kept, item)
	}
	return kept, removed, changed, nil
}

// companionItemFromRecord gives an item the current text of its record (a
// correction takes precedence over the text extracted earlier).
func companionItemFromRecord(item CompanionSegmentItem, rec CompanionContextRecord) CompanionSegmentItem {
	text := rec.Title
	if item.Kind == CompanionItemDecision && strings.TrimSpace(rec.Summary) != "" {
		text += " — " + rec.Summary
	}
	item.Text = companionTruncate(strings.Join(strings.Fields(text), " "), CompanionSegmentItemTextBytes)
	item.Keywords = append([]string(nil), rec.Keywords...)
	item.RecordRevision = rec.Revision
	item.ContentSHA256 = rec.Source.ContentSHA256
	return item
}

func companionItemsBytes(items []CompanionSegmentItem) int {
	n := 0
	for _, item := range items {
		n += len(item.Text)
	}
	return n
}

// reconcileCompanionStateRecords applies the records' current state to a
// state about to be committed: topic and decision references to a forgotten,
// deleted or invalidated record are dropped and corrected titles refreshed,
// work pointers lose a record that is gone, and the open draft is reconciled
// like a segment. It runs under companionContextMu, so no forget or
// correction can slip between it and the commit.
//
// Only entries from events after since are checked: everything at or before
// it is already in the stored state, which every forget, correction and
// revocation rewrites in place (propagateCompanionRecordChange), and the
// commit's revision check guarantees the state was derived from that stored
// state. A full check passes since 0. This keeps a commit's reads bounded by
// the events it adds.
func (s *SessionStore) reconcileCompanionStateRecords(prefix string, state *CompanionState, cache map[string]companionRecordView, since uint64) error {
	refs := func(list []CompanionStateRef) ([]CompanionStateRef, error) {
		kept := list[:0]
		for _, ref := range list {
			if ref.Source.EventSeq <= since {
				kept = append(kept, ref)
				continue
			}
			view, err := s.companionRecordView(prefix, ref.RecordID, cache)
			if err != nil {
				return nil, err
			}
			if !view.found || !view.active {
				continue
			}
			ref.Title = companionTruncate(strings.Join(strings.Fields(view.record.Title), " "), CompanionStateMaxTitleBytes)
			kept = append(kept, ref)
		}
		return kept, nil
	}
	var err error
	if state.Topics, err = refs(append([]CompanionStateRef(nil), state.Topics...)); err != nil {
		return err
	}
	if state.Decisions, err = refs(append([]CompanionStateRef(nil), state.Decisions...)); err != nil {
		return err
	}
	work := make([]CompanionWorkRef, 0, len(state.Work))
	for _, ref := range state.Work {
		if ref.RecordID != "" && ref.Source.EventSeq > since {
			view, err := s.companionRecordView(prefix, ref.RecordID, cache)
			if err != nil {
				return err
			}
			if !view.found || !view.active {
				if ref.Kind == CompanionWorkSession {
					continue // the pointer was the record
				}
				ref.RecordID = ""
			}
		}
		work = append(work, ref)
	}
	state.Work = work
	// A reply commitment's title is the person's words: once the request's
	// topic is forgotten, the commitment keeps its identity and events but
	// not the text.
	for i := range state.Commitments {
		c := &state.Commitments[i]
		if c.Kind != CompanionCommitmentReply || c.Title == "" || c.Opened.MessageSeq == 0 || c.Opened.EventSeq <= since {
			continue
		}
		view, err := s.companionRecordView(prefix, CompanionMessageContextID(c.Opened.SessionID, c.Opened.MessageSeq), cache)
		if err != nil {
			return err
		}
		if view.forgotten || (view.found && !view.active) {
			c.Title = ""
		}
	}
	split := 0 // the draft is in event order: items at or before since come first
	for split < len(state.Continuity.Open) && state.Continuity.Open[split].Source.EventSeq <= since {
		split++
	}
	added, removed, _, err := s.reconcileCompanionItems(prefix, state.Continuity.Open[split:], cache)
	if err != nil {
		return err
	}
	open := append(append([]CompanionSegmentItem{}, state.Continuity.Open[:split]...), added...)
	state.Continuity.Open, state.Continuity.Removed = open, state.Continuity.Removed+removed
	state.Continuity.OpenBytes = companionItemsBytes(open)
	if len(open) == 0 {
		state.Continuity.OpenStartSeq = 0
	}
	return nil
}

// propagateCompanionRecordChange carries a record's forget (next nil),
// invalidation or correction into the stored state, focus and sealed
// segments, in batch. It runs under companionContextMu with the record
// change itself, so the change and its propagation commit together. A state
// it changes takes a new revision, so a maintainer that read the old state
// is refused (and retries from the new one).
func (s *SessionStore) propagateCompanionRecordChange(batch *pebble.Batch, prefix, account, user, id string, next *CompanionContextRecord) error {
	cache := map[string]companionRecordView{}
	switch {
	case next == nil:
		cache[id] = companionRecordView{forgotten: true}
	default:
		cache[id] = companionRecordView{found: true, active: next.State == CompanionContextStateActive, record: *next}
	}
	removed := uint64(0)
	segIDs := make([]string, 0, 4)
	reversePrefix := prefix + "gi/" + id + "/"
	if err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: reversePrefix, Limit: companionSegmentReverseScan}, func(_ string, value []byte) (bool, error) {
		segIDs = append(segIDs, string(value))
		return true, nil
	}); err != nil {
		return err
	}
	for _, segID := range segIDs {
		seg, found, err := s.getCompanionSegment(prefix, segID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		items, gone, changed, err := s.reconcileCompanionItems(prefix, seg.Items, cache)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		previous := seg
		seg.Items, seg.Removed, seg.Revision = items, seg.Removed+gone, seg.Revision+1
		seg.Keywords = CompanionSegmentKeywords(items)
		if err := writeCompanionSegment(batch, prefix, &previous, &seg); err != nil {
			return err
		}
		removed += gone
	}
	state, stored, err := s.GetCompanionState(account, user)
	if err != nil {
		return err
	}
	if stored {
		before, _ := json.Marshal(state)
		nextState := state
		nextState.Continuity.Removed += removed
		if err := s.reconcileCompanionStateRecords(prefix, &nextState, cache, 0); err != nil {
			return err
		}
		after, _ := json.Marshal(nextState)
		if string(before) != string(after) {
			nextState.Revision = state.Revision + 1
			payload, err := json.Marshal(nextState)
			if err != nil {
				return err
			}
			if err := batch.Set(companionStateKey(prefix), payload, nil); err != nil {
				return err
			}
		}
	}
	if next == nil || next.State != CompanionContextStateActive {
		var focus CompanionFocus
		if ok, err := getJSONFromReader(s.store.db, string(companionFocusKey(prefix)), &focus); err != nil {
			return err
		} else if ok && focus.RecordID == id {
			if err := batch.Delete(companionFocusKey(prefix), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// CompanionSegmentView is a segment as read now: its items re-checked
// against their records and source messages, with what the check dropped.
type CompanionSegmentView struct {
	CompanionSegment
	// Dropped counts items left out of this read because their record or
	// source message is gone, changed or revoked.
	Dropped int `json:"dropped,omitempty"`
}

// CompanionSegmentReadStats counts what one segment read touched.
type CompanionSegmentReadStats struct {
	IndexKeys int `json:"index_keys"`
	Segments  int `json:"segments"`
	Records   int `json:"records"`
	Sources   int `json:"sources"`
}

// companionSegmentScope is the permission scope of a segment read for a
// turn: an item whose context record is bound to a workspace is kept only
// when allow admits that workspace (nil admits none), the same rule as
// RetrieveCompanionContext. An unscoped read (the owner's state view) keeps
// every item.
type companionSegmentScope struct {
	scoped bool
	allow  func(string) bool
}

func (scope companionSegmentScope) admits(view companionRecordView) bool {
	if !scope.scoped || !view.found || view.record.WorkspacePath == "" {
		return true
	}
	return scope.allow != nil && scope.allow(view.record.WorkspacePath)
}

// verifyCompanionSegment re-checks a segment's items now: each item's record
// (dropped when gone or inactive, its text refreshed when corrected), the
// requests a reply answered (dropped when one was forgotten), each item's
// source message when it cites one (dropped when deleted or changed) and,
// for a scoped read, each record's workspace against the caller's scope.
func (s *SessionStore) verifyCompanionSegment(prefix string, seg CompanionSegment, stats *CompanionSegmentReadStats, scope companionSegmentScope) (CompanionSegmentView, error) {
	cache := map[string]companionRecordView{}
	view := CompanionSegmentView{CompanionSegment: seg}
	items, _, _, err := s.reconcileCompanionItems(prefix, seg.Items, cache)
	if err != nil {
		return view, err
	}
	stats.Records += len(cache)
	kept := make([]CompanionSegmentItem, 0, len(items))
	for _, item := range items {
		if item.RecordID != "" && !scope.admits(cache[item.RecordID]) {
			continue
		}
		if item.ContentSHA256 != "" && item.Source.MessageSeq > 0 && item.RecordID == "" {
			stats.Sources++
			messages, err := listV3SessionMessagesRangeFromReader(s.store.db, item.Source.SessionID, item.Source.MessageSeq, item.Source.MessageSeq, 1)
			if err != nil {
				return view, err
			}
			if len(messages) != 1 || messages[0].GlobalSeq != item.Source.MessageSeq || companionSourceDigest(messages[0]) != item.ContentSHA256 {
				continue
			}
		}
		kept = append(kept, item)
	}
	view.Dropped = len(seg.Items) - len(kept)
	view.Items = kept
	return view, nil
}

// ListCompanionSegments reads the newest sealed segments of the account
// user's Companion session, verified now: at most limit (bounded by
// CompanionSegmentMaxRead) segments and their items' records and sources.
func (s *SessionStore) ListCompanionSegments(account, user, sessionID string, limit int) ([]CompanionSegmentView, CompanionSegmentReadStats, error) {
	return s.listCompanionSegments(account, user, sessionID, limit, 0, companionSegmentScope{})
}

// ListCompanionSegmentsInScope reads, for one turn, the newest sealed
// segments that start before beforeSeq (0: any), verified now and limited to
// the caller's workspace scope. It reads at most 2*limit+companionSegmentTailSkip
// recency keys and limit segments, whatever the history size.
func (s *SessionStore) ListCompanionSegmentsInScope(account, user, sessionID string, limit int, beforeSeq uint64, allow func(string) bool) ([]CompanionSegmentView, CompanionSegmentReadStats, error) {
	return s.listCompanionSegments(account, user, sessionID, limit, beforeSeq, companionSegmentScope{scoped: true, allow: allow})
}

// companionSegmentTailSkip bounds the extra recency keys a turn's read may
// pass over for segments that lie wholly inside its recent-message tail.
const companionSegmentTailSkip = 8

func (s *SessionStore) listCompanionSegments(account, user, sessionID string, limit int, beforeSeq uint64, scope companionSegmentScope) ([]CompanionSegmentView, CompanionSegmentReadStats, error) {
	var stats CompanionSegmentReadStats
	account, user, sessionID = strings.TrimSpace(account), strings.TrimSpace(user), strings.TrimSpace(sessionID)
	if !validCompanionPrincipal(account, user) || sessionID == "" {
		return nil, stats, ErrCompanionContextInvalid
	}
	if limit <= 0 || limit > CompanionSegmentMaxRead {
		limit = CompanionSegmentMaxRead
	}
	prefix := companionContextPrefix(account, user)
	ids := make([]string, 0, limit)
	// Segments of an earlier Companion generation are skipped; each costs
	// one index key, and the scan reads at most twice the limit (plus the
	// segments inside a turn's tail when beforeSeq is set).
	scanLimit := limit * 2
	if beforeSeq > 0 {
		scanLimit += companionSegmentTailSkip
	}
	if err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: prefix + "gt/", Limit: scanLimit}, func(_ string, value []byte) (bool, error) {
		stats.IndexKeys++
		ids = append(ids, string(value))
		return true, nil
	}); err != nil {
		return nil, stats, err
	}
	out := make([]CompanionSegmentView, 0, limit)
	for _, id := range ids {
		if len(out) == limit {
			break
		}
		stats.Segments++
		seg, found, err := s.getCompanionSegment(prefix, id)
		if err != nil {
			return nil, stats, err
		}
		if !found || seg.SessionID != sessionID || (beforeSeq > 0 && seg.StartEventSeq >= beforeSeq) {
			continue
		}
		view, err := s.verifyCompanionSegment(prefix, seg, &stats, scope)
		if err != nil {
			return nil, stats, err
		}
		if scope.scoped && len(view.Items) == 0 {
			continue
		}
		out = append(out, view)
	}
	return out, stats, nil
}

// RetrieveCompanionSegments returns the sealed segments of the Companion
// session that best match keywords (matched keyword count, then newest),
// verified now. It reads at most CompanionContextMaxQueryKeywords *
// CompanionSegmentIndexScanPerKeyword index keys and 2*limit segments,
// whatever the history size. There is no semantic search: matching is the
// same deterministic keyword extraction as the context bank.
func (s *SessionStore) RetrieveCompanionSegments(account, user, sessionID string, keywords []string, limit int) ([]CompanionSegmentView, CompanionSegmentReadStats, error) {
	return s.retrieveCompanionSegments(account, user, sessionID, keywords, limit, companionSegmentScope{})
}

// RetrieveCompanionSegmentsInScope is RetrieveCompanionSegments for one turn:
// items whose context record is bound to a workspace outside allow are left
// out (nil allows none), as RetrieveCompanionContext leaves such records out.
func (s *SessionStore) RetrieveCompanionSegmentsInScope(account, user, sessionID string, keywords []string, limit int, allow func(string) bool) ([]CompanionSegmentView, CompanionSegmentReadStats, error) {
	return s.retrieveCompanionSegments(account, user, sessionID, keywords, limit, companionSegmentScope{scoped: true, allow: allow})
}

func (s *SessionStore) retrieveCompanionSegments(account, user, sessionID string, keywords []string, limit int, scope companionSegmentScope) ([]CompanionSegmentView, CompanionSegmentReadStats, error) {
	var stats CompanionSegmentReadStats
	account, user, sessionID = strings.TrimSpace(account), strings.TrimSpace(user), strings.TrimSpace(sessionID)
	if !validCompanionPrincipal(account, user) || sessionID == "" {
		return nil, stats, ErrCompanionContextInvalid
	}
	if limit <= 0 || limit > CompanionSegmentMaxRead {
		limit = CompanionSegmentMaxRead
	}
	keywords = normalizeCompanionKeywords(keywords, CompanionContextMaxQueryKeywords)
	if len(keywords) == 0 {
		return nil, stats, nil
	}
	prefix := companionContextPrefix(account, user)
	candidates := map[string]*companionCandidate{}
	for _, kw := range keywords {
		indexPrefix := prefix + "gk/" + kw + "/"
		if err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: indexPrefix, Limit: CompanionSegmentIndexScanPerKeyword}, func(key string, value []byte) (bool, error) {
			stats.IndexKeys++
			id := string(value)
			recency, _, _ := strings.Cut(strings.TrimPrefix(key, indexPrefix), "/")
			if c, ok := candidates[id]; ok {
				c.score++
			} else {
				candidates[id] = &companionCandidate{id: id, score: 1, recency: recency}
			}
			return true, nil
		}); err != nil {
			return nil, stats, err
		}
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
	if len(ordered) > limit*2 {
		ordered = ordered[:limit*2]
	}
	out := make([]CompanionSegmentView, 0, limit)
	for _, c := range ordered {
		if len(out) == limit {
			break
		}
		stats.Segments++
		seg, found, err := s.getCompanionSegment(prefix, c.id)
		if err != nil {
			return nil, stats, err
		}
		if !found || seg.SessionID != sessionID {
			continue
		}
		view, err := s.verifyCompanionSegment(prefix, seg, &stats, scope)
		if err != nil {
			return nil, stats, err
		}
		if len(view.Items) > 0 {
			out = append(out, view)
		}
	}
	return out, stats, nil
}

// FilterCompanionItemsInScope re-checks continuity items held in the state's
// open draft for one turn: an item whose record is gone, inactive or answers
// a forgotten request is dropped (as a sealed segment read drops it), a
// corrected record's text is refreshed, and an item whose record is bound to
// a workspace outside allow (nil allows none) is left out. It reads at most
// one record per distinct record ID among items, so a draft's bound
// (CompanionSegmentMaxItems) bounds it.
func (s *SessionStore) FilterCompanionItemsInScope(account, user string, items []CompanionSegmentItem, allow func(string) bool) ([]CompanionSegmentItem, int, error) {
	account, user = strings.TrimSpace(account), strings.TrimSpace(user)
	if !validCompanionPrincipal(account, user) {
		return nil, 0, ErrCompanionContextInvalid
	}
	prefix := companionContextPrefix(account, user)
	cache := map[string]companionRecordView{}
	kept, _, _, err := s.reconcileCompanionItems(prefix, items, cache)
	if err != nil {
		return nil, len(cache), err
	}
	scope := companionSegmentScope{scoped: true, allow: allow}
	out := kept[:0]
	for _, item := range kept {
		if item.RecordID != "" && !scope.admits(cache[item.RecordID]) {
			continue
		}
		out = append(out, item)
	}
	return out, len(cache), nil
}

// validateCompanionSegment checks a segment the maintainer seals.
func validateCompanionSegment(seg CompanionSegment, sessionID string, generation uint64) error {
	switch {
	case seg.ID == "" || seg.ID != CompanionSegmentID(sessionID, seg.StartEventSeq):
		return fmt.Errorf("%w: segment id", ErrCompanionContextInvalid)
	case seg.SessionID != sessionID || seg.Generation != generation:
		return fmt.Errorf("%w: segment session", ErrCompanionContextInvalid)
	case seg.StartEventSeq == 0 || seg.EndEventSeq < seg.StartEventSeq:
		return fmt.Errorf("%w: segment range", ErrCompanionContextInvalid)
	case len(seg.Items) > CompanionSegmentMaxItems || companionItemsBytes(seg.Items) > CompanionSegmentMaxTextBytes:
		return fmt.Errorf("%w: segment exceeds its bounds", ErrCompanionContextInvalid)
	}
	return nil
}
