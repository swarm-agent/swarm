package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

const remoteCandidatesPathID = "swarm.remote_candidates.v1"

var remoteCandidateProbeTimeout = 900 * time.Millisecond

type remoteCandidatesResponse struct {
	OK         bool                             `json:"ok"`
	PathID     string                           `json:"path_id"`
	Tailscale  remoteCandidatesTailscalePayload `json:"tailscale"`
	Candidates []remoteSwarmCandidatePayload    `json:"candidates"`
	Count      int                              `json:"count"`
}

type remoteCandidatesTailscalePayload struct {
	Available   bool   `json:"available"`
	Connected   bool   `json:"connected"`
	TailnetName string `json:"tailnet_name,omitempty"`
	Error       string `json:"error,omitempty"`
}

type remoteSwarmCandidatePayload struct {
	ID                   string                           `json:"id"`
	Source               string                           `json:"source"`
	Name                 string                           `json:"name"`
	DNSName              string                           `json:"dns_name,omitempty"`
	TailnetURL           string                           `json:"tailnet_url,omitempty"`
	Endpoint             string                           `json:"endpoint,omitempty"`
	EndpointCandidates   []remoteCandidateEndpointPayload `json:"endpoint_candidates,omitempty"`
	IPs                  []string                         `json:"ips,omitempty"`
	OS                   string                           `json:"os,omitempty"`
	Online               bool                             `json:"online"`
	TransportMode        string                           `json:"transport_mode"`
	RendezvousTransports []onboardingTransportPayload     `json:"rendezvous_transports,omitempty"`
}

type remoteCandidateEndpointPayload struct {
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port"`
	Scheme string `json:"scheme,omitempty"`
}

func (s *Server) handleSwarmRemoteCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	tailscale, status := detectTailscaleWithStatus()
	response := remoteCandidatesResponse{
		OK:     true,
		PathID: remoteCandidatesPathID,
		Tailscale: remoteCandidatesTailscalePayload{
			Available:   tailscale.Available,
			Connected:   tailscale.Connected,
			TailnetName: strings.TrimSpace(tailscale.TailnetName),
			Error:       safeRemoteCandidateTailscaleError(tailscale.Error),
		},
	}
	if tailscale.Connected && status != nil {
		cfg, err := s.loadStartupConfig()
		if err != nil {
			cfg = startupconfig.FileConfig{}
		}
		response.Candidates = s.remoteCandidatesFromTailscaleStatus(status, cfg)
		response.Count = len(response.Candidates)
	}
	writeJSON(w, http.StatusOK, response)
}

func safeRemoteCandidateTailscaleError(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "tskey-") || strings.Contains(lower, "authurl") || strings.Contains(lower, "auth url") {
		return "tailscale status unavailable"
	}
	return message
}

func (s *Server) remoteCandidatesFromTailscaleStatus(status *tailscaleStatusWire, cfg startupconfig.FileConfig) []remoteSwarmCandidatePayload {
	if status == nil || len(status.Peer) == 0 {
		return nil
	}
	seeds := discoverTailscaleSwarmSeeds(status)
	out := make([]remoteSwarmCandidatePayload, 0, len(seeds))
	for _, seed := range seeds {
		if !seed.Online {
			continue
		}
		peer := tailscalePeerByDNSOrIP(status, seed.DNSName, seed.IPs)
		endpoints := s.remoteCandidateEndpointCandidates(seed.DNSName, seed.IPs, cfg)
		reachableEndpoint, reachableCandidates := reachableRemoteCandidateEndpoints(endpoints, seed.Transports)
		if reachableEndpoint == "" {
			continue
		}
		candidate := remoteSwarmCandidatePayload{
			ID:                   remoteCandidateID(seed),
			Source:               startupconfig.NetworkModeTailscale,
			Name:                 firstNonEmpty(strings.TrimSpace(seed.Name), strings.TrimSpace(peer.OS), firstString(seed.IPs), "Tailscale device"),
			DNSName:              strings.TrimSpace(seed.DNSName),
			TailnetURL:           strings.TrimSpace(seed.TailnetURL),
			Endpoint:             reachableEndpoint,
			EndpointCandidates:   reachableCandidates,
			IPs:                  append([]string(nil), seed.IPs...),
			OS:                   strings.TrimSpace(peer.OS),
			Online:               true,
			TransportMode:        startupconfig.NetworkModeTailscale,
			RendezvousTransports: append([]onboardingTransportPayload(nil), seed.Transports...),
		}
		out = append(out, candidate)
	}
	return out
}

func tailscalePeerByDNSOrIP(status *tailscaleStatusWire, dnsName string, ips []string) tailscalePeerStatusWire {
	if status == nil {
		return tailscalePeerStatusWire{}
	}
	dnsName = strings.TrimSuffix(strings.TrimSpace(dnsName), ".")
	ipSet := map[string]struct{}{}
	for _, ip := range ips {
		if ip = strings.TrimSpace(ip); ip != "" {
			ipSet[ip] = struct{}{}
		}
	}
	for _, peer := range status.Peer {
		if strings.TrimSuffix(strings.TrimSpace(peer.DNSName), ".") == dnsName && dnsName != "" {
			return peer
		}
		for _, ip := range peer.TailscaleIPs {
			if _, ok := ipSet[strings.TrimSpace(ip)]; ok {
				return peer
			}
		}
	}
	return tailscalePeerStatusWire{}
}

