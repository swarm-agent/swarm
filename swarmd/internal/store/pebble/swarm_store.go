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

// Retired pairing record types carry no persistence behavior; they remain
// source-compatible until the API cleanup removes stale interface references.
type SwarmLocalPairingRecord struct {
	PairingState         string
	ParentSwarmID        string
	UserID               string
	AccountScopeID       string
	ActiveInviteID       string
	LastEnrollmentID     string
	LastDecision         string
	LastDecisionReason   string
	LastUpdatedByRole    string
	RendezvousTransports []SwarmTransportRecord
	WorkspaceBootstrapAt int64
	CreatedAt            int64
	UpdatedAt            int64
}
type SwarmInviteRecord struct {
	ID                   string
	Token                string
	PrimarySwarmID       string
	PrimaryName          string
	GroupID              string
	TransportMode        string
	RendezvousTransports []SwarmTransportRecord
	ExpiresAt            int64
	ConsumedAt           int64
	CreatedAt            int64
	UpdatedAt            int64
}
type SwarmEnrollmentRecord struct {
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
	RendezvousTransports []SwarmTransportRecord
	Status               string
	DecisionReason       string
	ReviewedAt           int64
	CreatedAt            int64
	UpdatedAt            int64
}
type SwarmTrustedPeerRecord struct {
	SwarmID               string
	Name                  string
	Role                  string
	PublicKey             string
	Fingerprint           string
	Relationship          string
	ParentSwarmID         string
	TransportMode         string
	RendezvousTransports  []SwarmTransportRecord
	OutgoingPeerAuthToken string
	IncomingPeerAuthHash  string
	ApprovedAt            int64
	CreatedAt             int64
	UpdatedAt             int64
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
	store    *Store
	topology *TopologyStore
}

func NewSwarmStore(store *Store, extras ...any) *SwarmStore {
	swarmStore := &SwarmStore{store: store}
	for _, extra := range extras {
		switch value := extra.(type) {
		case *TopologyStore:
			swarmStore.topology = value
		}
	}
	return swarmStore
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
	if err := syncTopologyRuntimeFromLocalNode(s.topology, record); err != nil {
		return SwarmLocalNodeRecord{}, err
	}
	return record, nil
}

func (s *SwarmStore) GetLocalPairing() (SwarmLocalPairingRecord, bool, error) {
	return SwarmLocalPairingRecord{}, false, nil
}

func (s *SwarmStore) PutLocalPairing(SwarmLocalPairingRecord) (SwarmLocalPairingRecord, error) {
	return SwarmLocalPairingRecord{}, errors.New("swarm pairing has been removed")
}

func (s *SwarmStore) PutTrustedPeer(SwarmTrustedPeerRecord) (SwarmTrustedPeerRecord, error) {
	return SwarmTrustedPeerRecord{}, errors.New("swarm trusted-peer pairing has been removed")
}

func (s *SwarmStore) GetTrustedPeer(string) (SwarmTrustedPeerRecord, bool, error) {
	return SwarmTrustedPeerRecord{}, false, nil
}

func (s *SwarmStore) DeleteTrustedPeer(string) error {
	return errors.New("swarm trusted-peer pairing has been removed")
}

func (s *SwarmStore) ListTrustedPeers(int) ([]SwarmTrustedPeerRecord, error) {
	return nil, nil
}

func (s *SwarmStore) GetInvite(string) (SwarmInviteRecord, bool, error) {
	return SwarmInviteRecord{}, false, nil
}

func (s *SwarmStore) PutInvite(SwarmInviteRecord) (SwarmInviteRecord, error) {
	return SwarmInviteRecord{}, errors.New("swarm pairing has been removed")
}

func (s *SwarmStore) FindInviteByToken(string) (SwarmInviteRecord, bool, error) {
	return SwarmInviteRecord{}, false, nil
}

func (s *SwarmStore) GetEnrollment(string) (SwarmEnrollmentRecord, bool, error) {
	return SwarmEnrollmentRecord{}, false, nil
}

func (s *SwarmStore) PutEnrollment(SwarmEnrollmentRecord) (SwarmEnrollmentRecord, error) {
	return SwarmEnrollmentRecord{}, errors.New("swarm enrollment has been removed")
}

func (s *SwarmStore) ListEnrollments(int) ([]SwarmEnrollmentRecord, error) {
	return nil, nil
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
