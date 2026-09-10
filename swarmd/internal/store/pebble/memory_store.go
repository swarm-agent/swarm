package pebblestore

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

// Memory is contextual guidance, never an access grant. Callers must derive the
// account and actor from authenticated authority, not model-authored arguments.
const (
	MemorySchemaVersion    = 1
	MemoryWorkspaceMapID   = "workspace-map"
	MemoryMaxEntries       = 256
	MemoryMaxHistory       = 100
	MemoryMaxTombstones    = 4096
	MemoryMaxDocumentBytes = 8 * 1024 * 1024
)

var (
	ErrMemoryConflict = errors.New("memory revision conflict")
	ErrMemoryPolicy   = errors.New("memory policy rejected mutation")
	ErrMemoryBudget   = errors.New("memory budget exceeded")
)

type MemorySettings struct {
	// AutomationUserID is assigned from authenticated settings mutations.
	AutomationUserID   string   `json:"automation_user_id,omitempty"`
	ReadEnabled        bool     `json:"read_enabled"`
	RememberEnabled    bool     `json:"remember_enabled"`
	AutomationEnabled  bool     `json:"automation_enabled"`
	Mode               string   `json:"mode"`
	IntervalMinutes    int      `json:"interval_minutes"`
	IncludedWorkspaces []string `json:"included_workspaces"`
	IncludedSessions   []string `json:"included_sessions"`
	ExcludedWorkspaces []string `json:"excluded_workspaces"`
	ExcludedSessions   []string `json:"excluded_sessions"`
	LookbackDays       int      `json:"lookback_days"`
	InputTokens        int      `json:"input_tokens"`
	OutputTokens       int      `json:"output_tokens"`
	StorageTokens      int      `json:"storage_tokens"`
	InjectionTokens    int      `json:"injection_tokens"`
	RetentionDays      int      `json:"retention_days"`
}

func DefaultMemorySettings() MemorySettings {
	return MemorySettings{ReadEnabled: true, RememberEnabled: true, Mode: "manual", IntervalMinutes: 1440, LookbackDays: 30, InputTokens: 16000, OutputTokens: 2000, StorageTokens: 131072, InjectionTokens: 8192, RetentionDays: 90}
}

// Token accounting is a deterministic UTF-8 byte upper-bound, not a provider
// tokenizer estimate. Metadata and historical revisions have a separate byte cap.
func MemoryTokenCount(content string) int { return len([]byte(content)) }

type MemorySource struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	EventSeq    int64  `json:"event_seq"`
}
type MemoryEntry struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"` // rule, orientation, learned
	Content     string         `json:"content"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	Pinned      bool           `json:"pinned"`
	Revision    int64          `json:"revision"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
	ExpiresAt   int64          `json:"expires_at,omitempty"`
	Sources     []MemorySource `json:"sources,omitempty"`
}
type MemoryActor struct {
	Kind  string               `json:"kind"` // user, learned, migration, retention
	ID    string               `json:"id"`
	JobID string               `json:"job_id,omitempty"`
	Model AgentModelAssignment `json:"model"`
}
type MemoryChange struct {
	Revision       int64           `json:"revision"`
	EntryID        string          `json:"entry_id,omitempty"`
	Operation      string          `json:"operation"`
	Timestamp      int64           `json:"timestamp"`
	Actor          MemoryActor     `json:"actor"`
	Reason         string          `json:"reason"`
	Before         *MemoryEntry    `json:"before,omitempty"`
	After          *MemoryEntry    `json:"after,omitempty"`
	SettingsBefore *MemorySettings `json:"settings_before,omitempty"`
	SettingsAfter  *MemorySettings `json:"settings_after,omitempty"`
	Redacted       bool            `json:"redacted"`
}
type MemoryDocument struct {
	SchemaVersion        int               `json:"schema_version"`
	AccountScopeID       string            `json:"account_scope_id"`
	Revision             int64             `json:"revision"`
	Settings             MemorySettings    `json:"settings"`
	Entries              []MemoryEntry     `json:"entries"`
	History              []MemoryChange    `json:"history"`
	Forgotten            []string          `json:"forgotten"` // hashes of IDs/source scope; never removed by restore
	WorkspaceMapMigrated bool              `json:"workspace_map_migrated"`
	TokenMethod          string            `json:"token_method"`
	StoredTokens         int               `json:"stored_tokens"`
	Jobs                 []MemoryJob       `json:"jobs,omitempty"`
	JobCursors           map[string]uint64 `json:"job_cursors,omitempty"`
	JobOffsets           map[string]int    `json:"job_offsets,omitempty"`
	ScanAfter            string            `json:"scan_after,omitempty"`
	NextJobAt            int64             `json:"next_job_at,omitempty"`
}
type MemoryMutation struct {
	ExpectedRevision int64
	Actor            MemoryActor
	Reason           string
	Operation        string // put, delete, forget_source, settings, restore
	Entry            MemoryEntry
	EntryID          string
	Source           MemorySource
	Settings         *MemorySettings
	RestoreRevision  int64
	// Legacy approval input is retained only for decoding historical callers;
	// automatic learning is revision-bound and does not require approval.
	Approved bool
}
type MemoryStore struct {
	store *Store
	now   func() time.Time
}

