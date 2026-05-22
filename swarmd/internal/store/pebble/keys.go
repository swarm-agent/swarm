package pebblestore

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	KeyAuthCodexDefault                         = "auth/codex/default" // legacy single-record key; retained for migration.
	KeyAuthAttachDefault                        = "auth/attach/default"
	KeyAuthVaultMeta                            = "auth/vault/meta" // legacy singleton key; retained for explicit migration only.
	KeyAuthVaultMetaAccountPrefix               = "auth/vault/meta_by_account/"
	KeyAuthCredentialPrefix                     = "auth/credential/"
	KeyAuthCredentialActivePrefix               = "auth/credential_active/"
	KeyAuthCredentialTagPrefix                  = "auth/index/auth_tag/"
	KeyAuthManagedVaultKeyPrefix                = "auth/managed_vault_key/"
	KeyUISettingsDefault                        = "ui/settings/default"
	KeyUISettingsAccountPrefix                  = "ui/settings_by_account/"
	KeyUIChatSettingsDefault                    = "ui/chat_settings/default"
	KeyIdentityPrefix                           = "identity/"
	KeyIdentityUserPrefix                       = "identity/user/"
	KeyIdentityUserByUsernamePrefix             = "identity/user_by_username/"
	KeyIdentityAuthSubjectPrefix                = "identity/auth-subject/"
	KeyIdentityAccountScopePrefix               = "identity/account_scope/"
	KeyAccountScopePrefix                       = "account/scope/"
	KeyAccountUserPrefix                        = "account/user/"
	KeyAccountUserByUserPrefix                  = "account/user-by-user/"
	KeyIdentityTeamPrefix                       = "identity/team/"
	KeyIdentityTeamByAccountScopePrefix         = "identity/team_by_account_scope/"
	KeyIdentityTeamMembershipPrefix             = "identity/membership/"
	KeyIdentityCurrentSelectionDefault          = "identity/current_selection/default"
	KeyIdentityCurrentSelectionPrefix           = "identity/current_selection/"
	KeyIdentityLocalProductJWTSigningKeyDefault = "identity/session/local_product_jwt_signing_key"
	KeyVoiceConfigDefault                       = "voice/config/default" // legacy global key; retained for explicit migration only.
	KeyVoiceConfigAccountPrefix                 = "voice/config_by_account/"
	KeyVoiceProfilePrefix                       = "voice/profile/" // legacy global prefix; retained for explicit migration only.
	KeyVoiceProfileAccountPrefix                = "voice/profile_by_account/"
	KeyVoiceProfileActiveSTT                    = "voice/profile_active/stt"
	KeyModelPrefGlobal                          = "model_pref/global/default" // legacy single-record key; retained for explicit migration only.
	KeyModelPrefAccountPrefix                   = "model_pref/account/"
	KeyModelFavoritePrefix                      = "model_favorite/" // legacy global favorite prefix; retained for explicit migration only.
	KeyModelFavoriteAccountPrefix               = "model_favorite/account/"
	KeyWorktreeGlobalConfig                     = "worktree/global/config"
	KeyWorktreeConfigPrefix                     = "worktree/config/" // legacy single-workspace config prefix; retained for migration.
	KeyWorktreeConfigAccountPrefix              = "worktree/config_by_account/"
	KeyMCPServerPrefix                          = "mcp/server/"
	KeyWorkspaceCurrent                         = "workspace/current" // legacy global current key; retained for explicit migration only.
	KeyWorkspaceCurrentAccountPrefix            = "workspace/current_by_account/"
	KeyWorkspaceEntryPrefix                     = "workspace/entry/" // legacy global entry prefix; retained for explicit migration only.
	KeyWorkspaceEntryAccountPrefix              = "workspace/entry_by_account/"
	KeyWorkspaceTodoItemPrefix                  = "workspace_todo/item/"
	KeyVideoThreadPrefix                        = "video/thread/"
	KeyImageThreadPrefix                        = "image/thread/" // legacy global image thread prefix; retained for explicit migration only.
	KeyImageThreadAccountPrefix                 = "image/thread_by_account/"
	KeyModelCatalogMeta                         = "model_catalog/meta"
	KeyAgentProfilePrefix                       = "agent/profile/"
	KeyAgentCustomToolPrefix                    = "agent/custom_tool/"
	KeyAgentCustomToolAccountPrefix             = "agent/custom_tool_by_account/"
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
	KeySwarmContainerProfileByAccountPrefix     = "swarm/container_profile_by_account/"
	KeySwarmLocalContainerPrefix                = "swarm/local_container/"
	KeySwarmLocalContainerByAccountPrefix       = "swarm/local_container_by_account/"
	KeySwarmNodePrefix                          = "swarm/node/"
	KeyDeployContainerPrefix                    = "deploy/container/"
	KeyDeployContainerByAccountPrefix           = "deploy/container_by_account/"
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
	KeyNotificationByAccountSwarmPrefix         = "notification_by_account_swarm/"
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

