#!/bin/bash
# Drives the console on a virtual display, never on your screen, from a
# script of lines on stdin:
#   key NAME        press a key (xdotool names: Return, Tab, Escape, ctrl+v)
#   type TEXT       type text into the focused field
#   click X Y       click at window coordinates
#   wheel X Y [N] [B]  scroll at X Y: N notches (5), B 5 down or 4 up
#   shot NAME       screenshot to $DRIVE_OUT/NAME.png (default: ./target/drive)
#   sleep SECONDS
#   log             print the app's log so far
#   quit
# The environment passes through to the app (SWARM_CONSOLE_ONBOARDING=1,
# SWARM_CONSOLE_SSH, SWARM_CONSOLE_INSTALLER, ...), as scripts/dogfood.sh
# sets it. Needs Xvfb, i3, xdotool, xwd and ffmpeg (see scripts/snap.sh).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
bin=$root/target/debug/swarm-console
out=${DRIVE_OUT:-$root/target/drive}
mkdir -p "$out"
[[ -x $bin ]] || cargo build --manifest-path "$root/Cargo.toml"
work=$(mktemp -d "${TMPDIR:-/tmp}/drive.XXXXXX")
printf '# i3 config file (v4)\nipc-socket %s/i3.sock\nfont pango:monospace 8\nfor_window [class=".*"] floating enable, border none, move position 0 0\n' "$work" >"$work/i3.conf"
display=:$((80 + RANDOM % 9))
Xvfb "$display" -screen 0 1480x900x24 -nolisten tcp >/dev/null 2>&1 &
xvfb=$!
trap 'kill ${app:-} ${wm:-} "$xvfb" 2>/dev/null || true; rm -rf "$work"' EXIT
for _ in $(seq 50); do xdpyinfo -display "$display" >/dev/null 2>&1 && break; sleep 0.1; done
DISPLAY=$display i3 -c "$work/i3.conf" >/dev/null 2>&1 &
wm=$!
sleep 0.5
lvp=/usr/share/vulkan/icd.d/lvp_icd.json
env -u WAYLAND_DISPLAY DISPLAY="$display" \
  VK_ICD_FILENAMES="$([[ -f $lvp ]] && echo "$lvp" || echo "${VK_ICD_FILENAMES:-}")" \
  "$bin" >"$out/app.log" 2>&1 &
app=$!
wid=$(DISPLAY=$display timeout 30 xdotool search --all --sync --pid "$app" --name "Swarm Console" | head -1)
sleep 2
shot() {
  xwd -silent -display "$display" -id "$wid" -out "$work/shot.xwd" </dev/null
  ffmpeg -nostdin -loglevel error -y -i "$work/shot.xwd" -frames:v 1 "$out/$1.png"
  echo "shot $out/$1.png"
}
while read -r cmd rest; do
  case $cmd in
    key) DISPLAY=$display xdotool windowactivate --sync "$wid" key $rest </dev/null; sleep 0.4 ;;
    # xdotool drops long strings on this display: typed in pieces.
    type) DISPLAY=$display xdotool windowactivate --sync "$wid" </dev/null
          while [[ -n $rest ]]; do DISPLAY=$display xdotool type --delay 12 "${rest:0:24}" </dev/null; rest=${rest:24}; done; sleep 0.3 ;;
    click) DISPLAY=$display xdotool mousemove --window "$wid" $rest click 1 </dev/null; sleep 0.5 ;;
    # paste TEXT: through the clipboard (ctrl-v), for long text.
    wheel) set -- $rest; DISPLAY=$display xdotool mousemove --window "$wid" "$1" "$2" click --repeat "${3:-5}" "${4:-5}" </dev/null; sleep 0.4 ;;
    paste) printf "%s" "$rest" | DISPLAY=$display xsel --clipboard --input; DISPLAY=$display xdotool windowactivate --sync "$wid" key ctrl+v </dev/null; sleep 0.4 ;;
    shot) shot "$rest" ;;
    sleep) sleep "$rest" ;;
    log) cat "$out/app.log" ;;
    quit) break ;;
    "") ;;
    *) echo "unknown: $cmd" >&2 ;;
  esac
  kill -0 "$app" 2>/dev/null || { echo "the app exited"; cat "$out/app.log" | tail -20; break; }
done
