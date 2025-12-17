#!/bin/bash
# spawn-agent.sh - Spawn a swarm agent in a podman container
#
# Usage: ./spawn-agent.sh <agent-name> <workspace-path> [port]
#
# Example:
#   ./spawn-agent.sh twitter-bot /home/roy/socialmedia/twitter 4201
#   ./spawn-agent.sh voice-agent /home/roy/pi/voiceagent 4202

set -e

AGENT_NAME="${1:?Usage: $0 <agent-name> <workspace-path> [port]}"
WORKSPACE="${2:?Usage: $0 <agent-name> <workspace-path> [port]}"
PORT="${3:-0}"  # 0 means auto-assign

# Validate workspace exists
if [[ ! -d "$WORKSPACE" ]]; then
    echo "Error: Workspace does not exist: $WORKSPACE"
    exit 1
fi

# Paths
SWARM_CLI="/home/roy/swarm-cli"
SWARM_BIN="$SWARM_CLI/packages/opencode/dist/swarm-linux-x64/bin/swarm"
AUTH_FILE="$HOME/.local/share/opencode/auth.json"
CONFIG_DIR="$SWARM_CLI/.opencode"

# Container name
CONTAINER_NAME="swarm-$AGENT_NAME"

# Check if container already exists
if podman ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Container $CONTAINER_NAME already exists"
    echo "To remove: podman rm -f $CONTAINER_NAME"
    exit 1
fi

# Check binary exists
if [[ ! -f "$SWARM_BIN" ]]; then
    echo "Error: Swarm binary not found at $SWARM_BIN"
    echo "Build it first: cd $SWARM_CLI && ./build.sh"
    exit 1
fi

# Check auth exists
if [[ ! -f "$AUTH_FILE" ]]; then
    echo "Error: Auth file not found at $AUTH_FILE"
    echo "Run 'swarm auth' first to authenticate"
    exit 1
fi

# Build port mapping argument
PORT_ARG=""
if [[ "$PORT" != "0" ]]; then
    PORT_ARG="-p 127.0.0.1:${PORT}:4096"
fi

echo "=== Spawning Agent: $AGENT_NAME ==="
echo "Workspace: $WORKSPACE"
echo "Port: ${PORT:-auto}"

# Create container
# Mounts:
#   /workspace        - The agent's workspace (read/write)
#   /swarm            - Swarm CLI binary and config (read-only)
#   /root/.local/share/opencode/auth.json - Auth tokens (read-only)
#   /root/.config/opencode - Config symlinked from /swarm/.opencode
CONTAINER_ID=$(podman run -d \
    --name "$CONTAINER_NAME" \
    $PORT_ARG \
    -v "$WORKSPACE:/workspace:rw" \
    -v "$SWARM_CLI:/swarm:ro" \
    -v "$AUTH_FILE:/root/.local/share/opencode/auth.json:ro" \
    -e "OPENCODE_CONFIG_DIR=/swarm/.opencode" \
    -w /workspace \
    docker.io/oven/bun:1.3 \
    /swarm/packages/opencode/dist/swarm-linux-x64/bin/swarm serve \
        --port 4096 \
        --hostname 0.0.0.0)

echo "Container ID: $CONTAINER_ID"

# Wait for server to start
echo "Waiting for server to start..."
sleep 2

# Get the assigned port if auto
if [[ "$PORT" == "0" ]]; then
    ASSIGNED_PORT=$(podman port "$CONTAINER_NAME" 4096 | cut -d: -f2)
    echo "Assigned port: $ASSIGNED_PORT"
    PORT="$ASSIGNED_PORT"
fi

# Check if server is responding
if curl -s "http://127.0.0.1:${PORT}/session" > /dev/null 2>&1; then
    echo ""
    echo "=== Agent Ready ==="
    echo "Name: $AGENT_NAME"
    echo "Container: $CONTAINER_NAME"
    echo "API: http://127.0.0.1:${PORT}"
    echo ""
    echo "Commands:"
    echo "  List sessions:  curl http://127.0.0.1:${PORT}/session"
    echo "  Create session: curl -X POST http://127.0.0.1:${PORT}/session -H 'Content-Type: application/json' -d '{}'"
    echo "  View logs:      podman logs -f $CONTAINER_NAME"
    echo "  Stop:           podman stop $CONTAINER_NAME"
    echo "  Remove:         podman rm -f $CONTAINER_NAME"
else
    echo ""
    echo "Warning: Server may not be ready yet. Check logs:"
    echo "  podman logs $CONTAINER_NAME"
fi