func KeyModelPreferenceForAccount(accountScopeID string) string {
	return KeyModelPrefAccountPrefix + keyPart(accountScopeID)
}

func KeyModelFavorite(providerID, modelID string) string {
	return fmt.Sprintf("%s%s/%s", KeyModelFavoritePrefix, keyPart(providerID), keyPart(modelID))
}

func KeyModelFavoriteForAccount(accountScopeID, providerID, modelID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyModelFavoriteAccountPrefix, keyPart(accountScopeID), keyPart(providerID), keyPart(modelID))
}

func ModelFavoritePrefix(providerID string) string {
	providerPart := keyPart(providerID)
	if providerPart == "" {
		return KeyModelFavoritePrefix
	}
	return fmt.Sprintf("%s%s/", KeyModelFavoritePrefix, providerPart)
}

func ModelFavoritePrefixForAccount(accountScopeID, providerID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyModelFavoriteAccountPrefix
	}
	providerPart := keyPart(providerID)
	if providerPart == "" {
		return fmt.Sprintf("%s%s/", KeyModelFavoriteAccountPrefix, accountPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyModelFavoriteAccountPrefix, accountPart, providerPart)
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

func KeyIdentityAuthSubject(provider, subject string) string {
	return fmt.Sprintf("%s%s/%s", KeyIdentityAuthSubjectPrefix, keyPart(provider), keyPart(subject))
}

func IdentityAuthSubjectPrefix(provider string) string {
	providerPart := keyPart(provider)
	if providerPart == "" {
		return KeyIdentityAuthSubjectPrefix
	}
	return fmt.Sprintf("%s%s/", KeyIdentityAuthSubjectPrefix, providerPart)
}

func KeyIdentityAccountScope(accountScopeID string) string {
	return KeyIdentityAccountScopePrefix + keyPart(accountScopeID)
}

func KeyAccountScope(accountScopeID string) string {
	return KeyAccountScopePrefix + keyPart(accountScopeID)
}

func IdentityAccountScopePrefix() string {
	return KeyIdentityAccountScopePrefix
}

func AccountScopePrefix() string {
	return KeyAccountScopePrefix
}

func KeyAccountUser(accountScopeID, userID string) string {
	return fmt.Sprintf("%s%s/%s", KeyAccountUserPrefix, keyPart(accountScopeID), keyPart(userID))
}

func AccountUserPrefix(accountScopeID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyAccountUserPrefix
	}
	return fmt.Sprintf("%s%s/", KeyAccountUserPrefix, accountPart)
}

func KeyAccountUserByUser(userID, accountScopeID string) string {
	return fmt.Sprintf("%s%s/%s", KeyAccountUserByUserPrefix, keyPart(userID), keyPart(accountScopeID))
}

func AccountUserByUserPrefix(userID string) string {
	userPart := keyPart(userID)
	if userPart == "" {
		return KeyAccountUserByUserPrefix
	}
	return fmt.Sprintf("%s%s/", KeyAccountUserByUserPrefix, userPart)
}

func KeyIdentityTeam(teamID string) string {
	return KeyIdentityTeamPrefix + keyPart(teamID)
}

func IdentityTeamPrefix() string {
	return KeyIdentityTeamPrefix
}

func KeyIdentityTeamByAccountScope(accountScopeID string) string {
	return KeyIdentityTeamByAccountScopePrefix + keyPart(accountScopeID)
}

