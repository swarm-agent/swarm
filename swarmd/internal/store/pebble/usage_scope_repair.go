package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/google/uuid"
)

type UsageScopeRepairResult struct {
	Scanned         int    `json:"scanned"`
	Repaired        int    `json:"repaired"`
	Unresolved      int    `json:"unresolved"`
	NextCursor      string `json:"next_cursor,omitempty"`
	HistoryComplete bool   `json:"history_complete"`
}

// RepairUsageScopes walks only canonical account receipt indexes, with bounded
// work and resumable account-bound cursors. Each receipt/projection/day/binding
// is one synced transaction under the billing lock. Billing and context records
// are intentionally untouched. Completion proves only indexed receipt coverage,
// never completeness of unavailable historic lineage or missing indexes.
func (s *SessionStore) RepairUsageScopes(account, cursor string, limit int) (UsageScopeRepairResult, error) {
	result := UsageScopeRepairResult{}
	if strings.TrimSpace(account) == "" || len(cursor) > 3072 || limit < 1 || limit > 100 {
		return result, errors.New("invalid usage repair bounds")
	}
	prefixes := []string{SessionTurnUsageByAccountPrefix(account, ""), SessionMediaUsagePrefix(account)}
	start := ""
	phase := 0
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return result, errors.New("invalid usage repair cursor")
		}
		start = string(raw)
		if strings.HasPrefix(start, prefixes[1]) {
			phase = 1
		} else if !strings.HasPrefix(start, prefixes[0]) {
			return result, errors.New("usage repair cursor account mismatch")
		}
	}
	for ; phase < len(prefixes); phase++ {
		prefix := prefixes[phase]
		it, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
		if err != nil {
			return result, err
		}
		ok := it.First()
		if start != "" {
			ok = it.SeekGE([]byte(start))
			if ok && string(it.Key()) == start {
				ok = it.Next()
			}
		}
		for ; ok; ok = it.Next() {
			if result.Scanned == limit {
				it.Close()
				return result, nil
			}
			key := string(it.Key())
			value := append([]byte(nil), it.Value()...)
			repaired, unresolved, err := s.repairUsageScopeReceipt(account, key, value, phase == 1)
			if err != nil {
				it.Close()
				return result, err
			}
			result.Scanned++
			if repaired {
				result.Repaired++
			}
			if unresolved {
				result.Unresolved++
			}
			result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(key))
		}
		err = it.Error()
		it.Close()
		if err != nil {
			return result, err
		}
		start = ""
	}
	result.NextCursor = ""
	return result, nil
}

func (s *SessionStore) repairUsageScopeReceipt(account, key string, value []byte, media bool) (bool, bool, error) {
	unlock := s.store.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	s.store.sessionMutations.libraryRepairMu.RLock()
	defer s.store.sessionMutations.libraryRepairMu.RUnlock()
	batch := s.store.NewBatch()
	defer batch.Close()
	var scopes []UsageScopeTotal
	var session string
	if media {
		var receipt SessionMediaUsageRecord
		found, err := s.store.GetJSON(key, &receipt)
		if err != nil || !found {
			return false, !found, err
		}
		if receipt.AccountScopeID != account {
			return false, false, errors.New("repair receipt account mismatch")
		}
		if len(receipt.ScopeTotals) > 0 {
			return false, false, nil
		}
		session = receipt.SessionID
		scopes, err = s.prepareMediaScopeTotals(receipt)
		if err != nil {
			return false, false, err
		}
		if len(scopes) == 0 {
			return false, true, nil
		}
		for i := range scopes {
			scopes[i].Coverage = "repaired_receipts_incomplete"
		}
		receipt.ScopeTotals = scopes
		if err := s.setMediaUsageDays(batch, receipt); err != nil {
			return false, false, err
		}
		payload, err := json.Marshal(receipt)
		if err != nil {
			return false, false, err
		}
		if err := batch.Set([]byte(key), payload, nil); err != nil {
			return false, false, err
		}
		if err := batch.Set([]byte(KeySessionMediaUsageBySession(session, receipt.ID)), payload, nil); err != nil {
			return false, false, err
		}
	} else {
		// Index values retain run IDs; the session key segment is canonical keyPart.
		parts := strings.Split(strings.TrimPrefix(key, SessionTurnUsageByAccountPrefix(account, "")), "/")
		if len(parts) != 2 {
			return false, false, errors.New("invalid usage receipt index")
		}
		var receipt SessionTurnUsageSnapshot
		found, err := s.store.GetJSON("session_turn_usage/"+parts[0]+"/"+parts[1], &receipt)
		if err != nil || !found {
			return false, !found, err
		}
		if receipt.AccountScopeID != account || KeySessionTurnUsageByAccount(account, receipt.SessionID, receipt.RunID) != key || receipt.RunID != string(value) {
			return false, false, errors.New("repair receipt index mismatch")
		}
		if len(receipt.ScopeTotals) > 0 && receipt.ScopeProjectionVersion >= 3 {
			return false, false, nil
		}
		previous := receipt
		if strings.EqualFold(receipt.Provider, "codex") && receipt.CostProvenance == "provider" {
			if _, reported := receipt.APIUsageRaw["estimated_cost_usd"]; reported {
				receipt.CostProvenance = "provider_estimate"
			}
		}
		session = receipt.SessionID
		scopes, err = s.prepareUsageScopeTotals(receipt, previous)
		if err != nil {
			return false, false, err
		}
		if len(scopes) == 0 {
			return false, true, nil
		}
		for i := range scopes {
			scopes[i].Coverage = "repaired_receipts_incomplete"
		}
		receipt.ScopeTotals, receipt.ScopeProjectionVersion = scopes, 3
		if err := s.setUsageDays(batch, receipt, previous); err != nil {
			return false, false, err
		}
		payload, err := json.Marshal(receipt)
		if err != nil {
			return false, false, err
		}
		if err := batch.Set([]byte(KeySessionTurnUsage(session, receipt.RunID)), payload, nil); err != nil {
			return false, false, err
		}
	}
	for i := range scopes {
		scopes[i].Coverage = "repaired_receipts_incomplete"
		scopes[i].HistoryComplete = false
	}
	if err := setUsageScopeTotals(batch, account, scopes); err != nil {
		return false, false, err
	}
	if err := setUsageBinding(batch, account, session, scopes); err != nil {
		return false, false, err
	}
	snapshot, found, err := s.GetSession(session)
	if err != nil || !found {
		return false, !found, err
	}
	payload, err := json.Marshal(map[string]any{"scope_totals": scopes})
	if err != nil {
		return false, false, err
	}
	request := uuid.NewString()
	input := normalizeV3SessionMutationInput(V3SessionMutationInput{SessionID: session, AccountScopeID: account, UserID: snapshot.UserID, Kind: "usage.scope.updated", EventType: "usage.scope.updated", ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, EventPayload: payload, usageScopeRepair: batch})
	if err := validateV3SessionMutationInput(input); err != nil {
		return false, false, err
	}
	_, err = s.applyFreshV3SessionMutation(input, KeyV3SessionOperationIdempotency(account, session, input.Kind, input.ClientRequestID))
	return err == nil, false, err
}
