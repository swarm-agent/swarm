#!/bin/bash
# Swarm Internal State Debug
# Queries the swarm API for internal state information

PORT=${1:-4096}
BASE_URL="http://localhost:$PORT"

echo "=== Swarm Internal State Debug ==="
echo "Querying: $BASE_URL"
echo "Time: $(date)"
echo ""

# Check if server is running
echo "--- Health Check ---"
curl -s "$BASE_URL/health" 2>/dev/null || echo "Server not responding on port $PORT"
echo ""

# Get session list
echo "--- Sessions ---"
SESSIONS=$(curl -s "$BASE_URL/session" 2>/dev/null)
if [ -n "$SESSIONS" ]; then
    echo "$SESSIONS" | jq -r '.data | length' 2>/dev/null && echo " sessions"
    echo "$SESSIONS" | jq -r '.data[]? | "\(.id) - \(.title // "untitled") - msgs: \(.messageCount // 0)"' 2>/dev/null | head -20
else
    echo "Could not fetch sessions"
fi
echo ""

# Get MCP servers
echo "--- MCP Servers ---"
curl -s "$BASE_URL/mcp" 2>/dev/null | jq '.' 2>/dev/null || echo "Could not fetch MCP info"
echo ""

# Get profiles
echo "--- Container Profiles ---"
curl -s "$BASE_URL/profile" 2>/dev/null | jq '.data[]? | "\(.name) - \(.status)"' 2>/dev/null || echo "Could not fetch profiles"
echo ""

# Check process stats via /proc if we have the PID
echo "--- Process Stats ---"
SWARM_PID=$(pgrep -f 'swarm.*serve' | head -1)
if [ -n "$SWARM_PID" ]; then
    echo "PID: $SWARM_PID"
    echo "Memory (RSS): $(ps -p $SWARM_PID -o rss= | awk '{printf "%.1f MB", $1/1024}')"
    echo "Memory (VSZ): $(ps -p $SWARM_PID -o vsz= | awk '{printf "%.1f MB", $1/1024}')"
    echo "CPU: $(ps -p $SWARM_PID -o %cpu=)%"
    echo "Threads: $(ps -p $SWARM_PID -o nlwp=)"
    echo "File Descriptors: $(ls /proc/$SWARM_PID/fd 2>/dev/null | wc -l)"
    echo "Child Processes: $(pgrep -P $SWARM_PID | wc -l)"
    
    echo ""
    echo "--- Child Process Details ---"
    for child in $(pgrep -P $SWARM_PID); do
        CMD=$(ps -p $child -o args= 2>/dev/null | head -c 60)
        RSS=$(ps -p $child -o rss= 2>/dev/null)
        echo "  PID $child: ${RSS}KB - $CMD"
    done
    
    echo ""
    echo "--- Open Sockets ---"
    ls -la /proc/$SWARM_PID/fd 2>/dev/null | grep socket | wc -l | xargs echo "Socket FDs:"
    
    echo ""
    echo "--- Event Loop (Bun internals) ---"
    # Check for any stuck async operations
    cat /proc/$SWARM_PID/stack 2>/dev/null | head -20 || echo "Cannot read stack"
else
    echo "Swarm process not found"
fi
