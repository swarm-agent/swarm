package pebblestore

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	KeyAuthCodexDefault                         = "auth/codex/default" // legacy single-record key; retained for migration.
	KeyAuthAttachDefault                        = "auth/attach/default"
	KeyAuthVaultMeta                            = "auth/vault/meta"
	KeyAuthCredentialPrefix                     = "auth/credential/"
	KeyAuthCredentialActivePrefix               = "auth/credential_active/"
	KeyAuthCredentialTagPrefix                  = "auth/index/auth_tag/"
	KeyAuthManagedVaultKeyPrefix                = "auth/managed_vault_key/"
	KeyUISettingsDefault                        = "ui/settings/default"
	KeyUIChatSettingsDefault                    = "ui/chat_settings/default"
	KeyIdentityPrefix                           = "identity/"
	KeyIdentityUserPrefix                       = "identity/user/"
	KeyIdentityUserByUsernamePrefix             = "identity/user_by_username/"
	KeyIdentityTeamPrefix                       = "identity/team/"
	KeyIdentityTeamMembershipPrefix             = "identity/membership/"
	KeyIdentityCurrentSelectionDefault          = "identity/current_selection/default"
	KeyIdentityCurrentSelectionPrefix           = "identity/current_selection/"
	KeyIdentityLocalProductJWTSigningKeyDefault = "identity/session/local_product_jwt_signing_key"
	KeyVoiceConfigDefault                       = "voice/config/default"
	KeyVoiceProfilePrefix                       = "voice/profile/"
	KeyVoiceProfileActiveSTT                    = "voice/profile_active/stt"
	KeyModelPrefGlobal                          = "model_pref/global/default"
	KeyModelFavoritePrefix                      = "model_favorite/"
	KeyWorktreeGlobalConfig                     = "worktree/global/config"
	KeyWorktreeConfigPrefix                     = "worktree/config/"
	KeyMCPServerPrefix                          = "mcp/server/"
	KeyWorkspaceCurrent                         = "workspace/current"
	KeyWorkspaceEntryPrefix                     = "workspace/entry/"
	KeyWorkspaceTodoItemPrefix                  = "workspace_todo/item/"
	KeyVideoThreadPrefix                        = "video/thread/"
	KeyImageThreadPrefix                        = "image/thread/"
	KeyModelCatalogMeta                         = "model_catalog/meta"
	KeyAgentProfilePrefix                       = "agent/profile/"
	KeyAgentCustomToolPrefix                    = "agent/custom_tool/"
	KeyAgentActivePrimary                       = "agent/active/primary"
	KeyAgentActiveSubagentPrefix                = "agent/active/subagent/"
	KeyAgentVersion                             = "agent/version"
	KeySwarmLocalNodeDefault                    = "swarm/local_node/default"
	KeySwarmLocalPairingDefault                 = "swarm/local_pairing/default"
	KeySwarmCurrentGroupDefault                 = "swarm/current_group/default"
	KeySwarmGroupPrefix                         = "swarm/group/"
	KeySwarmGroupMembershipPrefix               = "swarm/group_membership/"
	KeySwarmGroupBySwarmPrefix                  = "swarm/group_membership_by_swarm/"
	KeySwarmContainerProfilePrefix              = "swarm/container_profile/"
	KeySwarmLocalContainerPrefix                = "swarm/local_container/"
	KeySwarmNodePrefix                          = "swarm/node/"
	KeyDeployContainerPrefix                    = "deploy/container/"
	KeyRemoteDeploySessionPrefix                = "deploy/remote_session/"
	KeySwarmInvitePrefix                        = "swarm/invite/"
	KeySwarmInviteTokenPrefix                   = "swarm/invite_token/"
	KeySwarmEnrollmentPrefix                    = "swarm/enrollment/"
	KeySwarmTrustedPeerPrefix                   = "swarm/trusted_peer/"
	KeySwarmDesktopTargetCurrent                = "swarm/desktop_target/current"
	KeyTopologyRuntimePrefix                    = "topology/runtime/"
	KeyTopologyHostContainerPrefix              = "topology/host_container/"
	KeyTopologyAttachmentPrefix                 = "topology/attachment/"
	KeyTopologyWorkspaceBindingPrefix           = "topology/workspace_binding/"
	KeyTopologySessionRoutePrefix               = "topology/session_route/"
	KeyTopologyMigrationStatusPrefix            = "topology/migration_status/"
	KeySwarmMirrorLocalSeq                      = "swarm/mirror/local/seq"
	KeySwarmMirrorLocalEventPrefix              = "swarm/mirror/local/event/"
	KeySwarmMirrorLocalResourcePrefix           = "swarm/mirror/local/resource/"
	KeySwarmMirrorRemoteCursorPrefix            = "swarm/mirror/remote/cursor/"
	KeySwarmMirrorRemoteResourcePrefix          = "swarm/mirror/remote/resource/"
	KeyNotificationPrefix                       = "notification/"
	KeyNotificationBySwarmPrefix                = "notification_by_swarm/"
	KeyNotificationPermissionRefPrefix          = "notification_permission_ref/"
	KeyNotificationSummaryPrefix                = "notification_summary/"
	KeyFlowDefinitionPrefix                     = "flow/definition/"
	KeyFlowAssignmentStatusPrefix               = "flow/assignment_status/"
	KeyFlowOutboxPrefix                         = "flow/outbox/"
	KeyFlowOutboxStatusPrefix                   = "flow/outbox_status/"
	KeyFlowMirroredRunPrefix                    = "flow/mirrored_run/"
	KeyFlowTargetAcceptedPrefix                 = "flow_target/accepted/"
	KeyFlowTargetCommandLedgerPrefix            = "flow_target/command_ledger/"
	KeyFlowTargetDuePrefix                      = "flow_target/due/"
	KeyFlowTargetRunPrefix                      = "flow_target/run/"
	KeyFlowTargetRunByFlowPrefix                = "flow_target/run_by_flow/"
	KeyFlowTargetRunClaimPrefix                 = "flow_target/run_claim/"
	KeyIntegrationPackPrefix                    = "integration/pack/"
	KeyIntegrationPackVersionPrefix             = "integration/pack_version/"
	KeyIntegrationToolPrefix                    = "integration/tool/"
	KeyIntegrationAdapterPrefix                 = "integration/adapter/"
	KeyIntegrationPromptFragmentPrefix          = "integration/prompt_fragment/"
	KeyIntegrationAssignmentPrefix              = "integration/assignment/"
	KeyIntegrationAssignmentAgentPrefix         = "integration/assignment_by_agent/"
	KeyIntegrationAssignmentPackPrefix          = "integration/assignment_by_pack/"
	KeyIntegrationWorkspacePrefix               = "integration/workspace/"
	KeyIntegrationWorkspaceSessionPrefix        = "integration/workspace_session/"
	KeyIntegrationWorkspaceSessionUpdatedPrefix = "integration/workspace_session_updated/"
	keyGlobalSequenceCounter                    = "meta/global_seq"
)

