// swarm-remote-1: the contract between a Swarm daemon (device) and a relay.
// The device dials out over WebSocket; nothing listens on the device. Every
// frame is one JSON object with a `type`.
//
// relay -> device: challenge, ready, mcp.request, consent.request, error
// device -> relay: auth, mcp.response, consent.decision
export const PROTOCOL = 'swarm-remote-1';

// OAuth scopes a client can be granted. read is required for any access.
export const SCOPE_READ = 'swarm:read';
export const SCOPE_WRITE = 'swarm:write';
export const SCOPE_APPROVE = 'swarm:approve';
// Worker administration and account usage limits.
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
  swarm_start_session: SCOPE_WRITE,
  swarm_send_message: SCOPE_WRITE,
  swarm_run_plan: SCOPE_WRITE,
  swarm_stop_run: SCOPE_WRITE,
  swarm_assign_worker_task: SCOPE_WRITE,
  swarm_resolve_permission: SCOPE_APPROVE,
  swarm_create_project: SCOPE_MANAGE,
  swarm_create_worker: SCOPE_MANAGE,
  swarm_update_worker: SCOPE_MANAGE,
  swarm_manage_worker: SCOPE_MANAGE,
  swarm_set_usage_limits: SCOPE_MANAGE,
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
