package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type PreparedPlanSave struct {
	Plan             pebblestore.SessionPlanSnapshot
	ArchivedRevision *pebblestore.SessionPlanSnapshot
	Activate         bool
	EventPayload     json.RawMessage
}

// CommitPreparedPlanSave commits every durable component of a V3 plan save in
// the caller-supplied canonical session mutation batch.
func (s *Service) CommitPreparedPlanSave(prepared PreparedPlanSave, applySessionMutation func(SessionMutationInput) (SessionMutationResult, error)) (SessionMutationResult, error) {
	if applySessionMutation == nil {
		return SessionMutationResult{}, errors.New("canonical V3 plan save mutation callback is required")
	}
	plan := prepared.Plan
	if strings.TrimSpace(plan.SessionID) == "" || strings.TrimSpace(plan.ID) == "" {
		return SessionMutationResult{}, errors.New("prepared plan save is missing session or plan id")
	}
	clientRequestID := fmt.Sprintf("plan-save:%s:%s:v%d", strings.TrimSpace(plan.SessionID), strings.TrimSpace(plan.ID), plan.Version)
	sum := sha256.Sum256([]byte(strings.TrimSpace(plan.SessionID) + "\x00" + strings.TrimSpace(plan.ID) + "\x00" + fmt.Sprintf("%d", plan.Version) + "\x00" + string(prepared.EventPayload)))
	payloadHash := hex.EncodeToString(sum[:])
	result, err := applySessionMutation(SessionMutationInput{
		SessionID:       plan.SessionID,
		UserID:          plan.UserID,
		AccountScopeID:  plan.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            SessionMutationSavePlan,
		EventType:       "session.plan.saved",
		EventPayload:    append(json.RawMessage(nil), prepared.EventPayload...),
		PlanSave: &pebblestore.V3PlanSaveMutation{
			Plan:                  plan,
			ArchivedRevision:      prepared.ArchivedRevision,
			Activate:              prepared.Activate,
			ExpectedParentVersion: plan.ParentRevision,
		},
		NowUnixMs: plan.UpdatedAt,
	})
	if err != nil {
		return SessionMutationResult{}, err
	}
	if result.Plan == nil {
		return SessionMutationResult{}, errors.New("V3 plan save mutation did not return committed plan")
	}
	return result, nil
}

func (s *Service) PreparePlanSaveWithMetadata(sessionID, planID, title, plan, status, approvalState string, activate bool, metadata PlanSaveMetadata) (PreparedPlanSave, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	title = strings.TrimSpace(title)
	plan = strings.TrimSpace(plan)
	status = strings.ToLower(strings.TrimSpace(status))
	approvalState = strings.ToLower(strings.TrimSpace(approvalState))
	metadata.UpdateSummary = strings.TrimSpace(metadata.UpdateSummary)
	metadata.UpdateScope = strings.TrimSpace(metadata.UpdateScope)
	metadata.UpdateKind = strings.TrimSpace(metadata.UpdateKind)
	metadata.RevisionKind = classifyPlanRevisionKind(metadata)
	if sessionID == "" {
		return PreparedPlanSave{}, errors.New("session id is required")
	}
	if title == "" {
		if metadata.Document != nil && strings.TrimSpace(metadata.Document.Title) != "" {
			title = strings.TrimSpace(metadata.Document.Title)
		} else {
			title = "Plan"
		}
	}
	if status == "" {
		status = "draft"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok, err := s.store.GetSession(sessionID)
	if err != nil {
		return PreparedPlanSave{}, err
	}
	if !ok {
		return PreparedPlanSave{}, fmt.Errorf("session %q not found", sessionID)
	}
	now := time.Now().UnixMilli()
	if planID == "" {
		if metadata.Document != nil && strings.TrimSpace(metadata.Document.ID) != "" {
			planID = strings.TrimSpace(metadata.Document.ID)
		} else {
			planID = s.newPlanID(now)
		}
	}
	existing, found, err := s.store.GetPlan(sessionID, planID)
	if err != nil {
		return PreparedPlanSave{}, err
	}
	record := pebblestore.SessionPlanSnapshot{ID: planID, SessionID: sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Title: title, Plan: plan, Status: status, ApprovalState: approvalState, CreatedAt: now, UpdatedAt: now, UpdateSummary: metadata.UpdateSummary, UpdateScope: metadata.UpdateScope, UpdateKind: metadata.UpdateKind, RevisionKind: metadata.RevisionKind, RestoredFromVersion: metadata.RestoredFromVersion, Checkpoint: metadata.Checkpoint, Version: 1}
	var archived *pebblestore.SessionPlanSnapshot
	if found {
		if plan == "" && metadata.Document != nil {
			record.Plan = existing.Plan
		}
		record.CreatedAt = existing.CreatedAt
		record.Document, err = NormalizePlanDocumentForSave(planID, title, metadata.Document, existing.Document)
		if err != nil {
			return PreparedPlanSave{}, err
		}
		if record.UserID == "" {
			record.UserID = existing.UserID
		}
		if record.AccountScopeID == "" {
			record.AccountScopeID = existing.AccountScopeID
		}
		record.PriorTitle = existing.Title
		record.PriorPlan = existing.Plan
		record.DiffLines = BuildPlanDiffLines(existing.Plan, plan)
		if existing.Version <= 0 {
			existing.Version = 1
		}
		record.Version = existing.Version + 1
		record.ParentRevision = existing.Version
		existing.Active = false
		archived = &existing
	} else {
		record.Document, err = NormalizePlanDocumentForSave(planID, title, metadata.Document, nil)
		if err != nil {
			return PreparedPlanSave{}, err
		}
	}
	if record.Document != nil {
		record.Document.RevisionID = fmt.Sprintf("%s:v%d", planID, record.Version)
		if s.store != nil {
			if err := s.authenticatePlanDocumentArtifacts(record.AccountScopeID, sessionID, record.Document); err != nil {
				return PreparedPlanSave{}, err
			}
		}
	}
	record.Active = activate
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "plan_id": planID, "plan_title": record.Title, "plan_status": record.Status, "plan_approval_state": record.ApprovalState, "activate": activate, "has_active_plan": activate, "active_plan": record, "updated_at": now, "updated": found, "version": record.Version, "parent_revision": record.ParentRevision, "update_summary": record.UpdateSummary, "update_scope": record.UpdateScope, "update_kind": record.UpdateKind, "revision_kind": record.RevisionKind, "restored_from_version": record.RestoredFromVersion, "checkpoint": record.Checkpoint})
	if err != nil {
		return PreparedPlanSave{}, err
	}
	return PreparedPlanSave{Plan: record, ArchivedRevision: archived, Activate: activate, EventPayload: payload}, nil
}