func EventKey(sequence uint64) string {
	return fmt.Sprintf("evt/%020d", sequence)
}

func KeyModelCatalog(providerID, modelID string) string {
	return fmt.Sprintf("model_catalog/%s/%s", keyPart(providerID), keyPart(modelID))
}

func ModelCatalogPrefix(providerID string) string {
	providerPart := keyPart(providerID)
	if providerPart == "" {
		return "model_catalog/"
	}
	return fmt.Sprintf("model_catalog/%s/", providerPart)
}

func KeyModelFavorite(providerID, modelID string) string {
	return fmt.Sprintf("%s%s/%s", KeyModelFavoritePrefix, keyPart(providerID), keyPart(modelID))
}

func ModelFavoritePrefix(providerID string) string {
	providerPart := keyPart(providerID)
	if providerPart == "" {
		return KeyModelFavoritePrefix
	}
	return fmt.Sprintf("%s%s/", KeyModelFavoritePrefix, providerPart)
}

func IdentityPrefix() string {
	return KeyIdentityPrefix
}

func KeyIdentityUser(userID string) string {
	return KeyIdentityUserPrefix + keyPart(userID)
}

func IdentityUserPrefix() string {
	return KeyIdentityUserPrefix
}

func KeyIdentityUserByUsername(username string) string {
	return KeyIdentityUserByUsernamePrefix + keyPart(username)
}

