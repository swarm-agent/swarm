#!/bin/bash
# send-message.sh - Send a message to an agent and auto-approve permissions
#
# Usage: ./send-message.sh <port> <message>
#
# Example:
#   ./send-message.sh 4200 "List all files in the workspace"

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
if [[ "$SESSION_ID" == "null" || -z "$SESSION_ID" ]]; then
    echo "Error: Failed to create session"
    echo "$SESSION_RESPONSE"
    exit 1
fi

echo "Session: $SESSION_ID"
echo "Sending message: $MESSAGE"
echo ""

# Send message (async - returns immediately)
curl -s -X POST "$BASE_URL/session/$SESSION_ID/message" \
    -H "Content-Type: application/json" \
    -d "{\"parts\":[{\"type\":\"text\",\"text\":$(echo "$MESSAGE" | jq -R .)}]}" > /dev/null

# Poll for completion and auto-approve permissions
echo "Waiting for response (auto-approving permissions)..."
echo ""

COMPLETED=false
LAST_TEXT=""

while [[ "$COMPLETED" != "true" ]]; do
    sleep 1

    # Check for pending permissions and approve them
    # Get the session's log file from container to check permission IDs
    # For now we'll use a simpler approach - just keep checking session status

    # Get session info
    SESSION_INFO=$(curl -s "$BASE_URL/session/$SESSION_ID")

    # Try to get children/messages
    CHILDREN=$(curl -s "$BASE_URL/session/$SESSION_ID/children" 2>/dev/null || echo "[]")

    # Check if there are any pending permissions by looking at the log
    # This is a hack - ideally the API would expose pending permissions

    # For now, just print dots to show we're waiting
    echo -n "."
done

echo ""
echo "=== Response ==="
# Get final messages
curl -s "$BASE_URL/session/$SESSION_ID/children" | jq -r '.[].parts[]? | select(.type == "text") | .text' 2>/dev/null || echo "(No text response)"
