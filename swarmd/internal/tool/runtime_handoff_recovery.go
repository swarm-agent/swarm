package tool

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A recovery is separate evidence for one resumed run, never a rewrite of the
// original launch. All consumers still pass ResolveCommittedSource's Git,
// lifecycle, generation, catalog and destination checks.
type recoveredCoderHandoff struct {
	Source         CommittedSourceRequest `json:"source"`
	RunID          string                 `json:"run_id"`
	OriginalDigest string                 `json:"original_digest"`
}

// This in-memory value cannot be supplied by decoded session metadata. It is
// attached only after both current V3 run state and its owned intent verify.
type verifiedHandoffContinuation struct{ pebblestore.V3SessionRunState }

func handoffDigest(v any) string {
	b, _ := json.Marshal(v)
	// Normalize structs and the maps returned by durable metadata decoding to
	// the same JSON key order, so replay does not look like a different receipt.
	var normalized any
	if json.Unmarshal(b, &normalized) == nil {
		b, _ = json.Marshal(normalized)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func handoffRecoveryKey(source CommittedSourceRequest, runID string) string {
	return handoffDigest([]string{source.TaskCallID, source.ChildSessionID, runID})
}

func metadataWithRecoveredHandoff(metadata map[string]any, receipt recoveredCoderHandoff) (map[string]any, error) {
	b, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	var copy map[string]any
	if err = json.Unmarshal(b, &copy); err != nil {
		return nil, err
	}
	if copy == nil {
		copy = map[string]any{}
	}
	entries, _ := copy["recovered_coder_handoffs"].(map[string]any)
	if entries == nil {
		entries = map[string]any{}
	}
	key := handoffRecoveryKey(receipt.Source, receipt.RunID)
	if old, exists := entries[key]; exists && handoffDigest(old) != handoffDigest(receipt) {
		return nil, errors.New("recovered handoff already binds a different commit or launch")
	}
	entries[key] = receipt
	copy["recovered_coder_handoffs"] = entries
	return copy, nil
}

func (r *Runtime) recoveredHandoffRow(parent, child pebblestore.SessionSnapshot, req CommittedSourceRequest, original map[string]any) (map[string]any, error) {
	authority, ok := r.sessions.(interface {
		GetLifecycle(string) (pebblestore.SessionLifecycleSnapshot, bool, error)
		GetSessionRunState(string) (pebblestore.V3SessionRunState, bool, error)
		GetV3SessionRunIntent(string, string) (pebblestore.V3SessionRunIntent, bool, error)
	})
	if !ok {
		return original, nil
	}
	current, found, err := authority.GetSessionRunState(child.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return original, nil
	}
	entries, _ := parent.Metadata["recovered_coder_handoffs"].(map[string]any)
	raw, exists := entries[handoffRecoveryKey(req, current.RunID)]
	if !exists {
		return original, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var recovered recoveredCoderHandoff
	if err = json.Unmarshal(b, &recovered); err != nil {
		return nil, err
	}
	if recovered.Source != req || recovered.RunID == "" || recovered.RunID != current.RunID || recovered.OriginalDigest != handoffDigest(original) {
		return nil, errors.New("recovered handoff does not match the exact source, current run or original launch")
	}
	switch asString(original["phase"]) {
	case "failed", "cancelled", "stopped", "blocked":
	default:
		return nil, errors.New("handoff recovery requires an unsuccessful original launch")
	}
	if prior := asString(original["child_run_id"]); prior == "" || prior == recovered.RunID {
		return nil, errors.New("handoff recovery requires a distinct recorded continuation run")
	}
	if current.SessionID != child.ID || current.UserID != parent.UserID || current.AccountScopeID != parent.AccountScopeID || current.ParentSessionID != parent.ID || current.Active || current.Status != pebblestore.V3RunIntentCompleted || current.CompletedAt == 0 || current.BlockedReason != "" {
		return nil, errors.New("handoff recovery requires this parent's current completed V3 continuation")
	}
	life, found, err := authority.GetLifecycle(child.ID)
	if err != nil {
		return nil, err
	}
	// Native task lifecycle can retain the original attempt while the resumed
	// run is recorded by V3. Reject a competing native attempt, not that history.
	if !found || life.Active || life.EndedAt == 0 || (life.RunID != asString(original["child_run_id"]) && life.RunID != recovered.RunID) {
		return nil, errors.New("handoff recovery conflicts with the native child lifecycle")
	}
	intent, found, err := authority.GetV3SessionRunIntent(child.ID, recovered.RunID)
	if err != nil {
		return nil, err
	}
	if !found || intent.SessionID != child.ID || intent.RunID != recovered.RunID || intent.ParentSessionID != parent.ID || intent.AccountScopeID != parent.AccountScopeID || intent.UserID != parent.UserID || intent.Status != pebblestore.V3RunIntentCompleted {
		return nil, errors.New("handoff recovery requires this parent's completed continuation run")
	}
	row := make(map[string]any, len(original))
	for k, v := range original {
		row[k] = v
	}
	row["phase"], row["error"] = "completed", ""
	row["head_commit"], row["child_run_id"] = req.HeadCommit, recovered.RunID
	row["verified_handoff_continuation"] = verifiedHandoffContinuation{current}
	return row, nil
}

func (r *Runtime) manageWorktreeRecoverHandoff(scope WorkspaceScope, args map[string]any) (string, error) {
	if scope.TaskHistoryOnly || scope.ReadOnlyBash || scope.ReviewCheckout != "" || len(scope.MutationScopes) > 0 || len(scope.ReadOnlyRoots) > 0 {
		return "", errors.New("handoff recovery is unavailable in a restricted specialist scope")
	}
	req := CommittedSourceRequest{TaskCallID: asString(args["task_call_id"]), ChildSessionID: asString(args["child_session_id"]), HeadCommit: asString(args["source_head"])}
	if err := req.Validate(); err != nil {
		return "", err
	}
	runID := asString(args["child_run_id"])
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(runID) != runID {
		return "", errors.New("exact child_run_id is required")
	}
	projections, ok := r.sessions.(interface {
		GetSessionProjection(string) (pebblestore.V3SessionProjection, bool, error)
	})
	if !ok {
		return "", errors.New("parent projection authority unavailable")
	}
	projection, found, err := projections.GetSessionProjection(scope.SessionID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("parent projection unavailable")
	}
	parent, err := r.manageWorktreeRecoveryParent(scope)
	if err != nil {
		return "", err
	}
	launches, _ := parent.Metadata["task_launches"].(map[string]any)
	entry, _ := launches[req.TaskCallID].(map[string]any)
	var original map[string]any
	for _, raw := range manageWorktreeLaunchRows(entry) {
		row, _ := raw.(map[string]any)
		if asString(row["child_session_id"]) == req.ChildSessionID {
			if original != nil {
				return "", errors.New("ambiguous handoff recovery lineage")
			}
			original = row
		}
	}
	if original == nil {
		return "", errors.New("owned child launch not found")
	}
	receipt := recoveredCoderHandoff{Source: req, RunID: runID, OriginalDigest: handoffDigest(original)}
	// Validate a private candidate overlay before any durable effect. The same
	// resolver authenticates it again on every future use; racing child changes
	// can only stale a receipt, never authorize the changed run or tree.
	if _, err = r.resolveCommittedSource(scope, req, &receipt); err != nil {
		return "", err
	}
	child, err := r.manageWorktreeRecoveryChild(parent, map[string]any{"child_session_id": req.ChildSessionID})
	if err != nil {
		return "", err
	}
	checker, ok := r.sessions.(interface {
		GetSessionRunState(string) (pebblestore.V3SessionRunState, bool, error)
	})
	if !ok {
		return "", errors.New("lifecycle authority unavailable")
	}
	current, found, err := checker.GetSessionRunState(child.ID)
	if err != nil {
		return "", err
	}
	if !found || current.RunID != runID || current.Active || current.Status != pebblestore.V3RunIntentCompleted {
		return "", errors.New("recovery run is not the current child run")
	}
	metadata, err := metadataWithRecoveredHandoff(parent.Metadata, receipt)
	if err != nil {
		return "", err
	}
	parent.Metadata = metadata
	now := time.Now().UnixMilli()
	parent.UpdatedAt = now
	hash := handoffDigest(receipt)
	key := "coder-handoff-recovery:" + handoffRecoveryKey(req, runID)
	seq := projection.LastEventSeq
	result, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: parent.ID, AccountScopeID: parent.AccountScopeID, UserID: parent.UserID,
		Kind: pebblestore.V3SessionMutationUpdateMetadata, Session: &parent,
		ExpectedLastEventSeq: &seq, ClientRequestID: key, IdempotencyKey: key,
		PayloadHash: hash, RequestHash: hash, NowUnixMs: now,
	})
	if err != nil {
		return "", err
	}
	if result.Error != nil || result.Conflict != nil {
		return "", fmt.Errorf("handoff recovery not recorded: error=%v conflict=%v", result.Error, result.Conflict)
	}
	if r.publishSessionOutbox != nil && result.RealtimeOutbox != nil {
		_ = r.publishSessionOutbox(*result.RealtimeOutbox)
	}
	if _, err = r.ResolveCommittedSource(scope, req); err != nil {
		return "", fmt.Errorf("recorded recovery is stale; validation refused: %w", err)
	}
	b, err := json.Marshal(map[string]any{"action": "recover_handoff", "committed_source": req, "child_run_id": runID, "original_attempt_preserved": true, "replayed": result.Replayed})
	return string(b), err
}