func IdentityUserByUsernamePrefix() string {
	return KeyIdentityUserByUsernamePrefix
}

func KeyIdentityTeam(teamID string) string {
	return KeyIdentityTeamPrefix + keyPart(teamID)
}

func IdentityTeamPrefix() string {
	return KeyIdentityTeamPrefix
}

func KeyIdentityTeamMembership(teamID, userID string) string {
	return fmt.Sprintf("%s%s/%s", KeyIdentityTeamMembershipPrefix, keyPart(teamID), keyPart(userID))
}

func IdentityTeamMembershipPrefix(teamID string) string {
	part := keyPart(teamID)
	if part == "" {
		return KeyIdentityTeamMembershipPrefix
	}
	return fmt.Sprintf("%s%s/", KeyIdentityTeamMembershipPrefix, part)
}

func KeyIdentityCurrentSelection() string {
	return KeyIdentityCurrentSelectionDefault
}

func IdentityCurrentSelectionPrefix() string {
	return KeyIdentityCurrentSelectionPrefix
}

func KeyIdentityLocalProductJWTSigningKey() string {
	return KeyIdentityLocalProductJWTSigningKeyDefault
}

func KeySession(sessionID string) string {
	return fmt.Sprintf("session/%s", keyPart(sessionID))
}

func KeySessionMode(sessionID string) string {
	return fmt.Sprintf("session_mode/%s", keyPart(sessionID))
}

func SessionPrefix() string {
	return "session/"
}

func KeySessionRoute(sessionID string) string {
	return fmt.Sprintf("session_route/%s", keyPart(sessionID))
}

func SessionRoutePrefix() string {
	return "session_route/"
}

func KeyTopologyRuntime(swarmID string) string {
	return KeyTopologyRuntimePrefix + keyPart(swarmID)
}

func TopologyRuntimePrefix() string {
	return KeyTopologyRuntimePrefix
}

func KeyTopologyHostContainer(hostContainerID string) string {
	return KeyTopologyHostContainerPrefix + keyPart(hostContainerID)
}

func TopologyHostContainerPrefix() string {
	return KeyTopologyHostContainerPrefix
}

func KeyTopologyAttachment(attachmentID string) string {
	return KeyTopologyAttachmentPrefix + keyPart(attachmentID)
}

func TopologyAttachmentPrefix() string {
	return KeyTopologyAttachmentPrefix
}

func KeyTopologyWorkspaceBinding(bindingID string) string {
	return KeyTopologyWorkspaceBindingPrefix + keyPart(bindingID)
}

func TopologyWorkspaceBindingPrefix() string {
	return KeyTopologyWorkspaceBindingPrefix
}

func KeyTopologySessionRoute(sessionID string) string {
	return KeyTopologySessionRoutePrefix + keyPart(sessionID)
}

func TopologySessionRoutePrefix() string {
	return KeyTopologySessionRoutePrefix
}

func KeyTopologyMigrationStatus(statusID string) string {
	return KeyTopologyMigrationStatusPrefix + keyPart(statusID)
}

func TopologyMigrationStatusPrefix() string {
	return KeyTopologyMigrationStatusPrefix
}

func KeySessionLifecycle(sessionID string) string {
	return fmt.Sprintf("session_lifecycle/%s", keyPart(sessionID))
}

func SessionLifecyclePrefix() string {
	return "session_lifecycle/"
}

func KeyWorkspaceEntry(path string) string {
	return KeyWorkspaceEntryPrefix + keyPart(path)
}

func KeyVideoThread(threadID string) string {
	return KeyVideoThreadPrefix + keyPart(threadID)
}

func VideoThreadPrefix() string {
	return KeyVideoThreadPrefix
}

func KeyImageThread(threadID string) string {
	return KeyImageThreadPrefix + keyPart(threadID)
}

func ImageThreadPrefix() string {
	return KeyImageThreadPrefix
}

func KeyWorktreeConfig(workspacePath string) string {
	return KeyWorktreeConfigPrefix + keyPart(workspacePath)
}

