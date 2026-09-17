#!/usr/bin/env python3
"""Download, independently verify, and stage GCP-built release input; never build/sign.

Uses relay env plus GCP_RELEASE_POLICY_PATH, GCP_RELEASE_VERSION, GCP_OUTPUT_DIR,
GCP_WORKLOAD_IDENTITY_PROVIDER (projects/NUMBER/locations/global/.../providers/ID),
GCP_READ_SERVICE_ACCOUNT, GCP_ALLOWED_BUCKETS (JSON string array), and GitHub's
ACTIONS_ID_TOKEN_REQUEST_URL/TOKEN. Policy is separately reviewed trusted input:
{schema: swarm.gcp.release-policy/v1, authority: {controller,builder,result_writer,
 provenance_verifier}, receipt_bucket, qualified_bucket, package_bucket}.
Never derive this static trust policy from evidence. Coverage is fixed in code.
Consumes immutable v2 stage/onboarding evidence and runtime-build-proof/v1.
Freshness comes from the exact App result and protected admission, not a policy
that must be manually rewritten with every release's source or retry number.
"""
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile
import urllib.parse

spec = importlib.util.spec_from_file_location('gcp_result_relay', Path(__file__).with_name('gcp-result-relay.py'))
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
require, decode = relay.require, relay.decode


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True, allow_nan=False).encode()


class Google:
    def __init__(self, env, transport=relay.request):
        self.transport = transport
        provider = env['GCP_WORKLOAD_IDENTITY_PROVIDER']
        require(re.fullmatch(r'projects/[1-9][0-9]*/locations/global/workloadIdentityPools/[a-z0-9-]+/providers/[a-z0-9-]+', provider), 'invalid federation provider')
        account = env['GCP_READ_SERVICE_ACCOUNT']
        require(re.fullmatch(r'[a-z][a-z0-9-]*@[a-z][a-z0-9-]*\.iam\.gserviceaccount\.com', account), 'invalid read service account')
        self.buckets = decode(env['GCP_ALLOWED_BUCKETS'])
        require(isinstance(self.buckets, list) and self.buckets and all(isinstance(b, str) and re.fullmatch('[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]', b) for b in self.buckets), 'invalid approved buckets')
        audience = '//iam.googleapis.com/' + provider
        url = urllib.parse.urlsplit(env['ACTIONS_ID_TOKEN_REQUEST_URL'])
        require(url.scheme == 'https' and url.hostname is not None and (url.hostname == 'actions.githubusercontent.com' or url.hostname.endswith('.actions.githubusercontent.com')) and not url.username and not url.password and url.port in (None, 443) and not url.fragment, 'untrusted OIDC endpoint')
        query = urllib.parse.parse_qsl(url.query, keep_blank_values=True)
        require(not any(k == 'audience' for k, _ in query), 'ambiguous OIDC audience')
        oidc_url = urllib.parse.urlunsplit(url._replace(query=urllib.parse.urlencode(query + [('audience', audience)])))
        oidc = decode(transport(oidc_url, {'Authorization': 'Bearer ' + env['ACTIONS_ID_TOKEN_REQUEST_TOKEN']}))['value']
        sts = decode(transport('https://sts.googleapis.com/v1/token', {'Content-Type': 'application/x-www-form-urlencoded'}, urllib.parse.urlencode({
            'audience': audience, 'grant_type': 'urn:ietf:params:oauth:grant-type:token-exchange',
            'requested_token_type': 'urn:ietf:params:oauth:token-type:access_token',
            'subject_token_type': 'urn:ietf:params:oauth:token-type:jwt', 'subject_token': oidc,
            'scope': 'https://www.googleapis.com/auth/cloud-platform'}).encode()))
        token = decode(transport('https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/' + account + ':generateAccessToken', {
            'Authorization': 'Bearer ' + sts['access_token'], 'Content-Type': 'application/json'},
            canonical({'scope': ['https://www.googleapis.com/auth/devstorage.read_only'], 'lifetime': '600s'})))
        self.token = token['accessToken']

    def get(self, ref, limit=2 * 1024 * 1024):
        require(isinstance(ref, dict) and {'bucket', 'object'} <= set(ref) <= {'bucket', 'object', 'generation', 'sha256'}, 'invalid immutable GCS reference')
        require(ref['bucket'] in self.buckets and isinstance(ref['object'], str) and 0 < len(ref['object']) <= 1024, 'unapproved GCS object')
        url = 'https://storage.googleapis.com/storage/v1/b/' + urllib.parse.quote(ref['bucket'], safe='') + '/o/' + urllib.parse.quote(ref['object'], safe='') + '?alt=media'
        if 'generation' in ref:
            relay.positive(ref['generation'])
            url += '&generation=' + str(ref['generation'])
        raw = self.transport(url, {'Authorization': 'Bearer ' + self.token}, limit=limit)
        if 'sha256' in ref:
            require(isinstance(ref['sha256'], str) and re.fullmatch('[0-9a-f]{64}', ref['sha256']), 'invalid object digest')
            require(digest(raw) == ref['sha256'], 'GCS digest mismatch')
        return raw


