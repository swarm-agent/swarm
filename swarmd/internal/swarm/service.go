package swarm

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	EnrollmentStatusPending  = "pending"
	EnrollmentStatusApproved = "approved"
	EnrollmentStatusRejected = "rejected"

	RelationshipManager = "manager"
	RelationshipManaged = "managed"

	// Legacy relationship constants are kept as aliases for existing stored records.
	RelationshipParent = "parent"
	RelationshipChild  = "child"

	bootstrapRoleMaster = "master"
	bootstrapRoleChild  = "child"
)

type Service struct {
	store   *pebblestore.SwarmStore
	events  *pebblestore.EventLog
	publish func(pebblestore.EventEnvelope)
}

type TransportSummary struct {
	Kind    string   `json:"kind"`
	Primary string   `json:"primary,omitempty"`
	All     []string `json:"all,omitempty"`
}

type LocalNodeState struct {
	SwarmID       string             `json:"swarm_id"`
	Name          string             `json:"name"`
	Role          string             `json:"role"`
	PublicKey     string             `json:"public_key,omitempty"`
	Fingerprint   string             `json:"fingerprint,omitempty"`
	AdvertiseMode string             `json:"advertise_mode,omitempty"`
	AdvertiseAddr string             `json:"advertise_addr,omitempty"`
	Transports    []TransportSummary `json:"transports,omitempty"`
}

type PairingState struct {
	PairingState         string             `json:"pairing_state"`
	ParentSwarmID        string             `json:"parent_swarm_id,omitempty"`
	ActiveInviteID       string             `json:"active_invite_id,omitempty"`
	LastEnrollmentID     string             `json:"last_enrollment_id,omitempty"`
	LastDecision         string             `json:"last_decision,omitempty"`
	LastDecisionReason   string             `json:"last_decision_reason,omitempty"`
	LastUpdatedByRole    string             `json:"last_updated_by_role,omitempty"`
	RendezvousTransports []TransportSummary `json:"rendezvous_transports,omitempty"`
}

type Invite struct {
	ID                   string             `json:"id"`
	Token                string             `json:"token"`
	PrimarySwarmID       string             `json:"primary_swarm_id"`
	PrimaryName          string             `json:"primary_name,omitempty"`
	GroupID              string             `json:"group_id,omitempty"`
	TransportMode        string             `json:"transport_mode,omitempty"`
	RendezvousTransports []TransportSummary `json:"rendezvous_transports,omitempty"`
	ExpiresAt            int64              `json:"expires_at"`
	ConsumedAt           int64              `json:"consumed_at,omitempty"`
	CreatedAt            int64              `json:"created_at"`
	UpdatedAt            int64              `json:"updated_at"`
}

type Enrollment struct {
	ID                   string             `json:"id"`
	InviteID             string             `json:"invite_id"`
	InviteToken          string             `json:"invite_token"`
	PrimarySwarmID       string             `json:"primary_swarm_id"`
	ParentSwarmID        string             `json:"parent_swarm_id,omitempty"`
	GroupID              string             `json:"group_id,omitempty"`
	ChildSwarmID         string             `json:"child_swarm_id"`
	ChildName            string             `json:"child_name"`
	ChildRole            string             `json:"child_role"`
	ChildPublicKey       string             `json:"child_public_key"`
	ChildFingerprint     string             `json:"child_fingerprint"`
	TransportMode        string             `json:"transport_mode,omitempty"`
	ObservedRemoteAddr   string             `json:"observed_remote_addr,omitempty"`
	RendezvousTransports []TransportSummary `json:"rendezvous_transports,omitempty"`
	Status               string             `json:"status"`
	DecisionReason       string             `json:"decision_reason,omitempty"`
	ReviewedAt           int64              `json:"reviewed_at,omitempty"`
	CreatedAt            int64              `json:"created_at"`
	UpdatedAt            int64              `json:"updated_at"`
}

type TrustedPeer struct {
	SwarmID              string             `json:"swarm_id"`
	Name                 string             `json:"name"`
	Role                 string             `json:"role"`
	PublicKey            string             `json:"public_key"`
	Fingerprint          string             `json:"fingerprint"`
	Relationship         string             `json:"relationship"`
	ParentSwarmID        string             `json:"parent_swarm_id,omitempty"`
	TransportMode        string             `json:"transport_mode,omitempty"`
	RendezvousTransports []TransportSummary `json:"rendezvous_transports,omitempty"`
	ApprovedAt           int64              `json:"approved_at"`
	CreatedAt            int64              `json:"created_at"`
	UpdatedAt            int64              `json:"updated_at"`
}

type LocalState struct {
	Node           LocalNodeState `json:"node"`
	Pairing        PairingState   `json:"pairing"`
	TrustedPeers   []TrustedPeer  `json:"trusted_peers"`
	CurrentGroupID string         `json:"current_group_id,omitempty"`
	Groups         []GroupState   `json:"groups,omitempty"`
}

