package client

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

type SwarmTransportSummary struct {
	Kind    string   `json:"kind"`
	Primary string   `json:"primary,omitempty"`
	All     []string `json:"all,omitempty"`
}

type SwarmInvite struct {
	ID                   string                  `json:"id"`
	Token                string                  `json:"token"`
	PrimarySwarmID       string                  `json:"primary_swarm_id"`
	PrimaryName          string                  `json:"primary_name,omitempty"`
	TransportMode        string                  `json:"transport_mode,omitempty"`
	RendezvousTransports []SwarmTransportSummary `json:"rendezvous_transports,omitempty"`
	ExpiresAt            int64                   `json:"expires_at"`
	ConsumedAt           int64                   `json:"consumed_at,omitempty"`
	CreatedAt            int64                   `json:"created_at"`
	UpdatedAt            int64                   `json:"updated_at"`
}

type SwarmEnrollment struct {
	ID                   string                  `json:"id"`
	InviteID             string                  `json:"invite_id"`
	InviteToken          string                  `json:"invite_token"`
	PrimarySwarmID       string                  `json:"primary_swarm_id"`
	ParentSwarmID        string                  `json:"parent_swarm_id,omitempty"`
	ChildSwarmID         string                  `json:"child_swarm_id"`
	ChildName            string                  `json:"child_name"`
	ChildRole            string                  `json:"child_role"`
	ChildPublicKey       string                  `json:"child_public_key"`
	ChildFingerprint     string                  `json:"child_fingerprint"`
	TransportMode        string                  `json:"transport_mode,omitempty"`
	ObservedRemoteAddr   string                  `json:"observed_remote_addr,omitempty"`
	RendezvousTransports []SwarmTransportSummary `json:"rendezvous_transports,omitempty"`
	Status               string                  `json:"status"`
	DecisionReason       string                  `json:"decision_reason,omitempty"`
	ReviewedAt           int64                   `json:"reviewed_at,omitempty"`
	CreatedAt            int64                   `json:"created_at"`
	UpdatedAt            int64                   `json:"updated_at"`
}

type SwarmTrustedPeer struct {
	SwarmID              string                  `json:"swarm_id"`
	Name                 string                  `json:"name"`
	Role                 string                  `json:"role"`
	PublicKey            string                  `json:"public_key"`
	Fingerprint          string                  `json:"fingerprint"`
	Relationship         string                  `json:"relationship"`
	ParentSwarmID        string                  `json:"parent_swarm_id,omitempty"`
	TransportMode        string                  `json:"transport_mode,omitempty"`
	RendezvousTransports []SwarmTransportSummary `json:"rendezvous_transports,omitempty"`
	ApprovedAt           int64                   `json:"approved_at"`
	CreatedAt            int64                   `json:"created_at"`
	UpdatedAt            int64                   `json:"updated_at"`
}

type SwarmLocalNodeState struct {
	SwarmID       string                  `json:"swarm_id"`
	Name          string                  `json:"name"`
	Role          string                  `json:"role"`
	PublicKey     string                  `json:"public_key,omitempty"`
	Fingerprint   string                  `json:"fingerprint,omitempty"`
	AdvertiseMode string                  `json:"advertise_mode,omitempty"`
	AdvertiseAddr string                  `json:"advertise_addr,omitempty"`
	Transports    []SwarmTransportSummary `json:"transports,omitempty"`
}

type SwarmPairingState struct {
	PairingState         string                  `json:"pairing_state"`
	ParentSwarmID        string                  `json:"parent_swarm_id,omitempty"`
	ActiveInviteID       string                  `json:"active_invite_id,omitempty"`
	LastEnrollmentID     string                  `json:"last_enrollment_id,omitempty"`
	LastDecision         string                  `json:"last_decision,omitempty"`
	LastDecisionReason   string                  `json:"last_decision_reason,omitempty"`
	LastUpdatedByRole    string                  `json:"last_updated_by_role,omitempty"`
	RendezvousTransports []SwarmTransportSummary `json:"rendezvous_transports,omitempty"`
}

type SwarmLocalState struct {
	Node         SwarmLocalNodeState `json:"node"`
	Pairing      SwarmPairingState   `json:"pairing"`
	TrustedPeers []SwarmTrustedPeer  `json:"trusted_peers"`
}

func (c *API) GetSwarmState(ctx context.Context) (SwarmLocalState, error) {
	var resp struct {
		OK    bool            `json:"ok"`
		State SwarmLocalState `json:"state"`
	}
	if err := c.getJSON(ctx, "/v1/swarm/state", &resp, true); err != nil {
		return SwarmLocalState{}, err
	}
	return resp.State, nil
}

func (c *API) CreateSwarmInvite(ctx context.Context, ttlSeconds int) (SwarmInvite, error) {
	payload := map[string]any{}
	if ttlSeconds > 0 {
		payload["ttl_seconds"] = ttlSeconds
	}
	var resp struct {
		OK     bool        `json:"ok"`
		Invite SwarmInvite `json:"invite"`
	}
	if err := c.postJSON(ctx, "/v1/swarm/invites", payload, &resp, true); err != nil {
		return SwarmInvite{}, err
	}
	if strings.TrimSpace(resp.Invite.ID) == "" {
		return SwarmInvite{}, fmt.Errorf("invite creation response missing invite data")
	}
	return resp.Invite, nil
}

func (c *API) ListPendingSwarmEnrollments(ctx context.Context) ([]SwarmEnrollment, error) {
	var resp struct {
		OK    bool              `json:"ok"`
		Items []SwarmEnrollment `json:"items"`
	}
	if err := c.getJSON(ctx, "/v1/swarm/pending-children", &resp, true); err != nil {
		return nil, err
	}
	return append([]SwarmEnrollment(nil), resp.Items...), nil
}

func (c *API) StreamSwarmEvents(ctx context.Context, lastSeen uint64, onEvent func(StreamEventEnvelope)) error {
	if c == nil {
		return fmt.Errorf("api client is not configured")
	}
	return c.StreamEvents(ctx, lastSeen, []string{"swarm:*"}, onEvent)
}

func (c *API) DecideSwarmEnrollment(ctx context.Context, enrollmentID string, approve bool, reason string) (SwarmEnrollment, []SwarmTrustedPeer, error) {
	enrollmentID = strings.TrimSpace(enrollmentID)
	if enrollmentID == "" {
		return SwarmEnrollment{}, nil, fmt.Errorf("enrollment id is required")
	}
	action := "reject"
	if approve {
		action = "approve"
	}
	path := "/v1/swarm/enrollment/" + url.PathEscape(enrollmentID) + "/" + action
	var resp struct {
		OK           bool               `json:"ok"`
		Enrollment   SwarmEnrollment    `json:"enrollment"`
		TrustedPeers []SwarmTrustedPeer `json:"trusted_peers"`
	}
	if err := c.postJSON(ctx, path, map[string]any{
		"approve": approve,
		"reason":  strings.TrimSpace(reason),
	}, &resp, true); err != nil {
		return SwarmEnrollment{}, nil, err
	}
	if strings.TrimSpace(resp.Enrollment.ID) == "" {
		return SwarmEnrollment{}, nil, fmt.Errorf("enrollment decision response missing enrollment data")
	}
	return resp.Enrollment, append([]SwarmTrustedPeer(nil), resp.TrustedPeers...), nil
}
