#!/usr/bin/env python3
"""Compact maintained-runner contract. Authority comes from the pinned App, not JSON identities."""
import re

POLICY = 'swarm.gcp.runner-release-policy/v1'
SCHEMA = 'swarm.gcp.runner-release/v1'
RECEIPT = 'swarm.gcp.runner-qualification/v1'
# Independent coverage policy; never derived from producer-supplied rows.
NATIVE = frozenset(('source', 'setup', 'repository-policy', 'main-source-policy',
                    'changelog', 'dependency-vulnerabilities', 'critical-fast',
                    'critical-deep', 'critical-agents', 'version', 'build',
                    'package-smoke', 'ubuntu-sudo', 'arch-sudo', 'ubuntu-root', 'cleanup'))
HEADLESS = frozenset(('startup', 'scoped-auth', 'sdk-session', 'restart'))


def ref(r, value, bucket):
    r.require(isinstance(value, dict) and set(value) == {'bucket', 'object', 'generation', 'sha256'}, 'immutable runner reference required')
    r.require(value['bucket'] == bucket and isinstance(value['object'], str)
              and re.fullmatch(r'[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*', value['object'])
              and all(p not in ('.', '..') for p in value['object'].split('/'))
              and len(value['object']) <= 1024, 'runner bucket/object mismatch')
    r.relay.positive(value['generation'])
    r.require(isinstance(value['sha256'], str) and re.fullmatch('[0-9a-f]{64}', value['sha256']), 'runner digest required')


def verify(r, result, manifest, receipt, policy, version, source):
    r.require(set(policy) == {'schema', 'qualified_bucket'} and policy['schema'] == POLICY
              and isinstance(policy['qualified_bucket'], str)
              and re.fullmatch('[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]', policy['qualified_bucket']), 'runner policy required')
    r.require(re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version), 'stable runner version required')
    r.relay.sha(source)
    r.require(result['schema'] == 'swarm.gcp.check-result/v1' and result['context'] == 'build-main'
              and result['event_name'] == 'push' and result['pull_number'] == 0
              and result['head_sha'] == result['execution_sha'] == source
              and result['source_tree'] == result['execution_tree']
              and result['state'] == 'passed' and result['phase'] == 'execution'
              and result['cleanup_verified'] is True, 'runner main result required')
    r.relay.sha(result['source_tree'])
    r.passed(result['stages'], sorted(NATIVE))
    handoff = result['release']
    r.require(set(handoff) == {'manifest', 'verification'}, 'runner handoff required')
    for value in handoff.values():
        ref(r, value, policy['qualified_bucket'])
    r.require(set(manifest) == {'schema', 'version', 'source_sha', 'source_tree', 'repository_id',
              'run_id', 'build_id', 'archive', 'checksum', 'oci_archive', 'oci_metadata', 'evidence', 'native'}, 'runner manifest fields')
    r.require(manifest['schema'] == SCHEMA and manifest['version'] == version
              and manifest['source_sha'] == source and manifest['source_tree'] == result['source_tree']
              and manifest['repository_id'] == result['repository_id']
              and manifest['run_id'] == result['run_id']
              and isinstance(manifest['build_id'], str) and re.fullmatch('[A-Za-z0-9_.:-]{1,160}', manifest['build_id']), 'runner identity mismatch')
    r.require(manifest['evidence'] == handoff['verification'], 'runner receipt reference mismatch')
    for key in ('archive', 'checksum', 'oci_archive', 'oci_metadata', 'evidence'):
        ref(r, manifest[key], policy['qualified_bucket'])
    native = manifest['native']
    r.require(set(native) == {'source_sha', 'version', 'ref', 'actor', 'built_at'}
              and native['source_sha'] == source and native['version'] == version and native['ref'] == 'detached'
              and all(isinstance(native[k], str) and native[k] for k in ('actor', 'built_at')), 'native build identity required')
    expected = {k: manifest[k] for k in ('version', 'source_sha', 'source_tree', 'repository_id', 'run_id', 'build_id', 'native')}
    r.require(set(receipt) == set(expected) | {'schema', 'artifacts', 'native_exit_codes', 'headless', 'cleanup_verified'}
              and receipt['schema'] == RECEIPT and all(receipt[k] == v for k, v in expected.items())
              and receipt['cleanup_verified'] is True, 'runner qualification identity mismatch')
    r.require(receipt['artifacts'] == {k: manifest[k] for k in ('archive', 'checksum', 'oci_archive', 'oci_metadata')}, 'runner artifact binding mismatch')
    exits(r, receipt['native_exit_codes'], NATIVE)
    headless = receipt['headless']
    r.require(set(headless) == {'image', 'exit_codes'}, 'headless receipt required')
    exits(r, headless['exit_codes'], HEADLESS)
    return dict(native)


def exits(r, rows, required):
    r.require(isinstance(rows, dict) and set(rows) == required
              and all(type(v) is int and v == 0 for v in rows.values()), 'missing/failed runner gates')


