package pebblestore

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type SwarmTransportRecord struct {
	Kind    string   `json:"kind"`
	Primary string   `json:"primary,omitempty"`
	All     []string `json:"all,omitempty"`
}

type SwarmLocalNodeRecord struct {
	SwarmID       string                 `json:"swarm_id"`
	Name          string                 `json:"name"`
	Role          string                 `json:"role"`
	PublicKey     string                 `json:"public_key,omitempty"`
	PrivateKey    string                 `json:"private_key,omitempty"`
	Fingerprint   string                 `json:"fingerprint,omitempty"`
	AdvertiseMode string                 `json:"advertise_mode,omitempty"`
	AdvertiseAddr string                 `json:"advertise_addr,omitempty"`
	Transports    []SwarmTransportRecord `json:"transports,omitempty"`
	CreatedAt     int64                  `json:"created_at"`
	UpdatedAt     int64                  `json:"updated_at"`
}

type SwarmLocalPairingRecord struct {
	PairingState                   string                 `json:"pairing_state"`
	ParentSwarmID                  string                 `json:"parent_swarm_id,omitempty"`
	ActiveInviteID                 string                 `json:"active_invite_id,omitempty"`
	LastEnrollmentID               string                 `json:"last_enrollment_id,omitempty"`
	LastDecision                   string                 `json:"last_decision,omitempty"`
	LastDecisionReason             string                 `json:"last_decision_reason,omitempty"`
	LastUpdatedByRole              string                 `json:"last_updated_by_role,omitempty"`
	RendezvousTransports           []SwarmTransportRecord `json:"rendezvous_transports,omitempty"`
	WorkspaceBootstrapDeploymentID string                 `json:"workspace_bootstrap_deployment_id,omitempty"`
	WorkspaceBootstrapAt           int64                  `json:"workspace_bootstrap_at,omitempty"`
	ManagedAuthOwnerSwarmID        string                 `json:"managed_auth_owner_swarm_id,omitempty"`
	ManagedAuthSnapshotHash        string                 `json:"managed_auth_snapshot_hash,omitempty"`
	ManagedAuthAppliedAt           int64                  `json:"managed_auth_applied_at,omitempty"`
	ManagedAuthLastAttemptAt       int64                  `json:"managed_auth_last_attempt_at,omitempty"`
	ManagedAuthLastError           string                 `json:"managed_auth_last_error,omitempty"`
	CreatedAt                      int64                  `json:"created_at"`
	UpdatedAt                      int64                  `json:"updated_at"`
}

type SwarmInviteRecord struct {
	ID                   string                 `json:"id"`
	Token                string                 `json:"token"`
	PrimarySwarmID       string                 `json:"primary_swarm_id"`
	PrimaryName          string                 `json:"primary_name,omitempty"`
	GroupID              string                 `json:"group_id,omitempty"`
	TransportMode        string                 `json:"transport_mode,omitempty"`
	RendezvousTransports []SwarmTransportRecord `json:"rendezvous_transports,omitempty"`
	ExpiresAt            int64                  `json:"expires_at"`
	ConsumedAt           int64                  `json:"consumed_at,omitempty"`
	CreatedAt            int64                  `json:"created_at"`
	UpdatedAt            int64                  `json:"updated_at"`
}

type SwarmEnrollmentRecord struct {
	ID                   string                 `json:"id"`
	InviteID             string                 `json:"invite_id"`
	InviteToken          string                 `json:"invite_token"`
	PrimarySwarmID       string                 `json:"primary_swarm_id"`
	ParentSwarmID        string                 `json:"parent_swarm_id,omitempty"`
	GroupID              string                 `json:"group_id,omitempty"`
	ChildSwarmID         string                 `json:"child_swarm_id"`
	ChildName            string                 `json:"child_name"`
	ChildRole            string                 `json:"child_role"`
	ChildPublicKey       string                 `json:"child_public_key"`
	ChildFingerprint     string                 `json:"child_fingerprint"`
	TransportMode        string                 `json:"transport_mode,omitempty"`
	ObservedRemoteAddr   string                 `json:"observed_remote_addr,omitempty"`
	RendezvousTransports []SwarmTransportRecord `json:"rendezvous_transports,omitempty"`
	Status               string                 `json:"status"`
	DecisionReason       string                 `json:"decision_reason,omitempty"`
	ReviewedAt           int64                  `json:"reviewed_at,omitempty"`
	CreatedAt            int64                  `json:"created_at"`
	UpdatedAt            int64                  `json:"updated_at"`
}

