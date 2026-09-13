#!/usr/bin/env python3
"""Download, independently verify, and stage GCP-built release input; never build/sign.

Uses relay env plus GCP_RELEASE_POLICY_PATH, GCP_RELEASE_VERSION, GCP_OUTPUT_DIR,
GCP_WORKLOAD_IDENTITY_PROVIDER (projects/NUMBER/locations/global/.../providers/ID),
GCP_READ_SERVICE_ACCOUNT, GCP_ALLOWED_BUCKETS (JSON string array), and GitHub's
ACTIONS_ID_TOKEN_REQUEST_URL/TOKEN. Policy is separately reviewed trusted input:
{schema: swarm.gcp.release-policy/v1, stages: [...], onboarding: [...],
 binding: {all receipt binding fields except artifacts/build_id/run_id},
 current_attempts: {job_id: positive_attempt}}. Never derive policy from evidence.
Receipt requires binding, stages, onboarding, jobs [{id,attempt,status}], cleanup
{verified,remaining_resources}; admission.binding and observed_authority.binding
must match receipt.binding exactly. Provenance binds source/version/build/run and
archive digest. Parent must align producer contracts; missing evidence fails shut.
"""
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
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()


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
        require(isinstance(ref, dict) and set(ref) == {'bucket', 'object', 'generation', 'sha256'}, 'invalid immutable GCS reference')
        require(ref['bucket'] in self.buckets and isinstance(ref['object'], str) and 0 < len(ref['object']) <= 1024, 'unapproved GCS object')
        relay.positive(ref['generation'])
        require(isinstance(ref['sha256'], str) and re.fullmatch('[0-9a-f]{64}', ref['sha256']), 'invalid object digest')
        url = 'https://storage.googleapis.com/storage/v1/b/' + urllib.parse.quote(ref['bucket'], safe='') + '/o/' + urllib.parse.quote(ref['object'], safe='') + '?alt=media&generation=' + str(ref['generation'])
        raw = self.transport(url, {'Authorization': 'Bearer ' + self.token}, limit=limit)
        require(digest(raw) == ref['sha256'], 'GCS digest mismatch')
        return raw


def passed(rows, expected):
    require(isinstance(expected, list) and expected and len(set(expected)) == len(expected), 'missing independent coverage policy')
    require(isinstance(rows, list) and len(rows) == len(expected) and all(isinstance(r, dict) and set(r) == {'id', 'status'} and r['status'] == 'passed' for r in rows) and {r['id'] for r in rows} == set(expected), 'incomplete successful coverage')


def verify_evidence(result, manifest, evidence, policy, version):
    require(policy['schema'] == 'swarm.gcp.release-policy/v1', 'invalid independent policy')
    require(re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version), 'unstable version')
    require(manifest['schema'] == 'swarm.release-handoff/v1' and manifest['qualification'] == 'passed' and manifest['cleanup_verified'] is True, 'unqualified manifest')
    require(evidence['schema'] == 'swarm.gcp.release-evidence/v1', 'invalid evidence')
    receipt = evidence['receipt']
    binding = receipt['binding']
    fields = {'repository_id', 'pull_number', 'source_sha', 'comparison_base_sha', 'execution_sha', 'execution_tree', 'source_tree', 'trust_profile', 'build_inputs', 'build_input_digest', 'artifacts', 'controller_id', 'build_id', 'run_id'}
    require(set(binding) == fields and set(policy['binding']) == fields - {'artifacts', 'build_id', 'run_id'}, 'incomplete binding policy')
    require(all(binding[k] == v for k, v in policy['binding'].items()), 'policy binding mismatch')
    require(evidence['admission']['binding'] == binding and evidence['observed_authority']['binding'] == binding, 'authority binding mismatch')
    require(manifest['evidence_binding'] == binding, 'manifest binding mismatch')
    for key, result_key in [('repository_id', 'repository_id'), ('source_sha', 'head_sha'), ('comparison_base_sha', 'base_sha'), ('execution_sha', 'execution_sha'), ('source_tree', 'source_tree'), ('execution_tree', 'execution_tree'), ('build_input_digest', 'input_digest'), ('run_id', 'run_id')]:
        require(binding[key] == result[result_key], 'result binding mismatch')
    require(binding['pull_number'] == 0 and binding['source_sha'] == binding['execution_sha'] and binding['source_tree'] == binding['execution_tree'], 'PR/tree-only release reuse forbidden')
    require(manifest['source_sha'] == binding['source_sha'] and manifest['build_id'] == binding['build_id'] and manifest['run_id'] == binding['run_id'], 'stale release manifest')
    inputs = binding['build_inputs']
    require(set(inputs) == {'source_sha', 'tree_sha', 'build_spec_sha256', 'locks_sha256', 'toolchains_sha256', 'version', 'built_at', 'actor', 'ref', 'trust_realm', 'harness_sha'}, 'incomplete byte-affecting inputs')
    require(inputs['source_sha'] == binding['source_sha'] and inputs['tree_sha'] == binding['source_tree'] and inputs['version'] == version and inputs['ref'] == 'refs/heads/main', 'source/version mismatch')
    require(digest(canonical({'schema': 'swarm.gcp.workload/v2', **inputs})) == binding['build_input_digest'], 'build input digest mismatch')
    passed(receipt['stages'], policy['stages'])
    require(set(policy['stages']) == {r['id'] for r in result['stages']}, 'relay/release coverage mismatch')
    passed(receipt['onboarding'], policy['onboarding'])
    attempts = policy['current_attempts']
    require(isinstance(attempts, dict) and attempts, 'missing current attempts policy')
    for attempt in attempts.values():
        relay.positive(attempt)
    jobs = receipt['jobs']
    require(len(jobs) == len(attempts) and {j['id'] for j in jobs} == set(attempts) and all(set(j) == {'id', 'attempt', 'status'} and type(j['attempt']) is int and j['attempt'] == attempts[j['id']] and j['status'] == 'passed' for j in jobs), 'stale/failed job attempt')
    require(receipt['cleanup'] == {'verified': True, 'remaining_resources': []}, 'cleanup incomplete')
    require(binding['artifacts'] == {k: manifest[k] for k in ('archive', 'checksum', 'provenance')}, 'artifact binding mismatch')
    return inputs


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
    require(values == {'version': version, 'commit': inputs['source_sha'], 'actor': inputs['actor'], 'ref': inputs['ref'], 'built_at': inputs['built_at']}, 'archive build metadata mismatch')
    require({root + '/install.sh', root + '/linux-amd64/root/swarm', root + '/linux-amd64/swarmd/swarmd'} <= names, 'incomplete archive')
    return info


