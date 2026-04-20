package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

type swarmInviteCreateRequest struct {
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
}

type swarmInviteResponse struct {
	OK     bool                `json:"ok"`
	Invite swarmruntime.Invite `json:"invite"`
}

type swarmEnrollRequest struct {
	InviteToken          string                       `json:"invite_token"`
	PrimarySwarmID       string                       `json:"primary_swarm_id,omitempty"`
	GroupID              string                       `json:"group_id,omitempty"`
	ChildSwarmID         string                       `json:"child_swarm_id,omitempty"`
	ChildName            string                       `json:"child_name,omitempty"`
	ChildRole            string                       `json:"child_role,omitempty"`
	ChildPublicKey       string                       `json:"child_public_key,omitempty"`
	TransportMode        string                       `json:"transport_mode,omitempty"`
	RendezvousTransports []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
}

type swarmEnrollmentDecisionRequest struct {
	Approve bool   `json:"approve"`
	Reason  string `json:"reason,omitempty"`
}

type swarmRemotePairingStartRequest struct {
	Endpoint             string                       `json:"endpoint,omitempty"`
	DNSName              string                       `json:"dns_name,omitempty"`
	IPs                  []string                     `json:"ips,omitempty"`
	GroupID              string                       `json:"group_id,omitempty"`
	ChildSwarmID         string                       `json:"child_swarm_id,omitempty"`
	ChildName            string                       `json:"child_name,omitempty"`
	RendezvousTransports []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
}

type swarmRemotePairingCeremony struct {
	ChildSwarmID string `json:"child_swarm_id"`
	ChildName    string `json:"child_name"`
	AuthCode     string `json:"auth_code"`
}

type swarmRemotePairingStartResponse struct {
	OK       bool                       `json:"ok"`
	Invite   swarmruntime.Invite        `json:"invite"`
	Ceremony swarmRemotePairingCeremony `json:"ceremony"`
}

type swarmRemotePairingRequest struct {
	InviteToken          string                       `json:"invite_token"`
	PrimarySwarmID       string                       `json:"primary_swarm_id"`
	PrimaryName          string                       `json:"primary_name,omitempty"`
	PrimaryEndpoint      string                       `json:"primary_endpoint"`
	TransportMode        string                       `json:"transport_mode,omitempty"`
	RendezvousTransports []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
}

type swarmRemotePairingResponse struct {
	OK           bool   `json:"ok"`
	ChildSwarmID string `json:"child_swarm_id"`
	ChildName    string `json:"child_name"`
	AuthCode     string `json:"auth_code"`
}

type swarmRemotePairingFinalizeRequest struct {
	PrimarySwarmID       string                       `json:"primary_swarm_id"`
	PrimaryName          string                       `json:"primary_name,omitempty"`
	PrimaryPublicKey     string                       `json:"primary_public_key,omitempty"`
	PrimaryFingerprint   string                       `json:"primary_fingerprint,omitempty"`
	TransportMode        string                       `json:"transport_mode,omitempty"`
	RendezvousTransports []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
}

type swarmStateResponse struct {
	OK    bool                    `json:"ok"`
	State swarmruntime.LocalState `json:"state"`
}

type swarmDiscoveryResponse struct {
	OK                   bool                         `json:"ok"`
	SwarmID              string                       `json:"swarm_id,omitempty"`
	Name                 string                       `json:"name,omitempty"`
	Role                 string                       `json:"role,omitempty"`
	Endpoint             string                       `json:"endpoint,omitempty"`
	TransportMode        string                       `json:"transport_mode,omitempty"`
	RendezvousTransports []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
}

func logSwarmPairingTiming(step string, startedAt time.Time, err error, fields ...string) {
	parts := []string{
		fmt.Sprintf("swarm pairing timing step=%q", strings.TrimSpace(step)),
		fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
	}
	if err != nil {
		parts = append(parts, fmt.Sprintf("status=%q", "error"), fmt.Sprintf("err=%q", strings.TrimSpace(err.Error())))
	} else {
		parts = append(parts, fmt.Sprintf("status=%q", "ok"))
	}
	for idx := 0; idx+1 < len(fields); idx += 2 {
		key := strings.TrimSpace(fields[idx])
		value := strings.TrimSpace(fields[idx+1])
		if key == "" || value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, value))
	}
	log.Print(strings.Join(parts, " "))
}

