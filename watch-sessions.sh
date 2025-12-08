#!/bin/bash

# Watch script for monitoring swarm session status
# Run this in a separate terminal while testing

SESSIONS_FILE="$HOME/.config/swarm/sessions.json"

echo "Watching $SESSIONS_FILE for changes..."
echo "Press Ctrl+C to stop"
echo ""

# Check if file exists
if [ ! -f "$SESSIONS_FILE" ]; then
    echo "Sessions file doesn't exist yet. Waiting..."
    while [ ! -f "$SESSIONS_FILE" ]; do
        sleep 1
    done
fi

# Watch for changes
while true; do
    clear
    echo "=== Swarm Sessions Status ==="
    echo "Updated: $(date)"
    echo ""
    
    if [ -f "$SESSIONS_FILE" ]; then
        # Show formatted output with Hyprland workspace
        jq -r '.sessions[] | "PID: \(.pid) | Hypr WS: \(.hyprWorkspace // "?") | Status: \(.status) | CWD: \(.cwd)"' "$SESSIONS_FILE" 2>/dev/null
        
        # Check for blocked sessions
        blocked=$(jq -r '.sessions[] | select(.status == "blocked") | "\(.pid) (ws:\(.hyprWorkspace // "?"))"' "$SESSIONS_FILE" 2>/dev/null)
        if [ -n "$blocked" ]; then
            echo ""
            echo "!!! BLOCKED: $blocked !!!"
        fi
    else
        echo "(no sessions)"
    fi
    
    sleep 1
done
