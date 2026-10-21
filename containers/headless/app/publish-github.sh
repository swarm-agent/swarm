#!/usr/bin/env bash
# Delivers a Swarm job as a GitHub pull request: Swarm runs this as a folder's
# publish command (install.sh workspace publish PATH COMMAND). It pushes the
# job's head commit to a branch and opens or updates the pull request, as a
# GitHub App installed on that one repository, and prints the pull request URL
# on its last line. Merging stays with GitHub: with AUTO_MERGE=on it turns on
# GitHub's auto-merge, so the repository's own required checks decide. A pull
# request that conflicts with its base fails the delivery, so Swarm reports it.
# With SWARM_PUBLISH_ACTION=update (a folder published with --sync, before each
# job) it fetches the base branch into refs/swarm/publish/BRANCH in the folder
# and prints `COMMIT BRANCH`: the job starts from the forge's latest base.
#
# Install it root-owned outside home folders, e.g.
#   install -m 0755 -o root -g root publish-github.sh /usr/local/lib/swarm/publish-github
# with its settings next to it, root-owned and readable by Swarm's user only:
#   /usr/local/lib/swarm/publish-github.conf (mode 0640, group swarm)
#     REPO=owner/name
#     APP_ID=123456
#     INSTALLATION_ID=7890123
#     KEY_FILE=/usr/local/lib/swarm/publish-github.pem   (0640 root:swarm)
#     BRANCH_PREFIX=ai/swarm-          (optional; default ai/swarm-)
#     AUTO_MERGE=off                   (optional: on|off)
#     MERGE_METHOD=squash              (optional: merge|squash|rebase)
#     PUSH_HOOKS=on                    (optional: on|off; off skips the folder's own
#                                       git hooks for this push only, for a repository
#                                       whose pre-push hook runs a gate its base branch
#                                       already requires as a check)
#     BASE_BRANCH=main                 (optional: the branch pull requests target;
#                                       default: the branch the job started from)
# The App needs Contents read/write and Pull requests read/write on the
# repository only; branch rules keep it from pushing to the base.
set -euo pipefail

conf="${PUBLISH_GITHUB_CONF:-$0.conf}"
api="${PUBLISH_GITHUB_API:-https://api.github.com}"
git_base="${PUBLISH_GITHUB_GIT:-https://github.com}"
die() { echo "publish-github: $*" >&2; exit 1; }

[[ -r $conf ]] || die "settings file $conf is missing"
REPO='' APP_ID='' INSTALLATION_ID='' KEY_FILE='' BRANCH_PREFIX='ai/swarm-' AUTO_MERGE=off MERGE_METHOD=squash BASE_BRANCH='' PUSH_HOOKS=on
while IFS='=' read -r key value; do
  case $key in
    REPO|APP_ID|INSTALLATION_ID|KEY_FILE|BRANCH_PREFIX|AUTO_MERGE|MERGE_METHOD|BASE_BRANCH|PUSH_HOOKS) printf -v "$key" '%s' "$value" ;;
    ''|'#'*) ;;
    *) die "unknown setting $key in $conf" ;;
  esac