func (s *Server) handleSwarmInvites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := requireSwarmModeEnabled(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.onboardingResponse(true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if cfg.Child {
		writeError(w, http.StatusBadRequest, errors.New("only master swarms can create invites"))
		return
	}
	var req swarmInviteCreateRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ttl := 15 * time.Minute
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	invite, err := s.swarm.CreateInvite(swarmruntime.CreateInviteInput{
		PrimarySwarmID:       strings.TrimSpace(state.Node.SwarmID),
		PrimaryName:          status.Config.SwarmName,
		GroupID:              strings.TrimSpace(req.GroupID),
		TransportMode:        status.Config.Mode,
		RendezvousTransports: onboardingTransportsToSwarm(detectedOnboardingTransports(cfg)),
		TTL:                  ttl,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, swarmInviteResponse{OK: true, Invite: invite})
}

func (s *Server) handleSwarmEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	var req swarmEnrollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := requireSwarmModeEnabled(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.onboardingResponse(true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	childSwarmID := firstNonEmpty(strings.TrimSpace(req.ChildSwarmID), strings.TrimSpace(state.Node.SwarmID))
	childName := firstNonEmpty(strings.TrimSpace(req.ChildName), status.Config.SwarmName)
	childRole := firstNonEmpty(strings.TrimSpace(req.ChildRole), localSwarmRole(cfg), bootstrapRoleChild)
	childPublicKey := strings.TrimSpace(req.ChildPublicKey)
	if childPublicKey == "" {
		childPublicKey = strings.TrimSpace(state.Node.PublicKey)
	}
	enrollment, err := s.swarm.SubmitEnrollment(swarmruntime.SubmitEnrollmentInput{
		InviteToken:          strings.TrimSpace(req.InviteToken),
		PrimarySwarmID:       strings.TrimSpace(req.PrimarySwarmID),
		GroupID:              strings.TrimSpace(req.GroupID),
		ChildSwarmID:         childSwarmID,
		ChildName:            childName,
		ChildRole:            childRole,
		ChildPublicKey:       childPublicKey,
		TransportMode:        firstNonEmpty(strings.TrimSpace(req.TransportMode), status.Config.Mode),
		ObservedRemoteAddr:   strings.TrimSpace(r.RemoteAddr),
		RendezvousTransports: onboardingTransportsToSwarm(req.RendezvousTransports),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enrollment": enrollment})
}

func (s *Server) handleSwarmRemotePairingStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	var req swarmRemotePairingStartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	childTransports := remotePairingStartTransports(req)
	childEndpoint := normalizeRemoteSwarmEndpoint(firstNonEmpty(req.Endpoint, req.DNSName, firstString(req.IPs)))
	if childEndpoint == "" {
		writeError(w, http.StatusBadRequest, errors.New("child endpoint is required"))
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := requireSwarmModeEnabled(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.onboardingResponse(true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if cfg.Child {
		writeError(w, http.StatusBadRequest, errors.New("only master swarms can start child pairing"))
		return
	}
	primaryEndpoint := canonicalRemoteSwarmEndpoint(cfg, status)
	if primaryEndpoint == "" {
		writeError(w, http.StatusBadRequest, errors.New("this master swarm does not have a reachable remote endpoint yet"))
		return
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	invite, err := s.swarm.CreateInvite(swarmruntime.CreateInviteInput{
		PrimarySwarmID:       strings.TrimSpace(state.Node.SwarmID),
		PrimaryName:          status.Config.SwarmName,
		GroupID:              strings.TrimSpace(req.GroupID),
		TransportMode:        status.Config.Mode,
		RendezvousTransports: onboardingTransportsToSwarm(detectedOnboardingTransports(cfg)),
		TTL:                  15 * time.Minute,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var remote swarmRemotePairingResponse
	if err := postRemoteSwarmJSONWithTransportFallback(childEndpoint, "/v1/swarm/remote-pairing/request", childTransports, swarmRemotePairingRequest{
		InviteToken:          invite.Token,
		PrimarySwarmID:       strings.TrimSpace(state.Node.SwarmID),
		PrimaryName:          status.Config.SwarmName,
		PrimaryEndpoint:      primaryEndpoint,
		TransportMode:        status.Config.Mode,
		RendezvousTransports: detectedOnboardingTransports(cfg),
	}, &remote); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if requestedChildSwarmID := strings.TrimSpace(req.ChildSwarmID); requestedChildSwarmID != "" && strings.TrimSpace(remote.ChildSwarmID) != "" && !strings.EqualFold(requestedChildSwarmID, strings.TrimSpace(remote.ChildSwarmID)) {
		writeError(w, http.StatusBadGateway, fmt.Errorf("remote pairing target mismatch: requested child swarm %s but remote node responded as %s", requestedChildSwarmID, strings.TrimSpace(remote.ChildSwarmID)))
		return
	}
	writeJSON(w, http.StatusOK, swarmRemotePairingStartResponse{
		OK:     true,
		Invite: invite,
		Ceremony: swarmRemotePairingCeremony{
			ChildSwarmID: strings.TrimSpace(remote.ChildSwarmID),
			ChildName:    strings.TrimSpace(remote.ChildName),
			AuthCode:     strings.TrimSpace(remote.AuthCode),
		},
	})
}

func (s *Server) handleSwarmRemotePairingRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	requestStartedAt := time.Now()
	var req swarmRemotePairingRequest
	var requestErr error
	var primaryEndpoint string
	var childSwarmID string
	defer func() {
		logSwarmPairingTiming(
			"remote_pairing.total",
			requestStartedAt,
			requestErr,
			"primary_swarm_id",
			strings.TrimSpace(req.PrimarySwarmID),
			"primary_endpoint",
			primaryEndpoint,
			"child_swarm_id",
			childSwarmID,
		)
	}()
	fail := func(statusCode int, err error) {
		requestErr = err
		writeError(w, statusCode, err)
	}
	if err := decodeJSON(r, &req); err != nil {
		fail(http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.InviteToken) == "" {
		fail(http.StatusBadRequest, errors.New("invite token is required"))
		return
	}
	primaryEndpoint = normalizeRemoteSwarmEndpoint(req.PrimaryEndpoint)
	if primaryEndpoint == "" {
		fail(http.StatusBadRequest, errors.New("primary endpoint is required"))
		return
	}

	stepStartedAt := time.Now()
	cfg, err := s.loadStartupConfig()
	logSwarmPairingTiming("remote_pairing.load_startup_config", stepStartedAt, err, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID))
	if err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}
	if err := requireSwarmModeEnabled(cfg); err != nil {
		fail(http.StatusBadRequest, err)
		return
	}
	stepStartedAt = time.Now()
	state, err := s.currentSwarmState(cfg)
	logSwarmPairingTiming("remote_pairing.current_swarm_state", stepStartedAt, err, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID))
	if err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}
	if !cfg.Child {
		fail(http.StatusBadRequest, errors.New("a master swarm cannot be paired as a child"))
		return
	}
	pairingState := strings.TrimSpace(state.Pairing.PairingState)
	if pairingState == "" {
		pairingState = startupconfig.PairingStateUnpaired
	}
	if strings.TrimSpace(state.Pairing.ParentSwarmID) != "" &&
		!strings.EqualFold(strings.TrimSpace(state.Pairing.ParentSwarmID), strings.TrimSpace(req.PrimarySwarmID)) &&
		pairingState == startupconfig.PairingStatePaired {
		fail(http.StatusBadRequest, errors.New("this swarm is already paired to another parent"))
		return
	}

	childSwarmID = strings.TrimSpace(state.Node.SwarmID)
	childName := firstNonEmpty(strings.TrimSpace(cfg.SwarmName), strings.TrimSpace(state.Node.Name), "Child swarm")
	transportMode := firstNonEmpty(strings.TrimSpace(req.TransportMode), bootstrapNetworkMode(cfg))
	if childSwarmID == "" {
		fail(http.StatusInternalServerError, errors.New("child swarm identity is not configured"))
		return
	}
	if strings.TrimSpace(state.Node.PublicKey) == "" {
		fail(http.StatusInternalServerError, errors.New("child public key is unavailable"))
		return
	}

	stepStartedAt = time.Now()
	authCode, err := randomSwarmCeremonyCode()
	logSwarmPairingTiming("remote_pairing.generate_auth_code", stepStartedAt, err, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID), "child_swarm_id", childSwarmID)
	if err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}

	var enrollResponse struct {
		OK         bool                    `json:"ok"`
		Enrollment swarmruntime.Enrollment `json:"enrollment"`
	}
	stepStartedAt = time.Now()
	if err := postRemoteSwarmJSONWithTailscaleFallback(primaryEndpoint, req.RendezvousTransports, swarmEnrollRequest{
		InviteToken:          strings.TrimSpace(req.InviteToken),
		PrimarySwarmID:       strings.TrimSpace(req.PrimarySwarmID),
		ChildSwarmID:         childSwarmID,
		ChildName:            childName,
		ChildRole:            bootstrapRoleChild,
		ChildPublicKey:       strings.TrimSpace(state.Node.PublicKey),
		TransportMode:        transportMode,
		RendezvousTransports: detectedOnboardingTransports(cfg),
	}, &enrollResponse); err != nil {
		logSwarmPairingTiming("remote_pairing.post_primary_enroll", stepStartedAt, err, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID), "primary_endpoint", primaryEndpoint, "child_swarm_id", childSwarmID)
		fail(http.StatusBadGateway, err)
		return
	}
	logSwarmPairingTiming("remote_pairing.post_primary_enroll", stepStartedAt, nil, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID), "primary_endpoint", primaryEndpoint, "child_swarm_id", childSwarmID, "enrollment_id", strings.TrimSpace(enrollResponse.Enrollment.ID))
	if strings.TrimSpace(enrollResponse.Enrollment.ID) == "" {
		fail(http.StatusBadGateway, errors.New("primary enrollment response was missing enrollment data"))
		return
	}

	updatedCfg := cfg
	updatedCfg.Child = true
	stepStartedAt = time.Now()
	if err := startupconfig.Write(updatedCfg); err != nil {
		logSwarmPairingTiming("remote_pairing.write_startup_config", stepStartedAt, err, "child_swarm_id", childSwarmID)
		fail(http.StatusInternalServerError, err)
		return
	}
	logSwarmPairingTiming("remote_pairing.write_startup_config", stepStartedAt, nil, "child_swarm_id", childSwarmID)
	stepStartedAt = time.Now()
	if _, err := s.swarm.UpdateLocalPairingFromConfig(updatedCfg, onboardingTransportsToSwarm(detectedOnboardingTransports(updatedCfg))); err != nil {
		logSwarmPairingTiming("remote_pairing.update_local_pairing_from_config", stepStartedAt, err, "child_swarm_id", childSwarmID, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID))
		fail(http.StatusInternalServerError, err)
		return
	}
	logSwarmPairingTiming("remote_pairing.update_local_pairing_from_config", stepStartedAt, nil, "child_swarm_id", childSwarmID, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID))
	if cfg.RemoteDeploy.Enabled {
		stepStartedAt = time.Now()
		if err := s.swarm.PrepareRemoteBootstrapParentPeer(swarmruntime.PrepareRemoteBootstrapParentPeerInput{
			ParentSwarmID:         strings.TrimSpace(req.PrimarySwarmID),
			ParentName:            strings.TrimSpace(req.PrimaryName),
			TransportMode:         transportMode,
			RendezvousTransports:  onboardingTransportsToSwarm(req.RendezvousTransports),
			OutgoingPeerAuthToken: strings.TrimSpace(cfg.RemoteDeploy.SessionToken),
			IncomingPeerAuthToken: strings.TrimSpace(req.InviteToken),
		}); err != nil {
			logSwarmPairingTiming("remote_pairing.prepare_remote_bootstrap_parent_peer", stepStartedAt, err, "child_swarm_id", childSwarmID, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID))
			fail(http.StatusInternalServerError, err)
			return
		}
		logSwarmPairingTiming("remote_pairing.prepare_remote_bootstrap_parent_peer", stepStartedAt, nil, "child_swarm_id", childSwarmID, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID))
	}
	stepStartedAt = time.Now()
	if err := s.publishSwarmPairingEvent("swarm.ceremony.requested", childSwarmID, map[string]any{
		"auth_code":        authCode,
		"primary_name":     strings.TrimSpace(req.PrimaryName),
		"primary_swarm_id": strings.TrimSpace(req.PrimarySwarmID),
		"child_name":       childName,
		"child_swarm_id":   childSwarmID,
		"enrollment_id":    strings.TrimSpace(enrollResponse.Enrollment.ID),
	}); err != nil {
		logSwarmPairingTiming("remote_pairing.publish_pairing_event", stepStartedAt, err, "child_swarm_id", childSwarmID, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID), "enrollment_id", strings.TrimSpace(enrollResponse.Enrollment.ID))
		fail(http.StatusInternalServerError, err)
		return
	}
	logSwarmPairingTiming("remote_pairing.publish_pairing_event", stepStartedAt, nil, "child_swarm_id", childSwarmID, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID), "enrollment_id", strings.TrimSpace(enrollResponse.Enrollment.ID))

	stepStartedAt = time.Now()
	writeJSON(w, http.StatusOK, swarmRemotePairingResponse{
		OK:           true,
		ChildSwarmID: childSwarmID,
		ChildName:    childName,
		AuthCode:     authCode,
	})
	logSwarmPairingTiming("remote_pairing.write_response", stepStartedAt, nil, "child_swarm_id", childSwarmID, "primary_swarm_id", strings.TrimSpace(req.PrimarySwarmID))
}

