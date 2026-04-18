package swarm

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	GroupMembershipRoleHost   = "host"
	GroupMembershipRoleMember = "member"
)

type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	NetworkName string `json:"network_name,omitempty"`
	HostSwarmID string `json:"host_swarm_id"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type GroupMember struct {
	GroupID        string `json:"group_id"`
	SwarmID        string `json:"swarm_id"`
	Name           string `json:"name"`
	SwarmRole      string `json:"swarm_role"`
	MembershipRole string `json:"membership_role"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

type GroupState struct {
	Group   Group         `json:"group"`
	Members []GroupMember `json:"members"`
}

type UpsertGroupInput struct {
	GroupID     string
	Name        string
	NetworkName string
	HostSwarmID string
	SetCurrent  bool
}

type UpsertGroupMemberInput struct {
	GroupID        string
	SwarmID        string
	Name           string
	SwarmRole      string
	MembershipRole string
}

type RemoveGroupMemberInput struct {
	GroupID string
	SwarmID string
}

func (s *Service) ListGroupsForSwarm(swarmID string, limit int) ([]GroupState, string, error) {
	if s == nil || s.store == nil {
		return nil, "", errors.New("swarm service is not configured")
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return nil, "", errors.New("swarm id is required")
	}
	memberships, err := s.store.ListGroupMembershipsBySwarm(swarmID, limit)
	if err != nil {
		return nil, "", err
	}
	currentGroupID, _, err := s.store.GetCurrentGroupID()
	if err != nil {
		return nil, "", err
	}
	groupStates := make([]GroupState, 0, len(memberships))
	for _, membership := range memberships {
		group, ok, err := s.store.GetGroup(membership.GroupID)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		group, err = s.ensureGroupNetworkName(group)
		if err != nil {
			return nil, "", err
		}
		memberRecords, err := s.store.ListGroupMemberships(group.ID, 500)
		if err != nil {
			return nil, "", err
		}
		groupStates = append(groupStates, GroupState{
			Group:   toGroup(group),
			Members: toGroupMembers(memberRecords),
		})
	}
	sort.Slice(groupStates, func(i, j int) bool {
		if strings.EqualFold(groupStates[i].Group.ID, currentGroupID) {
			return true
		}
		if strings.EqualFold(groupStates[j].Group.ID, currentGroupID) {
			return false
		}
		if groupStates[i].Group.CreatedAt == groupStates[j].Group.CreatedAt {
			return strings.ToLower(groupStates[i].Group.ID) < strings.ToLower(groupStates[j].Group.ID)
		}
		return groupStates[i].Group.CreatedAt < groupStates[j].Group.CreatedAt
	})
	return groupStates, strings.TrimSpace(currentGroupID), nil
}

