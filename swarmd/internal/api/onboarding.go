package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

const (
	bootstrapRoleStandalone = "standalone"
	bootstrapRoleMaster     = "master"
	bootstrapRoleChild      = "child"
	bootstrapRoleManaged    = startupconfig.SwarmRoleManaged
)

type onboardingTransportPayload struct {
	Kind    string   `json:"kind"`
	Primary string   `json:"primary,omitempty"`
	All     []string `json:"all,omitempty"`
}

type onboardingConfigPayload struct {
	SwarmName                 string `json:"swarm_name"`
	DesktopOnboardingComplete bool   `json:"desktop_onboarding_complete"`
	Child                     bool   `json:"child"`
	SwarmRole                 string `json:"swarm_role,omitempty"`
	Mode                      string `json:"mode"`
	Host                      string `json:"host,omitempty"`
	Port                      int    `json:"port"`
	DesktopPort               int    `json:"desktop_port"`
	AdvertiseHost             string `json:"advertise_host,omitempty"`
	AdvertisePort             int    `json:"advertise_port"`
	TailscaleURL              string `json:"tailscale_url,omitempty"`
	BypassPermissions         bool   `json:"bypass_permissions,omitempty"`
	DevMode                   bool   `json:"dev_mode,omitempty"`
	PeerTransportPort         int    `json:"peer_transport_port"`
	RestartRequired           bool   `json:"restart_required,omitempty"`
	RestartReason             string `json:"restart_reason,omitempty"`
}

type onboardingHeuristicsPayload struct {
	MissingSwarmName    bool `json:"missing_swarm_name"`
	CredentialCount     int  `json:"credential_count"`
	AgentCount          int  `json:"agent_count"`
	SavedWorkspaceCount int  `json:"saved_workspace_count"`
	VaultConfigured     bool `json:"vault_configured"`
}

type onboardingTailscaleServePayload struct {
	Configured                 bool   `json:"configured"`
	Ready                      bool   `json:"ready"`
	Mode                       string `json:"mode,omitempty"`
	URL                        string `json:"url,omitempty"`
	ProxyTarget                string `json:"proxy_target,omitempty"`
	ExpectedDesktopProxy       string `json:"expected_desktop_proxy,omitempty"`
	ExpectedAPIProxy           string `json:"expected_api_proxy,omitempty"`
	ExpectedPeerTransportProxy string `json:"expected_peer_transport_proxy,omitempty"`
	Command                    string `json:"command,omitempty"`
	Error                      string `json:"error,omitempty"`
}

type onboardingIdentityPayload struct {
	Bootstrapped    bool   `json:"bootstrapped"`
	UserID          string `json:"user_id,omitempty"`
	AccountScopeID  string `json:"account_scope_id,omitempty"`
	Username        string `json:"username,omitempty"`
	TeamID          string `json:"team_id,omitempty"`
	TeamDisplayName string `json:"team_display_name,omitempty"`
	TeamDefault     bool   `json:"team_default,omitempty"`
	MembershipRole  string `json:"membership_role,omitempty"`
}

type onboardingSessionPayload struct {
	ExpiresAt string `json:"expires_at,omitempty"`
}

type onboardingTailscalePayload struct {
	Available    bool                            `json:"available"`
	Connected    bool                            `json:"connected"`
	DNSName      string                          `json:"dns_name,omitempty"`
	TailnetName  string                          `json:"tailnet_name,omitempty"`
	TailnetURL   string                          `json:"tailnet_url,omitempty"`
	CandidateURL string                          `json:"candidate_url,omitempty"`
	IPs          []string                        `json:"ips"`
	AuthURL      string                          `json:"auth_url,omitempty"`
	Error        string                          `json:"error,omitempty"`
	Serve        onboardingTailscaleServePayload `json:"serve"`
}

type onboardingDiscoveredSwarmPayload struct {
	ID                   string                       `json:"id,omitempty"`
	Name                 string                       `json:"name,omitempty"`
	Role                 string                       `json:"role,omitempty"`
	Endpoint             string                       `json:"endpoint,omitempty"`
	TailnetURL           string                       `json:"tailnet_url,omitempty"`
	DNSName              string                       `json:"dns_name,omitempty"`
	IPs                  []string                     `json:"ips,omitempty"`
	Online               bool                         `json:"online"`
	Source               string                       `json:"source,omitempty"`
	Running              bool                         `json:"running"`
	InCurrentGroup       bool                         `json:"in_current_group,omitempty"`
	CurrentRelationship  string                       `json:"current_relationship,omitempty"`
	TransportMode        string                       `json:"transport_mode,omitempty"`
	RendezvousTransports []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
}

type onboardingResponse struct {
	OK              bool                        `json:"ok"`
	NeedsOnboarding bool                        `json:"needs_onboarding"`
	Identity        onboardingIdentityPayload   `json:"identity"`
	Session         *onboardingSessionPayload   `json:"session,omitempty"`
	Config          onboardingConfigPayload     `json:"config"`
	Heuristics      onboardingHeuristicsPayload `json:"heuristics"`
	Tailscale       onboardingTailscalePayload  `json:"tailscale"`
}

type onboardingUpdateRequest struct {
	Username                  *string `json:"username,omitempty"`
	SwarmName                 *string `json:"swarm_name,omitempty"`
	DesktopOnboardingComplete *bool   `json:"desktop_onboarding_complete,omitempty"`
	Child                     *bool   `json:"child,omitempty"`
	Mode                      *string `json:"mode,omitempty"`
	Port                      *int    `json:"port,omitempty"`
	AdvertiseHost             *string `json:"advertise_host,omitempty"`
	AdvertisePort             *int    `json:"advertise_port,omitempty"`
	TailscaleURL              *string `json:"tailscale_url,omitempty"`
	PeerTransportPort         *int    `json:"peer_transport_port,omitempty"`
}

