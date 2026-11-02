package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// This file holds the completeness side of the MCP / route parity guard:
// every home-reach MCP action and every registered route is either compared
// or listed with the reason it is not, so a new action or route fails until
// it is placed. A change to a route or MCP action must keep these tests
// green; an allowlist entry needs a reason (AGENTS.md).

// mcpHomeRouteSpecs bind the routes of home-reach actions (they run on this
// machine only, through the catalog handler in-process) that this test
// compares with them. They are not reached over the link, so
// mcpRouteSpecs' stale check does not cover them;
// TestMCPRouteParityHomeActions does.
var mcpHomeRouteSpecs = map[string]mcpRouteSpec{
	"POST /v3/projects":       {Func: "handleProjects", Anchors: []string{`=trimmed == ""`, `=r.Method == http.MethodPost`}},
	"PATCH /v3/projects/{id}": {Func: "handleProjects", Anchors: []string{`=len(segments) == 1`, `=r.Method == http.MethodPatch`}},
}

// mcpHomeAction is how a home-reach MCP action relates to an HTTP route: the
// route shape it is compared with (Path: inputs carried in the path), or why
// it is not compared.
type mcpHomeAction struct {
	Shape  string
	Path   []string
	Reason string
}

// mcpHomeTODO exempts a home action whose HTTP counterpart is not compared
// yet.
const mcpHomeTODO = "TODO: home-only; Swarm Control and the Orchestrator run the catalog handler in-process; a route it dispatches to is placed in mcpControlRoutes, but this action/route pair is not compared yet"

// mcpHomeActions lists every home-reach action Swarm Control serves. An
// action only the Orchestrator lists (not ControlCallable) has no MCP
// surface and needs no entry.
var mcpHomeActions = map[string]mcpHomeAction{
	"manage_projects create":            {Shape: "POST /v3/projects"},
	"manage_projects update":            {Shape: "PATCH /v3/projects/{id}", Path: []string{"id", "project_id"}},
	"manage_projects update_task":       {Reason: "TODO: the task PATCH route is the Desktop's record patch (status, plan and delivery facts) and reads none of update_task's priority, group, order or expected_revision; update_task runs its own service in-process, so they are not one operation to compare"},
	"manage_projects flow":              {Reason: mcpHomeTODO},
	"manage_projects list_machines":     {Reason: "reads this home's own linked-machine records (the fleet store); no project route"},
	"manage_projects list_linked_tasks": {Reason: "TODO: reads this home's own records of tasks it sent to linked machines; the home's linked-tasks route is not compared"},
	"manage_projects integrate_task":    {Reason: mcpHomeTODO},
	"manage_projects unarchive_task":    {Reason: mcpHomeTODO},
	"manage_projects delete_task":       {Reason: mcpHomeTODO},
	"manage_projects delete":            {Reason: mcpHomeTODO},
	"manage_workflows help":             {Reason: "static guide text: no inputs and no route"},
	"manage_setup permissions":          {Reason: "compared with GET /v3/permissions, the route it dispatches to, in mcpControlRoutes"},
	"manage_setup list_keys":            {Reason: mcpHomeTODO},
	"manage_setup list_fleet_keys":      {Reason: mcpHomeTODO},
	"manage_setup list_secrets":         {Reason: mcpHomeTODO},
	"manage_setup set_permissions":      {Reason: "compared with PATCH /v3/permissions, the route it dispatches to, in mcpControlRoutes"},
	"manage_setup create_key":           {Reason: mcpHomeTODO},
	"manage_setup create_client_key":    {Reason: mcpHomeTODO},
	"manage_setup revoke_key":           {Reason: mcpHomeTODO},
	"manage_setup request_fleet_key":    {Reason: mcpHomeTODO},
	"manage_setup revoke_fleet_key":     {Reason: mcpHomeTODO},
	"manage_setup request_provider_key": {Reason: mcpHomeTODO},
	"manage_setup connect_chatgpt":      {Reason: mcpHomeTODO},
	"manage_setup connect_claude_code":  {Reason: mcpHomeTODO},
	"manage_fleet list_machines":        {Reason: "reads this home's own linked-machine records (the fleet store); no project route"},
	"manage_fleet list_linked_tasks":    {Reason: "TODO: reads this home's own records of tasks it sent to linked machines; the home's linked-tasks route is not compared"},
	"manage_custom_agents list":         {Reason: mcpHomeTODO},
	"manage_custom_agents get":          {Reason: mcpHomeTODO},
	"manage_custom_agents define":       {Reason: mcpHomeTODO},
	"manage_custom_agents disable":      {Reason: mcpHomeTODO},
	"manage_themes get":                 {Reason: mcpHomeTODO},
}

// mcpHomeRouteParityAllow are known differences between a compared home
// action and its route, by "tool action field kind" (input_unread: the
// action declares an input the route does not read; route_undeclared: the
// route reads a field the action does not declare). Each needs its reason.
var mcpHomeRouteParityAllow = map[string]string{
	"manage_projects create account_id route_undeclared":          "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create created_at route_undeclared":          "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create updated_at route_undeclared":          "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create creation_hash route_undeclared":       "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create creation_request_id route_undeclared": "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create id route_undeclared":                  "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create context_generation route_undeclared":  "the route decodes the whole project record (pebblestore.ProjectRecord); a record field, not a create input",
	"manage_projects create active_task_ids route_undeclared":     "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create automation_ids route_undeclared":      "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create theme_id route_undeclared":            "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create icon_png_data_url route_undeclared":   "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create uploaded_media route_undeclared":      "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects create primary_session_id route_undeclared":  "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update active_task_ids route_undeclared":     "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update automation_ids route_undeclared":      "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update theme_id route_undeclared":            "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update icon_png_data_url route_undeclared":   "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update primary_session_id route_undeclared":  "TODO: a Desktop project-form field (theme, icon, media, primary session, links) the MCP action does not offer",
	"manage_projects update project_md input_unread":              "TODO: update writes project_md through its own in-process service; the project PATCH route does not take it",
}

