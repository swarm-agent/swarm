package pebblestore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/cockroachdb/pebble"
)

// ArtifactV3GenerationMember is immutable server-owned allocation identity.
// A wave connects independent roots without turning them into revisions of one
// another. Exact source-bound members retain their artifact and base commit.
type ArtifactV3GenerationMember struct {
	WaveID              string `json:"wave_id"`
	Index               int    `json:"index"`
	Count               int    `json:"count"`
	ArtifactID          string `json:"artifact_id"`
	TurnID              string `json:"turn_id"`
	CandidateID         string `json:"candidate_id"`
	BaseCommitOID       string `json:"base_commit_oid,omitempty"`
	SourceProjectionSeq uint64 `json:"source_projection_seq,omitempty"`
}

func artifactV3GenerationKey(account, session, wave string, index int) string {
	return fmt.Sprintf("v3/artifact/generation/%s/%s/%s/%04d", keyPart(account), keyPart(session), keyPart(wave), index)
}

// Called under ApplySessionMutation serialization. Only a new draft allocation
// can add membership; publication/recovery inherit the existing public index.
func (s *SessionStore) prepareArtifactV3Generation(input V3SessionMutationInput, current ArtifactV3RepositoryProjection, next *ArtifactV3RepositoryProjection) error {
	next.Generations = append([]ArtifactV3GenerationMember(nil), current.Generations...)
	if input.Kind != V3SessionMutationArtifactV3DraftSaved {
		return nil
	}
	var grant struct {
		Generation                                                     *ArtifactV3GenerationMember
		ArtifactID, OwnerSessionID, TurnID, CandidateID, BaseCommitOID string
		SourceProjectionSeq                                            uint64
	}
	if err := json.Unmarshal(input.ArtifactV3.Draft.Grant, &grant); err != nil {
		return ErrArtifactV3Invalid
	}
	if grant.Generation == nil {
		return nil
	} // Older grants remain ungrouped, never guessed.
	member := *grant.Generation
	if grant.ArtifactID != next.ArtifactID || grant.OwnerSessionID != input.SessionID || grant.TurnID != member.TurnID || grant.CandidateID != member.CandidateID || grant.BaseCommitOID != member.BaseCommitOID || grant.SourceProjectionSeq != member.SourceProjectionSeq {
		return ErrArtifactV3Unauthorized
	}
	if strings.TrimSpace(member.WaveID) == "" || len(member.WaveID) > 128 || member.Index < 1 || member.Count < member.Index || member.Count > 256 || member.ArtifactID != next.ArtifactID || member.TurnID == "" || member.CandidateID == "" || (member.BaseCommitOID != "" && !validGitOID(member.BaseCommitOID)) {
		return ErrArtifactV3Invalid
	}
	for _, prior := range current.Generations {
		if prior.WaveID == member.WaveID && prior.Index == member.Index {
			if !reflect.DeepEqual(prior, member) {
				return ErrArtifactV3Conflict
			}
			return nil
		}
		if prior.TurnID == member.TurnID && prior.CandidateID == member.CandidateID {
			return ErrArtifactV3Conflict
		}
	}
	if member.BaseCommitOID != "" && (current.HeadCommitOID != member.BaseCommitOID || member.SourceProjectionSeq == 0) {
		return ErrArtifactV3Conflict
	}
	members, err := s.listArtifactV3Generation(input.AccountScopeID, input.SessionID, member.WaveID)
	if err != nil {
		return err
	}
	for _, prior := range members {
		if prior.Index == member.Index || prior.Count != member.Count {
			return ErrArtifactV3Conflict
		}
	}
	if len(next.Generations) >= 256 {
		return ErrArtifactV3Invalid
	}
	next.Generations = append(next.Generations, member)
	return nil
}

func (s *SessionStore) listArtifactV3Generation(account, session, wave string) ([]ArtifactV3GenerationMember, error) {
	prefix := fmt.Sprintf("v3/artifact/generation/%s/%s/%s/", keyPart(account), keyPart(session), keyPart(wave))
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	out := make([]ArtifactV3GenerationMember, 0)
	for iter.First(); iter.Valid(); iter.Next() {
		if len(out) >= 256 {
			return nil, ErrArtifactV3Invalid
		}
		var member ArtifactV3GenerationMember
		if json.Unmarshal(iter.Value(), &member) != nil || member.WaveID != wave || string(iter.Key()) != artifactV3GenerationKey(account, session, wave, member.Index) {
			return nil, ErrArtifactV3Integrity
		}
		out = append(out, member)
	}
	return out, iter.Error()
}

// GetArtifactV3Generation authenticates both the anchor and every sibling. The
// index is only a lookup accelerator; repository membership is revalidated.
func (s *SessionStore) GetArtifactV3Generation(account, user, session, artifact, wave string) ([]ArtifactV3GenerationMember, error) {
	anchor, ok, err := s.GetArtifactV3Repository(account, user, artifact)
	if err != nil {
		return nil, err
	}
	if !ok || anchor.OwnerSessionID != session {
		return nil, ErrArtifactV3Unauthorized
	}
	found := false
	for _, member := range anchor.Generations {
		if member.WaveID == wave {
			found = true
		}
	}
	if !found {
		return nil, ErrArtifactV3NotFound
	}
	members, err := s.listArtifactV3Generation(account, session, wave)
	if err != nil {
		return nil, err
	}
	for _, member := range members {
		repo, ok, err := s.GetArtifactV3Repository(account, user, member.ArtifactID)
		if err != nil {
			return nil, err
		}
		if !ok || repo.OwnerSessionID != session {
			return nil, ErrArtifactV3Integrity
		}
		matched := false
		for _, stored := range repo.Generations {
			if reflect.DeepEqual(stored, member) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, ErrArtifactV3Integrity
		}
	}
	for _, local := range anchor.Generations {
		if local.WaveID != wave {
			continue
		}
		matched := false
		for _, member := range members {
			if reflect.DeepEqual(local, member) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, ErrArtifactV3Integrity
		}
	}
	return members, nil
}
