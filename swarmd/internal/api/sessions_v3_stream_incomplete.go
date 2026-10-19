package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

const (
	sessionV3StreamIncompleteReplayed        = "replayed"
	sessionV3StreamIncompleteOutputDurable   = "not_replayed_output_durable"
	sessionV3StreamIncompleteCappedWorker    = "not_replayed_capped_worker"
	sessionV3StreamIncompleteRecoveryEventTy = "session.run.recovery.stream_incomplete"
)

// replayableAfterIncompleteStream reports whether the step's attempt left
// nothing durable that a replay of the same request would duplicate: no
// assistant text on the step's stream and no provider tool-construction
// record. Reasoning alone is allowed; the replay's snapshots replace it.
func (s *sessionV3ProviderStreamState) replayableAfterIncompleteStream() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progressErr == nil && s.tracker != nil && s.tracker.Offset == 0 && s.providerToolEventIndex == 0
}

// replayIncompleteProviderStream gives a step whose provider stream closed
// without its terminal signal (provideriface.ErrStreamIncomplete) one replay of
// the exact same request. The failed attempt ran no tools (tools run only from
// a successful step's FunctionCalls) and its response is discarded. The replay
// is refused when the attempt already left durable output, or when the worker
// is budget-capped (its reservation for the failed attempt has no receipt to
// settle it). Every decision is a durable session.run.recovery.stream_incomplete
// event written before any replay; if it cannot be written, nothing is replayed.
func (e *sessionV3Executor) replayIncompleteProviderStream(ctx context.Context, job sessionV3ExecutorJob, step int, stream *sessionV3ProviderStreamState, failed provideriface.Response, failedErr error, attempt func() (provideriface.Response, error)) (provideriface.Response, error) {
	if !errors.Is(failedErr, provideriface.ErrStreamIncomplete) || ctx.Err() != nil {
		return failed, failedErr
	}
	decision := sessionV3StreamIncompleteReplayed
	if !stream.replayableAfterIncompleteStream() {
		decision = sessionV3StreamIncompleteOutputDurable
	} else if err := e.server.sessions.Store().CheckWorkerUnmeteredOperation(job.Principal.AccountScopeID, job.SessionID); err != nil {
		decision = sessionV3StreamIncompleteCappedWorker
	}
	if err := e.recordStreamIncompleteEvent(job, step, decision); err != nil {
		return failed, fmt.Errorf("%w (replay decision not recorded: %v)", failedErr, err)
	}
	if decision != sessionV3StreamIncompleteReplayed {
		return failed, failedErr
	}
	response, err := attempt()
	if errors.Is(err, provideriface.ErrStreamIncomplete) {
		return response, fmt.Errorf("%w after 2 attempts", err)
	}
	return response, err
}

func (e *sessionV3Executor) recordStreamIncompleteEvent(job sessionV3ExecutorJob, step int, decision string) error {
	now := time.Now().UnixMilli()
	raw, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		EpochID   string `json:"epoch_id,omitempty"`
		Step      int    `json:"step"`
		State     string `json:"recovery_state"`
		Decision  string `json:"decision"`
		Recorded  int64  `json:"recorded_at"`
	}{strings.TrimSpace(job.SessionID), strings.TrimSpace(job.RunID), strings.TrimSpace(job.EpochID), step, "stream_incomplete", decision, now})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	// Steps restart at 1 when a run continues in a new epoch after compaction,
	// so the epoch keeps a later record from colliding with an earlier one.
	key := sessionV3ExecutorEpochScopedID(sessionV3ExecutorClientRequestID(fmt.Sprintf("%s.step.%d", sessionV3StreamIncompleteRecoveryEventTy, step), job.RunID), job.EpochID)
	_, err = e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: job.SessionID, UserID: job.Principal.UserID, AccountScopeID: job.Principal.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: hash, RequestHash: hash,
		Kind: sessionruntime.SessionMutationRecordDiagnostic, EventType: sessionV3StreamIncompleteRecoveryEventTy, EventPayload: raw, NowUnixMs: now,
	})
	return err
}