// Purpose:
//   - Requirement: every home-reach action Swarm Control serves is either
//     compared with the HTTP route for the same operation (its declared
//     inputs against the fields the route's source reads, both ways) or
//     listed with the reason it is not; a new home action without an entry
//     fails.
//   - Threat/regression: an MCP action and the API route for the same
//     operation drifting apart (a field one takes and the other ignores) on
//     actions that never cross the link, which the linked forwarding test
//     cannot see.
//   - Boundary/authority: tool.Catalog (Reach, ControlCallable, Inputs)
//     and the route handler source (mcpHomeRouteSpecs).
//   - Narrowest layer: declarations and handler source; no daemon runs.
func TestMCPRouteParityHomeActions(t *testing.T) {
	var drift []string
	matched := map[string]bool{}
	note := func(id, field, kind, detail string) {
		key := id + " " + field + " " + kind
		if _, ok := mcpHomeRouteParityAllow[key]; ok {
			matched[key] = true
			return
		}
		drift = append(drift, fmt.Sprintf("%q: %s", key, detail))
	}
	seen := map[string]bool{}
	usedShapes := map[string]bool{}
	for _, catalog := range tool.Catalog() {
		for _, action := range catalog.Actions {
			if action.Reach != tool.CatalogReachHome || !action.ControlCallable() {
				continue
			}
			id := catalog.Name + " " + action.Action
			seen[id] = true
			entry, ok := mcpHomeActions[id]
			switch {
			case !ok:
				drift = append(drift, id+" is a home-reach MCP action with no mcpHomeActions entry: compare it with its route (Shape) or give the reason it has none")
				continue
			case entry.Shape == "" && strings.TrimSpace(entry.Reason) == "":
				drift = append(drift, id+": an mcpHomeActions entry needs a Shape or a Reason")
				continue
			case entry.Shape == "":
				continue
			}
			usedShapes[entry.Shape] = true
			_, fields, found, err := mcpRouteAccepts(entry.Shape)
			if !found || err != nil {
				drift = append(drift, fmt.Sprintf("%s: route %s: found=%v err=%v", id, entry.Shape, found, err))
				continue
			}
			if len(fields.Unresolved) > 0 {
				drift = append(drift, fmt.Sprintf("%s: route %s: %s", id, entry.Shape, strings.Join(fields.Unresolved, "; ")))
			}
			accepted := map[string]bool{}
			for key := range fields.Query {
				accepted[key] = true
			}
			for key := range fields.Body {
				accepted[key] = true
			}
			declared := map[string]bool{}
			for _, name := range mcpParityInputs(action) {
				declared[name] = true
				if slices.Contains(entry.Path, name) {
					continue
				}
				if !accepted[name] {
					note(id, name, "input_unread", entry.Shape+" does not read "+name)
				}
			}
			for _, key := range mcpParitySortedKeys(accepted) {
				if !declared[key] {
					note(id, key, "route_undeclared", entry.Shape+" reads "+key+", which the action does not declare")
				}
			}
		}
	}
	for _, id := range mcpParitySortedKeys(mcpHomeActions) {
		if !seen[id] {
			drift = append(drift, "mcpHomeActions lists "+id+", which is not a home-reach action Swarm Control serves: remove it")
		}
	}
	for _, shape := range mcpParitySortedKeys(mcpHomeRouteSpecs) {
		if !usedShapes[shape] {
			drift = append(drift, "mcpHomeRouteSpecs entry "+shape+" is compared with no action: remove it")
		}
	}
	for key := range mcpHomeRouteParityAllow {
		if !matched[key] {
			drift = append(drift, fmt.Sprintf("mcpHomeRouteParityAllow entry %q no longer matches: remove it", key))
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("home MCP action / HTTP route drift (%d):\n%s", len(drift), strings.Join(drift, "\n"))
	}
}

// mcpRouteParityReached are the route registrations MCP actions reach over
// HTTP, with how: their shapes are in mcpRouteSpecs or mcpHomeRouteSpecs
// (handler source compared) or they serve catalog tools
// (mcpCatalogRoutePrefixes: serveCatalogLinked takes exactly the declared
// inputs).
var mcpRouteParityReached = map[string]string{
	`ProjectsPath -> s.handleProjects`:                   "manage_projects list on a linked machine; mcpRouteSpecs",
	`ProjectsPath + "/" -> s.handleProjects`:             "manage_projects project and task routes; mcpRouteSpecs and mcpHomeRouteSpecs",
	`JobAutomationsPath -> s.handleJobAutomations`:       "manage_workflows list on a linked machine; mcpRouteSpecs",
	`JobAutomationsPath + "/" -> s.handleJobAutomations`: "manage_workflows routes on a linked machine; mcpRouteSpecs",
	`JobDeliveriesPath -> s.handleJobDeliveries`:         "manage_workflows deliveries on a linked machine; mcpRouteSpecs",
	`JobDeliveriesPath + "/" -> s.handleJobDeliveries`:   "registered with the collection; a linked deliveries call reads the collection or one delivery through the workflows route",
	`CatalogActionsPath + "/" -> s.handleCatalogActions`: "catalog tools on a linked machine (serveCatalogLinked)",
	`CatalogLinkedPath + "/" -> s.handleCatalogLinked`:   "catalog tools on a linked machine (serveCatalogLinked)",
	`SetupActionsPath + "/" -> s.handleSetupActions`:     "manage_setup on a linked machine (serveCatalogLinked)",
	`WorkerActionsPath + "/" -> s.handleWorkerActions`:   "manage_workers on a linked machine (serveCatalogLinked)",
	`JobsPath + "/" -> s.handleJobs`:                     "manage_jobs on a linked machine (the catalog-linked jobs route)",
	`WorkersPath -> s.handleWorkers`:                     "Swarm Control's job and worker functions (GET and POST v3/workers in controlMCPRoutes; mcpControlRoutes)",
	`WorkersPath + "/" -> s.handleWorkers`:               "Swarm Control's worker functions (mcpControlRoutes); TODO: a linked worker list reads WorkersPath/{id}/runs (limit, cursor) for its summary (compactFleetWorkers); that page is not derived",
	// Reached in-process by Swarm Control's catalog actions through c.dispatch
	// (controlMCPRoutes); each allowlist entry is placed in mcpControlRoutes,
	// and TestMCPRouteParityControlRoutes derives which registration serves it.
	`"/v1/auth/codex/oauth/start" -> s.ownerOnlyScope(s.handleCodexOAuthStart)`:   "Swarm Control; mcpControlRoutes",
	`"/v1/auth/codex/oauth/status" -> s.ownerOnlyScope(s.handleCodexOAuthStatus)`: "Swarm Control; mcpControlRoutes",
	`"/v1/auth/credentials" -> s.ownerOnlyScope(s.handleAuthCredentials)`:         "Swarm Control; mcpControlRoutes",
	`"/v1/claudecode/usage" -> s.handleClaudeCodeUsage`:                           "Swarm Control; mcpControlRoutes",
	`"/v1/codex/account/usage" -> s.handleCodexAccountUsage`:                      "Swarm Control; mcpControlRoutes",
	`"/v1/model/catalog" -> s.handleModelCatalog`:                                 "Swarm Control; mcpControlRoutes",
	`"/v1/providers" -> s.handleProviders`:                                        "Swarm Control; mcpControlRoutes",
	`"/v1/ui/settings" -> s.handleUISettings`:                                     "Swarm Control; mcpControlRoutes",
	`"/v1/workspace/add" -> s.handleWorkspaceAdd`:                                 "Swarm Control; mcpControlRoutes",
	`"/v1/workspace/list" -> s.handleWorkspaceList`:                               "Swarm Control; mcpControlRoutes",
	`"/v1/workspace/repository/setup" -> s.handleWorkspaceRepositorySetup`:        "Swarm Control; mcpControlRoutes",
	`"/v2/agents" -> s.agentConfigScope(s.handleAgentsV2)`:                        "Swarm Control; mcpControlRoutes",
	`"/v2/agents/" -> s.agentConfigScope(s.handleAgentByNameV2)`:                  "Swarm Control; mcpControlRoutes",
	`"/v3/activity" -> s.handleActivity`:                                          "Swarm Control; mcpControlRoutes",
	`"/v3/agents" -> s.handleCustomAgents`:                                        "Swarm Control; mcpControlRoutes",
	`"/v3/agents/" -> s.handleCustomAgents`:                                       "Swarm Control; mcpControlRoutes",
	`"/v3/auth/tokens" -> s.handleAuthTokens`:                                     "Swarm Control; mcpControlRoutes",
	`"/v3/box/drain" -> s.handleBoxDrain`:                                         "Swarm Control; mcpControlRoutes",
	`"/v3/box/summary" -> s.handleBoxSummary`:                                     "Swarm Control; mcpControlRoutes",
	`"/v3/limits" -> s.handleLimits`:                                              "Swarm Control; mcpControlRoutes",
	`"/v3/permissions" -> s.handleAccountPermissions`:                             "manage_setup permissions and set_permissions; compared in mcpControlRoutes",
	`"/v3/runs" -> s.handleRuns`:                                                  "Swarm Control; mcpControlRoutes",
	`"/v3/secrets/status" -> s.handleSecretStatus`:                                "Swarm Control; mcpControlRoutes",
	`"/v3/sessions" -> s.handleSessionsV3Primary`:                                 "manage_sessions list and create; compared in mcpControlRoutes",
	`"/v3/sessions/" -> s.handleSessionV3PrimaryByID`:                             "Swarm Control's session functions; mcpControlRoutes",
	`"/v3/signals" -> s.handleSignals`:                                            "Swarm Control; mcpControlRoutes",
	`"/v3/subagents:stop" -> s.handleSessionsV3SubagentStop`:                      "Swarm Control; mcpControlRoutes",
	`"/v3/usage" -> s.handleSessionsV3Usage`:                                      "Swarm Control; mcpControlRoutes",
	`"/v3/usage/limits" -> s.handleSessionsV3UsageLimits`:                         "Swarm Control; mcpControlRoutes",
	`"/v3/usage/worker-budget" -> s.handleWorkerBudget`:                           "Swarm Control; mcpControlRoutes",
	`AgentModelSettingsPath -> s.handleAgentModelSettings`:                        "Swarm Control; mcpControlRoutes",
	`LimitRetryPath -> s.handleLimitRetry`:                                        "Swarm Control; mcpControlRoutes",
	`SandboxEnvironmentsPath -> s.handleSandboxEnvironments`:                      "Swarm Control; mcpControlRoutes",
	`SandboxEnvironmentsPath + "/" -> s.handleSandboxEnvironments`:                "Swarm Control; mcpControlRoutes",
	`WorkersEnsurePresetsPath -> s.handleWorkersEnsurePresets`:                    "Swarm Control; mcpControlRoutes",
	`fleetAgentsPath -> s.handleFleetAgents`:                                      "Swarm Control; mcpControlRoutes",
	`fleetKeysPath -> s.handleFleetKeys`:                                          "Swarm Control; mcpControlRoutes",
	`fleetKeysPath + "/" -> s.handleFleetKeys`:                                    "Swarm Control; mcpControlRoutes",
	`fleetLinkedTasksPath -> s.handleFleetLinkedTasks`:                            "Swarm Control; mcpControlRoutes",
}

// mcpRouteParityNoMCPAction are the route registrations no MCP action reaches
// over the link or through Swarm Control's dispatch: Desktop, console,
// sync/realtime, auth, onboarding, provider, fleet-link and owner routes the
// controlMCPRoutes allowlist does not admit. A route Swarm Control starts
// calling fails TestMCPRouteParityControlRoutes until it moves to
// mcpRouteParityReached; a linked forwarder reaching one needs an
// mcpRouteSpecs entry. Frozen: a new registration fails
// TestMCPRouteParityRegistrations until it is listed in one place.
var mcpRouteParityNoMCPAction = []string{
	`"/ws" -> s.handleDesktopStream`,
	`"/v1/auth/desktop/session" -> s.handleDesktopLocalSessionBootstrap`,
	`TailscaleOnboardingApprovalPath -> s.handleTailscaleOnboardingApproval`,
	`"/v1/feedback" -> s.handleDesktopFeedback`,
	`"/v1/update/status" -> s.handleUpdateStatus`,
	`"/v1/update/apply" -> s.handleUpdateApply`,
	`"/v1/update/run" -> s.handleUpdateRun`,
	`fleetPath -> s.ownerOnlyScope(s.handleFleet)`,
	`fleetPath + "/" -> s.ownerOnlyScope(s.handleFleet)`,
	`fleetActivityPagePath -> s.handleFleetActivity`,
	`fleetActivityStreamPath -> s.handleFleetActivity`,
	`fleetLinkedTaskSessionPath -> s.handleFleetLinkedTasks`,
	`"/v1/memory" -> s.handleMemory`,
	`"/v1/memory/workspace-map" -> s.handleWorkspaceMap`,
	`"/healthz" -> s.handleHealth`,
	`"/readyz" -> s.handleReady`,
	`controlMCPPath -> s.handleControlMCP`,
	`"/v1/remote" -> s.handleRemoteTransport`,
	`"/v1/remote/" -> s.handleRemoteTransport`,
	`"/v1/auth/codex" -> s.ownerOnlyScope(s.handleCodexAuth)`,
	`"/v1/auth/codex/oauth/complete" -> s.ownerOnlyScope(s.handleCodexOAuthComplete)`,
	`"/v1/claudecode/accounts" -> s.ownerOnlyScope(s.handleClaudeCodeAccounts)`,
	`"/v1/auth/credentials/verify" -> s.ownerOnlyScope(s.handleAuthCredentialVerify)`,
	`"/v1/auth/credentials/active" -> s.ownerOnlyScope(s.handleAuthCredentialActive)`,
	`"/v1/auth/credentials/delete" -> s.ownerOnlyScope(s.handleAuthCredentialDelete)`,
	`"/v1/auth/attach/rotate" -> s.ownerOnlyScope(s.handleAttachRotate)`,
	`clientSessionsPath -> s.ownerOnlyScope(s.handleClientSessions)`,
	`clientSessionsPath + "/" -> s.ownerOnlyScope(s.handleClientSessions)`,
	`"/v1/vault" -> s.ownerOnlyScope(s.handleVaultStatus)`,
	`"/v1/vault/enable" -> s.ownerOnlyScope(s.handleVaultEnable)`,
	`"/v1/vault/unlock" -> s.ownerOnlyScope(s.handleVaultUnlock)`,
	`"/v1/vault/lock" -> s.ownerOnlyScope(s.handleVaultLock)`,
	`"/v1/vault/disable" -> s.ownerOnlyScope(s.handleVaultDisable)`,
	`"/v1/vault/export" -> s.ownerOnlyScope(s.handleVaultExport)`,
	`"/v1/vault/import" -> s.ownerOnlyScope(s.handleVaultImport)`,
	`"/me" -> s.handleMe`,
	`"/v1/me" -> s.handleMe`,
	`"/v1/onboarding" -> s.handleOnboarding`,
	`"/v1/onboarding/provider/credential" -> s.ownerOnlyScope(s.handleOnboardingProviderCredential)`,
	`"/v3/box/busy" -> s.handleBoxBusy`,
	`"/v3/outbox" -> s.handleOutbox`,
	`ProposalsPath -> s.handleProposals`,
	`ProposalsPath + "/" -> s.handleProposals`,
	`"/v3/signals/sinks" -> s.handleSignalSinks`,
	`"/v3/signals/sinks/" -> s.handleSignalSinks`,
	`"/v1/secrets" -> s.handleSecrets`,
	`"/v1/secrets/" -> s.handleSecrets`,
	`"/v1/account/username" -> s.handleAccountUsername`,
	`"/v1/account/avatar" -> s.handleAccountAvatar`,
	`"/v1/account/team/upgrade" -> s.handleAccountTeamUpgrade`,
	`AgentModelSettingsPath + "/restore-defaults" -> s.handleRestoreAgentModelDefaults`,
	`"/v1/swarm/targets" -> s.handleSwarmTargets`,
	`"/v1/swarm/target/current" -> s.handleSwarmCurrentTarget`,
	`"/v1/swarm/target/select" -> s.handleSwarmSelectTarget`,
	`"/v1/swarm/groups" -> s.handleSwarmGroups`,
	`"/v1/swarm/groups/upsert" -> s.handleSwarmGroupUpsert`,
	`"/v1/swarm/groups/current" -> s.handleSwarmCurrentGroup`,
	`"/v1/swarm/groups/members/delete" -> s.handleSwarmGroupMemberDelete`,
	`"/v1/swarm/topology" -> s.handleSwarmTopologySnapshot`,
	`"/v1/swarm/topology/workspace-bindings" -> s.handleSwarmTopologyWorkspaceBindings`,
	`"/v2/custom-tools" -> s.agentConfigScope(s.handleCustomToolsV2)`,
	`"/v2/custom-tools/" -> s.agentConfigScope(s.handleCustomToolByNameV2)`,
	`"/v2/agents/defaults/restore" -> s.agentConfigScope(s.handleAgentDefaultsRestoreV2)`,
	`"/v2/agents/defaults/reset" -> s.agentConfigScope(s.handleAgentDefaultsResetV2)`,
	`"/v1/codex/account/reset-credits" -> s.handleCodexResetCredits`,
	`"/v1/codex/account/reset-credits/consume" -> s.handleCodexConsumeResetCredit`,
	`"/v1/image/providers" -> s.handleImageGenerationProviders`,
	`"/v1/media/settings/catalog" -> s.handleMediaSettingsCatalog`,
	`"/v1/image/generations" -> s.handleImageGenerations`,
	`"/v1/image/assets" -> s.handleImageAssets`,
	`"/v1/image/storage/reveal" -> s.handleImageStorageReveal`,
	`"/v1/model" -> s.handleModelPreference`,
	`"/v1/model-profiles" -> s.handleModelProfiles`,
	`"/v1/model-profiles/default" -> s.handleModelProfileDefault`,
	`"/v1/model-profiles/bulk-delete" -> s.handleModelProfilesBulkDelete`,
	`"/v1/model-profiles/" -> s.handleModelProfileByID`,
	`"/v1/model/catalog/check" -> s.handleModelCatalogCheck`,
	`"/v1/models/favorites" -> s.handleModelFavorites`,
	`"/v1/models/favorites/delete" -> s.handleModelFavoriteDelete`,
	`"/v1/stt/transcribe" -> s.handleSTTTranscribe`,
	`"/v1/voice/status" -> s.handleVoiceStatus`,
	`"/v1/voice/profiles" -> s.handleVoiceProfiles`,
	`"/v1/voice/profiles/upsert" -> s.handleVoiceProfileUpsert`,
	`"/v1/voice/profiles/delete" -> s.handleVoiceProfileDelete`,
	`"/v1/voice/config" -> s.handleVoiceConfig`,
	`"/v1/voice/devices" -> s.handleVoiceDevices`,
	`"/v1/voice/test-stt" -> s.handleVoiceTestSTT`,
	`TailscaleSettingsPath -> s.handleTailscaleSettings`,
	`TailscaleSettingsApprovePath -> s.handleTailscaleSettingsApprove`,
	`TailscaleSettingsRevokePath -> s.handleTailscaleSettingsRevoke`,
	`"/v1/workspace/resolve" -> s.handleWorkspaceResolve`,
	`"/v1/workspace/select" -> s.handleWorkspaceSelect`,
	`"/v1/workspace/current" -> s.handleWorkspaceCurrent`,
	`WorkspaceDefinitionRefreshPath -> s.handleWorkspaceDefinitionRefresh`,
	`"/v1/workspace/overview" -> s.handleWorkspaceOverview`,
	`"/v1/workspace/cwd/resolve" -> s.handleWorkspaceCWDResolve`,
	`"/v1/workspace/discover" -> s.handleWorkspaceDiscover`,
	`"/v1/workspace/browse" -> s.handleWorkspaceBrowse`,
	`"/v1/workspace/video/scan" -> s.handleWorkspaceVideoScan`,
	`"/v1/workspace/video/transcribe" -> s.handleWorkspaceVideoTranscribe`,
	`"/v1/workspace/video/transcribe/status" -> s.handleWorkspaceVideoTranscribeStatus`,
	`"/v1/workspace/video/transcribe/read" -> s.handleWorkspaceVideoTranscribeRead`,
	`"/v1/workspace/video/transcribe/cancel" -> s.handleWorkspaceVideoTranscribeCancel`,
	`"/v1/workspace/audio/transcribe" -> s.handleWorkspaceAudioTranscribe`,
	`"/v1/workspace/audio/transcribe/status" -> s.handleWorkspaceAudioTranscribeStatus`,
	`"/v1/workspace/audio/transcribe/read" -> s.handleWorkspaceAudioTranscribeRead`,
	`"/v1/workspace/audio/transcribe/cancel" -> s.handleWorkspaceAudioTranscribeCancel`,
	`"/v1/workspace/audio/analysis/read" -> s.handleWorkspaceAudioAnalysisRead`,
	`"/v1/workspace/video/storage/reveal" -> s.handleVideoStorageReveal`,
	`"/v1/workspace/video/projects" -> s.handleWorkspaceVideoProjects`,
	`"/v1/workspace/video/projects/fork" -> s.handleWorkspaceVideoProjectFork`,
	`"/v1/workspace/video/threads" -> s.handleWorkspaceVideoThreads`,
	`"/v1/workspace/video/threads/" -> s.handleWorkspaceVideoThread`,
	`"/v1/workspace/image/threads" -> s.handleWorkspaceImageThreads`,
	`"/v1/workspace/image/threads/" -> s.handleWorkspaceImageThread`,
	`"/v1/workspace/folders/create" -> s.handleWorkspaceFolderCreate`,
	`"/v1/workspace/repository" -> s.handleWorkspaceRepositoryInspect`,
	`"/v1/workspace/repository/review" -> s.handleWorkspaceRepositoryReview`,
	`"/v1/workspace/repository/baseline" -> s.handleWorkspaceRepositoryBaseline`,
	`"/v1/workspace/directories/add" -> s.handleWorkspaceDirectoryAdd`,
	`"/v1/workspace/directories/remove" -> s.handleWorkspaceDirectoryRemove`,
	`"/v1/workspace/source-media/directories" -> s.handleWorkspaceSourceMediaDirectories`,
	`"/v1/workspace/source-media/directories/add" -> s.handleWorkspaceSourceMediaDirectoryAdd`,
	`"/v1/workspace/source-media/directories/remove" -> s.handleWorkspaceSourceMediaDirectoryRemove`,
	`"/v1/workspace/theme" -> s.handleWorkspaceTheme`,
	`"/v1/workspace/icon" -> s.handleWorkspaceIcon`,
	`"/v1/workspace/rename" -> s.handleWorkspaceRename`,
	`"/v1/workspace/move" -> s.handleWorkspaceMove`,
	`"/v1/workspace/todos" -> s.handleWorkspaceTodos`,
	`"/v1/workspace/actions" -> s.ownerOnlyScope(s.handleWorkspaceActions)`,
	`"/v1/workspace/skills/delete" -> s.handleWorkspaceSkillDelete`,
	`"/v1/workspace/actions/run" -> s.ownerOnlyScope(s.handleWorkspaceActionRunStart)`,
	`"/v1/workspace/actions/runs" -> s.ownerOnlyScope(s.handleWorkspaceActionRuns)`,
	`"/v1/workspace/actions/runs/cancel" -> s.ownerOnlyScope(s.handleWorkspaceActionRunCancel)`,
	`"/v1/workspace/git/status" -> s.handleGitStatus`,
	`"/v1/workspace/git/commit" -> s.handleGitCommit`,
	`"/v1/workspace/git/commit/suggestion" -> s.handleGitCommitSuggestion`,
	`"/v1/workspace/git/realtime" -> s.handleGitRealtime`,
	`"/v1/workspace/git/subscriptions" -> s.handleGitSubscriptions`,
	`"/v1/workspace/delete" -> s.handleWorkspaceDelete`,
	`"/v1/worktrees" -> s.handleWorktrees`,
	`"/v1/manage-worktree" -> s.handleManageWorktree`,
	`"/v1/connections" -> s.handleConnections`,
	`"/v1/task-environments" -> s.handleTaskEnvironments`,
	`"/v1/environments" -> s.handleEnvironments`,
	`"/v1/environments/" -> s.handleEnvironments`,
	`"/v1/deployments" -> s.handleDeployments`,
	`"/v1/context/sources" -> s.handleContextSources`,
	`"/v1/system/shutdown" -> s.handleSystemShutdown`,
	`"/v1/permissions" -> s.handlePermissions`,
	`"/v1/permissions/" -> s.handlePermissions`,
	`"/v1/alerts" -> s.handleNotifications`,
	`"/v1/alerts/" -> s.handleNotifications`,
	`"/v1/alerts/summary" -> s.handleNotifications`,
	`"/v1/alerts/inbox" -> s.handleNotifications`,
	`"/v1/notifications" -> s.handleNotifications`,
	`"/v1/notifications/" -> s.handleNotifications`,
	`"/v1/notifications/summary" -> s.handleNotifications`,
	`"/v1/notifications/inbox" -> s.handleNotifications`,
	`"/v1/storage/buckets" -> s.handleStorageBuckets`,
	`"/v1/storage/buckets/" -> s.handleStorageBuckets`,
	`"/v1/storage/canonical" -> s.handleStorageCanonical`,
	`"/v1/storage/proposals" -> s.handleStorageProposals`,
	`"/v1/storage/workers" -> s.handleStorageWorkers`,
	`"/v1/storage/workers/" -> s.handleStorageWorkers`,
	`"/v1/storage/deliverables" -> s.handleStorageDeliverables`,
	`"/v1/storage/deliverables/" -> s.handleStorageDeliverables`,
	`webPushRoutePrefix -> s.handleWebPush`,
	`webPushRoutePrefix + "/" -> s.handleWebPush`,
	`SessionStorageMaintenancePath -> s.handleSessionStorageMaintenance`,
	`SessionDumpPath -> s.handleSessionDump`,
	`sessionDumpFilePath -> s.handleSessionDumpFile`,
	`LongSessionDiagnosticsConfigPath -> s.handleLongSessionDiagnosticsConfig`,
	`LongSessionDiagnosticsSamplePath -> s.handleLongSessionDiagnosticsSample`,
	`LongSessionDiagnosticsCapturePath -> s.handleLongSessionDiagnosticsCapture`,
	`"/v1/integrations" -> s.handleIntegrations`,
	`"/v1/integrations/workspaces" -> s.handleIntegrationWorkspaces`,
	`"/v1/integrations/workspaces/" -> s.handleIntegrationWorkspaceByID`,
	`MediaStagingCollectionPath -> s.handleMediaStagingCollection`,
	`MediaStagingCollectionPath + "/" -> s.handleMediaStagingItem`,
	`RoutedSessionsPath -> s.handleRoutedSessionStart`,
	`BackgroundRouterSessionsPath -> s.handleBackgroundRouterSessionStart`,
	`WorkspaceOnboardingSessionsPath -> s.handleWorkspaceOnboardingSessionStart`,
	`V3RealtimeStreamPath -> s.handleV3RealtimeStream`,
	`V3SyncBootstrapPath -> s.handleSessionsV3SyncBootstrap`,
	`V3SyncHydratePath -> s.handleSessionsV3SyncHydrate`,
	`V3SyncStreamPath -> s.handleSessionsV3SyncStream`,
	`AutomationsV2Path -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/proposal" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/review" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/accept" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/decline" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/control" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/progress" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/trigger" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/token" -> s.handleAutomationsV2`,
	`AutomationsV2Path + "/webhooks" -> s.handleAutomationsV2Webhooks`,
	`AutomationsV2Path + "/webhooks/" -> s.handleAutomationsV2Webhooks`,
	`DeliverablesPath -> s.handleDeliverables`,
	`DeliverablesPath + "/" -> s.handleDeliverables`,
	`ProjectsPath + ":order" -> s.handleProjects`,
	`"/v3/auth/tokens/" -> s.handleAuthTokens`,
	`AutomationsPath -> s.handleAutomations`,
	`AutomationsPath + "/approve" -> s.handleAutomations`,
	`AutomationsPath + "/revoke" -> s.handleAutomations`,
	`"/v3/sessions:reconnect" -> s.handleSessionsV3Reconnect`,
	`"/v3/sessions:discover" -> s.handleSessionsV3Discovery`,
	`"/v3/sessions:search" -> s.handleSessionsV3Search`,
	`"/v3/sessions:usage" -> s.handleSessionsV3Usage`,
	`"/v3/usage/scope" -> s.handleUsageScope`,
	`"/v3/usage/scopes/repair" -> s.handleUsageScopeRepair`,
	`"/v3/sessions:usage-limits" -> s.handleSessionsV3UsageLimits`,
	`"/v3/sessions:end-user-usage" -> s.handleSessionsV3EndUserUsage`,
	`"/v3/sessions:archive" -> s.handleSessionsV3Primary`,
	`"/v3/sessions:review-worktrees" -> s.handleSessionsV3ReviewWorktrees`,
	`"/v3/sessions:unarchive" -> s.handleSessionsV3Unarchive`,
	`"/v3/sessions:delete" -> s.handleSessionsV3Delete`,
	`"/v3/sessions:agent" -> s.handleSessionsV3AgentSessions`,
	`"/v3/sessions:agent-events" -> s.handleSessionsV3AgentEvents`,
	`"/v3/artifacts" -> s.handleSessionsV3Artifacts`,
	`V3SyncLegacySessionsWorksetPath -> s.handleSessionsV3Workset`,
	`V3SyncLegacyTUIWorksetPath -> s.handleSessionsV3TUIWorkset`,
	`"/v3/tui/sessions" -> s.handleSessionsV3TUI`,
	`"/v3/tui/sessions/" -> s.handleSessionV3TUIByID`,
	`"/v3/application-agents" -> s.handleApplicationAgents`,
	`"/v3/application-agents/" -> s.handleApplicationAgents`,
	// A client's top-level Companion destination (get-or-create, explicit
	// delete). Its conversation is an ordinary V3 session, which MCP already
	// reaches through the session actions; an outside AI has no Companion.
	`CompanionPath -> s.handleCompanion`,
	// The Companion's context bank (its topics, decisions and work pointers)
	// belongs to that client destination; an outside AI has no Companion.
	`CompanionContextPath -> s.handleCompanionContext`,
	`"/v3/activity/stream" -> s.handleActivity`,
	`"/v1/sessions" -> s.handleSessions`,
	`"/v1/sessions/" -> s.handleSessionByID`,
}

// mcpControlRoute places one controlMCPRoutes entry: a route Swarm Control's
// catalog actions reach in-process through c.dispatch (the route allowlist
// gates every such sub-request, so the allowlist is the source of what is
// reached). Shape (with its mcpControlRouteSpecs entry) compares the
// route's accepted fields with each of Actions' declared inputs (Path:
// inputs carried in the path); without a Shape, Reason says why not.
type mcpControlRoute struct {
	Actions []string
	Shape   string
	Path    []string
	Reason  string
}

// mcpControlTODO marks a reached route whose action/route pair is not
// compared yet.
func mcpControlTODO(via string) string {
	return "TODO: reached by " + via + " through c.dispatch; the action/route pair (inputs and answer fields) is not compared yet"
}

// mcpControlRouteSpecs bind the control route shapes that are compared to
// their handler branch. Only inputs are compared: the answer fields of these
// pairs are reshaped by the Swarm Control function (controlMCPListSessions
// picks a summary; a started session answers its current state), and
// comparing them is a TODO.
var mcpControlRouteSpecs = map[string]mcpRouteSpec{
	"GET /v3/sessions":      {Func: "handleSessionsV3PrimaryList"},
	"POST /v3/sessions":     {Func: "handleSessionsV3PrimaryCreate"},
	"GET /v3/permissions":   {Func: "handleAccountPermissions", Anchors: []string{`=r.Method == http.MethodGet`}},
	"PATCH /v3/permissions": {Func: "handleAccountPermissions"},
}

// mcpControlRoutes place every controlMCPRoutes entry. "via" names the
// package api source whose dispatch calls reach the route; an entry with no
// literal dispatch call in package api says so.
var mcpControlRoutes = map[string]mcpControlRoute{
	// Compared pairs.
	"GET v3/sessions":      {Shape: "GET /v3/sessions", Actions: []string{"manage_sessions list"}},
	"POST v3/sessions":     {Shape: "POST /v3/sessions", Actions: []string{"manage_sessions create"}},
	"GET v3/permissions":   {Shape: "GET /v3/permissions", Actions: []string{"manage_setup permissions"}},
	"PATCH v3/permissions": {Shape: "PATCH /v3/permissions", Actions: []string{"manage_setup set_permissions"}},

	// Reached, not compared yet.
	"GET v3/agents":                                        {Reason: mcpControlNoLiteral},
	"GET v3/agents/*":                                      {Reason: mcpControlNoLiteral},
	"PUT v3/agents/*":                                      {Reason: mcpControlNoLiteral},
	"POST v3/agents/*/disable":                             {Reason: mcpControlNoLiteral},
	"GET v3/workflows":                                     {Reason: mcpControlNoLiteral},
	"POST v3/workflows":                                    {Reason: mcpControlNoLiteral},
	"PUT v3/workflows":                                     {Reason: mcpControlNoLiteral},
	"DELETE v3/workflows":                                  {Reason: mcpControlNoLiteral},
	"GET v3/workflows/*":                                   {Reason: mcpControlNoLiteral},
	"DELETE v3/workflows/*":                                {Reason: mcpControlNoLiteral},
	"POST v3/workflows/stop":                               {Reason: mcpControlNoLiteral},
	"POST v3/workflows/restore":                            {Reason: mcpControlNoLiteral},
	"POST v3/workflows/import":                             {Reason: mcpControlNoLiteral},
	"POST v3/workflows/export":                             {Reason: mcpControlNoLiteral},
	"POST v3/subagents:stop":                               {Reason: mcpControlTODO("the stop-run functions (control_mcp_tools.go)")},
	"GET v3/limits":                                        {Reason: mcpControlTODO("manage_setup limits (limits.go)")},
	"PUT v3/limits":                                        {Reason: mcpControlTODO("manage_setup set_limits (limits.go)")},
	"GET v1/workspace/list":                                {Reason: mcpControlTODO("the workspace lookups (control_mcp_tools.go, control_mcp_media_jobs.go)")},
	"POST v1/workspace/repository/setup":                   {Reason: mcpControlTODO("workspace creation (control_mcp_tools.go)")},
	"POST v1/workspace/add":                                {Reason: mcpControlTODO("workspace creation (control_mcp_tools.go)")},
	"GET v1/providers":                                     {Reason: mcpControlTODO("manage_setup providers (control_mcp_tools.go, control_mcp_provider_keys.go)")},
	"GET v1/model/catalog":                                 {Reason: mcpControlTODO("the model listing (control_mcp_tools.go)")},
	"GET v1/agent-model-settings":                          {Reason: mcpControlTODO("the agent-model reads (control_mcp_tools.go)")},
	"PATCH v1/agent-model-settings":                        {Reason: mcpControlTODO("manage_setup set_agent_model (control_mcp_tools.go)")},
	"GET v1/ui/settings":                                   {Reason: mcpControlTODO("the media-model settings (control_mcp_media.go, control_mcp_tools.go)")},
	"POST v1/ui/settings":                                  {Reason: mcpControlTODO("manage_setup set_media_models (control_mcp_media.go)")},
	"GET v3/sessions/*":                                    {Reason: mcpControlTODO("the session reads (control_mcp_tools.go, control_mcp_jobs.go, control_mcp_tasks.go, control_mcp_review.go)")},
	"GET v3/sessions/*/messages":                           {Reason: mcpControlTODO("manage_sessions get's message chunk read (session_message_chunk.go)")},
	"POST v3/sessions/*/messages":                          {Reason: mcpControlTODO("manage_sessions send_message (control_mcp_tools.go)")},
	"PUT v3/sessions/*/model-profile":                      {Reason: mcpControlTODO("manage_sessions set_model (control_mcp_tools.go)")},
	"POST v3/sessions/*/run/stop":                          {Reason: mcpControlTODO("the stop-run functions (control_mcp_tools.go)")},
	"POST v3/sessions/*/permissions/*/resolve":             {Reason: mcpControlTODO("manage_sessions resolve_permission (control_mcp_tools.go)")},
	"POST v3/sessions/*/access":                            {Reason: mcpControlTODO("session start's access (control_mcp_tools.go)")},
	"GET v3/fleet/keys":                                    {Reason: mcpControlTODO("manage_setup list_fleet_keys (control_mcp_fleet_keys.go)")},
	"POST v3/fleet/keys/requests":                          {Reason: mcpControlTODO("manage_setup request_fleet_key (control_mcp_fleet_keys.go)")},
	"POST v3/fleet/keys/*/revoke":                          {Reason: mcpControlTODO("manage_setup revoke_fleet_key (control_mcp_fleet_keys.go)")},
	"GET v3/fleet/linked-tasks":                            {Reason: mcpControlNoLiteral},
	"POST v3/sessions/*/media":                             {Reason: mcpControlTODO("the session attachment upload (control_mcp_tools.go)")},
	"POST v3/sessions/*/plans":                             {Reason: mcpControlTODO("session start with a plan (control_mcp_tools.go)")},
	"GET v3/sessions/*/plans/active":                       {Reason: mcpControlTODO("session start with a plan (control_mcp_tools.go)")},
	"POST v3/sessions/*/plan-mode/plans/*/start-automatic": {Reason: mcpControlTODO("session start with a plan (control_mcp_tools.go)")},
	"GET v3/projects":                                      {Reason: mcpControlTODO("the job and workflow-transfer reads (control_mcp_jobs.go, workflow_transfer.go); manage_projects list is compared over the link (mcpRouteSpecs)")},
	"POST v3/projects":                                     {Reason: mcpControlNoLiteral + "; manage_projects create is compared with POST /v3/projects in mcpHomeActions"},
	"GET v3/projects/*":                                    {Reason: mcpControlTODO("the project reads (control_mcp_tools.go)")},
	"PATCH v3/projects/*":                                  {Reason: mcpControlNoLiteral + "; manage_projects update is compared with PATCH /v3/projects/{id} in mcpHomeActions"},
	"POST v3/projects/*/context:retry":                     {Reason: mcpControlTODO("the project.md retry (control_mcp_tools.go)")},
	"POST v3/projects/*/sessions":                          {Reason: mcpControlTODO("manage_sessions create with project_id (control_mcp_tools.go)")},
	"GET v3/projects/*/tasks":                              {Reason: mcpControlTODO("the task lists (control_mcp_tools.go, control_mcp_project_status.go)")},
	"POST v3/projects/*/tasks":                             {Reason: mcpControlTODO("one-hop delegation (control_mcp_tasks.go)")},
	"GET v3/projects/*/tasks/*":                            {Reason: mcpControlTODO("the task reads (control_mcp_tools.go, control_mcp_tasks.go)")},
	"POST v3/projects/*/tasks/*/integrate":                 {Reason: mcpControlTODO("the task integrate function (control_mcp_tools.go)")},
	"POST v3/projects/*/tasks/*/approve":                   {Reason: mcpControlNoLiteral},
	"POST v3/projects/*/tasks/*/reject":                    {Reason: mcpControlNoLiteral},
	"POST v3/projects/*/design-jobs":                       {Reason: mcpControlTODO("design jobs (control_mcp_design.go)")},
	"POST v3/projects/*/design-jobs/*/cancel":              {Reason: mcpControlTODO("design jobs (control_mcp_design.go)")},
	"POST v3/sessions/*/plan-mode/checkpoints/*/accept":    {Reason: mcpControlNoLiteral},
	"GET v3/workers":                                       {Reason: mcpControlTODO("the job tools (control_mcp_jobs.go)")},
	"POST v3/workers":                                      {Reason: mcpControlTODO("worker creation (control_mcp_tools.go, control_mcp_jobs.go)")},
	"POST v3/workers:ensure-presets":                       {Reason: mcpControlTODO("the job tools' preset workers (control_mcp_jobs.go)")},
	"GET v3/workers/*":                                     {Reason: mcpControlTODO("the worker reads (control_mcp_tools.go, job_automations.go, outbox.go)")},
	"PUT v3/workers/*":                                     {Reason: mcpControlTODO("the worker model pin (worker_model_pin.go)")},
	"GET v3/workers/*/summary":                             {Reason: mcpControlTODO("the worker summary (control_mcp_tools.go)")},
	"POST v3/workers/*/activate":                           {Reason: mcpControlTODO("worker activation (control_mcp_tools.go, control_mcp_jobs.go)")},
	"PUT v3/workers/*/model":                               {Reason: mcpControlTODO("the worker model (control_mcp_tools.go, worker_model_pin.go)")},
	"POST v3/workers/*/automations/*/enable":               {Reason: mcpControlTODO("worker schedules (control_mcp_tools.go, control_mcp_jobs.go)")},
	"POST v3/workers/*/automations/*/disable":              {Reason: mcpControlTODO("worker schedules (control_mcp_tools.go)")},
	"POST v3/workers/*/pause":                              {Reason: mcpControlTODO("the worker lifecycle (control_mcp_tools.go)")},
	"POST v3/workers/*/resume":                             {Reason: mcpControlTODO("the worker lifecycle (control_mcp_tools.go)")},
	"POST v3/workers/*/archive":                            {Reason: mcpControlTODO("the worker lifecycle (control_mcp_tools.go)")},
	"POST v3/workers/*/delete":                             {Reason: mcpControlTODO("the worker lifecycle (control_mcp_tools.go)")},
	"POST v3/workers/*/direct":                             {Reason: mcpControlTODO("worker direction (control_mcp_tools.go, control_mcp_jobs.go)")},
	"GET v3/workers/*/runs":                                {Reason: mcpControlTODO("the worker run reads (control_mcp_tools.go, control_mcp_jobs.go)")},
	"GET v3/workers/*/runs/*":                              {Reason: mcpControlTODO("the worker run reads (control_mcp_tools.go, control_mcp_jobs.go, job_automations.go)")},
	"POST v3/workers/*/runs/*/cancel":                      {Reason: mcpControlTODO("run cancel (control_mcp_tools.go, control_mcp_jobs.go)")},
	"GET v3/usage":                                         {Reason: mcpControlTODO("manage_setup usage (control_mcp_tools.go)")},
	"PUT v3/usage/worker-budget":                           {Reason: mcpControlTODO("the job budget (control_mcp_jobs_budget.go)")},
	"GET v1/codex/account/usage":                           {Reason: mcpControlTODO("manage_setup plan_usage (control_mcp_tools.go)")},
	"GET v1/claudecode/usage":                              {Reason: mcpControlTODO("manage_setup plan_usage (control_mcp_tools.go, control_mcp_provider_keys.go)")},
	"GET v3/usage/limits":                                  {Reason: mcpControlTODO("manage_setup set_usage_limits' narrowing read (setup_catalog.go)")},
	"POST v3/usage/limits":                                 {Reason: mcpControlTODO("manage_setup set_usage_limits (control_mcp_tools.go)")},
	"GET v3/box/summary":                                   {Reason: mcpControlTODO("the monitoring reads (control_mcp_jobs.go, outbox.go)")},
	"POST v3/box/drain":                                    {Reason: mcpControlTODO("the drain tools (box_drain.go)")},
	"DELETE v3/box/drain":                                  {Reason: mcpControlTODO("the drain tools (box_drain.go)")},
	"GET v3/box/drain":                                     {Reason: mcpControlNoLiteral},
	"GET v3/runs":                                          {Reason: mcpControlTODO("the run list (control_mcp_jobs.go)")},
	"POST v3/provider-limit-waits/retry":                   {Reason: mcpControlTODO("manage_jobs retry_now (limit_retry.go)")},
	"GET v3/fleet/agents":                                  {Reason: mcpControlNoLiteral},
	"GET v3/signals":                                       {Reason: mcpControlTODO("the monitoring reads (control_mcp_jobs.go)")},
	"GET v3/activity":                                      {Reason: mcpControlTODO("the monitoring reads (control_mcp_jobs.go)")},
	"GET v3/secrets/status":                                {Reason: mcpControlTODO("the secret status read (control_mcp_jobs.go)")},
	"GET v3/sandbox-environments":                          {Reason: mcpControlTODO("folder environments (control_mcp_env.go)")},
	"PUT v3/sandbox-environments":                          {Reason: mcpControlTODO("folder environments (control_mcp_env.go)")},
	"PATCH v3/sandbox-environments":                        {Reason: mcpControlTODO("folder environments (control_mcp_env.go)")},
	"POST v3/sandbox-environments/rebuild":                 {Reason: mcpControlTODO("folder environments (control_mcp_env.go)")},
	"GET v3/sandbox-environments/list":                     {Reason: mcpControlNoLiteral},
	"DELETE v3/sandbox-environments":                       {Reason: mcpControlTODO("folder environments (control_mcp_env.go)")},
	"GET v2/agents":                                        {Reason: mcpControlNoLiteral},
	"GET v2/agents/*":                                      {Reason: mcpControlTODO("the box-setup sub-agent builders (control_mcp_builders.go)")},
	"PUT v2/agents/*":                                      {Reason: mcpControlNoLiteral},
	"POST v3/auth/tokens":                                  {Reason: mcpControlNoLiteral},
	"POST v1/auth/codex/oauth/start":                       {Reason: mcpControlTODO("ChatGPT device sign-in (control_mcp_builders.go)")},
	"GET v1/auth/codex/oauth/status":                       {Reason: mcpControlTODO("ChatGPT device sign-in (control_mcp_builders.go)")},
	"POST v1/auth/credentials":                             {Reason: mcpControlTODO("Claude Code activation (control_mcp_claude_code.go)")},
}

// mcpControlNoLiteral is the reason for an allowlisted route no literal
// dispatch call in package api names (the path is built elsewhere or the
// entry has no caller left), so the actions reaching it are not identified.
const mcpControlNoLiteral = "TODO: admitted by controlMCPRoutes, but no literal c.dispatch call in package api names it; the actions that reach it are not identified or compared yet"

// mcpControlRouteParityAllow are known differences between a compared
// control action and its route, keyed like mcpHomeRouteParityAllow.
var mcpControlRouteParityAllow = map[string]string{
	// manage_sessions list: the catalog handler answers from its in-process
	// session search (manageSessionsSearch); Swarm Control adds project_id's
	// conversations through controlMCPListSessions, which sends only limit,
	// project_id and view.
	"manage_sessions list query input_unread":             "filtered by the catalog handler's in-process session search, not the route",
	"manage_sessions list state input_unread":             "filtered by the catalog handler's in-process session search, not the route",
	"manage_sessions list category input_unread":          "filtered by the catalog handler's in-process session search, not the route",
	"manage_sessions list cursor input_unread":            "paged by the catalog handler's in-process session search, not the route",
	"manage_sessions list workspace_path input_unread":    "filtered by the catalog handler's in-process session search, not the route",
	"manage_sessions list view route_undeclared":          "controlMCPListSessions sets view=summary with project_id; not a caller input",
	"manage_sessions list archived_mode route_undeclared": "TODO: the route's archived filter; list's archived handling is the in-process search, so the route's filter is not offered",
	// manage_sessions create: a Swarm Control create with plan, attachments
	// or project_id runs controlMCPStartSession, which builds the route's
	// own body and sends the rest through follow-up routes.
	"manage_sessions create prompt input_unread":                          "sent as the first message (POST v3/sessions/*/messages) after the session is created",
	"manage_sessions create plan input_unread":                            "started through the plan routes (POST v3/sessions/*/plans, start-automatic) after the session is created",
	"manage_sessions create attachments input_unread":                     "uploaded through POST v3/sessions/*/media after the session is created",
	"manage_sessions create wait_seconds input_unread":                    "a wait Swarm Control holds after starting the work; no route field",
	"manage_sessions create agent input_unread":                           "sent as agent_name",
	"manage_sessions create agent_name route_undeclared":                  "the route's name for agent",
	"manage_sessions create provider input_unread":                        "resolved with model and thinking into model_profile (c.modelPreference)",
	"manage_sessions create model input_unread":                           "resolved with provider and thinking into model_profile (c.modelPreference)",
	"manage_sessions create thinking input_unread":                        "resolved with provider and model into model_profile (c.modelPreference)",
	"manage_sessions create model_profile route_undeclared":               "built from provider, model and thinking",
	"manage_sessions create client_request_id route_undeclared":           "minted per call by controlMCPStartSession (controlMCPRequestID)",
	"manage_sessions create mode route_undeclared":                        "fixed to auto by controlMCPStartSession: callers bring their own plan",
	"manage_sessions create preference route_undeclared":                  mcpControlSessionCreateOnly,
	"manage_sessions create end_user route_undeclared":                    mcpControlSessionCreateOnly,
	"manage_sessions create host_workspace_path route_undeclared":         mcpControlSessionCreateOnly,
	"manage_sessions create idempotency_key route_undeclared":             mcpControlSessionCreateOnly,
	"manage_sessions create labels route_undeclared":                      mcpControlSessionCreateOnly,
	"manage_sessions create managed_worktree_requested route_undeclared":  mcpControlSessionCreateOnly,
	"manage_sessions create metadata route_undeclared":                    mcpControlSessionCreateOnly,
	"manage_sessions create purpose route_undeclared":                     mcpControlSessionCreateOnly,
	"manage_sessions create runtime_workspace_path route_undeclared":      mcpControlSessionCreateOnly,
	"manage_sessions create session_id route_undeclared":                  mcpControlSessionCreateOnly,
	"manage_sessions create swarm_id route_undeclared":                    mcpControlSessionCreateOnly,
	"manage_sessions create target_kind route_undeclared":                 mcpControlSessionCreateOnly,
	"manage_sessions create target_relationship route_undeclared":         mcpControlSessionCreateOnly,
	"manage_sessions create workspace_binding_id route_undeclared":        mcpControlSessionCreateOnly,
	"manage_sessions create workspace_id route_undeclared":                mcpControlSessionCreateOnly,
	"manage_sessions create workspace_name route_undeclared":              mcpControlSessionCreateOnly,
	"manage_sessions create worktree_base_branch route_undeclared":        mcpControlSessionCreateOnly,
	"manage_sessions create worktree_branch_name route_undeclared":        mcpControlSessionCreateOnly,
	"manage_sessions create worktree_existing_path route_undeclared":      mcpControlSessionCreateOnly,
	"manage_sessions create worktree_mode route_undeclared":               mcpControlSessionCreateOnly,
	"manage_sessions create worktree_use_current_branch route_undeclared": mcpControlSessionCreateOnly,
	// manage_setup set_permissions: controlMCPSetPermissions passes only
	// expected_revision, client_request_id, tasks and linked.
	"manage_setup set_permissions devices route_undeclared": "device rules change only on that device's Permissions page; controlMCPSetPermissions refuses them",
}

// mcpControlSessionCreateOnly is the reason for a session-create route field
// manage_sessions create does not offer.
const mcpControlSessionCreateOnly = "TODO: a session-create field of the Desktop, SDK and internal starts (targets, worktree options, labels, metadata, idempotency); manage_sessions create does not offer it"

// mcpRegistrationResolver resolves registration patterns written as Go
// expressions (literals, package constants, concatenations) to paths.
type mcpRegistrationResolver struct {
	consts map[string]ast.Expr
	src    *mcpRouteSource
}

func newMCPRegistrationResolver(t *testing.T) *mcpRegistrationResolver {
	t.Helper()
	src, err := loadMCPRouteSource()
	if err != nil {
		t.Fatal(err)
	}
	r := &mcpRegistrationResolver{consts: map[string]ast.Expr{}, src: src}
	files, _ := filepath.Glob("*.go")
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, ident := range vs.Names {
					if i < len(vs.Values) {
						r.consts[ident.Name] = vs.Values[i]
					}
				}
			}
		}
	}
	return r
}

