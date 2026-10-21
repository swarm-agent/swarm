package pebblestore

import (
	"strings"
)

type strictHistoricalPlanReader struct {
	projects  map[string]VideoProjectSnapshot
	revisions map[string]VideoProjectRevisionSnapshot
	proposals map[string]VideoEditProposalSnapshot
	listErr   error
}

func (r *strictHistoricalPlanReader) GetVideoProject(accountScopeID, sessionID, projectID string) (VideoProjectSnapshot, bool, error) {
	p, ok := r.projects[sessionID+":"+projectID]
	if !ok {
		return VideoProjectSnapshot{}, false, nil
	}
	if p.AccountScopeID != accountScopeID {
		return VideoProjectSnapshot{}, false, nil
	}
	return p, true, nil
}

func (r *strictHistoricalPlanReader) GetVideoProjectRevision(accountScopeID, sessionID, projectID, revisionID string) (VideoProjectRevisionSnapshot, bool, error) {
	rev, ok := r.revisions[sessionID+":"+projectID+":"+revisionID]
	if !ok {
		return VideoProjectRevisionSnapshot{}, false, nil
	}
	if rev.AccountScopeID != accountScopeID {
		return VideoProjectRevisionSnapshot{}, false, nil
	}
	return rev, true, nil
}

func (r *strictHistoricalPlanReader) GetVideoEditProposal(accountScopeID, sessionID, projectID, proposalID string) (VideoEditProposalSnapshot, bool, error) {
	prop, ok := r.proposals[sessionID+":"+projectID+":"+proposalID]
	if !ok {
		return VideoEditProposalSnapshot{}, false, nil
	}
	if prop.AccountScopeID != accountScopeID {
		return VideoEditProposalSnapshot{}, false, nil
	}
	return prop, true, nil
}

func (r *strictHistoricalPlanReader) ListVideoEditProposals(accountScopeID, sessionID, projectID string, limit int) ([]VideoEditProposalSnapshot, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	var list []VideoEditProposalSnapshot
	prefix := sessionID + ":" + projectID + ":"
	for k, prop := range r.proposals {
		if strings.HasPrefix(k, prefix) && prop.AccountScopeID == accountScopeID {
			list = append(list, prop)
		}
	}
	return list, nil
}