def verify_bytes(r, result, manifest_raw, receipt_raw, policy, version, source, files):
    manifest, receipt = r.decode(manifest_raw), r.decode(receipt_raw)
    inputs = verify(r, result, manifest, receipt, policy, version, source)
    for raw, reference in ((manifest_raw, result['release']['manifest']), (receipt_raw, result['release']['verification'])):
        r.require(r.digest(raw) == reference['sha256'], 'App handoff digest mismatch')
    for kind in ('archive', 'checksum', 'oci_archive', 'oci_metadata'):
        r.require(r.digest(files[kind]) == manifest[kind]['sha256'], 'runner artifact digest mismatch')
    name = 'swarm-' + version + '-linux-amd64.tar.gz'
    r.require(files['checksum'] == (r.digest(files['archive']) + '  ' + name + '\n').encode(), 'runner checksum mismatch')
    info = r.archive_info(files['archive'], version, inputs)
    metadata = r.image.verify(files['oci_archive'], r.decode(files['oci_metadata']), version, source)
    r.require(receipt['headless']['image'] == metadata, 'headless image qualification mismatch')
    return info


def predicate(r, result, manifest_raw, receipt_raw, policy, github_run):
    m = r.decode(manifest_raw)
    return dict(schema='swarm.runner-release-promotion/v1', builder='gcp-runner', promoter='github-actions',
                source_sha=m['source_sha'], source_tree=m['source_tree'], version=m['version'],
                archive_sha256=m['archive']['sha256'], gcp_build_id=m['build_id'], gcp_run_id=m['run_id'],
                github_run_id=str(r.relay.positive(github_run)), manifest_sha256=r.digest(manifest_raw),
                qualification_evidence_sha256=r.digest(receipt_raw), check_result_sha256=r.hashed(result),
                policy=policy, release=result['release'])


def consume(r, env, event, policy, google, transport):
    r.require(env['GITHUB_EVENT_NAME'] == 'push' and env['GITHUB_REF'] == 'refs/heads/main', 'runner release requires main push')
    # Only static policy selects coverage. The untrusted check cannot select weaker gates.
    result = r.relay.poll(env, event, transport=transport, required_stages=NATIVE)
    for reference in result['release'].values():
        ref(r, reference, policy['qualified_bucket'])
    manifest_raw = google.get(result['release']['manifest'])
    receipt_raw = google.get(result['release']['verification'])
    m = r.decode(manifest_raw)
    verify(r, result, m, r.decode(receipt_raw), policy, env['GCP_RELEASE_VERSION'], env['GITHUB_SHA'])
    files = {k: google.get(m[k], limit=1024**3 if k in ('archive', 'oci_archive') else 2*1024**2)
             for k in ('archive', 'checksum', 'oci_archive', 'oci_metadata')}
    info = verify_bytes(r, result, manifest_raw, receipt_raw, policy, env['GCP_RELEASE_VERSION'], env['GITHUB_SHA'], files)
    current = r.relay.GitHub(env, transport).identity(event)
    r.require(all(result.get(k) == v for k, v in current.items()), 'main moved during runner download')
    name = 'swarm-' + env['GCP_RELEASE_VERSION'] + '-linux-amd64.tar.gz'
    output = {name: files['archive'], name+'.sha256': files['checksum'], 'build-info.txt': info,
              'swarm-headless.oci.tar': files['oci_archive'], 'headless-image.json': files['oci_metadata'],
              'gcp-release-manifest.json': manifest_raw, 'gcp-qualification-evidence.json': receipt_raw,
              'gcp-build-provenance.json': r.canonical(result),
              'promotion-predicate.json': r.canonical(predicate(r, result, manifest_raw, receipt_raw, policy, env['GITHUB_RUN_ID'])),
              'version.txt': (env['GCP_RELEASE_VERSION']+'\n').encode()}
    destination = r.Path(env['GCP_OUTPUT_DIR'])
    destination.mkdir(mode=0o700, parents=True, exist_ok=True)
    for name, raw in output.items():
        (destination / name).write_bytes(raw)
    if env.get('GITHUB_OUTPUT'):
        with open(env['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
            stream.write('version=' + env['GCP_RELEASE_VERSION'] + '\n')


def reverify(r, directory, source, signed_predicate):
    """Called only after gh verifies the exact workflow's signed predicate."""
    mraw = (directory / 'gcp-release-manifest.json').read_bytes()
    eraw = (directory / 'gcp-qualification-evidence.json').read_bytes()
    result = r.decode((directory / 'gcp-build-provenance.json').read_bytes())
    m = r.decode(mraw)
    r.require(signed_predicate == predicate(r, result, mraw, eraw, signed_predicate['policy'], signed_predicate['github_run_id']), 'signed runner promotion mismatch')
    version = m['version']
    name = 'swarm-' + version + '-linux-amd64.tar.gz'
    files = {k: (directory / filename).read_bytes() for k, filename in
             [('archive', name), ('checksum', name+'.sha256'), ('oci_archive', 'swarm-headless.oci.tar'), ('oci_metadata', 'headless-image.json')]}
    info = verify_bytes(r, result, mraw, eraw, signed_predicate['policy'], version, source, files)
    r.require(info == (directory / 'build-info.txt').read_bytes(), 'runner build info mismatch')
    return name
