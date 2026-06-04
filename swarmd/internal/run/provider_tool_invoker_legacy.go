package run

import (
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/tool"
)

func (s *Service) storeProviderManagedToolResultLegacy(config providerToolInvokerConfig, call toolCallSnapshot, metadata map[string]any, result toolResultSnapshot) error {
	if s == nil || s.sessions == nil {
		return errors.New("session store is not configured")
	}

	toolHistoryText := formatToolHistoryWithMetadata(call.ToolCall(), metadata, result.ToolResult())
	storedToolMessage, _, event, err := s.sessions.AppendMessage(config.sessionID, "tool", toolHistoryText, nil)
	if err != nil {
		return err
	}

	if config.emit != nil {
		config.emit(StreamEvent{Type: StreamEventMessageStored, Step: config.step, Message: &storedToolMessage})
	}
	if sessionSnapshot, ok, sessionErr := s.sessions.GetSession(config.sessionID); sessionErr == nil && ok {
		if commitMeta, detected := detectGitCommit(call.ToolCall(), result.ToolResult()); detected {
			updatedMetadata := sessionGitMetadata(sessionSnapshot.Metadata)
			gitMeta, _ := updatedMetadata["git"].(map[string]any)
			if gitMeta != nil {
				gitMeta["commit_detected"] = true
				gitMeta["commit_count"] = sessionGitCommitCount(updatedMetadata) + 1
				gitMeta["last_commit"] = commitMeta
				gitMeta["last_commit_at"] = storedToolMessage.CreatedAt
				if updatedSession, env, updateErr := s.sessions.UpdateMetadata(config.sessionID, updatedMetadata); updateErr == nil {
					sessionSnapshot = updatedSession
					if env != nil {
						s.publishEventEnvelope(*env)
					}
				}
			}
		}
		s.maybeRefreshSessionGitState(config.sessionID, sessionSnapshot)
	}
	if event != nil {
		s.publishEventEnvelope(*event)
	}

	return nil
}

type toolCallSnapshot struct {
	CallID    string
	Name      string
	Arguments string
}

func newToolCallSnapshot(call tool.Call) toolCallSnapshot {
	return toolCallSnapshot{CallID: strings.TrimSpace(call.CallID), Name: strings.TrimSpace(call.Name), Arguments: strings.TrimSpace(call.Arguments)}
}

func (s toolCallSnapshot) ToolCall() tool.Call {
	return tool.Call{CallID: s.CallID, Name: s.Name, Arguments: s.Arguments}
}

type toolResultSnapshot struct {
	CallID     string
	Name       string
	Output     string
	Error      string
	DurationMS int64
}

func newToolResultSnapshot(result tool.Result) toolResultSnapshot {
	return toolResultSnapshot{CallID: strings.TrimSpace(result.CallID), Name: strings.TrimSpace(result.Name), Output: strings.TrimSpace(result.Output), Error: strings.TrimSpace(result.Error), DurationMS: result.DurationMS}
}

func (s toolResultSnapshot) ToolResult() tool.Result {
	return tool.Result{CallID: s.CallID, Name: s.Name, Output: s.Output, Error: s.Error, DurationMS: s.DurationMS}
}
