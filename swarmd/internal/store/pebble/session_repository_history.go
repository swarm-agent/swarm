package pebblestore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

const repositoryHistoryMetaKey = "v3/repository_history/meta"
const repositoryHistoryRevisionKey = "v3/repository_history/revision"

var ErrRepositoryHistoryNotReady = errors.New("repository history requires explicit backfill")
var ErrRepositoryHistoryCursor = errors.New("invalid or stale repository history cursor")

// RepositoryHistoryQuery is principal-scoped. Limit must be 1..100 and must
// remain unchanged while following an opaque cursor.
type RepositoryHistoryQuery struct {
	AccountScopeID string
	UserID string
	ParentSessionID string
	Limit int
	Cursor string
}

// SessionRepositoryHistory retains each observed workspace/worktree context.
// Snapshot metadata is evidence, never authorization to access its paths.
// Historical rows must be independently authorized against current grants.
type SessionRepositoryHistory struct {
	Session SessionSnapshot `json:"session"`
	Archived bool `json:"archived"`
	Deleted bool `json:"deleted"`
	ContextID string `json:"context_id"`
}

type RepositoryHistoryPage struct {
	Sessions []SessionRepositoryHistory `json:"sessions"`
	Programs []TaskProgramRecord `json:"programs"`
	NextCursor string `json:"next_cursor,omitempty"`
	// Older overwritten contexts are not reconstructible from snapshots.
	HistoryCoverage string `json:"history_coverage"`
}

type repositoryHistoryMeta struct {
	Secret []byte `json:"secret"`
	Phase int `json:"phase"`
	After string `json:"after"`
	Ready bool `json:"ready"`
}

type repositoryHistoryCursor struct {
	Scope string
	Revision string
	After string
	Limit int
}

func repositoryHistoryPrefix(account, user, parent string) string {
	return "v3/repository_history/rows/" + keyPart(account) + "/" + keyPart(user) + "/" + keyPart(parent) + "/"
}

func repositoryHistoryRevision(batch *pebble.Batch) error {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil { return err }
	return batch.Set([]byte(repositoryHistoryRevisionKey), token, nil)
}

// Called in the same batch as canonical snapshot/library updates. Context keys
// are stable across lifecycle changes but differ across default/grant changes.
func (s *SessionStore) retainRepositoryHistoryInBatch(batch *pebble.Batch, session SessionSnapshot, archived, deleted bool) error {
	session = normalizeSessionOwnership(session)
	identity := struct {
		Workspace string
		Root string
		Branch string
		Base string
		BaseCommit string
		Source string
		Grants []WorkspaceGrant
	}{session.WorkspacePath, session.WorktreeRootPath, session.WorktreeBranch, session.WorktreeBaseBranch, v3LibraryMetadataString(session.Metadata, "base_commit"), v3LibraryMetadataString(session.Metadata, "swarm_v3_source_workspace_path"), session.WorkspaceGrants}
	payload, err := json.Marshal(identity)
	if err != nil { return err }
	digest := sha256.Sum256(payload)
	row := SessionRepositoryHistory{Session: session, Archived: archived, Deleted: deleted, ContextID: hex.EncodeToString(digest[:])}
	payload, err = json.Marshal(row)
	if err != nil { return err }
	parents := []string{session.ID}
	if parent := v3LibraryMetadataString(session.Metadata, "parent_session_id"); parent != "" && parent != session.ID { parents = append(parents, parent) }
	for _, parent := range parents {
		key := repositoryHistoryPrefix(session.AccountScopeID, session.UserID, parent) + keyPart(session.ID) + "/" + row.ContextID
		if err := batch.Set([]byte(key), payload, nil); err != nil { return err }
	}
	return repositoryHistoryRevision(batch)
}

