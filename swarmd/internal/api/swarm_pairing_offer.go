package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

const (
	swarmRemotePairingOfferPathID     = "swarm.remote_pairing.offer.v1"
	swarmRemotePairingOfferVersion    = "managed-swarm-offer/v1"
	swarmRemotePairingOfferDefaultTTL = 10 * time.Minute
	swarmRemotePairingOfferMinTTL     = 60 * time.Second
	swarmRemotePairingOfferMaxTTL     = 30 * time.Minute
)

type swarmRemotePairingOfferCreateRequest struct {
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

type swarmRemotePairingOfferResponse struct {
	OK     bool                           `json:"ok"`
	PathID string                         `json:"path_id"`
	Offer  swarmRemotePairingOfferPayload `json:"offer"`
}

type swarmRemotePairingOfferPayload struct {
	Version              string                           `json:"version"`
	Type                 string                           `json:"type"`
	Token                string                           `json:"token"`
	SingleUse            bool                             `json:"single_use"`
	SwarmID              string                           `json:"swarm_id"`
	SwarmName            string                           `json:"swarm_name"`
	PublicKey            string                           `json:"public_key"`
	Fingerprint          string                           `json:"fingerprint"`
	Endpoint             string                           `json:"endpoint"`
	EndpointCandidates   []remoteCandidateEndpointPayload `json:"endpoint_candidates,omitempty"`
	APIPort              int                              `json:"api_port"`
	TransportMode        string                           `json:"transport_mode"`
	RendezvousTransports []onboardingTransportPayload     `json:"rendezvous_transports,omitempty"`
	ExpiresAt            int64                            `json:"expires_at"`
	CreatedAt            int64                            `json:"created_at"`
	Ceremony             swarmRemotePairingOfferCeremony  `json:"ceremony"`
}

type swarmRemotePairingOfferCeremony struct {
	Code             string `json:"code"`
	VerificationOnly bool   `json:"verification_only"`
	Description      string `json:"description"`
}

type swarmRemotePairingOfferTranscript struct {
	Version              string                       `json:"version"`
	Type                 string                       `json:"type"`
	Token                string                       `json:"token"`
	SwarmID              string                       `json:"swarm_id"`
	SwarmName            string                       `json:"swarm_name"`
	PublicKey            string                       `json:"public_key"`
	Fingerprint          string                       `json:"fingerprint"`
	Endpoint             string                       `json:"endpoint"`
	APIPort              int                          `json:"api_port"`
	TransportMode        string                       `json:"transport_mode"`
	RendezvousTransports []onboardingTransportPayload `json:"rendezvous_transports,omitempty"`
	ExpiresAt            int64                        `json:"expires_at"`
}

func (s *Server) handleSwarmRemotePairingOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	var req swarmRemotePairingOfferCreateRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	ttl, err := swarmRemotePairingOfferTTL(req.TTLSeconds)
	if err != nil {
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
	offer, err := buildSwarmRemotePairingOffer(cfg, status, state, ttl, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, swarmRemotePairingOfferResponse{OK: true, PathID: swarmRemotePairingOfferPathID, Offer: offer})
}

func buildSwarmRemotePairingOffer(cfg startupconfig.FileConfig, status onboardingResponse, state swarmruntime.LocalState, ttl time.Duration, now time.Time) (swarmRemotePairingOfferPayload, error) {
	endpoint := canonicalRemoteSwarmEndpoint(cfg, status)
	if endpoint == "" {
		return swarmRemotePairingOfferPayload{}, errors.New("managed swarm does not have a reachable remote endpoint yet")
	}
	swarmID := strings.TrimSpace(state.Node.SwarmID)
	if swarmID == "" {
		return swarmRemotePairingOfferPayload{}, errors.New("managed swarm identity is not configured")
	}
	publicKey := strings.TrimSpace(state.Node.PublicKey)
	if publicKey == "" {
		return swarmRemotePairingOfferPayload{}, errors.New("managed swarm public key is unavailable")
	}
	fingerprint := strings.TrimSpace(state.Node.Fingerprint)
	if fingerprint == "" {
		fingerprint = swarmruntime.FingerprintPublicKey(publicKey)
	}
	token, err := randomSwarmRemotePairingOfferToken()
	if err != nil {
		return swarmRemotePairingOfferPayload{}, fmt.Errorf("generate managed swarm offer token: %w", err)
	}
	createdAt := now.Unix()
	transports := detectedOnboardingTransports(cfg)
	offer := swarmRemotePairingOfferPayload{
		Version:              swarmRemotePairingOfferVersion,
		Type:                 "managed_swarm_offer",
		Token:                token,
		SingleUse:            true,
		SwarmID:              swarmID,
		SwarmName:            firstNonEmpty(strings.TrimSpace(cfg.SwarmName), strings.TrimSpace(state.Node.Name), "Managed swarm"),
		PublicKey:            publicKey,
		Fingerprint:          fingerprint,
		Endpoint:             endpoint,
		EndpointCandidates:   remotePairingOfferEndpointCandidates(cfg, status, endpoint),
		APIPort:              canonicalAdvertisePort(cfg),
		TransportMode:        bootstrapNetworkMode(cfg),
		RendezvousTransports: append([]onboardingTransportPayload(nil), transports...),
		CreatedAt:            createdAt,
		ExpiresAt:            now.Add(ttl).Unix(),
	}
	offer.Ceremony = swarmRemotePairingOfferCeremony{
		Code:             deriveSwarmRemotePairingOfferCeremonyCode(offer),
		VerificationOnly: true,
		Description:      "Compare this code on both hosts; it does not unlock configuration by itself.",
	}
	return offer, nil
}

func swarmRemotePairingOfferTTL(seconds int) (time.Duration, error) {
	if seconds == 0 {
		return swarmRemotePairingOfferDefaultTTL, nil
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl < swarmRemotePairingOfferMinTTL {
		return 0, fmt.Errorf("offer ttl must be at least %d seconds", int(swarmRemotePairingOfferMinTTL.Seconds()))
	}
	if ttl > swarmRemotePairingOfferMaxTTL {
		return 0, fmt.Errorf("offer ttl must be at most %d seconds", int(swarmRemotePairingOfferMaxTTL.Seconds()))
	}
	return ttl, nil
}

func randomSwarmRemotePairingOfferToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func deriveSwarmRemotePairingOfferCeremonyCode(offer swarmRemotePairingOfferPayload) string {
	transcript := swarmRemotePairingOfferTranscript{
		Version:              strings.TrimSpace(offer.Version),
		Type:                 strings.TrimSpace(offer.Type),
		Token:                strings.TrimSpace(offer.Token),
		SwarmID:              strings.TrimSpace(offer.SwarmID),
		SwarmName:            strings.TrimSpace(offer.SwarmName),
		PublicKey:            strings.TrimSpace(offer.PublicKey),
		Fingerprint:          strings.TrimSpace(offer.Fingerprint),
		Endpoint:             strings.TrimSpace(offer.Endpoint),
		APIPort:              offer.APIPort,
		TransportMode:        strings.TrimSpace(offer.TransportMode),
		RendezvousTransports: append([]onboardingTransportPayload(nil), offer.RendezvousTransports...),
		ExpiresAt:            offer.ExpiresAt,
	}
	raw, err := json.Marshal(transcript)
	if err != nil {
		raw = []byte(strings.Join([]string{transcript.Version, transcript.Type, transcript.Token, transcript.SwarmID, transcript.SwarmName, transcript.PublicKey, transcript.Fingerprint, transcript.Endpoint, strconv.Itoa(transcript.APIPort), transcript.TransportMode, strconv.FormatInt(transcript.ExpiresAt, 10)}, "\x00"))
	}
	sum := sha256.Sum256(raw)
	return strings.ToUpper(hex.EncodeToString(sum[:3]))
}

func remotePairingOfferEndpointCandidates(cfg startupconfig.FileConfig, status onboardingResponse, primaryEndpoint string) []remoteCandidateEndpointPayload {
	apiPort := canonicalAdvertisePort(cfg)
	seen := map[string]struct{}{}
	out := make([]remoteCandidateEndpointPayload, 0, 4)
	add := func(candidate remoteCandidateEndpointPayload) {
		candidate.URL = strings.TrimSpace(strings.TrimSuffix(candidate.URL, "/"))
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
	add(remotePairingOfferEndpointCandidate("primary", primaryEndpoint, apiPort))
	if tailscaleURL := firstNonEmpty(strings.TrimSpace(cfg.TailscaleURL), strings.TrimSpace(status.Tailscale.TailnetURL)); tailscaleURL != "" {
		add(remotePairingOfferEndpointCandidate("tailscale_https", tailscaleURL, apiPort))
	}
	if dnsName := strings.TrimSuffix(strings.TrimSpace(status.Tailscale.DNSName), "."); dnsName != "" {
		add(remotePairingOfferEndpointCandidate("tailscale_https", tailscalePeerURL(dnsName), apiPort))
	}
	for _, transport := range detectedOnboardingTransports(cfg) {
		kind := firstNonEmpty(strings.TrimSpace(transport.Kind), "transport")
		for _, value := range append([]string{transport.Primary}, transport.All...) {
			if endpoint := remotePairingOfferAPIEndpoint(value, apiPort); endpoint != "" {
				add(remotePairingOfferEndpointCandidate(kind+"_api", endpoint, apiPort))
			}
		}
	}
	return out
}

func remotePairingOfferAPIEndpoint(value string, apiPort int) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, "/"))
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed != nil && parsed.Scheme != "" && parsed.Hostname() != "" {
		host := parsed.Hostname()
		if port := parsed.Port(); port != "" {
			return parsed.Scheme + "://" + net.JoinHostPort(host, port)
		}
		return "http://" + net.JoinHostPort(host, strconv.Itoa(apiPort))
	}
	return "http://" + net.JoinHostPort(value, strconv.Itoa(apiPort))
}

func remotePairingOfferEndpointCandidate(kind, rawURL string, fallbackPort int) remoteCandidateEndpointPayload {
	rawURL = strings.TrimSpace(strings.TrimSuffix(rawURL, "/"))
	candidate := remoteCandidateEndpointPayload{Kind: strings.TrimSpace(kind), URL: rawURL}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil {
		candidate.Port = fallbackPort
		return candidate
	}
	candidate.Host = strings.TrimSpace(parsed.Hostname())
	candidate.Scheme = strings.TrimSpace(parsed.Scheme)
	if portText := strings.TrimSpace(parsed.Port()); portText != "" {
		if port, err := strconv.Atoi(portText); err == nil {
			candidate.Port = port
		}
	}
	if candidate.Port == 0 {
		switch strings.ToLower(candidate.Scheme) {
		case "https":
			candidate.Port = 443
		case "http":
			candidate.Port = 80
		default:
			candidate.Port = fallbackPort
		}
	}
	return candidate
}