func IdentityTeamByAccountScopePrefix() string {
	return KeyIdentityTeamByAccountScopePrefix
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

func KeySessionByAccount(accountScopeID, sessionID string) string {
	return fmt.Sprintf("session_by_account/%s/%s", keyPart(accountScopeID), keyPart(sessionID))
}

func SessionByAccountPrefix(accountScopeID string) string {
	part := keyPart(accountScopeID)
	if part == "" {
		return "session_by_account/"
	}
	return fmt.Sprintf("session_by_account/%s/", part)
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

func KeyWorkspaceEntryForAccount(accountScopeID, path string) string {
	return fmt.Sprintf("%s%s/%s", KeyWorkspaceEntryAccountPrefix, keyPart(accountScopeID), keyPart(path))
}

func WorkspaceEntryPrefixForAccount(accountScopeID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyWorkspaceEntryAccountPrefix
	}
	return fmt.Sprintf("%s%s/", KeyWorkspaceEntryAccountPrefix, accountPart)
}

func KeyWorkspaceCurrentForAccount(accountScopeID, userID string) string {
	return fmt.Sprintf("%s%s/%s", KeyWorkspaceCurrentAccountPrefix, keyPart(accountScopeID), keyPart(userID))
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

func KeyImageThreadForAccount(accountScopeID, threadID string) string {
	return fmt.Sprintf("%s%s/%s", KeyImageThreadAccountPrefix, keyPart(accountScopeID), keyPart(threadID))
}

func ImageThreadPrefix() string {
	return KeyImageThreadPrefix
}

func ImageThreadPrefixForAccount(accountScopeID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyImageThreadAccountPrefix
	}
	return fmt.Sprintf("%s%s/", KeyImageThreadAccountPrefix, accountPart)
}

func KeyWorktreeConfig(workspacePath string) string {
	return KeyWorktreeConfigPrefix + keyPart(workspacePath)
}

func KeyWorktreeConfigForAccount(accountScopeID, workspacePath string) string {
	return fmt.Sprintf("%s%s/%s", KeyWorktreeConfigAccountPrefix, keyPart(accountScopeID), keyPart(workspacePath))
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

func KeyAuthVaultMetaForAccount(accountScopeID string) string {
	return KeyAuthVaultMetaAccountPrefix + keyPart(accountScopeID)
}

func KeyAuthCredentialForAccount(accountScopeID, providerID, credentialID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyAuthCredentialPrefix, keyPart(accountScopeID), keyPart(providerID), keyPart(credentialID))
}

func AuthCredentialPrefix() string {
	return KeyAuthCredentialPrefix
}

func AuthCredentialAccountPrefix(accountScopeID string) string {
	part := keyPart(accountScopeID)
	if part == "" {
		return KeyAuthCredentialPrefix
	}
	return fmt.Sprintf("%s%s/", KeyAuthCredentialPrefix, part)
}

func AuthCredentialProviderPrefix(providerID string) string {
	part := keyPart(providerID)
	if part == "" {
		return KeyAuthCredentialPrefix
	}
	return fmt.Sprintf("%s%s/", KeyAuthCredentialPrefix, part)
}

func AuthCredentialProviderPrefixForAccount(accountScopeID, providerID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyAuthCredentialPrefix
	}
	providerPart := keyPart(providerID)
	if providerPart == "" {
		return fmt.Sprintf("%s%s/", KeyAuthCredentialPrefix, accountPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyAuthCredentialPrefix, accountPart, providerPart)
}

func KeyAuthCredentialActive(providerID string) string {
	return KeyAuthCredentialActivePrefix + keyPart(providerID)
}

func KeyAuthCredentialActiveForAccount(accountScopeID, providerID string) string {
	return fmt.Sprintf("%s%s/%s", KeyAuthCredentialActivePrefix, keyPart(accountScopeID), keyPart(providerID))
}

func KeyVoiceConfigForAccount(accountScopeID string) string {
	return KeyVoiceConfigAccountPrefix + keyPart(accountScopeID)
}

func KeyVoiceProfile(profileID string) string {
	return KeyVoiceProfilePrefix + keyPart(profileID)
}

func KeyVoiceProfileForAccount(accountScopeID, profileID string) string {
	return fmt.Sprintf("%s%s/%s", KeyVoiceProfileAccountPrefix, keyPart(accountScopeID), keyPart(profileID))
}

func VoiceProfilePrefix() string {
	return KeyVoiceProfilePrefix
}

func VoiceProfilePrefixForAccount(accountScopeID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyVoiceProfileAccountPrefix
	}
	return fmt.Sprintf("%s%s/", KeyVoiceProfileAccountPrefix, accountPart)
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

func KeyAuthCredentialTagForAccount(accountScopeID, tag, providerID, credentialID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s", KeyAuthCredentialTagPrefix, keyPart(accountScopeID), keyPart(tag), keyPart(providerID), keyPart(credentialID))
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

func KeyMessageByAccount(accountScopeID, sessionID string, globalSeq uint64) string {
	return fmt.Sprintf("msg_by_account/%s/%s/%020d", keyPart(accountScopeID), keyPart(sessionID), globalSeq)
}

func MessageByAccountPrefix(accountScopeID, sessionID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return "msg_by_account/"
	}
	sessionPart := keyPart(sessionID)
	if sessionPart == "" {
		return fmt.Sprintf("msg_by_account/%s/", accountPart)
	}
	return fmt.Sprintf("msg_by_account/%s/%s/", accountPart, sessionPart)
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

func KeySessionPlanByAccount(accountScopeID, sessionID, planID string) string {
	return fmt.Sprintf("session_plan_by_account/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(planID))
}

func SessionPlanByAccountPrefix(accountScopeID, sessionID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return "session_plan_by_account/"
	}
	sessionPart := keyPart(sessionID)
	if sessionPart == "" {
		return fmt.Sprintf("session_plan_by_account/%s/", accountPart)
	}
	return fmt.Sprintf("session_plan_by_account/%s/%s/", accountPart, sessionPart)
}

func KeySessionPlanActive(sessionID string) string {
	return fmt.Sprintf("session_plan_active/%s", keyPart(sessionID))
}

func KeySessionPlanActiveByAccount(accountScopeID, sessionID string) string {
	return fmt.Sprintf("session_plan_active_by_account/%s/%s", keyPart(accountScopeID), keyPart(sessionID))
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

func KeySessionTurnUsageByAccount(accountScopeID, sessionID, runID string) string {
	return fmt.Sprintf("session_turn_usage_by_account/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(runID))
}

func SessionTurnUsageByAccountPrefix(accountScopeID, sessionID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return "session_turn_usage_by_account/"
	}
	sessionPart := keyPart(sessionID)
	if sessionPart == "" {
		return fmt.Sprintf("session_turn_usage_by_account/%s/", accountPart)
	}
	return fmt.Sprintf("session_turn_usage_by_account/%s/%s/", accountPart, sessionPart)
}

func KeySessionUsageSummary(sessionID string) string {
	return fmt.Sprintf("session_usage_summary/%s", keyPart(sessionID))
}

func KeySessionUsageSummaryByAccount(accountScopeID, sessionID string) string {
	return fmt.Sprintf("session_usage_summary_by_account/%s/%s", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionLifecycleByAccount(accountScopeID, sessionID string) string {
	return fmt.Sprintf("session_lifecycle_by_account/%s/%s", keyPart(accountScopeID), keyPart(sessionID))
}

func SessionLifecycleByAccountPrefix(accountScopeID string) string {
	part := keyPart(accountScopeID)
	if part == "" {
		return "session_lifecycle_by_account/"
	}
	return fmt.Sprintf("session_lifecycle_by_account/%s/", part)
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

func KeyUISettingsForAccount(accountScopeID string) string {
	return KeyUISettingsAccountPrefix + keyPart(accountScopeID)
}

func KeyNotification(swarmID, notificationID string) string {
	return fmt.Sprintf("%s%s/%s", KeyNotificationPrefix, keyPart(swarmID), keyPart(notificationID))
}

func KeyNotificationForAccount(accountScopeID, swarmID, notificationID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyNotificationPrefix, keyPart(accountScopeID), keyPart(swarmID), keyPart(notificationID))
}

func KeyNotificationBySwarm(swarmID string, createdAt int64, notificationID string) string {
	return fmt.Sprintf("%s%s/%020d/%s", KeyNotificationBySwarmPrefix, keyPart(swarmID), createdAt, keyPart(notificationID))
}

func KeyNotificationByAccountSwarm(accountScopeID, swarmID string, createdAt int64, notificationID string) string {
	return fmt.Sprintf("%s%s/%s/%020d/%s", KeyNotificationByAccountSwarmPrefix, keyPart(accountScopeID), keyPart(swarmID), createdAt, keyPart(notificationID))
}

func NotificationBySwarmPrefix(swarmID string) string {
	part := keyPart(swarmID)
	if part == "" {
		return KeyNotificationBySwarmPrefix
	}
	return fmt.Sprintf("%s%s/", KeyNotificationBySwarmPrefix, part)
}

func NotificationByAccountSwarmPrefix(accountScopeID, swarmID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyNotificationByAccountSwarmPrefix
	}
	swarmPart := keyPart(swarmID)
	if swarmPart == "" {
		return fmt.Sprintf("%s%s/", KeyNotificationByAccountSwarmPrefix, accountPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyNotificationByAccountSwarmPrefix, accountPart, swarmPart)
}

func KeyNotificationPermissionRef(sessionID, permissionID string) string {
	return fmt.Sprintf("%s%s/%s", KeyNotificationPermissionRefPrefix, keyPart(sessionID), keyPart(permissionID))
}

func KeyNotificationPermissionRefForAccount(accountScopeID, sessionID, permissionID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyNotificationPermissionRefPrefix, keyPart(accountScopeID), keyPart(sessionID), keyPart(permissionID))
}

func KeyNotificationSummary(swarmID string) string {
	return KeyNotificationSummaryPrefix + keyPart(swarmID)
}

func KeyNotificationSummaryForAccount(accountScopeID, swarmID string) string {
	return fmt.Sprintf("%s%s/%s", KeyNotificationSummaryPrefix, keyPart(accountScopeID), keyPart(swarmID))
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

func KeyAgentCustomToolForAccount(accountScopeID, name string) string {
	return fmt.Sprintf("%s%s/%s", KeyAgentCustomToolAccountPrefix, keyPart(accountScopeID), keyPart(name))
}

func AgentProfilePrefix() string {
	return KeyAgentProfilePrefix
}

func AgentCustomToolPrefix() string {
	return KeyAgentCustomToolPrefix
}

func AgentCustomToolPrefixForAccount(accountScopeID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyAgentCustomToolAccountPrefix
	}
	return fmt.Sprintf("%s%s/", KeyAgentCustomToolAccountPrefix, accountPart)
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

func KeyIntegrationPackForAccount(accountScopeID, packID string) string {
	return fmt.Sprintf("%s%s/%s", KeyIntegrationPackPrefix, keyPart(accountScopeID), keyPart(packID))
}

func IntegrationPackPrefixForAccount(accountScopeID string) string {
	part := keyPart(accountScopeID)
	if part == "" {
		return KeyIntegrationPackPrefix
	}
	return fmt.Sprintf("%s%s/", KeyIntegrationPackPrefix, part)
}

func KeyIntegrationPackVersionForAccount(accountScopeID, packID, versionID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyIntegrationPackVersionPrefix, keyPart(accountScopeID), keyPart(packID), keyPart(versionID))
}

func IntegrationPackVersionPrefixForAccount(accountScopeID, packID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyIntegrationPackVersionPrefix
	}
	packPart := keyPart(packID)
	if packPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationPackVersionPrefix, accountPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyIntegrationPackVersionPrefix, accountPart, packPart)
}

func KeyIntegrationToolForAccount(accountScopeID, packID, versionID, toolID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s", KeyIntegrationToolPrefix, keyPart(accountScopeID), keyPart(packID), keyPart(versionID), keyPart(toolID))
}

func IntegrationToolPrefixForAccount(accountScopeID, packID, versionID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyIntegrationToolPrefix
	}
	packPart := keyPart(packID)
	if packPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationToolPrefix, accountPart)
	}
	versionPart := keyPart(versionID)
	if versionPart == "" {
		return fmt.Sprintf("%s%s/%s/", KeyIntegrationToolPrefix, accountPart, packPart)
	}
	return fmt.Sprintf("%s%s/%s/%s/", KeyIntegrationToolPrefix, accountPart, packPart, versionPart)
}

