package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/artifactv2"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func artifactV2GrantID(parentSessionID, taskCallID string, candidateIndex int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"artifact-v2-designer-grant", strings.TrimSpace(parentSessionID), strings.TrimSpace(taskCallID), fmt.Sprint(candidateIndex)}, "\x00")))
	return "grantv2_" + hex.EncodeToString(sum[:16])
}

func artifactV2PolicyFromLaunch(spec taskLaunchSpec) artifactv2.PolicySnapshot {
	policy := artifactv2.PolicySnapshot{Revision: artifactv2.DefaultPolicyRevision, MaxPartBytes: 8 << 20, MaxParts: 64}
	if req := spec.OutputRequirements; req != nil {
		policy.Width, policy.Height, policy.AspectRatio, policy.Orientation, policy.Preset = req.Width, req.Height, strings.TrimSpace(req.AspectRatio), strings.TrimSpace(req.Orientation), strings.TrimSpace(req.PresetID)
		policy.Revision = strings.TrimSpace(req.RegistryVersion)
		if policy.Revision == "" {
			policy.Revision = artifactv2.DefaultPolicyRevision
		}
	}
	if profile := spec.AnimationProfile; profile != nil {
		policy.AnimationProfile = strings.TrimSpace(profile.ProfileID)
		policy.Revision = strings.TrimSpace(strings.Join([]string{policy.Revision, policy.AnimationProfile, profile.RegistryVersion}, "+"))
	}
	return policy
}

// allocateManagedDesignerArtifactV2 replaces V1 collection/placeholder
// allocation for managed Designers. Image-model launches retain their separate
// image generation authority and never receive this context.
func (s *Service) allocateManagedDesignerArtifactV2(ctx context.Context, parent pebblestore.SessionSnapshot, taskCallID string, specs []taskLaunchSpec) ([]*tool.ArtifactV2AuthorRunContext, *artifactv2.AuthorIterationContext, error) {
	contexts := make([]*tool.ArtifactV2AuthorRunContext, len(specs))
	if s == nil || s.tools == nil || s.tools.ArtifactV2AuthorService() == nil {
		for _, spec := range specs {
			if strings.TrimSpace(spec.OutputMode) == taskOutputModeManaged && agentruntime.IsDesignerAgentName(spec.RequestedSubagentType) {
				return nil, nil, errors.New("managed Designer Artifact V2 author service is unavailable")
			}
		}
		return contexts, nil, nil
	}

	var iteration *artifactv2.AuthorIterationContext
	for _, spec := range specs {
		if spec.ArtifactV2Source == nil {
			continue
		}
		if iteration != nil {
			if iteration.ArtifactID != spec.ArtifactV2Source.ArtifactID || iteration.BaseComposition.ID != spec.ArtifactV2Source.CompositionID || iteration.CandidateCount != len(specs) {
				return nil, nil, errors.New("managed Designer wave contains conflicting Artifact V2 iteration sources")
			}
			continue
		}
		labels := map[string]string{}
		if target, _ := spec.SourceArguments["section_target"].(*taskSwarmSectionTarget); target != nil {
			labels[target.ID] = target.Label
		}
		if values, _ := spec.SourceArguments["section_targets"].([]*taskSwarmSectionTarget); len(values) != 0 {
			for _, target := range values {
				if target != nil {
					labels[target.ID] = target.Label
				}
			}
		}
		targets := make([]artifactv2.AuthorIterationTarget, 0, len(spec.ArtifactV2Source.TargetPartIDs))
		for _, partID := range spec.ArtifactV2Source.TargetPartIDs {
			targets = append(targets, artifactv2.AuthorIterationTarget{PartID: partID, Label: labels[partID]})
		}
		prepared, err := s.tools.ArtifactV2AuthorService().PrepareIteration(ctx, artifactv2.Principal{AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, SessionID: parent.ID, ActorClass: "orchestrator"}, "task-artifact-v2-iteration:"+strings.TrimSpace(taskCallID), spec.ArtifactV2Source.ArtifactID, spec.ArtifactV2Source.WorkingRevision, spec.ArtifactV2Source.CompositionHeadRev, targets, len(specs))
		if err != nil {
			return nil, nil, fmt.Errorf("prepare managed Designer Artifact V2 iteration: %w", err)
		}
		if prepared.BaseComposition.ID != spec.ArtifactV2Source.CompositionID {
			return nil, nil, errors.New("managed Designer Artifact V2 iteration composition is stale")
		}
		iteration = &prepared
	}

	for index, spec := range specs {
		if strings.TrimSpace(spec.OutputMode) != taskOutputModeManaged || !agentruntime.IsDesignerAgentName(spec.RequestedSubagentType) {
			continue
		}
		policy := artifactV2PolicyFromLaunch(spec)
		requestID := fmt.Sprintf("task-artifact-v2:%s:%d", strings.TrimSpace(taskCallID), index+1)
		principal := artifactv2.Principal{AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, SessionID: parent.ID, RunID: requestID, ActorClass: "orchestrator"}
		var working pebblestore.ArtifactV2WorkingArtifact
		var err error
		if iteration != nil {
			working, err = s.tools.ArtifactV2AuthorService().AllocateIterationCandidate(ctx, principal, requestID, strings.TrimSpace(spec.MetaPrompt), *iteration, index+1, policy)
		} else {
			working, err = s.tools.ArtifactV2AuthorService().AllocateWorking(ctx, principal, requestID, "managed_creative", strings.TrimSpace(spec.MetaPrompt), policy)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("allocate managed Designer Artifact V2 working artifact: %w", err)
		}
		grant := artifactv2.AuthorGrant{
			ID: artifactV2GrantID(parent.ID, taskCallID, index+1), ArtifactID: working.ID, OwnerSessionID: parent.ID,
			TaskCallID: strings.TrimSpace(taskCallID), CandidateSlotID: fmt.Sprintf("candidate-%d", index+1),
			AllowedActions: []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}, AllowPartDeclaration: true,
			ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli(), Policy: policy,
		}
		if iteration != nil {
			grant.IterationID = iteration.IterationID
			for _, target := range iteration.Targets {
				grant.DeclaredPartKeys = append(grant.DeclaredPartKeys, target.Key)
			}
		} else if spec.SwarmMode {
			grant.IterationID = taskManagedArtifactID("roundv2", parent.ID, taskCallID, 0)
		}
		contexts[index] = &tool.ArtifactV2AuthorRunContext{Grant: grant}
	}
	return contexts, iteration, nil
}
