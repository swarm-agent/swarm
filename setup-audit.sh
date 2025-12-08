#!/bin/bash
# Setup npm/bun audit monitoring - v3 (systemd timer)

echo "🛡️ Setting up security audit monitoring..."

# Create the audit script
mkdir -p ~/.local/bin
cat > ~/.local/bin/npm-audit-cron.sh << 'SCRIPT'
#!/bin/bash
LOG_FILE="$HOME/.npm-audit.log"
PROJECTS=(
    "$HOME/swarm_agent_prod"
    "$HOME/swarm-cli"
    "$HOME/todo"
)

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
    [[ -d "$p" ]] && scan_project "$p"
    for s in "$p"/*/; do
        [[ -f "${s}package.json" ]] && scan_project "$s"
    done
done
SCRIPT

chmod +x ~/.local/bin/npm-audit-cron.sh
echo "✅ Created ~/.local/bin/npm-audit-cron.sh"

# Setup systemd user timer (every 3 hours)
mkdir -p ~/.config/systemd/user

cat > ~/.config/systemd/user/npm-audit.service << 'SERVICE'
[Unit]
Description=NPM/Bun security audit

[Service]
Type=oneshot
ExecStart=%h/.local/bin/npm-audit-cron.sh
Environment=PATH=/home/roy/.bun/bin:/usr/local/bin:/usr/bin
SERVICE

cat > ~/.config/systemd/user/npm-audit.timer << 'TIMER'
[Unit]
Description=Run npm audit every 3 hours

[Timer]
OnBootSec=2min
OnUnitActiveSec=3h
Persistent=true

[Install]
WantedBy=timers.target
TIMER

systemctl --user daemon-reload
systemctl --user enable --now npm-audit.timer
echo "✅ Enabled systemd timer (every 3 hours)"

# Add to Hyprland autostart if not already there
HYPR_CONF="$HOME/.config/hypr/hyprland.conf"
if [[ -f "$HYPR_CONF" ]] && ! grep -q "npm-audit-cron" "$HYPR_CONF"; then
    echo "" >> "$HYPR_CONF"
    echo "# Security audit on startup" >> "$HYPR_CONF"
    echo "exec-once = sleep 30 && ~/.local/bin/npm-audit-cron.sh" >> "$HYPR_CONF"
    echo "✅ Added to Hyprland autostart"
else
    echo "ℹ️  Hyprland autostart already configured or not found"
fi

# Run initial scan
echo ""
echo "🔍 Running initial scan..."
~/.local/bin/npm-audit-cron.sh

echo ""
echo "📊 Results:"
tail -10 ~/.npm-audit.log

echo ""
echo "✅ Setup complete!"
echo "   - Timer: every 3 hours (systemd)"
echo "   - Also runs: 2min after boot"
echo "   - Hyprland: 30s after session starts"
echo "   - Projects: swarm_agent_prod, swarm-cli, todo"
echo ""
echo "📋 Commands:"
echo "   systemctl --user status npm-audit.timer  # Check timer"
echo "   systemctl --user list-timers             # List all timers"
echo "   journalctl --user -u npm-audit           # View logs"