type EnsureLocalStateInput struct {
	SwarmID       string
	Name          string
	Role          string
	SwarmMode     bool
	PublicKey     string
	PrivateKey    string
	Fingerprint   string
	AdvertiseMode string
	AdvertiseAddr string
	Transports    []TransportSummary
}

type CreateInviteInput struct {
	PrimarySwarmID       string
	PrimaryName          string
	GroupID              string
	TransportMode        string
	RendezvousTransports []TransportSummary
	TTL                  time.Duration
	Token                string
}

type EnsureInviteInput struct {
	Token                string
	PrimarySwarmID       string
	PrimaryName          string
	GroupID              string
	TransportMode        string
	RendezvousTransports []TransportSummary
	TTL                  time.Duration
}

type SubmitEnrollmentInput struct {
	InviteToken           string
	PrimarySwarmID        string
	GroupID               string
	ChildSwarmID          string
	ChildName             string
	ChildRole             string
	ChildPublicKey        string
	TransportMode         string
	ObservedRemoteAddr    string
	RendezvousTransports  []TransportSummary
	IncomingPeerAuthToken string
}

type DecideEnrollmentInput struct {
	EnrollmentID          string
	Approve               bool
	Reason                string
	IncomingPeerAuthToken string
}

type PrepareRemoteBootstrapParentPeerInput struct {
	ParentSwarmID         string
	ParentName            string
	ParentPublicKey       string
	ParentFingerprint     string
	TransportMode         string
	RendezvousTransports  []TransportSummary
	OutgoingPeerAuthToken string
	IncomingPeerAuthToken string
}

type ApproveManagedPairingInput struct {
	ManagerSwarmID        string
	ManagerName           string
	ManagerPublicKey      string
	ManagerFingerprint    string
	TransportMode         string
	RendezvousTransports  []TransportSummary
	OutgoingPeerAuthToken string
	IncomingPeerAuthToken string
}

type TrustManagedPeerInput struct {
	ManagedSwarmID        string
	ManagedName           string
	ManagedRole           string
	ManagedPublicKey      string
	ManagedFingerprint    string
	TransportMode         string
	RendezvousTransports  []TransportSummary
	OutgoingPeerAuthToken string
	IncomingPeerAuthToken string
}

func NewService(store *pebblestore.SwarmStore, events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) *Service {
	return &Service{store: store, events: events, publish: publish}
}

func GenerateNodeKeypair() (string, string, string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", "", fmt.Errorf("generate ed25519 keypair: %w", err)
	}
	publicKeyText := base64.StdEncoding.EncodeToString(publicKey)
	privateKeyText := base64.StdEncoding.EncodeToString(privateKey)
	return publicKeyText, privateKeyText, FingerprintPublicKey(publicKeyText), nil
}

func FingerprintPublicKey(publicKey string) string {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil {
		decoded = []byte(strings.TrimSpace(publicKey))
	}
	sum := sha256.Sum256(decoded)
	return hex.EncodeToString(sum[:])
}

func GenerateSwarmID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate swarm id: %w", err)
	}
	return "swarm_" + hex.EncodeToString(buf), nil
}

func GeneratePeerAuthToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate peer auth token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func HashPeerAuthToken(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func (s *Service) OutgoingPeerAuthToken(swarmID string) (string, bool, error) {
	if s == nil || s.store == nil {
		return "", false, errors.New("swarm service is not configured")
	}
	record, ok, err := s.store.GetTrustedPeer(strings.TrimSpace(swarmID))
	if err != nil || !ok {
		return "", ok, err
	}
	token := strings.TrimSpace(record.OutgoingPeerAuthToken)
	if token == "" {
		return "", false, nil
	}
	return token, true, nil
}

func (s *Service) PrepareRemoteBootstrapParentPeer(input PrepareRemoteBootstrapParentPeerInput) error {
	if s == nil || s.store == nil {
		return errors.New("swarm service is not configured")
	}
	parentSwarmID := strings.TrimSpace(input.ParentSwarmID)
	if parentSwarmID == "" {
		return errors.New("parent swarm id is required")
	}
	existing, _, err := s.store.GetTrustedPeer(parentSwarmID)
	if err != nil {
		return err
	}
	transports := toStoreTransports(input.RendezvousTransports)
	if len(transports) == 0 {
		transports = existing.RendezvousTransports
	}
	incomingPeerAuthHash := strings.TrimSpace(existing.IncomingPeerAuthHash)
	if token := strings.TrimSpace(input.IncomingPeerAuthToken); token != "" {
		incomingPeerAuthHash = HashPeerAuthToken(token)
	}
	approvedAt := existing.ApprovedAt
	if approvedAt <= 0 {
		approvedAt = time.Now().UnixMilli()
	}
	_, err = s.store.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
		SwarmID:               parentSwarmID,
		Name:                  firstNonEmpty(strings.TrimSpace(input.ParentName), existing.Name, "Manager"),
		Role:                  firstNonEmpty(existing.Role, "manager"),
		PublicKey:             firstNonEmpty(strings.TrimSpace(input.ParentPublicKey), existing.PublicKey),
		Fingerprint:           firstNonEmpty(strings.TrimSpace(input.ParentFingerprint), existing.Fingerprint),
		Relationship:          RelationshipManager,
		ParentSwarmID:         "",
		TransportMode:         firstNonEmpty(strings.TrimSpace(input.TransportMode), existing.TransportMode, startupconfig.NetworkModeTailscale),
		RendezvousTransports:  transports,
		OutgoingPeerAuthToken: firstNonEmpty(strings.TrimSpace(input.OutgoingPeerAuthToken), existing.OutgoingPeerAuthToken),
		IncomingPeerAuthHash:  incomingPeerAuthHash,
		ApprovedAt:            approvedAt,
	})
	return err
}

