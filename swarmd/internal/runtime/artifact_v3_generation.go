package runtime

import (
	"context"
	"encoding/json"
	"swarm/packages/swarmd/internal/api"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Detail hydration returns bounded exact sibling slots, not recursive artifacts.
// List hydration carries local memberships; navigation never invokes selection.
func (a *artifactV3RuntimeAdapter) generationGroups(ctx context.Context, principal api.ArtifactV3Principal, repository pebblestore.ArtifactV3RepositoryProjection) ([]api.ArtifactV3GenerationGroup, error) {
	groups := make([]api.ArtifactV3GenerationGroup, 0)
	seen := map[string]bool{}
	total := 0
	for _, local := range repository.Generations {
		if seen[local.WaveID] {
			continue
		}
		seen[local.WaveID] = true
		members, err := a.sessions.GetArtifactV3Generation(principal.AccountScopeID, principal.UserID, repository.OwnerSessionID, repository.ArtifactID, local.WaveID)
		if err != nil {
			return nil, err
		}
		total += len(members)
		if total > 4096 {
			return nil, pebblestore.ErrArtifactV3Invalid
		}
		group := api.ArtifactV3GenerationGroup{WaveID: local.WaveID, Count: local.Count, Members: make([]api.ArtifactV3GenerationSibling, 0, len(members))}
		for _, member := range members {
			repo, ok, err := a.sessions.GetArtifactV3Repository(principal.AccountScopeID, principal.UserID, member.ArtifactID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, pebblestore.ErrArtifactV3Integrity
			}
			sibling := api.ArtifactV3GenerationSibling{ArtifactV3GenerationMember: member, Status: "creating", ProjectionSeq: repo.EventSeq}
			candidate, found, err := a.sessions.GetArtifactV3Candidate(principal.AccountScopeID, principal.UserID, member.ArtifactID, member.TurnID, member.CandidateID)
			if err != nil {
				return nil, err
			}
			if found {
				sibling.Status = candidate.Status
				sibling.CommitOID = candidate.CommitOID
			} else {
				matched := false
				for _, draft := range repo.Drafts {
					var grant tool.ArtifactV3AuthorGrant
					if json.Unmarshal(draft.Grant, &grant) != nil {
						return nil, pebblestore.ErrArtifactV3Integrity
					}
					if grant.TurnID == member.TurnID && grant.CandidateID == member.CandidateID {
						sibling.Status = draft.Status
						var state struct{ Finished *tool.ArtifactV3AuthorFinish }
						if len(draft.State) != 0 && json.Unmarshal(draft.State, &state) != nil {
							return nil, pebblestore.ErrArtifactV3Integrity
						}
						if state.Finished != nil {
							sibling.CommitOID = state.Finished.Revision.CommitOID
							if _, ok, err := a.sessions.GetArtifactV3Revision(principal.AccountScopeID, principal.UserID, member.ArtifactID, sibling.CommitOID); err != nil || !ok {
								return nil, pebblestore.ErrArtifactV3Integrity
							}
						}
						matched = true
					}
				}
				if !matched {
					return nil, pebblestore.ErrArtifactV3Integrity
				}
			}
			if sibling.CommitOID != "" {
				revision, err := a.revision(ctx, principal, repo, sibling.CommitOID)
				if err != nil {
					return nil, err
				}
				gitRepo, err := pebblestore.OpenArtifactV3Repository(ctx, a.repositoryRoot, repo.ArtifactID, pebblestore.ArtifactV3Owner{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, SessionID: repo.OwnerSessionID}, a.limits)
				if err != nil {
					return nil, err
				}
				entrypoint, err := gitRepo.ReadFile(ctx, sibling.CommitOID, revision.Manifest.Entrypoint)
				if err != nil {
					return nil, err
				}
				sibling.Label = artifactV3DocumentTitle(entrypoint)
			}
			group.Members = append(group.Members, sibling)
		}
		groups = append(groups, group)
	}
	return groups, nil
}
