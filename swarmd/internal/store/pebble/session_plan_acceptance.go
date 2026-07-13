package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// V3PlanAcceptanceMutation is the canonical native-V3 plan acceptance payload.
// It is persisted with the session mode transition and ordered realtime events
// in one synced Pebble batch.
type V3PlanAcceptanceMutation struct {
	Plan             SessionPlanSnapshot  `json:"plan"`
	ArchivedRevision *SessionPlanSnapshot `json:"archived_revision,omitempty"`
	Session          SessionSnapshot      `json:"session"`
	PlanEventPayload json.RawMessage      `json:"plan_event_payload"`
	ModeEventPayload json.RawMessage      `json:"mode_event_payload"`
	ModeMessage      *MessageSnapshot     `json:"mode_message,omitempty"`
}

func (s *SessionStore) applyV3PlanAcceptanceMutation(input V3SessionMutationInput) (V3SessionMutationResult, error) {
	acceptance := input.PlanAcceptance
	if acceptance == nil {
		return V3SessionMutationResult{}, errors.New("plan acceptance payload is required")
	}
	if acceptance.Plan.SessionID != input.SessionID || acceptance.Session.ID != input.SessionID {
		return V3SessionMutationResult{}, errors.New("plan acceptance session ids do not match mutation session")
	}
	if strings.TrimSpace(acceptance.Plan.ID) == "" {
		return V3SessionMutationResult{}, errors.New("plan acceptance plan id is required")
	}
	if !json.Valid(acceptance.PlanEventPayload) || !json.Valid(acceptance.ModeEventPayload) {
		return V3SessionMutationResult{}, errors.New("plan acceptance event payloads must be valid json")
	}

	unlockSession := s.store.sessionMutations.lockSessions(input.SessionID)
	defer unlockSession()
	idempotencyKey := KeyV3SessionOperationIdempotency(input.AccountScopeID, input.SessionID, input.Kind, input.ClientRequestID)
	if existing, ok, err := s.getV3SessionIdempotencyRecordByKey(idempotencyKey); err != nil {
		return V3SessionMutationResult{}, err
	} else if ok {
		result, err := s.resultFromV3IdempotencyRecord(existing)
		if existing.PayloadHash != input.PayloadHash {
			if err != nil {
				return V3SessionMutationResult{}, fmt.Errorf("%w: %v", ErrV3IdempotencyConflict, err)
			}
			result.Conflict = &V3SessionMutationConflict{Code: V3SessionMutationStatusConflict, Message: "idempotency key was reused with a different payload hash", ExistingPayloadHash: existing.PayloadHash, IncomingPayloadHash: input.PayloadHash}
			result.ResponseStatus = V3SessionMutationStatusConflict
			return result, ErrV3IdempotencyConflict
		}
		if err != nil {
			return V3SessionMutationResult{}, err
		}
		result.Replayed = true
		result.Plan = &acceptance.Plan
		eventCount := int(existing.Result.LastSeq - existing.Result.FirstSeq + 1)
		result.Events, err = s.ListV3SessionEvents(input.SessionID, existing.Result.FirstSeq-1, eventCount)
		if err != nil {
			return V3SessionMutationResult{}, err
		}
		if len(result.Events) != eventCount {
			return V3SessionMutationResult{}, fmt.Errorf("plan acceptance replay loaded %d events, want %d", len(result.Events), eventCount)
		}
		for _, event := range result.Events {
			rows, listErr := s.ListV3RealtimeOutboxForSessionAfterSeq(input.SessionID, event.Seq-1, 1)
			if listErr != nil {
				return V3SessionMutationResult{}, listErr
			}
			if len(rows) != 1 || rows[0].Event.Seq != event.Seq {
				return V3SessionMutationResult{}, fmt.Errorf("plan acceptance replay is missing realtime outbox for session sequence %d", event.Seq)
			}
			result.RealtimeOutboxes = append(result.RealtimeOutboxes, rows[0])
		}
		if len(result.RealtimeOutboxes) > 0 {
			result.RealtimeOutbox = &result.RealtimeOutboxes[len(result.RealtimeOutboxes)-1]
		}
		return result, nil
	}

	currentSeq, err := s.readV3SessionSequence(input.SessionID)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	eventCount := 2
	if acceptance.ModeMessage != nil {
		eventCount++
	}
	reserved, err := s.store.sessionMutations.reserveOutbox(s.store, eventCount)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	reservationCommitted := false
	defer func() {
		if !reservationCommitted {
			s.store.sessionMutations.abandonOutbox(reserved)
		}
	}()

	now := input.NowUnixMs
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	session := normalizeSessionOwnership(acceptance.Session)
	session.Mode = "auto"
	session.UpdatedAt = now
	plan := acceptance.Plan
	plan.Active = true
	plan.UpdatedAt = now
	membership := newV3RealtimeOutboxMembershipFromSession(session, now)

	type eventSpec struct {
		eventType string
		payload   json.RawMessage
		message   *MessageSnapshot
	}
	specs := []eventSpec{{eventType: "session.plan.saved", payload: acceptance.PlanEventPayload}, {eventType: "session.mode.updated", payload: acceptance.ModeEventPayload}}
	if acceptance.ModeMessage != nil {
		message := sanitizeMessageSnapshot(*acceptance.ModeMessage)
		message.SessionID = input.SessionID
		message.UserID = firstNonEmpty(message.UserID, input.UserID, session.UserID)
		message.AccountScopeID = firstNonEmpty(message.AccountScopeID, input.AccountScopeID, session.AccountScopeID)
		message.CreatedAt = now
		specs = append(specs, eventSpec{eventType: "session.message.appended", message: &message})
	}

	batch := s.store.NewBatch()
	defer batch.Close()
	if err := setPlanAcceptancePlanInBatch(batch, plan, acceptance.ArchivedRevision); err != nil {
		return V3SessionMutationResult{}, err
	}
	active := SessionPlanActive{SessionID: input.SessionID, PlanID: plan.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, UpdatedAt: now}
	activePayload, err := json.Marshal(active)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	if err := batch.Set([]byte(KeySessionPlanActive(input.SessionID)), activePayload, nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	if active.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionPlanActiveByAccount(active.AccountScopeID, input.SessionID)), activePayload, nil); err != nil {
			return V3SessionMutationResult{}, err
		}
	}
	if err := s.setSessionInBatch(batch, session); err != nil {
		return V3SessionMutationResult{}, err
	}

	events := make([]V3SessionEvent, 0, len(specs))
	outboxes := make([]V3RealtimeOutboxRecord, 0, len(specs))
	for i, spec := range specs {
		seq := currentSeq + uint64(i+1)
		payload := spec.payload
		if spec.message != nil {
			spec.message.GlobalSeq = seq
			if strings.TrimSpace(spec.message.ID) == "" {
				spec.message.ID = fmt.Sprintf("v3msg_%s_%020d", input.SessionID, seq)
			}
			replayPayload, marshalErr := json.Marshal(v3SessionEventReplayPayload{SessionID: input.SessionID, Seq: seq, Kind: V3SessionMutationAppendMessage, Message: spec.message})
			if marshalErr != nil {
				return V3SessionMutationResult{}, marshalErr
			}
			payload = replayPayload
			messagePayload, marshalErr := json.Marshal(spec.message)
			if marshalErr != nil {
				return V3SessionMutationResult{}, marshalErr
			}
			if err := batch.Set([]byte(KeyV3SessionMessage(input.SessionID, seq)), messagePayload, nil); err != nil {
				return V3SessionMutationResult{}, err
			}
			if err := s.appendV3SessionSearchMessageInBatch(batch, s.store.db, session, false, *spec.message); err != nil {
				return V3SessionMutationResult{}, err
			}
			session.MessageCount++
			session.LastMessageAt = now
		}
		event := V3SessionEvent{ID: fmt.Sprintf("v3evt_%s_%020d", input.SessionID, seq), SessionID: input.SessionID, Seq: seq, EventType: spec.eventType, Payload: payload, TsUnixMs: now, CausationID: input.CausationID, CorrelationID: input.CorrelationID}
		projection := V3SessionProjection{SessionID: input.SessionID, LastEventSeq: seq, ProjectionHighWatermarkSeq: seq, UpdatedAt: now}
		outbox := V3RealtimeOutboxRecord{EndpointSeq: reserved[i], EndpointCursor: V3RealtimeOutboxCursor(reserved[i]), SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID, Membership: membership, Event: event, Projection: projection, CreatedAt: now}
		if err := setV3PlanAcceptanceEventInBatch(batch, event, outbox); err != nil {
			return V3SessionMutationResult{}, err
		}
		events = append(events, event)
		outboxes = append(outboxes, outbox)
	}
	if err := s.setSessionInBatch(batch, session); err != nil {
		return V3SessionMutationResult{}, err
	}
	lastSeq := events[len(events)-1].Seq
	projection := V3SessionProjection{SessionID: input.SessionID, LastEventSeq: lastSeq, ProjectionHighWatermarkSeq: lastSeq, UpdatedAt: now}
	projectionPayload, _ := json.Marshal(projection)
	if err := batch.Set([]byte(KeyV3SessionSequence(input.SessionID)), uint64ToBytes(lastSeq), nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	if err := batch.Set([]byte(KeyV3SessionProjection(input.SessionID)), projectionPayload, nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	stored := V3SessionMutationStoredResult{SessionID: input.SessionID, FirstSeq: events[0].Seq, LastSeq: lastSeq, PayloadHash: input.PayloadHash, ResponseVersion: V3SessionMutationResponseVersion, ResponseStatus: V3SessionMutationStatusCompleted, EventType: events[len(events)-1].EventType, LastEventSeq: lastSeq, ProjectionHighWatermarkSeq: lastSeq, AppliedAt: now}
	for _, event := range events {
		stored.EventIDs = append(stored.EventIDs, event.ID)
	}
	stored.ResponseBody, err = buildV3MutationResponseBody(stored)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	idempotency := V3SessionIdempotencyRecord{SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID, Operation: input.Kind, ClientRequestID: input.ClientRequestID, Key: input.IdempotencyKey, PayloadHash: input.PayloadHash, RequestHash: input.RequestHash, Kind: input.Kind, Status: V3SessionMutationStatusCompleted, Result: stored, CreatedAt: now, CompletedAt: now}
	idempotencyPayload, _ := json.Marshal(idempotency)
	if err := batch.Set([]byte(idempotencyKey), idempotencyPayload, nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	if hook := s.store.sessionMutations.beforeDurableCommit; hook != nil {
		hook(input.SessionID)
	}
	if err := s.store.commitV3PlanAcceptanceObserved(batch, pebble.Sync); err != nil {
		return V3SessionMutationResult{}, err
	}
	reservationCommitted = true
	if err := s.store.sessionMutations.commitOutboxObserved(s.store, reserved, observeV3PlanAcceptanceCommit); err != nil {
		return V3SessionMutationResult{}, err
	}
	return V3SessionMutationResult{SessionID: input.SessionID, PrimarySeq: lastSeq, FirstSeq: events[0].Seq, LastSeq: lastSeq, EventIDs: stored.EventIDs, PayloadHash: input.PayloadHash, ResponseVersion: stored.ResponseVersion, ResponseStatus: stored.ResponseStatus, ResponseBody: stored.ResponseBody, Event: events[len(events)-1], Session: &session, Projection: projection, Idempotency: idempotency, RealtimeOutbox: &outboxes[len(outboxes)-1], Events: events, RealtimeOutboxes: outboxes, Plan: &plan}, nil
}

func setPlanAcceptancePlanInBatch(batch *pebble.Batch, plan SessionPlanSnapshot, archived *SessionPlanSnapshot) error {
	if archived != nil {
		payload, err := json.Marshal(archived)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(KeySessionPlanRevision(archived.SessionID, archived.ID, archived.Version)), payload, nil); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	for _, key := range []string{KeySessionPlan(plan.SessionID, plan.ID), KeySessionPlanRevision(plan.SessionID, plan.ID, plan.Version)} {
		if err := batch.Set([]byte(key), payload, nil); err != nil {
			return err
		}
	}
	if plan.AccountScopeID != "" {
		return batch.Set([]byte(KeySessionPlanByAccount(plan.AccountScopeID, plan.SessionID, plan.ID)), []byte(plan.ID), nil)
	}
	return nil
}

func setV3PlanAcceptanceEventInBatch(batch *pebble.Batch, event V3SessionEvent, outbox V3RealtimeOutboxRecord) error {
	eventPayload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	outboxPayload, err := json.Marshal(outbox)
	if err != nil {
		return err
	}
	refPayload, err := marshalV3RealtimeOutboxReference(outbox)
	if err != nil {
		return err
	}
	sets := []struct {
		key   string
		value []byte
	}{
		{KeyV3SessionEvent(event.SessionID, event.Seq), eventPayload},
		{KeyV3RealtimeOutbox(outbox.EndpointSeq), outboxPayload},
		{KeyV3RealtimeOutboxBySessionEndpoint(event.SessionID, outbox.EndpointSeq), refPayload},
		{KeyV3RealtimeOutboxBySessionSeq(event.SessionID, event.Seq), refPayload},
		{KeyV3RealtimeOutboxByAuthScope(outbox.AccountScopeID, outbox.UserID, outbox.EndpointSeq), refPayload},
	}
	for _, set := range sets {
		if err := batch.Set([]byte(set.key), set.value, nil); err != nil {
			return err
		}
	}
	return nil
}