func KeyIntegrationAdapterForAccount(accountScopeID, packID, versionID, adapterID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s", KeyIntegrationAdapterPrefix, keyPart(accountScopeID), keyPart(packID), keyPart(versionID), keyPart(adapterID))
}

func IntegrationAdapterPrefixForAccount(accountScopeID, packID, versionID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyIntegrationAdapterPrefix
	}
	packPart := keyPart(packID)
	if packPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationAdapterPrefix, accountPart)
	}
	versionPart := keyPart(versionID)
	if versionPart == "" {
		return fmt.Sprintf("%s%s/%s/", KeyIntegrationAdapterPrefix, accountPart, packPart)
	}
	return fmt.Sprintf("%s%s/%s/%s/", KeyIntegrationAdapterPrefix, accountPart, packPart, versionPart)
}

func KeyIntegrationPromptFragmentForAccount(accountScopeID, packID, versionID, fragmentID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s", KeyIntegrationPromptFragmentPrefix, keyPart(accountScopeID), keyPart(packID), keyPart(versionID), keyPart(fragmentID))
}

func IntegrationPromptFragmentPrefixForAccount(accountScopeID, packID, versionID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyIntegrationPromptFragmentPrefix
	}
	packPart := keyPart(packID)
	if packPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationPromptFragmentPrefix, accountPart)
	}
	versionPart := keyPart(versionID)
	if versionPart == "" {
		return fmt.Sprintf("%s%s/%s/", KeyIntegrationPromptFragmentPrefix, accountPart, packPart)
	}
	return fmt.Sprintf("%s%s/%s/%s/", KeyIntegrationPromptFragmentPrefix, accountPart, packPart, versionPart)
}