func (s *Server) handleSwarmRemotePairingFinalize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	var req swarmRemotePairingFinalizeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.PrimarySwarmID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("primary swarm id is required"))
		return
	}
	pairing, err := s.swarm.FinalizeRemoteBootstrapChildPairing(swarmruntime.FinalizeRemoteBootstrapChildPairingInput{
		ParentSwarmID:        strings.TrimSpace(req.PrimarySwarmID),
		ParentName:           strings.TrimSpace(req.PrimaryName),
		ParentPublicKey:      strings.TrimSpace(req.PrimaryPublicKey),
		ParentFingerprint:    strings.TrimSpace(req.PrimaryFingerprint),
		TransportMode:        strings.TrimSpace(req.TransportMode),
		RendezvousTransports: onboardingTransportsToSwarm(req.RendezvousTransports),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path_id": "swarm.remote_pairing.finalize.v1",
		"pairing": pairing,
	})
}

func (s *Server) handleSwarmDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !swarmModeEnabled(cfg) {
		writeJSON(w, http.StatusOK, swarmDiscoveryResponse{
			OK:            true,
			SwarmID:       strings.TrimSpace(state.Node.SwarmID),
			Name:          firstNonEmpty(strings.TrimSpace(cfg.SwarmName), strings.TrimSpace(state.Node.Name), "Unnamed swarm"),
			Role:          bootstrapRoleStandalone,
			Endpoint:      "",
			TransportMode: "",
		})
		return
	}
	transports := detectedOnboardingTransports(cfg)
	tailscale := detectTailscale()
	status := onboardingResponse{Tailscale: tailscale}
	writeJSON(w, http.StatusOK, swarmDiscoveryResponse{
		OK:                   true,
		SwarmID:              strings.TrimSpace(state.Node.SwarmID),
		Name:                 firstNonEmpty(strings.TrimSpace(cfg.SwarmName), strings.TrimSpace(state.Node.Name), "Unnamed swarm"),
		Role:                 localSwarmRole(cfg),
		Endpoint:             canonicalRemoteSwarmEndpoint(cfg, status),
		TransportMode:        bootstrapNetworkMode(cfg),
		RendezvousTransports: transports,
	})
}