type onboardingProviderCredentialRequest struct {
	Provider     string   `json:"provider"`
	Type         string   `json:"type"`
	Label        string   `json:"label"`
	Tags         []string `json:"tags"`
	APIKey       string   `json:"api_key"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresAt    int64    `json:"expires_at"`
	AccountID    string   `json:"account_id"`
	Active       *bool    `json:"active,omitempty"`
}

type tailscalePeerStatusWire struct {
	DNSName        string   `json:"DNSName"`
	OS             string   `json:"OS"`
	TailscaleIPs   []string `json:"TailscaleIPs"`
	Online         bool     `json:"Online"`
	Active         bool     `json:"Active"`
	Self           bool     `json:"Self"`
	ExitNode       bool     `json:"ExitNode"`
	ExitNodeOption bool     `json:"ExitNodeOption"`
}

type tailscaleStatusWire struct {
	BackendState   string `json:"BackendState"`
	AuthURL        string `json:"AuthURL"`
	CurrentTailnet struct {
		Name string `json:"Name"`
	} `json:"CurrentTailnet"`
	Self struct {
		DNSName      string   `json:"DNSName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		Online       bool     `json:"Online"`
	} `json:"Self"`
	Peer map[string]tailscalePeerStatusWire `json:"Peer"`
}

type tailscaleServeStatusWire struct {
	Web map[string]tailscaleServeWebStatusWire `json:"Web"`
}

type tailscaleServeWebStatusWire struct {
	Handlers map[string]tailscaleServeHandlerWire `json:"Handlers"`
}

type tailscaleServeHandlerWire struct {
	Proxy string `json:"Proxy"`
}

type remoteSwarmDiscoverySeed struct {
	Source        string
	Name          string
	Endpoint      string
	TailnetURL    string
	DNSName       string
	IPs           []string
	Online        bool
	Probe         bool
	TransportMode string
	Transports    []onboardingTransportPayload
}

func discoverTailscaleSwarmSeeds(tailscaleStatus *tailscaleStatusWire) []remoteSwarmDiscoverySeed {
	if tailscaleStatus == nil || len(tailscaleStatus.Peer) == 0 {
		return nil
	}
	keys := make([]string, 0, len(tailscaleStatus.Peer))
	for key := range tailscaleStatus.Peer {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seeds := make([]remoteSwarmDiscoverySeed, 0, len(keys))
	for _, key := range keys {
		peer := tailscaleStatus.Peer[key]
		if peer.Self {
			continue
		}
		dnsName := strings.TrimSuffix(strings.TrimSpace(peer.DNSName), ".")
		ips := dedupeTransportStrings(peer.TailscaleIPs)
		transports := []onboardingTransportPayload{{
			Kind:    startupconfig.NetworkModeTailscale,
			Primary: firstNonEmptyTransport(dnsName, firstString(ips)),
			All:     dedupeTransportStrings(append([]string{dnsName}, ips...)),
		}}
		seeds = append(seeds, remoteSwarmDiscoverySeed{
			Source:        startupconfig.NetworkModeTailscale,
			Name:          tailscalePeerDisplayName(dnsName),
			Endpoint:      remoteSwarmProbeEndpoint(startupconfig.NetworkModeTailscale, dnsName, ips),
			TailnetURL:    tailscalePeerURL(dnsName),
			DNSName:       dnsName,
			IPs:           ips,
			Online:        peer.Online || peer.Active,
			Probe:         peer.Online || peer.Active,
			TransportMode: startupconfig.NetworkModeTailscale,
			Transports:    transports,
		})
	}
	return seeds
}

func fetchRemoteSwarmDiscovery(seed remoteSwarmDiscoverySeed) (swarmDiscoveryResponse, error) {
	var remote swarmDiscoveryResponse
	if err := getRemoteSwarmJSONWithTransportFallback(seed.Endpoint, "/v1/swarm/discovery", seed.Transports, &remote); err != nil {
		return swarmDiscoveryResponse{}, err
	}
	return remote, nil
}

func remoteSwarmProbeEndpoint(mode, dnsName string, ips []string) string {
	dnsName = strings.TrimSpace(dnsName)
	if dnsName != "" {
		return normalizeRemoteSwarmEndpoint(dnsName)
	}
	return ""
}

func tailscalePeerDisplayName(dnsName string) string {
	dnsName = strings.TrimSpace(strings.TrimSuffix(dnsName, "."))
	if dnsName == "" {
		return ""
	}
	parts := strings.Split(dnsName, ".")
	if len(parts) == 0 {
		return dnsName
	}
	return strings.TrimSpace(parts[0])
}

func tailscalePeerURL(dnsName string) string {
	dnsName = strings.TrimSpace(strings.TrimSuffix(dnsName, "."))
	if dnsName == "" {
		return ""
	}
	return "https://" + dnsName
}

func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	includeSensitive := s.allowSensitiveOnboardingMetadata(r)
	switch r.Method {
	case http.MethodGet:
		response, err := s.onboardingResponse(includeSensitive)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		var req onboardingUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		response, issued, err := s.updateOnboarding(req, includeSensitive)
		if err != nil {
			writeError(w, onboardingErrorStatus(err), err)
			return
		}
		if issued != nil {
			http.SetCookie(w, buildDesktopLocalSessionCookie(issued.Token, issued.ExpiresAt, requestScheme(r) == "https"))
		}
		writeJSON(w, http.StatusOK, response)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleOnboardingProviderCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrProductIdentityRequired)
		return
	}
	var req onboardingProviderCredentialRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.acceptFirstOnboardingProviderCredential(r.Context(), principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) allowSensitiveOnboardingMetadata(r *http.Request) bool {
	if r == nil {
		return false
	}
	if isLocalTransportRequest(r) {
		return true
	}
	if shouldUseDesktopLocalSessionAuth(r) && s != nil {
		if _, ok := s.actorFromDesktopLocalSession(r); ok {
			return true
		}
	}
	if isLocalTransportRequest(r) {
		return true
	}
	if s != nil && s.security != nil {
		ok, err := s.security.ValidateAttachToken(extractAttachToken(r))
		if err == nil && ok {
			return true
		}
	}
	if s != nil && s.swarm != nil {
		peerSwarmID, peerToken := extractPeerAuth(r)
		if strings.TrimSpace(peerSwarmID) != "" && strings.TrimSpace(peerToken) != "" {
			ok, err := s.swarm.ValidateIncomingPeerAuth(peerSwarmID, peerToken)
			if err == nil && ok {
				return true
			}
		}
	}
	return false
}

func (s *Server) onboardingResponse(includeSensitive bool) (onboardingResponse, error) {
	return s.onboardingResponseWithServeDetection(includeSensitive, true)
}