func KeyIntegrationAssignmentForAccount(accountScopeID, assignmentID string) string {
	return fmt.Sprintf("%s%s/%s", KeyIntegrationAssignmentPrefix, keyPart(accountScopeID), keyPart(assignmentID))
}

func IntegrationAssignmentPrefixForAccount(accountScopeID string) string {
	part := keyPart(accountScopeID)
	if part == "" {
		return KeyIntegrationAssignmentPrefix
	}
	return fmt.Sprintf("%s%s/", KeyIntegrationAssignmentPrefix, part)
}

func KeyIntegrationAssignmentByAgentForAccount(accountScopeID, agentName, assignmentID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyIntegrationAssignmentAgentPrefix, keyPart(accountScopeID), keyPart(agentName), keyPart(assignmentID))
}

func IntegrationAssignmentByAgentPrefixForAccount(accountScopeID, agentName string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyIntegrationAssignmentAgentPrefix
	}
	agentPart := keyPart(agentName)
	if agentPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationAssignmentAgentPrefix, accountPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyIntegrationAssignmentAgentPrefix, accountPart, agentPart)
}

func KeyIntegrationAssignmentByPackForAccount(accountScopeID, packID, versionID, assignmentID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s", KeyIntegrationAssignmentPackPrefix, keyPart(accountScopeID), keyPart(packID), keyPart(versionID), keyPart(assignmentID))
}

