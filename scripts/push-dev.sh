#!/usr/bin/env bash
# Push local dev to origin dev when other machines land on dev all the time.
#
# 1. Rebase local dev onto the remote's dev (fetched over HTTPS, no key).
# 2. Run the full pre-push checks now, before any key is needed; a pass is
#    remembered for these commits (scripts/check-prepush.sh).
# 3. Open one SSH connection (the only key touch / PIN) and reuse it: fetch,
#    rebase onto whatever landed meanwhile, push. The pre-push hook finds the
#    commits already checked, so the push goes out in seconds. If dev moved in
#    between, it rebases and retries on the same connection.
# Stops on a rebase conflict (local dev is left as it was) and never forces.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

[[ "$(git symbolic-ref --short HEAD)" == dev ]] || { echo "push-dev: not on dev" >&2; exit 1; }
[[ -z "$(git status --porcelain --untracked-files=no)" ]] || { echo "push-dev: uncommitted changes" >&2; exit 1; }

url="$(git remote get-url origin)"
case "$url" in
  git@github.com:*) https="https://github.com/${url#git@github.com:}" ;;
  https://github.com/*) https="$url" ;;
  *) echo "push-dev: origin is not GitHub: $url" >&2; exit 1 ;;
esac

line_up() {
  local before
  before="$(git rev-parse dev)"
  if ! git rebase -q refs/remotes/origin/dev; then
    git rebase --abort || true
    git reset -q --keep "$before"
    echo "push-dev: dev conflicts with the remote's new commits; nothing pushed" >&2
    exit 1
  fi
}

git fetch -q "$https" +refs/heads/dev:refs/remotes/origin/dev
line_up
if [[ "$(git rev-list --count refs/remotes/origin/dev..dev)" == 0 ]]; then
  echo "push-dev: nothing to push; dev matches the remote"
  exit 0
fi
echo "push-dev: checking $(git rev-list --count refs/remotes/origin/dev..dev) commit(s) before anything is sent"
printf 'refs/heads/dev %s refs/heads/dev %s\n' "$(git rev-parse dev)" "$(git rev-parse refs/remotes/origin/dev)" |
  scripts/check-prepush.sh origin "$url"

ctl="$(mktemp -d)"
trap 'ssh -o ControlPath="$ctl/c" -O exit git@github.com >/dev/null 2>&1 || true; rm -rf "$ctl"' EXIT
base_ssh="$(git config core.sshCommand || echo ssh)"
export GIT_SSH_COMMAND="$base_ssh -o ControlMaster=auto -o ControlPath=$ctl/c -o ControlPersist=300"
for attempt in 1 2 3 4 5; do
  git fetch -q origin +refs/heads/dev:refs/remotes/origin/dev
  line_up
  if git push origin dev:dev; then
    echo "push-dev: pushed $(git rev-parse --short dev) to dev"
    exit 0
  fi
  echo "push-dev: dev moved while pushing; lining up again (attempt $attempt)" >&2
done
echo "push-dev: dev kept moving; nothing more pushed" >&2
exit 1
