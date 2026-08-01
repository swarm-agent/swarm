package api

import swarmruntime "swarm/packages/swarmd/internal/swarm"

// setReplicateFakeSwarmState installs the static swarm state used by the
// workspace bootstrap and CWD resolution tests without restoring a retired
// runtime or route seam.
func setReplicateFakeSwarmState(server *Server, state swarmruntime.LocalState) {
	server.SetSwarmService(replicateFakeSwarmService{state: state})
}

type replicateFakeSwarmService struct {
	state swarmruntime.LocalState
}

func (f replicateFakeSwarmService) EnsureLocalState(swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}

func (f replicateFakeSwarmService) RenameLocalSwarm(input swarmruntime.RenameLocalSwarmInput) (swarmruntime.LocalState, error) {
	state := f.state
	state.Node.Name = input.Name
	return state, nil
}

func (f replicateFakeSwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return f.state.Groups, f.state.CurrentGroupID, nil
}

func (f replicateFakeSwarmService) UpsertGroup(input swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{
		ID:          input.GroupID,
		Name:        input.Name,
		NetworkName: input.NetworkName,
		HostSwarmID: input.HostSwarmID,
	}, nil
}

func (f replicateFakeSwarmService) DeleteGroup(string) error {
	return nil
}

func (f replicateFakeSwarmService) SetCurrentGroup(groupID string, _ string) (swarmruntime.GroupState, error) {
	for _, group := range f.state.Groups {
		if group.Group.ID == groupID {
			return group, nil
		}
	}
	return swarmruntime.GroupState{}, nil
}

func (f replicateFakeSwarmService) UpsertGroupMember(input swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{
		GroupID:        input.GroupID,
		SwarmID:        input.SwarmID,
		Name:           input.Name,
		SwarmRole:      input.SwarmRole,
		MembershipRole: input.MembershipRole,
	}, nil
}

func (f replicateFakeSwarmService) RemoveGroupMember(swarmruntime.RemoveGroupMemberInput) error {
	return nil
}