func WorkspaceEntryPrefix() string {
	return KeyWorkspaceEntryPrefix
}

func KeyWorkspaceTodoItem(workspacePath, itemID string) string {
	return fmt.Sprintf("%s%s/%s", KeyWorkspaceTodoItemPrefix, keyPart(workspacePath), keyPart(itemID))
}

func WorkspaceTodoPrefix(workspacePath string) string {
	workspacePart := keyPart(workspacePath)
	if workspacePart == "" {
		return KeyWorkspaceTodoItemPrefix
	}
	return fmt.Sprintf("%s%s/", KeyWorkspaceTodoItemPrefix, workspacePart)
}

func KeyMCPServer(serverID string) string {
	return KeyMCPServerPrefix + keyPart(serverID)
}

func MCPServerPrefix() string {
	return KeyMCPServerPrefix
}

func KeyAuthCredential(providerID, credentialID string) string {
	return fmt.Sprintf("%s%s/%s", KeyAuthCredentialPrefix, keyPart(providerID), keyPart(credentialID))
}

func AuthCredentialPrefix() string {
	return KeyAuthCredentialPrefix
}

func AuthCredentialProviderPrefix(providerID string) string {
	part := keyPart(providerID)
	if part == "" {
		return KeyAuthCredentialPrefix
	}
	return fmt.Sprintf("%s%s/", KeyAuthCredentialPrefix, part)
}

func KeyAuthCredentialActive(providerID string) string {
	return KeyAuthCredentialActivePrefix + keyPart(providerID)
}

func KeyVoiceProfile(profileID string) string {
	return KeyVoiceProfilePrefix + keyPart(profileID)
}

func VoiceProfilePrefix() string {
	return KeyVoiceProfilePrefix
}

func KeySwarmGroup(groupID string) string {
	return KeySwarmGroupPrefix + keyPart(groupID)
}

func SwarmGroupPrefix() string {
	return KeySwarmGroupPrefix
}

func KeySwarmGroupMembership(groupID, swarmID string) string {
	return fmt.Sprintf("%s%s/%s", KeySwarmGroupMembershipPrefix, keyPart(groupID), keyPart(swarmID))
}

func SwarmGroupMembershipPrefix(groupID string) string {
	part := keyPart(groupID)
	if part == "" {
		return KeySwarmGroupMembershipPrefix
	}
	return fmt.Sprintf("%s%s/", KeySwarmGroupMembershipPrefix, part)
}

func KeySwarmGroupMembershipBySwarm(swarmID, groupID string) string {
	return fmt.Sprintf("%s%s/%s", KeySwarmGroupBySwarmPrefix, keyPart(swarmID), keyPart(groupID))
}

func SwarmGroupMembershipBySwarmPrefix(swarmID string) string {
	part := keyPart(swarmID)
	if part == "" {
		return KeySwarmGroupBySwarmPrefix
	}
	return fmt.Sprintf("%s%s/", KeySwarmGroupBySwarmPrefix, part)
}

func KeyAuthCredentialTag(tag, providerID, credentialID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyAuthCredentialTagPrefix, keyPart(tag), keyPart(providerID), keyPart(credentialID))
}

func KeyAuthManagedVaultKey(scopeID string) string {
	return KeyAuthManagedVaultKeyPrefix + keyPart(scopeID)
}

func KeyMessage(sessionID string, globalSeq uint64) string {
	return fmt.Sprintf("msg/%s/%020d", keyPart(sessionID), globalSeq)
}

func MessagePrefix(sessionID string) string {
	return fmt.Sprintf("msg/%s/", keyPart(sessionID))
}

func KeySessionPlan(sessionID, planID string) string {
	return fmt.Sprintf("session_plan/%s/%s", keyPart(sessionID), keyPart(planID))
}

func SessionPlanPrefix(sessionID string) string {
	part := keyPart(sessionID)
	if part == "" {
		return "session_plan/"
	}
	return fmt.Sprintf("session_plan/%s/", part)
}

func KeySessionPlanActive(sessionID string) string {
	return fmt.Sprintf("session_plan_active/%s", keyPart(sessionID))
}

func KeySessionTurnUsage(sessionID, runID string) string {
	return fmt.Sprintf("session_turn_usage/%s/%s", keyPart(sessionID), keyPart(runID))
}

