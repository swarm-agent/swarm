package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tailscale"
)

const (
	TailscaleSettingsPath        = "/v1/settings/tailscale"
	TailscaleSettingsApprovePath = "/v1/settings/tailscale/approve"
	TailscaleSettingsRevokePath  = "/v1/settings/tailscale/revoke"
)

type tailscaleServeDetector interface {
	Snapshot(context.Context, tailscale.RefreshMode) (tailscale.Snapshot, error)
	Invalidate()
	EffectiveTarget() string
	RemediationCommand() string
}

type tailscaleSettingsRoute struct {
	tailscale.Route
	Approved bool `json:"approved"`
	Active   bool `json:"active"`
}

type tailscaleSettingsResponse struct {
	State           string                   `json:"state"`
	Approvals       []string                 `json:"approvals"`
	Revision        uint64                   `json:"revision"`
	UpdatedAt       int64                    `json:"updated_at,omitempty"`
	Routes          []tailscaleSettingsRoute `json:"routes"`
	SelfDNSName     string                   `json:"self_dns_name,omitempty"`
	EffectiveTarget string                   `json:"effective_target,omitempty"`
	Remediation     string                   `json:"remediation,omitempty"`
	DetectionError  string                   `json:"detection_error,omitempty"`
}

func (s *Server) SetTailscaleServePolicy(store *pebblestore.TailscaleServeAllowlistStore, detector tailscaleServeDetector) {
	if s == nil {
		return
	}
	s.tailscaleServePolicy = store
	s.tailscaleServeDetector = detector
}

func (s *Server) handleTailscaleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, ok := PrincipalFromRequest(r); !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	response, err := s.tailscaleSettingsStatus(r.Context(), tailscale.RequireFresh)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleTailscaleSettingsApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, ok := PrincipalFromRequest(r); !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var request struct {
		Origin string `json:"origin"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, status, err := s.approveTailscaleOrigin(r.Context(), request.Origin)
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) approveTailscaleOrigin(ctx context.Context, rawOrigin string) (tailscaleSettingsResponse, int, error) {
	if s == nil || s.tailscaleServePolicy == nil || s.tailscaleServeDetector == nil {
		return tailscaleSettingsResponse{}, http.StatusServiceUnavailable, errors.New("tailscale desktop policy is not configured")
	}
	origin, err := tailscale.NormalizeHTTPSOrigin(rawOrigin)
	if err != nil {
		return tailscaleSettingsResponse{}, http.StatusBadRequest, err
	}
	s.tailscaleServeDetector.Invalidate()
	snapshot, err := s.tailscaleServeDetector.Snapshot(ctx, tailscale.RequireFresh)
	if err != nil {
		return tailscaleSettingsResponse{}, http.StatusServiceUnavailable, errors.New("tailscale verification is unavailable: " + err.Error())
	}
	route, found := snapshot.RouteForOrigin(origin)
	if !found || route.Classification != tailscale.ClassificationVerifiedSwarmDesktop {
		return tailscaleSettingsResponse{}, http.StatusConflict, errors.New("origin is not a freshly verified Swarm desktop Serve route")
	}
	if _, _, err := s.tailscaleServePolicy.Add(origin); err != nil {
		return tailscaleSettingsResponse{}, http.StatusInternalServerError, err
	}
	s.tailscaleServeDetector.Invalidate()
	response, err := s.tailscaleSettingsStatusFromSnapshot(snapshot)
	if err != nil {
		return tailscaleSettingsResponse{}, http.StatusInternalServerError, err
	}
	return response, http.StatusOK, nil
}

func (s *Server) handleTailscaleSettingsRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, ok := PrincipalFromRequest(r); !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.tailscaleServePolicy == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("tailscale desktop policy is not configured"))
		return
	}
	var request struct {
		Origin string `json:"origin"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, _, err := s.tailscaleServePolicy.Remove(request.Origin); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.tailscaleServeDetector != nil {
		s.tailscaleServeDetector.Invalidate()
	}
	response, err := s.tailscaleSettingsStatus(r.Context(), tailscale.RequireFresh)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) tailscaleSettingsStatus(ctx context.Context, mode tailscale.RefreshMode) (tailscaleSettingsResponse, error) {
	if s.tailscaleServePolicy == nil {
		return tailscaleSettingsResponse{}, errors.New("tailscale desktop policy is not configured")
	}
	if s.tailscaleServeDetector == nil {
		return s.tailscaleSettingsStatusWithError(errors.New("tailscale verifier is not configured"))
	}
	if mode == tailscale.RequireFresh {
		s.tailscaleServeDetector.Invalidate()
	}
	snapshot, err := s.tailscaleServeDetector.Snapshot(ctx, mode)
	if err != nil {
		return s.tailscaleSettingsStatusWithError(err)
	}
	return s.tailscaleSettingsStatusFromSnapshot(snapshot)
}