func (r *mcpRegistrationResolver) resolve(e ast.Expr, depth int) (string, bool) {
	if depth > 8 {
		return "", false
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		return v, err == nil
	case *ast.Ident:
		value, ok := r.consts[x.Name]
		if !ok {
			return "", false
		}
		return r.resolve(value, depth+1)
	case *ast.ParenExpr:
		return r.resolve(x.X, depth+1)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok := r.resolve(x.X, depth+1)
		if !ok {
			return "", false
		}
		b, ok := r.resolve(x.Y, depth+1)
		return a + b, ok
	}
	return "", false
}

// registrationFor is the registration net/http's mux picks for path: an
// exact pattern, else the longest "/"-terminated prefix.
func (r *mcpRegistrationResolver) registrationFor(t *testing.T, regs []mcpRouteRegistration, path string) (string, bool) {
	t.Helper()
	best, bestLen := "", -1
	for _, reg := range regs {
		expr, err := parser.ParseExpr(reg.Pattern)
		if err != nil {
			continue
		}
		pattern, ok := r.resolve(expr, 0)
		if !ok {
			continue
		}
		key := reg.Pattern + " -> " + reg.Handler
		if pattern == path {
			return key, true
		}
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(path, pattern) && len(pattern) > bestLen {
			best, bestLen = key, len(pattern)
		}
	}
	return best, bestLen >= 0
}

