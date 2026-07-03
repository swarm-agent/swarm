package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func managedHostTargetBySwarmID(targets []swarmTarget, targetSwarmID string) *swarmTarget {
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	if targetSwarmID == "" {
		return nil
	}
	for i := range targets {
		if !strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), targetSwarmID) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(targets[i].Relationship), swarmruntime.RelationshipManaged) && !strings.EqualFold(strings.TrimSpace(targets[i].Kind), "manager") && !strings.EqualFold(strings.TrimSpace(targets[i].Relationship), "self") {
			target := targets[i]
			return &target
		}
	}
	return nil
}

func (s *Server) resolveManagedHostSessionTarget(r *http.Request, targetSwarmID string) (*swarmTarget, string, string, int, error) {
	if s == nil || s.swarm == nil {
		return nil, "", "", http.StatusInternalServerError, errors.New("swarm service is not configured")
	}
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	if targetSwarmID == "" {
		return nil, "", "", http.StatusBadRequest, errors.New("target_swarm_id is required")
	}
	targets, _, err := s.swarmTargetsForRequestWithOptions(requestWithSwarmTargetQuery(r, targetSwarmID), true)
	if err != nil {
		return nil, "", "", http.StatusBadRequest, err
	}
	var target *swarmTarget
	for i := range targets {
		if !strings.EqualFold(strings.TrimSpace(targets[i].SwarmID), targetSwarmID) {
			continue
		}
		if target == nil || targets[i].Selectable {
			targetCopy := targets[i]
			target = &targetCopy
		}
		if target.Selectable {
			break
		}
	}
	if target == nil {
		return nil, "", "", http.StatusBadRequest, errors.New("managed host target was not found")
	}
	if !strings.EqualFold(strings.TrimSpace(target.Relationship), swarmruntime.RelationshipManaged) || strings.EqualFold(strings.TrimSpace(target.Kind), "manager") || strings.EqualFold(strings.TrimSpace(target.Relationship), "self") {
		if byID := managedHostTargetBySwarmID(targets, targetSwarmID); byID != nil {
			target = byID
		}
	}
	if !strings.EqualFold(strings.TrimSpace(target.Relationship), swarmruntime.RelationshipManaged) || strings.EqualFold(strings.TrimSpace(target.Kind), "manager") || strings.EqualFold(strings.TrimSpace(target.Relationship), "self") {
		return nil, "", "", http.StatusBadRequest, fmt.Errorf("target must be a managed host (resolved swarm_id=%q name=%q relationship=%q kind=%q)", target.SwarmID, target.Name, target.Relationship, target.Kind)
	}
	if strings.TrimSpace(target.BackendURL) == "" {
		return nil, "", "", http.StatusBadRequest, errors.New("managed host route is missing")
	}
	if !target.Selectable {
		return nil, "", "", http.StatusBadRequest, errors.New("managed host is not selectable")
	}
	ctx, cancel := context.WithTimeout(r.Context(), swarmTargetHealthTimeout)
	defer cancel()
	if !probeSwarmTargetBackend(ctx, target.BackendURL) {
		return nil, "", "", http.StatusBadGateway, errors.New("managed host route is not reachable")
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(r, *target)
	if err != nil {
		return nil, "", "", http.StatusBadRequest, err
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return nil, "", "", http.StatusInternalServerError, err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return nil, "", "", http.StatusInternalServerError, err
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if localSwarmID == "" {
		return nil, "", "", http.StatusInternalServerError, errors.New("local swarm id is not configured")
	}
	return target, localSwarmID, peerToken, http.StatusOK, nil
}

func requestWithSwarmTargetQuery(r *http.Request, targetSwarmID string) *http.Request {
	if r == nil {
		return &http.Request{URL: &url.URL{RawQuery: url.Values{"swarm_id": []string{strings.TrimSpace(targetSwarmID)}}.Encode()}}
	}
	clone := r.Clone(r.Context())
	if clone.URL == nil {
		clone.URL = &url.URL{}
	} else {
		urlCopy := *clone.URL
		clone.URL = &urlCopy
	}
	query := clone.URL.Query()
	query.Set("swarm_id", strings.TrimSpace(targetSwarmID))
	clone.URL.RawQuery = query.Encode()
	return clone
}

func peerManagedWorkspacePrincipal() identity.Principal {
	return identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             "peer-managed-workspace",
		AccountScopeID:     "peer-managed-workspace",
		AccountScopeSource: identity.AccountScopeSourceServerState,
	}
}

func (s *Server) peerManagedWorkspacePrincipalForRequest(r *http.Request) (identity.Principal, bool) {
	if r != nil {
		if principal, ok := s.trustedPairingPrincipalForPeerRequest(r); ok && principal.Valid() {
			return principal, true
		}
		if principal, ok := PrincipalFromRequest(r); ok && principal.Valid() {
			return principal, true
		}
	}
	if s != nil && s.swarmStore != nil {
		if pairing, ok, err := s.swarmStore.GetLocalPairing(); err == nil && ok {
			principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: strings.TrimSpace(pairing.UserID), AccountScopeID: strings.TrimSpace(pairing.AccountScopeID), AccountScopeSource: identity.AccountScopeSourceServerState}
			if principal.Valid() {
				return principal, true
			}
		}
	}
	return peerManagedWorkspacePrincipal(), true
}