func SessionTurnUsagePrefix(sessionID string) string {
	part := keyPart(sessionID)
	if part == "" {
		return "session_turn_usage/"
	}
	return fmt.Sprintf("session_turn_usage/%s/", part)
}

func KeySessionUsageSummary(sessionID string) string {
	return fmt.Sprintf("session_usage_summary/%s", keyPart(sessionID))
}

func KeyPermission(sessionID, permissionID string) string {
	return fmt.Sprintf("perm/%s/%s", keyPart(sessionID), keyPart(permissionID))
}

func PermissionPrefix(sessionID string) string {
	part := keyPart(sessionID)
	if part == "" {
		return "perm/"
	}
	return fmt.Sprintf("perm/%s/", part)
}

func KeyPermissionPending(sessionID string, createdAt int64, permissionID string) string {
	return fmt.Sprintf("perm_pending/%s/%020d/%s", keyPart(sessionID), createdAt, keyPart(permissionID))
}

func PermissionPendingPrefix(sessionID string) string {
	part := keyPart(sessionID)
	if part == "" {
		return "perm_pending/"
	}
	return fmt.Sprintf("perm_pending/%s/", part)
}

func KeyPermissionSummary(principalID, sessionID string) string {
	return fmt.Sprintf("perm_summary/%s/%s", keyPart(principalID), keyPart(sessionID))
}

func KeyPermissionPolicy() string {
	return "perm_policy/current"
}

func KeyRunWait(sessionID, runID string) string {
	return fmt.Sprintf("run_wait/%s/%s", keyPart(sessionID), keyPart(runID))
}

func RunWaitPrefix(sessionID string) string {
	part := keyPart(sessionID)
	if part == "" {
		return "run_wait/"
	}
	return fmt.Sprintf("run_wait/%s/", part)
}

func KeyRunPermission(sessionID, runID, permissionID string) string {
	return fmt.Sprintf("run_perm/%s/%s/%s", keyPart(sessionID), keyPart(runID), keyPart(permissionID))
}

func RunPermissionPrefix(sessionID, runID string) string {
	sessionPart := keyPart(sessionID)
	runPart := keyPart(runID)
	if sessionPart == "" {
		return "run_perm/"
	}
	if runPart == "" {
		return fmt.Sprintf("run_perm/%s/", sessionPart)
	}
	return fmt.Sprintf("run_perm/%s/%s/", sessionPart, runPart)
}

func KeyNotification(swarmID, notificationID string) string {
	return fmt.Sprintf("%s%s/%s", KeyNotificationPrefix, keyPart(swarmID), keyPart(notificationID))
}

func KeyNotificationBySwarm(swarmID string, createdAt int64, notificationID string) string {
	return fmt.Sprintf("%s%s/%020d/%s", KeyNotificationBySwarmPrefix, keyPart(swarmID), createdAt, keyPart(notificationID))
}

func NotificationBySwarmPrefix(swarmID string) string {
	part := keyPart(swarmID)
	if part == "" {
		return KeyNotificationBySwarmPrefix
	}
	return fmt.Sprintf("%s%s/", KeyNotificationBySwarmPrefix, part)
}

func KeyNotificationPermissionRef(sessionID, permissionID string) string {
	return fmt.Sprintf("%s%s/%s", KeyNotificationPermissionRefPrefix, keyPart(sessionID), keyPart(permissionID))
}

func KeyNotificationSummary(swarmID string) string {
	return KeyNotificationSummaryPrefix + keyPart(swarmID)
}

func KeyFlowDefinition(flowID string) string {
	return KeyFlowDefinitionPrefix + keyPart(flowID)
}

func FlowDefinitionPrefix() string {
	return KeyFlowDefinitionPrefix
}

func KeyFlowAssignmentStatus(flowID, targetSwarmID string) string {
	return fmt.Sprintf("%s%s/%s", KeyFlowAssignmentStatusPrefix, keyPart(flowID), keyPart(targetSwarmID))
}