func (s *Server) onboardingResponseWithServeDetection(includeSensitive bool, detectServe bool) (onboardingResponse, error) {
	return s.onboardingResponseWithServeDetectionAndLocalLinkState(includeSensitive, detectServe, nil)
}

func (s *Server) onboardingResponseWithServeDetectionAndLocalLinkState(includeSensitive bool, detectServe bool, localLinkState *localManagedLinkState) (onboardingResponse, error) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return onboardingResponse{}, err
	}
	if !cfg.Exists {
		cfg = startupconfig.Default(cfg.Path)
	}
	vaultStatus, err := s.readVaultStatus()
	if err != nil {
		return onboardingResponse{}, err
	}
	identityPayload, identityBootstrapped, err := s.onboardingIdentityPayload()
	if err != nil {
		return onboardingResponse{}, err
	}
	credentialList, err := s.readCredentialList(identityPayload.AccountScopeID)
	if err != nil {
		return onboardingResponse{}, err
	}
	agentCount, err := s.readAgentCount(identityPayload.AccountScopeID)
	if err != nil {
		return onboardingResponse{}, err
	}
	savedCount, err := s.readSavedWorkspaceCount(identityPayload.AccountScopeID, identityPayload.UserID)
	if err != nil {
		return onboardingResponse{}, err
	}
	needsOnboarding := shouldShowOnboarding(cfg, identityBootstrapped)
	tailscale, _ := detectTailscaleWithStatus()
	if !needsOnboarding {
		tailscale.TailnetURL = firstNonEmpty(strings.TrimSpace(cfg.TailscaleURL), strings.TrimSpace(tailscale.TailnetURL))
		tailscale.Available = tailscale.Available || tailscale.TailnetURL != ""
	}
	if localLinkState == nil {
		resolvedLocalLinkState, err := s.onboardingLocalManagedLinkState(cfg)
		if err != nil {
			return onboardingResponse{}, err
		}
		localLinkState = &resolvedLocalLinkState
	}
	response := onboardingResponse{
		OK:              true,
		NeedsOnboarding: needsOnboarding,
		Identity:        identityPayload,
		Config: onboardingConfigPayload{
			SwarmName:                 strings.TrimSpace(cfg.SwarmName),
			DesktopOnboardingComplete: cfg.DesktopOnboardingComplete,
			Child:                     localLinkState.Managed,
			SwarmRole:                 localLinkState.Role,
			Host:                      strings.TrimSpace(cfg.Host),
			Port:                      cfg.Port,
			DesktopPort:               cfg.DesktopPort,
			AdvertiseHost:             strings.TrimSpace(cfg.AdvertiseHost),
			AdvertisePort:             canonicalAdvertisePort(cfg),
			TailscaleURL:              strings.TrimSpace(cfg.TailscaleURL),
			BypassPermissions:         cfg.BypassPermissions,
			DevMode:                   cfg.DevMode,
			PeerTransportPort:         cfg.PeerTransportPort,
		},
		Heuristics: onboardingHeuristicsPayload{
			MissingSwarmName:    strings.TrimSpace(cfg.SwarmName) == "",
			CredentialCount:     credentialList.Total,
			AgentCount:          agentCount,
			SavedWorkspaceCount: savedCount,
			VaultConfigured:     vaultStatus.Enabled,
		},
		Tailscale: tailscale,
	}
	if !includeSensitive {
		response.Config = redactSensitiveOnboardingConfig(response.Config)
		response.Tailscale = redactSensitiveOnboardingTailscale(response.Tailscale)
		return response, nil
	}
	response.Tailscale.CandidateURL = tailscaleCandidateURL(cfg, tailscale)
	if detectServe && shouldDetectTailscaleServeForOnboarding(cfg, response.Tailscale) {
		response.Tailscale.Serve = detectTailscaleServe(cfg, response.Tailscale)
	} else {
		response.Tailscale.Serve = expectedTailscaleServe(cfg, response.Tailscale)
	}
	// Keep first-launch onboarding fast: remote swarm discovery probes peers and
	// should not block the initial setup screen. Discovery can be loaded by
	// explicit swarm-management screens instead of the base onboarding status.
	return response, nil
}

func redactSensitiveOnboardingConfig(config onboardingConfigPayload) onboardingConfigPayload {
	config.Host = ""
	config.AdvertiseHost = ""
	config.AdvertisePort = 0
	config.TailscaleURL = ""
	config.PeerTransportPort = 0
	return config
}

func redactSensitiveOnboardingTailscale(payload onboardingTailscalePayload) onboardingTailscalePayload {
	return onboardingTailscalePayload{
		Available: payload.Available,
		Connected: payload.Connected,
		Error:     strings.TrimSpace(payload.Error),
		Serve:     onboardingTailscaleServePayload{},
	}
}

func (s *Server) onboardingIdentityPayload() (onboardingIdentityPayload, bool, error) {
	if s == nil || s.identityService == nil {
		return onboardingIdentityPayload{}, false, nil
	}
	summary, err := s.identityService.StateSummary()
	if err != nil {
		return onboardingIdentityPayload{}, false, err
	}
	if summary.CurrentUser == nil || summary.AccountScope == nil || summary.CurrentSelection == nil {
		return onboardingIdentityPayload{Bootstrapped: false}, false, nil
	}
	payload := onboardingIdentityPayload{
		Bootstrapped:   true,
		UserID:         summary.CurrentUser.ID,
		AccountScopeID: summary.CurrentUser.AccountScopeID,
		Username:       summary.CurrentUser.Username,
	}
	if summary.CurrentTeam != nil {
		payload.TeamID = summary.CurrentTeam.ID
		payload.TeamDisplayName = summary.CurrentTeam.Name
		payload.TeamDefault = summary.CurrentTeam.Default
	}
	if summary.CurrentMembership != nil {
		payload.MembershipRole = summary.CurrentMembership.Role
	}
	return payload, true, nil
}

func actorOnboardingIdentityPayload(actor identity.ActorContext) onboardingIdentityPayload {
	return onboardingIdentityPayload{
		Bootstrapped:    strings.TrimSpace(actor.UserID) != "",
		UserID:          actor.UserID,
		AccountScopeID:  actor.AccountScopeID,
		Username:        actor.User.Username,
		TeamID:          actor.TeamID,
		TeamDisplayName: actor.Team.Name,
		TeamDefault:     actor.Team.Default,
		MembershipRole:  actor.Membership.Role,
	}
}