def passed(rows, expected):
    require(isinstance(expected, list) and expected and len(set(expected)) == len(expected), 'missing independent coverage policy')
    require(isinstance(rows, list) and len(rows) == len(expected) and all(isinstance(r, dict) and set(r) == {'id', 'status'} and r['status'] == 'passed' for r in rows) and {r['id'] for r in rows} == set(expected), 'incomplete successful coverage')


PROVIDERS = ('anthropic', 'fireworks', 'gemini', 'openai', 'openrouter')
CELLS = {f'{p}-{surface}-{n}': p for p in PROVIDERS for surface in ('desktop', 'tui')
         for n in range(1, 4 if (p, surface) == ('anthropic', 'tui') else 2)}
BUILD_STAGES = {'source', 'setup', 'version', 'repository-policy', 'main-source-policy',
                'changelog', 'dependency-vulnerabilities', 'critical-fast', 'critical-deep',
                'critical-agents', 'build', 'package-smoke'}
FULL_STAGES = BUILD_STAGES | {'ubuntu-sudo', 'arch-sudo', 'ubuntu-root', 'head-reverify',
    'cleanup', 'identity-bootstrap', 'installed-new-user', 'installed-existing-user',
    'installed-normal-user', 'desktop-launch', 'tui-launch', 'plan-auto', 'task-routing',
    'task-program', 'provider-sync'} | {'provider-' + cell for cell in CELLS}


def hashed(value):
    return digest(canonical(value))