// mcpCompareActionRoute compares one action's declared inputs with the
// fields the route shape reads, both ways, reporting each difference to
// note (input_unread / route_undeclared) and failures to drift.
func mcpCompareActionRoute(id string, action tool.CatalogAction, shape string, path []string, note func(id, field, kind, detail string), drift *[]string) {
	_, fields, found, err := mcpRouteAccepts(shape)
	if !found || err != nil {
		*drift = append(*drift, fmt.Sprintf("%s: route %s: found=%v err=%v", id, shape, found, err))
		return
	}
	if len(fields.Unresolved) > 0 {
		*drift = append(*drift, fmt.Sprintf("%s: route %s: %s", id, shape, strings.Join(fields.Unresolved, "; ")))
	}
	accepted := map[string]bool{}
	for key := range fields.Query {
		accepted[key] = true
	}
	for key := range fields.Body {
		accepted[key] = true
	}
	declared := map[string]bool{}
	for _, name := range mcpParityInputs(action) {
		declared[name] = true
		if slices.Contains(path, name) {
			continue
		}
		if !accepted[name] {
			note(id, name, "input_unread", shape+" does not read "+name)
		}
	}
	for _, key := range mcpParitySortedKeys(accepted) {
		if !declared[key] {
			note(id, key, "route_undeclared", shape+" reads "+key+", which the action does not declare")
		}
	}
}

