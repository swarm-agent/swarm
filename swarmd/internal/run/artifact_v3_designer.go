package run

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const artifactV3PolicyRevision = "artifact-v3-managed-designer-v1"

// Native task handoffs must not disguise Git identities as legacy variants.
// A pending/failed slot names its candidate, never the base as a ready revision.
func taskArtifactV3Reference(grant tool.ArtifactV3AuthorGrant, commit string, seq uint64, status string) *taskArtifactReference {
	return &taskArtifactReference{
		SessionID: grant.OwnerSessionID, ArtifactID: grant.ArtifactID,
		CommitOID: commit, ProjectionSeq: seq, TurnID: grant.TurnID,
		CandidateID: grant.CandidateID, Status: status,
	}
}

func artifactV3TargetPartIDs(spec taskLaunchSpec) ([]string, error) {
	var ids []string
	parseTarget := parseTaskSwarmSectionTarget
	parseTargets := parseTaskSwarmSectionTargets
	if spec.ArtifactV3Source != nil {
		parseTarget = parseTaskArtifactV3TargetHint
		parseTargets = parseTaskArtifactV3TargetHints
	}
	target, err := parseTarget(spec.SourceArguments["section_target"])
	if err != nil {
		return nil, err
	}
	if target != nil {
		ids = append(ids, strings.TrimSpace(target.ID))
	}
	var targets []*taskSwarmSectionTarget
	if raw := spec.SourceArguments["section_targets"]; raw != nil {
		targets, err = parseTargets(raw)
		if err != nil {
			return nil, err
		}
	}
	for _, target := range targets {
		if target != nil {
			ids = append(ids, strings.TrimSpace(target.ID))
		}
	}
	return uniqueNonEmptyStrings(ids), nil
}

