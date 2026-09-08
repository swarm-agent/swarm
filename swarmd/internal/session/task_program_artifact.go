package session

import (
	"errors"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ValidateTaskProgramArtifact authenticates native candidate evidence rather than
// reconstructing a legacy artifact destination from a job name.
func (s *Service) ValidateTaskProgramArtifact(parent pebblestore.SessionSnapshot, childID, callID, programID, jobID string, ref pebblestore.TaskProgramArtifactRef) error {
	if s == nil || s.store == nil {
		return errors.New("native artifact authority unavailable")
	}
	child, ok, err := s.GetSession(childID)
	if err != nil {
		return err
	}
	if !ok || child.AccountScopeID != parent.AccountScopeID || child.UserID != parent.UserID {
		return errors.New("native artifact producer lineage mismatch")
	}
	fields := map[string]string{"parent_session_id": parent.ID, "parent_task_call_id": callID, "task_program_id": programID, "task_program_job_id": jobID, "artifact_v3_owner_session_id": parent.ID, "artifact_v3_artifact_id": ref.ArtifactID, "artifact_v3_turn_id": ref.TurnID, "artifact_v3_candidate_id": ref.CandidateID}
	for key, want := range fields {
		if got, _ := child.Metadata[key].(string); want == "" || got != want {
			return errors.New("native artifact producer lineage mismatch: " + key)
		}
	}
	repo, ok, err := s.store.GetArtifactV3Repository(parent.AccountScopeID, parent.UserID, ref.ArtifactID)
	if err != nil {
		return err
	}
	if !ok || repo.OwnerSessionID != parent.ID || ref.SessionID != parent.ID {
		return errors.New("native artifact owner mismatch")
	}
	revision, ok, err := s.store.GetArtifactV3Revision(parent.AccountScopeID, parent.UserID, ref.ArtifactID, ref.CommitOID)
	if err != nil {
		return err
	}
	if !ok || revision.OwnerSessionID != parent.ID || revision.EventSeq != ref.ProjectionSeq || ref.ProjectionSeq == 0 {
		return errors.New("native artifact revision mismatch")
	}
	evidenceRows := []pebblestore.ArtifactV3EvidenceProjection{revision.Build, revision.Preview}
	if initial, _ := child.Metadata["artifact_v3_initial"].(bool); initial {
		if len(revision.ParentCommitOIDs) != 0 {
			return errors.New("native initial artifact is not its genesis revision")
		}
	} else {
		candidate, ok, err := s.store.GetArtifactV3Candidate(parent.AccountScopeID, parent.UserID, ref.ArtifactID, ref.TurnID, ref.CandidateID)
		if err != nil {
			return err
		}
		if !ok || candidate.OwnerSessionID != parent.ID || candidate.CommitOID != ref.CommitOID || candidate.Status != "ready" || candidate.FailureCode != "" {
			return errors.New("native artifact candidate is not ready")
		}
		evidenceRows = append(evidenceRows, candidate.Build, candidate.Preview)
	}
	for _, evidence := range evidenceRows {
		if evidence.Status != "succeeded" || evidence.CommitOID != ref.CommitOID || evidence.DigestSHA256 == "" {
			return errors.New("native artifact evidence is not validated")
		}
	}
	return nil
}
