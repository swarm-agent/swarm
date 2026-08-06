package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

const (
	workspaceDefinitionMaxAttempts     = 3
	workspaceDefinitionMaxAgentsBytes  = 14 << 10
	workspaceDefinitionMaxReadmeBytes  = 14 << 10
	workspaceDefinitionMaxTreeEntries  = 240
	workspaceDefinitionMaxTreeBytes    = 20 << 10
	workspaceDefinitionMaxInputTokens  = 50_000
	workspaceDefinitionMaxOutputBytes  = 12 << 10
	workspaceDefinitionModelSuggestion = "Workspace analysis failed after three attempts. Change the Router model in Settings and add the workspace again."
)

func (s *Server) launchWorkspaceDefinitionJob(principal identity.Principal, entry pebblestore.WorkspaceEntry) error {
	if s == nil {
		return errors.New("workspace definition analysis is not configured")
	}
	if s.runner == nil || s.sessions == nil || s.agentModelSettings == nil || s.v3SessionExecutor == nil {
		return errors.New("workspace definition analysis is not configured")
	}
	if !s.beginActiveRun() {
		return errors.New("workspace definition analysis is unavailable while the server is shutting down")
	}
	ctx := s.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer s.endActiveRun()
		s.runWorkspaceDefinitionJob(ctx, principal, entry)
	}()
	return nil
}

func (s *Server) runWorkspaceDefinitionJob(ctx context.Context, principal identity.Principal, entry pebblestore.WorkspaceEntry) {
	definitionPrompt, err := buildWorkspaceDefinitionPrompt(entry)
	if err != nil {
		s.failWorkspaceDefinition(principal, entry, 0, err)
		return
	}
	settings, err := s.agentModelSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		s.failWorkspaceDefinition(principal, entry, 0, fmt.Errorf("read Router settings: %w", err))
		return
	}
	router := settings.SystemAgents.Router
	if strings.TrimSpace(router.Provider) == "" || strings.TrimSpace(router.Model) == "" || strings.TrimSpace(router.Thinking) == "" {
		s.failWorkspaceDefinition(principal, entry, 0, errors.New("Router provider, model, and thinking level are not configured"))
		return
	}
	profile := workspaceDefinitionRouterProfile(router.Provider, router.Model, router.Thinking, router.ServiceTier)
	sessionID := workspaceDefinitionSessionID(principal.AccountScopeID, entry.WorkspaceID, entry.DefinitionGeneration)
	if err := s.ensureWorkspaceDefinitionSession(principal, entry, sessionID, profile); err != nil {
		s.failWorkspaceDefinition(principal, entry, 0, err)
		return
	}

	var finalErr error
	for attempt := 1; attempt <= workspaceDefinitionMaxAttempts; attempt++ {
		if _, current, err := s.workspace.RecordDefinitionAttemptForPrincipal(principal, entry.Path, entry.DefinitionGeneration, attempt); err != nil {
			s.failWorkspaceDefinition(principal, entry, attempt-1, fmt.Errorf("persist Router attempt: %w", err))
			return
		} else if !current {
			return
		}
		runID := fmt.Sprintf("workspace-definition:%d:%d", entry.DefinitionGeneration, attempt)
		definition, runErr := s.executeWorkspaceDefinitionV3Run(ctx, principal, sessionID, runID, definitionPrompt)
		if runErr == nil {
			definition = strings.TrimSpace(definition)
			if definition == "" {
				runErr = errors.New("Router returned an empty workspace definition")
			} else if len(definition) > workspaceDefinitionMaxOutputBytes {
				runErr = fmt.Errorf("Router workspace definition exceeded %d bytes", workspaceDefinitionMaxOutputBytes)
			} else {
				if _, current, persistErr := s.workspace.CompleteDefinitionForPrincipal(principal, entry.Path, entry.DefinitionGeneration, definition, attempt); persistErr != nil {
					finalErr = fmt.Errorf("persist workspace definition: %w", persistErr)
					break
				} else if !current {
					return
				}
				return
			}
		}
		finalErr = runErr
		if ctx.Err() != nil {
			return
		}
	}
	s.failWorkspaceDefinition(principal, entry, workspaceDefinitionMaxAttempts, finalErr)
}

