#!/usr/bin/env python3
"""Read-only GCP check consumer; never imports or executes candidate source.

Required env: GITHUB_TOKEN, GITHUB_REPOSITORY, GITHUB_SHA, GITHUB_REF,
GITHUB_EVENT_NAME, GITHUB_EVENT_PATH, GITHUB_RUN_ID, GCP_CHECK_APP_ID,
GCP_CHECK_CONTEXT. Optional GCP_RESULT_PATH writes the verified JSON.
Producer freshness fields: event_name, before_sha (push), logical_run and
invocation_created_at (dispatch/schedule). Full qualification contexts require
GCP_REQUIRED_STAGES as a reviewed JSON array, NOT candidate/receipt-derived.
Run this script from a trusted immutable checkout. No redirects are accepted.
"""
import datetime
import json
import os
import re
import sys
import time
import urllib.parse
import urllib.request


class Invalid(ValueError):
    pass


def require(ok, message):
    if not ok:
        raise Invalid(message)


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'duplicate JSON key')
        result[key] = value
    return result


def decode(raw):
    require(len(raw) <= 2 * 1024 * 1024, 'JSON too large')
    return json.loads(raw, object_pairs_hook=unique,
                      parse_constant=lambda _: (_ for _ in ()).throw(Invalid('nonfinite JSON')))


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise Invalid('HTTP redirects forbidden')


def request(url, headers, data=None, limit=2 * 1024 * 1024):
    opener = urllib.request.build_opener(NoRedirect())
    with opener.open(urllib.request.Request(url, data=data, headers=headers), timeout=20) as response:
        require(response.status == 200 and response.geturl() == url, 'unexpected response')
        raw = response.read(limit + 1)
        require(len(raw) <= limit, 'response too large')
        return raw


def sha(value):
    require(isinstance(value, str) and re.fullmatch('[0-9a-f]{40}', value), 'invalid Git SHA')
    return value


def positive(value):
    require(re.fullmatch('[1-9][0-9]*', str(value)) is not None and not isinstance(value, bool), 'invalid positive ID')
    return int(value)


COMMON = {'source', 'setup', 'cleanup'}
STAGES = {
    'critical-tests': COMMON | {'repository-policy', 'critical-fast', 'critical-deep', 'critical-agents'},
    'guard-main-pr-source': COMMON | {'main-source-policy'},
    'require-changelog': COMMON | {'changelog'},
    'dependency-vulnerability-scan': COMMON | {'dependency-vulnerabilities'},
    'install-distro-smoke': COMMON | {'version', 'build', 'package-smoke', 'ubuntu-sudo', 'arch-sudo'},
}


def stage_policy(context, env):
    if context in STAGES:
        return STAGES[context]
    require(context in ('build-main', 'release-candidate'), 'unknown check context')
    values = decode(env['GCP_REQUIRED_STAGES'])
    require(isinstance(values, list) and all(isinstance(x, str) and re.fullmatch('[a-z0-9-]+', x) for x in values), 'invalid stage policy')
    require(len(values) == len(set(values)) and STAGES['install-distro-smoke'] | STAGES['critical-tests'] <= set(values), 'incomplete qualification policy')
    return set(values)


class GitHub:
    def __init__(self, env, transport=request):
        self.env, self.transport = env, transport
        self.repo = env['GITHUB_REPOSITORY']
        require(re.fullmatch('[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', self.repo), 'invalid repository')
        self.root = '/repos/' + self.repo

    def get(self, path):
        require(path.startswith(self.root + '/') or path == self.root, 'invalid API path')
        return decode(self.transport('https://api.github.com' + path, {
            'Authorization': 'Bearer ' + self.env['GITHUB_TOKEN'],
            'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28'}))

    def identity(self, event):
        env = self.env
        repository_id = positive(self.get(self.root)['id'])
        require(repository_id == positive(event['repository']['id']), 'event repository mismatch')
        name = env['GITHUB_EVENT_NAME']
        head = sha(env['GITHUB_SHA'])
        expected = {'repository_id': repository_id, 'event_name': name}
        if name == 'pull_request':
            pr = self.get(self.root + '/pulls/' + str(positive(event['number'])))
            require(pr['state'] == 'open', 'PR no longer open')
            source, base = sha(pr['head']['sha']), sha(pr['base']['sha'])
            require(source == event['pull_request']['head']['sha'] and base == event['pull_request']['base']['sha'], 'stale PR event')
            require(head == sha(pr['merge_commit_sha']), 'stale synthetic merge')
            commit = self.get(self.root + '/git/commits/' + head)
            require([p['sha'] for p in commit['parents']] == [base, source], 'merge parents mismatch')
        else:
            require(name in ('push', 'workflow_dispatch', 'schedule'), 'unsupported event')
            ref = env['GITHUB_REF']
            require(ref in ('refs/heads/main', 'refs/heads/dev'), 'unsupported branch')
            current = self.get(self.root + '/git/ref/' + ref[5:])
            require(current['object']['sha'] == head, 'stale branch head')
            source = head
            commit = self.get(self.root + '/git/commits/' + head)
            if name == 'push':
                base = sha(event['before'])
                require(event['after'] == head and event['ref'] == ref and base != '0' * 40, 'invalid push input')
                expected['before_sha'] = base
            else:
                require(commit['parents'], 'missing comparison base')
                base = sha(commit['parents'][0]['sha'])
                run_id = positive(env['GITHUB_RUN_ID'])
                run = self.get(self.root + '/actions/runs/' + str(run_id))
                require(run['id'] == run_id and run['head_sha'] == head and run['event'] == name, 'workflow invocation mismatch')
                datetime.datetime.fromisoformat(run['created_at'].replace('Z', '+00:00'))
                expected.update(logical_run='github-' + str(run_id), invocation_created_at=run['created_at'])
        tree = sha(commit['tree']['sha'])
        source_tree = sha(self.get(self.root + '/git/commits/' + source)['tree']['sha'])
        expected.update(head_sha=source, base_sha=base, execution_sha=head,
                        source_tree=source_tree, execution_tree=tree)
        return expected