func (s *Service) ApproveManagedPairing(input ApproveManagedPairingInput) (PairingState, error) {
	if s == nil || s.store == nil {
		return PairingState{}, errors.New("swarm service is not configured")
	}
	managerSwarmID := strings.TrimSpace(input.ManagerSwarmID)
	if managerSwarmID == "" {
		return PairingState{}, errors.New("manager swarm id is required")
	}
	if err := s.PrepareRemoteBootstrapParentPeer(PrepareRemoteBootstrapParentPeerInput{
		ParentSwarmID:         managerSwarmID,
		ParentName:            input.ManagerName,
		ParentPublicKey:       input.ManagerPublicKey,
		ParentFingerprint:     input.ManagerFingerprint,
		TransportMode:         input.TransportMode,
		RendezvousTransports:  input.RendezvousTransports,
		OutgoingPeerAuthToken: strings.TrimSpace(input.OutgoingPeerAuthToken),
		IncomingPeerAuthToken: strings.TrimSpace(input.IncomingPeerAuthToken),
	}); err != nil {
		return PairingState{}, err
	}
	record, ok, err := s.store.GetLocalPairing()
	if err != nil {
		return PairingState{}, err
	}
	if !ok {
		record = pebblestore.SwarmLocalPairingRecord{}
	}
	record.PairingState = startupconfig.PairingStatePaired
	record.ParentSwarmID = managerSwarmID
	record.LastUpdatedByRole = "managed"
	if transports := toStoreTransports(input.RendezvousTransports); len(transports) > 0 {
		record.RendezvousTransports = transports
	}
	record, err = s.store.PutLocalPairing(record)
	if err != nil {
		return PairingState{}, err
	}
	return toPairingState(record), nil
}

func (s *Service) TrustManagedPeer(input TrustManagedPeerInput) (TrustedPeer, error) {
	if s == nil || s.store == nil {
		return TrustedPeer{}, errors.New("swarm service is not configured")
	}
	managedSwarmID := strings.TrimSpace(input.ManagedSwarmID)
	if managedSwarmID == "" {
		return TrustedPeer{}, errors.New("managed swarm id is required")
	}
	existing, _, err := s.store.GetTrustedPeer(managedSwarmID)
	if err != nil {
		return TrustedPeer{}, err
	}
	transports := toStoreTransports(input.RendezvousTransports)
	if len(transports) == 0 {
		transports = existing.RendezvousTransports
	}
	incomingPeerAuthHash := strings.TrimSpace(existing.IncomingPeerAuthHash)
	if token := strings.TrimSpace(input.IncomingPeerAuthToken); token != "" {
		incomingPeerAuthHash = HashPeerAuthToken(token)
	}
	approvedAt := existing.ApprovedAt
	if approvedAt <= 0 {
		approvedAt = time.Now().UnixMilli()
	}
	role := firstNonEmpty(strings.TrimSpace(input.ManagedRole), existing.Role, RelationshipManaged)
	peer, err := s.store.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
		SwarmID:               managedSwarmID,
		Name:                  firstNonEmpty(strings.TrimSpace(input.ManagedName), existing.Name, "Managed swarm"),
		Role:                  role,
		PublicKey:             firstNonEmpty(strings.TrimSpace(input.ManagedPublicKey), existing.PublicKey),
		Fingerprint:           firstNonEmpty(strings.TrimSpace(input.ManagedFingerprint), existing.Fingerprint),
		Relationship:          RelationshipManaged,
		ParentSwarmID:         "",
		TransportMode:         firstNonEmpty(strings.TrimSpace(input.TransportMode), existing.TransportMode, startupconfig.NetworkModeTailscale),
		RendezvousTransports:  transports,
		OutgoingPeerAuthToken: firstNonEmpty(strings.TrimSpace(input.OutgoingPeerAuthToken), existing.OutgoingPeerAuthToken),
		IncomingPeerAuthHash:  incomingPeerAuthHash,
		ApprovedAt:            approvedAt,
	})
	if err != nil {
		return TrustedPeer{}, err
	}
	return toTrustedPeer(peer), nil
}

