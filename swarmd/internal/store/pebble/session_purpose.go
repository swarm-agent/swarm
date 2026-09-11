package pebblestore

const (
	SessionPurposeMetadataKey          = "swarm_v3_session_purpose"
	SessionPurposeWorkspaceMetadataKey = "swarm_v3_purpose_workspace_id"
	SessionPurposeAutomationManagement = "automation_management"
)

// SessionAutomationManagementWorkspace is presentation/context identity, never
// an automation binding, execution policy, or workspace permission grant.
func SessionAutomationManagementWorkspace(session SessionSnapshot) string {
	purpose, _ := session.Metadata[SessionPurposeMetadataKey].(string)
	workspaceID, _ := session.Metadata[SessionPurposeWorkspaceMetadataKey].(string)
	if purpose != SessionPurposeAutomationManagement {
		return ""
	}
	return workspaceID
}