def verify_check(check, expected, context, app_id, stages):
    require(check['app']['id'] == app_id and type(check['app']['id']) is int, 'wrong App')
    require(check['head_sha'] == expected['head_sha'], 'wrong check head')
    text = check['output']['text']
    require(isinstance(text, str) and len(text.encode()) <= 65536, 'oversized check evidence')
    result = decode(text)
    allowed = {'schema', 'repository_id', 'head_sha', 'base_sha', 'execution_sha', 'source_tree', 'execution_tree', 'input_digest', 'run_id', 'context', 'stages', 'state', 'cleanup_verified', 'diagnostics', 'release_handoff', 'event_name', 'before_sha', 'logical_run', 'invocation_created_at'}
    require(set(result) <= allowed, 'unknown check evidence fields')
    require(result['schema'] == 'swarm.gcp.check-result/v1' and result['context'] == context, 'wrong result contract')
    require(all(result.get(k) == v and type(result.get(k)) is type(v) for k, v in expected.items()), 'stale result identity')
    require(re.fullmatch('[0-9a-f]{64}', result['input_digest']), 'invalid input digest')
    require(re.fullmatch('[A-Za-z0-9_.:-]{1,160}', result['run_id']), 'invalid run identity')
    rows = result['stages']
    require(isinstance(rows, list) and all(isinstance(x, dict) and set(x) == {'id', 'status'} and isinstance(x['id'], str) for x in rows), 'invalid stages')
    require(len(rows) == len(stages) and {x['id'] for x in rows} == stages, 'incomplete or duplicate stages')
    require(all(x['status'] in ('pending', 'running', 'passed', 'failed', 'cancelled') for x in rows), 'unknown stage status')
    require(result['state'] in ('queued', 'running', 'success', 'failure', 'cancelled'), 'unknown state')
    if check['status'] == 'completed':
        require(check['conclusion'] == 'success' and result['state'] == 'success' and result['cleanup_verified'] is True and all(x['status'] == 'passed' for x in rows), 'qualification failed')
        return result, True
    require(check['status'] in ('queued', 'in_progress') and check.get('conclusion') is None and result['state'] in ('queued', 'running'), 'inconsistent check state')
    return result, False


def poll(env, event, transport=request, sleep=time.sleep, clock=time.monotonic):
    api = GitHub(env, transport)
    context = env['GCP_CHECK_CONTEXT']
    app = positive(env['GCP_CHECK_APP_ID'])
    stages = stage_policy(context, env)
    expected = api.identity(event)
    deadline, pinned = clock() + 2400, None
    while clock() < deadline:
        require(api.identity(event) == expected, 'input changed while polling')
        path = api.root + '/commits/' + expected['head_sha'] + '/check-runs?per_page=100&filter=all&check_name=' + urllib.parse.quote(context, safe='')
        response = api.get(path)
        require(response['total_count'] <= 100 and len(response['check_runs']) == response['total_count'], 'ambiguous paginated checks')
        matches = [c for c in response['check_runs'] if c['name'] == context and c['app']['id'] == app]
        require(len(matches) <= 1, 'ambiguous producer checks')
        if matches:
            result, done = verify_check(matches[0], expected, context, app, stages)
            identity = (matches[0]['id'], result['run_id'], result['input_digest'])
            require(pinned is None or pinned == identity, 'producer identity changed')
            pinned = identity
            if done:
                require(api.identity(event) == expected, 'input changed at completion')
                return result
        sleep(min(30, max(0, deadline - clock())))
    raise Invalid('GCP qualification timed out after 40 minutes')


def main():
    with open(os.environ['GITHUB_EVENT_PATH'], 'rb') as stream:
        event = decode(stream.read(2 * 1024 * 1024 + 1))
    result = poll(os.environ, event)
    if os.environ.get('GCP_RESULT_PATH'):
        with open(os.environ['GCP_RESULT_PATH'], 'x', encoding='utf-8') as stream:
            json.dump(result, stream, sort_keys=True)
    print('Verified exact-head GCP qualification: ' + result['context'])


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('GCP result verification failed (credentials and producer diagnostics withheld)', file=sys.stderr)
        sys.exit(1)