func (s *Server) handleSwarmPendingChildren(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := requireSwarmModeEnabled(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.swarm.ListPendingEnrollments(500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "count": len(items)})
}

func (s *Server) handleSwarmEnrollmentDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := requireSwarmModeEnabled(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path := strings.Trim(strings.TrimPrefix(strings.TrimSpace(r.URL.Path), "/v1/swarm/enrollment/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		writeError(w, http.StatusBadRequest, errors.New("expected /v1/swarm/enrollment/{id}/approve or reject"))
		return
	}
	action := strings.ToLower(strings.TrimSpace(parts[1]))
	approve := false
	switch action {
	case "approve":
		approve = true
	case "reject":
		approve = false
	default:
		writeError(w, http.StatusBadRequest, errors.New("unknown enrollment action"))
		return
	}
	var req swarmEnrollmentDecisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	enrollment, peers, err := s.swarm.DecideEnrollment(swarmruntime.DecideEnrollmentInput{EnrollmentID: parts[0], Approve: approve, Reason: strings.TrimSpace(req.Reason)})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enrollment": enrollment, "trusted_peers": peers})
}

func (s *Server) handleSwarmState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, swarmStateResponse{OK: true, State: state})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeRemoteSwarmEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(strings.TrimSuffix(endpoint, "/"))
	if endpoint == "" {
		return ""
	}
	if strings.HasPrefix(endpoint, "https://") || strings.HasPrefix(endpoint, "http://") {
		return endpoint
	}
	return "https://" + endpoint
}