func equalStringSet(left, right []string) bool {
	left = uniqueNonEmptyStrings(left)
	right = uniqueNonEmptyStrings(right)
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]bool, len(left))
	for _, value := range left {
		seen[value] = true
	}
	for _, value := range right {
		if !seen[value] {
			return false
		}
	}
	return true
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// allocateManagedDesignerArtifactV3 creates a fresh whole-project turn or
// authenticates an exact source artifact/head for a focused follow-up. It does
// not allocate V1/V2 collections, variants, Parts, or composition revisions.
func (s *Service) allocateManagedDesignerArtifactV3(ctx context.Context, parent pebblestore.SessionSnapshot, taskCallID string, specs []taskLaunchSpec) ([]*tool.ArtifactV3AuthorRunContext, error) {
	contexts := make([]*tool.ArtifactV3AuthorRunContext, len(specs))
	managed := false
	for _, spec := range specs {
		managed = managed || (agentruntime.IsDesignerAgentName(spec.RequestedSubagentType) && strings.TrimSpace(spec.OutputMode) == taskOutputModeManaged)
	}
	if !managed {
		return contexts, nil
	}
	if s == nil || s.tools == nil || s.tools.ArtifactV3AuthorService() == nil {
		return nil, errors.New("managed Designer Artifact V3 author service is unavailable")
	}
	requests := make([]tool.ArtifactV3PrepareTurnRequest, len(specs))
	waveSeed := parent.ID + "\x00" + taskCallID
	managedCount := 0
	for _, spec := range specs {
		if agentruntime.IsDesignerAgentName(spec.RequestedSubagentType) && strings.TrimSpace(spec.OutputMode) == taskOutputModeManaged {
			managedCount++
			waveSeed += "\x00" + mapString(spec.SourceArguments, "program_id") + ":" + mapString(spec.SourceArguments, "program_job_id")
		}
	}
	waveID := fmt.Sprintf("wave-%x", sha256.Sum256([]byte(waveSeed)))
	memberIndex := 0
	for index, spec := range specs {
		if !agentruntime.IsDesignerAgentName(spec.RequestedSubagentType) || strings.TrimSpace(spec.OutputMode) != taskOutputModeManaged {
			continue
		}
		if spec.ProgramArtifactSource != nil {
			if spec.ArtifactV3Source != nil {
				return nil, errors.New("program artifact dependency conflicts with explicit source")
			}
			spec.ArtifactV3Source = cloneTaskArtifactV3Source(spec.ProgramArtifactSource)
		}
		targetPartIDs, err := artifactV3TargetPartIDs(spec)
		if err != nil {
			return nil, fmt.Errorf("parse Artifact V3 target Parts: %w", err)
		}
		artifactCallID := strings.TrimSpace(taskCallID)
		if programID := mapString(spec.SourceArguments, "program_id"); programID != "" {
			jobID := mapString(spec.SourceArguments, "program_job_id")
			if jobID == "" {
				return nil, errors.New("managed Designer program job identity missing")
			}
			// Candidate indices are cohort-local; stable program/job identity avoids
			// accidentally reusing a previous cohort's genesis or candidate slot.
			artifactCallID += ":program:" + programID + ":job:" + jobID
		}
		memberIndex++
		request := tool.ArtifactV3PrepareTurnRequest{
			GenerationWaveID: waveID, GenerationIndex: memberIndex, GenerationCount: managedCount,
			AnimationProfile:   spec.AnimationProfile,
			OutputRequirements: spec.OutputRequirements,
			AccountScopeID:     parent.AccountScopeID,
			UserID:             parent.UserID,
			OwnerSessionID:     parent.ID,
			TaskCallID:         artifactCallID,
			Prompt:             strings.TrimSpace(spec.MetaPrompt),
			PolicyRevision:     artifactV3PolicyRevision,
			CandidateIndex:     index + 1,
			Initial:            spec.ArtifactV3Source == nil && spec.SourceArtifact == nil,
			TargetPartIDs:      targetPartIDs,
			ExpiresAt:          time.Now().Add(2 * time.Hour).UnixMilli(),
		}
		if raw, exists := spec.SourceArguments["scene_contract"]; exists {
			contract, err := tool.ParseArtifactV3SceneContract(raw)
			if err != nil || spec.AnimationProfile == nil {
				return nil, errors.New("invalid native scene_contract; requires animation profile and ordered complete scenes")
			}
			request.SceneContract = contract
		}
		if spec.ArtifactV3Source != nil {
			if spec.ArtifactV3Source.SessionID != parent.ID {
				return nil, errors.New("managed Designer Artifact V3 source is not owned by the parent session")
			}
			if spec.ArtifactV3Source.ProjectionSeq == 0 {
				return nil, errors.New("managed Designer Artifact V3 source is missing its projection sequence")
			}
			request.RevisionIntent = spec.ArtifactV3Source.RevisionIntent
			request.Initial = false
			request.ArtifactID = strings.TrimSpace(spec.ArtifactV3Source.ArtifactID)
			request.BaseCommitOID = strings.TrimSpace(spec.ArtifactV3Source.CommitOID)
			request.ProjectionSeq = spec.ArtifactV3Source.ProjectionSeq
			combinedTargets := append([]string(nil), request.TargetPartIDs...)
			combinedTargets = append(combinedTargets, spec.ArtifactV3Source.TargetPartIDs...)
			request.TargetPartIDs = uniqueNonEmptyStrings(combinedTargets)
		} else if spec.SourceArtifact != nil {
			return nil, errors.New("managed Designer Artifact V3 follow-up requires artifact_v3_source; legacy source_artifact cannot seed native Git history")
		}
		if !request.Initial && request.RevisionIntent == "" {
			request.RevisionIntent = pebblestore.ArtifactV3RevisionWholeProject
			if len(request.TargetPartIDs) > 0 {
				request.RevisionIntent = pebblestore.ArtifactV3RevisionFocusedParts
			}
		}
		if err := pebblestore.ValidateArtifactV3RevisionIntent(request.RevisionIntent, request.TargetPartIDs); err != nil {
			return nil, err
		}
		// Independent initial designs have no common committed base; allocate
		// distinct genesis artifacts rather than racing one repository genesis.
		if request.Initial {
			request.TaskCallID += fmt.Sprintf(":initial:%d", index+1)
		}
		requests[index] = request
	}
	// Validate every launch before allocating any capability or starting children.
	for index, request := range requests {
		if request.OwnerSessionID == "" {
			continue
		}
		grant, err := s.tools.ArtifactV3AuthorService().PrepareTurn(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("prepare managed Designer Artifact V3 turn %d: %w", index+1, err)
		}
		if grant.Initial != request.Initial || grant.OwnerSessionID != parent.ID || grant.PolicyRevision != request.PolicyRevision || (!request.Initial && (grant.ArtifactID != request.ArtifactID || grant.BaseCommitOID != request.BaseCommitOID)) {
			return nil, errors.New("managed Designer Artifact V3 coordinator returned a mismatched source binding")
		}
		if !equalStringSet(grant.TargetPartIDs, request.TargetPartIDs) {
			return nil, errors.New("managed Designer Artifact V3 coordinator returned mismatched target Parts")
		}
		if len(grant.AllowedActions) == 0 {
			return nil, errors.New("managed Designer Artifact V3 coordinator returned no author actions")
		}
		specs[index].AnimationProfile = cloneTaskAnimationProfile(grant.AnimationProfile)
		specs[index].OutputRequirements = cloneTaskOutputRequirements(grant.OutputRequirements)
		contexts[index] = &tool.ArtifactV3AuthorRunContext{Grant: grant}
	}
	return contexts, nil
}