func (s *Service) ValidateIncomingPeerAuth(swarmID, rawToken string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("swarm service is not configured")
	}
	record, ok, err := s.store.GetTrustedPeer(strings.TrimSpace(swarmID))
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	expected := strings.TrimSpace(record.IncomingPeerAuthHash)
	provided := HashPeerAuthToken(rawToken)
	if expected == "" || provided == "" || len(expected) != len(provided) {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1, nil
}

func (s *Service) EnsureLocalState(input EnsureLocalStateInput) (LocalState, error) {
	if s == nil || s.store == nil {
		return LocalState{}, errors.New("swarm service is not configured")
	}
	swarmID := strings.TrimSpace(input.SwarmID)
	nodeRecord, ok, err := s.store.GetLocalNode()
	if err != nil {
		return LocalState{}, err
	}
	if !ok {
		nodeRecord = pebblestore.SwarmLocalNodeRecord{}
	}
	if swarmID == "" {
		swarmID = strings.TrimSpace(nodeRecord.SwarmID)
	}
	if swarmID == "" {
		generated, err := GenerateSwarmID()
		if err != nil {
			return LocalState{}, err
		}
		swarmID = generated
	}
	nodeRecord.SwarmID = swarmID
	nodeRecord.Name = strings.TrimSpace(input.Name)
	nodeRecord.Role = strings.ToLower(strings.TrimSpace(input.Role))
	publicKey := firstNonEmpty(strings.TrimSpace(input.PublicKey), strings.TrimSpace(nodeRecord.PublicKey))
	privateKey := firstNonEmpty(strings.TrimSpace(input.PrivateKey), strings.TrimSpace(nodeRecord.PrivateKey))
	fingerprint := firstNonEmpty(strings.TrimSpace(input.Fingerprint), strings.TrimSpace(nodeRecord.Fingerprint))
	if publicKey == "" || privateKey == "" {
		generatedPublicKey, generatedPrivateKey, generatedFingerprint, err := GenerateNodeKeypair()
		if err != nil {
			return LocalState{}, err
		}
		if publicKey == "" {
			publicKey = generatedPublicKey
		}
		if privateKey == "" {
			privateKey = generatedPrivateKey
		}
		if fingerprint == "" {
			fingerprint = generatedFingerprint
		}
	}
	if fingerprint == "" && publicKey != "" {
		fingerprint = FingerprintPublicKey(publicKey)
	}
	nodeRecord.PublicKey = publicKey
	nodeRecord.PrivateKey = privateKey
	nodeRecord.Fingerprint = fingerprint
	nodeRecord.AdvertiseMode = strings.ToLower(strings.TrimSpace(input.AdvertiseMode))
	nodeRecord.AdvertiseAddr = strings.TrimSpace(input.AdvertiseAddr)
	nodeRecord.Transports = toStoreTransports(input.Transports)
	nodeRecord, err = s.store.PutLocalNode(nodeRecord)
	if err != nil {
		return LocalState{}, err
	}
	pairingRecord, ok, err := s.store.GetLocalPairing()
	if err != nil {
		return LocalState{}, err
	}
	if !ok {
		pairingRecord = pebblestore.SwarmLocalPairingRecord{PairingState: startupconfig.PairingStateUnpaired}
		pairingRecord, err = s.store.PutLocalPairing(pairingRecord)
		if err != nil {
			return LocalState{}, err
		}
	}
	trustedPeers, err := s.store.ListTrustedPeers(500)
	if err != nil {
		return LocalState{}, err
	}
	if !input.SwarmMode {
		return LocalState{
			Node:         toLocalNodeState(nodeRecord),
			Pairing:      toPairingState(pairingRecord),
			TrustedPeers: toTrustedPeers(trustedPeers),
		}, nil
	}
	currentGroupID, err := s.EnsureGroupForLocalState(nodeRecord, input.SwarmMode)
	if err != nil {
		return LocalState{}, err
	}
	groups, storedCurrentGroupID, err := s.ListGroupsForSwarm(nodeRecord.SwarmID, 500)
	if err != nil {
		return LocalState{}, err
	}
	if currentGroupID == "" {
		currentGroupID = storedCurrentGroupID
	}
	return LocalState{
		Node:           toLocalNodeState(nodeRecord),
		Pairing:        toPairingState(pairingRecord),
		TrustedPeers:   toTrustedPeers(trustedPeers),
		CurrentGroupID: currentGroupID,
		Groups:         groups,
	}, nil
}

