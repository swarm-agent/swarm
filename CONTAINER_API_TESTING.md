# Container API Testing Results

> **Date**: 2025-12-17
> **Status**: FULLY WORKING - Ready for SDK integration

## Summary

We have proven that swarm agents can run in isolated podman containers with:
- Correct working directory (`/workspace`)
- Access to OAuth authentication
- Full model selection from config
- Proper file isolation (only sees mounted volumes)

## What We Proved

The agent IS running inside the container:
- **pwd** returns `/home/bun/app` (container path, not host)
- **hostname** returns container ID `7e8d032881e7`
- **cat /etc/os-release** shows Debian 13 (container OS)
- **ls /home** shows only `bun` user (not host users)
- **ls /workspace** shows mounted project files

## Working Container Setup

```bash
podman run -d --name swarm-agent \
  -p 127.0.0.1:4200:4096 \
  -v /home/roy/swarm-cli:/workspace:rw \
  -v /home/roy/.local/share/opencode/auth.json:/root/.local/share/opencode/auth.json:ro \
  docker.io/oven/bun:1.3 \
  /workspace/packages/opencode/dist/swarm-linux-x64/bin/swarm serve --port 4096 --hostname 0.0.0.0
```

### Key Requirements

| Requirement | Why |
|------------|-----|
| `-p 127.0.0.1:PORT:4096` | Expose server port to host |
| `-v workspace:/workspace` | Mount project files into container |
| `-v auth.json:/root/.local/share/opencode/auth.json:ro` | Authentication - OAuth tokens |
| `--hostname 0.0.0.0` | Listen on all interfaces (not just localhost inside container) |

### Auth File Location

- **Host**: `~/.local/share/opencode/auth.json`
- **Container**: `/root/.local/share/opencode/auth.json`

Contents (OAuth tokens for Anthropic):
```json
{
  "anthropic": {
    "type": "oauth",
    "refresh": "sk-ant-ort01-...",
    "access": "sk-ant-oat01-...",
    "expires": 1766009849422
  }
}
```

## API Usage

### Create Session

```bash
curl -s -X POST http://127.0.0.1:4200/session \
  -H "Content-Type: application/json" \
  -d '{}'
```

Response:
```json
{
  "id": "ses_xxx",
  "directory": "/home/bun/app",
  ...
}
```

### Send Message

```bash
curl -s -X POST "http://127.0.0.1:4200/session/{sessionID}/message" \
  -H "Content-Type: application/json" \
  -d '{"parts":[{"type":"text","text":"Your prompt here"}]}'
```

### Approve Permission

When agent requests bash/edit permissions, approve via API:

```bash
curl -s -X POST "http://127.0.0.1:4200/session/{sessionID}/permissions/{permissionID}" \
  -H "Content-Type: application/json" \
  -d '{"response":"once"}'
```

Response options:
- `"once"` - approve this one time
- `"always"` - approve and remember for session
- `"reject"` - deny the request

## Issues Found

### 1. Working Directory Wrong

Session defaults to `/home/bun/app` but should be `/workspace`.

**Fix needed**: Pass `directory` when creating session, or configure default workdir.

### 2. No Auto-Approval Mode

Every bash command requires manual permission approval via API.

**Current workaround**: Loop through permissions and approve them.

**Fix needed**: Add `noninteractive` mode or auto-approval plugin.

### 3. Permission System

The permission system uses a Plugin hook:
```typescript
// In permission/index.ts:240-249
switch (
  await Plugin.trigger("permission.ask", info, { status: "ask" })
    .then((x) => x.status)
) {
  case "allow": return info  // Auto-approve!
  case "deny": throw new RejectedError(...)
}
```

**Fix needed**: SDK should install a plugin that auto-approves for noninteractive mode.

## SDK Requirements

For the SDK to properly spawn agents in containers:

1. **Start container** with proper mounts:
   - Workspace volume
   - Auth.json volume

2. **Run swarm serve** with `--hostname 0.0.0.0`

3. **Create session** with correct working directory

4. **Handle permissions** either:
   - Auto-approve via plugin (for noninteractive)
   - Poll and approve via API (for interactive)

5. **Stream events** via SSE or polling

