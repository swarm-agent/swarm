package session

import (
	"errors"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) ListArtifactV2WorkingForSession(accountScopeID, sessionID string, limit int) ([]pebblestore.ArtifactV2WorkingArtifact, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2WorkingForSession(accountScopeID, sessionID, limit)
}

func (s *Service) GetArtifactV2Working(accountScopeID, artifactID string) (pebblestore.ArtifactV2WorkingArtifact, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2WorkingArtifact{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2Working(accountScopeID, artifactID)
}
func (s *Service) ListArtifactV2Parts(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2Part, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2Parts(accountScopeID, artifactID, limit)
}
func (s *Service) GetArtifactV2Part(accountScopeID, artifactID, partID string) (pebblestore.ArtifactV2Part, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2Part{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2Part(accountScopeID, artifactID, partID)
}
func (s *Service) GetArtifactV2PartRevision(accountScopeID, artifactID, partID, revisionID string) (pebblestore.ArtifactV2PartRevision, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2PartRevision{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2PartRevision(accountScopeID, artifactID, partID, revisionID)
}
func (s *Service) ListArtifactV2PartRevisions(accountScopeID, artifactID, partID string, limit int) ([]pebblestore.ArtifactV2PartRevision, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2PartRevisions(accountScopeID, artifactID, partID, limit)
}
func (s *Service) ListArtifactV2Compositions(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2Composition, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2Compositions(accountScopeID, artifactID, limit)
}
func (s *Service) GetArtifactV2Composition(accountScopeID, artifactID, compositionID string) (pebblestore.ArtifactV2Composition, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2Composition{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2Composition(accountScopeID, artifactID, compositionID)
}
func (s *Service) ListArtifactV2Builds(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2BuildResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2Builds(accountScopeID, artifactID, limit)
}
func (s *Service) GetArtifactV2Build(accountScopeID, artifactID, buildID string) (pebblestore.ArtifactV2BuildResult, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2BuildResult{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2Build(accountScopeID, artifactID, buildID)
}
func (s *Service) ListArtifactV2Validations(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2ValidationResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2Validations(accountScopeID, artifactID, limit)
}
func (s *Service) GetArtifactV2Validation(accountScopeID, artifactID, validationID string) (pebblestore.ArtifactV2ValidationResult, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2ValidationResult{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2Validation(accountScopeID, artifactID, validationID)
}
func (s *Service) ListArtifactV2Derivatives(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2Derivative, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2Derivatives(accountScopeID, artifactID, limit)
}
func (s *Service) GetArtifactV2Derivative(accountScopeID, artifactID, derivativeID string) (pebblestore.ArtifactV2Derivative, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2Derivative{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2Derivative(accountScopeID, artifactID, derivativeID)
}
func (s *Service) ListArtifactV2Iterations(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2IterationRound, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2Iterations(accountScopeID, artifactID, limit)
}
func (s *Service) GetArtifactV2Iteration(accountScopeID, artifactID, iterationID string) (pebblestore.ArtifactV2IterationRound, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2IterationRound{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2Iteration(accountScopeID, artifactID, iterationID)
}
func (s *Service) ListArtifactV2PublishedHeads(accountScopeID, artifactID string, limit int) ([]pebblestore.ArtifactV2PublishedHead, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListArtifactV2PublishedHeads(accountScopeID, artifactID, limit)
}
func (s *Service) GetArtifactV2PublishedHead(accountScopeID, artifactID, publishedHeadID string) (pebblestore.ArtifactV2PublishedHead, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.ArtifactV2PublishedHead{}, false, errors.New("session store is not configured")
	}
	return s.store.GetArtifactV2PublishedHead(accountScopeID, artifactID, publishedHeadID)
}