func FlowAssignmentStatusPrefix(flowID string) string {
	part := keyPart(flowID)
	if part == "" {
		return KeyFlowAssignmentStatusPrefix
	}
	return fmt.Sprintf("%s%s/", KeyFlowAssignmentStatusPrefix, part)
}

func KeyFlowOutbox(commandID string) string {
	return KeyFlowOutboxPrefix + keyPart(commandID)
}

func KeyFlowOutboxStatus(status string, nextAttemptAt int64, commandID string) string {
	return fmt.Sprintf("%s%s/%020d/%s", KeyFlowOutboxStatusPrefix, keyPart(status), nextAttemptAt, keyPart(commandID))
}

func FlowOutboxStatusPrefix(status string) string {
	part := keyPart(status)
	if part == "" {
		return KeyFlowOutboxStatusPrefix
	}
	return fmt.Sprintf("%s%s/", KeyFlowOutboxStatusPrefix, part)
}

func KeyFlowMirroredRun(flowID string, startedAt int64, runID string) string {
	return fmt.Sprintf("%s%s/%020d/%s", KeyFlowMirroredRunPrefix, keyPart(flowID), reverseMillis(startedAt), keyPart(runID))
}

func FlowMirroredRunPrefix(flowID string) string {
	part := keyPart(flowID)
	if part == "" {
		return KeyFlowMirroredRunPrefix
	}
	return fmt.Sprintf("%s%s/", KeyFlowMirroredRunPrefix, part)
}

func KeyFlowTargetAccepted(flowID string) string {
	return KeyFlowTargetAcceptedPrefix + keyPart(flowID)
}

func FlowTargetAcceptedPrefix() string {
	return KeyFlowTargetAcceptedPrefix
}

func KeyFlowTargetCommandLedger(flowID string, revision int64, commandID string) string {
	return fmt.Sprintf("%s%s/%020d/%s", KeyFlowTargetCommandLedgerPrefix, keyPart(flowID), revision, keyPart(commandID))
}

func FlowTargetCommandLedgerPrefix(flowID string) string {
	part := keyPart(flowID)
	if part == "" {
		return KeyFlowTargetCommandLedgerPrefix
	}
	return fmt.Sprintf("%s%s/", KeyFlowTargetCommandLedgerPrefix, part)
}

func KeyFlowTargetDue(dueAt int64, flowID string, revision int64) string {
	return fmt.Sprintf("%s%020d/%s/%020d", KeyFlowTargetDuePrefix, dueAt, keyPart(flowID), revision)
}

func FlowTargetDuePrefix() string {
	return KeyFlowTargetDuePrefix
}

func KeyFlowTargetRun(runID string) string {
	return KeyFlowTargetRunPrefix + keyPart(runID)
}

func KeyFlowTargetRunByFlow(flowID string, startedAt int64, runID string) string {
	return fmt.Sprintf("%s%s/%020d/%s", KeyFlowTargetRunByFlowPrefix, keyPart(flowID), reverseMillis(startedAt), keyPart(runID))
}

func FlowTargetRunByFlowPrefix(flowID string) string {
	part := keyPart(flowID)
	if part == "" {
		return KeyFlowTargetRunByFlowPrefix
	}
	return fmt.Sprintf("%s%s/", KeyFlowTargetRunByFlowPrefix, part)
}

func KeyFlowTargetRunClaim(flowID string, revision int64, scheduledAt int64) string {
	return fmt.Sprintf("%s%s/%020d/%020d", KeyFlowTargetRunClaimPrefix, keyPart(flowID), revision, scheduledAt)
}

func KeyAgentProfile(name string) string {
	return KeyAgentProfilePrefix + keyPart(name)
}

func KeyAgentCustomTool(name string) string {
	return KeyAgentCustomToolPrefix + keyPart(name)
}

func AgentProfilePrefix() string {
	return KeyAgentProfilePrefix
}

func AgentCustomToolPrefix() string {
	return KeyAgentCustomToolPrefix
}

func KeyAgentActiveSubagent(purpose string) string {
	return KeyAgentActiveSubagentPrefix + keyPart(purpose)
}

func KeyIntegrationPack(packID string) string {
	return KeyIntegrationPackPrefix + keyPart(packID)
}

func IntegrationPackPrefix() string {
	return KeyIntegrationPackPrefix
}

