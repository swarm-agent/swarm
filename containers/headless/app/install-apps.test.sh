#!/usr/bin/env bash
# Purpose: an app (a console or any frontend) driving the installer gets
# machine-readable progress, a key of its own that never replaces or revokes
# the person's AI key, and is recognized when it asks through pkexec.
# Stubs only host boundaries (root ownership, runuser, swarmctl); no daemon.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
work=$(mktemp -d "${TMPDIR:?}/install-apps.XXXXXX")
trap 'rm -rf "$work"' EXIT
command -v jq >/dev/null || { echo 'jq is required for this test' >&2; exit 1; }

# Progress lines are JSON, carry the step id and text, and survive quotes,
# backslashes and newlines in the text.
PROGRESS=json
line=$(step build 'Building "dev" \ part
two' | grep '^{"swarm_progress"')
[[ $(jq -r '.swarm_progress.step' <<<"$line") == build ]]
[[ $(jq -r '.swarm_progress.text' <<<"$line") == 'Building "dev" \ part
two' ]]
err=$( (die 'no "network"') 2>/dev/null || true)
[[ $(jq -r '.swarm_progress.error' <<<"$err") == 'no "network"' ]]
PROGRESS=off
[[ -z $(step fetch 'Fetching' | grep '^{' || true) ]]

# main takes --progress=json in any position and removes it before dispatch.
usage() { echo "usage called with $#"; }
out=$(main help --progress=json)
[[ $out == 'usage called with 0' ]]
( main --progress=json help >/dev/null; [[ $PROGRESS == json ]] )

# App names are what the key file is named after: no paths, no surprises.
[[ $(app_name swarm-console) == swarm-console ]]
for bad in '' Bad_Name ../x 'a b' -lead "$(printf 'a%.0s' {1..41})"; do
  if (app_name "$bad" >/dev/null 2>&1); then echo "app name accepted: $bad" >&2; exit 1; fi
done

# The account that asked: sudo first, then pkexec's uid.
me=$(id -nu)
[[ $(SUDO_USER=someone PKEXEC_UID=$(id -u) invoking_user) == someone ]]
[[ $(SUDO_USER='' PKEXEC_UID=$(id -u) invoking_user) == "$me" ]]
[[ -z $(SUDO_USER='' PKEXEC_UID='' invoking_user) ]]

# Keys: the AI key and each app key live in their own files and replace only
# their own previous key.
person_dir=$work/home
mkdir -p "$person_dir"
AI_ACCESS=on AI_KEY_SECONDS=60 AI_KEY_IDS=$work/ids
SWARMCTL=$work/swarmctl
cat >"$SWARMCTL" <<'STUB'
#!/usr/bin/env bash
dir=$(dirname "$0")
n=$(( $(cat "$dir/count" 2>/dev/null || echo 0) + 1 ))
echo "$n" >"$dir/count"
case "$1 $2" in
  "setup sdk-token") printf '{"token":"swk_test%s","id":"tok_%s"}\n' "$n" "$n" ;;
  "setup revoke-sdk-token") echo "$4" >>"$dir/revoked" ;;
  *) exit 2 ;;
esac
STUB
chmod +x "$SWARMCTL"
ensure_owner() { :; }
person_home() { printf "%s" "$person_dir"; }
runuser() { shift 3; "$@"; }
install() {
  local -a args=()
  while (($#)); do case $1 in -o|-g) shift 2 ;; *) args+=("$1"); shift ;; esac; done
  command install "${args[@]}"
}
mint_ai_key "$me" write >/dev/null
mint_ai_key "$me" full swarm-console >/dev/null
ai=$(<"$person_dir/.config/swarm/ai-key")
app=$(<"$person_dir/.config/swarm/apps/swarm-console.key")
[[ $ai == swk_test1 && $app == swk_test2 ]]
[[ $(stat -c %a "$person_dir/.config/swarm/apps/swarm-console.key") == 600 ]]
[[ $(stat -c %a "$person_dir/.config/swarm/apps") == 700 ]]
[[ ! -f $work/revoked ]]
mint_ai_key "$me" full swarm-console >/dev/null
[[ $(<"$person_dir/.config/swarm/apps/swarm-console.key") == swk_test3 ]]
[[ $(<"$person_dir/.config/swarm/ai-key") == swk_test1 ]]
[[ $(cat "$work/revoked") == tok_2 ]]
mint_ai_key "$me" write >/dev/null
[[ $(<"$person_dir/.config/swarm/apps/swarm-console.key") == swk_test3 ]]
[[ $(tail -1 "$work/revoked") == tok_1 ]]
echo 'app keys, progress lines and pkexec detection passed'
