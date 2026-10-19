#!/usr/bin/env bash
# Hermetic test of publish-github.sh: a fake GitHub API (node) and a local bare
# repository stand in for GitHub; no network. Run: bash publish-github.test.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'kill "${api_pid:-}" 2>/dev/null || true; rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# The job's folder with one commit, and the "GitHub" repository it pushes to.
git init -q -b main "$work/folder"
git -C "$work/folder" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "refresh models"
head=$(git -C "$work/folder" rev-parse HEAD)
git init -q --bare "$work/git/example/models.git"

openssl genrsa -out "$work/app.pem" 2048 2>/dev/null
cat > "$work/publish-github.conf" <<EOF
REPO=example/models
APP_ID=42
INSTALLATION_ID=7
KEY_FILE=$work/app.pem
AUTO_MERGE=on
BASE_BRANCH=main
EOF

cat > "$work/api.mjs" <<'EOF'
import http from 'node:http';
import fs from 'node:fs';
const [log, portFile, conflictFile] = process.argv.slice(2);
let pr = null;
http.createServer((req, res) => {
  let body = '';
  req.on('data', c => body += c);
  req.on('end', () => {
    fs.appendFileSync(log, JSON.stringify({ method: req.method, url: req.url, auth: req.headers.authorization || '', body }) + '\n');
    const send = (code, value) => { res.writeHead(code, { 'content-type': 'application/json' }); res.end(JSON.stringify(value)); };
    if (req.method === 'POST' && req.url === '/app/installations/7/access_tokens') return send(201, { token: 'ghs_testtoken' });
    if (req.headers.authorization !== 'Bearer ghs_testtoken') return send(401, { message: 'bad token' });
    if (req.method === 'GET' && req.url.startsWith('/repos/example/models/pulls?')) return send(200, pr ? [pr] : []);
    if (req.method === 'POST' && req.url === '/repos/example/models/pulls') { pr = { number: 5, html_url: 'https://github.com/example/models/pull/5', node_id: 'PR_5' }; return send(201, pr); }
    if (req.method === 'PATCH' && req.url === '/repos/example/models/pulls/5') return send(200, pr);
    if (req.method === 'GET' && req.url === '/repos/example/models/pulls/5') return send(200, { ...pr, mergeable: !fs.existsSync(conflictFile) });
    if (req.method === 'POST' && req.url === '/graphql') return send(200, { data: { enablePullRequestAutoMerge: { clientMutationId: null } } });
    send(404, { message: 'not found' });
  });
}).listen(0, '127.0.0.1', function () { fs.writeFileSync(portFile, String(this.address().port)); });
EOF
node "$work/api.mjs" "$work/requests.log" "$work/port" "$work/conflict" & api_pid=$!
for _ in $(seq 50); do [[ -s $work/port ]] && break; sleep 0.1; done
[[ -s $work/port ]] || fail "fake API did not start"

run() {
  printf 'models changed\n' > "$work/body"
  PUBLISH_GITHUB_CONF="$work/publish-github.conf" PUBLISH_GITHUB_API="http://127.0.0.1:$(cat "$work/port")" PUBLISH_GITHUB_GIT="file://$work/git" \
    SWARM_PUBLISH_FOLDER="$work/folder" SWARM_PUBLISH_JOB_ID="worker/w1/r1" SWARM_PUBLISH_BRANCH=agent/worker-r1 \
    SWARM_PUBLISH_HEAD="$head" SWARM_PUBLISH_BASE_BRANCH="${SWARM_PUBLISH_BASE_BRANCH:-dev}" SWARM_PUBLISH_TITLE="Refresh models" SWARM_PUBLISH_BODY_FILE="$work/body" \
    bash "$here/publish-github.sh"
}

# The folder's own pre-push hook runs by default; PUSH_HOOKS=off skips it.
mkdir -p "$work/folder/.git/hooks"
printf '#!/bin/sh\necho "pre-push gate refused" >&2\nexit 1\n' > "$work/folder/.git/hooks/pre-push"
chmod +x "$work/folder/.git/hooks/pre-push"
if out=$(run 2>&1); then fail "pushed past a failing pre-push hook with PUSH_HOOKS on: $out"; fi
grep -q "pre-push gate refused" <<<"$out" || fail "the pre-push hook did not run: $out"
echo PUSH_HOOKS=off >> "$work/publish-github.conf"