func (s *Service) PreparePlanActivation(sessionID, planID string) (PreparedPlanSave, error) {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	if sessionID == "" {
		return PreparedPlanSave{}, errors.New("session id is required")
	}
	if planID == "" {
		return PreparedPlanSave{}, errors.New("plan id is required")
	}
	plan, ok, err := s.GetPlan(sessionID, planID)
	if err != nil {
		return PreparedPlanSave{}, err
	}
	if !ok {
		return PreparedPlanSave{}, fmt.Errorf("plan %q not found", planID)
	}
	plan.Active = true
	plan.UpdatedAt = time.Now().UnixMilli()
	payload, err := json.Marshal(map[string]any{"session_id": sessionID, "plan_id": planID, "updated_at": plan.UpdatedAt})
	if err != nil {
		return PreparedPlanSave{}, err
	}
	return PreparedPlanSave{Plan: plan, Activate: true, EventPayload: payload}, nil
}

func (s *Service) PreparePlanPatch(sessionID string, options PlanPatchOptions) (PreparedPlanSave, error) {
	planID := strings.TrimSpace(options.PlanID)
	if options.Patch.IsZero() && options.Document == nil && options.DocumentPatch == nil {
		return PreparedPlanSave{}, errors.New("plan patch requires at least one edit field or document/document_patch")
	}
	var existing pebblestore.SessionPlanSnapshot
	var ok bool
	var err error
	if planID == "" || strings.EqualFold(planID, "active") {
		existing, ok, err = s.GetActivePlan(sessionID)
	} else {
		existing, ok, err = s.GetPlan(sessionID, planID)
	}
	if err != nil {
		return PreparedPlanSave{}, err
	}
	if !ok {
		return PreparedPlanSave{}, fmt.Errorf("plan %q not found", planID)
	}
	planID = existing.ID
	patchedPlan := existing.Plan
	if !options.Patch.IsZero() {
		patchedPlan, err = ApplyPlanPatch(existing.Plan, options.Patch)
		if err != nil {
			return PreparedPlanSave{}, err
		}
	}
	if patchedPlan == existing.Plan && options.Document == nil && options.DocumentPatch == nil {
		return PreparedPlanSave{}, errors.New("plan patch produced no changes")
	}
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = existing.Title
	}
	status := strings.TrimSpace(options.Status)
	if status == "" {
		status = existing.Status
	}
	approval := strings.TrimSpace(options.ApprovalState)
	if approval == "" {
		approval = existing.ApprovalState
	}
	activate := true
	if options.Activate != nil {
		activate = *options.Activate
	}
	metadata := options.Metadata
	if options.DocumentPatch != nil {
		if metadata.RevisionKind == "" {
			metadata.RevisionKind = classifyPlanDocumentPatchRevisionKind(*options.DocumentPatch)
		}
		metadata.Document, err = ApplyPlanDocumentPatch(planID, title, existing.Document, *options.DocumentPatch)
		if err != nil {
			return PreparedPlanSave{}, err
		}
	} else if options.Document != nil {
		metadata.Document = options.Document
	}
	return s.PreparePlanSaveWithMetadata(sessionID, planID, title, patchedPlan, status, approval, activate, metadata)
}