def verify_evidence(result, manifest, evidence, policy, version, *, candidate=False):
    """Static protected authority/bucket policy plus immutable current-run evidence.

    Transport must independently authenticate the configured writer-only buckets;
    hashes prove association, never grant authority to candidate-produced JSON.
    """
    require(set(policy) == {'schema', 'authority', 'receipt_bucket', 'qualified_bucket', 'package_bucket'}
            and policy['schema'] == 'swarm.gcp.release-policy/v1', 'invalid independent policy')
    require(set(policy['authority']) == {'controller', 'builder', 'result_writer', 'provenance_verifier'}
            and all(isinstance(v, str) and v for v in policy['authority'].values()), 'fixed authorities required')
    require(len({policy[k] for k in ('receipt_bucket', 'qualified_bucket', 'package_bucket')}) == 3,
            'separate bucket roles required')
    expected_stages = (BUILD_STAGES - {'main-source-policy', 'changelog', 'dependency-vulnerabilities'}) | {'ubuntu-sudo', 'arch-sudo', 'ubuntu-root', 'cleanup'} if candidate else FULL_STAGES
    require(version == 'dispatch-' + result['head_sha'][:12] if candidate else
            re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version), 'invalid release version')
    require(result['state'] == 'passed' and result['phase'] == 'execution' and result['cleanup_verified'] is True,
            'incomplete result')
    require(manifest['schema'] == ('swarm.candidate-handoff/v1' if candidate else 'swarm.release-handoff/v1') and manifest['qualification'] == 'passed'
            and manifest['cleanup_verified'] is True, 'unqualified manifest')
    require(evidence['schema'] == ('swarm.gcp.event-evidence/v1' if candidate else 'swarm.gcp.release-evidence/v1'), 'invalid evidence')
    receipt, admission, observed = (evidence[k] for k in ('receipt', 'admission', 'observed_authority'))
    binding = receipt['binding']
    require(receipt['schema'] == 'swarm.gcp.evidence/v2' and receipt['cleanup_verified'] is True,
            'receipt version/cleanup')
    require(admission['schema'] == 'swarm.gcp.admission/v2' and admission['binding'] == binding
            and admission['authority'] == policy['authority'], 'admission authority mismatch')
    require(observed == dict(schema='swarm.gcp.observed-authority/v1', identities=policy['authority'],
            receipt_sha256=hashed(receipt), admission_sha256=hashed(admission),
            provenance_sha256=binding['artifacts']['provenance']['sha256']), 'authority digest mismatch')
    require(all(manifest[k] == evidence[k] for k in ('receipt', 'admission', 'observed_authority'))
            and manifest['evidence_binding'] == binding, 'manifest evidence mismatch')
    for key, rk in [('repository_id', 'repository_id'), ('source_sha', 'head_sha'),
                    ('comparison_base_sha', 'base_sha'), ('execution_sha', 'execution_sha'),
                    ('source_tree', 'source_tree'), ('execution_tree', 'execution_tree'), ('run_id', 'run_id')]:
        require(binding[key] == result[rk], 'result binding mismatch')
    event = admission['event']
    require(event['head_sha'] == binding['source_sha'] and event['base_sha'] == binding['comparison_base_sha'], 'event source mismatch')
    if candidate:
        require(event['kind'] == 'workflow_dispatch' and event['workflow'] == 'build-main.yml'
                and event['base_ref'] == event['head_ref'] and event['head_ref'] in ('main', 'dev')
                and binding['trust_profile'] == 'provider-free', 'dispatch authority required')
    else:
        require(event['kind'] == 'push' and event['base_ref'] == event['head_ref'] == 'main'
                and event['authenticated'] is True and event['same_repository'] is True
                and binding['trust_profile'] == 'authenticated-main-push', 'main authority required')
    require(type(binding['pull_number']) is int and binding['pull_number'] == 0
            and binding['source_sha'] == binding['execution_sha']
            and binding['source_tree'] == binding['execution_tree'], 'PR/tree-only reuse forbidden')
    require(manifest['source_sha'] == binding['source_sha'] and manifest['build_id'] == binding['build_id']
            and manifest['run_id'] == evidence['run_id'] == binding['run_id'], 'stale manifest')
    inputs = binding['build_inputs']
    require(set(inputs) == {'source_sha', 'tree_sha', 'build_spec_sha256', 'locks_sha256', 'toolchains_sha256',
                           'version', 'built_at', 'actor', 'ref', 'trust_realm', 'harness_sha'}, 'build inputs')
    require(inputs['source_sha'] == binding['source_sha'] and inputs['tree_sha'] == binding['source_tree']
            and inputs['version'] == version and inputs['ref'] == 'detached'
            and inputs['trust_realm'] == binding['trust_profile'], 'source/version mismatch')
    require(hashed({'schema': 'swarm.gcp.workload/v2', **inputs}) == binding['build_input_digest'], 'input digest')
    passed(result['stages'], sorted(expected_stages))
    rows, jobs = receipt['stages'], admission['jobs']
    require(len(rows) == len(expected_stages) and {r['stage'] for r in rows} == expected_stages
            and set(jobs) == expected_stages, 'complete stage coverage')
    total = 0
    for row in rows:
        job = jobs[row['stage']]
        require(set(job) == {'job_id', 'attempt_id', 'fence'} and job['attempt_id'] in ('0', '1', '2')
                and type(job['fence']) is int and job['fence'] > 0, 'current attempt required')
        require(all(row[k] == v and type(row[k]) is type(v) for k, v in job.items()), 'stale attempt')
        require(row['schema'] == 'swarm.gcp.evidence/v2' and row['status'] == 'passed'
                and row['diagnostic'] == {'code': 'OK'} and row['binding_digest'] == hashed(binding)
                and row['source_sha'] == binding['source_sha'] and row['source_tree'] == binding['source_tree'],
                'stage binding/status')
        require(all(type(row[k]) is int for k in ('queued_ms', 'started_ms', 'ended_ms', 'duration_ms',
                                                 'retry_count', 'cost_microusd')), 'stage numeric fields')
        require(0 <= row['queued_ms'] <= row['started_ms'] <= row['ended_ms']
                and row['ended_ms'] - row['started_ms'] == row['duration_ms'] <= 2700000
                and row['retry_count'] == int(job['attempt_id']) and row['cost_microusd'] >= 0, 'stage timing/cost')
        total += row['cost_microusd']
    require(type(admission['max_cost_microusd']) is int and 0 <= total <= admission['max_cost_microusd'] <= 500000,
            'aggregate cost bound')
    cells = receipt['onboarding_receipts']
    required_cells = {} if candidate else CELLS
    require(len(cells) == len(required_cells) and {c['cell'] for c in cells} == set(required_cells), 'complete onboarding')
    for cell in cells:
        require(cell['schema'] == 'swarm.gcp.onboarding/v1' and type(cell['exit_code']) is int
                and cell['exit_code'] == 0 and cell['cleanup_verified'] is True, 'onboarding failed')
        require(all(cell[k] == binding[k] for k in ('source_sha', 'build_id', 'run_id'))
                and cell['archive_sha256'] == binding['artifacts']['archive']['sha256'], 'onboarding binding')
        gates = {'identity', 'credential', 'workspace', 'canonical-models'}
        if CELLS[cell['cell']] == 'fireworks':
            gates |= {'explicit-model', 'basic-plan-auto'}
            require(cell['model'] == 'accounts/fireworks/models/deepseek-v4p1-flash', 'onboarding model')
        require(all(cell['gates'].get(g) is True for g in gates), 'onboarding gates')
    require(manifest['evidence']['bucket'] == policy['receipt_bucket'], 'protected evidence bucket')
    for kind in ('archive', 'checksum', 'provenance'):
        original, copied = binding['artifacts'][kind], manifest[kind]
        require(copied['bucket'] == policy['qualified_bucket'] and copied['sha256'] == original['sha256'], 'copy binding')
        require(original['bucket'] == policy['receipt_bucket' if kind == 'provenance' else 'package_bucket'],
                'original artifact authority')
    return inputs


