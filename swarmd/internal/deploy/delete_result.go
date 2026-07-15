package deploy

// DeleteItemResult describes the deletion outcome for a deploy-owned child runtime.
type DeleteItemResult struct {
	ID                      string `json:"id"`
	Name                    string `json:"name,omitempty"`
	ContainerName           string `json:"container_name,omitempty"`
	Deleted                 bool   `json:"deleted"`
	ChildSwarmID            string `json:"child_swarm_id,omitempty"`
	ChildDisplayName        string `json:"child_display_name,omitempty"`
	ChildInfoDetected       bool   `json:"child_info_detected,omitempty"`
	RemovedDeployment       bool   `json:"removed_deployment,omitempty"`
	RemovedTrustedPeer      bool   `json:"removed_trusted_peer,omitempty"`
	RemovedGroupMemberships int    `json:"removed_group_memberships,omitempty"`
	RemovedWorkspaceRoutes  int    `json:"removed_workspace_routes,omitempty"`
	Error                   string `json:"error,omitempty"`
}

// DeleteResult summarizes deletion outcomes for deploy-owned runtime records.
type DeleteResult struct {
	Deleted          []string           `json:"deleted"`
	Count            int                `json:"count"`
	Failed           int                `json:"failed,omitempty"`
	ChildInfoRemoved int                `json:"child_info_removed,omitempty"`
	Items            []DeleteItemResult `json:"items,omitempty"`
}