func KeyIntegrationPackVersion(packID, versionID string) string {
	return fmt.Sprintf("%s%s/%s", KeyIntegrationPackVersionPrefix, keyPart(packID), keyPart(versionID))
}

func IntegrationPackVersionPrefix(packID string) string {
	part := keyPart(packID)
	if part == "" {
		return KeyIntegrationPackVersionPrefix
	}
	return fmt.Sprintf("%s%s/", KeyIntegrationPackVersionPrefix, part)
}

func KeyIntegrationTool(packID, versionID, toolID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyIntegrationToolPrefix, keyPart(packID), keyPart(versionID), keyPart(toolID))
}

func IntegrationToolPrefix(packID, versionID string) string {
	packPart := keyPart(packID)
	if packPart == "" {
		return KeyIntegrationToolPrefix
	}
	versionPart := keyPart(versionID)
	if versionPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationToolPrefix, packPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyIntegrationToolPrefix, packPart, versionPart)
}

func KeyIntegrationAdapter(packID, versionID, adapterID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyIntegrationAdapterPrefix, keyPart(packID), keyPart(versionID), keyPart(adapterID))
}

func IntegrationAdapterPrefix(packID, versionID string) string {
	packPart := keyPart(packID)
	if packPart == "" {
		return KeyIntegrationAdapterPrefix
	}
	versionPart := keyPart(versionID)
	if versionPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationAdapterPrefix, packPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyIntegrationAdapterPrefix, packPart, versionPart)
}

func KeyIntegrationPromptFragment(packID, versionID, fragmentID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyIntegrationPromptFragmentPrefix, keyPart(packID), keyPart(versionID), keyPart(fragmentID))
}

func IntegrationPromptFragmentPrefix(packID, versionID string) string {
	packPart := keyPart(packID)
	if packPart == "" {
		return KeyIntegrationPromptFragmentPrefix
	}
	versionPart := keyPart(versionID)
	if versionPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationPromptFragmentPrefix, packPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyIntegrationPromptFragmentPrefix, packPart, versionPart)
}

func KeyIntegrationAssignment(assignmentID string) string {
	return KeyIntegrationAssignmentPrefix + keyPart(assignmentID)
}

func IntegrationAssignmentPrefix() string {
	return KeyIntegrationAssignmentPrefix
}

func KeyIntegrationAssignmentByAgent(agentName, assignmentID string) string {
	return fmt.Sprintf("%s%s/%s", KeyIntegrationAssignmentAgentPrefix, keyPart(agentName), keyPart(assignmentID))
}

func IntegrationAssignmentByAgentPrefix(agentName string) string {
	part := keyPart(agentName)
	if part == "" {
		return KeyIntegrationAssignmentAgentPrefix
	}
	return fmt.Sprintf("%s%s/", KeyIntegrationAssignmentAgentPrefix, part)
}

func KeyIntegrationAssignmentByPack(packID, versionID, assignmentID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyIntegrationAssignmentPackPrefix, keyPart(packID), keyPart(versionID), keyPart(assignmentID))
}

func IntegrationAssignmentByPackPrefix(packID, versionID string) string {
	packPart := keyPart(packID)
	if packPart == "" {
		return KeyIntegrationAssignmentPackPrefix
	}
	versionPart := keyPart(versionID)
	if versionPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationAssignmentPackPrefix, packPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyIntegrationAssignmentPackPrefix, packPart, versionPart)
}

func KeyIntegrationWorkspace(workspaceID string) string {
	return KeyIntegrationWorkspacePrefix + keyPart(workspaceID)
}

func IntegrationWorkspacePrefix() string {
	return KeyIntegrationWorkspacePrefix
}

func KeyIntegrationWorkspaceSession(workspaceID, sessionID string) string {
	return fmt.Sprintf("%s%s/%s", KeyIntegrationWorkspaceSessionPrefix, keyPart(workspaceID), keyPart(sessionID))
}

func IntegrationWorkspaceSessionPrefix(workspaceID string) string {
	part := keyPart(workspaceID)
	if part == "" {
		return KeyIntegrationWorkspaceSessionPrefix
	}
	return fmt.Sprintf("%s%s/", KeyIntegrationWorkspaceSessionPrefix, part)
}

