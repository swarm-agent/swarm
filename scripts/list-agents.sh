#!/bin/bash
# list-agents.sh - List all running swarm agents

echo "=== Running Swarm Agents ==="
echo ""

# Get all swarm containers
CONTAINERS=$(podman ps --filter "name=swarm-" --format '{{.Names}}\t{{.Status}}\t{{.Ports}}' 2>/dev/null)

if [[ -z "$CONTAINERS" ]]; then
    echo "No swarm agents running."
    echo ""
    echo "Start one with:"
    echo "  ./scripts/spawn-agent.sh <name> <workspace-path> [port]"
    exit 0
fi

printf "%-30s %-20s %s\n" "NAME" "STATUS" "PORT"
printf "%-30s %-20s %s\n" "----" "------" "----"

while IFS=$'\t' read -r name status ports; do
    # Extract port from "127.0.0.1:4200->4096/tcp"
    port=$(echo "$ports" | grep -oP '\d+(?=->4096)')
    printf "%-30s %-20s %s\n" "$name" "$status" "${port:-N/A}"
done <<< "$CONTAINERS"

echo ""
echo "Commands:"
echo "  View logs:    podman logs -f <container-name>"
echo "  Stop agent:   podman stop <container-name>"
echo "  Remove agent: podman rm -f <container-name>"
echo "  API call:     curl http://127.0.0.1:<port>/session"