func NewMemoryStore(store *Store) *MemoryStore { return &MemoryStore{store: store, now: time.Now} }
func memoryKey(account string) string          { return "account_memory:" + hex.EncodeToString([]byte(account)) }
func memoryAccount(account string) (string, error) {
	account = strings.TrimSpace(account)
	if account == "" || len(account) > 256 || !utf8.ValidString(account) {
		return "", fmt.Errorf("memory account is required and bounded")
	}
	return account, nil
}

// GetForAccount atomically migrates the old map and expires content before any
// caller can read history. The old key is removed in the same synchronous batch.
func (s *MemoryStore) GetForAccount(account string) (MemoryDocument, error) {
	account, err := memoryAccount(account)
	if err != nil {
		return MemoryDocument{}, err
	}
	if s == nil || s.store == nil {
		return MemoryDocument{}, errors.New("memory store is not configured")
	}
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, changed, err := s.load(account)
	if err != nil {
		return MemoryDocument{}, err
	}
	if changed {
		if err = s.persist(d); err != nil {
			return MemoryDocument{}, err
		}
	}
	return d, nil
}
func (s *MemoryStore) load(account string) (MemoryDocument, bool, error) {
	var d MemoryDocument
	found, err := s.store.GetJSON(memoryKey(account), &d)
	if err != nil {
		return d, false, err
	}
	changed := false
	if !found {
		d = MemoryDocument{SchemaVersion: 1, AccountScopeID: account, Revision: 1, Settings: DefaultMemorySettings(), WorkspaceMapMigrated: true, TokenMethod: "utf8_bytes_upper_bound"}
		var legacy WorkspaceMap
		ok, err := s.store.GetJSON(KeyWorkspaceMapForAccount(account), &legacy)
		if err != nil {
			return d, false, err
		}
		if ok {
			if err := validateStoredWorkspaceMap(legacy); err != nil {
				return d, false, err
			}
			e := MemoryEntry{ID: MemoryWorkspaceMapID, Kind: "orientation", Content: legacy.Content, Revision: legacy.Revision, CreatedAt: legacy.CreatedAt, UpdatedAt: legacy.UpdatedAt}
			d.Entries = append(d.Entries, e)
			d.History = append(d.History, MemoryChange{Revision: 1, EntryID: e.ID, Operation: "migrate", Timestamp: s.now().UnixMilli(), Actor: MemoryActor{Kind: "migration", ID: "workspace-map"}, Reason: "Preserve existing Workspace Map", After: &e})
		}
		changed = true
	}
	if d.SchemaVersion != 1 || d.AccountScopeID != account || d.Revision < 1 || !d.WorkspaceMapMigrated {
		return d, false, errors.New("invalid memory authority")
	}
	now := s.now().UnixMilli()
	// Legacy review jobs cannot block automatic processing. Preserve cursors and
	// notes; discard uncommitted proposals so their sources are processed anew.
	for i := range d.Jobs {
		if d.Jobs[i].Status == "review" {
			d.Jobs[i].Status = "interrupted"
			d.Jobs[i].Proposal = nil
			d.Jobs[i].Error = "automatic processing will retry uncommitted sources"
			changed = true
		}
	}
	for i := range d.Jobs {
		j := &d.Jobs[i]
		if j.Proposal != nil && (j.CreatedAt+int64(d.Settings.RetentionDays)*86400000 <= now || j.Revision != d.Revision) {
			j.Proposal = nil
			j.Status = "revoked"
			j.Error = "proposal expired or memory changed"
			j.UpdatedAt = now
			changed = true
		}
		if j.Proposal != nil {
			for _, src := range j.Sources {
				sess, ok, err := NewSessionStore(s.store).GetSession(src.SessionID)
				if err != nil {
					return d, false, err
				}
				_, scopeErr := s.memorySessionSource(d, sess, j.UserID)
				if !ok || scopeErr != nil {
					j.Proposal = nil
					j.Status = "revoked"
					j.Error = "source no longer authorized"
					changed = true
					break
				}
			}
		}
	}
	for _, e := range append([]MemoryEntry(nil), d.Entries...) {
		if e.ExpiresAt > 0 && e.ExpiresAt <= now {
			if err := forgetEntry(&d, e.ID); err != nil {
				return d, false, err
			}
			appendMemoryChange(&d, MemoryChange{EntryID: e.ID, Operation: "expire", Timestamp: now, Actor: MemoryActor{Kind: "retention", ID: "retention"}, Reason: "Retention expired", Redacted: true})
			changed = true
		}
	}
	// Source deletion revokes live and history-only copies before any read.
	candidates := append([]MemoryEntry(nil), d.Entries...)
	for _, h := range d.History {
		if h.Before != nil {
			candidates = append(candidates, *h.Before)
		}
		if h.After != nil {
			candidates = append(candidates, *h.After)
		}
	}
	checkedSources := map[string]bool{}
	for _, e := range candidates {
		for _, src := range e.Sources {
			if src.SessionID == "" || checkedSources[e.ID+"\x00"+src.SessionID] {
				continue
			}
			checkedSources[e.ID+"\x00"+src.SessionID] = true
			sess, ok, err := NewSessionStore(s.store).GetSession(src.SessionID)
			if err != nil {
				return d, false, err
			}
			if !ok || sess.AccountScopeID != account {
				if err := forgetEntry(&d, e.ID); err != nil {
					return d, false, err
				}
				appendMemoryChange(&d, MemoryChange{EntryID: e.ID, Operation: "source_deleted", Timestamp: now, Actor: MemoryActor{Kind: "retention", ID: "source-validation"}, Reason: "Source no longer available", Redacted: true})
				changed = true
				break
			}
		}
	}
	// Older versions may expire even after a newer version extended retention.
	historyExpired := false
	for i := range d.History {
		h := &d.History[i]
		if (h.Before != nil && h.Before.ExpiresAt > 0 && h.Before.ExpiresAt <= now) || (h.After != nil && h.After.ExpiresAt > 0 && h.After.ExpiresAt <= now) {
			h.Before = nil
			h.After = nil
			h.Reason = "Historical retention expired"
			h.Actor = MemoryActor{Kind: h.Actor.Kind, ID: "redacted"}
			h.Redacted = true
			changed = true
			historyExpired = true
		}
	}
	if historyExpired {
		appendMemoryChange(&d, MemoryChange{Operation: "expire_history", Timestamp: now, Actor: MemoryActor{Kind: "retention", ID: "retention"}, Reason: "Historical retention expired", Redacted: true})
	}
	return d, changed, validateMemoryDocument(&d)
}
func (s *MemoryStore) persist(d MemoryDocument) error {
	if err := validateMemoryDocument(&d); err != nil {
		return err
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if len(payload) > MemoryMaxDocumentBytes {
		return ErrMemoryBudget
	}
	b := s.store.NewBatch()
	defer b.Close()
	if err = b.Set([]byte(memoryKey(d.AccountScopeID)), payload, nil); err != nil {
		return err
	}
	if err = b.Delete([]byte(KeyWorkspaceMapForAccount(d.AccountScopeID)), nil); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}
func appendMemoryChange(d *MemoryDocument, c MemoryChange) {
	d.Revision++
	c.Revision = d.Revision
	d.History = append(d.History, c)
	if len(d.History) > MemoryMaxHistory {
		d.History = append([]MemoryChange(nil), d.History[len(d.History)-MemoryMaxHistory:]...)
	}
}
func memoryEntryIndex(d MemoryDocument, id string) int {
	for i, e := range d.Entries {
		if e.ID == id {
			return i
		}
	}
	return -1
}
func containsMemory(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func memoryMarker(kind, id string) string { return workspaceMapDigest(kind + "\x00" + id) }
func addMemoryMarker(d *MemoryDocument, kind, id string) error {
	if id == "" {
		return nil
	}
	h := memoryMarker(kind, id)
	if containsMemory(d.Forgotten, h) {
		return nil
	}
	if len(d.Forgotten) >= MemoryMaxTombstones {
		return ErrMemoryBudget
	}
	d.Forgotten = append(d.Forgotten, h)
	return nil
}
func memorySourceDenied(d MemoryDocument, src MemorySource) bool {
	return containsMemory(d.Settings.ExcludedWorkspaces, src.WorkspaceID) || containsMemory(d.Settings.ExcludedSessions, src.SessionID) || containsMemory(d.Forgotten, memoryMarker("workspace", src.WorkspaceID)) || containsMemory(d.Forgotten, memoryMarker("session", src.SessionID))
}
func memoryEntryDenied(d MemoryDocument, e MemoryEntry) bool {
	if containsMemory(d.Forgotten, memoryMarker("entry", e.ID)) || memorySourceDenied(d, MemorySource{WorkspaceID: e.WorkspaceID, SessionID: e.SessionID}) {
		return true
	}
	for _, src := range e.Sources {
		if memorySourceDenied(d, src) {
			return true
		}
	}
	return false
}
func forgetEntry(d *MemoryDocument, id string) error {
	if err := addMemoryMarker(d, "entry", id); err != nil {
		return err
	}
	// Forget live and historical provenance: changing an entry's sources before
	// deletion must not let a new generated ID resurrect its older sources.
	versions := append([]MemoryEntry(nil), d.Entries...)
	for _, h := range d.History {
		if h.EntryID == id {
			if h.Before != nil {
				versions = append(versions, *h.Before)
			}
			if h.After != nil {
				versions = append(versions, *h.After)
			}
		}
	}
	for _, e := range versions {
		if e.ID == id {
			for _, src := range e.Sources {
				if src.SessionID != "" {
					if err := addMemoryMarker(d, "session", src.SessionID); err != nil {
						return err
					}
				} else if err := addMemoryMarker(d, "workspace", src.WorkspaceID); err != nil {
					return err
				}
			}
		}
	}
	kept := d.Entries[:0]
	for _, e := range d.Entries {
		if !memoryEntryDenied(*d, e) {
			kept = append(kept, e)
		}
	}
	d.Entries = kept
	for i := range d.History {
		c := &d.History[i]
		if c.EntryID == id || (c.Before != nil && memoryEntryDenied(*d, *c.Before)) || (c.After != nil && memoryEntryDenied(*d, *c.After)) {
			c.Before = nil
			c.After = nil
			c.Reason = "Content forgotten"
			c.Actor = MemoryActor{Kind: c.Actor.Kind, ID: "redacted"}
			c.Redacted = true
		}
	}
	return nil
}

// MutateForAccount performs policy, CAS, content, history and tombstone updates
// on a private decoded value; one synchronous batch publishes all or nothing.
func (s *MemoryStore) MutateForAccount(account string, m MemoryMutation) (MemoryDocument, error) {
	account, err := memoryAccount(account)
	if err != nil {
		return MemoryDocument{}, err
	}
	if s == nil || s.store == nil {
		return MemoryDocument{}, errors.New("memory store is not configured")
	}
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, _, err := s.load(account)
	if err != nil {
		return MemoryDocument{}, err
	}
	return s.mutateLoaded(d, m)
}

func (s *MemoryStore) mutateLoaded(d MemoryDocument, m MemoryMutation) (MemoryDocument, error) {
	d, err := s.prepareMutation(d, m)
	if err != nil {
		return MemoryDocument{}, err
	}
	if err = s.persist(d); err != nil {
		return MemoryDocument{}, err
	}
	return d, nil
}

// prepareMutation changes only the private document; batch learning publishes once.
func (s *MemoryStore) prepareMutation(d MemoryDocument, m MemoryMutation) (MemoryDocument, error) {
	if d.Revision != m.ExpectedRevision {
		return MemoryDocument{}, ErrMemoryConflict
	}
	if m.Actor.ID == "" || len(m.Actor.ID) > 256 || len(m.Actor.JobID) > 256 || strings.TrimSpace(m.Reason) == "" || len(m.Reason) > 500 {
		return MemoryDocument{}, ErrMemoryPolicy
	}
	if m.Actor.Kind != "user" && m.Actor.Kind != "learned" {
		return MemoryDocument{}, ErrMemoryPolicy
	}
	if m.Actor.Kind == "learned" {
		if !d.Settings.AutomationEnabled || m.Actor.JobID == "" || ValidateAgentModelAssignment(m.Actor.Model) != nil || m.Operation != "put" {
			return MemoryDocument{}, ErrMemoryPolicy
		}
	}
	now := s.now().UnixMilli()
	c := MemoryChange{Operation: m.Operation, Timestamp: now, Actor: m.Actor, Reason: m.Reason}
	switch m.Operation {
	case "settings":
		if m.Settings == nil {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		old := d.Settings
		d.Settings = *m.Settings
		d.Settings.AutomationUserID = m.Actor.ID
		if d.Settings.AutomationEnabled && (!old.AutomationEnabled || old.Mode != d.Settings.Mode || old.IntervalMinutes != d.Settings.IntervalMinutes) {
			d.NextJobAt = now
		}
		if err := validateMemorySettings(d.Settings); err != nil {
			return MemoryDocument{}, err
		}
		c.SettingsBefore = &old
		next := d.Settings
		c.SettingsAfter = &next
		for _, id := range d.Settings.ExcludedWorkspaces {
			if err := addMemoryMarker(&d, "workspace", id); err != nil {
				return MemoryDocument{}, err
			}
		}
		for _, id := range d.Settings.ExcludedSessions {
			if err := addMemoryMarker(&d, "session", id); err != nil {
				return MemoryDocument{}, err
			}
		}
		for _, e := range append([]MemoryEntry(nil), d.Entries...) {
			if memoryEntryDenied(d, e) {
				if err := forgetEntry(&d, e.ID); err != nil {
					return MemoryDocument{}, err
				}
			}
		}
		// Shortening retention applies to existing learned content, never extends it.
		for i := range d.Entries {
			e := &d.Entries[i]
			if e.Kind == "learned" {
				limit := e.CreatedAt + int64(d.Settings.RetentionDays)*86400000
				if e.ExpiresAt == 0 || limit < e.ExpiresAt {
					e.ExpiresAt = limit
				}
			}
		}
	case "forget_source":
		if m.Source.SessionID == "" && m.Source.WorkspaceID == "" {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		if err := addMemoryMarker(&d, "session", m.Source.SessionID); err != nil {
			return MemoryDocument{}, err
		}
		if m.Source.SessionID == "" {
			if err := addMemoryMarker(&d, "workspace", m.Source.WorkspaceID); err != nil {
				return MemoryDocument{}, err
			}
		}
		for _, e := range append([]MemoryEntry(nil), d.Entries...) {
			if memoryEntryDenied(d, e) {
				if err := forgetEntry(&d, e.ID); err != nil {
					return MemoryDocument{}, err
				}
			}
		}
		c.Redacted = true
		c.Reason = "Source forgotten"
	case "delete":
		if memoryEntryIndex(d, m.EntryID) < 0 {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		c.EntryID = m.EntryID
		if err := forgetEntry(&d, m.EntryID); err != nil {
			return MemoryDocument{}, err
		}
		c.Redacted = true
		c.Reason = "Entry forgotten"
	case "put", "restore":
		e := m.Entry
		if m.Operation == "restore" {
			found := false
			for _, h := range d.History {
				if h.Revision == m.RestoreRevision && h.EntryID == m.EntryID && !h.Redacted && h.After != nil {
					e = *h.After
					found = true
					break
				}
			}
			if !found {
				return MemoryDocument{}, ErrMemoryPolicy
			}
		}
		if !d.Settings.RememberEnabled || memoryEntryDenied(d, e) {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		if e.ID == "" || len(e.ID) > 128 || !utf8.ValidString(e.Content) || strings.TrimSpace(e.Content) == "" || strings.ContainsRune(e.Content, 0) || len(e.Content) > WorkspaceMapMaxBytes || len(e.Sources) > 32 {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		if len(e.WorkspaceID) > 256 || len(e.SessionID) > 256 || !utf8.ValidString(e.ID) {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		for _, src := range e.Sources {
			if len(src.WorkspaceID) > 256 || len(src.SessionID) > 256 || src.EventSeq < 0 || (src.SessionID == "" && src.WorkspaceID == "") {
				return MemoryDocument{}, ErrMemoryPolicy
			}
		}
		if e.Kind != "rule" && e.Kind != "orientation" && e.Kind != "learned" {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		if e.Kind != "learned" && e.ExpiresAt != 0 {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		if e.ID == MemoryWorkspaceMapID {
			if e.Kind != "orientation" || e.WorkspaceID != "" || e.SessionID != "" || e.Pinned || len(e.Sources) > 0 || e.ExpiresAt != 0 {
				return MemoryDocument{}, ErrMemoryPolicy
			}
			if _, err := NormalizeWorkspaceMapContent(e.Content); err != nil {
				return MemoryDocument{}, err
			}
		}
		i := memoryEntryIndex(d, e.ID)
		if m.Actor.Kind == "learned" {
			if e.Kind != "learned" || e.Pinned || len(e.Sources) == 0 || (i >= 0 && (d.Entries[i].Kind != "learned" || d.Entries[i].Pinned)) {
				return MemoryDocument{}, ErrMemoryPolicy
			}
			for _, src := range e.Sources {
				if (e.WorkspaceID != "" && e.WorkspaceID != src.WorkspaceID) || (e.SessionID != "" && e.SessionID != src.SessionID) {
					return MemoryDocument{}, ErrMemoryPolicy
				}
				if src.SessionID == "" || src.WorkspaceID == "" || src.EventSeq <= 0 || memorySourceDenied(d, src) || !(containsMemory(d.Settings.IncludedWorkspaces, src.WorkspaceID) || containsMemory(d.Settings.IncludedSessions, src.SessionID)) {
					return MemoryDocument{}, ErrMemoryPolicy
				}
			}
			if MemoryTokenCount(e.Content) > d.Settings.OutputTokens {
				return MemoryDocument{}, ErrMemoryBudget
			}
		}
		e.Revision = 1
		e.CreatedAt = now
		e.UpdatedAt = now
		if i >= 0 {
			old := d.Entries[i]
			c.Before = &old
			e.Revision = old.Revision + 1
			e.CreatedAt = old.CreatedAt
		}
		if e.Kind == "learned" {
			limit := now + int64(d.Settings.RetentionDays)*86400000
			if e.ExpiresAt == 0 || e.ExpiresAt > limit {
				e.ExpiresAt = limit
			}
		}
		if e.ExpiresAt != 0 && e.ExpiresAt <= now {
			return MemoryDocument{}, ErrMemoryPolicy
		}
		if i >= 0 {
			d.Entries[i] = e
		} else {
			d.Entries = append(d.Entries, e)
		}
		c.EntryID = e.ID
		c.After = &e
	default:
		return MemoryDocument{}, ErrMemoryPolicy
	}
	// Job proposals are another copy of source-derived content and must be revoked
	// with settings/content changes, in the same atomic document publication.
	for i := range d.Jobs {
		j := &d.Jobs[i]
		if j.Status == "queued" || j.Status == "running" || j.Status == "review" {
			j.Status = "revoked"
			j.Spend = j.ReservedSpend
			j.Error = "memory changed; create a fresh job"
			j.Proposal = nil
			j.UpdatedAt = now
		}
	}
	// Source revocation also scrubs history-only versions, not just live entries.
	for i := range d.History {
		h := &d.History[i]
		if (h.Before != nil && memoryEntryDenied(d, *h.Before)) || (h.After != nil && memoryEntryDenied(d, *h.After)) {
			h.Before = nil
			h.After = nil
			h.Reason = "Content forgotten"
			h.Actor = MemoryActor{Kind: h.Actor.Kind, ID: "redacted"}
			h.Redacted = true
		}
	}
	for _, e := range append([]MemoryEntry(nil), d.Entries...) {
		if e.ExpiresAt > 0 && e.ExpiresAt <= now {
			if err := forgetEntry(&d, e.ID); err != nil {
				return MemoryDocument{}, err
			}
		}
	}
	appendMemoryChange(&d, c)
	if err := validateMemoryDocument(&d); err != nil {
		return MemoryDocument{}, err
	}
	return d, nil
}
func validateMemorySettings(s MemorySettings) error {
	if (s.Mode != "manual" && s.Mode != "recurring") || s.IntervalMinutes < 15 || s.IntervalMinutes > 43200 || s.LookbackDays < 1 || s.LookbackDays > 365 || s.RetentionDays < 1 || s.RetentionDays > 3650 || s.InputTokens < 1 || s.InputTokens > 1000000 || s.OutputTokens < 1 || s.OutputTokens > 32768 || s.StorageTokens < 1 || s.StorageTokens > 1048576 || s.InjectionTokens < 1 || s.InjectionTokens > s.StorageTokens {
		return ErrMemoryBudget
	}
	for _, xs := range [][]string{s.IncludedWorkspaces, s.IncludedSessions, s.ExcludedWorkspaces, s.ExcludedSessions} {
		if len(xs) > 256 {
			return ErrMemoryBudget
		}
		for _, x := range xs {
			if strings.TrimSpace(x) == "" || len(x) > 256 {
				return ErrMemoryPolicy
			}
		}
	}
	return nil
}
func validateMemoryDocument(d *MemoryDocument) error {
	if err := validateMemorySettings(d.Settings); err != nil {
		return err
	}
	if len(d.Jobs) > MemoryMaxJobs {
		return ErrMemoryBudget
	}
	if len(d.Entries) > MemoryMaxEntries || len(d.History) > MemoryMaxHistory || len(d.Forgotten) > MemoryMaxTombstones {
		return ErrMemoryBudget
	}
	d.StoredTokens = 0
	seen := map[string]bool{}
	for _, e := range d.Entries {
		if seen[e.ID] || e.ID == "" || e.Revision < 1 || memoryEntryDenied(*d, e) {
			return ErrMemoryPolicy
		}
		seen[e.ID] = true
		d.StoredTokens += MemoryTokenCount(e.Content)
	}
	if d.StoredTokens > d.Settings.StorageTokens {
		return ErrMemoryBudget
	}
	return nil
}
