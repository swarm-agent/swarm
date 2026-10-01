package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

// setDesignCatalogAdmission runs under designMu, including the enclosing commit.
// A durable counter orders admissions even when clocks tie or move backwards.
func (s *Store) setDesignCatalogAdmission(p DesignPrincipal, r DesignRequest, b *pebble.Batch) error {
	counter := designKey(p, "session-order", r.ParentSessionID)
	var seq uint64
	if err := s.designGet(counter, &seq); err != nil && !errors.Is(err, ErrDesignNotFound) {
		return err
	}
	if seq == ^uint64(0) {
		return ErrDesignConflict
	}
	seq++
	if err := designSet(b, counter, seq); err != nil {
		return err
	}
	return designSet(b, designKey(p, "session-catalog", fmt.Sprintf("%s/%020d", r.ParentSessionID, seq)), r.ID)
}

// ListSessionDesignRequests returns newest admissions first, including terminal
// requests. The caller authorizes the session and echoes the opaque cursor only.
// At most limit+1 index entries are visited; no historical store scan is used.
func (s *Store) ListSessionDesignRequests(p DesignPrincipal, sessionID, after string, limit int) ([]DesignRequest, string, error) {
	if designOwner(p) != nil || !designID(sessionID) || limit < 1 || limit > MaxDesignHistoryPage {
		return nil, "", ErrDesignInvalid
	}
	prefix := designKey(p, "session-catalog", sessionID+"/")
	seek := prefix + "\xff"
	if after != "" {
		if len(after) > 4096 {
			return nil, "", ErrDesignInvalid
		}
		decoded, err := base64.RawURLEncoding.DecodeString(after)
		if err != nil || !strings.HasPrefix(string(decoded), prefix) || len(decoded) != len(prefix)+20 {
			return nil, "", ErrDesignInvalid
		}
		seek = string(decoded)
		for _, c := range seek[len(prefix):] {
			if c < '0' || c > '9' {
				return nil, "", ErrDesignInvalid
			}
		}
	}
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, "", err
	}
	defer iter.Close()
	rows := make([]DesignRequest, 0, limit)
	last := ""
	ok := iter.SeekLT([]byte(seek))
	for ; ok && len(rows) < limit; ok = iter.Prev() {
		var id string
		if err := json.Unmarshal(iter.Value(), &id); err != nil {
			return nil, "", err
		}
		r, err := s.GetDesignRequest(p, id)
		if err != nil {
			return nil, "", err
		}
		if r.ParentSessionID != sessionID {
			return nil, "", ErrDesignConflict
		}
		// Briefs and context are inputs, not catalog metadata.
		for i := range r.Candidates {
			r.Candidates[i].Spec.Brief = ""
		}
		rows = append(rows, r)
		last = string(iter.Key())
	}
	if err := iter.Error(); err != nil {
		return nil, "", err
	}
	cursor := ""
	if ok {
		cursor = base64.RawURLEncoding.EncodeToString([]byte(last))
	}
	return rows, cursor, nil
}

// RequireDesignArtifactSession binds even historical refs to their originating
// session. Owning the same account is not permission to cross session scopes.
func (s *Store) RequireDesignArtifactSession(p DesignPrincipal, sessionID, artifactID string) (DesignArtifact, error) {
	a, err := s.GetDesignArtifact(p, artifactID)
	if err != nil {
		return a, err
	}
	r, err := s.GetDesignRequest(p, a.RequestGroupID)
	if err != nil {
		return DesignArtifact{}, err
	}
	if r.ParentSessionID != sessionID {
		return DesignArtifact{}, ErrDesignNotFound
	}
	return a, nil
}

// ReadSessionDesignPreview only exposes evidence attached to a published ready
// revision, not a validated-but-unpublished or cancelled response.
func (s *Store) ReadSessionDesignPreview(p DesignPrincipal, sessionID string, ref DesignPreviewRef) ([]byte, error) {
	r, err := s.GetDesignRequest(p, ref.Output.RequestID)
	if err != nil {
		return nil, err
	}
	if r.ParentSessionID != sessionID {
		return nil, ErrDesignNotFound
	}
	if !designOutputValid(ref.Output) || ref.Output.Candidate >= len(r.Candidates) {
		return nil, ErrDesignInvalid
	}
	c := r.Candidates[ref.Output.Candidate]
	if ref.Output.Attempt > len(c.Attempts) {
		return nil, ErrDesignNotFound
	}
	a := c.Attempts[ref.Output.Attempt-1]
	if a.Result == nil || a.Output == nil || *a.Output != ref.Output {
		return nil, ErrDesignConflict
	}
	if _, err := s.ReadDesignRevision(p, *a.Result); err != nil {
		return nil, err
	}
	return s.ReadDesignPreview(p, ref)
}

