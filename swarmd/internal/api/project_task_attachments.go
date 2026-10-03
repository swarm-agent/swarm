package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Preflight is read-only and runs before task reservation. Resolve the selected
// model once; attachment incompatibility must never select a different model.
func (s *Server) preflightProjectTaskAttachments(ctx context.Context, p identity.Principal, task *pebblestore.ProjectTaskRecord) error {
	if len(task.AttachedMedia) == 0 || isDirectMediaTask(task) {
		return nil
	}
	pref, source, err := s.resolveTaskModelPreference(p, task)
	if err != nil {
		return err
	}
	name, mode := projectTaskExecutionAgent(task)
	id, ok := agentruntime.CanonicalSystemAgentID(name)
	if !ok || s.agents == nil {
		return errors.New("task attachments require a canonical agent profile")
	}
	profile, err := s.agents.ResolveSystemAgent(id, pebblestore.AgentProfile{Provider: pref.Provider, Model: pref.Model, Thinking: pref.Thinking, AutoServiceTier: pref.ServiceTier, ContextMode: pref.ContextMode})
	if err != nil {
		return err
	}
	available := true
	candidate := pebblestore.SessionSnapshot{ID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: task.SourceWorkspace.Path, Mode: mode, Preference: pref,
		Metadata:        map[string]any{"agent_profile": cloneSessionsV3AgentProfile(profile)},
		WorkspaceGrants: []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: task.SourceWorkspace.WorkspaceID, WorkspaceGeneration: task.SourceWorkspace.WorkspaceGeneration, Path: task.SourceWorkspace.Path, Available: &available}},
	}
	plan, _, err := s.prepareProjectTaskAttachments(ctx, p, candidate, task.AttachedMedia)
	if err != nil {
		return err
	}
	// Freeze byte-derived facts in the reservation so recovery rejects a stale
	// or replaced source instead of delivering different bytes.
	task.AttachedMedia = append([]pebblestore.ProjectTaskMediaRef(nil), task.AttachedMedia...)
	for i, binding := range plan.Bindings {
		task.AttachedMedia[i].DigestSHA256 = binding.Metadata.DigestSHA256
		task.AttachedMedia[i].SizeBytes = binding.Metadata.Size
		task.AttachedMedia[i].MediaType = binding.Metadata.DetectedMIMEType
		task.AttachedMedia[i].Kind = binding.Metadata.Modality
	}
	// Persist the exact admitted preference so deployment cannot drift with an
	// account setting update between admission and session creation.
	task.Provider, task.Model, task.Thinking = pref.Provider, pref.Model, pref.Thinking
	task.ServiceTier, task.ContextMode = pref.ServiceTier, pref.ContextMode
	if source == "account_default" && id != agentruntime.SwarmAgentID {
		alert := fmt.Sprintf("Configured model for agent %q could not be resolved or was unconfigured; fell back to Swarm default (%s/%s).", name, pref.Provider, pref.Model)
		if task.RouterAlert != "" {
			task.RouterAlert += " | "
		}
		task.RouterAlert += alert
	}
	return nil
}

func projectTaskExecutionAgent(task *pebblestore.ProjectTaskRecord) (string, string) {
	name, mode := strings.TrimSpace(task.Agent), "auto"
	if name == "plan" || task.Status == "planning" || task.TaskProgram != nil || task.OutcomeType == "plan_spec" {
		name, mode = "swarm", "plan"
	}
	if name == "" || task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
		name = "swarm"
		if task.ActiveAttemptID != "" && task.ActiveAttemptID != "initial" {
			mode = "auto"
		}
	}
	return name, mode
}