func KeyIntegrationWorkspaceSessionUpdated(workspaceID string, updatedAt int64, sessionID string) string {
	return fmt.Sprintf("%s%s/%020d/%s", KeyIntegrationWorkspaceSessionUpdatedPrefix, keyPart(workspaceID), reverseMillis(updatedAt), keyPart(sessionID))
}

func IntegrationWorkspaceSessionUpdatedPrefix(workspaceID string) string {
	part := keyPart(workspaceID)
	if part == "" {
		return KeyIntegrationWorkspaceSessionUpdatedPrefix
	}
	return fmt.Sprintf("%s%s/", KeyIntegrationWorkspaceSessionUpdatedPrefix, part)
}

func KeySwarmInvite(inviteID string) string {
	return KeySwarmInvitePrefix + keyPart(inviteID)
}

func KeySwarmContainerProfile(profileID string) string {
	return KeySwarmContainerProfilePrefix + keyPart(profileID)
}

func KeySwarmLocalContainer(containerID string) string {
	return KeySwarmLocalContainerPrefix + keyPart(containerID)
}

func KeySwarmNode(swarmID string) string {
	return KeySwarmNodePrefix + keyPart(swarmID)
}

func KeyDeployContainer(deploymentID string) string {
	return KeyDeployContainerPrefix + keyPart(deploymentID)
}

func KeyRemoteDeploySession(sessionID string) string {
	return KeyRemoteDeploySessionPrefix + keyPart(sessionID)
}

func SwarmContainerProfilePrefix() string {
	return KeySwarmContainerProfilePrefix
}

func SwarmLocalContainerPrefix() string {
	return KeySwarmLocalContainerPrefix
}

func SwarmNodePrefix() string {
	return KeySwarmNodePrefix
}

func DeployContainerPrefix() string {
	return KeyDeployContainerPrefix
}

func RemoteDeploySessionPrefix() string {
	return KeyRemoteDeploySessionPrefix
}

func KeySwarmMirrorLocalEvent(sequence uint64) string {
	return fmt.Sprintf("%s%020d", KeySwarmMirrorLocalEventPrefix, sequence)
}

func KeySwarmMirrorLocalResource(kind, id string) string {
	return fmt.Sprintf("%s%s/%s", KeySwarmMirrorLocalResourcePrefix, keyPart(kind), keyPart(id))
}

func SwarmMirrorLocalResourcePrefix() string {
	return KeySwarmMirrorLocalResourcePrefix
}

func KeySwarmMirrorRemoteCursor(managedSwarmID string) string {
	return KeySwarmMirrorRemoteCursorPrefix + keyPart(managedSwarmID)
}

func KeySwarmMirrorRemoteResource(managedSwarmID, kind, id string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeySwarmMirrorRemoteResourcePrefix, keyPart(managedSwarmID), keyPart(kind), keyPart(id))
}

func SwarmMirrorRemoteResourcePrefix() string {
	return KeySwarmMirrorRemoteResourcePrefix
}

func KeySwarmMirrorRemoteResourcePrefixForSwarm(managedSwarmID string) string {
	return KeySwarmMirrorRemoteResourcePrefix + keyPart(managedSwarmID) + "/"
}

func KeySwarmInviteToken(token string) string {
	return KeySwarmInviteTokenPrefix + keyPart(token)
}

func KeySwarmEnrollment(enrollmentID string) string {
	return KeySwarmEnrollmentPrefix + keyPart(enrollmentID)
}

func SwarmEnrollmentPrefix() string {
	return KeySwarmEnrollmentPrefix
}

func KeySwarmTrustedPeer(swarmID string) string {
	return KeySwarmTrustedPeerPrefix + keyPart(swarmID)
}

func SwarmTrustedPeerPrefix() string {
	return KeySwarmTrustedPeerPrefix
}

func AgentActiveSubagentPrefix() string {
	return KeyAgentActiveSubagentPrefix
}

func reverseMillis(value int64) int64 {
	if value < 0 {
		value = 0
	}
	return 999999999999999999 - value
}

func keyPart(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	return url.PathEscape(clean)
}