type SwarmTrustedPeerRecord struct {
	SwarmID               string                 `json:"swarm_id"`
	Name                  string                 `json:"name"`
	Role                  string                 `json:"role"`
	PublicKey             string                 `json:"public_key"`
	Fingerprint           string                 `json:"fingerprint"`
	Relationship          string                 `json:"relationship"`
	ParentSwarmID         string                 `json:"parent_swarm_id,omitempty"`
	TransportMode         string                 `json:"transport_mode,omitempty"`
	RendezvousTransports  []SwarmTransportRecord `json:"rendezvous_transports,omitempty"`
	OutgoingPeerAuthToken string                 `json:"outgoing_peer_auth_token,omitempty"`
	IncomingPeerAuthHash  string                 `json:"incoming_peer_auth_hash,omitempty"`
	ApprovedAt            int64                  `json:"approved_at"`
	CreatedAt             int64                  `json:"created_at"`
	UpdatedAt             int64                  `json:"updated_at"`
}

type SwarmGroupRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	NetworkName string `json:"network_name,omitempty"`
	HostSwarmID string `json:"host_swarm_id"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type SwarmGroupMembershipRecord struct {
	GroupID        string `json:"group_id"`
	SwarmID        string `json:"swarm_id"`
	Name           string `json:"name"`
	SwarmRole      string `json:"swarm_role"`
	MembershipRole string `json:"membership_role"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

type SwarmStore struct {
	store *Store
}

func NewSwarmStore(store *Store) *SwarmStore {
	return &SwarmStore{store: store}
}

func (s *SwarmStore) GetLocalNode() (SwarmLocalNodeRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmLocalNodeRecord{}, false, errors.New("swarm store is not configured")
	}
	var record SwarmLocalNodeRecord
	ok, err := s.store.GetJSON(KeySwarmLocalNodeDefault, &record)
	if err != nil {
		return SwarmLocalNodeRecord{}, false, err
	}
	if !ok {
		return SwarmLocalNodeRecord{}, false, nil
	}
	record = normalizeSwarmLocalNodeRecord(record)
	return record, true, nil
}

func (s *SwarmStore) PutLocalNode(record SwarmLocalNodeRecord) (SwarmLocalNodeRecord, error) {
	if s == nil || s.store == nil {
		return SwarmLocalNodeRecord{}, errors.New("swarm store is not configured")
	}
	now := time.Now().UnixMilli()
	record = normalizeSwarmLocalNodeRecord(record)
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeySwarmLocalNodeDefault, record); err != nil {
		return SwarmLocalNodeRecord{}, err
	}
	return record, nil
}

func (s *SwarmStore) GetLocalPairing() (SwarmLocalPairingRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmLocalPairingRecord{}, false, errors.New("swarm store is not configured")
	}
	var record SwarmLocalPairingRecord
	ok, err := s.store.GetJSON(KeySwarmLocalPairingDefault, &record)
	if err != nil {
		return SwarmLocalPairingRecord{}, false, err
	}
	if !ok {
		return SwarmLocalPairingRecord{}, false, nil
	}
	record = normalizeSwarmLocalPairingRecord(record)
	return record, true, nil
}

func (s *SwarmStore) PutLocalPairing(record SwarmLocalPairingRecord) (SwarmLocalPairingRecord, error) {
	if s == nil || s.store == nil {
		return SwarmLocalPairingRecord{}, errors.New("swarm store is not configured")
	}
	now := time.Now().UnixMilli()
	record = normalizeSwarmLocalPairingRecord(record)
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeySwarmLocalPairingDefault, record); err != nil {
		return SwarmLocalPairingRecord{}, err
	}
	return record, nil
}