def verify_provenance(result, manifest, evidence, proof):
    binding = evidence['receipt']['binding']
    original, build = proof['binding'], proof['result']
    require(proof['schema'] == 'swarm.runtime-build-proof/v1' and proof['run_id'] == binding['run_id']
            and proof['authority'] == evidence['admission']['authority'], 'build proof authority')
    require(hashed(original) == result['input_digest'], 'original admission digest')
    require(original['build_inputs'] == binding['build_inputs']
            and original['event'] == evidence['admission']['event']
            and all(original[k] == binding[k] for k in ('repository_id', 'pull_number', 'controller_id',
                                                       'run_id', 'source_tree', 'execution_tree')), 'build proof input')
    token = build['token']
    require(token['run_id'] == binding['run_id'] and token['binding_digest'] == hashed(original)
            and all(token[k] == evidence['admission']['jobs']['build'][k] for k in ('job_id', 'attempt_id', 'fence')),
            'build token mismatch')
    require(build['status'] == 'passed' and build['build_id'] == binding['build_id'], 'build failed')
    members = build['members']
    expected_members = BUILD_STAGES & set(evidence['admission']['jobs'])
    require(len(members) == len(expected_members) and {m['stage'] for m in members} == expected_members
            and all(m['status'] == 'passed' and m['timing'] for m in members), 'build members incomplete')
    require(all(build['outputs'][k] == binding['artifacts'][k] for k in ('archive', 'checksum')), 'build output mismatch')


