#!/bin/bash
# Screenshot the console on a virtual display, never on your screen.
#   scripts/snap.sh OUT.png [concept] [page]
# Env passes through: SWARM_CONSOLE_ICONS, SWARM_CONSOLE_RAIL=1,
# SWARM_CONSOLE_TRAY=1, SWARM_CONSOLE_FOCUS_CELL=1, SWARM_CONSOLE_ONBOARDING_DEMO=STAGE
# (welcome checking found missing server installing connecting provider chatgpt
# project settingup context ready), SWARM_CONSOLE_ONBOARDING_ERROR=1.
# Demo data unless SNAP_LIVE=1 (then the console connects to Swarm: this PC, or
# SWARM_CONSOLE_SSH). SNAP_WAIT seconds before the shot (default 4); SNAP_TYPE
# types that message into the active pane and presses enter, then waits
# SNAP_AFTER seconds (default 20) and takes the shot; ";;" separates several
# messages, and SNAP_ENTER=0 leaves the last one unsent.
# Needs Xvfb, i3, xdotool, xwd and ffmpeg. GPUI only paints under a window
# manager, so a private i3 (own config and IPC socket) runs inside the virtual
# display; Mesa's software Vulkan (lavapipe) renders, so no GPU is involved.
set -euo pipefail
out=${1:?usage: snap.sh OUT.png [concept] [page]}
concept=${2:-graphite}
page=${3:-sessions}
root=$(cd "$(dirname "$0")/.." && pwd)
bin=$root/target/debug/swarm-console
for tool in Xvfb i3 xdotool xwd ffmpeg; do
  command -v "$tool" >/dev/null || { echo "$tool is missing" >&2; exit 1; }
done
[[ -x $bin ]] || cargo build --manifest-path "$root/Cargo.toml"

work=$(mktemp -d "${TMPDIR:-/tmp}/snap.XXXXXX")
printf '# i3 config file (v4)\nipc-socket %s/i3.sock\nfont pango:monospace 8\nfor_window [class=".*"] floating enable, border none, move position 0 0\n' "$work" >"$work/i3.conf"

display=:$((90 + RANDOM % 9))
Xvfb "$display" -screen 0 1480x900x24 -nolisten tcp >/dev/null 2>&1 &
xvfb=$!
trap 'kill ${app:-} ${wm:-} "$xvfb" 2>/dev/null || true; rm -rf "$work"' EXIT
for _ in $(seq 50); do xdpyinfo -display "$display" >/dev/null 2>&1 && break; sleep 0.1; done
DISPLAY=$display i3 -c "$work/i3.conf" >/dev/null 2>&1 &
wm=$!
sleep 0.5

lvp=/usr/share/vulkan/icd.d/lvp_icd.json
skip=1
[[ -n ${SWARM_CONSOLE_ONBOARDING_DEMO:-}${SWARM_CONSOLE_ONBOARDING:-} ]] && skip=
env -u WAYLAND_DISPLAY DISPLAY="$display" \
  ${skip:+SWARM_CONSOLE_SKIP_ONBOARDING=1} \
  $([[ -n ${SNAP_LIVE:-} ]] || echo SWARM_CONSOLE_DEMO=1) \
  VK_ICD_FILENAMES="$([[ -f $lvp ]] && echo "$lvp" || echo "${VK_ICD_FILENAMES:-}")" \
  SWARM_CONSOLE_CONCEPT="$concept" SWARM_CONSOLE_PAGE="$page" "$bin" >"$work/app.log" 2>&1 &
app=$!
wid=$(DISPLAY=$display timeout 30 xdotool search --all --sync --pid "$app" --name "Swarm Console" | head -1)
sleep "${SNAP_WAIT:-4}"
# SNAP_BEFORE=PATH also takes a shot here, before any clicks.
if [[ -n ${SNAP_BEFORE:-} ]]; then
  xwd -silent -display "$display" -id "$wid" -out "$work/before.xwd"
  ffmpeg -loglevel error -y -i "$work/before.xwd" -frames:v 1 "$SNAP_BEFORE"
fi
# SNAP_CLICK="X Y[;X Y...]" clicks there first (window coordinates).
if [[ -n ${SNAP_CLICK:-} ]]; then
  IFS=";" read -ra clicks <<<"$SNAP_CLICK"
  for c in "${clicks[@]}"; do DISPLAY=$display xdotool mousemove --window "$wid" $c click 1; sleep 0.5; done
  sleep 1
fi
# SNAP_DRAG="X1 Y1 X2 Y2[;...]" drags from one point to the other, one drag per group.
if [[ -n ${SNAP_DRAG:-} ]]; then
  IFS=";" read -ra drags <<<"$SNAP_DRAG"
  for d in "${drags[@]}"; do
    read -r x1 y1 x2 y2 <<<"$d"
    DISPLAY=$display xdotool mousemove --window "$wid" "$x1" "$y1" mousedown 1
    for i in 1 2 3 4 5 6 7 8 9 10; do
      DISPLAY=$display xdotool mousemove --window "$wid" $((x1 + (x2 - x1) * i / 10)) $((y1 + (y2 - y1) * i / 10)); sleep 0.05
    done
    sleep 0.3; DISPLAY=$display xdotool mouseup 1; sleep 0.7
  done
fi
# SNAP_PASTE="text" puts text on the clipboard and pastes it (ctrl-v) into the
# focused field, for characters xdotool cannot type (emoji).
if [[ -n ${SNAP_PASTE:-} ]]; then
  printf '%s' "$SNAP_PASTE" | DISPLAY=$display xsel --clipboard --input
  DISPLAY=$display xdotool windowactivate --sync "$wid" key ctrl+v
  sleep 1
fi
if [[ -n ${SNAP_TYPE:-} ]]; then
  # Several lines separated by ";;" are typed and sent one after another.
  IFS=$'\n' read -rd '' -a lines < <(printf '%s' "${SNAP_TYPE//;;/$'\n'}") || true
  for i in "${!lines[@]}"; do
    DISPLAY=$display xdotool windowactivate --sync "$wid" type --delay 15 "${lines[$i]}"
    # SNAP_ENTER=0 leaves the last line typed but unsent (to see the command list).
    if (( i < ${#lines[@]} - 1 )) || [[ ${SNAP_ENTER:-1} != 0 ]]; then DISPLAY=$display xdotool key Return; sleep 1.5; fi
  done
  sleep "${SNAP_AFTER:-20}"
fi
# SNAP_KEY="ctrl+c" presses keys (xdotool names) after the clicks and drags.
if [[ -n ${SNAP_KEY:-} ]]; then
  DISPLAY=$display xdotool key --window "$wid" $SNAP_KEY; sleep 0.5
fi
# SNAP_SETTLE seconds more before the final shot.
sleep "${SNAP_SETTLE:-0}"
xwd -silent -display "$display" -id "$wid" -out "$work/shot.xwd"
ffmpeg -loglevel error -y -i "$work/shot.xwd" -frames:v 1 "$out"
# SNAP_CLIP_OUT=PATH saves the clipboard there (to check what was copied).
[[ -n ${SNAP_CLIP_OUT:-} ]] && DISPLAY=$display xsel --clipboard --output > "$SNAP_CLIP_OUT" 2>/dev/null || true
echo "$out"
