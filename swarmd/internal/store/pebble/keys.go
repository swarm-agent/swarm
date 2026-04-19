package pebblestore

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	KeyAuthCodexDefault                = "auth/codex/default" // legacy single-record key; retained for migration.
	KeyAuthAttachDefault               = "auth/attach/default"
	KeyAuthVaultMeta                   = "auth/vault/meta"
	KeyAuthCredentialPrefix            = "auth/credential/"
	KeyAuthCredentialActivePrefix      = "auth/credential_active/"
	KeyAuthCredentialTagPrefix         = "auth/index/auth_tag/"
	KeyAuthManagedVaultKeyPrefix       = "auth/managed_vault_key/"
	KeyUISettingsDefault               = "ui/settings/default"
	KeyUIChatSettingsDefault           = "ui/chat_settings/default"
	KeyVoiceConfigDefault              = "voice/config/default"
	KeyVoiceProfilePrefix              = "voice/profile/"
	KeyVoiceProfileActiveSTT           = "voice/profile_active/stt"
	KeyModelPrefGlobal                 = "model_pref/global/default"
	KeyModelFavoritePrefix             = "model_favorite/"
	KeySandboxGlobalState              = "sandbox/global/state"
	KeyWorktreeGlobalConfig            = "worktree/global/config"
	KeyWorktreeConfigPrefix            = "worktree/config/"
	KeyMCPServerPrefix                 = "mcp/server/"
	KeyWorkspaceCurrent                = "workspace/current"
	KeyWorkspaceEntryPrefix            = "workspace/entry/"
	KeyWorkspaceTodoItemPrefix         = "workspace_todo/item/"
	KeyModelCatalogMeta                = "model_catalog/meta"
	KeyAgentProfilePrefix              = "agent/profile/"
	KeyAgentCustomToolPrefix           = "agent/custom_tool/"
	KeyAgentActivePrimary              = "agent/active/primary"
	KeyAgentActiveSubagentPrefix       = "agent/active/subagent/"
	KeyAgentVersion                    = "agent/version"
	KeySwarmLocalNodeDefault           = "swarm/local_node/default"
	KeySwarmLocalPairingDefault        = "swarm/local_pairing/default"
	KeySwarmCurrentGroupDefault        = "swarm/current_group/default"
	KeySwarmGroupPrefix                = "swarm/group/"
	KeySwarmGroupMembershipPrefix      = "swarm/group_membership/"
	KeySwarmGroupBySwarmPrefix         = "swarm/group_membership_by_swarm/"
	KeySwarmContainerProfilePrefix     = "swarm/container_profile/"
	KeySwarmLocalContainerPrefix       = "swarm/local_container/"
	KeyDeployContainerPrefix           = "deploy/container/"
	KeyRemoteDeploySessionPrefix       = "deploy/remote_session/"
	KeySwarmInvitePrefix               = "swarm/invite/"
	KeySwarmInviteTokenPrefix          = "swarm/invite_token/"
	KeySwarmEnrollmentPrefix           = "swarm/enrollment/"
	KeySwarmTrustedPeerPrefix          = "swarm/trusted_peer/"
	KeySwarmDesktopTargetCurrent       = "swarm/desktop_target/current"
	KeyNotificationPrefix              = "notification/"
	KeyNotificationBySwarmPrefix       = "notification_by_swarm/"
	KeyNotificationPermissionRefPrefix = "notification_permission_ref/"
	KeyNotificationSummaryPrefix       = "notification_summary/"
	keyGlobalSequenceCounter           = "meta/global_seq"
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

func KeySession(sessionID string) string {
	return fmt.Sprintf("session/%s", keyPart(sessionID))
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

func KeySessionLifecycle(sessionID string) string {
	return fmt.Sprintf("session_lifecycle/%s", keyPart(sessionID))
}

func SessionLifecyclePrefix() string {
	return "session_lifecycle/"
}

func KeyWorkspaceEntry(path string) string {
	return KeyWorkspaceEntryPrefix + keyPart(path)
}

func KeyWorktreeConfig(workspacePath string) string {
	return KeyWorktreeConfigPrefix + keyPart(workspacePath)
}

func WorktreeConfigPrefix() string {
	return KeyWorktreeConfigPrefix
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

func AuthCredentialTagPrefix(tag string) string {
	part := keyPart(tag)
	if part == "" {
		return KeyAuthCredentialTagPrefix
	}
	return fmt.Sprintf("%s%s/", KeyAuthCredentialTagPrefix, part)
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

func KeySessionMode(sessionID string) string {
	return fmt.Sprintf("session_mode/%s", keyPart(sessionID))
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

func PermissionSummaryPrefix(principalID string) string {
	part := keyPart(principalID)
	if part == "" {
		return "perm_summary/"
	}
	return fmt.Sprintf("perm_summary/%s/", part)
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

func KeyClientCursor(clientID, streamID string) string {
	return fmt.Sprintf("client_cursor/%s/%s", keyPart(clientID), keyPart(streamID))
}

func KeyNotification(swarmID, notificationID string) string {
	return fmt.Sprintf("%s%s/%s", KeyNotificationPrefix, keyPart(swarmID), keyPart(notificationID))
}

func NotificationPrefix(swarmID string) string {
	part := keyPart(swarmID)
	if part == "" {
		return KeyNotificationPrefix
	}
	return fmt.Sprintf("%s%s/", KeyNotificationPrefix, part)
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

func KeySwarmInvite(inviteID string) string {
	return KeySwarmInvitePrefix + keyPart(inviteID)
}

func KeySwarmContainerProfile(profileID string) string {
	return KeySwarmContainerProfilePrefix + keyPart(profileID)
}

func KeySwarmLocalContainer(containerID string) string {
	return KeySwarmLocalContainerPrefix + keyPart(containerID)
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

func DeployContainerPrefix() string {
	return KeyDeployContainerPrefix
}

func RemoteDeploySessionPrefix() string {
	return KeyRemoteDeploySessionPrefix
}

func SwarmInvitePrefix() string {
	return KeySwarmInvitePrefix
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

func keyPart(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	return url.PathEscape(clean)
}