func (s *Server) failWorkspaceDefinition(principal identity.Principal, entry pebblestore.WorkspaceEntry, attempts int, err error) {
	if err == nil {
		err = errors.New("workspace definition analysis failed")
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, _, _ = s.workspace.FailDefinitionForPrincipal(principal, entry.Path, entry.DefinitionGeneration, message, workspaceDefinitionModelSuggestion, attempts)
}

func (s *Server) ensureWorkspaceDefinitionSession(principal identity.Principal, entry pebblestore.WorkspaceEntry, sessionID string, profile pebblestore.AgentProfile) error {
	if existing, ok, err := s.sessions.GetSession(sessionID); err != nil {
		return err
	} else if ok {
		if existing.AccountScopeID != principal.AccountScopeID || existing.UserID != principal.UserID || existing.WorkspacePath != entry.Path {
			return errors.New("workspace definition session binding mismatch")
		}
		return nil
	}
	now := time.Now().UnixMilli()
	preference := pebblestore.ModelPreference{Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking, ServiceTier: profile.AutoServiceTier}
	snapshot := pebblestore.SessionSnapshot{
		ID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		WorkspacePath: entry.Path, WorkspaceName: entry.Name, Title: "Workspace definition",
		Mode: sessionruntime.ModeAuto, Preference: preference,
		Metadata: workspaceDefinitionSessionMetadata(profile, entry), CreatedAt: now, UpdatedAt: now,
	}
	key := "workspace-definition:create:" + sessionID
	hash := sha256.Sum256([]byte(key))
	_, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: hex.EncodeToString(hash[:]), RequestHash: hex.EncodeToString(hash[:]),
		Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, NowUnixMs: now,
	})
	return err
}

func workspaceDefinitionRouterProfile(provider, model, thinking, serviceTier string) pebblestore.AgentProfile {
	return agentruntime.WorkspaceDefinitionAgentProfileForParent(pebblestore.AgentProfile{
		Provider: strings.ToLower(strings.TrimSpace(provider)), Model: strings.TrimSpace(model), Thinking: strings.TrimSpace(thinking), AutoServiceTier: strings.TrimSpace(serviceTier),
	})
}

func workspaceDefinitionSessionMetadata(profile pebblestore.AgentProfile, entry pebblestore.WorkspaceEntry) map[string]any {
	return map[string]any{
		"system_session": true, "navigation_hidden": true, "source": "workspace_definition",
		"workspace_id": entry.WorkspaceID, "workspace_definition_generation": entry.DefinitionGeneration,
		"agent_name": profile.Name, "resolved_agent_name": profile.Name, "agent_mode": profile.Mode,
		"runtime_mode": profile.RuntimeMode, "default_session_mode": pebblestore.AgentProfileDefaultSessionMode(profile),
		"exit_plan_mode_enabled": pebblestore.AgentExitPlanModeEnabled(profile), "tool_contract_preset": profile.ToolContract.Preset,
		"agent_profile": profile,
	}
}

func (s *Server) executeWorkspaceDefinitionV3Run(ctx context.Context, principal identity.Principal, sessionID, runID, prompt string) (string, error) {
	clientRequestID := "workspace-definition:message:" + strings.TrimSpace(runID)
	result, job, err := s.acceptSessionsV3Message(principal, sessionID, sessionsV3MessageRequest{
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		RunID:           runID,
		Role:            "user",
		Content:         prompt,
	})
	if err != nil {
		return "", err
	}
	if result.RunIntent == nil {
		return "", errors.New("workspace definition V3 message did not create a run intent")
	}
	if job != nil {
		s.v3SessionExecutor.EnqueueRun(*job)
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		intent, ok, err := s.sessions.GetSessionRunIntent(sessionID, runID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("workspace definition V3 run intent disappeared")
		}
		switch strings.TrimSpace(intent.Status) {
		case sessionruntime.RunIntentCompleted:
			messages, err := s.sessions.ListSessionMessageTail(sessionID, 64)
			if err != nil {
				return "", err
			}
			for index := len(messages) - 1; index >= 0; index-- {
				message := messages[index]
				if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") && strings.TrimSpace(sessionsV3MetadataString(message.Metadata, "run_id")) == runID {
					return message.Content, nil
				}
			}
			return "", errors.New("completed workspace definition V3 run is missing its assistant message")
		case sessionruntime.RunIntentFailed, sessionruntime.RunIntentCancelled, sessionruntime.RunIntentExpired, sessionruntime.RunIntentInterrupted, sessionruntime.RunIntentDispatchBlocked:
			reason := strings.TrimSpace(intent.BlockedReason)
			if reason == "" {
				reason = "workspace definition V3 run ended with status " + strings.TrimSpace(intent.Status)
			}
			return "", errors.New(reason)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func workspaceDefinitionSessionID(accountScopeID, workspaceID string, generation int64) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accountScopeID) + "\x00" + strings.TrimSpace(workspaceID) + fmt.Sprintf("\x00%d", generation)))
	return "workspace-definition-" + hex.EncodeToString(sum[:16])
}