func (s *Service) CreateInvite(input CreateInviteInput) (Invite, error) {
	if s == nil || s.store == nil {
		return Invite{}, errors.New("swarm service is not configured")
	}
	record, err := s.buildInviteRecord(strings.TrimSpace(input.PrimarySwarmID), strings.TrimSpace(input.PrimaryName), strings.TrimSpace(input.GroupID), strings.TrimSpace(input.TransportMode), input.RendezvousTransports, input.TTL, strings.TrimSpace(input.Token))
	if err != nil {
		return Invite{}, err
	}
	record, err = s.store.PutInvite(record)
	if err != nil {
		return Invite{}, err
	}
	_, _ = s.appendEvent("swarm:pairing", "swarm.invite.created", record.ID, record)
	return toInvite(record), nil
}

func (s *Service) EnsureInvite(input EnsureInviteInput) (Invite, error) {
	if s == nil || s.store == nil {
		return Invite{}, errors.New("swarm service is not configured")
	}
	token := strings.TrimSpace(input.Token)
	if token == "" {
		return Invite{}, errors.New("invite token is required")
	}
	existing, ok, err := s.store.FindInviteByToken(token)
	if err != nil {
		return Invite{}, err
	}
	if ok {
		if err := s.validateInviteRecord(existing, strings.TrimSpace(input.PrimarySwarmID), strings.TrimSpace(input.GroupID)); err != nil {
			return Invite{}, err
		}
		ttl := input.TTL
		if ttl <= 0 {
			ttl = 15 * time.Minute
		}
		if existing.ConsumedAt == 0 && existing.ExpiresAt > 0 && existing.ExpiresAt < time.Now().UnixMilli() {
			existing.ExpiresAt = time.Now().Add(ttl).UnixMilli()
			existing, err = s.store.PutInvite(existing)
			if err != nil {
				return Invite{}, err
			}
		}
		return toInvite(existing), nil
	}
	record, err := s.buildInviteRecord(strings.TrimSpace(input.PrimarySwarmID), strings.TrimSpace(input.PrimaryName), strings.TrimSpace(input.GroupID), strings.TrimSpace(input.TransportMode), input.RendezvousTransports, input.TTL, token)
	if err != nil {
		return Invite{}, err
	}
	record, err = s.store.PutInvite(record)
	if err != nil {
		return Invite{}, err
	}
	_, _ = s.appendEvent("swarm:pairing", "swarm.invite.restored", record.ID, record)
	return toInvite(record), nil
}