func canonicalRemoteSwarmEndpoint(cfg startupconfig.FileConfig, status onboardingResponse) string {
	if !swarmModeEnabled(cfg) {
		return ""
	}
	switch bootstrapNetworkMode(cfg) {
	case startupconfig.NetworkModeTailscale:
		if endpoint := strings.TrimSpace(cfg.TailscaleURL); endpoint != "" {
			return normalizeRemoteSwarmEndpoint(endpoint)
		}
		if endpoint := strings.TrimSpace(status.Tailscale.TailnetURL); endpoint != "" {
			return normalizeRemoteSwarmEndpoint(endpoint)
		}
		if dnsName := strings.TrimSpace(status.Tailscale.DNSName); dnsName != "" {
			return "https://" + dnsName
		}
		return ""
	default:
		if host := strings.TrimSpace(cfg.AdvertiseHost); host != "" {
			if strings.HasPrefix(host, "https://") || strings.HasPrefix(host, "http://") {
				return strings.TrimSuffix(host, "/")
			}
			return "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", canonicalAdvertisePort(cfg)))
		}
		transports := detectedOnboardingTransports(cfg)
		if endpoint := firstTransportForKind(transports, startupconfig.NetworkModeLAN); endpoint != "" {
			if strings.HasPrefix(endpoint, "https://") || strings.HasPrefix(endpoint, "http://") {
				return strings.TrimSuffix(endpoint, "/")
			}
			return "http://" + net.JoinHostPort(endpoint, fmt.Sprintf("%d", canonicalAdvertisePort(cfg)))
		}
		return ""
	}
}