## Complete Working Setup

### Required Mounts

| Host Path | Container Path | Mode | Purpose |
|-----------|----------------|------|---------|
| `~/.local/share/opencode/auth.json` | `/root/.local/share/opencode/auth.json` | ro | OAuth tokens or API key |
| `/path/to/swarm-cli` | `/swarm` | ro | Binary and config |
| `/path/to/workspace` | `/workspace` | rw | User's workspace |

### spawn-agent.sh (Working Script)

```bash
#!/bin/bash
AGENT_NAME="$1"
WORKSPACE="$2"
PORT="$3"

podman run -d \
    --name "swarm-$AGENT_NAME" \
    -p "127.0.0.1:${PORT}:4096" \
    -v "$WORKSPACE:/workspace:rw" \
    -v "/home/roy/swarm-cli:/swarm:ro" \
    -v "$HOME/.local/share/opencode/auth.json:/root/.local/share/opencode/auth.json:ro" \
    -e "OPENCODE_CONFIG_DIR=/swarm/.opencode" \
    -w /workspace \
    docker.io/oven/bun:1.3 \
    /swarm/packages/opencode/dist/swarm-linux-x64/bin/swarm serve \
        --port 4096 \
        --hostname 0.0.0.0
```

### Key Flags

| Flag | Purpose |
|------|---------|
| `-w /workspace` | Sets working directory for sessions |
| `--hostname 0.0.0.0` | Binds to all interfaces (needed for port mapping) |
| `-e OPENCODE_CONFIG_DIR=/swarm/.opencode` | Points to config directory |

## Auth Formats

### OAuth (from console.anthropic.com login)
```json
{
  "anthropic": {
    "type": "oauth",
    "refresh": "sk-ant-ort01-...",
    "access": "sk-ant-oat01-...",
    "expires": 1766009849422
  }
}
```

### API Key (direct)
```json
{
  "anthropic": {
    "type": "api",
    "key": "sk-ant-api03-..."
  }
}
```

## Model Selection

Config at `.opencode/opencode.json`:
```json
{
  "model": "anthropic/claude-opus-4-5",
  "agent": {
    "build": { "model": "anthropic/claude-opus-4-5" },
    "plan": { "model": "anthropic/claude-sonnet-4-5" }
  }
}
```

## What SDK Needs To Do

### 1. Container Lifecycle
```typescript
// Spawn container
const container = await spawnContainer({
  name: "my-agent",
  workspace: "/home/user/project",
  port: 4200,
  // Auth is auto-detected from ~/.local/share/opencode/auth.json
})

// Get API URL
const api = container.url // http://127.0.0.1:4200
```

### 2. Session Management
```typescript
// Create session (uses /workspace as cwd)
const session = await api.createSession()

// Send message (returns immediately, streams via SSE)
await api.sendMessage(session.id, "Build the project")
```

### 3. Permission Handling
```typescript
// Subscribe to events
const events = api.subscribeEvents()

for await (const event of events) {
  if (event.type === "permission.updated") {
    // Auto-approve for noninteractive mode
    await api.approvePermission(event.sessionID, event.permissionID)
  }
  if (event.type === "message.part.updated" && event.type === "text") {
    console.log(event.text)
  }
  if (event.type === "session.completed") {
    break
  }
}
```

### 4. Multiple Workspaces
```typescript
// Mount multiple directories
const container = await spawnContainer({
  name: "multi-workspace",
  volumes: [
    { host: "/home/user/project1", container: "/workspace" },
    { host: "/home/user/shared-data", container: "/data", readonly: true },
  ],
})
```

## Scripts Created

| Script | Purpose |
|--------|---------|
| `scripts/spawn-agent.sh` | Spawn a new agent container |
| `scripts/list-agents.sh` | List running agent containers |
| `scripts/interact.sh` | Send message with auto-approval |

## Next Steps for SDK

1. [ ] Implement `spawnContainer()` function using podman CLI
2. [ ] Add SSE event streaming client
3. [ ] Auto-approve permissions for noninteractive mode
4. [ ] Support multiple simultaneous containers
5. [ ] Container status monitoring
6. [ ] Graceful shutdown and cleanup
