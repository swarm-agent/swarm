#!/bin/bash
# Listen to voice agent SSE events for 60 seconds
# Run this while talking to the agent

echo "Listening to SSE events for 60 seconds..."
echo "Talk to the voice agent now!"
echo "---"

timeout 60 curl -sN \
  -H "Accept: text/event-stream" \
  -H "Cache-Control: no-cache" \
  http://localhost:4097/events
