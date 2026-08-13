package run

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// applyTaskContextCompaction is the Task-owned replacement for successor-session
// handoff. It uses Compact as a tool-free summarizer, but Task orchestration owns
// the boundary, trusted assignment/identity, workspace evidence, repetition
// limit, persistence, and same-session continuation.
func (s *Service) applyTaskContextCompaction(ctx context.Context, sessionID, runID, providerID string, preference pebblestore.ModelPreference, contextWindow, maxOutputTokens, step, maxCompactions int, task *TaskContextCompaction, emit StreamHandler, appendInput runAppendMessageInput) ([]map[string]any, error) {
	if task == nil {
		return nil, errors.New("Task context compaction contract is required")
	}
	messages, err := s.listMessagesForMemoryCompaction(sessionID, true)
	if err != nil {
		return nil, fmt.Errorf("load Task context for compaction: %w", err)
	}
	compactIndex := nextMemoryCompactionIndex(messages)
	if maxCompactions < 1 {
		maxCompactions = 1
	}
	if compactIndex-1 > maxCompactions {
		return nil, fmt.Errorf("Task context reached its configured maximum of %d same-session compactions", maxCompactions)
	}

	workspaceEvidence := "- inspection: unavailable"
	if s.worktrees != nil && strings.TrimSpace(task.WorkspacePath) != "" {
		if state, inspectErr := s.worktrees.InspectTaskWorkspace(task.WorkspacePath); inspectErr != nil {
			workspaceEvidence = "- inspection error: " + truncateRunes(inspectErr.Error(), 800)
		} else {
			status := strings.TrimSpace(state.Status)
			if status == "" {
				status = "clean"
			}
			workspaceEvidence = fmt.Sprintf("- branch: %s\n- HEAD: %s\n- clean: %t\n- changed files/status:\n%s", state.BranchName, state.HeadCommit, state.Clean, truncateRunes(status, 4000))
		}
	}
	brief := fmt.Sprintf(`Task-specific same-session compaction boundary.

This is Compact #%d for one logical Task. It replaces successor-session handoff: the same durable child session, run, workspace, permissions, reservation, and logical Task continue after this checkpoint.

Immutable delegated assignment:
%s

Trusted logical identity:
- logical task: %s
- Task call: %s
- Task Program: %s
- Task Program job: %s
- child session: %s
- run: %s
- workspace: %s
- allocated branch: %s
- immutable base: %s

Trusted workspace/Git snapshot captured at the safe boundary:
%s

Produce the normal stable Compact sections. Explicitly state what was complete before this compact, what changed since any prior compact, what remains, and the exact next action. Do not treat discovery-only or truncated tool output as proof of completion.`, compactIndex, strings.TrimSpace(task.OriginalAssignment), strings.TrimSpace(task.LogicalTaskID), strings.TrimSpace(task.TaskCallID), strings.TrimSpace(task.ProgramID), strings.TrimSpace(task.ProgramJobID), strings.TrimSpace(sessionID), strings.TrimSpace(runID), strings.TrimSpace(task.WorkspacePath), strings.TrimSpace(task.WorktreeBranch), strings.TrimSpace(task.ImmutableBaseCommit), workspaceEvidence)

	var compactionToolStream *memoryCompactionToolStream
	summary, err := s.compactRunContextWithMemory(ctx, sessionID, brief, "", preference, contextWindow, maxOutputTokens, false, contextCompactionOriginTask, true, step, compactIndex-1, emit, &compactionToolStream)
	if err != nil {
		return nil, fmt.Errorf("Task same-session compact failed: %w", err)
	}
	var compactEvents []pebblestore.EventEnvelope
	if toolMessage, persistErr := s.persistMemoryCompactionToolMessage(sessionID, &compactEvents, nil, compactionToolStream, runAppendMessageInput{RunID: runID, Step: step, LogicalKey: fmt.Sprintf("tool:%d:%s", step, strings.TrimSpace(compactionToolStream.CallID)), Principal: appendInput.Principal, ApplySessionMutation: appendInput.ApplySessionMutation}); persistErr != nil {
		return nil, persistErr
	} else if toolMessage != nil && emit != nil {
		emit(StreamEvent{Type: StreamEventMessageStored, Step: step, Message: toolMessage})
	}
	if _, _, _, err := s.applyContextCompactionArtifacts(sessionID, summary, contextCompactionOriginTask, contextWindow, providerID, preference.Model, step, emit, runAppendMessageInput{RunID: runID, Step: step, LogicalKey: fmt.Sprintf("system:task_context_compaction:%d", compactIndex), Principal: appendInput.Principal, ApplySessionMutation: appendInput.ApplySessionMutation}); err != nil {
		return nil, fmt.Errorf("persist Task same-session compact: %w", err)
	}
	input := buildCompactedContinuationInput(strings.TrimSpace(task.OriginalAssignment), summary, nil, contextCompactionOriginTask)
	if len(input) == 0 {
		return nil, errors.New("Task same-session compact produced empty continuation input")
	}
	return input, nil
}

func (s *Service) runDelegatedLogicalLaunch(ctx context.Context, launch taskLaunchPrepared, delegatedPrompt, permissionSessionID string, principal identity.Principal, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), onEvent StreamHandler) (taskLaunchPrepared, RunResult, error) {
	if err := s.requireCurrentDelegatedLaunch(launch); err != nil {
		return launch, RunResult{}, err
	}
	result, err := s.RunTurnStreaming(ctx, launch.ChildSession.ID, RunRequest{
		Prompt: delegatedPrompt, TargetKind: RunTargetKindSubagent, TargetName: launch.SubagentProfile.Name, AgentName: launch.SubagentProfile.Name,
	}, func() RunStartMeta {
		meta := delegatedSubagentRunStartMeta(launch, permissionSessionID, principal, applySessionMutation)
		meta.DisabledTools = taskDisabledTools(strings.EqualFold(strings.TrimSpace(launch.RequestedSubagent), "coder"))
		if launch.ArtifactRunContext != nil {
			meta.DisabledTools["write"], meta.DisabledTools["edit"] = true, true
		}
		return meta
	}(), onEvent)
	return launch, result, err
}

func (s *Service) requireCurrentDelegatedLaunch(launch taskLaunchPrepared) error {
	lineage, ok, err := s.sessions.GetDelegatedChildLineage(launch.ChildSession.AccountScopeID, launch.LogicalTaskID)
	if err != nil {
		return fmt.Errorf("revalidate delegated logical Task ownership: %w", err)
	}
	if !ok || lineage.CurrentSessionID != launch.ChildSession.ID {
		return errors.New("stale delegated child generation cannot start or publish a logical Task outcome")
	}
	generation := intFromMetadata(launch.ChildSession.Metadata, "context_generation", 1)
	record, ok, err := s.sessions.GetDelegatedChildGeneration(lineage.AccountScopeID, lineage.LogicalTaskID, generation)
	if err != nil {
		return fmt.Errorf("revalidate delegated child generation: %w", err)
	}
	if !ok || lineage.CurrentGeneration != generation || record.SessionID != launch.ChildSession.ID || record.State != pebblestore.DelegatedChildGenerationActive {
		return errors.New("stale delegated child generation cannot start or publish a logical Task outcome")
	}
	return nil
}

func intFromMetadata(metadata map[string]any, key string, fallback int) int {
	switch value := metadata[key].(type) {
	case int:
		if value > 0 {
			return value
		}
	case float64:
		if value >= 1 && value == float64(int(value)) {
			return int(value)
		}
	}
	return fallback
}