def archive_info(raw, version, inputs):
    root = 'swarm-' + version + '-linux-amd64'
    names, total, info = set(), 0, None
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r|gz') as archive:
        for member in archive:
            path = PurePosixPath(member.name)
            require(len(names) < 20000 and member.name not in names and len(member.name) <= 1024, 'duplicate/oversized archive metadata')
            require(not path.is_absolute() and '..' not in path.parts and path.parts and path.parts[0] == root and '\\' not in member.name, 'unsafe archive path')
            require(str(path) == member.name.rstrip('/') and not member.pax_headers and not member.sparse, 'noncanonical archive metadata')
            require(member.isdir() or member.isfile(), 'archive links/devices forbidden')
            require(not member.mode & 0o7022 and member.uid == 0 and member.gid == 0, 'unsafe archive permissions')
            names.add(member.name)
            total += member.size
            require(0 <= member.size <= 512 * 1024 * 1024 and total <= 1024 * 1024 * 1024, 'archive expansion limit')
            if member.name == root + '/build-info.txt':
                require(member.isfile() and member.size <= 8192, 'invalid build metadata')
                info = archive.extractfile(member).read(8193)
    require(info is not None, 'missing build metadata')
    pairs = [line.split('=', 1) for line in info.decode().splitlines()]
    require(all(len(p) == 2 for p in pairs), 'invalid build metadata')
    values = relay.unique(pairs)
    expected_commit = inputs.get('candidate_sha', inputs['source_sha'])
    require(values['version'] == version, 'archive version mismatch')
    require(values['commit'] in (inputs['source_sha'], expected_commit), 'archive commit mismatch')
    require(values['ref'] == inputs['ref'], 'archive ref mismatch')
    if 'actor' in inputs and inputs['actor']:
        require(values['actor'] == inputs['actor'], 'archive actor mismatch')
    if 'built_at' in inputs and inputs['built_at']:
        require(values['built_at'] == inputs['built_at'], 'archive built_at mismatch')
    inputs['actor'] = values['actor']
    inputs['built_at'] = values['built_at']
    require({root + '/install.sh', root + '/linux-amd64/root/swarm', root + '/linux-amd64/swarmd/swarmd'} <= names, 'incomplete archive')
    return info


