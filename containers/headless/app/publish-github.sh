#!/usr/bin/env bash
# Delivers a Swarm job as a GitHub pull request: Swarm runs this as a folder's
# publish command (install.sh workspace publish PATH COMMAND). It pushes the
# job's head commit to a branch and opens or updates the pull request, as a
# GitHub App installed on that one repository, and prints the pull request URL
# on its last line. Merging stays with GitHub: with AUTO_MERGE=on it turns on
# GitHub's auto-merge, so the repository's own required checks decide.
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
#     BASE_BRANCH=main                 (optional: the branch pull requests target;
#                                       default: the branch the job started from)
# The App needs Contents read/write and Pull requests read/write on the
# repository only; branch rules on the base branch keep it from pushing there.
set -euo pipefail

conf="${PUBLISH_GITHUB_CONF:-$0.conf}"
api="${PUBLISH_GITHUB_API:-https://api.github.com}"
git_base="${PUBLISH_GITHUB_GIT:-https://github.com}"
die() { echo "publish-github: $*" >&2; exit 1; }

[[ -r $conf ]] || die "settings file $conf is missing"
REPO='' APP_ID='' INSTALLATION_ID='' KEY_FILE='' BRANCH_PREFIX='ai/swarm-' AUTO_MERGE=off MERGE_METHOD=squash BASE_BRANCH=''
while IFS='=' read -r key value; do
  case $key in
    REPO|APP_ID|INSTALLATION_ID|KEY_FILE|BRANCH_PREFIX|AUTO_MERGE|MERGE_METHOD|BASE_BRANCH) printf -v "$key" '%s' "$value" ;;
    ''|'#'*) ;;
    *) die "unknown setting $key in $conf" ;;
  esac
done <"$conf"
[[ $REPO =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "REPO must be owner/name"
[[ $APP_ID =~ ^[0-9]+$ && $INSTALLATION_ID =~ ^[0-9]+$ ]] || die "APP_ID and INSTALLATION_ID must be numbers"
[[ -r $KEY_FILE ]] || die "KEY_FILE is not readable"
[[ $BRANCH_PREFIX =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "BRANCH_PREFIX is not a branch name prefix"
[[ $AUTO_MERGE == on || $AUTO_MERGE == off ]] || die "AUTO_MERGE must be on or off"
[[ $MERGE_METHOD =~ ^(merge|squash|rebase)$ ]] || die "MERGE_METHOD must be merge, squash or rebase"
[[ -z $BASE_BRANCH || $BASE_BRANCH =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "BASE_BRANCH is not a branch name"

folder="${SWARM_PUBLISH_FOLDER:?}" head="${SWARM_PUBLISH_HEAD:?}" job="${SWARM_PUBLISH_JOB_ID:?}"
base="${BASE_BRANCH:-${SWARM_PUBLISH_BASE_BRANCH:-main}}" title="${SWARM_PUBLISH_TITLE:-Swarm job}" body_file="${SWARM_PUBLISH_BODY_FILE:-/dev/null}"
[[ $head =~ ^[0-9a-f]{40}$ ]] || die "SWARM_PUBLISH_HEAD is not a commit id"
# The folder belongs to its owner, not Swarm's user: trust it for these commands only.
git_folder() { git -c safe.directory="$folder" -C "$folder" "$@"; }
git_folder cat-file -e "$head^{commit}" || die "commit $head is not in $folder"
# One branch per job: a follow-up of the same job updates the same pull request.
branch="$BRANCH_PREFIX$(printf '%s' "$job" | sha256sum | cut -c1-12)"

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
TOKEN=$(github POST "/app/installations/$INSTALLATION_ID/access_tokens" \
  '{"permissions":{"contents":"write","pull_requests":"write","metadata":"read"}}' | jq -er .token) || die "the App could not get a token for $REPO"

# Push with the token in the environment only, never on a command line.
basic=$(printf 'x-access-token:%s' "$TOKEN" | openssl base64 -A)
GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0="http.https://github.com/.extraheader" GIT_CONFIG_VALUE_0="AUTHORIZATION: basic $basic" \
  GIT_TERMINAL_PROMPT=0 git_folder -c credential.helper= push --force --quiet "$git_base/$REPO.git" "$head:refs/heads/$branch" \
  || die "push of $head to $branch failed"
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
if [[ $AUTO_MERGE == on ]]; then
  query='mutation($id:ID!,$m:PullRequestMergeMethod!){enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:$m}){clientMutationId}}'
  if github POST /graphql "$(jq -n --arg q "$query" --arg id "$node" --arg m "${MERGE_METHOD^^}" '{query: $q, variables: {id: $id, m: $m}}')" | jq -e '.errors | not' >/dev/null; then
    echo "auto-merge on: GitHub merges it when the required checks pass"
  else
    echo "auto-merge could not be turned on (allow auto-merge in the repository settings)" >&2
  fi
fi
echo "$url"