func (s *Server) prepareProjectTaskAttachments(ctx context.Context, p identity.Principal, candidate pebblestore.SessionSnapshot, attachments []pebblestore.ProjectTaskMediaRef) (runruntime.PreSessionMediaBindingPlan, [][]byte, error) {
	var empty runruntime.PreSessionMediaBindingPlan
	if len(attachments) == 0 {
		return empty, nil, nil
	}
	if len(attachments) > pebblestore.SessionMediaDefaultMaxCount {
		return empty, nil, errors.New("task attachment count limit exceeded")
	}
	contract, err := s.routedSessionMediaContract(ctx, p, candidate)
	if err != nil {
		return empty, nil, fmt.Errorf("task attachment capability resolution: %w", err)
	}
	if contract.ProviderID != candidate.Preference.Provider || contract.Model != candidate.Preference.Model {
		return empty, nil, errors.New("task attachment contract changed the selected model")
	}
	staged := make([]runruntime.PreSessionMediaStagedMetadata, 0, len(attachments))
	payloads := make([][]byte, 0, len(attachments))
	var total int64
	for i, attachment := range attachments {
		// Bound inline encodings before decoding allocates a second copy.
		if int64(len(attachment.Data))+int64(len(attachment.URL)) > pebblestore.SessionMediaDefaultMaxBytes*4/3+4096 {
			return empty, nil, fmt.Errorf("task attachment %d encoded byte limit exceeded", i+1)
		}
		if attachment.URL != "" && attachment.Data != "" {
			return empty, nil, fmt.Errorf("task attachment %d has conflicting sources", i+1)
		}
		payload, declared, err := s.resolveProjectUploadBytes(ctx, p, attachment)
		if err != nil {
			return empty, nil, fmt.Errorf("task attachment %d: %w", i+1, err)
		}
		detected := pebblestore.DetectSessionMediaMIME(payload)
		if parsed, _, err := mime.ParseMediaType(declared); err == nil {
			declared = parsed
		}
		if declared == "" || declared == "application/octet-stream" {
			declared = detected
		}
		if attachment.MediaType != "" {
			claimed, _, err := mime.ParseMediaType(attachment.MediaType)
			if err != nil || claimed != detected {
				return empty, nil, fmt.Errorf("task attachment %d MIME declaration does not match its bytes", i+1)
			}
		}
		kind := routedSessionModality(detected)
		if attachment.Kind != "" && attachment.Kind != kind && !(attachment.Kind == "doc" && kind == "document") {
			return empty, nil, fmt.Errorf("task attachment %d kind does not match its bytes", i+1)
		}
		sum := sha256.Sum256(payload)
		digest := hex.EncodeToString(sum[:])
		if attachment.DigestSHA256 != "" && attachment.DigestSHA256 != digest || attachment.SizeBytes != 0 && attachment.SizeBytes != int64(len(payload)) {
			return empty, nil, fmt.Errorf("task attachment %d immutable reference mismatch", i+1)
		}
		total += int64(len(payload))
		if int64(len(payload)) > pebblestore.SessionMediaDefaultMaxBytes || total > pebblestore.SessionMediaDefaultQuotaBytes {
			return empty, nil, errors.New("task attachment byte limit exceeded")
		}
		fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(attachment.Filename)), ".")
		staged = append(staged, runruntime.PreSessionMediaStagedMetadata{StagingID: fmt.Sprintf("task-attachment-%d", i), AccountScopeID: p.AccountScopeID, Modality: kind, DeclaredMIMEType: declared, DetectedMIMEType: detected, FileType: fileType, Size: int64(len(payload)), DigestSHA256: digest})
		payloads = append(payloads, payload)
	}
	plan, err := runruntime.PreparePreSessionMediaBindings(runruntime.PreSessionMediaBindingInput{AccountScopeID: p.AccountScopeID, SessionID: candidate.ID, WorkspaceScope: candidate.WorkspacePath, Contract: contract, Staged: staged})
	if err != nil {
		return empty, nil, fmt.Errorf("task attachments unsupported by selected model %s/%s: %w", contract.ProviderID, contract.Model, err)
	}
	return plan, payloads, nil
}

func (s *Server) retainProjectTaskAttachments(plan runruntime.PreSessionMediaBindingPlan, payloads [][]byte) (refs []pebblestore.SessionMediaReference, resultErr error) {
	if len(plan.Bindings) != len(payloads) {
		return nil, errors.New("task attachment payload count mismatch")
	}
	var created []string
	defer func() {
		if resultErr != nil {
			for _, id := range created {
				_, err := s.sessions.Store().DeleteUnreferencedSessionMediaAsset(plan.AccountScopeID, plan.SessionID, id)
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
	refs = make([]pebblestore.SessionMediaReference, 0, len(plan.Bindings))
	for i, binding := range plan.Bindings {
		asset, replayed, err := s.sessions.PutSessionMediaAsset(pebblestore.PutSessionMediaAssetInput{AccountScopeID: plan.AccountScopeID, SessionID: plan.SessionID, Modality: binding.Metadata.Modality, DeclaredMIMEType: binding.Metadata.DetectedMIMEType, FileType: binding.Metadata.FileType, ContractHash: plan.ContractHash, ProviderID: plan.ProviderID, Model: plan.Model, Reader: bytes.NewReader(payloads[i])})
		if err != nil {
			return nil, fmt.Errorf("retain task attachment %d: %w", i+1, err)
		}
		if !replayed {
			created = append(created, asset.ID)
		}
		ref := binding.Reference
		ref.AssetID, ref.FileType = asset.ID, asset.FileType
		refs = append(refs, ref)
	}
	return refs, nil
}