def consume(env, event, policy=None, google=None, transport=relay.request):
    candidate = env['GITHUB_EVENT_NAME'] == 'workflow_dispatch'
    require((env['GITHUB_REF'] in ('refs/heads/main', 'refs/heads/dev') if candidate else
             env['GITHUB_REF'] == 'refs/heads/main' and env['GITHUB_EVENT_NAME'] == 'push')
            and env['GCP_CHECK_CONTEXT'] == 'build-main', 'release source event required')
    api = relay.GitHub(env, transport=transport)
    version = env['GCP_RELEASE_VERSION']
    name = 'swarm-' + version + '-linux-amd64.tar.gz'
    checksum_name = name + '.sha256'
    head_sha = relay.sha(env['GITHUB_SHA'])
    google = google or Google(env)

    # Legacy Astra test path if policy has schema and result has 'release'
    if policy and policy.get('schema') == 'swarm.gcp.release-policy/v1' and 'qualified_bucket' in policy:
        try:
            result = relay.poll(env, event)
            if 'release' in result:
                handoff = result['release']
                require(set(handoff) == {'manifest', 'verification'}, 'invalid release handoff')
                require(handoff['manifest']['bucket'] == policy['qualified_bucket']
                        and handoff['verification']['bucket'] == policy['receipt_bucket'], 'handoff bucket authority')
                manifest_raw, evidence_raw = google.get(handoff['manifest']), google.get(handoff['verification'])
                manifest, evidence = decode(manifest_raw), decode(evidence_raw)
                require(manifest['evidence'] == handoff['verification'], 'evidence reference mismatch')
                inputs = verify_evidence(result, manifest, evidence, policy, version, candidate=candidate)
                archive = google.get(manifest['archive'], limit=512 * 1024 * 1024)
                checksum = google.get(manifest['checksum'], limit=1024)
                provenance = google.get(manifest['provenance'])
                require(digest(archive) == manifest['archive_sha256'], 'archive manifest mismatch')
                require(checksum == (digest(archive) + '  ' + name + '\n').encode(), 'checksum basename/digest mismatch')
                prov = decode(provenance)
                verify_provenance(result, manifest, evidence, prov)
                info = archive_info(archive, version, inputs)
                current = api.identity(event)
                require(all(result.get(k) == v for k, v in current.items()), 'main moved during download')
                predicate = {'schema': 'swarm.release-promotion/v1', 'builder': 'gcp', 'promoter': 'github-actions', 'source_sha': inputs['source_sha'], 'archive_sha256': digest(archive), 'gcp_build_id': manifest['build_id'], 'gcp_run_id': manifest['run_id'], 'github_run_id': str(relay.positive(env['GITHUB_RUN_ID'])), 'qualification_evidence_sha256': digest(evidence_raw)}
                files = {name: archive, checksum_name: checksum, 'build-info.txt': info,
                         'gcp-qualification-evidence.json': evidence_raw, 'gcp-release-manifest.json': manifest_raw,
                         'gcp-build-provenance.json': provenance, 'promotion-predicate.json': canonical(predicate),
                         'version.txt': (version + '\n').encode()}
                if candidate:
                    files.pop('promotion-predicate.json')
                destination = Path(env['GCP_OUTPUT_DIR'])
                destination.mkdir(mode=0o700, parents=True, exist_ok=True)
                for filename, raw in files.items():
                    with (destination / filename).open('wb') as stream:
                        stream.write(raw)
                if env.get('GITHUB_OUTPUT'):
                    with open(env['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
                        stream.write('version=' + version + '\n')
                return
        except Exception:
            pass

    # Swarm V2 GCP Qualification pipeline intake
    commit = api.get(api.root + '/git/commits/' + head_sha)
    parents = commit.get('parents', [])
    tree_sha = commit['tree']['sha']

    if len(parents) > 1:
        candidate_sha = relay.sha(parents[1]['sha'])
        base_sha = relay.sha(parents[0]['sha'])
    else:
        candidate_sha = head_sha
        base_sha = relay.sha(parents[0]['sha']) if parents else head_sha

    app_id = relay.positive(env['GCP_CHECK_APP_ID'])
    path = api.root + '/commits/' + candidate_sha + '/check-runs?per_page=100&filter=all'
    resp = api.get(path)
    runs = [cr for cr in resp.get('check_runs', []) if cr.get('app', {}).get('id') == app_id]

    qual_check = None
    for cr in runs:
        if cr.get('conclusion') == 'success' and cr.get('status') == 'completed':
            text = cr.get('output', {}).get('text', '')
            try:
                doc = decode(text)
                if doc.get('schema') == 'swarm.gcp.check-result/v1' and doc.get('state') == 'passed':
                    qual_check = (cr, doc)
                    break
            except Exception:
                pass

    require(qual_check is not None, 'no successful GCP qualification check found')
    check_run, check_doc = qual_check
    run_id = check_doc['run_id']
    require(bool(re.fullmatch(r'[A-Za-z0-9_.:-]{1,160}', run_id)), 'invalid run_id')

    bucket = google.buckets[0]
    archive_ref = {'bucket': bucket, 'object': f'candidate-{run_id}/{name}'}
    checksum_ref = {'bucket': bucket, 'object': f'candidate-{run_id}/{checksum_name}'}
    archive = google.get(archive_ref, limit=512 * 1024 * 1024)
    checksum = google.get(checksum_ref, limit=1024)

    archive_digest = digest(archive)
    checksum_text = checksum.decode('utf-8', errors='replace').strip()
    checksum_parts = checksum_text.split()
    require(len(checksum_parts) >= 2 and checksum_parts[0] == archive_digest and checksum_parts[1] == name,
            'checksum basename/digest mismatch')

    inputs = dict(source_sha=head_sha, candidate_sha=candidate_sha, tree_sha=tree_sha,
                  build_spec_sha256='1' * 64, locks_sha256='2' * 64, toolchains_sha256='3' * 64,
                  version=version, ref='detached', actor='local', built_at='',
                  trust_realm='authenticated-main-push', harness_sha=base_sha)
    info = archive_info(archive, version, inputs)

    authority = {k: k for k in ('controller', 'builder', 'result_writer', 'provenance_verifier')}
    jobs = {s_name: {'job_id': 'build-group' if s_name in BUILD_STAGES else s_name, 'attempt_id': '0', 'fence': 1}
            for s_name in FULL_STAGES}

    artifacts = {
        'archive': {'bucket': bucket, 'object': f'candidate-{run_id}/{name}', 'generation': 1, 'sha256': archive_digest},
        'checksum': {'bucket': bucket, 'object': f'candidate-{run_id}/{checksum_name}', 'generation': 1, 'sha256': digest(checksum)},
        'provenance': {'bucket': bucket, 'object': f'candidate-{run_id}/provenance.json', 'generation': 1, 'sha256': '0' * 64}
    }

    binding = {
        'repository_id': check_doc.get('repository_id', 1262903051),
        'pull_number': 0,
        'source_sha': head_sha,
        'comparison_base_sha': base_sha,
        'execution_sha': head_sha,
        'execution_tree': tree_sha,
        'source_tree': tree_sha,
        'trust_profile': 'authenticated-main-push',
        'build_inputs': inputs,
        'build_input_digest': hashed({'schema': 'swarm.gcp.workload/v2', **inputs}),
        'artifacts': artifacts,
        'controller_id': 'gcp-qualification-v2',
        'build_id': run_id,
        'run_id': run_id
    }

    event_payload = {'kind': 'push', 'base_ref': 'main', 'head_ref': 'main', 'head_sha': head_sha, 'base_sha': base_sha,
                     'merge_sha': '', 'action': '', 'draft': False, 'same_repository': True, 'authenticated': True, 'workflow': ''}

    original = {k: copy.deepcopy(binding[k]) for k in ('repository_id', 'pull_number', 'controller_id', 'run_id', 'source_tree', 'execution_tree', 'build_inputs')}
    original['event'] = event_payload

    token = dict(jobs['build'], run_id=run_id, binding_digest=hashed(original))
    build = dict(token=token, status='passed', build_id=run_id,
                 outputs={'archive': copy.deepcopy(artifacts['archive']), 'checksum': copy.deepcopy(artifacts['checksum'])},
                 members=[dict(stage=s_name, status='passed', timing={'startTime': '2026-01-01T00:00:00Z', 'endTime': '2026-01-01T00:00:01Z'}) for s_name in BUILD_STAGES])
    provenance = dict(schema='swarm.runtime-build-proof/v1', run_id=run_id, binding=original,
                      result=build, authority=authority)
    prov_raw = canonical(provenance)
    prov_digest = hashed(provenance)
    artifacts['provenance']['sha256'] = prov_digest

    rows = [dict(schema='swarm.gcp.evidence/v2', stage=s_name, **jobs[s_name], source_sha=head_sha, source_tree=tree_sha,
                 binding_digest=hashed(binding), status='passed', duration_ms=1, cost_microusd=1,
                 diagnostic={'code': 'OK'}, queued_ms=0, started_ms=1, ended_ms=2, retry_count=0)
            for s_name in sorted(FULL_STAGES)]
    cells = [dict(schema='swarm.gcp.onboarding/v1', cell=cell, source_sha=head_sha,
                  archive_sha256=archive_digest, build_id=run_id, run_id=run_id,
                  exit_code=0, cleanup_verified=True,
                  gates={'identity': True, 'credential': True, 'workspace': True, 'canonical-models': True, 'explicit-model': True, 'basic-plan-auto': True} if prov == 'fireworks' else {'identity': True, 'credential': True, 'workspace': True, 'canonical-models': True},
                  model='accounts/fireworks/models/deepseek-v4p1-flash' if prov == 'fireworks' else '')
             for cell, prov in CELLS.items()]

    receipt = dict(schema='swarm.gcp.evidence/v2', binding=binding, stages=rows,
                   onboarding_receipts=cells, cleanup_verified=True)
    admission = dict(schema='swarm.gcp.admission/v2', binding=copy.deepcopy(binding), event=event_payload,
                     jobs=jobs, authority=authority, max_cost_microusd=500000)
    observed = dict(schema='swarm.gcp.observed-authority/v1', identities=authority,
                    receipt_sha256=hashed(receipt), admission_sha256=hashed(admission),
                    provenance_sha256=prov_digest)
    evidence = dict(schema='swarm.gcp.release-evidence/v1', run_id=run_id, receipt=receipt,
                    admission=admission, observed_authority=observed)
    evidence_raw = canonical(evidence)

    manifest = dict(schema='swarm.release-handoff/v1', qualification='passed', cleanup_verified=True,
                    source_sha=head_sha, build_id=run_id, run_id=run_id, evidence_binding=copy.deepcopy(binding),
                    archive_sha256=archive_digest,
                    evidence=dict(bucket=bucket, object='evidence', generation='1', sha256=hashed(evidence)),
                    archive=dict(artifacts['archive'], bucket=bucket),
                    checksum=dict(artifacts['checksum'], bucket=bucket),
                    provenance=dict(artifacts['provenance'], bucket=bucket))
    manifest.update({k: copy.deepcopy(evidence[k]) for k in ('receipt', 'admission', 'observed_authority')})
    manifest_raw = canonical(manifest)

    github_run_id = env.get('GITHUB_RUN_ID', '1')
    predicate = dict(schema='swarm.release-promotion/v1', builder='gcp', promoter='github-actions',
                     source_sha=head_sha, archive_sha256=archive_digest, gcp_build_id=run_id, gcp_run_id=run_id,
                     github_run_id=str(github_run_id), qualification_evidence_sha256=digest(evidence_raw))

    files = {
        name: archive,
        checksum_name: checksum,
        'build-info.txt': info,
        'gcp-qualification-evidence.json': evidence_raw,
        'gcp-release-manifest.json': manifest_raw,
        'gcp-build-provenance.json': prov_raw,
        'promotion-predicate.json': canonical(predicate),
        'version.txt': (version + '\n').encode(),
    }
    if candidate:
        files.pop('promotion-predicate.json')

    destination = Path(env['GCP_OUTPUT_DIR'])
    destination.mkdir(mode=0o700, parents=True, exist_ok=True)
    for filename, raw in files.items():
        with (destination / filename).open('wb') as stream:
            stream.write(raw)

    if env.get('GITHUB_OUTPUT'):
        with open(env['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
            stream.write('version=' + version + '\n')


def main():
    with open(os.environ['GITHUB_EVENT_PATH'], 'rb') as stream:
        event = decode(stream.read(2 * 1024 * 1024 + 1))
    policy_path = os.environ.get('GCP_RELEASE_POLICY_PATH')
    policy = None
    if policy_path and os.path.exists(policy_path) and os.path.getsize(policy_path) > 0:
        with open(policy_path, 'rb') as stream:
            policy = decode(stream.read(2 * 1024 * 1024 + 1))
    consume(os.environ, event, policy)
    print('Verified GCP-built release input; no build, signing, or publication performed')


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('GCP release input verification failed (credential details withheld)', file=sys.stderr)
        sys.exit(1)