func (s *SwarmStore) GetCurrentGroupID() (string, bool, error) {
	if s == nil || s.store == nil {
		return "", false, errors.New("swarm store is not configured")
	}
	value, ok, err := s.store.GetBytes(KeySwarmCurrentGroupDefault)
	if err != nil {
		return "", false, err
	}
	groupID := strings.TrimSpace(string(value))
	if !ok || groupID == "" {
		return "", false, nil
	}
	return groupID, true, nil
}

func (s *SwarmStore) PutCurrentGroupID(groupID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return errors.New("group id is required")
	}
	return s.store.PutBytes(KeySwarmCurrentGroupDefault, []byte(groupID))
}

func (s *SwarmStore) DeleteCurrentGroupID() error {
	if s == nil || s.store == nil {
		return errors.New("swarm store is not configured")
	}
	return s.store.Delete(KeySwarmCurrentGroupDefault)
}

func (s *SwarmStore) GetGroup(groupID string) (SwarmGroupRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmGroupRecord{}, false, errors.New("swarm store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return SwarmGroupRecord{}, false, errors.New("group id is required")
	}
	var record SwarmGroupRecord
	ok, err := s.store.GetJSON(KeySwarmGroup(groupID), &record)
	if err != nil {
		return SwarmGroupRecord{}, false, err
	}
	if !ok {
		return SwarmGroupRecord{}, false, nil
	}
	return normalizeSwarmGroupRecord(record), true, nil
}

func (s *SwarmStore) PutGroup(record SwarmGroupRecord) (SwarmGroupRecord, error) {
	if s == nil || s.store == nil {
		return SwarmGroupRecord{}, errors.New("swarm store is not configured")
	}
	record = normalizeSwarmGroupRecord(record)
	if record.ID == "" {
		return SwarmGroupRecord{}, errors.New("group id is required")
	}
	if record.Name == "" {
		return SwarmGroupRecord{}, errors.New("group name is required")
	}
	if record.HostSwarmID == "" {
		return SwarmGroupRecord{}, errors.New("host swarm id is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeySwarmGroup(record.ID), record); err != nil {
		return SwarmGroupRecord{}, err
	}
	return record, nil
}

func (s *SwarmStore) DeleteGroup(groupID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return errors.New("group id is required")
	}
	if err := s.DeleteGroupMembershipsByGroup(groupID); err != nil {
		return err
	}
	return s.store.Delete(KeySwarmGroup(groupID))
}