func onboardingErrorStatus(err error) int {
	switch {
	case errors.Is(err, identity.ErrBootstrapExists):
		return http.StatusConflict
	case errors.Is(err, identity.ErrProductIdentityRequired), errors.Is(err, identity.ErrServiceNotConfigured), errors.Is(err, identity.ErrSessionServiceNotConfigured):
		return http.StatusUnauthorized
	default:
		return http.StatusBadRequest
	}
}

func (s *Server) updateOnboarding(req onboardingUpdateRequest, includeSensitive bool) (onboardingResponse, *identity.IssuedSession, error) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return onboardingResponse{}, nil, err
	}
	if !cfg.Exists {
		cfg = startupconfig.Default(cfg.Path)
	}

	identityPayload, identityBootstrapped, err := s.onboardingIdentityPayload()
	if err != nil {
		return onboardingResponse{}, nil, err
	}
	bootstrapUsername := ""
	if req.Username != nil {
		bootstrapUsername = strings.TrimSpace(*req.Username)
	}
	if !identityBootstrapped && bootstrapUsername == "" {
		return onboardingResponse{}, nil, errors.New("username is required for product identity bootstrap")
	}
	if !identityBootstrapped && (req.SwarmName == nil || strings.TrimSpace(*req.SwarmName) == "") {
		return onboardingResponse{}, nil, errors.New("swarm name is required for daemon identity")
	}
	if identityBootstrapped && bootstrapUsername != "" {
		return onboardingResponse{}, nil, identity.ErrBootstrapExists
	}

	updated := cfg
	changed := false
	turnedOffChildMode := false
	restartRequired := false
	var restartReasons []string

	if req.SwarmName != nil {
		updated.SwarmName = strings.TrimSpace(*req.SwarmName)
		if updated.SwarmName == "" && !requestChangesSwarmShape(req) {
			return onboardingResponse{}, nil, errors.New("swarm name is required")
		}
		changed = true
	}
	if req.DesktopOnboardingComplete != nil {
		updated.DesktopOnboardingComplete = *req.DesktopOnboardingComplete
		updated.DesktopOnboardingCompleteSet = true
		changed = true
	}
	if req.Child != nil {
		if *req.Child {
			return onboardingResponse{}, nil, errors.New("primary onboarding cannot enable child mode; use Link Swarm pairing to make this swarm managed")
		}
		updated.Child = false
		turnedOffChildMode = cfg.Child
		if turnedOffChildMode {
			updated = startupconfig.ScrubManagedLinkState(updated)
			updated.PairingState = startupconfig.PairingStateUnpaired
		}
		changed = true
	}
	if req.Mode != nil {
		return onboardingResponse{}, nil, errors.New("mode was removed; use tailscale_url for explicit pairing endpoint configuration")
	}
	if req.Port != nil {
		updated.Port = *req.Port
		if updated.Port < 1 || updated.Port > 65535 {
			return onboardingResponse{}, nil, fmt.Errorf("port must be between %d and %d", 1, 65535)
		}
		changed = true
	}
	if req.AdvertiseHost != nil {
		updated.AdvertiseHost = strings.TrimSpace(*req.AdvertiseHost)
		changed = true
	}
	if req.AdvertisePort != nil {
		updated.AdvertisePort = *req.AdvertisePort
		if updated.AdvertisePort < 1 || updated.AdvertisePort > 65535 {
			return onboardingResponse{}, nil, fmt.Errorf("advertise_port must be between %d and %d", 1, 65535)
		}
		changed = true
	}
	if req.TailscaleURL != nil {
		updated.TailscaleURL = strings.TrimSpace(*req.TailscaleURL)
		changed = true
		restartRequired = true
		restartReasons = append(restartReasons, "peer transport endpoint changed")
	}
	if strings.TrimSpace(updated.TailscaleURL) == "" {
		if tailscale, _ := detectTailscaleWithStatus(); strings.TrimSpace(tailscale.TailnetURL) != "" {
			updated.TailscaleURL = strings.TrimSpace(tailscale.TailnetURL)
			changed = true
		}
	}
	if req.PeerTransportPort != nil {
		updated.PeerTransportPort = *req.PeerTransportPort
		if updated.PeerTransportPort < 1 || updated.PeerTransportPort > 65535 {
			return onboardingResponse{}, nil, fmt.Errorf("peer_transport_port must be between %d and %d", 1, 65535)
		}
		changed = true
		restartRequired = true
		restartReasons = append(restartReasons, "peer transport port changed")
	}
	if strings.TrimSpace(updated.SwarmName) == "" && identityBootstrapped {
		updated.SwarmName = defaultOnboardingSwarmName(updated)
		changed = true
	}
	if !changed && bootstrapUsername == "" {
		return onboardingResponse{}, nil, errors.New("no onboarding fields were provided")
	}
	if !identityBootstrapped && strings.TrimSpace(updated.SwarmName) == "" {
		return onboardingResponse{}, nil, errors.New("swarm name is required for daemon identity")
	}
	if err := startupconfig.Write(updated); err != nil {
		return onboardingResponse{}, nil, err
	}
	if turnedOffChildMode && s.swarm != nil {
		state, err := s.currentSwarmState(cfg)
		if err != nil {
			return onboardingResponse{}, nil, err
		}
		if err := s.swarm.DetachToStandalone(strings.TrimSpace(state.Node.SwarmID)); err != nil {
			return onboardingResponse{}, nil, err
		}
	}
	var responseLocalLinkState *localManagedLinkState
	if req.SwarmName != nil {
		if s.swarm != nil {
			state, err := s.currentSwarmState(updated)
			if err != nil {
				return onboardingResponse{}, nil, err
			}
			linkState := dbBackedLocalManagedLinkState(state)
			responseLocalLinkState = &linkState
		}
		if err := s.persistUISwarmName(updated.SwarmName); err != nil {
			return onboardingResponse{}, nil, err
		}
	}

	var issued *identity.IssuedSession
	if !identityBootstrapped {
		if s.identityService == nil {
			return onboardingResponse{}, nil, identity.ErrServiceNotConfigured
		}
		if s.identitySessions == nil {
			return onboardingResponse{}, nil, identity.ErrSessionServiceNotConfigured
		}
		if _, err := s.identityService.BootstrapFirstIdentity(bootstrapUsername); err != nil {
			return onboardingResponse{}, nil, err
		}
		createdSession, err := s.identitySessions.IssueForCurrentSelection()
		if err != nil {
			return onboardingResponse{}, nil, err
		}
		issued = &createdSession
		identityPayload = actorOnboardingIdentityPayload(createdSession.Actor)
	}
	if err := s.ensureOnboardingPrimaryTopology(identityPayload); err != nil {
		return onboardingResponse{}, nil, err
	}
	response, err := s.onboardingResponseWithServeDetectionAndLocalLinkState(includeSensitive, true, responseLocalLinkState)
	if err != nil {
		return onboardingResponse{}, nil, err
	}
	if issued != nil {
		response.Identity = identityPayload
		response.Session = &onboardingSessionPayload{ExpiresAt: issued.ExpiresAt.UTC().Format(time.RFC3339)}
	}
	if restartRequired {
		response.Config.RestartRequired = true
		response.Config.RestartReason = strings.Join(dedupeTransportStrings(restartReasons), "; ")
	}
	return response, issued, nil
}