def consume(env, event, policy, google=None):
    require(env['GITHUB_REF'] == 'refs/heads/main' and env['GCP_CHECK_CONTEXT'] == 'build-main', 'release requires main qualification')
    result = relay.poll(env, event)
    google = google or Google(env)
    handoff = result['release_handoff']
    require(set(handoff) == {'manifest', 'evidence'}, 'invalid release handoff')
    manifest_raw, evidence_raw = google.get(handoff['manifest']), google.get(handoff['evidence'])
    manifest, evidence = decode(manifest_raw), decode(evidence_raw)
    require(manifest['evidence'] == handoff['evidence'], 'evidence reference mismatch')
    version = env['GCP_RELEASE_VERSION']
    inputs = verify_evidence(result, manifest, evidence, policy, version)
    archive = google.get(manifest['archive'], limit=512 * 1024 * 1024)
    checksum = google.get(manifest['checksum'], limit=1024)
    provenance = google.get(manifest['provenance'])
    name = 'swarm-' + version + '-linux-amd64.tar.gz'
    require(digest(archive) == manifest['archive_sha256'], 'archive manifest mismatch')
    require(checksum == (digest(archive) + '  ' + name + '\n').encode(), 'checksum basename/digest mismatch')
    prov = decode(provenance)
    require(prov == {'schema': 'swarm.gcp.build-provenance/v1', 'builder': 'gcp', 'source_sha': inputs['source_sha'], 'version': version, 'build_id': manifest['build_id'], 'run_id': manifest['run_id'], 'archive_sha256': digest(archive), 'build_inputs': inputs}, 'untruthful build provenance')
    info = archive_info(archive, version, inputs)
    # Recheck live GitHub immediately before publishing local bytes.
    api = relay.GitHub(env)
    current = api.identity(event)
    require(all(result.get(k) == v for k, v in current.items()), 'main moved during download')
    predicate = {'schema': 'swarm.release-promotion/v1', 'builder': 'gcp', 'promoter': 'github-actions', 'source_sha': inputs['source_sha'], 'archive_sha256': digest(archive), 'gcp_build_id': manifest['build_id'], 'gcp_run_id': manifest['run_id'], 'github_run_id': str(relay.positive(env['GITHUB_RUN_ID'])), 'qualification_evidence_sha256': digest(evidence_raw)}
    files = {name: archive, name + '.sha256': checksum, 'build-info.txt': info,
             'gcp-qualification-evidence.json': evidence_raw, 'gcp-release-manifest.json': manifest_raw,
             'gcp-build-provenance.json': provenance, 'promotion-predicate.json': canonical(predicate),
             'version.txt': (version + '\n').encode()}
    destination = Path(env['GCP_OUTPUT_DIR'])
    destination.mkdir(mode=0o700, parents=False, exist_ok=False)
    for filename, raw in files.items():
        with (destination / filename).open('xb') as stream:
            stream.write(raw)
    if env.get('GITHUB_OUTPUT'):
        with open(env['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
            stream.write('version=' + version + '\n')


def main():
    with open(os.environ['GITHUB_EVENT_PATH'], 'rb') as stream:
        event = decode(stream.read(2 * 1024 * 1024 + 1))
    with open(os.environ['GCP_RELEASE_POLICY_PATH'], 'rb') as stream:
        policy = decode(stream.read(2 * 1024 * 1024 + 1))
    consume(os.environ, event, policy)
    print('Verified GCP-built release input; no build, signing, or publication performed')


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('GCP release input verification failed (credential details withheld)', file=sys.stderr)
        sys.exit(1)
