package run

import (
	"context"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type providerToolInvokerConfig struct {
	sessionID            string
	permissionSessionID  string
	runID                string
	step                 int
	sessionMode          string
	workspacePath        string
	workspaceRoots       []string
	workspaceOriginPath  string
	workspaceOriginRoots []string
	workspaceName        string
	emit                 StreamHandler
	policy               *permission.Policy
	agentProfile         pebblestore.AgentProfile
}

type providerToolInvoker struct {
	service *Service
	config  providerToolInvokerConfig
}

func (s *Service) newProviderToolInvoker(config providerToolInvokerConfig) provideriface.ToolInvoker {
	if s == nil {
		return nil
	}
	return &providerToolInvoker{
		service: s,
		config:  config,
	}
}

func (i *providerToolInvoker) ExecuteTool(ctx context.Context, invocation provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	if i == nil || i.service == nil {
		return provideriface.ToolExecutionResult{}, errors.New("provider tool invoker is not configured")
	}

	call := tool.Call{
		CallID:    strings.TrimSpace(invocation.CallID),
		Name:      strings.TrimSpace(invocation.Name),
		Arguments: strings.TrimSpace(invocation.Arguments),
	}
	if call.CallID == "" {
		call.CallID = "tool_call"
	}
	if call.Name == "" {
		call.Name = "tool"
	}
	if call.Arguments == "" {
		call.Arguments = "{}"
	}

	result, err := i.service.executeProviderManagedToolCall(ctx, i.config, call, cloneGenericMap(invocation.Metadata))
	if err != nil {
		return provideriface.ToolExecutionResult{}, err
	}

	return provideriface.ToolExecutionResult{
		CallID:       strings.TrimSpace(result.CallID),
		Name:         strings.TrimSpace(result.Name),
		Output:       strings.TrimSpace(result.Output),
		Error:        strings.TrimSpace(result.Error),
		DurationMS:   result.DurationMS,
		TextForModel: prepareToolOutputForModel(call, result),
		RestartTurn:  providerManagedToolRequiresTurnRestart(call, result),
	}, nil
}

func providerManagedToolRequiresTurnRestart(call tool.Call, result tool.Result) bool {
	payload := decodeToolPayload(strings.TrimSpace(result.Output))
	if payload == nil {
		return false
	}
	if mapBool(payload, "restart_turn") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(call.Name), "exit_plan_mode") && mapBool(payload, "mode_changed") {
		return true
	}
	return false
}

