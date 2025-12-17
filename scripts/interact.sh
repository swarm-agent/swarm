#!/bin/bash
# interact.sh - Interactive agent session with auto-approval
#
# Usage: ./interact.sh <port> <message>
#
# This script:
# 1. Creates a session
# 2. Sends the message
# 3. Streams events via SSE
# 4. Auto-approves any permission requests
# 5. Shows the response

set -e

PORT="${1:?Usage: $0 <port> <message>}"
MESSAGE="${2:?Usage: $0 <port> <message>}"
BASE_URL="http://127.0.0.1:${PORT}"

# Create session
echo "Creating session..."
SESSION_RESPONSE=$(curl -s -X POST "$BASE_URL/session" \
    -H "Content-Type: application/json" \
    -d '{}')

SESSION_ID=$(echo "$SESSION_RESPONSE" | jq -r '.id')
DIRECTORY=$(echo "$SESSION_RESPONSE" | jq -r '.directory')

if [[ "$SESSION_ID" == "null" || -z "$SESSION_ID" ]]; then
    echo "Error: Failed to create session"
    echo "$SESSION_RESPONSE"
    exit 1
fi

echo "Session: $SESSION_ID"
echo "Directory: $DIRECTORY"
echo ""
echo "Message: $MESSAGE"
echo ""
echo "=== Response ==="

# Function to approve a permission
approve_permission() {
    local session_id="$1"
    local perm_id="$2"
    curl -s -X POST "$BASE_URL/session/$session_id/permissions/$perm_id" \
        -H "Content-Type: application/json" \
        -d '{"response":"once"}' > /dev/null 2>&1
    echo "[Auto-approved: $perm_id]" >&2
}

# Start SSE listener in background and process events
process_events() {
    curl -s -N "$BASE_URL/event" | while IFS= read -r line; do
        # Skip empty lines and "data: " prefix
        if [[ "$line" =~ ^data:\ (.+)$ ]]; then
            json="${BASH_REMATCH[1]}"
            event_type=$(echo "$json" | jq -r '.type // empty' 2>/dev/null)

            case "$event_type" in
                "permission.updated")
                    perm_id=$(echo "$json" | jq -r '.properties.id // empty')
                    perm_session=$(echo "$json" | jq -r '.properties.sessionID // empty')
                    if [[ -n "$perm_id" && "$perm_session" == "$SESSION_ID" ]]; then
                        approve_permission "$SESSION_ID" "$perm_id"
                    fi
                    ;;
                "message.part.updated")
                    part_session=$(echo "$json" | jq -r '.properties.sessionID // empty')
                    part_type=$(echo "$json" | jq -r '.properties.type // empty')
                    if [[ "$part_session" == "$SESSION_ID" && "$part_type" == "text" ]]; then
                        # Print text incrementally
                        text=$(echo "$json" | jq -r '.properties.text // empty')
                        echo -n "$text"
                    fi
                    ;;
                "session.completed")
                    completed_session=$(echo "$json" | jq -r '.properties.sessionID // empty')
                    if [[ "$completed_session" == "$SESSION_ID" ]]; then
                        echo ""
                        echo ""
                        echo "=== Completed ==="
                        exit 0
                    fi
                    ;;
            esac
        fi
    done
}

# Start event processing in background
process_events &
EVENT_PID=$!

# Give SSE a moment to connect
sleep 0.5

# Send the message
curl -s -X POST "$BASE_URL/session/$SESSION_ID/message" \
    -H "Content-Type: application/json" \
    -d "{\"parts\":[{\"type\":\"text\",\"text\":$(echo "$MESSAGE" | jq -R .)}]}" > /dev/null

# Wait for completion (timeout after 5 minutes)
TIMEOUT=300
ELAPSED=0
while kill -0 $EVENT_PID 2>/dev/null && [ $ELAPSED -lt $TIMEOUT ]; do
    sleep 1
    ((ELAPSED++))
done

# Clean up
kill $EVENT_PID 2>/dev/null || true
