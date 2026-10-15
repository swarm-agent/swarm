#!/usr/bin/env bash
# Purpose: exercise workspace's real publish parser with every supported option,
# preventing argument-count rejection; invalid flags must preserve saved policy.
# Only host ownership/ACL prerequisites are adapted for an unprivileged fixture.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
STATE=$work/state PUBLISH=$work/publish
workspace_listed() { echo /srv/project; }
setfacl() { :; }
# The test writes only its temporary policy, without requiring host root access.
eval "$(declare -f workspace | sed '/\[\[ $EUID -eq 0 \]\]/d')"
install() { if [[ $1 == -d ]]; then command install "$@"; else command install -m 0644 /dev/null "${@: -1}"; fi; }
stat() { case $2 in %u) echo 0;; %a) echo 755;; *) return 1;; esac; }
workspace publish /srv/project /usr/bin/true --land --require-review --require-proof --auto --sync
expected=$'/srv/project\t/usr/bin/true\tland,require-review,require-proof,auto,sync'
[[ $(cat "$PUBLISH") == "$expected" ]]
if (workspace publish /srv/project /usr/bin/true --land --unknown) >"$work/error" 2>&1; then
  echo 'invalid option accepted' >&2; exit 1
fi
[[ $(cat "$PUBLISH") == "$expected" ]]
echo 'publish installer tests passed'