func IntegrationAssignmentByPackPrefixForAccount(accountScopeID, packID, versionID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyIntegrationAssignmentPackPrefix
	}
	packPart := keyPart(packID)
	if packPart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationAssignmentPackPrefix, accountPart)
	}
	versionPart := keyPart(versionID)
	if versionPart == "" {
		return fmt.Sprintf("%s%s/%s/", KeyIntegrationAssignmentPackPrefix, accountPart, packPart)
	}
	return fmt.Sprintf("%s%s/%s/%s/", KeyIntegrationAssignmentPackPrefix, accountPart, packPart, versionPart)
}

func KeyIntegrationWorkspaceForAccount(accountScopeID, workspaceID string) string {
	return fmt.Sprintf("%s%s/%s", KeyIntegrationWorkspacePrefix, keyPart(accountScopeID), keyPart(workspaceID))
}

func IntegrationWorkspacePrefixForAccount(accountScopeID string) string {
	part := keyPart(accountScopeID)
	if part == "" {
		return KeyIntegrationWorkspacePrefix
	}
	return fmt.Sprintf("%s%s/", KeyIntegrationWorkspacePrefix, part)
}

func KeyIntegrationWorkspaceSessionForAccount(accountScopeID, workspaceID, sessionID string) string {
	return fmt.Sprintf("%s%s/%s/%s", KeyIntegrationWorkspaceSessionPrefix, keyPart(accountScopeID), keyPart(workspaceID), keyPart(sessionID))
}