func (s *Service) buildInviteRecord(primarySwarmID, primaryName, groupID, transportMode string, rendezvous []TransportSummary, ttl time.Duration, token string) (pebblestore.SwarmInviteRecord, error) {
	if primarySwarmID == "" {
		return pebblestore.SwarmInviteRecord{}, errors.New("primary swarm id is required")
	}
	if localNode, ok, err := s.store.GetLocalNode(); err != nil {
		return pebblestore.SwarmInviteRecord{}, err
	} else if ok && strings.EqualFold(strings.TrimSpace(localNode.SwarmID), primarySwarmID) {
		if groupID == "" {
			currentGroupID, currentOK, err := s.store.GetCurrentGroupID()
			if err != nil {
				return pebblestore.SwarmInviteRecord{}, err
			}
			if currentOK {
				groupID = currentGroupID
			}
		}
		if groupID == "" {
			group, err := s.ensureLocalHostGroup(localNode, firstNonEmpty(primaryName, localNode.Name))
			if err != nil {
				return pebblestore.SwarmInviteRecord{}, err
			}
			groupID = group.ID
		}
	}
	if groupID != "" {
		group, ok, err := s.store.GetGroup(groupID)
		if err != nil {
			return pebblestore.SwarmInviteRecord{}, err
		}
		if !ok {
			return pebblestore.SwarmInviteRecord{}, errors.New("group not found")
		}
		if !strings.EqualFold(group.HostSwarmID, primarySwarmID) {
			return pebblestore.SwarmInviteRecord{}, errors.New("invite group must be hosted by the primary swarm")
		}
	}
	inviteID, err := GenerateSwarmID()
	if err != nil {
		return pebblestore.SwarmInviteRecord{}, err
	}
	if token == "" {
		token, err = randomHex(24)
		if err != nil {
			return pebblestore.SwarmInviteRecord{}, err
		}
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	now := time.Now().UnixMilli()
	return pebblestore.SwarmInviteRecord{
		ID:                   inviteID,
		Token:                token,
		PrimarySwarmID:       primarySwarmID,
		PrimaryName:          primaryName,
		GroupID:              groupID,
		TransportMode:        strings.ToLower(strings.TrimSpace(transportMode)),
		RendezvousTransports: toStoreTransports(rendezvous),
		ExpiresAt:            now + ttl.Milliseconds(),
	}, nil
}

func (s *Service) validateInviteRecord(record pebblestore.SwarmInviteRecord, primarySwarmID, groupID string) error {
	if primarySwarmID != "" && !strings.EqualFold(strings.TrimSpace(record.PrimarySwarmID), primarySwarmID) {
		return errors.New("invite token belongs to a different primary swarm")
	}
	if groupID != "" && !strings.EqualFold(strings.TrimSpace(record.GroupID), groupID) {
		return errors.New("invite token belongs to a different group")
	}
	return nil
}

func (s *Service) SubmitEnrollment(input SubmitEnrollmentInput) (Enrollment, error) {
	if s == nil || s.store == nil {
		return Enrollment{}, errors.New("swarm service is not configured")
	}
	invite, ok, err := s.store.FindInviteByToken(input.InviteToken)
	if err != nil {
		return Enrollment{}, err
	}
	if !ok {
		return Enrollment{}, errors.New("invite token not found")
	}
	now := time.Now().UnixMilli()
	if invite.ExpiresAt > 0 && invite.ExpiresAt < now {
		return Enrollment{}, errors.New("invite token has expired")
	}
	enrollmentID, err := GenerateSwarmID()
	if err != nil {
		return Enrollment{}, err
	}
	record := pebblestore.SwarmEnrollmentRecord{
		ID:                   enrollmentID,
		InviteID:             invite.ID,
		InviteToken:          strings.TrimSpace(input.InviteToken),
		PrimarySwarmID:       firstNonEmpty(strings.TrimSpace(input.PrimarySwarmID), invite.PrimarySwarmID),
		GroupID:              firstNonEmpty(strings.TrimSpace(input.GroupID), invite.GroupID),
		ChildSwarmID:         strings.TrimSpace(input.ChildSwarmID),
		ChildName:            strings.TrimSpace(input.ChildName),
		ChildRole:            strings.ToLower(strings.TrimSpace(input.ChildRole)),
		ChildPublicKey:       strings.TrimSpace(input.ChildPublicKey),
		ChildFingerprint:     FingerprintPublicKey(input.ChildPublicKey),
		TransportMode:        strings.ToLower(strings.TrimSpace(input.TransportMode)),
		ObservedRemoteAddr:   strings.TrimSpace(input.ObservedRemoteAddr),
		RendezvousTransports: toStoreTransports(input.RendezvousTransports),
		Status:               EnrollmentStatusPending,
	}
	if record.ChildSwarmID == "" {
		return Enrollment{}, errors.New("child swarm id is required")
	}
	if record.ChildName == "" {
		return Enrollment{}, errors.New("child name is required")
	}
	if record.ChildRole == "" {
		record.ChildRole = bootstrapRoleChild
	}
	if record.ChildPublicKey == "" {
		return Enrollment{}, errors.New("child public key is required")
	}
	record, err = s.store.PutEnrollment(record)
	if err != nil {
		return Enrollment{}, err
	}
	invite.ConsumedAt = now
	invite.UpdatedAt = now
	if _, err := s.store.PutInvite(invite); err != nil {
		return Enrollment{}, err
	}
	_, _ = s.appendEvent("swarm:pairing", "swarm.enrollment.pending", record.ID, record)
	return toEnrollment(record), nil
}

func (s *Service) ListPendingEnrollments(limit int) ([]Enrollment, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm service is not configured")
	}
	records, err := s.store.ListEnrollments(limit)
	if err != nil {
		return nil, err
	}
	out := make([]Enrollment, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.Status) != EnrollmentStatusPending {
			continue
		}
		out = append(out, toEnrollment(record))
	}
	return out, nil
}

