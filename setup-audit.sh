#!/bin/bash
# Setup npm/bun audit monitoring

# Create the audit script
cat > ~/.local/bin/npm-audit-cron.sh << 'SCRIPT'
#!/bin/bash
LOG_FILE="$HOME/.npm-audit.log"
PROJECTS=("$HOME/swarm_agent_prod" "$HOME/swarm-cli" "$HOME/todo")

[[ -z "$HYPRLAND_INSTANCE_SIGNATURE" ]] && \
    export HYPRLAND_INSTANCE_SIGNATURE=$(ls -t /run/user/1000/hypr/ 2>/dev/null | head -1)

notify() {
    hyprctl notify $1 $2 "$3" "  $4  " 2>/dev/null
    notify-send -u "$([[ $1 -gt 2 ]] && echo critical || echo normal)" "🛡️ Security" "$4" 2>/dev/null
}

scan_project() {
    local dir=$1
    local name=$(basename "$dir")
    local c=0 h=0 m=0
    
    cd "$dir" 2>/dev/null || return
    
    if [[ -f "bun.lock" ]] || [[ -f "bun.lockb" ]]; then
        local result=$(bun audit 2>/dev/null)
        h=$(echo "$result" | grep -oP '\d+(?= high)' | head -1)
        m=$(echo "$result" | grep -oP '\d+(?= moderate)' | head -1)
        c=$(echo "$result" | grep -oP '\d+(?= critical)' | head -1)
        local pm="bun"
    elif [[ -f "package-lock.json" ]]; then
        local result=$(/usr/bin/npm audit --json 2>/dev/null)
        c=$(echo "$result" | grep -oP '"critical":\s*\K[0-9]+' | head -1)
        h=$(echo "$result" | grep -oP '"high":\s*\K[0-9]+' | head -1)
        m=$(echo "$result" | grep -oP '"moderate":\s*\K[0-9]+' | head -1)
        local pm="npm"
    else
        return
    fi
    
    c=${c:-0}; h=${h:-0}; m=${m:-0}
    echo "[$(date '+%Y-%m-%d %H:%M')] $name ($pm): C:$c H:$h M:$m" >> "$LOG_FILE"
    
    [[ $c -gt 0 ]] && notify 3 20000 "rgb(ff0055)" "🚨 $c CRITICAL in $name!"
    [[ $h -gt 0 ]] && notify 3 15000 "rgb(ff6600)" "⚠️ $h HIGH vulns in $name"
    [[ $m -gt 0 && $c -eq 0 && $h -eq 0 ]] && notify 0 8000 "rgb(ffcc00)" "⚡ $m moderate in $name"
}

echo "[$(date '+%Y-%m-%d %H:%M')] === Audit scan ===" >> "$LOG_FILE"

for p in "${PROJECTS[@]}"; do
    scan_project "$p"
    for s in "$p"/*/; do
        [[ -f "${s}package.json" ]] && scan_project "$s"
    done
done
SCRIPT

chmod +x ~/.local/bin/npm-audit-cron.sh
echo "✅ Created ~/.local/bin/npm-audit-cron.sh"

# Add cron job
(crontab -l 2>/dev/null | grep -v npm-audit-cron; echo "0 */6 * * * $HOME/.local/bin/npm-audit-cron.sh") | crontab -
echo "✅ Added cron job (every 6 hours)"

# Test it
echo "🔍 Running test scan..."
~/.local/bin/npm-audit-cron.sh

echo ""
echo "📊 Results:"
tail -10 ~/.npm-audit.log