out=$(run) || fail "first run failed: $out"
[[ $(tail -n1 <<<"$out") == https://github.com/example/models/pull/5 ]] || fail "last line is not the pull request URL: $out"
grep -q "opened pull request #5" <<<"$out" || fail "did not open a pull request: $out"
grep -q "auto-merge on" <<<"$out" || fail "did not turn on auto-merge: $out"
branch=$(git --git-dir="$work/git/example/models.git" for-each-ref --format='%(refname:short)' refs/heads/)
[[ $branch == ai/swarm-* ]] || fail "pushed branch $branch is not under ai/swarm-"
[[ $(git --git-dir="$work/git/example/models.git" rev-parse "$branch") == "$head" ]] || fail "pushed branch is not the job's head"
jq -se 'any(.[]; .url == "/repos/example/models/pulls" and (.body | fromjson | .head | startswith("ai/swarm-")) and (.body | fromjson | .base == "main") and (.body | fromjson | .body | contains("models changed")))' "$work/requests.log" >/dev/null \
  || fail "pull request body or branches are wrong"
jq -se 'any(.[]; .url == "/app/installations/7/access_tokens" and (.auth | startswith("Bearer ey")))' "$work/requests.log" >/dev/null || fail "token call did not use the App JWT"

# The same job again (a follow-up) updates the same pull request.
out=$(run) || fail "second run failed: $out"
grep -q "updated pull request #5" <<<"$out" || fail "a second delivery did not update the pull request: $out"
[[ $(grep -c '"url":"/repos/example/models/pulls"' "$work/requests.log") == 1 ]] || fail "opened a second pull request"

# A pull request that conflicts with its base fails the delivery, naming it.
touch "$work/conflict"
if out=$(run 2>&1); then fail "a conflicting pull request was reported as delivered: $out"; fi
grep -q "pull request #5 (https://github.com/example/models/pull/5) conflicts with main" <<<"$out" || fail "the conflict is not named: $out"
rm "$work/conflict"

# Update (sync): the forge's main moved on; the folder gets it and is told where to start.
git --git-dir="$work/git/example/models.git" fetch -q "$work/folder" "$head:refs/heads/main"
merged=$(git --git-dir="$work/git/example/models.git" -c user.name=t -c user.email=t@example.invalid commit-tree "$head^{tree}" -p "$head" -m "merged on the forge")
git --git-dir="$work/git/example/models.git" update-ref refs/heads/main "$merged"
out=$(PUBLISH_GITHUB_CONF="$work/publish-github.conf" PUBLISH_GITHUB_API="http://127.0.0.1:$(cat "$work/port")" PUBLISH_GITHUB_GIT="file://$work/git" \
  SWARM_PUBLISH_ACTION=update SWARM_PUBLISH_FOLDER="$work/folder" SWARM_PUBLISH_BASE_BRANCH=dev bash "$here/publish-github.sh") || fail "update failed: $out"
[[ $(tail -n1 <<<"$out") == "$merged main" ]] || fail "update did not print the forge's main: $out"
[[ $(git -C "$work/folder" rev-parse refs/swarm/publish/main) == "$merged" ]] || fail "the forge's main is not in the folder"
[[ $(git -C "$work/folder" rev-parse HEAD) == "$head" ]] || fail "update moved the owner's checkout"
jq -se 'any(.[]; .url == "/app/installations/7/access_tokens" and (.body | fromjson | .permissions == {"contents":"read","metadata":"read"}))' "$work/requests.log" >/dev/null \
  || fail "update asked for more than read access"

# The removed land action is refused before any token request or push.
before=$(wc -l <"$work/requests.log")
if out=$(SWARM_PUBLISH_ACTION=land run 2>&1); then fail "land action accepted: $out"; fi
grep -q 'SWARM_PUBLISH_ACTION must be publish or update' <<<"$out" || fail "unclear land refusal: $out"
[[ $(wc -l <"$work/requests.log") == "$before" ]] || fail "land action reached the API"

# The token only goes to curl on stdin and to git in the environment, never on a
# command line: its only uses are those two.
[[ $(grep -cE '\$\{?TOKEN' "$here/publish-github.sh") == 2 ]] || fail "the token is used somewhere new; keep it off command lines"
echo "publish-github: all checks passed"