func (s *Service) executeProviderManagedToolCall(ctx context.Context, config providerToolInvokerConfig, call tool.Call, metadata map[string]any) (tool.Result, error) {
	if s == nil {
		return tool.Result{}, errors.New("run service is not configured")
	}

	name := strings.TrimSpace(call.Name)
	callID := strings.TrimSpace(call.CallID)
	if name == "" {
		name = "tool"
	}
	if callID == "" {
		callID = "tool_call"
	}
	call.Name = name
	call.CallID = callID
	if strings.TrimSpace(call.Arguments) == "" {
		call.Arguments = "{}"
	}

	if config.emit != nil {
		config.emit(StreamEvent{
			Type:      StreamEventToolStarted,
			Step:      config.step,
			ToolName:  name,
			CallID:    callID,
			Arguments: call.Arguments,
		})
	}

	permissionSessionID := strings.TrimSpace(config.permissionSessionID)
	if permissionSessionID == "" {
		permissionSessionID = strings.TrimSpace(config.sessionID)
	}

	gatedResults, approvedCalls, _, _, permissionFeedback, err := s.gateToolCalls(
		ctx,
		permissionSessionID,
		config.runID,
		config.step,
		config.sessionMode,
		[]tool.Call{call},
		config.emit,
		config.policy,
	)
	if err != nil {
		return tool.Result{}, err
	}

	result := gatedResults[0]
	if len(approvedCalls) > 0 {
		feedback := PermissionFeedback{}
		if len(permissionFeedback) > 0 {
			feedback = permissionFeedback[0]
		}

		handled, controlResult, controlErr := s.executeControlPlaneTool(ctx, config.sessionID, config.sessionMode, config.agentProfile, config.step, call, feedback.ApprovedArguments, config.emit)
		if handled {
			result = controlResult
			if controlErr != nil {
				result.Error = strings.TrimSpace(controlErr.Error())
				if strings.TrimSpace(result.Output) == "" {
					result.Output = strings.TrimSpace(controlErr.Error())
				}
			}
		} else {
			if s.tools == nil {
				result = tool.Result{
					CallID: call.CallID,
					Name:   call.Name,
					Output: "tool runtime is not configured",
					Error:  "tool runtime is not configured",
				}
			} else {
				originRoots := append([]string(nil), config.workspaceOriginRoots...)
				if len(originRoots) == 0 {
					originRoots = append([]string(nil), config.workspaceRoots...)
				}
				workspaceCtx := runWorkspaceContext{
					WorkspacePath:        config.workspacePath,
					WorkspaceRoots:       append([]string(nil), config.workspaceRoots...),
					OriginWorkspacePath:  strings.TrimSpace(firstNonEmptyString(config.workspaceOriginPath, config.workspacePath)),
					OriginWorkspaceRoots: originRoots,
				}
				if len(workspaceCtx.OriginWorkspaceRoots) == 0 && strings.TrimSpace(workspaceCtx.OriginWorkspacePath) != "" {
					workspaceCtx.OriginWorkspaceRoots = []string{workspaceCtx.OriginWorkspacePath}
				}
				scopeResults, scopeApprovedCalls, _, _, scopeErr := s.gateWorkspaceScopeCalls(
					ctx,
					config.sessionID,
					permissionSessionID,
					config.runID,
					config.step,
					config.sessionMode,
					workspaceCtx.OriginWorkspacePath,
					config.workspaceName,
					&workspaceCtx,
					[]tool.Call{call},
					config.emit,
				)
				if scopeErr != nil {
					return tool.Result{}, scopeErr
				}
				if len(scopeApprovedCalls) == 0 && len(scopeResults) > 0 {
					result = scopeResults[0]
				} else if len(scopeApprovedCalls) > 0 {
					runtimeCtx := tool.WithWorkspaceScope(ctx, tool.WorkspaceScope{
						PrimaryPath: workspaceCtx.WorkspacePath,
						Roots:       append([]string(nil), workspaceCtx.WorkspaceRoots...),
					})
					executed := s.tools.ExecuteBatchStreamingWithProgress(runtimeCtx, workspaceCtx.WorkspacePath, scopeApprovedCalls, func(_ int, current tool.Call, progress tool.Progress) {
						if config.emit == nil {
							return
						}
						if strings.ToLower(strings.TrimSpace(progress.Stage)) != "output" {
							return
						}
						delta := progress.Output
						if delta == "" {
							return
						}
						config.emit(StreamEvent{
							Type:     StreamEventToolDelta,
							Step:     config.step,
							ToolName: strings.TrimSpace(current.Name),
							CallID:   strings.TrimSpace(current.CallID),
							Output:   truncateRunes(delta, maxToolDeltaChars),
						})
					}, nil)
					if len(executed) > 0 {
						result = executed[0]
					}
				}
			}
		}
	}

	if strings.TrimSpace(result.CallID) == "" {
		result.CallID = call.CallID
	}
	if strings.TrimSpace(result.Name) == "" {
		result.Name = call.Name
	}

	if config.emit != nil {
		config.emit(StreamEvent{
			Type:       StreamEventToolCompleted,
			Step:       config.step,
			ToolName:   strings.TrimSpace(result.Name),
			CallID:     strings.TrimSpace(result.CallID),
			Output:     formatToolCompletedOutput(call, result),
			RawOutput:  liveStreamRawOutput(call, result),
			Error:      strings.TrimSpace(result.Error),
			DurationMS: result.DurationMS,
		})
	}

	if err := s.storeProviderManagedToolResult(config, call, metadata, result); err != nil {
		return tool.Result{}, err
	}

	return result, nil
}

func (s *Service) storeProviderManagedToolResult(config providerToolInvokerConfig, call tool.Call, metadata map[string]any, result tool.Result) error {
	if s == nil || s.sessions == nil {
		return errors.New("session store is not configured")
	}

	toolHistoryText := formatToolHistoryWithMetadata(call, metadata, result)
	storedToolMessage, _, event, err := s.sessions.AppendMessage(config.sessionID, "tool", toolHistoryText, nil)
	if err != nil {
		return err
	}

	if config.emit != nil {
		config.emit(StreamEvent{Type: StreamEventMessageStored, Step: config.step, Message: &storedToolMessage})
	}
	if sessionSnapshot, ok, sessionErr := s.sessions.GetSession(config.sessionID); sessionErr == nil && ok {
		if commitMeta, detected := detectGitCommit(call, result); detected {
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