func (s *Service) UpsertGroup(input UpsertGroupInput) (Group, error) {
	if s == nil || s.store == nil {
		return Group{}, errors.New("swarm service is not configured")
	}
	hostSwarmID := strings.TrimSpace(input.HostSwarmID)
	if hostSwarmID == "" {
		return Group{}, errors.New("host swarm id is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Group{}, errors.New("group name is required")
	}
	groupID := strings.TrimSpace(input.GroupID)
	existingCreatedAt := int64(0)
	existingNetworkName := ""
	if groupID != "" {
		existing, ok, err := s.store.GetGroup(groupID)
		if err != nil {
			return Group{}, err
		}
		if ok {
			if !strings.EqualFold(existing.HostSwarmID, hostSwarmID) {
				return Group{}, errors.New("group host swarm id cannot change")
			}
			existingCreatedAt = existing.CreatedAt
			existingNetworkName = existing.NetworkName
		}
	} else {
		generated, err := generateGroupID()
		if err != nil {
			return Group{}, err
		}
		groupID = generated
	}
	networkName := firstNonEmpty(input.NetworkName, existingNetworkName)
	if networkName == "" {
		networkName = SuggestedGroupNetworkName(name, groupID)
	}
	record, err := s.store.PutGroup(pebblestore.SwarmGroupRecord{
		ID:          groupID,
		Name:        name,
		NetworkName: networkName,
		HostSwarmID: hostSwarmID,
		CreatedAt:   existingCreatedAt,
	})
	if err != nil {
		return Group{}, err
	}
	if _, err := s.ensureGroupHostMembership(record, name); err != nil {
		return Group{}, err
	}
	if input.SetCurrent {
		if err := s.store.PutCurrentGroupID(record.ID); err != nil {
			return Group{}, err
		}
	}
	_, _ = s.appendEvent("swarm:group", "swarm.group.upserted", record.ID, record)
	return toGroup(record), nil
}

func (s *Service) DeleteGroup(groupID string) error {
	if s == nil || s.store == nil {
		return errors.New("swarm service is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return errors.New("group id is required")
	}
	group, ok, err := s.store.GetGroup(groupID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("group not found")
	}
	currentGroupID, currentSet, err := s.store.GetCurrentGroupID()
	if err != nil {
		return err
	}
	if err := s.store.DeleteGroup(groupID); err != nil {
		return err
	}
	if currentSet && strings.EqualFold(currentGroupID, groupID) {
		remaining, _, err := s.ListGroupsForSwarm(group.HostSwarmID, 500)
		if err != nil {
			return err
		}
		if len(remaining) > 0 {
			if err := s.store.PutCurrentGroupID(remaining[0].Group.ID); err != nil {
				return err
			}
		} else if err := s.store.DeleteCurrentGroupID(); err != nil {
			return err
		}
	}
	_, _ = s.appendEvent("swarm:group", "swarm.group.deleted", groupID, map[string]any{"group_id": groupID})
	return nil
}

func (s *Service) SetCurrentGroup(groupID string, localSwarmID string) (GroupState, error) {
	if s == nil || s.store == nil {
		return GroupState{}, errors.New("swarm service is not configured")
	}
	groupID = strings.TrimSpace(groupID)
	localSwarmID = strings.TrimSpace(localSwarmID)
	if groupID == "" {
		return GroupState{}, errors.New("group id is required")
	}
	if localSwarmID == "" {
		return GroupState{}, errors.New("local swarm id is required")
	}
	membership, ok, err := s.store.GetGroupMembership(groupID, localSwarmID)
	if err != nil {
		return GroupState{}, err
	}
	if !ok {
		return GroupState{}, errors.New("local swarm is not a member of this group")
	}
	if err := s.store.PutCurrentGroupID(groupID); err != nil {
		return GroupState{}, err
	}
	group, ok, err := s.store.GetGroup(membership.GroupID)
	if err != nil {
		return GroupState{}, err
	}
	if !ok {
		return GroupState{}, errors.New("group not found")
	}
	group, err = s.ensureGroupNetworkName(group)
	if err != nil {
		return GroupState{}, err
	}
	memberRecords, err := s.store.ListGroupMemberships(group.ID, 500)
	if err != nil {
		return GroupState{}, err
	}
	state := GroupState{Group: toGroup(group), Members: toGroupMembers(memberRecords)}
	_, _ = s.appendEvent("swarm:group", "swarm.group.current_set", groupID, map[string]any{"group_id": groupID})
	return state, nil
}

func (s *Service) UpsertGroupMember(input UpsertGroupMemberInput) (GroupMember, error) {
	if s == nil || s.store == nil {
		return GroupMember{}, errors.New("swarm service is not configured")
	}
	record, err := s.resolveGroupMembershipRecord(input)
	if err != nil {
		return GroupMember{}, err
	}
	record, err = s.store.PutGroupMembership(record)
	if err != nil {
		return GroupMember{}, err
	}
	_, _ = s.appendEvent("swarm:group", "swarm.group.member.upserted", record.GroupID, record)
	return toGroupMember(record), nil
}

func (s *Service) RemoveGroupMember(input RemoveGroupMemberInput) error {
	if s == nil || s.store == nil {
		return errors.New("swarm service is not configured")
	}
	groupID := strings.TrimSpace(input.GroupID)
	swarmID := strings.TrimSpace(input.SwarmID)
	if groupID == "" {
		return errors.New("group id is required")
	}
	if swarmID == "" {
		return errors.New("swarm id is required")
	}
	group, ok, err := s.store.GetGroup(groupID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("group not found")
	}
	if strings.EqualFold(group.HostSwarmID, swarmID) {
		return errors.New("cannot remove the host swarm from its own group")
	}
	if err := s.store.DeleteGroupMembership(groupID, swarmID); err != nil {
		return err
	}
	_, _ = s.appendEvent("swarm:group", "swarm.group.member.removed", groupID, map[string]any{"group_id": groupID, "swarm_id": swarmID})
	return nil
}

func (s *Service) EnsureGroupForLocalState(localNode pebblestore.SwarmLocalNodeRecord, swarmMode bool) (string, error) {
	if s == nil || s.store == nil {
		return "", errors.New("swarm service is not configured")
	}
	if !swarmMode {
		return "", nil
	}
	switch strings.ToLower(strings.TrimSpace(localNode.Role)) {
	case bootstrapRoleMaster:
		group, err := s.ensureLocalHostGroup(localNode, localNode.Name)
		if err != nil {
			return "", err
		}
		return group.ID, nil
	case bootstrapRoleChild:
		currentGroupID, ok, err := s.store.GetCurrentGroupID()
		if err != nil {
			return "", err
		}
		if !ok {
			return "", nil
		}
		membership, membershipOK, err := s.store.GetGroupMembership(currentGroupID, localNode.SwarmID)
		if err != nil {
			return "", err
		}
		if !membershipOK {
			return "", nil
		}
		return membership.GroupID, nil
	default:
		return "", nil
	}
}

func (s *Service) ensureLocalHostGroup(localNode pebblestore.SwarmLocalNodeRecord, fallbackName string) (pebblestore.SwarmGroupRecord, error) {
	localNode = pebblestore.SwarmLocalNodeRecord{
		SwarmID:       strings.TrimSpace(localNode.SwarmID),
		Name:          strings.TrimSpace(localNode.Name),
		Role:          strings.ToLower(strings.TrimSpace(localNode.Role)),
		PublicKey:     localNode.PublicKey,
		PrivateKey:    localNode.PrivateKey,
		Fingerprint:   localNode.Fingerprint,
		AdvertiseMode: localNode.AdvertiseMode,
		AdvertiseAddr: localNode.AdvertiseAddr,
		Transports:    localNode.Transports,
		CreatedAt:     localNode.CreatedAt,
		UpdatedAt:     localNode.UpdatedAt,
	}
	if localNode.SwarmID == "" {
		return pebblestore.SwarmGroupRecord{}, errors.New("local swarm id is required")
	}
	name := firstNonEmpty(localNode.Name, fallbackName, "Swarm Group")
	currentGroupID, ok, err := s.store.GetCurrentGroupID()
	if err != nil {
		return pebblestore.SwarmGroupRecord{}, err
	}
	if ok {
		group, found, err := s.store.GetGroup(currentGroupID)
		if err != nil {
			return pebblestore.SwarmGroupRecord{}, err
		}
		if found && strings.EqualFold(group.HostSwarmID, localNode.SwarmID) {
			group, err = s.ensureGroupNetworkName(group)
			if err != nil {
				return pebblestore.SwarmGroupRecord{}, err
			}
			if _, err := s.ensureGroupHostMembership(group, name); err != nil {
				return pebblestore.SwarmGroupRecord{}, err
			}
			return group, nil
		}
	}
	memberships, err := s.store.ListGroupMembershipsBySwarm(localNode.SwarmID, 500)
	if err != nil {
		return pebblestore.SwarmGroupRecord{}, err
	}
	for _, membership := range memberships {
		if membership.MembershipRole != GroupMembershipRoleHost {
			continue
		}
		group, found, err := s.store.GetGroup(membership.GroupID)
		if err != nil {
			return pebblestore.SwarmGroupRecord{}, err
		}
		if !found || !strings.EqualFold(group.HostSwarmID, localNode.SwarmID) {
			continue
		}
		group, err = s.ensureGroupNetworkName(group)
		if err != nil {
			return pebblestore.SwarmGroupRecord{}, err
		}
		if err := s.store.PutCurrentGroupID(group.ID); err != nil {
			return pebblestore.SwarmGroupRecord{}, err
		}
		if _, err := s.ensureGroupHostMembership(group, name); err != nil {
			return pebblestore.SwarmGroupRecord{}, err
		}
		return group, nil
	}
	groupID, err := generateGroupID()
	if err != nil {
		return pebblestore.SwarmGroupRecord{}, err
	}
	group, err := s.store.PutGroup(pebblestore.SwarmGroupRecord{
		ID:          groupID,
		Name:        name,
		NetworkName: SuggestedGroupNetworkName(name, groupID),
		HostSwarmID: localNode.SwarmID,
	})
	if err != nil {
		return pebblestore.SwarmGroupRecord{}, err
	}
	if _, err := s.ensureGroupHostMembership(group, name); err != nil {
		return pebblestore.SwarmGroupRecord{}, err
	}
	if err := s.store.PutCurrentGroupID(group.ID); err != nil {
		return pebblestore.SwarmGroupRecord{}, err
	}
	_, _ = s.appendEvent("swarm:group", "swarm.group.created", group.ID, group)
	return group, nil
}

func (s *Service) ensureGroupHostMembership(group pebblestore.SwarmGroupRecord, fallbackName string) (pebblestore.SwarmGroupMembershipRecord, error) {
	localNode, ok, err := s.store.GetLocalNode()
	if err != nil {
		return pebblestore.SwarmGroupMembershipRecord{}, err
	}
	if !ok {
		return pebblestore.SwarmGroupMembershipRecord{}, errors.New("local swarm node is not configured")
	}
	return s.store.PutGroupMembership(pebblestore.SwarmGroupMembershipRecord{
		GroupID:        group.ID,
		SwarmID:        group.HostSwarmID,
		Name:           firstNonEmpty(localNode.Name, fallbackName, group.Name, "Host swarm"),
		SwarmRole:      bootstrapRoleMaster,
		MembershipRole: GroupMembershipRoleHost,
	})
}

func (s *Service) resolveGroupMembershipRecord(input UpsertGroupMemberInput) (pebblestore.SwarmGroupMembershipRecord, error) {
	groupID := strings.TrimSpace(input.GroupID)
	swarmID := strings.TrimSpace(input.SwarmID)
	if groupID == "" {
		return pebblestore.SwarmGroupMembershipRecord{}, errors.New("group id is required")
	}
	if swarmID == "" {
		return pebblestore.SwarmGroupMembershipRecord{}, errors.New("swarm id is required")
	}
	group, ok, err := s.store.GetGroup(groupID)
	if err != nil {
		return pebblestore.SwarmGroupMembershipRecord{}, err
	}
	if !ok {
		return pebblestore.SwarmGroupMembershipRecord{}, errors.New("group not found")
	}
	existing, existingOK, err := s.store.GetGroupMembership(groupID, swarmID)
	if err != nil {
		return pebblestore.SwarmGroupMembershipRecord{}, err
	}
	name := strings.TrimSpace(input.Name)
	swarmRole := strings.ToLower(strings.TrimSpace(input.SwarmRole))
	membershipRole := strings.ToLower(strings.TrimSpace(input.MembershipRole))
	if strings.EqualFold(group.HostSwarmID, swarmID) {
		membershipRole = GroupMembershipRoleHost
		if swarmRole == "" {
			swarmRole = bootstrapRoleMaster
		}
	}
	if membershipRole == "" {
		if existingOK && existing.MembershipRole != "" {
			membershipRole = existing.MembershipRole
		} else {
			membershipRole = GroupMembershipRoleMember
		}
	}
	if name == "" || swarmRole == "" {
		localNode, localOK, err := s.store.GetLocalNode()
		if err != nil {
			return pebblestore.SwarmGroupMembershipRecord{}, err
		}
		if localOK && strings.EqualFold(localNode.SwarmID, swarmID) {
			name = firstNonEmpty(name, localNode.Name)
			swarmRole = firstNonEmpty(swarmRole, localNode.Role)
		}
	}
	if name == "" || swarmRole == "" {
		peers, err := s.store.ListTrustedPeers(500)
		if err != nil {
			return pebblestore.SwarmGroupMembershipRecord{}, err
		}
		for _, peer := range peers {
			if !strings.EqualFold(peer.SwarmID, swarmID) {
				continue
			}
			name = firstNonEmpty(name, peer.Name)
			swarmRole = firstNonEmpty(swarmRole, peer.Role)
			break
		}
	}
	name = firstNonEmpty(name, existing.Name)
	swarmRole = firstNonEmpty(swarmRole, existing.SwarmRole)
	if name == "" {
		return pebblestore.SwarmGroupMembershipRecord{}, errors.New("member name is required")
	}
	if swarmRole == "" {
		return pebblestore.SwarmGroupMembershipRecord{}, errors.New("member swarm role is required")
	}
	record := pebblestore.SwarmGroupMembershipRecord{
		GroupID:        groupID,
		SwarmID:        swarmID,
		Name:           name,
		SwarmRole:      swarmRole,
		MembershipRole: membershipRole,
	}
	if existingOK {
		record.CreatedAt = existing.CreatedAt
	}
	return record, nil
}

func toGroup(record pebblestore.SwarmGroupRecord) Group {
	return Group{
		ID:          record.ID,
		Name:        record.Name,
		NetworkName: record.NetworkName,
		HostSwarmID: record.HostSwarmID,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	}
}

func toGroupMember(record pebblestore.SwarmGroupMembershipRecord) GroupMember {
	return GroupMember{
		GroupID:        record.GroupID,
		SwarmID:        record.SwarmID,
		Name:           record.Name,
		SwarmRole:      record.SwarmRole,
		MembershipRole: record.MembershipRole,
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
	}
}

func toGroupMembers(records []pebblestore.SwarmGroupMembershipRecord) []GroupMember {
	out := make([]GroupMember, 0, len(records))
	for _, record := range records {
		out = append(out, toGroupMember(record))
	}
	return out
}

func generateGroupID() (string, error) {
	value, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("generate group id: %w", err)
	}
	return "group_" + value, nil
}

func (s *Service) ensureGroupNetworkName(record pebblestore.SwarmGroupRecord) (pebblestore.SwarmGroupRecord, error) {
	if strings.TrimSpace(record.NetworkName) != "" {
		return record, nil
	}
	record.NetworkName = SuggestedGroupNetworkName(record.Name, record.ID)
	return s.store.PutGroup(record)
}

func SuggestedGroupNetworkName(value string, fallback string) string {
	candidates := []string{value, fallback, "swarm-group"}
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		var builder strings.Builder
		builder.Grow(len(candidate))
		lastDash := false
		for _, r := range candidate {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				builder.WriteRune(r)
				lastDash = false
			default:
				if builder.Len() == 0 || lastDash {
					continue
				}
				builder.WriteByte('-')
				lastDash = true
			}
		}
		out := strings.Trim(builder.String(), "-")
		if out != "" {
			return out
		}
	}
	return "swarm-group"
}