func (s *Server) loadStartupConfig() (startupconfig.FileConfig, error) {
	path := strings.TrimSpace(s.startupConfigPath)
	if path == "" {
		var err error
		path, err = startupconfig.ResolvePath()
		if err != nil {
			return startupconfig.FileConfig{}, err
		}
	}
	return startupconfig.Load(path)
}

func (s *Server) readVaultStatus() (auth.VaultStatus, error) {
	if s == nil || s.auth == nil {
		return auth.VaultStatus{}, errors.New("auth service not configured")
	}
	return s.auth.VaultStatus()
}

func (s *Server) readCredentialList(accountScopeID string) (auth.CredentialList, error) {
	if s == nil || s.auth == nil {
		return auth.CredentialList{}, errors.New("auth service not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		// Onboarding status is intentionally public before product identity exists.
		// Do not read account credential data through the removed legacy global path.
		return auth.CredentialList{}, nil
	}
	return s.auth.ListCredentialsForAccount(accountScopeID, "", "", 200)
}

func (s *Server) persistUISwarmName(name string) error {
	if s == nil || s.uiSettings == nil {
		return errors.New("ui settings service is not configured")
	}
	settings, err := s.uiSettings.Get()
	if err != nil {
		return err
	}
	settings.Swarm.Name = strings.TrimSpace(name)
	_, err = s.uiSettings.Set(settings)
	return err
}

func (s *Server) readAgentCount(accountScopeID string) (int, error) {
	if s == nil || s.agents == nil || strings.TrimSpace(accountScopeID) == "" {
		return 0, nil
	}
	state, err := s.agents.ListStateForAccount(accountScopeID, 2000)
	if err != nil {
		return 0, err
	}
	return len(state.Profiles), nil
}

func (s *Server) readSavedWorkspaceCount(accountScopeID, userID string) (int, error) {
	if s == nil || s.workspace == nil {
		return 0, errors.New("workspace service not configured")
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: strings.TrimSpace(userID), AccountScopeID: strings.TrimSpace(accountScopeID)}
	if !principal.Valid() {
		// Onboarding status can be requested before a trusted product principal exists.
		// Do not read account-scoped workspace data through legacy global access here.
		return 0, nil
	}
	entries, err := s.workspace.ListKnownForPrincipal(principal, 200)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func (s *Server) ensureOnboardingPrimaryTopology(identityPayload onboardingIdentityPayload) error {
	if s == nil || s.topology == nil || s.workspace == nil {
		return nil
	}
	principal := identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             strings.TrimSpace(identityPayload.UserID),
		AccountScopeID:     strings.TrimSpace(identityPayload.AccountScopeID),
		AccountScopeSource: identity.AccountScopeSourceServerState,
	}
	if !principal.Valid() {
		return nil
	}
	placement, err := s.topology.EnsureLocalSelfPlacementForPrincipal(principal.AccountScopeID, principal.UserID)
	if err != nil {
		return err
	}
	if !isPrimarySelfPlacement(placement) {
		return fmt.Errorf("onboarding primary placement is invalid for local swarm %q", strings.TrimSpace(placement.RuntimeSwarmID))
	}
	entries, err := s.workspace.ListKnownForPrincipal(principal, 100000)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		workspaceEntry := pebblestore.WorkspaceEntry{
			AccountScopeID:      principal.AccountScopeID,
			WorkspaceID:         strings.TrimSpace(entry.WorkspaceID),
			WorkspaceGeneration: entry.WorkspaceGeneration,
			State:               strings.TrimSpace(entry.State),
			Path:                strings.TrimSpace(entry.Path),
			Name:                strings.TrimSpace(entry.WorkspaceName),
		}
		if strings.TrimSpace(workspaceEntry.WorkspaceID) == "" || workspaceEntry.WorkspaceGeneration <= 0 || strings.TrimSpace(workspaceEntry.Path) == "" {
			continue
		}
		binding, err := s.topology.EnsureLocalWorkspaceSelfBindingForPrincipal(principal.AccountScopeID, principal.UserID, workspaceEntry)
		if err != nil {
			return err
		}
		if !isPrimarySelfWorkspaceBinding(placement, binding) {
			return fmt.Errorf("onboarding primary workspace binding %q is invalid for local swarm %q", strings.TrimSpace(binding.BindingID), strings.TrimSpace(placement.RuntimeSwarmID))
		}
	}
	return nil
}

func isPrimarySelfPlacement(placement pebblestore.TopologyRuntimePlacementRecord) bool {
	runtimeSwarmID := strings.TrimSpace(placement.RuntimeSwarmID)
	return runtimeSwarmID != "" &&
		strings.TrimSpace(placement.AuthorityHostSwarmID) == runtimeSwarmID &&
		strings.TrimSpace(placement.RuntimeKind) == pebblestore.TopologyRuntimeKindHost &&
		strings.TrimSpace(placement.AuthorityContainerID) == "" &&
		strings.TrimSpace(placement.State) == pebblestore.TopologyRuntimePlacementStateActive &&
		placement.PlacementGeneration > 0
}