func IntegrationWorkspaceSessionPrefixForAccount(accountScopeID, workspaceID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyIntegrationWorkspaceSessionPrefix
	}
	workspacePart := keyPart(workspaceID)
	if workspacePart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationWorkspaceSessionPrefix, accountPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyIntegrationWorkspaceSessionPrefix, accountPart, workspacePart)
}

func KeyIntegrationWorkspaceSessionUpdatedForAccount(accountScopeID, workspaceID string, updatedAt int64, sessionID string) string {
	return fmt.Sprintf("%s%s/%s/%020d/%s", KeyIntegrationWorkspaceSessionUpdatedPrefix, keyPart(accountScopeID), keyPart(workspaceID), reverseMillis(updatedAt), keyPart(sessionID))
}

func IntegrationWorkspaceSessionUpdatedPrefixForAccount(accountScopeID, workspaceID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyIntegrationWorkspaceSessionUpdatedPrefix
	}
	workspacePart := keyPart(workspaceID)
	if workspacePart == "" {
		return fmt.Sprintf("%s%s/", KeyIntegrationWorkspaceSessionUpdatedPrefix, accountPart)
	}
	return fmt.Sprintf("%s%s/%s/", KeyIntegrationWorkspaceSessionUpdatedPrefix, accountPart, workspacePart)
}

func KeySwarmInvite(inviteID string) string {
	return KeySwarmInvitePrefix + keyPart(inviteID)
}

func KeySwarmContainerProfile(profileID string) string {
	return KeySwarmContainerProfilePrefix + keyPart(profileID)
}

func KeySwarmContainerProfileByAccount(accountScopeID, profileID string) string {
	return fmt.Sprintf("%s%s/%s", KeySwarmContainerProfileByAccountPrefix, keyPart(accountScopeID), keyPart(profileID))
}

func KeySwarmLocalContainer(containerID string) string {
	return KeySwarmLocalContainerPrefix + keyPart(containerID)
}

func KeySwarmLocalContainerByAccount(accountScopeID, containerID string) string {
	return fmt.Sprintf("%s%s/%s", KeySwarmLocalContainerByAccountPrefix, keyPart(accountScopeID), keyPart(containerID))
}

func KeySwarmNode(swarmID string) string {
	return KeySwarmNodePrefix + keyPart(swarmID)
}

func KeyDeployContainer(deploymentID string) string {
	return KeyDeployContainerPrefix + keyPart(deploymentID)
}

func KeyDeployContainerByAccount(accountScopeID, deploymentID string) string {
	return fmt.Sprintf("%s%s/%s", KeyDeployContainerByAccountPrefix, keyPart(accountScopeID), keyPart(deploymentID))
}

func KeyRemoteDeploySession(sessionID string) string {
	return KeyRemoteDeploySessionPrefix + keyPart(sessionID)
}

func SwarmContainerProfilePrefix() string {
	return KeySwarmContainerProfilePrefix
}

func SwarmContainerProfileByAccountPrefix(accountScopeID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeySwarmContainerProfileByAccountPrefix
	}
	return fmt.Sprintf("%s%s/", KeySwarmContainerProfileByAccountPrefix, accountPart)
}

func SwarmLocalContainerPrefix() string {
	return KeySwarmLocalContainerPrefix
}

func SwarmLocalContainerByAccountPrefix(accountScopeID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeySwarmLocalContainerByAccountPrefix
	}
	return fmt.Sprintf("%s%s/", KeySwarmLocalContainerByAccountPrefix, accountPart)
}

func SwarmNodePrefix() string {
	return KeySwarmNodePrefix
}

func DeployContainerPrefix() string {
	return KeyDeployContainerPrefix
}

func DeployContainerByAccountPrefix(accountScopeID string) string {
	accountPart := keyPart(accountScopeID)
	if accountPart == "" {
		return KeyDeployContainerByAccountPrefix
	}
	return fmt.Sprintf("%s%s/", KeyDeployContainerByAccountPrefix, accountPart)
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