func (s *SwarmStore) ListGroups(limit int) ([]SwarmGroupRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm store is not configured")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]SwarmGroupRecord, 0, limit)
	err := s.store.IteratePrefix(SwarmGroupPrefix(), 100000, func(_ string, value []byte) error {
		var record SwarmGroupRecord
		if err := jsonUnmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, normalizeSwarmGroupRecord(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SwarmStore) GetGroupMembership(groupID, swarmID string) (SwarmGroupMembershipRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmGroupMembershipRecord{}, false, errors.New("swarm store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	swarmID = strings.TrimSpace(swarmID)
	if groupID == "" {
		return SwarmGroupMembershipRecord{}, false, errors.New("group id is required")
	}
	if swarmID == "" {
		return SwarmGroupMembershipRecord{}, false, errors.New("swarm id is required")
	}
	var record SwarmGroupMembershipRecord
	ok, err := s.store.GetJSON(KeySwarmGroupMembership(groupID, swarmID), &record)
	if err != nil {
		return SwarmGroupMembershipRecord{}, false, err
	}
	if !ok {
		return SwarmGroupMembershipRecord{}, false, nil
	}
	return normalizeSwarmGroupMembershipRecord(record), true, nil
}

func (s *SwarmStore) PutGroupMembership(record SwarmGroupMembershipRecord) (SwarmGroupMembershipRecord, error) {
	if s == nil || s.store == nil {
		return SwarmGroupMembershipRecord{}, errors.New("swarm store is not configured")
	}
	record = normalizeSwarmGroupMembershipRecord(record)
	if record.GroupID == "" {
		return SwarmGroupMembershipRecord{}, errors.New("group id is required")
	}
	if record.SwarmID == "" {
		return SwarmGroupMembershipRecord{}, errors.New("swarm id is required")
	}
	if record.Name == "" {
		return SwarmGroupMembershipRecord{}, errors.New("member name is required")
	}
	if record.SwarmRole == "" {
		return SwarmGroupMembershipRecord{}, errors.New("member swarm role is required")
	}
	if record.MembershipRole == "" {
		return SwarmGroupMembershipRecord{}, errors.New("membership role is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	payload, err := jsonMarshal(record)
	if err != nil {
		return SwarmGroupMembershipRecord{}, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySwarmGroupMembership(record.GroupID, record.SwarmID)), payload, nil); err != nil {
		return SwarmGroupMembershipRecord{}, err
	}
	if err := batch.Set([]byte(KeySwarmGroupMembershipBySwarm(record.SwarmID, record.GroupID)), payload, nil); err != nil {
		return SwarmGroupMembershipRecord{}, err
	}
	if err := batch.Commit(nil); err != nil {
		return SwarmGroupMembershipRecord{}, err
	}
	return record, nil
}

func (s *SwarmStore) DeleteGroupMembership(groupID, swarmID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	swarmID = strings.TrimSpace(swarmID)
	if groupID == "" {
		return errors.New("group id is required")
	}
	if swarmID == "" {
		return errors.New("swarm id is required")
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeySwarmGroupMembership(groupID, swarmID)), nil); err != nil {
		return err
	}
	if err := batch.Delete([]byte(KeySwarmGroupMembershipBySwarm(swarmID, groupID)), nil); err != nil {
		return err
	}
	return batch.Commit(nil)
}

func (s *SwarmStore) ListGroupMemberships(groupID string, limit int) ([]SwarmGroupMembershipRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, errors.New("group id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]SwarmGroupMembershipRecord, 0, limit)
	err := s.store.IteratePrefix(SwarmGroupMembershipPrefix(groupID), 100000, func(_ string, value []byte) error {
		var record SwarmGroupMembershipRecord
		if err := jsonUnmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, normalizeSwarmGroupMembershipRecord(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		leftHost := out[i].MembershipRole == "host"
		rightHost := out[j].MembershipRole == "host"
		if leftHost != rightHost {
			return leftHost
		}
		return strings.ToLower(out[i].SwarmID) < strings.ToLower(out[j].SwarmID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SwarmStore) ListGroupMembershipsBySwarm(swarmID string, limit int) ([]SwarmGroupMembershipRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm store is not configured")
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return nil, errors.New("swarm id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]SwarmGroupMembershipRecord, 0, limit)
	err := s.store.IteratePrefix(SwarmGroupMembershipBySwarmPrefix(swarmID), 100000, func(_ string, value []byte) error {
		var record SwarmGroupMembershipRecord
		if err := jsonUnmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, normalizeSwarmGroupMembershipRecord(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].GroupID) < strings.ToLower(out[j].GroupID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SwarmStore) DeleteGroupMembershipsByGroup(groupID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm store is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return errors.New("group id is required")
	}
	records, err := s.ListGroupMemberships(groupID, 100000)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, record := range records {
		if err := batch.Delete([]byte(KeySwarmGroupMembership(record.GroupID, record.SwarmID)), nil); err != nil {
			return err
		}
		if err := batch.Delete([]byte(KeySwarmGroupMembershipBySwarm(record.SwarmID, record.GroupID)), nil); err != nil {
			return err
		}
	}
	return batch.Commit(nil)
}

func (s *SwarmStore) GetInvite(inviteID string) (SwarmInviteRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmInviteRecord{}, false, errors.New("swarm store is not configured")
	}
	inviteID = strings.TrimSpace(inviteID)
	if inviteID == "" {
		return SwarmInviteRecord{}, false, errors.New("invite id is required")
	}
	var record SwarmInviteRecord
	ok, err := s.store.GetJSON(KeySwarmInvite(inviteID), &record)
	if err != nil {
		return SwarmInviteRecord{}, false, err
	}
	if !ok {
		return SwarmInviteRecord{}, false, nil
	}
	record = normalizeSwarmInviteRecord(record)
	return record, true, nil
}

func (s *SwarmStore) PutInvite(record SwarmInviteRecord) (SwarmInviteRecord, error) {
	if s == nil || s.store == nil {
		return SwarmInviteRecord{}, errors.New("swarm store is not configured")
	}
	record = normalizeSwarmInviteRecord(record)
	if strings.TrimSpace(record.ID) == "" {
		return SwarmInviteRecord{}, errors.New("invite id is required")
	}
	if strings.TrimSpace(record.Token) == "" {
		return SwarmInviteRecord{}, errors.New("invite token is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	batch := s.store.NewBatch()
	defer batch.Close()
	payload, err := jsonMarshal(record)
	if err != nil {
		return SwarmInviteRecord{}, err
	}
	if err := batch.Set([]byte(KeySwarmInvite(record.ID)), payload, nil); err != nil {
		return SwarmInviteRecord{}, err
	}
	if err := batch.Set([]byte(KeySwarmInviteToken(record.Token)), []byte(record.ID), nil); err != nil {
		return SwarmInviteRecord{}, err
	}
	if err := batch.Commit(nil); err != nil {
		return SwarmInviteRecord{}, err
	}
	return record, nil
}

func (s *SwarmStore) FindInviteByToken(token string) (SwarmInviteRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmInviteRecord{}, false, errors.New("swarm store is not configured")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return SwarmInviteRecord{}, false, nil
	}
	raw, ok, err := s.store.GetBytes(KeySwarmInviteToken(token))
	if err != nil {
		return SwarmInviteRecord{}, false, err
	}
	if !ok {
		return SwarmInviteRecord{}, false, nil
	}
	return s.GetInvite(string(raw))
}

func (s *SwarmStore) GetEnrollment(enrollmentID string) (SwarmEnrollmentRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmEnrollmentRecord{}, false, errors.New("swarm store is not configured")
	}
	enrollmentID = strings.TrimSpace(enrollmentID)
	if enrollmentID == "" {
		return SwarmEnrollmentRecord{}, false, errors.New("enrollment id is required")
	}
	var record SwarmEnrollmentRecord
	ok, err := s.store.GetJSON(KeySwarmEnrollment(enrollmentID), &record)
	if err != nil {
		return SwarmEnrollmentRecord{}, false, err
	}
	if !ok {
		return SwarmEnrollmentRecord{}, false, nil
	}
	record = normalizeSwarmEnrollmentRecord(record)
	return record, true, nil
}

func (s *SwarmStore) PutEnrollment(record SwarmEnrollmentRecord) (SwarmEnrollmentRecord, error) {
	if s == nil || s.store == nil {
		return SwarmEnrollmentRecord{}, errors.New("swarm store is not configured")
	}
	record = normalizeSwarmEnrollmentRecord(record)
	if strings.TrimSpace(record.ID) == "" {
		return SwarmEnrollmentRecord{}, errors.New("enrollment id is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeySwarmEnrollment(record.ID), record); err != nil {
		return SwarmEnrollmentRecord{}, err
	}
	return record, nil
}

func (s *SwarmStore) ListEnrollments(limit int) ([]SwarmEnrollmentRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm store is not configured")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]SwarmEnrollmentRecord, 0, limit)
	err := s.store.IteratePrefix(SwarmEnrollmentPrefix(), 100000, func(_ string, value []byte) error {
		var record SwarmEnrollmentRecord
		if err := jsonUnmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, normalizeSwarmEnrollmentRecord(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SwarmStore) PutTrustedPeer(record SwarmTrustedPeerRecord) (SwarmTrustedPeerRecord, error) {
	if s == nil || s.store == nil {
		return SwarmTrustedPeerRecord{}, errors.New("swarm store is not configured")
	}
	record = normalizeSwarmTrustedPeerRecord(record)
	if strings.TrimSpace(record.SwarmID) == "" {
		return SwarmTrustedPeerRecord{}, errors.New("trusted peer swarm id is required")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	if record.ApprovedAt <= 0 {
		record.ApprovedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeySwarmTrustedPeer(record.SwarmID), record); err != nil {
		return SwarmTrustedPeerRecord{}, err
	}
	return record, nil
}

func (s *SwarmStore) GetTrustedPeer(swarmID string) (SwarmTrustedPeerRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmTrustedPeerRecord{}, false, errors.New("swarm store is not configured")
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return SwarmTrustedPeerRecord{}, false, errors.New("trusted peer swarm id is required")
	}
	var record SwarmTrustedPeerRecord
	ok, err := s.store.GetJSON(KeySwarmTrustedPeer(swarmID), &record)
	if err != nil {
		return SwarmTrustedPeerRecord{}, false, err
	}
	if !ok {
		return SwarmTrustedPeerRecord{}, false, nil
	}
	return normalizeSwarmTrustedPeerRecord(record), true, nil
}

func (s *SwarmStore) DeleteTrustedPeer(swarmID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm store is not configured")
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return errors.New("trusted peer swarm id is required")
	}
	return s.store.Delete(KeySwarmTrustedPeer(swarmID))
}

func (s *SwarmStore) ListTrustedPeers(limit int) ([]SwarmTrustedPeerRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm store is not configured")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]SwarmTrustedPeerRecord, 0, limit)
	err := s.store.IteratePrefix(SwarmTrustedPeerPrefix(), 100000, func(_ string, value []byte) error {
		var record SwarmTrustedPeerRecord
		if err := jsonUnmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, normalizeSwarmTrustedPeerRecord(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(out[i].SwarmID))
		right := strings.ToLower(strings.TrimSpace(out[j].SwarmID))
		return left < right
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func normalizeSwarmLocalNodeRecord(record SwarmLocalNodeRecord) SwarmLocalNodeRecord {
	record.SwarmID = strings.TrimSpace(record.SwarmID)
	record.Name = strings.TrimSpace(record.Name)
	record.Role = strings.ToLower(strings.TrimSpace(record.Role))
	record.PublicKey = strings.TrimSpace(record.PublicKey)
	record.PrivateKey = strings.TrimSpace(record.PrivateKey)
	record.Fingerprint = strings.TrimSpace(record.Fingerprint)
	record.AdvertiseMode = strings.ToLower(strings.TrimSpace(record.AdvertiseMode))
	record.AdvertiseAddr = strings.TrimSpace(record.AdvertiseAddr)
	record.Transports = normalizeSwarmTransports(record.Transports)
	return record
}

func normalizeSwarmLocalPairingRecord(record SwarmLocalPairingRecord) SwarmLocalPairingRecord {
	record.PairingState = strings.ToLower(strings.TrimSpace(record.PairingState))
	record.ParentSwarmID = strings.TrimSpace(record.ParentSwarmID)
	record.ActiveInviteID = strings.TrimSpace(record.ActiveInviteID)
	record.LastEnrollmentID = strings.TrimSpace(record.LastEnrollmentID)
	record.LastDecision = strings.ToLower(strings.TrimSpace(record.LastDecision))
	record.LastDecisionReason = strings.TrimSpace(record.LastDecisionReason)
	record.LastUpdatedByRole = strings.ToLower(strings.TrimSpace(record.LastUpdatedByRole))
	record.RendezvousTransports = normalizeSwarmTransports(record.RendezvousTransports)
	record.WorkspaceBootstrapDeploymentID = strings.TrimSpace(record.WorkspaceBootstrapDeploymentID)
	record.ManagedAuthOwnerSwarmID = strings.TrimSpace(record.ManagedAuthOwnerSwarmID)
	record.ManagedAuthSnapshotHash = strings.TrimSpace(record.ManagedAuthSnapshotHash)
	record.ManagedAuthLastError = strings.TrimSpace(record.ManagedAuthLastError)
	if record.WorkspaceBootstrapAt < 0 {
		record.WorkspaceBootstrapAt = 0
	}
	if record.ManagedAuthAppliedAt < 0 {
		record.ManagedAuthAppliedAt = 0
	}
	if record.ManagedAuthLastAttemptAt < 0 {
		record.ManagedAuthLastAttemptAt = 0
	}
	return record
}

func normalizeSwarmInviteRecord(record SwarmInviteRecord) SwarmInviteRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.Token = strings.TrimSpace(record.Token)
	record.PrimarySwarmID = strings.TrimSpace(record.PrimarySwarmID)
	record.PrimaryName = strings.TrimSpace(record.PrimaryName)
	record.GroupID = strings.TrimSpace(record.GroupID)
	record.TransportMode = strings.ToLower(strings.TrimSpace(record.TransportMode))
	record.RendezvousTransports = normalizeSwarmTransports(record.RendezvousTransports)
	return record
}

func normalizeSwarmEnrollmentRecord(record SwarmEnrollmentRecord) SwarmEnrollmentRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.InviteID = strings.TrimSpace(record.InviteID)
	record.InviteToken = strings.TrimSpace(record.InviteToken)
	record.PrimarySwarmID = strings.TrimSpace(record.PrimarySwarmID)
	record.ParentSwarmID = strings.TrimSpace(record.ParentSwarmID)
	record.GroupID = strings.TrimSpace(record.GroupID)
	record.ChildSwarmID = strings.TrimSpace(record.ChildSwarmID)
	record.ChildName = strings.TrimSpace(record.ChildName)
	record.ChildRole = strings.ToLower(strings.TrimSpace(record.ChildRole))
	record.ChildPublicKey = strings.TrimSpace(record.ChildPublicKey)
	record.ChildFingerprint = strings.TrimSpace(record.ChildFingerprint)
	record.TransportMode = strings.ToLower(strings.TrimSpace(record.TransportMode))
	record.ObservedRemoteAddr = strings.TrimSpace(record.ObservedRemoteAddr)
	record.RendezvousTransports = normalizeSwarmTransports(record.RendezvousTransports)
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	record.DecisionReason = strings.TrimSpace(record.DecisionReason)
	return record
}

func normalizeSwarmTrustedPeerRecord(record SwarmTrustedPeerRecord) SwarmTrustedPeerRecord {
	record.SwarmID = strings.TrimSpace(record.SwarmID)
	record.Name = strings.TrimSpace(record.Name)
	record.Role = strings.ToLower(strings.TrimSpace(record.Role))
	record.PublicKey = strings.TrimSpace(record.PublicKey)
	record.Fingerprint = strings.TrimSpace(record.Fingerprint)
	record.Relationship = strings.ToLower(strings.TrimSpace(record.Relationship))
	record.ParentSwarmID = strings.TrimSpace(record.ParentSwarmID)
	record.TransportMode = strings.ToLower(strings.TrimSpace(record.TransportMode))
	record.RendezvousTransports = normalizeSwarmTransports(record.RendezvousTransports)
	record.OutgoingPeerAuthToken = strings.TrimSpace(record.OutgoingPeerAuthToken)
	record.IncomingPeerAuthHash = strings.TrimSpace(record.IncomingPeerAuthHash)
	return record
}

func normalizeSwarmGroupRecord(record SwarmGroupRecord) SwarmGroupRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.Name = strings.TrimSpace(record.Name)
	record.NetworkName = normalizeContainerSlug(record.NetworkName)
	record.HostSwarmID = strings.TrimSpace(record.HostSwarmID)
	return record
}

func normalizeSwarmGroupMembershipRecord(record SwarmGroupMembershipRecord) SwarmGroupMembershipRecord {
	record.GroupID = strings.TrimSpace(record.GroupID)
	record.SwarmID = strings.TrimSpace(record.SwarmID)
	record.Name = strings.TrimSpace(record.Name)
	record.SwarmRole = strings.ToLower(strings.TrimSpace(record.SwarmRole))
	record.MembershipRole = strings.ToLower(strings.TrimSpace(record.MembershipRole))
	return record
}

func normalizeSwarmTransports(records []SwarmTransportRecord) []SwarmTransportRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]SwarmTransportRecord, 0, len(records))
	for _, record := range records {
		kind := strings.ToLower(strings.TrimSpace(record.Kind))
		if kind == "" {
			continue
		}
		all := make([]string, 0, len(record.All))
		seen := map[string]struct{}{}
		for _, value := range record.All {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			all = append(all, value)
		}
		sort.Strings(all)
		primary := strings.TrimSpace(record.Primary)
		if primary == "" && len(all) > 0 {
			primary = all[0]
		}
		out = append(out, SwarmTransportRecord{Kind: kind, Primary: primary, All: all})
	}
	return out
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func jsonUnmarshal(data []byte, out any) error {
	return json.Unmarshal(data, out)
}
