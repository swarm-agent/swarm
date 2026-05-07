package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

const remoteCandidatesPathID = "swarm.remote_candidates.v1"

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
		response.Candidates = remoteCandidatesFromTailscaleStatus(status)
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

func remoteCandidatesFromTailscaleStatus(status *tailscaleStatusWire) []remoteSwarmCandidatePayload {
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
		endpoints := remoteCandidateEndpointCandidates(seed.DNSName, seed.IPs)
		candidate := remoteSwarmCandidatePayload{
			ID:                   remoteCandidateID(seed),
			Source:               startupconfig.NetworkModeTailscale,
			Name:                 firstNonEmpty(strings.TrimSpace(seed.Name), strings.TrimSpace(peer.OS), firstString(seed.IPs), "Tailscale device"),
			DNSName:              strings.TrimSpace(seed.DNSName),
			TailnetURL:           strings.TrimSpace(seed.TailnetURL),
			Endpoint:             remoteCandidatePrimaryEndpoint(seed.DNSName, endpoints),
			EndpointCandidates:   endpoints,
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

func remoteCandidatePrimaryEndpoint(dnsName string, endpoints []remoteCandidateEndpointPayload) string {
	if endpoint := tailscalePeerURL(dnsName); endpoint != "" {
		return endpoint
	}
	for _, candidate := range endpoints {
		if strings.TrimSpace(candidate.URL) != "" {
			return strings.TrimSpace(candidate.URL)
		}
	}
	return ""
}

func remoteCandidateEndpointCandidates(dnsName string, ips []string) []remoteCandidateEndpointPayload {
	dnsName = strings.TrimSuffix(strings.TrimSpace(dnsName), ".")
	hosts := orderedUniqueTransportStrings(append([]string{dnsName}, ips...))
	seen := map[string]struct{}{}
	out := make([]remoteCandidateEndpointPayload, 0, len(hosts)+1)
	add := func(candidate remoteCandidateEndpointPayload) {
		candidate.URL = strings.TrimSpace(candidate.URL)
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
		add(remoteCandidateEndpointPayload{
			Kind:   "tailscale_api",
			URL:    "http://" + net.JoinHostPort(host, strconv.Itoa(startupconfig.DefaultPort)),
			Host:   host,
			Port:   startupconfig.DefaultPort,
			Scheme: "http",
		})
	}
	return out
}
