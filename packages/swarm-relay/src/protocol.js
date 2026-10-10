// swarm-remote-1: the contract between a Swarm daemon (device) and a relay.
// The device dials out over WebSocket; nothing listens on the device. Every
// frame is one JSON object with a `type`.
//
// relay -> device: challenge, ready, pairing, mcp.request, consent.request, error
// device -> relay: auth, mcp.response, consent.decision
//
// A device whose key the relay does not trust yet sends its public_key in
// auth; after proving it holds that key it waits in `pairing` with a short
// code until an authorized client pairs it (swarm_pair_machine), then gets
// `ready` on the same connection.
export const PROTOCOL = 'swarm-remote-1';

// OAuth scopes a client can be granted. read is required for any access.
export const SCOPE_READ = 'swarm:read';
export const SCOPE_WRITE = 'swarm:write';
export const SCOPE_APPROVE = 'swarm:approve';
// Worker administration, usage limits and agent role default models.
export const SCOPE_MANAGE = 'swarm:manage';
export const SCOPES = [SCOPE_READ, SCOPE_WRITE, SCOPE_APPROVE, SCOPE_MANAGE];

// Scope each Swarm Control tool needs. Unknown tools need write. The device
// enforces the same mapping against its own local ceiling.
export const TOOL_SCOPES = {
  swarm_list_machines: SCOPE_READ,
  swarm_list_workspaces: SCOPE_READ,
  swarm_list_sessions: SCOPE_READ,
  swarm_get_session: SCOPE_READ,
  swarm_list_projects: SCOPE_READ,
  swarm_list_workers: SCOPE_READ,
  swarm_get_worker: SCOPE_READ,
  swarm_get_usage: SCOPE_READ,
  swarm_list_models: SCOPE_READ,
  swarm_list_tasks: SCOPE_READ,
  swarm_create_workspace: SCOPE_WRITE,
  swarm_start_session: SCOPE_WRITE,
  swarm_send_message: SCOPE_WRITE,
  swarm_run_plan: SCOPE_WRITE,
  swarm_stop_run: SCOPE_WRITE,
  swarm_assign_worker_task: SCOPE_WRITE,
  swarm_set_session_model: SCOPE_WRITE,
  swarm_resolve_permission: SCOPE_APPROVE,
  swarm_integrate_task: SCOPE_APPROVE,
  swarm_create_project: SCOPE_MANAGE,
  swarm_create_worker: SCOPE_MANAGE,
  swarm_update_worker: SCOPE_MANAGE,
  swarm_manage_worker: SCOPE_MANAGE,
  swarm_set_usage_limits: SCOPE_MANAGE,
  swarm_set_agent_model: SCOPE_MANAGE,
  swarm_list_agents: SCOPE_READ,
  swarm_define_agent: SCOPE_MANAGE,
  swarm_create_client_key: SCOPE_MANAGE,
  swarm_connect_chatgpt: SCOPE_MANAGE,
  // Relay tools: trust a machine waiting to pair, or remove a paired one.
  swarm_pair_machine: SCOPE_MANAGE,
  swarm_remove_machine: SCOPE_MANAGE,
};

export function toolScope(name) {
  return TOOL_SCOPES[name] || SCOPE_WRITE;
}

// The device signs this exact message with its Ed25519 key. Binding the relay
// origin and device id prevents replaying a signature to another relay or as
// another device.
export function authMessage(relayOrigin, deviceId, nonce) {
  return `${PROTOCOL}\n${relayOrigin}\n${deviceId}\n${nonce}`;
}

export const MCP_PROTOCOL_VERSIONS = ['2025-11-25', '2025-06-18', '2025-03-26'];
