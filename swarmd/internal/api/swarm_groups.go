package api

import (
	"errors"
	"net/http"
	"strings"

	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func requireMasterSwarmForGroups(state swarmruntime.LocalState) error {
	if strings.TrimSpace(state.Node.Role) != "master" {
		return errors.New("only master swarms can manage groups")
	}
	return nil
}

func localGroupMembership(state swarmruntime.LocalState, groupID string) (swarmruntime.GroupMember, bool) {
	groupID = strings.TrimSpace(groupID)
	localSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if groupID == "" || localSwarmID == "" {
		return swarmruntime.GroupMember{}, false
	}
	for _, group := range state.Groups {
		if !strings.EqualFold(strings.TrimSpace(group.Group.ID), groupID) {
			continue
		}
		for _, member := range group.Members {
			if strings.EqualFold(strings.TrimSpace(member.SwarmID), localSwarmID) {
				return member, true
			}
		}
	}
	return swarmruntime.GroupMember{}, false
}

func requireHostedGroupMembership(state swarmruntime.LocalState, groupID string) error {
	member, ok := localGroupMembership(state, groupID)
	if !ok {
		return errors.New("group is not part of the current local swarm state")
	}
	if strings.TrimSpace(member.MembershipRole) != swarmruntime.GroupMembershipRoleHost {
		return errors.New("only the host swarm can manage this group")
	}
	return nil
}

type swarmGroupsResponse struct {
	OK             bool                      `json:"ok"`
	CurrentGroupID string                    `json:"current_group_id,omitempty"`
	Groups         []swarmruntime.GroupState `json:"groups,omitempty"`
}

type swarmGroupUpsertRequest struct {
	GroupID     string `json:"group_id,omitempty"`
	Name        string `json:"name,omitempty"`
	NetworkName string `json:"network_name,omitempty"`
	SetCurrent  bool   `json:"set_current,omitempty"`
}

type swarmGroupDeleteRequest struct {
	GroupID string `json:"group_id,omitempty"`
}

type swarmCurrentGroupRequest struct {
	GroupID string `json:"group_id,omitempty"`
}

type swarmGroupMemberUpsertRequest struct {
	GroupID        string `json:"group_id,omitempty"`
	SwarmID        string `json:"swarm_id,omitempty"`
	Name           string `json:"name,omitempty"`
	SwarmRole      string `json:"swarm_role,omitempty"`
	MembershipRole string `json:"membership_role,omitempty"`
}

type swarmGroupMemberDeleteRequest struct {
	GroupID string `json:"group_id,omitempty"`
	SwarmID string `json:"swarm_id,omitempty"`
}

func (s *Server) handleSwarmGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
		return
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, swarmGroupsResponse{
		OK:             true,
		CurrentGroupID: strings.TrimSpace(state.CurrentGroupID),
		Groups:         state.Groups,
	})
}

func (s *Server) handleSwarmGroupUpsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
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
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := requireMasterSwarmForGroups(state); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req swarmGroupUpsertRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.GroupID) != "" {
		if err := requireHostedGroupMembership(state, req.GroupID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	group, err := s.swarm.UpsertGroup(swarmruntime.UpsertGroupInput{
		GroupID:     strings.TrimSpace(req.GroupID),
		Name:        strings.TrimSpace(req.Name),
		NetworkName: strings.TrimSpace(req.NetworkName),
		HostSwarmID: strings.TrimSpace(state.Node.SwarmID),
		SetCurrent:  req.SetCurrent,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	groups, currentGroupID, err := s.swarm.ListGroupsForSwarm(strings.TrimSpace(state.Node.SwarmID), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"group":            group,
		"groups":           groups,
		"current_group_id": currentGroupID,
	})
}

func (s *Server) handleSwarmCurrentGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
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
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := requireMasterSwarmForGroups(state); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req swarmCurrentGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, ok := localGroupMembership(state, req.GroupID); !ok {
		writeError(w, http.StatusBadRequest, errors.New("group is not part of the current local swarm state"))
		return
	}
	group, err := s.swarm.SetCurrentGroup(strings.TrimSpace(req.GroupID), strings.TrimSpace(state.Node.SwarmID))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	groups, currentGroupID, err := s.swarm.ListGroupsForSwarm(strings.TrimSpace(state.Node.SwarmID), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"group":            group,
		"groups":           groups,
		"current_group_id": currentGroupID,
	})
}

func (s *Server) handleSwarmGroupMemberDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.swarm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("swarm service is not configured"))
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
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := requireMasterSwarmForGroups(state); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req swarmGroupMemberDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := requireHostedGroupMembership(state, req.GroupID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.swarm.RemoveGroupMember(swarmruntime.RemoveGroupMemberInput{
		GroupID: strings.TrimSpace(req.GroupID),
		SwarmID: strings.TrimSpace(req.SwarmID),
	}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	groups, currentGroupID, err := s.swarm.ListGroupsForSwarm(strings.TrimSpace(state.Node.SwarmID), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"groups":           groups,
		"current_group_id": currentGroupID,
	})
}
