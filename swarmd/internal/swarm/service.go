package swarm

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	RelationshipManager = "manager"
	RelationshipManaged = "managed"

	// Legacy relationship constants are kept as aliases for existing stored records.
	RelationshipParent = "parent"
	RelationshipChild  = "child"

	bootstrapRoleMaster = "master"

	mintReportStatePending   = "pending"
	mintReportStateCompleted = "completed"
)

type Service struct {
	store   *pebblestore.SwarmStore
	events  *pebblestore.EventLog
	publish func(pebblestore.EventEnvelope)
}

// Retired pairing service data types remain as source-compatible placeholders
// until the API layer removes its now-unregistered interface methods.
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
	ID                   string
	Token                string
	PrimarySwarmID       string
	PrimaryName          string
	GroupID              string
	TransportMode        string
	RendezvousTransports []TransportSummary
	ExpiresAt            int64
	ConsumedAt           int64
	CreatedAt            int64
	UpdatedAt            int64
}
type Enrollment struct {
	ID                   string
	InviteID             string
	InviteToken          string
	PrimarySwarmID       string
	ParentSwarmID        string
	GroupID              string
	ChildSwarmID         string
	ChildName            string
	ChildRole            string
	ChildPublicKey       string
	ChildFingerprint     string
	TransportMode        string
	ObservedRemoteAddr   string
	RendezvousTransports []TransportSummary
	Status               string
	DecisionReason       string
	ReviewedAt           int64
	CreatedAt            int64
	UpdatedAt            int64
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
	PublicKey     string
	PrivateKey    string
	Fingerprint   string
	AdvertiseMode string
	AdvertiseAddr string
	Transports    []TransportSummary
}

type RenameLocalSwarmInput struct {
	Name string
}

func NewService(store *pebblestore.SwarmStore, events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) *Service {
	return &Service{store: store, events: events, publish: publish}
}

func (s *Service) OutgoingPeerAuthToken(string) (string, bool, error) {
	return "", false, errors.New("remote peer authentication has been removed")
}

func (s *Service) ValidateIncomingPeerAuth(string, string) (bool, error) {
	return false, errors.New("remote peer authentication has been removed")
}

func (s *Service) CreateInvite(CreateInviteInput) (Invite, error) {
	return Invite{}, errors.New("swarm pairing has been removed")
}

func (s *Service) EnsureInvite(EnsureInviteInput) (Invite, error) {
	return Invite{}, errors.New("swarm pairing has been removed")
}

func (s *Service) SubmitEnrollment(SubmitEnrollmentInput) (Enrollment, error) {
	return Enrollment{}, errors.New("swarm enrollment has been removed")
}

func (s *Service) ListPendingEnrollments(int) ([]Enrollment, error) {
	return nil, errors.New("swarm enrollment has been removed")
}

func (s *Service) DecideEnrollment(DecideEnrollmentInput) (Enrollment, []TrustedPeer, error) {
	return Enrollment{}, nil, errors.New("swarm enrollment has been removed")
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

func (s *Service) EnsureLocalState(input EnsureLocalStateInput) (LocalState, error) {
	if s == nil || s.store == nil {
		return LocalState{}, errors.New("swarm service is not configured")
	}
	nodeRecord, ok, err := s.store.GetLocalNode()
	if err != nil {
		return LocalState{}, err
	}
	if !ok {
		nodeRecord = pebblestore.SwarmLocalNodeRecord{}
	}
	// Once minted, the persisted local swarm ID is canonical for this daemon.
	// Callers may supply an ID only while initializing an empty store (for
	// explicit restore/migration paths); they cannot remap an existing daemon.
	swarmID := strings.TrimSpace(nodeRecord.SwarmID)
	if swarmID == "" {
		swarmID = strings.TrimSpace(input.SwarmID)
	}
	if swarmID == "" {
		generated, err := GenerateSwarmID()
		if err != nil {
			return LocalState{}, err
		}
		swarmID = generated
		// Only an identity genuinely generated by this bootstrap path is a mint.
		// Existing records and caller-supplied restore/migration IDs remain
		// unmarked so upgrades and restores cannot create retroactive reports.
		nodeRecord.MintReportState = mintReportStatePending
		nodeRecord.MintReportCompletedAt = 0
	}
	nodeRecord.SwarmID = swarmID
	if strings.TrimSpace(nodeRecord.Name) == "" {
		nodeRecord.Name = strings.TrimSpace(input.Name)
	}
	nodeRecord.Role = bootstrapRoleMaster
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
	groups, currentGroupID, err := s.ListGroupsForSwarm(nodeRecord.SwarmID, 500)
	if err != nil {
		return LocalState{}, err
	}
	return LocalState{
		Node:           toLocalNodeState(nodeRecord),
		CurrentGroupID: currentGroupID,
		Groups:         groups,
	}, nil
}

func (s *Service) PendingMintReport() (string, bool, error) {
	if s == nil || s.store == nil {
		return "", false, errors.New("swarm service is not configured")
	}
	record, ok, err := s.store.GetLocalNode()
	if err != nil {
		return "", false, err
	}
	if !ok || strings.TrimSpace(record.SwarmID) == "" || record.MintReportState != mintReportStatePending {
		return "", false, nil
	}
	return strings.TrimSpace(record.SwarmID), true, nil
}

func (s *Service) CompleteMintReport(swarmID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm service is not configured")
	}
	record, ok, err := s.store.GetLocalNode()
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(record.SwarmID) == "" {
		return errors.New("local swarm is not initialized")
	}
	if strings.TrimSpace(swarmID) == "" || strings.TrimSpace(record.SwarmID) != strings.TrimSpace(swarmID) {
		return errors.New("mint report swarm id does not match local identity")
	}
	if record.MintReportState == mintReportStateCompleted {
		return nil
	}
	if record.MintReportState != mintReportStatePending {
		return errors.New("local swarm has no pending mint report")
	}
	record.MintReportState = mintReportStateCompleted
	record.MintReportCompletedAt = time.Now().UnixMilli()
	_, err = s.store.PutLocalNode(record)
	return err
}

func (s *Service) RenameLocalSwarm(input RenameLocalSwarmInput) (LocalState, error) {
	if s == nil || s.store == nil {
		return LocalState{}, errors.New("swarm service is not configured")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return LocalState{}, errors.New("swarm name is required")
	}
	nodeRecord, ok, err := s.store.GetLocalNode()
	if err != nil {
		return LocalState{}, err
	}
	if !ok || strings.TrimSpace(nodeRecord.SwarmID) == "" {
		return LocalState{}, errors.New("local swarm is not initialized")
	}
	nodeRecord.Name = name
	nodeRecord, err = s.store.PutLocalNode(nodeRecord)
	if err != nil {
		return LocalState{}, err
	}
	groups, currentGroupID, err := s.ListGroupsForSwarm(nodeRecord.SwarmID, 500)
	if err != nil {
		return LocalState{}, err
	}
	if currentGroupID == "" {
		if storedCurrentGroupID, ok, err := s.store.GetCurrentGroupID(); err != nil {
			return LocalState{}, err
		} else if ok {
			currentGroupID = storedCurrentGroupID
		}
	}
	return LocalState{
		Node:           toLocalNodeState(nodeRecord),
		CurrentGroupID: currentGroupID,
		Groups:         groups,
	}, nil
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