// Caller holds designMu. Existing low-level records without canonical sessions
// remain readable; runtime-owned records always commit through the V3 boundary.
func (s *Store) commitDesignChange(p DesignPrincipal, r DesignRequest, key string, payload any, b *pebble.Batch) error {
	ss := NewSessionStore(s)
	parent, found, err := ss.GetSession(r.ParentSessionID)
	if err != nil {
		return err
	}
	if !found {
		if r.Canonical {
			return ErrDesignNotFound
		}
		return b.Commit(pebble.Sync)
	}
	if parent.AccountScopeID != p.AccountID || parent.UserID != p.PrincipalID {
		return ErrDesignNotFound
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	id := designDigest([]byte(key))
	_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{
		SessionID: r.ParentSessionID, AccountScopeID: p.AccountID, UserID: p.PrincipalID,
		Kind: "design.updated", EventType: "design.updated", ClientRequestID: id, IdempotencyKey: id,
		PayloadHash: designDigest(data), EventPayload: data, designChange: b,
	})
	return err
}

// Allocation changes a child session and its parent's design metadata together.
// Called only inside ApplyV3SessionMutation with both session locks, designMu,
// and a second reserved endpoint sequence; neither half can commit alone.
func (s *SessionStore) setDesignAllocationInvalidation(b *pebble.Batch, in V3SessionMutationInput, endpoint uint64, now int64) error {
	p := DesignPrincipal{AccountID: in.AccountScopeID, PrincipalID: in.UserID}
	r, err := s.store.GetDesignRequest(p, in.DesignAllocation.RequestID)
	if err != nil {
		return err
	}
	parent, found, err := s.GetSession(r.ParentSessionID)
	if err != nil {
		return err
	}
	if !found || parent.AccountScopeID != p.AccountID || parent.UserID != p.PrincipalID {
		return ErrDesignNotFound
	}
	seq, err := s.readV3SessionSequence(parent.ID)
	if err != nil {
		return err
	}
	if seq == ^uint64(0) {
		return ErrDesignConflict
	}
	seq++
	payload, _ := json.Marshal(map[string]any{"request_id": r.ID, "revision": r.Revision + 1})
	event := V3SessionEvent{ID: "design-allocation-" + in.SessionID, SessionID: parent.ID, Seq: seq, EventType: "design.updated", Payload: payload, TsUnixMs: now}
	if epoch, ok, err := s.GetActiveExecutionEpoch(parent.ID); err != nil {
		return err
	} else if ok {
		event.EpochID = epoch.EpochID
		epoch.LastRootSeq, epoch.UpdatedAt = seq, now
		if err := setExecutionEpochInBatch(b, epoch, true); err != nil {
			return err
		}
	}
	projection := V3SessionProjection{SessionID: parent.ID, LastEventSeq: seq, ProjectionHighWatermarkSeq: seq, UpdatedAt: now, WorkspaceUsage: WorkspaceUsageFromGrants(NormalizeSessionWorkspaceGrants(parent))}
	out := V3RealtimeOutboxRecord{EndpointSeq: endpoint, EndpointCursor: V3RealtimeOutboxCursor(endpoint), SessionID: parent.ID, UserID: p.PrincipalID, AccountScopeID: p.AccountID, Membership: newV3RealtimeOutboxMembershipFromSession(parent, now), Event: event, Projection: projection, CreatedAt: now}
	for key, value := range map[string]any{KeyV3SessionEvent(parent.ID, seq): event, KeyV3SessionProjection(parent.ID): projection, KeyV3RealtimeOutbox(endpoint): out} {
		if err := designSet(b, key, value); err != nil {
			return err
		}
	}
	if err := b.Set([]byte(KeyV3SessionSequence(parent.ID)), uint64ToBytes(seq), nil); err != nil {
		return err
	}
	ref, err := marshalV3RealtimeOutboxReference(out)
	if err != nil {
		return err
	}
	for _, key := range []string{KeyV3RealtimeOutboxBySessionEndpoint(parent.ID, endpoint), KeyV3RealtimeOutboxBySessionSeq(parent.ID, seq), KeyV3RealtimeOutboxByAuthScope(p.AccountID, p.PrincipalID, endpoint)} {
		if err := b.Set([]byte(key), ref, nil); err != nil {
			return err
		}
	}
	return nil
}