done <"$conf"
[[ $REPO =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "REPO must be owner/name"
[[ $APP_ID =~ ^[0-9]+$ && $INSTALLATION_ID =~ ^[0-9]+$ ]] || die "APP_ID and INSTALLATION_ID must be numbers"
[[ -r $KEY_FILE ]] || die "KEY_FILE is not readable"
[[ $BRANCH_PREFIX =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "BRANCH_PREFIX is not a branch name prefix"
[[ $AUTO_MERGE == on || $AUTO_MERGE == off ]] || die "AUTO_MERGE must be on or off"
[[ $PUSH_HOOKS == on || $PUSH_HOOKS == off ]] || die "PUSH_HOOKS must be on or off"
[[ $MERGE_METHOD =~ ^(merge|squash|rebase)$ ]] || die "MERGE_METHOD must be merge, squash or rebase"
[[ -z $BASE_BRANCH || $BASE_BRANCH =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "BASE_BRANCH is not a branch name"

# Repository hooks (a pre-push release gate) need a scratch folder.
if [[ -z ${TMPDIR:-} ]]; then TMPDIR=$(mktemp -d) && export TMPDIR && trap 'rm -rf "$TMPDIR"' EXIT; fi
action="${SWARM_PUBLISH_ACTION:-publish}"
[[ $action == publish || $action == update ]] || die "SWARM_PUBLISH_ACTION must be publish or update"
folder="${SWARM_PUBLISH_FOLDER:?}"
base="${BASE_BRANCH:-${SWARM_PUBLISH_BASE_BRANCH:-main}}"
[[ $base =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "the base branch is not a branch name"
# The folder belongs to its owner, not Swarm's user: trust it for these commands only.
git_folder() { git -c safe.directory="$folder" -C "$folder" "$@"; }
permissions='{"contents":"read","metadata":"read"}'
if [[ $action == publish ]]; then
  head="${SWARM_PUBLISH_HEAD:?}" job="${SWARM_PUBLISH_JOB_ID:?}"
  title="${SWARM_PUBLISH_TITLE:-Swarm job}" body_file="${SWARM_PUBLISH_BODY_FILE:-/dev/null}"
  [[ $head =~ ^[0-9a-f]{40}$ ]] || die "SWARM_PUBLISH_HEAD is not a commit id"
  git_folder cat-file -e "$head^{commit}" || die "commit $head is not in $folder"
  # One branch per job: a follow-up of the same job updates the same pull request.
  branch="$BRANCH_PREFIX$(printf '%s' "$job" | sha256sum | cut -c1-12)"
  permissions='{"contents":"write","pull_requests":"write","metadata":"read"}'
fi

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
now=$(date +%s)
jwt_body="$(printf '{"alg":"RS256","typ":"JWT"}' | b64url).$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' $((now - 60)) $((now + 540)) "$APP_ID" | b64url)"
jwt="$jwt_body.$(printf '%s' "$jwt_body" | openssl dgst -sha256 -sign "$KEY_FILE" -binary | b64url)"
github() { # github METHOD PATH [JSON] with the installation token (or the app JWT for the token call)
  local auth=${TOKEN:-} method=$1 path=$2
  [[ -n $auth ]] || auth=$jwt
  curl -sS --fail-with-body --max-time 60 -X "$method" -K - "$api$path" ${3:+--data-binary "$3"} \
    -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' <<<"header = \"Authorization: Bearer $auth\""
}
TOKEN=$(github POST "/app/installations/$INSTALLATION_ID/access_tokens" "{\"permissions\":$permissions}" | jq -er .token) \
  || die "the App could not get a token for $REPO"

# Git talks to the forge with the token in the environment only, never on a
# command line. PUSH_HOOKS=off: the base branch's required checks run the gate
# the hook would.
hooks=(); [[ $PUSH_HOOKS == on ]] || hooks=(-c core.hooksPath=/dev/null)
basic=$(printf 'x-access-token:%s' "$TOKEN" | openssl base64 -A)
git_forge() {
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0="http.https://github.com/.extraheader" GIT_CONFIG_VALUE_0="AUTHORIZATION: basic $basic" \
    GIT_TERMINAL_PROMPT=0 git -c safe.directory="$folder" -C "$folder" -c credential.helper= "${hooks[@]}" "$@"
}

# Update (before a job starts, for a folder publishing with sync): bring the
# forge's base branch into the folder and print where the job starts.
if [[ $action == update ]]; then
  ref="refs/swarm/publish/$base"
  git_forge fetch --quiet --no-tags "$git_base/$REPO.git" "+refs/heads/$base:$ref" || die "fetch of $base from $REPO failed"
  echo "$(git_folder rev-parse --verify "$ref^{commit}") $base"
  exit 0
fi

git_forge push --force --quiet "$git_base/$REPO.git" "$head:refs/heads/$branch" || die "push of $head to $branch failed"
echo "pushed $head to $branch"

owner=${REPO%%/*}
existing=$(github GET "/repos/$REPO/pulls?state=open&head=$owner:$branch" | jq -r '.[0] | select(. != null) | "\(.number) \(.html_url) \(.node_id)"')
body=$(head -c 60000 "$body_file"; printf '\n\n---\nDelivered by Swarm (job `%s`, commit `%s`).\n' "$job" "$head")
if [[ -n $existing ]]; then
  read -r number url node <<<"$existing"
  github PATCH "/repos/$REPO/pulls/$number" "$(jq -n --arg t "$title" --arg b "$body" '{title: $t, body: $b}')" >/dev/null
  echo "updated pull request #$number"
else
  created=$(github POST "/repos/$REPO/pulls" "$(jq -n --arg t "$title" --arg b "$body" --arg h "$branch" --arg base "$base" '{title: $t, body: $b, head: $h, base: $base}')")
  number=$(jq -r .number <<<"$created") url=$(jq -r .html_url <<<"$created") node=$(jq -r .node_id <<<"$created")
  echo "opened pull request #$number"
fi
# A pull request that conflicts with its base would wait forever: fail the
# delivery so Swarm reports it. GitHub computes mergeability shortly after.
mergeable=null
for _ in 1 2 3 4 5 6 7 8 9 10; do
  mergeable=$(github GET "/repos/$REPO/pulls/$number" | jq -r '.mergeable') || mergeable=unknown
  [[ $mergeable == null ]] || break
  sleep 3
done
[[ $mergeable != false ]] || die "pull request #$number ($url) conflicts with $base and cannot merge"
if [[ $AUTO_MERGE == on ]]; then
  query='mutation($id:ID!,$m:PullRequestMergeMethod!){enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:$m}){clientMutationId}}'
  if github POST /graphql "$(jq -n --arg q "$query" --arg id "$node" --arg m "${MERGE_METHOD^^}" '{query: $q, variables: {id: $id, m: $m}}')" | jq -e '.errors | not' >/dev/null; then
    echo "auto-merge on: GitHub merges it when the required checks pass"
  else
    echo "auto-merge could not be turned on (allow auto-merge in the repository settings)" >&2
  fi
fi
echo "$url"
