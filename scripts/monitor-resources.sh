#!/bin/bash
# Swarm Resource Monitor
# Run this OUTSIDE the sandbox to track resource usage over time

INTERVAL=${1:-10}  # Default 10 seconds
DURATION=${2:-120} # Default 2 minutes (120 seconds)
OUTFILE="${3:-/tmp/swarm-baseline-$(date +%Y%m%d-%H%M%S).log}"

echo "=== Swarm Resource Monitor ==="
echo "Interval: ${INTERVAL}s, Duration: ${DURATION}s"
echo "Output: $OUTFILE"
echo ""

# Find swarm/opencode main process
find_swarm_pid() {
    # Look for the main swarm server process
    pgrep -f 'swarm.*serve' | head -1 || \
    pgrep -f 'opencode.*serve' | head -1 || \
    pgrep -f 'bun.*server\.ts' | head -1 || \
    pgrep -f 'swarm' | head -1
}

SWARM_PID=$(find_swarm_pid)

if [ -z "$SWARM_PID" ]; then
    echo "ERROR: No swarm/opencode process found!"
    echo ""
    echo "Looking for any bun processes:"
    ps aux | grep bun | grep -v grep
    echo ""
    echo "Looking for processes on port 4096:"
    ss -tlnp | grep 4096 || netstat -tlnp 2>/dev/null | grep 4096
    exit 1
fi

echo "Found swarm process: PID $SWARM_PID"
echo "Command: $(ps -p $SWARM_PID -o args= 2>/dev/null)"
echo ""

# Header
echo "timestamp,pid,rss_kb,vsz_kb,cpu_pct,threads,fds,children" | tee "$OUTFILE"

START=$(date +%s)
END=$((START + DURATION))

while [ $(date +%s) -lt $END ]; do
    NOW=$(date +%s)
    ELAPSED=$((NOW - START))
    
    # Check if process still exists
    if ! kill -0 $SWARM_PID 2>/dev/null; then
        echo "Process $SWARM_PID died!"
        SWARM_PID=$(find_swarm_pid)
        if [ -z "$SWARM_PID" ]; then
            echo "No swarm process found, exiting"
            break
        fi
        echo "Switched to new PID: $SWARM_PID"
    fi
    
    # Get metrics
    RSS=$(ps -p $SWARM_PID -o rss= 2>/dev/null | tr -d ' ')
    VSZ=$(ps -p $SWARM_PID -o vsz= 2>/dev/null | tr -d ' ')
    CPU=$(ps -p $SWARM_PID -o %cpu= 2>/dev/null | tr -d ' ')
    THREADS=$(ps -p $SWARM_PID -o nlwp= 2>/dev/null | tr -d ' ')
    FDS=$(ls /proc/$SWARM_PID/fd 2>/dev/null | wc -l)
    CHILDREN=$(pgrep -P $SWARM_PID 2>/dev/null | wc -l)
    
    # Output
    LINE="$NOW,$SWARM_PID,$RSS,$VSZ,$CPU,$THREADS,$FDS,$CHILDREN"
    echo "$LINE" | tee -a "$OUTFILE"
    
    # Progress
    echo -ne "\r[${ELAPSED}/${DURATION}s] RSS: ${RSS}KB, FDs: ${FDS}, Children: ${CHILDREN}  "
    
    sleep $INTERVAL
done

echo ""
echo ""
echo "=== Summary ==="
echo "Log saved to: $OUTFILE"
echo ""

# Calculate stats
if [ -f "$OUTFILE" ]; then
    echo "RSS (KB) - Min/Max/Avg:"
    tail -n +2 "$OUTFILE" | cut -d',' -f3 | sort -n | awk '
        NR==1 {min=$1; max=$1; sum=0}
        {sum+=$1; if($1<min)min=$1; if($1>max)max=$1}
        END {printf "  Min: %d KB (%d MB)\n  Max: %d KB (%d MB)\n  Avg: %d KB (%d MB)\n", min, min/1024, max, max/1024, sum/NR, (sum/NR)/1024}
    '
    
    echo ""
    echo "File Descriptors - Min/Max:"
    tail -n +2 "$OUTFILE" | cut -d',' -f7 | sort -n | awk '
        NR==1 {min=$1; max=$1}
        {if($1<min)min=$1; if($1>max)max=$1}
        END {printf "  Min: %d\n  Max: %d\n  Delta: %d\n", min, max, max-min}
    '
    
    echo ""
    echo "Child Processes - Min/Max:"
    tail -n +2 "$OUTFILE" | cut -d',' -f8 | sort -n | awk '
        NR==1 {min=$1; max=$1}
        {if($1<min)min=$1; if($1>max)max=$1}
        END {printf "  Min: %d\n  Max: %d\n  Delta: %d\n", min, max, max-min}
    '
fi
