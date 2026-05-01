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
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

const (
	bootstrapRoleStandalone = "standalone"
	bootstrapRoleMaster     = "master"
	bootstrapRoleChild      = "child"
)

type onboardingTransportPayload struct {
	Kind    string   `json:"kind"`
	Primary string   `json:"primary,omitempty"`
	All     []string `json:"all,omitempty"`
}

type onboardingConfigPayload struct {
	SwarmName         string `json:"swarm_name"`
	SwarmMode         bool   `json:"swarm_mode"`
	Child             bool   `json:"child"`
	Mode              string `json:"mode"`
	Host              string `json:"host,omitempty"`
	Port              int    `json:"port"`
	DesktopPort       int    `json:"desktop_port"`
	AdvertiseHost     string `json:"advertise_host,omitempty"`
	AdvertisePort     int    `json:"advertise_port"`
	TailscaleURL      string `json:"tailscale_url,omitempty"`
	BypassPermissions bool   `json:"bypass_permissions,omitempty"`
	DevMode           bool   `json:"dev_mode,omitempty"`
	PeerTransportPort int    `json:"peer_transport_port"`
	RestartRequired   bool   `json:"restart_required,omitempty"`
	RestartReason     string `json:"restart_reason,omitempty"`
}

type onboardingHeuristicsPayload struct {
	MissingSwarmName    bool `json:"missing_swarm_name"`
	CredentialCount     int  `json:"credential_count"`
	SavedWorkspaceCount int  `json:"saved_workspace_count"`
	VaultConfigured     bool `json:"vault_configured"`
}

type onboardingTailscaleServePayload struct {
	Configured                 bool   `json:"configured"`
	Mode                       string `json:"mode,omitempty"`
	URL                        string `json:"url,omitempty"`
	ProxyTarget                string `json:"proxy_target,omitempty"`
	ExpectedDesktopProxy       string `json:"expected_desktop_proxy,omitempty"`
	ExpectedAPIProxy           string `json:"expected_api_proxy,omitempty"`
	ExpectedPeerTransportProxy string `json:"expected_peer_transport_proxy,omitempty"`
	Error                      string `json:"error,omitempty"`
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
	Config          onboardingConfigPayload     `json:"config"`
	Heuristics      onboardingHeuristicsPayload `json:"heuristics"`
	Tailscale       onboardingTailscalePayload  `json:"tailscale"`
}