func isPrimarySelfWorkspaceBinding(placement pebblestore.TopologyRuntimePlacementRecord, binding pebblestore.TopologyWorkspaceBindingRecord) bool {
	runtimeSwarmID := strings.TrimSpace(placement.RuntimeSwarmID)
	return runtimeSwarmID != "" &&
		strings.TrimSpace(binding.DestinationRuntimeSwarmID) == runtimeSwarmID &&
		strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) == runtimeSwarmID &&
		strings.TrimSpace(binding.DestinationHostSwarmID) == runtimeSwarmID &&
		strings.TrimSpace(binding.DestinationRuntimeKind) == pebblestore.TopologyRuntimeKindHost &&
		strings.TrimSpace(binding.DestinationContainerID) == "" &&
		strings.TrimSpace(binding.State) == pebblestore.TopologyWorkspaceBindingStateBound &&
		binding.PlacementGeneration == placement.PlacementGeneration &&
		binding.BindingGeneration > 0 &&
		strings.TrimSpace(binding.SourceWorkspaceID) != "" &&
		binding.SourceWorkspaceGeneration > 0 &&
		strings.TrimSpace(binding.SourceWorkspacePath) != "" &&
		strings.TrimSpace(binding.DestinationWorkspacePath) == strings.TrimSpace(binding.SourceWorkspacePath)
}

func shouldShowOnboarding(cfg startupconfig.FileConfig, identityBootstrapped bool) bool {
	if !identityBootstrapped {
		return true
	}
	if strings.TrimSpace(cfg.SwarmName) == "" {
		return true
	}
	if !cfg.DesktopOnboardingCompleteSet {
		return false
	}
	return !cfg.DesktopOnboardingComplete
}

func requestChangesSwarmShape(req onboardingUpdateRequest) bool {
	return req.DesktopOnboardingComplete != nil || req.Child != nil || requestChangesSwarmReachability(req)
}

func requestChangesSwarmReachability(req onboardingUpdateRequest) bool {
	return req.Port != nil || req.AdvertiseHost != nil || req.AdvertisePort != nil || req.TailscaleURL != nil || req.PeerTransportPort != nil
}

func defaultOnboardingSwarmName(cfg startupconfig.FileConfig) string {
	if name := strings.TrimSpace(cfg.SwarmName); name != "" {
		return name
	}
	if name := tailscalePeerDisplayName(hostnameFromURL(cfg.TailscaleURL)); name != "" {
		return name
	}
	if hostname, err := os.Hostname(); err == nil {
		if name := strings.TrimSpace(hostname); name != "" {
			return name
		}
	}
	return "Local swarm"
}

func hostnameFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Hostname())
}

type localManagedLinkState struct {
	Role           string
	Managed        bool
	ManagerSwarmID string
}

func (s *Server) onboardingLocalManagedLinkState(cfg startupconfig.FileConfig) (localManagedLinkState, error) {
	if s.swarm == nil {
		return localManagedLinkState{Role: bootstrapRoleMaster}, nil
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return localManagedLinkState{}, err
	}
	return dbBackedLocalManagedLinkState(state), nil
}

func dbBackedLocalManagedLinkState(state swarmruntime.LocalState) localManagedLinkState {
	link := localManagedLinkState{Role: bootstrapRoleMaster}
	if dbPairingIsActiveManaged(state.Pairing) {
		link.Role = bootstrapRoleManaged
		link.Managed = true
		link.ManagerSwarmID = strings.TrimSpace(state.Pairing.ParentSwarmID)
		return link
	}
	return link
}

func dbPairingIsActiveManaged(pairing swarmruntime.PairingState) bool {
	return strings.EqualFold(strings.TrimSpace(pairing.PairingState), startupconfig.PairingStatePaired) && strings.TrimSpace(pairing.ParentSwarmID) != ""
}

func tailscaleCandidateURL(cfg startupconfig.FileConfig, tailscale onboardingTailscalePayload) string {
	return firstNonEmpty(strings.TrimSpace(cfg.TailscaleURL), strings.TrimSpace(tailscale.TailnetURL))
}

func detectedOnboardingTransports(cfg startupconfig.FileConfig) []onboardingTransportPayload {
	return detectedTailscaleOnboardingTransports(cfg)
}

func detectedCurrentSwarmStateTransports(cfg startupconfig.FileConfig) []onboardingTransportPayload {
	return detectedOnboardingTransports(cfg)
}
func detectedTailscaleOnboardingTransports(cfg startupconfig.FileConfig) []onboardingTransportPayload {
	tailscaleURL := strings.TrimSpace(cfg.TailscaleURL)
	if tailscaleURL == "" {
		return nil
	}
	return []onboardingTransportPayload{{
		Kind:    startupconfig.NetworkModeTailscale,
		Primary: tailscaleURL,
		All:     []string{tailscaleURL},
	}}
}

func canonicalAdvertisePort(cfg startupconfig.FileConfig) int {
	if cfg.AdvertisePort >= 1 && cfg.AdvertisePort <= 65535 {
		return cfg.AdvertisePort
	}
	return cfg.Port
}

func firstString(values []string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyTransport(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func dedupeTransportStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func orderedUniqueTransportStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func onboardingTransportsToSwarm(records []onboardingTransportPayload) []swarmruntime.TransportSummary {
	out := make([]swarmruntime.TransportSummary, 0, len(records))
	for _, record := range records {
		out = append(out, swarmruntime.TransportSummary{Kind: strings.TrimSpace(record.Kind), Primary: strings.TrimSpace(record.Primary), All: append([]string(nil), record.All...)})
	}
	return out
}

func swarmTransportsToOnboarding(records []swarmruntime.TransportSummary) []onboardingTransportPayload {
	out := make([]onboardingTransportPayload, 0, len(records))
	for _, record := range records {
		out = append(out, onboardingTransportPayload{Kind: strings.TrimSpace(record.Kind), Primary: strings.TrimSpace(record.Primary), All: append([]string(nil), record.All...)})
	}
	return out
}

func detectLANAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	ifaceAddrs := make([]lanInterfaceAddrs, 0, len(interfaces))
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		ifaceAddrs = append(ifaceAddrs, lanInterfaceAddrs{Interface: iface, Addrs: addrs})
	}
	return lanAddressesFromInterfaces(ifaceAddrs)
}

