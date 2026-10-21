package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/sandbox"
)

// executeCommittedSourceBash executes parent-owned command validation only in
// the exact completed child tree authenticated by the existing continuation
// authority. A path supplied by the model never selects this execution root.
// Normal Bash approval and sandbox policies still precede/contain execution.
func (r *Runtime) executeCommittedSourceBash(ctx context.Context, scope WorkspaceScope, args map[string]any, onDelta func(string)) (string, error) {
	if r == nil || r.workspace == nil {
		return "", errors.New("child validation workspace authority unavailable")
	}
	if strings.TrimSpace(asString(args["workspace_path"])) != "" {
		return "", errors.New("child validation committed_source cannot be combined with workspace_path")
	}
	if (scope.RejectScopeExpansion && !scope.ExplicitRepositoryRecovery) || scope.TaskHistoryOnly || scope.ReviewCheckout != "" || scope.ReadOnlyBash || len(scope.MutationScopes) > 0 || len(scope.ReadOnlyRoots) > 0 {
		return "", errors.New("child validation is unavailable in a restricted specialist scope")
	}
	raw, err := json.Marshal(args["committed_source"])
	if err != nil {
		return "", err
	}
	var request CommittedSourceRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		return "", fmt.Errorf("invalid child validation committed_source: %w", err)
	}
	binding, err := r.ResolveCommittedSource(scope, request)
	if err != nil {
		return "", fmt.Errorf("child validation preflight: %w", err)
	}
	canonical, canonicalErr := canonicalExistingPath(binding.SourceWorktreePath)
	if canonicalErr != nil || canonical != filepath.Clean(binding.SourceWorktreePath) {
		return "", errors.New("child validation requires a canonical non-symlink worktree root")
	}
	// Retarget both shell cwd and sandbox job identity together. Keep the caller's
	// principal/session for permission attribution; do not impersonate the child.
	executionScope := scope
	executionScope.PrimaryPath = binding.SourceWorktreePath
	executionScope.Roots = []string{binding.SourceWorktreePath}
	executionScope.RejectScopeExpansion = true
	executionScope.ExplicitRepositoryRecovery = false
	executionScope.WorktreeRootPath = binding.SourceWorktreePath
	executionScope.WorktreeBranch = binding.SourceBranch
	executionScope.WorktreeBaseCommit = binding.SourceBaseCommit
	ctx = sandbox.WithJobCheckout(ctx, binding.SourceWorktreePath)
	output, executionErr := executeBash(ctx, executionScope, args, onDelta)
	payload := map[string]any{}
	if output != "" {
		if err = json.Unmarshal([]byte(output), &payload); err != nil {
			return output, fmt.Errorf("decode child validation result: %w", err)
		}
	}
	receipt := map[string]any{"task_call_id": request.TaskCallID, "child_session_id": request.ChildSessionID, "head_commit": request.HeadCommit, "workspace_path": binding.SourceWorktreePath, "verified": false}
	after, recheckErr := r.ResolveCommittedSource(scope, request)
	canonical, canonicalErr = canonicalExistingPath(binding.SourceWorktreePath)
	if canonicalErr != nil || canonical != filepath.Clean(binding.SourceWorktreePath) {
		recheckErr = errors.New("child validation worktree root changed or became a symlink")
	}
	switch {
	case executionErr != nil:
		receipt["reason"] = executionErr.Error()
	case recheckErr != nil:
		receipt["reason"] = "child validation postcondition failed: " + recheckErr.Error()
	case after != binding:
		receipt["reason"] = "child validation ownership or source binding changed during execution"
	case payload["exit_code"] != float64(0) || payload["timed_out"] != false:
		receipt["reason"] = "validation command did not complete successfully"
	default:
		receipt["verified"] = true
	}
	if receipt["verified"] != true {
		reason := asString(receipt["reason"])
		if len(reason) > 4000 {
			reason = reason[:4000] + " (truncated)"
			receipt["reason"] = reason
		}
		payload["summary"] = "Child validation failed: " + reason
		if executionErr == nil {
			executionErr = errors.New(reason)
		}
	} else {
		payload["summary"] = "Command passed on verified child commit " + request.HeadCommit
	}
	payload["validation_receipt"] = receipt
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), executionErr
}