type onboardingUpdateRequest struct {
	SwarmName         *string `json:"swarm_name,omitempty"`
	SwarmMode         *bool   `json:"swarm_mode,omitempty"`
	Child             *bool   `json:"child,omitempty"`
	Mode              *string `json:"mode,omitempty"`
	Port              *int    `json:"port,omitempty"`
	AdvertiseHost     *string `json:"advertise_host,omitempty"`
	AdvertisePort     *int    `json:"advertise_port,omitempty"`
	TailscaleURL      *string `json:"tailscale_url,omitempty"`
	PeerTransportPort *int    `json:"peer_transport_port,omitempty"`
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

func (s *Server) discoverRemoteSwarms(cfg startupconfig.FileConfig, localState *swarmruntime.LocalState, tailscaleStatus *tailscaleStatusWire) []onboardingDiscoveredSwarmPayload {
	seeds := collectRemoteSwarmDiscoverySeeds(cfg, tailscaleStatus)
	if len(seeds) == 0 {
		return nil
	}
	relationshipBySwarmID := map[string]string{}
	localSwarmID := ""
	if localState != nil {
		localSwarmID = strings.TrimSpace(localState.Node.SwarmID)
		for _, peer := range localState.TrustedPeers {
			swarmID := strings.TrimSpace(peer.SwarmID)
			if swarmID == "" {
				continue
			}
			relationshipBySwarmID[swarmID] = strings.TrimSpace(peer.Relationship)
		}
	}
	merged := map[string]onboardingDiscoveredSwarmPayload{}
	order := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		candidate := onboardingDiscoveredSwarmPayload{
			Name:                 strings.TrimSpace(seed.Name),
			Endpoint:             strings.TrimSpace(seed.Endpoint),
			TailnetURL:           strings.TrimSpace(seed.TailnetURL),
			DNSName:              strings.TrimSpace(seed.DNSName),
			IPs:                  append([]string(nil), seed.IPs...),
			Online:               seed.Online,
			Source:               strings.TrimSpace(seed.Source),
			Running:              false,
			TransportMode:        strings.TrimSpace(seed.TransportMode),
			RendezvousTransports: append([]onboardingTransportPayload(nil), seed.Transports...),
		}
		if seed.Probe && strings.TrimSpace(seed.Endpoint) != "" {
			if remote, err := fetchRemoteSwarmDiscovery(seed); err == nil && remote.OK {
				candidate.ID = strings.TrimSpace(remote.SwarmID)
				candidate.Name = firstNonEmpty(strings.TrimSpace(remote.Name), candidate.Name)
				candidate.Role = firstNonEmpty(strings.TrimSpace(remote.Role), candidate.Role, bootstrapRoleStandalone)
				candidate.Endpoint = firstNonEmpty(strings.TrimSpace(remote.Endpoint), candidate.Endpoint)
				candidate.Running = true
				candidate.TransportMode = firstNonEmpty(strings.TrimSpace(remote.TransportMode), candidate.TransportMode)
				if len(remote.RendezvousTransports) > 0 {
					candidate.RendezvousTransports = append([]onboardingTransportPayload(nil), remote.RendezvousTransports...)
				}
			}
		}
		if candidate.Role == "" {
			candidate.Role = bootstrapRoleStandalone
		}
		if candidate.Name == "" {
			candidate.Name = firstNonEmpty(candidate.DNSName, firstString(candidate.IPs), "Unnamed device")
		}
		if candidate.Endpoint == "" {
			candidate.Endpoint = firstNonEmpty(candidate.TailnetURL, candidate.DNSName)
		}
		if candidate.ID != "" {
			if candidate.ID == localSwarmID {
				continue
			}
			if relationship := relationshipBySwarmID[candidate.ID]; relationship != "" {
				candidate.InCurrentGroup = true
				candidate.CurrentRelationship = relationship
			}
		}
		key := discoveredSwarmMergeKey(candidate)
		if key == "" {
			continue
		}
		if existing, ok := merged[key]; ok {
			merged[key] = mergeDiscoveredSwarmPayload(existing, candidate)
			continue
		}
		merged[key] = candidate
		order = append(order, key)
	}
	out := make([]onboardingDiscoveredSwarmPayload, 0, len(order))
	for _, key := range order {
		out = append(out, merged[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		if out[i].Online != out[j].Online {
			return out[i].Online
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func collectRemoteSwarmDiscoverySeeds(cfg startupconfig.FileConfig, tailscaleStatus *tailscaleStatusWire) []remoteSwarmDiscoverySeed {
	seeds := make([]remoteSwarmDiscoverySeed, 0)
	seeds = append(seeds, discoverTailscaleSwarmSeeds(tailscaleStatus)...)
	seeds = append(seeds, discoverLANSwarmSeeds(cfg)...)
	return dedupeRemoteSwarmDiscoverySeeds(seeds)
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

func discoverLANSwarmSeeds(cfg startupconfig.FileConfig) []remoteSwarmDiscoverySeed {
	_ = cfg
	return nil
}

func dedupeRemoteSwarmDiscoverySeeds(seeds []remoteSwarmDiscoverySeed) []remoteSwarmDiscoverySeed {
	seen := map[string]struct{}{}
	out := make([]remoteSwarmDiscoverySeed, 0, len(seeds))
	for _, seed := range seeds {
		key := strings.ToLower(strings.TrimSpace(strings.Join([]string{
			seed.Source,
			seed.DNSName,
			seed.Endpoint,
			seed.TailnetURL,
			firstString(seed.IPs),
		}, "|")))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, seed)
	}
	return out
}

func fetchRemoteSwarmDiscovery(seed remoteSwarmDiscoverySeed) (swarmDiscoveryResponse, error) {
	var remote swarmDiscoveryResponse
	if err := getRemoteSwarmJSONWithTransportFallback(seed.Endpoint, "/v1/swarm/discovery", seed.Transports, &remote); err != nil {
		return swarmDiscoveryResponse{}, err
	}
	return remote, nil
}

func discoveredSwarmMergeKey(candidate onboardingDiscoveredSwarmPayload) string {
	return strings.ToLower(strings.TrimSpace(firstNonEmpty(
		candidate.ID,
		candidate.TailnetURL,
		candidate.Endpoint,
		candidate.DNSName,
		firstString(candidate.IPs),
		candidate.Name,
	)))
}

func mergeDiscoveredSwarmPayload(left, right onboardingDiscoveredSwarmPayload) onboardingDiscoveredSwarmPayload {
	merged := left
	merged.ID = firstNonEmpty(left.ID, right.ID)
	merged.Name = firstNonEmpty(left.Name, right.Name)
	merged.Role = firstNonEmpty(left.Role, right.Role)
	merged.Endpoint = firstNonEmpty(left.Endpoint, right.Endpoint)
	merged.TailnetURL = firstNonEmpty(left.TailnetURL, right.TailnetURL)
	merged.DNSName = firstNonEmpty(left.DNSName, right.DNSName)
	if len(merged.IPs) == 0 {
		merged.IPs = append([]string(nil), right.IPs...)
	}
	merged.Online = left.Online || right.Online
	merged.Source = firstNonEmpty(left.Source, right.Source)
	merged.Running = left.Running || right.Running
	merged.InCurrentGroup = left.InCurrentGroup || right.InCurrentGroup
	merged.CurrentRelationship = firstNonEmpty(left.CurrentRelationship, right.CurrentRelationship)
	merged.TransportMode = firstNonEmpty(left.TransportMode, right.TransportMode)
	if len(merged.RendezvousTransports) == 0 {
		merged.RendezvousTransports = append([]onboardingTransportPayload(nil), right.RendezvousTransports...)
	}
	return merged
}

func remoteSwarmProbeEndpoint(mode, dnsName string, ips []string) string {
	mode = strings.TrimSpace(mode)
	dnsName = strings.TrimSpace(dnsName)
	if dnsName != "" {
		return normalizeRemoteSwarmEndpoint(dnsName)
	}
	if mode == startupconfig.NetworkModeLAN {
		if ip := strings.TrimSpace(firstString(ips)); ip != "" {
			return normalizeRemoteSwarmEndpoint(ip)
		}
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
		response, err := s.updateOnboarding(req, includeSensitive)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) allowSensitiveOnboardingMetadata(r *http.Request) bool {
	if r == nil {
		return false
	}
	if isLocalTransportRequest(r) {
		return true
	}
	if shouldUseDesktopLocalSessionAuth(r) && s != nil && s.desktopLocalSessions != nil {
		if token := desktopLocalSessionTokenFromRequest(r); s.desktopLocalSessions.Validate(token, time.Now()) {
			return true
		}
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
	credentialList, err := s.readCredentialList()
	if err != nil {
		return onboardingResponse{}, err
	}
	savedCount, err := s.readSavedWorkspaceCount()
	if err != nil {
		return onboardingResponse{}, err
	}
	needsOnboarding := shouldShowOnboarding(cfg, vaultStatus, credentialList.Total, savedCount)
	tailscale, _ := detectTailscaleWithStatus()
	response := onboardingResponse{
		OK:              true,
		NeedsOnboarding: needsOnboarding,
		Config: onboardingConfigPayload{
			SwarmName:         strings.TrimSpace(cfg.SwarmName),
			SwarmMode:         swarmModeEnabled(cfg),
			Child:             cfg.Child,
			Mode:              bootstrapNetworkMode(cfg),
			Host:              strings.TrimSpace(cfg.Host),
			Port:              cfg.Port,
			DesktopPort:       cfg.DesktopPort,
			AdvertiseHost:     strings.TrimSpace(cfg.AdvertiseHost),
			AdvertisePort:     canonicalAdvertisePort(cfg),
			TailscaleURL:      strings.TrimSpace(cfg.TailscaleURL),
			BypassPermissions: cfg.BypassPermissions,
			DevMode:           cfg.DevMode,
			PeerTransportPort: cfg.PeerTransportPort,
		},
		Heuristics: onboardingHeuristicsPayload{
			MissingSwarmName:    swarmModeEnabled(cfg) && strings.TrimSpace(cfg.SwarmName) == "",
			CredentialCount:     credentialList.Total,
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
	if !needsOnboarding {
		response.Tailscale.Serve = detectTailscaleServe(cfg, response.Tailscale)
		return response, nil
	}
	response.Tailscale.CandidateURL = tailscaleCandidateURL(cfg, tailscale)
	response.Tailscale.Serve = detectTailscaleServe(cfg, response.Tailscale)
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

func (s *Server) updateOnboarding(req onboardingUpdateRequest, includeSensitive bool) (onboardingResponse, error) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return onboardingResponse{}, err
	}
	if !cfg.Exists {
		cfg = startupconfig.Default(cfg.Path)
	}

	updated := cfg
	changed := false
	turnedOffSwarmMode := false
	restartRequired := false
	var restartReasons []string

	if req.SwarmName != nil {
		updated.SwarmName = strings.TrimSpace(*req.SwarmName)
		if updated.SwarmName == "" {
			return onboardingResponse{}, errors.New("swarm_name is required")
		}
		changed = true
	}
	if req.SwarmMode != nil {
		updated.SwarmMode = *req.SwarmMode
		turnedOffSwarmMode = cfg.SwarmMode && !updated.SwarmMode
		if !updated.SwarmMode {
			updated.Child = false
		}
		changed = true
	}
	if req.Child != nil {
		updated.Child = *req.Child
		changed = true
	}
	if req.Mode != nil {
		updated.NetworkMode = strings.ToLower(strings.TrimSpace(*req.Mode))
		if !isValidBootstrapNetworkMode(updated.NetworkMode) {
			return onboardingResponse{}, fmt.Errorf("mode must be %q or %q", startupconfig.NetworkModeLAN, startupconfig.NetworkModeTailscale)
		}
		changed = true
		restartRequired = true
		restartReasons = append(restartReasons, "reachability mode changed")
	}
	if req.Port != nil {
		updated.Port = *req.Port
		if updated.Port < 1 || updated.Port > 65535 {
			return onboardingResponse{}, fmt.Errorf("port must be between %d and %d", 1, 65535)
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
			return onboardingResponse{}, fmt.Errorf("advertise_port must be between %d and %d", 1, 65535)
		}
		changed = true
	}
	if req.TailscaleURL != nil {
		updated.TailscaleURL = strings.TrimSpace(*req.TailscaleURL)
		changed = true
		restartRequired = true
		restartReasons = append(restartReasons, "peer transport endpoint changed")
	}
	if req.PeerTransportPort != nil {
		updated.PeerTransportPort = *req.PeerTransportPort
		if updated.PeerTransportPort < 1 || updated.PeerTransportPort > 65535 {
			return onboardingResponse{}, fmt.Errorf("peer_transport_port must be between %d and %d", 1, 65535)
		}
		changed = true
		restartRequired = true
		restartReasons = append(restartReasons, "peer transport port changed")
	}
	if !swarmModeEnabled(updated) {
		updated.Child = false
	}
	if swarmModeEnabled(updated) && bootstrapNetworkMode(updated) == startupconfig.NetworkModeLAN {
		updated.AdvertiseHost = firstNonEmpty(
			strings.TrimSpace(updated.AdvertiseHost),
			firstString(lanConfigHosts(updated)),
			firstString(detectLANAddresses()),
		)
		if updated.AdvertisePort < 1 || updated.AdvertisePort > 65535 {
			updated.AdvertisePort = updated.Port
		}
		if strings.TrimSpace(updated.AdvertiseHost) == "" {
			return onboardingResponse{}, errors.New("could not determine a LAN advertise host; enter one explicitly")
		}
	}

	if !changed {
		return onboardingResponse{}, errors.New("no onboarding fields were provided")
	}
	if err := startupconfig.Write(updated); err != nil {
		return onboardingResponse{}, err
	}
	if (turnedOffSwarmMode || (req.Child != nil && !updated.Child)) && s.swarm != nil {
		state, err := s.currentSwarmState(cfg)
		if err != nil {
			return onboardingResponse{}, err
		}
		if err := s.swarm.DetachToStandalone(strings.TrimSpace(state.Node.SwarmID)); err != nil {
			return onboardingResponse{}, err
		}
	}
	if req.SwarmName != nil {
		if err := s.persistUISwarmName(updated.SwarmName); err != nil {
			return onboardingResponse{}, err
		}
	}
	response, err := s.onboardingResponse(includeSensitive)
	if err != nil {
		return onboardingResponse{}, err
	}
	if restartRequired {
		response.Config.RestartRequired = true
		response.Config.RestartReason = strings.Join(dedupeTransportStrings(restartReasons), "; ")
	}
	return response, nil
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

func (s *Server) readCredentialList() (auth.CredentialList, error) {
	if s == nil || s.auth == nil {
		return auth.CredentialList{}, errors.New("auth service not configured")
	}
	return s.auth.ListCredentials("", "", 200)
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

func (s *Server) readSavedWorkspaceCount() (int, error) {
	if s == nil || s.workspace == nil {
		return 0, errors.New("workspace service not configured")
	}
	workspaces, err := s.workspace.ListKnown(10000)
	if err != nil {
		return 0, err
	}
	return len(workspaces), nil
}

func shouldShowOnboarding(cfg startupconfig.FileConfig, vault auth.VaultStatus, credentialCount, savedCount int) bool {
	if !swarmModeEnabled(cfg) {
		return false
	}
	if strings.TrimSpace(cfg.SwarmName) == "" {
		return true
	}
	return looksLikeFreshDesktopSetup(cfg, vault, credentialCount, savedCount)
}

func looksLikeFreshDesktopSetup(cfg startupconfig.FileConfig, vault auth.VaultStatus, credentialCount, savedCount int) bool {
	return swarmModeEnabled(cfg) &&
		strings.TrimSpace(cfg.SwarmName) == "" &&
		!cfg.Child &&
		bootstrapNetworkMode(cfg) == startupconfig.NetworkModeLAN &&
		!vault.Enabled &&
		credentialCount == 0 &&
		savedCount == 0
}

func localSwarmRole(cfg startupconfig.FileConfig) string {
	if !swarmModeEnabled(cfg) {
		return bootstrapRoleStandalone
	}
	if cfg.Child {
		return bootstrapRoleChild
	}
	return bootstrapRoleMaster
}

func swarmModeEnabled(cfg startupconfig.FileConfig) bool {
	return cfg.SwarmMode
}

func bootstrapNetworkMode(cfg startupconfig.FileConfig) string {
	mode := strings.ToLower(strings.TrimSpace(cfg.NetworkMode))
	if mode == "" {
		return startupconfig.NetworkModeLAN
	}
	return mode
}

func isValidBootstrapNetworkMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case startupconfig.NetworkModeLAN, startupconfig.NetworkModeTailscale:
		return true
	default:
		return false
	}
}

func tailscaleCandidateURL(cfg startupconfig.FileConfig, tailscale onboardingTailscalePayload) string {
	return firstNonEmpty(strings.TrimSpace(cfg.TailscaleURL), strings.TrimSpace(tailscale.TailnetURL))
}

func detectedOnboardingTransports(cfg startupconfig.FileConfig) []onboardingTransportPayload {
	transports := make([]onboardingTransportPayload, 0, 2)
	transports = append(transports, detectedLANOnboardingTransports(cfg)...)
	transports = append(transports, detectedTailscaleOnboardingTransports(cfg)...)
	return transports
}

func detectedCurrentSwarmStateTransports(cfg startupconfig.FileConfig) []onboardingTransportPayload {
	if bootstrapNetworkMode(cfg) == startupconfig.NetworkModeLAN {
		return detectedLANOnboardingTransports(cfg)
	}
	return detectedOnboardingTransports(cfg)
}

func detectedLANOnboardingTransports(cfg startupconfig.FileConfig) []onboardingTransportPayload {
	lan := lanCandidateHosts(cfg)
	if len(lan) > 0 {
		return []onboardingTransportPayload{{Kind: startupconfig.NetworkModeLAN, Primary: lan[0], All: lan}}
	}
	return nil
}

func detectedTailscaleOnboardingTransports(cfg startupconfig.FileConfig) []onboardingTransportPayload {
	tailscale := detectTailscale()
	tailscaleValues := dedupeTransportStrings([]string{
		strings.TrimSpace(cfg.TailscaleURL),
		strings.TrimSpace(tailscale.TailnetURL),
		strings.TrimSpace(tailscale.DNSName),
	})
	tailscaleValues = dedupeTransportStrings(append(tailscaleValues, tailscale.IPs...))
	if len(tailscaleValues) > 0 {
		return []onboardingTransportPayload{{
			Kind: startupconfig.NetworkModeTailscale,
			Primary: firstNonEmptyTransport(
				strings.TrimSpace(cfg.TailscaleURL),
				strings.TrimSpace(tailscale.TailnetURL),
				strings.TrimSpace(tailscale.DNSName),
				firstString(tailscale.IPs),
			),
			All: tailscaleValues,
		}}
	}
	return nil
}

func lanCandidateHosts(cfg startupconfig.FileConfig) []string {
	return orderedUniqueTransportStrings(append(lanConfigHosts(cfg), detectLANAddresses()...))
}

func lanConfigHosts(cfg startupconfig.FileConfig) []string {
	values := make([]string, 0, 2)
	if host := strings.TrimSpace(cfg.AdvertiseHost); isUsableLANHost(host) {
		values = append(values, host)
	}
	if host := strings.TrimSpace(cfg.Host); isUsableLANHost(host) {
		values = append(values, host)
	}
	return values
}

func canonicalAdvertisePort(cfg startupconfig.FileConfig) int {
	if cfg.AdvertisePort >= 1 && cfg.AdvertisePort <= 65535 {
		return cfg.AdvertisePort
	}
	return cfg.Port
}

func isUsableLANHost(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch strings.ToLower(value) {
	case "localhost", "0.0.0.0", "::", "::1", "[::]", "[::1]":
		return false
	}
	ipText := strings.TrimPrefix(strings.TrimSuffix(value, "]"), "[")
	if ip := net.ParseIP(ipText); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return false
	}
	return true
}

func isLoopbackBindHost(value string) bool {
	value = strings.TrimSpace(strings.Trim(strings.TrimSpace(value), "[]"))
	if value == "" {
		return false
	}
	if strings.EqualFold(value, "localhost") {
		return true
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.IsLoopback()
	}
	return false
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

func detectLANAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil {
				continue
			}
			ip = ip.To4()
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
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

func detectTailscale() onboardingTailscalePayload {
	tailscale, _ := detectTailscaleWithStatus()
	return tailscale
}

func detectTailscaleServe(cfg startupconfig.FileConfig, tailscale onboardingTailscalePayload) onboardingTailscaleServePayload {
	payload := onboardingTailscaleServePayload{
		URL:                        firstNonEmpty(strings.TrimSpace(cfg.TailscaleURL), strings.TrimSpace(tailscale.TailnetURL)),
		ExpectedDesktopProxy:       httpProxyTarget(strings.TrimSpace(cfg.Host), cfg.DesktopPort),
		ExpectedAPIProxy:           httpProxyTarget(strings.TrimSpace(cfg.Host), cfg.Port),
		ExpectedPeerTransportProxy: httpProxyTarget("127.0.0.1", cfg.PeerTransportPort),
	}
	status, err := detectTailscaleServeStatus()
	if err != nil {
		payload.Error = strings.TrimSpace(err.Error())
		return payload
	}
	proxyTarget := tailscaleServeProxyTarget(status, payload.URL, strings.TrimSpace(tailscale.DNSName))
	payload.ProxyTarget = proxyTarget
	if proxyTarget == "" {
		return payload
	}
	payload.Configured = true
	payload.Mode = classifyTailscaleServeMode(proxyTarget, payload.ExpectedDesktopProxy, payload.ExpectedAPIProxy, payload.ExpectedPeerTransportProxy)
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
	for _, host := range dedupeTransportStrings(hostCandidates) {
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
	if host == "" {
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
	mode := bootstrapNetworkMode(cfg)
	advertiseAddr := firstTransportForKind(transports, mode)
	if mode == startupconfig.NetworkModeLAN {
		advertiseAddr = firstNonEmpty(strings.TrimSpace(cfg.AdvertiseHost), advertiseAddr)
	}
	state, err := s.swarm.EnsureLocalState(swarmruntime.EnsureLocalStateInput{
		Name:          strings.TrimSpace(cfg.SwarmName),
		Role:          localSwarmRole(cfg),
		SwarmMode:     swarmModeEnabled(cfg),
		AdvertiseMode: mode,
		AdvertiseAddr: advertiseAddr,
		Transports:    onboardingTransportsToSwarm(transports),
	})
	if err != nil {
		return swarmruntime.LocalState{}, err
	}
	return state, nil
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
