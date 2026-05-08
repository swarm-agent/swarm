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
	"sort"
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
	PeerAuthToken        string                       `json:"peer_auth_token,omitempty"`
}

type swarmEnrollmentDecisionRequest struct {
	Approve bool   `json:"approve"`
	Reason  string `json:"reason,omitempty"`
}

type swarmRemotePairingStartRequest struct {
	Endpoint             string                         `json:"endpoint,omitempty"`
	DNSName              string                         `json:"dns_name,omitempty"`
	IPs                  []string                       `json:"ips,omitempty"`
	GroupID              string                         `json:"group_id,omitempty"`
	ManagerSwarmID       string                         `json:"manager_swarm_id,omitempty"`
	ManagerName          string                         `json:"manager_name,omitempty"`
	ManagedSwarmID       string                         `json:"managed_swarm_id,omitempty"`
	ManagedName          string                         `json:"managed_name,omitempty"`
	Offer                swarmRemotePairingOfferPayload `json:"offer,omitempty"`
	CeremonyCode         string                         `json:"ceremony_code,omitempty"`
	RendezvousTransports []onboardingTransportPayload   `json:"rendezvous_transports,omitempty"`
	ChildSwarmID         string                         `json:"child_swarm_id,omitempty"`
	ChildName            string                         `json:"child_name,omitempty"`
}

type swarmRemotePairingCeremony struct {
	ManagedSwarmID   string `json:"managed_swarm_id"`
	ManagedName      string `json:"managed_name"`
	Code             string `json:"code"`
	VerificationOnly bool   `json:"verification_only"`
	ChildSwarmID     string `json:"child_swarm_id,omitempty"`
	ChildName        string `json:"child_name,omitempty"`
	AuthCode         string `json:"auth_code,omitempty"`
}

type swarmRemotePairingStartResponse struct {
	OK       bool                       `json:"ok"`
	Invite   swarmruntime.Invite        `json:"invite"`
	Request  swarmRemotePairingResponse `json:"request"`
	Ceremony swarmRemotePairingCeremony `json:"ceremony"`
}

type swarmRemotePairingRequest struct {
	InviteToken          string                         `json:"invite_token"`
	ManagerSwarmID       string                         `json:"manager_swarm_id"`
	ManagerName          string                         `json:"manager_name,omitempty"`
	ManagerEndpoint      string                         `json:"manager_endpoint"`
	Offer                swarmRemotePairingOfferPayload `json:"offer"`
	CeremonyCode         string                         `json:"ceremony_code"`
	TransportMode        string                         `json:"transport_mode,omitempty"`
	RendezvousTransports []onboardingTransportPayload   `json:"rendezvous_transports,omitempty"`
	PeerAuthToken        string                         `json:"peer_auth_token,omitempty"`
	PrimarySwarmID       string                         `json:"primary_swarm_id,omitempty"`
	PrimaryName          string                         `json:"primary_name,omitempty"`
	PrimaryEndpoint      string                         `json:"primary_endpoint,omitempty"`
}

type swarmRemotePairingResponse struct {
	OK                 bool   `json:"ok"`
	RequestID          string `json:"request_id"`
	Status             string `json:"status"`
	ManagedSwarmID     string `json:"managed_swarm_id"`
	ManagedName        string `json:"managed_name"`
	ManagedPublicKey   string `json:"managed_public_key,omitempty"`
	ManagedFingerprint string `json:"managed_fingerprint,omitempty"`
	CeremonyCode       string `json:"ceremony_code"`
	ChildSwarmID       string `json:"child_swarm_id,omitempty"`
	ChildName          string `json:"child_name,omitempty"`
	AuthCode           string `json:"auth_code,omitempty"`
}

type swarmRemotePairingFinalizeRequest struct {
	ManagerSwarmID        string                       `json:"manager_swarm_id"`
	ManagerName           string                       `json:"manager_name,omitempty"`
	ManagerPublicKey      string                       `json:"manager_public_key,omitempty"`
	ManagerFingerprint    string                       `json:"manager_fingerprint,omitempty"`
	TransportMode         string                       `json:"transport_mode,omitempty"`
	RendezvousTransports  []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
	PeerAuthToken         string                       `json:"peer_auth_token,omitempty"`
	IncomingPeerAuthToken string                       `json:"incoming_peer_auth_token,omitempty"`
	PrimarySwarmID        string                       `json:"primary_swarm_id,omitempty"`
	PrimaryName           string                       `json:"primary_name,omitempty"`
	PrimaryPublicKey      string                       `json:"primary_public_key,omitempty"`
	PrimaryFingerprint    string                       `json:"primary_fingerprint,omitempty"`
}