func requireSwarmModeEnabled(cfg startupconfig.FileConfig) error {
	if swarmModeEnabled(cfg) {
		return nil
	}
	return errors.New("turn on swarm mode before using swarm networking or pairing")
}

func postRemoteSwarmJSON(endpoint string, payload any, out any) error {
	return remoteSwarmJSONRequestWithClient(http.MethodPost, endpoint, payload, out, nil)
}

func postRemoteSwarmJSONWithTailscaleFallback(endpoint string, transports []onboardingTransportPayload, payload any, out any) error {
	return postRemoteSwarmJSONWithTransportFallback(endpoint, "/v1/swarm/enroll", transports, payload, out)
}

func postRemoteSwarmJSONWithTransportFallback(endpoint, requestPath string, transports []onboardingTransportPayload, payload any, out any) error {
	return remoteSwarmJSONRequestWithTransportFallback(http.MethodPost, endpoint, requestPath, transports, payload, out)
}

func getRemoteSwarmJSONWithTransportFallback(endpoint, requestPath string, transports []onboardingTransportPayload, out any) error {
	return remoteSwarmJSONRequestWithTransportFallback(http.MethodGet, endpoint, requestPath, transports, nil, out)
}

func remoteSwarmJSONRequestWithTransportFallback(method, endpoint, requestPath string, transports []onboardingTransportPayload, payload any, out any) error {
	endpoint = strings.TrimSpace(strings.TrimSuffix(endpoint, "/"))
	if endpoint == "" {
		return errors.New("remote endpoint is required")
	}
	requestPath = "/" + strings.TrimPrefix(strings.TrimSpace(requestPath), "/")
	requestURL := endpoint + requestPath
	if err := remoteSwarmJSONRequestWithClient(method, requestURL, payload, out, nil); err == nil {
		return nil
	} else {
		canonicalErr := err
		var lastErr error
		if client, clientErr := httpClientForTailscaleOutboundProxy(endpoint, transports); clientErr != nil {
			lastErr = clientErr
		} else if client != nil {
			if err := remoteSwarmJSONRequestWithClient(method, requestURL, payload, out, client); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
		for _, dialIP := range transportDialIPs(transports) {
			client, clientErr := httpClientForPinnedRemoteIP(endpoint, dialIP)
			if clientErr != nil {
				lastErr = clientErr
				continue
			}
			if err := remoteSwarmJSONRequestWithClient(method, requestURL, payload, out, client); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
		if lastErr != nil {
			return fmt.Errorf("canonical remote request failed: %w; transport fallback error: %v", canonicalErr, lastErr)
		}
		return canonicalErr
	}
}

func httpClientForTailscaleOutboundProxy(endpoint string, transports []onboardingTransportPayload) (*http.Client, error) {
	proxyAddr := strings.TrimSpace(os.Getenv("SWARM_TAILSCALE_OUTBOUND_PROXY"))
	if proxyAddr == "" {
		return nil, nil
	}
	if !endpointLooksLikeTailscale(endpoint) && !transportsContainKind(transports, startupconfig.NetworkModeTailscale) {
		return nil, nil
	}
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxyURL),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Timeout: 12 * time.Second, Transport: transport}, nil
}