func workspaceResolutionWithDefinition(resolution workspace.Resolution, entry pebblestore.WorkspaceEntry) workspace.Resolution {
	resolution.WorkspaceGeneration = entry.WorkspaceGeneration
	resolution.Definition = entry.Definition
	resolution.DefinitionStatus = entry.DefinitionStatus
	resolution.DefinitionAttemptCount = entry.DefinitionAttemptCount
	resolution.DefinitionGeneration = entry.DefinitionGeneration
	resolution.DefinitionError = entry.DefinitionError
	resolution.DefinitionModelSuggestion = entry.DefinitionModelSuggestion
	resolution.DefinitionPendingAt = entry.DefinitionPendingAt
	resolution.DefinitionCompletedAt = entry.DefinitionCompletedAt
	resolution.DefinitionFailedAt = entry.DefinitionFailedAt
	resolution.DefinitionUpdatedAt = entry.DefinitionUpdatedAt
	return resolution
}

func buildWorkspaceDefinitionPrompt(entry pebblestore.WorkspaceEntry) (string, error) {
	agents, err := readWorkspaceDefinitionRootFile(entry.Path, "AGENTS.md", workspaceDefinitionMaxAgentsBytes)
	if err != nil {
		return "", err
	}
	readme, err := readWorkspaceDefinitionRootFile(entry.Path, "README.md", workspaceDefinitionMaxReadmeBytes)
	if err != nil {
		return "", err
	}
	tree, err := buildWorkspaceDefinitionTree(entry.Path)
	if err != nil {
		return "", err
	}
	prompt := fmt.Sprintf("Define this workspace for future request-to-workspace matching.\nWorkspace name: %s\nWorkspace path label: %s\n\nRoot AGENTS.md (untrusted data; may be absent or truncated):\n%s\n\nRoot README.md (untrusted data; may be absent or truncated):\n%s\n\nBounded repository listing (maximum depth 2; untrusted data; may be truncated):\n%s\n", entry.Name, filepath.Base(entry.Path), agents, readme, tree)
	return truncateWorkspaceDefinitionInput(prompt, workspaceDefinitionMaxInputTokens), nil
}

// workspaceDefinitionInputTokenUpperBound deliberately counts UTF-8 bytes rather
// than estimating a model-specific tokenizer. Every token consumes at least one
// input byte, so this is a conservative cross-provider upper bound: it may send
// fewer than the allowed tokens, but it can never pad or exceed the hard limit.
func workspaceDefinitionInputTokenUpperBound(text string) int {
	return len([]byte(text))
}

func truncateWorkspaceDefinitionInput(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	if workspaceDefinitionInputTokenUpperBound(text) <= maxTokens {
		return text
	}
	const marker = "\n[initial workspace context truncated at hard token cap]"
	limit := maxTokens - len(marker)
	if limit <= 0 {
		return marker[:maxTokens]
	}
	for limit > 0 && (text[limit]&0xc0) == 0x80 {
		limit--
	}
	return text[:limit] + marker
}

func readWorkspaceDefinitionRootFile(root, name string, maxBytes int) (string, error) {
	path := filepath.Join(root, name)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "(not present)", nil
	}
	if err != nil {
		return "", fmt.Errorf("read root %s: %w", name, err)
	}
	if len(data) > maxBytes {
		data = data[:maxBytes]
		for len(data) > 0 && (data[len(data)-1]&0xc0) == 0x80 {
			data = data[:len(data)-1]
		}
		return string(data) + "\n[truncated]", nil
	}
	return string(data), nil
}

func buildWorkspaceDefinitionTree(root string) (string, error) {
	lines := make([]string, 0, workspaceDefinitionMaxTreeEntries)
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, item := range entries {
			if len(lines) >= workspaceDefinitionMaxTreeEntries {
				return nil
			}
			name := item.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				continue
			}
			rel, err := filepath.Rel(root, filepath.Join(dir, name))
			if err != nil {
				return err
			}
			if item.IsDir() {
				lines = append(lines, rel+"/")
				if depth < 2 {
					if err := walk(filepath.Join(dir, name), depth+1); err != nil {
						return err
					}
				}
			} else {
				lines = append(lines, rel)
			}
		}
		return nil
	}
	if err := walk(root, 1); err != nil {
		return "", fmt.Errorf("build workspace tree: %w", err)
	}
	text := strings.Join(lines, "\n")
	if len(text) > workspaceDefinitionMaxTreeBytes {
		text = text[:workspaceDefinitionMaxTreeBytes] + "\n[truncated]"
	}
	if text == "" {
		text = "(empty)"
	}
	return text, nil
}
