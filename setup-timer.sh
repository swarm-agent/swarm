#!/bin/bash
# Setup systemd timer for npm audit (every 3 hours)

mkdir -p ~/.config/systemd/user

cat > ~/.config/systemd/user/npm-audit.service << 'EOF'
[Unit]
Description=NPM/Bun security audit

[Service]
Type=oneshot
ExecStart=%h/.local/bin/npm-audit-cron.sh
Environment=PATH=/home/roy/.bun/bin:/usr/local/bin:/usr/bin
EOF

cat > ~/.config/systemd/user/npm-audit.timer << 'EOF'
[Unit]
Description=Run npm audit every 3 hours

[Timer]
OnBootSec=2min
OnUnitActiveSec=3h
Persistent=true

[Install]
WantedBy=timers.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now npm-audit.timer
echo "✅ Timer enabled:"
systemctl --user list-timers | grep -E "(NEXT|npm-audit)"