type lanInterfaceAddrs struct {
	Interface net.Interface
	Addrs     []net.Addr
}

func lanAddressesFromInterfaces(interfaces []lanInterfaceAddrs) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	for _, iface := range interfaces {
		if !isUsableLANInterface(iface.Interface) {
			continue
		}
		for _, addr := range iface.Addrs {
			ip := ipFromAddr(addr)
			if !isUsableLANIP(ip) {
				continue
			}
			text := strings.TrimSpace(ip.String())
			if text == "" {
				continue
			}
			if _, ok := seen[text]; ok {
				continue
			}
			seen[text] = struct{}{}
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

func ipFromAddr(addr net.Addr) net.IP {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}

func isUsableLANInterface(iface net.Interface) bool {
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(iface.Name))
	if name == "" {
		return false
	}
	for _, prefix := range []string{"docker", "br-", "veth", "tailscale", "ts"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func isUsableLANIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip = ip.To4()
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || !ip.IsPrivate() {
		return false
	}
	return !isTailscaleIP(ip)
}

func detectTailscale() onboardingTailscalePayload {
	tailscale, _ := detectTailscaleWithStatus()
	return tailscale
}

func expectedTailscaleServe(cfg startupconfig.FileConfig, tailscale onboardingTailscalePayload) onboardingTailscaleServePayload {
	return onboardingTailscaleServePayload{
		URL:                        firstNonEmpty(strings.TrimSpace(cfg.TailscaleURL), strings.TrimSpace(tailscale.TailnetURL)),
		ExpectedDesktopProxy:       httpProxyTarget(strings.TrimSpace(cfg.Host), cfg.DesktopPort),
		ExpectedAPIProxy:           httpProxyTarget(strings.TrimSpace(cfg.Host), cfg.Port),
		ExpectedPeerTransportProxy: httpProxyTarget("127.0.0.1", cfg.PeerTransportPort),
		Command:                    tailscaleServeCommand(cfg),
	}
}

func shouldDetectTailscaleServeForOnboarding(cfg startupconfig.FileConfig, tailscale onboardingTailscalePayload) bool {
	if strings.TrimSpace(tailscale.Error) != "" || !tailscale.Available {
		return false
	}
	if !tailscale.Connected && strings.TrimSpace(cfg.TailscaleURL) == "" {
		return false
	}
	return strings.TrimSpace(cfg.TailscaleURL) != "" || strings.TrimSpace(tailscale.TailnetURL) != "" || strings.TrimSpace(tailscale.DNSName) != ""
}

func detectTailscaleServe(cfg startupconfig.FileConfig, tailscale onboardingTailscalePayload) onboardingTailscaleServePayload {
	payload := expectedTailscaleServe(cfg, tailscale)
	status, err := detectTailscaleServeStatus()
	if err != nil {
		payload.Error = strings.TrimSpace(err.Error())
		return payload
	}
	dnsName := strings.TrimSpace(tailscale.DNSName)
	if strings.TrimSpace(cfg.TailscaleURL) != "" && hostnameWithHTTPSPort(cfg.TailscaleURL) != hostnameWithHTTPSPort(dnsName) {
		dnsName = ""
	}
	proxyTarget := tailscaleServeProxyTarget(status, payload.URL, dnsName)
	payload.ProxyTarget = proxyTarget
	if proxyTarget == "" {
		return payload
	}
	payload.Configured = true
	payload.Mode = classifyTailscaleServeMode(proxyTarget, payload.ExpectedDesktopProxy, payload.ExpectedAPIProxy, payload.ExpectedPeerTransportProxy)
	payload.Ready = tailscaleServeModePairingReady(payload.Mode)
	return payload
}

func detectTailscaleServeStatus() (tailscaleServeStatusWire, error) {
	path, err := exec.LookPath("tailscale")
	if err != nil {
		return tailscaleServeStatusWire{}, nil
	}

	commandArgs := []string{"serve", "status", "--json"}
	if socketPath := strings.TrimSpace(os.Getenv("TS_SOCKET")); socketPath != "" {
		commandArgs = append([]string{"--socket=" + socketPath}, commandArgs...)
	}

	output, err := exec.Command(path, commandArgs...).CombinedOutput()
	message := strings.TrimSpace(string(output))
	if err != nil && message == "" {
		return tailscaleServeStatusWire{}, nil
	}
	if message == "" {
		return tailscaleServeStatusWire{}, nil
	}

	var status tailscaleServeStatusWire
	if parseErr := json.Unmarshal(output, &status); parseErr == nil {
		return status, nil
	}
	if err != nil {
		lower := strings.ToLower(message)
		if strings.Contains(lower, "not serving") || strings.Contains(lower, "serve config is empty") {
			return tailscaleServeStatusWire{}, nil
		}
		return tailscaleServeStatusWire{}, errors.New(message)
	}
	return tailscaleServeStatusWire{}, fmt.Errorf("parse tailscale serve status: %s", message)
}

func tailscaleServeProxyTarget(status tailscaleServeStatusWire, rawURL, dnsName string) string {
	hostCandidates := make([]string, 0, 2)
	if parsedHost := hostnameWithHTTPSPort(rawURL); parsedHost != "" {
		hostCandidates = append(hostCandidates, parsedHost)
	}
	if parsedHost := hostnameWithHTTPSPort(dnsName); parsedHost != "" {
		hostCandidates = append(hostCandidates, parsedHost)
	}
	for _, host := range orderedUniqueTransportStrings(hostCandidates) {
		if proxyTarget := strings.TrimSpace(status.Web[host].Handlers["/"].Proxy); proxyTarget != "" {
			return proxyTarget
		}
	}
	return ""
}

func hostnameWithHTTPSPort(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" || !strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".ts.net") {
		return ""
	}
	return net.JoinHostPort(host, "443")
}

func httpProxyTarget(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if port < 1 || port > 65535 {
		return ""
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func classifyTailscaleServeMode(proxyTarget, desktopProxy, apiProxy, peerProxy string) string {
	proxyTarget = strings.TrimSpace(proxyTarget)
	switch {
	case proxyTarget == "":
		return ""
	case desktopProxy != "" && proxyTarget == desktopProxy:
		return "desktop"
	case apiProxy != "" && proxyTarget == apiProxy:
		return "api"
	case peerProxy != "" && proxyTarget == peerProxy:
		return "peer_transport"
	default:
		return "other"
	}
}

func tailscaleServeModePairingReady(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "desktop", "api":
		return true
	default:
		return false
	}
}

func tailscaleServeCommand(cfg startupconfig.FileConfig) string {
	target := httpProxyTarget(strings.TrimSpace(cfg.Host), cfg.DesktopPort)
	if target == "" {
		target = httpProxyTarget("127.0.0.1", cfg.DesktopPort)
	}
	if target == "" {
		return ""
	}
	return "tailscale serve --bg " + target
}

func tailscaleServePairingError(serve onboardingTailscaleServePayload) error {
	command := strings.TrimSpace(serve.Command)
	if command == "" {
		command = "tailscale serve --bg http://127.0.0.1:5555"
	}
	if strings.TrimSpace(serve.Error) != "" {
		return fmt.Errorf("this requester is not ready for Link Swarm because Tailscale Serve status could not be checked: %s. Run `%s` on this host, then press Link again", strings.TrimSpace(serve.Error), command)
	}
	if !serve.Configured {
		return fmt.Errorf("this requester is not ready for Link Swarm because its Tailscale URL is not being served. Run `%s` on this host, then press Link again", command)
	}
	return fmt.Errorf("this requester is not ready for Link Swarm because Tailscale Serve points to %q instead of the Swarm desktop/API port. Run `%s` on this host, then press Link again", strings.TrimSpace(serve.ProxyTarget), command)
}

func requireTailscaleServeReadyForPairing(cfg startupconfig.FileConfig, status onboardingResponse) error {
	if canonicalRemoteSwarmEndpoint(cfg, status) == "" {
		return nil
	}
	if !status.Tailscale.Available || strings.TrimSpace(status.Tailscale.Error) != "" || !status.Tailscale.Connected {
		return tailscaleServePairingError(status.Tailscale.Serve)
	}
	if status.Tailscale.Serve.Ready {
		return nil
	}
	return tailscaleServePairingError(status.Tailscale.Serve)
}

func detectTailscaleWithStatus() (onboardingTailscalePayload, *tailscaleStatusWire) {
	authURL := strings.TrimSpace(firstNonEmptyEnv("TAILSCALE_AUTH_URL", "SWARM_TAILSCALE_AUTH_URL"))
	path, err := exec.LookPath("tailscale")
	if err != nil {
		return onboardingTailscalePayload{
			Available: authURL != "",
			Connected: false,
			AuthURL:   authURL,
			IPs:       nil,
			Serve:     onboardingTailscaleServePayload{},
		}, nil
	}

	commandArgs := []string{"status", "--json"}
	if socketPath := strings.TrimSpace(os.Getenv("TS_SOCKET")); socketPath != "" {
		commandArgs = append([]string{"--socket=" + socketPath}, commandArgs...)
	}

	output, err := exec.Command(path, commandArgs...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return onboardingTailscalePayload{
			Available: true,
			Connected: false,
			AuthURL:   authURL,
			IPs:       nil,
			Error:     message,
			Serve:     onboardingTailscaleServePayload{},
		}, nil
	}

	var status tailscaleStatusWire
	if err := json.Unmarshal(output, &status); err != nil {
		return onboardingTailscalePayload{
			Available: true,
			Connected: false,
			AuthURL:   authURL,
			IPs:       nil,
			Error:     fmt.Sprintf("parse tailscale status: %v", err),
			Serve:     onboardingTailscaleServePayload{},
		}, nil
	}

	ips := make([]string, 0, len(status.Self.TailscaleIPs))
	for _, ip := range status.Self.TailscaleIPs {
		ip = strings.TrimSpace(ip)
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	sort.Strings(ips)

	if authURL == "" {
		authURL = strings.TrimSpace(status.AuthURL)
	}
	dnsName := strings.TrimSuffix(strings.TrimSpace(status.Self.DNSName), ".")
	tailnetURL := ""
	if dnsName != "" {
		tailnetURL = "https://" + dnsName
	}

	statusCopy := status
	return onboardingTailscalePayload{
		Available:   true,
		Connected:   status.Self.Online || dnsName != "" || len(ips) > 0,
		DNSName:     dnsName,
		TailnetName: strings.TrimSpace(status.CurrentTailnet.Name),
		TailnetURL:  tailnetURL,
		IPs:         ips,
		AuthURL:     authURL,
		Serve:       onboardingTailscaleServePayload{},
	}, &statusCopy
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) currentSwarmState(cfg startupconfig.FileConfig) (swarmruntime.LocalState, error) {
	if s.swarm == nil {
		return swarmruntime.LocalState{}, errors.New("swarm service is not configured")
	}
	transports := detectedCurrentSwarmStateTransports(cfg)
	advertiseAddr := firstTransportForKind(transports, startupconfig.NetworkModeTailscale)
	state, err := s.swarm.EnsureLocalState(swarmruntime.EnsureLocalStateInput{
		Name:          strings.TrimSpace(cfg.SwarmName),
		Role:          bootstrapRoleMaster,
		AdvertiseMode: firstTransportKind(transports),
		AdvertiseAddr: advertiseAddr,
		Transports:    onboardingTransportsToSwarm(transports),
	})
	if err != nil {
		return swarmruntime.LocalState{}, err
	}
	return state, nil
}

func firstTransportKind(transports []onboardingTransportPayload) string {
	for _, transport := range transports {
		if strings.TrimSpace(transport.Kind) == startupconfig.NetworkModeTailscale {
			return startupconfig.NetworkModeTailscale
		}
	}
	for _, transport := range transports {
		if kind := strings.TrimSpace(transport.Kind); kind != "" {
			return kind
		}
	}
	return ""
}

func firstTransportForKind(transports []onboardingTransportPayload, kind string) string {
	kind = strings.TrimSpace(kind)
	for _, transport := range transports {
		if strings.TrimSpace(transport.Kind) != kind {
			continue
		}
		if value := strings.TrimSpace(transport.Primary); value != "" {
			return value
		}
		for _, value := range transport.All {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}