func endpointLooksLikeTailscale(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), ".")
	return strings.HasSuffix(host, ".ts.net")
}

func transportsContainKind(transports []onboardingTransportPayload, kind string) bool {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return false
	}
	for _, transport := range transports {
		if strings.EqualFold(strings.TrimSpace(transport.Kind), kind) {
			return true
		}
	}
	return false
}

func remoteSwarmJSONRequestWithClient(method, endpoint string, payload any, out any, client *http.Client) error {
	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(body)
	}
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	req, err := http.NewRequest(method, endpoint, bodyReader)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		if readErr != nil {
			return fmt.Errorf("remote request failed with status %d", resp.StatusCode)
		}
		text := strings.TrimSpace(string(message))
		if text == "" {
			return fmt.Errorf("remote request failed with status %d", resp.StatusCode)
		}
		return fmt.Errorf("remote request failed with status %d: %s", resp.StatusCode, text)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}

func transportDialIPs(transports []onboardingTransportPayload) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(transports))
	for _, transport := range transports {
		for _, value := range append([]string{transport.Primary}, transport.All...) {
			value = strings.TrimSpace(value)
			if value == "" || net.ParseIP(value) == nil {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func remotePairingStartTransports(req swarmRemotePairingStartRequest) []onboardingTransportPayload {
	if len(req.RendezvousTransports) > 0 {
		return append([]onboardingTransportPayload(nil), req.RendezvousTransports...)
	}
	values := make([]string, 0, len(req.IPs)+1)
	if dnsName := strings.TrimSpace(req.DNSName); dnsName != "" {
		values = append(values, dnsName)
	}
	for _, ip := range req.IPs {
		ip = strings.TrimSpace(ip)
		if ip != "" {
			values = append(values, ip)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return []onboardingTransportPayload{{
		Kind:    startupconfig.NetworkModeTailscale,
		Primary: firstNonEmpty(strings.TrimSpace(req.DNSName), firstString(req.IPs)),
		All:     dedupeTransportStrings(values),
	}}
}

func httpClientForPinnedRemoteIP(endpoint, dialIP string) (*http.Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return nil, err
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return nil, errors.New("remote endpoint host is required")
	}
	port := strings.TrimSpace(parsed.Port())
	if port == "" {
		switch strings.ToLower(parsed.Scheme) {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	targetAddr := net.JoinHostPort(strings.TrimSpace(dialIP), port)
	baseDialer := &net.Dialer{Timeout: 12 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return baseDialer.DialContext(ctx, network, targetAddr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		transport.TLSClientConfig = &tls.Config{ServerName: host}
	}
	return &http.Client{Timeout: 12 * time.Second, Transport: transport}, nil
}

func randomSwarmCeremonyCode() (string, error) {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(buf)), nil
}

func (s *Server) publishSwarmPairingEvent(eventType, entityID string, payload any) error {
	if s == nil || s.events == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env, err := s.events.Append("swarm:pairing", eventType, entityID, raw, "", "")
	if err != nil {
		return err
	}
	if s.hub != nil {
		s.hub.Publish(env)
	}
	return nil
}