// mcpControlCatalogAction finds a Control-callable catalog action by
// "tool action".
func mcpControlCatalogAction(id string) (tool.CatalogAction, bool) {
	toolName, actionName, _ := strings.Cut(id, " ")
	for _, catalog := range tool.Catalog() {
		if catalog.Name != toolName {
			continue
		}
		action, ok := catalog.Action(actionName)
		if ok && action.ControlCallable() {
			return action, true
		}
	}
	return tool.CatalogAction{}, false
}

// Purpose:
//   - Requirement: every route Swarm Control reaches (controlMCPRoutes, the
//     allowlist c.dispatch enforces for every catalog action's in-process
//     sub-request) is served by a registration listed in
//     mcpRouteParityReached, never in mcpRouteParityNoMCPAction, and is
//     placed in mcpControlRoutes: compared with the actions that reach it
//     (their declared inputs against the fields the route's source reads,
//     both ways) or listed with a reasoned TODO. A new allowlist entry fails
//     until it is placed.
//   - Threat/regression: an MCP action and the local route it calls
//     drifting apart (a route field the action lacks, or an action input
//     the route ignores), or a route Swarm Control starts calling that the
//     completeness lists still call unreached.
//   - Boundary/authority: controlMCPRoutes (control_mcp_tools.go), the mux
//     registrations, tool.Catalog and the handler source
//     (mcpControlRouteSpecs).
//   - Narrowest layer: declarations and handler source; no daemon runs.
func TestMCPRouteParityControlRoutes(t *testing.T) {
	var drift []string
	matched := map[string]bool{}
	note := func(id, field, kind, detail string) {
		key := id + " " + field + " " + kind
		if _, ok := mcpControlRouteParityAllow[key]; ok {
			matched[key] = true
			return
		}
		drift = append(drift, fmt.Sprintf("%q: %s", key, detail))
	}
	regs := mcpRouteRegistrations(t)
	resolver := newMCPRegistrationResolver(t)
	none := map[string]bool{}
	for _, key := range mcpRouteParityNoMCPAction {
		none[key] = true
	}
	allowlisted := map[string]bool{}
	usedShapes := map[string]bool{}
	for _, route := range controlMCPRoutes {
		allowlisted[route] = true
		method, pattern, _ := strings.Cut(route, " ")
		path := "/" + strings.ReplaceAll(pattern, "*", "pf-id")
		reg, ok := resolver.registrationFor(t, regs, path)
		switch {
		case !ok:
			drift = append(drift, route+": no route registration serves "+path)
		case none[reg]:
			drift = append(drift, route+": served by "+reg+", which mcpRouteParityNoMCPAction calls unreached: move it to mcpRouteParityReached")
		default:
			if _, reached := mcpRouteParityReached[reg]; !reached {
				drift = append(drift, route+": served by "+reg+", which mcpRouteParityReached does not list")
			}
		}
		entry, ok := mcpControlRoutes[route]
		switch {
		case !ok:
			drift = append(drift, route+" is a controlMCPRoutes entry with no mcpControlRoutes entry: compare it with the actions that reach it (Shape) or give a reasoned TODO")
			continue
		case entry.Shape == "" && strings.TrimSpace(entry.Reason) == "":
			drift = append(drift, route+": an mcpControlRoutes entry needs a Shape or a Reason")
			continue
		case entry.Shape == "":
			continue
		}
		if !strings.HasPrefix(entry.Shape, method+" ") {
			drift = append(drift, fmt.Sprintf("%s: Shape %s has another method", route, entry.Shape))
		}
		if len(entry.Actions) == 0 {
			drift = append(drift, route+": a compared entry names the actions that reach it")
		}
		usedShapes[entry.Shape] = true
		for _, id := range entry.Actions {
			action, ok := mcpControlCatalogAction(id)
			if !ok {
				drift = append(drift, route+": "+id+" is not a Control-callable catalog action")
				continue
			}
			mcpCompareActionRoute(id, action, entry.Shape, entry.Path, note, &drift)
		}
	}
	for _, route := range mcpParitySortedKeys(mcpControlRoutes) {
		if !allowlisted[route] {
			drift = append(drift, "mcpControlRoutes lists "+route+", which is not in controlMCPRoutes: remove it")
		}
	}
	for _, shape := range mcpParitySortedKeys(mcpControlRouteSpecs) {
		if !usedShapes[shape] {
			drift = append(drift, "mcpControlRouteSpecs entry "+shape+" is compared with no action: remove it")
		}
	}
	for key := range mcpControlRouteParityAllow {
		if !matched[key] {
			drift = append(drift, fmt.Sprintf("mcpControlRouteParityAllow entry %q no longer matches: remove it", key))
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("Swarm Control route / MCP action drift (%d):\n%s", len(drift), strings.Join(drift, "\n"))
	}
}

// Purpose:
//   - Requirement: every route package api registers is either reached by an
//     MCP action over HTTP (and bound to the source the parity tests compare)
//     or listed as reached by none; a new route fails until it is placed.
//   - Threat/regression: a route added for an MCP action (or an MCP action
//     forwarded to a new route) with no parity entry, so the parity tests
//     silently skip it.
//   - Boundary/authority: the mux.Handle/HandleFunc registrations in the
//     package's source (server_routes.go and the files that register their
//     own routes).
//   - Narrowest layer: the registration source; no daemon runs.
func TestMCPRouteParityRegistrations(t *testing.T) {
	var drift []string
	registered := map[string]bool{}
	none := map[string]bool{}
	for _, key := range mcpRouteParityNoMCPAction {
		if none[key] {
			drift = append(drift, "mcpRouteParityNoMCPAction lists "+key+" twice")
		}
		none[key] = true
	}
	for _, r := range mcpRouteRegistrations(t) {
		key := r.Pattern + " -> " + r.Handler
		registered[key] = true
		_, reached := mcpRouteParityReached[key]
		switch {
		case reached && none[key]:
			drift = append(drift, key+" is in both mcpRouteParityReached and mcpRouteParityNoMCPAction")
		case !reached && !none[key]:
			drift = append(drift, key+" is a new route registration: list it in mcpRouteParityReached (with its mcpRouteSpecs entry) or mcpRouteParityNoMCPAction")
		}
	}
	for key := range mcpRouteParityReached {
		if !registered[key] {
			drift = append(drift, "mcpRouteParityReached lists "+key+", which is no longer registered: remove it")
		}
	}
	for key := range none {
		if !registered[key] {
			drift = append(drift, "mcpRouteParityNoMCPAction lists "+key+", which is no longer registered: remove it")
		}
	}
	if len(registered) < 100 {
		drift = append(drift, fmt.Sprintf("only %d route registrations found: the source scan is broken", len(registered)))
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("route registration parity (%d):\n%s", len(drift), strings.Join(drift, "\n"))
	}
}