// BackfillRepositoryHistory explicitly advances at most limit durable rows.
// It shares the canonical library repair exclusion lock; progress and rows
// commit atomically. Reads never trigger migration. Existing overwritten
// contexts cannot be reconstructed; retained snapshots and tombstones are the
// migration authority, and new context changes are retained going forward.
func (s *SessionStore) BackfillRepositoryHistory(limit int) (bool, error) {
	if limit < 1 || limit > 100 { return false, errors.New("backfill limit must be 1..100") }
	s.store.sessionMutations.libraryRepairMu.Lock()
	defer s.store.sessionMutations.libraryRepairMu.Unlock()
	var meta repositoryHistoryMeta
	_, err := s.store.GetJSON(repositoryHistoryMetaKey, &meta)
	if err != nil { return false, err }
	if meta.Ready { return true, nil }
	if len(meta.Secret) == 0 {
		meta.Secret = make([]byte, 32)
		if _, err := rand.Read(meta.Secret); err != nil { return false, err }
	}
	prefix := SessionPrefix()
	if meta.Phase == 1 { prefix = V3SessionTombstonePrefix() }
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil { return false, err }
	defer iter.Close()
	batch := s.store.NewBatch()
	defer batch.Close()
	valid := iter.First()
	if meta.After != "" { valid = iter.SeekGE([]byte(meta.After)); if valid && string(iter.Key()) == meta.After { valid = iter.Next() } }
	for n := 0; valid && n < limit; n++ {
		var session SessionSnapshot
		archived, deleted := false, false
		if meta.Phase == 0 { err = json.Unmarshal(iter.Value(), &session) } else {
			var tombstone V3SessionTombstone
			err = json.Unmarshal(iter.Value(), &tombstone)
			session, archived, deleted = tombstone.Session, tombstone.Archived, tombstone.Deleted
		}
		if err != nil { return false, err }
		if session.ID != "" {
			if err := s.retainRepositoryHistoryInBatch(batch, session, archived, deleted); err != nil { return false, err }
		}
		meta.After = string(iter.Key())
		valid = iter.Next()
	}
	if err := iter.Error(); err != nil { return false, err }
	if !valid { meta.Phase++; meta.After = ""; meta.Ready = meta.Phase == 2 }
	payload, err := json.Marshal(meta)
	if err != nil { return false, err }
	if err := batch.Set([]byte(repositoryHistoryMetaKey), payload, nil); err != nil { return false, err }
	if err := repositoryHistoryRevision(batch); err != nil { return false, err }
	if err := batch.Commit(pebble.Sync); err != nil { return false, err }
	return meta.Ready, nil
}

func repositoryHistoryOwner(reader pebble.Reader, q RepositoryHistoryQuery) error {
	if strings.TrimSpace(q.AccountScopeID) == "" || strings.TrimSpace(q.UserID) == "" || strings.TrimSpace(q.ParentSessionID) == "" || q.Limit < 1 || q.Limit > 100 { return errors.New("repository history requires principal, parent and limit 1..100") }
	var parent SessionSnapshot
	ok, err := getJSONFromReader(reader, KeySession(q.ParentSessionID), &parent)
	if err != nil { return err }
	if !ok {
		var tombstone V3SessionTombstone
		ok, err = getJSONFromReader(reader, KeyV3SessionTombstone(q.ParentSessionID), &tombstone)
		if err != nil { return err }
		if !ok || tombstone.Deleted { return errors.New("repository history parent unavailable") }
		parent = tombstone.Session
	}
	parent = normalizeSessionOwnership(parent)
	if parent.AccountScopeID != q.AccountScopeID || parent.UserID != q.UserID { return errors.New("repository history principal mismatch") }
	return nil
}

// ListSessionRepositoryHistory reads at most Limit+1 parent-indexed rows from
// one snapshot. It includes the parent's retained contexts and direct children,
// regardless of archive, rotation or worker outcome; it never touches Git.
func (s *SessionStore) ListSessionRepositoryHistory(q RepositoryHistoryQuery) (RepositoryHistoryPage, error) {
	return s.repositoryHistoryPage(q, false)
}

// ListTaskProgramRepositoryHistory returns bounded full durable program records
// (including exact lane, job and generation history), without reconciliation.
func (s *SessionStore) ListTaskProgramRepositoryHistory(q RepositoryHistoryQuery) (RepositoryHistoryPage, error) {
	return s.repositoryHistoryPage(q, true)
}