func (s *Service) DecideEnrollment(input DecideEnrollmentInput) (Enrollment, []TrustedPeer, error) {
	if s == nil || s.store == nil {
		return Enrollment{}, nil, errors.New("swarm service is not configured")
	}
	record, ok, err := s.store.GetEnrollment(input.EnrollmentID)
	if err != nil {
		return Enrollment{}, nil, err
	}
	if !ok {
		return Enrollment{}, nil, errors.New("enrollment not found")
	}
	now := time.Now().UnixMilli()
	record.ReviewedAt = now
	record.DecisionReason = strings.TrimSpace(input.Reason)
	trustedPeers := []TrustedPeer{}
	if input.Approve {
		record.Status = EnrollmentStatusApproved
		record.ParentSwarmID = record.PrimarySwarmID
		localNode, localOK, err := s.store.GetLocalNode()
		if err != nil {
			return Enrollment{}, nil, err
		}
		if !localOK {
			return Enrollment{}, nil, errors.New("local swarm node is not configured")
		}
		groupID := strings.TrimSpace(record.GroupID)
		if groupID == "" {
			group, err := s.ensureLocalHostGroup(localNode, localNode.Name)
			if err != nil {
				return Enrollment{}, nil, err
			}
			groupID = group.ID
			record.GroupID = group.ID
		}
		if _, err := s.UpsertGroupMember(UpsertGroupMemberInput{
			GroupID:        groupID,
			SwarmID:        record.PrimarySwarmID,
			Name:           firstNonEmpty(localNode.Name, "Primary"),
			SwarmRole:      bootstrapRoleMaster,
			MembershipRole: GroupMembershipRoleHost,
		}); err != nil {
			return Enrollment{}, nil, err
		}
		if _, err := s.UpsertGroupMember(UpsertGroupMemberInput{
			GroupID:        groupID,
			SwarmID:        record.ChildSwarmID,
			Name:           record.ChildName,
			SwarmRole:      record.ChildRole,
			MembershipRole: GroupMembershipRoleMember,
		}); err != nil {
			return Enrollment{}, nil, err
		}
		primaryPeer, err := s.store.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
			SwarmID:              record.PrimarySwarmID,
			Name:                 "Primary",
			Role:                 bootstrapRoleMaster,
			Relationship:         RelationshipManager,
			ParentSwarmID:        "",
			TransportMode:        record.TransportMode,
			RendezvousTransports: toStoreTransports(fromStoreTransports(record.RendezvousTransports)),
			ApprovedAt:           now,
		})
		if err != nil {
			return Enrollment{}, nil, err
		}
		childPeer, err := s.store.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{
			SwarmID:               record.ChildSwarmID,
			Name:                  record.ChildName,
			Role:                  record.ChildRole,
			PublicKey:             record.ChildPublicKey,
			Fingerprint:           record.ChildFingerprint,
			Relationship:          RelationshipManaged,
			ParentSwarmID:         record.PrimarySwarmID,
			TransportMode:         record.TransportMode,
			RendezvousTransports:  record.RendezvousTransports,
			OutgoingPeerAuthToken: strings.TrimSpace(record.InviteToken),
			IncomingPeerAuthHash:  HashPeerAuthToken(input.IncomingPeerAuthToken),
			ApprovedAt:            now,
		})
		if err != nil {
			return Enrollment{}, nil, err
		}
		trustedPeers = []TrustedPeer{toTrustedPeer(primaryPeer), toTrustedPeer(childPeer)}
		record, err = s.store.PutEnrollment(record)
		if err != nil {
			return Enrollment{}, nil, err
		}
		_, _ = s.appendEvent("swarm:pairing", "swarm.enrollment.approved", record.ID, record)
	} else {
		record.Status = EnrollmentStatusRejected
		record, err = s.store.PutEnrollment(record)
		if err != nil {
			return Enrollment{}, nil, err
		}
		_, _ = s.appendEvent("swarm:pairing", "swarm.enrollment.rejected", record.ID, record)
	}
	return toEnrollment(record), trustedPeers, nil
}

func (s *Service) UpdateLocalPairingFromConfig(cfg startupconfig.FileConfig, transports []TransportSummary) (PairingState, error) {
	if s == nil || s.store == nil {
		return PairingState{}, errors.New("swarm service is not configured")
	}
	record, ok, err := s.store.GetLocalPairing()
	if err != nil {
		return PairingState{}, err
	}
	if !ok {
		record = pebblestore.SwarmLocalPairingRecord{PairingState: startupconfig.PairingStateUnpaired}
	}
	if cfg.Child {
		if strings.TrimSpace(record.PairingState) == "" || strings.EqualFold(strings.TrimSpace(record.PairingState), startupconfig.PairingStateUnpaired) {
			record.PairingState = startupconfig.PairingStatePendingApproval
		}
		if strings.TrimSpace(record.LastUpdatedByRole) == "" {
			record.LastUpdatedByRole = bootstrapRoleChild
		}
	} else {
		record.PairingState = startupconfig.PairingStateUnpaired
		record.ParentSwarmID = ""
		record.ActiveInviteID = ""
		record.LastEnrollmentID = ""
		record.LastDecision = ""
		record.LastDecisionReason = ""
		record.LastUpdatedByRole = bootstrapRoleMaster
	}
	record.RendezvousTransports = toStoreTransports(transports)
	record, err = s.store.PutLocalPairing(record)
	if err != nil {
		return PairingState{}, err
	}
	return toPairingState(record), nil
}

func (s *Service) DetachToStandalone(localSwarmID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm service is not configured")
	}
	localSwarmID = strings.TrimSpace(localSwarmID)

	pairingRecord, ok, err := s.store.GetLocalPairing()
	if err != nil {
		return err
	}
	if !ok {
		pairingRecord = pebblestore.SwarmLocalPairingRecord{}
	}
	pairingRecord.PairingState = startupconfig.PairingStateUnpaired
	pairingRecord.ParentSwarmID = ""
	pairingRecord.ActiveInviteID = ""
	pairingRecord.LastEnrollmentID = ""
	pairingRecord.LastDecision = ""
	pairingRecord.LastDecisionReason = ""
	pairingRecord.LastUpdatedByRole = bootstrapRoleMaster
	pairingRecord.RendezvousTransports = nil
	if _, err := s.store.PutLocalPairing(pairingRecord); err != nil {
		return err
	}

	trustedPeers, err := s.store.ListTrustedPeers(500)
	if err != nil {
		return err
	}
	for _, peer := range trustedPeers {
		if localSwarmID != "" && strings.EqualFold(strings.TrimSpace(peer.SwarmID), localSwarmID) {
			continue
		}
		if err := s.store.DeleteTrustedPeer(peer.SwarmID); err != nil {
			return err
		}
	}
	_, _ = s.appendEvent("swarm:pairing", "swarm.detached.standalone", localSwarmID, map[string]any{"swarm_id": localSwarmID})
	return nil
}

