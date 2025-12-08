#!/bin/bash

# Test script for Hyprland session status broadcasting
# This demonstrates the concept before implementing in TypeScript

SESSIONS_FILE="$HOME/.config/swarm/sessions.json"
SESSIONS_DIR="$HOME/.config/swarm"

# Ensure directory exists
mkdir -p "$SESSIONS_DIR"

# Initialize empty sessions file if it doesn't exist
if [ ! -f "$SESSIONS_FILE" ]; then
    echo '{"sessions":[]}' > "$SESSIONS_FILE"
fi

# Get current PID
PID=$$
CWD="$(pwd)"

# Get Hyprland workspace info
HYPR_INFO=$(hyprctl activewindow -j 2>/dev/null)
if [ -n "$HYPR_INFO" ] && [ "$HYPR_INFO" != "null" ]; then
    HYPR_WORKSPACE=$(echo "$HYPR_INFO" | jq -r '.workspace.id // empty')
    HYPR_WINDOW_PID=$(echo "$HYPR_INFO" | jq -r '.pid // empty')
else
    HYPR_WORKSPACE=""
    HYPR_WINDOW_PID=""
fi

echo "=== Session Status Test ==="
echo "PID: $PID"
echo "CWD: $CWD"
echo "Hyprland Workspace: ${HYPR_WORKSPACE:-N/A}"
echo "Hyprland Window PID: ${HYPR_WINDOW_PID:-N/A}"
echo "Sessions file: $SESSIONS_FILE"
echo ""

# Function to read current sessions
read_sessions() {
    cat "$SESSIONS_FILE"
}

# Function to register this session
register() {
    local status="${1:-idle}"
    local now=$(date +%s)000  # milliseconds
    
    # Build JSON entry with optional hyprland fields
    local hypr_fields=""
    if [ -n "$HYPR_WORKSPACE" ]; then
        hypr_fields="\"hyprWorkspace\": $HYPR_WORKSPACE, \"hyprWindowPid\": $HYPR_WINDOW_PID,"
    fi
    
    # Read existing sessions, filter out this PID, add new entry
    local new_entry=$(cat <<EOF
{
  "pid": $PID,
  "cwd": "$CWD",
  $hypr_fields
  "status": "$status",
  "startedAt": $now,
  "lastUpdated": $now
}
EOF
)
    
    # Use jq to update the file atomically
    local tmp_file="${SESSIONS_FILE}.tmp.$$"
    jq --argjson entry "$new_entry" \
       '.sessions = [.sessions[] | select(.pid != '"$PID"')] + [$entry]' \
       "$SESSIONS_FILE" > "$tmp_file" && mv "$tmp_file" "$SESSIONS_FILE"
    
    echo "Registered with status: $status"
}

# Function to update status
set_status() {
    local status="$1"
    local now=$(date +%s)000
    
    local tmp_file="${SESSIONS_FILE}.tmp.$$"
    jq --arg status "$status" --argjson now "$now" \
       '.sessions = [.sessions[] | if .pid == '"$PID"' then .status = $status | .lastUpdated = $now else . end]' \
       "$SESSIONS_FILE" > "$tmp_file" && mv "$tmp_file" "$SESSIONS_FILE"
    
    echo "Status updated to: $status"
}

# Function to unregister (cleanup)
unregister() {
    local tmp_file="${SESSIONS_FILE}.tmp.$$"
    jq '.sessions = [.sessions[] | select(.pid != '"$PID"')]' \
       "$SESSIONS_FILE" > "$tmp_file" && mv "$tmp_file" "$SESSIONS_FILE"
    
    echo "Unregistered PID $PID"
}

# Function to prune dead PIDs
prune() {
    echo "Pruning dead sessions..."
    local tmp_file="${SESSIONS_FILE}.tmp.$$"
    
    # Get list of PIDs and check which are alive
    local pids=$(jq -r '.sessions[].pid' "$SESSIONS_FILE")
    local alive_pids=""
    
    for pid in $pids; do
        if kill -0 "$pid" 2>/dev/null; then
            alive_pids="$alive_pids $pid"
        else
            echo "  Removing dead PID: $pid"
        fi
    done
    
    # Filter to only alive PIDs
    jq '[.sessions[] | select(.pid as $p | ['"$(echo $pids | tr ' ' ',')"'] | any(. == $p and (. as $pid | '"$(for p in $alive_pids; do echo -n "($pid == $p) or "; done)"' false)))]' "$SESSIONS_FILE" 2>/dev/null || true
    
    # Simpler approach - just rebuild with alive PIDs
    local new_sessions='{"sessions":['
    local first=true
    for pid in $alive_pids; do
        if kill -0 "$pid" 2>/dev/null; then
            local entry=$(jq ".sessions[] | select(.pid == $pid)" "$SESSIONS_FILE")
            if [ -n "$entry" ]; then
                if [ "$first" = true ]; then
                    first=false
                else
                    new_sessions+=","
                fi
                new_sessions+="$entry"
            fi
        fi
    done
    new_sessions+=']}'
    
    echo "$new_sessions" | jq '.' > "$tmp_file" && mv "$tmp_file" "$SESSIONS_FILE"
    echo "Prune complete"
}

# Cleanup on exit
cleanup() {
    echo ""
    echo "Cleaning up..."
    unregister
    exit 0
}

trap cleanup EXIT INT TERM

# Main demo
echo "1. Registering session..."
register "idle"
echo ""
echo "Current sessions:"
read_sessions | jq '.'
echo ""

echo "2. Simulating work..."
set_status "working"
echo ""
echo "Current sessions:"
read_sessions | jq '.'
echo ""

echo "3. Simulating BLOCKED state (waiting for user input)..."
set_status "blocked"
echo ""
echo "Current sessions:"
read_sessions | jq '.'
echo ""

echo "=== Now open another terminal and run: ==="
echo "cat $SESSIONS_FILE | jq '.'"
echo "watch -n1 'cat $SESSIONS_FILE | jq .sessions[].status'"
echo ""
echo "Press Enter to simulate user responding (unblock)..."
read

echo "4. User responded, back to working..."
set_status "working"
echo ""
echo "Current sessions:"
read_sessions | jq '.'
echo ""

echo "Press Enter to exit (will cleanup)..."
read

echo "Exiting..."