func (s *Server) tailscaleSettingsStatusWithError(detectionErr error) (tailscaleSettingsResponse, error) {
	record, _, err := s.tailscaleServePolicy.Get()
	if err != nil {
		return tailscaleSettingsResponse{}, err
	}
	state := string(tailscale.ClassificationUnavailable)
	var schemaErr *tailscale.SchemaError
	if errors.As(detectionErr, &schemaErr) {
		state = string(tailscale.ClassificationIncompatible)
	}
	effectiveTarget := ""
	remediation := ""
	if s.tailscaleServeDetector != nil {
		effectiveTarget = s.tailscaleServeDetector.EffectiveTarget()
		remediation = s.tailscaleServeDetector.RemediationCommand()
	}
	return tailscaleSettingsResponse{
		State:           state,
		Approvals:       append([]string(nil), record.Origins...),
		Revision:        record.Revision,
		UpdatedAt:       record.UpdatedAt,
		Routes:          []tailscaleSettingsRoute{},
		EffectiveTarget: effectiveTarget,
		Remediation:     remediation,
		DetectionError:  detectionErr.Error(),
	}, nil
}

func (s *Server) tailscaleSettingsStatusFromSnapshot(snapshot tailscale.Snapshot) (tailscaleSettingsResponse, error) {
	record, _, err := s.tailscaleServePolicy.Get()
	if err != nil {
		return tailscaleSettingsResponse{}, err
	}
	approved := make(map[string]struct{}, len(record.Origins))
	for _, origin := range record.Origins {
		approved[origin] = struct{}{}
	}
	routes := make([]tailscaleSettingsRoute, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		_, isApproved := approved[route.Origin]
		routes = append(routes, tailscaleSettingsRoute{
			Route:    route,
			Approved: isApproved,
			Active:   isApproved && route.Classification == tailscale.ClassificationVerifiedSwarmDesktop,
		})
	}
	return tailscaleSettingsResponse{
		State:           tailscaleSnapshotState(snapshot),
		Approvals:       append([]string(nil), record.Origins...),
		Revision:        record.Revision,
		UpdatedAt:       record.UpdatedAt,
		Routes:          routes,
		SelfDNSName:     snapshot.SelfDNSName,
		EffectiveTarget: s.tailscaleServeDetector.EffectiveTarget(),
		Remediation:     s.tailscaleServeDetector.RemediationCommand(),
	}, nil
}

func tailscaleSnapshotState(snapshot tailscale.Snapshot) string {
	state := string(tailscale.ClassificationUnconfigured)
	priority := map[tailscale.Classification]int{
		tailscale.ClassificationUnconfigured:         1,
		tailscale.ClassificationInvalid:              2,
		tailscale.ClassificationUnsupportedHandler:   3,
		tailscale.ClassificationIncompatible:         4,
		tailscale.ClassificationWrongTarget:          5,
		tailscale.ClassificationFunnelEnabled:        6,
		tailscale.ClassificationVerifiedSwarmDesktop: 7,
	}
	best := 0
	for _, route := range snapshot.Routes {
		if priority[route.Classification] > best {
			best = priority[route.Classification]
			state = string(route.Classification)
		}
	}
	return strings.TrimSpace(state)
}