func (s *Service) appendEvent(streamName, eventType, entityID string, payload any) (*pebblestore.EventEnvelope, error) {
	if s == nil || s.events == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	env, err := s.events.Append(streamName, eventType, entityID, raw, "", "")
	if err != nil {
		return nil, err
	}
	if s.publish != nil {
		s.publish(env)
	}
	return &env, nil
}

func toLocalNodeState(record pebblestore.SwarmLocalNodeRecord) LocalNodeState {
	return LocalNodeState{
		SwarmID:       record.SwarmID,
		Name:          record.Name,
		Role:          record.Role,
		PublicKey:     record.PublicKey,
		Fingerprint:   record.Fingerprint,
		AdvertiseMode: record.AdvertiseMode,
		AdvertiseAddr: record.AdvertiseAddr,
		Transports:    fromStoreTransports(record.Transports),
	}
}

func toPairingState(record pebblestore.SwarmLocalPairingRecord) PairingState {
	return PairingState{
		PairingState:         record.PairingState,
		ParentSwarmID:        record.ParentSwarmID,
		ActiveInviteID:       record.ActiveInviteID,
		LastEnrollmentID:     record.LastEnrollmentID,
		LastDecision:         record.LastDecision,
		LastDecisionReason:   record.LastDecisionReason,
		LastUpdatedByRole:    record.LastUpdatedByRole,
		RendezvousTransports: fromStoreTransports(record.RendezvousTransports),
	}
}

func toInvite(record pebblestore.SwarmInviteRecord) Invite {
	return Invite{
		ID:                   record.ID,
		Token:                record.Token,
		PrimarySwarmID:       record.PrimarySwarmID,
		PrimaryName:          record.PrimaryName,
		GroupID:              record.GroupID,
		TransportMode:        record.TransportMode,
		RendezvousTransports: fromStoreTransports(record.RendezvousTransports),
		ExpiresAt:            record.ExpiresAt,
		ConsumedAt:           record.ConsumedAt,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}
}

func toEnrollment(record pebblestore.SwarmEnrollmentRecord) Enrollment {
	return Enrollment{
		ID:                   record.ID,
		InviteID:             record.InviteID,
		InviteToken:          record.InviteToken,
		PrimarySwarmID:       record.PrimarySwarmID,
		ParentSwarmID:        record.ParentSwarmID,
		GroupID:              record.GroupID,
		ChildSwarmID:         record.ChildSwarmID,
		ChildName:            record.ChildName,
		ChildRole:            record.ChildRole,
		ChildPublicKey:       record.ChildPublicKey,
		ChildFingerprint:     record.ChildFingerprint,
		TransportMode:        record.TransportMode,
		ObservedRemoteAddr:   record.ObservedRemoteAddr,
		RendezvousTransports: fromStoreTransports(record.RendezvousTransports),
		Status:               record.Status,
		DecisionReason:       record.DecisionReason,
		ReviewedAt:           record.ReviewedAt,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}
}

func toTrustedPeer(record pebblestore.SwarmTrustedPeerRecord) TrustedPeer {
	return TrustedPeer{
		SwarmID:              record.SwarmID,
		Name:                 record.Name,
		Role:                 record.Role,
		PublicKey:            record.PublicKey,
		Fingerprint:          record.Fingerprint,
		Relationship:         record.Relationship,
		ParentSwarmID:        record.ParentSwarmID,
		TransportMode:        record.TransportMode,
		RendezvousTransports: fromStoreTransports(record.RendezvousTransports),
		ApprovedAt:           record.ApprovedAt,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}
}

func toTrustedPeers(records []pebblestore.SwarmTrustedPeerRecord) []TrustedPeer {
	out := make([]TrustedPeer, 0, len(records))
	for _, record := range records {
		out = append(out, toTrustedPeer(record))
	}
	return out
}

func toStoreTransports(records []TransportSummary) []pebblestore.SwarmTransportRecord {
	out := make([]pebblestore.SwarmTransportRecord, 0, len(records))
	for _, record := range records {
		out = append(out, pebblestore.SwarmTransportRecord{Kind: record.Kind, Primary: record.Primary, All: append([]string(nil), record.All...)})
	}
	return out
}

func fromStoreTransports(records []pebblestore.SwarmTransportRecord) []TransportSummary {
	out := make([]TransportSummary, 0, len(records))
	for _, record := range records {
		out = append(out, TransportSummary{Kind: record.Kind, Primary: record.Primary, All: append([]string(nil), record.All...)})
	}
	return out
}

func randomHex(size int) (string, error) {
	if size <= 0 {
		return "", errors.New("random size must be positive")
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