func (s *SessionStore) repositoryHistoryPage(q RepositoryHistoryQuery, programs bool) (RepositoryHistoryPage, error) {
	out := RepositoryHistoryPage{Sessions: []SessionRepositoryHistory{}, Programs: []TaskProgramRecord{}, HistoryCoverage: "retained_snapshots_and_contexts_since_index_install"}
	reader := s.store.db.NewSnapshot()
	defer reader.Close()
	if err := repositoryHistoryOwner(reader, q); err != nil { return out, err }
	var meta repositoryHistoryMeta
	ok, err := getJSONFromReader(reader, repositoryHistoryMetaKey, &meta)
	if err != nil { return out, err }
	if !ok || !meta.Ready { return out, ErrRepositoryHistoryNotReady }
	block, err := aes.NewCipher(meta.Secret)
	if err != nil { return out, err }
	aead, err := cipher.NewGCM(block)
	if err != nil { return out, err }
	revision, closer, err := reader.Get([]byte(repositoryHistoryRevisionKey))
	if err != nil { return out, err }
	rev := hex.EncodeToString(revision)
	closer.Close()
	prefix := repositoryHistoryPrefix(q.AccountScopeID, q.UserID, q.ParentSessionID)
	scope := prefix
	if programs { prefix = TaskProgramSessionPrefix(q.ParentSessionID); scope += "programs" }
	cursor := repositoryHistoryCursor{Scope: scope, Revision: rev, Limit: q.Limit}
	if q.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || len(data) < aead.NonceSize() || len(data) > 8192 { return out, ErrRepositoryHistoryCursor }
		plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
		if err != nil || json.Unmarshal(plain, &cursor) != nil || cursor.Scope != scope || cursor.Revision != rev || cursor.Limit != q.Limit || !strings.HasPrefix(cursor.After, prefix) { return out, ErrRepositoryHistoryCursor }
	}
	iter, err := reader.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil { return out, err }
	defer iter.Close()
	valid := iter.First()
	if cursor.After != "" { valid = iter.SeekGE([]byte(cursor.After)); if valid && string(iter.Key()) == cursor.After { valid = iter.Next() } }
	for n := 0; valid && n < q.Limit; n++ {
		if programs {
			var row TaskProgramRecord
			if err := json.Unmarshal(iter.Value(), &row); err != nil { return RepositoryHistoryPage{}, err }
			if row.ParentSessionID != q.ParentSessionID { return RepositoryHistoryPage{}, fmt.Errorf("repository history program ownership mismatch") }
			out.Programs = append(out.Programs, row)
		} else {
			var row SessionRepositoryHistory
			if err := json.Unmarshal(iter.Value(), &row); err != nil { return RepositoryHistoryPage{}, err }
			if row.Session.AccountScopeID != q.AccountScopeID || row.Session.UserID != q.UserID { return RepositoryHistoryPage{}, errors.New("repository history row ownership mismatch") }
			out.Sessions = append(out.Sessions, row)
		}
		cursor.After = string(iter.Key())
		valid = iter.Next()
	}
	if err := iter.Error(); err != nil { return RepositoryHistoryPage{}, err }
	if valid {
		plain, err := json.Marshal(cursor)
		if err != nil { return RepositoryHistoryPage{}, err }
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil { return RepositoryHistoryPage{}, err }
		out.NextCursor = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil))
	}
	return out, nil
}

func (s *SessionStore) putTaskProgramHistory(record TaskProgramRecord) error {
	batch := s.store.NewBatch()
	defer batch.Close()
	payload, err := json.Marshal(record)
	if err != nil { return err }
	if err := batch.Set([]byte(KeyTaskProgram(record.ParentSessionID, record.ProgramID)), payload, nil); err != nil { return err }
	if err := repositoryHistoryRevision(batch); err != nil { return err }
	return batch.Commit(pebble.Sync)
}