type swarmRemotePairingApprovalRequest struct {
	RequestID    string `json:"request_id"`
	Approve      bool   `json:"approve"`
	CeremonyCode string `json:"ceremony_code"`
	Reason       string `json:"reason,omitempty"`
}

type swarmRemotePairingApprovalResponse struct {
	OK          bool                        `json:"ok"`
	Status      string                      `json:"status"`
	RequestID   string                      `json:"request_id"`
	Invite      swarmruntime.Invite         `json:"invite,omitempty"`
	Pairing     swarmruntime.PairingState   `json:"pairing,omitempty"`
	Enrollment  swarmruntime.Enrollment     `json:"enrollment,omitempty"`
	TrustedPeer *swarmruntime.TrustedPeer   `json:"trusted_peer,omitempty"`
	Routing     *swarmManagedPairingRouting `json:"routing,omitempty"`
}

type swarmRemotePairingPendingItem struct {
	RequestID          string                       `json:"request_id"`
	Status             string                       `json:"status"`
	ManagerSwarmID     string                       `json:"manager_swarm_id,omitempty"`
	ManagerName        string                       `json:"manager_name,omitempty"`
	ManagerEndpoint    string                       `json:"manager_endpoint,omitempty"`
	ManagedSwarmID     string                       `json:"managed_swarm_id,omitempty"`
	ManagedName        string                       `json:"managed_name,omitempty"`
	ManagedFingerprint string                       `json:"managed_fingerprint,omitempty"`
	ManagedEndpoint    string                       `json:"managed_endpoint,omitempty"`
	CeremonyCode       string                       `json:"ceremony_code,omitempty"`
	TransportMode      string                       `json:"transport_mode,omitempty"`
	Rendezvous         []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
	CreatedAt          int64                        `json:"created_at,omitempty"`
}

type swarmRemotePairingPendingResponse struct {
	OK    bool                            `json:"ok"`
	Items []swarmRemotePairingPendingItem `json:"items"`
	Count int                             `json:"count"`
}

type swarmManagedPairingRouting struct {
	ManagedSwarmID       string                       `json:"managed_swarm_id"`
	ManagedName          string                       `json:"managed_name"`
	Relationship         string                       `json:"relationship"`
	BackendURL           string                       `json:"backend_url,omitempty"`
	TransportMode        string                       `json:"transport_mode,omitempty"`
	RepresentAsLocal     bool                         `json:"represent_as_local"`
	ContainerScope       string                       `json:"container_scope"`
	ProxyPathPrefix      string                       `json:"proxy_path_prefix,omitempty"`
	RendezvousTransports []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
}