func remoteCandidateID(seed remoteSwarmDiscoverySeed) string {
	return strings.ToLower(strings.TrimSpace("tailscale:" + firstNonEmpty(seed.DNSName, firstString(seed.IPs), seed.Endpoint, seed.Name)))
}

func reachableRemoteCandidateEndpoints(endpoints []remoteCandidateEndpointPayload, transports []onboardingTransportPayload) (string, []remoteCandidateEndpointPayload) {
	out := make([]remoteCandidateEndpointPayload, 0, len(endpoints))
	for _, candidate := range endpoints {
		if !remoteCandidateEndpointReachable(strings.TrimSpace(candidate.URL), transports) {
			continue
		}
		out = append(out, candidate)
	}
	if len(out) == 0 {
		return "", nil
	}
	return strings.TrimSpace(out[0].URL), out
}

func remoteCandidateEndpointReachable(endpoint string, transports []onboardingTransportPayload) bool {
	endpoint = remoteCandidateNormalizeEndpoint(endpoint)
	if endpoint == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteCandidateProbeTimeout)
	defer cancel()
	client := &http.Client{Timeout: remoteCandidateProbeTimeout}
	if endpointLooksLikeTailscale(endpoint) || transportsContainKind(transports, startupconfig.NetworkModeTailscale) {
		if pinned, err := remoteCandidatePinnedClient(endpoint, transports); err == nil && pinned != nil {
			client = pinned
		}
	}
	return remoteCandidateProbeURL(ctx, client, endpoint+"/readyz") || remoteCandidateProbeURL(ctx, client, endpoint+"/healthz")
}

func remoteCandidateNormalizeEndpoint(endpoint string) string {
	return strings.TrimSpace(strings.TrimRight(endpoint, "/"))
}

func remoteCandidateProbeURL(ctx context.Context, client *http.Client, probeURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
}

func remoteCandidatePinnedClient(endpoint string, transports []onboardingTransportPayload) (*http.Client, error) {
	for _, dialIP := range transportDialIPs(transports) {
		client, err := httpClientForPinnedRemoteIP(endpoint, dialIP)
		if err == nil && client != nil {
			client.Timeout = remoteCandidateProbeTimeout
			return client, nil
		}
	}
	return nil, nil
}

func (s *Server) remoteCandidateEndpointCandidates(dnsName string, ips []string, cfg startupconfig.FileConfig) []remoteCandidateEndpointPayload {
	dnsName = strings.TrimSuffix(strings.TrimSpace(dnsName), ".")
	hosts := orderedUniqueTransportStrings(append([]string{dnsName}, ips...))
	ports := s.remoteCandidateProbePortsForConfig(cfg)
	seen := map[string]struct{}{}
	out := make([]remoteCandidateEndpointPayload, 0, len(hosts)*(len(ports)+1))
	add := func(candidate remoteCandidateEndpointPayload) {
		candidate.URL = remoteCandidateNormalizeEndpoint(candidate.URL)
		if candidate.URL == "" {
			return
		}
		key := strings.ToLower(candidate.URL)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	if dnsName != "" {
		add(remoteCandidateEndpointPayload{
			Kind:   "tailscale_https",
			URL:    tailscalePeerURL(dnsName),
			Host:   dnsName,
			Port:   443,
			Scheme: "https",
		})
	}
	for _, host := range hosts {
		for _, port := range ports {
			add(remoteCandidateEndpointPayload{
				Kind:   "tailscale_api",
				URL:    "http://" + net.JoinHostPort(host, strconv.Itoa(port)),
				Host:   host,
				Port:   port,
				Scheme: "http",
			})
		}
	}
	return out
}

func (s *Server) remoteCandidateProbePortsForConfig(cfg startupconfig.FileConfig) []int {
	if s != nil && len(s.remoteCandidateProbePorts) > 0 {
		return dedupeRemoteCandidatePorts(s.remoteCandidateProbePorts)
	}
	ports := []int{startupconfig.DefaultPort, cfg.Port, cfg.AdvertisePort}
	for _, key := range []string{"SWARM_API_PORT", "SWARMD_PORT", "PORT"} {
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(getenvRemoteCandidate(key)), ":"))
		if value == "" {
			continue
		}
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		ports = append(ports, port)
	}
	return dedupeRemoteCandidatePorts(ports)
}

func getenvRemoteCandidate(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func dedupeRemoteCandidatePorts(ports []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(ports))
	for _, port := range ports {
		if port < 1 || port > 65535 {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		out = append(out, port)
	}
	return out
}