func (s *Service) authenticatePlanDocumentArtifacts(accountScopeID, sessionID string, doc *pebblestore.SessionPlanDocument) error {
	if s == nil || s.store == nil || doc == nil {
		return nil
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	if err := s.authenticatePlanArtifactList(accountScopeID, sessionID, "artifacts", doc.Artifacts); err != nil {
		return err
	}
	for i := range doc.Checkpoints {
		if err := s.authenticatePlanArtifactList(accountScopeID, sessionID, fmt.Sprintf("checkpoints[%d].artifacts", i), doc.Checkpoints[i].Artifacts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) authenticatePlanArtifactList(accountScopeID, sessionID, field string, artifacts []pebblestore.SessionPlanArtifactReference) error {
	for i := range artifacts {
		ref := &artifacts[i]
		if !isManagedPlanArtifact(*ref) {
			continue
		}
		prefix := fmt.Sprintf("%s[%d]", field, i)
		artSessionID := strings.TrimSpace(ref.SessionID)
		collectionID := strings.TrimSpace(ref.CollectionID)
		variantID := strings.TrimSpace(ref.VariantID)
		eventSeq := ref.EventSeq
		if artSessionID == "" {
			return fmt.Errorf("%s: managed artifact session_id is required", prefix)
		}
		if collectionID == "" {
			return fmt.Errorf("%s: managed artifact collection_id is required", prefix)
		}
		if variantID == "" {
			return fmt.Errorf("%s: managed artifact variant_id is required", prefix)
		}
		if eventSeq == 0 {
			return fmt.Errorf("%s: managed artifact event_seq is required", prefix)
		}
		variant, found, err := s.store.GetSessionArtifactVariantByID(accountScopeID, artSessionID, variantID)
		if err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
		if !found {
			variant, found, err = s.store.GetSessionArtifactVariant(accountScopeID, artSessionID, collectionID, variantID)
			if err != nil {
				return fmt.Errorf("%s: %w", prefix, err)
			}
		}
		if !found {
			return fmt.Errorf("%s: managed artifact variant %q not found in session %q", prefix, variantID, artSessionID)
		}
		if strings.TrimSpace(variant.CollectionID) != collectionID {
			return fmt.Errorf("%s: managed artifact variant %q collection %q does not match store collection %q", prefix, variantID, collectionID, variant.CollectionID)
		}
		if strings.TrimSpace(variant.AccountScopeID) != "" && accountScopeID != "" && strings.TrimSpace(variant.AccountScopeID) != accountScopeID {
			return fmt.Errorf("%s: managed artifact variant %q does not belong to account scope", prefix, variantID)
		}
		if artSessionID != sessionID && strings.TrimSpace(variant.Lineage.ParentSessionID) != sessionID && strings.TrimSpace(variant.SessionID) != sessionID {
			return fmt.Errorf("%s: managed artifact variant %q session %q does not belong to session %q or its lineage", prefix, variantID, artSessionID, sessionID)
		}
		if variant.Status != pebblestore.SessionArtifactStatusReady {
			return fmt.Errorf("%s: managed artifact variant %q is not ready (status: %s)", prefix, variantID, variant.Status)
		}
		if variant.EventSeq != eventSeq {
			return fmt.Errorf("%s: managed artifact variant %q event sequence %d does not match ready variant event sequence %d", prefix, variantID, eventSeq, variant.EventSeq)
		}
		if ref.Label == "" {
			if strings.TrimSpace(variant.Presentation.Label) != "" {
				ref.Label = strings.TrimSpace(variant.Presentation.Label)
			} else if strings.TrimSpace(variant.Filename) != "" {
				ref.Label = strings.TrimSpace(variant.Filename)
			}
		}
		if ref.Description == "" && strings.TrimSpace(variant.Presentation.Description) != "" {
			ref.Description = strings.TrimSpace(variant.Presentation.Description)
		}
		if ref.MediaType == "" && strings.TrimSpace(variant.MediaType) != "" {
			ref.MediaType = strings.TrimSpace(variant.MediaType)
		}
	}
	return nil
}