type swarmRemotePairingPendingRequest struct {
	ID                          string
	InviteToken                 string
	ManagerSwarmID              string
	ManagerName                 string
	ManagerPublicKey            string
	ManagerFingerprint          string
	ManagerEndpoint             string
	ManagedSwarmID              string
	ManagedName                 string
	ManagedPublicKey            string
	ManagedFingerprint          string
	ManagedEndpoint             string
	CeremonyCode                string
	TransportMode               string
	ManagerRendezvousTransports []onboardingTransportPayload
	ManagedRendezvousTransports []onboardingTransportPayload
	ManagerToManagedPeerToken   string
	ManagedToManagerPeerToken   string
	CreatedAt                   time.Time
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
		InviteToken:           strings.TrimSpace(req.InviteToken),
		PrimarySwarmID:        strings.TrimSpace(req.PrimarySwarmID),
		GroupID:               strings.TrimSpace(req.GroupID),
		ChildSwarmID:          childSwarmID,
		ChildName:             childName,
		ChildRole:             childRole,
		ChildPublicKey:        childPublicKey,
		TransportMode:         firstNonEmpty(strings.TrimSpace(req.TransportMode), status.Config.Mode),
		ObservedRemoteAddr:    strings.TrimSpace(r.RemoteAddr),
		RendezvousTransports:  onboardingTransportsToSwarm(req.RendezvousTransports),
		IncomingPeerAuthToken: strings.TrimSpace(req.PeerAuthToken),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if peerAuthToken := strings.TrimSpace(req.PeerAuthToken); peerAuthToken != "" {
		if _, peers, err := s.swarm.DecideEnrollment(swarmruntime.DecideEnrollmentInput{EnrollmentID: enrollment.ID, Approve: true, Reason: "managed pairing approved", IncomingPeerAuthToken: peerAuthToken}); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enrollment": enrollment, "trusted_peers": peers})
			return
		}
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
	managerEndpoint := normalizeRemoteSwarmEndpoint(firstNonEmpty(req.Endpoint, req.DNSName, firstString(req.IPs)))
	if managerEndpoint == "" {
		writeError(w, http.StatusBadRequest, errors.New("manager endpoint is required"))
		return
	}
	managedEndpoint := canonicalRemoteSwarmEndpoint(cfg, status)
	if managedEndpoint == "" {
		writeError(w, http.StatusBadRequest, errors.New("this managed swarm does not have a reachable remote endpoint yet"))
		return
	}
	managedPublicKey := strings.TrimSpace(state.Node.PublicKey)
	if managedPublicKey == "" {
		writeError(w, http.StatusInternalServerError, errors.New("managed swarm public key is unavailable"))
		return
	}
	managedSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if managedSwarmID == "" {
		writeError(w, http.StatusInternalServerError, errors.New("managed swarm identity is not configured"))
		return
	}
	if requestedManagedSwarmID := firstNonEmpty(strings.TrimSpace(req.ManagedSwarmID), strings.TrimSpace(req.ChildSwarmID)); requestedManagedSwarmID != "" && !strings.EqualFold(requestedManagedSwarmID, managedSwarmID) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("managed pairing target mismatch: this managed swarm is %s", managedSwarmID))
		return
	}
	managerDiscoveryTransports := remotePairingStartTransports(req)
	managerDiscovery, err := fetchRemoteSwarmDiscovery(remoteSwarmDiscoverySeed{Endpoint: managerEndpoint, Transports: managerDiscoveryTransports})
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("fetch manager swarm discovery from %s: %w", managerEndpoint, err))
		return
	}
	managerSwarmID := firstNonEmpty(strings.TrimSpace(req.ManagerSwarmID), strings.TrimSpace(managerDiscovery.SwarmID))
	if managerSwarmID == "" {
		writeError(w, http.StatusBadGateway, errors.New("manager swarm discovery response was missing swarm identity"))
		return
	}
	managerName := firstNonEmpty(strings.TrimSpace(req.ManagerName), strings.TrimSpace(managerDiscovery.Name), "Manager")
	managerTransports := managerDiscovery.RendezvousTransports
	if len(managerTransports) == 0 {
		managerTransports = managerDiscoveryTransports
	}
	offer := req.Offer
	if strings.TrimSpace(offer.Token) == "" {
		offer, err = buildSwarmRemotePairingOffer(cfg, status, state, swarmRemotePairingOfferDefaultTTL, time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if strings.TrimSpace(offer.Token) == "" {
		writeError(w, http.StatusInternalServerError, errors.New("managed pairing offer was missing its high-entropy token"))
		return
	}
	expectedCode := deriveSwarmRemotePairingOfferCeremonyCode(offer)
	if strings.TrimSpace(req.CeremonyCode) == "" {
		req.CeremonyCode = expectedCode
	}
	if expectedCode == "" || !strings.EqualFold(strings.TrimSpace(req.CeremonyCode), expectedCode) || !strings.EqualFold(strings.TrimSpace(offer.Ceremony.Code), expectedCode) {
		writeError(w, http.StatusBadRequest, errors.New("managed pairing ceremony code does not match offer transcript"))
		return
	}
	if offer.ExpiresAt > 0 && offer.ExpiresAt < time.Now().Unix() {
		writeError(w, http.StatusBadRequest, errors.New("managed pairing offer has expired"))
		return
	}
	managerToManagedPeerToken := strings.TrimSpace(offer.Token)
	managedToManagerPeerToken, err := swarmruntime.GeneratePeerAuthToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var remote swarmRemotePairingResponse
	if err := postRemoteSwarmJSONWithTransportFallback(managerEndpoint, "/v1/swarm/remote-pairing/request", managerTransports, swarmRemotePairingRequest{
		InviteToken:          managerToManagedPeerToken,
		ManagerSwarmID:       managerSwarmID,
		ManagerName:          managerName,
		ManagerEndpoint:      managerEndpoint,
		Offer:                offer,
		CeremonyCode:         strings.TrimSpace(req.CeremonyCode),
		TransportMode:        firstNonEmpty(strings.TrimSpace(offer.TransportMode), status.Config.Mode),
		RendezvousTransports: detectedOnboardingTransports(cfg),
		PeerAuthToken:        managedToManagerPeerToken,
	}, &remote); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if strings.TrimSpace(remote.RequestID) == "" {
		writeError(w, http.StatusBadGateway, errors.New("manager pairing response was missing request id"))
		return
	}
	if strings.TrimSpace(remote.ManagedSwarmID) != "" && !strings.EqualFold(strings.TrimSpace(remote.ManagedSwarmID), managedSwarmID) {
		writeError(w, http.StatusBadGateway, fmt.Errorf("remote pairing target mismatch: requested managed swarm %s but manager stored %s", managedSwarmID, strings.TrimSpace(remote.ManagedSwarmID)))
		return
	}
	remote.AuthCode = ""
	ceremonyCode := firstNonEmpty(strings.TrimSpace(remote.CeremonyCode), strings.TrimSpace(req.CeremonyCode), strings.TrimSpace(offer.Ceremony.Code))
	writeJSON(w, http.StatusOK, swarmRemotePairingStartResponse{
		OK:      true,
		Request: remote,
		Ceremony: swarmRemotePairingCeremony{
			ManagedSwarmID:   managedSwarmID,
			ManagedName:      firstNonEmpty(strings.TrimSpace(remote.ManagedName), strings.TrimSpace(offer.SwarmName), status.Config.SwarmName),
			Code:             ceremonyCode,
			VerificationOnly: true,
			ChildSwarmID:     managedSwarmID,
			ChildName:        firstNonEmpty(strings.TrimSpace(remote.ManagedName), strings.TrimSpace(offer.SwarmName), status.Config.SwarmName),
			AuthCode:         ceremonyCode,
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
	var managerEndpoint string
	var managedSwarmID string
	defer func() {
		logSwarmPairingTiming(
			"remote_pairing.total",
			requestStartedAt,
			requestErr,
			"manager_swarm_id",
			firstNonEmpty(strings.TrimSpace(req.ManagerSwarmID), strings.TrimSpace(req.PrimarySwarmID)),
			"manager_endpoint",
			managerEndpoint,
			"managed_swarm_id",
			managedSwarmID,
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
		fail(http.StatusBadRequest, errors.New("manager-to-managed peer auth token is required"))
		return
	}
	managerSwarmID := firstNonEmpty(strings.TrimSpace(req.ManagerSwarmID), strings.TrimSpace(req.PrimarySwarmID))
	if managerSwarmID == "" {
		fail(http.StatusBadRequest, errors.New("manager swarm id is required"))
		return
	}
	managerEndpoint = normalizeRemoteSwarmEndpoint(firstNonEmpty(req.ManagerEndpoint, req.PrimaryEndpoint))
	offer := req.Offer
	if strings.TrimSpace(offer.Token) == "" {
		fail(http.StatusBadRequest, errors.New("managed pairing offer token is required"))
		return
	}
	expectedCode := deriveSwarmRemotePairingOfferCeremonyCode(offer)
	if expectedCode == "" || !strings.EqualFold(strings.TrimSpace(req.CeremonyCode), expectedCode) || !strings.EqualFold(strings.TrimSpace(offer.Ceremony.Code), expectedCode) {
		fail(http.StatusBadRequest, errors.New("managed pairing ceremony code does not match offer transcript"))
		return
	}
	if offer.ExpiresAt > 0 && offer.ExpiresAt < time.Now().Unix() {
		fail(http.StatusBadRequest, errors.New("managed pairing offer has expired"))
		return
	}

	stepStartedAt := time.Now()
	cfg, err := s.loadStartupConfig()
	logSwarmPairingTiming("remote_pairing.load_startup_config", stepStartedAt, err, "manager_swarm_id", managerSwarmID)
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
	logSwarmPairingTiming("remote_pairing.current_swarm_state", stepStartedAt, err, "manager_swarm_id", managerSwarmID)
	if err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if localSwarmID == "" {
		fail(http.StatusInternalServerError, errors.New("manager swarm identity is not configured"))
		return
	}
	if !strings.EqualFold(localSwarmID, managerSwarmID) {
		fail(http.StatusBadRequest, fmt.Errorf("pairing request targets manager swarm %s but this manager is %s", managerSwarmID, localSwarmID))
		return
	}
	managerName := firstNonEmpty(strings.TrimSpace(req.ManagerName), strings.TrimSpace(req.PrimaryName), strings.TrimSpace(cfg.SwarmName), strings.TrimSpace(state.Node.Name), "Manager")
	managerPublicKey := strings.TrimSpace(state.Node.PublicKey)
	managerFingerprint := firstNonEmpty(strings.TrimSpace(state.Node.Fingerprint), swarmruntime.FingerprintPublicKey(managerPublicKey))
	if managerEndpoint == "" {
		managerEndpoint = canonicalRemoteSwarmEndpoint(cfg, onboardingResponse{Tailscale: onboardingTailscalePayload{TailnetURL: strings.TrimSpace(cfg.TailscaleURL)}})
	}

	managedSwarmID = strings.TrimSpace(offer.SwarmID)
	managedName := firstNonEmpty(strings.TrimSpace(offer.SwarmName), "Managed swarm")
	managedPublicKey := strings.TrimSpace(offer.PublicKey)
	managedFingerprint := firstNonEmpty(strings.TrimSpace(offer.Fingerprint), swarmruntime.FingerprintPublicKey(managedPublicKey))
	managedEndpoint := normalizeRemoteSwarmEndpoint(offer.Endpoint)
	transportMode := firstNonEmpty(strings.TrimSpace(req.TransportMode), strings.TrimSpace(offer.TransportMode), bootstrapNetworkMode(cfg))
	if managedSwarmID == "" {
		fail(http.StatusBadRequest, errors.New("managed swarm identity is required"))
		return
	}
	if managedPublicKey == "" {
		fail(http.StatusBadRequest, errors.New("managed swarm public key is required"))
		return
	}
	if managedEndpoint == "" {
		fail(http.StatusBadRequest, errors.New("managed endpoint is required"))
		return
	}

	managerToManagedPeerToken := strings.TrimSpace(req.InviteToken)
	managedToManagerPeerToken := strings.TrimSpace(req.PeerAuthToken)
	if managedToManagerPeerToken == "" {
		managedToManagerPeerToken, err = swarmruntime.GeneratePeerAuthToken()
		if err != nil {
			fail(http.StatusInternalServerError, err)
			return
		}
	}
	managedTransports := append([]onboardingTransportPayload(nil), offer.RendezvousTransports...)
	if len(managedTransports) == 0 {
		managedTransports = []onboardingTransportPayload{{
			Kind:    firstNonEmpty(strings.TrimSpace(offer.TransportMode), transportMode, startupconfig.NetworkModeTailscale),
			Primary: managedEndpoint,
			All:     []string{managedEndpoint},
		}}
	}
	requestID, err := randomSwarmRemotePairingRequestID()
	if err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}
	pending := swarmRemotePairingPendingRequest{
		ID:                          requestID,
		InviteToken:                 strings.TrimSpace(req.InviteToken),
		ManagerSwarmID:              managerSwarmID,
		ManagerName:                 managerName,
		ManagerPublicKey:            managerPublicKey,
		ManagerFingerprint:          managerFingerprint,
		ManagerEndpoint:             managerEndpoint,
		ManagedSwarmID:              managedSwarmID,
		ManagedName:                 managedName,
		ManagedPublicKey:            managedPublicKey,
		ManagedFingerprint:          managedFingerprint,
		ManagedEndpoint:             managedEndpoint,
		CeremonyCode:                expectedCode,
		TransportMode:               transportMode,
		ManagerRendezvousTransports: detectedOnboardingTransports(cfg),
		ManagedRendezvousTransports: managedTransports,
		ManagerToManagedPeerToken:   managerToManagedPeerToken,
		ManagedToManagerPeerToken:   managedToManagerPeerToken,
		CreatedAt:                   time.Now(),
	}
	if s.remotePairingPending == nil {
		s.remotePairingPending = make(map[string]swarmRemotePairingPendingRequest)
	}
	s.remotePairingPending[requestID] = pending

	stepStartedAt = time.Now()
	if err := s.publishSwarmPairingEvent("swarm.managed_pairing.requested", managedSwarmID, map[string]any{
		"request_id":          requestID,
		"ceremony_code":       expectedCode,
		"manager_name":        pending.ManagerName,
		"manager_swarm_id":    managerSwarmID,
		"managed_name":        managedName,
		"managed_swarm_id":    managedSwarmID,
		"managed_fingerprint": managedFingerprint,
		"managed_endpoint":    managedEndpoint,
		"managed_transports":  pending.ManagedRendezvousTransports,
	}); err != nil {
		logSwarmPairingTiming("remote_pairing.publish_pairing_event", stepStartedAt, err, "managed_swarm_id", managedSwarmID, "manager_swarm_id", managerSwarmID)
		fail(http.StatusInternalServerError, err)
		return
	}
	logSwarmPairingTiming("remote_pairing.publish_pairing_event", stepStartedAt, nil, "managed_swarm_id", managedSwarmID, "manager_swarm_id", managerSwarmID)

	stepStartedAt = time.Now()
	writeJSON(w, http.StatusOK, swarmRemotePairingResponse{
		OK:                 true,
		RequestID:          requestID,
		Status:             startupconfig.PairingStatePendingApproval,
		ManagedSwarmID:     managedSwarmID,
		ManagedName:        managedName,
		ManagedPublicKey:   managedPublicKey,
		ManagedFingerprint: managedFingerprint,
		CeremonyCode:       expectedCode,
		ChildSwarmID:       managedSwarmID,
		ChildName:          managedName,
		AuthCode:           managedToManagerPeerToken,
	})
	logSwarmPairingTiming("remote_pairing.write_response", stepStartedAt, nil, "managed_swarm_id", managedSwarmID, "manager_swarm_id", managerSwarmID)
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
	managerSwarmID := firstNonEmpty(strings.TrimSpace(req.ManagerSwarmID), strings.TrimSpace(req.PrimarySwarmID))
	if managerSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("manager swarm id is required"))
		return
	}
	pairing, err := s.swarm.ApproveManagedPairing(swarmruntime.ApproveManagedPairingInput{
		ManagerSwarmID:        managerSwarmID,
		ManagerName:           firstNonEmpty(strings.TrimSpace(req.ManagerName), strings.TrimSpace(req.PrimaryName)),
		ManagerPublicKey:      firstNonEmpty(strings.TrimSpace(req.ManagerPublicKey), strings.TrimSpace(req.PrimaryPublicKey)),
		ManagerFingerprint:    firstNonEmpty(strings.TrimSpace(req.ManagerFingerprint), strings.TrimSpace(req.PrimaryFingerprint)),
		TransportMode:         strings.TrimSpace(req.TransportMode),
		RendezvousTransports:  onboardingTransportsToSwarm(req.RendezvousTransports),
		OutgoingPeerAuthToken: strings.TrimSpace(req.PeerAuthToken),
		IncomingPeerAuthToken: strings.TrimSpace(req.IncomingPeerAuthToken),
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

func (s *Server) handleSwarmRemotePairingPending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	items := make([]swarmRemotePairingPendingItem, 0, len(s.remotePairingPending))
	for _, pending := range s.remotePairingPending {
		items = append(items, swarmRemotePairingPendingItem{
			RequestID:          pending.ID,
			Status:             startupconfig.PairingStatePendingApproval,
			ManagerSwarmID:     pending.ManagerSwarmID,
			ManagerName:        pending.ManagerName,
			ManagerEndpoint:    pending.ManagerEndpoint,
			ManagedSwarmID:     pending.ManagedSwarmID,
			ManagedName:        pending.ManagedName,
			ManagedFingerprint: pending.ManagedFingerprint,
			ManagedEndpoint:    pending.ManagedEndpoint,
			CeremonyCode:       pending.CeremonyCode,
			TransportMode:      pending.TransportMode,
			Rendezvous:         append([]onboardingTransportPayload(nil), pending.ManagedRendezvousTransports...),
			CreatedAt:          pending.CreatedAt.Unix(),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt < items[j].CreatedAt })
	writeJSON(w, http.StatusOK, swarmRemotePairingPendingResponse{OK: true, Items: items, Count: len(items)})
}

func (s *Server) handleSwarmRemotePairingApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	var req swarmRemotePairingApprovalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("pairing request id is required"))
		return
	}
	pending, ok := s.remotePairingPending[requestID]
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("pairing request not found"))
		return
	}
	if !req.Approve {
		delete(s.remotePairingPending, requestID)
		_ = s.publishSwarmPairingEvent("swarm.managed_pairing.rejected", pending.ManagedSwarmID, map[string]any{
			"request_id":       pending.ID,
			"manager_swarm_id": pending.ManagerSwarmID,
			"managed_swarm_id": pending.ManagedSwarmID,
			"reason":           strings.TrimSpace(req.Reason),
		})
		writeJSON(w, http.StatusOK, swarmRemotePairingApprovalResponse{OK: true, Status: startupconfig.PairingStateRejected, RequestID: requestID})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(req.CeremonyCode), strings.TrimSpace(pending.CeremonyCode)) {
		writeError(w, http.StatusBadRequest, errors.New("managed pairing ceremony code mismatch"))
		return
	}

	invite, err := s.swarm.CreateInvite(swarmruntime.CreateInviteInput{
		PrimarySwarmID:       pending.ManagerSwarmID,
		PrimaryName:          pending.ManagerName,
		TransportMode:        pending.TransportMode,
		RendezvousTransports: onboardingTransportsToSwarm(pending.ManagedRendezvousTransports),
		TTL:                  15 * time.Minute,
		Token:                pending.InviteToken,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	enrollment, err := s.swarm.SubmitEnrollment(swarmruntime.SubmitEnrollmentInput{
		InviteToken:           pending.InviteToken,
		PrimarySwarmID:        pending.ManagerSwarmID,
		ChildSwarmID:          pending.ManagedSwarmID,
		ChildName:             pending.ManagedName,
		ChildRole:             swarmruntime.RelationshipManaged,
		ChildPublicKey:        pending.ManagedPublicKey,
		TransportMode:         pending.TransportMode,
		RendezvousTransports:  onboardingTransportsToSwarm(pending.ManagedRendezvousTransports),
		IncomingPeerAuthToken: pending.ManagerToManagedPeerToken,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(enrollment.ID) == "" {
		writeError(w, http.StatusInternalServerError, errors.New("manager enrollment response was missing enrollment data"))
		return
	}
	enrollment, _, err = s.swarm.DecideEnrollment(swarmruntime.DecideEnrollmentInput{EnrollmentID: enrollment.ID, Approve: true, Reason: "managed pairing approved", IncomingPeerAuthToken: pending.ManagedToManagerPeerToken})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pairing := swarmruntime.PairingState{
		PairingState:         startupconfig.PairingStatePaired,
		ParentSwarmID:        pending.ManagerSwarmID,
		LastEnrollmentID:     strings.TrimSpace(enrollment.ID),
		LastDecision:         startupconfig.PairingStatePaired,
		LastUpdatedByRole:    "managed",
		RendezvousTransports: onboardingTransportsToSwarm(pending.ManagerRendezvousTransports),
	}
	routing := swarmManagedPairingRouting{
		ManagedSwarmID:       pending.ManagedSwarmID,
		ManagedName:          pending.ManagedName,
		Relationship:         swarmruntime.RelationshipManaged,
		BackendURL:           firstNonEmpty(pending.ManagedEndpoint, normalizeRemoteSwarmEndpoint(firstTransportForKind(pending.ManagedRendezvousTransports, startupconfig.NetworkModeTailscale))),
		TransportMode:        pending.TransportMode,
		RepresentAsLocal:     false,
		ContainerScope:       "managed_host_local",
		ProxyPathPrefix:      "/v1/swarm/containers/local",
		RendezvousTransports: append([]onboardingTransportPayload(nil), pending.ManagedRendezvousTransports...),
	}
	delete(s.remotePairingPending, requestID)
	_ = s.publishSwarmPairingEvent("swarm.managed_pairing.approved", pending.ManagedSwarmID, map[string]any{
		"request_id":       pending.ID,
		"ceremony_code":    pending.CeremonyCode,
		"manager_name":     pending.ManagerName,
		"manager_swarm_id": pending.ManagerSwarmID,
		"managed_name":     pending.ManagedName,
		"managed_swarm_id": pending.ManagedSwarmID,
		"enrollment_id":    strings.TrimSpace(enrollment.ID),
	})
	writeJSON(w, http.StatusOK, swarmRemotePairingApprovalResponse{OK: true, Status: startupconfig.PairingStatePaired, RequestID: requestID, Invite: invite, Pairing: pairing, Enrollment: enrollment, Routing: &routing})
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

	response := swarmDiscoveryResponse{
		OK:      true,
		SwarmID: strings.TrimSpace(state.Node.SwarmID),
		Name:    firstNonEmpty(strings.TrimSpace(cfg.SwarmName), strings.TrimSpace(state.Node.Name), "Unnamed swarm"),
	}
	if !swarmModeEnabled(cfg) {
		response.Role = bootstrapRoleStandalone
		response = redactSensitiveSwarmDiscoveryResponse(response, s.allowSensitiveOnboardingMetadata(r))
		writeJSON(w, http.StatusOK, response)
		return
	}
	transports := detectedOnboardingTransports(cfg)
	tailscale := detectTailscale()
	status := onboardingResponse{Tailscale: tailscale}
	response.Role = localSwarmRole(cfg)
	response.Endpoint = canonicalRemoteSwarmEndpoint(cfg, status)
	response.TransportMode = bootstrapNetworkMode(cfg)
	response.RendezvousTransports = transports
	response = redactSensitiveSwarmDiscoveryResponse(response, s.allowSensitiveOnboardingMetadata(r))
	writeJSON(w, http.StatusOK, response)
}

func redactSensitiveSwarmDiscoveryResponse(response swarmDiscoveryResponse, allowSensitive bool) swarmDiscoveryResponse {
	if allowSensitive {
		return response
	}
	response.SwarmID = ""
	response.Name = ""
	response.Role = ""
	response.Endpoint = ""
	response.TransportMode = ""
	response.RendezvousTransports = nil
	return response
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

func postRemoteSwarmJSONWithTransportFallback(endpoint, requestPath string, transports []onboardingTransportPayload, payload any, out any) error {
	return remoteSwarmJSONRequestWithTransportFallback(http.MethodPost, endpoint, requestPath, transports, payload, out)
}

func getRemoteSwarmJSONWithTransportFallback(endpoint, requestPath string, transports []onboardingTransportPayload, out any) error {
	return remoteSwarmJSONRequestWithTransportFallback(http.MethodGet, endpoint, requestPath, transports, nil, out)
}

func fetchSwarmRemotePairingOffer(endpoint string, transports []onboardingTransportPayload) (swarmRemotePairingOfferPayload, error) {
	var response swarmRemotePairingOfferResponse
	if err := postRemoteSwarmJSONWithTransportFallback(endpoint, "/v1/swarm/remote-pairing/offer", transports, swarmRemotePairingOfferCreateRequest{}, &response); err != nil {
		return swarmRemotePairingOfferPayload{}, err
	}
	if !response.OK {
		return swarmRemotePairingOfferPayload{}, errors.New("managed swarm offer response was not ok")
	}
	return response.Offer, nil
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
	if len(req.Offer.RendezvousTransports) > 0 {
		return append([]onboardingTransportPayload(nil), req.Offer.RendezvousTransports...)
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

func randomSwarmRemotePairingRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "pair_" + hex.EncodeToString(buf), nil
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
