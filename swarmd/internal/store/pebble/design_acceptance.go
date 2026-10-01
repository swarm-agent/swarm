package pebblestore

import (
	"encoding/json"
	"strings"

	"github.com/cockroachdb/pebble"
)

const V3SessionMutationAcceptDesign = "design.accept"

// DesignAcceptance is an independent artifact participant in the canonical
// session transaction. It creates no alternative session or run authority.
type DesignAcceptance struct {
	Submit DesignSubmit `json:"submit"`
}

func validateDesignAcceptance(input V3SessionMutationInput) error {
	if input.DesignAcceptance == nil {
		if input.Kind == V3SessionMutationAcceptDesign {
			return ErrDesignInvalid
		}
		return nil
	}
	in := input.DesignAcceptance.Submit
	if input.Kind != V3SessionMutationAcceptDesign || input.Session != nil || input.RunIntent != nil || input.Message != nil || input.PlanSave != nil || input.Artifact != nil || input.ArtifactV2 != nil || input.ArtifactV3 != nil || designOwner(DesignPrincipal{AccountID: input.AccountScopeID, PrincipalID: input.UserID}) != nil {
		return ErrDesignInvalid
	}
	if in.ParentSessionID != input.SessionID || in.IdempotencyKey != input.ClientRequestID {
		return ErrDesignInvalid
	}
	return nil
}

// ListPendingDesignRequests reads a bounded account/principal index, not all
// histories. The index and request are committed together. The cursor is a
// request ID, and requests remain discoverable until a terminal observation.
// Claiming execution uses RecordDesignAttempt's request revision CAS; an
// interrupted attempt must never be silently resubmitted to a provider.
func (s *Store) ListPendingDesignRequests(p DesignPrincipal, after string, limit int) ([]DesignRequest, error) {
	if err := designOwner(p); err != nil {
		return nil, err
	}
	if (after != "" && !designID(after)) || limit < 1 || limit > MaxDesignHistoryPage {
		return nil, ErrDesignInvalid
	}
	s.designMu.Lock()
	defer s.designMu.Unlock()
	prefix := designKey(p, "pending", "")
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	rows := make([]DesignRequest, 0, limit)
	for ok := iter.SeekGE([]byte(prefix + after)); ok && len(rows) < limit; ok = iter.Next() {
		id := strings.TrimPrefix(string(iter.Key()), prefix)
		if id == after {
			continue
		}
		r, err := s.GetDesignRequest(p, id)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, iter.Error()
}

func (s *SessionStore) setDesignAcceptanceInBatch(batch *pebble.Batch, input V3SessionMutationInput) error {
	if input.DesignAcceptance == nil {
		return nil
	}
	p := DesignPrincipal{AccountID: input.AccountScopeID, PrincipalID: input.UserID}
	parent, ok, err := s.GetSession(input.SessionID)
	if err != nil {
		return err
	}
	if !ok || parent.AccountScopeID != p.AccountID || parent.UserID != p.PrincipalID {
		return ErrDesignNotFound
	}
	run, ok, err := s.GetV3SessionRunIntent(input.SessionID, input.DesignAcceptance.Submit.ParentRunID)
	if err != nil {
		return err
	}
	if !ok || run.AccountScopeID != p.AccountID || run.UserID != p.PrincipalID {
		return ErrDesignNotFound
	}
	_, err = s.store.submitDesignRequestInBatch(p, input.DesignAcceptance.Submit, batch)
	return err
}

// DesignAcceptanceHash is derived by trusted runtime from the entire immutable
// acceptance request, never from a model-supplied digest.
func DesignAcceptanceHash(in DesignSubmit) string {
	b, _ := json.Marshal(in)
	return designDigest(b)
}
